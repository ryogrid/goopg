package planner

import "testing"

// useLegacyEnumerator pins one test to the OLD subset-bitmask DP — the
// `GOOPG_PGSHAPED_DP=0` kill-switch arm — for its duration.
//
// # Why this exists (M0127-P5.9, 2026-08-06)
//
// P5.9 flipped `GOOPG_PGSHAPED_DP` on, and the flip changes what KIND of
// decision picks a join operator. The old enumerator promotes operators by
// RULE: `rewriteJoinsToNLI` turns any equi-join on an indexed inner into a
// `NestedLoopIndexJoin`, `rewriteMultiWayChain` packs a hash cascade into a
// `MultiHashJoin`, `IsSmallDimensionSide` pins a build side. The PG-shaped
// search has no such rules — 08 §2 / [03] §10 make P5.9-b's eight skips keep
// all of them off a searched tree — and picks the operator by COST, like
// upstream's `add_path`.
//
// On a fixture built from `catalog.NewInMemory()` with no `TableStats`, cost
// has nothing to work with: every relation is zero rows, so the cheapest join
// really is a bare nested loop and the search plans one. That is not the
// production case. A real relation with no stats still has a FILE, and the
// seam sizes it from the block count before the search ever runs
// (`joinsearchseam.go`, tier 3 of `bushySeedRowCounts`' ladder) — which is why
// the acceptance bar's TPC-H and TPC-DS arms never saw this, and why
// `TestPGShapedSearchPicksNLIOnCost` reaches the same NLI these tests assert
// once the fixture carries the numbers that justify it.
//
// So these tests are not stale and they are not wrong: they cover the rewrite
// rules of an enumerator that still ships, still runs behind the kill-switch,
// and is not deleted until S7 (08 §4). Until that deletion they belong on the
// arm that has the rules. What must NOT happen is that they be re-pointed at
// the searched plan by relaxing their assertion — a test that accepts either
// operator would stop being able to fail.
//
// The counterpart obligation is coverage: anything asserted here on the legacy
// arm alone is a claim about a non-production path, so a searched-arm test
// must exist for the same behaviour or the behaviour is untested where it
// counts. Ledger row 2026-08-06 (M0127-P5.9) tracks the residue.
func useLegacyEnumerator(t *testing.T) {
	t.Helper()
	prev := pgShapedDP
	pgShapedDP = false
	t.Cleanup(func() { pgShapedDP = prev })
}
