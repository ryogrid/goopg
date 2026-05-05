package planner

// Nested-Loop Index Join planner rule (M0054-0006c).
//
// After `rewriteScanInputsWithSingleTablePredicates` has tightened
// the input scans where possible, this pass walks the plan tree
// looking for binary `*Join{Algo:JoinAlgoHash}` (and the rare
// `JoinAlgoNestedLoop` with a SeqScan inner) whose join predicate
// has at least one equi-conjunct of the shape `outer.colA =
// inner.colB`, where:
//
//   - `outer` is the join's `Left` (or `Right` for BuildLeft hash
//     joins; we treat the build side as the outer driver),
//   - `inner` is a `*SeqScan` whose table has a single-column
//     B-tree index keyed on `colB` (or a composite index whose
//     leading column is `colB`),
//   - the join is INNER or LEFT (RIGHT/FULL stay on Hash/Merge
//     because the outer-row preservation contract requires both
//     sides be in memory).
//
// When the cost gate accepts (see `nliCostGateAccepts`), the
// rule rewrites the `*Join` to a `*NestedLoopIndexJoin` whose
// inner is a brand-new `*IndexScan` with `Key` set to the OUTER
// column reference. The executor's `indexScanOp.lookupKey`
// resolves the column reference against the row that the parent
// `nestedLoopIndexJoinOp` binds before each `Rescan` (M0054-0006a).
//
// The rule has a kill-switch: when the GUC `enable_nestloop_index`
// is `off`, the walk is a no-op. Per the M0054 no-deferral clause
// this provides the rollback path.

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
)

// nliEnabled is the package-level kill-switch the M0054-0006
// rollback path uses. Initialised to "on" (1). Tests toggle it via
// `SetNLIEnabled(false)`. The corresponding `enable_nestloop_index`
// GUC is registered in `internal/config/defaults.go`; runtime SET
// to consult this flag is plumbed through the dispatch path in a
// follow-up (M0054-0006e-followup) so the present land has a
// programmatic kill-switch but not a SQL-driven one.
var nliEnabled atomic.Bool

func init() {
	nliEnabled.Store(true)
}

// SetNLIEnabled flips the M0054-0006 NLI rule on or off. Test-
// only API; the production toggle path is the GUC mentioned above.
func SetNLIEnabled(on bool) {
	nliEnabled.Store(on)
}

// nliMaxOuterRowsHeuristic is the row-count threshold above which
// NLI is rejected unless cost-aware statistics say otherwise. The
// number is intentionally permissive: the dominant costs in goopg
// are GC and per-row decoding, not the index-probe overhead, so
// even moderately large outer side benefits from NLI when the
// inner is highly selective.
const nliMaxOuterRowsHeuristic int64 = 100000

// rewriteJoinsToNLI walks the plan tree and rewrites eligible
// `*Join` nodes to `*NestedLoopIndexJoin`. Mutates in place; the
// returned root is the same node (or a substitute when the root
// itself is rewritten). Catalog is needed to look up indexes.
// The package-level kill-switch `nliEnabled` short-circuits the
// walk when off.
func rewriteJoinsToNLI(n Node, cat catalog.Catalog) Node {
	if n == nil || cat == nil {
		return n
	}
	if !nliEnabled.Load() {
		return n
	}
	return walkRewriteNLI(n, cat)
}

func walkRewriteNLI(n Node, cat catalog.Catalog) Node {
	if n == nil {
		return nil
	}
	switch x := n.(type) {
	case *Join:
		x.Left = walkRewriteNLI(x.Left, cat)
		x.Right = walkRewriteNLI(x.Right, cat)
		if nli, ok := tryBuildNLI(x, cat); ok {
			return nli
		}
		return x
	case *Filter:
		x.Child = walkRewriteNLI(x.Child, cat)
		return x
	case *Project:
		x.Child = walkRewriteNLI(x.Child, cat)
		return x
	case *Sort:
		x.Child = walkRewriteNLI(x.Child, cat)
		return x
	case *Limit:
		x.Child = walkRewriteNLI(x.Child, cat)
		return x
	case *Aggregate:
		x.Child = walkRewriteNLI(x.Child, cat)
		return x
	case *WindowAgg:
		x.Child = walkRewriteNLI(x.Child, cat)
		return x
	case *MultiHashJoin:
		for i := range x.Tables {
			x.Tables[i] = walkRewriteNLI(x.Tables[i], cat)
		}
		return x
	case *NestedLoopIndexJoin:
		x.Outer = walkRewriteNLI(x.Outer, cat)
		// Inner is *IndexScan — leaf; nothing to recurse.
		return x
	}
	return n
}

// tryBuildNLI returns a *NestedLoopIndexJoin replacement for `j`
// when the equi-join shape and index availability admit it.
func tryBuildNLI(j *Join, cat catalog.Catalog) (*NestedLoopIndexJoin, bool) {
	// Only INNER and LEFT join types are supported. RIGHT/FULL
	// require both sides materialised for the outer-row
	// preservation contract.
	if j.Type != JoinTypeInner && j.Type != JoinTypeLeft {
		return nil, false
	}
	// Equi-join detection: prefer the `LeftKey = RightKey` shape
	// already attached when Algo == Hash. Fall back to inspecting
	// `Predicate` for non-Hash joins.
	leftKey, rightKey, ok := extractEquiKeys(j)
	if !ok {
		return nil, false
	}
	leftWidth := len(j.Left.Output())
	// Identify which side of the equi-conjunct references the
	// inner SeqScan. The "outer driver" is the OPPOSITE side.
	leftCol, leftIsCol := leftKey.(*ColumnRef)
	rightCol, rightIsCol := rightKey.(*ColumnRef)
	if !leftIsCol || !rightIsCol {
		return nil, false
	}
	innerScan, innerKey, outerKey := pickInnerSide(j, leftCol, rightCol, leftWidth)
	if innerScan == nil {
		return nil, false
	}
	idx := findBTreeIndexForColumn(cat, innerScan.Table, innerKey.Name)
	if idx == nil {
		return nil, false
	}
	// Cost gate: reject if the outer side is plausibly larger
	// than the heuristic and we have no statistics to override.
	outerNode := otherChild(j, innerScan)
	if outerNode == nil {
		return nil, false
	}
	if !nliCostGateAccepts(outerNode, innerScan, idx) {
		return nil, false
	}

	// Build the inner IndexScan whose Key references the outer
	// column. The Key Expr is the OUTER ColumnRef directly — its
	// `Index` is in joined-row coordinates (`outer ++ inner`),
	// which is exactly what `nestedLoopIndexJoinOp` binds before
	// each Rescan, so `evalExpr` resolves correctly.
	inner := &IndexScan{
		pos:    innerScan.Pos(),
		Table:  innerScan.Table,
		Index:  idx,
		Key:    outerKey,
		schema: innerScan.Output(),
	}
	// Build the joined output schema. NLI always emits
	// `outer ++ inner` regardless of which side contributed which
	// to the original Join — the Join's `schema` already encodes
	// this. We rebuild from outer.Output() ++ inner.Output() to
	// keep the substitution self-consistent.
	joinedSchema := make(Schema, 0, len(outerNode.Output())+len(inner.Output()))
	joinedSchema = append(joinedSchema, outerNode.Output()...)
	joinedSchema = append(joinedSchema, inner.Output()...)

	nli := &NestedLoopIndexJoin{
		pos:       j.pos,
		Type:      j.Type,
		Outer:     outerNode,
		Inner:     inner,
		Predicate: nil, // residuals stay attached at outer Filter; the
		// equi-conjunct is fully consumed by the IndexScan probe,
		// and the planner already separated non-key conjuncts
		// upstream via `pushdown.go`.
		schema: joinedSchema,
	}
	return nli, true
}

// extractEquiKeys returns the `(leftKey, rightKey, ok)` triple from
// a join. For Algo=Hash, `j.LeftKey`/`j.RightKey` are pre-populated
// by the planner; for other algos we look at `j.Predicate` if it is
// a single equality.
func extractEquiKeys(j *Join) (Expr, Expr, bool) {
	if j.LeftKey != nil && j.RightKey != nil {
		return j.LeftKey, j.RightKey, true
	}
	if bop, ok := j.Predicate.(*BinaryOp); ok && bop.Op == "=" {
		return bop.Left, bop.Right, true
	}
	return nil, nil, false
}

// pickInnerSide identifies which child is the inner SeqScan
// candidate, and which ColumnRef refers to the inner / outer side.
// `leftWidth` is the width of `j.Left.Output()` so we can tell
// which `ColumnRef.Index` belongs to which side.
//
// Returns (innerSeqScan, innerColumnRef, outerColumnRef). innerScan
// is nil when neither side is a `*SeqScan` directly.
func pickInnerSide(j *Join, leftCol, rightCol *ColumnRef, leftWidth int) (*SeqScan, *ColumnRef, *ColumnRef) {
	// The ColumnRef whose `Index` lies in `[0, leftWidth)` belongs
	// to the LEFT side; the other belongs to RIGHT.
	leftIsLeftSide := leftCol.Index >= 0 && leftCol.Index < leftWidth
	rightIsLeftSide := rightCol.Index >= 0 && rightCol.Index < leftWidth

	var leftSideRef, rightSideRef *ColumnRef
	if leftIsLeftSide && !rightIsLeftSide {
		leftSideRef = leftCol
		rightSideRef = rightCol
	} else if !leftIsLeftSide && rightIsLeftSide {
		leftSideRef = rightCol
		rightSideRef = leftCol
	} else {
		// Both refs on the same side, or ambiguous — not an
		// equi-join across the two children.
		return nil, nil, nil
	}

	// Prefer making the RIGHT side the inner (matches goopg's
	// existing build-on-right convention; covers the common
	// fact-table-on-right TPC-H pattern). When the right side is
	// not a SeqScan, try the LEFT side.
	if rss, ok := j.Right.(*SeqScan); ok {
		return rss, rightSideRef, leftSideRef
	}
	if lss, ok := j.Left.(*SeqScan); ok {
		// Inner is left; outer is right.
		return lss, leftSideRef, rightSideRef
	}
	return nil, nil, nil
}

// otherChild returns the child of `j` that is NOT the given
// `innerScan`. When neither child is `innerScan`, returns nil.
func otherChild(j *Join, innerScan *SeqScan) Node {
	if Node(innerScan) == j.Left {
		return j.Right
	}
	if Node(innerScan) == j.Right {
		return j.Left
	}
	return nil
}

// nliCostGateAccepts returns true when the cost model finds NLI
// preferable to (or at least competitive with) Hash for the given
// shape. The heuristic:
//
//   - When the outer side has an estimated row count ≤
//     `nliMaxOuterRowsHeuristic`, NLI is preferred (each outer row
//     costs an index probe; the build cost of Hash dominates for
//     small outer sides).
//   - Otherwise, NLI is rejected — Hash's amortised O(L+R) wins
//     over NLI's O(L * log R) at scale until cost-model
//     statistics from M0006 are wired to override this threshold.
//
// Rows are estimated via the existing `EstimateRows`; when
// statistics are absent, the function returns small values and
// NLI is accepted by default — which is fine for the goopg
// workloads where the outer driver of an unanalysed table is
// typically small (CTEs, derived tables, small dimension tables).
func nliCostGateAccepts(outer Node, innerScan *SeqScan, idx *catalog.Index) bool {
	outerRows := EstimateRows(outer)
	if outerRows <= 0 {
		// No estimate available — be optimistic. The cost gate
		// will be tightened in M0054-0006 follow-ups when
		// statistics-aware row counts land.
		return true
	}
	if outerRows <= nliMaxOuterRowsHeuristic {
		return true
	}
	return false
}
