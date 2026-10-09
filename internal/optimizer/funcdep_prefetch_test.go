package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestFuncDepPassthroughSurvivesGroupingElection pins M0145-0008m. With
// `GROUP BY c1` on the primary key, the functionally dependent c2 read above
// the aggregate must be a column the elected aggregate input actually emits.
// The grouping election used to run BEFORE c2 joined the Aggregate's
// Passthrough, so `indexOrderedAggInput`'s Index Only Scan on the key — an
// input emitting only c1 — could win, and the executor read c2 as NULL.
//
// Hashing and seq scans are switched off so the index-ordered input is the
// election's natural winner; the assertion holds for whatever input wins.
func TestFuncDepPassthroughSurvivesGroupingElection(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "aso"}, []catalog.Column{
		{Name: "c1", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "c2", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "aso_pkey"}, tbl, []string{"c1"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 100, Analyzed: true,
		Columns: []catalog.ColumnStats{{NDistinctFrac: -1}, {NDistinctFrac: -1}}}
	ps := DefaultPlannerSettings()
	ps.EnableHashAgg = false
	ps.EnableSeqScan = false
	for _, sql := range []string{
		`select sum(c1), c2 from aso group by c1 order by 1`,
		`select sum(c1), c2 from aso group by c1 order by c2 desc`,
		`select c2, count(*) from aso group by c1 order by c1`,
	} {
		t.Run(sql, func(t *testing.T) {
			node, err := PlanWithSettings(parseOne(t, sql), cat, ps)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			agg := spineAggregate(node)
			if agg == nil {
				t.Fatalf("no Aggregate in %s", describePlanTree(node))
			}
			if len(agg.Passthrough) == 0 {
				t.Fatalf("c2 is not a passthrough; tree: %s", describePlanTree(node))
			}
			childOut := agg.Child.Output()
			for _, p := range agg.Passthrough {
				cr, ok := p.(*ColumnRef)
				if !ok {
					continue
				}
				if cr.Index >= len(childOut) || childOut[cr.Index].Name != cr.Name {
					t.Fatalf("passthrough %s reads child column %d, but the elected input %T emits %v",
						cr.Name, cr.Index, agg.Child, childOut)
				}
			}
		})
	}
}

// TestPrefetchFuncDepPassthroughsBeforeElection pins the mechanism of the
// M0145-0008m fix at the point it matters: right after the aggregate stage is
// built — where `createGroupingPaths` runs the election and the index-ordered
// input's coverage check reads `aggNode.Passthrough` — the functionally
// dependent c2 (target list, ORDER BY) must already be a passthrough. An
// aggregate argument (`sum(c2)`) reads the input and must NOT add one.
func TestPrefetchFuncDepPassthroughsBeforeElection(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "aso"}, []catalog.Column{
		{Name: "c1", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "c2", Type: catalog.Type{Name: "int4"}},
		{Name: "c3", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "aso_pkey"}, tbl, []string{"c1"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		sql  string
		want []string
	}{
		{`select sum(c1), c2 from aso group by c1`, []string{"c2"}},
		{`select sum(c1) from aso group by c1 order by c2`, []string{"c2"}},
		{`select sum(c2) from aso group by c1`, nil},
		{`select c2 + c3, count(*) from aso group by c1`, []string{"c2", "c3"}},
		// A grouping expression resolves whole; its inner column is never
		// read above the aggregate (TPC-DS Q23's substr(i_item_desc, 1, 30)).
		{`select c2 + 1, count(*) from aso group by c1, c2 + 1`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			s, ok := parseOne(t, tc.sql).(*parser.SelectStmt)
			if !ok {
				t.Fatal("not a SELECT")
			}
			node, ctx, err := planFromClause(s, cat, DefaultPlannerSettings(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx.cat = cat
			_, _, agg, _, err := buildAggregateStage(s, node, ctx, cat)
			if err != nil {
				t.Fatal(err)
			}
			prefetchFuncDepPassthroughs(s, agg)
			var got []string
			for _, p := range agg.node.Passthrough {
				if cr, ok := p.(*ColumnRef); ok {
					got = append(got, cr.Name)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("passthroughs before the election = %v, want %v", got, tc.want)
			}
		})
	}
}

// spineAggregate returns the first *Aggregate down the plan's single-child
// spine (Project / Sort / Limit / Filter above it).
func spineAggregate(n Node) *Aggregate {
	for n != nil {
		switch x := n.(type) {
		case *Aggregate:
			return x
		case *Project:
			n = x.Child
		case *Sort:
			n = x.Child
		case *Limit:
			n = x.Child
		case *Filter:
			n = x.Child
		default:
			return nil
		}
	}
	return nil
}
