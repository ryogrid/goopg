package optimizer

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/parser"
	"testing"
)

// upper_narrow_gate_test.go — B-01c applying half, slice (a).
//
// The gate is refused-by-default machinery, and a refusing gate is trivially
// "safe" and trivially useless, so these tests are written in pairs: an
// ACCEPT case that proves the gate is not vacuously closed, and a REJECT case
// that states the wrong answer it exists to stop.
//
// The centrepiece is TestOrderSensitiveOracle*: an ORDERING oracle, not a
// membership one. D-06 records why — the executor gate checks ordering
// explicitly, not membership, so a mis-based comparator emits out-of-order
// rows with no error and NO row-count gate and NO sort-then-compare values
// gate can see it. The oracle itself carries a vacuity guard
// (TestOrderSensitiveOracleDetectsMisbasedKeys): a deliberately mis-based
// rewrite must make it FAIL, or its passes prove nothing.

// ---------------------------------------------------------------------
// keepMap
// ---------------------------------------------------------------------

func TestNewKeepMapRejectsMalformedKeeps(t *testing.T) {
	cases := []struct {
		name    string
		keep    []int
		inWidth int
	}{
		{"descending", []int{2, 1}, 4},
		{"duplicate", []int{1, 1}, 4},
		{"negative", []int{-1, 2}, 4},
		{"out of range", []int{0, 4}, 4},
		{"longer than the row", []int{0, 1, 2}, 2},
		{"negative width", []int{}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := newKeepMap(tc.keep, tc.inWidth); ok {
				t.Fatalf("newKeepMap(%v, %d) accepted a malformed keep", tc.keep, tc.inWidth)
			}
		})
	}
}

func TestKeepMapDirectionsAndIdentity(t *testing.T) {
	km, ok := newKeepMap([]int{1, 3, 4}, 6)
	if !ok {
		t.Fatal("newKeepMap declined a well-formed keep")
	}
	if got := km.narrowedWidth(); got != 3 {
		t.Fatalf("narrowedWidth = %d, want 3", got)
	}
	if km.isIdentity() {
		t.Fatal("a 3-of-6 keep reported itself as the identity cut")
	}
	for newIdx, old := range []int{1, 3, 4} {
		if got, ok := km.forward(old); !ok || got != newIdx {
			t.Fatalf("forward(%d) = (%d,%v), want (%d,true)", old, got, ok, newIdx)
		}
		if got, ok := km.inverse(newIdx); !ok || got != old {
			t.Fatalf("inverse(%d) = (%d,%v), want (%d,true)", newIdx, got, ok, old)
		}
	}
	for _, dropped := range []int{0, 2, 5} {
		if _, ok := km.forward(dropped); ok {
			t.Fatalf("forward(%d) accepted a column the cut drops", dropped)
		}
	}
	if _, ok := km.forward(6); ok {
		t.Fatal("forward accepted an out-of-range pre-cut position")
	}
	if _, ok := km.inverse(3); ok {
		t.Fatal("inverse accepted an out-of-range post-cut position")
	}

	full, ok := newKeepMap([]int{0, 1, 2}, 3)
	if !ok || !full.isIdentity() {
		t.Fatalf("a full-width keep did not report itself as the identity cut (ok=%v)", ok)
	}
}

// ---------------------------------------------------------------------
// remapExprIndices — accept / refuse
// ---------------------------------------------------------------------

func ungCol(name string, idx int) *ColumnRef { return &ColumnRef{Index: idx, Name: name} }

// TestRemapExprIndicesRebasesNestedContainers: the remap must reach INSIDE
// containers, not just top-level refs. CaseExpr is the shape whose missing
// arm produced the RC-1a wrong answer (exprwalk.go header).
func TestRemapExprIndicesRebasesNestedContainers(t *testing.T) {
	km, _ := newKeepMap([]int{1, 3, 4}, 6)
	e := &CaseExpr{
		Whens: []CaseWhen{{
			When: &IsNullExpr{Operand: ungCol("b", 1)},
			Then: &FuncCall{Name: "lower", Args: []Expr{ungCol("d", 3)}},
		}},
		Else: &BinaryOp{Op: parser.OpAdd, Left: ungCol("e", 4), Right: &IntegerConst{Value: 1}},
	}
	out, ok := remapExprIndices(e, km.forward)
	if !ok {
		t.Fatal("remapExprIndices refused an expression whose every ref survives the cut")
	}
	got := map[string]int{}
	if !walkExprRefs(out, scopeVeto, exprVisitor{Visit: func(n Expr) bool {
		if c, isCol := n.(*ColumnRef); isCol {
			got[c.Name] = c.Index
		}
		return true
	}}) {
		t.Fatal("walking the remapped expression aborted")
	}
	for name, want := range map[string]int{"b": 0, "d": 1, "e": 2} {
		if got[name] != want {
			t.Fatalf("column %q remapped to %d, want %d (all: %v)", name, got[name], want, got)
		}
	}

	// The ORIGINAL must be untouched: cloneExprRefs, not an in-place
	// rewrite. A caller that runs the gate and then declines must be left
	// with the pre-cut tree intact.
	if orig := e.Whens[0].When.(*IsNullExpr).Operand.(*ColumnRef).Index; orig != 1 {
		t.Fatalf("remapExprIndices mutated the caller's tree (index now %d, want 1)", orig)
	}
}

func TestRemapExprIndicesRefuses(t *testing.T) {
	km, _ := newKeepMap([]int{1, 3}, 5)
	cases := []struct {
		name string
		e    Expr
	}{
		{"a column the cut drops", ungCol("a", 0)},
		{"an unnamed column ref", &ColumnRef{Index: 1}},
		{"an outer-scope ref", &OuterColumnRef{Level: 1, Index: 1, Name: "b"}},
		{"a CTID read", &CTIDExpr{}},
		{"a MERGE whole-row read", &MergeWholeRowRef{}},
		{"a dropped column nested in a container",
			&FuncCall{Name: "abs", Args: []Expr{ungCol("c", 2)}}},
		{"an inner plan (subquery)",
			&SubqueryExpr{Plan: &noNode{sch: noSchema("x")}, Args: []Expr{ungCol("b", 1)}}},
		{"an inner plan nested in a container",
			&IsNullExpr{Operand: &ExistsExpr{Plan: &noNode{sch: noSchema("x")}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := remapExprIndices(tc.e, km.forward); ok {
				t.Fatalf("remapExprIndices accepted %s", tc.name)
			}
		})
	}
}

// TestRemapExprIndicesAcceptsRowlessLeaves: params and the per-table OID read
// no row column, so the cut cannot invalidate them. Refusing them would
// needlessly close the gate on every parameterised plan.
func TestRemapExprIndicesAcceptsRowlessLeaves(t *testing.T) {
	km, _ := newKeepMap([]int{1}, 3)
	for _, e := range []Expr{
		&ParamRef{},
		&ExecParamRef{},
		&TableOidExpr{},
		&BinaryOp{Op: parser.OpEq, Left: ungCol("b", 1), Right: &ParamRef{}},
	} {
		if _, ok := remapExprIndices(e, km.forward); !ok {
			t.Fatalf("remapExprIndices refused a row-less leaf shape %T", e)
		}
	}
}

// ---------------------------------------------------------------------
// keepPreservesExpr — the round trip
// ---------------------------------------------------------------------

// TestKeepPreservesExprRoundTrips: the forward remap moves indices (so a
// direct comparison against the original would prove nothing) and the
// inverse restores them exactly.
func TestKeepPreservesExprRoundTrips(t *testing.T) {
	km, _ := newKeepMap([]int{2, 5}, 8)
	e := &BinaryOp{Op: parser.OpLt, Left: ungCol("c", 2), Right: ungCol("f", 5)}
	fwd, ok := keepPreservesExpr(e, km)
	if !ok {
		t.Fatal("keepPreservesExpr refused an expression whose refs all survive")
	}
	origKey, _ := exprIdentityKey(e, scopeVeto)
	fwdKey, _ := exprIdentityKey(fwd, scopeVeto)
	if origKey == fwdKey {
		t.Fatalf("forward remap left the identity key unchanged (%s) — the cut moved no coordinate, so the round trip proves nothing", fwdKey)
	}
	back, ok := remapExprIndices(fwd, km.inverse)
	if !ok {
		t.Fatal("inverse remap refused the forward result")
	}
	backKey, _ := exprIdentityKey(back, scopeVeto)
	if backKey != origKey {
		t.Fatalf("round trip changed the expression: %s -> %s", origKey, backKey)
	}
}

// ---------------------------------------------------------------------
// ORDERING ORACLE — the load-bearing half
// ---------------------------------------------------------------------

// ungPermutation sorts row indices under keys read POSITIONALLY out of rows,
// with the same comparator shape the executor uses (per-key, DESC inverts,
// NULLS placement by flag). It is a coordinate-level oracle: the failure it
// exists to catch is a key that ends up reading the wrong column after a
// keep-list re-base, and that is visible in the resulting PERMUTATION even
// with a toy value domain.
//
// A negative value stands for NULL, which is what makes NullsFirst
// observable at all.
func ungPermutation(rows [][]int64, keys []SortKey) []int {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rows[idx[a]], rows[idx[b]]
		for _, k := range keys {
			c := k.Expr.(*ColumnRef).Index
			va, vb := ra[c], rb[c]
			na, nb := va < 0, vb < 0
			if na || nb {
				if na && nb {
					continue
				}
				// One side is NULL: flags decide, DESC does not
				// re-invert NULL placement (PG semantics).
				if k.NullsFirst {
					return na
				}
				return nb
			}
			if va == vb {
				continue
			}
			if k.Desc {
				return va > vb
			}
			return va < vb
		}
		return false
	})
	return idx
}

func ungNarrowRows(rows [][]int64, keep []int) [][]int64 {
	out := make([][]int64, len(rows))
	for i, r := range rows {
		nr := make([]int64, len(keep))
		for j, c := range keep {
			nr[j] = r[c]
		}
		out[i] = nr
	}
	return out
}

func ungSameOrder(a, b []int) bool {
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

// ungOrderFixture is one wide row set plus a multi-key ordering that
// exercises ASC/DESC and both NULLS placements, with ties that force the
// later keys to matter.
func ungOrderFixture() ([][]int64, []SortKey, []string) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	rows := [][]int64{
		{9, 2, 7, 5, 1, 4},
		{9, 2, 7, 5, 3, 4},
		{1, 2, 7, -1, 2, 4}, // NULL in d
		{5, 1, 7, 8, 9, 4},
		{5, 1, 7, 2, 9, 4},
		{5, 1, 7, -1, 0, 4}, // NULL in d
		{2, 3, 7, 6, 6, 4},
	}
	keys := []SortKey{
		{Expr: ungCol("b", 1), Desc: false},
		{Expr: ungCol("d", 3), Desc: true, NullsFirst: true},
		{Expr: ungCol("e", 4), Desc: false},
	}
	return rows, keys, names
}

// TestOrderSensitiveOracleSortKeysSurviveNarrowing: the gate's rewritten key
// list, read against the NARROWED rows, induces exactly the ordering the
// original key list induces against the WIDE rows.
//
// This is the check a row-count gate structurally cannot perform and a
// values gate that sorts before comparing structurally discards.
func TestOrderSensitiveOracleSortKeysSurviveNarrowing(t *testing.T) {
	rows, keys, names := ungOrderFixture()
	// Keep the three key columns plus one payload column; drop a, c, f.
	keep := []int{1, 3, 4}
	km, ok := newKeepMap(keep, len(names))
	if !ok {
		t.Fatal("newKeepMap declined the fixture keep")
	}
	newKeys, ok := keepPreservesSortKeyList(keys, km)
	if !ok {
		t.Fatal("the gate refused a keep that contains every sort-key column")
	}
	want := ungPermutation(rows, keys)
	got := ungPermutation(ungNarrowRows(rows, keep), newKeys)
	if !ungSameOrder(want, got) {
		t.Fatalf("narrowing changed the emitted order: wide %v, narrowed %v", want, got)
	}
	// Not vacuous: the fixture's ordering is not the input order, so an
	// oracle that ignored the keys entirely would fail here.
	if ungSameOrder(want, []int{0, 1, 2, 3, 4, 5, 6}) {
		t.Fatal("fixture ordering equals input order — the oracle would pass without comparing anything")
	}
}

// TestOrderSensitiveOracleDetectsMisbasedKeys is the VACUITY GUARD. It feeds
// the oracle a deliberately mis-based key list — every index shifted by one,
// the exact off-by-one a hand-written keep-list re-base produces — and
// requires the oracle to NOTICE. Without this, the test above could be
// passing because the oracle compares nothing.
func TestOrderSensitiveOracleDetectsMisbasedKeys(t *testing.T) {
	rows, keys, names := ungOrderFixture()
	keep := []int{1, 3, 4}
	km, _ := newKeepMap(keep, len(names))
	good, ok := keepPreservesSortKeyList(keys, km)
	if !ok {
		t.Fatal("the gate refused the fixture keep")
	}
	bad := make([]SortKey, len(good))
	for i, k := range good {
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

// TestKeepPreservesSortKeyListCarriesFlags: DESC/NULLS FIRST are half the
// comparator and are not expressions, so no expression-level check can see
// them. They must survive the rewrite element by element.
func TestKeepPreservesSortKeyListCarriesFlags(t *testing.T) {
	km, _ := newKeepMap([]int{1, 3, 4}, 6)
	_, keys, _ := ungOrderFixture()
	out, ok := keepPreservesSortKeyList(keys, km)
	if !ok {
		t.Fatal("the gate refused a preserving keep")
	}
	if len(out) != len(keys) {
		t.Fatalf("key list length changed: %d -> %d", len(keys), len(out))
	}
	for i := range keys {
		if out[i].Desc != keys[i].Desc || out[i].NullsFirst != keys[i].NullsFirst {
			t.Fatalf("key %d flags changed: {%v,%v} -> {%v,%v}",
				i, keys[i].Desc, keys[i].NullsFirst, out[i].Desc, out[i].NullsFirst)
		}
	}
}

func TestKeepPreservesSortKeyListRefusesNilComparator(t *testing.T) {
	km, _ := newKeepMap([]int{0}, 2)
	if _, ok := keepPreservesSortKeyList([]SortKey{{Expr: nil}}, km); ok {
		t.Fatal("the gate accepted a nil key expression — an undecidable comparator must not read as preserved")
	}
}

// ---------------------------------------------------------------------
// The node-level gate
// ---------------------------------------------------------------------

func ungSort(names []string, keys []SortKey) *Sort {
	return &Sort{Child: &noNode{sch: noSchema(names...)}, Keys: keys}
}

func TestGateAcceptsSortKeepCoveringItsKeys(t *testing.T) {
	_, keys, names := ungOrderFixture()
	s := ungSort(names, keys)
	ok, reason := keepPreservesUpperNodeKeys(s, []int{1, 3, 4})
	if !ok {
		t.Fatalf("the gate refused a keep covering every sort key: %s", reason)
	}
	if reason != "" {
		t.Fatalf("an accepting verdict carried a reason: %q", reason)
	}
}

// TestGateRejectsSortKeepDroppingAKey states the wrong answer: a keep that
// drops a sort-key column would leave the Sort comparing a column that is no
// longer in the row — out-of-order rows with a correct row count.
func TestGateRejectsSortKeepDroppingAKey(t *testing.T) {
	_, keys, names := ungOrderFixture()
	s := ungSort(names, keys)
	// Drops d (position 3), the second sort key.
	ok, reason := keepPreservesUpperNodeKeys(s, []int{1, 4})
	if ok {
		t.Fatal("the gate accepted a keep that drops a sort-key column")
	}
	if reason == "" {
		t.Fatal("a refusing verdict carried no reason")
	}
}

func TestGateRejectsMalformedAndNonSiteNodes(t *testing.T) {
	_, keys, names := ungOrderFixture()
	s := ungSort(names, keys)
	if ok, _ := keepPreservesUpperNodeKeys(s, []int{4, 1}); ok {
		t.Fatal("the gate accepted a descending keep")
	}
	if ok, _ := keepPreservesUpperNodeKeys(s, []int{1, 3, 9}); ok {
		t.Fatal("the gate accepted an out-of-range keep")
	}
	if ok, r := keepPreservesUpperNodeKeys(&Filter{Child: s}, []int{0}); ok {
		t.Fatalf("the gate accepted a *Filter as a narrowing site (%s)", r)
	}
	if ok, _ := keepPreservesUpperNodeKeys(&Join{
		Left:  &noNode{sch: noSchema("a")},
		Right: &noNode{sch: noSchema("b")},
	}, []int{0}); ok {
		t.Fatal("the gate accepted a *Join, whose enclosing width is the MERGED row")
	}
}

func TestGateAggregateGroupKeysAndArgs(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	agg := &Aggregate{
		Child:      &noNode{sch: noSchema(names...)},
		GroupExprs: []Expr{ungCol("a", 0), ungCol("c", 2)},
		Aggs: []AggregateCall{{
			Name: "sum",
			Arg:  ungCol("d", 3),
		}},
	}
	if ok, reason := keepPreservesUpperNodeKeys(agg, []int{0, 2, 3}); !ok {
		t.Fatalf("the gate refused a keep covering every group key and aggregate arg: %s", reason)
	}
	// Drops c, a group key.
	if ok, _ := keepPreservesUpperNodeKeys(agg, []int{0, 3}); ok {
		t.Fatal("the gate accepted a keep that drops a group key")
	}
	// Drops d, the aggregate's argument — membership, not ordering, but
	// the same wrong answer class.
	if ok, _ := keepPreservesUpperNodeKeys(agg, []int{0, 2}); ok {
		t.Fatal("the gate accepted a keep that drops an aggregate argument")
	}
}

// TestGateAggregateChecksFilterAndPassthrough: enclosingNodeScopeOf is WIDER
// than walkPlanExprs — it covers AggregateCall.Filter and
// Aggregate.Passthrough. Using it as the membership source is what makes the
// gate see those two fields; a re-derived expression set would not.
func TestGateAggregateChecksFilterAndPassthrough(t *testing.T) {
	names := []string{"a", "b", "c"}
	base := func() *Aggregate {
		return &Aggregate{
			Child:      &noNode{sch: noSchema(names...)},
			GroupExprs: []Expr{ungCol("a", 0)},
			Aggs:       []AggregateCall{{Name: "count"}},
		}
	}
	withFilter := base()
	withFilter.Aggs[0].Filter = ungCol("b", 1)
	if ok, _ := keepPreservesUpperNodeKeys(withFilter, []int{0}); ok {
		t.Fatal("the gate accepted a keep that drops a column read by an aggregate FILTER")
	}
	withPass := base()
	withPass.Passthrough = []Expr{ungCol("c", 2)}
	if ok, _ := keepPreservesUpperNodeKeys(withPass, []int{0}); ok {
		t.Fatal("the gate accepted a keep that drops a passthrough column")
	}
	if ok, reason := keepPreservesUpperNodeKeys(withPass, []int{0, 2}); !ok {
		t.Fatalf("the gate refused a keep that covers the passthrough column: %s", reason)
	}
}

func TestGateWindowPartitionOrderAndFrame(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	win := &WindowAgg{
		Child:       &noNode{sch: noSchema(names...)},
		PartitionBy: []Expr{ungCol("a", 0)},
		OrderBy:     []SortKey{{Expr: ungCol("b", 1), Desc: true}},
		Funcs:       []WindowFunc{{Name: "sum", Args: []Expr{ungCol("c", 2)}}},
	}
	if ok, reason := keepPreservesUpperNodeKeys(win, []int{0, 1, 2}); !ok {
		t.Fatalf("the gate refused a keep covering partition, order and arg columns: %s", reason)
	}
	if ok, _ := keepPreservesUpperNodeKeys(win, []int{1, 2}); ok {
		t.Fatal("the gate accepted a keep that drops a PARTITION BY column")
	}
	if ok, _ := keepPreservesUpperNodeKeys(win, []int{0, 2}); ok {
		t.Fatal("the gate accepted a keep that drops a window ORDER BY column")
	}
	if ok, _ := keepPreservesUpperNodeKeys(win, []int{0, 1}); ok {
		t.Fatal("the gate accepted a keep that drops a window function argument")
	}
	// The frame offsets are in enclosingNodeScopeOf's set too.
	win.Frame = &WindowFrame{StartOffset: ungCol("d", 3)}
	if ok, _ := keepPreservesUpperNodeKeys(win, []int{0, 1, 2}); ok {
		t.Fatal("the gate accepted a keep that drops a column read by a frame offset")
	}
}

// ---------------------------------------------------------------------
// Stamp integration + the output-width asymmetry
// ---------------------------------------------------------------------

// TestGateAgreesWithTheComputeHalfStamp: the keep the compute half derives
// must pass the gate. If it did not, one of the two halves would be wrong.
func TestGateAgreesWithTheComputeHalfStamp(t *testing.T) {
	_, keys, names := ungOrderFixture()
	s := ungSort(names, keys)
	stampSortInputTarget(s, nil)
	if !s.InputTargetKnown {
		t.Fatal("the compute half declined an enumerable fixture")
	}
	if ok, reason := keepPreservesStampedTarget(s); !ok {
		t.Fatalf("the gate refused the keep the compute half stamped: %s (keep %v)", reason, s.InputTarget)
	}
}

// TestGateRefusesAnUnknownStamp: unknown means "the derivation could not
// enumerate this site", and InputTarget is then nil — which as a keep-list
// would mean "drop every column". It must never read as "narrow nothing".
func TestGateRefusesAnUnknownStamp(t *testing.T) {
	names := []string{"a", "b"}
	s := ungSort(names, []SortKey{{Expr: &OuterColumnRef{Level: 1, Index: 0, Name: "x"}}})
	stampSortInputTarget(s, nil)
	if s.InputTargetKnown {
		t.Fatal("the compute half claimed to enumerate an outer-scope key")
	}
	ok, reason := keepPreservesStampedTarget(s)
	if ok {
		t.Fatal("the gate accepted an unknown stamp")
	}
	if !strings.Contains(reason, "unknown") {
		t.Fatalf("refusal reason %q does not name the unknown stamp", reason)
	}
}

// TestUpperNarrowingChangesOutput pins the asymmetry that sequences any
// applying cut: Sort and WindowAgg republish their child's columns (so a
// narrowed input is a narrowed output, and every expression ABOVE must be
// re-based by an ancestor-chain walk — still open, ledger
// `take3-B-01c-applying-blocked`), while Aggregate's output is built from its
// OWN expression lists and does not move at all. That asymmetry is exactly
// why applying slice (b) (upper_narrow_apply.go) landed the AGGREGATE site
// and only that one.
func TestUpperNarrowingChangesOutput(t *testing.T) {
	if changes, ok := upperNarrowingChangesOutput(&Sort{}); !ok || !changes {
		t.Fatalf("Sort: (changes=%v, ok=%v), want (true, true)", changes, ok)
	}
	if changes, ok := upperNarrowingChangesOutput(&WindowAgg{}); !ok || !changes {
		t.Fatalf("WindowAgg: (changes=%v, ok=%v), want (true, true)", changes, ok)
	}
	if changes, ok := upperNarrowingChangesOutput(&Aggregate{}); !ok || changes {
		t.Fatalf("Aggregate: (changes=%v, ok=%v), want (false, true)", changes, ok)
	}
	if _, ok := upperNarrowingChangesOutput(&Filter{}); ok {
		t.Fatal("upperNarrowingChangesOutput answered for a non-site node")
	}

	// The Sort/WindowAgg claim is a fact about Output(), not a comment:
	// state it against the real methods so a change to either is a test
	// failure here rather than a wrong answer later.
	child := &noNode{sch: noSchema("a", "b", "c")}
	if got, want := len((&Sort{Child: child}).Output()), len(child.Output()); got != want {
		t.Fatalf("Sort.Output() width %d != child width %d", got, want)
	}
}

// TestGateOrderingHalfCatchesWhatMembershipCannot proves the ordering half of
// keepPreservesUpperNodeKeys is not redundant with the membership half.
//
// enclosingNodeScopeOf's `sortKeyExprs` DROPS nil key expressions, and
// keepPreservesExpr(nil) is trivially true, so a key list containing a nil
// comparator passes the membership pass VACUOUSLY — the check has nothing to
// look at. Only the ordering pass, which walks the key list itself, sees it.
// The same holds for a nil GroupExpr, whose position `GroupKeyOrder` and
// `GroupingMasks` index into.
//
// If check (2) were deleted from the gate, this test is the one that fails.
func TestGateOrderingHalfCatchesWhatMembershipCannot(t *testing.T) {
	names := []string{"a", "b"}

	s := ungSort(names, []SortKey{{Expr: ungCol("a", 0)}, {Expr: nil, Desc: true}})
	sc, known := enclosingNodeScopeOf(s)
	if !known {
		t.Fatal("enclosingNodeScopeOf does not enumerate *Sort")
	}
	km, _ := newKeepMap([]int{0}, sc.width)
	for i, e := range sc.exprs {
		if _, ok := keepPreservesExpr(e, km); !ok {
			t.Fatalf("membership pass unexpectedly refused input expression %d", i)
		}
	}
	if ok, _ := keepPreservesUpperNodeKeys(s, []int{0}); ok {
		t.Fatal("the gate accepted a Sort with a nil comparator that the membership pass cannot see")
	}

	agg := &Aggregate{
		Child:      &noNode{sch: noSchema(names...)},
		GroupExprs: []Expr{ungCol("a", 0), nil},
		Aggs:       []AggregateCall{{Name: "count"}},
	}
	if ok, _ := keepPreservesUpperNodeKeys(agg, []int{0}); ok {
		t.Fatal("the gate accepted an Aggregate with a nil group key that the membership pass cannot see")
	}
}
