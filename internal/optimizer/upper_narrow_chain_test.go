package optimizer

import (
	"reflect"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// upper_narrow_chain_test.go — B-01c applying half, slice (c).
//
// Same rule as slice (b)'s tests: every "it narrows" case is paired with a
// "it refuses" case, because a pass that never fires is trivially safe and
// trivially worthless — and every claim about ORDER is checked with an
// order-sensitive oracle carrying its own vacuity guard, because the failure
// this cut can produce is out-of-order rows with a correct row count, which no
// row-count gate and no sort-then-compare values gate can see
// (`internal/executor/operators.go`:1010-1015, ledger row D-06).
//
// The chain walk adds one failure mode slice (b) did not have: a refusal
// discovered at ancestor N must leave ancestors N-1..1 UNTOUCHED, because a
// half-rebased chain is a wrong column read at every level below the refusal.
// `TestNarrowSortInputLeavesNoPartialRewrite` is that pin.

// ---------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------

// uncSortTree builds `Project -> Sort -> 6-column input`: the general ORDER BY
// shape this slice exists for, with the slice-(a) multi-key ASC/DESC/NULLS
// ordering and a keep of the three columns the sort and the projection read.
//
// It returns the root, the Sort, the keep and the fixture's rows/keys so the
// order oracle can be run against both the wide and the narrowed row.
func uncSortTree() (*Project, *Sort, []int, [][]int64, []SortKey) {
	rows, keys, names := ungOrderFixture()
	srt := &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
	// The projection selects b, d and e — the same three the ordering reads,
	// so the keep is exactly {1,3,4} and a, c, f are dead weight in the sort.
	proj := &Project{
		Child:   srt,
		Targets: []Expr{ungCol("b", 1), ungCol("d", 3), ungCol("e", 4)},
		schema:  noSchema("b", "d", "e"),
	}
	keep := []int{1, 3, 4}
	srt.InputTarget, srt.InputTargetKnown = keep, true
	return proj, srt, keep, rows, keys
}

// uncApply drives the production entry point over a hand-built tree, so the
// tests exercise the pass's own descent and ancestor bookkeeping rather than
// calling `narrowSortInput` with a chain the test invented.
func uncApply(root Node) {
	applyUpperSortNarrowing(root, upperNarrowRefCounts(root))
}

// uncColIndices reads the (name -> index) pairs an expression list reads.
func uncColIndices(es []Expr) map[string]int {
	out := map[string]int{}
	for _, e := range es {
		walkExprRefs(e, scopeVeto, exprVisitor{Visit: func(n Expr) bool {
			if c, ok := n.(*ColumnRef); ok {
				out[c.Name] = c.Index
			}
			return true
		}})
	}
	return out
}

// ---------------------------------------------------------------------
// It fires, and the absorber absorbs
// ---------------------------------------------------------------------

// TestApplyUpperSortNarrowingCutsUnderAProjectAbsorber is the whole point of
// the slice: the general ORDER BY sort now carries a narrowed row, and the
// `*Project` above it absorbs the coordinate change so the ROOT ROW — the
// query's answer — is untouched.
func TestApplyUpperSortNarrowingCutsUnderAProjectAbsorber(t *testing.T) {
	proj, srt, _, _, _ := uncSortTree()
	wantOut := proj.Output()

	uncApply(proj)

	narrow, ok := srt.Child.(*Project)
	if !ok {
		t.Fatalf("the cut did not land under the Sort (child is %T)", srt.Child)
	}
	if len(narrow.Targets) != 3 {
		t.Fatalf("narrowing Project keeps %d columns, want 3", len(narrow.Targets))
	}
	// The Sort now compares a three-column row.
	if idx := uncColIndices(sortKeyExprs(srt.Keys)); idx["b"] != 0 || idx["d"] != 1 || idx["e"] != 2 {
		t.Fatalf("sort keys were not re-based onto the narrowed row: %v", idx)
	}
	// The absorber was re-based too — and its OUTPUT did not move.
	if idx := uncColIndices(proj.Targets); idx["b"] != 0 || idx["d"] != 1 || idx["e"] != 2 {
		t.Fatalf("the absorbing Project's targets were not re-based: %v", idx)
	}
	if got := proj.Output(); !reflect.DeepEqual(got, wantOut) {
		t.Fatalf("the root row moved: %v, want %v", got, wantOut)
	}
	// The stamp described pre-cut positions and must not be reusable.
	if srt.InputTargetKnown {
		t.Fatal("the Sort's stamp survived the cut — a second pass would re-cut an already-narrowed row")
	}
}

// TestApplyUpperSortNarrowingAbsorbsAtAnAggregate: the other absorber. An
// Aggregate above the Sort rebuilds its own output from its own expression
// lists, so it stops the cut exactly as a Project does.
func TestApplyUpperSortNarrowingAbsorbsAtAnAggregate(t *testing.T) {
	rows, keys, names := ungOrderFixture()
	_ = rows
	srt := &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
	srt.InputTarget, srt.InputTargetKnown = []int{1, 3, 4}, true
	agg := &Aggregate{
		Child:      srt,
		Strategy:   AggStrategySorted,
		GroupExprs: []Expr{ungCol("b", 1), ungCol("d", 3)},
		Aggs:       []AggregateCall{{Name: "sum", Arg: ungCol("e", 4)}},
	}
	// No stamp on the Aggregate: this test is about the CHAIN, not about
	// slice (b)'s site firing first.
	uncApply(agg)

	if _, ok := srt.Child.(*Project); !ok {
		t.Fatalf("the cut did not land under the Sort (child is %T)", srt.Child)
	}
	if idx := uncColIndices(agg.GroupExprs); idx["b"] != 0 || idx["d"] != 1 {
		t.Fatalf("the absorbing Aggregate's group keys were not re-based: %v", idx)
	}
	if a := agg.Aggs[0].Arg.(*ColumnRef); a.Index != 2 {
		t.Fatalf("the absorbing Aggregate's arg was not re-based: %+v", a)
	}
}

// TestApplyUpperSortNarrowingPropagatesThroughFilterLimitSort walks all three
// PROPAGATE kinds in one chain and proves each was re-based — including
// `*Limit.TiesKeys`, the field `enclosingNodeScopeOf`'s Limit arm does not
// enumerate and which the name-derived keep therefore never saw.
func TestApplyUpperSortNarrowingPropagatesThroughFilterLimitSort(t *testing.T) {
	rows, keys, names := ungOrderFixture()
	_ = rows
	srt := &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
	srt.InputTarget, srt.InputTargetKnown = []int{1, 3, 4}, true
	flt := &Filter{Child: srt, Predicate: &BinaryOp{Op: parser.OpGt, Left: ungCol("e", 4), Right: &IntegerConst{Value: 1}}}
	lim := &Limit{Child: flt, Limit: &IntegerConst{Value: 3}, WithTies: true, TiesKeys: []Expr{ungCol("b", 1)}}
	outer := &Sort{Child: lim, Keys: []SortKey{{Expr: ungCol("d", 3), Desc: true}}}
	root := &Project{Child: outer, Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}

	uncApply(root)

	if _, ok := srt.Child.(*Project); !ok {
		t.Fatalf("the cut did not land under the inner Sort (child is %T)", srt.Child)
	}
	if idx := uncColIndices([]Expr{flt.Predicate}); idx["e"] != 2 {
		t.Fatalf("the Filter's predicate was not re-based: %v", idx)
	}
	if idx := uncColIndices(lim.TiesKeys); idx["b"] != 0 {
		t.Fatalf("Limit.TiesKeys was not re-based: %v — this is the field the name-level derivation cannot see", idx)
	}
	if idx := uncColIndices(sortKeyExprs(outer.Keys)); idx["d"] != 1 {
		t.Fatalf("the outer Sort's keys were not re-based: %v", idx)
	}
	if idx := uncColIndices(root.Targets); idx["b"] != 0 {
		t.Fatalf("the absorbing Project's targets were not re-based: %v", idx)
	}
	if outer.InputTargetKnown {
		t.Fatal("the propagating Sort's stamp counted pre-cut positions and must have been cleared")
	}
}

// ---------------------------------------------------------------------
// The ORDER oracle — the load-bearing check
// ---------------------------------------------------------------------

// TestApplyUpperSortNarrowingPreservesEmittedOrder: after the cut, the Sort's
// rewritten key list read against the NARROWED rows induces exactly the
// ordering the original key list induced against the wide rows.
func TestApplyUpperSortNarrowingPreservesEmittedOrder(t *testing.T) {
	proj, srt, keep, rows, keys := uncSortTree()
	want := ungPermutation(rows, keys)
	if ungSameOrder(want, []int{0, 1, 2, 3, 4, 5, 6}) {
		t.Fatal("fixture ordering equals input order — the oracle would pass without comparing anything")
	}

	uncApply(proj)
	if _, ok := srt.Child.(*Project); !ok {
		t.Fatalf("the cut did not land under the Sort (child is %T)", srt.Child)
	}
	got := ungPermutation(ungNarrowRows(rows, keep), srt.Keys)
	if !ungSameOrder(want, got) {
		t.Fatalf("the applied cut changed the emitted order: wide %v, narrowed %v", want, got)
	}
}

// TestApplyUpperSortNarrowingOrderOracleIsNotVacuous is the vacuity guard: a
// deliberately mis-based key list — every index shifted by one, the exact
// off-by-one a hand-written re-base produces — must make the oracle FAIL.
// Without this the passing case could be comparing nothing.
func TestApplyUpperSortNarrowingOrderOracleIsNotVacuous(t *testing.T) {
	proj, srt, keep, rows, keys := uncSortTree()
	uncApply(proj)
	bad := make([]SortKey, len(srt.Keys))
	for i, k := range srt.Keys {
		c := k.Expr.(*ColumnRef)
		bad[i] = SortKey{
			Expr:       &ColumnRef{Index: (c.Index + 1) % len(keep), Name: c.Name},
			Desc:       k.Desc,
			NullsFirst: k.NullsFirst,
		}
	}
	want := ungPermutation(rows, keys)
	got := ungPermutation(ungNarrowRows(rows, keep), bad)
	if ungSameOrder(want, got) {
		t.Fatal("the ordering oracle did not notice a one-column mis-base — it proves nothing about the passing case")
	}
}

// ---------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------

func TestNarrowSortInputRefuses(t *testing.T) {
	cases := []struct {
		name string
		make func() (Node, *Sort)
	}{
		{"an unknown stamp", func() (Node, *Sort) {
			proj, srt, _, _, _ := uncSortTree()
			srt.InputTarget, srt.InputTargetKnown = nil, false
			return proj, srt
		}},
		{"an identity keep", func() (Node, *Sort) {
			proj, srt, _, _, _ := uncSortTree()
			srt.InputTarget = []int{0, 1, 2, 3, 4, 5}
			return proj, srt
		}},
		{"a malformed (descending) keep", func() (Node, *Sort) {
			proj, srt, _, _, _ := uncSortTree()
			srt.InputTarget = []int{4, 3, 1}
			return proj, srt
		}},
		{"a keep that drops a sort-key column", func() (Node, *Sort) {
			proj, srt, _, _, _ := uncSortTree()
			srt.InputTarget = []int{1, 3} // e (key 3) dropped
			return proj, srt
		}},
		{"a keep that drops a column the ABSORBER reads", func() (Node, *Sort) {
			// The keys survive, so slice (a)'s node gate PASSES; only the
			// positional re-verification of the ancestor catches it. This is
			// the exact shape a keys-only construction-time stamp produces.
			proj, srt, _, _, _ := uncSortTree()
			proj.Targets = append(proj.Targets, ungCol("a", 0))
			proj.schema = noSchema("b", "d", "e", "a")
			return proj, srt
		}},
		{"a chain that never absorbs before the root", func() (Node, *Sort) {
			_, srt, _, _, _ := uncSortTree()
			// Filter propagates; nothing above it absorbs, so the narrowed row
			// would BE the query's answer.
			return &Filter{Child: srt, Predicate: &BinaryOp{
				Op: parser.OpGt, Left: ungCol("e", 4), Right: &IntegerConst{Value: 1},
			}}, srt
		}},
		{"the Sort as the plan root", func() (Node, *Sort) {
			_, srt, _, _, _ := uncSortTree()
			return srt, srt
		}},
		{"a Distinct ancestor (it compares the WHOLE row)", func() (Node, *Sort) {
			_, srt, _, _, _ := uncSortTree()
			d := &Distinct{Child: srt, schema: srt.Child.Output()}
			return &Project{Child: d, Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}, srt
		}},
		{"a Gather ancestor (a parallel boundary)", func() (Node, *Sort) {
			_, srt, _, _, _ := uncSortTree()
			g := &Gather{Child: srt}
			return &Project{Child: g, Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}, srt
		}},
		{"an IsolatedScope Project absorber", func() (Node, *Sort) {
			proj, srt, _, _, _ := uncSortTree()
			proj.IsolatedScope = true
			return proj, srt
		}},
		{"a split (non-Simple) Aggregate absorber", func() (Node, *Sort) {
			rows, keys, names := ungOrderFixture()
			_ = rows
			s := &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
			s.InputTarget, s.InputTargetKnown = []int{1, 3, 4}, true
			return &Aggregate{
				Child: s, Mode: AggModePartial, Strategy: AggStrategySorted,
				GroupExprs: []Expr{ungCol("b", 1)},
				Aggs:       []AggregateCall{{Name: "sum", Arg: ungCol("e", 4)}},
			}, s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, srt := tc.make()
			before := srt.Child
			beforeKeys := append([]SortKey(nil), srt.Keys...)
			uncApply(root)
			if srt.Child != before {
				t.Fatalf("the pass narrowed a site it must refuse: Sort.Child is now %T", srt.Child)
			}
			if !reflect.DeepEqual(srt.Keys, beforeKeys) {
				t.Fatal("the pass rewrote the sort keys of a site it must refuse")
			}
		})
	}
}

// TestNarrowSortInputRefusesASharedSort is the DAG guard. A Sort reachable
// from two parents has two ancestor chains; re-basing one leaves the other
// reading pre-cut positions — a wrong column with a correct row count.
func TestNarrowSortInputRefusesASharedSort(t *testing.T) {
	_, srt, _, _, _ := uncSortTree()
	left := &Project{Child: srt, Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}
	right := &Project{Child: srt, Targets: []Expr{ungCol("d", 3)}, schema: noSchema("d")}
	root := &Join{Left: left, Right: right}

	before := srt.Child
	uncApply(root)
	if srt.Child != before {
		t.Fatalf("the pass narrowed a Sort with two parents (child is now %T) — one of the two chains would read pre-cut positions", srt.Child)
	}
	if idx := uncColIndices(left.Targets); idx["b"] != 1 {
		t.Fatalf("a chain above a shared Sort was rewritten anyway: %v", idx)
	}
	if idx := uncColIndices(right.Targets); idx["d"] != 3 {
		t.Fatalf("a chain above a shared Sort was rewritten anyway: %v", idx)
	}
}

// TestNarrowSortInputRefusesASharedSinkWrapper is the DAG guard on the other
// side of the narrowing site. The chain walk checks the ancestors; the SINK
// rewrites wrappers BELOW the site (a `*Filter`'s predicate, a `*Sort`'s keys),
// and one of those reachable from a second parent would be left reading pre-cut
// positions on the other path.
func TestNarrowSortInputRefusesASharedSinkWrapper(t *testing.T) {
	rows, keys, names := ungOrderFixture()
	_ = rows
	shared := &Filter{
		Child:     &noNode{sch: noSchema(names...)},
		Predicate: &BinaryOp{Op: parser.OpGt, Left: ungCol("e", 4), Right: &IntegerConst{Value: 1}},
	}
	srt := &Sort{Child: shared, Keys: keys}
	srt.InputTarget, srt.InputTargetKnown = []int{1, 3, 4}, true
	mine := &Project{Child: srt, Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}
	// A second consumer of the very Filter the sink would rewrite.
	other := &Project{Child: shared, Targets: []Expr{ungCol("a", 0)}, schema: noSchema("a")}
	root := &Join{Left: mine, Right: other}

	before := srt.Child
	uncApply(root)
	if srt.Child != before {
		t.Fatalf("the pass sank past a Filter with two parents (Sort.Child is now %T)", srt.Child)
	}
	if idx := uncColIndices([]Expr{shared.Predicate}); idx["e"] != 4 {
		t.Fatalf("a shared Filter was rewritten anyway: %v — its other consumer still reads the wide row", idx)
	}
}

// TestNarrowSortInputLeavesNoPartialRewrite is slice (c)'s counterpart to
// slice (b)'s `TestNarrowAggregateInputLeavesNoPartialRewrite`, and it pins
// the failure mode the chain walk adds: a refusal discovered HIGH on the chain
// must leave every ancestor below it untouched.
//
// Here the Filter and the inner Sort would both re-base cleanly, and the
// refusal comes from the Distinct above them. If the walk mutated as it
// climbed, the Filter and the outer Sort would be left reading the narrowed
// row while the Sort below them still emits the wide one — which, one column
// to the left, is a silently wrong column rather than an error.
func TestNarrowSortInputLeavesNoPartialRewrite(t *testing.T) {
	rows, keys, names := ungOrderFixture()
	_ = rows
	srt := &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
	srt.InputTarget, srt.InputTargetKnown = []int{1, 3, 4}, true
	flt := &Filter{Child: srt, Predicate: &BinaryOp{Op: parser.OpGt, Left: ungCol("e", 4), Right: &IntegerConst{Value: 1}}}
	outer := &Sort{Child: flt, Keys: []SortKey{{Expr: ungCol("d", 3), Desc: true}}}
	dist := &Distinct{Child: outer, schema: srt.Child.Output()}
	root := &Project{Child: dist, Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}

	childBefore := srt.Child
	uncApply(root)

	if srt.Child != childBefore {
		t.Fatalf("a refused cut still spliced the narrowing Project (child is %T)", srt.Child)
	}
	if idx := uncColIndices(sortKeyExprs(srt.Keys)); idx["b"] != 1 || idx["d"] != 3 || idx["e"] != 4 {
		t.Fatalf("a refused cut rewrote the narrowing site's own keys: %v", idx)
	}
	if idx := uncColIndices([]Expr{flt.Predicate}); idx["e"] != 4 {
		t.Fatalf("a refused cut left the Filter rewritten: %v", idx)
	}
	if idx := uncColIndices(sortKeyExprs(outer.Keys)); idx["d"] != 3 {
		t.Fatalf("a refused cut left the outer Sort rewritten: %v", idx)
	}
	if !srt.InputTargetKnown {
		t.Fatal("a refused cut cleared the stamp")
	}
}

// TestApplyUpperSortNarrowingFlagOff: `GOOPG_NARROW_UPPER_SORT=0` reproduces
// the pre-slice-(c) tree bit-identically, and so does `GOOPG_NARROW_UPPER=0`.
// The flag exists so a gate can measure the FLAG rather than the commit; a
// flag that does not fully restore the old arm cannot do that.
func TestApplyUpperSortNarrowingFlagOff(t *testing.T) {
	for _, tc := range []struct{ name, upper, upperSort string }{
		{"GOOPG_NARROW_UPPER_SORT=0", "", "0"},
		{"GOOPG_NARROW_UPPER=0", "0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(u, us bool) { narrowUpper, narrowUpperSort = u, us }(narrowUpper, narrowUpperSort)
			narrowUpper = narrowUpperFromEnv(tc.upper)
			narrowUpperSort = narrowUpperSortFromEnv(tc.upperSort)

			proj, srt, _, _, _ := uncSortTree()
			before := srt.Child
			uncApply(proj)
			if srt.Child != before {
				t.Fatalf("the pass fired with the flag off (child is %T)", srt.Child)
			}
			if idx := uncColIndices(proj.Targets); idx["b"] != 1 {
				t.Fatalf("the pass rewrote an ancestor with the flag off: %v", idx)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The ancestor-class inventory — the fail-closed pin
// ---------------------------------------------------------------------

// TestAncestorActionInventory pins the CLASSIFICATION, not just the code.
//
// `planRewriteAncestor`'s switch is a positive enumeration whose default is
// REFUSE, so a node kind added to goopg tomorrow is safe by construction. What
// is NOT safe by construction is someone moving an existing kind between
// classes — promoting `*Distinct` to PROPAGATE because "its Output is its
// child's" (it is not; and even if it were, narrowing changes which rows are
// distinct) would be a silent wrong answer with a plausible diff. So the
// class of every kind this pass can meet is written down here, and moving one
// has to be done deliberately, in this table, with the reason.
func TestAncestorActionInventory(t *testing.T) {
	six := noSchema("a", "b", "c", "d", "e", "f")
	child := func() Node { return &noNode{sch: six} }
	km, ok := newKeepMap([]int{1, 3, 4}, 6)
	if !ok {
		t.Fatal("fixture keep is malformed")
	}

	cases := []struct {
		name string
		node Node
		want ancestorAction
	}{
		{"Project absorbs — its schema pairs with its own targets",
			&Project{Child: child(), Targets: []Expr{ungCol("b", 1)}, schema: noSchema("b")}, ancestorAbsorb},
		{"Aggregate absorbs — its output is its own expression lists",
			&Aggregate{Child: child(), GroupExprs: []Expr{ungCol("b", 1)}}, ancestorAbsorb},
		{"Filter propagates — Output() is Child.Output()",
			&Filter{Child: child(), Predicate: ungCol("e", 4)}, ancestorPropagate},
		{"Sort propagates — Output() is Child.Output()",
			&Sort{Child: child(), Keys: []SortKey{{Expr: ungCol("d", 3)}}}, ancestorPropagate},
		{"Limit propagates — Output() is Child.Output()",
			&Limit{Child: child(), Limit: &IntegerConst{Value: 1}}, ancestorPropagate},
		{"Distinct refuses — it compares the WHOLE row",
			&Distinct{Child: child(), schema: six}, ancestorRefuse},
		{"DistinctOn refuses — KeyCols index an output whose width would move",
			&DistinctOn{Child: child(), KeyCols: []int{1}, schema: six}, ancestorRefuse},
		{"WindowAgg refuses — it publishes child row ++ func outputs",
			&WindowAgg{Child: child(), schema: six}, ancestorRefuse},
		{"Gather refuses — a parallel boundary",
			&Gather{Child: child()}, ancestorRefuse},
		{"GatherMerge refuses — a parallel boundary with its own key list",
			&GatherMerge{Child: child()}, ancestorRefuse},
		{"Join refuses — it publishes a merged Left ++ Right row",
			&Join{Left: child(), Right: child()}, ancestorRefuse},
		{"NestedLoopIndexJoin refuses — same merged row, plus a parameterised probe",
			&NestedLoopIndexJoin{Outer: child()}, ancestorRefuse},
		{"Result refuses — it publishes a STORED schema it cannot re-derive",
			&Result{Child: child(), schema: six}, ancestorRefuse},
		{"CTEScan refuses — a label node whose stored schema would go stale",
			&CTEScan{Child: child(), schema: six}, ancestorRefuse},
		{"Memoize refuses — it caches rows keyed by a probe this pass does not model",
			&Memoize{}, ancestorRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			act, apply, ok := planRewriteAncestor(tc.node, km)
			if act != tc.want {
				t.Fatalf("class moved: got %v, pinned %v", act, tc.want)
			}
			if tc.want == ancestorRefuse {
				if ok || apply != nil {
					t.Fatal("a REFUSE class returned an install closure")
				}
				return
			}
			if !ok || apply == nil {
				t.Fatal("a non-REFUSE class returned no install closure")
			}
		})
	}
}

// TestUpperNarrowChainNodeFieldInventory extends slice (b)'s reflection pin to
// the two node types only the chain walk rewrites.
//
// `remapExprIndices` is exhaustive over the `Expr` TYPE set by construction.
// The NODE-FIELD side is not: a new `Expr`-bearing field on `*Project` or
// `*Limit` would be left silently in PRE-CUT coordinates by the chain walk,
// which is a wrong column read with a correct row count. Adding a field fails
// this test, and the fix is to rewrite it (or to refuse the kind), never to
// extend the list without doing one of the two.
//
// `*Sort`, `*Filter` and `*Aggregate` are pinned by
// `TestUpperNarrowApplyNodeFieldInventory` (upper_narrow_apply_test.go) and the
// chain walk rewrites exactly the fields that pin names, through the same
// helpers, so they are not re-pinned here.
func TestUpperNarrowChainNodeFieldInventory(t *testing.T) {
	want := map[string][]string{
		// Rewritten by planRewriteAncestor's *Project arm.
		"Project": {"Targets"},
		// Rewritten by planRewriteAncestor's *Limit arm. TiesKeys is the one
		// `enclosingNodeScopeOf`'s Limit arm does NOT enumerate, so the
		// name-derived keep never saw it and only the positional rewrite here
		// keeps WITH TIES honest.
		"Limit": {"Limit", "Offset", "TiesKeys"},
	}
	types := map[string]reflect.Type{
		"Project": reflect.TypeOf(Project{}),
		"Limit":   reflect.TypeOf(Limit{}),
	}
	exprIface := reflect.TypeOf((*Expr)(nil)).Elem()
	sortKey := reflect.TypeOf(SortKey{})

	var readsRow func(reflect.Type) bool
	readsRow = func(ft reflect.Type) bool {
		switch ft.Kind() {
		case reflect.Interface:
			return ft == exprIface
		case reflect.Struct:
			return ft == sortKey
		case reflect.Slice, reflect.Array:
			return readsRow(ft.Elem())
		}
		return false
	}

	for name, rt := range types {
		var got []string
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if readsRow(f.Type) {
				got = append(got, f.Name)
			}
		}
		sort.Strings(got)
		w := append([]string(nil), want[name]...)
		sort.Strings(w)
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("%s's expression-bearing field set moved: have %v, pinned %v.\n"+
				"A new field here is read against the PRE-CUT row unless upper_narrow_chain.go "+
				"rewrites it (or refuses the kind). Extending this list without doing one of "+
				"those two is the wrong fix.", name, got, w)
		}
	}
}
