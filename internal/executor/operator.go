package executor

import (
	"errors"

	"github.com/goopg/goopg/internal/planner"
)

// EOF is the sentinel returned by Operator.Next when no more rows
// remain. Operators must keep returning EOF on subsequent Next calls.
var EOF = errors.New("executor: end of stream")

// Operator is the iterator interface every executor node implements.
//
// Lifecycle:
//
//	Open(ctx) -> Next ... Next -> EOF -> Close
//
// Open prepares any per-statement state (pinning resources,
// pre-computing constant subexpressions). Next advances by one row
// and returns either (row, nil) or (nil, EOF) at end of stream. Close
// releases resources; Close MUST be called even after Next returned
// an error.
type Operator interface {
	Open(ctx *Context) error
	Next() (Row, error)
	Close() error
	Schema() planner.Schema
}

// RowCounter is implemented by DML operators that report a
// post-execution affected-row count (Insert, Update, Delete). The
// wire-protocol path uses this to build the canonical
// `INSERT 0 N`/`UPDATE N`/`DELETE N` CommandComplete tag.
type RowCounter interface {
	RowsAffected() int64
}

// BorrowSemantics describes whether an Operator's Next() returns
// a Row the caller may keep across the next Next() call
// (`OwnedRow`) or whose contents are invalidated by the next
// Next() (`BorrowedRow`).
//
// The default is `OwnedRow`. The post-build walker
// `setBorrowSemanticsRec` flips the bit to `BorrowedRow` on
// Operators whose parent is one of `filterOp` / `projectOp` /
// `limitOp` / `outputOp` — pipeline-internal consumers that read
// each row before pulling the next one. Operators that retain
// rows (sort, hash-build, materialize, aggregate) leave the
// child at `OwnedRow` so the row stays valid for retention.
//
// (M0054-0005a-followup; design doc 0054-0002 §4.2.)
type BorrowSemantics int

const (
	OwnedRow BorrowSemantics = iota
	BorrowedRow
)

// Borrowable is the optional capability advertised by Operators
// that can return BorrowedRows. Plain Operators stay at OwnedRow
// (the default — no `SetBorrow` method needed). An Operator that
// implements Borrowable promises:
//
//   - When SetBorrow(BorrowedRow) has been called, Next() may
//     return a row whose backing storage is invalidated by the
//     subsequent Next() call. The caller must consume the row
//     before pulling the next one.
//   - When SetBorrow(OwnedRow) has been called (or never called),
//     Next() returns a row the caller may retain freely.
//
// (M0054-0005a-followup; design doc 0054-0002 §4.2.)
type Borrowable interface {
	SetBorrow(BorrowSemantics)
}

// setChildBorrow flips an operator's borrow contract when the
// operator implements Borrowable. Unwraps `instrumentedOp` so
// EXPLAIN ANALYZE wiring does not block the propagation. No-op
// when the operator does not advertise Borrowable.
// (M0054-0005a-followup.)
func setChildBorrow(op Operator, s BorrowSemantics) {
	if b, ok := op.(Borrowable); ok {
		b.SetBorrow(s)
		return
	}
	// instrumentedOp wraps the underlying operator; unwrap so
	// the borrow flag reaches the real implementation.
	if u, ok := op.(interface{ underlying() Operator }); ok {
		setChildBorrow(u.underlying(), s)
	}
}
