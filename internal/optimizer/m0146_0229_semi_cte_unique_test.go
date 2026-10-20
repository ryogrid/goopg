package optimizer

import (
	"fmt"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0229 slice 2: `x IN (SELECT k FROM cte)` over a CTE whose body is a
// lone GROUP BY k. pull_up_sublinks makes it a semi join, and
// pull_up_subqueries flattens the simple sub-select, so the semi join's inner
// Var is the CTE's own. examine_simple_variable's RTE_CTE arm finds the CTE's
// planned subquery, sees the lone group key, and marks it isunique;
// get_variable_numdistinct then gives nd2 = the CTE rel's tuples instead of
// DEFAULT_NUM_DISTINCT. eqjoinsel_semi's nd arm therefore applies
// (nd2/nd1)·(1 − nullfrac1) rather than the default 0.5·(1 − nullfrac1).
// TPC-DS SF1 Q23 statement 2's best_ss_customer semi join is the witness:
// goopg estimated 150 rows (0.4971) where PG estimates 173 (0.5732), so its
// UNION ALL branches summed to 226 rows against PG's 299 and the ordered
// Merge Append + Incremental Sort lost to a full Sort.
func TestSemiJoinAgainstGroupedCTEUsesIsUnique(t *testing.T) {
	cat := catalog.NewInMemory()
	f, err := cat.CreateTable(parser.ObjectName{Name: "sf"}, []catalog.Column{
		{Name: "fk", Type: catalog.Type{Name: "int4"}},
		{Name: "fx", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Stats = &catalog.TableStats{RowCount: 1_000_000, Pages: 5000, Analyzed: true, Columns: []catalog.ColumnStats{
		{NDistinct: 50_000, AvgWidth: 4}, {NDistinct: 1000, AvgWidth: 4},
	}}
	g, err := cat.CreateTable(parser.ObjectName{Name: "sg"}, []catalog.Column{
		{Name: "gk", Type: catalog.Type{Name: "int4"}},
		{Name: "gv", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	g.Stats = &catalog.TableStats{RowCount: 300_000, Pages: 1500, Analyzed: true, Columns: []catalog.ColumnStats{
		{NDistinct: 30_000, AvgWidth: 4}, {NDistinct: 100, AvgWidth: 4},
	}}

	const q = `WITH b AS MATERIALIZED (SELECT gk, sum(gv) s FROM sg GROUP BY gk)
		SELECT count(*) FROM sf WHERE fk IN (SELECT gk FROM b)`
	n, err := Plan(parseOne(t, q), cat)
	if err != nil {
		t.Fatal(err)
	}
	// The joinrel's estimate is what is pinned: a semi-join path and the
	// unique-ified inner-join path over the CTE (create_unique_path) both
	// belong to the semi joinrel and carry its rows.
	var join Node
	var walk func(Node)
	walk = func(m Node) {
		if m == nil || join != nil {
			return
		}
		switch m.(type) {
		case *Join, *NestedLoopIndexJoin:
			join = m
			return
		}
		kids, _ := planChildNodes(m)
		for _, k := range kids {
			walk(k)
		}
	}
	walk(n)
	if join == nil {
		t.Fatalf("no join in the plan:\n%s", dumpRows(n))
	}
	// The CTE has 30000 rows (one per gk); nd1 = 50000 > nd2 = 30000, so
	// the semi selectivity is 30000/50000 = 0.6 of sf's 1000000 rows.
	got := float64(EstimateRows(join))
	if want := 600_000.0; math.Abs(got-want) > 1 {
		t.Fatalf("semi joinrel rows = %v, want %v (isunique nd2 = CTE rows); 500000 is the default-nd 0.5 arm\n%s",
			got, want, dumpRows(n))
	}
}

// dumpRows renders a plan as node types and row estimates, for failure output.
func dumpRows(n Node) string {
	out := ""
	var walk func(Node, string)
	walk = func(m Node, ind string) {
		if m == nil {
			return
		}
		out += fmt.Sprintf("%s%T rows=%d\n", ind, m, EstimateRows(m))
		kids, _ := planChildNodes(m)
		for _, k := range kids {
			walk(k, ind+"  ")
		}
	}
	walk(n, "")
	return out
}
