package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
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
	tx, err := o.ctx.TxnMgr.Begin(o.ctx.Session.IsolationLevel())
	if err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	snap, err := o.ctx.TxnMgr.SnapshotFor(tx)
	if err != nil {
		_ = o.ctx.TxnMgr.Rollback(tx)
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.ctx.Session.BeginExplicitTransaction(tx, snap)
	o.ctx.Tx = tx
	o.ctx.Snap = snap.Clone()
	return nil
}

func (o *transactionOp) execCommit() error {
	tx, _, ok := o.ctx.Session.CurrentTransaction()
	if !ok {
		// PostgreSQL returns a warning for COMMIT outside tx block.
		return nil
	}
	if err := o.ctx.TxnMgr.Commit(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.ctx.Session.EndExplicitTransaction()
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
		for _, entry := range sess.TakePendingDDLCreates() {
			rollbackDDLCreate(o.ctx, entry)
		}
	}
	if err := o.ctx.TxnMgr.Rollback(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.ctx.Session.EndExplicitTransaction()
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
	if catalogHeapSyncAvailable(ctx) && ctx.Tx.XID != storage.InvalidTransactionID {
		deleteCatalogRowsForOID(ctx, entry.RelOID, ctx.Tx.XID)
	}
}

func (o *transactionOp) clearCtxTransaction() {
	o.ctx.Tx = mvcc.Transaction{}
	o.ctx.Snap = mvcc.Snapshot{}
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
	o.ctx.Tx.XID = newSubXid
	return nil
}
