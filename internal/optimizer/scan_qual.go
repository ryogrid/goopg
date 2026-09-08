package optimizer

// Scan-resident qual analysis (E-17 / EX3-08 cut 2, design
// docs/design/executor-ex3-08-scan-resident-qual/DESIGN.md).
//
// PG carries the scan qual on the scan plan node (`Scan.plan.qual`) and
// `ExecScanExtended` evaluates it exactly ONCE per tuple, before projection
// (`execScan.h:223`). goopg's executor absorbs the qual of a `Filter` sitting
// directly above a `SeqScan` into the scan for the same reason, and this file
// is the plan-time half of that: it answers "how may the scan evaluate this
// qual?".
//
// Two answers, and keeping them apart is the whole point:
//
//   - MaxCols — the exclusive upper bound on the column indexes the qual
//     reads. The scan deforms [0, MaxCols) before an EARLY evaluation, so an
//     UNDER-count is a wrong answer: the qual would read a cell that still
//     holds the PREVIOUS tuple's datum. This must be computed by a FULL
//     descent, always.
//   - Early — whether the qual may be judged on that bare prefix at all. A
//     "no" costs performance only: the scan then evaluates LATE, on the
//     finished row, which is exactly what the deleted filterOp did.
//
// Built on walkExprRefs / exprChildSlots rather than a hand-written type
// switch, for two reasons. First, exprChildSlots enumerates all 32 Expr types
// with no `default:` and is kept exhaustive by exprwalk_exhaustive_test.go,
// and walkExprRefs ABORTS on an unenumerated type — fail-closed by
// construction ("Silence must mean 'refuse', not 'safe'"). Second, a new
// hand-written Expr switch would be a fresh member of the RC-1a defect class
// the walker-inventory gate exists to end (exprwalk_inventory_test.go).
//
// NOTE on the mechanism, because the obvious spelling is wrong: eligibility
// is NOT expressed by returning false from Visit. A false return PRUNES the
// node's children without aborting (exprwalk.go:333-335), which would stop
// the descent and leave a ColumnRef beneath a vetoed node UNSEEN — the exact
// MaxCols under-count described above. Visit therefore always returns true
// and records the column bound; ineligibility is a side effect on the
// closure.

// ScanQualPlan reports how a scan may evaluate a qual.
type ScanQualPlan struct {
	// Early is true when the qual may be evaluated on the deformed
	// prefix [0, MaxCols). False means the scan must evaluate it on the
	// finished row.
	Early bool
	// MaxCols is the exclusive upper bound on the column indexes the qual
	// reads. Meaningful only when Early is true.
	MaxCols int
}

// PlanScanQual analyses qual for a scan whose row is ncols wide.
//
// Early is false — fail-closed — for a nil qual, an unenumerated expression
// type, a qual that reads no column at all or one that reads past ncols, a
// qual carrying an inner plan (a correlated subquery's own coordinate space),
// and for the leaf kinds that cannot be judged from a bare prefix. It is also
// false when the qual reads EVERY column, because then the two-phase deform
// saves nothing and only adds a resume offset.
func PlanScanQual(qual Expr, ncols int) ScanQualPlan {
	if qual == nil || ncols <= 0 {
		return ScanQualPlan{}
	}
	maxIdx := -1
	early := true
	ok := walkExprRefs(qual, scopeSignal, exprVisitor{
		Visit: func(e Expr) bool {
			// ALWAYS true: see the note above. Returning false here
			// would prune the subtree and under-count maxIdx.
			switch x := e.(type) {
			case *ColumnRef:
				if x.Index < 0 {
					early = false
					break
				}
				if x.Index > maxIdx {
					maxIdx = x.Index
				}
			case *OuterColumnRef, *ParamRef, *ExecParamRef:
				// Depend on state the scan does not own at this point
				// in Next.
				early = false
			case *CTIDExpr, *TableOidExpr, *MergeWholeRowRef, *MergeActionExpr:
				// Read the tuple or the row as a whole rather than named
				// columns, so MaxCols cannot bound them. CTIDExpr in
				// particular is unbounded here for a concrete reason: the
				// scan stamps hasCTID/ctidBlock/ctidOff BELOW the row
				// clone, so the prefix slot has no ctid to read.
				early = false
			case *SubqueryExpr, *ExistsExpr, *InExpr, *ArraySubqueryExpr,
				*MultiAssignSubqRow, *MultiAssignSubqElem:
				// Named explicitly as well as caught by OnScope, because
				// OnScope is NOT sufficient: exprChildSlots reports the
				// slotInnerPlan slot only when the node's Plan is non-nil
				// (exprwalk.go:228-236), so a subquery node that has not been
				// planned yet — or that is constructed bare, as tests do —
				// would walk as a childless leaf and read as EARLY-eligible.
				// That is fail-OPEN, and it is exactly the direction this
				// analysis must never fail in. Found by
				// TestPlanScanPrefilterWhitelist's "subquery — declined" case.
				early = false
			case *FuncCall:
				// Overloads are not resolved at this point and volatility
				// is therefore unknown. PG places no such restriction on a
				// scan qual; this is a goopg performance classification,
				// not a safety one — an ineligible qual is still evaluated,
				// just later.
				early = false
			}
			return true
		},
		// A slotInnerPlan / slotSubqRow crossing under scopeSignal: the
		// qual opens a plan of its own. This is the GENERAL guard — it
		// catches a future scope-opening node the Visit switch above does
		// not name — while the named arms cover the nil-Plan spelling this
		// hook cannot see.
		OnScope: func(Node) { early = false },
		// An Expr type the traversal layer has never been taught.
		OnUnknown: func(Expr) { early = false },
	})
	if !ok {
		// scopeVeto or an unenumerated type aborted the walk. The bound is
		// not trustworthy, so no early evaluation.
		return ScanQualPlan{}
	}
	if !early {
		return ScanQualPlan{}
	}
	// A qual that reads no column at all (a constant) would make the
	// "partial row" empty; one that reads past the row is a bound we cannot
	// honour.
	if maxIdx < 0 || maxIdx >= ncols {
		return ScanQualPlan{}
	}
	need := maxIdx + 1
	// Reading the whole row saves no deform work.
	if need >= ncols {
		return ScanQualPlan{}
	}
	return ScanQualPlan{Early: true, MaxCols: need}
}
