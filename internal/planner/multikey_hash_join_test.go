package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// multiKeyCatalog is TPC-H Q20's shape: a table joined to an expensive derived
// table (a GROUP BY subquery) on TWO equalities. `rows > 0` installs row counts;
// `rows == 0` leaves both relations without statistics, which is what decides
// which of the two arms below can be asserted (see the test).
func multiKeyCatalog(t *testing.T, rows int64) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column) {
		tb, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		if rows <= 0 {
			return
		}
		tb.Stats = &catalog.TableStats{RowCount: rows}
		tb.Stats.Columns = make([]catalog.ColumnStats, len(cols))
		for i := range cols {
			tb.Stats.Columns[i] = catalog.ColumnStats{NDistinct: rows}
		}
	}
	mk("partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "ps_availqty", Type: catalog.Type{Name: "numeric"}},
	})
	mk("lineitem", []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_quantity", Type: catalog.Type{Name: "numeric"}},
	})
	return c
}

const multiKeyQuery = `select ps.ps_suppkey
	from partsupp ps
	join (select l_partkey, l_suppkey, sum(l_quantity) as s from lineitem group by l_partkey, l_suppkey) agg
	  on ps.ps_partkey = agg.l_partkey and ps.ps_suppkey = agg.l_suppkey
	where ps.ps_availqty > agg.s`

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
//
// M0127-P5.9-r split this into two arms, because the statement now has two
// producers rather than one. Before it, `tryPGShapedJoinSearch` declined every
// FROM clause written `JOIN … ON` (the leaf walk descended CROSS links only), so
// this shape ALWAYS reached the legacy syntactic builder no matter what the
// `GOOPG_PGSHAPED_DP` default was — which is why one arm used to be enough.
//
//   - the KILL-SWITCH arm (flag off) is the original test unchanged, and it is
//     the only one that exercises `splitEqualityForHash` itself: the algorithm
//     is chosen by `planFromItem` from the predicate's SHAPE, with no reference
//     to statistics;
//   - the DEFAULT arm (flag on) is the searched enumerator, which chooses from
//     COST. It is given row counts, because a relation whose size the planner
//     cannot see is estimated at the one-row floor and at one row per side a
//     nested loop is genuinely the cheaper plan — so a blind fixture would be
//     asserting the cost model is wrong rather than that the join is hashable.
//     That blind-relation behaviour is not new here and not specific to the
//     explicit-JOIN spelling: the comma-FROM spelling of this same statement
//     already planned a nested loop at the S5 flip, unnoticed, because this
//     test's JOIN spelling was the one shape the search could not reach. See
//     the deferral-ledger row for 2026-08-06 / M0127-P5.9-r.
func TestSplitEqualityForHashMultiKey(t *testing.T) {
	assertHashJoin := func(t *testing.T, c catalog.Catalog) {
		t.Helper()
		node, err := Plan(parseOne(t, multiKeyQuery), c)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		visit(node, func(n Node) bool {
			if j, ok := n.(*Join); ok && j.Type == JoinTypeInner {
				if j.Algo == JoinAlgoHash && j.LeftKey != nil && j.RightKey != nil {
					found = true
				}
			}
			return true
		})
		if !found {
			t.Errorf("multi-key JOIN...ON did not hash-join on any of the AND'd equalities "+
				"(fell back to Nested Loop); got plan:\n%s", planTreeString(node))
		}
	}

	t.Run("legacy syntactic builder", func(t *testing.T) {
		prev := pgShapedDP
		pgShapedDP = false
		t.Cleanup(func() { pgShapedDP = prev })
		assertHashJoin(t, multiKeyCatalog(t, 0))
	})

	t.Run("searched enumerator", func(t *testing.T) {
		withPGShapedDP(t)
		assertHashJoin(t, multiKeyCatalog(t, 800_000))
	})
}
