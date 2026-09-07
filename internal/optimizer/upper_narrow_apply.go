package optimizer

import "os"

// upper_narrow_apply.go — B-01c APPLYING half, slice (b): the
// NARROWING-AWARE UPPER REWRITER, and the first production site that
// consumes it.
//
// Slice (a) (upper_narrow_gate.go) landed the proof obligation: given a
// keep-list, does the cut preserve every comparator the node reads, in
// position, with its DESC/NULLS flags. It had zero callers by design. This
// file is the caller, and it narrows nothing the gate has not passed.
//
// # What "applying" means here, and why the Aggregate site is first
//
// `upperNarrowingChangesOutput` (slice (a)) is the sequencing fact:
//
//   - `*Sort.Output()` and `*WindowAgg.Output()` are derived from the CHILD's
//     row, so narrowing their input narrows what every ancestor reads — the
//     cut has to re-base the whole chain up to the plan root, and must refuse
//     unless that chain provably ABSORBS the change before the root (the root
//     row is the query's answer).
//   - `*Aggregate.Output()` is built from the node's OWN expression lists
//     (group exprs ++ aggs ++ grouping masks ++ passthrough), so narrowing an
//     Aggregate's input moves NOTHING above it. The Aggregate IS the
//     absorption point.
//
// So the Aggregate site needs the gate and a local rewrite, and nothing else.
// That is this slice. The Sort and WindowAgg SITES — narrowing a Sort whose
// own stamped target is the keep — additionally need the ancestor-chain walk,
// which stays open under ledger row `take3-B-01c-applying-blocked` (its
// resume point names the walk) and is not attempted here.
//
// # Where the memory is actually saved: the SINK
//
// Wrapping `agg.Child` in a narrowing `Project` and stopping there would
// narrow only the row that crosses one operator boundary — the aggregate
// consumes its input a tuple at a time and retains transition states, not
// rows. The retention site under an Aggregate is the SORT beneath a sorted
// aggregation (and, transitively, whatever it spills). So the Project is
// SUNK: pushed down past order- and row-preserving wrappers (`*Sort`,
// `*Filter`) for as long as each wrapper's OWN expressions survive the cut,
// so the Sort sorts and spills the NARROWED row. That is D-06's "sort-side
// projection", reached from the one upper site that needs no chain walk.
//
// And the cut is COMMITTED ONLY IF it landed below a Sort. A narrowing
// Project is a per-row cost with nothing to show for it above a HASH
// aggregate, which retains transition states rather than rows: TPC-H Q1 would
// pay seven datum copies on six million rows and save zero bytes. Declining
// there is not timidity, it is what keeps this cut from being a regression
// dressed as an optimisation.
//
// Sinking past a `*Sort` is exactly the step the gate exists to police:
// `keepPreservesSortKeyList` re-proves the key list position by position,
// flags included, against the cut. `internal/executor/operators.go`
// (:1010-1015) checks ordering EXPLICITLY, not membership — a comparator
// that reads a shifted column emits out-of-order rows with NO error, which
// no row-count gate and no order-insensitive values gate can see.
//
// # Fail-closed, and the two independent checks
//
// The keep-list this file consumes was derived BY NAME (group_input_target.go
// unions column NAMES and takes the matching input positions). This file does
// NOT trust that derivation. Every expression that will read the narrowed row
// is re-verified POSITIONALLY here, through `keepPreservesExpr`'s round trip,
// against the node's fields AS THEY ARE NOW. A blind spot in the name-level
// collector therefore surfaces as a refusal, never as a wrong coordinate:
// `*Limit.TiesKeys`, for instance, is a row read that
// `enclosingNodeScopeOf`'s Limit arm does not enumerate, so a name-derived
// keep could drop a column it needs — and this file refuses to sink past a
// `*Limit` at all.
//
// Every traversal is `remapExprIndices`, i.e. `cloneExprRefs` under
// `scopeVeto`: exhaustive over all 32 `Expr` types by construction, with an
// unenumerated 33rd a build-time failure of `exprwalk_exhaustive_test.go`
// rather than a silent pass-through (`shiftColumnRefsBy`'s 13-of-32 mistake,
// tracked in `exprwalk_inventory_test.go`). The NODE-field side has the same
// hazard — a new `Expr`-bearing field on `*Aggregate`, `AggregateCall`,
// `*Sort` or `*Filter` would be silently left in pre-cut coordinates — and is
// pinned by `TestUpperNarrowApplyNodeFieldInventory`
// (upper_narrow_apply_test.go), which fails when the field set moves.
//
// A refusal at any point leaves the plan BIT-IDENTICAL to today's.

// narrowUpper resolves GOOPG_NARROW_UPPER at process start. Opt-out polarity
// (`=0` disables), matching GOOPG_NARROW_BUILD and GOOPG_PGSHAPED_DP: the
// flag exists so a gate can measure the FLAG rather than the commit, and so
// the pre-flip behaviour is one export away. Read once, in the server —
// putting it on a client command line sets it where nothing reads it.
var narrowUpper = narrowUpperFromEnv(os.Getenv("GOOPG_NARROW_UPPER"))

// narrowUpperFromEnv is the flag's polarity, factored out so tests resolve
// the same default the process starts with (flaglabels.go's contract: no
// literal restating a default elsewhere).
func narrowUpperFromEnv(v string) bool { return v != "0" }

// ---------------------------------------------------------------------
// The driver
// ---------------------------------------------------------------------

// applyUpperNarrowing narrows every eligible upper site in the finished tree
// rooted at n, IN PLACE, and returns n.
//
// In place is deliberate and is contained: the only site applied here is the
// Aggregate site, whose `Output()` does not move, so no ancestor's coordinates
// change and no caller holding the root (or holding `orderSort`, which the
// planner does) observes a different node identity. Nodes on the sunk path
// keep their pointers for the same reason; only their `Expr` fields are
// replaced, and with CLONES (`remapExprIndices` never mutates its input), so
// an expression shared with another tree cannot be corrupted.
//
// The descent is fail-closed in the cheap direction: `upperNarrowChildren`
// enumerates the node kinds this pass will walk THROUGH, and an unenumerated
// kind simply ends the descent. Missing a site costs memory; walking into a
// kind whose child coordinates are not what they look like costs correctness.
func applyUpperNarrowing(n Node) Node {
	if !narrowUpper || n == nil {
		return n
	}
	applyUpperNarrowingAt(n, map[Node]bool{})
	return n
}

// applyUpperNarrowingAt is applyUpperNarrowing's recursion. `seen` makes the
// walk idempotent over a DAG: a node reachable by two paths (the DML/CTE
// prefix shapes share subtrees) must not be narrowed twice, and a second
// application would be computed against an already-narrowed row.
func applyUpperNarrowingAt(n Node, seen map[Node]bool) {
	if n == nil || seen[n] {
		return
	}
	seen[n] = true
	for _, c := range upperNarrowChildren(n) {
		applyUpperNarrowingAt(c, seen)
	}
	if agg, ok := n.(*Aggregate); ok {
		narrowAggregateInput(agg)
	}
}

// upperNarrowChildren lists the children the pass descends into.
//
// This is NOT `enclosingNodeScopeOf`'s child list: that walker answers "which
// row do this node's own expressions index into", and its `*NestedLoopIndexJoin`
// arm deliberately omits the inner side for that reason. Here the question is
// only "where else in this tree might an Aggregate be", so both join arms are
// walked — and the enumeration is separate so neither question is answered by
// accident with the other's list.
//
// Unenumerated kinds return nil: the descent stops, and the sites below them
// keep today's full width.
func upperNarrowChildren(n Node) []Node {
	switch x := n.(type) {
	case *Project:
		return []Node{x.Child}
	case *Filter:
		return []Node{x.Child}
	case *Sort:
		return []Node{x.Child}
	case *Limit:
		return []Node{x.Child}
	case *Distinct:
		return []Node{x.Child}
	case *DistinctOn:
		return []Node{x.Child}
	case *Aggregate:
		return []Node{x.Child}
	case *WindowAgg:
		return []Node{x.Child}
	case *Gather:
		return []Node{x.Child}
	case *GatherMerge:
		return []Node{x.Child}
	case *Join:
		return []Node{x.Left, x.Right}
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	case *Result:
		return []Node{x.Child}
	case *CTEScan:
		return []Node{x.Child}
	}
	return nil
}

// ---------------------------------------------------------------------
// The Aggregate site
// ---------------------------------------------------------------------

// narrowAggregateInput applies agg's stamped input target: it rewrites agg's
// own input-scope expressions into post-cut coordinates and replaces agg's
// child with the narrowed subtree. Reports whether the cut was applied.
//
// Every early return leaves agg untouched. The two rewrites are computed to
// completion BEFORE either is installed, so a refusal discovered halfway
// through the sink cannot leave the aggregate rewritten over an un-narrowed
// row — which would be the silent wrong answer this whole cut is arranged to
// avoid.
func narrowAggregateInput(agg *Aggregate) bool {
	if agg == nil || agg.Child == nil || !agg.InputTargetKnown {
		return false
	}
	// A split aggregate reads the Gather'd PARTIAL row, not the row its
	// GroupExprs are written against; `stampAggregateInputTarget` already
	// declines to stamp one, and this is the same refusal restated where it
	// is load-bearing rather than inherited.
	if agg.Mode != AggModeSimple {
		return false
	}
	km, valid := newKeepMap(agg.InputTarget, len(agg.Child.Output()))
	if !valid || km.isIdentity() {
		// Identity is not a refusal, it is "nothing to drop": a Project that
		// reproduces its input verbatim is pure cost with zero memory saved.
		return false
	}
	// THE GATE. It re-derives the node's read set from `enclosingNodeScopeOf`
	// (wider than the name-level collector: Passthrough, agg Filter, the
	// window frame offsets) and re-proves the ORDER of every key list.
	if ok, _ := keepPreservesUpperNodeKeys(agg, agg.InputTarget); !ok {
		return false
	}
	// Belt and braces: the site must be one whose output does not move.
	if changes, ok := upperNarrowingChangesOutput(agg); !ok || changes {
		return false
	}

	rewritten, ok := rewriteAggregateInputExprs(agg, km)
	if !ok {
		return false
	}
	sunk, ok := planSinkNarrowingProject(agg.Child, km)
	if !ok {
		return false
	}
	// THE RETENTION-SITE CONDITION, and the reason this is a cut and not a
	// blanket rewrite. A narrowing `Project` is a per-row cost: `projectOp`
	// evaluates one `ColumnRef` per kept column for every row that crosses
	// it. Under a HASH aggregate that cost buys nothing — a hash aggregate
	// retains per-group transition states, not rows, so the input row is
	// consumed and dropped and there is no memory to save. TPC-H Q1 would
	// pay seven datum copies on six million rows for zero bytes retained.
	//
	// The site where a narrowed row IS retained is the SORT beneath a
	// sorted aggregation: it materialises every input row, compares them,
	// and spills them. So the cut is committed only when it landed BELOW a
	// Sort. Everywhere else the plan stays bit-identical, which is also why
	// this pass cannot regress a hash-aggregate shape.
	if !sunk.pastSort {
		return false
	}

	// COMMIT. Everything above this line is pure computation; everything
	// below is a write, and by here no write can fail.
	for _, apply := range sunk.install {
		apply()
	}

	installAggregateInputExprs(agg, rewritten)
	agg.Child = sunk.root
	// The stamp described positions in the PRE-cut row and now describes
	// nothing. Unknown is the safe reading everywhere it is consulted
	// (`assertAggregateInputTargetCoversKeys` asserts nothing on unknown) and
	// it makes a second pass over the same tree a no-op.
	agg.InputTarget, agg.InputTargetKnown = nil, false
	return true
}

// aggregateInputExprs is one Aggregate's complete set of input-row reads,
// rewritten. It exists so the rewrite can be computed in full and installed
// atomically; the field set it covers is pinned by
// TestUpperNarrowApplyNodeFieldInventory.
type aggregateInputExprs struct {
	groupExprs  []Expr
	passthrough []Expr
	aggs        []aggregateCallInputExprs
}

// aggregateCallInputExprs is one AggregateCall's rewritten input-row reads.
type aggregateCallInputExprs struct {
	arg                Expr
	arg2               Expr
	extraArgs          []Expr
	filter             Expr
	orderBy            []SortKey
	withinGroupOrderBy []SortKey
}

// rewriteAggregateInputExprs remaps every expression agg evaluates against its
// input row through km, without touching agg.
//
// `GroupingSets`, `GroupingMasks` and `GroupKeyOrder` are deliberately absent:
// they index into `GroupExprs` and into agg's OWN output row, neither of which
// this cut moves. `Strategy`, `Mode` and `Child` are not expressions.
//
// ok == false is a refusal for any of `keepPreservesExpr`'s reasons — a
// dropped column, an outer/CTID/whole-row read, an inner plan, an unenumerable
// type — and for a nil key inside an ordering list, which is undecidable and
// must not read as "preserved".
func rewriteAggregateInputExprs(agg *Aggregate, km keepMap) (aggregateInputExprs, bool) {
	var out aggregateInputExprs
	var ok bool
	if out.groupExprs, ok = keepPreservesExprList(agg.GroupExprs, km); !ok {
		return aggregateInputExprs{}, false
	}
	if out.passthrough, ok = keepPreservesExprList(agg.Passthrough, km); !ok {
		return aggregateInputExprs{}, false
	}
	out.aggs = make([]aggregateCallInputExprs, len(agg.Aggs))
	for i := range agg.Aggs {
		a := &agg.Aggs[i]
		var c aggregateCallInputExprs
		// keepPreservesExpr answers (nil, true) for a nil expression, which is
		// the right answer for an OPTIONAL field (count(*) has no Arg) and the
		// wrong one inside an ordering list — hence the two helpers.
		if c.arg, ok = keepPreservesExpr(a.Arg, km); !ok {
			return aggregateInputExprs{}, false
		}
		if c.arg2, ok = keepPreservesExpr(a.Arg2, km); !ok {
			return aggregateInputExprs{}, false
		}
		if c.extraArgs, ok = keepPreservesExprList(a.ExtraArgs, km); !ok {
			return aggregateInputExprs{}, false
		}
		if c.filter, ok = keepPreservesExpr(a.Filter, km); !ok {
			return aggregateInputExprs{}, false
		}
		if c.orderBy, ok = keepPreservesSortKeyList(a.OrderBy, km); !ok {
			return aggregateInputExprs{}, false
		}
		if c.withinGroupOrderBy, ok = keepPreservesSortKeyList(a.WithinGroupOrderBy, km); !ok {
			return aggregateInputExprs{}, false
		}
		out.aggs[i] = c
	}
	return out, true
}

// installAggregateInputExprs writes a completed rewrite onto agg. Split from
// the computation so no partial rewrite can be observed.
func installAggregateInputExprs(agg *Aggregate, r aggregateInputExprs) {
	agg.GroupExprs = r.groupExprs
	agg.Passthrough = r.passthrough
	for i := range agg.Aggs {
		a := &agg.Aggs[i]
		c := r.aggs[i]
		a.Arg = c.arg
		a.Arg2 = c.arg2
		a.ExtraArgs = c.extraArgs
		a.Filter = c.filter
		a.OrderBy = c.orderBy
		a.WithinGroupOrderBy = c.withinGroupOrderBy
	}
}

// ---------------------------------------------------------------------
// The sink
// ---------------------------------------------------------------------

// sinkPlan is a PLANNED sink: the subtree root that will hang under the
// Aggregate, whether the insertion point is below a `*Sort`, and the field
// writes that realise it.
//
// The deferred `install` list is not a style choice, it is the fix for a bug
// this file shipped and its own executor caught. An earlier revision mutated
// each wrapper as the recursion descended and only THEN tested the caller's
// retention-site condition; a refusal at that test left the `Filter` rewritten
// and the `Project` spliced in while the Aggregate above still read pre-cut
// positions — `SELECT grp, sum(v) ... WHERE v > 300 GROUP BY grp` came out as
// "column ref v/2 out of MaterializedSlot range 2". It surfaced as an error
// only because the stale index happened to fall outside the narrowed row; one
// column further left it would have been a silently wrong column, which is the
// exact failure this slice is arranged to make impossible. So nothing is
// written until every check has passed.
type sinkPlan struct {
	root     Node
	pastSort bool
	install  []func()
}

// planSinkNarrowingProject computes where the narrowing Project goes: as
// DEEPLY as the cut can be proved safe, reporting whether the insertion point
// is BELOW a `*Sort` (the caller's retention-site condition). It writes
// NOTHING; the returned `install` list does, once the caller commits.
//
// The recursion descends past a wrapper only when BOTH hold:
//
//   - the wrapper's OUTPUT row is its CHILD's row verbatim, so the SAME km
//     describes both sides of it and no second coordinate system appears; and
//   - every expression the wrapper itself evaluates against that row survives
//     the cut, re-proved here rather than inherited from the name-level
//     derivation.
//
// `*Sort` is the reason the sink exists: pushing the Project below it is what
// makes the sort — and its spill — carry the narrowed row. Its key list is
// re-proved by `keepPreservesSortKeyList`, position by position, DESC and
// NULLS FIRST included.
//
// The `pastSort` flag is what makes this a cut rather than a blanket rewrite —
// see the retention-site condition in `narrowAggregateInput`.
//
// Deliberately NOT sunk past:
//
//   - `*Limit` — `TiesKeys` is a row read that `enclosingNodeScopeOf`'s Limit
//     arm does not enumerate, so a name-derived keep may not cover it; and a
//     Limit under an Aggregate is not a retention site worth the risk.
//   - `*Distinct` / `*DistinctOn` — they compare the WHOLE row (Distinct) or
//     `KeyCols` into their own output (DistinctOn). Narrowing their input
//     changes which rows are distinct: a correct-looking row count over the
//     wrong tuple set.
//   - `*Gather` / `*GatherMerge` and every join — a parallel boundary and a
//     merged Left++Right row are each a second coordinate system, which is the
//     chain walk this slice does not do.
//
// Everything else falls through to wrapping the node as it stands, which is
// always sound: the Project sits directly on a row km was built against.
func planSinkNarrowingProject(child Node, km keepMap) (sinkPlan, bool) {
	switch x := child.(type) {
	case *Sort:
		if x.Child == nil {
			break
		}
		// Sort.Output() is Child.Output(), so km describes the Sort's input
		// row unchanged — but only if the widths actually agree; a malformed
		// tree declines rather than narrows.
		if len(x.Child.Output()) != km.inWidth {
			break
		}
		keys, ok := keepPreservesSortKeyList(x.Keys, km)
		if !ok {
			break
		}
		below, ok := planSinkNarrowingProject(x.Child, km)
		if !ok {
			break
		}
		return sinkPlan{
			root:     x,
			pastSort: true,
			install: append(below.install, func() {
				x.Keys = keys
				x.Child = below.root
				// The Sort's own stamped target counted positions in the
				// pre-cut row and now describes nothing.
				x.InputTarget, x.InputTargetKnown = nil, false
			}),
		}, true

	case *Filter:
		if x.Child == nil {
			break
		}
		if len(x.Child.Output()) != km.inWidth {
			break
		}
		pred, ok := keepPreservesExpr(x.Predicate, km)
		if !ok {
			break
		}
		// PushedBelow holds COPIES of Predicate conjuncts in this same
		// coordinate space (they are read by `filterSelectivity`, which must
		// keep matching the conjuncts it is excusing). Leaving them in pre-cut
		// coordinates would make the estimator excuse the wrong clause.
		pushed, ok := keepPreservesExprList(x.PushedBelow, km)
		if !ok {
			break
		}
		below, ok := planSinkNarrowingProject(x.Child, km)
		if !ok {
			break
		}
		return sinkPlan{
			root:     x,
			pastSort: below.pastSort,
			install: append(below.install, func() {
				x.Predicate = pred
				x.PushedBelow = pushed
				x.Child = below.root
			}),
		}, true
	}
	p, ok := narrowingProjectOver(child, km)
	if !ok {
		return sinkPlan{}, false
	}
	return sinkPlan{root: p}, true
}

// narrowingProjectOver builds the `Project` that performs the cut: one
// `ColumnRef` per kept column, in keep order, over n's row.
//
// It mirrors `narrowPlanOutput` (narrowoutput.go), the join-input narrower,
// including the one field that is easy to lose: `SourceTableIdx` is CARRIED,
// not dropped. Self-joins disambiguate two same-named columns by it (Q21's
// three `lineitem` aliases), and a Project that loses it makes them
// indistinguishable to every name-level walker above.
//
// The layout half of `narrowPlanOutput` has no counterpart here: an
// `outputLayout` is a join-search artefact of the leaf coordinate space, and
// this cut runs above the join tree on a finished plan, where the only
// coordinate is the schema position.
func narrowingProjectOver(n Node, km keepMap) (Node, bool) {
	if n == nil {
		return nil, false
	}
	out := n.Output()
	if len(out) != km.inWidth {
		return nil, false
	}
	targets := make([]Expr, km.narrowedWidth())
	schema := make(Schema, km.narrowedWidth())
	for i := 0; i < km.narrowedWidth(); i++ {
		old, ok := km.inverse(i)
		if !ok {
			return nil, false
		}
		col := out[old]
		targets[i] = &ColumnRef{
			Index:          old,
			Name:           col.Name,
			Type:           col.Type,
			SourceTableIdx: col.SourceTableIdx,
		}
		schema[i] = col
	}
	return &Project{pos: n.Pos(), Child: n, Targets: targets, schema: schema}, true
}
