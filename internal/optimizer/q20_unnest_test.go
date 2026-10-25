package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanQ20OuterInFullyUnnested guards the M-NIGHTLY
// tpch/Q20-timeout root cause: the OUTERMOST `s_suppkey IN (SELECT
// ps_suppkey FROM partsupp WHERE ...)` must be unnested into a
// SemiJoin, not left as a raw `*InExpr` inside a Filter predicate.
//
// `TestPlanQ20DumpsTree`'s `containsJoinType(node, JoinTypeSemi)`
// check does NOT catch a regression here: the MIDDLE-level
// `ps_partkey IN (SELECT p_partkey FROM part WHERE ...)` always
// unnests into its own SemiJoin regardless of whether the outer one
// does, so that assertion passes even when the outer IN is left
// un-unnested. This test walks every Filter predicate in the tree
// and fails if any live `*InExpr` remains — the exact signature
// EXPLAIN rendered as `Filter: (<*planner.InExpr> AND ...)` when the
// bug was live, which forced the executor to re-run the entire
// partsupp+lineitem-aggregate subtree once per supplier row (10000
// rows at TPC-H SF1) instead of building it once as a hash-join
// probe side. The root cause was `planHasOuterRef` (planner.go)
// treating ANY OuterColumnRef found inside a nested subquery as
// "escapes the tested plan", even when its Level (hop count) placed
// it squarely within the tested plan's own scope — see that
// function's doc comment for the corrected depth-aware semantics.
func TestPlanQ20OuterInFullyUnnested(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "s_name", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_address", Type: catalog.Type{Name: "varchar"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "n_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_availqty", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "p_name", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_quantity", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_shipdate", Type: catalog.Type{Name: "date"}},
	}); err != nil {
		t.Fatal(err)
	}
	q20 := `select s_name, s_address from supplier, nation where s_suppkey in ( select ps_suppkey from partsupp where ps_partkey in ( select p_partkey from part where p_name like 'forest%') and ps_availqty > ( select 0.5 * sum(l_quantity) from lineitem where l_partkey = ps_partkey and l_suppkey = ps_suppkey and l_shipdate >= date '1994-01-01' and l_shipdate < date '1994-01-01' + interval '1 year')) and s_nationkey = n_nationkey and n_name = 'CANADA' order by s_name`
	node, err := Plan(parseOne(t, q20), c)
	if err != nil {
		t.Fatal(err)
	}
	s := planTreeString(node)

	foundRawInExpr := false
	visit(node, func(n Node) bool {
		f, ok := n.(*Filter)
		if !ok {
			return true
		}
		for _, conjunct := range splitAnd(f.Predicate) {
			if _, ok := conjunct.(*InExpr); ok {
				foundRawInExpr = true
			}
		}
		return true
	})
	if foundRawInExpr {
		t.Errorf("Q20: outer `s_suppkey IN (...)` was left as a raw InExpr (not unnested to a SemiJoin) — this forces per-outer-row re-execution of the partsupp+lineitem subtree; got plan:\n%s", s)
	}
}
