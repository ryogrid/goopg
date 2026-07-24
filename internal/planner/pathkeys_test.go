package planner

import "testing"

// Phase C1 — pathkeys. The type and comparison landed in C0.1 (add_path needs
// the axis); these tests pin the public containment/comparison API and the
// produce helper with non-empty orderings, including the deliberate
// false-negative (design ch. 04 §2.1).

func col(idx int) Expr { return &ColumnRef{Index: idx} }

func pk(idx int, asc bool) PathKey { return PathKey{Expr: col(idx), SortAsc: asc} }

func TestPathkeysContainedIn_PrefixSatisfies(t *testing.T) {
	keys := []PathKey{pk(0, true), pk(1, true), pk(2, true)} // sorted by (a,b,c)
	if !pathkeysContainedIn(keys, []PathKey{pk(0, true), pk(1, true)}) {
		t.Fatalf("(a,b,c) must satisfy a requirement of (a,b)")
	}
	if !pathkeysContainedIn(keys, keys) {
		t.Fatalf("an ordering must satisfy itself")
	}
	if !pathkeysContainedIn(keys, nil) {
		t.Fatalf("any ordering satisfies the empty requirement")
	}
}

func TestPathkeysContainedIn_LongerRequirementNotSatisfied(t *testing.T) {
	keys := []PathKey{pk(0, true), pk(1, true)} // sorted by (a,b)
	if pathkeysContainedIn(keys, []PathKey{pk(0, true), pk(1, true), pk(2, true)}) {
		t.Fatalf("(a,b) must NOT satisfy a requirement of (a,b,c)")
	}
}

func TestPathkeysContainedIn_DirectionMismatch(t *testing.T) {
	keys := []PathKey{pk(0, true)}         // a ASC
	req := []PathKey{pk(0, false)}         // a DESC
	if pathkeysContainedIn(keys, req) {
		t.Fatalf("ASC ordering must not satisfy a DESC requirement")
	}
}

func TestPathkeysContainedIn_NullsPlacementMismatch(t *testing.T) {
	keys := []PathKey{{Expr: col(0), SortAsc: true, NullsFirst: false}}
	req := []PathKey{{Expr: col(0), SortAsc: true, NullsFirst: true}}
	if pathkeysContainedIn(keys, req) {
		t.Fatalf("differing NULLS placement must not satisfy the requirement")
	}
}

// TestPathkeysContainedIn_FalseNegativeIsAcceptable documents the deliberate
// syntactic limitation (design ch. 04 §2.1): an `a`-sorted path is NOT recognised
// as satisfying a `b` requirement even when a = b holds, because the comparison
// is syntactic, not equivalence-class-driven. This is a missed optimisation
// (a redundant sort), never a wrong plan.
func TestPathkeysContainedIn_FalseNegativeIsAcceptable(t *testing.T) {
	sortedByA := []PathKey{pk(0, true)}
	requireB := []PathKey{pk(1, true)}
	if pathkeysContainedIn(sortedByA, requireB) {
		t.Fatalf("syntactic comparison must not equate distinct columns")
	}
}

func TestComparePathkeysDim_LongerDominates(t *testing.T) {
	abc := []PathKey{pk(0, true), pk(1, true), pk(2, true)}
	ab := []PathKey{pk(0, true), pk(1, true)}
	if got := comparePathkeysDim(abc, ab); got != dimBetter1 {
		t.Fatalf("(a,b,c) should dominate (a,b) on ordering, got %v", got)
	}
	if got := comparePathkeysDim(ab, abc); got != dimBetter2 {
		t.Fatalf("(a,b) should be dominated by (a,b,c), got %v", got)
	}
	if got := comparePathkeysDim(ab, ab); got != dimEqual {
		t.Fatalf("identical orderings should be equal, got %v", got)
	}
}

func TestComparePathkeysDim_DivergenceIsIncomparable(t *testing.T) {
	ax := []PathKey{pk(0, true), pk(1, true)}
	ay := []PathKey{pk(0, true), pk(2, true)}
	if got := comparePathkeysDim(ax, ay); got != dimIncomparable {
		t.Fatalf("orderings diverging at a shared position are incomparable, got %v", got)
	}
}

func TestPathkeysForSortKeys(t *testing.T) {
	sk := []SortKey{
		{Expr: col(0), Desc: false, NullsFirst: false},
		{Expr: col(1), Desc: true, NullsFirst: true},
	}
	pks := pathkeysForSortKeys(sk)
	if len(pks) != 2 {
		t.Fatalf("want 2 pathkeys, got %d", len(pks))
	}
	if !pks[0].SortAsc || pks[1].SortAsc {
		t.Fatalf("Desc must invert to SortAsc: got asc=%v,%v", pks[0].SortAsc, pks[1].SortAsc)
	}
	if pks[0].NullsFirst || !pks[1].NullsFirst {
		t.Fatalf("NullsFirst must carry through: got %v,%v", pks[0].NullsFirst, pks[1].NullsFirst)
	}
	if pathkeysForSortKeys(nil) != nil {
		t.Fatalf("empty sort keys -> nil pathkeys")
	}
}
