package executor

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// DeferredFKCheck records one FK constraint to be verified at COMMIT time.
// DEFERRABLE INITIALLY DEFERRED constraints queue checks here instead of
// enforcing immediately. M0096-0011.
type DeferredFKCheck struct {
	ChildTableName string
	FK             catalog.ForeignKey
}

// Session stores per-connection state the Transaction operator needs
// to manage BEGIN/COMMIT/ROLLBACK.
type Session interface {
	IsolationLevel() transam.IsolationLevel
	// SetIsolationLevel updates the session-default isolation level.
	// Used by BEGIN ISOLATION LEVEL and SET TRANSACTION ISOLATION LEVEL.
	SetIsolationLevel(level transam.IsolationLevel) error
	InExplicitTransaction() bool
	// TracksDDLUndo reports whether enum/composite-type DDL (CREATE TYPE ... AS
	// ENUM/composite, ALTER TYPE ... ADD VALUE/RENAME TO) should be tracked in
	// ctx.Pending* for potential undo — true both for a real explicit
	// transaction (InExplicitTransaction()) and for a message-scoped autocommit
	// session (NewAutocommitUndoSession), whose batch can still abort mid-message
	// and needs the same undo. Deliberately NOT the same as
	// InExplicitTransaction(): flipping that would also perturb ~20 unrelated
	// call sites (deferred FK/UNIQUE/EXCLUDE check timing, TRUNCATE/DROP-in-
	// savepoint snapshotting, pg_stat scoping) — root-0024 residual, M0110-0001.
	TracksDDLUndo() bool
	IsReadOnlyTxn() bool // true when the current transaction was started with READ ONLY
	SetReadOnlyTxn(v bool)
	CurrentTransaction() (transam.Transaction, transam.Snapshot, bool)
	BeginExplicitTransaction(tx transam.Transaction, snap transam.Snapshot)
	EndExplicitTransaction()
}

// DDLUndoEntry records one CREATE TABLE or CREATE INDEX performed inside
// an explicit transaction so that ROLLBACK can reverse the catalog mutation
// and remove the physical relfile (M0030-0006).
type DDLUndoEntry struct {
	Name    parser.ObjectName
	RelOID  uint32 // physical relfile OID (= table.OID or index.OID)
	IsIndex bool
}

// DDLDropUndoEntry records a DROP TABLE performed inside an active savepoint
// so that ROLLBACK TO SAVEPOINT can restore the catalog entries. M0097-0023.
type DDLDropUndoEntry struct {
	Table          *catalog.Table
	Indexes        []*catalog.Index
	SavepointDepth int // subxactStack depth at time of drop
}

// PendingIndexDrop records a non-CONCURRENTLY DROP INDEX issued inside an
// explicit transaction whose catalog removal is deferred until COMMIT. Until the
// dropping transaction commits, the index stays in the shared catalog so other
// sessions still observe its pg_class row and the dropper's AccessExclusiveLock
// (PG transactional-DDL visibility; partition-drop-index-locking isolation
// spec). The recorded fields are everything ApplyPendingIndexDrops needs to
// perform the real removal at commit (catalog + relfile + WAL + pg_class xmax).
// M0118-0008.
type PendingIndexDrop struct {
	Name           parser.ObjectName   // catalog lookup key passed to Catalog.DropIndex
	OID            uint32              // index relation OID (WAL + pg_class xmax)
	Schema         string              // index schema (WAL payload)
	IdxName        string              // bare index name (WAL payload)
	Rel            storage.RelFileNode // physical relfile to invalidate + drop
	SavepointDepth int                 // savepoint depth at drop time (for ROLLBACK TO cancel)
}

// PendingPartitionAttach records an ALTER TABLE … ATTACH PARTITION issued inside
// an explicit transaction whose catalog registration is deferred until COMMIT.
// Until the attaching transaction commits, the new partition is NOT registered in
// the shared catalog, so a concurrent session does not see it and an INSERT
// routing through the parent falls to the parent's DEFAULT partition (PG
// transactional-DDL visibility: an uncommitted ATTACH is invisible to other
// snapshots, exactly what the partition-concurrent-attach isolation spec needs to
// make s2's insert route to the default and block on the default-partition lock).
// The recorded fields are everything ApplyPendingPartitionAttaches needs to
// perform the real registration at commit. In autocommit the attach stays
// immediate. M0118-0008.
type PendingPartitionAttach struct {
	ParentOID      uint32                   // partitioned parent table OID
	ChildOID       uint32                   // attached child table OID
	Bounds         []catalog.PartitionBound // child relpartbound (nil if none computed)
	SavepointDepth int                      // savepoint depth at attach time (ROLLBACK TO cancel)
}

// PendingInheritanceChange records an ALTER TABLE … {NO} INHERIT issued inside an
// explicit transaction whose catalog mutation (register/unregister the parent↔child
// inheritance link) is deferred until COMMIT. Until the altering transaction
// commits the shared catalog keeps the OLD inheritance state, so a concurrent
// session scanning the parent still expands to the pre-change child set (and a
// child being un-inherited stays scanned, blocking on the AccessExclusiveLock the
// altering session holds on it) — PG transactional-DDL visibility, the
// alter-table-4 isolation spec. In autocommit the mutation stays immediate.
// ApplyPendingInheritanceChanges replays the recorded change at commit. M0118-0008.
type PendingInheritanceChange struct {
	ParentOID      uint32 // inheritance parent table OID
	ChildOID       uint32 // inheritance child table OID
	NoInherit      bool   // true = NO INHERIT (unregister); false = INHERIT (register)
	SavepointDepth int    // savepoint depth at statement time (ROLLBACK TO cancel)
}

// PendingTableDrop records a DROP TABLE issued inside an explicit transaction
// whose catalog/relfile/WAL removal is deferred until COMMIT. Until the dropping
// transaction commits, the table stays in the shared catalog so a concurrent
// session still expands the parent's inheritance tree to include it and blocks on
// the AccessExclusiveLock the dropper holds on it (PG transactional-DDL
// visibility, alter-table-4 isolation spec perm 3: `DROP TABLE c1` while a
// concurrent `SELECT SUM(a) FROM p` runs — the reader identifies c1 as a child
// before locking it, waits behind the drop, then skips the now-vanished child).
// Deferral is gated narrowly (see dropTableByRef): only a leaf table with no
// partition/inheritance children, no pending-detach state, and no temp shadow —
// so cascade/partition removal keeps its immediate, ordered behaviour. In
// autocommit the removal stays immediate. ApplyPendingTableDrops replays the
// recorded removal at commit. Like the DROP INDEX deferral (PendingIndexDrop),
// the dropping session itself also keeps seeing the table until commit — goopg's
// shared catalog has no per-session MVCC visibility — a known limitation the
// spec never exercises (s1 does not reference c1 after dropping it). M0118-0008.
type PendingTableDrop struct {
	Name           parser.ObjectName // catalog lookup key passed to dropTableByRefImmediate
	Table          *catalog.Table    // resolved table (carries OID/Schema/Name for removal)
	SavepointDepth int               // savepoint depth at drop time (for ROLLBACK TO cancel)
}

// DeferredRoutineDrop records a DROP FUNCTION issued inside an explicit
// transaction whose registry removal AND cumulative function-stats drop are
// deferred until COMMIT. Until the dropping transaction commits, the routine
// stays in the shared registry so a concurrent session still resolves and calls
// it (PG transactional-DDL visibility, `stats` isolation spec: s1 BEGINs, drops
// test_stat_func, and s2 must still SELECT test_stat_func() until s1 commits;
// the function's pg_stat_get_function_* counters likewise survive until then).
// ApplyDeferredRoutineDrops performs the real removal at commit; ROLLBACK simply
// discards the entry (the routine was never removed). Like PendingTableDrop, the
// dropping session itself also keeps seeing the function until commit — goopg's
// shared catalog has no per-session MVCC visibility — a limitation the spec
// never exercises (s1 does not call test_stat_func after dropping it); a
// CREATE FUNCTION of the same signature in the same transaction is handled by
// TakeDeferredRoutineDropMatching (the deferred drop is applied early so the
// recreate proceeds). M0118-0009 (`stats`).
type DeferredRoutineDrop struct {
	Routine        *catalog.Routine
	SavepointDepth int
}

// relPageSnapshot captures the full page contents of one relation before
// a TRUNCATE so that ROLLBACK can restore them.
type relPageSnapshot struct {
	Rel   storage.RelFileNode
	Pages [][]byte // one entry per block; each slice is BlockSize bytes
}

// TruncateUndoEntry records the before-image of one table (heap + indexes)
// truncated inside an explicit transaction. Used by ROLLBACK to restore.
type TruncateUndoEntry struct {
	Heap    relPageSnapshot
	Indexes []relPageSnapshot
}

// SeqRestoreEntry records the pre-restart sequence counter for rollback.
type SeqRestoreEntry struct {
	Name    string
	OldCurr int64  // seqState.current value before RESTART
	DBOid   uint32 // owning database oid (M0122-0007 4e)
}

// BasicSession is a minimal Session implementation for the v0
// executor path.
type BasicSession struct {
	isolation           transam.IsolationLevel
	inTx                bool
	tx                  transam.Transaction
	snap                transam.Snapshot
	pendingDDL          []DDLUndoEntry             // DDL creates pending rollback
	pendingTruncates    []TruncateUndoEntry        // heap/index page snapshots for TRUNCATE rollback
	pendingSeqRestores  []SeqRestoreEntry          // sequence counter restores for RESTART IDENTITY rollback
	savepointDDLDrops   []DDLDropUndoEntry         // DROP TABLE inside savepoints, for ROLLBACK TO (M0097-0023)
	pendingIndexDrops   []PendingIndexDrop         // DROP INDEX deferred to COMMIT (M0118-0008)
	pendingPartAttaches []PendingPartitionAttach   // ATTACH PARTITION deferred to COMMIT (M0118-0008)
	pendingInheritChng  []PendingInheritanceChange // ALTER TABLE {NO} INHERIT deferred to COMMIT (M0118-0008)
	pendingTableDrops   []PendingTableDrop         // DROP TABLE deferred to COMMIT (M0118-0008 alter-table-4)
	deferRoutineDrops   []DeferredRoutineDrop      // DROP FUNCTION deferred to COMMIT (M0118-0009 stats)
	subxactStack        transam.SubxactStack          // savepoint stack (M0050-0004)
	currentSubXid       storage.TransactionID      // 0 = use top-level tx.XID
	txFailed            bool                       // in_failed_sql_transaction (25P02)
	txnReadOnly         bool                       // true while inside a READ ONLY transaction (M0097-0024)
	deferredFKChecks    []DeferredFKCheck          // INITIALLY DEFERRED FK checks (M0096-0011)
	deferredUniqChecks  []DeferredUniqueCheck      // INITIALLY DEFERRED UNIQUE/PK checks (0119-0004)
	deferredExclChecks  []DeferredExclusionCheck   // INITIALLY DEFERRED EXCLUDE checks (0119-0004)
	constraintsAllMode  int8                       // SET CONSTRAINTS ALL: 0 unset, 1 DEFERRED, 2 IMMEDIATE (0119-0004)
	constraintDeferral  map[string]bool            // SET CONSTRAINTS <name>: per-constraint override (0119-0004)
	activeQueryTables   map[uint32]bool            // OIDs of tables currently in active DML (M0097-0023)
	statsSnapshot       *funcStatSnapshot          // per-txn cumulative-stats snapshot (M0118-0009 stats_fetch_consistency)
	autocommitUndoScope bool                       // see NewAutocommitUndoSession / TracksDDLUndo (root-0024 residual, M0110-0001)
	cmdCounter          CommandCounter             // per-transaction command counter (M0129-S8.3)
}

// NewBasicSession constructs an explicit-transaction session state
// holder with READ COMMITTED as default isolation.
func NewBasicSession() *BasicSession {
	return &BasicSession{isolation: transam.IsolationReadCommitted}
}

// NewAutocommitUndoSession constructs a message-scoped, throwaway session for
// an autocommit multi-statement batch (dispatchSimpleQueryViaExecutor), like
// NewBasicSession but with TracksDDLUndo() reporting true. inTx stays false —
// this does NOT make the session an explicit transaction, so the ~20 call
// sites gated on InExplicitTransaction() are unaffected — it only lets
// CREATE TYPE ... AS ENUM/composite and ALTER TYPE ... ADD VALUE/RENAME TO
// record into ctx.Pending* for the duration of this one Query message, so a
// LATER statement's failure in the same message can undo them (mirrors
// RecordDDLCreate's existing unconditional recording for CREATE TABLE/INDEX).
// root-0024 residual, M0110-0001.
func NewAutocommitUndoSession() *BasicSession {
	s := NewBasicSession()
	s.autocommitUndoScope = true
	return s
}

// SetIsolationLevel updates the default isolation level used by BEGIN.
func (s *BasicSession) SetIsolationLevel(level transam.IsolationLevel) error {
	switch level {
	case transam.IsolationReadCommitted, transam.IsolationRepeatableRead, transam.IsolationSerializable:
	default:
		return fmt.Errorf("executor: unsupported isolation level %v", level)
	}
	s.isolation = level
	return nil
}

func (s *BasicSession) IsolationLevel() transam.IsolationLevel { return s.isolation }

func (s *BasicSession) InExplicitTransaction() bool { return s.inTx }

func (s *BasicSession) TracksDDLUndo() bool { return s.inTx || s.autocommitUndoScope }

func (s *BasicSession) IsReadOnlyTxn() bool    { return s.txnReadOnly }
func (s *BasicSession) SetReadOnlyTxn(v bool)  { s.txnReadOnly = v }

func (s *BasicSession) CurrentTransaction() (transam.Transaction, transam.Snapshot, bool) {
	if !s.inTx {
		return transam.Transaction{}, transam.Snapshot{}, false
	}
	return s.tx, s.snap.Clone(), true
}

func (s *BasicSession) BeginExplicitTransaction(tx transam.Transaction, snap transam.Snapshot) {
	s.tx = tx
	s.snap = snap.Clone()
	s.inTx = true
	s.cmdCounter.Reset() // M0129-S8.3: fresh command counter per transaction
}

// CmdCounter returns a pointer to the session's per-transaction command counter
// so dispatch can seed a fresh Context with the correct prior-command state.
// M0129-S8.3.
func (s *BasicSession) CmdCounter() *CommandCounter { return &s.cmdCounter }

// RelocateTransaction updates the session's cached transaction handle after the
// transaction has been moved to a dedicated proc slot (PREPARE TRANSACTION
// detach). The XID, snapshot and pending-work state are unchanged; only the
// Handle (and thus the proc slot the later COMMIT/ROLLBACK PREPARED finalises)
// differs. No-op outside an open explicit transaction. M0118-0009.
func (s *BasicSession) RelocateTransaction(tx transam.Transaction) {
	if !s.inTx {
		return
	}
	s.tx = tx
}

// OnTopLevelXIDAssigned is invoked by Context.MaterializeWriterXID
// after the top-level transaction's XID is lazily materialised
// (M0093 Design B). It keeps the session's cached tx.XID in sync
// so later savepoint AllocateSubXid calls — which consult
// EffectiveWriterXID's top-level fallback — see the real parent
// XID instead of zero. No-op when there's no open explicit
// transaction or when the cached tx.XID is already set (e.g.
// inside an active savepoint whose currentSubXid will mask the
// top-level XID anyway).
func (s *BasicSession) OnTopLevelXIDAssigned(xid storage.TransactionID) {
	if !s.inTx {
		return
	}
	if s.tx.XID == storage.InvalidTransactionID {
		s.tx.XID = xid
	}
}

func (s *BasicSession) EndExplicitTransaction() {
	s.tx = transam.Transaction{}
	s.snap = transam.Snapshot{}
	s.inTx = false
	s.pendingDDL = nil
	s.pendingTruncates = nil
	s.pendingSeqRestores = nil
	s.savepointDDLDrops = nil
	s.pendingIndexDrops = nil
	s.pendingPartAttaches = nil
	s.pendingInheritChng = nil
	s.pendingTableDrops = nil
	s.deferRoutineDrops = nil
	s.subxactStack = transam.SubxactStack{}
	s.currentSubXid = 0
	s.txFailed = false
	s.deferredFKChecks = nil
	s.deferredUniqChecks = nil
	s.deferredExclChecks = nil
	// SET CONSTRAINTS settings last only for the current transaction (PG
	// AfterTriggerEndXact resets the deferral state at every txn boundary). 0119-0004.
	s.constraintsAllMode = 0
	s.constraintDeferral = nil
	s.activeQueryTables = nil
	// Discard the per-transaction cumulative-stats snapshot — PG's AtEOXact_PgStat
	// drops the stats snapshot at every transaction boundary so the next
	// transaction reads live values again. M0118-0009 (stats_fetch_consistency).
	s.statsSnapshot = nil
	s.cmdCounter.Reset() // M0129-S8.3: reset command counter at transaction boundary
}

// ensureStatsSnapshot returns the session's per-transaction cumulative-stats
// snapshot, allocating it on first use. Used by fetchFuncStat to honour
// stats_fetch_consistency = 'cache'/'snapshot'. M0118-0009.
func (s *BasicSession) ensureStatsSnapshot() *funcStatSnapshot {
	if s.statsSnapshot == nil {
		s.statsSnapshot = &funcStatSnapshot{perObject: make(map[uint32]cachedFuncStat)}
	}
	return s.statsSnapshot
}

// ClearStatsSnapshot discards the per-transaction cumulative-stats snapshot,
// forcing the next stat read to consult live shared values. Invoked by
// pg_stat_clear_snapshot(). M0118-0009.
func (s *BasicSession) ClearStatsSnapshot() { s.statsSnapshot = nil }

// AddDeferredFKCheck queues a FK constraint to be checked at COMMIT time.
// M0096-0011.
func (s *BasicSession) AddDeferredFKCheck(check DeferredFKCheck) {
	// Deduplicate: if an identical (ChildTableName, FK.Columns, FK.RefTable)
	// check is already queued, skip — one full-table scan at COMMIT suffices.
	for _, existing := range s.deferredFKChecks {
		if existing.ChildTableName == check.ChildTableName &&
			existing.FK.RefTable == check.FK.RefTable {
			return
		}
	}
	s.deferredFKChecks = append(s.deferredFKChecks, check)
}

// TakeDeferredFKChecks returns and clears the queued deferred FK checks.
// Called by execCommit before issuing TxnMgr.Commit. M0096-0011.
func (s *BasicSession) TakeDeferredFKChecks() []DeferredFKCheck {
	out := s.deferredFKChecks
	s.deferredFKChecks = nil
	return out
}

// SetConstraintsAll records `SET CONSTRAINTS ALL { DEFERRED | IMMEDIATE }` for
// the current transaction. ALL supersedes any prior per-constraint overrides.
// 0119-0004.
func (s *BasicSession) SetConstraintsAll(deferred bool) {
	if deferred {
		s.constraintsAllMode = 1
	} else {
		s.constraintsAllMode = 2
	}
	s.constraintDeferral = nil
}

// SetConstraintsNamed records `SET CONSTRAINTS name [, ...] { DEFERRED |
// IMMEDIATE }`. A per-name setting overrides the ALL mode for that constraint.
// 0119-0004.
func (s *BasicSession) SetConstraintsNamed(names []string, deferred bool) {
	if s.constraintDeferral == nil {
		s.constraintDeferral = make(map[string]bool)
	}
	for _, n := range names {
		s.constraintDeferral[n] = deferred
	}
}

// constraintDeferredByName resolves whether a DEFERRABLE constraint's check
// should be deferred to COMMIT, honouring any in-effect SET CONSTRAINTS override
// and the constraint's declared INITIALLY {DEFERRED|IMMEDIATE} default. A
// per-name override wins over an ALL setting, which wins over the declared
// default. The deferral state is constraint-kind-agnostic (PG's
// AfterTriggerSetState applies equally to FK, UNIQUE and EXCLUDE constraints),
// so FK and UNIQUE callers share it. 0119-0004.
func (s *BasicSession) constraintDeferredByName(name string, initiallyDeferred bool) bool {
	if name != "" && s.constraintDeferral != nil {
		if v, ok := s.constraintDeferral[name]; ok {
			return v
		}
	}
	switch s.constraintsAllMode {
	case 1:
		return true
	case 2:
		return false
	}
	return initiallyDeferred
}

// FKConstraintDeferred reports whether a foreign-key constraint's check should
// be deferred to COMMIT. Callers must have already established the constraint is
// DEFERRABLE. 0119-0004.
func (s *BasicSession) FKConstraintDeferred(name string, initiallyDeferred bool) bool {
	return s.constraintDeferredByName(name, initiallyDeferred)
}

// UniqueConstraintDeferred reports whether a UNIQUE / PRIMARY KEY constraint's
// uniqueness check should be deferred to COMMIT. Callers must have already
// established the constraint is DEFERRABLE. 0119-0004 (deferred-unique).
func (s *BasicSession) UniqueConstraintDeferred(name string, initiallyDeferred bool) bool {
	return s.constraintDeferredByName(name, initiallyDeferred)
}

// ExclusionConstraintDeferred reports whether an EXCLUDE constraint's check
// should be deferred to COMMIT. Callers must have already established the
// constraint is DEFERRABLE. 0119-0004 (deferred-exclusion).
func (s *BasicSession) ExclusionConstraintDeferred(name string, initiallyDeferred bool) bool {
	return s.constraintDeferredByName(name, initiallyDeferred)
}

// ConstraintsOverrideActive reports whether a SET CONSTRAINTS override is in
// effect for the current transaction (ALL mode set or any per-name override).
// The simple-query COMMIT path uses this to decide whether to run the deferred
// FK checks it would otherwise leave to the (bypassed) executor commit path:
// commit-time enforcement is activated for SET CONSTRAINTS-controlled
// deferrals, while a plain INITIALLY DEFERRED constraint keeps its existing
// behaviour (whose concurrent/partitioned-parent snapshot semantics —
// fk-snapshot — are a separate, pre-existing gap). 0119-0004.
func (s *BasicSession) ConstraintsOverrideActive() bool {
	return s.constraintsAllMode != 0 || len(s.constraintDeferral) > 0
}

// DeferredUniqueCheck records one UNIQUE / PRIMARY KEY constraint violation to
// be re-verified at COMMIT time (a DEFERRABLE constraint deferred by INITIALLY
// DEFERRED or by an in-effect SET CONSTRAINTS … DEFERRED). The encoded btree
// Key identifies the candidate key whose uniqueness must hold once the
// transaction's final row state is visible; Detail is the pre-rendered "Key
// (...)=(...) already exists." text for the 23505 raised at COMMIT. 0119-0004.
type DeferredUniqueCheck struct {
	TableName string
	IndexName string // == the UNIQUE/PK constraint name (SET CONSTRAINTS match key)
	Key       []byte
	// NNDKeyCols, when non-nil, marks a NULLS NOT DISTINCT check whose candidate
	// has one or more NULL key columns: such a row has no btree Key (Key is nil),
	// so the COMMIT recheck is a heap scan over the recorded NULL pattern instead
	// of a btree RangeScan. nil for an ordinary (btree-keyed) deferred check.
	// Design 0119-0004-deferred-unique-nnd.
	NNDKeyCols []DeferredNNDKeyCol
	Detail     string
	// DeferToCommit is true when this check must wait until COMMIT (the
	// constraint is INITIALLY DEFERRED, or SET CONSTRAINTS … DEFERRED is in
	// effect for it); false means it is only deferred to the END of the
	// statement that queued it (the common DEFERRABLE-without-INITIALLY-DEFERRED
	// case, and any index made partial-checked while SET CONSTRAINTS …
	// IMMEDIATE is in effect). Resolved once at queue time by
	// uniqueCheckDeferToCommit. b4-s1-stmt-end-unique.
	DeferToCommit bool
}

// DeferredNNDKeyCol is one index key column's candidate value, captured at DML
// time so a NULLS NOT DISTINCT deferred check can re-scan the heap at COMMIT
// without holding live catalog/Row pointers across statements. Null records that
// the candidate was NULL for this column; Key is the column's encoded btree key
// when non-NULL (nil when Null). 0119-0004-deferred-unique-nnd.
type DeferredNNDKeyCol struct {
	ColName string
	Null    bool
	Key     []byte
}

// AddDeferredUniqueCheck queues a UNIQUE/PK constraint key to be re-verified at
// COMMIT time. Deduplicates on (IndexName, Key, NNDKeyCols): one re-probe per
// distinct candidate suffices — two distinct NULL patterns on the same NND index
// (e.g. (null,1) and (null,2)) queue separately. 0119-0004.
func (s *BasicSession) AddDeferredUniqueCheck(check DeferredUniqueCheck) {
	for _, existing := range s.deferredUniqChecks {
		if existing.IndexName == check.IndexName && bytes.Equal(existing.Key, check.Key) &&
			sameNNDKeyCols(existing.NNDKeyCols, check.NNDKeyCols) {
			return
		}
	}
	s.deferredUniqChecks = append(s.deferredUniqChecks, check)
}

// sameNNDKeyCols reports whether two NND candidate NULL-pattern descriptors are
// identical (same columns, NULL flags, and encoded non-NULL key bytes). Used to
// dedup deferred NND unique checks. 0119-0004-deferred-unique-nnd.
func sameNNDKeyCols(a, b []DeferredNNDKeyCol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ColName != b[i].ColName || a[i].Null != b[i].Null || !bytes.Equal(a[i].Key, b[i].Key) {
			return false
		}
	}
	return true
}

// TakeDeferredUniqueChecks returns and clears the queued deferred UNIQUE/PK
// checks. Called before TxnMgr.Commit on both commit paths. 0119-0004.
func (s *BasicSession) TakeDeferredUniqueChecks() []DeferredUniqueCheck {
	out := s.deferredUniqChecks
	s.deferredUniqChecks = nil
	return out
}

// TakeDeferredUniqueChecksStmtEnd removes and returns every queued deferred
// UNIQUE/PK check that is NOT currently deferred to COMMIT (DeferToCommit ==
// false) — the end-of-statement tier. Checks whose DeferToCommit is true stay
// queued for COMMIT (TakeDeferredUniqueChecks / TakeDeferredUniqueChecksMatching).
// Called once per top-level statement by RunStmtEndDeferredUniqueChecks.
// b4-s1-stmt-end-unique.
func (s *BasicSession) TakeDeferredUniqueChecksStmtEnd() []DeferredUniqueCheck {
	if len(s.deferredUniqChecks) == 0 {
		return nil
	}
	var taken, kept []DeferredUniqueCheck
	for _, c := range s.deferredUniqChecks {
		if c.DeferToCommit {
			kept = append(kept, c)
		} else {
			taken = append(taken, c)
		}
	}
	s.deferredUniqChecks = kept
	return taken
}

// TakeDeferredUniqueChecksMatching removes and returns the queued deferred
// UNIQUE/PK checks made immediate by a `SET CONSTRAINTS … IMMEDIATE`, mirroring
// TakeDeferredFKChecksMatching. The rest stay queued for COMMIT. 0119-0004.
func (s *BasicSession) TakeDeferredUniqueChecksMatching(all bool, names []string) []DeferredUniqueCheck {
	if all {
		out := s.deferredUniqChecks
		s.deferredUniqChecks = nil
		return out
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var taken, kept []DeferredUniqueCheck
	for _, c := range s.deferredUniqChecks {
		if c.IndexName != "" && want[c.IndexName] {
			taken = append(taken, c)
		} else {
			kept = append(kept, c)
		}
	}
	s.deferredUniqChecks = kept
	return taken
}

// DeferredExclusionCheck records one EXCLUDE constraint candidate to be
// re-verified at COMMIT time (a DEFERRABLE constraint deferred by INITIALLY
// DEFERRED or by an in-effect SET CONSTRAINTS … DEFERRED). For a `WITH =`
// (btree) exclusion the encoded Key identifies the candidate key; for a
// `USING gist … WITH &&` overlap exclusion BoxStr holds the candidate box text.
// Detail is the pre-rendered "Key (...)=(...) conflicts with existing key
// (...)=(...)." text for the 23P01 raised at COMMIT (built at recheck time for
// the &&/overlap case, where the conflicting box is only known then). 0119-0004.
type DeferredExclusionCheck struct {
	TableName   string
	IndexName   string // == the EXCLUDE constraint name (SET CONSTRAINTS match key)
	ExclusionOp string // "=" (btree) or "&&" (gist overlap)
	Key         []byte // encoded btree key for ExclusionOp == "="
	BoxStr      string // candidate box text for ExclusionOp == "&&"
	Detail      string
}

// AddDeferredExclusionCheck queues an EXCLUDE constraint candidate to be
// re-verified at COMMIT time. Deduplicates on (IndexName, Key, BoxStr): one
// re-probe per distinct candidate suffices. 0119-0004.
func (s *BasicSession) AddDeferredExclusionCheck(check DeferredExclusionCheck) {
	for _, existing := range s.deferredExclChecks {
		if existing.IndexName == check.IndexName &&
			bytes.Equal(existing.Key, check.Key) && existing.BoxStr == check.BoxStr {
			return
		}
	}
	s.deferredExclChecks = append(s.deferredExclChecks, check)
}

// TakeDeferredExclusionChecks returns and clears the queued deferred EXCLUDE
// checks. Called before TxnMgr.Commit on both commit paths. 0119-0004.
func (s *BasicSession) TakeDeferredExclusionChecks() []DeferredExclusionCheck {
	out := s.deferredExclChecks
	s.deferredExclChecks = nil
	return out
}

// TakeDeferredExclusionChecksMatching removes and returns the queued deferred
// EXCLUDE checks made immediate by a `SET CONSTRAINTS … IMMEDIATE`, mirroring
// TakeDeferredUniqueChecksMatching. The rest stay queued for COMMIT. 0119-0004.
func (s *BasicSession) TakeDeferredExclusionChecksMatching(all bool, names []string) []DeferredExclusionCheck {
	if all {
		out := s.deferredExclChecks
		s.deferredExclChecks = nil
		return out
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var taken, kept []DeferredExclusionCheck
	for _, c := range s.deferredExclChecks {
		if c.IndexName != "" && want[c.IndexName] {
			taken = append(taken, c)
		} else {
			kept = append(kept, c)
		}
	}
	s.deferredExclChecks = kept
	return taken
}

// TakeDeferredFKChecksMatching removes and returns the queued deferred FK
// checks affected by a `SET CONSTRAINTS … IMMEDIATE` so they can be run now.
// When all is true every queued check is taken; otherwise only those whose FK
// constraint name is in names. The rest stay queued for COMMIT. 0119-0004.
func (s *BasicSession) TakeDeferredFKChecksMatching(all bool, names []string) []DeferredFKCheck {
	if all {
		out := s.deferredFKChecks
		s.deferredFKChecks = nil
		return out
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var taken, kept []DeferredFKCheck
	for _, c := range s.deferredFKChecks {
		if c.FK.Name != "" && want[c.FK.Name] {
			taken = append(taken, c)
		} else {
			kept = append(kept, c)
		}
	}
	s.deferredFKChecks = kept
	return taken
}

// EffectiveWriterXID returns the XID to stamp on new heap tuples.
// Inside an open savepoint this is the sub-transaction's XID; otherwise
// it is the top-level transaction XID.
func (s *BasicSession) EffectiveWriterXID() storage.TransactionID {
	if s.currentSubXid != 0 {
		return s.currentSubXid
	}
	return s.tx.XID
}

// PushSavepoint records a new savepoint on the stack with the given
// sub-transaction XID. The sub-XID is stamped on subsequent heap mutations.
func (s *BasicSession) PushSavepoint(name string, snap transam.Snapshot, subXid storage.TransactionID) {
	entry := s.subxactStack.Push(name, snap)
	entry.SubXid = subXid
	s.currentSubXid = subXid
}

// ReleaseSavepoint marks the named savepoint and all savepoints above it
// as committed and removes them from the stack. Returns released entries.
func (s *BasicSession) ReleaseSavepoint(name string) ([]*transam.SubTransactionState, error) {
	released, err := s.subxactStack.Release(name)
	if err != nil {
		return nil, err
	}
	top := s.subxactStack.Top()
	if top != nil {
		s.currentSubXid = top.SubXid
	} else {
		s.currentSubXid = 0
	}
	return released, nil
}

// RollbackToSavepoint aborts the named savepoint and all savepoints above
// it, then pushes a fresh entry for the same name with the given snapshot
// and new sub-XID. Returns the aborted entries.
func (s *BasicSession) RollbackToSavepoint(name string, newSnap transam.Snapshot, newSubXid storage.TransactionID) ([]*transam.SubTransactionState, *transam.SubTransactionState, error) {
	aborted, fresh, err := s.subxactStack.RollbackTo(name, newSnap)
	if err != nil {
		return nil, nil, err
	}
	fresh.SubXid = newSubXid
	s.currentSubXid = newSubXid
	s.txFailed = false
	return aborted, fresh, nil
}

// IsTransactionFailed reports whether the transaction has an unresolved
// error that prevents non-rollback statements (SQLSTATE 25P02).
func (s *BasicSession) IsTransactionFailed() bool { return s.txFailed }

// SetTransactionFailed marks the transaction as failed.
func (s *BasicSession) SetTransactionFailed() { s.txFailed = true }

// RecordDDLCreate records a CREATE TABLE or CREATE INDEX for potential
// rollback. Called by DDL operators after catalog.CreateTable /
// catalog.CreateIndex succeed. Recorded unconditionally: besides an explicit
// BEGIN block, a *BasicSession is also wired message-scoped for an autocommit
// simple-query batch (dispatchSimpleQueryViaExecutor) so that a LATER
// statement's failure in the SAME multi-statement message can still undo an
// earlier CREATE via ProcessRollbackUndos — that batch runs under one
// implicit mvcc transaction, so it is not actually durable until every
// statement in the message succeeds (M0110-0001 DU-002 slice 444 deferral,
// 2026-07-04). A session that never aborts simply never drains this list.
func (s *BasicSession) RecordDDLCreate(e DDLUndoEntry) {
	s.pendingDDL = append(s.pendingDDL, e)
}

// TakePendingDDLCreates drains and returns the pending DDL undo list.
// Called by execRollback to obtain the list of creates that need undoing.
func (s *BasicSession) TakePendingDDLCreates() []DDLUndoEntry {
	p := append([]DDLUndoEntry(nil), s.pendingDDL...)
	s.pendingDDL = nil
	return p
}

// RecordTruncate saves the before-image of a table (heap + indexes) so
// ROLLBACK can restore them. Must be called BEFORE the physical truncation.
func (s *BasicSession) RecordTruncate(e TruncateUndoEntry) {
	s.pendingTruncates = append(s.pendingTruncates, e)
}

// TakePendingTruncates returns and clears the queued truncate undo entries.
func (s *BasicSession) TakePendingTruncates() []TruncateUndoEntry {
	t := s.pendingTruncates
	s.pendingTruncates = nil
	return t
}

// RecordSeqRestore records a sequence's current counter for RESTART IDENTITY rollback.
func (s *BasicSession) RecordSeqRestore(e SeqRestoreEntry) {
	s.pendingSeqRestores = append(s.pendingSeqRestores, e)
}

// TakePendingSeqRestores returns and clears the queued sequence restore entries.
func (s *BasicSession) TakePendingSeqRestores() []SeqRestoreEntry {
	r := s.pendingSeqRestores
	s.pendingSeqRestores = nil
	return r
}

// SavepointDepth returns the current number of active savepoint stack entries.
func (s *BasicSession) SavepointDepth() int {
	return s.subxactStack.Len()
}

// RecordDDLDrop records a DROP TABLE performed inside an active savepoint
// so ROLLBACK TO SAVEPOINT can restore the catalog entries. M0097-0023.
func (s *BasicSession) RecordDDLDrop(e DDLDropUndoEntry) {
	s.savepointDDLDrops = append(s.savepointDDLDrops, e)
}

// RollbackDDLDropsToDepth returns all DDL drop entries recorded at
// savepointDepth >= depth and removes them from the list.
// Called by ROLLBACK TO SAVEPOINT to identify entries to restore.
func (s *BasicSession) RollbackDDLDropsToDepth(depth int) []DDLDropUndoEntry {
	var toUndo, keep []DDLDropUndoEntry
	for _, e := range s.savepointDDLDrops {
		if e.SavepointDepth >= depth {
			toUndo = append(toUndo, e)
		} else {
			keep = append(keep, e)
		}
	}
	s.savepointDDLDrops = keep
	return toUndo
}

// TakePendingDDLDrops drains and returns all recorded DDL drop entries.
// Called at full ROLLBACK time to restore all tables dropped inside savepoints.
func (s *BasicSession) TakePendingDDLDrops() []DDLDropUndoEntry {
	out := s.savepointDDLDrops
	s.savepointDDLDrops = nil
	return out
}

// AddPendingIndexDrop records a DROP INDEX whose catalog removal is deferred
// until COMMIT (see PendingIndexDrop). M0118-0008.
func (s *BasicSession) AddPendingIndexDrop(e PendingIndexDrop) {
	s.pendingIndexDrops = append(s.pendingIndexDrops, e)
}

// TakePendingIndexDrops drains and returns the deferred DROP INDEX entries.
// Called from the commit paths (ApplyPendingIndexDrops) to perform the real
// removal; a full ROLLBACK simply lets EndExplicitTransaction discard them.
// M0118-0008.
func (s *BasicSession) TakePendingIndexDrops() []PendingIndexDrop {
	out := s.pendingIndexDrops
	s.pendingIndexDrops = nil
	return out
}

// CancelPendingIndexDropsToDepth discards deferred DROP INDEX entries recorded
// at savepoint depth >= depth, so ROLLBACK TO SAVEPOINT before a deferred DROP
// INDEX does not still remove the index at the outer COMMIT. Mirrors
// RollbackDDLDropsToDepth's convention. M0118-0008.
func (s *BasicSession) CancelPendingIndexDropsToDepth(depth int) {
	keep := s.pendingIndexDrops[:0]
	for _, e := range s.pendingIndexDrops {
		if e.SavepointDepth < depth {
			keep = append(keep, e)
		}
	}
	s.pendingIndexDrops = keep
}

// AddPendingPartitionAttach records an ATTACH PARTITION whose catalog
// registration is deferred until COMMIT (see PendingPartitionAttach). M0118-0008.
func (s *BasicSession) AddPendingPartitionAttach(e PendingPartitionAttach) {
	s.pendingPartAttaches = append(s.pendingPartAttaches, e)
}

// TakePendingPartitionAttaches drains and returns the deferred ATTACH PARTITION
// entries. Called from the commit paths (ApplyPendingPartitionAttaches) to
// perform the real registration; a full ROLLBACK simply lets
// EndExplicitTransaction discard them. M0118-0008.
func (s *BasicSession) TakePendingPartitionAttaches() []PendingPartitionAttach {
	out := s.pendingPartAttaches
	s.pendingPartAttaches = nil
	return out
}

// CancelPendingPartitionAttachesToDepth discards deferred ATTACH PARTITION
// entries recorded at savepoint depth >= depth, so ROLLBACK TO SAVEPOINT before a
// deferred ATTACH does not still register the partition at the outer COMMIT.
// Mirrors CancelPendingIndexDropsToDepth. M0118-0008.
func (s *BasicSession) CancelPendingPartitionAttachesToDepth(depth int) {
	keep := s.pendingPartAttaches[:0]
	for _, e := range s.pendingPartAttaches {
		if e.SavepointDepth < depth {
			keep = append(keep, e)
		}
	}
	s.pendingPartAttaches = keep
}

// AddPendingInheritanceChange records an ALTER TABLE … {NO} INHERIT whose catalog
// mutation is deferred until COMMIT (see PendingInheritanceChange). M0118-0008.
func (s *BasicSession) AddPendingInheritanceChange(e PendingInheritanceChange) {
	s.pendingInheritChng = append(s.pendingInheritChng, e)
}

// TakePendingInheritanceChanges drains and returns the deferred inheritance
// changes. Called from the commit paths (ApplyPendingInheritanceChanges) to apply
// the real catalog mutation, and from the rollback path (via
// DiscardPendingInheritanceChanges) to discard them. M0118-0008.
func (s *BasicSession) TakePendingInheritanceChanges() []PendingInheritanceChange {
	out := s.pendingInheritChng
	s.pendingInheritChng = nil
	return out
}

// CancelPendingInheritanceChangesToDepth discards deferred inheritance changes
// recorded at savepoint depth >= depth and returns the count discarded, so the
// caller can drop the matching catalog pending-counter marks. Mirrors
// CancelPendingPartitionAttachesToDepth. M0118-0008.
func (s *BasicSession) CancelPendingInheritanceChangesToDepth(depth int) int {
	keep := s.pendingInheritChng[:0]
	cancelled := 0
	for _, e := range s.pendingInheritChng {
		if e.SavepointDepth < depth {
			keep = append(keep, e)
		} else {
			cancelled++
		}
	}
	s.pendingInheritChng = keep
	return cancelled
}

// AddPendingTableDrop records a DROP TABLE whose catalog/relfile removal is
// deferred until COMMIT (see PendingTableDrop). M0118-0008 (alter-table-4).
func (s *BasicSession) AddPendingTableDrop(e PendingTableDrop) {
	s.pendingTableDrops = append(s.pendingTableDrops, e)
}

// TakePendingTableDrops drains and returns the deferred DROP TABLE entries.
// Called from the commit paths (ApplyPendingTableDrops) to perform the real
// removal; a full ROLLBACK simply lets EndExplicitTransaction discard them.
// M0118-0008 (alter-table-4).
func (s *BasicSession) TakePendingTableDrops() []PendingTableDrop {
	out := s.pendingTableDrops
	s.pendingTableDrops = nil
	return out
}

// CancelPendingTableDropsToDepth discards deferred DROP TABLE entries recorded at
// savepoint depth >= depth, so ROLLBACK TO SAVEPOINT before a deferred DROP TABLE
// does not still remove the table at the outer COMMIT. Mirrors
// CancelPendingIndexDropsToDepth. M0118-0008 (alter-table-4).
func (s *BasicSession) CancelPendingTableDropsToDepth(depth int) {
	keep := s.pendingTableDrops[:0]
	for _, e := range s.pendingTableDrops {
		if e.SavepointDepth < depth {
			keep = append(keep, e)
		}
	}
	s.pendingTableDrops = keep
}

// AddDeferredRoutineDrop records a DROP FUNCTION whose registry removal and
// cumulative-stats drop are deferred until COMMIT (see DeferredRoutineDrop).
// M0118-0009 (`stats`).
func (s *BasicSession) AddDeferredRoutineDrop(e DeferredRoutineDrop) {
	s.deferRoutineDrops = append(s.deferRoutineDrops, e)
}

// TakeDeferredRoutineDrops drains and returns the deferred DROP FUNCTION
// entries. The commit path (ApplyDeferredRoutineDrops) performs the real
// removal; ROLLBACK calls this only to discard them (the routines were never
// removed, so there is nothing to undo). M0118-0009 (`stats`).
func (s *BasicSession) TakeDeferredRoutineDrops() []DeferredRoutineDrop {
	out := s.deferRoutineDrops
	s.deferRoutineDrops = nil
	return out
}

// CancelDeferredRoutineDropsToDepth discards deferred DROP FUNCTION entries
// recorded at savepoint depth >= depth, so ROLLBACK TO SAVEPOINT before a
// deferred DROP FUNCTION does not still remove the function at the outer COMMIT.
// Mirrors CancelPendingTableDropsToDepth. M0118-0009 (`stats`).
func (s *BasicSession) CancelDeferredRoutineDropsToDepth(depth int) {
	keep := s.deferRoutineDrops[:0]
	for _, e := range s.deferRoutineDrops {
		if e.SavepointDepth < depth {
			keep = append(keep, e)
		}
	}
	s.deferRoutineDrops = keep
}

// TakeDeferredRoutineDropMatching removes and returns the deferred DROP FUNCTION
// entry whose target routine matches the given schema, name, and signature, or
// nil if none. CREATE FUNCTION calls this so that a DROP-then-CREATE of the same
// signature inside one transaction proceeds cleanly: the deferred drop is
// applied early (the old routine removed now) instead of dropping the freshly
// created routine at COMMIT. M0118-0009 (`stats`).
func (s *BasicSession) TakeDeferredRoutineDropMatching(schema, name, signature string) *catalog.Routine {
	for i, e := range s.deferRoutineDrops {
		r := e.Routine
		if r == nil {
			continue
		}
		rSchema := r.Schema
		if rSchema == "" {
			rSchema = "public"
		}
		wantSchema := schema
		if wantSchema == "" {
			wantSchema = "public"
		}
		if strings.EqualFold(rSchema, wantSchema) && strings.EqualFold(r.Name, name) && r.Signature() == signature {
			s.deferRoutineDrops = append(s.deferRoutineDrops[:i], s.deferRoutineDrops[i+1:]...)
			return r
		}
	}
	return nil
}

// MarkTableActive marks a table OID as currently being mutated by a DML
// statement. Used by the DDL-during-active-query guard. M0097-0023.
func (s *BasicSession) MarkTableActive(oid uint32) {
	if s.activeQueryTables == nil {
		s.activeQueryTables = make(map[uint32]bool)
	}
	s.activeQueryTables[oid] = true
}

// UnmarkTableActive clears the active-DML mark for a table OID.
func (s *BasicSession) UnmarkTableActive(oid uint32) {
	delete(s.activeQueryTables, oid)
}

// IsTableActive reports whether a table OID is currently being mutated
// by an active DML statement in this session.
func (s *BasicSession) IsTableActive(oid uint32) bool {
	return s.activeQueryTables[oid]
}
