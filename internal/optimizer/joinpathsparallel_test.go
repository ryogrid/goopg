package optimizer

// C-19f / P5-06 pins (take3 08 §8;
// docs/design/planner-c19f-parallel-hashjoin/DESIGN.md §8). Eight properties,
// in the order that doc lists them:
//
//	(1) the cost is hashJoinCost over the PARTIAL outer, the COMPLETE inner and
//	    the per-worker output count — and the BUILD is charged ONCE, undivided.
//	    That last clause is the E-09a/E-09b pin: a reverted D-05 experiment
//	    charged a 5x participant multiplier derived from a sharing rule that no
//	    longer exists, and this test fails if anyone reintroduces it;
//	(2) Rows round-trip: the divisor final_cost_hashjoin applies and cost_gather
//	    undoes is ONE divisor, not two;
//	(3) BOTH candidates are generated before any cost is compared — five wrong
//	    hypotheses were burned on Q8 because a producer emitted nothing and the
//	    costs were never the question;
//	(4) the Gather-over-partial-hash-join wins exactly at the crossover the
//	    named constants define, and loses on the other side of it. THIS is the
//	    property C-19d could not obtain at a base rel at any relation size;
//	(5) partial paths PROPAGATE upward — a joinrel's partial path is the partial
//	    outer of the join above it, which is what lets one Gather sit over a
//	    whole join tree;
//	(6) every §4.2 refusal, each asserting NOTHING was filed;
//	(7) the parallel_aware flag's route to createPlan, both directions;
//	(8) mode `off` — the default — produces no partial join path at all.

import (
	"github.com/goopg/goopg/internal/parser"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// phjProblem is a two-relation fixture: `a` is a large ANALYZEd relation (well
// above min_parallel_table_scan_size, so create_plain_partial_paths sizes it a
// partial seq scan) equijoined to a small `b`. `bRows` steers the JOIN'S OUTPUT
// cardinality, which is the axis the crossover in §7.1 turns on.
func phjProblem(aRows, bRows int64) *joinlistProblem {
	names := []string{"a", "b"}
	rows := []int64{aRows, bRows}
	pages := []int{20_000, 1}
	prob := rfjProblem(names, rows, []Expr{rfjEq(names, 0, 1)})
	for i := range names {
		prob.relInfos[i].table.Stats = &catalog.TableStats{
			RowCount: rows[i], Pages: pages[i], Analyzed: true,
		}
	}
	return prob
}

// phjJoinrel runs the production protocol under a mode and returns the single
// level-2 joinrel.
func phjJoinrel(t *testing.T, prob *joinlistProblem, mode gatherPathMode) (*searchCtx, *RelOptInfo) {
	t.Helper()
	s := gpSearch(t, prob, mode)
	if len(s.joinrels) < 3 || len(s.joinrels[2]) != 1 {
		t.Fatalf("fixture produced %d level-2 joinrels, want exactly 1", len(s.joinrels[2]))
	}
	return s, s.joinrels[2][0]
}

func phjPathOfKind(list []*Path, k PathKind) *Path {
	for _, p := range list {
		if p.Kind == k {
			return p
		}
	}
	return nil
}

func phjClose(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

// (1) The cost identity, term by term through the named costParams fields, and
// the build charged ONCE.
//
// The two halves of "once" both need asserting and they fail differently:
//
//   - UNDIVIDED — the inner is a COMPLETE path, so its rows are the whole inner
//     relation. Upstream's parallel_hash arm has to multiply the count back up
//     for exactly this reason (costsize.c:4209-4210) in the variant goopg does
//     not have; dividing here would price a build the executor never performs.
//   - NOT PER-PARTICIPANT — because after E-09a/E-09b goopg performs it once:
//     `prebuildSharedHashJoins` runs the build in the leader and publishes it,
//     spilling builds included, and each batch is loaded once. The reverted
//     `tmp/d05p4-buildcost.patch` charged 5x here, derived from the
//     sharing-decline rule E-09a deleted. Reintroducing any participant
//     multiplier fails this test.
func TestPartialHashJoinChargesTheBuildOnceUndivided(t *testing.T) {
	withParallelOn(t, func() {
		prob := phjProblem(2_000_000, 10)
		s, joinrel := phjJoinrel(t, prob, gatherPathsAll)
		cp := s.cp

		pp := phjPathOfKind(joinrel.PartialPathlist, PathHashJoin)
		if pp == nil {
			t.Fatal("no partial hash join path on the joinrel; every pin below would be vacuous")
		}
		o, i := pp.Children[0], pp.Children[1]
		if o.ParallelWorkers <= 0 || !o.ParallelSafe {
			t.Fatalf("child[0] is not a partial path: workers=%d safe=%v", o.ParallelWorkers, o.ParallelSafe)
		}
		if i.ParallelWorkers != 0 {
			t.Fatalf("child[1] must be a COMPLETE path, got one planning %d workers", i.ParallelWorkers)
		}

		d := getParallelDivisor(o.ParallelWorkers, cp.parallelLeaderParticipation)
		wantRows := clampRowEst(joinrel.Rows / d)
		if pp.Rows != wantRows {
			t.Errorf("partial rows = %v, want clamp_row_est(%v / %v) = %v (final_cost_hashjoin:4307)",
				pp.Rows, joinrel.Rows, d, wantRows)
		}

		want := hashJoinCost(cp, hashJoinInputs{
			outer: o.Cost, inner: i.Cost,
			outerRows: o.Rows, innerRows: i.Rows,
			outputRows:      wantRows,
			numHashClauses:  len(pp.HashKeys),
			innerBucketSize: s.estimateHashBucketSize(pp.HashKeys, i.Rel.Relids),
			outerCols:       pathNCols(o), innerCols: pathNCols(i),
			outerAvgVarBytes: pathAvgVarBytes(o), innerAvgVarBytes: pathAvgVarBytes(i),
		})
		want.Total += qualEvalCost(cp, len(pp.Residual), wantRows)
		if !phjClose(pp.Cost.Startup, want.Startup) || !phjClose(pp.Cost.Total, want.Total) {
			t.Fatalf("partial hash cost = %+v, want %+v", pp.Cost, want)
		}

		// The build term, isolated: it is the SERIAL path's build term exactly,
		// with no divisor and no participant multiplier. Expressed through the
		// constants, never a literal.
		wantBuild := (cp.cpuOperatorCost*float64(len(pp.HashKeys))+cp.cpuTupleCost)*i.Rows + i.Cost.Total
		serial := phjPathOfKind(joinrel.Pathlist, PathHashJoin)
		if serial == nil {
			t.Fatal("no serial hash join path to compare the build term against")
		}
		si := serial.Children[1]
		serialBuild := (cp.cpuOperatorCost*float64(len(serial.HashKeys))+cp.cpuTupleCost)*si.Rows + si.Cost.Total
		if !phjClose(wantBuild, serialBuild) {
			t.Errorf("the partial path's build term %v differs from the serial path's %v; "+
				"the build is performed ONCE by the leader (prebuildSharedHashJoins) and shared, "+
				"so no divisor and no participant multiplier belongs on it (E-09a/E-09b)",
				wantBuild, serialBuild)
		}
		// And the whole startup is that build plus the partial outer's startup —
		// so a multiplier could not hide in a neighbouring term either.
		if !phjClose(pp.Cost.Startup, o.Cost.Startup+wantBuild) {
			t.Errorf("partial startup %v != outer startup %v + build %v",
				pp.Cost.Startup, o.Cost.Startup, wantBuild)
		}
	})
}

// (2) One divisor, not two. `final_cost_hashjoin` divides the join's row count
// by the parallel divisor and `cost_gather`'s compute_gather_rows multiplies it
// back; if either side ever applied it twice the Gather would be charged
// parallel_tuple_cost on a quarter of the rows that actually cross it.
func TestPartialHashJoinRowsRoundTripThroughExactlyOneDivisor(t *testing.T) {
	withParallelOn(t, func() {
		s, joinrel := phjJoinrel(t, phjProblem(2_000_000, 10), gatherPathsAll)
		pp := phjPathOfKind(joinrel.PartialPathlist, PathHashJoin)
		if pp == nil {
			t.Fatal("no partial hash join path")
		}
		if got, want := computeGatherRows(pp, s.cp), clampRowEst(joinrel.Rows); !phjClose(got, want) {
			t.Errorf("compute_gather_rows(partial hash join) = %v, want the joinrel's own %v", got, want)
		}
	})
}

// (3) BOTH candidates exist before any cost is compared.
//
// This is the Q8 trap in test form: five wrong cost hypotheses were burned
// there because the index path producer emitted nothing at that
// parameterisation, so the costs were never the question. A crossover test that
// does not first prove both producers fired proves nothing about costs.
func TestPartialHashJoinAndItsSerialTwinAreBothOffered(t *testing.T) {
	withParallelOn(t, func() {
		_, joinrel := phjJoinrel(t, phjProblem(2_000_000, 10), gatherPathsAll)
		if phjPathOfKind(joinrel.Pathlist, PathHashJoin) == nil {
			t.Error("the SERIAL hash join path was not offered")
		}
		if phjPathOfKind(joinrel.PartialPathlist, PathHashJoin) == nil {
			t.Error("the PARTIAL hash join path was not offered")
		}
		if phjPathOfKind(joinrel.Pathlist, PathGather) == nil {
			t.Error("generateUsefulGatherPaths offered no Gather over the partial hash join")
		}
	})
}

// (4) The crossover — the headline property, and the one C-19d could not have.
//
// A base-rel Gather is DOMINATED at any relation size (C-19d §5.1): the whole
// relation crosses the boundary, so parallel_tuple_cost x rows exceeds the
// entire scan's cost while the saving is only the CPU share. A partial JOIN
// puts the join below the boundary, so what crosses is the join's OUTPUT. The
// Gather then wins iff
//
//	(1 - 1/d) * (cpu_tuple_cost + cpu_operator_cost*k) * N
//	    >  parallel_setup_cost + (parallel_tuple_cost - (1-1/d)*cpu_tuple_cost) * J
//
// Every term below is read from costParams — never written as a literal. A
// literal here would pin today's calibration and hide the next one, which is
// exactly how the index-probe multiplier shipped for months at the value its own
// comment called wrong; and C-19d hit a round literal that put a crossover test
// inside add_path's 1% fuzz band, which is why the two fixtures sit at half and
// double the break-even rather than either side of it.
func TestPartialHashJoinGatherWinsExactlyAtTheCrossover(t *testing.T) {
	withParallelOn(t, func() {
		const aRows = 2_000_000

		// Calibrate the fixture from the model rather than from a guess: one
		// probe run reports the workers the ladder sized and the join output
		// per unit of `b`, so the two arms below are placed by the constants.
		s, probe := phjJoinrel(t, phjProblem(aRows, 1), gatherPathsAll)
		cp := s.cp
		if len(s.joinrels[1][0].PartialPathlist) == 0 {
			t.Fatal("the large rel has no partial seq scan; the fixture cannot exercise this")
		}
		outerPartial := s.joinrels[1][0].PartialPathlist[0]
		jPerUnit := probe.Rows
		if jPerUnit <= 0 {
			t.Fatalf("probe fixture produced a %v-row join; cannot scale it", jPerUnit)
		}

		d := getParallelDivisor(outerPartial.ParallelWorkers, cp.parallelLeaderParticipation)
		share := 1 - 1/d
		savingPerOuterRow := share * (cp.cpuTupleCost + cp.cpuOperatorCost)
		chargePerOutputRow := cp.parallelTupleCost - share*cp.cpuTupleCost
		if chargePerOutputRow <= 0 {
			t.Skip("parallel_tuple_cost no longer exceeds the per-row CPU saving; the crossover has moved")
		}
		breakEvenJ := (savingPerOuterRow*aRows - cp.parallelSetupCost) / chargePerOutputRow
		if breakEvenJ <= 0 {
			t.Fatalf("the constants say a %d-row outer can never pay for a Gather (break-even J = %v)", aRows, breakEvenJ)
		}

		for _, tc := range []struct {
			name string
			j    float64
			want PathKind
		}{
			{"well below break-even: the Gather pays", breakEvenJ / 2, PathGather},
			{"well above break-even: the serial hash join wins", breakEvenJ * 2, PathHashJoin},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bRows := int64(math.Round(tc.j / jPerUnit))
				if bRows < 1 {
					bRows = 1
				}
				_, joinrel := phjJoinrel(t, phjProblem(aRows, bRows), gatherPathsAll)
				// Not vacuous in either direction: BOTH rivals must exist, or
				// the winner says nothing about the crossover.
				if phjPathOfKind(joinrel.Pathlist, PathHashJoin) == nil ||
					phjPathOfKind(joinrel.PartialPathlist, PathHashJoin) == nil {
					t.Fatal("one of the two candidates was never generated; the comparison is meaningless")
				}
				if got := joinrel.CheapestTotal.Kind; got != tc.want {
					t.Errorf("J=%.0f (break-even %.0f): cheapest path is %v, want %v",
						joinrel.Rows, breakEvenJ, got, tc.want)
				}
			})
		}
	})
}

// (5) Upward propagation — the mechanism C-19d §5 named as the thing it did not
// have. A joinrel's partial path is the partial OUTER of the join above it, so
// the paths climb the tree and one Gather can sit over the whole of it.
func TestPartialHashJoinPropagatesUpTheJoinTree(t *testing.T) {
	withParallelOn(t, func() {
		s := gpSearch(t, cpBigProblem([]string{"a", "b", "c"}), gatherPathsAll)
		// The producer reads the mode, and gpSearch restores it on return, so
		// the direct calls below have to re-pin it.
		defer setGatherPathsModeForTest(gatherPathsAll)()

		var mid, top *RelOptInfo
		for lev := 2; lev < len(s.joinrels); lev++ {
			for _, rel := range s.joinrels[lev] {
				if relLevel(rel.Relids) == s.nrels {
					top = rel
				} else if mid == nil {
					mid = rel
				}
			}
		}
		if mid == nil || top == nil {
			t.Fatal("fixture has no intermediate and final joinrel pair")
		}
		midPP := phjPathOfKind(mid.PartialPathlist, PathHashJoin)
		if midPP == nil {
			t.Fatal("the intermediate joinrel gained no partial hash join path")
		}
		if len(top.PartialPathlist) == 0 {
			t.Fatal("the FINAL joinrel gained no partial path; without one, a Gather can never sit above the join tree")
		}

		// The mechanism, pinned at the OFFER rather than at the survivor: a
		// joinrel's partial path must be usable as the partial OUTER of the
		// join above it. Which candidate then survives add_partial_path's
		// dominance pruning is a cost question and not this pin's subject —
		// in this fixture the level-3 winner happens to probe a base rel
		// instead, which is a legitimate cheaper answer, not a missing
		// mechanism.
		var third *RelOptInfo
		for _, rel := range s.joinrels[1] {
			if rel.Relids&mid.Relids == 0 {
				third = rel
			}
		}
		if third == nil {
			t.Fatal("no base rel outside the intermediate joinrel")
		}
		probe := &RelOptInfo{Relids: top.Relids, Rows: top.Rows, Width: top.Width, ConsiderParallel: true}
		addPartialHashJoinPath(s, probe, mid, third, s.cp, parser.JoinInner, midPP.HashKeys, nil, 0)
		if len(probe.PartialPathlist) != 1 {
			t.Fatalf("a joinrel's partial path was not usable as the partial outer one level up: %d paths filed", len(probe.PartialPathlist))
		}
		stacked := probe.PartialPathlist[0]
		if stacked.Children[0] != midPP {
			t.Fatalf("the stacked path's probe side is a %v, not the level below's partial join", stacked.Children[0].Kind)
		}
		if stacked.ParallelWorkers != midPP.ParallelWorkers {
			t.Errorf("the stacked path plans %d workers, want the partial outer's %d (create_hashjoin_path:2866)",
				stacked.ParallelWorkers, midPP.ParallelWorkers)
		}
		// And the shape walk agrees: the probe chain bottoms out in a scan the
		// executor's attach walks model, so a Gather over it is admissible.
		if got := partialPathDrivingKind(stacked); got != PathSeqScan {
			t.Errorf("partialPathDrivingKind of a two-level partial join = %v, want the probe chain's seq scan", got)
		}
		if !partialPathShapeIsGatherable(stacked) {
			t.Error("a two-level partial hash join is not gatherable; the Gather can never be placed")
		}
	})
}

// (6) The fail-closed refusals of DESIGN §4.2, each asserting NOTHING was
// filed. Every one names the wrong ANSWER it prevents, not a missed plan.
func TestPartialHashJoinRefusals(t *testing.T) {
	withParallelOn(t, func() {
		// A base fixture whose parts can be broken one at a time.
		build := func(t *testing.T) (*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo, []*restrictInfo) {
			t.Helper()
			prob := phjProblem(2_000_000, 10)
			s, joinrel := phjJoinrel(t, prob, gatherPathsAll)
			outer, inner := s.joinrels[1][0], s.joinrels[1][1]
			// gpSearch restores the mode on return, and the producer reads it.
			t.Cleanup(setGatherPathsModeForTest(gatherPathsAll))
			pp := phjPathOfKind(joinrel.PartialPathlist, PathHashJoin)
			if pp == nil {
				t.Fatal("the unbroken fixture files no partial path; the refusals would be vacuous")
			}
			keys := pp.HashKeys
			joinrel.PartialPathlist = nil
			return s, joinrel, outer, inner, keys
		}

		for _, tc := range []struct {
			name     string
			sabotage func(s *searchCtx, joinrel, outer, inner *RelOptInfo)
		}{
			{
				// A rel that does not consider parallel has no business
				// producing a partial path; addPartialPath refuses it too.
				name:     "joinrel does not consider parallel",
				sabotage: func(_ *searchCtx, joinrel, _, _ *RelOptInfo) { joinrel.ConsiderParallel = false },
			},
			{
				// No partial outer at all — upstream's first condition
				// (`outerrel->partial_pathlist != NIL`, joinpath.c:2421).
				name:     "outer has no partial path",
				sabotage: func(_ *searchCtx, _, outer, _ *RelOptInfo) { outer.PartialPathlist = nil },
			},
			{
				// try_partial_hashjoin_path returns early on a parameterised
				// inner (:1317): a hash build is materialised in full before
				// the probe, so there is no per-outer-row binding to supply.
				name: "every inner path is parameterised",
				sabotage: func(_ *searchCtx, _, _, inner *RelOptInfo) {
					for _, p := range inner.Pathlist {
						p.RequiredOuter = RelSet(1)
					}
				},
			},
			{
				// THE NESTED-GATHER GUARD. A Gather sets ParallelSafe=false
				// and parallelSafeWith ANDs its children, so a path carrying
				// one is excluded from the inner side. Without it a Gather
				// could land on the build side of a join whose build the
				// leader runs inside gatherOp.Open.
				name: "no parallel-safe inner path (a Gather on the build side)",
				sabotage: func(_ *searchCtx, _, _, inner *RelOptInfo) {
					for _, p := range inner.Pathlist {
						p.ParallelSafe = false
					}
				},
			},
			{
				// A partial outer whose driving scan the executor's per-worker
				// attach walks do not model. runWorker IGNORES
				// attachParallelScan's return value, so such a subtree does not
				// "stay serial" — every worker reads the whole relation and the
				// Gather returns N copies of every row.
				name: "the partial outer's shape is not one the attach walks model",
				sabotage: func(_ *searchCtx, _, outer, _ *RelOptInfo) {
					for _, p := range outer.PartialPathlist {
						p.Kind = PathPrebuilt
					}
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, joinrel, outer, inner, keys := build(t)
				tc.sabotage(s, joinrel, outer, inner)
				addPartialHashJoinPath(s, joinrel, outer, inner, s.cp, parser.JoinInner, keys, nil, 0)
				if len(joinrel.PartialPathlist) != 0 {
					t.Errorf("filed %d partial path(s) despite the refusal", len(joinrel.PartialPathlist))
				}
			})
		}

		// And the control arm: unbroken, the same call DOES file one — so the
		// refusals above are not all passing because the call is inert.
		t.Run("control: unbroken, a path IS filed", func(t *testing.T) {
			s, joinrel, outer, inner, keys := build(t)
			addPartialHashJoinPath(s, joinrel, outer, inner, s.cp, parser.JoinInner, keys, nil, 0)
			if len(joinrel.PartialPathlist) != 1 {
				t.Fatalf("the control arm filed %d partial paths, want 1", len(joinrel.PartialPathlist))
			}
			if p := joinrel.PartialPathlist[0]; !p.ParallelAware || !p.ParallelSafe || p.ParallelWorkers <= 0 {
				t.Errorf("filed path: aware=%v safe=%v workers=%d; create_hashjoin_path sets all three",
					p.ParallelAware, p.ParallelSafe, p.ParallelWorkers)
			}
		})
	})
}

// (7) The parallel_aware flag's route to createPlan, both directions.
//
// The refusal is unreachable from the live search (createHashJoinPlan hard-codes
// JoinTypeInner and the search's jointype pin admits nothing else), which is
// exactly why it is asserted directly: an unwinnable path is an untested path,
// and this guard's whole job is to fire the day a producer changes.
func TestParallelAwareJoinGuardRefusesAJoinTheExecutorWillNotRun(t *testing.T) {
	aware := &Path{Kind: PathHashJoin, ParallelAware: true, Rel: newRelOptInfo(RelSet(3), 10, 8)}
	notAware := &Path{Kind: PathHashJoin, Rel: aware.Rel}

	for _, tc := range []struct {
		name      string
		p         *Path
		j         *Join
		wantPanic bool
	}{
		{"inner hash join: admitted", aware, &Join{Type: JoinTypeInner, Algo: JoinAlgoHash}, false},
		{"semi hash join: admitted", aware, &Join{Type: JoinTypeSemi, Algo: JoinAlgoHash}, false},
		// FULL and RIGHT need to know which BUILD rows went unmatched across
		// ALL workers — a cross-worker reduction the executor does not have.
		{"full hash join: refused", aware, &Join{Type: JoinTypeFull, Algo: JoinAlgoHash}, true},
		{"right hash join: refused", aware, &Join{Type: JoinTypeRight, Algo: JoinAlgoHash}, true},
		// LEFT is admitted only with the outer on the PROBE side.
		{"left with build on the probe side: refused", aware, &Join{Type: JoinTypeLeft, Algo: JoinAlgoHash, BuildLeft: true}, true},
		{"left with outer on the probe side: admitted", aware, &Join{Type: JoinTypeLeft, Algo: JoinAlgoHash}, false},
		// A merge join is not shareable at all.
		{"merge join: refused", aware, &Join{Type: JoinTypeInner, Algo: JoinAlgoMerge}, true},
		// The guard says nothing about a path that never claimed the flag.
		{"not parallel-aware: no opinion", notAware, &Join{Type: JoinTypeFull, Algo: JoinAlgoMerge}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				switch r := recover(); {
				case r == nil && tc.wantPanic:
					t.Error("the guard admitted a join the executor's own predicate declines")
				case r != nil && !tc.wantPanic:
					t.Errorf("the guard refused a runnable join: %v", r)
				}
			}()
			assertParallelAwareJoinIsRunnable(tc.p, tc.j)
		})
	}
}

// (7, second half) A Gather built over a partial hash join stamps the PROBE
// side's scan and leaves the build side's alone. If the two ever swapped, every
// worker would hash a PARTITION of the build input and the join would quietly
// drop matches — the failure mode parallel_hash_join_test.go's header names.
func TestGatherOverPartialHashJoinStampsTheProbeScanOnly(t *testing.T) {
	withParallelOn(t, func() {
		s, joinrel := phjJoinrel(t, phjProblem(2_000_000, 10), gatherPathsAll)
		pp := phjPathOfKind(joinrel.PartialPathlist, PathHashJoin)
		if pp == nil {
			t.Fatal("no partial hash join path")
		}
		g := makeGatherPath(joinrel, pp, s.cp)
		if g == nil {
			t.Fatal("makeGatherPath declined a partial hash join; the shape walk and the producer disagree")
		}
		node, _ := createPlanNode(g)
		gather, ok := node.(*Gather)
		if !ok {
			t.Fatalf("createPlanNode built a %T, want *Gather", node)
		}
		if gather.WorkersPlanned != pp.ParallelWorkers {
			t.Errorf("Gather plans %d workers, want the subpath's %d", gather.WorkersPlanned, pp.ParallelWorkers)
		}
		j, ok := gather.Child.(*Join)
		if !ok {
			t.Fatalf("the Gather's child is a %T, want the hash join", gather.Child)
		}
		if !hashJoinIsPartialCapable(j) {
			t.Fatal("the built join is not partial-capable; the executor would refuse to share its build")
		}
		probe, buildSide := j.Left, j.Right
		if j.BuildLeft {
			probe, buildSide = j.Right, j.Left
		}
		probeScan, _ := drivingScan(probe).(*SeqScan)
		if probeScan == nil || !probeScan.Parallel {
			t.Errorf("the PROBE side's scan is not stamped Parallel (%v); the workers would each read the whole relation", probeScan)
		}
		if bs, ok := buildSide.(*SeqScan); ok && bs.Parallel {
			t.Error("the BUILD side's scan is stamped Parallel; each worker would hash only a partition and the join would drop matches")
		}
	})
}

// (8) Mode `off` — the default — produces no partial join path at all, so the
// search is unchanged by construction. This is the slice's serial-control-arm
// argument, and it is what makes "C-19f is inert at the default" a fact rather
// than a claim.
func TestPartialHashJoinIsInertUnderTheDefaultMode(t *testing.T) {
	withParallelOn(t, func() {
		_, joinrel := phjJoinrel(t, phjProblem(2_000_000, 10), gatherPathsOff)
		if n := len(joinrel.PartialPathlist); n != 0 {
			t.Errorf("mode off filed %d partial join path(s); the only reader is behind the same mode, so producing them is pure planner time", n)
		}
		if gpCountGathers(joinrel) != 0 {
			t.Error("mode off produced a Gather path")
		}
		if got := joinrel.CheapestTotal.Kind; got != PathHashJoin {
			t.Errorf("mode off changed the chosen path to %v", got)
		}
	})
}
