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

func (o *transactionOp) Next() (Row, error) {
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
// catalog entry and physical relfile. The physical file removal mirrors what
// execDropTable does — no WAL record is needed here since the XID stamped on
// the pg_class/pg_attribute rows is now aborted, making those rows invisible
// via MVCC in subsequent sessions. The startup scan's `xmax == 0` filter
// will still see the aborted row until VACUUM removes it; this is a known
// limitation pending pg_xact persistence (M0030-0006 Phase 2).
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
}

func (o *transactionOp) clearCtxTransaction() {
	o.ctx.Tx = mvcc.Transaction{}
	o.ctx.Snap = mvcc.Snapshot{}
}
