package executor

import (
	"errors"
	"sync/atomic"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
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
	plan  *optimizer.SetOp
	left  Operator
	right Operator

	// streaming is true only for UNION ALL.
	streaming bool
	leftDone  bool
	rightDone bool
	opened    bool

	// claimLeft / claimRight (M0140-0006c-3) are PG's `pa_finished` on a
	// non-partial Append subplan (nodeAppend.c): wired by attachAll's
	// *setOp arm to the shared claimedWhole flag on the branch's leaf
	// claim set when — and only when — the plan marks that branch
	// claimed-whole (SetOp.LeftNonPartial/RightNonPartial). Every
	// participant's private *setOp points at the SAME shared flag, so the
	// CAS in nextStreaming decides which single participant drains its
	// private copy of the branch whole; losers treat it as exhausted.
	// nil for a partial branch (the ordinary per-block claim sets on
	// setOpLeft/setOpRight drive those) and everywhere outside a Gather.
	claimLeft  *atomic.Bool
	claimRight *atomic.Bool
	// leftClaimed / rightClaimed remember a won CAS so later Next calls
	// drain without re-claiming — CAS(true→true) would lose against
	// itself. They are NOT reset by Open: a worker that won keeps its
	// claim across a re-open (the shared flag stays true either way, and
	// only the winner may re-emit the branch), while a loser that
	// re-opens simply loses the CAS again.
	leftClaimed  bool
	rightClaimed bool

	// buffered output (non-streaming variants) produced at Open.
	rows []Row
	idx  int

	// M0141-S2b-4c: ordered merge (plan.MergeKeys). Each side's front row
	// and its evaluated keys, and whether the side is exhausted.
	mergeCur  [2]Row
	mergeKeys [2][]Datum
	mergeLive [2]bool
	mergeInit bool
	mergeErr  error
	ctx       *Context
}

func newSetOp(p *optimizer.SetOp, left, right Operator) *setOp {
	return &setOp{
		plan:      p,
		left:      left,
		right:     right,
		streaming: p.All && p.Op == parser.SetOpUnion,
	}
}

func (o *setOp) Schema() optimizer.Schema {
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
	if o.streaming && len(o.plan.MergeKeys) > 0 {
		return o.nextMerge()
	}
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
// nextMerge is nodeMergeAppend.c's ExecMergeAppend for two inputs, each
// already sorted on plan.MergeKeys: emit whichever front row sorts first.
// A left-deep chain of these links is an n-way merge, and it stays sorted
// because a merge of sorted streams is sorted. The comparator is
// mergeKeysLess, the rule gatherMergeOp and sortOp share, so the inputs'
// Sorts and this merge cannot disagree about NULL placement or direction.
func (o *setOp) nextMerge() (TupleSlot, error) {
	if !o.mergeInit {
		o.mergeInit = true
		for side := 0; side < 2; side++ {
			if err := o.mergeAdvance(side); err != nil {
				return nil, err
			}
		}
	}
	pick := -1
	switch {
	case o.mergeLive[0] && o.mergeLive[1]:
		pick = 0
		if mergeKeysLess(o.plan.MergeKeys, o.mergeKeys[1], o.mergeKeys[0], &o.mergeErr) {
			pick = 1
		}
		if o.mergeErr != nil {
			return nil, o.mergeErr
		}
	case o.mergeLive[0]:
		pick = 0
	case o.mergeLive[1]:
		pick = 1
	default:
		return nil, EOF
	}
	row := o.mergeCur[pick]
	if err := o.mergeAdvance(pick); err != nil {
		return nil, err
	}
	return SlotFromRow(o.plan.Output(), row), nil
}

// mergeAdvance pulls side's next row, copying it out of the child's reused
// slot (it must survive until it is emitted, possibly several Next calls
// later) and evaluating its merge keys once.
func (o *setOp) mergeAdvance(side int) error {
	in := o.left
	if side == 1 {
		in = o.right
	}
	slot, err := in.Next()
	if errors.Is(err, EOF) {
		o.mergeLive[side] = false
		o.mergeCur[side] = nil
		return nil
	}
	if err != nil {
		return err
	}
	row := transferRowForQueue(slot)
	keys := make([]Datum, len(o.plan.MergeKeys))
	for i, k := range o.plan.MergeKeys {
		v, kerr := evalSortKeyValue(k.Expr, row, o.ctx)
		if kerr != nil {
			return kerr
		}
		keys[i] = v
	}
	o.mergeCur[side], o.mergeKeys[side], o.mergeLive[side] = row, keys, true
	return nil
}

func (o *setOp) nextStreaming() (TupleSlot, error) {
	if !o.leftDone {
		// M0140-0006c-3: a claimed-whole branch is drained by exactly ONE
		// participant. The first Next on the branch CASes the shared
		// pa_finished-style flag — the winner drains its private copy
		// serially, every loser marks the branch done and moves on,
		// closing its (opened but never-to-be-read) copy exactly as the
		// EOF path does. The claim is demand-driven at first touch, not at
		// Open: a participant still draining the other branch must not
		// hold this one's claim, matching nodeAppend's claim-on-select.
		if o.claimLeft != nil && !o.leftClaimed {
			if o.claimLeft.CompareAndSwap(false, true) {
				o.leftClaimed = true
			} else {
				o.leftDone = true
				o.left.Close()
			}
		}
	}
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
		if o.claimRight != nil && !o.rightClaimed {
			if o.claimRight.CompareAndSwap(false, true) {
				o.rightClaimed = true
			} else {
				o.rightDone = true
				o.right.Close()
			}
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
	if src, err := findScanLeaf(active); err == nil && src != nil {
		return src.currentTID()
	}
	return storage.RelFileNode{}, storage.ItemPointer{}, false
}
