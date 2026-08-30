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
// only the equalities `pickIndexCoveringAllLeadingColumns` accepted
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

import "fmt"

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
		panic(fmt.Sprintf("createPlan: parameterised PathNestLoop over relset %#04x; nothing above the search root binds a parameter",
			uint16(relsOf(p))))
	}

	innerPath := p.Children[1]
	if innerPath != nil && innerPath.RequiredOuter != 0 {
		return createNestLoopIndexJoinPlan(p, innerPath)
	}

	// The plain nested loop. Every clause is residual and is evaluated against
	// the merged row, exactly like the hash arm's — which is why the prologue
	// and the predicate builder are shared verbatim rather than restated.
	in := joinInputsFor(p, "PathNestLoop", p.Children[0], innerPath)
	j := &Join{
		pos:  in.outer.Pos(),
		Type: JoinTypeInner,
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
		schema:    in.merged,
	}
	return j, in.lay
}

// createNestLoopIndexJoinPlan is the NLI shape: the inner is a parameterised
// index path and this loop is what binds its parameter.
//
// `innerPath` is passed rather than re-read from `p.Children[1]` so the caller's
// dispatch decision and this function's assumption are the same value.
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
		panic(fmt.Sprintf("createPlan: NLI inner is parameterised by %#04x, which the outer relset %#04x does not supply",
			uint16(unsupplied), uint16(outerPath.Rel.Relids)))
	}

	in := joinInputsFor(p, "PathNestLoop(NLI)", outerPath, innerPath)
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
		keys = append(keys, translateToLayout("index probe key", c.key, outerLay, outerIndex))
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

	nli := &NestedLoopIndexJoin{
		pos:   in.outer.Pos(),
		Type:  JoinTypeInner,
		Outer: in.outer,
		Inner: is,
		// The residual is evaluated against `outer ++ inner` through the
		// operator's `virtualOut` (operators_nljoin.go:133-141), so it is
		// translated onto the MERGED layout — the other of this arm's two
		// coordinate spaces. No key conjuncts are folded in: unlike the hash
		// arm, where the key is enforced only by a hash bucket and must be
		// re-checked, an index probe enforces its keys exactly.
		Predicate: in.joinPredicate("PathNestLoop(NLI)", nil, p.Residual),
		schema:    in.merged,
		// InnerMemo is filled below, and only when the SEARCH chose a
		// `PathMemoize` inner. It is never decided here: attaching a cache to a
		// join whose cost was computed without one makes the executed plan
		// cheaper than the plan that won the comparison, which is the uncosted
		// opinion 06 §2.1 retires. `getMemoizePath` (joinpathsmemoize.go) is
		// where the decision lives, beside every alternative it competes with.
	}
	if memoPath != nil {
		nli.InnerMemo = memoizeNodeFor(memoPath, is, keys)
	}
	return nli, in.lay
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
