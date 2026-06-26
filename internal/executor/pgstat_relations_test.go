package executor

import "testing"

// TestRelStatsAccumulateFlushGet verifies the two-tier relation-stats model:
// per-session pending counters accumulate independently and only become visible
// in the shared store after a flush, mirroring PostgreSQL's pgstat report cycle.
// M0118-0009 (`stats`, rung 6; design 0118-0128).
func TestRelStatsAccumulateFlushGet(t *testing.T) {
	m := &relationStatsManager{
		shared:  make(map[uint32]*relStatCounters),
		pending: make(map[uint64]map[uint32]*relStatCounters),
	}
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
	m := &relationStatsManager{
		shared:  make(map[uint32]*relStatCounters),
		pending: make(map[uint64]map[uint32]*relStatCounters),
	}
	const oid = uint32(7)
	m.recordInsert(1, oid, 1) // live +1
	m.recordUpdate(1, oid, 2) // dead +2, live unchanged
	m.flush(1)
	got, _ := m.get(oid)
	if got.deltaLive != 1 || got.deltaDead != 2 || got.tuplesUpdated != 2 {
		t.Fatalf("update deltas: got %+v want live=1 dead=2 upd=2", got)
	}
}

// TestRelStatsDropTable verifies DROP removes both shared and pending counters
// so a getter on the dropped OID reads 0, and a concurrent backend's stale
// pending counts are not revived on its next flush (pgstat_drop_relation).
func TestRelStatsDropTable(t *testing.T) {
	m := &relationStatsManager{
		shared:  make(map[uint32]*relStatCounters),
		pending: make(map[uint64]map[uint32]*relStatCounters),
	}
	const oid = uint32(99)
	m.recordInsert(1, oid, 5)
	m.flush(1)
	m.recordScan(2, oid, 4) // session 2 has stale pending after the drop

	m.dropTable(oid)
	if _, ok := m.get(oid); ok {
		t.Fatalf("dropTable: shared entry survived")
	}
	// A flush of the stale pending must not revive the dropped OID.
	m.flush(2)
	if _, ok := m.get(oid); ok {
		t.Fatalf("dropTable: stale pending revived the dropped OID on flush")
	}
}
