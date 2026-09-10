package optimizer

// M0127-P5.5-e-ii-b — `create_nestloop_plan` (createplan.c:4322): the third and
// last join arm, and the only one that BINDS a parameter instead of merging two
// finished streams.
//
// PG oracle: `create_nestloop_plan` (createplan.c:4322), the
// `NestLoopParam`/`replace_nestloop_params` machinery it drives
// (createplan.c:4364-4392, setrefs.c:3050), and
// `is_redundant_with_indexclauses` (createplan.c:3075). Design: leftdeep-joins
// 03 §5.2, §5.4.
//
// # Two shapes, one path kind
//
// `PathNestLoop` is produced by two different arms and they emit two different
// executor nodes:
//
//   - `addNestLoopPath` (pathgen.go:97) — the PLAIN nested loop. It keys on
//     nothing, rescans the whole inner per outer row, and every clause is
//     residual. goopg's node is `*Join` with `JoinAlgoNestedLoop`, whose
//     predicate is evaluated against the merged `outer ++ inner` row like every
//     other `*Join`.
//   - `addNLIPaths` (joinpathsnli.go:156) — the NLI, where the inner is a
//     PARAMETERISED index path and the loop's job is to bind that parameter per
//     outer row. goopg's node is `*NestedLoopIndexJoin`, which is a different
//     type, not a flag: its `Inner` is typed `*IndexScan` precisely because the
//     driver calls `Rescan` on it with the outer slot bound.
//
// The arm therefore dispatches on the INNER PATH's parameterisation, not on
// anything recorded on the join path. That is the same fact PG dispatches on —
// PG emits `NestLoopParam` entries exactly when the inner's `param_info`
// references the outer — and it is checkable here, which is why the shape is
// read off the child rather than carried as a second field that could disagree
// with it (rule #2).
//
// # The parameter-binding contract: two coordinate spaces, not one
//
// This is the arm's whole difficulty, and it is a coordinate problem that the
// hash and merge arms do not have.
//
// A `*Join` predicate — hash, merge, or plain nested loop — is evaluated once
// per candidate PAIR, against the merged row. So every expression on those
// nodes lives in ONE space and `joinInputs.index` re-bases all of it.
//
// An NLI's inner probe key does not. `indexScanOp.Rescan` (operators_index.go:345)
// evaluates `IndexScan.Key`/`Keys` against the slot the parent bound
// (`nestedLoopIndexJoinOp.outerMS`, operators_nljoin.go:136), which holds the
// OUTER row alone — the inner row does not exist yet; producing it is what the
// probe is for. So the probe keys are in OUTER-node coordinates while the
// residual `Predicate` is in merged `outer ++ inner` coordinates, and the arm
// must translate the two onto DIFFERENT layouts.
//
// Getting this wrong is not a build failure. `p.IndexClauses[i].key` arrives in
// pre-search binding coordinates; on a two-relation query where the outer
// happens to be first in binding order the two spaces coincide, so a single
// merged translation passes every small test and reads the wrong column the
// moment the search reorders the join. Both translations are therefore written
// explicitly, against layouts named for which space they are.
//
// # The residual DROP, and why goopg's is narrower than PG's
//
// `create_nestloop_path` (pathnode.c:2478-2500) removes from the join's
// restrict clauses every clause that is "movable into" the parameterised inner,
// because such a clause is ALREADY being applied down there and charging for it
// again on the full cross product is exactly the mis-costing that would hide a
// good NLI. `nestloopResidualClauses` (joinpathsnli.go) implements that test.
//
// PG may drop on movability alone because a PG parameterised path really does
// carry every movable clause: `get_baserel_parampathinfo` puts them in
// `ppi_clauses`, and `create_indexscan_plan` places whatever the index did not
// consume into the scan's `qpqual`. goopg's parameterised index path carries
// only the equalities `pickIndexCoveringLeadingPrefix` accepted
// (`Path.IndexClauses`), and goopg's `*IndexScan` has no qual field at all — so
// a movable NON-index clause such as `b.y > a.x` would be dropped from the join
// residual and enforced by nothing. That is a wrong answer, and it is this arm
// that would have materialised it. The producer-side narrowing is in
// `nestloopResidualClauses`; this comment is the consumer's half of the same
// statement.
//
// # What the arm refuses
//
// A parameterised index path whose leaf carries local quals (`Filter{*IndexScan}`,
// `attachRelationLocalFilters`) has nowhere to put them: `NestedLoopIndexJoin.Inner`
// is typed `*IndexScan`, so the `*Filter` cannot ride along, and hoisting its
// predicate onto the join residual is the D6.3b blowup (`innerUnwrapCostAccepts`,
// nl_index_join.go:345-380) — a per-probe re-evaluation of a qual the path was
// costed as applying once per inner row. The refusal is therefore made
// UNREACHABLE at the producer instead: `addParameterizedIndexPaths` now declines
// such a leaf, the same way P5.5-c made it decline a leaf that is not a scan at
// all. Ledgered, with the capability loss stated.
//
// Live since M0127-P5.9 (2026-08-06): `GOOPG_PGSHAPED_DP` defaults ON and
// `planSelect` calls the search, so plans and rows DO move here. Falsifiable
// in `createplannl_test.go`, but no longer only there.

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
)

// createNestLoopPlan is `create_nestloop_plan` (createplan.c:4322). See the file
// header for the two shapes it emits and why the shape is read off the inner
// child.
//
// Preconditions, each naming the wrong answer it prevents:
//
//   - a `PathNestLoop` carrying hash keys is a path whose producer thought it
//     was building something else; a nested loop keys on nothing (its
//     equalities are residual clauses, or — for the NLI — index clauses on the
//     inner), so a key list here would be silently ignored and the join would
//     run without the restriction the producer believed it had recorded;
//   - a parameterised RESULT is undischargeable: `addNLIPaths` refuses one
//     (`req != 0` → skip, pending P5.6's `ppi_rows`) and the search publishes an
//     unparameterised root, so nothing above this node would bind it and the
//     probe keys would be evaluated against a slot that never receives the
//     missing relation's row.
func createNestLoopPlan(p *Path) (Node, outputLayout) {
	if len(p.Children) != 2 {
		panic(fmt.Sprintf("createPlan: PathNestLoop with %d children, want exactly 2", len(p.Children)))
	}
	if len(p.HashKeys) != 0 {
		panic(fmt.Sprintf("createPlan: PathNestLoop carries %d hash keys; a nested loop keys on nothing and would ignore them", len(p.HashKeys)))
	}
	if p.RequiredOuter != 0 {
		panic(fmt.Sprintf("createPlan: parameterised PathNestLoop over relset %#08x; nothing above the search root binds a parameter",
			uint32(relsOf(p))))
	}

	innerPath := p.Children[1]
	if innerPath != nil && innerPath.RequiredOuter != 0 {
		return createNestLoopIndexJoinPlan(p, innerPath)
	}

	// The plain nested loop. Every clause is residual and is evaluated against
	// the merged row, exactly like the hash arm's — which is why the prologue
	// and the predicate builder are shared verbatim rather than restated.
	in := joinInputsFor(p, "PathNestLoop", p.Children[0], innerPath)
	// C-03c: the join the PATH says it performs. The plain nested loop is the
	// one arm a searched SEMI or ANTI join can reach — `jointypeForDirection`
	// gives them the nestloop arms and nothing else.
	jt := planJoinTypeFor(p, "PathNestLoop")
	j := &Join{
		pos:  in.outer.Pos(),
		Type: jt,
		Algo: JoinAlgoNestedLoop,
		// Children[0] is the OUTER (driving) side, the same convention the hash
		// and merge arms use. For a nested loop it is also the only one that is
		// meaningful: the outer is rescanned once, the inner once per outer row,
		// and `addNestLoopPath` costed exactly that assignment.
		Left:  in.outer,
		Right: in.inner,
		// nil when there is no clause at all — the cartesian pair, which is the
		// one join a plain nested loop is the ONLY available path for
		// (`Join.Predicate` is documented nil for CROSS JOIN, plan.go:812).
		Predicate: in.joinPredicate("PathNestLoop", nil, p.Residual),
		schema:    in.publishedSchema(jt),
	}
	return j, in.publishedLayout(jt)
}

// outerParamKey translates a probe-key expression onto the outer layout
// (validating coordinates exactly as translateToLayout does — same
// panics, same order) and then re-roots every same-scope ColumnRef as an
// OuterColumnRef at level 1: PG's nestloop param. R25
// (plan-parity-fix-take2) decomposes `NestedLoopIndexJoin` into a
// `Join{Lateral}` over a parameterized `IndexScan`, and the lateral
// driver resolves outer references from its per-tuple pushed row
// (`ctx.OuterRows`), not from a bound slot — so positional outer keys
// must become levelled outer refs at plan time.
//
// The conversion runs under the same scopeIgnore policy as the
// translation: refs inside inner plans belong to that scope and are
// stepped over, never re-rooted. cloneExprRefs is fail-closed (an
// unenumerated node aborts rather than passing a bare ColumnRef
// through): a surviving bare ColumnRef would read the probe's own
// (nil) bound slot at runtime instead of failing loudly here.
func outerParamKey(what string, e Expr, lay outputLayout, index map[int]int) Expr {
	t := translateToLayout(what, e, lay, index)
	out, ok := cloneExprRefs(t, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if cr, isCol := n.(*ColumnRef); isCol {
				return &OuterColumnRef{pos: cr.Pos(), Level: 1, Index: cr.Index,
					Name: cr.Name, Type: cr.Type, SourceTableIdx: cr.SourceTableIdx}
			}
			return n
		},
	})
	if !ok {
		panic(fmt.Sprintf("createPlan: %s contains an expression the walker does not enumerate; teach exprChildSlots about it", what))
	}
	return out
}

// createNestLoopIndexJoinPlan is the NLI shape: the inner is a parameterised
// index path and this loop is what binds its parameter.
//
// `innerPath` is passed rather than re-read from `p.Children[1]` so the caller's
// dispatch decision and this function's assumption are the same value.
//
// R25 (plan-parity-fix-take2): the search-arm IndexScan site is DECOMPOSED —
// it emits `Join{Algo: NestedLoop, Lateral}` over the probe with
// OuterColumnRef keys (PG's nestloop params) instead of the fused
// `NestedLoopIndexJoin`. The Memoize-wrapped shape stays fused for now
// (slice 1b): a Memoize node as a generic right child needs its own
// buildNode arm plus lazy-fill Open semantics, and per-row re-Open would
// rebuild the cache per outer row (correct, pointless) — so the cache
// shape keeps the driver that preserves it, with zero behavior change.
func createNestLoopIndexJoinPlan(p *Path, innerPath *Path) (Node, outputLayout) {
	// M0127-P5.4b-ii-b-2: the inner may be a `PathMemoize` wrapping the probe.
	// It is unwrapped HERE rather than given its own `createPlanNode` arm
	// because goopg's cache is a field on the join (`InnerMemo`) and not a node
	// between the join and its inner — so the wrapper's translation is part of
	// building the join, and the probe below it is built exactly as an unwrapped
	// one is. Everything from here down therefore reads the INDEX path, and the
	// wrapper is consulted again only at the very end.
	memoPath := (*Path)(nil)
	if innerPath.Kind == PathMemoize {
		if len(innerPath.Children) != 1 || innerPath.Children[0] == nil {
			panic(fmt.Sprintf("createPlan: PathMemoize with %d children, want exactly 1", len(innerPath.Children)))
		}
		if innerPath.MemoizeInfo == nil {
			// `getMemoizePath` is the only producer and always sets it; a nil
			// here would mean the cache was sized by nothing, and the executor
			// would build a hash table the search never priced.
			panic("createPlan: PathMemoize with no MemoizeInfo; the cache was never costed")
		}
		memoPath, innerPath = innerPath, innerPath.Children[0]
	}
	if innerPath.Kind == PathBitmapHeapScan {
		return createNestLoopBitmapJoinPlan(p, innerPath)
	}
	if innerPath.Kind != PathIndexScan {
		// The only parameterised path kind goopg builds is the base index scan
		// (`addParameterizedIndexPaths`). Any other kind reaching here is a
		// producer that learned to parameterise something without teaching this
		// arm how the parameter is delivered to it.
		panic(fmt.Sprintf("createPlan: PathNestLoop over a parameterised child of kind %d; only a parameterised index scan can have its parameter bound here",
			innerPath.Kind))
	}
	outerPath := p.Children[0]
	if outerPath == nil || outerPath.Rel == nil {
		panic("createPlan: NLI over an outer child with no RelOptInfo")
	}
	if unsupplied := innerPath.RequiredOuter &^ outerPath.Rel.Relids; unsupplied != 0 {
		// `addNLIPaths` admits the pair only when the join's own RequiredOuter
		// comes out empty, which is exactly this containment. A leftover bit
		// means the probe key references a relation the bound slot does not
		// contain, and `translateToLayout` below would refuse it — but it is
		// checked here because THIS is the fact that was violated, and the
		// message that names it is the one that finds the producer.
		panic(fmt.Sprintf("createPlan: NLI inner is parameterised by %#08x, which the outer relset %#08x does not supply",
			uint32(unsupplied), uint32(outerPath.Rel.Relids)))
	}

	in := joinInputsFor(p, "PathNestLoop(NLI)", outerPath, innerPath)
	jtNLI := planJoinTypeFor(p, "PathNestLoop(NLI)")
	if memoPath != nil {
		// Slice 1b: the memoized shape keeps the fused node and driver
		// (function header). The decomposed Join below cannot carry a
		// Memoize child until it gains a buildNode arm plus lazy-fill
		// Open semantics — and per-row re-Open would rebuild the cache
		// per outer row anyway — so the cache shape keeps the driver
		// that preserves it, with zero behavior change.
		return createNestLoopIndexJoinPlanFused(p, innerPath, memoPath, in, jtNLI)
	}
	// The leaf's local quals arrive as `*Filter` wrappers that `scanLeafFor`'s
	// rewrapper rebuilt over the probe. `NestedLoopIndexJoin.Inner` is typed
	// `*IndexScan` and cannot hold them, so they are absorbed into the scan's
	// own `Cond` — PG's `Filter:` sitting beside `Index Cond:` on one Index Scan
	// node, which is the shape being reproduced here.
	//
	// Absorbing is not hoisting: `Cond` is evaluated once per row the probe
	// returns, which is what the path was costed for. Moving the same quals to
	// the join residual instead would evaluate them once per probed PAIR — the
	// D6.3b Q9 blowup (`innerUnwrapCostAccepts`, nl_index_join.go).
	innerBase, leafCond, absorbable := absorbableLeafCond(in.inner)
	if !absorbable {
		// Made unreachable at the producer, which applies the same predicate
		// (`addParameterizedIndexPaths`). Reaching it means a path was costed
		// over a leaf whose wrappers are not leaf-local, and evaluating those
		// against the scan's own row would read the wrong columns.
		panic(fmt.Sprintf("createPlan: NLI inner %T carries wrappers that are not leaf-local; IndexScan.Cond cannot evaluate them in the scan's coordinates", in.inner))
	}
	is, bare := innerBase.(*IndexScan)
	if !bare {
		panic(fmt.Sprintf("createPlan: NLI inner emitted a %T, but NestedLoopIndexJoin.Inner is an *IndexScan", innerBase))
	}
	if leafCond != nil {
		// The probe rebuilt by `createIndexScanPlan` is a fresh node this arm
		// owns (`scanLeafFor` never mutates the leaf), so setting Cond here
		// cannot disturb the leaf the search still references by pointer.
		is.Cond = leafCond
	}

	// The probe keys are re-based onto the OUTER alone — see the file header.
	// The outer occupies merged positions [0, outerWidth), so its layout is the
	// prefix of the merged one; taking the prefix rather than re-deriving it
	// keeps the two spaces provably the same map (a re-derivation could disagree
	// with the schema that was actually concatenated).
	outerLay := in.lay[:len(in.outer.Output())]
	outerIndex := outerLay.bindingIndex()
	keys := make([]Expr, 0, len(innerPath.IndexClauses))
	for i, c := range innerPath.IndexClauses {
		// The same order assertion `createIndexScanPlan` makes, restated because
		// this arm REPLACES the key list that function built: a silently
		// reordered list binds the right values to the wrong index columns and
		// returns wrong rows rather than failing.
		if c.indexCol != i {
			panic(fmt.Sprintf("createPlan: NLI index clause %d of %s claims index column %d; the index-column order was lost",
				i, innerPath.IndexInfo.Name, c.indexCol))
		}
		if c.key == nil {
			panic(fmt.Sprintf("createPlan: NLI index clause %d of %s has no probe expression", i, innerPath.IndexInfo.Name))
		}
	// R25 (plan-parity-fix-take2): the probe keys become PG nestloop
	// params — OuterColumnRef at level 1 over the outer row — instead of
	// positional outer-layout refs. The lateral driver resolves them from
	// its per-tuple pushed row (`ctx.OuterRows`); no slot is bound, so the
	// whole `translateToLayout` coordinate space (and the outerLay map
	// above) is deleted with the fused node rather than moved.
	keys = append(keys, outerParamKey("index probe key", c.key, outerLay, outerIndex))
	}
	if len(keys) == 0 {
		// `createIndexScanPlan` already refuses a parameterised path with no
		// index clauses (it would mean a full index scan). Restated because the
		// consequence HERE is different and worse: a probe with no key is
		// rescanned in full per outer row.
		panic(fmt.Sprintf("createPlan: NLI inner %s binds no probe key; the parameter would never be applied", innerPath.IndexInfo.Name))
	}
	// Overwrite the key list `createIndexScanPlan` built. That function emits the
	// clauses' expressions in BINDING coordinates, because a base index scan has
	// no outer to re-base onto and only its consumer knows what the bound slot
	// will hold. `translateToLayout` clones, so the path's own clause
	// expressions — which the search still owns — are untouched.
	is.Key, is.Keys = nil, nil
	if len(keys) == 1 {
		is.Key = keys[0]
	} else {
		is.Keys = keys
	}

	// R25 (plan-parity-fix-take2): the fused `NestedLoopIndexJoin` is
	// gone — PG has no such node. The decomposed shape is a lateral
	// `Join` over the parameterized probe: `NestLoop` with an inner
	// `IndexScan` whose keys are PG nestloop params (OuterColumnRef,
	// level 1), driven per outer row by the lateral stream, which
	// pushes the outer tuple before re-opening the probe. Residual,
	// schema and layout are unchanged (same jt, same builders).
	j := &Join{
		pos:  in.outer.Pos(),
		Type: jtNLI,
		Algo: JoinAlgoNestedLoop,
		// Lateral marks the right child as referencing the left row:
		// the driver re-opens it per outer tuple with that tuple in
		// scope (BindLateralOuter contract, plan.go). Without it the
		// generic nestloop would Materialize the probe once and replay
		// it — returning every row for the first outer tuple's key.
		Lateral:   true,
		Left:      in.outer,
		Right:     is,
		Predicate: in.joinPredicate("PathNestLoop(NLI)", nil, p.Residual),
		schema:    in.publishedSchema(jtNLI),
	}
	return j, in.publishedLayout(jtNLI)
}

// createNestLoopIndexJoinPlanFused builds the pre-R25 fused node for the
// memoized shape only (slice 1b keeps it: no Memoize-as-child support
// yet). It is the pre-decomposition body verbatim — positional outer keys
// via translateToLayout, `NestedLoopIndexJoin` node, `InnerMemo` field —
// and dies in slice 4 with the type. Callers must not extend it: every new
// shape goes through the decomposed arm above.
func createNestLoopIndexJoinPlanFused(p *Path, innerPath *Path, memoPath *Path, in joinInputs, jtNLI JoinType) (Node, outputLayout) {
	innerBase, leafCond, absorbable := absorbableLeafCond(in.inner)
	if !absorbable {
		panic(fmt.Sprintf("createPlan: NLI inner %T carries wrappers that are not leaf-local; IndexScan.Cond cannot evaluate them in the scan's coordinates", in.inner))
	}
	is, bare := innerBase.(*IndexScan)
	if !bare {
		panic(fmt.Sprintf("createPlan: NLI inner emitted a %T, but NestedLoopIndexJoin.Inner is an *IndexScan", innerBase))
	}
	if leafCond != nil {
		is.Cond = leafCond
	}
	outerLay := in.lay[:len(in.outer.Output())]
	outerIndex := outerLay.bindingIndex()
	keys := make([]Expr, 0, len(innerPath.IndexClauses))
	for i, c := range innerPath.IndexClauses {
		if c.indexCol != i {
			panic(fmt.Sprintf("createPlan: NLI index clause %d of %s claims index column %d; the index-column order was lost",
				i, innerPath.IndexInfo.Name, c.indexCol))
		}
		if c.key == nil {
			panic(fmt.Sprintf("createPlan: NLI index clause %d of %s has no probe expression", i, innerPath.IndexInfo.Name))
		}
		keys = append(keys, translateToLayout("index probe key", c.key, outerLay, outerIndex))
	}
	if len(keys) == 0 {
		panic(fmt.Sprintf("createPlan: NLI inner %s binds no probe key; the parameter would never be applied", innerPath.IndexInfo.Name))
	}
	is.Key, is.Keys = nil, nil
	if len(keys) == 1 {
		is.Key = keys[0]
	} else {
		is.Keys = keys
	}
	nli := &NestedLoopIndexJoin{
		pos:       in.outer.Pos(),
		Type:      jtNLI,
		Outer:     in.outer,
		Inner:     is,
		Predicate: in.joinPredicate("PathNestLoop(NLI)", nil, p.Residual),
		schema:    in.publishedSchema(jtNLI),
	}
	nli.InnerMemo = memoizeNodeFor(memoPath, is, keys)
	return nli, in.publishedLayout(jtNLI)
}

// memoizeNodeFor builds the `Memoize` the search's `PathMemoize` stands for.
//
// It takes the ALREADY-TRANSLATED probe keys rather than re-deriving them from
// the path, and that is the point of the function existing at all: the cache
// key and the probe key must be the same expressions in the same coordinate
// space, because `memoizeOp` evaluates `KeyExprs` against the very slot
// `indexScanOp.Rescan` evaluates `IndexScan.Keys` against (the OUTER row alone,
// see the file header). Two derivations would be two chances to key the cache on
// one column and probe on another — a cache that returns the wrong rows, not a
// slow one.
//
// `SingleRow` is decided here and not on the path because it is a property of
// the INDEX the probe ended up using (`Index.Unique` with every index column
// bound), which is a fact about the built node; the path-level decision was only
// ever whether a cache pays. It is the same test `maybeAttachMemoize` applies
// (memoize.go:134-136), shared in intent so the legacy and searched arms cannot
// mark the same probe differently.
func memoizeNodeFor(memoPath *Path, is *IndexScan, keys []Expr) *Memoize {
	singleRow := is.Index != nil && is.Index.Unique && len(keys) == len(is.Index.Columns)
	return &Memoize{
		pos:        is.Pos(),
		Child:      is,
		KeyExprs:   keys,
		SingleRow:  singleRow,
		EstEntries: memoPath.MemoizeInfo.estEntries,
	}
}

// relsOf is the relset a path stands for, or 0 when it has no RelOptInfo. It
// exists only so a panic message can name the relset without a nil check at
// every call site.
func relsOf(p *Path) RelSet {
	if p == nil || p.Rel == nil {
		return 0
	}
	return p.Rel.Relids
}

func createNestLoopBitmapJoinPlan(p *Path, innerPath *Path) (Node, outputLayout) {
	idxPath := innerPath.Children[0]
	outerPath := p.Children[0]
	in := joinInputsFor(p, "PathNestLoop(NLI-bitmap)", outerPath, innerPath)
	// The leaf's local quals arrive as *Filter wrappers the rewrapper rebuilt
	// over the scan; absorb them into the node's own Cond, since
	// NestedLoopIndexJoin.Inner re-probes per outer row and cannot carry
	// wrappers. Same move, same reason, as the index arm above.
	innerBase, leafCond, absorbable := absorbableLeafCond(in.inner)
	if !absorbable {
		panic(fmt.Sprintf("createPlan: NLI bitmap inner %T carries wrappers that are not leaf-local", in.inner))
	}
	bhs := innerBase.(*BitmapHeapScan)
	if leafCond != nil {
		bhs.Cond = leafCond
	}
	bis := bhs.Outer.(*BitmapIndexScan)
	outerLay := in.lay[:len(in.outer.Output())]
	outerIndex := outerLay.bindingIndex()
	keys := make([]Expr, 0, len(idxPath.IndexClauses))
	for _, c := range idxPath.IndexClauses {
		keys = append(keys, translateToLayout("bitmap probe key", c.key, outerLay, outerIndex))
	}
	if len(keys) == 1 {
		bis.Key, bis.Keys = keys[0], nil
	} else {
		bis.Key, bis.Keys = nil, keys
	}
	// R49 Slice B: the recheck qual survives as `BitmapQual` in merged
	// outer++inner coordinates instead of being folded into the join Predicate.
	// Dropping it the way the index arm drops its keys is not an option: an
	// index probe enforces its keys exactly, but a bitmap heap scan does not.
	// Once the per-probe bitmap exceeds work_mem, `tbmLossify` degrades pages
	// to lossy and the heap scan yields EVERY tuple on such a page, relying on
	// `BitmapQual` to filter them — which is exactly what PG keeps
	// `bitmapqualorig` for. The executor evaluates it against the combined
	// outer++inner row the heap op builds from the bound outer slot
	// (operators_bitmap.go `evalBitmapQual`), where the layout translation
	// below is well defined; a leaf-local form is never needed.
	//
	// Orientation is inner-left (`kp.Right` first): PG's line reads
	// `Recheck Cond: (ss_item_sk = item.i_item_sk)`, the same inner-first
	// order `formatIndexCondParts` produces for the sibling `Index Cond:`.
	probeClauses := make([]*restrictInfo, 0, len(idxPath.IndexClauses))
	for _, c := range idxPath.IndexClauses {
		if c.ri != nil {
			probeClauses = append(probeClauses, c.ri)
		}
	}
	jt := planJoinTypeFor(p, "PathNestLoop(NLI-bitmap)")
	pairs := in.keyPairs("PathNestLoop(NLI-bitmap)", probeClauses)
	bhs.BitmapQual = make([]Expr, 0, len(pairs))
	for _, kp := range pairs {
		bhs.BitmapQual = append(bhs.BitmapQual,
			&BinaryOp{pos: kp.Right.Pos(), Op: parser.OpEq, Left: kp.Right, Right: kp.Left})
	}
	return &NestedLoopIndexJoin{
		pos: in.outer.Pos(), Type: jt, Outer: in.outer, Inner: bhs,
		// Residual-only: the probe clauses moved onto the probe above (MOVE,
		// not copy — R48 doctrine). In the corpus equi-probe shape Residual
		// is nil and combineAnd(nil) is nil, so the join line vanishes.
		Predicate: in.joinPredicate("PathNestLoop(NLI-bitmap)", nil, p.Residual),
		schema: in.publishedSchema(jt),
	}, in.publishedLayout(jt)
}
