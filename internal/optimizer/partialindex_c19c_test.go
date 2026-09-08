package optimizer

// C-19c (P5-03) pins: add_partial_path's own comparator, partial plain /
// index-only index paths priced by cost_index's parallel arm, and the
// post-pass's admission of a bare plain index scan as a Gather's driving scan.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func partialCostPath(rel *RelOptInfo, startup, total float64, keys ...PathKey) *Path {
	return &Path{Kind: PathSeqScan, Rel: rel, Cost: Cost{Startup: startup, Total: total}, ParallelSafe: true, Pathkeys: keys}
}

// (a) add_partial_path compares TOTAL cost and pathkeys only. The premise is
// checked first: under the serial comparator a lower-startup / higher-total
// rival SURVIVES on a rel that considers startup (add_path's "different"
// arm), so the second assertion is a pin on the comparator, not on the data.
func TestAddPartialPath_TotalCostAndPathkeysOnly(t *testing.T) {
	rel := &RelOptInfo{ConsiderParallel: true, ConsiderStartup: true}
	incumbent := partialCostPath(rel, 50, 100)
	fastStart := partialCostPath(rel, 1, 200)
	if got := addToPathlist([]*Path{incumbent}, fastStart); len(got) != 2 {
		t.Fatalf("premise: the serial comparator should keep both (startup trade-off), got %d", len(got))
	}
	addPartialPath(rel, incumbent, "test")
	addPartialPath(rel, fastStart, "test")
	if len(rel.PartialPathlist) != 1 || rel.PartialPathlist[0] != incumbent {
		t.Fatalf("add_partial_path must drop the dearer-total path whatever its startup, got %+v", rel.PartialPathlist)
	}
	// And the reverse order: the dearer incumbent is evicted.
	rel2 := &RelOptInfo{ConsiderParallel: true, ConsiderStartup: true}
	dear := partialCostPath(rel2, 1, 200)
	cheap := partialCostPath(rel2, 50, 100)
	addPartialPath(rel2, dear, "test")
	addPartialPath(rel2, cheap, "test")
	if len(rel2.PartialPathlist) != 1 || rel2.PartialPathlist[0] != cheap {
		t.Fatalf("a cheaper-total newcomer must evict the incumbent, got %+v", rel2.PartialPathlist)
	}
}

func TestAddPartialPath_PathkeysAndOrdering(t *testing.T) {
	rel := &RelOptInfo{ConsiderParallel: true}
	k1 := PathKey{Expr: &ColumnRef{Name: "k", Index: 0}}
	unordered := partialCostPath(rel, 0, 100)
	orderedDearer := partialCostPath(rel, 0, 150, k1)
	orderedCheaper := partialCostPath(rel, 0, 50, k1)

	// A dearer path with BETTER pathkeys survives alongside the cheaper one.
	addPartialPath(rel, unordered, "test")
	addPartialPath(rel, orderedDearer, "test")
	if len(rel.PartialPathlist) != 2 {
		t.Fatalf("better pathkeys must keep a dearer partial path, got %d", len(rel.PartialPathlist))
	}
	// The list is in ascending total-cost order (upstream's insert_at).
	if rel.PartialPathlist[0] != unordered || rel.PartialPathlist[1] != orderedDearer {
		t.Fatalf("partial list must be sorted by total cost: %v / %v", rel.PartialPathlist[0].Cost.Total, rel.PartialPathlist[1].Cost.Total)
	}
	// A cheaper path with the SAME (better) pathkeys evicts both: it dominates
	// the unordered one on cost with better keys, and the ordered one on cost
	// with equal keys.
	addPartialPath(rel, orderedCheaper, "test")
	if len(rel.PartialPathlist) != 1 || rel.PartialPathlist[0] != orderedCheaper {
		t.Fatalf("a cheaper, better-ordered path must evict both incumbents, got %d", len(rel.PartialPathlist))
	}
	// Same pathkeys, not materially cheaper (within 1e-10): incumbent stays.
	dup := partialCostPath(rel, 0, 50, k1)
	addPartialPath(rel, dup, "test")
	if len(rel.PartialPathlist) != 1 || rel.PartialPathlist[0] != orderedCheaper {
		t.Fatalf("an identical re-offer must not replace the incumbent, got %+v", rel.PartialPathlist)
	}
	// Incomparable pathkeys keep both, whatever the cost.
	k2 := PathKey{Expr: &ColumnRef{Name: "j", Index: 1}}
	other := partialCostPath(rel, 0, 500, k2)
	addPartialPath(rel, other, "test")
	if len(rel.PartialPathlist) != 2 {
		t.Fatalf("incomparable pathkeys must keep both, got %d", len(rel.PartialPathlist))
	}
	// disabled_nodes trumps cost.
	disabled := partialCostPath(rel, 0, 1, k1)
	disabled.DisabledNodes = 1
	addPartialPath(rel, disabled, "test")
	for _, p := range rel.PartialPathlist {
		if p == disabled {
			t.Fatal("a disabled path must not survive against an enabled one, whatever its cost")
		}
	}
}

// PG asserts parallel_safe and consider_parallel; goopg refuses.
func TestAddPartialPath_RefusesUnsafeOrNonParallelRel(t *testing.T) {
	rel := &RelOptInfo{ConsiderParallel: true}
	unsafe := partialCostPath(rel, 0, 10)
	unsafe.ParallelSafe = false
	addPartialPath(rel, unsafe, "test")
	if len(rel.PartialPathlist) != 0 {
		t.Fatal("a non-parallel-safe path must not enter the partial list")
	}
	serialRel := &RelOptInfo{}
	addPartialPath(serialRel, partialCostPath(serialRel, 0, 10), "test")
	if len(serialRel.PartialPathlist) != 0 {
		t.Fatal("a rel that does not consider parallel must hold no partial path")
	}
}

// compute_parallel_worker's index-pages arm: each applicable count climbs its
// own ladder, either below its threshold is 0, and the smaller ladder wins.
func TestComputeParallelWorker_IndexPagesArm(t *testing.T) {
	cp := defaultCostParams() // table 1024 blocks, index 64 blocks, max 4
	cp.maxParallelWorkersPerGather = 8
	cases := []struct {
		name        string
		heap, index float64
		relopt      int
		want        int
	}{
		{"heap only (create_plain_partial_paths)", 20000, -1, 0, 3},
		{"index only (index-only scan)", -1, 5000, 0, 4},
		{"both: the smaller ladder wins", 20000, 5000, 0, 3},
		{"both: the smaller ladder wins, other way", 2000000, 200, 0, 2},
		{"index below min_parallel_index_scan_size", 20000, 63, 0, 0},
		{"heap below min_parallel_table_scan_size", 100, 5000, 0, 0},
		{"reloption wins outright", 100, 63, 3, 3},
		{"capped by max_parallel_workers_per_gather", 1e9, 1e9, 0, 8},
	}
	for _, c := range cases {
		if got := computeParallelWorker(cp, c.heap, c.index, c.relopt); got != c.want {
			t.Errorf("%s: computeParallelWorker(heap=%v, index=%v, relopt=%d) = %d, want %d", c.name, c.heap, c.index, c.relopt, got, c.want)
		}
	}
	// The C-19b twin is the heap-only call, unchanged.
	if a, b := computeParallelWorkerForRel(cp, 20000, 0), computeParallelWorker(cp, 20000, -1, 0); a != b {
		t.Errorf("computeParallelWorkerForRel(20000) = %d, computeParallelWorker(20000, -1) = %d", a, b)
	}
}

// cost_index's partial arm: the serial function is bit-identical to the
// zero-worker core; with workers the CPU run cost alone is divided.
func TestCostPartialIndexScan_DividesCPUOnly(t *testing.T) {
	cp := defaultCostParams()
	in := indexScanInputs{
		relPages: 20000, relTuples: 2_000_000,
		indexPages: 5000, indexTuples: 2_000_000, treeHeight: 2,
		selectivity: 1, totalTablePages: 20000,
	}
	serial := costIndexScan(cp, in)
	core, randHeap, indexPages := costIndexScanCore(cp, in, 0)
	if serial != core {
		t.Fatalf("serial cost_index %+v != zero-worker core %+v", serial, core)
	}
	if randHeap <= 0 || indexPages <= 0 {
		t.Fatalf("core must report the page counts the partial arm sizes on: heap %v index %v", randHeap, indexPages)
	}
	cost, rows, workers := costPartialIndexScan(cp, in, 1_000_000, 0)
	if want := computeParallelWorker(cp, randHeap, indexPages, 0); workers != want || workers <= 0 {
		t.Fatalf("workers = %d, want %d (> 0)", workers, want)
	}
	d := getParallelDivisor(workers, cp.parallelLeaderParticipation)
	cpu := cp.cpuTupleCost * clampRowEst(in.selectivity*in.relTuples)
	if wantTotal := serial.Total - cpu + cpu/d; math.Abs(cost.Total-wantTotal) > 1e-6 {
		t.Errorf("partial total %.4f, want serial %.4f - cpu %.4f + cpu/%.2f = %.4f", cost.Total, serial.Total, cpu, d, wantTotal)
	}
	if cost.Startup != serial.Startup {
		t.Errorf("startup must not be divided: %v vs %v", cost.Startup, serial.Startup)
	}
	if want := clampRowEst(1_000_000 / d); rows != want {
		t.Errorf("rows %v, want clamp_row_est(1e6 / %.2f) = %v", rows, d, want)
	}
	// Index-only: heap pages are ignored for sizing ("rand_heap_pages = -1").
	ios := in
	ios.indexOnly = true
	ios.relPages = 100 // far below min_parallel_table_scan_size
	if _, _, w := costPartialIndexScan(cp, ios, 1000, 0); w <= 0 {
		t.Errorf("an index-only partial path must be sized on index pages alone, got %d workers", w)
	}
	plain := in
	plain.relPages = 100
	plain.relTuples = 1000
	if _, _, w := costPartialIndexScan(cp, plain, 1000, 0); w != 0 {
		t.Errorf("a plain scan fetching too few heap pages must size at 0 workers, got %d", w)
	}
}

// cpIndexedProblem is cpBigProblem with the tables registered in a catalog
// and a btree index on each join column, so the ordered-index producer runs.
func cpIndexedProblem(t *testing.T, names []string) *joinlistProblem {
	t.Helper()
	prob := cpBigProblem(names)
	c := catalog.NewInMemory()
	for i, name := range names {
		old := prob.relInfos[i].table
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, old.Columns)
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = old.Stats
		prob.relInfos[i].table = tbl
		prob.bindings[i].table = tbl
		prob.scans[i] = &SeqScan{Table: tbl, Alias: name, schema: cpjSchema(name, rfjWidth)}
		if _, err := c.CreateIndex(parser.ObjectName{Name: name + "_idx"}, tbl, []string{name + "0"}, false, "btree", false); err != nil {
			t.Fatal(err)
		}
	}
	prob.cat = c
	return prob
}

// (b) A large rel with a usable ordered index holds TWO partial paths — the
// seq scan and the index scan — and the index twin is the serial index path
// with its CPU share divided.
func TestPartialIndexPath_PricedByCostIndexParallelArm(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b", "c"}
		prob := cpIndexedProblem(t, names)
		s := cpSearch(t, prob)
		cp := prob.cp
		sawIndexTwin := false
		for i, rel := range s.joinrels[1] {
			tbl := prob.relInfos[i].table
			var serialIdx *Path
			for _, p := range rel.Pathlist {
				if p.Kind == PathIndexScan && p.RequiredOuter == 0 && !p.IndexOnly {
					serialIdx = p
				}
			}
			var partialIdx, partialSeq *Path
			for _, p := range rel.PartialPathlist {
				switch p.Kind {
				case PathIndexScan:
					partialIdx = p
				case PathSeqScan:
					partialSeq = p
				}
			}
			if serialIdx == nil || tbl.Stats.Pages < 1024 {
				if partialIdx != nil {
					t.Errorf("%s: partial index path without a serial one / on a small rel", tbl.Name)
				}
				continue
			}
			if partialIdx == nil {
				t.Fatalf("%s: serial index path present but no partial twin (%d partial paths)", tbl.Name, len(rel.PartialPathlist))
			}
			if partialSeq == nil {
				t.Fatalf("%s: the partial seq scan must coexist with the partial index path", tbl.Name)
			}
			sawIndexTwin = true
			if partialIdx.ParallelWorkers <= 0 || !partialIdx.ParallelSafe {
				t.Errorf("%s: twin workers %d safe %v", tbl.Name, partialIdx.ParallelWorkers, partialIdx.ParallelSafe)
			}
			if partialIdx.IndexInfo != serialIdx.IndexInfo || len(partialIdx.Pathkeys) != len(serialIdx.Pathkeys) || partialIdx.IndexScanDir != serialIdx.IndexScanDir {
				t.Errorf("%s: twin must carry the serial path's index/pathkeys/direction", tbl.Name)
			}
			d := getParallelDivisor(partialIdx.ParallelWorkers, cp.parallelLeaderParticipation)
			relTuples := float64(prob.relInfos[i].baseRows)
			cpu := cp.cpuTupleCost * clampRowEst(relTuples)
			if wantTotal := serialIdx.Cost.Total - cpu + cpu/d; math.Abs(partialIdx.Cost.Total-wantTotal) > 1e-6 {
				t.Errorf("%s: twin total %.4f, want %.4f", tbl.Name, partialIdx.Cost.Total, wantTotal)
			}
			if partialIdx.Cost.Startup != serialIdx.Cost.Startup {
				t.Errorf("%s: twin startup %v, serial %v", tbl.Name, partialIdx.Cost.Startup, serialIdx.Cost.Startup)
			}
			if want := clampRowEst(serialIdx.Rows / d); partialIdx.Rows != want {
				t.Errorf("%s: twin rows %v, want %v", tbl.Name, partialIdx.Rows, want)
			}
			// The partial list is sorted by total cost, cheapest first.
			for j := 1; j < len(rel.PartialPathlist); j++ {
				if rel.PartialPathlist[j-1].Cost.Total > rel.PartialPathlist[j].Cost.Total {
					t.Errorf("%s: partial list not sorted at %d", tbl.Name, j)
				}
			}
		}
		if !sawIndexTwin {
			t.Fatal("fixture produced no partial index path; the pin would be vacuous")
		}
		// The serial arm is unchanged by construction: the same problem
		// without parallel mode yields the same Pathlist on every base rel.
		off := cpIndexedProblem(t, names)
		off.cp.maxParallelWorkersPerGather = 0
		s2 := cpSearch(t, off)
		for i := range s.joinrels[1] {
			on, offList := s.joinrels[1][i].Pathlist, s2.joinrels[1][i].Pathlist
			if len(on) != len(offList) {
				t.Fatalf("rel %d: serial pathlist differs with parallel on/off: %d vs %d", i, len(on), len(offList))
			}
			for j := range on {
				if on[j].Kind != offList[j].Kind || on[j].Cost != offList[j].Cost || on[j].Rows != offList[j].Rows {
					t.Errorf("rel %d path %d differs with parallel on/off", i, j)
				}
			}
			if n := len(s2.joinrels[1][i].PartialPathlist); n != 0 {
				t.Errorf("rel %d has %d partial paths at max_parallel_workers_per_gather = 0", i, n)
			}
		}
	})
}

// A `USING hash` index is not amcanparallel: no partial twin.
func TestPartialIndexPath_HashIndexHasNoTwin(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b", "c"}
		prob := cpIndexedProblem(t, names)
		for i := range names {
			for _, idx := range prob.cat.IndexesOnTable(prob.relInfos[i].table) {
				idx.DeclaredHash = true
			}
		}
		s := cpSearch(t, prob)
		for i, rel := range s.joinrels[1] {
			for _, p := range rel.PartialPathlist {
				if p.Kind == PathIndexScan {
					t.Errorf("rel %d: a hash index produced a partial index path", i)
				}
			}
		}
	})
}

// (c) drivingScan admits a bare btree range/full plain index scan, and only
// that; stampParallelScan labels the same node; sortPartialRootPays declines.
func TestDrivingScan_AdmitsBarePlainIndexScanOnly(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{{Name: "k", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "t_k_idx"}, tbl, []string{"k"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	key := &ColumnRef{Name: "k", Index: 0}
	bare := &IndexScan{Table: tbl, Index: idx, LowKey: &NumericConst{Value: "1"}}
	if drivingScan(bare) != bare {
		t.Fatal("a bare btree range index scan must be a driving scan")
	}
	if drivingScan(&Filter{Child: bare, Predicate: key}) != bare {
		t.Fatal("a Filter over the scan must still find it")
	}
	stamped := stampParallelScan(bare)
	if stamped == bare {
		t.Fatal("stampParallelScan must copy, not mutate")
	}
	if is, ok := stamped.(*IndexScan); !ok || !is.Parallel || bare.Parallel {
		t.Fatalf("stamped copy must carry Parallel=true and the original must not: %T", stamped)
	}
	refused := map[string]*IndexScan{
		"point probe Key":  {Table: tbl, Index: idx, Key: key},
		"point probe Keys": {Table: tbl, Index: idx, Keys: []Expr{key}},
		"SAOP":             {Table: tbl, Index: idx, SAOPKeys: []Expr{key}},
		"declared hash":    {Table: tbl, Index: &catalog.Index{Name: "h", Columns: []string{"k"}, Method: "btree", DeclaredHash: true}},
		"hash AM":          {Table: tbl, Index: &catalog.Index{Name: "h2", Columns: []string{"k"}, Method: "hash"}},
		"no index":         {Table: tbl},
	}
	for name, n := range refused {
		if drivingScan(n) != nil {
			t.Errorf("%s: must not be a driving scan", name)
		}
		if stampParallelScan(n) != n {
			t.Errorf("%s: stampParallelScan must return the node unchanged", name)
		}
	}
	if sortPartialRootPays(&Sort{Child: bare, Keys: []SortKey{{Expr: key}}}) {
		t.Fatal("a per-worker Sort over a plain index scan must decline (Gather Merge attaches no leaf claim set)")
	}
}
