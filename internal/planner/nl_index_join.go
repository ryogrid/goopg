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
	"fmt"
	"os"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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
	// D6.3a escape hatch: GOOPG_NLI_COSTGATE=legacy restores the
	// stats-blind semi/anti gate for one stage (deleted in R2-8 if
	// unused).
	nliCostGateLegacy.Store(os.Getenv("GOOPG_NLI_COSTGATE") == "legacy")
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
			// S7 (D5.1): consider a Memoize cache on the inner probe.
			// The Filter-promotion path above recurses through this
			// case, so one insertion point covers every NLI build.
			return maybeAttachMemoize(nli)
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
		// M0067-0003 attempt: Q9 composite-NLI hoist. Reverted —
		// the hoist correctly moves `ps_partkey = l_partkey` from
		// the outer Filter into the Hash join's Predicate so
		// composite `partsupp_pk` fires, but the rebind still
		// surfaces the schema-annotation-vs-runtime-layout
		// mismatch (Q9 returned 1 row instead of canonical 7,
		// confirming the rebind picked the wrong runtime slot).
		// Defer to M0068 once the underlying schema/layout
		// reconciliation is solved.
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
	if !ok || bop.Op != parser.OpOr {
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
	if !ok || bop.Op != parser.OpOr {
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
	if !aOK || !bOK || ab.Op != parser.OpEq || bb.Op != parser.OpEq {
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
	// S6 (D6.2): accept `Filter{SeqScan}` as the RIGHT inner side by
	// tentatively unwrapping it — the Filter's conjuncts join the
	// residual set (indices shifted into outer++inner coordinates via
	// clones; the originals stay untouched) and the bare SeqScan takes
	// its place for the rest of the analysis. `committed` gates the
	// restore: every decline path below leaves j exactly as found.
	//
	// This is what lets a decorrelated EXISTS body with an inner-only
	// conjunct (Q4's `l_commitdate < l_receiptdate`) stay index-driven:
	// its inner arrives as Filter{residual}(SeqScan), which previously
	// fell through pickInnerSide to the hash path.
	//
	// LEFT is deliberately excluded: its no-match fallback currently
	// evaluates the residual against the null-padded row (see
	// operators_nljoin.go), which would drop preserved outer rows —
	// and the canonical Q13 LEFT-join shape (`orders` behind a
	// NOT-LIKE Filter) is this repo's most expensive historical
	// silent-regression tripwire. The hash path keeps serving it.
	// Semi/Anti unwrap unconditionally; INNER behind a cost check
	// (D6.3b). The first cut included INNER unconditionally and
	// regressed TPC-H Q9 (115 s → DNF at 300 s): its `part` scan with a
	// `%green%` LIKE arrives as Filter{SeqScan}, and unwrapping it
	// turned a hash-side build into ~6 M per-row index probes with the
	// LIKE evaluated per probe. Semi/Anti have no such exposure (their
	// outer is the small side by construction of the unnest rewrite).
	// For INNER the unwrap is tentative here and confirmed by
	// `innerUnwrapCostAccepts` once the probe index is known — a
	// decline returns through the deferred restore below, so the hash
	// path keeps serving exactly as before.
	committed := false
	var hoistedInner []Expr
	innerUnwrapped := false
	var innerUnwrapResidualMult int64
	if f, isF := j.Right.(*Filter); isF &&
		(j.Type == JoinTypeSemi || j.Type == JoinTypeAnti || j.Type == JoinTypeInner) {
		if ss, isSS := f.Child.(*SeqScan); isSS && f.Predicate != nil && !f.LeafLocal {
			shift := len(j.Left.Output())
			okAll := true
			for _, c := range splitAnd(f.Predicate) {
				if bc, isBool := c.(*BooleanConst); isBool && bc.Value {
					continue
				}
				cl, supported := cloneExprShiftIdx(c, shift)
				if !supported {
					okAll = false
					break
				}
				hoistedInner = append(hoistedInner, cl)
			}
			if okAll {
				savedRight := j.Right
				j.Right = ss
				defer func() {
					if !committed {
						j.Right = savedRight
					}
				}()
				if j.Type == JoinTypeInner {
					innerUnwrapped = true
					innerUnwrapResidualMult = residualCostMultiplier(splitAnd(f.Predicate))
				}
			} else {
				hoistedInner = nil
			}
		}
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
			if bop, isBin := eq.(*BinaryOp); isBin && bop.Op == parser.OpEq {
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
	if !nliCostGateAccepts(j.Type, outerNode, innerScan, idx) {
		return nil, false
	}
	if innerUnwrapped && !innerUnwrapCostAccepts(outerNode, innerScan, idx, innerUnwrapResidualMult) {
		// The deferred restore above reverts the tentative unwrap;
		// the join stays on the hash path.
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

	// M0063-0001 / M0064: re-bind each outer-side Key's
	// ColumnRef.Index against the chosen `outerNode.Output()`
	// schema by Name — but only when `outerNode` is a
	// *MultiHashJoin. That's the shape where (a) the upstream
	// rewrite OID-sorted (or otherwise reordered) the joined
	// output schema, and (b) the j.Predicate's ColumnRef indices
	// were resolved at parse time against the FROM-cumulative
	// layout and so disagree with the runtime row layout the MHJ
	// actually produces (Q8's `l_partkey` rebind 40 → 42, where
	// 40 maps to `l_quantity` in the OID-sorted layout).
	//
	// For *NestedLoopIndexJoin outers (Q9's chained-NLI shape)
	// the downstream remap walkers (`remapExprRefsToMHJ`,
	// `remapWithBindings`) leave NLI keys in their pre-rewrite
	// indices on purpose; rebinding here would move the probe
	// onto a different table's column at runtime even though the
	// schema annotation looks OID-sorted (Q9's `l_suppkey` 15 → 24
	// regression). For other outer kinds (SeqScan, Filter, …) the
	// Index space matches the runtime layout directly and rebind
	// is unnecessary.
	_, outerIsMHJ := outerNode.(*MultiHashJoin)
	_, outerIsNLI := outerNode.(*NestedLoopIndexJoin)
	outerJoin, outerIsJoin := outerNode.(*Join)
	outerSchema := outerNode.Output()
	if outerIsJoin {
		// A binary Join outer may already have a rewritten child
		// (typically an MHJ) even though its cached schema still
		// reflects the pre-rewrite layout. Refresh it before we
		// bind NLI probe keys against the outer row coordinates.
		reresolveJoinByName(outerJoin)
		outerSchema = outerJoin.Output()
	}
	if outerIsMHJ || outerIsNLI || outerIsJoin {
		// M0075-0002: for *NestedLoopIndexJoin outers
		// (Q9's chained-NLI shape) the rebind needs the per-
		// outer selectivity guard. M0072-0002 attempted the
		// rebind without it and Q9 hung at 380-600 s with 0
		// rows because the resolved column was high-
		// cardinality at runtime. The guard rejects rebinds
		// where the new column's NDistinct produces a per-
		// outer match-set above nliRebindSelectivityThreshold.
		// MHJ outers (already-stable from M0064) retain the
		// original Name-only rebind path.
		for _, k := range keys {
			cr, ok := k.(*ColumnRef)
			if !ok || cr.Name == "" {
				continue
			}
			if cr.Index >= 0 && cr.Index < len(outerSchema) && outerSchema[cr.Index].Name == cr.Name {
				continue
			}
			// M0077-0001: type-mismatch override. When the
			// slot the original cr.Index points to carries a
			// type incompatible with cr.Type, the runtime
			// IndexScan key encoder will fail with 42804
			// (`not numeric at runtime`) regardless of the
			// M0075-0002 selectivity guard's verdict. Detect
			// that case here so the rebind path can bypass
			// the guard — Q9's "preserve stale index" mode
			// works only because Q9's stale slot happens to
			// have the right runtime type even when the
			// schema annotation diverges; Q8's stale slot
			// for `c_nationkey` is `n_name` (char) which the
			// numeric encoder rejects.
			origTypeMismatch := !origTypeMatches(cr, outerSchema)
			var newIdx int
			if outerIsNLI {
				// SourceTableIdx-aware lookup (M0071-0009).
				if cr.SourceTableIdx == 0 {
					// No source disambiguator → too risky to
					// guess; preserve original index.
					continue
				}
				newIdx = findColumnIndexByNameAndSource(outerSchema, cr.Name, cr.SourceTableIdx, 0)
				if newIdx < 0 {
					continue
				}
				// Per-outer selectivity guard: M0075-0002.
				// Bypass when origTypeMismatch — runtime
				// would fail without rebind (M0077-0001).
				if !origTypeMismatch && !rebindPassesSelectivityGuard(outerNode, outerSchema, newIdx) {
					continue
				}
			} else if outerIsJoin {
				if cr.SourceTableIdx != 0 {
					newIdx = findColumnIndexByNameAndSource(outerSchema, cr.Name, cr.SourceTableIdx, 0)
				}
				if newIdx < 0 {
					newIdx = findUniqueColumnIndex(outerSchema, cr.Name, 0)
				}
				if newIdx < 0 {
					continue
				}
			} else {
				newIdx = findUniqueColumnIndex(outerSchema, cr.Name, 0)
				if newIdx < 0 {
					continue
				}
			}
			cr.Index = newIdx
		}
	}

	// Fallback rebind: for outer types not covered above (e.g. Values,
	// SeqScan-of-right), the combined-schema index may exceed the outer
	// slot width (when inner=left and outer=right, outer cols start at
	// leftWidth in the combined schema but at 0 in the outer slot).
	// Rebind by name when out of range.
	for _, k := range keys {
		cr, ok := k.(*ColumnRef)
		if !ok || cr.Name == "" {
			continue
		}
		if cr.Index >= 0 && cr.Index < len(outerSchema) && outerSchema[cr.Index].Name == cr.Name {
			continue
		}
		if newIdx := findUniqueColumnIndex(outerSchema, cr.Name, 0); newIdx >= 0 {
			cr.Index = newIdx
		}
	}

	// Keep any conjunct the index probe does NOT enforce.
	//
	// The pre-existing code assumed `j.Predicate` held nothing but the
	// equi-conjunct that becomes the probe key, on the grounds that
	// pushdown separates non-key conjuncts upstream. That assumption
	// breaks for a predicate spanning two relations: pushdown cannot
	// place it on either scan, so it stays on the join — and NLI then
	// dropped it silently. TPC-H Q7's nation pair
	// `(n1.n_name='FRANCE' AND n2.n_name='GERMANY') OR (…GERMANY…FRANCE…)`
	// is exactly that shape, and losing it made Q7 return every
	// nation pair (486 K rows instead of 4 after grouping).
	//
	// Such conjuncts are kept as the NLI's residual Predicate, which the
	// executor evaluates against `outer ++ inner`. Their ColumnRef
	// indices cannot be reused as-is: they were resolved against this
	// join's layout at bind time, and an earlier NLI/MHJ rewrite below
	// us may since have reordered a child's output. They are therefore
	// re-resolved by name against the joined schema below, using the
	// same (Name, SourceTableIdx) rule `reresolveJoinByName`'s
	// predRebind uses so self-joined aliases (Q7's `nation n1` / `n2`,
	// Q21's three `lineitem` aliases) disambiguate correctly.
	var leftover []Expr
	if residualPred == nil && j.Predicate != nil {
		for _, c := range splitAnd(j.Predicate) {
			if nliConsumedByProbe(c, idx, keys) {
				continue
			}
			leftover = append(leftover, c)
		}
	}
	// S6: conjuncts hoisted out of a Filter{SeqScan} inner join the
	// residual set. Their clones already carry outer++inner-shaped
	// indices (see the unwrap block above); the rebind below re-resolves
	// them by name against the actual joined schema like every other
	// leftover conjunct.
	leftover = append(leftover, hoistedInner...)

	// Build the inner IndexScan. For single-column indexes we
	// keep using `Key` for backward compatibility with all
	// existing single-column callers / tests; for composite
	// indexes we use `Keys` so the executor encodes every
	// leading column in declared order with no suffix padding.
	inner := &IndexScan{
		pos:   innerScan.Pos(),
		Table: innerScan.Table,
		// The FROM-clause alias must survive the rewrite: later
		// passes disambiguate a self-joined relation by alias, so
		// dropping it makes `n1.col` in `FROM nation n1, nation n2`
		// bind to whichever relation happens to sit at the matching
		// slot base — silently returning a neighbouring table's
		// columns instead of failing.
		Alias:  innerScan.Alias,
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

	// Place the surviving conjuncts (see above) on the NLI, re-resolving
	// their ColumnRefs against `outer ++ inner`. This includes Semi /
	// Anti: although they EMIT the outer schema only, the executor
	// evaluates the residual through virtualOut, whose column mapping
	// always spans outer ++ inner regardless of the emit schema
	// (operators_nljoin.go builds cols over both source slots, and
	// evalExprSlot bounds-checks against Width(), not the schema) — so
	// an inner-referencing residual resolves correctly. S6 (D6.2)
	// replaced the earlier blanket decline, which forced every
	// residual-bearing semi/anti onto the hash path and cost Q4 a
	// 6 M-row build where an index probe sufficed. Resolution is
	// computed for every ref BEFORE any is written back, so a rewrite we
	// end up declining leaves the shared predicate untouched.
	if len(leftover) > 0 {
		outerSchema2 := outerNode.Output()
		innerSchema2 := inner.Output()
		resolveIn := func(schema Schema, cr *ColumnRef, offset int) int {
			if cr.SourceTableIdx != 0 {
				if i := findColumnIndexByNameAndSource(schema, cr.Name, cr.SourceTableIdx, offset); i >= 0 {
					return i
				}
			}
			return findUniqueColumnIndex(schema, cr.Name, offset)
		}
		type rebindTarget struct {
			ref    *ColumnRef
			newIdx int
		}
		var targets []rebindTarget
		resolvable := true
		for _, c := range leftover {
			visitColumnRefs(c, func(e Expr) {
				cr, isCol := e.(*ColumnRef)
				if !isCol || cr.Name == "" || !resolvable {
					return
				}
				// Prefer the side the current index points at, then
				// fall back to the other — the same hint predRebind
				// uses, which keeps `a.id = b.id` on its own sides
				// when names collide.
				first, firstOff := outerSchema2, 0
				second, secondOff := innerSchema2, len(outerSchema2)
				if cr.Index >= len(outerSchema2) {
					first, firstOff = innerSchema2, len(outerSchema2)
					second, secondOff = outerSchema2, 0
				}
				if i := resolveIn(first, cr, firstOff); i >= 0 {
					targets = append(targets, rebindTarget{cr, i})
					return
				}
				if i := resolveIn(second, cr, secondOff); i >= 0 {
					targets = append(targets, rebindTarget{cr, i})
					return
				}
				resolvable = false
			})
		}
		if !resolvable {
			return nil, false
		}
		for _, t := range targets {
			t.ref.Index = t.newIdx
		}
		if residualPred != nil {
			// OR-factoring path (Q19) plus hoisted inner conjuncts:
			// keep the OR predicate AND the hoisted set. (The OR
			// predicate itself is intentionally not re-resolved here —
			// pre-existing behaviour; see the Q7 write-up's follow-ups.)
			residualPred = combineAnd(append([]Expr{residualPred}, leftover...))
		} else {
			residualPred = combineAnd(leftover)
		}
	}

	committed = true
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

// cloneExprShiftIdx deep-clones a conjunct hoisted out of a
// Filter{SeqScan} inner side, shifting every ColumnRef.Index by `shift`
// so the clone addresses the NLI's outer++inner row instead of the inner
// scan's own row. Name/Type/SourceTableIdx are preserved — the leftover
// rebind re-resolves the final index by name, using the shifted value
// only as the which-side hint.
//
// Returns ok=false for any node kind it does not positively recognise —
// including OuterColumnRef, which inside an inner filter means a
// correlation into a scope above this join that a flat outer++inner row
// cannot supply. Declining the whole rewrite (the caller's response)
// keeps the hash path serving those shapes.
func cloneExprShiftIdx(e Expr, shift int) (Expr, bool) {
	switch x := e.(type) {
	case nil:
		return nil, true
	case *ColumnRef:
		cl := *x
		if cl.Index >= 0 {
			cl.Index += shift
		}
		return &cl, true
	case *IntegerConst, *NumericConst, *StringConst, *BooleanConst,
		*NullConst, *TypedStringLit, *IntervalLit:
		return e, true // literals are immutable — share
	case *BinaryOp:
		l, okL := cloneExprShiftIdx(x.Left, shift)
		r, okR := cloneExprShiftIdx(x.Right, shift)
		if !okL || !okR {
			return nil, false
		}
		return &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}, true
	case *UnaryOp:
		op, ok := cloneExprShiftIdx(x.Operand, shift)
		if !ok {
			return nil, false
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, true
	case *CastExpr:
		op, ok := cloneExprShiftIdx(x.Operand, shift)
		if !ok {
			return nil, false
		}
		cl := *x
		cl.Operand = op
		return &cl, true
	case *FuncCall:
		args := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			cl, ok := cloneExprShiftIdx(a, shift)
			if !ok {
				return nil, false
			}
			args[i] = cl
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name, Args: args, Star: x.Star}, true
	}
	return nil, false
}

// nliConsumedByProbe reports whether conjunct c is exactly one of the
// equi-conjuncts the inner IndexScan probe encodes — inner index column
// = outer expression — and is therefore already enforced by the probe
// itself. Matching requires both the index column name AND pointer
// identity of the outer side against the chosen probe key, so a second
// equality binding the same inner column to a different outer
// expression is correctly reported as NOT consumed and survives as a
// residual (the shape bushy DP's edge selection can leave behind).
func nliConsumedByProbe(c Expr, idx *catalog.Index, keys []Expr) bool {
	bin, ok := c.(*BinaryOp)
	if !ok || bin.Op != parser.OpEq || idx == nil {
		return false
	}
	for i, k := range keys {
		if i >= len(idx.Columns) {
			break
		}
		col := idx.Columns[i]
		if lc, isCol := bin.Left.(*ColumnRef); isCol && lc.Name == col && bin.Right == k {
			return true
		}
		if rc, isCol := bin.Right.(*ColumnRef); isCol && rc.Name == col && bin.Left == k {
			return true
		}
	}
	return false
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
	// M0122 TPC-H Q9-timeout: an extra conjunct AND'd onto j.Predicate
	// by bushy.go's attachUnusedCrossEdges carries RAW (global
	// FROM-order, unremapped) ColumnRef indices rather than this
	// Join's own local Left/Right subset indices — the index-based
	// classification below can't place it. Fall back to classifying
	// by column NAME (unique per this project's convention) against
	// the two sides' actual output columns whenever the index-based
	// verdict is inconclusive (both/neither side).
	var leftNames, rightNames map[string]bool
	nameSets := func() (map[string]bool, map[string]bool) {
		if leftNames == nil {
			leftNames = map[string]bool{}
			rightNames = map[string]bool{}
			collectScanOutputNames(j.Left, leftNames)
			collectScanOutputNames(j.Right, rightNames)
		}
		return leftNames, rightNames
	}
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
			ln, rn := nameSets()
			aInLeft, aInRight := ln[ac.Name], rn[ac.Name]
			bInLeft, bInRight := ln[bc.Name], rn[bc.Name]
			switch {
			case aInLeft && !aInRight && bInRight && !bInLeft:
				aLeft, bLeft = true, false
			case aInRight && !aInLeft && bInLeft && !bInRight:
				aLeft, bLeft = false, true
			default:
				return
			}
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
		if !ok || bop.Op != parser.OpEq {
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
	if !ok || bop.Op != parser.OpAnd {
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
		if !ok || bop.Op != parser.OpEq {
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
	if !ok || bop.Op != parser.OpEq {
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
		out = &BinaryOp{Op: parser.OpAnd, Left: out, Right: conjuncts[i]}
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
	if bop, ok := j.Predicate.(*BinaryOp); ok && bop.Op == parser.OpEq {
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
		// Making Left the inner (probed/index-scanned) side means
		// Right becomes the outer (loop-driving) side. That's a
		// semantics-changing swap for any join type where one side
		// must stay preserved/outer-only: LEFT JOIN must keep Left
		// as the driving side (every Left row must appear at least
		// once, NULL-extended when unmatched — impossible if Right
		// drives the loop instead, since an unmatched Left row then
		// has no iteration that ever visits it); Semi/Anti must keep
		// Left as the sole emitted side. Only INNER permits the
		// free swap. tpch/Q13-regression: `customer LEFT JOIN orders
		// ON c_custkey = o_custkey AND o_comment NOT LIKE '...'`
		// took this branch once the NOT-LIKE conjunct pushed
		// `orders` behind a Filter (no longer a bare *SeqScan, so
		// the `rss` branch above declined) — Right(orders) became
		// the outer driver, silently dropping every customer with
		// zero matching orders (~50k of 150k rows in the TPC-H
		// SF=1 data) from the LEFT JOIN's output.
		if j.Type != JoinTypeInner {
			return nil, nil, nil
		}
		// Inner is left; outer is right. M0071-0002-followup:
		// when this branch fires, the NLI's emitted schema is
		// `outer ++ inner` = `Right ++ Left`, which is the FLIP
		// of the original `Left ++ Right`. Downstream operators
		// whose column refs were bound to the original layout
		// land on wrong slots after the flip. Decline NLI when
		// the would-be outer (j.Right) is a scalar-decorrelation
		// Aggregate — that shape's parent Filter (e.g. Q20's
		// `ps_availqty > 0.5 * sum`) breaks under the flip
		// because the chained-NLI defensive gates at line 399
		// and bushy.go:1548 leave the parent Filter's
		// ColumnRefs un-rebind'd. The HashJoin path keeps the
		// canonical Left ⋈ Right schema and works correctly.
		if _, rightIsAgg := j.Right.(*Aggregate); rightIsAgg {
			return nil, nil, nil
		}
		// Also decline when outer=right is a *Values node: the schema
		// flip (outer ++ inner vs original Left ++ Right) can't be
		// corrected by remapWithBindings because Values columns are not
		// tracked in the scanMap. Fall through to the hash-join path.
		if _, rightIsValues := j.Right.(*Values); rightIsValues {
			return nil, nil, nil
		}
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

// nliCostGateLegacy restores the pre-D6.3a bare heuristic for
// semi/anti joins (env GOOPG_NLI_COSTGATE=legacy at process start, or
// SetNLICostGateLegacy from tests). Escape hatch for one stage; slated
// for deletion in R2-8 if unused. INNER behavior is identical either
// way.
var nliCostGateLegacy atomic.Bool

// SetNLICostGateLegacy toggles the legacy (stats-blind) cost gate for
// semi/anti NLI decisions. Test hook, mirroring SetNLIEnabled.
func SetNLICostGateLegacy(on bool) { nliCostGateLegacy.Store(on) }

// nliCostGateAccepts returns true when the cost model finds NLI
// preferable to (or at least competitive with) Hash for the given
// shape.
//
// INNER (and LEFT) joins keep the historical heuristic unchanged:
//
//   - outer estimate ≤ nliMaxOuterRowsHeuristic → accept;
//     unknown estimate → optimistic accept. INNER regressions are out
//     of D6.3a's scope.
//
// SEMI/ANTI joins (D6.3a, design bundle ch.06 §3.2; oracle:
// postgres/src/backend/optimizer/path/costsize.c
// compute_semi_anti_join_factors / final_cost_nestloop) use ANALYZE
// statistics:
//
//	matchSet  = max(1, innerRows / NDistinct(inner probe column))
//	probeCost = matchSet
//	accept  ⇔ outerRows × probeCost < innerRows + outerRows
//
// The right-hand side is the hash alternative: one build pass over the
// inner (≈ innerRows) plus one O(1) probe per outer row. probeCost is
// deliberately the FULL match set, not an early-out blend: both semi
// and anti probes can stop at the first matching row (semi emits, anti
// suppresses), but the expensive case — no match — must exhaust the
// match set, and estimating the match probability from column overlap
// is more model than the decision needs. Using the pessimistic bound
// biases toward hash, the safe direction (a wrong "hash" costs a build
// pass; a wrong "NLI" on a big outer costs outerRows × matchSet, the
// Q4-regression class).
//
// No usable stats (RowCount or probe-column NDistinct missing) →
// REJECT for semi/anti — keep the hash join. This is the documented
// conservative rule: a stats-blind index-probe loop over an unknown
// inner is exactly how the 71× Q4-class regressions happen.
func nliCostGateAccepts(joinType JoinType, outer Node, innerScan *SeqScan, idx *catalog.Index) bool {
	outerRows := EstimateRows(outer)
	semiAnti := joinType == JoinTypeSemi || joinType == JoinTypeAnti
	if !semiAnti || nliCostGateLegacy.Load() {
		// Historical heuristic (also the legacy escape hatch for
		// semi/anti): small-or-unknown outer → NLI.
		if outerRows <= 0 {
			return true
		}
		return outerRows <= nliMaxOuterRowsHeuristic
	}
	var innerRows, matchSet int64
	if innerScan != nil && innerScan.Table != nil {
		innerRows = tableRows(innerScan.Table)
		matchSet = matchSetByColumnName(innerScan.Table, firstIndexColumn(idx))
	}
	statsKnown := outerRows > 0 && innerRows > 0 && matchSet > 0
	accept := true
	if statsKnown {
		accept = outerRows*matchSet < innerRows+outerRows
	}
	// No usable stats → OPTIMISTIC accept (the pre-D6.3a behavior).
	//
	// The first cut of this gate rejected semi/anti NLI without stats
	// ("a stats-blind index-probe loop is how Q4-class regressions
	// happen"). Measured reality inverted that: goopg's ANALYZE
	// statistics live in memory only and are lost on every server
	// restart, so the no-stats case is the COMMON case — and rejecting
	// it permanently disabled semi/anti NLI in practice, reproducing
	// the exact regression the gate exists to prevent (Q4 as a hash
	// semi: 276 s; as NLI-semi: ~3.5 s — the 71× class, measured
	// 2026-07-21 on the fresh bench server). The optimistic default is
	// the behavior every green sweep to date actually ran with; the
	// stats-aware formula refines the decision only where ANALYZE data
	// exists (unit-test fixtures, long-lived sessions).
	if os.Getenv("GOOPG_NLI_COSTGATE_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "nli-costgate: type=%v outer=%T outerRows=%d innerRows=%d matchSet=%d statsKnown=%v accept=%v\n",
			joinType, outer, outerRows, innerRows, matchSet, statsKnown, accept)
	}
	return accept
}

// residualCostMultiplier classifies the per-probe evaluation cost of the
// Filter conjuncts an INNER unwrap would hoist onto the NLI. Pattern-match
// predicates (LIKE family, regex family) and function calls are an order
// of magnitude more expensive per evaluation than plain comparisons — the
// Q9 killer was exactly a `%green%` LIKE evaluated ~6 M times — so their
// presence surcharges the probe cost in innerUnwrapCostAccepts.
func residualCostMultiplier(conjuncts []Expr) int64 {
	expensive := false
	for _, c := range conjuncts {
		WalkExprTree(c, func(e Expr) {
			switch x := e.(type) {
			case *BinaryOp:
				switch x.Op {
				case parser.OpLike, parser.OpNotLike, parser.OpILike, parser.OpNotILike,
					parser.OpRegexMatch, parser.OpRegexIMatch, parser.OpRegexNoMatch, parser.OpRegexINoMatch:
					expensive = true
				}
			case *FuncCall:
				expensive = true
			}
		})
	}
	if expensive {
		return 8
	}
	return 1
}

// innerUnwrapCostAccepts confirms a tentative INNER Filter{SeqScan}-inner
// unwrap once the probe index is known (D6.3b, resolving the csq-S2/S3
// ledger row about the Q9 71× regression).
//
//	matchSet  = max(1, innerRows / NDistinct(probe column))
//	accept  ⇔ outerRows × (matchSet + residualMult) < innerRows + outerRows
//
// The right-hand side is the hash alternative (one build pass over the
// inner plus O(1) probes); residualMult surcharges pattern-match/function
// residuals per residualCostMultiplier.
//
// No usable stats → DECLINE — deliberately the OPPOSITE default from
// nliCostGateAccepts' semi/anti rule (optimistic accept), and both are
// argued from the same fact: goopg's ANALYZE statistics are in-memory and
// restart-lost, so no-stats is the COMMON case. For semi/anti, rejecting
// without stats would permanently disable a shape whose measured upside is
// large (Q4: NLI 3.5 s vs hash 276 s) — so unknown accepts. For the INNER
// unwrap the asymmetry runs the other way: declining just keeps today's
// hash behavior (Q9 runs fine at ~104–118 s), while a wrong accept is the
// catastrophic direction (Q9 → DNF). Status quo is the safe default on
// unknown stats, so unknown declines.
func innerUnwrapCostAccepts(outer Node, innerScan *SeqScan, idx *catalog.Index, residualMult int64) bool {
	outerRows := EstimateRows(outer)
	var innerRows, matchSet int64
	if innerScan != nil && innerScan.Table != nil {
		innerRows = tableRows(innerScan.Table)
		matchSet = matchSetByColumnName(innerScan.Table, firstIndexColumn(idx))
	}
	statsKnown := outerRows > 0 && innerRows > 0 && matchSet > 0
	accept := statsKnown && outerRows*(matchSet+residualMult) < innerRows+outerRows
	if os.Getenv("GOOPG_NLI_COSTGATE_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "nli-costgate: inner-unwrap outer=%T outerRows=%d innerRows=%d matchSet=%d residualMult=%d statsKnown=%v accept=%v\n",
			outer, outerRows, innerRows, matchSet, residualMult, statsKnown, accept)
	}
	return accept
}
