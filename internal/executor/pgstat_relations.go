package executor

// pgstat_relations.go — cumulative per-relation (table) statistics
// (M0118-0009 `stats` spec; designs 0118-0128 rung 6, 0118-0131 rung 7).
//
// This mirrors PostgreSQL's per-relation cumulative counters (pgstat_relation.c).
// PostgreSQL splits a relation's counters into two classes:
//
//   - numscans / tuples_returned / tuples_fetched — NON-transactional. They are
//     accumulated in the backend's PgStat_TableStatus.t_counts as scans run and
//     flushed to shared memory at pgstat_report_stat regardless of whether the
//     surrounding transaction commits or aborts. goopg records these directly
//     into the backend-local `pending` counters at scan time.
//   - tuples_inserted / _updated / _deleted and the live/dead-tuple deltas —
//     TRANSACTIONAL. PostgreSQL accumulates them per (sub)transaction in
//     PgStat_TableXactStatus and only folds them into the backend-local
//     t_counts at AtEOXact_PgStat_Relations, applying different math on commit
//     vs abort (an aborted INSERT/UPDATE leaves dead tuples; an aborted DELETE
//     is a no-op on live/dead). goopg mirrors this with a third `staging` tier:
//     DML records into a per-session, per-transaction staging entry, which is
//     folded into `pending` at commit/abort. TRUNCATE (and in-transaction DROP)
//     set a truncdropped flag that, on commit, forgets all prior live/dead
//     counts. Two-phase commit moves the staged counters into a per-gid
//     `prepared` record at PREPARE; COMMIT/ROLLBACK PREPARED folds that record
//     into the *finalising* backend's pending counters (pgstat_twophase_post*).
//
// As with function stats, goopg has no separate statistics-collector process:
// a single process-global manager holds the shared (flushed) counters, each
// session accumulates pending counters that pg_stat_force_next_flush() merges
// into the shared store, and the staging tier sits in front of pending. The
// pg_stat_get_numscans / _tuples_* / _live_tuples / _dead_tuples getters read
// the shared store. Unlike the function-stats getters (which return SQL NULL
// when no stats exist), the relation-stats getters return 0 for an absent OID —
// matching PostgreSQL, where pg_stat_get_numscans of a dropped relation reads 0.

import (
	"sync"

	"github.com/goopg/goopg/internal/catalog"
)

// relStatCounters holds the cumulative counters for one relation OID. It is used
// both for the per-session pending counters and the cluster-global shared store.
type relStatCounters struct {
	numScans       int64
	tuplesReturned int64 // visible tuples read by sequential scans
	tuplesInserted int64
	tuplesUpdated  int64
	tuplesDeleted  int64
	deltaLive      int64 // accumulated live-tuple delta (inserted - deleted)
	deltaDead      int64 // accumulated dead-tuple delta (updated + deleted)
	// truncDropped is meaningful only on a pending entry: a committed TRUNCATE /
	// in-transaction DROP folded into this entry forgets all prior live/dead
	// counts. It is consumed (and reset) when the pending entry is flushed into
	// the shared store, where it resets the shared live/dead totals to zero
	// before adding this cycle's deltas (pgstat_relation_flush_cb).
	truncDropped bool
}

// relXactCounters holds the transactional counters staged for one relation OID
// within a single (sub)transaction, mirroring PgStat_TableXactStatus. It is
// folded into the pending counters at end of transaction (commit or abort).
type relXactCounters struct {
	tuplesInserted int64
	tuplesUpdated  int64
	tuplesDeleted  int64
	// truncDropped records that a TRUNCATE (or in-txn DROP) happened in this
	// transaction. The pre-truncdrop counts are saved so an abort can restore
	// them (restore_truncdrop_counters): an aborted TRUNCATE must not lose the
	// counts of the rows that existed before it.
	truncDropped         bool
	insertedPreTruncDrop int64
	updatedPreTruncDrop  int64
	deletedPreTruncDrop  int64
}

// relationStatsManager is the process-global cumulative relation-stats store.
type relationStatsManager struct {
	mu       sync.Mutex
	shared   map[uint32]*relStatCounters            // flushed, cluster-global
	pending  map[uint64]map[uint32]*relStatCounters // per-session, not yet flushed
	staging  map[uint64]map[uint32]*relXactCounters // per-session current-transaction
	prepared map[string]map[uint32]*relXactCounters // per-gid 2PC records
}

func newRelationStatsManager() *relationStatsManager {
	return &relationStatsManager{
		shared:   make(map[uint32]*relStatCounters),
		pending:  make(map[uint64]map[uint32]*relStatCounters),
		staging:  make(map[uint64]map[uint32]*relXactCounters),
		prepared: make(map[string]map[uint32]*relXactCounters),
	}
}

var relStats = newRelationStatsManager()

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

// stagingFor returns (creating if needed) the per-transaction staging counter
// for a session+OID. Caller must hold m.mu.
func (m *relationStatsManager) stagingFor(sessionID uint64, oid uint32) *relXactCounters {
	sess := m.staging[sessionID]
	if sess == nil {
		sess = make(map[uint32]*relXactCounters)
		m.staging[sessionID] = sess
	}
	c := sess[oid]
	if c == nil {
		c = &relXactCounters{}
		sess[oid] = c
	}
	return c
}

// recordScan records one sequential scan over a relation that read `returned`
// visible tuples. Mirrors pgstat_count_heap_scan + per-tuple
// pgstat_count_heap_getnext. Scans are NON-transactional, so this writes
// straight into the pending counters.
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

// recordInsert stages n inserted tuples for the current transaction
// (pgstat_count_heap_insert).
func (m *relationStatsManager) recordInsert(sessionID uint64, oid uint32, n int64) {
	if oid == 0 || n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stagingFor(sessionID, oid).tuplesInserted += n
}

// recordUpdate stages n updated tuples for the current transaction
// (pgstat_count_heap_update).
func (m *relationStatsManager) recordUpdate(sessionID uint64, oid uint32, n int64) {
	if oid == 0 || n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stagingFor(sessionID, oid).tuplesUpdated += n
}

// recordDelete stages n deleted tuples for the current transaction
// (pgstat_count_heap_delete).
func (m *relationStatsManager) recordDelete(sessionID uint64, oid uint32, n int64) {
	if oid == 0 || n == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stagingFor(sessionID, oid).tuplesDeleted += n
}

// recordTruncate stages a TRUNCATE (pgstat_count_truncate): it saves the
// pre-truncate insert/update/delete counts so an abort can restore them, marks
// the staging entry truncdropped, then resets the in-transaction counters so the
// post-truncate inserts/updates are counted afresh.
func (m *relationStatsManager) recordTruncate(sessionID uint64, oid uint32) {
	if oid == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.stagingFor(sessionID, oid)
	saveTruncDropCounters(x)
	x.tuplesInserted = 0
	x.tuplesUpdated = 0
	x.tuplesDeleted = 0
}

// saveTruncDropCounters mirrors save_truncdrop_counters: record the current
// tuple counts as the pre-truncate/drop values so an aborting transaction can
// restore them. Only saved on the first truncate/drop in this staging entry.
func saveTruncDropCounters(x *relXactCounters) {
	if x.truncDropped {
		return
	}
	x.insertedPreTruncDrop = x.tuplesInserted
	x.updatedPreTruncDrop = x.tuplesUpdated
	x.deletedPreTruncDrop = x.tuplesDeleted
	x.truncDropped = true
}

// applyXactToPending folds a staged transaction's counters into a pending
// counter, applying PostgreSQL's commit vs abort math
// (AtEOXact_PgStat_Relations / pgstat_twophase_post{commit,abort}). Caller must
// hold m.mu.
func applyXactToPending(c *relStatCounters, x *relXactCounters, isCommit bool) {
	// On abort, restore the counts obliterated by a truncate/drop so the
	// attempted-action totals reflect the pre-truncate work.
	if !isCommit && x.truncDropped {
		x.tuplesInserted = x.insertedPreTruncDrop
		x.tuplesUpdated = x.updatedPreTruncDrop
		x.tuplesDeleted = x.deletedPreTruncDrop
	}
	// Count attempted actions regardless of commit/abort.
	c.tuplesInserted += x.tuplesInserted
	c.tuplesUpdated += x.tuplesUpdated
	c.tuplesDeleted += x.tuplesDeleted
	if isCommit {
		if x.truncDropped {
			// Forget live/dead stats seen by the backend thus far; the flag
			// rides into the shared store at flush to forget already-flushed
			// totals too.
			c.truncDropped = true
			c.deltaLive = 0
			c.deltaDead = 0
		}
		// insert adds a live tuple, delete removes one; update and delete each
		// create a dead tuple.
		c.deltaLive += x.tuplesInserted - x.tuplesDeleted
		c.deltaDead += x.tuplesUpdated + x.tuplesDeleted
	} else {
		// Inserted (and updated) tuples are dead; deleted tuples are unaffected
		// (the delete never happened).
		c.deltaDead += x.tuplesInserted + x.tuplesUpdated
	}
}

// commitXact folds a session's staged transaction counters into its pending
// counters using commit math, then clears the staging set. Mirrors
// AtEOXact_PgStat_Relations(commit).
func (m *relationStatsManager) commitXact(sessionID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.foldStagingLocked(sessionID, true)
}

// abortXact folds a session's staged transaction counters into its pending
// counters using abort math, then clears the staging set. Mirrors
// AtEOXact_PgStat_Relations(abort).
func (m *relationStatsManager) abortXact(sessionID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.foldStagingLocked(sessionID, false)
}

// foldStagingLocked folds and clears a session's staging set. Caller holds m.mu.
func (m *relationStatsManager) foldStagingLocked(sessionID uint64, isCommit bool) {
	sess := m.staging[sessionID]
	if sess == nil {
		return
	}
	for oid, x := range sess {
		applyXactToPending(m.pendingFor(sessionID, oid), x, isCommit)
	}
	delete(m.staging, sessionID)
}

// prepareXact moves a session's staged transaction counters into a per-gid 2PC
// record, mirroring AtPrepare_PgStat_Relations + PostPrepare_PgStat_Relations
// (the staged transactional counts are carried in the 2PC state; the backend's
// already-pending non-transactional scan counts stay and report normally). The
// session's staging is cleared.
func (m *relationStatsManager) prepareXact(sessionID uint64, gid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.staging[sessionID]
	delete(m.staging, sessionID)
	if sess == nil {
		sess = make(map[uint32]*relXactCounters)
	}
	m.prepared[gid] = sess
}

// finalizePrepared applies a prepared 2PC record's staged counters into the
// finalising backend's pending counters (commit or abort math) and drops the
// record. Mirrors pgstat_twophase_postcommit / pgstat_twophase_postabort, which
// load the saved counts into the *local* backend's pending stats.
func (m *relationStatsManager) finalizePrepared(gid string, sessionID uint64, isCommit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.prepared[gid]
	if !ok {
		return
	}
	delete(m.prepared, gid)
	for oid, x := range rec {
		applyXactToPending(m.pendingFor(sessionID, oid), x, isCommit)
	}
}

// flush merges a session's pending counters into the shared store and clears the
// pending set. Mirrors pgstat_relation_flush_cb; the `stats` spec triggers it via
// pg_stat_force_next_flush(). A truncdropped pending entry first resets the
// shared live/dead totals to zero (forgetting already-flushed counts).
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
		if c.truncDropped {
			s.deltaLive = 0
			s.deltaDead = 0
		}
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

// dropTable removes all cumulative statistics for a relation OID — the shared
// (flushed) counters, any not-yet-flushed pending counters, and any staged or
// prepared transactional counters across every session/gid. Mirrors
// pgstat_drop_relation: after the drop the getters read 0 and a concurrent
// backend's stale counts for the OID are discarded rather than revived on a
// later flush. M0118-0009 (`stats`).
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
	for _, sess := range m.staging {
		delete(sess, oid)
	}
	for _, rec := range m.prepared {
		delete(rec, oid)
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

// inExplicitTxn reports whether the session is inside an explicit transaction
// block (BEGIN … without a terminating COMMIT/ROLLBACK). Autocommit statements
// commit immediately, so their staged transactional stats are folded into
// pending at DML time; explicit-block statements stay staged until COMMIT.
func inExplicitTxn(ctx *Context) bool {
	return ctx != nil && ctx.Session != nil && ctx.Session.InExplicitTransaction()
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

// recordRelInsert / recordRelUpdate / recordRelDelete / recordRelTruncate stage
// a DML statement's transactional counts for the current transaction, gated by
// track_counts. In autocommit (no explicit transaction block) the statement
// commits immediately, so the staged counters are folded into pending right
// away — matching "applied at commit" for the simple-query path.

func recordRelInsert(ctx *Context, oid uint32, n int64) {
	if oid == 0 || n == 0 || !shouldTrackCounts(ctx) {
		return
	}
	id := sessionStatsID(ctx)
	relStats.recordInsert(id, oid, n)
	if !inExplicitTxn(ctx) {
		relStats.commitXact(id)
	}
}

func recordRelUpdate(ctx *Context, oid uint32, n int64) {
	if oid == 0 || n == 0 || !shouldTrackCounts(ctx) {
		return
	}
	id := sessionStatsID(ctx)
	relStats.recordUpdate(id, oid, n)
	if !inExplicitTxn(ctx) {
		relStats.commitXact(id)
	}
}

func recordRelDelete(ctx *Context, oid uint32, n int64) {
	if oid == 0 || n == 0 || !shouldTrackCounts(ctx) {
		return
	}
	id := sessionStatsID(ctx)
	relStats.recordDelete(id, oid, n)
	if !inExplicitTxn(ctx) {
		relStats.commitXact(id)
	}
}

func recordRelTruncate(ctx *Context, oid uint32) {
	if oid == 0 || !shouldTrackCounts(ctx) {
		return
	}
	id := sessionStatsID(ctx)
	relStats.recordTruncate(id, oid)
	if !inExplicitTxn(ctx) {
		relStats.commitXact(id)
	}
}

// CommitRelStats / AbortRelStats fold a session's staged relation-stat counters
// into its pending counters at end of an explicit transaction. Exported for the
// transaction-control operators (execCommit / execRollback). A no-op when no
// counters were staged.
func CommitRelStats(ctx *Context) { relStats.commitXact(sessionStatsID(ctx)) }
func AbortRelStats(ctx *Context)  { relStats.abortXact(sessionStatsID(ctx)) }

// PrepareRelStats moves the session's staged relation-stat counters into a
// per-gid 2PC record at PREPARE TRANSACTION (AtPrepare_PgStat_Relations). Called
// only on the detached (RC/RR) prepare path; the same-backend keep-open path
// leaves the staging in place for the finalising COMMIT/ROLLBACK to fold.
func PrepareRelStats(ctx *Context, gid string) { relStats.prepareXact(sessionStatsID(ctx), gid) }

// FinalizePreparedRelStats applies a prepared 2PC record's staged relation-stat
// counters into the finalising backend's pending counters (COMMIT/ROLLBACK
// PREPARED). The finalising backend then flushes them via its own
// pg_stat_force_next_flush(), exactly as pgstat_twophase_post{commit,abort}.
func FinalizePreparedRelStats(ctx *Context, gid string, isCommit bool) {
	relStats.finalizePrepared(gid, sessionStatsID(ctx), isCommit)
}

// tableOIDFromCatalog resolves a catalog.Table's OID, or 0 when unavailable.
func tableOIDFromCatalog(t *catalog.Table) uint32 {
	if t == nil {
		return 0
	}
	return t.OID
}
