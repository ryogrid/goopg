package optimizer

// M0127-P5.4b-ii-b-1 — the NLI arm (joinpathsnli.go) and the pairwise-union
// parameterisations that feed it (pathparamindex.go).
//
// The arm is LIVE in the same sense P5.4b-ii-a's paths are, since M0127-P5.9
// (2026-08-06): `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the
// search, so these tests are no longer the only thing that can falsify it —
// they are the only thing that can falsify it CHEAPLY. What they pin, in
// order: that the arm
// closes the hole P5.4b-i opened, that it costs the rescan from the inner PATH
// rather than from the inner REL (the mis-costing PG moves the pair here to
// avoid), that a clause already enforced as an index qual is not charged twice
// at the join, which parameterisations are admitted and which are deferred, and
// that a composite index bound from two different outer rels now gets a path at
// all.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// nliInnerRel is an inner relation carrying BOTH representations of itself: an
// expensive unparameterised seq scan (what a plain nested loop would rescan)
// and a cheap parameterised index path (what the NLI arm probes).
func nliInnerRel(relids RelSet, rows float64, param RelSet, probeCost float64) *RelOptInfo {
	return nliInnerRelBinding(relids, rows, param, probeCost)
}

// nliInnerRelBinding is nliInnerRel with the clauses the probe ENFORCES named
// (M0127-P5.5-e-ii-b). The real producer records them in `Path.IndexClauses`,
// and `nestloopResidualClauses` now drops only those — so a helper that left the
// list empty would model a probe that enforces nothing, which is a different
// path from the one under test.
func nliInnerRelBinding(relids RelSet, rows float64, param RelSet, probeCost float64, enforced ...*restrictInfo) *RelOptInfo {
	rel := newRelOptInfo(relids, rows, 32)
	generateScanPaths(rel, defaultCostParams(), estScanPages(rows, 32), 0, 0, true)
	cls := make([]indexPathClause, 0, len(enforced))
	for i, ri := range enforced {
		cls = append(cls, indexPathClause{ri: ri, indexCol: i, key: &ColumnRef{Index: 0}})
	}
	addPath(rel, &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		Rows:          1,
		Cost:          Cost{Total: probeCost},
		IndexClauses:  cls,
		RequiredOuter: param,
	})
	setCheapest(rel)
	return rel
}

// TestNLIArmCostsTheRescanFromTheInnerPath is the arm's whole reason to exist,
// and the reason PG moves this pair out of the plain-nestloop arm (joinpath.c:1874)
// rather than costing it there.
//
// The same two relations, joined the same way, are priced twice: once as a plain
// nested loop over the inner's unparameterised representative, and once as an
// NLI over its parameterised one. The rescan cost is the inner PATH's total in
// both cases — which for the seq scan is a full scan of a million rows per
// outer row, and for the index path is one probe. The gap is not a tuning
// difference; it is the difference between a plan you would never choose and
// the one PG chooses.
func TestNLIArmCostsTheRescanFromTheInnerPath(t *testing.T) {
	cp := defaultCostParams()
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)

	outer := scanRel(outerRelids, 100, estScanPages(100, 32))
	inner := nliInnerRel(innerRelids, 1_000_000, outerRelids, indexProbeCost(cp))
	joinrel := newRelOptInfo(outerRelids|innerRelids, 100, 64)

	// The inner's cheapest-total is the seq scan (a parameterised path can
	// never win that slot, 03 §9 rule 1), so the plain-NL arm is reachable and
	// both paths land in the same pathlist.
	if inner.CheapestTotal.RequiredOuter != 0 {
		t.Fatal("rule 1 broken: a parameterised path took CheapestTotal")
	}
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}

	// Both nested loops are generated; the plain one is then eliminated by
	// `addPath`'s tournament, because the two have the SAME (empty)
	// parameterisation and the NLI is strictly cheaper. That elimination is
	// the observable outcome, so assert on it rather than on the intermediate.
	if len(joinrel.Pathlist) != 1 {
		t.Fatalf("got %d paths, want 1: the plain nested loop must be dominated away", len(joinrel.Pathlist))
	}
	nli := joinrel.Pathlist[0]
	if nli.Kind != PathNestLoop || nli.Children[1].RequiredOuter != outerRelids {
		t.Fatal("the surviving path must be the nested loop over the PARAMETERISED inner")
	}
	if nli.RequiredOuter != 0 {
		t.Fatalf("got RequiredOuter %#04b, want 0: the nested loop supplies the parameter itself", nli.RequiredOuter)
	}

	// The same pair, priced with the parameterised path withheld, is what the
	// plain-NL arm alone produces — the number the NLI has to beat.
	bare := scanRel(innerRelids, 1_000_000, estScanPages(1_000_000, 32))
	plainJoinrel := newRelOptInfo(outerRelids|innerRelids, 100, 64)
	if err := addPathsToJoinrel(nil, plainJoinrel, outer, bare, nil, cp); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	plain := plainJoinrel.Pathlist[0]
	if plain.Kind != PathNestLoop {
		t.Fatalf("fixture: want a plain nested loop, got kind %v", plain.Kind)
	}
	if nli.Cost.Total >= plain.Cost.Total {
		t.Fatalf("NLI total %.1f is not below the plain nested loop's %.1f — the rescan is being costed from the rel, not the path",
			nli.Cost.Total, plain.Cost.Total)
	}
}

// TestNLIArmDoesNotDuplicateTheUnparameterizedMember: `set_cheapest` prepends
// the cheapest unparameterised path to `cheapest_parameterized_paths`
// (pathnode.c:375) so PG's single `foreach` covers the plain nested loop too.
// goopg reaches that member through `addNestLoopPath` instead, so the arm must
// skip it — otherwise every pair gets two identical plain nested loops through
// `addPath`'s tournament.
func TestNLIArmDoesNotDuplicateTheUnparameterizedMember(t *testing.T) {
	cp := defaultCostParams()
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)

	outer := scanRel(outerRelids, 100, estScanPages(100, 32))
	inner := scanRel(innerRelids, 1000, estScanPages(1000, 32))
	joinrel := newRelOptInfo(outerRelids|innerRelids, 5000, 64)

	if len(inner.CheapestParameterized) != 1 || inner.CheapestParameterized[0].RequiredOuter != 0 {
		t.Fatalf("fixture: want exactly the prepended unparameterised member, got %d", len(inner.CheapestParameterized))
	}
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	nestloops := 0
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathNestLoop {
			nestloops++
		}
	}
	if nestloops != 1 {
		t.Fatalf("got %d nested loops, want 1: the arm must not re-emit the unparameterised member", nestloops)
	}
	// Directly, so the assertion does not depend on how `addPath` breaks an
	// exact tie: the arm contributes NOTHING for an inner whose only
	// representative is unparameterised.
	direct := newRelOptInfo(outerRelids|innerRelids, 5000, 64)
	addNLIPaths(nil, direct, outer, inner, cp, nil)
	if len(direct.Pathlist) != 0 {
		t.Fatalf("the arm added %d paths for an unparameterised inner, want 0", len(direct.Pathlist))
	}
}

// TestNLIArmAdmission covers `try_nestloop_path`'s test (joinpath.c:882-889)
// and goopg's own sizer deferral behind it. The three shapes are genuinely
// different verdicts, not three spellings of one.
func TestNLIArmAdmission(t *testing.T) {
	cp := defaultCostParams()
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)

	run := func(t *testing.T, innerParam RelSet) int {
		t.Helper()
		outer := scanRel(a, 100, estScanPages(100, 32))
		inner := nliInnerRel(b, 1000, innerParam, indexProbeCost(cp))
		joinrel := newRelOptInfo(a|b, 100, 64)
		if err := addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
		n := 0
		for _, p := range joinrel.Pathlist {
			if p.Kind == PathNestLoop && p.Children[1].RequiredOuter != 0 {
				n++
			}
		}
		return n
	}

	t.Run("fully satisfied by the outer", func(t *testing.T) {
		if got := run(t, a); got != 1 {
			t.Fatalf("got %d NLI paths, want 1", got)
		}
	})

	t.Run("parameterised by a rel outside the join", func(t *testing.T) {
		// required_outer = {c}, which nothing in param_source_rels wants and
		// which allow_star_schema_join refuses because the outer supplies
		// NONE of it. PG rejects this too.
		if got := run(t, c); got != 0 {
			t.Fatalf("got %d NLI paths, want 0: {c} is not supplied by this join", got)
		}
	})

	t.Run("star-schema: partially satisfied", func(t *testing.T) {
		// PG ACCEPTS this one (allow_star_schema_join) and carries a join path
		// still parameterised by {c}. goopg declines only because such a path
		// needs `get_parameterized_joinrel_size` for its ppi_rows, which is
		// P5.6's sizer — the rule below is already correct.
		if !allowStarSchemaJoin(a, a|c) {
			t.Fatal("allow_star_schema_join must accept a partially-supplied inner")
		}
		if got := run(t, a|c); got != 0 {
			t.Fatalf("got %d NLI paths, want 0 until P5.6's joinrel sizer lands", got)
		}
	})

	t.Run("allow_star_schema_join rejects the extremes", func(t *testing.T) {
		if allowStarSchemaJoin(a, a) {
			t.Fatal("a fully-supplied inner is not a star-schema case — required_outer is empty")
		}
		if allowStarSchemaJoin(a, c) {
			t.Fatal("a wholly-unsupplied inner is not a star-schema case")
		}
	})
}

// TestNLIArmDropsClausesMovableIntoTheInner is `create_nestloop_path`'s
// restrict-clause drop (pathnode.c:2478-2500). The clause that parameterised
// the inner is being enforced down there as an index qual; carrying it at the
// join too would charge `cost_qual_eval` for it on the full cross product,
// which is precisely the charge that would keep the NLI from winning.
//
// M0127-P5.5-e-ii-b narrowed the drop from "movable" to "movable AND actually
// in the probe's `IndexClauses`", because goopg's parameterised inner enforces
// nothing else — see `nestloopResidualClauses`' doc.
func TestNLIArmDropsClausesMovableIntoTheInner(t *testing.T) {
	cp := defaultCostParams()
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)

	bound := equiClause(a, b)       // a.x = b.y — became the index qual
	local := plainClause(b)         // a qual on the inner alone
	elsewhere := plainClause(b | c) // references a rel the inner cannot see

	probe := &Path{IndexClauses: []indexPathClause{{ri: bound, key: &ColumnRef{}}}}
	got := nestloopResidualClauses([]*restrictInfo{bound, local, elsewhere}, probe, b, a)
	// `local` SURVIVES: it is movable, but goopg's `*IndexScan` has no qual
	// field and `addParameterizedIndexPaths` only builds over a leaf with no
	// `*Filter` above it, so nothing below this join applies it. PG would drop
	// it (its scan's qpqual would carry it); goopg dropping it would delete the
	// restriction from the plan.
	if len(got) != 2 || got[0] != local || got[1] != elsewhere {
		t.Fatalf("residual = %d clauses, want the inner-local one and the {c} one", len(got))
	}

	// And the same list at a plain nested loop, where the inner is
	// unparameterised: nothing has been pushed down, so nothing is dropped.
	if got := nestloopResidualClauses([]*restrictInfo{bound}, nil, b, 0); len(got) != 1 {
		t.Fatal("an unparameterised inner cannot enforce a two-rel clause, so it must stay residual")
	}

	// A probe that enforces NOTHING drops nothing, even though the clause is
	// movable — the whole point of the narrowing.
	if got := nestloopResidualClauses([]*restrictInfo{bound}, &Path{}, b, a); len(got) != 1 {
		t.Fatal("a probe with no index clauses enforces nothing, so a movable clause must stay residual")
	}

	// The cost consequence, measured rather than asserted by inspection.
	outer := scanRel(a, 100, estScanPages(100, 32))
	inner := nliInnerRelBinding(b, 1000, a, indexProbeCost(cp), bound)
	withDrop := newRelOptInfo(a|b, 100, 64)
	if err := addPathsToJoinrel(nil, withDrop, outer, inner, []*restrictInfo{bound}, cp); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	for _, p := range withDrop.Pathlist {
		if p.Kind == PathNestLoop && p.Children[1].RequiredOuter != 0 && len(p.Residual) != 0 {
			t.Fatalf("NLI path kept %d residual clauses, want 0: the index qual is not a join qual", len(p.Residual))
		}
	}
}

// TestJoinClauseIsMovableInto pins the two surviving halves of
// `join_clause_is_movable_into` (restrictinfo.c:610) separately, because they
// reject for different reasons and one of them is easy to write as the other.
func TestJoinClauseIsMovableInto(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)

	if !joinClauseIsMovableInto(equiClause(a, b), b, a|b) {
		t.Fatal("a.x = b.y must be movable into a scan of {b} parameterised by {a}")
	}
	if joinClauseIsMovableInto(plainClause(a|c), b, a|b) {
		t.Fatal("a clause that does not reference {b} at all is not a placement there, it is an invention")
	}
	if joinClauseIsMovableInto(plainClause(b|c), b, a|b) {
		t.Fatal("{c} is not available at that scan, so the clause cannot be evaluated there")
	}
}

// TestConsideredParameterizationsGeneratesPairwiseUnions is
// `consider_index_join_outer_rels` (indxpath.c:531-583). The union sets are the
// only way a composite index whose columns come from two different outer rels
// is ever fully bound.
func TestConsideredParameterizationsGeneratesPairwiseUnions(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	cand := func(outer RelSet, ec int) paramIndexClause {
		return paramIndexClause{ri: &restrictInfo{ecID: ec}, outerRels: outer}
	}

	// PG's order: for each clause, its unions with every set already in the
	// list — INCLUDING unions contributed by earlier clauses, which is how
	// {a,b,c} appears without any three-way rule — then the clause's own set.
	got := consideredParameterizations([]paramIndexClause{
		cand(a, noEquivClass), cand(b, noEquivClass), cand(c, noEquivClass),
	})
	want := []RelSet{a, a | b, b, a | c, a | b | c, b | c, c}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// A repeated outer relset is considered once (PG's `list_member` guard).
	if got := consideredParameterizations([]paramIndexClause{
		cand(a, noEquivClass), cand(a, noEquivClass),
	}); len(got) != 1 || got[0] != a {
		t.Fatalf("got %v, want exactly [{a}]", got)
	}

	// Two clauses from the SAME equivalence class describe the same
	// restriction, so combining them cannot produce a usefully different
	// parameterisation — only a more-parameterised path with the same key set.
	if got := consideredParameterizations([]paramIndexClause{
		cand(a, 7), cand(b, 7),
	}); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("got %v, want [{a} {b}] with no union", got)
	}
}

// TestParameterizedIndexPathsBindCompositeIndexFromTwoOuterRels is the union
// rule end to end, on the shape that motivates it: the composite-FK probe.
// `lineitem(l_partkey, l_suppkey)` is equated to `part` and to `supplier`
// separately, so no SINGLE outer rel binds the whole index key —
// `pickIndexCoveringAllLeadingColumns` correctly declines both singletons — and
// only the union {part, supplier} yields a path.
func TestParameterizedIndexPathsBindCompositeIndexFromTwoOuterRels(t *testing.T) {
	c := catalog.NewInMemory()
	lineitem, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "lineitem_ps_idx"},
		lineitem, []string{"l_partkey", "l_suppkey"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	part, supplier, fact := relsetOf(0), relsetOf(1), relsetOf(2)
	s, err := newSearchCtx(3, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, rows := range []float64{200, 100, 600_000} {
		rel := newRelOptInfo(RelSet(1)<<uint(i), rows, 32)
		if err := s.addRel(rel); err != nil {
			t.Fatal(err)
		}
		generateScanPaths(rel, s.cp, estScanPages(rows, 32), 0, 0, true)
		setCheapest(rel)
	}
	s.relInfos = []baseRelInfo{{}, {}, {table: lineitem}}
	s.levelRels(1)[2].baseLeaf = &SeqScan{Table: lineitem}
	s.clauses = &restrictInfoList{all: []*restrictInfo{
		ppiEquiClause(part, "p_partkey", fact, "l_partkey"),
		ppiEquiClause(supplier, "s_suppkey", fact, "l_suppkey"),
	}}

	s.addParameterizedIndexPaths(c)

	factRel := s.levelRels(1)[2]
	var params []RelSet
	for _, p := range factRel.Pathlist {
		if p.RequiredOuter != 0 {
			params = append(params, p.RequiredOuter)
		}
	}
	if len(params) != 1 || params[0] != part|supplier {
		t.Fatalf("got parameterisations %v, want exactly [{part,supplier}]: neither singleton binds both index columns", params)
	}
}
