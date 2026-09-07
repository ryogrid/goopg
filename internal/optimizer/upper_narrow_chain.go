package optimizer

import "os"

// upper_narrow_chain.go — B-01c APPLYING half, slice (c): the ANCESTOR-CHAIN
// WALK, and the `*Sort` site it unblocks.
//
// # What slices (a) and (b) left open, and why
//
// Slice (a) (upper_narrow_gate.go) is the proof obligation: does a keep-list
// preserve every comparator a node reads, in position, with its DESC/NULLS
// flags. Slice (b) (upper_narrow_apply.go) discharged it at the ONE upper site
// that needs nothing else — the `*Aggregate`, whose `Output()` is built from
// its own expression lists, so narrowing its input moves not one column above
// it.
//
// `*Sort` is the other half of `upperNarrowingChangesOutput`, and it is the
// harder half for exactly one reason: `Sort.Output()` IS `Child.Output()`. Cut
// a column out of a Sort's input row and the Sort's own output row loses it
// too, and so does every row above the Sort, all the way to the plan root. The
// cut is therefore not local: it is a coordinate change propagating UP a chain,
// and it is only sound if the chain provably ABSORBS the change before the
// query's answer row is reached.
//
// # The three classes, and why the third is a refusal rather than a TODO
//
// A parent of a narrowed row is exactly one of:
//
//   - ABSORB — its `Output()` is derived from its OWN expressions, not from
//     the child's width. `*Project` (its `schema` pairs with `Targets`) and
//     `*Aggregate` (group exprs ++ aggs ++ masks ++ passthrough). Rewrite its
//     expressions onto the narrowed row and the cut STOPS: nothing above it
//     can tell the difference. This is the same fact slice (b) leaned on, read
//     from the other side.
//   - PROPAGATE — schema-preserving: `Output()` is the child's row verbatim
//     (`*Filter`, `*Limit`, `*Sort`). Rewrite its own expressions and keep
//     walking; the SAME keepMap describes its output too, so no second
//     coordinate system appears anywhere on the chain.
//   - REFUSE — everything else. `*Distinct` compares the WHOLE row, so
//     narrowing its input changes which rows are distinct (a correct-looking
//     row count over the wrong tuple set); `*DistinctOn`'s `KeyCols` are
//     positions into an output whose width would move; `*Gather` /
//     `*GatherMerge` are a parallel boundary; a join publishes a merged
//     `Left ++ Right` row; `*CTEScan` and `*Result` publish a STORED schema
//     they have no targets to re-derive, so a narrowed child would leave
//     `Output()` describing a row that no longer exists. None of these is a
//     missing feature to be filled in later by widening this switch — each is
//     a different coordinate system, and the enumeration is deliberately
//     positive so that a node kind added to goopg tomorrow lands in REFUSE.
//
// And running OFF THE TOP of the chain is a refusal too. If the walk reaches
// the plan root still propagating, the root's row has been narrowed — and the
// root's row is the query's answer, so the client would receive fewer columns
// than it asked for. Absorption AT the root is fine (the root `*Project`'s
// `Output()` does not move); absorption never happening is not.
//
// # What this file does NOT do
//
//   - `*WindowAgg` as a narrowing SITE. It publishes
//     `[child row..., window func outputs...]`, so narrowing its input moves
//     its own output columns too — the chain above it would need a SECOND,
//     derived keepMap rather than this one, which is a different cut. It stays
//     open on `take3-B-01c-applying-blocked`, and `*WindowAgg` is in REFUSE
//     here both as a site and as an ancestor.
//   - `*OuterColumnRef`. The gate refuses it (upper_narrow_gate.go), so a
//     correlated upper expression declines rather than narrowing. Unchanged.
//   - Sinking past `*Limit` / `*Distinct` / `*DistinctOn` / `*Gather` / joins.
//     `planSinkNarrowingProject` (slice (b)) is reused verbatim, refusals
//     included.
//
// # The two lessons slice (b) paid for, restated because they apply here too
//
//  1. NOTHING IS WRITTEN WHILE DECIDING. Every ancestor's rewrite is computed
//     to completion and handed back as a deferred `install`; a refusal
//     discovered at the fourth ancestor must not leave the first three
//     rewritten over a row that was never narrowed. Slice (b) shipped that bug
//     inside its sink and its own executor caught it ("column ref v/2 out of
//     MaterializedSlot range 2") only because the stale index happened to fall
//     off the end of the narrowed row; one column further left it would have
//     been a silently wrong column. `TestNarrowSortInputLeavesNoPartialRewrite`
//     pins the same shape here.
//  2. A CUT THAT IS FREE TO COMPUTE IS NOT AUTOMATICALLY WORTH APPLYING. Slice
//     (b) commits only below a Sort because above a hash aggregate a narrowing
//     `Project` is per-row cost with zero bytes retained. Here the retention
//     site is the narrowing site: a `*Sort` materialises every input row,
//     compares them and spills them, so a narrowed input row is retained bytes
//     saved by construction. That is why this site carries no `pastSort`
//     condition — and it is a claim about THIS node kind, not a general
//     licence.
//
// # Fail-closed everywhere, and where the name-level derivation is distrusted
//
// The keep this file consumes is `Sort.InputTarget`, derived BY NAME
// (sort_input_target.go unions the sort keys' column names with the names the
// above-chain reads, then takes the matching input positions). Two of its
// properties make trusting it unsafe, and this file trusts none of it:
//
//   - the construction-time stamp is taken with `above == nil` — KEYS ONLY.
//     A `*Sort` still carrying that stamp has a keep that drops columns its
//     ancestors read, and the only thing standing between it and a wrong
//     answer is that every ancestor is re-verified POSITIONALLY here and the
//     chain refuses when a read does not survive.
//   - `*Limit.TiesKeys` is a row read `enclosingNodeScopeOf`'s `*Limit` arm
//     does not enumerate, so the above-chain derivation never saw it. This
//     file rewrites it explicitly and refuses when it does not survive; the
//     field set is pinned by `TestUpperNarrowApplyNodeFieldInventory`.
//
// Every traversal is `keepPreservesExpr` / `keepPreservesExprList` /
// `keepPreservesSortKeyList` — i.e. `cloneExprRefs` under `scopeVeto`,
// exhaustive over all 32 `Expr` types by construction, with an unenumerated
// 33rd a build-time failure rather than a silent pass-through.
//
// A refusal at any point leaves the plan BIT-IDENTICAL to slice (b)'s.

// narrowUpperSort resolves GOOPG_NARROW_UPPER_SORT at process start. Opt-out
// polarity (`=0` disables), matching GOOPG_NARROW_UPPER and GOOPG_NARROW_BUILD.
//
// It is a SECOND flag rather than a widening of the first because the two
// sites move different plans: slice (b) moves sorted-aggregate shapes, this one
// moves every ORDER BY sort whose input is wider than what the chain above it
// reads. An A/B that cannot separate them cannot attribute a regression to
// either. `GOOPG_NARROW_UPPER=0` still disables both — this site is a strict
// extension of that pass, not a parallel one.
var narrowUpperSort = narrowUpperSortFromEnv(os.Getenv("GOOPG_NARROW_UPPER_SORT"))

// narrowUpperSortFromEnv is the flag's polarity, factored out so tests and
// flaglabels.go resolve the same default the process starts with.
func narrowUpperSortFromEnv(v string) bool { return v != "0" }

// ---------------------------------------------------------------------
// The driver
// ---------------------------------------------------------------------

// applyUpperSortNarrowing narrows every eligible `*Sort` site in the finished
// tree rooted at n, IN PLACE.
//
// It is a SECOND, TOP-DOWN pass, run after `applyUpperNarrowingAt`'s bottom-up
// Aggregate pass rather than merged into it, and the sequencing is
// load-bearing in both directions:
//
//   - Slice (b) stays bit-identical where it fires. A Sort beneath a sorted
//     aggregation is narrowed by the Aggregate site, which CLEARS that Sort's
//     stamp on commit; by the time this pass sees it there is no target left
//     to apply, so the two sites cannot both cut the same node. Merging the
//     passes would have let this one reach such a Sort first (the Aggregate
//     recursion is children-first) and silently replace slice (b)'s cut with a
//     different one.
//   - The ancestor chain is read AFTER the Aggregate pass has finished moving
//     nodes, so no chain is computed against a tree that is about to change.
//
// Top-down is what makes the walk composable with itself: cutting at a Sort
// inserts a narrowing `*Project` as its child, and a deeper Sort found
// afterwards sees that Project as an ABSORBER on its own chain — which is
// correct, and is why the chain is re-derived at every site rather than cached.
func applyUpperSortNarrowing(root Node, refs map[Node]int) {
	if !narrowUpper || !narrowUpperSort || root == nil {
		return
	}
	applyUpperSortNarrowingAt(root, nil, map[Node]bool{}, refs)
}

// applyUpperSortNarrowingAt is the recursion. `path` is the ancestor chain from
// the plan ROOT down to (but not including) n, in root-first order.
//
// `seen` makes the walk idempotent over a DAG exactly as slice (b)'s does. It
// is NOT what makes a shared node safe — that is `refs`, checked at the site —
// it only stops the walk from re-deriving the same subtree.
func applyUpperSortNarrowingAt(n Node, path []Node, seen map[Node]bool, refs map[Node]int) {
	if n == nil || seen[n] {
		return
	}
	seen[n] = true
	if srt, ok := n.(*Sort); ok {
		narrowSortInput(srt, path, refs)
	}
	// A fresh backing array per level: `append(path, n)` would let two
	// siblings' recursions write the same slot, and the second subtree would
	// then be walked against the first's ancestor chain.
	below := make([]Node, len(path)+1)
	copy(below, path)
	below[len(path)] = n
	for _, c := range upperNarrowChildren(n) {
		applyUpperSortNarrowingAt(c, below, seen, refs)
	}
}

// upperNarrowRefCounts counts the parent edges each node has within the tree
// `upperNarrowChildren` walks.
//
// The `*Aggregate` site needed no such census: it moves nothing above itself,
// so a subtree reachable by two paths is narrowed once (the `seen` set) and
// both paths read the same, unchanged, output row. The `*Sort` site DOES move
// what is above it, and "above it" is then not a single answer: a Sort
// reachable from two parents has two ancestor chains, and rewriting one of them
// leaves the other reading pre-cut positions — the silent wrong column this
// whole slice is arranged around. So a node with more than one parent edge is
// refused, both as a site and anywhere on a chain.
//
// A node reachable through a kind `upperNarrowChildren` does not enumerate is
// not counted here — and is also never reached by the walk, so it is never a
// site either.
func upperNarrowRefCounts(root Node) map[Node]int {
	refs := make(map[Node]int)
	seen := make(map[Node]bool)
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		for _, c := range upperNarrowChildren(n) {
			if c == nil {
				continue
			}
			refs[c]++
			walk(c)
		}
	}
	walk(root)
	return refs
}

// ---------------------------------------------------------------------
// The Sort site
// ---------------------------------------------------------------------

// narrowSortInput applies srt's stamped input target: it re-bases srt's own
// keys, re-bases every ancestor up to the absorption point, and replaces srt's
// child with the narrowed subtree. Reports whether the cut was applied.
//
// Every early return leaves the whole tree untouched. The three rewrites — the
// sink below, the keys here, the chain above — are computed to completion
// BEFORE any of them is installed (see the file header, lesson 1).
func narrowSortInput(srt *Sort, path []Node, refs map[Node]int) bool {
	if srt == nil || srt.Child == nil || !srt.InputTargetKnown {
		return false
	}
	// A shared Sort has more than one ancestor chain; see upperNarrowRefCounts.
	if refs[srt] > 1 {
		return false
	}
	km, valid := newKeepMap(srt.InputTarget, len(srt.Child.Output()))
	if !valid || km.isIdentity() {
		// Identity is not a refusal, it is "nothing to drop": a Project that
		// reproduces its input verbatim is pure cost with zero memory saved.
		return false
	}
	// THE GATE (slice (a)). It re-derives the node's read set from
	// `enclosingNodeScopeOf` and re-proves the ORDER of the key list.
	if ok, _ := keepPreservesUpperNodeKeys(srt, srt.InputTarget); !ok {
		return false
	}
	// Belt and braces, and the inverse of slice (b)'s assertion: this site is
	// one whose output DOES move, which is what the chain walk is for. A node
	// kind that reached here claiming otherwise is a bug, not a fast path.
	if changes, ok := upperNarrowingChangesOutput(srt); !ok || !changes {
		return false
	}

	keys, ok := keepPreservesSortKeyList(srt.Keys, km)
	if !ok {
		return false
	}
	chain, ok := planRewriteAncestorChain(path, km, refs)
	if !ok {
		return false
	}
	// The sink is slice (b)'s, reused verbatim — including its refusals to
	// descend past `*Limit` / `*Distinct` / `*DistinctOn` / `*Gather` / joins.
	// Its `pastSort` report is deliberately IGNORED here: slice (b) needs it
	// because an Aggregate is not itself a retention site, whereas this cut's
	// narrowing site IS the sort that materialises, compares and spills the
	// row (file header, lesson 2).
	sunk, ok := planSinkNarrowingProject(srt.Child, km)
	if !ok {
		return false
	}
	// The chain above was checked node by node; the sink's wrappers are the
	// same question below the site, and a shared one would leave its other
	// parent reading pre-cut positions.
	if anyShared(sunk.touched, refs) {
		return false
	}

	// COMMIT. Everything above this line is pure computation; everything below
	// is a write, and by here no write can fail.
	for _, apply := range sunk.install {
		apply()
	}
	for _, apply := range chain {
		apply()
	}
	srt.Keys = keys
	srt.Child = sunk.root
	// The stamp described positions in the PRE-cut row and now describes
	// nothing. Unknown is the safe reading everywhere it is consulted
	// (`assertSortInputTargetCoversKeys` asserts nothing on unknown) and it
	// makes a second pass over the same tree a no-op.
	srt.InputTarget, srt.InputTargetKnown = nil, false
	return true
}

// ---------------------------------------------------------------------
// The chain walk
// ---------------------------------------------------------------------

// ancestorAction is what one parent kind does with a narrowed child row: see
// the three classes in the file header.
type ancestorAction int

const (
	// ancestorRefuse is the zero value on purpose: a node kind that falls off
	// the end of `planRewriteAncestor`'s switch refuses, it does not propagate.
	ancestorRefuse ancestorAction = iota
	ancestorPropagate
	ancestorAbsorb
)

// planRewriteAncestorChain computes the rewrite of every ancestor between the
// narrowing site and the absorption point, WITHOUT writing any of it.
//
// `path` is root-first, so the walk runs BACKWARDS from the nearest parent. It
// returns the install closures in the order they were computed; they are
// order-independent (each writes only its own node's fields) but are applied in
// one batch by the caller after every check has passed.
//
// ok == false is a refusal, for any of:
//
//   - a parent kind in the REFUSE class;
//   - a parent whose own expressions do not survive the cut — the case that
//     catches a keys-only `Sort.InputTarget` stamp before it can drop a column
//     an ancestor reads;
//   - a parent shared with a second subtree (`refs > 1`), whose other chain
//     would be left in pre-cut coordinates;
//   - a width disagreement, i.e. an ancestor whose child row is not the row
//     this keepMap was built against — a malformed or unexpectedly-shaped tree
//     declines rather than narrows;
//   - the chain running out before anything ABSORBED. The root row is the
//     query's answer and must not lose a column.
func planRewriteAncestorChain(path []Node, km keepMap, refs map[Node]int) ([]func(), bool) {
	install := make([]func(), 0, len(path))
	for i := len(path) - 1; i >= 0; i-- {
		n := path[i]
		if n == nil || refs[n] > 1 {
			return nil, false
		}
		act, apply, ok := planRewriteAncestor(n, km)
		if !ok || act == ancestorRefuse {
			return nil, false
		}
		install = append(install, apply)
		if act == ancestorAbsorb {
			return install, true
		}
	}
	// Ran off the top still propagating: the narrowed row would BE the answer.
	return nil, false
}

// planRewriteAncestor classifies one parent of a narrowed row and computes its
// rewrite. It writes nothing; `apply` does, once the caller commits.
//
// The width precondition is checked per node rather than argued once for the
// chain: every PROPAGATE kind publishes its child's row verbatim, so the same
// `km.inWidth` must describe every level, and an ancestor that disagrees is
// evidence the chain is not the shape this walk believes it is.
func planRewriteAncestor(n Node, km keepMap) (ancestorAction, func(), bool) {
	switch x := n.(type) {

	// ---- ABSORB ----

	case *Project:
		// An IsolatedScope Project's targets are "inner-indexed-then-outer-
		// relabeled" (plan.go) and the posMap family deliberately skips its
		// child; a scope this pass does not model is refused rather than
		// rewritten on the assumption that "indexes into the child row" still
		// means the same thing there.
		if x.IsolatedScope || x.Child == nil || len(x.Child.Output()) != km.inWidth {
			return ancestorRefuse, nil, false
		}
		// The Project's own `schema` is what makes it an absorber, and it must
		// pair with the targets it publishes; a node where it does not is not
		// one this pass understands.
		if len(x.schema) != len(x.Targets) {
			return ancestorRefuse, nil, false
		}
		targets, ok := keepPreservesExprList(x.Targets, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		return ancestorAbsorb, func() { x.Targets = targets }, true

	case *Aggregate:
		// A split aggregate reads the Gather'd PARTIAL row, not the row its
		// GroupExprs are written against — the same refusal slice (b) makes at
		// its own site, restated where it is load-bearing.
		if x.Mode != AggModeSimple || x.Child == nil || len(x.Child.Output()) != km.inWidth {
			return ancestorRefuse, nil, false
		}
		rewritten, ok := rewriteAggregateInputExprs(x, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		return ancestorAbsorb, func() {
			installAggregateInputExprs(x, rewritten)
			// Its stamp counted positions in the pre-cut row.
			x.InputTarget, x.InputTargetKnown = nil, false
		}, true

	// ---- PROPAGATE ----

	case *Filter:
		if x.Child == nil || len(x.Child.Output()) != km.inWidth {
			return ancestorRefuse, nil, false
		}
		pred, ok := keepPreservesExpr(x.Predicate, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		// PushedBelow holds COPIES of Predicate conjuncts in the same
		// coordinate space, read by `filterSelectivity`; leaving them pre-cut
		// would make the estimator excuse the wrong clause.
		pushed, ok := keepPreservesExprList(x.PushedBelow, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		return ancestorPropagate, func() {
			x.Predicate = pred
			x.PushedBelow = pushed
		}, true

	case *Sort:
		if x.Child == nil || len(x.Child.Output()) != km.inWidth {
			return ancestorRefuse, nil, false
		}
		keys, ok := keepPreservesSortKeyList(x.Keys, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		return ancestorPropagate, func() {
			x.Keys = keys
			x.InputTarget, x.InputTargetKnown = nil, false
		}, true

	case *Limit:
		if x.Child == nil || len(x.Child.Output()) != km.inWidth {
			return ancestorRefuse, nil, false
		}
		// Limit/Offset are constants or params in every production plan, but a
		// lowered `LIMIT (SELECT ...)` could put a same-scope reference there,
		// and `enclosingNodeScopeOf` checks them for that reason. Two nil
		// round-trips is not a cost worth arguing about.
		lim, ok := keepPreservesExpr(x.Limit, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		off, ok := keepPreservesExpr(x.Offset, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		// TiesKeys is THE field the name-level derivation cannot have covered:
		// `enclosingNodeScopeOf`'s Limit arm does not enumerate it, so a
		// keep derived from that walk never saw these columns. It is an
		// ORDERING list (the executor emits rows until the key changes), so it
		// gets the order- and length-preserving list check, and a keep that
		// drops one of its columns refuses here rather than silently changing
		// how many tied rows come back.
		ties, ok := keepPreservesExprList(x.TiesKeys, km)
		if !ok {
			return ancestorRefuse, nil, false
		}
		if len(x.TiesKeys) == 0 {
			// Preserve nil-ness: `keepPreservesExprList` returns an empty
			// non-nil slice, and `WithTies == false` plans carry nil.
			ties = nil
		}
		return ancestorPropagate, func() {
			x.Limit = lim
			x.Offset = off
			x.TiesKeys = ties
		}, true
	}

	// ---- REFUSE ----
	//
	// Deliberately the default, and deliberately reached by falling off the
	// end of a POSITIVE enumeration: a node kind added to goopg tomorrow lands
	// here, at today's full width, rather than being propagated through on the
	// assumption that its Output() is its child's.
	return ancestorRefuse, nil, false
}
