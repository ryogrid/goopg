package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
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
	if err := o.ctx.TxnMgr.Rollback(tx); err != nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: err.Error()}
	}
	o.ctx.Session.EndExplicitTransaction()
	o.clearCtxTransaction()
	return nil
}

func (o *transactionOp) clearCtxTransaction() {
	o.ctx.Tx = mvcc.Transaction{}
	o.ctx.Snap = mvcc.Snapshot{}
}
