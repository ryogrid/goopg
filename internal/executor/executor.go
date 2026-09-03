package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
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
func Build(plan optimizer.Node) (Operator, error) {
	return buildNode(plan, deformBoundNone)
}

// BuildWorker is the per-worker entry point for Gather/GatherMerge
// closures. It used to set inWorker=true on a per-build `buildEnv` so
// tryFuseHashCascade declined inside parallel workers (C10/F4);
// M0127-P6.1 deleted runtime hash-join fusion, which was the sole
// reader of that env, so a worker build is now byte-for-byte a leader
// build. The entry point stays because gatherOp/gatherMergeOp and
// join_worker_path_test.go name it as the worker seam.
func BuildWorker(plan optimizer.Node) (Operator, error) {
	return buildNode(plan, deformBoundNone)
}

func buildNode(plan optimizer.Node, bound int) (Operator, error) {
	switch p := plan.(type) {
	case *optimizer.Values:
		return maybeInstrument(p, newValuesOp(p)), nil
	case *optimizer.GenerateSeries:
		return maybeInstrument(p, newGenerateSeriesOp(p)), nil
	case *optimizer.UserSrfScan:
		return maybeInstrument(p, newUserSrfScanOp(p)), nil
	case *optimizer.GenerateSubscripts:
		return maybeInstrument(p, newGenerateSubscriptsOp(p)), nil
	case *optimizer.FromUnnest:
		return maybeInstrument(p, newFromUnnestOp(p)), nil
	case *optimizer.OrdinalityWrap:
		child, err := buildNode(p.Child, deformBoundFull)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newOrdinalityOp(p, child)), nil
	case *optimizer.RowsFrom:
		children := make([]Operator, len(p.Funcs))
		for i, f := range p.Funcs {
			c, err := buildNode(f, deformBoundFull)
			if err != nil {
				return nil, err
			}
			children[i] = c
		}
		return maybeInstrument(p, newRowsFromOp(p, children)), nil
	case *optimizer.PgInputErrorInfo:
		return maybeInstrument(p, newPgInputErrorInfoOp(p)), nil
	case *optimizer.PgGetPublicationTables:
		return maybeInstrument(p, newPgGetPublicationTablesOp(p)), nil
	case *optimizer.PgAvailableWalSummaries:
		return maybeInstrument(p, newPgAvailableWalSummariesOp(p)), nil
	case *optimizer.PgGetCatalogForeignKeys:
		return maybeInstrument(p, newPgGetCatalogForeignKeysOp(p)), nil
	case *optimizer.PgGetSequenceData:
		return maybeInstrument(p, newPgGetSequenceDataOp(p)), nil
	case *optimizer.PgSequenceParameters:
		return maybeInstrument(p, newPgSequenceParametersOp(p)), nil
	case *optimizer.TSTokenType:
		return maybeInstrument(p, newTSTokenTypeOp(p)), nil
	case *optimizer.VerifyHeapam:
		return maybeInstrument(p, newVerifyHeapamOp(p)), nil
	case *optimizer.ProjectSet:
		child, err := buildNode(p.Child, deformBoundFull)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newProjectSetOp(p, child)), nil
	case *optimizer.ScalarFuncScan:
		return maybeInstrument(p, newScalarFuncScanOp(p)), nil
	case *optimizer.PgPartitionTree:
		return maybeInstrument(p, newPgPartitionTreeOp(p)), nil
	case *optimizer.PgOptionsToTable:
		return maybeInstrument(p, newPgOptionsToTableOp(p)), nil
	case *optimizer.FromRegexpMatches:
		return maybeInstrument(p, newFromRegexpMatchesOp(p)), nil
	case *optimizer.FromRegexpSplitToTable:
		return maybeInstrument(p, newFromRegexpSplitToTableOp(p)), nil
	case *optimizer.CTEScan:
		// CTEScan wraps the inlined CTE body. Use cteScanOp which materializes
		// all rows on first Open() and replays them on subsequent Open() calls
		// (same CTE declaration, same ctx.CTERowCache entry). This implements PostgreSQL's
		// CTE optimization-fence: a volatile CTE (e.g. random()) produces the
		// same rows regardless of how many times it is referenced. M0097-0099.
		op, err := newCteScanOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op), nil
	case *optimizer.CTEDMLPrefix:
		return maybeInstrument(p, newCTEDMLPrefixOp(p)), nil
	case *optimizer.MaterializedCTEScan:
		return maybeInstrument(p, newMaterializedCTEScanOp(p)), nil
	case *optimizer.Project:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		// M0071-0015 Stage E: projectOp materialises evaluated
		// targets into o.out and clones for the consumer; child
		// slot lifetime is bounded by projectOp's per-Next read
		// — no borrow contract needed.
		return maybeInstrument(p, newProjectOp(p, child)), nil
	case *optimizer.Filter:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		// M0118-0002 (design 0118-0137): hand a Filter sitting directly above a
		// SeqScan its spatial predicate so a SERIALIZABLE scan of a GiST-indexed
		// table can take per-matching-tuple grid-cell SIREAD locks instead of a
		// relation-grain lock. No-op unless the runtime scan resolves a GiST index
		// (gistSSIIdxOID stays 0 otherwise). Mirrors the buildRec twin.
		if _, ok := p.Child.(*optimizer.SeqScan); ok {
			if so := unwrapSeqScanOp(child); so != nil {
				so.ssiGistPred = p.Predicate
				so.ssiGinPred = p.Predicate
				// Same predicate, second use: let the scan reject tuples
				// before deforming and deep-copying them. Pure
				// pre-rejection — filterOp still evaluates it on the
				// survivors — and armed only for expressions
				// planScanPrefilter proves safe to evaluate twice.
				// See scan_prefilter.go.
				if pf, pok := planScanPrefilter(p.Predicate, len(so.cols)); pok {
					so.prefilter, so.prefilterSet = pf, true
				}
			}
		}
		// M0054-0005a-followup: filterOp is a pure pass-through
		// — it returns its child's row unchanged. So filter's
		// own borrow contract must MATCH its child's. We leave
		// the child at the default OwnedRow at Build time;
		// filterOp.SetBorrow propagates to the child only when
		// the eventual parent (project, output sink) flips the
		// filter itself to BorrowedRow.
		return maybeInstrument(p, newFilterOp(p, child)), nil
	case *optimizer.Limit:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		// M0054-0005a-followup: limitOp is pass-through like
		// filterOp; child borrow propagates from limit's own
		// parent via SetBorrow.
		return maybeInstrument(p, newLimitOp(p, child)), nil
	case *optimizer.Sort:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newSortOp(p, child)), nil
	case *optimizer.Join:
		// EX1-01: deformBoundBelow examines the side-local keys first
		// and resets to full width below on both sides.
		joinBound := deformBoundBelow(p, bound)
		left, err := buildNode(p.Left, joinBound)
		if err != nil {
			return nil, err
		}
		right, err := buildNode(p.Right, joinBound)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newJoinOp(p, left, right)), nil
	case *optimizer.NestedLoopIndexJoin:
		outer, err := buildNode(p.Outer, deformBoundFull)
		if err != nil {
			return nil, err
		}
		// Inner is a probe node the join re-executes per outer row. The
		// legal kinds are enumerated here and by `nliInnerProbe` in the
		// optimizer; both operators satisfy `nliInner`, which is what lets
		// the join driver stay ignorant of which one it got.
		var innerScan nliInner
		switch in := p.Inner.(type) {
		case *optimizer.IndexScan:
			innerScan = newIndexScanOp(in)
		case *optimizer.IndexOnlyScan:
			innerScan = newIndexOnlyScanOp(in)
		case *optimizer.BitmapHeapScan:
			innerScan = newBitmapHeapScanOp(in)
		default:
			return nil, &ExecError{Code: "XX000", Pos: p.Pos(),
				Message: fmt.Sprintf("NestedLoopIndexJoin inner is a %T, which is not a re-probeable scan", p.Inner)}
		}
		// The memoize cache (S7) wraps only a plain index probe: `Memoize.Child`
		// is typed `*IndexScan`, and the optimizer declines to build the cache
		// for any other inner kind, so this assertion cannot fire.
		if p.InnerMemo != nil {
			innerScan = newMemoizeOp(p.InnerMemo, innerScan.(*indexScanOp))
		}
		// M0071-0013 Stage D-1: NLI now composes outer + inner via
		// a persistent VirtualSlot. outerMS.row is overwritten per
		// outer row before the inner Rescan; the IndexScan still
		// reads outer columns from the bound Row (slot-aware
		// BindOuter is M0072 future work). No borrow contract
		// needed at this boundary.
		return maybeInstrument(p, newNestedLoopIndexJoinOp(p, outer, innerScan)), nil
	case *optimizer.Aggregate:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		// M0071-0015 Stage E: aggregateOp.Open's drain loop
		// extracts value-typed Datums into aggRuntime fields and
		// into a fresh groupValues Row before pulling the next
		// child slot — slot lifetime is bounded by the per-Next
		// read, no borrow contract needed.
		return maybeInstrument(p, newAggregateOp(p, child)), nil
	case *optimizer.WindowAgg:
		child, err := buildNode(p.Child, deformBoundFull)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newWindowOp(p, child)), nil
	case *optimizer.SeqScan:
		// EX1-01: stamp the threaded bound. effectiveDeformBound maps
		// None/Full to full width; a narrow bound narrows the survivor
		// deform in Next. Unset (0) also means full (safe default for
		// directly-constructed scans that bypass both Build paths).
		op := newSeqScanOp(p)
		op.deformBound = effectiveDeformBound(bound, len(op.cols))
		return maybeInstrument(p, op), nil
	case *optimizer.IndexScan:
		return maybeInstrument(p, newIndexScanOp(p)), nil
	case *optimizer.IndexOnlyScan:
		return maybeInstrument(p, newIndexOnlyScanOp(p)), nil
	case *optimizer.Result:
		if p.Child != nil {
			// Result-with-child (S6 Slice 3d const-arg rewrite): build the inner
			// scan so the One-Time Filter can stream projected rows through it.
			child, err := buildNode(p.Child, deformBoundBelow(p, bound))
			if err != nil {
				return nil, err
			}
			return maybeInstrument(p, newResultOp(p, child)), nil
		}
		// Childless Result (S6 min/max rewrite top node): resultOp evaluates
		// Targets once and emits exactly one row. No child to Build.
		return maybeInstrument(p, newResultOp(p, nil)), nil
	case *optimizer.LockRows:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newLockRowsOp(p, child)), nil
	case *optimizer.Insert:
		child, err := buildNode(p.Source, deformBoundFull)
		if err != nil {
			return nil, err
		}
		if p.OnConflict != nil {
			return maybeInstrument(p, newUpsertOp(p, child)), nil
		}
		return maybeInstrument(p, newInsertOp(p, child)), nil
	case *optimizer.Gather:
		// Each worker builds its OWN operator tree over the shared, read-only
		// partial plan — Build is a pure function of the plan node, so N calls
		// give N independent trees. The closure is what makes that per-worker
		// construction possible without the operator knowing about the planner.
		//
		// Deliberately NOT migrated to the slab path: buildRec's default arm
		// wraps this in an OpAdapter, so the live BuildFastIterator path
		// reaches it with no slab changes and no shared per-node state.
		//
		// EX1-01: the bound is captured into the worker buildChild closure
		// so every worker's private tree narrows the same leaves. A worker
		// built through the public BuildWorker entry (no bound in scope)
		// declines to full deform.
		workerBound := deformBoundBelow(p, bound)
		return maybeInstrument(p, newGatherOp(p, func() (Operator, error) {
			return buildNode(p.Child, workerBound)
		})), nil
	case *optimizer.GatherMerge:
		// Same per-worker construction as Gather; the difference is entirely in
		// how the leader consumes the streams. The GatherMerge keys (folded
		// by deformBoundBelow) are leader-side consumers of the worker rows.
		workerMergeBound := deformBoundBelow(p, bound)
		return maybeInstrument(p, newGatherMergeOp(p, func() (Operator, error) {
			return buildNode(p.Child, workerMergeBound)
		})), nil
	case *optimizer.Distinct:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newDistinctOp(p, child)), nil
	case *optimizer.DistinctOn:
		child, err := buildNode(p.Child, deformBoundFull)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newDistinctOnOp(p, child)), nil
	case *optimizer.SetOp:
		left, err := buildNode(p.Left, deformBoundFull)
		if err != nil {
			return nil, err
		}
		right, err := buildNode(p.Right, deformBoundFull)
		if err != nil {
			left.Close()
			return nil, err
		}
		return maybeInstrument(p, newSetOp(p, left, right)), nil
	case *optimizer.RecursiveUnion:
		anchor, err := buildNode(p.Anchor, deformBoundFull)
		if err != nil {
			return nil, err
		}
		recursive, err := buildNode(p.Recursive, deformBoundFull)
		if err != nil {
			anchor.Close()
			return nil, err
		}
		return maybeInstrument(p, newRecursiveUnionOp(p, anchor, recursive)), nil
	case *optimizer.WorkTableScan:
		return maybeInstrument(p, newWorkTableScanOp(p)), nil
	case *optimizer.Update:
		op, err := newUpdateOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op), nil
	case *optimizer.Delete:
		op, err := newDeleteOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op), nil
	case *optimizer.Merge:
		return maybeInstrument(p, newMergeOp(p)), nil
	case *optimizer.DDL:
		return newDDLOp(p), nil
	case *optimizer.Transaction:
		return newTransactionOp(p), nil
	case *optimizer.Checkpoint:
		return newCheckpointOp(p), nil
	case *optimizer.Explain:
		return newExplainOp(p), nil
	case *optimizer.Utility:
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
		if sc, ok := p.Stmt.(*parser.SetConstraintsStmt); ok {
			return newSetConstraintsOp(sc), nil
		}
		if rs, ok := p.Stmt.(*parser.ReindexStmt); ok {
			return newReindexOp(rs), nil
		}
		return newUtilityNoOp(p), nil
	case *optimizer.Copy:
		// COPY is currently driven from the wire-protocol layer
		// (see internal/server/copy.go). The planner produces a
		// Copy node so the layer can resolve table/columns through
		// the catalog without re-parsing; once the executor copy
		// operator lands the Build dispatch will return it. Until
		// then, fall through with a stable feature-not-supported
		// error rather than a generic "unsupported plan node".
		return nil, &ExecError{Code: "0A000", Pos: p.Pos(), Message: "COPY is driven from the wire layer; planner.Copy has no executor path yet"}
	case *optimizer.Call:
		return newCallOp(p), nil
	case *optimizer.BitmapIndexScan:
		return maybeInstrument(p, newBitmapIndexScanOp(p)), nil
	case *optimizer.BitmapHeapScan:
		return maybeInstrument(p, newBitmapHeapScanOp(p)), nil
	case *optimizer.BitmapAnd:
		return maybeInstrument(p, newBitmapAndOp(p)), nil
	case *optimizer.BitmapOr:
		return maybeInstrument(p, newBitmapOrOp(p)), nil
	}
	return nil, &ExecError{Code: "0A000", Pos: plan.Pos(), Message: fmt.Sprintf("unsupported plan node %T", plan)}
}

// utilityNoOp is the executor counterpart to planner.Utility. v0
// uses it for VACUUM/ANALYZE statements that the wire layer
// recognises but doesn't yet have a real implementation for at the
// executor level — running them is a no-op so pgbench's `vacuum
// analyze pgbench_branches` (and similar) succeed cleanly.
type utilityNoOp struct{ plan *optimizer.Utility }

func newUtilityNoOp(p *optimizer.Utility) *utilityNoOp { return &utilityNoOp{plan: p} }

func (o *utilityNoOp) Schema() optimizer.Schema   { return nil }
func (o *utilityNoOp) Open(*Context) error      { return nil }
func (o *utilityNoOp) Next() (TupleSlot, error) { return nil, EOF }
func (o *utilityNoOp) Close() error             { return nil }

// Run is a convenience that opens an operator, drains it into a slice
// of rows, then closes. Production paths use Open/Next/Close
// directly so they can stream into the wire-protocol encoder.
func Run(op Operator, ctx *Context) ([]Row, error) {
	// M0129-S8.3: advance the command counter before executing the operator,
	// matching PG's per-statement CommandCounterIncrement in the dispatch layer
	// (production paths advance before calling Build — Run is exclusively a
	// test helper and must do the same).
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
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

// unwrapSeqScanOp returns the concrete *seqScanOp behind an operator built for a
// *planner.SeqScan, transparently peeling the maybeInstrument wrapper. Returns
// nil for anything else. Used to hand a leaf Filter's spatial predicate to the
// scan for GiST spatial-SSI locking (design 0118-0137).
func unwrapSeqScanOp(op Operator) *seqScanOp {
	switch o := op.(type) {
	case *seqScanOp:
		return o
	case *instrumentedOp:
		if so, ok := o.inner.(*seqScanOp); ok {
			return so
		}
	}
	return nil
}

// buildRec is the recursive tree builder for BuildFast. It threads the
// EX1-01 deform bound exactly like buildNode; see scan_deform.go.
func (tree *opTreeSlab) buildRec(plan optimizer.Node, bound int) (int32, error) {
	switch p := plan.(type) {
	case *optimizer.SeqScan:
		// Same stamping as the buildNode SeqScan arm.
		op := newSeqScanOp(p)
		op.deformBound = effectiveDeformBound(bound, len(op.cols))
		return tree.add(OpNode{Kind: OpSeqScan, childA: noChild, childB: noChild, state: op}), nil

	case *optimizer.Filter:
		childIdx, err := tree.buildRec(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return noChild, err
		}
		// M0118-0002 (design 0118-0137): a Filter directly above a SeqScan hands
		// the scan its spatial predicate so a SERIALIZABLE scan of a GiST-indexed
		// table takes per-matching-tuple grid-cell SIREAD locks instead of a
		// relation-grain lock. This is the LIVE server path (BuildFastIterator);
		// Build has the twin. No-op unless the scan resolves a GiST index at Open.
		if _, ok := p.Child.(*optimizer.SeqScan); ok {
			if so, ok2 := tree.ops[childIdx].state.(*seqScanOp); ok2 {
				so.ssiGistPred = p.Predicate
				so.ssiGinPred = p.Predicate
				// Same predicate, second use: let the scan reject tuples
				// before deforming and deep-copying them. Pure
				// pre-rejection — filterOp still evaluates it on the
				// survivors — and armed only for expressions
				// planScanPrefilter proves safe to evaluate twice.
				// See scan_prefilter.go.
				if pf, pok := planScanPrefilter(p.Predicate, len(so.cols)); pok {
					so.prefilter, so.prefilterSet = pf, true
				}
			}
		}
		// Phase C.3: predicate compiled into exprTreeSlab; filterState holds
		// only the predIdx — no GC-traced planner.Expr reference needed.
		predIdx := tree.exprs.buildExpr(p.Predicate)
		return tree.add(OpNode{Kind: OpFilter, childA: childIdx, childB: noChild,
			state: &filterState{predIdx: predIdx}}), nil

	case *optimizer.Project:
		childIdx, err := tree.buildRec(p.Child, deformBoundBelow(p, bound))
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

	case *optimizer.Limit:
		childIdx, err := tree.buildRec(p.Child, deformBoundBelow(p, bound))
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

	case *optimizer.Sort:
		childIdx, err := tree.buildRec(p.Child, deformBoundBelow(p, bound))
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

	case *optimizer.Update:
		op, err := newUpdateOp(p)
		if err != nil {
			return noChild, err
		}
		return tree.add(OpNode{Kind: OpUpdate, childA: noChild, childB: noChild, state: &updateOpState{op: op}}), nil

	case *optimizer.Delete:
		op, err := newDeleteOp(p)
		if err != nil {
			return noChild, err
		}
		return tree.add(OpNode{Kind: OpDelete, childA: noChild, childB: noChild, state: &deleteOpState{op: op}}), nil

	case *optimizer.Insert:
		childIdx, err := tree.buildRec(p.Source, deformBoundFull)
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

	case *optimizer.Join:
		// Same terminator rule as the buildNode Join arm: keys examined
		// first, full width below on both sides.
		joinBound := deformBoundBelow(p, bound)
		leftIdx, err := tree.buildRec(p.Left, joinBound)
		if err != nil {
			return noChild, err
		}
		rightIdx, err := tree.buildRec(p.Right, joinBound)
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
		// The incoming bound threads through (not a fresh root Build)
		// so ancestors folded above the adapter boundary still cover
		// the leaves inside; reshape boundaries below drop it again by
		// the same rules.
		legacyOp, err := buildNode(plan, bound)
		if err != nil {
			return noChild, err
		}
		return tree.add(OpNode{Kind: OpAdapter, childA: noChild, childB: noChild, state: &opAdapterState{op: legacyOp}}), nil
	}
}

// BuildFast constructs an op-tree slab from plan and returns the slab and
// the root index. Non-migrated operators are wrapped in an opAdapter that
// drives the legacy Operator interface.
func BuildFast(plan optimizer.Node) (*opTreeSlab, int32, error) {
	tree := &opTreeSlab{
		ops:     make([]OpNode, 0, 8),
		exprs:   make(exprTreeSlab, 0, 16),
		plans:   make(planTreeSlab, 0, 8),
		schemas: make([]optimizer.Schema, 0, 4),
	}
	rootIdx, err := tree.buildRec(plan, deformBoundNone)
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
