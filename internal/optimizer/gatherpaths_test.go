package optimizer

// C-19d / P5-04 pins (take3 08 §8; docs/design/planner-c19d-gather-paths/
// DESIGN.md §8). Six properties, in the order that doc lists them:
//
//	(1) cost_gather / cost_gather_merge / compute_gather_rows reproduce
//	    upstream TERM BY TERM, expressed through the named costParams fields —
//	    never through a literal, because a literal pins a stale calibration
//	    (the index-probe multiplier shipped for months at the value its own
//	    comment called wrong);
//	(2) a Gather path wins over the serial path exactly at the crossover the
//	    constants define, and loses on the other side of it;
//	(3) the row estimate the Gather publishes undoes exactly one divisor;
//	(4) createPlanNode emits the executor nodes, carrying the subpath's worker
//	    count and stamping the driving scan — and REFUSES a subtree the
//	    executor's per-worker attach walks do not model;
//	(5) MaybeAddGather declines on a tree that already gathers (and the pin is
//	    not vacuous: the same tree without one does get a Gather);
//	(6) mode `off` (the default) changes nothing about the search, `top`
//	    admits only at the final rel, `all` admits at base rels too.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// gpSearch is cpSearch plus this slice's step: the production protocol of
// searchOneProblem, including addBaseRelGatherPaths, under an explicit mode.
func gpSearch(t *testing.T, prob *joinlistProblem, mode gatherPathMode) *searchCtx {
	t.Helper()
	defer setGatherPathsModeForTest(mode)()
	s, err := buildInitialRels(prob.bindings, prob.scans, prob.relInfos, prob.cp, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.clauses = buildRestrictInfos(prob.conjuncts, 0, prob.cumOffsets)
	s.setBaseRelConsiderParallel(prob.cat)
	s.addBaseRelPartialPaths()
	s.addBaseRelIndexPaths(prob.cat)
	s.addBaseRelGatherPaths()
	if _, err := s.joinSearch(s.clauses, newJoinRelBuilder(s, prob.cat)); err != nil {
		t.Fatal(err)
	}
	return s
}

// gpPartialSeqPath is a synthetic partial path with a stated cost and worker
// count: the shape addPartialSeqScanPath produces, without needing a fixture
// big enough to make the ladder produce that many workers.
func gpPartialSeqPath(rel *RelOptInfo, total float64, workers int) *Path {
	return &Path{
		Kind:            PathSeqScan,
		Rel:             rel,
		Rows:            1000,
		Cost:            Cost{Startup: 0, Total: total},
		ParallelSafe:    true,
		ParallelWorkers: workers,
	}
}

func gpClose(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

// (1) cost_gather (costsize.c:446) — startup gains parallel_setup_cost, total
// gains that plus parallel_tuple_cost per row EMERGING from the Gather, and
// the subpath's disabled_nodes passes through (cost_gather has no enable_*).
func TestGatherPathCostIsCostGather(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(RelSet(1), 1000, 8)
	rel.ConsiderParallel = true
	sub := gpPartialSeqPath(rel, 500, 3)
	sub.DisabledNodes = 2

	g := makeGatherPath(rel, sub, cp)
	if g == nil {
		t.Fatal("makeGatherPath declined a partial seq scan with workers")
	}
	rows := computeGatherRows(sub, cp)
	wantStartup := sub.Cost.Startup + cp.parallelSetupCost
	wantTotal := sub.Cost.Total + cp.parallelSetupCost + cp.parallelTupleCost*rows
	if !gpClose(g.Cost.Startup, wantStartup) || !gpClose(g.Cost.Total, wantTotal) {
		t.Errorf("gather cost = %+v, want startup %v total %v", g.Cost, wantStartup, wantTotal)
	}
	if g.Rows != rows {
		t.Errorf("gather rows = %v, want compute_gather_rows = %v", g.Rows, rows)
	}
	if g.DisabledNodes != sub.DisabledNodes {
		t.Errorf("gather disabled_nodes = %d, want the subpath's %d (cost_gather has no flag)", g.DisabledNodes, sub.DisabledNodes)
	}
	// create_gather_path: parallel_safe = false, parallel_workers = 0,
	// pathkeys = NIL. A Gather is the boundary, not a partial path.
	if g.ParallelSafe || g.ParallelWorkers != 0 || len(g.Pathkeys) != 0 {
		t.Errorf("gather path claims safe=%v workers=%d pathkeys=%d; all three must be zero", g.ParallelSafe, g.ParallelWorkers, len(g.Pathkeys))
	}
	if len(g.Children) != 1 || g.Children[0] != sub {
		t.Fatal("gather path must carry exactly the subpath it was priced over")
	}
}

// (1) cost_gather_merge (costsize.c:485), term by term through the named
// constants: heap creation, per-tuple maintenance, the cost_merge_append-like
// management term, setup, and the IPC term with its extra 5%.
func TestGatherMergeCostMatchesUpstreamTerms(t *testing.T) {
	cp := defaultCostParams()
	sub := Cost{Startup: 12, Total: 480}
	const workers = 3
	const rows = 4321.0

	n := float64(workers) + 1
	logN := math.Log2(n)
	comparison := 2.0 * cp.cpuOperatorCost
	wantStartup := comparison*n*logN + cp.parallelSetupCost + sub.Startup
	wantRun := rows*comparison*logN + cp.cpuOperatorCost*rows +
		cp.parallelTupleCost*rows*gatherMergeIPCFactor
	wantTotal := (comparison*n*logN + cp.parallelSetupCost) + wantRun + sub.Total

	got := gatherMergeCost(cp, sub, workers, rows)
	if !gpClose(got.Startup, wantStartup) {
		t.Errorf("startup = %v, want %v", got.Startup, wantStartup)
	}
	if !gpClose(got.Total, wantTotal) {
		t.Errorf("total = %v, want %v", got.Total, wantTotal)
	}
	// The whole reason a Gather Merge is not a Gather: it pays for the heap
	// and 5% more per tuple transferred. Expressed as a RELATION between the
	// two functions rather than as two numbers.
	plain := gatherCost(cp, sub, rows)
	if !(got.Total > plain.Total) || !(got.Startup > plain.Startup) {
		t.Errorf("gather merge %+v must cost more than gather %+v over the same subpath", got, plain)
	}
	// N counts the LEADER too (costsize.c:510-511): one more worker must
	// raise the heap terms.
	if !(gatherMergeCost(cp, sub, workers+1, rows).Total > got.Total) {
		t.Error("an extra worker must raise the merge heap cost")
	}
}

// (1) enable_gathermerge is COUNTED, not a gate: the path is still produced
// and carries one more disabled node (costsize.c:535-536). This is the
// conversion ParallelSettings.DisableGatherMerge's comment asked P5-04 for.
func TestGatherMergePathCountsEnableGathermerge(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(RelSet(1), 1000, 8)
	rel.ConsiderParallel = true
	sub := gpPartialSeqPath(rel, 500, 2)
	sub.Pathkeys = []PathKey{{Expr: cpCol(0), SortAsc: true}}
	sub.DisabledNodes = 1

	on := makeGatherMergePath(rel, sub, cp)
	if on == nil {
		t.Fatal("makeGatherMergePath declined an ordered partial seq scan")
	}
	if on.DisabledNodes != sub.DisabledNodes {
		t.Errorf("enable_gathermerge on: disabled_nodes = %d, want the input's %d", on.DisabledNodes, sub.DisabledNodes)
	}
	cpOff := cp
	cpOff.enableGatherMerge = false
	off := makeGatherMergePath(rel, sub, cpOff)
	if off == nil {
		t.Fatal("enable_gathermerge = off must still PRODUCE the path (PG counts, it does not skip)")
	}
	if off.DisabledNodes != on.DisabledNodes+1 {
		t.Errorf("enable_gathermerge off: disabled_nodes = %d, want %d", off.DisabledNodes, on.DisabledNodes+1)
	}
	if len(off.Pathkeys) != len(sub.Pathkeys) {
		t.Error("a gather merge must publish the ordering it preserves")
	}
}

// (3) compute_gather_rows (costsize.c:6625) undoes EXACTLY ONE divisor: the
// one cost_seqscan's parallel arm applied. Read from the real producer, so a
// change to either side breaks the pin.
func TestComputeGatherRowsUndoesTheScansDivisorOnce(t *testing.T) {
	withParallelOn(t, func() {
		prob := cpBigProblem([]string{"a", "b", "c"})
		s := gpSearch(t, prob, gatherPathsOff)
		found := 0
		for _, rel := range s.joinrels[1] {
			if len(rel.PartialPathlist) == 0 {
				continue
			}
			found++
			pp := rel.PartialPathlist[0]
			d := getParallelDivisor(pp.ParallelWorkers, s.cp.parallelLeaderParticipation)
			if want := clampRowEst(pp.Rows * d); computeGatherRows(pp, s.cp) != want {
				t.Errorf("computeGatherRows = %v, want clamp(%v * %v) = %v", computeGatherRows(pp, s.cp), pp.Rows, d, want)
			}
			// And the round trip lands back on the relation's own count: the
			// scan divided by d, the Gather multiplies by d. Allow one
			// divisor's worth of clamp_row_est rounding, no more.
			if diff := math.Abs(computeGatherRows(pp, s.cp) - rel.Rows); diff > d {
				t.Errorf("round trip: gather rows %v vs rel rows %v (diff %v > divisor %v)", computeGatherRows(pp, s.cp), rel.Rows, diff, d)
			}
		}
		if found == 0 {
			t.Fatal("fixture produced no partial path; the pin would be vacuous")
		}
	})
}

// (2) THE CROSSOVER. A Gather is worth it exactly when the CPU share the
// workers take off the subpath exceeds what the Gather charges to set up and
// to move the rows across. Both sides of the threshold, expressed through the
// constants — never as two magic totals.
func TestGatherPathWinsExactlyAtTheSetupCostCrossover(t *testing.T) {
	cp := defaultCostParams()
	const workers = 4
	rel := newRelOptInfo(RelSet(1), 1000, 8)
	rel.ConsiderParallel = true

	// The Gather's own charge, in the currency of the constants.
	probe := gpPartialSeqPath(rel, 0, workers)
	rows := computeGatherRows(probe, cp)
	gatherCharge := cp.parallelSetupCost + cp.parallelTupleCost*rows

	// The serial total is stated as a MULTIPLE of the Gather's own charge, so
	// both arms land well outside add_path's 1% fuzz band (stdFuzzFactor).
	// Writing a round literal here instead put a 0.7% difference inside the
	// band, where the higher-startup path loses on startup and the crossover
	// is not what is being measured.
	serialTotal := gatherCharge * 10

	for _, tc := range []struct {
		name     string
		saving   float64
		wantWins bool
	}{
		{"saving beats the charge", gatherCharge * 1.5, true},
		{"saving loses to the charge", gatherCharge * 0.5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serial := &Path{Kind: PathSeqScan, Rel: rel, Rows: 1000, Cost: Cost{Total: serialTotal}}
			sub := gpPartialSeqPath(rel, serialTotal-tc.saving, workers)
			g := makeGatherPath(rel, sub, cp)
			if g == nil {
				t.Fatal("makeGatherPath declined")
			}
			wins := g.Cost.Total < serial.Cost.Total
			if wins != tc.wantWins {
				t.Fatalf("gather total %v vs serial %v: wins = %v, want %v", g.Cost.Total, serial.Cost.Total, wins, tc.wantWins)
			}
			// …and add_path agrees with the arithmetic: the survivor of the
			// two is the one the crossover names.
			r := newRelOptInfo(RelSet(1), 1000, 8)
			r.ConsiderParallel = true
			addPath(r, serial, "test.serial")
			addPath(r, g, "test.gather")
			setCheapest(r)
			gotGather := r.CheapestTotal != nil && r.CheapestTotal.Kind == PathGather
			if gotGather != tc.wantWins {
				t.Errorf("CheapestTotal is a Gather = %v, want %v", gotGather, tc.wantWins)
			}
		})
	}
}

// (4)+(6) The refusals, each naming the wrong ANSWER it prevents. A path that
// is offered here can be built and executed; one that is not is never costed.
func TestGatherPathRefusesUnrunnableSubpaths(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(RelSet(1), 1000, 8)
	rel.ConsiderParallel = true

	zero := gpPartialSeqPath(rel, 500, 0)
	if makeGatherPath(rel, zero, cp) != nil {
		t.Error("a 0-worker subpath is upstream's single_copy Gather, which goopg's executor does not model")
	}
	unsafe := gpPartialSeqPath(rel, 500, 2)
	unsafe.ParallelSafe = false
	if makeGatherPath(rel, unsafe, cp) != nil {
		t.Error("a path that is not parallel-safe is not a partial path")
	}
	// A prebuilt subtree is opaque: no attach walk models it, so every worker
	// would read the whole relation.
	prebuilt := gpPartialSeqPath(rel, 500, 2)
	prebuilt.Kind = PathPrebuilt
	if makeGatherPath(rel, prebuilt, cp) != nil {
		t.Error("a prebuilt subtree has no driving scan the executor can partition")
	}
	// A parameterised index path is an NLI probe, not a partial scan.
	param := gpPartialSeqPath(rel, 500, 2)
	param.Kind = PathIndexScan
	param.RequiredOuter = RelSet(2)
	if makeGatherPath(rel, param, cp) != nil {
		t.Error("a parameterised probe must not be gathered")
	}
}

// (4) THE GATHER MERGE EXECUTOR GAP. gatherMergeOp attaches only the seq-scan
// block allocator — never the index or bitmap claim set — so a Gather Merge
// over a partial INDEX path would give every worker the whole index and return
// N copies of every row. The producer refuses it; when the executor grows the
// attachment, this pin is the thing to change.
func TestGatherMergeRefusesAnythingButASeqScanDrivenSubpath(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(RelSet(1), 1000, 8)
	rel.ConsiderParallel = true
	keys := []PathKey{{Expr: cpCol(0), SortAsc: true}}

	seq := gpPartialSeqPath(rel, 500, 2)
	seq.Pathkeys = keys
	if makeGatherMergePath(rel, seq, cp) == nil {
		t.Fatal("a seq-scan-driven ordered partial path is the shape gatherMergeOp CAN drive")
	}
	idx := gpPartialSeqPath(rel, 500, 2)
	idx.Kind = PathIndexScan
	idx.Pathkeys = keys
	if makeGatherMergePath(rel, idx, cp) != nil {
		t.Error("gather merge over a partial index path returns N copies of every row (operators_gather_merge.go attaches only attachParallelScan)")
	}
	// A plain Gather over that same index path is fine: gatherOp attaches all
	// three claim sets.
	if makeGatherPath(rel, idx, cp) == nil {
		t.Error("a plain Gather over a partial index path is runnable and must be offered")
	}
	// No ordering ⇒ no merge (upstream asserts pathkeys).
	unordered := gpPartialSeqPath(rel, 500, 2)
	if makeGatherMergePath(rel, unordered, cp) != nil {
		t.Error("a gather merge with nothing to merge by is a plain Gather")
	}
}

// makeGatherWorthwhile rewrites a partial path's cost so that gathering it is
// unambiguously worth it, which is what lets the ADMISSION pins below measure
// admission rather than economics.
//
// It is worth recording WHY the fixture needs this. With today's producers the
// only partial paths are BASE-relation scans, and every row of the relation
// crosses the Gather: the charge is parallel_tuple_cost (0.1) per row, while
// the saving is the cpu_tuple_cost (0.01) share the workers take off — an
// order of magnitude less. So a base-rel Gather over a plain seq scan is
// DOMINATED by the serial scan, correctly and by construction, at any relation
// size. That is the same reason PG's Gather ends up above joins and aggregates
// (where few rows cross it) rather than over a raw scan, and it is another way
// of saying what DESIGN.md §5 says: C-19d cannot pay for itself until partial
// JOIN / AGG paths (C-19f/g) shrink what crosses the boundary.
func makeGatherWorthwhile(rel *RelOptInfo, sub *Path, cp costParams) {
	// Both axes have to move, and that is the finding rather than a fixture
	// convenience. Leaving the relation's full row count crossing the Gather
	// makes parallel_tuple_cost * rows exceed the whole scan's cost on its own
	// — no reduction in the subpath's price can rescue it — which is why the
	// row count is shrunk to a selective scan's output first.
	sub.Rows = 10
	charge := cp.parallelSetupCost + cp.parallelTupleCost*computeGatherRows(sub, cp)
	sub.Cost.Total = rel.CheapestTotal.Cost.Total - 10*charge
	if sub.Cost.Total < 0 {
		sub.Cost.Total = 0
	}
}

func gpCountGathers(rel *RelOptInfo) int {
	n := 0
	for _, p := range rel.Pathlist {
		if p.Kind == PathGather || p.Kind == PathGatherMerge {
			n++
		}
	}
	return n
}

// (6) The admission mode. `off` — the default — offers nothing, which is this
// slice's serial-control-arm argument; `all` offers at a base rel; `top` offers
// at the search's FINAL rel only. The pins are not vacuous in either direction:
// the same fixture and the same partial path produce a Gather under one mode
// and none under another.
func TestGatherPathsModeGovernsAdmission(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b", "c"}

		// Base rels: `off` vs `all`.
		for _, tc := range []struct {
			mode gatherPathMode
			want int
		}{
			{gatherPathsOff, 0},
			{gatherPathsTop, 0}, // a base rel is not the final rel of a 3-rel search
			{gatherPathsAll, 1},
		} {
			s := gpSearch(t, cpBigProblem(names), gatherPathsOff)
			rel := s.joinrels[1][0]
			if len(rel.PartialPathlist) == 0 {
				t.Fatal("fixture produced no partial path on rel 0; the pins would be vacuous")
			}
			makeGatherWorthwhile(rel, rel.PartialPathlist[0], s.cp)
			restore := setGatherPathsModeForTest(tc.mode)
			s.addBaseRelGatherPaths()
			restore()
			if got := gpCountGathers(rel); got != tc.want {
				t.Errorf("mode %s: base rel has %d Gather paths, want %d", gatherPathModeLabel(tc.mode), got, tc.want)
			}
		}

		// The final rel: `top` offers there, and a NON-final joinrel gets
		// nothing under the same mode. Joinrels have no partial paths until
		// C-19f, so one is installed by hand — the mode is what is under test,
		// not the producer.
		s := gpSearch(t, cpBigProblem(names), gatherPathsOff)
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
		for _, rel := range []*RelOptInfo{mid, top} {
			rel.ConsiderParallel = true
			sub := gpPartialSeqPath(rel, 0, 2)
			rel.PartialPathlist = []*Path{sub}
			makeGatherWorthwhile(rel, sub, s.cp)
		}
		defer setGatherPathsModeForTest(gatherPathsTop)()
		s.generateUsefulGatherPaths(mid)
		s.generateUsefulGatherPaths(top)
		if got := gpCountGathers(mid); got != 0 {
			t.Errorf("mode top offered %d Gather paths at a NON-final joinrel", got)
		}
		if got := gpCountGathers(top); got != 1 {
			t.Errorf("mode top offered %d Gather paths at the final rel, want 1", got)
		}
	})
}

// (4) createPlanNode's arms: the executor node carries the SUBPATH's worker
// count, its driving scan is stamped parallel (so EXPLAIN says "Parallel Seq
// Scan" and the post-pass's label and this one agree), and the path's cost
// reaches the node.
//
// The path is built directly from the producer rather than fished out of a
// pathlist: whether a base-rel Gather SURVIVES add_path is the economics
// question makeGatherWorthwhile documents, and it is not this pin's subject.
func TestCreateGatherPlanCarriesWorkersAndStampsTheScan(t *testing.T) {
	withParallelOn(t, func() {
		s := gpSearch(t, cpBigProblem([]string{"a", "b", "c"}), gatherPathsOff)
		rel := s.joinrels[1][0]
		if len(rel.PartialPathlist) == 0 {
			t.Fatal("fixture produced no partial path")
		}
		g := makeGatherPath(rel, rel.PartialPathlist[0], s.cp)
		if g == nil {
			t.Fatal("makeGatherPath declined the fixture's partial seq scan")
		}
		node, _ := createPlanNode(g)
		gather, ok := node.(*Gather)
		if !ok {
			t.Fatalf("createPlanNode(PathGather) = %T, want *Gather", node)
		}
		if want := g.Children[0].ParallelWorkers; gather.WorkersPlanned != want {
			t.Errorf("WorkersPlanned = %d, want the subpath's %d", gather.WorkersPlanned, want)
		}
		scan := drivingScan(gather.Child)
		if scan == nil {
			t.Fatal("the built subtree has no driving scan; every worker would read the whole relation")
		}
		if sq, ok := scan.(*SeqScan); !ok || !sq.Parallel {
			t.Errorf("driving scan %T is not stamped Parallel; EXPLAIN would not say \"Parallel\" and the label would disagree with the post-pass", scan)
		}
		if pc, set := gather.PlanCostInfo(); !set || pc.TotalCost != g.Cost.Total {
			t.Errorf("the path's cost did not reach the node: %+v set=%v, want total %v", pc, set, g.Cost.Total)
		}
	})
}

// (4) …and it PANICS rather than emitting a Gather over a subtree the
// executor's attach walks do not model. runWorker ignores attachParallelScan's
// return value, so such a plan does not "stay serial" — it duplicates every
// row once per worker.
func TestCreateGatherPlanPanicsWithoutADrivingScan(t *testing.T) {
	rel := newRelOptInfo(RelSet(1), 1000, 8)
	rel.ConsiderParallel = true
	tbl := &catalog.Table{Name: "t"}
	// A Limit is modelled by neither drivingScan nor attachParallelScan.
	opaque := &Path{
		Kind:            PathPrebuilt,
		Rel:             rel,
		Rows:            10,
		Cost:            Cost{Total: 5},
		ParallelSafe:    true,
		ParallelWorkers: 2,
		node:            &Limit{Child: &SeqScan{Table: tbl, schema: Schema{{Name: "a"}}}},
	}
	g := &Path{Kind: PathGather, Rel: rel, Rows: 10, Cost: Cost{Total: 1005}, Children: []*Path{opaque}}
	defer func() {
		if recover() == nil {
			t.Fatal("createPlan built a Gather over a subtree with no driving scan")
		}
	}()
	_, _ = createPlanNode(g)
}

// (5) THE COEXISTENCE RULE. A tree that already carries a costed Gather is
// returned unchanged — the path model's placement stands. Without this the
// post-pass would descend THROUGH the Gather (terminatesPartial lists it, and
// findPartialSubtree descends through terminating single-child nodes) and nest
// a second one: N workers each launching N workers.
func TestMaybeAddGatherDeclinesOnATreeThatAlreadyGathers(t *testing.T) {
	tbl := bigTable(t, "big")
	settings := parallelTestSettings()

	// Control: the same tree WITHOUT a Gather does get one, so the pin below
	// cannot pass vacuously.
	plain := &Project{Child: &Filter{Child: seqScanOver(tbl)}}
	if MaybeAddGather(plain, settings) == plain {
		t.Fatal("control: the post-pass must gather this tree, or the pin below is vacuous")
	}

	gathered := &Project{Child: NewGather(0, &Filter{Child: seqScanOver(tbl)}, 2)}
	if got := MaybeAddGather(gathered, settings); got != gathered {
		t.Error("the post-pass must stand down on a tree the path model already gathered")
	}
	// The same for a Gather Merge, and for one buried under an EXPLAIN (the
	// EXPLAIN arm re-enters MaybeAddGather on the child, where the check runs).
	merged := &Project{Child: NewGatherMerge(0, &Filter{Child: seqScanOver(tbl)}, 2, []SortKey{{Expr: cpCol(0)}})}
	if got := MaybeAddGather(merged, settings); got != merged {
		t.Error("the post-pass must stand down on a tree that already gather-merges")
	}
	ex := &Explain{Child: gathered}
	if got := MaybeAddGather(ex, settings); got != ex {
		t.Error("EXPLAIN must render exactly the plan the query would run")
	}
}
