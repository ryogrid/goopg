package executor

import "testing"

// TestUpdateFromSelfReferentialAggregateInheritedTarget guards against a
// self-deadlock in updateOp.updateWithFrom's non-HOT write path
// (M-NIGHTLY with-regress hang triage, 2026-07-14): a row sourced from an
// inheritance child (puSrcRel != rel) always takes the "!used" branch,
// which Pin+Lock'd the block once, then — when isConcurrentlyUpdated was
// false (the common case) — fell through to an unconditional second
// Pin+Lock on the same block without releasing the first, deadlocking the
// connection's own goroutine against itself. Reproduces
// postgres/src/test/regress/sql/with.sql's "WITH attached to inherited
// UPDATE" case, which used to hang the regress suite for the fixed 120s
// per-subtest timeout on every one of `inherit`, `returning`, and `with`.
func TestUpdateFromSelfReferentialAggregateInheritedTarget(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE parent (id int, val text)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child1 () INHERITS (parent)"); err != nil {
		t.Fatalf("create child1: %v", err)
	}
	runSQL(t, ctx, "INSERT INTO parent VALUES (1, 'p1')")
	runSQL(t, ctx, "INSERT INTO child1 VALUES (11, 'c11')")
	runSQL(t, ctx, "INSERT INTO child1 VALUES (12, 'c12')")

	// sum(id) over the inheritance-expanded parent = 1+11+12 = 24. Every
	// row — parent's own and both inheritance children's — must be
	// updated exactly once: this also guards the sibling dedup-key bug
	// (a bare (block, slot) key collided across the parent/child1
	// relations and silently dropped one child row per colliding slot).
	runSQL(t, ctx, "WITH rcte AS (SELECT sum(id) AS totalid FROM parent) UPDATE parent SET id = id + totalid FROM rcte")

	got := runSQL(t, ctx, "SELECT id, val FROM parent ORDER BY id")
	want := map[string]int64{"p1": 25, "c11": 35, "c12": 36}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %d: %+v", len(want), len(got), got)
	}
	for _, row := range got {
		val := row[1].StringValue()
		wantID, ok := want[val]
		if !ok {
			t.Fatalf("unexpected row val=%q in result %+v", val, got)
		}
		if row[0].Int != wantID {
			t.Errorf("row val=%q: id=%d, want %d", val, row[0].Int, wantID)
		}
		delete(want, val)
	}
	if len(want) != 0 {
		t.Fatalf("rows missing from result (silently dropped by the dedup-key collision): %+v", want)
	}
}
