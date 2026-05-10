package executor

import "github.com/goopg/goopg/internal/planner"

// fakeBorrowSource is a minimal Operator stub used by tests to
// drive operators with a pre-built row sequence. M0071-0015
// removed the BorrowSemantics contract; fakeBorrowSource keeps
// its historical name and now simply emits each row as a
// Materialized slot (cloned defensively).
type fakeBorrowSource struct {
	rows []Row
	idx  int
}

func (o *fakeBorrowSource) Open(*Context) error    { return nil }
func (o *fakeBorrowSource) Schema() planner.Schema { return nil }
func (o *fakeBorrowSource) Close() error           { return nil }
func (o *fakeBorrowSource) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return asSlot(nil, cloneRow(r)), nil
}
