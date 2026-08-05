package planner

import "github.com/goopg/goopg/internal/parser"

// JoinKeyPair is one equi-join clause of a hash or merge join, already
// oriented: `Left` references only the join's left input, `Right` only
// the right one. It is the unit PG carries in `HashJoin.hashclauses` /
// `MergeJoin.mergeclauses`, where the list — not a single pair — IS the
// join's key.
//
// M0127-P2.1 (design `leftdeep-joins/05` §5, stage E4). goopg's join has
// carried exactly ONE key pair (`Join.LeftKey` / `Join.RightKey`, the
// first disjoint-side equality `splitEqualityForHash` finds) since M0003,
// with every other equality conjunct left in `Join.Predicate` and
// re-checked per hash match. That single-pair shape is the direct cause
// of two recorded defects:
//
//   - the degeneracy trap (M0125-0035b): when qual placement pins the
//     chosen key column to a constant on BOTH inputs, the whole build
//     side lands in one bucket and the join goes quadratic even though
//     the plan looks PG-identical. `reselectDegenerateHashKeys` swapped the
//     pair away from the pinned column as a workaround; with the full
//     list there is nothing to swap, because the other columns still
//     spread the buckets. PG never faces the choice. (That pass was
//     retired by P2.2, which made the full list the executor's key.)
//   - a per-match interpreted residual call for every extra conjunct
//     (`joinPredicateMatchSlot`), on the all-equijoin common case where
//     PG does no residual work at all.
//
// P2.1 is the planner half of the sibling pair: it publishes the list.
// P2.2 is the executor half (composite key encoding): it is where the
// list became the thing the hash table is actually keyed on (see
// `ExecHashKeyPlan`, join_exec_keys.go) and where
// `reselectDegenerateHashKeys` retired.
type JoinKeyPair struct {
	Left  Expr
	Right Expr
}

// forEachEqualityForHash calls fn for every AND-conjunct of pred that is
// an equality whose two sides reference DISJOINT join inputs, passing the
// operands already oriented as (left-input expr, right-input expr) —
// `right.col = left.col` is flipped here so callers stay one-direction.
// fn returning false stops the walk.
//
// leftWidth is the column count of the left input; a ColumnRef with
// Index < leftWidth is left-side, >= leftWidth is right-side. This is the
// extracted core of `splitEqualityForHash` (which keeps its exact former
// behaviour by stopping at the first pair) so that the single-pair and
// full-list views can never disagree about what counts as a hash key.
func forEachEqualityForHash(pred Expr, leftWidth int, fn func(l, r Expr) bool) {
	for _, conjunct := range splitAnd(pred) {
		bin, ok := conjunct.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			continue
		}
		lSide := exprSide(bin.Left, leftWidth)
		rSide := exprSide(bin.Right, leftWidth)
		switch {
		case lSide == sideLeft && rSide == sideRight:
			if !fn(bin.Left, bin.Right) {
				return
			}
		case lSide == sideRight && rSide == sideLeft:
			if !fn(bin.Right, bin.Left) {
				return
			}
		}
	}
}

// splitAllEqualitiesForHash returns EVERY usable equi-pair of pred, in
// conjunct order. `splitEqualityForHash` returns only the first.
func splitAllEqualitiesForHash(pred Expr, leftWidth int) []JoinKeyPair {
	var pairs []JoinKeyPair
	forEachEqualityForHash(pred, leftWidth, func(l, r Expr) bool {
		pairs = append(pairs, JoinKeyPair{Left: l, Right: r})
		return true
	})
	return pairs
}

// fillJoinHashKeys populates `Join.HashKeys` on every hash/merge join in
// the finished plan.
//
// WHY IT RUNS AS ONE LATE PASS RATHER THAN AT EACH CONSTRUCTION SITE.
// Nine sites assign `LeftKey`/`RightKey` (planner.go, pushdown.go,
// bushy.go, unnest.go ×5, inner_join_qual_pushdown.go) and at least six
// later passes REWRITE the key or predicate expressions in place —
// `reresolveJoinByName`'s predRebind, bushy's subRemap, FoldConstants,
// `lowerSubPlanParams`, the qual-placement passes, and (until P2.2
// retired it) `reselectDegenerateHashKeys`, which re-picked the pair
// outright. A field filled early is a field those passes must all be taught to
// maintain, and the one time that was not done the shared ColumnRef
// pointer got mutated under the keys and chained NATURAL JOINs probed
// the wrong column (M0097-0060). Deriving the list once, after the last
// rewriter has run, makes staleness structurally impossible instead of a
// maintenance obligation.
//
// The pass is idempotent (it recomputes from scratch). `Predicate` is left
// holding the FULL conjunction, including the equalities also published as
// keys — roughly fifteen planner consumers read it as the join's complete
// condition. `(*Join).Residual` is the non-equijoin projection; since P2.2
// the executor takes its own narrower split from `ExecHashKeyPlan`
// (join_exec_keys.go), which is what the hash table is keyed on.
func fillJoinHashKeys(root Node) {
	fillJoinHashKeysNodes(root)
	// Sublink bodies are plans of their own hanging off expressions, not
	// off planChildren, so the node walk cannot reach them. `scopeDescend`
	// is exactly the exprwalk policy for "hand me the inner Node and I
	// will traverse it" — using it instead of a hand-written 5-arm switch
	// over the sublink types is what keeps this pass out of the RC-1a
	// defect class (M0125-0001: an unenumerated 33rd Expr type would
	// otherwise be a silent no-op here forever). The recursion covers
	// sublinks nested inside sublinks; fillJoinHashKeysNodes is
	// idempotent, so overlapping visits are harmless.
	walkPlanExprs(root, func(e Expr) {
		walkExprRefs(e, scopeDescend, exprVisitor{
			OnScope: func(inner Node) { fillJoinHashKeys(inner) },
		})
	})
}

// fillJoinHashKeysNodes is fillJoinHashKeys' plan-node recursion. The
// arms were mirrored from `reselectDegenerateHashKeys` — the other late
// pass over join keys, retired by P2.2 — so this pass reaches exactly the
// subtrees key rewriting always did.
func fillJoinHashKeysNodes(n Node) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *Explain:
		fillJoinHashKeysNodes(x.Child)
	case *Filter:
		fillJoinHashKeysNodes(x.Child)
	case *Join:
		fillOneJoinHashKeys(x)
		fillJoinHashKeysNodes(x.Left)
		fillJoinHashKeysNodes(x.Right)
	case *NestedLoopIndexJoin:
		fillJoinHashKeysNodes(x.Outer)
		fillJoinHashKeysNodes(x.Inner)
	case *MultiHashJoin:
		for _, t := range x.Tables {
			fillJoinHashKeysNodes(t)
		}
	case *CTEScan:
		fillJoinHashKeysNodes(x.Child)
	case *CTEDMLPrefix:
		fillJoinHashKeysNodes(x.Body)
	case *Project:
		fillJoinHashKeysNodes(x.Child)
	case *Sort:
		fillJoinHashKeysNodes(x.Child)
	case *Limit:
		fillJoinHashKeysNodes(x.Child)
	case *Aggregate:
		fillJoinHashKeysNodes(x.Child)
	case *WindowAgg:
		fillJoinHashKeysNodes(x.Child)
	case *Distinct:
		fillJoinHashKeysNodes(x.Child)
	case *DistinctOn:
		fillJoinHashKeysNodes(x.Child)
	case *SetOp:
		fillJoinHashKeysNodes(x.Left)
		fillJoinHashKeysNodes(x.Right)
	case *RecursiveUnion:
		fillJoinHashKeysNodes(x.Anchor)
		fillJoinHashKeysNodes(x.Recursive)
	case *Gather:
		fillJoinHashKeysNodes(x.Child)
	case *GatherMerge:
		fillJoinHashKeysNodes(x.Child)
	case *LockRows:
		fillJoinHashKeysNodes(x.Child)
	}
}

// fillOneJoinHashKeys derives one join's key list.
//
// `HashKeys[0]` is pinned to the join's existing (LeftKey, RightKey) BY
// POINTER, not by re-deriving it: that pair is the one the executor
// hashes on today (`reselectDegenerateHashKeys` could deliberately move
// it off the first ON-clause conjunct until P2.2 retired that pass), and
// `IsCanonicalKeyEquality` identifies the canonical conjunct by pointer
// identity. Everything after
// index 0 is the remaining usable equalities in conjunct order.
//
// The extra pairs are CLONED when they are ColumnRefs (the M0097-0060
// lesson): they otherwise alias the `Predicate` nodes, and a future pass
// that rebinds an index in the predicate would silently move a key.
func fillOneJoinHashKeys(j *Join) {
	j.HashKeys = nil
	if j.Algo != JoinAlgoHash && j.Algo != JoinAlgoMerge {
		return
	}
	if j.LeftKey == nil || j.RightKey == nil || j.Left == nil || j.Right == nil {
		return
	}
	pairs := []JoinKeyPair{{Left: j.LeftKey, Right: j.RightKey}}
	leftWidth := len(j.Left.Output())
	forEachEqualityForHash(j.Predicate, leftWidth, func(l, r Expr) bool {
		for _, have := range pairs {
			if exprEqual(have.Left, l) && exprEqual(have.Right, r) {
				return true
			}
		}
		pairs = append(pairs, JoinKeyPair{Left: cloneKeyExpr(l), Right: cloneKeyExpr(r)})
		return true
	})
	j.HashKeys = pairs
}

// cloneKeyExpr shallow-copies a ColumnRef so the key list does not alias
// the predicate's nodes; other expression kinds are shared as-is, which
// is what the existing key slots already do for them.
func cloneKeyExpr(e Expr) Expr {
	if cr, ok := e.(*ColumnRef); ok {
		c := *cr
		return &c
	}
	return e
}

// Residual returns the conjunction of this join's predicate conjuncts
// that are NOT already published in `HashKeys` — the genuinely
// non-equijoin part of the ON clause, which is what an executor keying
// on the full list still has to evaluate per match. nil means "no
// residual work".
//
// This is a projection, not a mutation: `Predicate` keeps the full
// conjunction. Roughly fifteen planner consumers read `Predicate` as the
// join's complete condition (qual pushdown, cardinality estimation, NLI
// equi-key harvesting, bushy remapping, MHJ extra capture), and P2.1 is
// not the place to re-audit all of them — the executor half (P2.2) is
// what needs the split, and it can take it from here. See the deferral
// ledger row for M0127-P2.1.
//
// With `HashKeys` empty (a nested-loop join, or a join built after the
// fill pass ran) the residual is the whole predicate, which is exactly
// today's behaviour.
func (n *Join) Residual() Expr {
	return n.residualExcluding(n.HashKeys)
}

// residualExcluding is Residual's core, parameterised by which pairs count as
// discharged. `Residual` passes the full published list; the executor's
// `ExecHashKeyPlan` passes only the pairs it is actually going to fold into
// the key encoding (M0127-P2.2), which is a subset — a conjunct whose pair was
// declined must keep being evaluated per match.
func (n *Join) residualExcluding(keys []JoinKeyPair) Expr {
	if n == nil || n.Predicate == nil {
		return nil
	}
	if len(keys) == 0 || n.Left == nil {
		return n.Predicate
	}
	leftWidth := len(n.Left.Output())
	var keep []Expr
	for _, c := range splitAnd(n.Predicate) {
		if conjunctIsOneOfKeys(c, keys, leftWidth) {
			continue
		}
		keep = append(keep, c)
	}
	if len(keep) == 0 {
		return nil
	}
	return combineAnd(keep)
}

// conjunctIsOneOfKeys reports whether c is one of the given equi-pairs, in
// either operand order.
func conjunctIsOneOfKeys(c Expr, keys []JoinKeyPair, leftWidth int) bool {
	bin, ok := c.(*BinaryOp)
	if !ok || bin.Op != parser.OpEq {
		return false
	}
	l, r := bin.Left, bin.Right
	if exprSide(l, leftWidth) == sideRight && exprSide(r, leftWidth) == sideLeft {
		l, r = r, l
	}
	for _, k := range keys {
		if exprEqual(k.Left, l) && exprEqual(k.Right, r) {
			return true
		}
	}
	return false
}
