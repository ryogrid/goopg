package optimizer

import (
	"strings"
	"testing"
)

// B-01c Slice 1 (COMPUTE-ONLY sort_input_target): the Sort stamp is derived
// from existing walkers only and NEVER applied — no Project insertion, no
// schema change, no cost change. These tests pin the derivation (sort keys ∪
// above-needed, ascending child-output positions), the DESC/NULLS
// irrelevance, the fail-closed unknown marking, and the key-coverage assert.

// sitSort builds a Sort fixture over a fixed child schema.
func sitSort(names []string, keys []SortKey) *Sort {
	return &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
}

func sitCol(name string, idx int) *ColumnRef {
	return &ColumnRef{Index: idx, Name: name}
}

func sitKeepNames(t *testing.T, s *Sort, keep []int) []string {
	t.Helper()
	in := s.Child.Output()
	got := make([]string, len(keep))
	for i, c := range keep {
		if c < 0 || c >= len(in) {
			t.Fatalf("keep position %d out of range for %d-column input", c, len(in))
		}
		got[i] = in[c].Name
	}
	return got
}

func sitEqualInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDeriveSortInputKeepSingleKey: keys-only stamp keeps exactly the sort-key
// column, ascending by construction.
func TestDeriveSortInputKeepSingleKey(t *testing.T) {
	s := sitSort([]string{"a", "b", "c"}, []SortKey{{Expr: sitCol("b", 1)}})
	keep, ok := deriveSortInputKeep(s, nil)
	if !ok {
		t.Fatal("keys-only derivation declined an enumerable single key")
	}
	if !sitEqualInts(keep, []int{1}) {
		t.Fatalf("keep = %v (%v), want [1] (b)", keep, sitKeepNames(t, s, keep))
	}
}

// TestDeriveSortInputKeepMultiKey: every key column survives, in input order
// regardless of key order.
func TestDeriveSortInputKeepMultiKey(t *testing.T) {
	s := sitSort([]string{"a", "b", "c", "d"}, []SortKey{
		{Expr: sitCol("d", 3)},
		{Expr: sitCol("b", 1)},
	})
	keep, ok := deriveSortInputKeep(s, nil)
	if !ok {
		t.Fatal("keys-only derivation declined enumerable multi keys")
	}
	if !sitEqualInts(keep, []int{1, 3}) {
		t.Fatalf("keep = %v (%v), want [1 3] (b d)", keep, sitKeepNames(t, s, keep))
	}
}

// TestSortInputTargetIgnoresDescNulls: Desc / NullsFirst are ordering flags,
// not column reads — the keep is identical across all four variants.
func TestSortInputTargetIgnoresDescNulls(t *testing.T) {
	mkKeys := func(desc, nullsFirst bool) []SortKey {
		return []SortKey{
			{Expr: sitCol("c", 2), Desc: desc, NullsFirst: nullsFirst},
			{Expr: sitCol("a", 0), Desc: !desc, NullsFirst: !nullsFirst},
		}
	}
	var want []int
	for i, flags := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		s := sitSort([]string{"a", "b", "c"}, mkKeys(flags[0], flags[1]))
		keep, ok := deriveSortInputKeep(s, nil)
		if !ok {
			t.Fatalf("variant %v declined", flags)
		}
		if i == 0 {
			want = keep
			continue
		}
		if !sitEqualInts(keep, want) {
			t.Fatalf("variant %v keep = %v, want %v (flags must not move columns)", flags, keep, want)
		}
	}
	if !sitEqualInts(want, []int{0, 2}) {
		t.Fatalf("keep = %v, want [0 2] (a c)", want)
	}
}

// TestDeriveSortInputKeepExpressionKey: a key over an expression keeps every
// column the expression reads.
func TestDeriveSortInputKeepExpressionKey(t *testing.T) {
	key := &BinaryOp{Left: sitCol("a", 0), Right: sitCol("c", 2)}
	s := sitSort([]string{"a", "b", "c"}, []SortKey{{Expr: key}})
	keep, ok := deriveSortInputKeep(s, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable expression key")
	}
	if !sitEqualInts(keep, []int{0, 2}) {
		t.Fatalf("keep = %v, want [0 2] (a c)", keep)
	}
}

// TestDeriveSortInputKeepUnionAbove: keep = sort keys ∪ above-needed over a
// Limit(Project(Sort)) chain — the production upper shape.
func TestDeriveSortInputKeepUnionAbove(t *testing.T) {
	s := sitSort([]string{"a", "b", "c"}, []SortKey{{Expr: sitCol("b", 1)}})
	proj := &Project{
		Child: s,
		Targets: []Expr{
			sitCol("a", 0),
			&BinaryOp{Left: sitCol("c", 2), Right: &IntegerConst{Value: 1}},
		},
		schema: noSchema("a", "c"),
	}
	above := &Limit{Child: proj, Limit: &IntegerConst{Value: 10}}
	keep, ok := deriveSortInputKeep(s, above)
	if !ok {
		t.Fatal("derivation declined an enumerable Limit/Project chain")
	}
	// Keys {b} ∪ above {a, c} = all three, ascending.
	if !sitEqualInts(keep, []int{0, 1, 2}) {
		t.Fatalf("keep = %v (%v), want [0 1 2]", keep, sitKeepNames(t, s, keep))
	}
}

// TestDeriveSortInputKeepAboveAddsNothing: when the upper tree needs only
// what the keys already keep, the keep is the keys alone (no invention).
func TestDeriveSortInputKeepAboveAddsNothing(t *testing.T) {
	s := sitSort([]string{"a", "b", "c"}, []SortKey{{Expr: sitCol("b", 1)}})
	proj := &Project{Child: s, Targets: []Expr{sitCol("b", 1)}, schema: noSchema("b")}
	above := &Limit{Child: proj, Limit: &IntegerConst{Value: 5}, Offset: &IntegerConst{Value: 2}}
	keep, ok := deriveSortInputKeep(s, above)
	if !ok {
		t.Fatal("derivation declined a constant-Limit chain")
	}
	if !sitEqualInts(keep, []int{1}) {
		t.Fatalf("keep = %v, want [1] (b)", keep)
	}
}

// TestSortInputTargetUnknownOnOuterRefKey: a sort key reading another scope
// vetoes the derivation — never invent.
func TestSortInputTargetUnknownOnOuterRefKey(t *testing.T) {
	s := sitSort([]string{"a", "b"}, []SortKey{
		{Expr: &OuterColumnRef{Level: 1, Index: 0, Name: "a"}},
	})
	if _, ok := deriveSortInputKeep(s, nil); ok {
		t.Fatal("outer-ref sort key derived known; want unknown")
	}
	stampSortInputTarget(s, nil)
	if s.InputTargetKnown || s.InputTarget != nil {
		t.Fatalf("stamp = (%v, %v); want (nil, false)", s.InputTarget, s.InputTargetKnown)
	}
	// Unknown stamps assert nothing: no panic here is the pass.
	assertSortInputTargetCoversKeys(s)
}

// TestSortInputTargetUnknownOnSubqueryKey: an inner-scope plan inside a key
// vetoes via the scope signal.
func TestSortInputTargetUnknownOnSubqueryKey(t *testing.T) {
	inner := &Project{Child: &noNode{sch: noSchema("x")}, Targets: []Expr{sitCol("x", 0)}, schema: noSchema("x")}
	s := sitSort([]string{"a", "b"}, []SortKey{{Expr: &SubqueryExpr{Plan: inner}}})
	if _, ok := deriveSortInputKeep(s, nil); ok {
		t.Fatal("subquery sort key derived known; want unknown")
	}
}

// TestSortInputTargetUnknownOnUnnamedKey: a ref no test can check vetoes.
func TestSortInputTargetUnknownOnUnnamedKey(t *testing.T) {
	s := sitSort([]string{"a", "b"}, []SortKey{{Expr: &ColumnRef{Index: 0}}})
	if _, ok := deriveSortInputKeep(s, nil); ok {
		t.Fatal("unnamed sort key derived known; want unknown")
	}
}

// TestSortInputTargetUnknownOnUnenumeratedAboveKind: a node kind
// enclosingNodeScopeOf does not know on the path (LockRows) declines the
// whole derivation — a stop is never a pass.
func TestSortInputTargetUnknownOnUnenumeratedAboveKind(t *testing.T) {
	s := sitSort([]string{"a", "b"}, []SortKey{{Expr: sitCol("a", 0)}})
	above := &Project{
		Child:   &LockRows{Child: s},
		Targets: []Expr{sitCol("a", 0)},
		schema:  noSchema("a"),
	}
	if _, ok := deriveSortInputKeep(s, above); ok {
		t.Fatal("LockRows-on-path derived known; want unknown")
	}
	stampSortInputTarget(s, above)
	if s.InputTargetKnown {
		t.Fatalf("stamp known = true over LockRows; want unknown")
	}
}

// TestSortInputTargetUnknownOnUnenumerableAboveExpr: an outer ref in an
// above-chain target vetoes, even though the keys are enumerable.
func TestSortInputTargetUnknownOnUnenumerableAboveExpr(t *testing.T) {
	s := sitSort([]string{"a", "b"}, []SortKey{{Expr: sitCol("a", 0)}})
	above := &Project{
		Child:   s,
		Targets: []Expr{&OuterColumnRef{Level: 1, Index: 0, Name: "a"}},
		schema:  noSchema("a"),
	}
	if _, ok := deriveSortInputKeep(s, above); ok {
		t.Fatal("outer-ref above-target derived known; want unknown")
	}
}

// TestDeriveSortInputKeepAboveUnreachable: an above tree that does not
// contain the Sort declines rather than collecting a partial path.
func TestDeriveSortInputKeepAboveUnreachable(t *testing.T) {
	s := sitSort([]string{"a", "b"}, []SortKey{{Expr: sitCol("a", 0)}})
	other := sitSort([]string{"a", "b"}, []SortKey{{Expr: sitCol("b", 1)}})
	above := &Project{Child: other, Targets: []Expr{sitCol("a", 0)}, schema: noSchema("a")}
	if _, ok := deriveSortInputKeep(s, above); ok {
		t.Fatal("above tree without the Sort derived known; want unknown")
	}
}

// TestAssertFiresOnUncoveredKey: a sort key naming no input column stamps
// known-but-uncovering, and the assert panics loudly (fail-closed).
func TestAssertFiresOnUncoveredKey(t *testing.T) {
	s := sitSort([]string{"a", "b"}, []SortKey{{Expr: sitCol("ghost", 7)}})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("stamp with uncovered sort key did not panic; want fail-closed")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "ghost") {
			t.Fatalf("panic %q does not name the dropped key", msg)
		}
	}()
	stampSortInputTarget(s, nil)
}

// TestAssertFiresOnHandBuiltUncoveringStamp: the assert also fires when the
// payload is inconsistent for any other reason (not just via stamp) — it
// guards the invariant, not the call path.
func TestAssertFiresOnHandBuiltUncoveringStamp(t *testing.T) {
	s := sitSort([]string{"a", "b", "c"}, []SortKey{
		{Expr: sitCol("a", 0)},
		{Expr: sitCol("c", 2)},
	})
	s.InputTarget, s.InputTargetKnown = []int{0}, true
	defer func() {
		if recover() == nil {
			t.Fatal("uncovering hand-built stamp did not panic")
		}
	}()
	assertSortInputTargetCoversKeys(s)
}

// TestStampIsAdditiveNoMutation: stamping changes the payload and nothing
// else — same child pointer, same output, same keys, still a *Sort (no
// Project inserted, no narrowing).
func TestStampIsAdditiveNoMutation(t *testing.T) {
	s := sitSort([]string{"a", "b", "c"}, []SortKey{{Expr: sitCol("b", 1), Desc: true}})
	child, keys, outBefore := s.Child, s.Keys, s.Child.Output()
	proj := &Project{Child: s, Targets: []Expr{sitCol("a", 0), sitCol("b", 1)}, schema: noSchema("a", "b")}
	stampSortInputTarget(s, proj)
	if !s.InputTargetKnown || !sitEqualInts(s.InputTarget, []int{0, 1}) {
		t.Fatalf("stamp = (%v, %v); want ([0 1], true)", s.InputTarget, s.InputTargetKnown)
	}
	if s.Child != child {
		t.Fatal("stamp replaced the child")
	}
	if len(s.Output()) != len(outBefore) {
		t.Fatal("stamp changed the output schema")
	}
	for i := range outBefore {
		if s.Output()[i].Name != outBefore[i].Name {
			t.Fatal("stamp changed the output schema")
		}
	}
	if len(s.Keys) != len(keys) || s.Keys[0].Expr == nil || !s.Keys[0].Desc {
		t.Fatal("stamp changed the sort keys")
	}
	var _ Node = s // still a *Sort, no Project inserted above or below
}

// TestStampOverwriteWidens: the finalized above-aware re-stamp overwrites
// the construction-time keys-only stamp (overwrite-only, never accumulates).
func TestStampOverwriteWidens(t *testing.T) {
	s := sitSort([]string{"a", "b", "c"}, []SortKey{{Expr: sitCol("b", 1)}})
	stampSortInputTarget(s, nil)
	if !sitEqualInts(s.InputTarget, []int{1}) {
		t.Fatalf("keys-only stamp = %v, want [1]", s.InputTarget)
	}
	above := &Project{Child: s, Targets: []Expr{sitCol("a", 0)}, schema: noSchema("a")}
	stampSortInputTarget(s, above)
	if !s.InputTargetKnown || !sitEqualInts(s.InputTarget, []int{0, 1}) {
		t.Fatalf("re-stamp = (%v, %v); want ([0 1], true)", s.InputTarget, s.InputTargetKnown)
	}
}

// TestStampNilSortIsNoOp: stamping nil never panics (defensive; the planner
// only stamps non-nil Sorts).
func TestStampNilSortIsNoOp(t *testing.T) {
	stampSortInputTarget(nil, nil)
	assertSortInputTargetCoversKeys(nil)
}
