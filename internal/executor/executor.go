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
	case *planner.Join:
		left, err := Build(p.Left)
		if err != nil {
			return nil, err
		}
		right, err := Build(p.Right)
		if err != nil {
			return nil, err
		}
		return newJoinOp(p, left, right), nil
	case *planner.Aggregate:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return newAggregateOp(p, child), nil
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
	case *planner.Checkpoint:
		return newCheckpointOp(p), nil
	case *planner.Explain:
		return newExplainOp(p), nil
	case *planner.Utility:
		// VACUUM / ANALYZE / SHOW / SET / RESET are utility
		// statements. The wire layer already handles SHOW/SET/RESET
		// via the legacy string-matching path in
		// internal/server/query.go, so they shouldn't reach here in
		// practice. VACUUM and ANALYZE in v0 are optimisations
		// (their package functions are exposed in internal/vacuum)
		// and not load-bearing for correctness — emit a no-op
		// operator that succeeds silently. The wire layer's
		// commandTagFor builds the right CommandComplete tag.
		return newUtilityNoOp(p), nil
	case *planner.Copy:
		// COPY is currently driven from the wire-protocol layer
		// (see internal/server/copy.go). The planner produces a
		// Copy node so the layer can resolve table/columns through
		// the catalog without re-parsing; once the executor copy
		// operator lands the Build dispatch will return it. Until
		// then, fall through with a stable feature-not-supported
		// error rather than a generic "unsupported plan node".
		return nil, &ExecError{Code: "0A000", Pos: p.Pos(), Message: "COPY is driven from the wire layer; planner.Copy has no executor path yet"}
	}
	return nil, &ExecError{Code: "0A000", Pos: plan.Pos(), Message: fmt.Sprintf("unsupported plan node %T", plan)}
}

// utilityNoOp is the executor counterpart to planner.Utility. v0
// uses it for VACUUM/ANALYZE statements that the wire layer
// recognises but doesn't yet have a real implementation for at the
// executor level — running them is a no-op so pgbench's `vacuum
// analyze pgbench_branches` (and similar) succeed cleanly.
type utilityNoOp struct{ plan *planner.Utility }

func newUtilityNoOp(p *planner.Utility) *utilityNoOp { return &utilityNoOp{plan: p} }

func (o *utilityNoOp) Schema() planner.Schema  { return nil }
func (o *utilityNoOp) Open(*Context) error     { return nil }
func (o *utilityNoOp) Next() (Row, error)      { return nil, EOF }
func (o *utilityNoOp) Close() error            { return nil }

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
