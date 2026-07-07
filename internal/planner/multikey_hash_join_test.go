package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestSplitEqualityForHashMultiKey guards splitEqualityForHash's
// AND-of-equalities fallback (M-NIGHTLY tpch/Q20-timeout). An
// explicit multi-column `JOIN ... ON a=b AND c=d` between a table
// and an expensive derived table (here a GROUP BY subquery, mirroring
// TPC-H Q20's `partsupp JOIN (SELECT ... GROUP BY l_partkey,
// l_suppkey)`) must still pick JoinAlgoHash on one of the two
// equalities — the executor's joinPredicateMatchSlot re-checks the
// full Predicate (both conjuncts) per hash match, so nothing is
// silently dropped. Before the fix, splitEqualityForHash only
// recognised a bare single equality and any AND-of-equalities
// predicate fell through to Nested Loop, which for this shape
// recomputes the GROUP BY aggregate once per outer row of the
// left-hand table — catastrophic at any real scale.
func TestSplitEqualityForHashMultiKey(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_availqty", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_quantity", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	q := `select ps.ps_suppkey
		from partsupp ps
		join (select l_partkey, l_suppkey, sum(l_quantity) as s from lineitem group by l_partkey, l_suppkey) agg
		  on ps.ps_partkey = agg.l_partkey and ps.ps_suppkey = agg.l_suppkey
		where ps.ps_availqty > agg.s`
	node, err := Plan(parseOne(t, q), c)
	if err != nil {
		t.Fatal(err)
	}
	s := planTreeString(node)

	foundHashJoin := false
	visit(node, func(n Node) bool {
		if j, ok := n.(*Join); ok && j.Type == JoinTypeInner {
			if j.Algo == JoinAlgoHash && j.LeftKey != nil && j.RightKey != nil {
				foundHashJoin = true
			}
		}
		return true
	})
	if !foundHashJoin {
		t.Errorf("multi-key JOIN...ON did not hash-join on any of the AND'd equalities (fell back to Nested Loop); got plan:\n%s", s)
	}
}
