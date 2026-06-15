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
	case *planner.GenerateSeries:
		return maybeInstrument(p, newGenerateSeriesOp(p)), nil
	case *planner.UserSrfScan:
		return maybeInstrument(p, newUserSrfScanOp(p)), nil
	case *planner.GenerateSubscripts:
		return maybeInstrument(p, newGenerateSubscriptsOp(p)), nil
	case *planner.FromUnnest:
		return maybeInstrument(p, newFromUnnestOp(p)), nil
	case *planner.OrdinalityWrap:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newOrdinalityOp(p, child)), nil
	case *planner.RowsFrom:
		children := make([]Operator, len(p.Funcs))
		for i, f := range p.Funcs {
			c, err := Build(f)
			if err != nil {
				return nil, err
			}
			children[i] = c
		}
		return maybeInstrument(p, newRowsFromOp(p, children)), nil
	case *planner.PgInputErrorInfo:
		return maybeInstrument(p, newPgInputErrorInfoOp(p)), nil
	case *planner.PgGetPublicationTables:
		return maybeInstrument(p, newPgGetPublicationTablesOp(p)), nil
	case *planner.PgAvailableWalSummaries:
		return maybeInstrument(p, newPgAvailableWalSummariesOp(p)), nil
	case *planner.PgGetSequenceData:
		return maybeInstrument(p, newPgGetSequenceDataOp(p)), nil
	case *planner.VerifyHeapam:
		return maybeInstrument(p, newVerifyHeapamOp(p)), nil
	case *planner.ProjectSet:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newProjectSetOp(p, child)), nil
	case *planner.ScalarFuncScan:
		return maybeInstrument(p, newScalarFuncScanOp(p)), nil
	case *planner.PgPartitionTree:
		return maybeInstrument(p, newPgPartitionTreeOp(p)), nil
	case *planner.PgOptionsToTable:
		return maybeInstrument(p, newPgOptionsToTableOp(p)), nil
	case *planner.CTEScan:
		// CTEScan wraps the inlined CTE body. Use cteScanOp which materializes
		// all rows on first Open() and replays them on subsequent Open() calls
		// (same CTE name, same ctx.CTERowCache). This implements PostgreSQL's
		// CTE optimization-fence: a volatile CTE (e.g. random()) produces the
		// same rows regardless of how many times it is referenced. M0097-0099.
		return newCteScanOp(p)
	case *planner.CTEDMLPrefix:
		return newCTEDMLPrefixOp(p), nil
	case *planner.MaterializedCTEScan:
		return newMaterializedCTEScanOp(p), nil
	case *planner.Project:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		// M0071-0015 Stage E: projectOp materialises evaluated
		// targets into o.out and clones for the consumer; child
		// slot lifetime is bounded by projectOp's per-Next read
		// — no borrow contract needed.
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
		// M0071-0013 Stage D-1: NLI now composes outer + inner via
		// a persistent VirtualSlot. outerMS.row is overwritten per
		// outer row before the inner Rescan; the IndexScan still
		// reads outer columns from the bound Row (slot-aware
		// BindOuter is M0072 future work). No borrow contract
		// needed at this boundary.
		return maybeInstrument(p, newNestedLoopIndexJoinOp(p, outer, innerScan)), nil
	case *planner.Aggregate:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		// M0071-0015 Stage E: aggregateOp.Open's drain loop
		// extracts value-typed Datums into aggRuntime fields and
		// into a fresh groupValues Row before pulling the next
		// child slot — slot lifetime is bounded by the per-Next
		// read, no borrow contract needed.
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
	case *planner.Distinct:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newDistinctOp(p, child)), nil
	case *planner.DistinctOn:
		child, err := Build(p.Child)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newDistinctOnOp(p, child)), nil
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
	case *planner.Merge:
		return maybeInstrument(p, newMergeOp(p)), nil
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
		// ANALYZE drives the catalog-stats collector. SHOW/SET/RESET also
		// need executor support for multi-statement simple-query batches and
		// extended-query execution.
		if _, ok := p.Stmt.(*parser.ShowStmt); ok {
			return newUtilitySettingsOp(p), nil
		}
		if _, ok := p.Stmt.(*parser.SetStmt); ok {
			return newUtilitySettingsOp(p), nil
		}
		if _, ok := p.Stmt.(*parser.ResetStmt); ok {
			return newUtilitySettingsOp(p), nil
		}
		if _, ok := p.Stmt.(*parser.DiscardStmt); ok {
			return newUtilitySettingsOp(p), nil
		}
		if _, ok := p.Stmt.(*parser.VacuumStmt); ok {
			return newVacuumOp(p), nil
		}
		if as, ok := p.Stmt.(*parser.AnalyzeStmt); ok {
			return newAnalyzeOp(as), nil
		}
		if cs, ok := p.Stmt.(*parser.ClusterStmt); ok {
			return newClusterOp(cs), nil
		}
		if st, ok := p.Stmt.(*parser.SetTransactionStmt); ok {
			return newSetTransactionOp(st), nil
		}
		if rs, ok := p.Stmt.(*parser.ReindexStmt); ok {
			return newReindexOp(rs), nil
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

func (o *utilityNoOp) Schema() planner.Schema   { return nil }
func (o *utilityNoOp) Open(*Context) error      { return nil }
func (o *utilityNoOp) Next() (TupleSlot, error) { return nil, EOF }
func (o *utilityNoOp) Close() error             { return nil }

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

// ---------------------------------------------------------------------------
// Phase C.2 — BuildFast / RunFast
//
// BuildFast constructs an opTreeSlab from a plan, using concrete
// dispatch for migrated operators (OpSeqScan, OpFilter, OpProject,
// OpLimit, OpSort, OpUpdate, OpDelete, OpInsert, OpJoin) and opAdapter
// for everything else. RunFast drives the tree via opNext. Both
// functions are drop-in replacements for Build+Run; they produce
// identical result rows.
//
// Phase C.2 change: children are int32 slab indices (noChild = -1)
// instead of *OpNode pointers, eliminating GC-scanned pointers in the
// hot tree. BuildFast now returns (*opTreeSlab, int32, error).
// ---------------------------------------------------------------------------

// buildRec is the recursive tree builder for BuildFast.
func (tree *opTreeSlab) buildRec(plan planner.Node) (int32, error) {
	switch p := plan.(type) {
	case *planner.SeqScan:
		return tree.add(OpNode{Kind: OpSeqScan, childA: noChild, childB: noChild, state: newSeqScanOp(p)}), nil

	case *planner.Filter:
		childIdx, err := tree.buildRec(p.Child)
		if err != nil {
			return noChild, err
		}
		// Phase C.3: predicate compiled into exprTreeSlab; filterState holds
		// only the predIdx — no GC-traced planner.Expr reference needed.
		predIdx := tree.exprs.buildExpr(p.Predicate)
		return tree.add(OpNode{Kind: OpFilter, childA: childIdx, childB: noChild,
			state: &filterState{predIdx: predIdx}}), nil

	case *planner.Project:
		childIdx, err := tree.buildRec(p.Child)
		if err != nil {
			return noChild, err
		}
		// Phase C.3: target expressions compiled into exprTreeSlab; projectState
		// holds only compiled indices — no GC-traced *planner.Project or schema slice.
		// Schema is pooled in opTreeSlab.schemas; projectState stores an int32 index.
		targExprs := make([]int32, len(p.Targets))
		for i, t := range p.Targets {
			targExprs[i] = tree.exprs.buildExpr(t)
		}
		schemaIdx := int32(len(tree.schemas))
		tree.schemas = append(tree.schemas, p.Output())
		return tree.add(OpNode{Kind: OpProject, childA: childIdx, childB: noChild,
			state: &projectState{schemaIdx: schemaIdx, targExprs: targExprs}}), nil

	case *planner.Limit:
		childIdx, err := tree.buildRec(p.Child)
		if err != nil {
			return noChild, err
		}
		// Phase C.3: LIMIT and OFFSET expressions compiled into exprTreeSlab;
		// limitState holds only the compiled indices — no *planner.Limit reference.
		limitExprIdx := tree.exprs.buildExpr(p.Limit)
		offsetExprIdx := tree.exprs.buildExpr(p.Offset)
		var tieKeyExprIdxs []int32
		if p.WithTies {
			tieKeyExprIdxs = make([]int32, len(p.TiesKeys))
			for i, ke := range p.TiesKeys {
				tieKeyExprIdxs[i] = tree.exprs.buildExpr(ke)
			}
		}
		return tree.add(OpNode{Kind: OpLimit, childA: childIdx, childB: noChild,
			state: &limitState{
				limitExprIdx:   limitExprIdx,
				offsetExprIdx:  offsetExprIdx,
				tieKeyExprIdxs: tieKeyExprIdxs,
				limitCount:     -1,
				withTies:       p.WithTies,
			}}), nil

	case *planner.Sort:
		childIdx, err := tree.buildRec(p.Child)
		if err != nil {
			return noChild, err
		}
		// Bridge the child slab node into the Operator interface that sortOp
		// expects. sortOp.Open() drains the child in a tight loop; the
		// opNodeOperator hop adds one function call per row but the child
		// subtree runs on concrete switch dispatch.
		childOp := &opNodeOperator{tree: tree, idx: childIdx, schema: p.Child.Output()}
		sortLegacy := newSortOp(p, childOp)
		return tree.add(OpNode{Kind: OpSort, childA: noChild, childB: noChild, state: &sortOpState{op: sortLegacy, schema: p.Output()}}), nil

	case *planner.Update:
		op, err := newUpdateOp(p)
		if err != nil {
			return noChild, err
		}
		return tree.add(OpNode{Kind: OpUpdate, childA: noChild, childB: noChild, state: &updateOpState{op: op}}), nil

	case *planner.Delete:
		op, err := newDeleteOp(p)
		if err != nil {
			return noChild, err
		}
		return tree.add(OpNode{Kind: OpDelete, childA: noChild, childB: noChild, state: &deleteOpState{op: op}}), nil

	case *planner.Insert:
		childIdx, err := tree.buildRec(p.Source)
		if err != nil {
			return noChild, err
		}
		childOp := &opNodeOperator{tree: tree, idx: childIdx, schema: p.Source.Output()}
		if p.OnConflict != nil {
			// upsertOp has complex conflict-resolution logic; keep on adapter.
			uop := newUpsertOp(p, childOp)
			return tree.add(OpNode{Kind: OpAdapter, childA: noChild, childB: noChild, state: &opAdapterState{op: uop}}), nil
		}
		return tree.add(OpNode{Kind: OpInsert, childA: noChild, childB: noChild, state: &insertOpState{op: newInsertOp(p, childOp)}}), nil

	case *planner.Join:
		leftIdx, err := tree.buildRec(p.Left)
		if err != nil {
			return noChild, err
		}
		rightIdx, err := tree.buildRec(p.Right)
		if err != nil {
			return noChild, err
		}
		leftOp := &opNodeOperator{tree: tree, idx: leftIdx, schema: p.Left.Output()}
		rightOp := &opNodeOperator{tree: tree, idx: rightIdx, schema: p.Right.Output()}
		op := newJoinOp(p, leftOp, rightOp)
		return tree.add(OpNode{Kind: OpJoin, childA: noChild, childB: noChild, state: &joinOpState{op: op, schema: p.Output()}}), nil

	default:
		// For non-migrated operators, build the legacy Operator tree
		// and wrap in an adapter. This path preserves the existing
		// operator semantics exactly — Open/Next/Close are forwarded.
		legacyOp, err := Build(plan)
		if err != nil {
			return noChild, err
		}
		return tree.add(OpNode{Kind: OpAdapter, childA: noChild, childB: noChild, state: &opAdapterState{op: legacyOp}}), nil
	}
}

// BuildFast constructs an op-tree slab from plan and returns the slab and
// the root index. Non-migrated operators are wrapped in an opAdapter that
// drives the legacy Operator interface.
func BuildFast(plan planner.Node) (*opTreeSlab, int32, error) {
	tree := &opTreeSlab{
		ops:     make([]OpNode, 0, 8),
		exprs:   make(exprTreeSlab, 0, 16),
		plans:   make(planTreeSlab, 0, 8),
		schemas: make([]planner.Schema, 0, 4),
	}
	rootIdx, err := tree.buildRec(plan)
	if err != nil {
		return nil, noChild, err
	}
	return tree, rootIdx, nil
}

// RunFast opens the op-tree rooted at rootIdx in tree, drains it via
// opNext, and returns all rows. Rows are deep-copied via cloneRowOwned
// so callers receive independent storage (same invariant as Run).
func RunFast(tree *opTreeSlab, rootIdx int32, ctx *Context) ([]Row, error) {
	if err := opOpen(tree, rootIdx, ctx); err != nil {
		_ = opClose(tree, rootIdx)
		return nil, err
	}
	var (
		out []Row
		dst Slot
	)
	for {
		dst.Reset()
		err := opNext(tree, rootIdx, &dst)
		if err == EOF {
			break
		}
		if err != nil {
			_ = opClose(tree, rootIdx)
			return nil, err
		}
		// DML / utility ops surface nil-row (HasRow=false); skip.
		if !dst.HasRow {
			continue
		}
		// Deep-copy at the RunFast boundary so callers own independent rows.
		out = append(out, cloneRowOwned(Row(dst.Cells)))
	}
	if err := opClose(tree, rootIdx); err != nil {
		return nil, err
	}
	return out, nil
}
