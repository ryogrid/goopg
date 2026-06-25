package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
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
