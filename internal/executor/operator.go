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
	Next() (TupleSlot, error)
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
// The default is `OwnedRow`. The post-build walker propagation
// (`setChildBorrow` from Build paths in `executor.go`) flips the
// bit to `BorrowedRow` on Operators whose parent is in the
// borrow-safe class.
//
// # Operator class matrix (M0059-0001)
//
// Class 1 — pass-through. Reads each row, hands it back up, then
// pulls the next. Cannot retain. Safe to receive AND emit
// borrowed rows.
//   - filterOp        (operators.go)
//   - limitOp         (operators.go; LIMIT/OFFSET)
//   - projectOp       (operators.go; copies input cells into o.out)
//
// Class 2 — compute-only / streaming consumer. Reads each input
// row, computes per-row state, releases the input before
// pulling next. Safe to receive borrowed rows; may emit either
// kind depending on output buffer reuse.
//   - aggregateOp's input drain (operators_join_agg.go) —
//     copies group keys into a fresh Row before retaining; the
//     input row itself is released after applyAgg.
//   - windowOp                  (operators_window.go)
//   - nestedLoopIndexJoinOp's outer input (operators_nljoin.go) —
//     concatenates outer+inner into o.joinBuf per Next.
//
// Class 3 — retaining / materializing. Holds rows across Next()
// calls. MUST receive owned rows (default OwnedRow). Forces its
// child back to OwnedRow regardless of grandparent's class.
//   - sortOp           (operators.go; appends to o.rows[])
//   - joinOp           (operators_join_agg.go; build-side hash, lazyHash)
//   - multiHashJoinOp  (multi_hash_join.go; per-step hash tables)
//   - SetOpAll / RecursiveUnion (operators.go; UNION ALL retains)
//   - hashAggregateOp's group-state retention (when present)
//
// Leaf scans — class 2 emit. Decode-into a per-Next() buffer and
// either return it directly (BorrowedRow) or `cloneRow` it
// (OwnedRow).
//   - seqScanOp        (operators_storage.go)
//   - indexScanOp      (operators_index.go) — currently
//     pre-materialises into o.rows[] at Open(), so the rows are
//     stable across Next() and SetBorrow is a no-op for this op.
//   - indexOnlyScanOp  (operators_indexonly.go) — same as above.
//   - copyToOp         (copy.go) — terminal sink, doesn't emit.
//
// # Propagation rules
//
// - A class-1 parent: child gets BorrowedRow.
// - A class-2 parent: child gets BorrowedRow IF the parent's
//   own consumer allows; else the parent's SetBorrow chain
//   short-circuits.
// - A class-3 parent: child stays at OwnedRow (default). The
//   class-3 op MUST NOT call SetBorrow on its child; doing so
//   would invalidate retained rows. Tests in
//   `borrow_test.go::TestRetentionBoundaryStaysOwned*` pin this.
//
// (M0054-0005a-followup; M0059-0001 — design doc 0059-0001-
// borrowrow-volcano-row-lifetime-optimization.md.)
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
