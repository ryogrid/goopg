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
