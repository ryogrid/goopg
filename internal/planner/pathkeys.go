package planner

// Pathkeys — the ordering a Path guarantees. See docs/design/cost-model/ chapter
// 04. This is the minimal form: the PathKey type and the containment comparison
// add_path needs to treat a better-ordered path as non-dominated. The public
// pathkeysContainedIn API, the produce/consume wiring (sort paths, ordered index
// paths, merge joins, Gather Merge), and richer tests arrive in phase C1.
//
// Deliberately syntactic, not equivalence-class-driven (design ch. 04 §2.1): two
// pathkeys match only when their sort expressions are syntactically equal. The
// consequence is a false negative, never a false positive — goopg may fail to
// notice that an `a`-sorted path satisfies a `b` requirement across an `a = b`
// join and insert a redundant sort. That is a missed optimisation, not a wrong
// plan. Wiring the existing equivalence-class builder (equiv_class.go) through
// pathkeys is the first deferred refinement (ch. 04 §4), not a milestone item.

// PathKey is one column of a path's ordering.
type PathKey struct {
	Expr       Expr
	SortAsc    bool
	NullsFirst bool
}

// pathKeyEqual reports whether two pathkeys describe the same ordering on the
// same expression. Expression identity uses the existing exprEqual
// (planner.go:11522); direction and null placement must also match.
func pathKeyEqual(a, b PathKey) bool {
	return a.SortAsc == b.SortAsc && a.NullsFirst == b.NullsFirst && exprEqual(a.Expr, b.Expr)
}

// comparePathkeysDim reduces two orderings to a dominance dimension, reproducing
// the sense of compare_pathkeys (pathkeys.c): a path whose ordering is a superset
// (a longer list sharing the shorter's prefix) is "more useful" and dominates on
// this axis; equal-length identical lists are equal; a divergence at any position
// makes them incomparable. Empty-vs-empty is equal, so in C0/C1 — where no path
// yet carries pathkeys — this always returns dimEqual and add_path reduces to a
// pure cost comparison.
func comparePathkeysDim(a, b []PathKey) dimensionCmp {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if !pathKeyEqual(a[i], b[i]) {
			return dimIncomparable // diverge on a shared position
		}
	}
	// One is a prefix of the other (or they are identical up to min length).
	switch {
	case len(a) == len(b):
		return dimEqual
	case len(a) > len(b):
		return dimBetter1 // a is more ordered
	default:
		return dimBetter2
	}
}

// pathkeysContainedIn reports whether the ordering `keys` satisfies the ordering
// `required` — i.e. `required` is a prefix of `keys`. Reproduces
// pathkeys_contained_in (pathkeys.c:343). A path sorted by (a, b, c) satisfies a
// requirement of (a, b). Used from C1 onward to decide whether a path already
// delivers a needed order (a merge clause, an ORDER BY, a Gather Merge key).
func pathkeysContainedIn(keys, required []PathKey) bool {
	if len(required) > len(keys) {
		return false
	}
	for i := range required {
		if !pathKeyEqual(keys[i], required[i]) {
			return false
		}
	}
	return true
}

// pathkeysForSortKeys builds the pathkeys a Sort node's keys guarantee — the
// make_pathkeys_for_sortclauses analogue (pathkeys.c:1336). A Sort path, an
// ORDER-BY-satisfying scan, or a Gather Merge carries these so add_path can keep
// it as the more-ordered candidate ([04] §3). goopg's SortKey uses Desc; a
// PathKey uses SortAsc, so the sense is inverted here. Consumed from C3/C5; the
// helper and its behaviour are pinned now so those phases inherit a tested API.
func pathkeysForSortKeys(keys []SortKey) []PathKey {
	if len(keys) == 0 {
		return nil
	}
	pks := make([]PathKey, len(keys))
	for i, k := range keys {
		pks[i] = PathKey{Expr: k.Expr, SortAsc: !k.Desc, NullsFirst: k.NullsFirst}
	}
	return pks
}

// pathkeyRedundantIn reports whether pk can be dropped from a pathkey list —
// the two cases of PG's pathkey_is_redundant (pathkeys.c:159).
//
//   - Case 1: the pathkey's expression is a constant. PG detects this when
//     the pathkey's EquivalenceClass contains a constant (EC_MUST_BE_REDUNDANT);
//     goopg's pathkeys are syntactic, so the direct test is isConstantExpr.
//     This is what makes `string_agg(distinct f1, ',')` presort by f1 alone:
//     the delimiter is a constant sort key and is dropped (the acceptance
//     case `Sort Key: f1`, not `f1, ','`).
//   - Case 2: the same expression already appears in the list. PG compares
//     EquivalenceClasses; the sort direction is deliberately ignored (a
//     lower-order column with the same expr cannot distinguish rows the first
//     one already ordered, pathkeys.c:141-147).
func pathkeyRedundantIn(pk PathKey, list []PathKey) bool {
	// Case 1 must be a PLAIN literal, not row-constancy (isConstantExpr): a
	// zero-arg volatile function like `random()` is row-independent but its
	// value changes every evaluation, so presorting by it is meaningless and —
	// critically — PG keeps it in the pathkey list here so that the greedy
	// pass's has_volatile_pathkey (planner.c:3351) can drop the whole
	// aggregate. EC_MUST_BE_REDUNDANT is set only for real Const members;
	// random() is an EC member, not a Const.
	if isPlainConst(stripPureRelabel(pk.Expr)) {
		return true
	}
	for _, existing := range list {
		if exprEqual(pk.Expr, existing.Expr) {
			return true
		}
	}
	return false
}

// appendPathKeys appends every non-redundant PathKey of source onto target and
// returns the updated list — PG's append_pathkeys (pathkeys.c:107). Used by
// adjust_group_pathkeys_for_groupagg to prefix an aggregate's pathkeys with
// the GROUP BY pathkeys, dropping any aggregate key the group keys already
// guarantee (e.g. `GROUP BY ten` plus an aggregate `ORDER BY ten, two` sorts
// by `ten, two`).
//
// The caller must not share target's backing array with a slice it reuses:
// appendPathKeys may grow it in place.
func appendPathKeys(target, source []PathKey) []PathKey {
	for _, pk := range source {
		if pathkeyRedundantIn(pk, target) {
			continue
		}
		target = append(target, pk)
	}
	return target
}

// makeCandidatePathkeys builds the pathkeys an aggregate's DISTINCT/ORDER BY
// sortlist requires, dropping constants and duplicate expressions along the
// way — PG's make_pathkeys_for_sortclauses (pathkeys.c:1381), which appends a
// key only when !pathkey_is_redundant. The dedup is load-bearing: the
// delimiter in `string_agg(distinct f1, ',')` is a constant sort key and must
// not survive into the plan's Sort (pathkey_is_redundant case 1).
func makeCandidatePathkeys(sortlist []SortKey) []PathKey {
	var pks []PathKey
	for _, k := range sortlist {
		// isPlainConst, NOT isConstantExpr: see pathkeyRedundantIn — a
		// volatile zero-arg function must survive here so has_volatile_pathkey
		// can reject the aggregate later.
		if isPlainConst(stripPureRelabel(k.Expr)) {
			continue
		}
		pk := PathKey{Expr: k.Expr, SortAsc: !k.Desc, NullsFirst: k.NullsFirst}
		if pathkeyRedundantIn(pk, pks) {
			continue
		}
		pks = append(pks, pk)
	}
	return pks
}

// isPlainConst reports whether e is a literal value or parameter — goopg's
// analogue of PostgreSQL's Const for the pathkey-redundancy test
// (EC_MUST_BE_REDUNDANT). Deliberately narrower than isConstantExpr: it does
// NOT descend into FuncCall (a zero-arg volatile call like random() is
// row-independent but not constant across evaluations) nor into CastExpr /
// BinaryOp wrapping a literal. stripPureRelabel is applied by the callers so a
// relabeled literal still reads as a constant, matching PG's EC normalisation.
//
// Implemented as type assertions (isPlainConstantBound + a NullConst test) so
// the walker census does not count it as a new Expr switch site — it decides
// about the node in front of it and never descends.
func isPlainConst(e Expr) bool {
	if isPlainConstantBound(e) {
		return true
	}
	_, isNull := e.(*NullConst)
	return isNull
}
