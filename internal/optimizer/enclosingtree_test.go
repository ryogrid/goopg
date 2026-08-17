package optimizer

// M0127-P5.5-f-ii-b — the enclosing-tree tripwire and the pinned spine's
// consumption of the boundary map (enclosingtree.go, predp.go).
//
// Both are LIVE in production since M0127-P5.9 (2026-08-06):
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so these
// tests are no longer their only observer — enclosingtree.go's own header
// already records the correction. The unusual one is
// `TestEnclosingTripwireRefusesToPassVacuously`: it asserts that the tripwire
// FAILS on a tree it cannot check, which is the lesson P5.5-f-ii-a paid for —
// an assertion that abstains silently is a false green, and a partial tree walk
// is the same failure one level up.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// etSearched builds a tagged searched subtree in the ELIDED shape: the standard
// two-rel fixture already in binding order, so `createPlanAtSearchRoot` emits no
// boundary Project and the root is a bare `*Join` publishing
// `a0 a1 b0 b1 b2`.
//
// The elided shape is used deliberately — it is the one the tag exists for
// (searchedtree.go), and it is the one whose root is a node kind the tripwire
// walk would otherwise happily descend into.
func etSearched(t *testing.T) Node {
	t.Helper()
	a, b := cpjTwoRel()
	n := createPlanAtSearchRoot(stHashRoot(a, b), 5)
	if !isSearchedTree(n) {
		t.Fatalf("fixture root %T is not tagged; these tests would prove nothing", n)
	}
	return n
}

// etCol is a named same-scope reference at a given index.
func etCol(idx int, name string) *ColumnRef {
	return &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}}
}

// etScan is a plain leaf for the side of a spine join that is not the searched
// subtree.
func etScan(prefix string, width int) *SeqScan {
	return &SeqScan{Table: &catalog.Table{Name: prefix}, Alias: prefix, schema: cpjSchema(prefix, width)}
}

// etRecoverContains runs fn and requires it to panic with a message containing
// want. Used rather than a bare recover so a test that stops panicking fails
// loudly instead of passing on a nil recover.
func etRecoverContains(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic; expected one mentioning %q", want)
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, want) {
			t.Fatalf("panic %q does not mention %q", r, want)
		}
	}()
	fn()
}

// TestEnclosingTripwireAcceptsAWellFormedTree is the base case, and it also
// pins the two numbers that make the failure cases meaningful: the walk really
// reached the searched subtree, and it really had nodes to check.
func TestEnclosingTripwireAcceptsAWellFormedTree(t *testing.T) {
	searched := etSearched(t)
	top := &Project{
		Child:   searched,
		Targets: []Expr{etCol(0, "a0"), etCol(4, "b2")},
		schema:  Schema{{Name: "a0"}, {Name: "b2"}},
	}
	root := &Sort{Child: top, Keys: []SortKey{{Expr: etCol(1, "b2")}}}

	assertEnclosingTreeColumnRefs("test", root)

	w := walkEnclosingTree("test", root)
	if w.searchedRoots != 1 {
		t.Errorf("walk found %d searched roots, want 1", w.searchedRoots)
	}
	if w.checkedNodes != 2 {
		t.Errorf("walk checked %d nodes, want 2 (the Sort and the Project)", w.checkedNodes)
	}
	if len(w.stoppedAt) != 0 {
		t.Errorf("walk stopped at %v; nothing on this tree should be unenumerated", w.stoppedAt)
	}
}

// TestEnclosingTripwireCatchesAnOutOfRangeRefAboveTheSearchRoot is the M0097-0058
// class the tripwire exists for, moved from execution time to plan time. A
// target reading column 5 of the searched subtree's 5-column row is exactly the
// shape that used to surface as an out-of-bounds slice access inside the
// executor, on a query that had already been accepted and costed.
func TestEnclosingTripwireCatchesAnOutOfRangeRefAboveTheSearchRoot(t *testing.T) {
	searched := etSearched(t)
	top := &Project{Child: searched, Targets: []Expr{etCol(5, "b2")}, schema: Schema{{Name: "b2"}}}

	etRecoverContains(t, "references column 5", func() {
		assertEnclosingTreeColumnRefs("test", top)
	})
}

// TestEnclosingTripwireRefusesToPassVacuously is the load-bearing test.
//
// The walk is deliberately partial — it enumerates the node kinds that can
// stand between a searched subtree and the top of a SELECT, not all 53. A
// partial walk that stops on the PATH to the searched subtree checks nothing
// while returning normally, which is the same false green
// `assertSearchedTreeNeedsNoReconcile` hit with unnamed operands in P5.5-f-ii-a.
// The guard is therefore on reaching the subtree, and the message must name the
// kind that needs teaching.
func TestEnclosingTripwireRefusesToPassVacuously(t *testing.T) {
	// A `*Values` on the path: not enumerated, so the walk stops before the
	// searched subtree it was asked about.
	unreachable := &Project{
		Child:   &Values{schema: cpjSchema("v", 2)},
		Targets: []Expr{etCol(0, "v0")},
		schema:  Schema{{Name: "v0"}},
	}

	etRecoverContains(t, "never reached a searched subtree", func() {
		assertEnclosingTreeColumnRefs("test", unreachable)
	})
	etRecoverContains(t, "optimizer.Values", func() {
		assertEnclosingTreeColumnRefs("test", unreachable)
	})

	// And a tree that simply has no searched subtree at all — the same
	// verdict, because the tripwire is the SEARCH's and silence about a tree
	// the search never touched is not a result.
	etRecoverContains(t, "never reached a searched subtree", func() {
		assertEnclosingTreeColumnRefs("test", &Project{Child: etScan("a", 2), Targets: []Expr{etCol(0, "a0")}})
	})
}

// TestJoinExpressionsAreCheckedAgainstTheMergedRow pins the entry most likely to
// be "simplified" into a bug. A Semi join's `Output()` is the LEFT row only,
// but its predicate and keys evaluate against the padded `Left ++ Right` row —
// `reresolveJoinByName` rebinds the right side at `offset = leftWidth` for that
// reason. Checking join expressions against `Output()` would reject every legal
// right-side key on every semi/anti join in the pinned spine.
func TestJoinExpressionsAreCheckedAgainstTheMergedRow(t *testing.T) {
	searched := etSearched(t)
	inner := etScan("c", 2)
	semi := &Join{
		Type:     JoinTypeSemi,
		Algo:     JoinAlgoHash,
		Left:     searched,
		Right:    inner,
		LeftKey:  etCol(0, "a0"),
		RightKey: etCol(5, "c0"), // merged coordinate: right column 0 at 5 + 0
		schema:   append(append(Schema{}, searched.Output()...), inner.Output()...),
	}

	// In range for the 7-column merged row, out of range for the 5-column
	// output the semi join publishes.
	assertEnclosingTreeColumnRefs("test", semi)

	semi.RightKey = etCol(7, "c0")
	etRecoverContains(t, "references column 7", func() {
		assertEnclosingTreeColumnRefs("test", semi)
	})
}

// TestNLIInnerSideIsNotWalked: the inner `*IndexScan` of a parameterised nested
// loop holds probe keys written in the OUTER row's coordinates, so checking them
// against the inner's own width would be checking the wrong schema — the same
// scope boundary `assertColumnRefsWithinSchema`'s `scopeIgnore` policy draws for
// expressions. The residual predicate above it IS checked, against the merged
// row.
func TestNLIInnerSideIsNotWalked(t *testing.T) {
	searched := etSearched(t)
	probe := &IndexScan{
		Table:  &catalog.Table{Name: "c"},
		Alias:  "c",
		Key:    etCol(99, "a0"), // an OUTER coordinate, nonsense against `c`
		schema: cpjSchema("c", 2),
	}
	nli := &NestedLoopIndexJoin{
		Type:      JoinTypeInner,
		Outer:     searched,
		Inner:     probe,
		Predicate: etCol(6, "c1"), // merged: 5 + 1, in range
		schema:    append(append(Schema{}, searched.Output()...), probe.Output()...),
	}

	assertEnclosingTreeColumnRefs("test", nli)

	nli.Predicate = etCol(7, "c1")
	etRecoverContains(t, "references column 7", func() {
		assertEnclosingTreeColumnRefs("test", nli)
	})
}

// TestAggregateWiderExpressionSetIsChecked covers the two expression fields the
// shallow `walkPlanExprs` does not enumerate — `Aggregate.Passthrough` and
// `AggregateCall.Filter`. Both are resolved against the child's row like every
// other expression on the node, so an out-of-range index in one is the same
// defect; a tripwire that inherited the shallow walker's set would be blind to
// it. (The omission in `walkPlanExprs` itself is a ledger row, not a change
// made here — its callers are rewriters.)
func TestAggregateWiderExpressionSetIsChecked(t *testing.T) {
	for _, tc := range []struct {
		name string
		agg  func(*Aggregate)
	}{
		{"Passthrough", func(a *Aggregate) { a.Passthrough = []Expr{etCol(5, "b2")} }},
		{"AggregateCall.Filter", func(a *Aggregate) { a.Aggs = []AggregateCall{{Name: "count", Filter: etCol(5, "b2")}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agg := &Aggregate{Child: etSearched(t), GroupExprs: []Expr{etCol(0, "a0")}}
			tc.agg(agg)
			etRecoverContains(t, "references column 5", func() {
				assertEnclosingTreeColumnRefs("test", agg)
			})
		})
	}
}

// TestSplicedSearchedRootLooksThroughRetainedFilters: the DP block's two
// outcomes are "the searched root" and "a Filter holding the conjuncts the
// search did not consume, over the searched root". Anything else is a shape the
// helper has no opinion about, and answering nil there sends `predp.go` down the
// legacy re-resolution it would have run anyway.
func TestSplicedSearchedRootLooksThroughRetainedFilters(t *testing.T) {
	searched := etSearched(t)
	if got := splicedSearchedRoot(searched); got != searched {
		t.Errorf("splicedSearchedRoot(tagged root) = %v, want the root itself", got)
	}
	one := &Filter{Child: searched, Predicate: etCol(0, "a0")}
	if got := splicedSearchedRoot(one); got != searched {
		t.Errorf("splicedSearchedRoot did not look through one retained Filter")
	}
	two := &Filter{Child: one, Predicate: etCol(1, "a1")}
	if got := splicedSearchedRoot(two); got != searched {
		t.Errorf("splicedSearchedRoot did not look through two retained Filters")
	}
	if got := splicedSearchedRoot(&Filter{Child: etScan("a", 2)}); got != nil {
		t.Errorf("splicedSearchedRoot found %v over an untagged chain", got)
	}
	if got := splicedSearchedRoot(&Project{Child: searched}); got != nil {
		t.Errorf("splicedSearchedRoot looked through a *Project; only retained Filters are the DP block's shape")
	}
	if got := splicedSearchedRoot(nil); got != nil {
		t.Errorf("splicedSearchedRoot(nil) = %v, want nil", got)
	}
}

// TestLayoutPosMapReturnsNilForTwoDifferentReasons is the fact that decides how
// the spine assertion is written, so it is pinned rather than remembered.
//
// `pm == nil` cannot be read as "the boundary map is the identity": the helper
// also returns nil when the widths differ, where it means "refuse to remap
// rather than corrupt". A boundary that lost a column would take the second
// door and be indistinguishable from success — while the enclosing tree went on
// referencing columns that had moved.
func TestLayoutPosMapReturnsNilForTwoDifferentReasons(t *testing.T) {
	same := Schema{{Name: "a0"}, {Name: "a1"}}
	if pm := layoutPosMap(same, append(Schema(nil), same...)); pm != nil {
		t.Errorf("layoutPosMap on identical layouts returned a map")
	}
	if pm := layoutPosMap(same, Schema{{Name: "a0"}}); pm != nil {
		t.Errorf("layoutPosMap on mismatched widths returned a map; this test's premise is gone")
	}
}

// TestAssertSpineConsumesIdentityBoundaryMap: the identity is the CLAIM
// (P5.5-f-i — at the search root, canonical relid order and pre-search binding
// order are the same sequence), so it is checked, not assumed. The two failure
// messages have to distinguish the two ways it can break, because they mean
// different producer bugs: a lost/gained column is a boundary that did not
// reproduce the binding concatenation, a permutation is a boundary map that is
// not the identity.
func TestAssertSpineConsumesIdentityBoundaryMap(t *testing.T) {
	old := Schema{{Name: "a0", SourceTableIdx: 1}, {Name: "b1", SourceTableIdx: 2}}

	assertSpineConsumesIdentityBoundaryMap(old, append(Schema(nil), old...))

	etRecoverContains(t, "did not reproduce the binding concatenation", func() {
		assertSpineConsumesIdentityBoundaryMap(old, old[:1])
	})
	etRecoverContains(t, "the boundary map is not the identity", func() {
		assertSpineConsumesIdentityBoundaryMap(old, Schema{old[1], old[0]})
	})
	// Same names, different source: a self-join's two aliases swapped is a
	// permutation the name-only comparison would have called identical.
	etRecoverContains(t, "the boundary map is not the identity", func() {
		assertSpineConsumesIdentityBoundaryMap(
			Schema{{Name: "n_name", SourceTableIdx: 1}, {Name: "n_name", SourceTableIdx: 2}},
			Schema{{Name: "n_name", SourceTableIdx: 2}, {Name: "n_name", SourceTableIdx: 1}})
	})
}

// etPinnedSpine builds the tree `runJoinSearchBelowPinned` descends: a retained
// Filter over a pinned Semi join whose Left is `Filter{pred}(origChain)`.
//
// `origChain` is handed in already tagged, which models the post-P5.5-f wiring:
// with `ctx == nil` the historical DP block inside `runJoinSearchBelowPinned` is
// a no-op, so what the spine sees spliced beneath it is exactly the searched
// root. That is the only part of the future wiring these tests need, and it is
// the part the two assertions are about.
func etPinnedSpine(t *testing.T, retained Expr) (root Node, chain Node, semi *Join) {
	t.Helper()
	chain = etSearched(t)
	below := &Filter{Child: chain, Predicate: etCol(0, "a0")}
	semi = &Join{
		Type:     JoinTypeSemi,
		Algo:     JoinAlgoHash,
		Left:     below,
		Right:    etScan("c", 2),
		LeftKey:  etCol(2, "a0"), // deliberately mis-bound: `a0` lives at 0
		RightKey: etCol(5, "c0"),
		schema:   append(Schema(nil), below.Output()...),
	}
	return &Filter{Child: semi, Predicate: retained}, chain, semi
}

// TestPinnedSpineSkipsReresolutionOverASearchedSubtree is the `predp.go` half
// end to end: with a searched subtree spliced below the pinned spine, the spine
// re-resolution must be skipped — and skipped PROVABLY, via the asserted
// identity of the boundary map, rather than by the accident of `layoutPosMap`
// returning nil.
//
// The skip is made observable the same way P5.5-f-ii-a made its skips
// observable: the spine join's left key is left pointing at a column whose name
// lives elsewhere, so `reresolveJoinByName` WOULD move it. The second half of
// the test runs that pass directly to prove the key was movable all along.
func TestPinnedSpineSkipsReresolutionOverASearchedSubtree(t *testing.T) {
	root, chain, semi := etPinnedSpine(t, etCol(1, "a1"))

	out := runJoinSearchBelowPinned(root, chain, nil, nil)
	if out != root {
		t.Fatalf("runJoinSearchBelowPinned returned %T, want the spine root unchanged", out)
	}
	if got := semi.LeftKey.(*ColumnRef).Index; got != 2 {
		t.Errorf("the pinned spine's key was re-resolved to %d; over a searched subtree the boundary map is the identity and nothing may be rebound", got)
	}

	// …and it really was movable: the skip is a behavioural difference, not a
	// no-op dressed up as one.
	reresolveJoinByName(semi)
	if got := semi.LeftKey.(*ColumnRef).Index; got != 0 {
		t.Errorf("reresolveJoinByName left the key at %d; this test no longer demonstrates the skip", got)
	}
}

// TestPinnedSpineRunsTheTripwireOverTheWholeEnclosingTree: the widened
// assertion is reached from the real call site, not only from tests. The
// retained Filter above the pinned semi join references column 5 of the
// 5-column row that join publishes — the M0097-0058 shape, one node above the
// search boundary, which the boundary-node-only version of the tripwire could
// not see.
func TestPinnedSpineRunsTheTripwireOverTheWholeEnclosingTree(t *testing.T) {
	root, chain, _ := etPinnedSpine(t, etCol(5, "b2"))

	etRecoverContains(t, "references column 5", func() {
		runJoinSearchBelowPinned(root, chain, nil, nil)
	})
}
