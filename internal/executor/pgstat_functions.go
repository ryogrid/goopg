package executor

// pgstat_functions.go — cumulative per-function call statistics
// (M0118-0009 `stats` spec, rung 2; design 0118-0124).
//
// PostgreSQL's cumulative statistics subsystem (pgstat) tracks, per function
// OID, the number of calls and the total / self execution time. Backends
// accumulate these in backend-local *pending* state during execution and
// periodically flush them into shared memory (pgstat_report_stat, rate-limited
// to once per PGSTAT_MIN_INTERVAL). The getters pg_stat_get_function_calls /
// _total_time / _self_time read the *shared* (flushed) counters, so a value is
// not visible to other backends — or even re-read in the same backend — until a
// flush has occurred. The `stats` isolation spec forces a flush between mutating
// and observing steps via pg_stat_force_next_flush().
//
// goopg has no separate statistics-collector process. This file models the same
// two-tier shape with a single process-global manager (the goopg server is one
// OS process per cluster, and each isolation-spec run spawns a fresh server, so
// the global store starts empty per cluster — the same lifecycle as a freshly
// initdb'd PostgreSQL cluster):
//
//   - pending[sessionID][oid] — backend-local, incremented on every tracked
//     function invocation (gated by the calling session's track_functions GUC).
//   - shared[oid]            — cluster-global, merged from a session's pending
//     set when that session calls pg_stat_force_next_flush(). The getters read
//     here.
//
// This rung covers the function-statistics half of the spec (call counts +
// total/self time + reset). Relation tuple stats, SLRU stats, snapshot
// consistency caching, drop-revival protection, and 2PC interaction are later
// rungs — so the `stats` spec stays `defer`.

import (
	"sync"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// funcStatCounters holds the cumulative counters for one function OID.
type funcStatCounters struct {
	calls     int64
	totalTime time.Duration // wall time including nested tracked calls
	selfTime  time.Duration // wall time excluding nested tracked calls
}

// functionStatsManager is the process-global cumulative function-stats store.
type functionStatsManager struct {
	mu      sync.Mutex
	shared  map[uint32]*funcStatCounters            // flushed, cluster-global
	pending map[uint64]map[uint32]*funcStatCounters // per-session, not yet flushed
}

var funcStats = &functionStatsManager{
	shared:  make(map[uint32]*funcStatCounters),
	pending: make(map[uint64]map[uint32]*funcStatCounters),
}

// record adds one tracked call (with its total/self time) to the session's
// pending counters. Mirrors pgstat_init_function_usage / pgstat_end_function_usage.
func (m *functionStatsManager) record(sessionID uint64, oid uint32, total, self time.Duration) {
	if oid == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.pending[sessionID]
	if sess == nil {
		sess = make(map[uint32]*funcStatCounters)
		m.pending[sessionID] = sess
	}
	c := sess[oid]
	if c == nil {
		c = &funcStatCounters{}
		sess[oid] = c
	}
	c.calls++
	c.totalTime += total
	c.selfTime += self
}

// flush merges a session's pending counters into the shared store and clears
// the pending set. Mirrors pgstat_report_stat flushing function stats; the
// `stats` spec triggers this via pg_stat_force_next_flush().
func (m *functionStatsManager) flush(sessionID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.pending[sessionID]
	if sess == nil {
		return
	}
	for oid, c := range sess {
		s := m.shared[oid]
		if s == nil {
			s = &funcStatCounters{}
			m.shared[oid] = s
		}
		s.calls += c.calls
		s.totalTime += c.totalTime
		s.selfTime += c.selfTime
	}
	delete(m.pending, sessionID)
}

// get returns a copy of the shared counters for an OID, or (nil,false) when no
// flushed stats exist for it (the getters then return SQL NULL).
func (m *functionStatsManager) get(oid uint32) (funcStatCounters, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.shared[oid]
	if !ok {
		return funcStatCounters{}, false
	}
	return *c, true
}

// resetSingle drops the shared counters for one function OID
// (pg_stat_reset_single_function_counters). A non-existent OID is a no-op.
func (m *functionStatsManager) resetSingle(oid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.shared, oid)
}

// resetAll drops every shared function counter (the function-stats portion of
// pg_stat_reset, which resets all stats for the current database).
func (m *functionStatsManager) resetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shared = make(map[uint32]*funcStatCounters)
}

// sessionStatsID returns the stable per-connection identity used to key pending
// function stats. Zero means "no session identity" (internal/test contexts) —
// such calls still accumulate under the shared bucket 0 and flush correctly.
func sessionStatsID(ctx *Context) uint64 {
	if ctx == nil {
		return 0
	}
	if s, ok := ctx.AdvisorySessionIdentity.(sessionUniqueIDer); ok && s != nil {
		return s.UniqueID()
	}
	return 0
}

// statFuncOIDArg evaluates the single OID argument of a pg_stat_get_function_*
// getter. It returns ok=false when the argument evaluates to SQL NULL (the
// getter then returns NULL), and an error only when argument evaluation itself
// fails.
func statFuncOIDArg(x *planner.FuncCall, row Row, ctx *Context) (uint32, bool, error) {
	if len(x.Args) == 0 {
		return 0, false, nil
	}
	d, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return 0, false, err
	}
	if d.IsNull() {
		return 0, false, nil
	}
	return uint32(d.Int), true, nil
}

// shouldTrackFunction reports whether the calling session's track_functions GUC
// enables collection for the given routine. 'all' tracks every language; 'pl'
// tracks only procedural-language functions (not SQL or internal/C); 'none'
// (the default) tracks nothing. Mirrors pgstat_init_function_usage's
// pgstat_track_functions check. Default 'none' keeps the hot path
// zero-overhead.
func shouldTrackFunction(ctx *Context, r *catalog.Routine) bool {
	if ctx == nil || ctx.GetSetting == nil || r == nil {
		return false
	}
	v, ok := ctx.GetSetting("track_functions")
	if !ok {
		return false
	}
	switch v {
	case "all":
		return true
	case "pl":
		switch r.Language {
		case "sql", "internal", "c":
			return false
		default:
			return true
		}
	default:
		return false
	}
}
