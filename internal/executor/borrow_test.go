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
	first, err := op.Next()
	if err != nil {
		t.Fatal(err)
	}
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
func (o *fakeBorrowSource) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	if o.borrow == BorrowedRow {
		return r, nil
	}
	return cloneRow(r), nil
}
