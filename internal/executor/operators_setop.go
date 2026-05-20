package executor

import (
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// setOp executes UNION ALL by draining the left side then the right.
type setOp struct {
	plan      *planner.SetOp
	left      Operator
	right     Operator
	leftDone  bool
	rightDone bool
	opened    bool
}

func newSetOp(p *planner.SetOp, left, right Operator) *setOp {
	return &setOp{plan: p, left: left, right: right}
}

func (o *setOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *setOp) Open(ctx *Context) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		o.left.Close()
		return err
	}
	o.opened = true
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

// currentTID implements currentTIDProvider for partition UNION ALL scans
// (M0100-0005 follow-up). After each setOp.Next() call, the just-yielded
// row came from the left child while !leftDone, and from the right child
// once leftDone is true. Delegating to findScanLeaf on the active child
// lets lockRowsOp.drainAndStamp stamp the correct per-row xmax on the
// leaf partition's heap tuple for SELECT … FOR UPDATE / FOR SHARE.
func (o *setOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
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
