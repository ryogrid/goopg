package executor

import (
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// setOp executes SQL set operations (UNION / INTERSECT / EXCEPT).
//
// UNION ALL is streamed: the left child is drained, then the right, so the
// per-row currentTID provider keeps working for partition/inheritance scans
// under SELECT … FOR UPDATE (M0100-0005). Every other variant — UNION
// (DISTINCT), INTERSECT [ALL], EXCEPT [ALL] — buffers both inputs at Open and
// applies the appropriate multiset semantics. M0097-0024.
type setOp struct {
	plan  *planner.SetOp
	left  Operator
	right Operator

	// streaming is true only for UNION ALL.
	streaming bool
	leftDone  bool
	rightDone bool
	opened    bool

	// buffered output (non-streaming variants) produced at Open.
	rows []Row
	idx  int
}

func newSetOp(p *planner.SetOp, left, right Operator) *setOp {
	return &setOp{
		plan:      p,
		left:      left,
		right:     right,
		streaming: p.All && p.Op == parser.SetOpUnion,
	}
}

func (o *setOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *setOp) Open(ctx *Context) error {
	// Reset buffered state so the operator can be re-opened (e.g. as a
	// recursive member of WITH RECURSIVE that is opened every iteration).
	o.rows = nil
	o.idx = 0
	o.leftDone = false
	o.rightDone = false

	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		o.left.Close()
		return err
	}
	o.opened = true
	if o.streaming {
		return nil
	}
	if err := o.computeBuffered(); err != nil {
		return err
	}
	return nil
}

func (o *setOp) Close() error {
	var lErr, rErr error
	if o.left != nil {
		lErr = o.left.Close()
	}
	if o.right != nil {
		rErr = o.right.Close()
	}
	if lErr != nil {
		return lErr
	}
	return rErr
}

func (o *setOp) Next() (TupleSlot, error) {
	if o.streaming {
		return o.nextStreaming()
	}
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	if row == nil {
		return SlotFromRow(o.plan.Output(), Row{}), nil
	}
	return SlotFromRow(o.plan.Output(), row), nil
}

// nextStreaming yields the left child to exhaustion, then the right child
// (UNION ALL).
func (o *setOp) nextStreaming() (TupleSlot, error) {
	if !o.leftDone {
		slot, err := o.left.Next()
		if err == EOF {
			o.leftDone = true
			o.left.Close()
		} else if err != nil {
			return nil, err
		} else {
			return slot, nil
		}
	}
	if !o.rightDone {
		slot, err := o.right.Next()
		if err == EOF {
			o.rightDone = true
			o.right.Close()
		} else if err != nil {
			return nil, err
		} else {
			return slot, nil
		}
	}
	return nil, EOF
}

// computeBuffered drains both children and materialises the output rows for
// every non-UNION-ALL variant.
func (o *setOp) computeBuffered() error {
	leftRows, _, err := drainSetOpInput(o.left)
	if err != nil {
		return err
	}
	rightRows, rightCount, err := drainSetOpInput(o.right)
	if err != nil {
		return err
	}

	switch o.plan.Op {
	case parser.SetOpIntersect:
		o.computeIntersect(leftRows, rightCount)
	case parser.SetOpExcept:
		o.computeExcept(leftRows, rightCount)
	default: // UNION (DISTINCT) — UNION ALL never reaches here.
		o.computeUnionDistinct(leftRows, rightRows)
	}
	return nil
}

// computeUnionDistinct emits each distinct row once, preserving first-seen
// order (left rows before right rows).
func (o *setOp) computeUnionDistinct(leftRows, rightRows []Row) {
	seen := make(map[string]struct{})
	for _, r := range leftRows {
		k := rowKey(r)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		o.rows = append(o.rows, r)
	}
	for _, r := range rightRows {
		k := rowKey(r)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		o.rows = append(o.rows, r)
	}
}

// computeIntersect emits rows present in both inputs. INTERSECT keeps each
// distinct row once; INTERSECT ALL keeps min(leftCount, rightCount) copies.
func (o *setOp) computeIntersect(leftRows []Row, rightCount map[string]int) {
	emitted := make(map[string]int)
	for _, r := range leftRows {
		k := rowKey(r)
		rc := rightCount[k]
		if rc == 0 {
			continue
		}
		if o.plan.All {
			if emitted[k] >= rc {
				continue
			}
		} else if emitted[k] >= 1 {
			continue
		}
		emitted[k]++
		o.rows = append(o.rows, r)
	}
}

// computeExcept emits rows of the left input not cancelled by the right.
// EXCEPT keeps each surviving distinct row once; EXCEPT ALL keeps
// max(0, leftCount-rightCount) copies.
func (o *setOp) computeExcept(leftRows []Row, rightCount map[string]int) {
	emitted := make(map[string]int)
	for _, r := range leftRows {
		k := rowKey(r)
		rc := rightCount[k]
		if o.plan.All {
			// Each of the first rc left occurrences is cancelled.
			if emitted[k] < rc {
				emitted[k]++
				continue
			}
			emitted[k]++
			o.rows = append(o.rows, r)
		} else {
			if rc > 0 {
				continue
			}
			if _, done := emitted[k]; done {
				continue
			}
			emitted[k] = 1
			o.rows = append(o.rows, r)
		}
	}
}

// drainSetOpInput fully consumes an operator, returning the cloned rows in
// order plus a multiset count keyed by rowKey.
func drainSetOpInput(op Operator) ([]Row, map[string]int, error) {
	var rows []Row
	counts := make(map[string]int)
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if slot == nil {
			continue
		}
		owned := cloneRow(slot.Row())
		rows = append(rows, owned)
		counts[rowKey(owned)]++
	}
	return rows, counts, nil
}

// currentTID implements currentTIDProvider for partition UNION ALL scans
// (M0100-0005 follow-up). Only meaningful while streaming; buffered variants
// (INTERSECT/EXCEPT/UNION DISTINCT) have already closed their children and do
// not participate in row locking.
func (o *setOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
	if !o.streaming {
		return storage.RelFileNode{}, storage.ItemPointer{}, false
	}
	var active Operator
	if !o.leftDone {
		active = o.left
	} else {
		active = o.right
	}
	if src := findScanLeaf(active); src != nil {
		return src.currentTID()
	}
	return storage.RelFileNode{}, storage.ItemPointer{}, false
}
