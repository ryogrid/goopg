package executor

// pgstat_relations.go — cumulative per-relation (table) statistics
// (M0118-0009 `stats` spec, rung 6; design 0118-0128).
//
// This mirrors the function-statistics two-tier shape in pgstat_functions.go,
// modelling PostgreSQL's per-relation cumulative counters (pgstat_relation.c):
//
//   - numscans / tuples_returned — incremented as sequential scans run. In
//     PostgreSQL these are NON-transactional: they are accumulated in the
//     backend's PgStat_TableStatus.t_counts and flushed to shared memory at
//     pgstat_report_stat regardless of whether the surrounding transaction
//     commits or aborts.
//   - tuples_inserted / _updated / _deleted and the live/dead-tuple deltas —
//     in PostgreSQL these are transactional (PgStat_TableXactStatus), moved
//     into t_counts only at AtEOXact_PgStat. goopg's simple-query path commits
//     each autocommit statement immediately, so this rung records them into the
//     backend-local pending counters at DML time (equivalent to "applied at
//     commit" for a statement that succeeds). Full transactional discard on
//     mid-statement abort and the 2PC PREPARE/COMMIT-PREPARED counter handoff
//     are a later rung — so the `stats` spec stays `defer`.
//
// As with function stats, goopg has no separate statistics-collector process:
// a single process-global manager holds the shared (flushed) counters, and
// each session accumulates pending counters that pg_stat_force_next_flush()
// merges into the shared store. The pg_stat_get_numscans / _tuples_returned /
// _tuples_inserted / _tuples_updated / _tuples_deleted / _live_tuples /
// _dead_tuples getters read the shared store. Unlike the function-stats getters
// (which return SQL NULL when no stats exist), the relation-stats getters return
// 0 for an absent OID — matching PostgreSQL, where pg_stat_get_numscans of a
// dropped relation reads 0, not NULL.

import (
	"sync"

	"github.com/goopg/goopg/internal/catalog"
)

// relStatCounters holds the cumulative counters for one relation OID.
type relStatCounters struct {
	numScans       int64
	tuplesReturned int64 // visible tuples read by sequential scans
	tuplesInserted int64
	tuplesUpdated  int64
	tuplesDeleted  int64
	deltaLive      int64 // accumulated live-tuple delta (inserted - deleted)
	deltaDead      int64 // accumulated dead-tuple delta (updated + deleted)
}

// relationStatsManager is the process-global cumulative relation-stats store.
type relationStatsManager struct {
	mu      sync.Mutex
	shared  map[uint32]*relStatCounters            // flushed, cluster-global
	pending map[uint64]map[uint32]*relStatCounters // per-session, not yet flushed
}

var relStats = &relationStatsManager{
	shared:  make(map[uint32]*relStatCounters),
	pending: make(map[uint64]map[uint32]*relStatCounters),
}

// pendingFor returns (creating if needed) the pending counter for a session+OID.
// Caller must hold m.mu.
func (m *relationStatsManager) pendingFor(sessionID uint64, oid uint32) *relStatCounters {
	sess := m.pending[sessionID]
	if sess == nil {
		sess = make(map[uint32]*relStatCounters)
		m.pending[sessionID] = sess
	}
	c := sess[oid]
	if c == nil {
		c = &relStatCounters{}
		sess[oid] = c
	}
	return c
}

// recordScan records one sequential scan over a relation that read `returned`
// visible tuples. Mirrors pgstat_count_heap_scan + per-tuple
// pgstat_count_heap_getnext.
func (m *relationStatsManager) recordScan(sessionID uint64, oid uint32, returned int64) {
	if oid == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.pendingFor(sessionID, oid)
	c.numScans++
	c.tuplesReturned += returned
}

// recordInsert records n inserted tuples (pgstat_count_heap_insert): each
// insert adds one live tuple.
func (m *relationStatsManager) recordInsert(sessionID uint64, oid uint32, n int64) {
	if oid == 0 || n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.pendingFor(sessionID, oid)
	c.tuplesInserted += n
	c.deltaLive += n
}

// recordUpdate records n updated tuples (pgstat_count_heap_update): goopg has no
// HOT update, so each update leaves one dead tuple behind and the live count is
// unchanged (one new version replaces one old version).
func (m *relationStatsManager) recordUpdate(sessionID uint64, oid uint32, n int64) {
	if oid == 0 || n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.pendingFor(sessionID, oid)
	c.tuplesUpdated += n
	c.deltaDead += n
}

// recordDelete records n deleted tuples (pgstat_count_heap_delete): each delete
// removes one live tuple and produces one dead tuple.
func (m *relationStatsManager) recordDelete(sessionID uint64, oid uint32, n int64) {
	if oid == 0 || n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.pendingFor(sessionID, oid)
	c.tuplesDeleted += n
	c.deltaDead += n
	c.deltaLive -= n
}

// flush merges a session's pending counters into the shared store and clears the
// pending set. Mirrors pgstat_report_stat flushing table stats; the `stats` spec
// triggers it via pg_stat_force_next_flush().
func (m *relationStatsManager) flush(sessionID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.pending[sessionID]
	if sess == nil {
		return
	}
	for oid, c := range sess {
		s := m.shared[oid]
		if s == nil {
			s = &relStatCounters{}
			m.shared[oid] = s
		}
		s.numScans += c.numScans
		s.tuplesReturned += c.tuplesReturned
		s.tuplesInserted += c.tuplesInserted
		s.tuplesUpdated += c.tuplesUpdated
		s.tuplesDeleted += c.tuplesDeleted
		s.deltaLive += c.deltaLive
		s.deltaDead += c.deltaDead
	}
	delete(m.pending, sessionID)
}

// get returns a copy of the shared counters for an OID. The bool is false when
// no flushed stats exist; the relation getters then return 0 (not NULL).
func (m *relationStatsManager) get(oid uint32) (relStatCounters, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.shared[oid]
	if !ok {
		return relStatCounters{}, false
	}
	return *c, true
}

// dropTable removes all cumulative statistics for a relation OID — both the
// shared (flushed) counters and any not-yet-flushed pending counters across
// every session. Mirrors pgstat_drop_relation (called when DROP TABLE removes
// the relation): after the drop the getters read 0 and a concurrent backend's
// stale pending counts for the OID are discarded rather than revived on its next
// flush. M0118-0009 (`stats`).
func (m *relationStatsManager) dropTable(oid uint32) {
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

// resetAll zeroes every shared relation counter in place (the relation-stats
// portion of pg_stat_reset). Existing entries are kept and zeroed.
func (m *relationStatsManager) resetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.shared {
		*c = relStatCounters{}
	}
}

// shouldTrackCounts reports whether the calling session's track_counts GUC
// enables cumulative relation-stat collection. Default is 'on' (PG's BootVal),
// so an unset or unreadable GUC tracks. Mirrors the pgstat_track_counts guard
// around the pgstat_count_heap_* macros.
func shouldTrackCounts(ctx *Context) bool {
	if ctx == nil || ctx.GetSetting == nil {
		return true
	}
	v, ok := ctx.GetSetting("track_counts")
	if !ok {
		return true
	}
	return v != "off"
}

// recordRelScan is the shared seq-scan accounting helper used by seqScanOp and
// the UPDATE/DELETE scanMatching loop: it records one scan reading `returned`
// visible tuples for `oid`, gated by track_counts. A zero OID or stats-disabled
// session is a no-op.
func recordRelScan(ctx *Context, oid uint32, returned int64) {
	if oid == 0 || !shouldTrackCounts(ctx) {
		return
	}
	relStats.recordScan(sessionStatsID(ctx), oid, returned)
}

// tableOIDFromCatalog resolves a catalog.Table's OID, or 0 when unavailable.
func tableOIDFromCatalog(t *catalog.Table) uint32 {
	if t == nil {
		return 0
	}
	return t.OID
}
