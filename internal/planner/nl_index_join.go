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
		// M0054-0006-followup-Q15b: cross-side equi-conjuncts in a
		// Filter sitting over an unconstrained *Join (no Predicate
		// / LeftKey / RightKey) are produced by view substitution
		// — the inlined view body lifts the WHERE equality up to
		// a top-level Filter rather than attaching it to the
		// Join. Push such conjuncts into the Join.Predicate so the
		// standard NLI rule fires, leaving non-cross-side
		// conjuncts on the Filter.
		if jc, ok := x.Child.(*Join); ok && jc.Predicate == nil && jc.LeftKey == nil && jc.RightKey == nil &&
			(jc.Type == JoinTypeInner || jc.Type == JoinTypeCross) {
			leftWidth := len(jc.Left.Output())
			crossEqs, residuals := splitFilterPredicateForNLI(x.Predicate, leftWidth)
			if len(crossEqs) > 0 {
				origType := jc.Type
				jc.Predicate = andChainForNLI(crossEqs)
				// A CROSS JOIN with an injected equi-conjunct is
				// semantically an INNER join — flip the type so
				// `tryBuildNLI` (which only accepts INNER/LEFT)
				// can fire.
				if jc.Type == JoinTypeCross {
					jc.Type = JoinTypeInner
				}
				newChild := walkRewriteNLI(jc, cat)
				if _, isNLI := newChild.(*NestedLoopIndexJoin); isNLI {
					if len(residuals) == 0 {
						return newChild
					}
					x.Child = newChild
					x.Predicate = andChainForNLI(residuals)
					return x
				}
				// Promotion failed — restore the Join's
				// pre-modification state so we don't leak the
				// synthetic Predicate or flipped Type.
				jc.Predicate = nil
				jc.Type = origType
			}
		}
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

// extractCommonCrossSideEquiAcrossOR (M0054-0006-followup-Q19)
// scans an OR-of-AND predicate (Q19 shape) for a cross-side
// equi-conjunct (`outer.col = inner.col`) that appears in EVERY
// disjunct. When found, it can be used as the IndexScan probe
// key while keeping the full OR as a residual Predicate on NLI.
//
// `leftWidth` is the join's left-side schema width: a `*ColumnRef`
// with `Index < leftWidth` belongs to the LEFT side.
//
// Returns (eqExpr, true) when ok; (nil, false) otherwise.
func extractCommonCrossSideEquiAcrossOR(pred Expr, leftWidth int) (Expr, bool) {
	bop, ok := pred.(*BinaryOp)
	if !ok || bop.Op != "OR" {
		return nil, false
	}
	branches := walkOrConjunctsNLI(bop)
	if len(branches) < 2 {
		return nil, false
	}
	// Collect the cross-side equi-conjuncts present in the FIRST
	// branch — these are the candidates for the common factor.
	firstEquis := []Expr{}
	for _, c := range walkAndConjunctsNLI(branches[0]) {
		if isCrossSideEquiConjunctNLI(c, leftWidth) {
			firstEquis = append(firstEquis, c)
		}
	}
	if len(firstEquis) == 0 {
		return nil, false
	}
	// For each remaining branch, narrow the candidate set to
	// those equi-conjuncts that also appear there. Equality is
	// by structural ColumnRef-pair match (Op=`=` with the same
	// two ColumnRef.Index values, in either order).
	for i := 1; i < len(branches); i++ {
		branchEquis := []Expr{}
		for _, c := range walkAndConjunctsNLI(branches[i]) {
			if isCrossSideEquiConjunctNLI(c, leftWidth) {
				branchEquis = append(branchEquis, c)
			}
		}
		next := firstEquis[:0]
		for _, candidate := range firstEquis {
			for _, b := range branchEquis {
				if sameCrossSideEquiNLI(candidate, b) {
					next = append(next, candidate)
					break
				}
			}
		}
		firstEquis = next
		if len(firstEquis) == 0 {
			return nil, false
		}
	}
	// Pick the first surviving common equi-conjunct.
	return firstEquis[0], true
}

// walkOrConjunctsNLI flattens an OR-chain into its disjunct leaves.
// A non-OR node returns a single-element slice containing itself.
func walkOrConjunctsNLI(e Expr) []Expr {
	if e == nil {
		return nil
	}
	bop, ok := e.(*BinaryOp)
	if !ok || bop.Op != "OR" {
		return []Expr{e}
	}
	out := walkOrConjunctsNLI(bop.Left)
	out = append(out, walkOrConjunctsNLI(bop.Right)...)
	return out
}

// sameCrossSideEquiNLI reports whether two cross-side equi-
// conjuncts reference the same pair of `*ColumnRef.Index` values
// (in either order). Used to identify the common factor across
// OR branches in Q19's shape.
func sameCrossSideEquiNLI(a, b Expr) bool {
	ab, aOK := a.(*BinaryOp)
	bb, bOK := b.(*BinaryOp)
	if !aOK || !bOK || ab.Op != "=" || bb.Op != "=" {
		return false
	}
	al, alOK := ab.Left.(*ColumnRef)
	ar, arOK := ab.Right.(*ColumnRef)
	bl, blOK := bb.Left.(*ColumnRef)
	br, brOK := bb.Right.(*ColumnRef)
	if !alOK || !arOK || !blOK || !brOK {
		return false
	}
	if al.Index == bl.Index && ar.Index == br.Index {
		return true
	}
	if al.Index == br.Index && ar.Index == bl.Index {
		return true
	}
	return false
}

// tryBuildNLI returns a *NestedLoopIndexJoin replacement for `j`
// when the equi-join shape and index availability admit it.
//
// Composite-index handling (M0054-0006-followup-Q9-composite):
// when the chosen index has more than one leading column, every
// such column MUST be bound by an equi-conjunct from j.Predicate;
// otherwise we refuse to promote and keep HashJoin. Promoting on
// only the leading column would emit a partial-prefix probe, and
// the unbound trailing columns would carry whatever value the
// inner scan happens to read — opening the door to the Q9
// `column "ps_suppkey" is not numeric at runtime` regression
// observed in run-012 attempt #1.
func tryBuildNLI(j *Join, cat catalog.Catalog) (*NestedLoopIndexJoin, bool) {
	// Supported join types: INNER, LEFT (existing), and as of
	// M0063-0004: Semi / Anti for index-driven EXISTS / NOT
	// EXISTS unnested by M0061-0001. RIGHT / FULL require both
	// sides materialised for outer-row preservation and stay on
	// the hash / merge paths.
	if j.Type != JoinTypeInner && j.Type != JoinTypeLeft &&
		j.Type != JoinTypeSemi && j.Type != JoinTypeAnti {
		return nil, false
	}
	// Equi-join detection: prefer the `LeftKey = RightKey` shape
	// already attached when Algo == Hash. Fall back to inspecting
	// `Predicate` for non-Hash joins. M0054-0006-followup-Q19:
	// when the predicate is OR-of-ANDs (Q19 shape), look for an
	// equi-conjunct common to every branch — it's still safe to
	// use as the IndexScan probe key, with the full OR retained
	// as a residual Predicate on the NLI.
	leftKey, rightKey, ok := extractEquiKeys(j)
	residualPred := Expr(nil)
	if !ok {
		leftWidth := len(j.Left.Output())
		if eq, found := extractCommonCrossSideEquiAcrossOR(j.Predicate, leftWidth); found {
			if bop, isBin := eq.(*BinaryOp); isBin && bop.Op == "=" {
				leftKey = bop.Left
				rightKey = bop.Right
				ok = true
				residualPred = j.Predicate
			}
		}
		if !ok {
			return nil, false
		}
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
	// Collect ALL cross-side equi-conjuncts available to bind
	// inner-side columns to outer-side expressions. The seed
	// equi-conjunct (from extractEquiKeys) is always among these.
	innerToOuter := collectCrossSideEquiKeys(j, leftWidth, innerScan)
	// M0054-0006-followup-Q19: when the equi-conjunct came from
	// the OR-factor path, j.Predicate is the giant OR which the
	// AND-walking `collectCrossSideEquiKeys` cannot decompose.
	// Seed the map directly with the common equi-conjunct so
	// the index-coverage check can fire.
	if residualPred != nil && innerKey != nil && outerKey != nil {
		if _, present := innerToOuter[innerKey.Name]; !present {
			innerToOuter[innerKey.Name] = outerKey
		}
	}
	if len(innerToOuter) == 0 {
		return nil, false
	}
	// Choose an index that lives on `innerScan.Table` and whose
	// EVERY leading column appears in `innerToOuter`. Prefer the
	// longest such index (most columns bound = most selective).
	idx, keys := pickIndexCoveringAllLeadingColumns(cat, innerScan.Table, innerToOuter)
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
	// M0063-0001: skip NLI when the outer is an isolated-scope
	// Project (a derived-table or view rename wrapper). Those
	// shapes have an asymmetric row layout: NLI's substituted
	// `outer ++ inner` schema flips relative to the original
	// `*Join{Left: outer, Right: inner-SeqScan}` if `inner` was
	// the original Right (which it usually is for Q15b /
	// derived-supplier shapes), and the parent Filter's
	// ColumnRefs — resolved against the original Left ++ Right
	// layout — would land on the wrong slots at runtime. The
	// hash-join path handles this case correctly. Q15b's NLI
	// path is what triggered the regression; this gate keeps
	// the NLI rewrite for SeqScan outers (Q14 etc.) and
	// declines for Project-wrapped derived outers.
	if p, ok := outerNode.(*Project); ok && p.IsolatedScope {
		return nil, false
	}

	// M0063-0001: re-bind each outer-side Key's ColumnRef.Index
	// against the chosen `outerNode.Output()` schema by Name.
	// `keys` are outer-side ColumnRefs originally resolved against
	// the join's pre-substitution merged-schema (FROM-cumulative
	// offsets). At runtime, NLI binds the OUTER row to the inner
	// IndexScan; the Key is evaluated against `outerRow` whose
	// width equals `len(outerNode.Output())`. If `outerNode` is a
	// derived-table sub-plan whose Output schema doesn't match the
	// FROM cumulative offsets, the Key's Index would point past
	// the bound row and `lookupKey()` would silently return zero
	// matches (Q8 / Q15b 0-rows symptom).
	//
	// Name re-bind is safe: the outer-side ColumnRef Name was
	// resolved at SQL-parse / planSelect time and stays valid
	// regardless of subsequent plan-tree shape changes. A
	// Name-ambiguous case (the same Name appears twice in the
	// outer schema, e.g. self-join) keeps the original Index
	// (defensive).
	outerSchema := outerNode.Output()
	for _, k := range keys {
		cr, ok := k.(*ColumnRef)
		if !ok || cr.Name == "" {
			continue
		}
		if newIdx := findUniqueColumnIndex(outerSchema, cr.Name, 0); newIdx >= 0 {
			cr.Index = newIdx
		}
	}

	// Build the inner IndexScan. For single-column indexes we
	// keep using `Key` for backward compatibility with all
	// existing single-column callers / tests; for composite
	// indexes we use `Keys` so the executor encodes every
	// leading column in declared order with no suffix padding.
	inner := &IndexScan{
		pos:    innerScan.Pos(),
		Table:  innerScan.Table,
		Index:  idx,
		schema: innerScan.Output(),
	}
	if len(keys) == 1 {
		inner.Key = keys[0]
	} else {
		inner.Keys = keys
	}
	// Build the joined output schema. NLI usually emits
	// `outer ++ inner` regardless of which side contributed which
	// to the original Join. M0063-0004: Semi / Anti emit only
	// the OUTER schema — the inner side is consumed only for
	// matching, never projected.
	var joinedSchema Schema
	if j.Type == JoinTypeSemi || j.Type == JoinTypeAnti {
		joinedSchema = append(Schema(nil), outerNode.Output()...)
	} else {
		joinedSchema = make(Schema, 0, len(outerNode.Output())+len(inner.Output()))
		joinedSchema = append(joinedSchema, outerNode.Output()...)
		joinedSchema = append(joinedSchema, inner.Output()...)
	}

	nli := &NestedLoopIndexJoin{
		pos:   j.pos,
		Type:  j.Type,
		Outer: outerNode,
		Inner: inner,
		// M0054-0006-followup-Q19: when the equi-conjunct came
		// from the OR-factoring path, the original OR predicate
		// stays as a residual so each emitted joined row is
		// filtered by the per-branch ANDs the OR encoded.
		// Otherwise no residual: the equi-conjunct is fully
		// consumed by the IndexScan probe and non-key conjuncts
		// are separated upstream via `pushdown.go`.
		Predicate: residualPred,
		schema:    joinedSchema,
	}
	return nli, true
}

// collectCrossSideEquiKeys walks j.Predicate (and j.LeftKey/RightKey
// when Algo=Hash) and returns a map of inner-column-name → outer
// expression. Only equi-conjuncts that genuinely cross the join
// (one ColumnRef on each side, by joined-row coordinates relative
// to leftWidth) are collected. innerScan defines which side counts
// as "inner" — the side NOT equal to innerScan is "outer".
func collectCrossSideEquiKeys(j *Join, leftWidth int, innerScan *SeqScan) map[string]Expr {
	result := map[string]Expr{}
	innerIsRight := Node(innerScan) == j.Right
	addEq := func(a, b Expr) {
		ac, aIsCol := a.(*ColumnRef)
		bc, bIsCol := b.(*ColumnRef)
		if !aIsCol || !bIsCol {
			return
		}
		aLeft := ac.Index >= 0 && ac.Index < leftWidth
		bLeft := bc.Index >= 0 && bc.Index < leftWidth
		// Must be cross-side.
		if aLeft == bLeft {
			return
		}
		// Pick the inner-side ref. innerIsRight ⇒ inner-side has
		// Index in [leftWidth, leftWidth+innerWidth).
		var innerRef, outerRef *ColumnRef
		if innerIsRight {
			if !aLeft {
				innerRef, outerRef = ac, bc
			} else {
				innerRef, outerRef = bc, ac
			}
		} else {
			if aLeft {
				innerRef, outerRef = ac, bc
			} else {
				innerRef, outerRef = bc, ac
			}
		}
		if _, exists := result[innerRef.Name]; !exists {
			result[innerRef.Name] = outerRef
		}
	}
	// Hash-join's primary equi-key.
	if j.LeftKey != nil && j.RightKey != nil {
		addEq(j.LeftKey, j.RightKey)
	}
	// AND-conjuncts in Predicate.
	for _, c := range walkAndConjunctsNLI(j.Predicate) {
		bop, ok := c.(*BinaryOp)
		if !ok || bop.Op != "=" {
			continue
		}
		addEq(bop.Left, bop.Right)
	}
	return result
}

// walkAndConjunctsNLI flattens an AND chain into its leaf conjuncts.
func walkAndConjunctsNLI(e Expr) []Expr {
	if e == nil {
		return nil
	}
	bop, ok := e.(*BinaryOp)
	if !ok || bop.Op != "AND" {
		return []Expr{e}
	}
	out := walkAndConjunctsNLI(bop.Left)
	out = append(out, walkAndConjunctsNLI(bop.Right)...)
	return out
}

// pickIndexCoveringAllLeadingColumns returns the longest B-tree
// index on `tbl` whose EVERY column is bound by an entry in
// `innerToOuter`. Single-column indexes also satisfy this when
// the bound key is in the map. Returns (nil, nil) if no such index
// exists. The returned `keys` slice is in `Index.Columns` order
// — keys[i] is the outer Expr for Index.Columns[i].
func pickIndexCoveringAllLeadingColumns(cat catalog.Catalog, tbl *catalog.Table, innerToOuter map[string]Expr) (*catalog.Index, []Expr) {
	var best *catalog.Index
	var bestKeys []Expr
	for _, idx := range cat.IndexesOnTable(tbl) {
		if !isBTreeIndex(idx) {
			continue
		}
		if len(idx.Columns) == 0 {
			continue
		}
		keys := make([]Expr, 0, len(idx.Columns))
		covered := true
		for _, col := range idx.Columns {
			outer, ok := innerToOuter[col]
			if !ok {
				covered = false
				break
			}
			keys = append(keys, outer)
		}
		if !covered {
			continue
		}
		// Prefer the longest covered index (most-selective).
		if best == nil || len(idx.Columns) > len(best.Columns) {
			best = idx
			bestKeys = keys
		}
	}
	return best, bestKeys
}

// splitFilterPredicateForNLI walks an AND-chained Filter predicate
// and partitions the leaf conjuncts into "cross-side equi-
// conjuncts" (suitable for promotion to NLI Predicate) and
// "residuals" (everything else — non-equality, same-side, scalar).
// `leftWidth` is the join's left-side schema width: a `*ColumnRef`
// with `Index < leftWidth` belongs to the LEFT side. Cross-side
// means one ColumnRef on each side. (M0054-0006-followup-Q15b.)
//
// M0054-0006-followup-Q19 extension: when an AND-leaf is itself
// an OR-of-ANDs whose every branch contains the same cross-side
// equi-conjunct, that common equi is factored out into the cross
// slice while the OR stays on the residual slice. This unlocks
// the Q19 shape where the join equi-conjunct is repeated in
// each disjunct of a 3-way OR.
func splitFilterPredicateForNLI(e Expr, leftWidth int) (cross []Expr, residual []Expr) {
	seenCrossKey := map[string]struct{}{}
	addCross := func(eq Expr) {
		// Deduplicate by ColumnRef.Index pair so a redundant
		// equi-conjunct in both an AND-leaf AND inside an OR-leaf
		// doesn't appear twice.
		bop, ok := eq.(*BinaryOp)
		if !ok || bop.Op != "=" {
			cross = append(cross, eq)
			return
		}
		l, lOK := bop.Left.(*ColumnRef)
		r, rOK := bop.Right.(*ColumnRef)
		if !lOK || !rOK {
			cross = append(cross, eq)
			return
		}
		a, b := l.Index, r.Index
		if a > b {
			a, b = b, a
		}
		key := stringFromInts(a, b)
		if _, dup := seenCrossKey[key]; dup {
			return
		}
		seenCrossKey[key] = struct{}{}
		cross = append(cross, eq)
	}
	for _, c := range walkAndConjunctsNLI(e) {
		if isCrossSideEquiConjunctNLI(c, leftWidth) {
			addCross(c)
			continue
		}
		if eq, ok := extractCommonCrossSideEquiAcrossOR(c, leftWidth); ok {
			addCross(eq)
			residual = append(residual, c)
			continue
		}
		residual = append(residual, c)
	}
	return cross, residual
}

// stringFromInts builds a small key for the dedup map without
// pulling in fmt for the hot path.
func stringFromInts(a, b int) string {
	// Two-digit-ish range is enough for join column indices.
	return string(rune(a&0x7fff)) + string(rune(b&0x7fff))
}

// isCrossSideEquiConjunctNLI reports whether `e` is a binary
// equality whose left and right ColumnRefs reference opposite
// sides of the join (one Index < leftWidth, the other ≥ leftWidth).
func isCrossSideEquiConjunctNLI(e Expr, leftWidth int) bool {
	bop, ok := e.(*BinaryOp)
	if !ok || bop.Op != "=" {
		return false
	}
	a, aOK := bop.Left.(*ColumnRef)
	b, bOK := bop.Right.(*ColumnRef)
	if !aOK || !bOK {
		return false
	}
	aLeft := a.Index >= 0 && a.Index < leftWidth
	bLeft := b.Index >= 0 && b.Index < leftWidth
	return aLeft != bLeft
}

// andChainForNLI rebuilds an AND chain from a slice of leaf
// conjuncts. Single element returns itself; empty returns nil.
func andChainForNLI(conjuncts []Expr) Expr {
	if len(conjuncts) == 0 {
		return nil
	}
	if len(conjuncts) == 1 {
		return conjuncts[0]
	}
	out := conjuncts[0]
	for i := 1; i < len(conjuncts); i++ {
		out = &BinaryOp{Op: "AND", Left: out, Right: conjuncts[i]}
	}
	return out
}

// isBTreeIndex returns true when idx.Method is "btree" (case-insensitive).
func isBTreeIndex(idx *catalog.Index) bool {
	m := idx.Method
	// avoid pulling in strings.ToLower for a hot path; method is
	// always either "btree" or "BTREE".
	if m == "btree" || m == "BTREE" {
		return true
	}
	return false
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
