package executor

import "testing"

// TestBushyDPAttachesUnusedCrossEdge is the executor-level regression for the
// M-NIGHTLY tpch/Q9-timeout root cause (see .ralph/deferral_ledger.md,
// 2026-07-07 entries): a table pair connected by TWO equality conjuncts
// (here zz_partsupp<->zz_lineitem via suppkey AND partkey) only ever has ONE
// edge wired into the bushy-DP join's canonical LeftKey/RightKey
// (internal/planner/bushy.go's enumerateBushyPlans); the other conjunct must
// still apply as a real filter, not silently vanish or (worse) drop rows it
// shouldn't. This is also the exact shape — a Join later folded into a
// *MultiHashJoin by rewriteMultiWayChain — that a prior, reverted prototype
// fix corrupted: attaching the extra conjunct in the Join's own LOCAL
// bushy-DP-subset coordinates confused collectMultiHashTables/
// applyJoinTreePosMap's MHJ-Filters remap, which expects RAW global
// FROM-order coordinates instead (see attachUnusedCrossEdges's doc comment
// in bushy.go).
//
// zz_partsupp has 3 rows sharing one suppkey (100) with different partkeys
// (1,2,3); zz_lineitem has a single row (partkey=2, suppkey=100). The
// suppkey edge alone would hash-join all 3 partsupp rows against the one
// lineitem row; only the partkey edge narrows that down to the single
// genuinely-matching row. zz_part further requires partkey=2 to appear at
// all — a 3-way comma-FROM chain long enough to fold into a *MultiHashJoin
// (needs >= 3 tables) once bushy DP picks its tree.
//
// Caveat (does not, by itself, prove attachUnusedCrossEdges is necessary):
// at this small a scale bushy DP's chosen tree happens to keep bushy-DP
// LOCAL subset coordinates numerically identical to GLOBAL FROM-order
// coordinates, so the pre-existing pushdown.go pushOneConjunct also
// reattaches this conjunct on its own if attachUnusedCrossEdges is
// reverted — this test alone would not go red. It still guards
// attachUnusedCrossEdges and collectCrossSideEquiKeys's name-based
// fallback against a future correctness regression (wrong attachment,
// wrong side classification) whenever they DO fire. The actual case where
// the two coordinate spaces diverge — and pushOneConjunct's width-based
// heuristic silently fails, which is what timed out — only reproduces at
// real TPC-H Q9 scale (6-way join, real ANALYZE-driven cost estimates);
// see the deferral ledger for the end-to-end verification against
// bench/tpch/runtime_goopg/data.
func TestBushyDPAttachesUnusedCrossEdge(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
//
// M0127-P6.2 note: the MultiHashJoin node named below was deleted, so this
// shape now plans as the left-deep binary hash cascade PG builds. The test is
// kept unchanged — its assertions are about the RESULT, so it now guards the
// cascade against the same defect the packed node once carried.

	for _, ddl := range []string{
		"CREATE TABLE zz_part (p_partkey int, p_name text)",
		"CREATE TABLE zz_lineitem (l_partkey int, l_suppkey int)",
		"CREATE TABLE zz_partsupp (ps_partkey int, ps_suppkey int)",
		"INSERT INTO zz_part VALUES (2, 'widget')",
		"INSERT INTO zz_lineitem VALUES (2, 100)",
		"INSERT INTO zz_partsupp VALUES (1, 100), (2, 100), (3, 100)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	const q = `SELECT p_name, ps_partkey
	             FROM zz_part, zz_lineitem, zz_partsupp
	            WHERE ps_suppkey = l_suppkey
	              AND ps_partkey = l_partkey
	              AND p_partkey = l_partkey`

	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (ps_partkey=1 and ps_partkey=3 must NOT survive the suppkey-only join): %v", len(rows), rows)
	}
	if got := rows[0][0].StringValue(); got != "widget" {
		t.Errorf("p_name = %q, want %q", got, "widget")
	}
	if got := rows[0][1].Int; got != 2 {
		t.Errorf("ps_partkey = %d, want 2", got)
	}
}
