package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// Build walks a plan tree and produces an Operator tree ready to
// Open. Storage-touching operators (SeqScan/Insert/Update/Delete)
// are wired in operators_storage.go alongside the heap-write
// machinery; this file handles the pure-compute operators.
//
// When the package-local instrumentScope is non-nil (set by
// withInstrumentation around an EXPLAIN ANALYZE Open), each
// returned operator is wrapped in an instrumentedOp via
// maybeInstrument so the EXPLAIN renderer can read per-node
// rows/loops/timing counters. nil-scope (the default) returns
// raw operators byte-for-byte unchanged.
func Build(plan planner.Node) (Operator, error) {
	switch p := plan.(type) {
	case *planner.Values:
		return maybeInstrument(p, newValuesOp(p)), nil
	case *planner.CTEScan:
		// CTEScan is a labeling wrap from M0016-0004 — Stage A
		// inlines the CTE body under the wrap, so executing it
		// is just executing the child. The wrap exists so EXPLAIN
		// can surface the CTE name; runtime semantics are
		// identical to the child. The recursive Build wraps the
		// child, so the CTEScan layer doesn't need its own
		// instrumented row.
		return Build(p.Child)
	case *planner.Project:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newProjectOp(p, child)), nil
	case *planner.Filter:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newFilterOp(p, child)), nil
	case *planner.Limit:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newLimitOp(p, child)), nil
	case *planner.Sort:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newSortOp(p, child)), nil
	case *planner.Join:
		left, err := Build(p.Left)
		if err != nil {
			return nil, err
		}
		right, err := Build(p.Right)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newJoinOp(p, left, right)), nil
	case *planner.Aggregate:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newAggregateOp(p, child)), nil
	case *planner.WindowAgg:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newWindowOp(p, child)), nil
	case *planner.SeqScan:
		return maybeInstrument(p, newSeqScanOp(p)), nil
	case *planner.IndexScan:
		return maybeInstrument(p, newIndexScanOp(p)), nil
	case *planner.LockRows:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newLockRowsOp(p, child)), nil
	case *planner.Insert:
		child, err := Build(p.Source)
		if err != nil {
			return nil, err
		}
		if p.OnConflict != nil {
			return maybeInstrument(p, newUpsertOp(p, child)), nil
		}
		return maybeInstrument(p, newInsertOp(p, child)), nil
	case *planner.SetOp:
		left, err := Build(p.Left)
		if err != nil {
			return nil, err
		}
		right, err := Build(p.Right)
		if err != nil {
			left.Close()
			return nil, err
		}
		return maybeInstrument(p, newSetOp(p, left, right)), nil
	case *planner.Update:
		op, err := newUpdateOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op), nil
	case *planner.Delete:
		op, err := newDeleteOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op), nil
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
		// practice. ANALYZE drives the catalog-stats collector
		// (per-table row count + per-column NDistinct/NullFrac);
		// VACUUM still routes through utilityNoOp until the
		// vacuum package exposes a stmt-driven entry point.
		if as, ok := p.Stmt.(*parser.AnalyzeStmt); ok {
			return newAnalyzeOp(as), nil
		}
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
	case *planner.Call:
		return newCallOp(p), nil
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

func (o *utilityNoOp) Schema() planner.Schema { return nil }
func (o *utilityNoOp) Open(*Context) error    { return nil }
func (o *utilityNoOp) Next() (Row, error)     { return nil, EOF }
func (o *utilityNoOp) Close() error           { return nil }

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
