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
	// The 3 committed inserts are also 3 change events and 3 ins_since_vacuum.
	want := relStatCounters{numScans: 1, tuplesReturned: 2, tuplesInserted: 3, deltaLive: 3, changedTuples: 3, insSinceVacuum: 3}
	if got != want {
		t.Fatalf("after flush(1): got %+v want %+v", got, want)
	}

	// Session 2's delete is still pending; flush it and verify dead/live deltas.
	m.flush(2)
	got, _ = m.get(oid)
	// delete: tuplesDeleted +1, deltaDead +1, deltaLive -1 → live 3-1=2, dead 1;
	// the committed delete adds 1 change event (mod_since_analyze 4) but no
	// attempted insert (ins_since_vacuum stays 3).
	want = relStatCounters{numScans: 1, tuplesReturned: 2, tuplesInserted: 3, tuplesDeleted: 1, deltaLive: 2, deltaDead: 1, changedTuples: 4, insSinceVacuum: 3}
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
	// ins_since_vacuum counts the aborted inserts too (4); mod_since_analyze
	// only counts the baseline commit (1 — an abort generates no change events).
	want := relStatCounters{tuplesInserted: 4, tuplesUpdated: 5, tuplesDeleted: 1, deltaLive: 1, deltaDead: 8, changedTuples: 1, insSinceVacuum: 4}
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
	// mod_since_analyze = 4 baseline changes + 2 post-truncate = 6 (a committed
	// truncate does not reset it); ins_since_vacuum is reset by the committed
	// truncate, then rebuilt by the 1 post-truncate insert → 1.
	if got.tuplesInserted != 5 || got.tuplesUpdated != 1 || got.tuplesDeleted != 0 ||
		got.deltaLive != 1 || got.deltaDead != 1 ||
		got.changedTuples != 6 || got.insSinceVacuum != 1 {
		t.Fatalf("truncate commit: got %+v want ins=5 upd=1 del=0 live=1 dead=1 mod=6 insvac=1", got)
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
	// Commit: dead = upd + del = 6, live = ins - del = 2; the 9 committed events
	// are mod_since_analyze 9 and ins_since_vacuum 3.
	want := relStatCounters{tuplesInserted: 3, tuplesUpdated: 5, tuplesDeleted: 1, deltaLive: 2, deltaDead: 6, changedTuples: 9, insSinceVacuum: 3}
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
	// Abort: ins/upd/del counted, dead = ins + upd = 8, no live delta, no
	// change events — but ins_since_vacuum counts the 3 attempted inserts.
	want := relStatCounters{tuplesInserted: 3, tuplesUpdated: 5, tuplesDeleted: 1, deltaLive: 0, deltaDead: 8, insSinceVacuum: 3}
	if got != want {
		t.Fatalf("2PC abort: got %+v want %+v", got, want)
	}
}

// TestRelStatsTruncateAbortRestores verifies restore_truncdrop_counters: an
// aborted in-transaction TRUNCATE restores the pre-truncate attempted counts
// (the post-truncate work happened on the doomed new relfilenode), and the
// abort dead-tuple math uses the restored values. This is the stats.spec
// s1_table_truncate + ROLLBACK PREPARED expected row (3|9|4|2|0|4|2|0).
func TestRelStatsTruncateAbortRestores(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(23)
	// Baseline: setup k0 + an autocommit 3-row insert (ins=4, live=4).
	m.recordInsert(1, oid, 1)
	m.commitXact(1)
	m.recordInsert(1, oid, 3)
	m.commitXact(1)
	m.flush(1)

	// Explicit transaction: two updates (pre-truncate), TRUNCATE, insert 1,
	// update 1, then ABORT.
	m.recordUpdate(1, oid, 2)
	m.recordTruncate(1, oid)
	m.recordInsert(1, oid, 1)
	m.recordUpdate(1, oid, 1)
	m.abortXact(1)
	m.flush(1)

	got, _ := m.get(oid)
	// Abort restores the pre-truncate counters (ins 0, upd 2, del 0), discarding
	// the post-truncate work: attempted ins = 4, upd = 2, del = 0; live is
	// untouched (4); dead = restored ins + upd = 2. The 4 baseline commits are
	// change events; the abort adds none.
	want := relStatCounters{tuplesInserted: 4, tuplesUpdated: 2, deltaLive: 4, deltaDead: 2, changedTuples: 4, insSinceVacuum: 4}
	if got != want {
		t.Fatalf("truncate abort: got %+v want %+v", got, want)
	}
}

// TestRelStatsReportVacuumAnalyze verifies pgstat_report_vacuum /
// pgstat_report_analyze: a successful VACUUM overwrites the shared live/dead
// estimates with the pass's measured values, zeroes ins_since_vacuum and bumps
// vacuum_count; ANALYZE zeroes mod_since_analyze. Pending deltas keep applying
// on top of the report's absolute values at the next flush.
func TestRelStatsReportVacuumAnalyze(t *testing.T) {
	m := newRelationStatsManager()
	const oid = uint32(29)
	m.recordInsert(1, oid, 5)
	m.recordUpdate(1, oid, 2)
	m.commitXact(1)
	m.flush(1)
	// Baseline: live 5, dead 2, ins_since_vacuum 5, mod_since_analyze 7.
	m.reportVacuum(oid, 5, 0)
	got, _ := m.get(oid)
	if got.deltaLive != 5 || got.deltaDead != 0 || got.insSinceVacuum != 0 || got.vacuumCount != 1 {
		t.Fatalf("after reportVacuum: got %+v want live=5 dead=0 insvac=0 vac=1", got)
	}
	// Post-vacuum DML accumulates on top of the reported absolutes.
	m.recordInsert(1, oid, 1)
	m.commitXact(1)
	m.flush(1)
	got, _ = m.get(oid)
	if got.deltaLive != 6 || got.changedTuples != 8 || got.insSinceVacuum != 1 {
		t.Fatalf("post-vacuum flush: got %+v want live=6 mod=8 insvac=1", got)
	}
	m.reportAnalyze(oid)
	got, _ = m.get(oid)
	if got.changedTuples != 0 || got.deltaLive != 6 || got.vacuumCount != 1 {
		t.Fatalf("after reportAnalyze: got %+v want mod=0 live=6 vac=1", got)
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
