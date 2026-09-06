package optimizer

// C-12 (P4-03) — the ORDERED upper rel's real `PathSort`. What is pinned:
// the node the producer emits is the rewrite's node (same child, keys,
// position), it now carries `cost_sort`'s price through the NAMED cost
// constants, the price includes the external-merge arm when the sized rel
// says the sort spills (DESIGN §4.3 — the silent failure to look for first),
// both producers exist and are adjudicable on the DPPATH trace, and the C-10c
// arm the Sort still passes through is still reached.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// pricedNode is a Node that carries a search-style PlanCost, so the producer's
// input arm reads the stamp (`legacyDisplayCostOf`) rather than deriving.
type pricedNode struct {
	PlanCost
	sch Schema
}

func (n *pricedNode) Output() Schema { return n.sch }
func (n *pricedNode) Pos() int       { return 0 }
func (n *pricedNode) planNode()      {}

func upperOrderedInput(rows float64) *pricedNode {
	n := &pricedNode{sch: Schema{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"}},
		{Name: "w", Type: catalog.Type{Name: "numeric"}},
	}}
	n.setPlanCost(PlanCost{StartupCost: 10, TotalCost: 100, PlanRows: rows, PlanWidth: 1})
	return n
}

func upperOrderedKeys() []SortKey {
	return []SortKey{
		{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, Desc: true},
		{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, NullsFirst: true},
	}
}

// TestCreateOrderedPathsEmitsTheRewritesSortWithCostSortsPrice: the node is
// what `orderSort = &Sort{pos, Child: node, Keys: keys}` built — and its
// PlanCost is the input's total plus `costSortRun` over the SIZED rel.
func TestCreateOrderedPathsEmitsTheRewritesSortWithCostSortsPrice(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	keys := upperOrderedKeys()
	u := newUpperRels()

	got := createOrderedPaths(u, in, keys, 77, cp, 0, -1)

	srt, ok := got.(*Sort)
	if !ok {
		t.Fatalf("got %T, want *Sort", got)
	}
	if srt.Child != Node(in) {
		t.Fatalf("Sort.Child is %T, want the input node itself", srt.Child)
	}
	if srt.pos != 77 {
		t.Fatalf("Sort.pos = %d, want the statement position 77", srt.pos)
	}
	if len(srt.Keys) != len(keys) {
		t.Fatalf("%d keys, want %d", len(srt.Keys), len(keys))
	}
	for i := range keys {
		if srt.Keys[i].Expr != keys[i].Expr || srt.Keys[i].Desc != keys[i].Desc || srt.Keys[i].NullsFirst != keys[i].NullsFirst {
			t.Fatalf("key %d = %+v, want %+v emitted as written (no baseLeaf, no translation)", i, srt.Keys[i], keys[i])
		}
	}
	if isSearchedTree(srt) {
		t.Fatalf("the ORDERED Sort must not be tagged as a searched root: only createPlanAtSearchRoot marks")
	}

	rel := fetchUpperRel(u, UpperOrdered, 0, 0)
	if rel.NCols != 3 || rel.Rows != 1000 || rel.AvgVarBytes <= 0 {
		t.Fatalf("ORDERED rel not sized from the input: %+v", rel)
	}
	pc, set := srt.PlanCostInfo()
	if !set {
		t.Fatalf("the Sort carries no PlanCost: createPlanNode's stamp was bypassed")
	}
	want := costSortRun(cp, 1000, relNCols(rel), relAvgVarBytes(rel), -1)
	if !approx(pc.StartupCost, 100+want.Startup) || !approx(pc.TotalCost, 100+want.Total) {
		t.Fatalf("Sort cost = (%v, %v), want input total 100 + costSortRun (%v, %v)",
			pc.StartupCost, pc.TotalCost, 100+want.Startup, 100+want.Total)
	}
	if pc.PlanRows != 1000 {
		t.Fatalf("Sort rows = %v, want the input's 1000 (a Sort projects nothing)", pc.PlanRows)
	}
	// The child's stamp is value-identical to what it carried: nothing below
	// the Sort moves.
	if cpc, _ := in.PlanCostInfo(); cpc.StartupCost != 10 || cpc.TotalCost != 100 || cpc.PlanRows != 1000 {
		t.Fatalf("the input's PlanCost was rewritten to %+v", cpc)
	}
	if rel.CheapestTotal == nil || rel.CheapestTotal.Kind != PathSort || len(rel.Pathlist) != 1 {
		t.Fatalf("ORDERED rel pathlist = %d paths, cheapest %v; want the one sort path", len(rel.Pathlist), rel.CheapestTotal)
	}
}

// TestCreateOrderedPathsChargesTheSpillOfALargeSort is DESIGN §5.7's named
// negative result — "a Sort's new cost is LOWER than its legacy display cost:
// then §4.3's NCols population did not happen and the disk arm is suppressed."
// A sort whose sized input exceeds work_mem must be priced ABOVE both the
// unsized (NCols = 0) price and the legacy in-memory display price.
func TestCreateOrderedPathsChargesTheSpillOfALargeSort(t *testing.T) {
	cp := defaultCostParams()
	u := newUpperRels()
	// Size the rel first to learn its column model, then pick a row count
	// that overflows the budget at that width.
	probe := fetchUpperRel(u, UpperOrdered, 0, 0)
	sizeUpperRelFromNode(probe, upperOrderedInput(1))
	rows := sortRowsFillingBudget(cp.workMem, probe.NCols, 3.0)
	in := upperOrderedInput(rows)

	srt := createOrderedPaths(u, in, upperOrderedKeys(), 0, cp, 0, -1).(*Sort)
	pc, _ := srt.PlanCostInfo()

	unsized := costSortRun(cp, rows, 0, 0, -1)
	if !(pc.StartupCost-100 > unsized.Startup) {
		t.Fatalf("spilling sort priced at %v, not above the NCols=0 in-memory price %v: the disk arm is suppressed",
			pc.StartupCost-100, unsized.Startup)
	}
	legacy := DeriveLegacyDisplayCost(srt, int64(rows))
	if !(pc.StartupCost > legacy.StartupCost) {
		t.Fatalf("cost_sort price %v is not above the legacy display price %v — the negative result DESIGN §5.7 names", pc.StartupCost, legacy.StartupCost)
	}
}

// TestAddOrderedPathsOffersExactlyOneProducerPerInput: both producers exist
// and are told apart on the trace by name and by `relids=-`. The input arm is
// driven with a hand-ordered seed because no Node above the seam carries
// pathkeys today (DESIGN §5.5) — the arm C-12a turns live must not be dead
// code when it does.
func TestAddOrderedPathsOffersExactlyOneProducerPerInput(t *testing.T) {
	cp := defaultCostParams()
	keys := pathkeysForSortKeys(upperOrderedKeys())

	unordered := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	sizeUpperRelFromNode(unordered, upperOrderedInput(10))
	lines := captureTrace(t, func() {
		addOrderedPaths(unordered, newPrebuiltPath(unordered, upperOrderedInput(10)), keys, cp, -1)
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "producer="+upperOrderedSortProducer+" relids=- ") || !strings.Contains(lines[0], "verdict=accepted") {
		t.Fatalf("unordered input: trace = %q, want one accepted %s line at relids=-", lines, upperOrderedSortProducer)
	}
	if unordered.Pathlist[0].Kind != PathSort {
		t.Fatalf("unordered input must get a Sort path, got kind %d", unordered.Pathlist[0].Kind)
	}

	ordered := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	sizeUpperRelFromNode(ordered, upperOrderedInput(10))
	seed := newPrebuiltPath(ordered, upperOrderedInput(10))
	seed.Pathkeys = append(append([]PathKey{}, keys...), PathKey{Expr: &ColumnRef{Index: 2, Name: "w"}, SortAsc: true})
	lines = captureTrace(t, func() {
		addOrderedPaths(ordered, seed, keys, cp, -1)
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "producer="+upperOrderedInputProducer+" relids=- ") {
		t.Fatalf("ordered input: trace = %q, want one %s line", lines, upperOrderedInputProducer)
	}
	if len(ordered.Pathlist) != 1 || ordered.Pathlist[0] != seed {
		t.Fatalf("an input that already delivers the keys must be offered as-is, no Sort stacked")
	}
}

// TestCreateOrderedPathsHonoursEnableSortAsAPreference: B-17a — the producer
// is not skipped when enable_sort is off; the path is offered with a disabled
// node so a query whose only plan needs the sort still plans.
func TestCreateOrderedPathsHonoursEnableSortAsAPreference(t *testing.T) {
	cp := defaultCostParams()
	cp.enableSort = false
	u := newUpperRels()
	lines := captureTrace(t, func() {
		if _, ok := createOrderedPaths(u, upperOrderedInput(10), upperOrderedKeys(), 0, cp, 0, -1).(*Sort); !ok {
			t.Errorf("enable_sort=off must still emit the Sort")
		}
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "disabled=1 ") {
		t.Fatalf("trace = %q, want the sort path offered with disabled=1", lines)
	}
}

// TestCreateOrderedPathsNeverDropsTheSort: no keys hands the input back; no
// registry does NOT — a missing registry must never turn into a missing
// ORDER BY, which would be a wrong answer with green row counts.
func TestCreateOrderedPathsNeverDropsTheSort(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(10)
	if got := createOrderedPaths(newUpperRels(), in, nil, 0, cp, 0, -1); got != Node(in) {
		t.Fatalf("no keys: got %T, want the input back", got)
	}
	if _, ok := createOrderedPaths(nil, in, upperOrderedKeys(), 0, cp, 0, -1).(*Sort); !ok {
		t.Fatalf("no registry: the Sort was dropped")
	}
}

// TestC10cPreservedSideQualMovesThroughOrderedSortArm is C-10c's per-item
// re-assert for C-12 (p4-upper-rels DESIGN §8): the Sort the ORDERED rel
// emits sits at the same tree position as the rewrite's, so
// pushSingleSideQualsIntoInnerJoinInputs' `*Sort` arm still descends through
// it and the preserved-side move below a LEFT link still happens. Mirrors
// TestC10cPreservedSideQualMovesThroughAggregateArm one node up.
func TestC10cPreservedSideQualMovesThroughOrderedSortArm(t *testing.T) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	resid := &Filter{Child: j, Predicate: srcGt(0, "id", 1, 7)}
	keys := []SortKey{{Expr: &ColumnRef{Index: 1, Name: "name", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1}}}

	srt, ok := createOrderedPaths(newUpperRels(), resid, keys, 0, defaultCostParams(), 0, -1).(*Sort)
	if !ok {
		t.Fatalf("the ORDERED rel did not emit a *Sort")
	}
	got := pushSingleSideQualsIntoInnerJoinInputs(srt)

	root, ok := got.(*Sort)
	if !ok {
		t.Fatalf("pass returned %T, want the *Sort root back", got)
	}
	nj, ok := root.Child.(*Join)
	if !ok {
		t.Fatalf("Sort.Child is %T, want the *Join (residual spliced through the Sort arm)", root.Child)
	}
	if _, placed := nj.Left.(*Filter); !placed {
		t.Errorf("Join.Left is %T, want the placed *Filter on the preserved input", nj.Left)
	}
	if _, planted := nj.Right.(*Filter); planted {
		t.Errorf("a Filter reached the NULLABLE input for a preserved-side qual")
	}
}
