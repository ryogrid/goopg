package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// transactionOp is a one-shot operator that mutates explicit
// transaction state for BEGIN/COMMIT/ROLLBACK.
type transactionOp struct {
	plan *optimizer.Transaction
	ctx  *Context
	done bool
}

func newTransactionOp(p *optimizer.Transaction) *transactionOp {
	return &transactionOp{plan: p}
}

func (o *transactionOp) Schema() optimizer.Schema { return nil }

func (o *transactionOp) Open(ctx *Context) error {
	if ctx.TxnMgr == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "transaction statements require TxnMgr in Context"}
	}
	if ctx.Session == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "transaction statements require Session in Context"}
	}
	o.ctx = ctx
	o.done = false
	return nil
}

func (o *transactionOp) Close() error { return nil }

func (o *transactionOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	switch o.plan.Verb {
	case optimizer.TxBegin:
		return nil, o.execBegin()
	case optimizer.TxCommit:
		return nil, o.execCommit()
	case optimizer.TxRollback:
		return nil, o.execRollback()
	case optimizer.TxSavepoint:
		return nil, o.execSavepoint()
	case optimizer.TxRelease:
		return nil, o.execRelease()
	case optimizer.TxRollbackTo:
		return nil, o.execRollbackTo()
	default:
		return nil, &ExecError{Code: "0A000", Pos: o.plan.Pos(), Message: fmt.Sprintf("unsupported transaction verb %d", o.plan.Verb)}
	}
}

func (o *transactionOp) execBegin() error {
	if o.ctx.Session.InExplicitTransaction() {
		// PostgreSQL treats nested BEGIN as a warning + no-op.
		return nil
	}
	level := o.ctx.Session.IsolationLevel()
	// BEGIN ISOLATION LEVEL <level>: use the per-statement level instead of
	// the session default and update the session so subsequent operations in
	// this transaction use the same level.
	if o.plan.IsolationLevel != "" {
		parsed, err := transam.ParseIsolationLevel(o.plan.IsolationLevel)
		if err != nil {
			return &ExecError{Code: "0A000", Pos: o.plan.Pos(), Message: err.Error()}
		}
		_ = o.ctx.Session.SetIsolationLevel(parsed)
		level = parsed
	}
	tx, err := o.ctx.TxnMgr.Begin(level, o.ctx.ProcNum)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	// Record the declared READ ONLY / DEFERRABLE modes on the SSI xact so the
	// snapshot path can apply PostgreSQL's GetSafeSnapshot deferral for a
	// SERIALIZABLE READ ONLY DEFERRABLE transaction (it waits for concurrent
	// writers to drain instead of risking a 40001 abort). No-op for RC/RR.
	// M0118-0001 (read-only-anomaly-3).
	if level == transam.IsolationSerializable {
		o.ctx.TxnMgr.MarkSerializableModes(tx.Handle, o.plan.ReadOnly, o.plan.Deferrable)
	}
	// PG-parity: for RR/SERIALIZABLE, the snapshot is captured at the first
	// non-BEGIN statement, NOT at BEGIN time. We leave firstSnapshot unset
	// here; dispatchSimpleQueryViaExecutor's SnapshotFor call at the start of
	// each query sets it on the first real statement after BEGIN. For RC, the
	// snapshot is refreshed per-statement anyway, so the timing doesn't matter.
	// M0100-0001 (first-statement snapshot semantics for read-write-unique).
	var snap transam.Snapshot
	if level == transam.IsolationReadCommitted {
		snap, err = o.ctx.TxnMgr.SnapshotFor(tx)
		if err != nil {
			_ = o.ctx.TxnMgr.Rollback(tx)
			return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
		}
	}
	o.ctx.Session.BeginExplicitTransaction(tx, snap)
	o.ctx.Session.SetReadOnlyTxn(o.plan.ReadOnly)
	o.ctx.Tx = tx
	o.ctx.Snap = snap.Clone()
	if o.ctx.BeginLocalTransaction != nil {
		o.ctx.BeginLocalTransaction()
	}
	return nil
}

func (o *transactionOp) execCommit() error {
	tx, _, ok := o.ctx.Session.CurrentTransaction()
	if !ok {
		// PostgreSQL returns a warning for COMMIT outside tx block.
		return nil
	}
	// Run deferred FK constraint checks (DEFERRABLE INITIALLY DEFERRED).
	// M0096-0011: check before committing so a violation aborts the transaction.
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		checks := sess.TakeDeferredFKChecks()
		if len(checks) > 0 && o.ctx.Pool != nil {
			if err := runAllDeferredFKChecks(o.ctx, checks); err != nil {
				// Rollback the transaction on constraint violation.
				_ = o.ctx.TxnMgr.Rollback(tx)
				o.ctx.Session.EndExplicitTransaction()
				undoEnumDDLFromContext(o.ctx)
				o.clearCtxTransaction()
				return err
			}
		}
		// Deferred UNIQUE/PK constraint checks (DEFERRABLE INITIALLY DEFERRED, or
		// made deferred by SET CONSTRAINTS … DEFERRED). 0119-0004 (deferred-unique).
		if err := RunDeferredUniqueChecks(o.ctx, sess); err != nil {
			_ = o.ctx.TxnMgr.Rollback(tx)
			o.ctx.Session.EndExplicitTransaction()
			undoEnumDDLFromContext(o.ctx)
			o.clearCtxTransaction()
			return err
		}
		// Deferred EXCLUDE constraint checks (DEFERRABLE INITIALLY DEFERRED, or
		// made deferred by SET CONSTRAINTS … DEFERRED). 0119-0004 (deferred-exclusion).
		if err := RunDeferredExclusionChecks(o.ctx, sess); err != nil {
			_ = o.ctx.TxnMgr.Rollback(tx)
			o.ctx.Session.EndExplicitTransaction()
			undoEnumDDLFromContext(o.ctx)
			o.clearCtxTransaction()
			return err
		}
	}
	// M0134-0072: ON COMMIT {DELETE ROWS|DROP} pass for temp tables. Mirrors
	// xact.c PreCommit_on_commit_actions (:2311), which runs BEFORE the SSI
	// pre-commit check (:2339) and RecordTransactionCommit (:2365). A failure
	// (e.g. the ON COMMIT FK 0A000) aborts the commit exactly like the deferred
	// checks above. execRollback is a deliberate no-op for this pass — PG's
	// AtAbort has no ON-COMMIT action, and stale registrations left by a
	// rolled-back CREATE are skipped at the next commit (OIDs are monotonic).
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		if err := RunOnCommitActions(o.ctx, sess); err != nil {
			// A failed COMMIT aborts the transaction exactly like a ROLLBACK
			// (PG's AbortTransaction runs on COMMIT failure too), so DDL
			// creates made in the block — including the temp tables whose ON
			// COMMIT action just failed — must be undone BEFORE
			// TxnMgr.Rollback (ProcessRollbackUndos needs the catalog entries
			// live to find them). Without this the tables persist with a stale
			// ON COMMIT registration and a later commit fires the same FK
			// error again (temp.sql's temptest3/temptest4 case). M0134-0072.
			ProcessRollbackUndos(o.ctx, sess)
			_ = o.ctx.TxnMgr.Rollback(tx)
			o.ctx.Session.EndExplicitTransaction()
			undoEnumDDLFromContext(o.ctx)
			o.clearCtxTransaction()
			return err
		}
	}
	// M0104-0007: SSI pre-commit dangerous-structure check for SERIALIZABLE.
	// Runs BEFORE TxnMgr.Commit so a detected rw-cycle can be translated to
	// SQLSTATE 40001 and rolled back here without burning a commit record.
	// Helper returns nil for RC/RR and write-less SERIALIZABLE xacts.
	if ssiErr := ssiPreCommitCheck(o.ctx, tx); ssiErr != nil {
		_ = o.ctx.TxnMgr.Rollback(tx)
		o.ctx.Session.EndExplicitTransaction()
		undoEnumDDLFromContext(o.ctx)
		o.clearCtxTransaction()
		if ee, ok := ssiErr.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = o.plan.Pos()
		}
		return ssiErr
	}
	// M0118-0008: apply DROP INDEX removals deferred to COMMIT (the index's
	// pg_class row + lock were kept visible to other sessions until now). Must
	// run BEFORE TxnMgr.Commit so the drop WAL precedes the commit record and
	// the pg_class xmax stamp uses the still-live XID.
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		ApplyPendingIndexDrops(o.ctx, sess)
		// M0118-0008: register ATTACH PARTITION deferred to COMMIT (the new
		// partition was invisible to other sessions until now).
		ApplyPendingPartitionAttaches(o.ctx, sess)
		// M0118-0008: apply ALTER TABLE {NO} INHERIT deferred to COMMIT (the
		// inheritance link change was invisible to other sessions until now).
		ApplyPendingInheritanceChanges(o.ctx, sess)
		// M0118-0008 (alter-table-4 perm 3): apply DROP TABLE removals deferred to
		// COMMIT (the table's catalog row + the dropper's AccessExclusiveLock were
		// kept visible to other sessions until now).
		ApplyPendingTableDrops(o.ctx, sess)
		// M0118-0009 (`stats`): apply DROP FUNCTION removals deferred to COMMIT
		// (the routine + its cumulative function-stats were kept visible/callable
		// to other sessions until now).
		ApplyDeferredRoutineDrops(o.ctx, sess)
	}
	if err := o.ctx.CommitTransaction(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.clearPgClassRowMarks(tx)
	// M0102-0005: synchronous-replication wait. The xactMarker hook
	// in initdb.Open writes the commit WAL record and flushes locally
	// before TxnMgr.Commit returns; if SyncRep is configured and the
	// session-effective synchronous_commit level is remote_*, block
	// here until enough standbys have acknowledged the commit's LSN.
	// WrittenLSN reads the position just past the commit record (the
	// hook always advances it). NeedsWait short-circuits the cheap
	// path when synchronous_standby_names is empty.
	if o.ctx.SyncRep != nil && o.ctx.WAL != nil &&
		o.ctx.SyncCommitMode != xlog.SyncRepOff && o.ctx.SyncRep.NeedsWait() {
		_ = o.ctx.SyncRep.WaitForLSN(o.ctx.Ctx, o.ctx.WAL.WrittenLSN(), o.ctx.SyncCommitMode)
	}
	// M0118-0009 (`stats`, rung 7; design 0118-0131): fold this transaction's
	// staged relation-stat counters (tuples_inserted/_updated/_deleted + live/dead
	// deltas) into the backend's pending counters using commit math
	// (AtEOXact_PgStat_Relations). Non-transactional scan counters were already
	// applied at scan time. No-op for a transaction that staged nothing.
	CommitRelStats(o.ctx)
	o.ctx.Session.EndExplicitTransaction()
	if o.ctx.EndLocalTransaction != nil {
		o.ctx.EndLocalTransaction(true)
	}
	// Clear pending truncate/sequence undos — they're committed.
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		sess.TakePendingTruncates()
		sess.TakePendingSeqRestores()
	}
	globalRelLockMgr.ReleaseSession(o.ctx.Session)
	o.clearCtxTransaction()
	return nil
}

func (o *transactionOp) execRollback() error {
	tx, _, ok := o.ctx.Session.CurrentTransaction()
	if !ok {
		// PostgreSQL returns a warning for ROLLBACK outside tx block.
		return nil
	}
	// ON COMMIT actions never fire on ROLLBACK: PG's AtAbort has no ON-COMMIT
	// pass (only AtEOXact_on_commit_actions prunes entries in the abort case,
	// tablecmds.c:19427). goopg deliberately leaves the per-session
	// registrations untouched here — a stale entry left by a rolled-back CREATE
	// is skipped at the next commit because the relation no longer exists (OIDs
	// are monotonic and never reused), and an entry from an earlier committed
	// transaction must keep firing (a DELETE ROWS table fires at every commit).
	// M0134-0072.
	// Undo any DDL creates that happened in this transaction before
	// marking the transaction as aborted.  Must happen BEFORE
	// TxnMgr.Rollback so the catalog DropTable/DropIndex can still
	// find the entries (they're still in the in-memory map).
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		// Undo DDL creates, TRUNCATE page snapshots, and RESTART IDENTITY.
		ProcessRollbackUndos(o.ctx, sess)
		// M0118-0009 (`stats`): discard DROP FUNCTION/PROCEDURE drops deferred to COMMIT —
		// the routines were never removed from the registry (kept visible until
		// commit), so ROLLBACK only needs to drop the deferred entries.
		sess.TakeDeferredRoutineDrops()
		// fk-partitioned-1 (design 0118-0120): a deferred ATTACH PARTITION never
		// registers on ROLLBACK, so drop any in-flight-attach FK markers it set
		// (the partition stays unattached; a concurrent DELETE must not keep
		// waiting on this aborted attach — IsXIDActive already guards that, but
		// clear the markers eagerly to keep the map bounded).
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			for _, a := range sess.TakePendingPartitionAttaches() {
				im.ClearPendingAttachXID(a.ChildOID)
			}
		}
	}
	if err := o.ctx.TxnMgr.Rollback(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.clearPgClassRowMarks(tx)
	// M0118-0009 (`stats`, rung 7; design 0118-0131): fold this transaction's
	// staged relation-stat counters into pending using abort math — aborted
	// inserts/updates become dead tuples; aborted deletes are a no-op on live/dead;
	// attempted insert/update/delete totals still count
	// (AtEOXact_PgStat_Relations, abort case).
	AbortRelStats(o.ctx)
	o.ctx.Session.EndExplicitTransaction()
	if o.ctx.EndLocalTransaction != nil {
		o.ctx.EndLocalTransaction(false)
	}
	globalRelLockMgr.ReleaseSession(o.ctx.Session)
	undoEnumDDLFromContext(o.ctx)
	o.clearCtxTransaction()
	return nil
}

// clearPgClassRowMarks drops any explicit pg_class rowmarks this transaction
// recorded (SELECT … FROM pg_class … FOR …) now that it has finished, so a
// later in-place catalog updater no longer sees them as held. Keyed by the
// transaction's top-level id (the common case; savepoint sub-XID rowmarks, which
// no current spec uses, are left behind but are harmless — WaitForXID returns
// immediately once that sub-XID is no longer active). Harmless no-op when none
// were recorded. Design 0118-0113 (intra-grant-inplace).
func (o *transactionOp) clearPgClassRowMarks(tx transam.Transaction) {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok || tx.XID == storage.InvalidTransactionID {
		return
	}
	im.ClearPgClassRowMarksForXID(uint32(tx.XID))
}

// rollbackDDLCreate undoes one CREATE TABLE or CREATE INDEX by removing the
// catalog entry, the physical relfile, and — when the M0030 catalog heap
// substrate is available — stamping xmax on the pg_class/pg_attribute rows so
// the startup loader's xmax==0 filter skips them after a crash+restart.
func rollbackDDLCreate(ctx *Context, entry DDLUndoEntry) {
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	relDBOid := catalog.DefaultDBOid
	if !entry.IsIndex && dbOid != catalog.DefaultDBOid {
		// A distinct-dbOid connection's table files live under its own
		// base/<dbOid> directory (catalog.InMemory.RelFileNode routes by
		// Table.DBOid) — drop the file where it was actually created.
		// M0122-0007 4e follow-up 39.
		relDBOid = dbOid
	}
	rel := storage.RelFileNode{
		DBOid:  relDBOid,
		RelOid: entry.RelOID,
		Fork:   storage.MainFork,
	}
	if entry.IsIndex {
		_ = ctx.Catalog.DropIndex(entry.Name, dbOid)
	} else if entry.ShadowedTable != nil {
		// This CREATE recreated a name whose collision check was waived
		// because it matched a same-transaction deferred DROP (M0134-0023):
		// restore the table it displaced instead of leaving the slot empty —
		// a bare DropTable here would lose the original already-committed
		// table entirely. Mirrors the TempTableShadows restore precedent
		// (restoreTempShadow / RegisterTable, operators_ddl.go).
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			im.RegisterTable(entry.ShadowedTable, dbOid)
		} else {
			_ = ctx.Catalog.DropTable(entry.Name, dbOid)
		}
	} else {
		_ = ctx.Catalog.DropTable(entry.Name, dbOid)
	}
	if ctx.Pool != nil {
		ctx.Pool.InvalidateRel(rel)
		_ = ctx.Pool.Manager().DropRelation(rel)
	}
	// Stamp xmax on the pg_class / pg_attribute rows so that after a
	// crash+restart the heap loader's xmax==0 filter skips them. Without this,
	// WAL replay restores the HeapInsert records and the table reappears.
	// A table's rows follow tableCatalogDBOids (a distinct database's rows
	// live only in its own catalog heap, follow-up 39); index rows stay on
	// the DefaultDBOid+mirror pair (index writes are still pinned there).
	if catalogHeapSyncAvailable(ctx) && ctx.Tx.XID != storage.InvalidTransactionID {
		stampOids := tableCatalogDBOids(ctx)
		if entry.IsIndex {
			stampOids = catalogDBOids(ctx)
		}
		for _, dbOid := range stampOids {
			deleteCatalogRowsForOID(ctx, dbOid, entry.RelOID, ctx.Tx.XID)
		}
	}
}

// ProcessRollbackUndos runs all in-memory undo actions stored in sess:
//  1. DDL creates (CREATE TABLE / CREATE INDEX) — remove from catalog + relfile
//  2. TRUNCATE page snapshots — restore heap and index pages to pre-truncate state
//  3. RESTART IDENTITY sequence counter restores
//
// Exported so dispatch.go's TxRollback shortcut path can call it with the
// production-server Context (which has Pool set).  Must be called BEFORE
// TxnMgr.Rollback so catalog.DropTable/DropIndex can still find entries.
func ProcessRollbackUndos(ctx *Context, sess *BasicSession) {
	for _, entry := range sess.TakePendingDDLCreates() {
		rollbackDDLCreate(ctx, entry)
	}
	if ctx.Pool != nil {
		for _, entry := range sess.TakePendingTruncates() {
			restoreTruncateUndo(ctx, entry)
		}
	}
	for _, sr := range sess.TakePendingSeqRestores() {
		SetSequenceCurrentValue(sr.Name, sr.OldCurr, sr.DBOid)
	}
	// Restore catalog entries for any DROP TABLEs that happened inside savepoints.
	// On full ROLLBACK these are all being undone (the top-level transaction aborts).
	// M0097-0023.
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
		for _, drop := range sess.TakePendingDDLDrops() {
			im.RegisterTable(drop.Table, dbOid)
			for _, idx := range drop.Indexes {
				im.RestoreIndex(idx, dbOid)
			}
		}
	}
	// Drop any ALTER TABLE {NO} INHERIT changes deferred to COMMIT — a ROLLBACK
	// leaves the inheritance state untouched (and clears the catalog pending-change
	// marks that bypass the plan cache). M0118-0008 (alter-table-4).
	DiscardPendingInheritanceChanges(ctx, sess)
}

func (o *transactionOp) clearCtxTransaction() {
	o.ctx.Tx = transam.Transaction{}
	o.ctx.Snap = transam.Snapshot{}
	if o.ctx.Session != nil {
		o.ctx.Session.SetReadOnlyTxn(false)
	}
	// Clear pending enum values so that after COMMIT/ROLLBACK the
	// write-back in dispatch.go does not restore stale pending labels
	// and incorrectly block usage of committed enum values.
	o.ctx.PendingEnumValues = nil
	o.ctx.PendingEnumRenames = nil
	o.ctx.PendingCreatedEnums = nil
	o.ctx.PendingCreatedComposites = nil
	o.ctx.PendingCreatedRangeTypes = nil
}

// UndoEnumDDLOnAbort reverses enum/composite-type DDL (CREATE TYPE ... AS
// ENUM/composite, ALTER TYPE ... ADD VALUE/RENAME TO) recorded in ctx.Pending*
// for an aborting message-scoped autocommit batch. Exported so dispatch.go's
// implicit-batch abort defer can call it alongside ProcessRollbackUndos, the
// same way execRollback calls the unexported undoEnumDDLFromContext directly
// (same package). root-0024 residual, M0110-0001.
func UndoEnumDDLOnAbort(ctx *Context) {
	undoEnumDDLFromContext(ctx)
}

// undoEnumDDLFromContext reverses enum DDL (ADD VALUE, RENAME TO, CREATE TYPE AS ENUM)
// recorded in ctx.  Must be called before clearCtxTransaction() on ROLLBACK.  M0097-0022.
func undoEnumDDLFromContext(ctx *Context) {
	inm, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	// Step 1: Remove any enum values added via ALTER TYPE … ADD VALUE in this tx.
	// Do this before undoing renames so type names are still at current (renamed) values.
	for typeName, labels := range ctx.PendingEnumValues {
		for label := range labels {
			inm.RemoveEnumValue(typeName, label, ctx.CurrentDatabaseOid)
		}
	}
	// Step 2: Undo renames in reverse order.  Also reverse their effect on the
	// created-set so that after all renames are undone, 'created' holds current
	// (post-undo) names — the keys to pass to DropEnum.
	created := make(map[string]bool, len(ctx.PendingCreatedEnums))
	for k, v := range ctx.PendingCreatedEnums {
		created[k] = v
	}
	for i := len(ctx.PendingEnumRenames) - 1; i >= 0; i-- {
		r := ctx.PendingEnumRenames[i]
		_ = inm.RenameEnum(r.NewName, r.OldName, ctx.CurrentDatabaseOid)
		if created[r.NewName] {
			delete(created, r.NewName)
			created[r.OldName] = true
		}
	}
	// Step 3: Drop types created in this transaction (now at original names).
	for name := range created {
		_ = inm.DropEnum(name, false, ctx.CurrentDatabaseOid)
	}
	// Step 4: Drop composite types created via CREATE TYPE … AS (...) in this
	// transaction.  Their pg_type/pg_attribute heap rows carry the aborting
	// XID (MVCC-invisible post-rollback) and the virtual pg_class builder
	// iterates the in-memory registry, so removing the registration below is
	// what makes the aborted composite disappear.  DU-002 slice 244.
	for name := range ctx.PendingCreatedComposites {
		_ = inm.DropCompositeType(name, ctx.CurrentDatabaseOid)
	}
	// Step 5: Drop range types created via CREATE TYPE … AS RANGE in this
	// transaction. Their pg_type heap row (typtype='r', plus the
	// auto-generated multirange typtype='m') carries the aborting XID
	// (MVCC-invisible post-rollback); removing the in-memory registration is
	// what makes the aborted range type disappear from LookupRangeType and
	// the virtual pg_class builder. Range types had no rollback-undo
	// tracking at all until this step was added. M0122-0007 4e follow-up.
	for name := range ctx.PendingCreatedRangeTypes {
		_ = inm.DropRangeType(name, ctx.CurrentDatabaseOid)
	}
}

// execSavepoint allocates a sub-transaction XID and pushes the savepoint
// onto the session stack. All heap mutations after this point use the
// sub-XID so they can be selectively aborted by execRollbackTo.
func (o *transactionOp) execSavepoint() error {
	if !o.ctx.Session.InExplicitTransaction() {
		return &ExecError{Code: "25P01", Pos: o.plan.Pos(),
			Message: "SAVEPOINT can only be used in transaction blocks"}
	}
	sess, ok := o.ctx.Session.(*BasicSession)
	if !ok {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "savepoints require BasicSession"}
	}
	// Materialise the top-level XID BEFORE allocating the sub-XID. Transaction
	// XIDs are assigned lazily (only on first write), so a SAVEPOINT issued
	// before any write — e.g. BEGIN; SAVEPOINT f; UPDATE … (aborted-keyrevoke) —
	// would otherwise register the sub-XID with parent = 0. A zero parent breaks
	// cross-session resolution: another backend's TopLevelXid(subxid) returns 0,
	// so its snapshot cannot map the subxid to a running top-level xact and wrongly
	// treats the savepoint's uncommitted writes as visible (and a conflicting
	// row-lock waiter never blocks). delete-abort-savept escaped this only because
	// its FOR KEY SHARE assigned the top-level XID before the savepoint. Eager
	// assignment at SAVEPOINT keeps the subxid→parent link correct from birth;
	// it is a strict no-op once a top-level XID already exists. M0118-0009.
	if sess.tx.XID == storage.InvalidTransactionID {
		if err := o.ctx.MaterializeWriterXID(); err != nil {
			return err
		}
	}
	subXid, err := o.ctx.TxnMgr.AllocateSubXid(sess.tx.XID)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	snap, err := o.ctx.TxnMgr.SnapshotFor(sess.tx)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	sess.PushSavepoint(o.plan.Name, snap, subXid)
	o.ctx.Tx.XID = subXid
	return nil
}

// execRelease marks the named savepoint and all inner savepoints as
// committed and restores the effective writer XID to the parent level.
func (o *transactionOp) execRelease() error {
	if !o.ctx.Session.InExplicitTransaction() {
		return &ExecError{Code: "25P01", Pos: o.plan.Pos(),
			Message: "RELEASE SAVEPOINT can only be used in transaction blocks"}
	}
	sess, ok := o.ctx.Session.(*BasicSession)
	if !ok {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "savepoints require BasicSession"}
	}
	if _, err := sess.ReleaseSavepoint(o.plan.Name); err != nil {
		return &ExecError{Code: "3B001", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.ctx.Tx.XID = sess.EffectiveWriterXID()
	return nil
}

// execRollbackTo aborts all heap mutations in the named savepoint (and
// any inner savepoints), marks their sub-XIDs as aborted in the Manager
// so subsequent scans skip those tuples, then pushes a fresh savepoint
// entry with a new sub-XID for continued use.
func (o *transactionOp) execRollbackTo() error {
	if !o.ctx.Session.InExplicitTransaction() {
		return &ExecError{Code: "25P01", Pos: o.plan.Pos(),
			Message: "ROLLBACK TO SAVEPOINT can only be used in transaction blocks"}
	}
	sess, ok := o.ctx.Session.(*BasicSession)
	if !ok {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "savepoints require BasicSession"}
	}
	snap, err := o.ctx.TxnMgr.SnapshotFor(sess.tx)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	newSubXid, err := o.ctx.TxnMgr.AllocateSubXid(sess.tx.XID)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	aborted, _, err := sess.RollbackToSavepoint(o.plan.Name, snap, newSubXid)
	if err != nil {
		return &ExecError{Code: "3B001", Pos: o.plan.Pos(), Message: err.Error()}
	}
	for _, entry := range aborted {
		if entry.SubXid != 0 {
			o.ctx.TxnMgr.MarkSubxactAborted(entry.SubXid)
		}
	}
	// Restore catalog entries for DROP TABLEs performed inside the rolled-back
	// savepoint. Physical files were already deleted (idempotent on re-drop).
	// M0097-0023.
	newDepth := sess.SavepointDepth()
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		dbOid := catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)
		for _, drop := range sess.RollbackDDLDropsToDepth(newDepth) {
			im.RegisterTable(drop.Table, dbOid)
			for _, idx := range drop.Indexes {
				im.RestoreIndex(idx, dbOid)
			}
		}
	}
	// M0118-0008: discard DROP INDEX removals and ATTACH PARTITION registrations
	// deferred inside the rolled-back savepoint so they are not applied at the
	// outer COMMIT.
	sess.CancelPendingIndexDropsToDepth(newDepth)
	sess.CancelPendingPartitionAttachesToDepth(newDepth)
	// Discard DROP TABLE removals deferred inside the rolled-back savepoint so
	// they are not applied at the outer COMMIT. M0118-0008 (alter-table-4).
	sess.CancelPendingTableDropsToDepth(newDepth)
	// Discard DROP FUNCTION removals deferred inside the rolled-back savepoint so
	// the function survives the outer COMMIT. M0118-0009 (`stats`).
	sess.CancelDeferredRoutineDropsToDepth(newDepth)
	// Discard deferred ALTER TABLE {NO} INHERIT changes recorded inside the
	// rolled-back savepoint, clearing the matching catalog pending-change marks.
	// M0118-0008 (alter-table-4).
	if cancelled := sess.CancelPendingInheritanceChangesToDepth(newDepth); cancelled > 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			for i := 0; i < cancelled; i++ {
				im.UnmarkInheritanceChangePending()
			}
		}
	}
	o.ctx.Tx.XID = newSubXid
	return nil
}

// setTransactionOp applies a SET [LOCAL] TRANSACTION statement.
// Currently only ISOLATION LEVEL is acted upon; READ ONLY/WRITE and
// DEFERRABLE are accepted by the parser but ignored here.
type setTransactionOp struct {
	stmt *parser.SetTransactionStmt
	ctx  *Context
	done bool
}

func newSetTransactionOp(s *parser.SetTransactionStmt) *setTransactionOp {
	return &setTransactionOp{stmt: s}
}

func (o *setTransactionOp) Schema() optimizer.Schema  { return nil }
func (o *setTransactionOp) Open(ctx *Context) error { o.ctx = ctx; return nil }
func (o *setTransactionOp) Close() error            { return nil }

func (o *setTransactionOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.stmt.IsolationLevel == "" || o.ctx == nil || o.ctx.Session == nil {
		return nil, EOF
	}
	level, err := transam.ParseIsolationLevel(o.stmt.IsolationLevel)
	if err != nil {
		return nil, &ExecError{Code: "0A000", Message: err.Error()}
	}
	if serr := o.ctx.Session.SetIsolationLevel(level); serr != nil {
		return nil, &ExecError{Code: "0A000", Message: serr.Error()}
	}
	return nil, EOF
}

// setConstraintsOp applies a SET CONSTRAINTS { ALL | name [, ...] }
// { DEFERRED | IMMEDIATE } statement: it records the deferral override on the
// session and, for an IMMEDIATE request, runs any already-queued deferred FK
// checks the change makes immediate right away (so a violation raises at this
// statement, as PG does). 0119-0004.
type setConstraintsOp struct {
	stmt *parser.SetConstraintsStmt
	ctx  *Context
	done bool
}

func newSetConstraintsOp(s *parser.SetConstraintsStmt) *setConstraintsOp {
	return &setConstraintsOp{stmt: s}
}

func (o *setConstraintsOp) Schema() optimizer.Schema  { return nil }
func (o *setConstraintsOp) Open(ctx *Context) error { o.ctx = ctx; return nil }
func (o *setConstraintsOp) Close() error            { return nil }

func (o *setConstraintsOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.ctx == nil || o.ctx.Session == nil {
		return nil, EOF
	}
	sess, ok := o.ctx.Session.(*BasicSession)
	if !ok {
		return nil, EOF
	}
	// Outside an explicit transaction block the surrounding single-statement
	// transaction ends immediately, so the override has no lasting effect — PG
	// treats this as a near no-op. Record nothing to avoid leaking state across
	// autocommit statements.
	if !sess.InExplicitTransaction() {
		return nil, EOF
	}
	if o.stmt.All {
		sess.SetConstraintsAll(o.stmt.Deferred)
	} else {
		sess.SetConstraintsNamed(o.stmt.Names, o.stmt.Deferred)
	}
	// IMMEDIATE: run any pending deferred FK + UNIQUE checks the change makes
	// immediate right away, so a violation raises at this statement (PG semantics).
	if !o.stmt.Deferred {
		checks := sess.TakeDeferredFKChecksMatching(o.stmt.All, o.stmt.Names)
		if err := runAllDeferredFKChecks(o.ctx, checks); err != nil {
			return nil, err
		}
		uChecks := sess.TakeDeferredUniqueChecksMatching(o.stmt.All, o.stmt.Names)
		if err := runAllDeferredUniqueChecks(o.ctx, uChecks); err != nil {
			return nil, err
		}
		xChecks := sess.TakeDeferredExclusionChecksMatching(o.stmt.All, o.stmt.Names)
		if err := runAllDeferredExclusionChecks(o.ctx, xChecks); err != nil {
			return nil, err
		}
	}
	return nil, EOF
}
