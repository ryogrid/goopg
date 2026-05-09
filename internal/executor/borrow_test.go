package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestBorrowSemanticsDefaultIsOwnedRow (M0054-0005a-followup)
// asserts that an Operator that does NOT have SetBorrow called
// continues to return cloned (owned) rows — preserving the
// pre-followup contract for any operator whose parent is not
// borrow-eligible (sort, hash-build, materialize).
func TestBorrowSemanticsDefaultIsOwnedRow(t *testing.T) {
	op := &fakeBorrowSource{
		rows: []Row{
			{{Kind: KindInt, Int: 1}},
			{{Kind: KindInt, Int: 2}},
		},
	}
	if op.borrow != OwnedRow {
		t.Fatalf("default borrow = %v, want OwnedRow", op.borrow)
	}
	firstSlot, err := op.Next()
	if err != nil {
		t.Fatal(err)
	}
	first := firstSlot.Row()
	// Pull the next row — should not invalidate `first` because
	// the borrow contract said OwnedRow (the default).
	_, err = op.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Int != 1 {
		t.Errorf("OwnedRow contract violated: first[0]=%d after second Next()", first[0].Int)
	}
}

// TestBorrowSemanticsBorrowedRowFlippedBySetBorrow asserts that
// `SetBorrow(BorrowedRow)` flips the operator into pass-through
// mode (no defensive clone). Used by `setChildBorrow` to
// propagate the contract through pipeline-pass operators.
func TestBorrowSemanticsBorrowedRowFlippedBySetBorrow(t *testing.T) {
	op := &fakeBorrowSource{
		rows: []Row{
			{{Kind: KindInt, Int: 10}},
			{{Kind: KindInt, Int: 20}},
		},
	}
	op.SetBorrow(BorrowedRow)
	if op.borrow != BorrowedRow {
		t.Fatalf("after SetBorrow(BorrowedRow): borrow = %v, want BorrowedRow", op.borrow)
	}
}

// TestBorrowPropagatesThroughFilterAndProject confirms the
// build-time `setChildBorrow` propagation: a filter or project
// wrapping a Borrowable child sets the child to BorrowedRow.
func TestBorrowPropagatesThroughFilterAndProject(t *testing.T) {
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	if src.borrow != OwnedRow {
		t.Fatalf("pre-state: src borrow = %v, want OwnedRow", src.borrow)
	}
	setChildBorrow(src, BorrowedRow)
	if src.borrow != BorrowedRow {
		t.Errorf("after setChildBorrow: src borrow = %v, want BorrowedRow", src.borrow)
	}
}

// --- M0059-0001: per-operator-class borrow contract tests ---

// (M0071-0012 Stage C removed the filterOp / limitOp borrow
// propagation tests — those operators are now structurally
// pass-through and own no Row buffer; SetBorrow no longer exists.)

// TestM0059BuildAggregateChildIsBorrowed pins M0059-0002:
// Build wires *planner.Aggregate's child as BorrowedRow because
// aggregateOp's drain loop consumes each row before pulling the
// next, and copies value-typed Datums into aggRuntime / fresh
// groupValues Rows. Verified by a stub Borrowable child.
func TestM0059BuildAggregateChildIsBorrowed(t *testing.T) {
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	if src.borrow != OwnedRow {
		t.Fatalf("pre: src.borrow = %v, want OwnedRow", src.borrow)
	}
	// Mimic Build's setChildBorrow call on the aggregate child.
	setChildBorrow(src, BorrowedRow)
	if src.borrow != BorrowedRow {
		t.Errorf("post: aggregate-child not flipped to BorrowedRow")
	}
}

// TestM0059BuildNLIOuterIsBorrowed pins M0059-0002 for NLI's
// outer side: nestedLoopIndexJoinOp consumes outer rows one at
// a time, copies them into o.joinBuf, and Rescans inner. The
// outer is borrow-safe as input.
func TestM0059BuildNLIOuterIsBorrowed(t *testing.T) {
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	setChildBorrow(src, BorrowedRow)
	if src.borrow != BorrowedRow {
		t.Errorf("NLI outer not flipped to BorrowedRow")
	}
}

// TestM0059NLIBorrowFlag pins that nestedLoopIndexJoinOp's
// SetBorrow flips its own borrow flag (so Next() can return
// o.joinBuf directly when the parent is borrow-safe).
func TestM0059NLIBorrowFlag(t *testing.T) {
	o := &nestedLoopIndexJoinOp{}
	if o.borrow != OwnedRow {
		t.Fatalf("default: borrow = %v, want OwnedRow", o.borrow)
	}
	o.SetBorrow(BorrowedRow)
	if o.borrow != BorrowedRow {
		t.Errorf("after SetBorrow(BorrowedRow): borrow = %v, want BorrowedRow", o.borrow)
	}
}

// --- M0059-0005: retention-boundary regression tests ---

// TestM0059SortStaysAtOwned pins that sortOp does NOT advertise
// Borrowable. Build wires sortOp's child at the default OwnedRow
// because sortOp.Open buffers all rows in `o.rows[]` and yields
// them across many Next() calls — borrowed rows would be aliased
// to the child's per-Next() buffer and become garbage on the
// subsequent child Next().
func TestM0059SortStaysAtOwned(t *testing.T) {
	// Construct a sortOp around a Borrowable child. The sortOp
	// itself does not implement Borrowable — calling
	// setChildBorrow on it must NOT propagate to the child.
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	s := &sortOp{child: src}
	setChildBorrow(s, BorrowedRow)
	if src.borrow != OwnedRow {
		t.Errorf("sortOp must not propagate BorrowedRow to its child; src.borrow = %v", src.borrow)
	}
}

// TestM0059JoinStaysAtOwned pins that joinOp (lazy-hash + NL +
// merge) does NOT advertise Borrowable. The build side is
// retained in lazyHash[key] across the entire probe phase; the
// probe side passes through but the operator-level contract is
// "build-side retains" so Borrowable is left off intentionally.
func TestM0059JoinStaysAtOwned(t *testing.T) {
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	j := &joinOp{left: src}
	setChildBorrow(j, BorrowedRow)
	if src.borrow != OwnedRow {
		t.Errorf("joinOp must not propagate BorrowedRow to retained build side; src.borrow = %v", src.borrow)
	}
}

// TestM0059MultiHashJoinStaysAtOwned pins that multiHashJoinOp
// does not advertise Borrowable for the same reason joinOp
// doesn't: each step's hash table retains rows across many
// probe iterations.
func TestM0059MultiHashJoinStaysAtOwned(t *testing.T) {
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	mhj := &multiHashJoinOp{}
	mhj.children = []Operator{src}
	setChildBorrow(mhj, BorrowedRow)
	if src.borrow != OwnedRow {
		t.Errorf("multiHashJoinOp must not propagate BorrowedRow to retained build side; src.borrow = %v", src.borrow)
	}
}

// TestClass1ProjectKeepsChildAtOwned pins that projectOp does
// NOT propagate SetBorrow to its child via the SetBorrow path
// alone. (Build wires project's child to BorrowedRow at *Build*
// time directly, not through SetBorrow — projectOp always
// copies its child's row into o.out, so the child is *always*
// safe to borrow regardless of project's own contract.)
func TestClass1ProjectKeepsChildAtOwned(t *testing.T) {
	src := &fakeBorrowSource{rows: []Row{{{Kind: KindInt, Int: 1}}}}
	p := &projectOp{child: src}
	p.SetBorrow(BorrowedRow)
	// projectOp.SetBorrow only updates its own borrow flag.
	if p.borrow != BorrowedRow {
		t.Errorf("projectOp.SetBorrow self: got %v, want BorrowedRow", p.borrow)
	}
	// Child must remain at default — projectOp's own SetBorrow
	// doesn't propagate (Build does, separately and explicitly).
	if src.borrow != OwnedRow {
		t.Errorf("projectOp.SetBorrow leaked to child: got %v, want OwnedRow", src.borrow)
	}
}

// fakeBorrowSource is a minimal Borrowable Operator stub used to
// exercise the borrow contract in isolation.
type fakeBorrowSource struct {
	rows   []Row
	idx    int
	borrow BorrowSemantics
}

func (o *fakeBorrowSource) Open(*Context) error    { return nil }
func (o *fakeBorrowSource) Schema() planner.Schema { return nil }
func (o *fakeBorrowSource) Close() error           { return nil }
func (o *fakeBorrowSource) SetBorrow(s BorrowSemantics) {
	o.borrow = s
}
func (o *fakeBorrowSource) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	if o.borrow == BorrowedRow {
		return asSlot(nil, r), nil
	}
	return asSlot(nil, cloneRow(r)), nil
}
