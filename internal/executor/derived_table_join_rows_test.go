package executor

// R3-2: a derived table joined to an outer relation must return its rows.
//
// The csq-S6 deferral row recorded a wrong-results observation during
// round-1 stage-6b measurements: `FROM orders, (SELECT DISTINCT l_orderkey
// FROM lineitem WHERE l_commitdate < l_receiptdate) lk WHERE o_orderkey =
// lk.l_orderkey` returned 0 rows fast on SF1 while the derived subquery
// alone counted 1 375 096, with a `NL (CROSS) + Filter` over `Unique` plan.
// The hypothesised mechanism was the NL operator re-Opening a Unique-rooted
// inner and finding it already at EOF.
//
// R3-2 falsified both halves at HEAD:
//
//   - The mechanism is structurally impossible: the non-lateral nested-loop
//     path drains BOTH children exactly once into materialised slices and
//     then loops over those slices (joinOp.Open -> drainRowsCtx* ->
//     runNestedLoop); there is no per-outer re-Open. distinctOp.Open also
//     resets its accumulation state unconditionally (the Stage-9 / M12 fix).
//   - The result does not reproduce: on the capped SF1 bench server the
//     ledger's exact query returns 1 375 096, equal to the derived count.
//
// The observation is therefore attributed to round-1-stage-6b-era state
// that no longer exists. These tests are the permanent guard, because the
// ledger's plan shape is still reachable (index-free tables) even though
// TPC-H's own version of the query now plans as an index-driven NLI:
// both shapes are pinned here so a regression cannot hide behind the
// planner having moved on.

import (
	"strings"
	"testing"
)

// newDerivedJoinFixture builds the ledger's shape at doll-house scale.
// withIndexes selects which plan the query gets: without indexes the
// planner produces the ledger's NL-CROSS + Filter over Unique; with them
// it produces the index-driven NLI that SF1 takes today.
func newDerivedJoinFixture(t *testing.T, withIndexes bool) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{
		"CREATE TABLE dj_orders (o_orderkey int, o_flag int)",
		"CREATE TABLE dj_lineitem (l_orderkey int, l_linenumber int, l_commitdate int, l_receiptdate int)",
	}
	if withIndexes {
		stmts = append(stmts, "CREATE INDEX dj_orders_pk ON dj_orders (o_orderkey)")
	}
	stmts = append(stmts,
		"INSERT INTO dj_orders VALUES (1, 0)",
		"INSERT INTO dj_orders VALUES (2, 0)",
		"INSERT INTO dj_orders VALUES (3, 0)",
		// orderkey 1 qualifies twice (so DISTINCT has real work),
		// 2 never qualifies, 3 qualifies once.
		"INSERT INTO dj_lineitem VALUES (1, 1, 1, 2)",
		"INSERT INTO dj_lineitem VALUES (1, 2, 1, 3)",
		"INSERT INTO dj_lineitem VALUES (2, 1, 5, 1)",
		"INSERT INTO dj_lineitem VALUES (3, 1, 2, 9)",
	)
	for _, stmt := range stmts {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// derivedJoinQuery keeps the ledger's exact qualification pattern: the
// outer column unqualified, the derived column alias-qualified.
const derivedJoinQuery = "SELECT o_orderkey FROM dj_orders, " +
	"(SELECT DISTINCT l_orderkey FROM dj_lineitem WHERE l_commitdate < l_receiptdate) lk " +
	"WHERE o_orderkey = lk.l_orderkey ORDER BY o_orderkey"

func assertDerivedJoinRows(t *testing.T, ctx *Context) {
	t.Helper()
	// The derived subquery alone is the reference: the join must not
	// lose any of its keys, since every distinct key has exactly one
	// matching order row.
	alone, err := runQueryWithErr(ctx, "SELECT DISTINCT l_orderkey FROM dj_lineitem WHERE l_commitdate < l_receiptdate ORDER BY l_orderkey")
	if err != nil {
		t.Fatalf("derived alone: %v", err)
	}
	joined, err := runQueryWithErr(ctx, derivedJoinQuery)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	a, j := renderRows(alone), renderRows(joined)
	if len(j) == 0 && len(a) > 0 {
		t.Fatalf("derived-table join returned zero rows while the subquery alone returned %v (the csq-S6 ledger signature)", a)
	}
	if len(a) != len(j) {
		t.Fatalf("join lost rows: derived alone %v vs joined %v", a, j)
	}
	for i := range a {
		if a[i] != j[i] {
			t.Fatalf("row %d: derived alone %q vs joined %q", i, a[i], j[i])
		}
	}
}

// TestDerivedTableUnderCrossNLReturnsRows pins the ledger's exact plan
// shape: a plain nested loop with the equality retained as a join Filter,
// over a Unique-rooted derived input.
func TestDerivedTableUnderCrossNLReturnsRows(t *testing.T) {
	ctx, cleanup := newDerivedJoinFixture(t, false)
	defer cleanup()

	plan := nliResidualExplain(t, ctx, derivedJoinQuery)
	if !strings.Contains(plan, "Nested Loop") || !strings.Contains(plan, "Unique") {
		t.Skipf("the ledger's NL-over-Unique shape is no longer produced; plan:\n%s", plan)
	}
	assertDerivedJoinRows(t, ctx)
}

// TestDerivedTableUnderIndexNLReturnsRows pins the shape SF1 actually
// takes today, where the Unique output drives an index probe into orders.
func TestDerivedTableUnderIndexNLReturnsRows(t *testing.T) {
	ctx, cleanup := newDerivedJoinFixture(t, true)
	defer cleanup()
	assertDerivedJoinRows(t, ctx)
}
