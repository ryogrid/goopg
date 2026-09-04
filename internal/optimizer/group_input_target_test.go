package optimizer

import (
	"strings"
	"testing"
)

// B-01c second cut (COMPUTE-ONLY group_input_target): the Aggregate stamp is
// derived from existing walkers only and NEVER applied — no Project insertion,
// no schema change, no cost change. These tests pin the derivation (group
// inputs ∪ above-needed, ascending child-output positions), the Filter /
// Passthrough decline rules, the fail-closed unknown marking, and the
// group-input coverage assert. Mirrors sort_input_target_test.go.

// gitAgg builds an Aggregate fixture over a fixed child schema.
func gitAgg(names []string, groupExprs []Expr, aggs []AggregateCall) *Aggregate {
	return &Aggregate{Child: &noNode{sch: noSchema(names...)}, GroupExprs: groupExprs, Aggs: aggs}
}

func gitCol(name string, idx int) *ColumnRef {
	return &ColumnRef{Index: idx, Name: name}
}

func gitKeepNames(t *testing.T, a *Aggregate, keep []int) []string {
	t.Helper()
	in := a.Child.Output()
	got := make([]string, len(keep))
	for i, c := range keep {
		if c < 0 || c >= len(in) {
			t.Fatalf("keep position %d out of range for %d-column input", c, len(in))
		}
		got[i] = in[c].Name
	}
	return got
}

func gitEqualInts(a, b []int) bool {
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

// TestDeriveAggregateInputKeepSingleKey: keys-only stamp keeps exactly the
// group-key column, ascending by construction.
func TestDeriveAggregateInputKeepSingleKey(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"}, []Expr{gitCol("b", 1)}, nil)
	keep, ok := deriveAggregateInputKeep(a, nil)
	if !ok {
		t.Fatal("keys-only derivation declined an enumerable single group key")
	}
	if !gitEqualInts(keep, []int{1}) {
		t.Fatalf("keep = %v (%v), want [1] (b)", keep, gitKeepNames(t, a, keep))
	}
}

// TestDeriveAggregateInputKeepMultiKey: every group-key column survives, in
// input order regardless of key order.
func TestDeriveAggregateInputKeepMultiKey(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c", "d"},
		[]Expr{gitCol("d", 3), gitCol("b", 1)}, nil)
	keep, ok := deriveAggregateInputKeep(a, nil)
	if !ok {
		t.Fatal("keys-only derivation declined enumerable multi group keys")
	}
	if !gitEqualInts(keep, []int{1, 3}) {
		t.Fatalf("keep = %v (%v), want [1 3] (b d)", keep, gitKeepNames(t, a, keep))
	}
}

// TestAggregateInputTargetAggArgUnion: aggregate args join the keep — the
// Aggregate reads them from its input row alongside the group keys.
func TestAggregateInputTargetAggArgUnion(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("a", 0)},
		[]AggregateCall{{Name: "sum", Arg: gitCol("c", 2)}})
	keep, ok := deriveAggregateInputKeep(a, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable group key + agg arg")
	}
	if !gitEqualInts(keep, []int{0, 2}) {
		t.Fatalf("keep = %v (%v), want [0 2] (a c)", keep, gitKeepNames(t, a, keep))
	}
}

// TestAggregateInputTargetOrderByArgs: internal ORDER BY and WITHIN GROUP
// ORDER BY args are enumerable via the existing walkers and join the keep.
func TestAggregateInputTargetOrderByArgs(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("a", 0)},
		[]AggregateCall{{
			Name:               "array_agg",
			Arg:                gitCol("b", 1),
			OrderBy:            []SortKey{{Expr: gitCol("c", 2), Desc: true}},
			WithinGroup:        true,
			WithinGroupOrderBy: []SortKey{{Expr: gitCol("c", 2)}},
		}})
	keep, ok := deriveAggregateInputKeep(a, nil)
	if !ok {
		t.Fatal("derivation declined enumerable OrderBy/WithinGroupOrderBy args")
	}
	if !gitEqualInts(keep, []int{0, 1, 2}) {
		t.Fatalf("keep = %v (%v), want [0 1 2] (a b c)", keep, gitKeepNames(t, a, keep))
	}
}

// TestDeriveAggregateInputKeepExpressionGroupKey: a group key over an
// expression keeps every column the expression reads.
func TestDeriveAggregateInputKeepExpressionGroupKey(t *testing.T) {
	key := &BinaryOp{Left: gitCol("a", 0), Right: gitCol("c", 2)}
	a := gitAgg([]string{"a", "b", "c"}, []Expr{key}, nil)
	keep, ok := deriveAggregateInputKeep(a, nil)
	if !ok {
		t.Fatal("derivation declined an enumerable expression group key")
	}
	if !gitEqualInts(keep, []int{0, 2}) {
		t.Fatalf("keep = %v, want [0 2] (a c)", keep)
	}
}

// TestDeriveAggregateInputKeepUnionAbove: keep = group inputs ∪ above-needed
// over a Limit(Project(Aggregate)) chain — the production upper shape.
func TestDeriveAggregateInputKeepUnionAbove(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		[]AggregateCall{{Name: "sum", Arg: gitCol("b", 1)}})
	proj := &Project{
		Child: a,
		Targets: []Expr{
			gitCol("a", 0),
			&BinaryOp{Left: gitCol("c", 2), Right: &IntegerConst{Value: 1}},
		},
		schema: noSchema("a", "c"),
	}
	above := &Limit{Child: proj, Limit: &IntegerConst{Value: 10}}
	keep, ok := deriveAggregateInputKeep(a, above)
	if !ok {
		t.Fatal("derivation declined an enumerable Limit/Project chain")
	}
	// Group inputs {b} ∪ above {a, c} = all three, ascending.
	if !gitEqualInts(keep, []int{0, 1, 2}) {
		t.Fatalf("keep = %v (%v), want [0 1 2]", keep, gitKeepNames(t, a, keep))
	}
}

// TestDeriveAggregateInputKeepAboveAddsNothing: when the upper tree needs only
// what the group inputs already keep, the keep is the group inputs alone (no
// invention).
func TestDeriveAggregateInputKeepAboveAddsNothing(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		[]AggregateCall{{Name: "sum", Arg: gitCol("b", 1)}})
	proj := &Project{Child: a, Targets: []Expr{gitCol("b", 1)}, schema: noSchema("b")}
	above := &Limit{Child: proj, Limit: &IntegerConst{Value: 5}, Offset: &IntegerConst{Value: 2}}
	keep, ok := deriveAggregateInputKeep(a, above)
	if !ok {
		t.Fatal("derivation declined a constant-Limit chain")
	}
	if !gitEqualInts(keep, []int{1}) {
		t.Fatalf("keep = %v, want [1] (b)", keep)
	}
}

// TestAggregateInputTargetUnknownOnFilter: any AggregateCall.Filter present
// declines — walkPlanExprs' Aggregate arm never walks Filter, so no
// node-level walker enumerates it. This holds even when the filter expression
// itself is enumerable: the veto is on the field, not the expression.
func TestAggregateInputTargetUnknownOnFilter(t *testing.T) {
	a := gitAgg([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		[]AggregateCall{{
			Name:   "sum",
			Arg:    gitCol("b", 1),
			Filter: &BinaryOp{Left: gitCol("b", 1), Right: &IntegerConst{Value: 0}},
		}})
	if _, ok := deriveAggregateInputKeep(a, nil); ok {
		t.Fatal("filtered aggregate derived known; want unknown")
	}
	stampAggregateInputTarget(a, nil)
	if a.InputTargetKnown || a.InputTarget != nil {
		t.Fatalf("stamp = (%v, %v); want (nil, false)", a.InputTarget, a.InputTargetKnown)
	}
	// Unknown stamps assert nothing: no panic here is the pass.
	assertAggregateInputTargetCoversKeys(a)
}

// TestAggregateInputTargetUnknownOnPassthrough: any Passthrough present
// declines — walkPlanExprs' Aggregate arm never walks Passthrough either.
func TestAggregateInputTargetUnknownOnPassthrough(t *testing.T) {
	a := gitAgg([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		[]AggregateCall{{Name: "sum", Arg: gitCol("b", 1)}})
	a.Passthrough = []Expr{gitCol("b", 1)}
	if _, ok := deriveAggregateInputKeep(a, nil); ok {
		t.Fatal("passthrough aggregate derived known; want unknown")
	}
	stampAggregateInputTarget(a, nil)
	if a.InputTargetKnown || a.InputTarget != nil {
		t.Fatalf("stamp = (%v, %v); want (nil, false)", a.InputTarget, a.InputTargetKnown)
	}
	assertAggregateInputTargetCoversKeys(a)
}

// TestAggregateInputTargetUnknownOnOuterRefKey: a group key reading another
// scope vetoes the derivation — never invent.
func TestAggregateInputTargetUnknownOnOuterRefKey(t *testing.T) {
	a := gitAgg([]string{"a", "b"},
		[]Expr{&OuterColumnRef{Level: 1, Index: 0, Name: "a"}}, nil)
	if _, ok := deriveAggregateInputKeep(a, nil); ok {
		t.Fatal("outer-ref group key derived known; want unknown")
	}
	stampAggregateInputTarget(a, nil)
	if a.InputTargetKnown || a.InputTarget != nil {
		t.Fatalf("stamp = (%v, %v); want (nil, false)", a.InputTarget, a.InputTargetKnown)
	}
	// Unknown stamps assert nothing: no panic here is the pass.
	assertAggregateInputTargetCoversKeys(a)
}

// TestAggregateInputTargetUnknownOnSubqueryArg: an inner-scope plan inside an
// aggregate arg vetoes via the scope signal.
func TestAggregateInputTargetUnknownOnSubqueryArg(t *testing.T) {
	inner := &Project{Child: &noNode{sch: noSchema("x")}, Targets: []Expr{gitCol("x", 0)}, schema: noSchema("x")}
	a := gitAgg([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		[]AggregateCall{{Name: "sum", Arg: &SubqueryExpr{Plan: inner}}})
	if _, ok := deriveAggregateInputKeep(a, nil); ok {
		t.Fatal("subquery agg arg derived known; want unknown")
	}
}

// TestAggregateInputTargetUnknownOnUnnamedKey: a ref no test can check vetoes.
func TestAggregateInputTargetUnknownOnUnnamedKey(t *testing.T) {
	a := gitAgg([]string{"a", "b"}, []Expr{&ColumnRef{Index: 0}}, nil)
	if _, ok := deriveAggregateInputKeep(a, nil); ok {
		t.Fatal("unnamed group key derived known; want unknown")
	}
}

// TestAggregateInputTargetUnknownOnUnenumerableOrderBy: an unenumerable
// WithinGroupOrderBy arg vetoes like any other group-input expression.
func TestAggregateInputTargetUnknownOnUnenumerableOrderBy(t *testing.T) {
	a := gitAgg([]string{"a", "b"},
		[]Expr{gitCol("a", 0)},
		[]AggregateCall{{
			Name:               "percentile_cont",
			Arg:                &NumericConst{Value: "0.5"},
			WithinGroup:        true,
			WithinGroupOrderBy: []SortKey{{Expr: &OuterColumnRef{Level: 1, Index: 0, Name: "b"}}},
		}})
	if _, ok := deriveAggregateInputKeep(a, nil); ok {
		t.Fatal("outer-ref WithinGroupOrderBy arg derived known; want unknown")
	}
}

// TestAggregateInputTargetUnknownOnUnenumeratedAboveKind: a node kind
// enclosingNodeScopeOf does not know on the path (LockRows) declines the
// whole derivation — a stop is never a pass.
func TestAggregateInputTargetUnknownOnUnenumeratedAboveKind(t *testing.T) {
	a := gitAgg([]string{"a", "b"}, []Expr{gitCol("a", 0)}, nil)
	above := &Project{
		Child:   &LockRows{Child: a},
		Targets: []Expr{gitCol("a", 0)},
		schema:  noSchema("a"),
	}
	if _, ok := deriveAggregateInputKeep(a, above); ok {
		t.Fatal("LockRows-on-path derived known; want unknown")
	}
	stampAggregateInputTarget(a, above)
	if a.InputTargetKnown {
		t.Fatalf("stamp known = true over LockRows; want unknown")
	}
}

// TestAggregateInputTargetUnknownOnUnenumerableAboveExpr: an outer ref in an
// above-chain target vetoes, even though the group inputs are enumerable.
func TestAggregateInputTargetUnknownOnUnenumerableAboveExpr(t *testing.T) {
	a := gitAgg([]string{"a", "b"}, []Expr{gitCol("a", 0)}, nil)
	above := &Project{
		Child:   a,
		Targets: []Expr{&OuterColumnRef{Level: 1, Index: 0, Name: "a"}},
		schema:  noSchema("a"),
	}
	if _, ok := deriveAggregateInputKeep(a, above); ok {
		t.Fatal("outer-ref above-target derived known; want unknown")
	}
}

// TestDeriveAggregateInputKeepAboveUnreachable: an above tree that does not
// contain the Aggregate declines rather than collecting a partial path.
func TestDeriveAggregateInputKeepAboveUnreachable(t *testing.T) {
	a := gitAgg([]string{"a", "b"}, []Expr{gitCol("a", 0)}, nil)
	other := gitAgg([]string{"a", "b"}, []Expr{gitCol("b", 1)}, nil)
	above := &Project{Child: other, Targets: []Expr{gitCol("a", 0)}, schema: noSchema("a")}
	if _, ok := deriveAggregateInputKeep(a, above); ok {
		t.Fatal("above tree without the Aggregate derived known; want unknown")
	}
}

// TestAssertFiresOnUncoveredKey: a group key naming no input column stamps
// known-but-uncovering, and the assert panics loudly (fail-closed).
func TestAggregateAssertFiresOnUncoveredKey(t *testing.T) {
	a := gitAgg([]string{"a", "b"}, []Expr{gitCol("ghost", 7)}, nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("stamp with uncovered group key did not panic; want fail-closed")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "ghost") {
			t.Fatalf("panic %q does not name the dropped key", msg)
		}
	}()
	stampAggregateInputTarget(a, nil)
}

// TestAssertFiresOnHandBuiltUncoveringStamp: the assert also fires when the
// payload is inconsistent for any other reason (not just via stamp) — it
// guards the invariant, not the call path.
func TestAggregateAssertFiresOnHandBuiltUncoveringStamp(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("a", 0), gitCol("c", 2)}, nil)
	a.InputTarget, a.InputTargetKnown = []int{0}, true
	defer func() {
		if recover() == nil {
			t.Fatal("uncovering hand-built stamp did not panic")
		}
	}()
	assertAggregateInputTargetCoversKeys(a)
}

// TestStampIsAdditiveNoMutation: stamping changes the payload and nothing
// else — same child pointer, same output, same group exprs and calls, still
// an *Aggregate (no Project inserted, no narrowing).
func TestAggregateStampIsAdditiveNoMutation(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		[]AggregateCall{{Name: "sum", Arg: gitCol("b", 1)}})
	a.schema = noSchema("b", "sum")
	child, outBefore := a.Child, a.Output()
	proj := &Project{Child: a, Targets: []Expr{gitCol("a", 0), gitCol("b", 1)}, schema: noSchema("a", "b")}
	stampAggregateInputTarget(a, proj)
	if !a.InputTargetKnown || !gitEqualInts(a.InputTarget, []int{0, 1}) {
		t.Fatalf("stamp = (%v, %v); want ([0 1], true)", a.InputTarget, a.InputTargetKnown)
	}
	if a.Child != child {
		t.Fatal("stamp replaced the child")
	}
	if len(a.Output()) != len(outBefore) {
		t.Fatal("stamp changed the output schema")
	}
	for i := range outBefore {
		if a.Output()[i].Name != outBefore[i].Name {
			t.Fatal("stamp changed the output schema")
		}
	}
	if len(a.GroupExprs) != 1 || len(a.Aggs) != 1 || a.Aggs[0].Arg == nil {
		t.Fatal("stamp changed the group inputs")
	}
	var _ Node = a // still an *Aggregate, no Project inserted above or below
}

// TestStampOverwriteWidens: the finalized above-aware re-stamp overwrites
// the construction-time keys-only stamp (overwrite-only, never accumulates).
func TestAggregateStampOverwriteWidens(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"}, []Expr{gitCol("b", 1)}, nil)
	stampAggregateInputTarget(a, nil)
	if !gitEqualInts(a.InputTarget, []int{1}) {
		t.Fatalf("keys-only stamp = %v, want [1]", a.InputTarget)
	}
	above := &Project{Child: a, Targets: []Expr{gitCol("a", 0)}, schema: noSchema("a")}
	stampAggregateInputTarget(a, above)
	if !a.InputTargetKnown || !gitEqualInts(a.InputTarget, []int{0, 1}) {
		t.Fatalf("re-stamp = (%v, %v); want ([0 1], true)", a.InputTarget, a.InputTargetKnown)
	}
}

// TestSplitFinalDeclinesTarget: splitAggregate shallow-copies the Aggregate,
// so without a reset the Final would inherit a keep projected against the
// ORIGINAL input row while its child is the Gather over the Partial's output
// row. The Final must read unknown (fail-closed); the Partial reads the same
// input row and keeps the stamp.
func TestAggregateSplitFinalDeclinesTarget(t *testing.T) {
	a := gitAgg([]string{"a", "b", "c"},
		[]Expr{gitCol("b", 1)},
		[]AggregateCall{{Name: "sum", Arg: gitCol("c", 2)}})
	stampAggregateInputTarget(a, nil)
	if !a.InputTargetKnown {
		t.Fatal("construction stamp declined an enumerable aggregate")
	}
	final, ok := splitAggregate(a, 2).(*Aggregate)
	if !ok {
		t.Fatal("splitAggregate did not return an *Aggregate Final")
	}
	if final.InputTargetKnown || final.InputTarget != nil {
		t.Fatalf("Final stamp = (%v, %v); want (nil, false)", final.InputTarget, final.InputTargetKnown)
	}
	// Unknown stamps assert nothing: no panic here is the pass.
	assertAggregateInputTargetCoversKeys(final)
	if final.PartialSource == nil {
		t.Fatal("splitAggregate Final lost its PartialSource")
	}
	if !final.PartialSource.InputTargetKnown || !gitEqualInts(final.PartialSource.InputTarget, a.InputTarget) {
		t.Fatalf("Partial stamp = (%v, %v); want the original (%v, true)",
			final.PartialSource.InputTarget, final.PartialSource.InputTargetKnown, a.InputTarget)
	}
}

// TestStampNilAggIsNoOp: stamping nil never panics (defensive; the planner
// only stamps non-nil Aggregates).
func TestStampNilAggIsNoOp(t *testing.T) {
	stampAggregateInputTarget(nil, nil)
	assertAggregateInputTargetCoversKeys(nil)
}
