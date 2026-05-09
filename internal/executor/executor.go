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
		// M0054-0005a-followup: projectOp ALWAYS copies its
		// child's row into `o.out` (then either clones or returns
		// borrowed). So the child is always safe to borrow,
		// regardless of project's own borrow contract.
		setChildBorrow(child, BorrowedRow)
		return maybeInstrument(p, newProjectOp(p, child)), nil
	case *planner.Filter:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		// M0054-0005a-followup: filterOp is a pure pass-through
		// — it returns its child's row unchanged. So filter's
		// own borrow contract must MATCH its child's. We leave
		// the child at the default OwnedRow at Build time;
		// filterOp.SetBorrow propagates to the child only when
		// the eventual parent (project, output sink) flips the
		// filter itself to BorrowedRow.
		return maybeInstrument(p, newFilterOp(p, child)), nil
	case *planner.Limit:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		// M0054-0005a-followup: limitOp is pass-through like
		// filterOp; child borrow propagates from limit's own
		// parent via SetBorrow.
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
	case *planner.NestedLoopIndexJoin:
		outer, err := Build(p.Outer)
		if err != nil {
			return nil, err
		}
		// Inner is always an *IndexScan by plan-node contract.
		innerScan := newIndexScanOp(p.Inner)
		// M0059-0002: NLI consumes one outer row at a time, copies
		// it into o.joinBuf, then runs the inner Rescan. The outer
		// row is released before the next pull, so it is safe to
		// receive borrowed rows from the outer side. The inner is
		// an *IndexScan that pre-materialises into o.rows[] at
		// Open() — borrow is a no-op there.
		setChildBorrow(outer, BorrowedRow)
		return maybeInstrument(p, newNestedLoopIndexJoinOp(p, outer, innerScan)), nil
	case *planner.Aggregate:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		// M0059-0002: aggregateOp.Open's drain loop consumes each
		// child row, extracts value-typed Datums into aggRuntime
		// fields and into a fresh groupValues Row, then releases
		// the source row before pulling the next. Datums hold
		// independent string allocations from the scan decode
		// path, so borrowed input is safe.
		setChildBorrow(child, BorrowedRow)
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
	case *planner.IndexOnlyScan:
		return maybeInstrument(p, newIndexOnlyScanOp(p)), nil
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
	case *planner.RecursiveUnion:
		anchor, err := Build(p.Anchor)
		if err != nil {
			return nil, err
		}
		recursive, err := Build(p.Recursive)
		if err != nil {
			anchor.Close()
			return nil, err
		}
		return maybeInstrument(p, newRecursiveUnionOp(p, anchor, recursive)), nil
	case *planner.WorkTableScan:
		return maybeInstrument(p, newWorkTableScanOp(p)), nil
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
	case *planner.MultiHashJoin:
		children := make([]Operator, len(p.Tables))
		for i, tbl := range p.Tables {
			var err error
			children[i], err = Build(tbl)
			if err != nil {
				return nil, err
			}
		}
		return maybeInstrument(p, newMultiHashJoinOp(p, children)), nil
	case *planner.DDL:
		return newDDLOp(p), nil
	case *planner.Transaction:
		return newTransactionOp(p), nil
	case *planner.Checkpoint:
		return newCheckpointOp(p), nil
	case *planner.Explain:
		return newExplainOp(p), nil
	case *planner.Utility:
		// VACUUM / ANALYZE / SHOW / SET / RESET are utility statements.
		// VACUUM runs the heap page-prune and updates the FSM (M0046-0003).
		// ANALYZE drives the catalog-stats collector. SHOW/SET/RESET are
		// handled by the wire layer and shouldn't reach here in practice.
		if _, ok := p.Stmt.(*parser.VacuumStmt); ok {
			return newVacuumOp(p), nil
		}
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
func (o *utilityNoOp) Next() (TupleSlot, error) { return nil, EOF }
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
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			_ = op.Close()
			return nil, err
		}
		// Some operators (DML, transaction-control utility ops)
		// return a nil slot with nil err to signal "advance done,
		// no row to surface". Skip those.
		if slot == nil {
			continue
		}
		// Materialize at the public Run boundary so callers
		// receive independent rows.
		out = append(out, slot.Materialize().Row())
	}
	if err := op.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
