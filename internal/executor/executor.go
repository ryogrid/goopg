package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// Build walks a plan tree and produces an Operator tree ready to
// Open. Storage-touching operators (SeqScan/Insert/Update/Delete)
// are wired in operators_storage.go alongside the heap-write
// machinery; this file handles the pure-compute operators.
//
// When the caller threads a non-nil instrumenter scope through
// buildNode (withInstrumentation around an EXPLAIN ANALYZE Open,
// or a Gather per-site fresh table), each returned operator is
// wrapped in an instrumentedOp via maybeInstrument so the EXPLAIN
// renderer can read per-node rows/loops/timing counters. nil-scope
// (the default, and this entry point) returns raw operators
// byte-for-byte unchanged.
func Build(plan optimizer.Node) (Operator, error) {
	return buildNode(plan, deformBoundNone, nil)
}

// BuildWorker is the per-worker entry point for Gather/GatherMerge
// closures. It used to set inWorker=true on a per-build `buildEnv` so
// tryFuseHashCascade declined inside parallel workers (C10/F4);
// M0127-P6.1 deleted runtime hash-join fusion, which was the sole
// reader of that env, so a worker build is now byte-for-byte a leader
// build. The entry point stays because gatherOp/gatherMergeOp and
// join_worker_path_test.go name it as the worker seam.
func BuildWorker(plan optimizer.Node) (Operator, error) {
	return buildNode(plan, deformBoundNone, nil)
}

// buildNode walks the plan dispatch. scope is the instrumentation
// scope for this tree (nil = uninstrumented); it is threaded through
// every recursive arm and handed to maybeInstrument — never read
// from package state (M-NIGHTLY-instrumentscope-race-fix).
func buildNode(plan optimizer.Node, bound int, scope *instrumenter) (Operator, error) {
	switch p := plan.(type) {
	case *optimizer.Values:
		return maybeInstrument(p, newValuesOp(p), scope), nil
	case *optimizer.GenerateSeries:
		return maybeInstrument(p, newGenerateSeriesOp(p), scope), nil
	case *optimizer.UserSrfScan:
		return maybeInstrument(p, newUserSrfScanOp(p), scope), nil
	case *optimizer.GenerateSubscripts:
		return maybeInstrument(p, newGenerateSubscriptsOp(p), scope), nil
	case *optimizer.FromUnnest:
		return maybeInstrument(p, newFromUnnestOp(p), scope), nil
	case *optimizer.OrdinalityWrap:
		child, err := buildNode(p.Child, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newOrdinalityOp(p, child), scope), nil
	case *optimizer.RowsFrom:
		children := make([]Operator, len(p.Funcs))
		for i, f := range p.Funcs {
			c, err := buildNode(f, deformBoundFull, scope)
			if err != nil {
				return nil, err
			}
			children[i] = c
		}
		return maybeInstrument(p, newRowsFromOp(p, children), scope), nil
	case *optimizer.PgInputErrorInfo:
		return maybeInstrument(p, newPgInputErrorInfoOp(p), scope), nil
	case *optimizer.PgGetPublicationTables:
		return maybeInstrument(p, newPgGetPublicationTablesOp(p), scope), nil
	case *optimizer.PgAvailableWalSummaries:
		return maybeInstrument(p, newPgAvailableWalSummariesOp(p), scope), nil
	case *optimizer.PgGetCatalogForeignKeys:
		return maybeInstrument(p, newPgGetCatalogForeignKeysOp(p), scope), nil
	case *optimizer.PgGetSequenceData:
		return maybeInstrument(p, newPgGetSequenceDataOp(p), scope), nil
	case *optimizer.PgWalSummaryContents:
		return maybeInstrument(p, newPgWalSummaryContentsOp(p), scope), nil
	case *optimizer.PgSequenceParameters:
		return maybeInstrument(p, newPgSequenceParametersOp(p), scope), nil
	case *optimizer.TSTokenType:
		return maybeInstrument(p, newTSTokenTypeOp(p), scope), nil
	case *optimizer.VerifyHeapam:
		return maybeInstrument(p, newVerifyHeapamOp(p), scope), nil
	case *optimizer.ProjectSet:
		child, err := buildNode(p.Child, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newProjectSetOp(p, child), scope), nil
	case *optimizer.ScalarFuncScan:
		return maybeInstrument(p, newScalarFuncScanOp(p), scope), nil
	case *optimizer.PgPartitionTree:
		return maybeInstrument(p, newPgPartitionTreeOp(p), scope), nil
	case *optimizer.PgOptionsToTable:
		return maybeInstrument(p, newPgOptionsToTableOp(p), scope), nil
	case *optimizer.FromRegexpMatches:
		return maybeInstrument(p, newFromRegexpMatchesOp(p), scope), nil
	case *optimizer.FromRegexpSplitToTable:
		return maybeInstrument(p, newFromRegexpSplitToTableOp(p), scope), nil
	case *optimizer.SubqueryScan:
		// M0146-0005w: a labelling wrapper, not an operator — PG's
		// SubqueryScan node forwards tuples unchanged, which the child
		// op already does when it is built directly. The bound threads
		// through unchanged: the wrapper's output columns are the
		// child's own columns at identical positions.
		return buildNode(p.Child, deformBoundBelow(p, bound), scope)
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
		return maybeInstrument(p, op, scope), nil
	case *optimizer.CTEDMLPrefix:
		return maybeInstrument(p, newCTEDMLPrefixOp(p), scope), nil
	case *optimizer.MaterializedCTEScan:
		return maybeInstrument(p, newMaterializedCTEScanOp(p), scope), nil
	case *optimizer.Materialize:
		// M0146-0010: PG's Material node — build the child, wrap it in the
		// replay cache. Under a nested loop this op surfaces as joinOp's
		// o.right and the join drives it through `rescannable` instead of
		// wrapping a second cache (join_nl_stream.go); anywhere else it is a
		// standalone rescannable subtree the parent pulls once.
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newMaterializeOp(child), scope), nil
	case *optimizer.Project:
		return buildProjectNode(p, bound, nil, scope)
	case *optimizer.Filter:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
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
				// E-17 / EX3-08 cut 2: ABSORB the qual into the scan and
				// return the scan itself — no filterOp above it. PG's shape
				// (Scan.plan.qual run once by ExecScanExtended,
				// execScan.h:223); the predicate is now evaluated exactly
				// once per row instead of twice. The scan evaluates it EARLY
				// on the deformed prefix when PlanScanQual allows, otherwise
				// LATE on the finished row — the position filterOp occupied.
				// See scan_prefilter.go and
				// docs/design/executor-ex3-08-scan-resident-qual/DESIGN.md.
				//
				// The *optimizer.Filter PLAN node is untouched: all three
				// EXPLAIN renderers collapse it into the scan's `Filter:`
				// line and key on the plan node, never on the operator, and
				// the post-qual row estimate lives on it. Deleting the
				// OPERATOR therefore leaves plain EXPLAIN byte-identical.
				so.qual, so.qualSet = p.Predicate, true
				if pf, pok := planScanPrefilter(p.Predicate, len(so.cols)); pok {
					so.prefilter, so.prefilterSet = pf, true
				}
				// The rejection counter needs no wiring here: the SeqScan
				// arm's own maybeInstrument already probed the raw seqScanOp
				// for filterRemoveCounter and handed it
				// &stats[scanNode].filterRejected. That is the node the
				// count belongs on — PG keeps nfiltered1 on the scan
				// (execScan.h:245) and the JSON renderer already reads it
				// there. Calling maybeInstrument(p, child) instead would
				// wire NOTHING (child is an *instrumentedOp, which has no
				// setFilterRemoveCounter forwarder) and double-wrap the scan.
				return child, nil
			}
		}
		// filterOp is a pure pass-through — it returns its child's row
		// unchanged — so its borrow contract matches its child's, and the
		// child is left at the default OwnedRow at Build time.
		return maybeInstrument(p, newFilterOp(p, child), scope), nil
	case *optimizer.Limit:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
		if err != nil {
			return nil, err
		}
		// M0054-0005a-followup: limitOp is pass-through like
		// filterOp; child borrow propagates from limit's own
		// parent via SetBorrow.
		return maybeInstrument(p, newLimitOp(p, child), scope), nil
	case *optimizer.Sort:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
		if err != nil {
			return nil, err
		}
		return finishSortNode(p, child, scope), nil
	case *optimizer.IncrementalSort:
		// M0141-S7-exec-b: PathIncrementalSort's translated node, wired like
		// *optimizer.Sort just above — same child bound, same instrumentation
		// wrapper.
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
		if err != nil {
			return nil, err
		}
		return finishIncrementalSortNode(p, child, scope), nil
	case *optimizer.Join:
		// EX1-02: per-side bounds from the merged-space remap
		// (deformJoinBounds): above-join refs mapped through the output
		// layout unioned with the remapped keys/residual.
		leftBound, rightBound := deformJoinBounds(p, bound)
		left, err := buildNode(p.Left, leftBound, scope)
		if err != nil {
			return nil, err
		}
		right, err := buildNode(p.Right, rightBound, scope)
		if err != nil {
			return nil, err
		}
		return finishJoinNode(p, left, right, leftBound, rightBound, scope), nil
	case *optimizer.NestedLoopIndexJoin:
		// EX1-02: the outer follows the left-side rule; inner rescans
		// stay out (EX1-02b).
		outer, err := buildNode(p.Outer, deformNLIOuterBound(p, bound), scope)
		if err != nil {
			return nil, err
		}
		// Inner is a probe node the join re-executes per outer row. The
		// legal kinds are enumerated here and by `nliInnerProbe` in the
		// optimizer; both operators satisfy `nliInner`, which is what lets
		// the join driver stay ignorant of which one it got.
		var innerScan nliInner
		// EX1-02b: the inner probe's consumer set is plan-static (the join
		// Predicate's inner-side refs plus the probe-local Cond, plus the
		// parent refs mapped to inner-output space), so the bound threads
		// once at build into the probe's deformBound field and Rescan does
		// no per-call work. The Predicate evaluates per joined pair against
		// the merged (outer++inner) row, so its refs split by the exprSide
		// cutoff exactly like the outer half of the frozen left-side rule
		// (index-space split, never face-value). Key probe exprs are never
		// folded — they index outer space on an inner. The Memoize wrap
		// below preserves the stamped child untouched.
		outerW := deformSideWidth(p.Outer)
		switch in := p.Inner.(type) {
		case *optimizer.IndexScan:
			iop := newIndexScanOp(in)
			innerW := deformNLIInnerWidth(in)
			_, innerPred := deformSplitPredicate(p.Predicate, outerW, innerW)
			innerAbove := deformUnionBound(deformNLIMappedInner(bound, outerW, innerW), innerPred)
			innerNcols := 0
			if in.Table != nil {
				innerNcols = len(in.Table.Columns)
			}
			iop.deformBound = effectiveDeformBound(deformIndexLeafBound(
				innerAbove, in.Cond, in.Index, in.Table), innerNcols)
			innerScan = iop
		case *optimizer.IndexOnlyScan:
			// No plan walk: the consumer is the Covered list (local fixes
			// in operators_indexonly.go).
			innerScan = newIndexOnlyScanOp(in)
		case *optimizer.BitmapHeapScan:
			bop := newBitmapHeapScanOp(in)
			innerW := deformNLIInnerWidth(in)
			_, innerPred := deformSplitPredicate(p.Predicate, outerW, innerW)
			innerAbove := deformUnionBound(deformNLIMappedInner(bound, outerW, innerW), innerPred)
			bitmapNcols := 0
			if in.Table != nil {
				bitmapNcols = len(in.Table.Columns)
			}
			bop.deformBound = effectiveDeformBound(deformBitmapLeafBound(
				innerAbove, in.BitmapQual, in.Cond), bitmapNcols)
			innerScan = bop
		default:
			return nil, &ExecError{Code: "XX000", Pos: p.Pos(),
				Message: fmt.Sprintf("NestedLoopIndexJoin inner is a %T, which is not a re-probeable scan", p.Inner)}
		}
		// The memoize cache (S7) wraps an index probe — plain or, since
		// M0146-0005bq, index-only; the optimizer builds it over no other
		// inner kind.
		if p.InnerMemo != nil {
			probe, ok := innerScan.(memoProbe)
			if !ok {
				return nil, &ExecError{Code: "XX000", Pos: p.Pos(),
					Message: fmt.Sprintf("NestedLoopIndexJoin memoizes a %T, which is not an index probe", innerScan)}
			}
			innerScan = newMemoizeOp(p.InnerMemo, probe)
		}
		// M0071-0013 Stage D-1: NLI now composes outer + inner via
		// a persistent VirtualSlot. outerMS.row is overwritten per
		// outer row before the inner Rescan; the IndexScan still
		// reads outer columns from the bound Row (slot-aware
		// BindOuter is M0072 future work). No borrow contract
		// needed at this boundary.
		return maybeInstrument(p, newNestedLoopIndexJoinOp(p, outer, innerScan), scope), nil
	case *optimizer.Aggregate:
		var child Operator
		var err error
		childBound := deformBoundBelow(p, bound)
		if p.Mode != optimizer.AggModeFinal {
			// A plain or partial aggregate is consumer-complete: its arms
			// (folded into childBound) are the only readers of its child's
			// rows (M0122-0012 slice 35, buildCompleteChild).
			child, err = buildCompleteChild(p.Child, childBound, aggConsumerExprs(p), scope)
		} else {
			child, err = buildNode(p.Child, childBound, scope)
		}
		if err != nil {
			return nil, err
		}
		// M0071-0015 Stage E: aggregateOp.Open's drain loop
		// extracts value-typed Datums into aggRuntime fields and
		// into a fresh groupValues Row before pulling the next
		// child slot — slot lifetime is bounded by the per-Next
		// read, no borrow contract needed.
		//
		// M0145-0008u: for an aggregate that provably keeps nothing of
		// the row it reads (aggregateBorrowsScanRows), a seq scan child
		// hands up its own reused row instead of a per-tuple clone.
		if aggregateBorrowsScanRows(p) {
			markScanBorrowChain(child)
		}
		if aggregateBorrowsGatherRows(p) {
			markGatherMergeBorrow(child)
		}
		if aggregateIgnoresInputOrder(p) {
			markDistinctUnordered(child)
		}
		return maybeInstrument(p, newAggregateOp(p, child), scope), nil
	case *optimizer.WindowAgg:
		child, err := buildNode(p.Child, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newWindowOp(p, child), scope), nil
	case *optimizer.SeqScan:
		// EX1-01: stamp the threaded bound. effectiveDeformBound maps
		// None/Full to full width; a narrow bound narrows the survivor
		// deform in Next. Unset (0) also means full (safe default for
		// directly-constructed scans that bypass both Build paths).
		op := newSeqScanOp(p)
		op.deformBound = effectiveDeformBound(bound, len(op.cols))
		return maybeInstrument(p, op, scope), nil
	case *optimizer.IndexScan:
		// EX1-02b: stamp the threaded bound — union of the parent walk and
		// the leaf-local Cond refs, belt-and-braces widened with the index
		// key columns. Key probe exprs are never folded at face value (on
		// an NLI inner they index outer space). Unset (0) stays full for
		// directly-constructed scans. The buildRec slab twin reaches this
		// arm through its default branch with the bound forwarded, so both
		// Build paths thread it.
		indexOp := newIndexScanOp(p)
		indexNcols := 0
		if p.Table != nil {
			indexNcols = len(p.Table.Columns)
		}
		indexOp.deformBound = effectiveDeformBound(deformIndexLeafBound(bound, p.Cond, p.Index, p.Table), indexNcols)
		return maybeInstrument(p, indexOp, scope), nil
	case *optimizer.IndexOnlyScan:
		return maybeInstrument(p, newIndexOnlyScanOp(p), scope), nil
	case *optimizer.Result:
		if p.Child != nil {
			// Result-with-child (S6 Slice 3d const-arg rewrite): build the inner
			// scan so the One-Time Filter can stream projected rows through it.
			child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
			if err != nil {
				return nil, err
			}
			return maybeInstrument(p, newResultOp(p, child), scope), nil
		}
		// Childless Result (S6 min/max rewrite top node): resultOp evaluates
		// Targets once and emits exactly one row. No child to Build.
		return maybeInstrument(p, newResultOp(p, nil), scope), nil
	case *optimizer.LockRows:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newLockRowsOp(p, child), scope), nil
	case *optimizer.Insert:
		child, err := buildNode(p.Source, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		if p.OnConflict != nil {
			return maybeInstrument(p, newUpsertOp(p, child), scope), nil
		}
		return maybeInstrument(p, newInsertOp(p, child), scope), nil
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
		return maybeInstrument(p, newGatherNode(p, bound), scope), nil
	case *optimizer.GatherMerge:
		// Same per-worker construction as Gather; the difference is entirely in
		// how the leader consumes the streams. The GatherMerge keys (folded
		// by deformBoundBelow) are leader-side consumers of the worker rows.
		return maybeInstrument(p, newGatherMergeNode(p, bound), scope), nil
	case *optimizer.Distinct:
		child, err := buildNode(p.Child, deformBoundBelow(p, bound), scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newDistinctOp(p, child), scope), nil
	case *optimizer.DistinctOn:
		child, err := buildNode(p.Child, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, newDistinctOnOp(p, child), scope), nil
	case *optimizer.SetOp:
		left, err := buildNode(p.Left, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		right, err := buildNode(p.Right, deformBoundFull, scope)
		if err != nil {
			left.Close()
			return nil, err
		}
		return maybeInstrument(p, newSetOp(p, left, right), scope), nil
	case *optimizer.RecursiveUnion:
		anchor, err := buildNode(p.Anchor, deformBoundFull, scope)
		if err != nil {
			return nil, err
		}
		recursive, err := buildNode(p.Recursive, deformBoundFull, scope)
		if err != nil {
			anchor.Close()
			return nil, err
		}
		return maybeInstrument(p, newRecursiveUnionOp(p, anchor, recursive), scope), nil
	case *optimizer.WorkTableScan:
		return maybeInstrument(p, newWorkTableScanOp(p), scope), nil
	case *optimizer.Update:
		op, err := newUpdateOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op, scope), nil
	case *optimizer.Delete:
		op, err := newDeleteOp(p)
		if err != nil {
			return nil, err
		}
		return maybeInstrument(p, op, scope), nil
	case *optimizer.Merge:
		return maybeInstrument(p, newMergeOp(p), scope), nil
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
		if _, ok := p.Stmt.(*parser.AlterSystemStmt); ok {
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
		return maybeInstrument(p, newBitmapIndexScanOp(p), scope), nil
	case *optimizer.BitmapHeapScan:
		// EX1-02b: stamp the threaded bound — BitmapQual recheck refs and
		// Cond refs folded first, then unioned with the parent walk; either
		// declining re-widens the whole bound. Inner bitmap builds (the
		// BitmapIndexScan/And/Or subtrees) stay bound-free: they build TID
		// sets, not rows. Reached from both Build paths like IndexScan.
		bitmapOp := newBitmapHeapScanOp(p)
		bitmapNcols := 0
		if p.Table != nil {
			bitmapNcols = len(p.Table.Columns)
		}
		bitmapOp.deformBound = effectiveDeformBound(deformBitmapLeafBound(bound, p.BitmapQual, p.Cond), bitmapNcols)
		return maybeInstrument(p, bitmapOp, scope), nil
	case *optimizer.BitmapAnd:
		return maybeInstrument(p, newBitmapAndOp(p), scope), nil
	case *optimizer.BitmapOr:
		return maybeInstrument(p, newBitmapOrOp(p), scope), nil
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

func (o *utilityNoOp) Schema() optimizer.Schema { return nil }
func (o *utilityNoOp) Open(*Context) error      { return nil }
func (o *utilityNoOp) Next() (TupleSlot, error) { return nil, EOF }
func (o *utilityNoOp) Close() error             { return nil }

// Run is a convenience that opens an operator, drains it into a slice
// of rows, then closes. Production paths use Open/Next/Close
// directly so they can stream into the wire-protocol encoder.
func Run(op Operator, ctx *Context) ([]Row, error) {
	// M0146-0059: one statement's CTE materialisations, as the wire
	// dispatcher resets them per statement.
	op = scopeStatementCTEs(op)
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
// scanSkipDisabled turns markProjectScanReads off. Tests set it to compare a
// skipping scan's results with the full decode.
var scanSkipDisabled = false

// markProjectScanReads records, on a seq scan directly under a non-identity
// Project, the columns the Project's targets read (M0122-0012 slice 19). The
// Project evaluates only its targets against the scan's row, and the row
// leaves the scan through nothing else, so the remaining columns of the
// deform window need not be decoded — PG's projection likewise fetches only
// the attributes its expressions reference (ExecBuildProjectionInfo,
// EEOP_SCAN_FETCHSOME up to last_scan, with by-reference values left as
// pointers). Unknown target shapes leave readCols nil: everything decodes.
// buildCompleteChild builds the child of a consumer-complete parent — a plain
// or partial Aggregate, whose arms (consumers, folded into bound) are the
// only readers of the rows the child returns. Down a chain of sorts, which
// read only their keys and return every row unchanged, that stays true once
// the keys join the consumers, so a Project at the bottom of the chain may
// evaluate just the targets they read (projectLiveSet). TPC-DS Q14's
// GroupAggregate → Sort → Project → nested-loop chains are the shape: the
// re-expansion Project otherwise evaluates every join column, the sort
// stores ~4 kB per row, and each store_sales_pkey probe deforms all 23 heap
// columns. Any other node builds normally (every target live).
func buildCompleteChild(n optimizer.Node, bound int, consumers []optimizer.Expr, scope *instrumenter) (Operator, error) {
	switch p := n.(type) {
	case *optimizer.Project:
		return buildProjectNode(p, bound, projectLiveSet(p, consumers), scope)
	case *optimizer.Sort:
		keys := append(append([]optimizer.Expr(nil), consumers...), deformSortKeyExprs(p.Keys)...)
		child, err := buildCompleteChild(p.Child, deformBoundBelow(p, bound), keys, scope)
		if err != nil {
			return nil, err
		}
		// The sorted rows reach only the consumers, and the sort itself
		// reads its keys: it stores just those columns (M0122-0012 slice
		// 71). TPC-DS Q97's worker sorts held whole 51-column join rows,
		// built twice per row, to ship two of them.
		return finishSortNodeLive(p, child, consumerLiveColumns(keys), scope), nil
	case *optimizer.Gather:
		// The workers' rows go only to these consumers, so a worker ships
		// just the columns they read (M0122-0012 slice 39).
		g := newGatherNode(p, bound)
		g.liveCols = consumerLiveColumns(consumers)
		return maybeInstrument(p, g, scope), nil
	case *optimizer.GatherMerge:
		// As Gather (M0122-0012 slice 63): the rows reach only these
		// consumers and the leader's merge, which reads the GatherMerge keys,
		// so a worker — and the leader's own participating tree — ships just
		// those columns. TPC-DS Q78's three GroupAggregate → Gather Merge
		// branches deep-copied every 50-column join row across the queue.
		readers := append(append([]optimizer.Expr(nil), consumers...), deformSortKeyExprs(p.Keys)...)
		g := newGatherMergeNodeFor(p, bound, readers)
		g.liveCols = consumerLiveColumns(readers)
		return maybeInstrument(p, g, scope), nil
	case *optimizer.IncrementalSort:
		keys := append(append([]optimizer.Expr(nil), consumers...), deformSortKeyExprs(p.Keys)...)
		child, err := buildCompleteChild(p.Child, deformBoundBelow(p, bound), keys, scope)
		if err != nil {
			return nil, err
		}
		return finishIncrementalSortNode(p, child, scope), nil
	case *optimizer.Join:
		// The join's left rows reach only the consumers (through the merged
		// output) and the join's own predicate and left keys, so the left
		// subtree is consumer-complete too (M0122-0012 slice 66). TPC-DS
		// Q39's HashAggregate → Hash Join → Gather shipped every column of
		// its inventory × date_dim × warehouse rows across the queue.
		leftConsumers, ok := joinLeftConsumers(p, consumers)
		if !ok {
			break
		}
		leftBound, rightBound := deformJoinBounds(p, bound)
		left, err := buildCompleteChild(p.Left, leftBound, leftConsumers, scope)
		if err != nil {
			return nil, err
		}
		right, err := buildNode(p.Right, rightBound, scope)
		if err != nil {
			return nil, err
		}
		return finishJoinNode(p, left, right, leftBound, rightBound, scope), nil
	case *optimizer.SeqScan:
		return buildCompleteScan(n, bound, consumers, scope)
	case *optimizer.Filter:
		// A Filter directly over the scan is absorbed into it (slice 29):
		// the scan evaluates the qual itself and skipReadSet adds the
		// qual's columns.
		if _, ok := p.Child.(*optimizer.SeqScan); ok {
			return buildCompleteScan(n, bound, consumers, scope)
		}
	}
	return buildNode(n, bound, scope)
}

// buildCompleteScan builds a seq scan (or a Filter absorbed into one) whose
// rows reach only consumers, and gives the scan their read set (M0122-0012
// C57): the scan decodes, of its deform window, only the columns the
// consumers and its own qual read. TPC-H Q18's IN-subquery HashAggregate
// reads l_orderkey and l_quantity, but HammerDB's lineitem puts l_shipdate,
// l_discount, l_extendedprice and l_suppkey before l_quantity, so the prefix
// bound alone decoded a date and five NUMERICs per row to use two. PG's
// aggregate input expressions fetch only what they reference
// (ExecBuildAggTrans, EEOP_SCAN_FETCHSOME), and slot_deform_heap_tuple
// leaves a by-reference value as a pointer, so a column nobody reads costs
// PG no conversion. Unknown consumer shapes leave the read set nil (full
// decode), as markProjectScanReads does.
func buildCompleteScan(n optimizer.Node, bound int, consumers []optimizer.Expr, scope *instrumenter) (Operator, error) {
	op, err := buildNode(n, bound, scope)
	if err != nil || scanSkipDisabled || aggScanReadSetDisabled {
		return op, err
	}
	if so := unwrapSeqScanOp(op); so != nil {
		so.readCols = deformScanReadSet(len(so.cols), consumers...)
	}
	return op, nil
}

// aggScanReadSetDisabled turns buildCompleteScan's read set off. Tests and
// the A/B set it to compare the narrowed scan with the full decode.
var aggScanReadSetDisabled = false

// finishJoinNode wires a Join over its built children — shared by buildNode's
// Join arm and buildCompleteChild so the two cannot drift.
func finishJoinNode(p *optimizer.Join, left, right Operator, leftBound, rightBound int, scope *instrumenter) Operator {
	jop := newJoinOp(p, left, right)
	// Record the bounds the children were actually built with. The
	// cooperative parallel hash build rebuilds the build-side subtree
	// from the plan and must use the SAME bound; deriving it there is
	// impossible (it depends on `bound`, i.e. on everything above this
	// join). See joinOp.deformLeftBound.
	jop.deformLeftBound, jop.deformRightBound = leftBound, rightBound
	markScanBorrowChain(joinBorrowBuildChild(jop))
	return maybeInstrument(p, jop, scope)
}

// newGatherMergeNode builds a GatherMerge whose workers each construct their
// own tree over the partial plan, narrowed by the bound captured here —
// newGatherNode's twin, shared by buildNode's arm and buildCompleteChild.
func newGatherMergeNode(p *optimizer.GatherMerge, bound int) *gatherMergeOp {
	return newGatherMergeNodeFor(p, bound, nil)
}

// newGatherMergeNodeFor is newGatherMergeNode for a Gather Merge whose rows
// are read only by readers (the consumers of a consumer-complete parent plus
// the merge keys): each participant's partial plan is then consumer-complete
// too, so it is built through buildCompleteChild with those readers — the
// worker Sort below stores only their columns (M0122-0012 slice 71). nil
// readers build the partial plan normally.
func newGatherMergeNodeFor(p *optimizer.GatherMerge, bound int, readers []optimizer.Expr) *gatherMergeOp {
	workerMergeBound := deformBoundBelow(p, bound)
	return newGatherMergeOp(p, func(scope *instrumenter) (Operator, error) {
		var op Operator
		var err error
		if readers != nil {
			op, err = buildCompleteChild(p.Child, workerMergeBound, readers, scope)
		} else {
			op, err = buildNode(p.Child, workerMergeBound, scope)
		}
		// A Sort at the root of the partial plan is read only by the Gather
		// Merge, which copies every row it pulls (transferRowForQueueLive, in
		// runWorker and advanceRow alike): the sort may keep compact rows and
		// emit through one reused row (sortOp.compactOut, M0122-0012 slice 83).
		if so := unwrapSortOp(op); so != nil {
			so.compactOut = true
		}
		return op, err
	})
}

// unwrapSortOp is unwrapSeqScanOp for a *sortOp.
func unwrapSortOp(op Operator) *sortOp {
	switch o := op.(type) {
	case *sortOp:
		return o
	case *instrumentedOp:
		if so, ok := o.inner.(*sortOp); ok {
			return so
		}
	}
	return nil
}

// newGatherNode builds a Gather whose workers each construct their own tree
// over the partial plan, narrowed by the bound captured here — shared by
// buildNode's Gather arm and buildCompleteChild so the two cannot drift.
func newGatherNode(p *optimizer.Gather, bound int) *gatherOp {
	// EX1-01: the bound is captured into the worker buildChild closure
	// so every worker's private tree narrows the same leaves. A worker
	// built through the public BuildWorker entry (no bound in scope)
	// declines to full deform.
	workerBound := deformBoundBelow(p, bound)
	return newGatherOp(p, func(scope *instrumenter) (Operator, error) {
		return buildNode(p.Child, workerBound, scope)
	})
}

// finishSortNode and finishIncrementalSortNode wrap a built child in the sort
// operator — shared by buildNode's arms and buildCompleteChild so the two
// cannot drift.
// finishSortNode builds the Sort operator over its child. A sort is a borrow
// root (M0122-0012 slice 48): sortOp.Open materialises every child slot —
// cloneRowOwned copies the row slice and promotes arena payloads — before it
// pulls the next one, the retention boundary the seq scan's per-page arena
// reset already relies on. So a seq scan under it, through the pass-through
// operators markScanBorrowChain walks, may hand up its own reused row instead
// of a per-tuple clone, as it already does under an aggregate or a Gather.
// PG's Sort likewise copies each input slot into its tuplesort
// (tuplesort_puttupleslot). TPC-DS Q97 cloned every store_sales and
// catalog_sales probe row of the Parallel Hash Joins under its two sorts.
func finishSortNode(p *optimizer.Sort, child Operator, scope *instrumenter) Operator {
	return finishSortNodeLive(p, child, nil, scope)
}

// finishSortNodeLive is finishSortNode for a sort whose output is read only in
// the columns live marks (nil = every column; sortOp.live).
func finishSortNodeLive(p *optimizer.Sort, child Operator, live []bool, scope *instrumenter) Operator {
	markScanBorrowChain(child)
	op := newSortOp(p, child)
	op.live = live
	return maybeInstrument(p, op, scope)
}

// finishIncrementalSortNode builds the IncrementalSort operator; a borrow root
// for the same reason as a Sort (readGroup materialises each slot before the
// next pull).
func finishIncrementalSortNode(p *optimizer.IncrementalSort, child Operator, scope *instrumenter) Operator {
	markScanBorrowChain(child)
	return maybeInstrument(p, newIncrementalSortOp(child, p, p.Keys, p.PresortedCount), scope)
}

// buildProjectNode builds a Project that evaluates only the targets live
// marks (nil = all of them), threading the bound those targets need to its
// child.
func buildProjectNode(p *optimizer.Project, bound int, live []bool, scope *instrumenter) (Operator, error) {
	childBound := deformProjectBelow(p, bound, live)
	var child Operator
	var err error
	// A projection's only reader of its child's rows is itself, and it
	// evaluates just its live targets: its child is therefore
	// consumer-complete, its consumers being those targets — the rule
	// buildCompleteChild applies under an aggregate, one level down
	// (M0122-0012 slice 53). TPC-DS Q14 stacks a re-expansion Project on
	// another that lists every column of its lateral joins; built with every
	// target live, that inner Project folded the joins' last column, and the
	// store_sales, item and date_dim probes beneath deformed every column of
	// every probed row. Slice 66 extends it past a child projection: TPC-DS
	// Q39's aggregate reads a Project over a Hash Join over a Gather, whose
	// workers shipped whole join rows.
	child, err = buildCompleteChild(p.Child, childBound, liveProjectTargets(p, live), scope)
	if err != nil {
		return nil, err
	}
	// M0071-0015 Stage E: projectOp materialises evaluated
	// targets into o.out and clones for the consumer; child
	// slot lifetime is bounded by projectOp's per-Next read
	// — no borrow contract needed.
	markProjectScanReads(p, child, live)
	pop := newProjectOp(p, child)
	pop.live = live
	return maybeInstrument(p, pop, scope), nil
}

// liveProjectTargets is p's targets that live marks (every target when live
// is nil) — the expressions p evaluates against its child's rows.
func liveProjectTargets(p *optimizer.Project, live []bool) []optimizer.Expr {
	if live == nil {
		return p.Targets
	}
	out := make([]optimizer.Expr, 0, len(p.Targets))
	for i, t := range p.Targets {
		if live[i] {
			out = append(out, t)
		}
	}
	return out
}

func markProjectScanReads(p *optimizer.Project, child Operator, live []bool) {
	if scanSkipDisabled || isIdentityProject(p) {
		return
	}
	switch c := p.Child.(type) {
	case *optimizer.SeqScan:
	case *optimizer.Filter:
		// M0122-0012 slice 29: a Filter directly over the scan is absorbed
		// into it (the child operator IS the seqScanOp), and Open adds the
		// qual's own columns to the read set.
		if _, ok := c.Child.(*optimizer.SeqScan); !ok {
			return
		}
	default:
		return
	}
	so := unwrapSeqScanOp(child)
	if so == nil {
		return
	}
	targets := p.Targets
	if live != nil {
		targets = nil
		for i, t := range p.Targets {
			if live[i] {
				targets = append(targets, t)
			}
		}
	}
	so.readCols = deformScanReadSet(len(so.cols), targets...)
}

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
				// E-17 / EX3-08 cut 2, SIBLING of the buildNode arm: absorb
				// the qual and return the scan's node index, emitting no
				// OpFilter. This is the LIVE server path (BuildFastIterator)
				// and the one parallel workers build through, so a fix in
				// buildNode alone would leave every worker evaluating the
				// predicate twice — this codebase's documented sibling-path
				// failure mode.
				//
				// No instrumentation counter is wired here: buildRec never
				// calls maybeInstrument, so its nodes carry no nodeStats at
				// all (the old OpFilter path counted nothing either).
				so.qual, so.qualSet = p.Predicate, true
				if pf, pok := planScanPrefilter(p.Predicate, len(so.cols)); pok {
					so.prefilter, so.prefilterSet = pf, true
				}
				return childIdx, nil
			}
		}
		// Phase C.3: predicate compiled into exprTreeSlab; filterState holds
		// only the predIdx — no GC-traced planner.Expr reference needed.
		predIdx := tree.exprs.buildExpr(p.Predicate)
		return tree.add(OpNode{Kind: OpFilter, childA: childIdx, childB: noChild,
			state: &filterState{predIdx: predIdx, needsCTID: exprTreeUsesCTID(p.Predicate)}}), nil

	case *optimizer.Project:
		childIdx, err := tree.buildRec(p.Child, deformBoundBelow(p, bound))
		if err != nil {
			return noChild, err
		}
		// Phase C.3: target expressions compiled into exprTreeSlab; projectState
		// holds only compiled indices — no GC-traced *planner.Project or schema slice.
		// Schema is pooled in opTreeSlab.schemas; projectState stores an int32 index.
		// Every target is live: a slab parent is never a consumer-complete
		// Aggregate (buildProjectNode is the only narrowing builder).
		if so, ok := tree.ops[childIdx].state.(*seqScanOp); ok {
			markProjectScanReads(p, so, nil)
		}
		targExprs := make([]int32, len(p.Targets))
		for i, t := range p.Targets {
			targExprs[i] = tree.exprs.buildExpr(t)
		}
		schemaIdx := int32(len(tree.schemas))
		tree.schemas = append(tree.schemas, p.Output())
		return tree.add(OpNode{Kind: OpProject, childA: childIdx, childB: noChild,
			state: &projectState{schemaIdx: schemaIdx, targExprs: targExprs, needsCTID: exprTreeUsesCTID(p.Targets...)}}), nil

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
		// Same per-side remap as the buildNode Join arm.
		leftBound, rightBound := deformJoinBounds(p, bound)
		leftIdx, err := tree.buildRec(p.Left, leftBound)
		if err != nil {
			return noChild, err
		}
		rightIdx, err := tree.buildRec(p.Right, rightBound)
		if err != nil {
			return noChild, err
		}
		leftOp := &opNodeOperator{tree: tree, idx: leftIdx, schema: p.Left.Output()}
		rightOp := &opNodeOperator{tree: tree, idx: rightIdx, schema: p.Right.Output()}
		op := newJoinOp(p, leftOp, rightOp)
		// Same stamping as the buildNode Join arm — see joinOp.deformLeftBound.
		op.deformLeftBound, op.deformRightBound = leftBound, rightBound
		return tree.add(OpNode{Kind: OpJoin, childA: noChild, childB: noChild, state: &joinOpState{op: op, schema: p.Output()}}), nil

	default:
		// For non-migrated operators, build the legacy Operator tree
		// and wrap in an adapter. This path preserves the existing
		// operator semantics exactly — Open/Next/Close are forwarded.
		// The incoming bound threads through (not a fresh root Build)
		// so ancestors folded above the adapter boundary still cover
		// the leaves inside; reshape boundaries below drop it again by
		// the same rules.
		// The op-tree slab path is never instrumented (EXPLAIN ANALYZE
		// builds through the legacy buildNode path, not buildRec), so
		// the adapter's legacy subtree is built with a nil scope.
		legacyOp, err := buildNode(plan, bound, nil)
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

// stmtCTEScopeOp gives one statement execution its own CTE materialisations
// (M0146-0059). ctx.CTERowCache and ctx.CTEStableCache are keyed by CTE
// declaration, and a routine body's statements run on one Context, so a
// second statement declaring `x` at the same position replayed the first
// statement's rows. PG starts every statement with fresh CTE tuplestores.
// The statement's caches are swapped in around each Open, Next and Close
// and out again afterwards, as the LATERAL join does per outer tuple: an
// enclosing statement that is still replaying its own CTE scans keeps its
// caches, and a statement whose operator outlives its caller (a cursor
// fetched by later statements) never leaks its entries into them.
//
// It also owns the statement's AFTER trigger query level (M0146-0076),
// swapped in the same way and fired once the root has closed cleanly.
type stmtCTEScopeOp struct {
	Operator
	ctx    *Context
	rows   map[string][]Row
	stable map[string][]Row
	trig   stmtAfterTriggers
}

// scopeStatementCTEs wraps op, the root of one statement's operator tree.
func scopeStatementCTEs(op Operator) Operator {
	if op == nil {
		return nil
	}
	if _, already := op.(*stmtCTEScopeOp); already {
		return op
	}
	return &stmtCTEScopeOp{Operator: op}
}

// buildStatementScoped is Build for the root of one statement's plan.
func buildStatementScoped(plan optimizer.Node) (Operator, error) {
	op, err := Build(plan)
	if err != nil {
		return op, err
	}
	return scopeStatementCTEs(op), nil
}

func (o *stmtCTEScopeOp) enter() func() {
	ctx := o.ctx
	if ctx == nil {
		return func() {}
	}
	savedRows, savedStable := ctx.CTERowCache, ctx.CTEStableCache
	ctx.CTERowCache, ctx.CTEStableCache = o.rows, o.stable
	savedTrig := o.trig.swapIn(ctx)
	return func() {
		o.trig.swapOut(ctx, savedTrig)
		o.rows, o.stable = ctx.CTERowCache, ctx.CTEStableCache
		ctx.CTERowCache, ctx.CTEStableCache = savedRows, savedStable
	}
}

func (o *stmtCTEScopeOp) Open(ctx *Context) error {
	o.ctx, o.rows, o.stable = ctx, nil, nil
	o.trig = stmtAfterTriggers{}
	defer o.enter()()
	err := o.Operator.Open(ctx)
	o.trig.noteErr(err)
	return err
}

func (o *stmtCTEScopeOp) Next() (TupleSlot, error) {
	defer o.enter()()
	slot, err := o.Operator.Next()
	o.trig.noteErr(err)
	return slot, err
}

func (o *stmtCTEScopeOp) Close() error {
	defer o.enter()()
	return o.trig.finish(o.ctx, o.Operator.Close())
}

// RowsAffected forwards the root's DML count, which the embedded Operator
// interface does not promote (M0146-0081).
func (o *stmtCTEScopeOp) RowsAffected() int64 {
	if rc, ok := o.Operator.(RowCounter); ok {
		return rc.RowsAffected()
	}
	return 0
}
