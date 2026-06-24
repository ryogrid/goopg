package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// transactionOp is a one-shot operator that mutates explicit
// transaction state for BEGIN/COMMIT/ROLLBACK.
type transactionOp struct {
	plan *planner.Transaction
	ctx  *Context
	done bool
}

func newTransactionOp(p *planner.Transaction) *transactionOp {
	return &transactionOp{plan: p}
}

func (o *transactionOp) Schema() planner.Schema { return nil }

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
	case planner.TxBegin:
		return nil, o.execBegin()
	case planner.TxCommit:
		return nil, o.execCommit()
	case planner.TxRollback:
		return nil, o.execRollback()
	case planner.TxSavepoint:
		return nil, o.execSavepoint()
	case planner.TxRelease:
		return nil, o.execRelease()
	case planner.TxRollbackTo:
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
		parsed, err := mvcc.ParseIsolationLevel(o.plan.IsolationLevel)
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
	if level == mvcc.IsolationSerializable {
		o.ctx.TxnMgr.MarkSerializableModes(tx.Handle, o.plan.ReadOnly, o.plan.Deferrable)
	}
	// PG-parity: for RR/SERIALIZABLE, the snapshot is captured at the first
	// non-BEGIN statement, NOT at BEGIN time. We leave firstSnapshot unset
	// here; dispatchSimpleQueryViaExecutor's SnapshotFor call at the start of
	// each query sets it on the first real statement after BEGIN. For RC, the
	// snapshot is refreshed per-statement anyway, so the timing doesn't matter.
	// M0100-0001 (first-statement snapshot semantics for read-write-unique).
	var snap mvcc.Snapshot
	if level == mvcc.IsolationReadCommitted {
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
	}
	if err := o.ctx.TxnMgr.Commit(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	// M0102-0005: synchronous-replication wait. The xactMarker hook
	// in initdb.Open writes the commit WAL record and flushes locally
	// before TxnMgr.Commit returns; if SyncRep is configured and the
	// session-effective synchronous_commit level is remote_*, block
	// here until enough standbys have acknowledged the commit's LSN.
	// WrittenLSN reads the position just past the commit record (the
	// hook always advances it). NeedsWait short-circuits the cheap
	// path when synchronous_standby_names is empty.
	if o.ctx.SyncRep != nil && o.ctx.WAL != nil &&
		o.ctx.SyncCommitMode != wal.SyncRepOff && o.ctx.SyncRep.NeedsWait() {
		_ = o.ctx.SyncRep.WaitForLSN(o.ctx.Ctx, o.ctx.WAL.WrittenLSN(), o.ctx.SyncCommitMode)
	}
	o.ctx.Session.EndExplicitTransaction()
	if o.ctx.EndLocalTransaction != nil {
		o.ctx.EndLocalTransaction()
	}
	// Clear pending routine drops and truncate undos — they're committed.
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		sess.TakePendingRoutineDrops()
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
	// Undo any DDL creates that happened in this transaction before
	// marking the transaction as aborted.  Must happen BEFORE
	// TxnMgr.Rollback so the catalog DropTable/DropIndex can still
	// find the entries (they're still in the in-memory map).
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas {
		// Undo DDL creates, TRUNCATE page snapshots, and RESTART IDENTITY.
		ProcessRollbackUndos(o.ctx, sess)
		// Restore any routines that were dropped in this transaction.
		for _, r := range sess.TakePendingRoutineDrops() {
			if rs := o.ctx.Catalog.Routines(); rs != nil {
				// orReplace=true: if the key somehow exists (e.g. a concurrent
				// create raced in), overwrite it so the drop is fully reversed.
				_, _ = rs.Create(r, true)
			}
		}
	}
	if err := o.ctx.TxnMgr.Rollback(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.ctx.Session.EndExplicitTransaction()
	if o.ctx.EndLocalTransaction != nil {
		o.ctx.EndLocalTransaction()
	}
	globalRelLockMgr.ReleaseSession(o.ctx.Session)
	undoEnumDDLFromContext(o.ctx)
	o.clearCtxTransaction()
	return nil
}

// rollbackDDLCreate undoes one CREATE TABLE or CREATE INDEX by removing the
// catalog entry, the physical relfile, and — when the M0030 catalog heap
// substrate is available — stamping xmax on the pg_class/pg_attribute rows so
// the startup loader's xmax==0 filter skips them after a crash+restart.
func rollbackDDLCreate(ctx *Context, entry DDLUndoEntry) {
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: entry.RelOID,
		Fork:   storage.MainFork,
	}
	if entry.IsIndex {
		_ = ctx.Catalog.DropIndex(entry.Name)
	} else {
		_ = ctx.Catalog.DropTable(entry.Name)
	}
	if ctx.Pool != nil {
		ctx.Pool.InvalidateRel(rel)
		_ = ctx.Pool.Manager().DropRelation(rel)
	}
	// Stamp xmax on the pg_class / pg_attribute rows so that after a
	// crash+restart the heap loader's xmax==0 filter skips them. Without this,
	// WAL replay restores the HeapInsert records and the table reappears.
	// Stamp in all catalog DBOids (DefaultDBOid + mirror DBOID) so
	// loadUserTablesFromHeap (which reads from cat.DBOID()) also sees xmax.
	if catalogHeapSyncAvailable(ctx) && ctx.Tx.XID != storage.InvalidTransactionID {
		for _, dbOid := range catalogDBOids(ctx) {
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
		SetSequenceCurrentValue(sr.Name, sr.OldCurr)
	}
	// Restore catalog entries for any DROP TABLEs that happened inside savepoints.
	// On full ROLLBACK these are all being undone (the top-level transaction aborts).
	// M0097-0023.
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		for _, drop := range sess.TakePendingDDLDrops() {
			im.RegisterTable(drop.Table)
			for _, idx := range drop.Indexes {
				im.RestoreIndex(idx)
			}
		}
	}
	// Drop any ALTER TABLE {NO} INHERIT changes deferred to COMMIT — a ROLLBACK
	// leaves the inheritance state untouched (and clears the catalog pending-change
	// marks that bypass the plan cache). M0118-0008 (alter-table-4).
	DiscardPendingInheritanceChanges(ctx, sess)
}

func (o *transactionOp) clearCtxTransaction() {
	o.ctx.Tx = mvcc.Transaction{}
	o.ctx.Snap = mvcc.Snapshot{}
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
			inm.RemoveEnumValue(typeName, label)
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
		_ = inm.RenameEnum(r.NewName, r.OldName)
		if created[r.NewName] {
			delete(created, r.NewName)
			created[r.OldName] = true
		}
	}
	// Step 3: Drop types created in this transaction (now at original names).
	for name := range created {
		_ = inm.DropEnum(name, false)
	}
	// Step 4: Drop composite types created via CREATE TYPE … AS (...) in this
	// transaction.  Their pg_type/pg_attribute heap rows carry the aborting
	// XID (MVCC-invisible post-rollback) and the virtual pg_class builder
	// iterates the in-memory registry, so removing the registration below is
	// what makes the aborted composite disappear.  DU-002 slice 244.
	for name := range ctx.PendingCreatedComposites {
		_ = inm.DropCompositeType(name)
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
		for _, drop := range sess.RollbackDDLDropsToDepth(newDepth) {
			im.RegisterTable(drop.Table)
			for _, idx := range drop.Indexes {
				im.RestoreIndex(idx)
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

func (o *setTransactionOp) Schema() planner.Schema  { return nil }
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
	level, err := mvcc.ParseIsolationLevel(o.stmt.IsolationLevel)
	if err != nil {
		return nil, &ExecError{Code: "0A000", Message: err.Error()}
	}
	if serr := o.ctx.Session.SetIsolationLevel(level); serr != nil {
		return nil, &ExecError{Code: "0A000", Message: serr.Error()}
	}
	return nil, EOF
}
