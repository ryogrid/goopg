package optimizer

import (
	"strings"
	"testing"
)

// B-01c third cut (COMPUTE-ONLY window_input_target): the WindowAgg stamp is
// derived from existing walkers only and NEVER applied — no Project insertion,
// no schema change, no cost change. These tests pin the derivation (window
// inputs ∪ above-needed, ascending child-output positions), the fail-closed
// unknown marking, and the partition/order coverage assert. Mirrors
// group_input_target_test.go.
//
// Unlike the group cut there is NO field-level decline rule: every
// expression field on the node (PartitionBy, OrderBy keys, func args, func
// Filters, frame offsets) is enumerated by walkPlanExprs' WindowAgg arm, so
// Filters and offsets join the keep instead of vetoing it.

// witWin builds a WindowAgg fixture over a fixed child schema.
func witWin(names []string, partition []Expr, order []SortKey, funcs []WindowFunc, frame *WindowFrame) *WindowAgg {
	return &WindowAgg{
		Child:       &noNode{sch: noSchema(names...)},
		PartitionBy: partition,
		OrderBy:     order,
		Funcs:       funcs,
		Frame:       frame,
	}
}

func witKeepNames(t *testing.T, w *WindowAgg, keep []int) []string {
	t.Helper()
	in := w.Child.Output()
	got := make([]string, len(keep))
	for i, c := range keep {
		if c < 0 || c >= len(in) {
			t.Fatalf("keep position %d out of range for %d-column input", c, len(in))
		}
		got[i] = in[c].Name
	}
	return got
}

func witEqualInts(a, b []int) bool {
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

// TestDeriveWindowInputKeepSingleKey: keys-only stamp keeps exactly the
// partition column, ascending by construction.
func TestDeriveWindowInputKeepSingleKey(t *testing.T) {
	w := witWin([]string{"a", "b", "c"}, []Expr{gitCol("b", 1)}, nil, nil, nil)
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("keys-only derivation declined an enumerable single partition key")
	}
	if !witEqualInts(keep, []int{1}) {
		t.Fatalf("keep = %v (%v), want [1] (b)", keep, witKeepNames(t, w, keep))
	}
}

// TestDeriveWindowInputKeepMultiKey: every partition column survives, in
// input order regardless of key order.
func TestDeriveWindowInputKeepMultiKey(t *testing.T) {
	w := witWin([]string{"a", "b", "c", "d"},
		[]Expr{gitCol("d", 3), gitCol("b", 1)}, nil, nil, nil)
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("keys-only derivation declined enumerable multi partition keys")
	}
	if !witEqualInts(keep, []int{1, 3}) {
		t.Fatalf("keep = %v (%v), want [1 3] (b d)", keep, witKeepNames(t, w, keep))
	}
}

// TestWindowInputTargetOrderByUnion: order-key columns join the keep — the
// WindowAgg reads them from its input row alongside the partition keys.
func TestWindowInputTargetOrderByUnion(t *testing.T) {
	w := witWin([]string{"a", "b", "c"},
		[]Expr{gitCol("a", 0)},
		[]SortKey{{Expr: gitCol("c", 2), Desc: true}},
		nil, nil)
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable partition key + order key")
	}
	if !witEqualInts(keep, []int{0, 2}) {
		t.Fatalf("keep = %v (%v), want [0 2] (a c)", keep, witKeepNames(t, w, keep))
	}
}

// TestWindowInputTargetFuncArgUnion: func args join the keep.
func TestWindowInputTargetFuncArgUnion(t *testing.T) {
	w := witWin([]string{"a", "b", "c"},
		[]Expr{gitCol("a", 0)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{gitCol("c", 2)}}},
		nil)
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable partition key + func arg")
	}
	if !witEqualInts(keep, []int{0, 2}) {
		t.Fatalf("keep = %v (%v), want [0 2] (a c)", keep, witKeepNames(t, w, keep))
	}
}

// TestWindowInputTargetFuncFilterEnumerated: a func FILTER joins the keep
// instead of vetoing — walkPlanExprs' WindowAgg arm enumerates it (unlike
// the group cut's AggregateCall.Filter decline, which the narrower arm
// forced).
func TestWindowInputTargetFuncFilterEnumerated(t *testing.T) {
	w := witWin([]string{"a", "b", "f"},
		[]Expr{gitCol("a", 0)},
		nil,
		[]WindowFunc{{
			Name:   "sum",
			Args:   []Expr{gitCol("b", 1)},
			Filter: gitCol("f", 2),
		}},
		nil)
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable func filter")
	}
	if !witEqualInts(keep, []int{0, 1, 2}) {
		t.Fatalf("keep = %v (%v), want [0 1 2] (a b f)", keep, witKeepNames(t, w, keep))
	}
}

// TestWindowInputTargetFrameOffsetsEnumerated: frame offset columns join the
// keep instead of vetoing — both walkers enumerate them.
func TestWindowInputTargetFrameOffsetsEnumerated(t *testing.T) {
	w := witWin([]string{"a", "v", "o"},
		[]Expr{gitCol("a", 0)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{gitCol("v", 1)}}},
		&WindowFrame{StartOffset: gitCol("o", 2), EndOffset: &IntegerConst{Value: 1}})
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("derivation declined enumerable frame offsets")
	}
	if !witEqualInts(keep, []int{0, 1, 2}) {
		t.Fatalf("keep = %v (%v), want [0 1 2] (a v o)", keep, witKeepNames(t, w, keep))
	}
}

// TestDeriveWindowInputKeepExpressionKey: a partition key over an expression
// keeps every column the expression reads.
func TestDeriveWindowInputKeepExpressionKey(t *testing.T) {
	key := &BinaryOp{Left: gitCol("a", 0), Right: gitCol("c", 2)}
	w := witWin([]string{"a", "b", "c"}, []Expr{key}, nil, nil, nil)
	keep, ok := deriveWindowInputKeep(w, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable expression partition key")
	}
	if !witEqualInts(keep, []int{0, 2}) {
		t.Fatalf("keep = %v, want [0 2] (a c)", keep)
	}
}

// TestDeriveWindowInputKeepUnionAbove: keep = window inputs ∪ above-needed
// over a Limit(Project(WindowAgg)) chain — the production upper shape.
func TestDeriveWindowInputKeepUnionAbove(t *testing.T) {
	w := witWin([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{gitCol("b", 1)}}},
		nil)
	proj := &Project{
		Child: w,
		Targets: []Expr{
			gitCol("a", 0),
			&BinaryOp{Left: gitCol("c", 2), Right: &IntegerConst{Value: 1}},
		},
		schema: noSchema("a", "c"),
	}
	above := &Limit{Child: proj, Limit: &IntegerConst{Value: 10}}
	keep, ok := deriveWindowInputKeep(w, above)
	if !ok {
		t.Fatal("derivation declined an enumerable Limit/Project chain")
	}
	// Window inputs {b} ∪ above {a, c} = all three, ascending.
	if !witEqualInts(keep, []int{0, 1, 2}) {
		t.Fatalf("keep = %v (%v), want [0 1 2]", keep, witKeepNames(t, w, keep))
	}
}

// TestDeriveWindowInputKeepAboveAddsNothing: when the upper tree needs only
// what the window inputs already keep, the keep is the window inputs alone
// (no invention).
func TestDeriveWindowInputKeepAboveAddsNothing(t *testing.T) {
	w := witWin([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{gitCol("b", 1)}}},
		nil)
	proj := &Project{Child: w, Targets: []Expr{gitCol("b", 1)}, schema: noSchema("b")}
	above := &Limit{Child: proj, Limit: &IntegerConst{Value: 5}, Offset: &IntegerConst{Value: 2}}
	keep, ok := deriveWindowInputKeep(w, above)
	if !ok {
		t.Fatal("derivation declined a constant-Limit chain")
	}
	if !witEqualInts(keep, []int{1}) {
		t.Fatalf("keep = %v, want [1] (b)", keep)
	}
}

// TestWindowInputTargetUnknownOnOuterRefKey: a partition key reading another
// scope vetoes the derivation — never invent.
func TestWindowInputTargetUnknownOnOuterRefKey(t *testing.T) {
	w := witWin([]string{"a", "b"},
		[]Expr{&OuterColumnRef{Level: 1, Index: 0, Name: "a"}}, nil, nil, nil)
	if _, ok := deriveWindowInputKeep(w, nil); ok {
		t.Fatal("outer-ref partition key derived known; want unknown")
	}
	stampWindowInputTarget(w, nil)
	if w.InputTargetKnown || w.InputTarget != nil {
		t.Fatalf("stamp = (%v, %v); want (nil, false)", w.InputTarget, w.InputTargetKnown)
	}
	// Unknown stamps assert nothing: no panic here is the pass.
	assertWindowInputTargetCoversKeys(w)
}

// TestWindowInputTargetUnknownOnUnenumerableFilter: an outer ref in a func
// FILTER vetoes like any other window-input expression.
func TestWindowInputTargetUnknownOnUnenumerableFilter(t *testing.T) {
	w := witWin([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		nil,
		[]WindowFunc{{
			Name:   "sum",
			Args:   []Expr{gitCol("b", 1)},
			Filter: &OuterColumnRef{Level: 1, Index: 0, Name: "b"},
		}},
		nil)
	if _, ok := deriveWindowInputKeep(w, nil); ok {
		t.Fatal("outer-ref func filter derived known; want unknown")
	}
}

// TestWindowInputTargetUnknownOnUnenumerableOffset: an outer ref in a frame
// offset vetoes like any other window-input expression.
func TestWindowInputTargetUnknownOnUnenumerableOffset(t *testing.T) {
	w := witWin([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{gitCol("b", 1)}}},
		&WindowFrame{StartOffset: &OuterColumnRef{Level: 1, Index: 0, Name: "b"}})
	if _, ok := deriveWindowInputKeep(w, nil); ok {
		t.Fatal("outer-ref frame offset derived known; want unknown")
	}
}

// TestWindowInputTargetUnknownOnSubqueryArg: an inner-scope plan inside a
// func arg vetoes via the scope signal.
func TestWindowInputTargetUnknownOnSubqueryArg(t *testing.T) {
	inner := &Project{Child: &noNode{sch: noSchema("x")}, Targets: []Expr{gitCol("x", 0)}, schema: noSchema("x")}
	w := witWin([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{&SubqueryExpr{Plan: inner}}}},
		nil)
	if _, ok := deriveWindowInputKeep(w, nil); ok {
		t.Fatal("subquery func arg derived known; want unknown")
	}
}

// TestWindowInputTargetUnknownOnUnnamedKey: a ref no test can check vetoes.
func TestWindowInputTargetUnknownOnUnnamedKey(t *testing.T) {
	w := witWin([]string{"a", "b"}, []Expr{&ColumnRef{Index: 0}}, nil, nil, nil)
	if _, ok := deriveWindowInputKeep(w, nil); ok {
		t.Fatal("unnamed partition key derived known; want unknown")
	}
}

// TestWindowInputTargetUnknownOnUnenumeratedAboveKind: a node kind
// enclosingNodeScopeOf does not know on the path (LockRows) declines the
// whole derivation — a stop is never a pass.
func TestWindowInputTargetUnknownOnUnenumeratedAboveKind(t *testing.T) {
	w := witWin([]string{"a", "b"}, []Expr{gitCol("a", 0)}, nil, nil, nil)
	above := &Project{
		Child:   &LockRows{Child: w},
		Targets: []Expr{gitCol("a", 0)},
		schema:  noSchema("a"),
	}
	if _, ok := deriveWindowInputKeep(w, above); ok {
		t.Fatal("LockRows-on-path derived known; want unknown")
	}
	stampWindowInputTarget(w, above)
	if w.InputTargetKnown {
		t.Fatalf("stamp known = true over LockRows; want unknown")
	}
}

// TestWindowInputTargetUnknownOnUnenumerableAboveExpr: an outer ref in an
// above-chain target vetoes, even though the window inputs are enumerable.
func TestWindowInputTargetUnknownOnUnenumerableAboveExpr(t *testing.T) {
	w := witWin([]string{"a", "b"}, []Expr{gitCol("a", 0)}, nil, nil, nil)
	above := &Project{
		Child:   w,
		Targets: []Expr{&OuterColumnRef{Level: 1, Index: 0, Name: "a"}},
		schema:  noSchema("a"),
	}
	if _, ok := deriveWindowInputKeep(w, above); ok {
		t.Fatal("outer-ref above-target derived known; want unknown")
	}
}

// TestDeriveWindowInputKeepAboveUnreachable: an above tree that does not
// contain the WindowAgg declines rather than collecting a partial path.
func TestDeriveWindowInputKeepAboveUnreachable(t *testing.T) {
	w := witWin([]string{"a", "b"}, []Expr{gitCol("a", 0)}, nil, nil, nil)
	other := witWin([]string{"a", "b"}, []Expr{gitCol("b", 1)}, nil, nil, nil)
	above := &Project{Child: other, Targets: []Expr{gitCol("a", 0)}, schema: noSchema("a")}
	if _, ok := deriveWindowInputKeep(w, above); ok {
		t.Fatal("above tree without the WindowAgg derived known; want unknown")
	}
}

// TestWindowAssertFiresOnUncoveredPartitionKey: a partition key naming no
// input column stamps known-but-uncovering, and the assert panics loudly
// (fail-closed).
func TestWindowAssertFiresOnUncoveredPartitionKey(t *testing.T) {
	w := witWin([]string{"a", "b"}, []Expr{gitCol("ghost", 7)}, nil, nil, nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("stamp with uncovered partition key did not panic; want fail-closed")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "ghost") {
			t.Fatalf("panic %q does not name the dropped key", msg)
		}
	}()
	stampWindowInputTarget(w, nil)
}

// TestWindowAssertFiresOnUncoveredOrderKey: the assert covers order keys too,
// not just partition keys.
func TestWindowAssertFiresOnUncoveredOrderKey(t *testing.T) {
	w := witWin([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		[]SortKey{{Expr: gitCol("ghost", 7)}},
		nil, nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("stamp with uncovered order key did not panic; want fail-closed")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "ghost") {
			t.Fatalf("panic %q does not name the dropped key", msg)
		}
	}()
	stampWindowInputTarget(w, nil)
}

// TestWindowAssertFiresOnHandBuiltUncoveringStamp: the assert also fires when
// the payload is inconsistent for any other reason (not just via stamp) — it
// guards the invariant, not the call path.
func TestWindowAssertFiresOnHandBuiltUncoveringStamp(t *testing.T) {
	w := witWin([]string{"a", "b", "c"},
		[]Expr{gitCol("a", 0), gitCol("c", 2)}, nil, nil, nil)
	w.InputTarget, w.InputTargetKnown = []int{0}, true
	defer func() {
		if recover() == nil {
			t.Fatal("uncovering hand-built stamp did not panic")
		}
	}()
	assertWindowInputTargetCoversKeys(w)
}

// TestWindowStampIsAdditiveNoMutation: stamping changes the payload and
// nothing else — same child pointer, same output, same partition/order/funcs,
// still a *WindowAgg (no Project inserted, no narrowing).
func TestWindowStampIsAdditiveNoMutation(t *testing.T) {
	w := witWin([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		nil,
		[]WindowFunc{{Name: "sum", Args: []Expr{gitCol("b", 1)}}},
		nil)
	w.schema = noSchema("b", "sum")
	child, outBefore := w.Child, w.Output()
	proj := &Project{Child: w, Targets: []Expr{gitCol("a", 0), gitCol("b", 1)}, schema: noSchema("a", "b")}
	stampWindowInputTarget(w, proj)
	if !w.InputTargetKnown || !witEqualInts(w.InputTarget, []int{0, 1}) {
		t.Fatalf("stamp = (%v, %v); want ([0 1], true)", w.InputTarget, w.InputTargetKnown)
	}
	if w.Child != child {
		t.Fatal("stamp replaced the child")
	}
	if len(w.Output()) != len(outBefore) {
		t.Fatal("stamp changed the output schema")
	}
	for i := range outBefore {
		if w.Output()[i].Name != outBefore[i].Name {
			t.Fatal("stamp changed the output schema")
		}
	}
	if len(w.PartitionBy) != 1 || len(w.Funcs) != 1 || w.Funcs[0].Args == nil {
		t.Fatal("stamp changed the window inputs")
	}
	var _ Node = w // still a *WindowAgg, no Project inserted above or below
}

// TestWindowStampOverwriteWidens: the finalized above-aware re-stamp
// overwrites the construction-time keys-only stamp (overwrite-only, never
// accumulates).
func TestWindowStampOverwriteWidens(t *testing.T) {
	w := witWin([]string{"a", "b", "c"}, []Expr{gitCol("b", 1)}, nil, nil, nil)
	stampWindowInputTarget(w, nil)
	if !witEqualInts(w.InputTarget, []int{1}) {
		t.Fatalf("keys-only stamp = %v, want [1]", w.InputTarget)
	}
	above := &Project{Child: w, Targets: []Expr{gitCol("a", 0)}, schema: noSchema("a")}
	stampWindowInputTarget(w, above)
	if !w.InputTargetKnown || !witEqualInts(w.InputTarget, []int{0, 1}) {
		t.Fatalf("re-stamp = (%v, %v); want ([0 1], true)", w.InputTarget, w.InputTargetKnown)
	}
}

// TestWindowStampNilIsNoOp: stamping nil never panics (defensive; the planner
// only stamps non-nil WindowAggs).
func TestWindowStampNilIsNoOp(t *testing.T) {
	stampWindowInputTarget(nil, nil)
	assertWindowInputTargetCoversKeys(nil)
}
