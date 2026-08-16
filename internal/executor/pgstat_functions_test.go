package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
)

// TestFunctionStatsManager exercises the two-tier (pending → shared) cumulative
// function-statistics store: pending counters only become visible via the
// getters after a flush, flushes accumulate across calls, and the reset
// operations clear the shared store. Design 0118-0124 (M0118-0009 stats rung 2).
func TestFunctionStatsManager(t *testing.T) {
	m := &functionStatsManager{
		shared:  make(map[uint32]*funcStatCounters),
		pending: make(map[uint64]map[uint32]*funcStatCounters),
	}
	const (
		sess1 = uint64(1)
		sess2 = uint64(2)
		oidA  = uint32(131072)
		oidB  = uint32(131073)
	)

	// Pending stats are NOT visible until flushed.
	m.record(sess1, oidA, time.Millisecond, time.Millisecond)
	m.record(sess1, oidA, time.Millisecond, time.Millisecond)
	if _, ok := m.get(oidA); ok {
		t.Fatalf("oidA visible before flush: pending must not leak into shared")
	}

	// After flush, the two calls are visible with positive time.
	m.flush(sess1)
	c, ok := m.get(oidA)
	if !ok || c.calls != 2 {
		t.Fatalf("after flush want calls=2 got ok=%v calls=%d", ok, c.calls)
	}
	if c.totalTime <= 0 || c.selfTime <= 0 {
		t.Fatalf("want positive total/self time, got total=%v self=%v", c.totalTime, c.selfTime)
	}

	// A second session's flush accumulates onto the same shared OID.
	m.record(sess2, oidA, time.Millisecond, time.Millisecond)
	m.flush(sess2)
	if c, _ := m.get(oidA); c.calls != 3 {
		t.Fatalf("cross-session accumulate: want calls=3 got %d", c.calls)
	}

	// resetSingle drops only the targeted OID.
	// resetSingle ZEROES the targeted OID's entry in place (kept, not removed) so
	// the getter returns 0 rather than NULL — PG's
	// pg_stat_reset_single_function_counters resets the shared counters without
	// dropping the entry. Other OIDs are untouched.
	m.record(sess1, oidB, time.Millisecond, time.Millisecond)
	m.flush(sess1)
	m.resetSingle(oidA)
	if c, ok := m.get(oidA); !ok || c.calls != 0 || c.totalTime != 0 || c.selfTime != 0 {
		t.Fatalf("after resetSingle oidA must be present and zeroed, got ok=%v %+v", ok, c)
	}
	if c, ok := m.get(oidB); !ok || c.calls != 1 {
		t.Fatalf("oidB should survive resetSingle of oidA, got ok=%v calls=%d", ok, c.calls)
	}
	// resetSingle on an OID with no entry is a no-op (stays NULL).
	m.resetSingle(uint32(999999))
	if _, ok := m.get(uint32(999999)); ok {
		t.Fatalf("resetSingle must not materialise an entry for an unseen OID")
	}

	// resetAll zeroes every entry in place (kept, not removed).
	m.resetAll()
	if c, ok := m.get(oidB); !ok || c.calls != 0 {
		t.Fatalf("after resetAll oidB must be present and zeroed, got ok=%v calls=%d", ok, c.calls)
	}

	// dropFunction removes the OID from BOTH shared and every session's pending,
	// so the getter returns NULL and a stale pending count is not revived on a
	// later flush. Mirrors pgstat_drop_function on DROP FUNCTION commit.
	m.record(sess1, oidB, time.Millisecond, time.Millisecond)
	m.flush(sess1)                                            // shared[oidB] exists
	m.record(sess2, oidB, time.Millisecond, time.Millisecond) // sess2 pending for oidB
	m.dropFunction(oidB)
	if _, ok := m.get(oidB); ok {
		t.Fatalf("oidB must be gone after dropFunction")
	}
	m.flush(sess2) // the dropped OID's stale pending must not revive it
	if _, ok := m.get(oidB); ok {
		t.Fatalf("dropFunction must discard pending so a later flush does not revive the OID")
	}

	// A non-existent session flush is a no-op (no panic).
	m.flush(uint64(999))
	// OID 0 is never recorded.
	m.record(sess1, 0, time.Millisecond, time.Millisecond)
	m.flush(sess1)
	if _, ok := m.get(0); ok {
		t.Fatalf("OID 0 must never be recorded")
	}
}

// TestFetchFuncStatConsistency exercises stats_fetch_consistency = none/cache/
// snapshot through fetchFuncStat, mirroring the `stats` isolation spec's
// snapshot-consistency permutations:
//   - none:     each read sees the latest flushed value (no caching).
//   - cache:    a given object freezes at its first in-transaction read, but a
//               different object accessed later still reads live.
//   - snapshot: the first read freezes ALL objects, so a different object read
//               later returns its value as of that first access.
// The snapshot lives only inside an explicit transaction and is discarded at
// transaction end. M0118-0009 (`stats`).
func TestFetchFuncStatConsistency(t *testing.T) {
	const (
		oidA = uint32(300001)
		oidB = uint32(300002)
	)
	// Use the package-global store; clean up the test OIDs afterward so we don't
	// pollute sibling tests that read the same global.
	funcStats.dropFunction(oidA)
	funcStats.dropFunction(oidB)
	t.Cleanup(func() {
		funcStats.dropFunction(oidA)
		funcStats.dropFunction(oidB)
	})

	bump := func(oid uint32) { // record one tracked call and flush it to shared
		funcStats.record(0, oid, time.Millisecond, time.Millisecond)
		funcStats.flush(0)
	}

	mode := "none"
	sess := NewBasicSession()
	ctx := &Context{
		Session: sess,
		GetSetting: func(name string) (string, bool) {
			if name == "stats_fetch_consistency" {
				return mode, true
			}
			return "", false
		},
	}
	calls := func(oid uint32) (int64, bool) {
		c, ok := fetchFuncStat(ctx, oid)
		return c.calls, ok
	}

	// Seed: A=1, B=1 in shared.
	bump(oidA)
	bump(oidB)

	// none, outside a transaction: live read sees subsequent flushes.
	mode = "none"
	if n, ok := calls(oidA); !ok || n != 1 {
		t.Fatalf("none: want A=1 got ok=%v n=%d", ok, n)
	}
	bump(oidA) // A=2
	if n, _ := calls(oidA); n != 2 {
		t.Fatalf("none: want live A=2 after flush, got %d", n)
	}

	// cache, inside an explicit transaction: A freezes at first read; B read
	// later still sees live.
	sess.BeginExplicitTransaction(transam.Transaction{}, transam.Snapshot{})
	mode = "cache"
	if n, _ := calls(oidA); n != 2 { // first in-txn read of A → caches 2
		t.Fatalf("cache: want A=2 at first read, got %d", n)
	}
	bump(oidA) // A=3 (concurrent flush)
	bump(oidB) // B=2
	if n, _ := calls(oidA); n != 2 {
		t.Fatalf("cache: A must stay cached at 2, got %d", n)
	}
	if n, _ := calls(oidB); n != 2 { // B first accessed now → live 2
		t.Fatalf("cache: B first access must be live 2, got %d", n)
	}
	sess.EndExplicitTransaction() // snapshot discarded

	// After commit, a fresh live read sees the latest (A=3).
	mode = "none"
	if n, _ := calls(oidA); n != 3 {
		t.Fatalf("cache: after EndExplicitTransaction want live A=3, got %d", n)
	}

	// snapshot, inside an explicit transaction: first read freezes A AND B; B
	// read later returns its value as of that first access, not the live value.
	sess.BeginExplicitTransaction(transam.Transaction{}, transam.Snapshot{})
	mode = "snapshot"
	if n, _ := calls(oidA); n != 3 { // first read snapshots everything (A=3, B=2)
		t.Fatalf("snapshot: want A=3 at first read, got %d", n)
	}
	bump(oidA) // A=4
	bump(oidB) // B=3
	if n, _ := calls(oidB); n != 2 { // B frozen at snapshot time (2), not live 3
		t.Fatalf("snapshot: B must be frozen at 2, got %d", n)
	}

	// pg_stat_clear_snapshot equivalent: clearing rebuilds against live values.
	sess.ClearStatsSnapshot()
	if n, _ := calls(oidB); n != 3 {
		t.Fatalf("snapshot: after clear want live B=3, got %d", n)
	}
	sess.EndExplicitTransaction()
}

// TestShouldTrackFunction verifies the track_functions GUC gating: 'none'
// (default) tracks nothing, 'all' tracks every language, 'pl' tracks only
// procedural languages (plpgsql) and not sql/internal/c.
func TestShouldTrackFunction(t *testing.T) {
	plpgsql := &catalog.Routine{Language: "plpgsql"}
	sqlFn := &catalog.Routine{Language: "sql"}

	mk := func(val string, present bool) *Context {
		return &Context{GetSetting: func(name string) (string, bool) {
			if name == "track_functions" {
				return val, present
			}
			return "", false
		}}
	}

	cases := []struct {
		name string
		ctx  *Context
		r    *catalog.Routine
		want bool
	}{
		{"none-plpgsql", mk("none", true), plpgsql, false},
		{"unset-plpgsql", mk("", false), plpgsql, false},
		{"all-plpgsql", mk("all", true), plpgsql, true},
		{"all-sql", mk("all", true), sqlFn, true},
		{"pl-plpgsql", mk("pl", true), plpgsql, true},
		{"pl-sql", mk("pl", true), sqlFn, false},
	}
	for _, tc := range cases {
		if got := shouldTrackFunction(tc.ctx, tc.r); got != tc.want {
			t.Errorf("%s: shouldTrackFunction = %v, want %v", tc.name, got, tc.want)
		}
	}
	// nil context / nil GetSetting never tracks.
	if shouldTrackFunction(nil, plpgsql) {
		t.Errorf("nil ctx should not track")
	}
	if shouldTrackFunction(&Context{}, plpgsql) {
		t.Errorf("nil GetSetting should not track")
	}
}
