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
	"github.com/goopg/goopg/internal/optimizer"
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

// copyAll returns a by-value copy of every shared function counter, used to
// build a 'snapshot'-consistency transaction snapshot (all objects frozen at the
// first access). Mirrors pgstat_build_snapshot copying the whole shared store.
func (m *functionStatsManager) copyAll() map[uint32]funcStatCounters {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[uint32]funcStatCounters, len(m.shared))
	for oid, c := range m.shared {
		out[oid] = *c
	}
	return out
}

// cachedFuncStat is one object's cached read inside a 'cache'-consistency
// transaction snapshot — including its presence flag, so a cached absence keeps
// reading SQL NULL for the rest of the transaction.
type cachedFuncStat struct {
	c     funcStatCounters
	found bool
}

// funcStatSnapshot caches cumulative function-stat reads for the duration of one
// explicit transaction, implementing stats_fetch_consistency = 'cache'/'snapshot'.
// Mirrors PG's per-transaction stats snapshot: a 'cache' snapshot fills lazily,
// per object, on that object's first access; a 'snapshot' snapshot copies the
// whole shared store at the first access to any object. Both are discarded at
// end of transaction (AtEOXact_PgStat) or by pg_stat_clear_snapshot(). The
// distinction only matters across statements, which is why a snapshot is built
// only inside an explicit transaction — within a single autocommit statement no
// concurrent flush can intervene, so a live read is already consistent.
// M0118-0009 (`stats`).
type funcStatSnapshot struct {
	full       bool                        // a 'snapshot'-mode full copy has been taken
	allFlushed map[uint32]funcStatCounters // entire shared store frozen at first access (snapshot mode)
	perObject  map[uint32]cachedFuncStat   // lazily cached single-object reads (cache mode)
	// SLRU fixed-amount stats participate in the same per-transaction snapshot.
	// slruFrozen is captured together with allFlushed when the 'snapshot'-mode
	// full copy is taken (so a variable-amount access freezes SLRU too, and vice
	// versa); slruCache holds the whole SLRU set cached on its first access under
	// 'cache' mode (fixed-amount stats are cached as one unit, independent of
	// function objects). M0118-0009 (`stats`, SLRU rung).
	slruFrozen map[string]slruCounters // snapshot mode: SLRU frozen with the full copy
	slruCache  map[string]slruCounters // cache mode: SLRU cached on first SLRU access
}

// ensureFullSnapshot builds the 'snapshot'-mode whole-store copy on the first
// access to any cumulative statistic in the transaction, capturing BOTH the
// function-stats store and the fixed-amount SLRU store at the same instant.
// Mirrors pgstat_build_snapshot copying every stats kind at once, so a later
// access to a different kind reads its frozen value. Idempotent. M0118-0009.
func ensureFullSnapshot(snap *funcStatSnapshot) {
	if snap.full {
		return
	}
	snap.allFlushed = funcStats.copyAll()
	snap.slruFrozen = slruStats.snapshotAll()
	snap.full = true
}

// fetchFuncStat reads the cumulative counters for a function OID honouring the
// session's stats_fetch_consistency setting. Outside an explicit transaction (or
// with consistency 'none') it reads the live shared store directly. Inside an
// explicit transaction it consults/populates the per-transaction snapshot so a
// given object reads stably for the rest of the transaction ('cache'), or every
// object freezes at the first access ('snapshot'). M0118-0009 (`stats`).
func fetchFuncStat(ctx *Context, oid uint32) (funcStatCounters, bool) {
	sess, _ := ctx.Session.(*BasicSession)
	if sess == nil || !sess.InExplicitTransaction() {
		return funcStats.get(oid)
	}
	mode := "none"
	if ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("stats_fetch_consistency"); ok {
			mode = v
		}
	}
	switch mode {
	case "cache":
		snap := sess.ensureStatsSnapshot()
		if e, ok := snap.perObject[oid]; ok {
			return e.c, e.found
		}
		c, found := funcStats.get(oid)
		snap.perObject[oid] = cachedFuncStat{c: c, found: found}
		return c, found
	case "snapshot":
		snap := sess.ensureStatsSnapshot()
		ensureFullSnapshot(snap)
		c, ok := snap.allFlushed[oid]
		return c, ok
	default: // "none"
		return funcStats.get(oid)
	}
}

// dropFunction removes all cumulative statistics for a function OID — both the
// shared (flushed) counters and any not-yet-flushed pending counters across
// every session. Mirrors pgstat_drop_function (called transactionally when a
// DROP FUNCTION commits): after the drop, the getters return SQL NULL and a
// concurrent backend's stale pending counts for the OID are discarded rather
// than revived into the shared store on its next flush. M0118-0009 (`stats`).
func (m *functionStatsManager) dropFunction(oid uint32) {
	if oid == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.shared, oid)
	for _, sess := range m.pending {
		delete(sess, oid)
	}
}

// resetSingle zeroes the shared counters for one function OID in place
// (pg_stat_reset_single_function_counters). The entry is KEPT (calls reset to 0,
// total/self time to 0), so a subsequent getter returns 0 rather than SQL NULL —
// PG resets the shared entry's counters without removing it. An OID with no
// flushed entry is a no-op (the getter keeps returning NULL): PG's reset does
// not materialise a zeroed entry for a function that has none.
func (m *functionStatsManager) resetSingle(oid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.shared[oid]; ok {
		c.calls = 0
		c.totalTime = 0
		c.selfTime = 0
	}
}

// resetAll zeroes every shared function counter in place (the function-stats
// portion of pg_stat_reset, which resets all stats for the current database).
// Like resetSingle, existing entries are KEPT and zeroed — a subsequent getter
// returns 0, not SQL NULL — so a function that already had flushed stats reads
// back as 0 calls after the reset.
func (m *functionStatsManager) resetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.shared {
		c.calls = 0
		c.totalTime = 0
		c.selfTime = 0
	}
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
func statFuncOIDArg(x *optimizer.FuncCall, row Row, ctx *Context) (uint32, bool, error) {
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
