package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/planner"
)

// Build walks a plan tree and produces an Operator tree ready to
// Open. Storage-touching operators (SeqScan/Insert/Update/Delete)
// are wired in operators_storage.go alongside the heap-write
// machinery; this file handles the pure-compute operators.
func Build(plan planner.Node) (Operator, error) {
	switch p := plan.(type) {
	case *planner.Values:
		return newValuesOp(p), nil
	case *planner.Project:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return newProjectOp(p, child), nil
	case *planner.Filter:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return newFilterOp(p, child), nil
	case *planner.Limit:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return newLimitOp(p, child), nil
	case *planner.Sort:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return newSortOp(p, child), nil
	case *planner.SeqScan:
		return newSeqScanOp(p), nil
	case *planner.IndexScan:
		return newIndexScanOp(p), nil
	case *planner.Insert:
		child, err := Build(p.Source)
		if err != nil {
			return nil, err
		}
		return newInsertOp(p, child), nil
	case *planner.Update:
		return newUpdateOp(p)
	case *planner.Delete:
		return newDeleteOp(p)
	case *planner.DDL:
		return newDDLOp(p), nil
	case *planner.Transaction:
		return newTransactionOp(p), nil
	}
	return nil, &ExecError{Code: "0A000", Pos: plan.Pos(), Message: fmt.Sprintf("unsupported plan node %T", plan)}
}

// Run is a convenience that opens an operator, drains it into a slice
// of rows, then closes. Production paths use Open/Next/Close
// directly so they can stream into the wire-protocol encoder.
func Run(op Operator, ctx *Context) ([]Row, error) {
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return nil, err
	}
	var out []Row
	for {
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			_ = op.Close()
			return nil, err
		}
		out = append(out, row)
	}
	if err := op.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
