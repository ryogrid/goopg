package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0229: the ordered stage's Merge Append arm (generate_orderedappend_paths
// feeding create_ordered_paths). TPC-DS Q23's second statement orders a UNION ALL
// of two GroupAggregates grouped by (c_last_name, c_first_name) by those columns
// and the sum; PG merges the branches and incrementally sorts on the sum.

func orderedAppendFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	for _, n := range []string{"oa_cs", "oa_ws"} {
		if _, err := cat.CreateTable(parser.ObjectName{Name: n}, []catalog.Column{
			{Name: "ln", Type: catalog.Type{Name: "text"}},
			{Name: "fn", Type: catalog.Type{Name: "text"}},
			{Name: "q", Type: catalog.Type{Name: "int4"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return cat
}

// planSortedAggUnion plans sql with hash aggregation off, so each UNION ALL
// branch is a GroupAggregate emitting rows in its group-key order.
func planSortedAggUnion(t *testing.T, cat catalog.Catalog, sql string) Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ps := DefaultPlannerSettings()
	ps.EnableHashAgg = false
	plan, err := PlanWithSettings(stmts[0], cat, ps)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

const orderedAppendUnion = `SELECT ln, fn, sales FROM (SELECT ln, fn, sum(q) sales FROM oa_cs GROUP BY ln, fn
	UNION ALL SELECT ln, fn, sum(q) sales FROM oa_ws GROUP BY ln, fn) x `

// An ORDER BY the members' ordering already contains takes the Merge Append
// as-is: it costs the members plus cost_merge_append's heap terms, cheaper
// than cost_sort over the plain Append.
func TestOrderedStageMergesUnionAllMembers(t *testing.T) {
	plan := planSortedAggUnion(t, orderedAppendFixture(t), orderedAppendUnion+"ORDER BY ln, fn")
	var top Node = plan
	for {
		p, ok := top.(*Project)
		if !ok {
			break
		}
		top = p.Child
	}
	so, ok := top.(*SetOp)
	if !ok || len(so.MergeKeys) != 2 {
		t.Fatalf("ORDER BY ln, fn over sorted members: top is %T (want a Merge Append SetOp with 2 merge keys)", top)
	}
}

// An ORDER BY whose leading keys the members deliver gets an Incremental Sort
// over the Merge Append as a candidate, beside the seed's full Sort.
func TestOrderedStageOffersIncrementalSortOverMergeAppend(t *testing.T) {
	plan := planSortedAggUnion(t, orderedAppendFixture(t), orderedAppendUnion+"ORDER BY ln, fn, sales")
	so := findUnionAll(plan)
	if so == nil {
		t.Fatal("no UNION ALL in the plan")
	}
	out := so.Output()
	keys := make([]SortKey, 3)
	for i := range keys {
		keys[i] = SortKey{Expr: &ColumnRef{Index: i, Name: out[i].Name, Type: out[i].Type}}
	}
	cp := defaultCostParams()
	ordered := &RelOptInfo{}
	addOrderedAppendPaths(ordered, so, pathkeysForSortKeys(keys), cp, -1)
	var got *Path
	for _, p := range ordered.Pathlist {
		if p.Kind == PathIncrementalSort {
			got = p
		}
	}
	if got == nil {
		t.Fatalf("no Incremental Sort candidate over the Merge Append (pathlist %d)", len(ordered.Pathlist))
	}
	if got.PresortedCount != 2 || len(got.Children) != 1 || len(got.Children[0].Pathkeys) != 2 {
		t.Fatalf("Incremental Sort presorted=%d over %d-key input, want 2 over the members' (ln, fn)", got.PresortedCount, len(got.Children[0].Pathkeys))
	}
	cp.enableIncrementalSort = false
	off := &RelOptInfo{}
	addOrderedAppendPaths(off, so, pathkeysForSortKeys(keys), cp, -1)
	if len(off.Pathlist) != 0 {
		t.Fatalf("enable_incremental_sort off still filed %d paths", len(off.Pathlist))
	}
}

// cost_append's per-row overhead: a UNION ALL costs its members' seeded
// totals plus 0.5 * cpu_tuple_cost per row. (The members' own Project
// charge, which PG's trivial SubqueryScan does not make, is a ledgered
// residual: removing it flips TestParameterisedAppendOverUnionAll.)
func TestUnionAllAppendCostsMembersPlusAppendOverhead(t *testing.T) {
	so := findUnionAll(planSortedAggUnion(t, orderedAppendFixture(t), orderedAppendUnion))
	if so == nil {
		t.Fatal("no UNION ALL in the plan")
	}
	var want, rows float64
	for _, m := range unionAllMembers(so) {
		pc := seedPathForNode(&RelOptInfo{}, m).Cost
		if _, isProj := m.(*Project); !isProj {
			t.Fatalf("member %T, want the branch's Project", m)
		}
		want += pc.Total
		rows += float64(seedRowsForNode(m))
	}
	cp := defaultCostParams()
	want += cp.cpuTupleCost * appendCPUCostMultiplier * rows
	if got := legacyDisplayCostOf(so).TotalCost; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("Append total %.6f, want members %.6f (cost_append)", got, want)
	}
}

func findUnionAll(n Node) *SetOp {
	if so, ok := n.(*SetOp); ok && so.All {
		return so
	}
	kids, _ := planChildNodes(n)
	for _, k := range kids {
		if so := findUnionAll(k); so != nil {
			return so
		}
	}
	return nil
}
