package executor

import "testing"

// TestRelStatsAccumulateFlushGet verifies the three-tier relation-stats model:
// transactional DML counters stage per session+transaction, fold into the
// per-session pending counters at commit, and only become visible in the shared
// store after a flush. Non-transactional scan counters go straight to pending.
// M0118-0009 (`stats`, rung 7; design 0118-0131).
func TestRelStatsAccumulateFlushGet(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(42)

	// Before any flush, an absent OID reads as (zero, false) so the getters
	// return 0 (PG returns 0, not NULL, for relation stats).
	if c, ok := m.get(oid); ok || c != (relStatCounters{}) {
		t.Fatalf("absent OID: got %+v ok=%v, want zero,false", c, ok)
	}

	// Session 1 scans (reads 2 tuples) and inserts 3 rows; session 2 deletes 1.
	m.recordScan(1, oid, 2)
	m.recordInsert(1, oid, 3)
	m.recordDelete(2, oid, 1)

	// Staged transactional counters are invisible to pending until the
	// transaction commits; scans are already in pending but still unflushed.
	m.commitXact(1)
	m.commitXact(2)

	// Pending counters are invisible until flushed.
	if _, ok := m.get(oid); ok {
		t.Fatalf("pending counters leaked into shared before flush")
	}

	m.flush(1)
	got, ok := m.get(oid)
	if !ok {
		t.Fatalf("after flush(1): expected shared entry")
	}
	// Only session 1's pending applied: 1 scan, 2 returned, 3 inserted, +3 live.
	want := relStatCounters{numScans: 1, tuplesReturned: 2, tuplesInserted: 3, deltaLive: 3}
	if got != want {
		t.Fatalf("after flush(1): got %+v want %+v", got, want)
	}

	// Session 2's delete is still pending; flush it and verify dead/live deltas.
	m.flush(2)
	got, _ = m.get(oid)
	// delete: tuplesDeleted +1, deltaDead +1, deltaLive -1 → live 3-1=2, dead 1.
	want = relStatCounters{numScans: 1, tuplesReturned: 2, tuplesInserted: 3, tuplesDeleted: 1, deltaLive: 2, deltaDead: 1}
	if got != want {
		t.Fatalf("after flush(2): got %+v want %+v", got, want)
	}
}

// TestRelStatsUpdateDeadDelta verifies an UPDATE leaves a dead tuple without
// changing the live count (goopg has no HOT update).
func TestRelStatsUpdateDeadDelta(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(7)
	m.recordInsert(1, oid, 1) // live +1
	m.recordUpdate(1, oid, 2) // dead +2, live unchanged
	m.commitXact(1)
	m.flush(1)
	got, _ := m.get(oid)
	if got.deltaLive != 1 || got.deltaDead != 2 || got.tuplesUpdated != 2 {
		t.Fatalf("update deltas: got %+v want live=1 dead=2 upd=2", got)
	}
}

// TestRelStatsAbortDeadTuples verifies the abort math (AtEOXact_PgStat_Relations,
// abort case): an aborted transaction's inserts and updates become dead tuples,
// an aborted delete is a no-op on live/dead, and the attempted insert/update/
// delete totals still count.
func TestRelStatsAbortDeadTuples(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(11)
	// Committed baseline: one live tuple (mirrors the spec's setup k0 row).
	m.recordInsert(1, oid, 1)
	m.commitXact(1)
	m.flush(1)

	// Aborted transaction: insert 3, update 5, delete 1, then ROLL BACK.
	m.recordInsert(1, oid, 3)
	m.recordUpdate(1, oid, 5)
	m.recordDelete(1, oid, 1)
	m.abortXact(1)
	m.flush(1)

	got, _ := m.get(oid)
	// ins = 1 + 3 = 4 (attempted), upd = 5, del = 1, live stays 1 (no commit
	// delta), dead = 3 + 5 = 8 (aborted inserts + updates; aborted delete is a
	// no-op). This is the stats.spec s1_rollback_prepared_a expected row.
	want := relStatCounters{tuplesInserted: 4, tuplesUpdated: 5, tuplesDeleted: 1, deltaLive: 1, deltaDead: 8}
	if got != want {
		t.Fatalf("abort math: got %+v want %+v", got, want)
	}
}

// TestRelStatsTruncateCommit verifies pgstat_count_truncate semantics: a
// committed TRUNCATE forgets all prior live/dead counts (including already
// flushed ones) and resets the in-transaction tuple counters, while the
// non-transactional insert/update totals continue to accumulate. This is the
// stats.spec s1_table_truncate + COMMIT PREPARED expected row.
func TestRelStatsTruncateCommit(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(13)
	// Committed baseline: setup k0 (1 live) + an autocommit insert of 3 rows.
	m.recordInsert(1, oid, 1)
	m.commitXact(1)
	m.flush(1)
	m.recordInsert(1, oid, 3)
	m.commitXact(1)
	m.flush(1)
	if got, _ := m.get(oid); got.deltaLive != 4 {
		t.Fatalf("baseline live: got %d want 4", got.deltaLive)
	}

	// Explicit transaction: two updates, TRUNCATE, insert 1, update 1, COMMIT.
	m.recordUpdate(1, oid, 2)
	m.recordTruncate(1, oid)
	m.recordInsert(1, oid, 1)
	m.recordUpdate(1, oid, 1)
	m.commitXact(1)
	m.flush(1)

	got, _ := m.get(oid)
	// ins = 1(setup) + 3(autocommit) + 1(post-truncate) = 5; the two pre-truncate
	// updates were reset, so upd = 1; live/dead forgotten by the truncate then
	// rebuilt from the post-truncate insert/update → live 1, dead 1.
	if got.tuplesInserted != 5 || got.tuplesUpdated != 1 || got.tuplesDeleted != 0 ||
		got.deltaLive != 1 || got.deltaDead != 1 {
		t.Fatalf("truncate commit: got %+v want ins=5 upd=1 del=0 live=1 dead=1", got)
	}
}

// TestRelStatsTwoPhaseCommit verifies the 2PC handoff: staged counters move into
// a per-gid record at PREPARE and fold into the *finalising* backend's pending
// counters at COMMIT PREPARED (pgstat_twophase_postcommit), so a cross-backend
// COMMIT PREPARED + flush applies them.
func TestRelStatsTwoPhaseCommit(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(17)
	// Originating backend (session 1) stages an insert+update+delete, then prepares.
	m.recordInsert(1, oid, 3)
	m.recordUpdate(1, oid, 5)
	m.recordDelete(1, oid, 1)
	m.prepareXact(1, "g")
	// Session 1's staging is now empty; nothing folds into its pending.
	m.commitXact(1)
	m.flush(1)
	if _, ok := m.get(oid); ok {
		t.Fatalf("counters leaked to shared before COMMIT PREPARED")
	}
	// A DIFFERENT backend (session 2) issues COMMIT PREPARED, then flushes.
	m.finalizePrepared("g", 2, true)
	m.flush(2)
	got, _ := m.get(oid)
	want := relStatCounters{tuplesInserted: 3, tuplesUpdated: 5, tuplesDeleted: 1, deltaLive: 2, deltaDead: 6}
	if got != want {
		t.Fatalf("2PC commit: got %+v want %+v", got, want)
	}
}

// TestRelStatsTwoPhaseAbort verifies ROLLBACK PREPARED applies abort math to the
// finalising backend's pending counters (pgstat_twophase_postabort).
func TestRelStatsTwoPhaseAbort(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(19)
	m.recordInsert(1, oid, 3)
	m.recordUpdate(1, oid, 5)
	m.recordDelete(1, oid, 1)
	m.prepareXact(1, "g")
	m.finalizePrepared("g", 2, false)
	m.flush(2)
	got, _ := m.get(oid)
	// Abort: ins/upd/del counted, dead = ins + upd = 8, no live delta.
	want := relStatCounters{tuplesInserted: 3, tuplesUpdated: 5, tuplesDeleted: 1, deltaLive: 0, deltaDead: 8}
	if got != want {
		t.Fatalf("2PC abort: got %+v want %+v", got, want)
	}
}

// TestRelStatsDropTable verifies DROP removes shared, pending, staged and
// prepared counters so a getter on the dropped OID reads 0, and stale counts are
// not revived on a later flush or finalise (pgstat_drop_relation).
func TestRelStatsDropTable(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(99)
	m.recordInsert(1, oid, 5)
	m.commitXact(1)
	m.flush(1)
	m.recordScan(2, oid, 4)   // session 2 has stale pending after the drop
	m.recordInsert(3, oid, 2) // session 3 has stale staging after the drop
	m.recordInsert(4, oid, 7) // session 4 has a stale prepared record
	m.prepareXact(4, "g")

	m.dropTable(oid)
	if _, ok := m.get(oid); ok {
		t.Fatalf("dropTable: shared entry survived")
	}
	// A flush of the stale pending / a commit of the stale staging / a finalise of
	// the stale prepared record must not revive the dropped OID.
	m.flush(2)
	m.commitXact(3)
	m.flush(3)
	m.finalizePrepared("g", 4, true)
	m.flush(4)
	if _, ok := m.get(oid); ok {
		t.Fatalf("dropTable: stale counters revived the dropped OID")
	}
}

// TestRelStatsDropTableClearsAutovacuumTriggers pins the second half of
// pgstat_drop_relation: the autovacuum-trigger inputs (n_dead_tup,
// n_ins_since_vacuum, n_mod_since_analyze) belong to the same stats entry and
// must not outlive the relation, or an OID reused by a later relation starts
// life already part-way to an autovacuum. review/260831-2 ES-3.
func TestRelStatsDropTableClearsAutovacuumTriggers(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(4242)
	m.recordInsert(1, oid, 100)
	m.recordUpdate(1, oid, 40)
	m.commitXact(1)
	m.flush(1)
	if dead, ins, mod := m.triggerSnapshot(oid); dead == 0 && ins == 0 && mod == 0 {
		t.Fatalf("setup: expected non-zero trigger counters, got dead=%d ins=%d mod=%d", dead, ins, mod)
	}

	m.dropTable(oid)
	if dead, ins, mod := m.triggerSnapshot(oid); dead != 0 || ins != 0 || mod != 0 {
		t.Errorf("after dropTable: dead=%d ins=%d mod=%d, want all zero", dead, ins, mod)
	}
}
