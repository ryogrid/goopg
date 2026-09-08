package optimizer

// E-20 Cut 3 pins (`try_partial_mergejoin_path`, joinpath.c:1145-1214, at
// both PG sites). Properties, in test order:
//
//	(1) site 1 offers a partial merge for a pre-ordered partial outer and a
//	    pre-ordered safe inner, priced from the per-worker outer and the
//	    whole inner with the divided row estimate — and files nothing when
//	    either side would need a sort (the no-sort gate);
//	(2) site 2 offers the first candidate for an ordered partial outer and
//	    declines an unordered one;
//	(3) every refusal files nothing (mode off, nil search, no partial outer,
//	    unsafe/unparallel outer, parameterised inputs);
//	(4) the executor predicate admits INNER/SEMI/ANTI/LEFT merges and
//	    refuses FULL/RIGHT, non-merge algos and lateral;
//	(5) the driving-kind walk bottoms a partial merge out in its outer's
//	    scan and refuses a sorted outer;
//	(6) createPlan's fail-closed assert fires exactly for an unrunnable
//	    partial merge.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// pmjFixture is two base rels joined on one equi-clause, with hand-built
// ordered paths: the outer carries a partial path already ordered by the
// merge key, the inner a complete path ordered by its side. Both sides
// pre-ordered is what the no-sort gate demands — a sort under a partial
// path is correct but unmodelled (no PathSort arm in partialPathDrivingKind,
// no *Sort arm in drivingScan), so the producer declines it rather than
// pricing a path the Gather could never run.
func pmjFixture(t *testing.T) (*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo, []*restrictInfo) {
	t.Helper()
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 10000, 100), scanRel(b, 5000, 50)
	joinrel := newRelOptInfo(a|b, 2000, 64)
	joinrel.ConsiderParallel = true
	keys := []*restrictInfo{equiClauseOn(a, b, 1, 2)}

	outer.PartialPathlist = []*Path{{
		Kind:            PathSeqScan,
		Rel:             outer,
		Rows:            2500,
		Cost:            Cost{Total: 100},
		Pathkeys:        []PathKey{{Expr: col(1), SortAsc: true}},
		ParallelWorkers: 4,
		ParallelSafe:    true,
	}}
	inner.Pathlist = []*Path{{
		Kind:         PathSeqScan,
		Rel:          inner,
		Rows:         5000,
		Cost:         Cost{Total: 50},
		Pathkeys:     []PathKey{{Expr: col(2), SortAsc: true}},
		ParallelSafe: true,
	}}

	prob := phjProblem(2_000_000, 10)
	s, _ := phjJoinrel(t, prob, gatherPathsAll)
	t.Cleanup(setGatherPathsModeForTest(gatherPathsAll))
	return s, joinrel, outer, inner, keys
}

func pmjClosures(s *searchCtx, joinrel, outer, inner *RelOptInfo) (func([]*restrictInfo) float64, func([]*restrictInfo) (float64, float64)) {
	mergeTuplesFor := func(res []*restrictInfo) float64 {
		return s.mergeJoinTuples(joinrel.Rows, res, outer.Rows, inner.Rows)
	}
	scanSelFor := func(mc []*restrictInfo) (float64, float64) {
		return s.mergeJoinScanSel(mc, outer.Relids)
	}
	return mergeTuplesFor, scanSelFor
}

func pmjPartials(list []*Path) []*Path {
	var out []*Path
	for _, p := range list {
		if p.Kind == PathMergeJoin {
			out = append(out, p)
		}
	}
	return out
}

// (1) Site 1 offers the partial merge beside the serial one, priced from
// the per-worker outer and the whole inner.
func TestPartialMergeJoinSite1OffersPreorderedShapes(t *testing.T) {
	withParallelOn(t, func() {
		s, joinrel, outer, inner, keys := pmjFixture(t)
		mergeTuplesFor, scanSelFor := pmjClosures(s, joinrel, outer, inner)
		addPartialMergeJoinPath(s, joinrel, outer, inner, s.cp, parser.JoinInner,
			[]PathKey{{Expr: col(1), SortAsc: true}}, []PathKey{{Expr: col(1), SortAsc: true}}, []PathKey{{Expr: col(2), SortAsc: true}},
			keys, nil, mergeTuplesFor, scanSelFor, 0)

		got := pmjPartials(joinrel.PartialPathlist)
		if len(got) != 1 {
			t.Fatalf("site 1 filed %d partial merge paths, want 1", len(got))
		}
		p := got[0]
		if p.ParallelWorkers != 4 {
			t.Errorf("workers = %d, want the partial outer's 4 (pathnode.c:2800)", p.ParallelWorkers)
		}
		if !p.ParallelSafe {
			t.Error("partial merge is not ParallelSafe")
		}
		if p.ParallelAware {
			t.Error("ParallelAware set: no shared prebuild exists for merge")
		}
		wantRows := clampRowEst(joinrel.Rows / getParallelDivisor(4, s.cp.parallelLeaderParticipation))
		if p.Rows != wantRows {
			t.Errorf("Rows = %v, want clamp(joinrel.Rows/divisor) = %v (costsize.c:3875-3881)", p.Rows, wantRows)
		}
		if len(p.Pathkeys) != 1 || !exprEqual(p.Pathkeys[0].Expr, col(1)) {
			t.Errorf("Pathkeys = %+v, want the delivered outer ordering", p.Pathkeys)
		}
		if len(p.Children) != 2 || p.Children[0].Kind != PathSeqScan {
			t.Fatalf("outer child is %v, want the partial scan (not a Sort: the no-sort gate)", p.Children[0].Kind)
		}
		// The filed shape must be drivable end to end, or the Gather that
		// reads it mis-executes.
		if !partialPathShapeIsGatherable(p) {
			t.Error("filed partial merge is not gatherable by its own producer's gate")
		}
	})
}

// (1b) The no-sort gate: an outer that does not deliver the keys files
// nothing, even though the serial arm would sort it.
func TestPartialMergeJoinSite1DeclinesUnsortedOuter(t *testing.T) {
	withParallelOn(t, func() {
		s, joinrel, outer, inner, keys := pmjFixture(t)
		outer.PartialPathlist[0].Pathkeys = nil
		mergeTuplesFor, scanSelFor := pmjClosures(s, joinrel, outer, inner)
		addPartialMergeJoinPath(s, joinrel, outer, inner, s.cp, parser.JoinInner,
			[]PathKey{{Expr: col(1), SortAsc: true}}, []PathKey{{Expr: col(1), SortAsc: true}}, []PathKey{{Expr: col(2), SortAsc: true}},
			keys, nil, mergeTuplesFor, scanSelFor, 0)
		if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
			t.Fatalf("unordered partial outer filed %d paths; a sort under a partial is unmodelled", len(got))
		}
	})
}

// (2) Site 2 offers the first candidate for an ordered partial outer and
// declines an unordered one. resultKeys is the outer's FULL ordering.
func TestPartialMergeJoinSite2FirstCandidate(t *testing.T) {
	withParallelOn(t, func() {
		s, joinrel, outer, inner, keys := pmjFixture(t)
		mergeTuplesFor, scanSelFor := pmjClosures(s, joinrel, outer, inner)
		matchUnsortedOuterMergePartial(s, joinrel, outer, inner, s.cp, parser.JoinInner,
			keys, nil, mergeTuplesFor, scanSelFor, 0)
		got := pmjPartials(joinrel.PartialPathlist)
		if len(got) != 1 {
			t.Fatalf("site 2 filed %d partial merge paths, want 1", len(got))
		}
		if len(got[0].Pathkeys) != 1 || !exprEqual(got[0].Pathkeys[0].Expr, col(1)) {
			t.Errorf("result keeps %+v, want the outer's full ordering", got[0].Pathkeys)
		}

		// The cheapest partial is almost never the ORDERED one (a partial
		// seq scan prices below a partial index scan), so site 2 must look
		// past PartialPathlist[0]: an unordered cheapest with an ordered
		// second still offers.
		joinrel.PartialPathlist = nil
		outer.PartialPathlist = append([]*Path{{
			Kind:            PathSeqScan,
			Rel:             outer,
			Rows:            10000,
			Cost:            Cost{Total: 10},
			ParallelWorkers: 4,
			ParallelSafe:    true,
		}}, outer.PartialPathlist...)
		matchUnsortedOuterMergePartial(s, joinrel, outer, inner, s.cp, parser.JoinInner,
			keys, nil, mergeTuplesFor, scanSelFor, 0)
		if got := pmjPartials(joinrel.PartialPathlist); len(got) != 1 {
			t.Fatalf("ordered partial at [1] filed %d paths, want 1", len(got))
		}

		joinrel.PartialPathlist = nil
		for _, p := range outer.PartialPathlist {
			p.Pathkeys = nil
		}
		matchUnsortedOuterMergePartial(s, joinrel, outer, inner, s.cp, parser.JoinInner,
			keys, nil, mergeTuplesFor, scanSelFor, 0)
		if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
			t.Fatalf("fully unordered partial outers filed %d paths at site 2", len(got))
		}
	})
}

// (3) Every refusal files nothing.
func TestPartialMergeJoinRefusals(t *testing.T) {
	withParallelOn(t, func() {
		build := func(t *testing.T) (*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo, []*restrictInfo) {
			t.Helper()
			s, joinrel, outer, inner, keys := pmjFixture(t)
			if n := len(pmjPartials(joinrel.PartialPathlist)); n != 0 {
				t.Fatalf("fixture joinrel carries %d partial merges before the test", n)
			}
			return s, joinrel, outer, inner, keys
		}
		offer := func(s *searchCtx, joinrel, outer, inner *RelOptInfo, keys []*restrictInfo) {
			mergeTuplesFor, scanSelFor := pmjClosures(s, joinrel, outer, inner)
			ok := []PathKey{{Expr: col(1), SortAsc: true}}
			ik := []PathKey{{Expr: col(2), SortAsc: true}}
			addPartialMergeJoinPath(s, joinrel, outer, inner, s.cp, parser.JoinInner,
				ok, ok, ik, keys, nil, mergeTuplesFor, scanSelFor, 0)
		}

		t.Run("mode off", func(t *testing.T) {
			s, joinrel, outer, inner, keys := build(t)
			restore := setGatherPathsModeForTest(gatherPathsOff)
			t.Cleanup(restore)
			offer(s, joinrel, outer, inner, keys)
			if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
				t.Fatalf("mode off filed %d paths", len(got))
			}
		})
		t.Run("nil search", func(t *testing.T) {
			_, joinrel, outer, inner, keys := build(t)
			s, _, _, _, _ := build(t)
			mergeTuplesFor, scanSelFor := pmjClosures(s, joinrel, outer, inner)
			ok := []PathKey{{Expr: col(1), SortAsc: true}}
			addPartialMergeJoinPath(nil, joinrel, outer, inner, s.cp, parser.JoinInner,
				ok, ok, []PathKey{{Expr: col(2), SortAsc: true}}, keys, nil, mergeTuplesFor, scanSelFor, 0)
			if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
				t.Fatalf("nil search filed %d paths", len(got))
			}
		})
		t.Run("no partial outer", func(t *testing.T) {
			s, joinrel, outer, inner, keys := build(t)
			outer.PartialPathlist = nil
			offer(s, joinrel, outer, inner, keys)
			if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
				t.Fatalf("no partial outer filed %d paths", len(got))
			}
		})
		t.Run("unsafe outer", func(t *testing.T) {
			s, joinrel, outer, inner, keys := build(t)
			outer.PartialPathlist[0].ParallelSafe = false
			offer(s, joinrel, outer, inner, keys)
			if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
				t.Fatalf("unsafe outer filed %d paths", len(got))
			}
		})
		t.Run("parameterised outer", func(t *testing.T) {
			s, joinrel, outer, inner, keys := build(t)
			outer.PartialPathlist[0].RequiredOuter = relsetOf(7)
			offer(s, joinrel, outer, inner, keys)
			if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
				t.Fatalf("parameterised outer filed %d paths", len(got))
			}
		})
		t.Run("empty clauses", func(t *testing.T) {
			s, joinrel, outer, inner, _ := build(t)
			offer(s, joinrel, outer, inner, nil)
			if got := pmjPartials(joinrel.PartialPathlist); len(got) != 0 {
				t.Fatalf("empty clauses filed %d paths", len(got))
			}
		})
	})
}

// (4) The executor predicate: INNER/SEMI/ANTI/LEFT merges are worker-local;
// FULL/RIGHT need a cross-worker unmatched-inner reduction and are refused.
func TestMergeJoinIsPartialCapable(t *testing.T) {
	mk := func(algo JoinAlgo, jt JoinType, lateral bool) *Join {
		return &Join{Algo: algo, Type: jt, Lateral: lateral}
	}
	for _, tc := range []struct {
		name string
		j    *Join
		want bool
	}{
		{"inner", mk(JoinAlgoMerge, JoinTypeInner, false), true},
		{"semi", mk(JoinAlgoMerge, JoinTypeSemi, false), true},
		{"anti", mk(JoinAlgoMerge, JoinTypeAnti, false), true},
		{"left", mk(JoinAlgoMerge, JoinTypeLeft, false), true},
		{"full refused", mk(JoinAlgoMerge, JoinTypeFull, false), false},
		{"right refused", mk(JoinAlgoMerge, JoinTypeRight, false), false},
		{"hash algo refused", mk(JoinAlgoHash, JoinTypeInner, false), false},
		{"nested loop refused", mk(JoinAlgoNestedLoop, JoinTypeInner, false), false},
		{"lateral refused", mk(JoinAlgoMerge, JoinTypeInner, true), false},
		{"nil refused", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeJoinIsPartialCapable(tc.j); got != tc.want {
				t.Errorf("mergeJoinIsPartialCapable = %v, want %v", got, tc.want)
			}
		})
	}
}

// (5) The driving-kind walk bottoms a partial merge out in its outer's scan
// and refuses anything it cannot drive.
func TestPartialMergeJoinDrivingKind(t *testing.T) {
	scan := &Path{Kind: PathSeqScan}
	merge := &Path{Kind: PathMergeJoin, Children: []*Path{scan, {Kind: PathSeqScan}}}
	if got := partialPathDrivingKind(merge); got != PathSeqScan {
		t.Errorf("driving kind = %v, want the outer chain's seq scan", got)
	}
	if !partialPathShapeIsGatherable(merge) {
		t.Error("a scan-driven partial merge is not gatherable")
	}
	sorted := &Path{Kind: PathMergeJoin, Children: []*Path{{Kind: PathSort, Children: []*Path{scan}}, scan}}
	if got := partialPathDrivingKind(sorted); got != PathPrebuilt {
		t.Errorf("sorted-outer merge driving kind = %v, want Prebuilt (no PathSort arm)", got)
	}
	badKids := &Path{Kind: PathMergeJoin, Children: []*Path{scan}}
	if got := partialPathDrivingKind(badKids); got != PathPrebuilt {
		t.Errorf("one-child merge driving kind = %v, want Prebuilt", got)
	}
	param := &Path{Kind: PathMergeJoin, RequiredOuter: relsetOf(3), Children: []*Path{scan, scan}}
	if got := partialPathDrivingKind(param); got != PathPrebuilt {
		t.Errorf("parameterised merge driving kind = %v, want Prebuilt", got)
	}
}

// (6) createPlan's fail-closed assert: a partial merge building an unrunnable
// join panics LOUDLY instead of returning a silently partial join.
func TestPartialMergeJoinAssertFiresOnUnrunnable(t *testing.T) {
	// Serial merges (workers == 0) skip the check entirely.
	assertPartialMergeJoinIsRunnable(&Path{Kind: PathMergeJoin, ParallelWorkers: 0},
		&Join{Algo: JoinAlgoMerge, Type: JoinTypeFull})
	// A runnable partial merge passes.
	inner := &Path{Kind: PathSeqScan}
	assertPartialMergeJoinIsRunnable(
		&Path{Kind: PathMergeJoin, ParallelWorkers: 4, Children: []*Path{inner, inner}},
		&Join{Algo: JoinAlgoMerge, Type: JoinTypeInner})
	// An unrunnable one panics.
	defer func() {
		if r := recover(); r == nil {
			t.Error("partial FULL merge built without panic: workers would silently mis-execute it")
		}
	}()
	assertPartialMergeJoinIsRunnable(
		&Path{Kind: PathMergeJoin, ParallelWorkers: 4, Children: []*Path{inner, inner}},
		&Join{Algo: JoinAlgoMerge, Type: JoinTypeFull})
}

// (7) End-to-end filing through the production driver: the same ordered
// fixture run through the REAL addPathsToJoinrel (not the producer
// functions directly) files a partial merge join. This is what separates
// "the producer works when called" from "the search offers the shape" —
// the TPC-H parallel A/B moves zero plans, and this pin says why that is
// a missing SHAPE (no ordered partial outer aligns there), not a dead
// producer: here the shape exists and the filing happens.
func TestPartialMergeJoinFilesEndToEnd(t *testing.T) {
	withParallelOn(t, func() {
		a, b := relsetOf(0), relsetOf(1)
		outer, inner := scanRel(a, 10000, 100), scanRel(b, 5000, 50)
		joinrel := newRelOptInfo(a|b, 2000, 64)
		joinrel.ConsiderParallel = true
		keys := []*restrictInfo{equiClauseOn(a, b, 1, 2)}
		outer.PartialPathlist = []*Path{{
			Kind:            PathSeqScan,
			Rel:             outer,
			Rows:            2500,
			Cost:            Cost{Total: 100},
			Pathkeys:        []PathKey{{Expr: col(1), SortAsc: true}},
			ParallelWorkers: 4,
			ParallelSafe:    true,
		}}
		inner.Pathlist = []*Path{{
			Kind:         PathSeqScan,
			Rel:          inner,
			Rows:         5000,
			Cost:         Cost{Total: 50},
			Pathkeys:     []PathKey{{Expr: col(2), SortAsc: true}},
			ParallelSafe: true,
		}}
		setCheapest(outer)
		setCheapest(inner)
		prob := phjProblem(2_000_000, 10)
		s, _ := phjJoinrel(t, prob, gatherPathsAll)
		t.Cleanup(setGatherPathsModeForTest(gatherPathsAll))
		if err := addPathsToJoinrel(s, joinrel, outer, inner, append([]*restrictInfo{}, keys...), s.cp, nil); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
		got := pmjPartials(joinrel.PartialPathlist)
		if len(got) == 0 {
			t.Fatalf("end-to-end filed no partial merge (serial paths filed: %d)", len(joinrel.Pathlist))
		}
		if got[0].ParallelWorkers != 4 {
			t.Errorf("workers = %d, want 4", got[0].ParallelWorkers)
		}
		if !partialPathShapeIsGatherable(got[0]) {
			t.Error("end-to-end filed path is not gatherable")
		}
	})
}
