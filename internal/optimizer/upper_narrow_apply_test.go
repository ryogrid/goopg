package optimizer

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// upper_narrow_apply_test.go — B-01c applying half, slice (b).
//
// Slice (a)'s tests proved the GATE. These prove the APPLIER, and they are
// written to the same rule: every "it narrows" case is paired with a "it
// refuses" case, because a pass that never fires is trivially safe and
// trivially worthless — and every claim about ORDER is checked with an
// order-sensitive oracle carrying its own vacuity guard, because the failure
// this cut can produce is out-of-order rows with a correct row count, which
// no row-count gate and no sort-then-compare values gate can see
// (`internal/executor/operators.go`:1010-1015, ledger row D-06).

// ---------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------

// unaWideCatalog is one six-column table. Six columns and two group keys is
// the shape the cut exists for: the sort beneath a sorted aggregation
// materialises every input row, and four of the six are dead weight in it.
func unaWideCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	cols := make([]catalog.Column, 0, 6)
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		cols = append(cols, catalog.Column{Name: n, Type: catalog.Type{Name: "int4"}})
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "wide"}, cols); err != nil {
		t.Fatal(err)
	}
	return c
}

// unaFindSort returns the first *Sort under n, and unaFindAggregate the first
// *Aggregate, both by the pass's own descent so a shape the pass cannot reach
// is not silently "found" by a test walking further than the pass does.
func unaFind[T Node](n Node) (T, bool) {
	var zero T
	if n == nil {
		return zero, false
	}
	if hit, ok := n.(T); ok {
		return hit, true
	}
	for _, c := range upperNarrowChildren(n) {
		if hit, ok := unaFind[T](c); ok {
			return hit, true
		}
	}
	return zero, false
}

// unaColIndices reads the (name -> index) pairs an expression list reads.
func unaColIndices(es []Expr) map[string]int {
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
// It fires — and where
// ---------------------------------------------------------------------

// TestApplyUpperNarrowingSinksBelowSortedAggregateSort is the whole point of
// the slice: on a sorted aggregation the narrowing Project ends up BELOW the
// Sort, so the Sort materialises, compares and spills the narrow row.
func TestApplyUpperNarrowingSinksBelowSortedAggregateSort(t *testing.T) {
	// count(DISTINCT b) forces the sorted strategy, so the plan carries the
	// Sort this cut aims at.
	node, err := Plan(parseOne(t, "SELECT a, count(DISTINCT b) FROM wide GROUP BY a"), unaWideCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	agg, ok := unaFind[*Aggregate](node)
	if !ok {
		t.Fatalf("no Aggregate in the plan:\n%s", unaDump(node, 0))
	}
	srt, ok := agg.Child.(*Sort)
	if !ok {
		t.Skipf("this shape no longer plans as Aggregate over Sort (%T) — the cut's target site moved", agg.Child)
	}
	proj, ok := srt.Child.(*Project)
	if !ok {
		t.Fatalf("no narrowing Project below the Sort:\n%s", unaDump(node, 0))
	}
	if len(proj.Output()) >= len(proj.Child.Output()) {
		t.Fatalf("the Project below the Sort narrows nothing: %d of %d columns",
			len(proj.Output()), len(proj.Child.Output()))
	}
	// The stamp is consumed, not left describing pre-cut positions.
	if agg.InputTargetKnown {
		t.Fatal("the applied Aggregate still advertises a stamped input target in pre-cut coordinates")
	}
	if srt.InputTargetKnown {
		t.Fatal("the Sort the cut was sunk past still advertises a stamped input target in pre-cut coordinates")
	}
	// Every surviving reference is inside the narrowed row.
	w := len(proj.Output())
	for name, idx := range unaColIndices(agg.GroupExprs) {
		if idx < 0 || idx >= w {
			t.Fatalf("group key %q reads position %d of a %d-column row", name, idx, w)
		}
	}
	for _, k := range srt.Keys {
		c, ok := k.Expr.(*ColumnRef)
		if !ok {
			continue
		}
		if c.Index < 0 || c.Index >= w {
			t.Fatalf("sort key %q reads position %d of a %d-column row", c.Name, c.Index, w)
		}
	}
}

// TestApplyUpperNarrowingDeclinesHashAggregate states the cut's retention-site
// condition as a behaviour: above a HASH aggregate a narrowing Project is a
// per-row cost that saves nothing (transition states are retained, rows are
// not), so the plan must come out BIT-IDENTICAL — no Project, and the stamp
// left untouched for the asserts and a future cut.
func TestApplyUpperNarrowingDeclinesHashAggregate(t *testing.T) {
	node, err := Plan(parseOne(t, "SELECT a, sum(b) FROM wide GROUP BY a"), unaWideCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	agg, ok := unaFind[*Aggregate](node)
	if !ok {
		t.Fatalf("no Aggregate in the plan:\n%s", unaDump(node, 0))
	}
	if agg.Strategy != AggStrategyHashed {
		t.Skipf("this shape no longer plans as a hash aggregate (%v)", agg.Strategy)
	}
	if _, isProj := agg.Child.(*Project); isProj {
		t.Fatalf("a narrowing Project was inserted under a hash aggregate — pure per-row cost, zero bytes retained:\n%s", unaDump(node, 0))
	}
	if !agg.InputTargetKnown {
		t.Fatal("the declined site's stamp was consumed anyway")
	}
}

// TestApplyUpperNarrowingFlagOff: `GOOPG_NARROW_UPPER=0` reproduces the
// pre-flip tree exactly, so a gate can measure the FLAG rather than the
// commit.
func TestApplyUpperNarrowingFlagOff(t *testing.T) {
	if !narrowUpperFromEnv("") {
		t.Fatal("the flag's default is off; this test and the wiring assume on")
	}
	if narrowUpperFromEnv("0") {
		t.Fatal("`=0` did not opt out")
	}
	saved := narrowUpper
	narrowUpper = false
	defer func() { narrowUpper = saved }()

	node, err := Plan(parseOne(t, "SELECT a, count(DISTINCT b) FROM wide GROUP BY a"), unaWideCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	agg, ok := unaFind[*Aggregate](node)
	if !ok {
		t.Fatal("no Aggregate in the plan")
	}
	if !agg.InputTargetKnown {
		t.Fatal("the pass ran with the flag off")
	}
	if srt, ok := agg.Child.(*Sort); ok {
		if _, isProj := srt.Child.(*Project); isProj {
			t.Fatal("a narrowing Project was inserted with the flag off")
		}
	}
}

func unaDump(n Node, d int) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", d))
	b.WriteString(reflect.TypeOf(n).String())
	b.WriteString("\n")
	for _, c := range upperNarrowChildren(n) {
		b.WriteString(unaDump(c, d+1))
	}
	return b.String()
}

// ---------------------------------------------------------------------
// The ORDER oracle — the load-bearing check
// ---------------------------------------------------------------------

// unaSortedAggTree builds Aggregate(sorted) -> Sort -> 6-column input, with
// the multi-key ASC/DESC/NULLS ordering of the slice-(a) fixture, an
// Aggregate that groups on `b` and sums `e`, and a stamped keep of the three
// columns those two reads and the ordering need.
func unaSortedAggTree() (*Aggregate, *Sort, []int, [][]int64, []SortKey) {
	rows, keys, names := ungOrderFixture()
	srt := &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
	agg := &Aggregate{
		Child:      srt,
		Strategy:   AggStrategySorted,
		GroupExprs: []Expr{ungCol("b", 1), ungCol("d", 3)},
		Aggs:       []AggregateCall{{Name: "sum", Arg: ungCol("e", 4)}},
	}
	keep := []int{1, 3, 4}
	agg.InputTarget, agg.InputTargetKnown = keep, true
	return agg, srt, keep, rows, keys
}

// TestApplyUpperNarrowingPreservesEmittedOrder: after the cut, the Sort's
// rewritten key list read against the NARROWED rows induces exactly the
// ordering the original key list induced against the wide rows.
//
// This is the check the gate exists for, applied to the tree the APPLIER
// actually produced rather than to a keep-list in isolation.
func TestApplyUpperNarrowingPreservesEmittedOrder(t *testing.T) {
	agg, srt, keep, rows, keys := unaSortedAggTree()
	want := ungPermutation(rows, keys)
	if ungSameOrder(want, []int{0, 1, 2, 3, 4, 5, 6}) {
		t.Fatal("fixture ordering equals input order — the oracle would pass without comparing anything")
	}

	if !narrowAggregateInput(agg) {
		t.Fatal("the applier refused a sorted-aggregate site whose keep covers every key it reads")
	}
	if _, isProj := srt.Child.(*Project); !isProj {
		t.Fatalf("the cut did not land below the Sort (child is %T)", srt.Child)
	}
	got := ungPermutation(ungNarrowRows(rows, keep), srt.Keys)
	if !ungSameOrder(want, got) {
		t.Fatalf("the applied cut changed the emitted order: wide %v, narrowed %v", want, got)
	}

	// The aggregate now reads the narrowed row, and reads the SAME columns.
	idx := unaColIndices(agg.GroupExprs)
	if idx["b"] != 0 || idx["d"] != 1 {
		t.Fatalf("group keys were not re-based onto the narrowed row: %v", idx)
	}
	if a := agg.Aggs[0].Arg.(*ColumnRef); a.Index != 2 || a.Name != "e" {
		t.Fatalf("aggregate arg was not re-based: %+v", a)
	}
}

// TestApplyUpperNarrowingOrderOracleIsNotVacuous is the vacuity guard for the
// test above: a deliberately mis-based key list — every index shifted by one,
// the exact off-by-one a hand-written re-base produces — must make the oracle
// FAIL. Without this the passing case could be comparing nothing.
func TestApplyUpperNarrowingOrderOracleIsNotVacuous(t *testing.T) {
	agg, srt, keep, rows, keys := unaSortedAggTree()
	if !narrowAggregateInput(agg) {
		t.Fatal("the applier refused the fixture site")
	}
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

func TestNarrowAggregateInputRefuses(t *testing.T) {
	cases := []struct {
		name string
		make func() *Aggregate
	}{
		{"an unknown stamp", func() *Aggregate {
			agg, _, _, _, _ := unaSortedAggTree()
			agg.InputTarget, agg.InputTargetKnown = nil, false
			return agg
		}},
		{"an identity keep", func() *Aggregate {
			agg, _, _, _, _ := unaSortedAggTree()
			agg.InputTarget = []int{0, 1, 2, 3, 4, 5}
			return agg
		}},
		{"a malformed (descending) keep", func() *Aggregate {
			agg, _, _, _, _ := unaSortedAggTree()
			agg.InputTarget = []int{4, 1}
			return agg
		}},
		{"a split (non-simple) aggregate", func() *Aggregate {
			agg, _, _, _, _ := unaSortedAggTree()
			agg.Mode = AggModeFinal
			return agg
		}},
		{"a keep that drops a group key", func() *Aggregate {
			agg, _, _, _, _ := unaSortedAggTree()
			agg.InputTarget = []int{1, 4} // drops d, a group key
			return agg
		}},
		{"a keep that drops a sort key of the Sort below", func() *Aggregate {
			// The keep covers everything the AGGREGATE reads, so only the
			// sink's re-proof of the Sort's own key list can catch it.
			agg, srt, _, _, _ := unaSortedAggTree()
			agg.GroupExprs = []Expr{ungCol("b", 1)}
			agg.Aggs = nil
			agg.InputTarget = []int{1}
			_ = srt
			return agg
		}},
		{"no retention site below (hash-shaped: input is not a Sort)", func() *Aggregate {
			agg, srt, _, _, _ := unaSortedAggTree()
			agg.Child = srt.Child
			agg.Strategy = AggStrategyHashed
			return agg
		}},
		{"a nil child", func() *Aggregate {
			agg, _, _, _, _ := unaSortedAggTree()
			agg.Child = nil
			return agg
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := tc.make()
			before := unaDump(agg, 0)
			if narrowAggregateInput(agg) {
				t.Fatalf("the applier narrowed %s", tc.name)
			}
			if after := unaDump(agg, 0); after != before {
				t.Fatalf("a refusal changed the tree:\n--- before ---\n%s--- after ---\n%s", before, after)
			}
		})
	}
}

// TestNarrowAggregateInputLeavesNoPartialRewrite is the regression test for
// the bug this slice shipped once and its own executor caught: the sink used
// to mutate each wrapper as it descended, so a refusal at the LATER
// retention-site test left the Filter rewritten and the Project spliced in
// while the Aggregate above still read pre-cut positions
// (`SELECT grp, sum(v) ... WHERE v > 300 GROUP BY grp` failed as "column ref
// v/2 out of MaterializedSlot range 2"; one column further left it would have
// been a silently wrong column).
//
// The shape here refuses for the retention-site reason — a hash-shaped site
// with no Sort below — AFTER the sink has planned a full descent past a
// Filter it could feed. Nothing may have moved.
func TestNarrowAggregateInputLeavesNoPartialRewrite(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	leaf := &noNode{sch: noSchema(names...)}
	flt := &Filter{Child: leaf, Predicate: &BinaryOp{Op: parser.OpGt, Left: ungCol("e", 4), Right: &IntegerConst{Value: 1}}}
	agg := &Aggregate{
		Child:      flt,
		Strategy:   AggStrategyHashed,
		GroupExprs: []Expr{ungCol("b", 1)},
		Aggs:       []AggregateCall{{Name: "sum", Arg: ungCol("e", 4)}},
	}
	agg.InputTarget, agg.InputTargetKnown = []int{1, 4}, true

	if narrowAggregateInput(agg) {
		t.Fatal("the applier narrowed a site with no retention point below it")
	}
	if agg.Child != Node(flt) {
		t.Fatalf("a refusal re-parented the Aggregate (child is %T)", agg.Child)
	}
	if flt.Child != Node(leaf) {
		t.Fatalf("a refusal spliced a Project under the Filter (child is %T)", flt.Child)
	}
	if got := flt.Predicate.(*BinaryOp).Left.(*ColumnRef).Index; got != 4 {
		t.Fatalf("a refusal re-based the Filter predicate: index %d, want 4", got)
	}
	if got := agg.GroupExprs[0].(*ColumnRef).Index; got != 1 {
		t.Fatalf("a refusal re-based the group key: index %d, want 1", got)
	}
	if !agg.InputTargetKnown {
		t.Fatal("a refusal consumed the stamp")
	}
}

// TestSinkNarrowingProjectStopsAtAFilterItWouldStarve: sinking past a Filter
// whose predicate reads a dropped column would evaluate the predicate against
// a row that no longer carries it. The Project must stop ABOVE it.
func TestSinkNarrowingProjectStopsAtAFilterItWouldStarve(t *testing.T) {
	km, ok := newKeepMap([]int{1, 3, 4}, 6)
	if !ok {
		t.Fatal("newKeepMap declined a well-formed keep")
	}
	leaf := &noNode{sch: noSchema("a", "b", "c", "d", "e", "f")}
	// Predicate reads c (position 2), which the keep drops.
	flt := &Filter{Child: leaf, Predicate: &BinaryOp{Op: parser.OpEq, Left: ungCol("c", 2), Right: &IntegerConst{Value: 1}}}
	sunk, ok := planSinkNarrowingProject(flt, km)
	if !ok {
		t.Fatal("the sink refused outright rather than stopping above the Filter")
	}
	if sunk.pastSort {
		t.Fatal("the sink reported a retention site where there is no Sort")
	}
	for _, apply := range sunk.install {
		apply()
	}
	p, isProj := sunk.root.(*Project)
	if !isProj {
		t.Fatalf("the sink returned %T, want a *Project above the Filter", sunk.root)
	}
	if p.Child != Node(flt) {
		t.Fatalf("the Project was not placed directly above the Filter (child is %T)", p.Child)
	}
	if flt.Child != Node(leaf) {
		t.Fatal("the sink descended past a Filter whose predicate reads a dropped column")
	}
}

// TestSinkNarrowingProjectSinksPastAFilterItCanFeed is the paired ACCEPT: a
// predicate reading only kept columns is re-based and the Project goes below.
func TestSinkNarrowingProjectSinksPastAFilterItCanFeed(t *testing.T) {
	km, _ := newKeepMap([]int{1, 3, 4}, 6)
	leaf := &noNode{sch: noSchema("a", "b", "c", "d", "e", "f")}
	flt := &Filter{Child: leaf, Predicate: &BinaryOp{Op: parser.OpEq, Left: ungCol("d", 3), Right: &IntegerConst{Value: 1}}}
	sunk, ok := planSinkNarrowingProject(flt, km)
	if !ok {
		t.Fatal("the sink refused a Filter whose predicate survives the cut")
	}
	// The plan writes nothing until it is applied: a Filter still reading
	// pre-cut positions here is the invariant, not a bug.
	if flt.Predicate.(*BinaryOp).Left.(*ColumnRef).Index != 3 {
		t.Fatal("planSinkNarrowingProject mutated the tree before the caller committed")
	}
	for _, apply := range sunk.install {
		apply()
	}
	if sunk.root != Node(flt) {
		t.Fatalf("the sink did not descend past the Filter (returned %T)", sunk.root)
	}
	p, isProj := flt.Child.(*Project)
	if !isProj {
		t.Fatalf("no Project below the Filter (child is %T)", flt.Child)
	}
	if p.Child != Node(leaf) {
		t.Fatalf("the Project is not directly above the leaf (child is %T)", p.Child)
	}
	if got := flt.Predicate.(*BinaryOp).Left.(*ColumnRef).Index; got != 1 {
		t.Fatalf("the Filter predicate was not re-based onto the narrowed row: index %d, want 1", got)
	}
}

// TestSinkNarrowingProjectDoesNotSinkPastRowSetChangingWrappers: `*Limit`
// (whose TiesKeys `enclosingNodeScopeOf` does not enumerate), `*Distinct`
// (which compares the WHOLE row) and `*DistinctOn` (whose KeyCols index its
// own output) each change which rows come out when their input narrows. The
// Project must stop above them.
func TestSinkNarrowingProjectDoesNotSinkPastRowSetChangingWrappers(t *testing.T) {
	km, _ := newKeepMap([]int{1, 3, 4}, 6)
	names := []string{"a", "b", "c", "d", "e", "f"}
	for _, tc := range []struct {
		name string
		node Node
	}{
		{"Limit", &Limit{Child: &noNode{sch: noSchema(names...)}}},
		// Distinct/DistinctOn publish their OWN stored schema, so the
		// fixtures set it: a bare one would be refused for a width mismatch
		// and the test would pass without exercising the descent at all.
		{"Distinct", &Distinct{Child: &noNode{sch: noSchema(names...)}, schema: noSchema(names...)}},
		{"DistinctOn", &DistinctOn{Child: &noNode{sch: noSchema(names...)}, schema: noSchema(names...)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sunk, ok := planSinkNarrowingProject(tc.node, km)
			if !ok {
				t.Fatalf("the sink refused outright rather than stopping above the %s", tc.name)
			}
			if sunk.pastSort {
				t.Fatalf("the sink reported a retention site above a %s", tc.name)
			}
			p, isProj := sunk.root.(*Project)
			if !isProj || p.Child != tc.node {
				t.Fatalf("the sink descended past a %s", tc.name)
			}
		})
	}
}

// TestNarrowingProjectOverCarriesSourceTableIdx: self-joins disambiguate two
// same-named columns by SourceTableIdx (Q21's three lineitem aliases), and a
// Project that drops it makes them indistinguishable to every name-level
// walker above. narrowPlanOutput carries it; so must this.
func TestNarrowingProjectOverCarriesSourceTableIdx(t *testing.T) {
	km, _ := newKeepMap([]int{0, 2}, 3)
	leaf := &noNode{sch: noSchema("a", "b", "c")}
	out, ok := narrowingProjectOver(leaf, km)
	if !ok {
		t.Fatal("narrowingProjectOver refused a well-formed cut")
	}
	p := out.(*Project)
	for i, want := range []int16{1, 3} {
		if got := p.Targets[i].(*ColumnRef).SourceTableIdx; got != want {
			t.Fatalf("target %d carries SourceTableIdx %d, want %d", i, got, want)
		}
		if got := p.Output()[i].SourceTableIdx; got != want {
			t.Fatalf("schema column %d carries SourceTableIdx %d, want %d", i, got, want)
		}
	}
	// The Project reads its child by PRE-cut position and publishes them in
	// keep order: an off-by-one here is the silent misalignment the whole
	// slice is arranged around.
	if got := p.Targets[1].(*ColumnRef).Index; got != 2 {
		t.Fatalf("target 1 reads child position %d, want 2", got)
	}
}

func TestNarrowingProjectOverRefusesAWidthMismatch(t *testing.T) {
	km, _ := newKeepMap([]int{0}, 3)
	if _, ok := narrowingProjectOver(&noNode{sch: noSchema("a", "b")}, km); ok {
		t.Fatal("narrowingProjectOver accepted a node whose width is not the keep's input width")
	}
	if _, ok := narrowingProjectOver(nil, km); ok {
		t.Fatal("narrowingProjectOver accepted a nil node")
	}
}

// ---------------------------------------------------------------------
// The node-field inventory — the fail-closed pin
// ---------------------------------------------------------------------

// TestUpperNarrowApplyNodeFieldInventory is this file's counterpart to
// `exprwalk_exhaustive_test.go`.
//
// `remapExprIndices` is exhaustive over the `Expr` TYPE set by construction
// (cloneExprRefs). The NODE-FIELD side has no such construction: a new
// `Expr`-bearing field on any type this pass rewrites would be left silently
// in PRE-CUT coordinates, which is a wrong column read with a correct row
// count. So the field set is PINNED here: adding one fails this test, and the
// fix is to rewrite it (or to refuse the site), never to extend the list
// without doing one of the two.
func TestUpperNarrowApplyNodeFieldInventory(t *testing.T) {
	want := map[string][]string{
		// Rewritten by rewriteAggregateInputExprs / installAggregateInputExprs.
		"Aggregate": {"GroupExprs", "Passthrough"},
		"AggregateCall": {
			"Arg", "Arg2", "ExtraArgs", "Filter", "OrderBy", "WithinGroupOrderBy",
		},
		// Rewritten by planSinkNarrowingProject's *Sort arm.
		"Sort": {"Keys"},
		// Rewritten by planSinkNarrowingProject's *Filter arm.
		"Filter": {"Predicate", "PushedBelow"},
	}
	types := map[string]reflect.Type{
		"Aggregate":     reflect.TypeOf(Aggregate{}),
		"AggregateCall": reflect.TypeOf(AggregateCall{}),
		"Sort":          reflect.TypeOf(Sort{}),
		"Filter":        reflect.TypeOf(Filter{}),
	}
	exprIface := reflect.TypeOf((*Expr)(nil)).Elem()
	sortKey := reflect.TypeOf(SortKey{})

	// readsRow reports whether a field can hold an expression evaluated
	// against the node's INPUT row: an Expr, a slice of Expr, a slice of
	// SortKey, or a slice of any of those.
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
				"A new field here is read against the PRE-CUT row unless upper_narrow_apply.go "+
				"rewrites it (or refuses the site). Extending this list without doing one of "+
				"those two is the wrong fix.", name, got, w)
		}
	}
}
