package optimizer

// C-20a (P6-01) — goopg has TWO join cardinality estimators, and this file is
// the falsifiable record of what that costs.
//
// The item as written in TODO_ALL says "delete legacy `estimateJoin` /
// `EstimateRows` + the `joinkeyproof.go` mirror; everything reads
// `calcJoinrelSize`". The census in
// analysis/planner-refactor-take3/c20a-estimator-census-20260907/DESIGN.md
// found that none of the three deletions is available yet, and the reason is
// structural rather than a matter of remaining call sites:
//
//   - `calcJoinrelSize` is a `searchCtx` method over `*RelOptInfo`, reachable
//     only from inside the join search. `EstimateRows` is a walker over the
//     PLAN `Node` tree, and every consumer outside the search — EXPLAIN, the
//     executor's hash-table geometry, the correlated-subquery cache budget,
//     the whole legacy upper-planner region — has a Node and no RelOptInfo.
//     A deletion is not a call-site migration; it needs the path's number to
//     have reached the node first, which is what the item calls the "P0-02
//     remainder".
//
//   - `joinkeyproof.go` is not a mirror. Its `resolveBaseColumn` is the base
//     column resolver for selectivity.go, extstats.go and estimateNumGroups,
//     and its `columnsSubset` is a live dependency of C-05's own
//     `joinrelsize.go`. Only `superkeyJoinEstimate` belongs to `estimateJoin`.
//
// What the two tests below pin is the exposure the pair creates. Every
// estimate artefact in this repository — EXPLAIN's `rows=`, `make plan-gate
// MODE=semantic-cost`, the est-vs-actual tables in the c13a census, and the
// new EA ratchet — reads the number `EstimateRows` produced, never the
// `PlanCost.PlanRows` the winning path carried (executor's
// `explainCostFields` takes StartupCost/TotalCost/PlanWidth from the carrier
// and leaves PlanRows unread). So when the search built the plan, the number
// every artefact reports is NOT the number the planner decided with. Nothing
// checks that the two agree.
//
// They do agree on the shapes tested here, which is the point: the agreement
// is a coincidence of two independent implementations, not a guarantee, and
// these tests are what makes a future divergence loud instead of silent.
// When C-20a becomes executable — after EXPLAIN's `rows=` comes from the
// path — `TestTwoJoinEstimatorsAgreeOnSuperkeyEvidence` becomes trivially
// true by construction, and that is the item's exit criterion. It is also
// why the item must not be closed by deleting the code this file names: the
// agreement would then be unobserved rather than established.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// twoEstJoin builds the plan-tree spelling of the same two-relation join
// `jrsCtx` sets up for the search: `lineitem ⋈ partsupp` on the equated
// column pairs given, over the SAME catalog.Table values, so the two
// estimators are reading one set of statistics and any divergence is theirs
// and not the fixture's.
//
// `UniqueKeys` is populated here for the same reason planner.go populates it
// at every scan-construction site (planner.go:3522, :9909, :9989, :10100,
// :11107): `estimateJoin` has no catalog handle, so `resolveBaseColumn` reads
// a relation's uniqueness evidence off the NODE. A fixture that omits it does
// not fail — it silently falls back to the marginal product, which is exactly
// the 2500x "divergence" this test reported on its first run before the
// fixture was corrected.
func twoEstJoin(c catalog.Catalog, lineitem, partsupp *catalog.Table, pairs [][2]int) *Join {
	l := &SeqScan{Table: lineitem, schema: tableSchema(lineitem),
		UniqueKeys: uniqueKeyColumnSets(c, lineitem)}
	r := &SeqScan{Table: partsupp, schema: tableSchema(partsupp),
		UniqueKeys: uniqueKeyColumnSets(c, partsupp)}
	j := mergedJoin(JoinTypeInner, l, r)
	leftWidth := len(l.Output())
	for _, p := range pairs {
		lk := &ColumnRef{Index: p[0], Name: lineitem.Columns[p[0]].Name,
			Type: catalog.Type{Name: "int4"}}
		rk := &ColumnRef{Index: leftWidth + p[1], Name: partsupp.Columns[p[1]].Name,
			Type: catalog.Type{Name: "int4"}}
		j.HashKeys = append(j.HashKeys, JoinKeyPair{Left: lk, Right: rk})
		if j.LeftKey == nil {
			j.LeftKey, j.RightKey = lk, rk
		}
	}
	return j
}

// TestTwoJoinEstimatorsAgreeOnThePlainEqjoinselShape is the control.
//
// One non-key equality, no superkey to prove, no FK: both estimators reduce to
// PG's `outer × inner × 1/max(nd_l, nd_r)` and there is nothing for them to
// disagree about. Without this control the divergence test below would be
// consistent with "the two fixtures are not the same join", which is the
// failure mode that makes a divergence claim worthless.
func TestTwoJoinEstimatorsAgreeOnThePlainEqjoinselShape(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	searchRows, _ := s.calcJoinrelSize(c, outer, inner,
		[]*restrictInfo{jrsEq("l_partkey", "ps_partkey", noEquivClass)}, nil)
	legacyRows := estimateJoin(twoEstJoin(c, lineitem, partsupp, [][2]int{{0, 0}}))

	if searchRows != float64(legacyRows) {
		t.Fatalf("control shape diverges: calcJoinrelSize=%v estimateJoin=%d\n"+
			"the two fixtures no longer describe the same join, so the "+
			"divergence test below proves nothing", searchRows, legacyRows)
	}
}

// TestTwoJoinEstimatorsAgreeOnSuperkeyEvidence is the load-bearing one.
//
// Both columns of `partsupp`'s COMPOSITE primary key are equated, so each
// `lineitem` row matches at most one `partsupp` row and the join cannot fan
// out. `calcJoinrelSize` (C-05, joinrelsize.go) recognises this: it removes
// the two clauses and charges one 1/raw-tuples factor, landing on exactly the
// outer's 6,000,000 rows — PG's `get_foreign_key_join_selectivity` shape
// applied to unique-index evidence.
//
// `estimateJoin` reaches the same conclusion through `superkeyJoinEstimate`
// in joinkeyproof.go — the "mirror" the item wants deleted — and it is
// deliberately NOT the same code: a different coordinate space, a different
// clause list, no shared function. Which of the two numbers a reader sees
// depends only on whether they read the plan or the planner, and today
// nothing but this test observes that they match.
//
// The assertion is written as "record whatever each says, and fail loudly the
// moment either moves" rather than as two literals, because pinning a literal
// once already hid a calibration worth 27% of the TPC-H suite. Both sides are
// expressed through the fixtures' own inputs.
func TestTwoJoinEstimatorsAgreeOnSuperkeyEvidence(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(6000000, 800000)

	clauses := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	searchRows, _ := s.calcJoinrelSize(c, outer, inner, clauses, nil)
	legacyRows := estimateJoin(twoEstJoin(c, lineitem, partsupp, [][2]int{{0, 0}, {1, 1}}))

	// The search's answer is the no-fan-out one: the outer, unchanged.
	const outerRows = 6000000.0
	if searchRows != outerRows {
		t.Fatalf("calcJoinrelSize=%v, want the outer's %v (C-05's composite "+
			"unique-key no-fan-out rule)", searchRows, outerRows)
	}

	// The marginal product is what BOTH estimators exist to avoid: the two
	// per-column selectivities multiplied independently, which is wrong by
	// six orders of magnitude on this shape.
	marginal := clampRowEst(6000000.0 * 800000.0 / 200000.0 / 10000.0)
	if float64(legacyRows) <= marginal {
		t.Fatalf("estimateJoin=%d has fallen back to the marginal product "+
			"(%v) — superkeyJoinEstimate is no longer firing, and deleting "+
			"joinkeyproof.go's proof would now be a silent 6-order regression",
			legacyRows, marginal)
	}

	// And the record: today the two are equal on this shape, by two entirely
	// separate implementations. That equality is a coincidence of one fixture,
	// not a guarantee — the two functions share no code, no coordinate space
	// and no clause list — and it is the property C-20a must make structural
	// instead of coincidental. If this ever fails, do NOT relax it: it means
	// the planner and EXPLAIN have started reporting different cardinalities
	// for the same join, and every est-vs-actual artefact in the tree becomes
	// ambiguous about which estimator it measured.
	if searchRows != float64(legacyRows) {
		t.Fatalf("THE TWO ESTIMATORS NOW DISAGREE on the superkey shape: "+
			"calcJoinrelSize=%v (what the planner decides with) vs "+
			"estimateJoin=%d (what EXPLAIN, the executor's hash geometry and "+
			"every estimate artefact report). See C-20a: "+
			"analysis/planner-refactor-take3/c20a-estimator-census-20260907/",
			searchRows, legacyRows)
	}
}
