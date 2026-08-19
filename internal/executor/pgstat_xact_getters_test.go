package executor

import (
	"testing"
	"time"
)

// TestFunctionStatsPeekPendingNonAllocating exercises the read-only peek
// accessor added for pg_stat_get_xact_function_calls: an untouched OID reports
// not-found and leaves the pending maps completely untouched (no session or
// OID entry materialised), matching PG's find_funcstat_entry pure lookup. An
// OID with pending calls in the SAME session reports them; a different
// session's pending calls are not visible. M0134-0020.
func TestFunctionStatsPeekPendingNonAllocating(t *testing.T) {
	m := &functionStatsManager{
		shared:  make(map[uint32]*funcStatCounters),
		pending: make(map[uint64]map[uint32]*funcStatCounters),
	}
	const (
		sess1 = uint64(1)
		sess2 = uint64(2)
		oidA  = uint32(500001)
	)

	// Peeking an untouched OID must not allocate a session or OID entry.
	if _, found := m.peekPending(sess1, oidA); found {
		t.Fatalf("peekPending on untouched OID must report not-found")
	}
	if len(m.pending) != 0 {
		t.Fatalf("peekPending must not allocate a session entry, len(pending)=%d", len(m.pending))
	}

	// Record two calls in sess1; peek from sess1 sees them, sess2 does not.
	m.record(sess1, oidA, time.Millisecond, time.Millisecond)
	m.record(sess1, oidA, time.Millisecond, time.Millisecond)
	c, found := m.peekPending(sess1, oidA)
	if !found || c.calls != 2 {
		t.Fatalf("peekPending(sess1, oidA) want found=true calls=2, got found=%v calls=%d", found, c.calls)
	}
	if _, found := m.peekPending(sess2, oidA); found {
		t.Fatalf("peekPending(sess2, oidA) must not see sess1's pending calls")
	}
	// sess2 was never accessed for writes; peeking it must not allocate either.
	if _, ok := m.pending[sess2]; ok {
		t.Fatalf("peekPending(sess2, ...) must not allocate a pending entry for sess2")
	}

	// A different, untouched OID in sess1 still reports not-found (no entry
	// bleed within the session map).
	if _, found := m.peekPending(sess1, uint32(500002)); found {
		t.Fatalf("peekPending on a different untouched OID must report not-found")
	}
}

// TestPgStatGetXactFunctionCallsSQL drives pg_stat_get_xact_function_calls
// through the real evaluator: NULL for an untouched OID; after N pending calls
// in the current session (not yet flushed to the shared store) it returns N —
// visible before any flush, since the getter reads the pending tier directly.
// PG: pgstatfuncs.c:1804 (find_funcstat_entry → PG_RETURN_NULL()). M0134-0020.
func TestPgStatGetXactFunctionCallsSQL(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	const oid = uint32(500101)
	funcStats.dropFunction(oid)
	t.Cleanup(func() { funcStats.dropFunction(oid) })

	// Untouched OID: NULL, not 0, not an error.
	rows := runQueryRows(t, ctx, "SELECT pg_stat_get_xact_function_calls(500101)")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if !rows[0][0].IsNull() {
		t.Fatalf("untouched OID: want NULL, got %q", rows[0][0].Format())
	}

	// Record 3 pending calls in this session's (sessionStatsID(ctx) == 0)
	// bucket directly — no flush, mirroring calls made earlier in the same
	// open transaction.
	funcStats.record(sessionStatsID(ctx), oid, time.Millisecond, time.Millisecond)
	funcStats.record(sessionStatsID(ctx), oid, time.Millisecond, time.Millisecond)
	funcStats.record(sessionStatsID(ctx), oid, time.Millisecond, time.Millisecond)

	rows = runQueryRows(t, ctx, "SELECT pg_stat_get_xact_function_calls(500101)")
	if len(rows) != 1 || rows[0][0].IsNull() || rows[0][0].Format() != "3" {
		t.Fatalf("after 3 pending calls: want 3, got %q (null=%v)", rows[0][0].Format(), rows[0][0].IsNull())
	}

	// It must be visible BEFORE any flush: the shared store still has nothing.
	if _, ok := funcStats.get(oid); ok {
		t.Fatalf("shared store must still be empty before flush")
	}

	// A NULL argument returns NULL, not an error.
	rows = runQueryRows(t, ctx, "SELECT pg_stat_get_xact_function_calls(NULL)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Fatalf("NULL arg: want NULL, got %q", rows[0][0].Format())
	}
}

// TestRelationStatsPeekStagingNonAllocating exercises the read-only peek
// accessor added for pg_stat_get_xact_tuples_inserted: an untouched OID
// reports not-found and leaves the staging maps completely untouched (no
// session or OID entry materialised), matching PG's find_tabstat_entry pure
// lookup. An OID staged with inserts in the SAME session reports them. M0134-0020.
func TestRelationStatsPeekStagingNonAllocating(t *testing.T) {
	m := newRelationStatsManager()
	const (
		sess1 = uint64(11)
		sess2 = uint64(12)
		oidA  = uint32(600001)
	)

	if _, found := m.peekStaging(sess1, oidA); found {
		t.Fatalf("peekStaging on untouched OID must report not-found")
	}
	if len(m.staging) != 0 {
		t.Fatalf("peekStaging must not allocate a session entry, len(staging)=%d", len(m.staging))
	}

	m.recordInsert(sess1, oidA, 5)
	m.recordInsert(sess1, oidA, 2)
	c, found := m.peekStaging(sess1, oidA)
	if !found || c.tuplesInserted != 7 {
		t.Fatalf("peekStaging(sess1, oidA) want found=true inserted=7, got found=%v inserted=%d", found, c.tuplesInserted)
	}
	if _, found := m.peekStaging(sess2, oidA); found {
		t.Fatalf("peekStaging(sess2, oidA) must not see sess1's staged inserts")
	}
	if _, ok := m.staging[sess2]; ok {
		t.Fatalf("peekStaging(sess2, ...) must not allocate a staging entry for sess2")
	}
}

// TestPgStatGetXactTuplesInsertedSQL drives pg_stat_get_xact_tuples_inserted
// through the real evaluator: 0 (never NULL) for an untouched OID; after N
// staged inserts in the current session's open transaction it returns N,
// visible BEFORE COMMIT (before the staging tier folds into pending). PG:
// PG_STAT_GET_XACT_RELENTRY_INT64 macro, pgstatfuncs.c:1758 (instantiated
// :1796) — find_tabstat_entry == NULL → result = 0. M0134-0020.
func TestPgStatGetXactTuplesInsertedSQL(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	const oid = uint32(600101)

	// Untouched OID: 0, never NULL.
	rows := runQueryRows(t, ctx, "SELECT pg_stat_get_xact_tuples_inserted(600101)")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0][0].IsNull() || rows[0][0].Format() != "0" {
		t.Fatalf("untouched OID: want 0 (not NULL), got %q (null=%v)", rows[0][0].Format(), rows[0][0].IsNull())
	}

	// Stage 4 inserts in this session's current transaction, no commit.
	relStats.recordInsert(sessionStatsID(ctx), oid, 4)

	rows = runQueryRows(t, ctx, "SELECT pg_stat_get_xact_tuples_inserted(600101)")
	if len(rows) != 1 || rows[0][0].IsNull() || rows[0][0].Format() != "4" {
		t.Fatalf("after 4 staged inserts: want 4, got %q (null=%v)", rows[0][0].Format(), rows[0][0].IsNull())
	}

	// Visible BEFORE commit: the pending (post-fold) store still has nothing.
	if c, _ := relStats.get(oid); c.tuplesInserted != 0 {
		t.Fatalf("shared store must still read 0 before commit, got %d", c.tuplesInserted)
	}

	// A NULL argument returns NULL (not 0).
	rows = runQueryRows(t, ctx, "SELECT pg_stat_get_xact_tuples_inserted(NULL)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Fatalf("NULL arg: want NULL, got %q", rows[0][0].Format())
	}
}
