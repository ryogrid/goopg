package optimizer

// C-16 (P4-07) — the DISTINCT upper rel's hashed / unique paths.
// What is pinned: the DISTINCT sizing (P1-25 estimate), the single-
// candidate-per-shape invariant, hashed-disabled (not skipped) under
// enable_hashagg=off with the unique winner, the Unique arm emitting
// all-columns DistinctOn over the producer-stacked Sort, the DISTINCT ON
// gate (producer never fires there), and the C-10c Sort arm one node up.

import (
	"math"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// distinctTestSpec builds a minimal DISTINCT spec over a priced child.
func distinctTestSpec(child Node) *Distinct {
	return &Distinct{pos: 0, Child: child, schema: child.Output()}
}

// TestSizeDistinctRelFromNode pins the §3.4 duty: Rows from P1-25's
// estimate, Width/NCols/AvgVarBytes from the output.
func TestSizeDistinctRelFromNode(t *testing.T) {
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	if rel.Rows < 1 {
		t.Fatalf("DISTINCT rel rows = %v, want >= 1", rel.Rows)
	}
	if rel.NCols != 3 {
		t.Fatalf("DISTINCT rel NCols = %d, want 3 (output width)", rel.NCols)
	}
	if rel.Width <= 0 {
		t.Fatalf("DISTINCT rel Width = %d, want > 0", rel.Width)
	}
}

// TestAddDistinctPathsSingleCandidatePerShape pins the §5 negative: one
// hashed + one unique candidate, never two of a kind — and no third
// "sorted Distinct", which would price and order identically to unique
// and die as a duplicate in add_path (offering it would be noise).
func TestAddDistinctPathsSingleCandidatePerShape(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	seed := newPrebuiltPath(rel, in)
	seed.Rows = 1000
	seed.Cost = Cost{Total: 100}
	addDistinctPaths(rel, seed, spec, in, cp, DefaultPlannerSettings())
	if len(rel.Pathlist) != 2 {
		t.Fatalf("pathlist holds %d paths, want exactly 2 (hashed, unique)", len(rel.Pathlist))
	}
	var hashed, unique int
	for _, p := range rel.Pathlist {
		if p.Kind != PathDistinct {
			t.Fatalf("path kind %d, want PathDistinct", p.Kind)
		}
		if p.Distinct == nil {
			t.Fatalf("PathDistinct without a spec")
		}
		if p.Unique {
			unique++
			continue
		}
		if len(p.Children) == 1 && p.Children[0] == seed {
			hashed++
			continue
		}
		t.Fatalf("unexpected third shape: unique=%v children=%d", p.Unique, len(p.Children))
	}
	if hashed != 1 || unique != 1 {
		t.Fatalf("hashed=%d unique=%d, want one of each", hashed, unique)
	}
}

// TestCreateDistinctPathsHashAggOffDisabled pins the GUC-off migration:
// the hashed path is offered with disabled=1 (B-17a preference, never
// skip). The winner pin lives in TestCreateDistinctPathsUniqueEmitsDistinctOn
// (DisabledNodes dominance forces unique there, not coincidence).
func TestCreateDistinctPathsHashAggOffDisabled(t *testing.T) {
	lines := captureTrace(t, func() {
		cat := presortedAggCatalog(t)
		stmt := parseOne(t, "select distinct ten from tenk1")
		if _, err := PlanWithSettings(stmt, cat, hashAggSettings(false)); err != nil {
			t.Fatal(err)
		}
	})
	var hashed string
	nDistinct := 0
	for _, l := range lines {
		if strings.Contains(l, "producer="+distinctHashedProducer) {
			hashed = l
		}
		if strings.Contains(l, "producer=upper.distinct.") {
			nDistinct++
		}
	}
	if hashed == "" {
		t.Fatalf("no %s line: the hashed arm was skipped instead of disabled", distinctHashedProducer)
	}
	if !strings.Contains(hashed, "disabled=1 ") {
		t.Fatalf("hashed line = %q, want disabled=1", hashed)
	}
	if nDistinct < 2 {
		t.Fatalf("only %d distinct producers fired, want hashed + at least one Sort-driven arm", nDistinct)
	}
}

// TestCreateDistinctPathsUniqueEmitsDistinctOn pins the C-16b arm mapping:
// with hashing disabled, the winner over a sorted input is a DistinctOn
// with all-output-columns keys (streaming adjacent dedup), not a Distinct.
func TestCreateDistinctPathsUniqueEmitsDistinctOn(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select distinct ten from tenk1")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	// Unwrap an optional Project root: single-column DISTINCT may plan
	// without one.
	var below Node
	if p, ok := node.(*Project); ok {
		below = p.Child
	} else {
		below = node
	}
	uo, ok := below.(*DistinctOn)
	if !ok {
		t.Fatalf("plan child is %T, want *DistinctOn (unique winner GUC-off)", below)
	}
	if len(uo.KeyCols) != len(uo.Output()) {
		t.Fatalf("DistinctOn KeyCols = %v over %d output cols, want all-columns keys", uo.KeyCols, len(uo.Output()))
	}
	if _, ok := uo.Child.(*Sort); !ok {
		t.Fatalf("DistinctOn.Child is %T, want *Sort (producer-stacked order)", uo.Child)
	}
}

// TestCreateDistinctPathsDistinctOnGate pins BLOCKING-2's gate: DISTINCT ON
// planning never enters the DISTINCT producer (both parsers leave
// Distinct=false there today; the producer gate is defense-in-depth).
func TestCreateDistinctPathsDistinctOnGate(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select distinct on (ten) ten, two from tenk1 order by ten")
	lines := captureTrace(t, func() {
		if _, err := Plan(stmt, cat); err != nil {
			t.Fatal(err)
		}
	})
	for _, l := range lines {
		if strings.Contains(l, "producer=upper.distinct.") {
			t.Fatalf("DISTINCT producer fired on a DISTINCT ON query: %q", l)
		}
	}
}

// TestDistinctPathsC10cReassert is C-10c's per-item re-assert for C-16
// (DESIGN §7): the producer introduces no new evaluation site below the
// DISTINCT, and the qual-placement pass treats the DISTINCT subtree
// exactly as before — it has no Distinct/DistinctOn arm, so it stops at
// the DISTINCT root whether the winner is hashed or unique. Driven GUC-off
// so the Sort-driven (unique) winner is exercised. What is pinned is
// preservation: the pass returns the winner with its Sort→Filter→Join
// input spine byte-identical (no descent, no splice, no drop).
func TestDistinctPathsC10cReassert(t *testing.T) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	resid := &Filter{Child: j, Predicate: srcGt(0, "id", 1, 7)}
	spec := &Distinct{Child: resid, schema: resid.Output()}
	got, err := createDistinctPaths(newUpperRels(), spec, nil, hashAggSettings(false), 0)
	if err != nil {
		t.Fatal(err)
	}
	uo, ok := got.(*DistinctOn)
	if !ok {
		t.Fatalf("producer returned %T, want *DistinctOn (unique must win GUC-off)", got)
	}
	srt, ok := uo.Child.(*Sort)
	if !ok {
		t.Fatalf("DistinctOn.Child is %T, want *Sort (producer-stacked order)", uo.Child)
	}
	if _, ok := srt.Child.(*Filter); !ok {
		t.Fatalf("Sort.Child is %T, want the *Filter input", srt.Child)
	}
	moved := pushSingleSideQualsIntoInnerJoinInputs(got)
	muo, ok := moved.(*DistinctOn)
	if !ok {
		t.Fatalf("pass returned %T, want the *DistinctOn root back", moved)
	}
	if moved != got {
		t.Fatalf("pass rebuilt the DISTINCT root: want pointer-identical (no arm fires below Distinct)")
	}
	msrt, ok := muo.Child.(*Sort)
	if !ok {
		t.Fatalf("after pass: DistinctOn.Child is %T, want *Sort (no descent, no splice)", muo.Child)
	}
	if _, ok := msrt.Child.(*Filter); !ok {
		t.Fatalf("after pass: Sort.Child is %T, want the untouched *Filter", msrt.Child)
	}
}

// TestCreateDistinctPathsNilSpec pins the defensive error.
func TestCreateDistinctPathsNilSpec(t *testing.T) {
	if _, err := createDistinctPaths(nil, nil, nil, DefaultPlannerSettings(), 0); err == nil {
		t.Fatalf("nil spec: want an error, got nil")
	}
}

// TestDistinctCostSharedInput pins the pricing shape: hashed vs unique
// differ only in input price (seed vs Sort) — the dedup terms are shared,
// pinned as numbers (unique == uniquePathCost over its Sort input exactly),
// and unique prices strictly above hashed (the Sort costs something).
func TestDistinctCostSharedInput(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	seed := newPrebuiltPath(rel, in)
	seed.Rows = 1000
	seed.Cost = Cost{Total: 100}
	addDistinctPaths(rel, seed, spec, in, cp, DefaultPlannerSettings())
	var unique *Path
	for _, p := range rel.Pathlist {
		if p.Unique {
			unique = p
		}
	}
	if unique == nil {
		t.Fatalf("no unique candidate offered")
	}
	if len(unique.Children) != 1 {
		t.Fatalf("unique has %d children, want the 1 Sort input", len(unique.Children))
	}
	sortIn := unique.Children[0]
	want := uniquePathCost(sortIn.Cost.Startup, sortIn.Cost.Total, 1000, len(unique.Pathkeys), cp)
	if unique.Cost != want {
		t.Fatalf("unique %+v != uniquePathCost over its Sort input %+v", unique.Cost, want)
	}
	for _, p := range rel.Pathlist {
		if p.Unique {
			continue
		}
		if !(unique.Cost.Total > p.Cost.Total) {
			t.Fatalf("unique %+v not above hashed %+v: the Sort must cost something", unique.Cost, p.Cost)
		}
	}
}

// ── M0146-0027 slice 2 — the partial-DISTINCT arm ──────────────────────────
//
// `addPartialDistinctPaths` is `create_partial_distinct_paths`'s sorted arm
// (planner.c:4852): `Unique -> Gather Merge -> Unique -> Sort -> <partial>`.
// Pinned: the candidate is generated and priced, the `PartialUnique` marker
// flows spec -> emitted node, the four walk siblings admit only the marked
// node, and every gate refuses fail-closed.

// sizedDistinctFixture mirrors sizedAggFixture's catalog-backed scan under a
// DISTINCT spec instead of an aggregate.
func sizedDistinctFixture(t *testing.T, rows int64, ndistinct float64) *Distinct {
	t.Helper()
	cat := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "g0", Type: catalog.Type{Name: "int4"}},
		{Name: "g1", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	}
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "distinct_sized_t"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	colStats := make([]catalog.ColumnStats, len(cols))
	for i := range colStats {
		colStats[i].NDistinct = int64(ndistinct)
		colStats[i].NDistinctFrac = ndistinct / float64(rows)
	}
	tbl.Stats = &catalog.TableStats{RowCount: rows, Columns: colStats}
	scan := &SeqScan{Table: tbl, schema: Schema{{Name: "g0"}, {Name: "g1"}, {Name: "v"}}}
	return &Distinct{Child: scan, schema: scan.Output()}
}

// addPartialDistinctFor runs the producer the way createDistinctPaths does and
// returns the DISTINCT rel plus the partial arm's final path (nil when the
// producer declined).
func addPartialDistinctFor(t *testing.T, d *Distinct, ps PlannerSettings) (*RelOptInfo, *Path) {
	t.Helper()
	cp := ps.costParams()
	u := newUpperRels()
	dr := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(dr, d)
	child := d.Child
	seed := newPrebuiltPath(dr, child)
	seed.Rows = float64(EstimateRows(child))
	if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}
	if seed.Cost.Total <= 0 {
		seed.Cost = costSeqscan(cp, estScanPages(seed.Rows, 32), seed.Rows, 0)
	}
	addPartialDistinctPaths(u, dr, seed, d, child, cp, ps, nil)
	for _, p := range dr.Pathlist {
		if p != nil && p.Kind == PathDistinct && p.Unique &&
			len(p.Children) == 1 && p.Children[0].Kind == PathGatherMerge {
			return dr, p
		}
	}
	return dr, nil
}

// sortedArmSettings is upperSplitSettings with enable_hashagg off, so the
// partial HASHED arm carries a disabled node and the sorted Unique arm the
// tests below inspect is the one add_path keeps (M0146-0005bf).
func sortedArmSettings() PlannerSettings {
	ps := upperSplitSettings()
	ps.EnableHashAgg = false
	return ps
}

// TestPartialDistinctHashedArmLowers pins the hashed arm of
// create_partial_distinct_paths (planner.c:4983, M0146-0005bf): PG 18.3's
// select_distinct.sql `SELECT DISTINCT four FROM tenk1` plan is
// `Unique -> Gather Merge -> Sort -> HashAggregate -> Parallel Seq Scan`.
// The per-worker node is a group-only hashed Aggregate carrying the
// PartialGroup mark the driving-scan walks descend through.
func TestPartialDistinctHashedArmLowers(t *testing.T) {
	prev := parallelOn.Load()
	parallelOn.Store(true)
	defer parallelOn.Store(prev)

	d := sizedDistinctFixture(t, 5_900_000, 4)
	_, final := addPartialDistinctFor(t, d, upperSplitSettings())
	if final == nil {
		t.Fatal("producer filed no candidate")
	}
	node, _ := createPlanNode(final)
	leader, ok := node.(*DistinctOn)
	if !ok || leader.PartialUnique {
		t.Fatalf("final path lowered to %T (marked=%v), want an unmarked *DistinctOn", node, ok && leader.PartialUnique)
	}
	gm, ok := leader.Child.(*GatherMerge)
	if !ok {
		t.Fatalf("leader child is %T, want *GatherMerge", leader.Child)
	}
	srt, ok := gm.Child.(*Sort)
	if !ok {
		t.Fatalf("merge child is %T, want the worker *Sort", gm.Child)
	}
	agg, ok := srt.Child.(*Aggregate)
	if !ok || !agg.PartialGroup || agg.Strategy != AggStrategyHashed || len(agg.Aggs) != 0 {
		t.Fatalf("worker sort child is %T, want a PartialGroup hashed group-only *Aggregate", srt.Child)
	}
	if len(agg.GroupExprs) != len(agg.Output()) {
		t.Fatalf("the dedup must group on all %d output columns, got %d", len(agg.Output()), len(agg.GroupExprs))
	}
	scan, ok := agg.Child.(*SeqScan)
	if !ok || !scan.Parallel {
		t.Fatalf("the hashed dedup's input is %T, want a Parallel *SeqScan", agg.Child)
	}
}

// partialDistinctChain unwraps the candidate's `Unique -> Gather Merge ->
// Unique -> Sort -> partial` spine, or nils when the shape differs.
func partialDistinctChain(final *Path) (gm, partial, workerSort *Path) {
	if final == nil || len(final.Children) != 1 {
		return nil, nil, nil
	}
	gm = final.Children[0]
	if gm.Kind != PathGatherMerge || len(gm.Children) != 1 {
		return nil, nil, nil
	}
	partial = gm.Children[0]
	if partial.Kind != PathDistinct || !partial.Unique || len(partial.Children) != 1 {
		return gm, nil, nil
	}
	workerSort = partial.Children[0]
	if workerSort.Kind != PathSort {
		return gm, partial, nil
	}
	return gm, partial, workerSort
}

func TestPartialDistinctArmFilesThePGShape(t *testing.T) {
	prev := parallelOn.Load()
	parallelOn.Store(true)
	defer parallelOn.Store(prev)

	d := sizedDistinctFixture(t, 5_900_000, 4)
	dr, final := addPartialDistinctFor(t, d, sortedArmSettings())
	if final == nil {
		t.Fatal("no `Unique -> Gather Merge -> Unique` candidate was filed")
	}
	gm, partial, workerSort := partialDistinctChain(final)
	if partial == nil || workerSort == nil {
		t.Fatal("candidate is not Unique -> Gather Merge -> Unique -> Sort")
	}
	if partial.Distinct == nil || !partial.Distinct.PartialUnique {
		t.Fatal("the partial Unique's spec does not carry the PartialUnique mark")
	}
	if final.Distinct == nil || final.Distinct.PartialUnique {
		t.Fatal("the leader Unique's spec must NOT carry the mark — it runs above the merge")
	}
	if partial.ParallelWorkers <= 0 || workerSort.ParallelWorkers <= 0 {
		t.Fatal("worker-side Unique/Sort carry no planned workers")
	}
	if len(gm.Pathkeys) == 0 || len(partial.Pathkeys) == 0 || len(final.Pathkeys) == 0 {
		t.Fatal("the merge carries no pathkeys; the boundary is not ordered")
	}
	// The merge keys are the full-column dedup ordering: every output
	// position, in order.
	for i, pk := range gm.Pathkeys {
		cr, ok := pk.Expr.(*ColumnRef)
		if !ok || cr.Index != i {
			t.Fatalf("merge key %d = %#v, want ColumnRef at output position %d", i, pk.Expr, i)
		}
	}
	if len(dr.Pathlist) == 0 {
		t.Fatal("no path filed on the DISTINCT rel")
	}
}

// TestPartialDistinctArmLowers drives the filed path through createPlanNode:
// the emitted node chain must be DistinctOn -> GatherMerge ->
// DistinctOn{PartialUnique} -> Sort -> driving scan stamped Parallel — the
// marker is what lets gatherChildPlan's drivingScan assertion reach the scan.
func TestPartialDistinctArmLowers(t *testing.T) {
	prev := parallelOn.Load()
	parallelOn.Store(true)
	defer parallelOn.Store(prev)

	d := sizedDistinctFixture(t, 5_900_000, 4)
	_, final := addPartialDistinctFor(t, d, sortedArmSettings())
	if final == nil {
		t.Fatal("producer filed no candidate")
	}
	node, _ := createPlanNode(final)
	leader, ok := node.(*DistinctOn)
	if !ok {
		t.Fatalf("final path lowered to %T, want *DistinctOn", node)
	}
	if leader.PartialUnique {
		t.Fatal("the leader Unique must be unmarked — it runs above the merge")
	}
	gm, ok := leader.Child.(*GatherMerge)
	if !ok {
		t.Fatalf("leader child is %T, want *GatherMerge", leader.Child)
	}
	worker, ok := gm.Child.(*DistinctOn)
	if !ok {
		t.Fatalf("merge child is %T, want *DistinctOn", gm.Child)
	}
	if !worker.PartialUnique {
		t.Fatal("the per-worker Unique lost its PartialUnique mark")
	}
	srt, ok := worker.Child.(*Sort)
	if !ok {
		t.Fatalf("worker Unique child is %T, want *Sort", worker.Child)
	}
	scan, ok := srt.Child.(*SeqScan)
	if !ok {
		t.Fatalf("worker sort child is %T, want *SeqScan", srt.Child)
	}
	if !scan.Parallel {
		t.Fatal("the driving scan under the partial subtree is not stamped Parallel")
	}
}

// TestPartialDistinctArmUnwrapsGather is the Q38/Q87 route: a DISTINCT input
// already carrying `Sort -> Gather -> <subtree>` — the leader Sort is
// replaced by the worker-side one and the Gather spliced out, so the partial
// subtree runs the join/scan itself once per worker.
func TestPartialDistinctArmUnwrapsGather(t *testing.T) {
	prev := parallelOn.Load()
	parallelOn.Store(true)
	defer parallelOn.Store(prev)

	d := sizedDistinctFixture(t, 5_900_000, 4)
	gathered := &Gather{Child: d.Child, WorkersPlanned: 2, schema: d.Child.Output()}
	d.Child = &Sort{Child: gathered, Keys: distinctAllColKeys(gathered)}

	_, final := addPartialDistinctFor(t, d, upperSplitSettings())
	if final == nil {
		t.Fatal("a Gather-bearing DISTINCT input produced no candidate")
	}
	node, _ := createPlanNode(final)
	leader, ok := node.(*DistinctOn)
	if !ok {
		t.Fatalf("final path lowered to %T, want *DistinctOn", node)
	}
	gm, ok := leader.Child.(*GatherMerge)
	if !ok {
		t.Fatalf("leader child is %T, want *GatherMerge", leader.Child)
	}
	// No second Gather may survive inside the partial subtree — every
	// worker would read the whole relation.
	if subtreeHasGather(gm.Child) {
		t.Fatal("a Gather survived inside the partial subtree")
	}
	if drivingScan(gm.Child) == nil {
		t.Fatal("the partial subtree has no driving scan")
	}
}

// TestPartialDistinctArmRefusals pins the fail-closed gates: a nested-scope
// input that does not already carry a Gather, and the parallelism kill
// switch, each decline rather than building the shape.
func TestPartialDistinctArmRefusals(t *testing.T) {
	prev := parallelOn.Load()
	parallelOn.Store(true)
	defer parallelOn.Store(prev)

	t.Run("nested scope without existing gather", func(t *testing.T) {
		ps := upperSplitSettings()
		ps.ParallelStatementOK = false
		d := sizedDistinctFixture(t, 5_900_000, 4)
		if _, final := addPartialDistinctFor(t, d, ps); final != nil {
			t.Fatal("a nested-scope input with no Gather must not introduce one")
		}
	})
	t.Run("nested scope with existing gather", func(t *testing.T) {
		ps := upperSplitSettings()
		ps.ParallelStatementOK = false
		d := sizedDistinctFixture(t, 5_900_000, 4)
		d.Child = &Gather{Child: d.Child, WorkersPlanned: 2, schema: d.Child.Output()}
		if _, final := addPartialDistinctFor(t, d, ps); final == nil {
			t.Fatal("a nested-scope input already under a Gather keeps the arm")
		}
	})
	t.Run("workers disabled", func(t *testing.T) {
		ps := upperSplitSettings()
		ps.MaxParallelWorkersPerGather = 0
		d := sizedDistinctFixture(t, 5_900_000, 4)
		if _, final := addPartialDistinctFor(t, d, ps); final != nil {
			t.Fatal("max_parallel_workers_per_gather=0 must refuse the arm")
		}
	})
	t.Run("parallel off", func(t *testing.T) {
		parallelOn.Store(false)
		defer parallelOn.Store(true)
		d := sizedDistinctFixture(t, 5_900_000, 4)
		if _, final := addPartialDistinctFor(t, d, upperSplitSettings()); final != nil {
			t.Fatal("the parallel kill switch must refuse the arm")
		}
	})
}

// TestPartialDistinctWalkAgreement pins the sibling contract for
// *DistinctOn — the same four-walk agreement TestPartialGroupWalkAgreement
// pins for *Aggregate: the marked node is transparent to the stamp/driving
// walks and the unmarked node is a wall.
func TestPartialDistinctWalkAgreement(t *testing.T) {
	scan := sizedDistinctFixture(t, 100, 4).Child
	marked := &DistinctOn{Child: scan, PartialUnique: true}
	unmarked := &DistinctOn{Child: scan}

	if got := drivingScan(marked); got != Node(scan) {
		t.Fatalf("drivingScan(marked) = %#v, want the scan", got)
	}
	if got := drivingScan(unmarked); got != nil {
		t.Fatalf("drivingScan(unmarked) = %#v, want nil — an ordinary dedup is a wall", got)
	}

	stamped := stampParallelScan(marked)
	st, ok := stamped.(*DistinctOn)
	if !ok {
		t.Fatalf("stampParallelScan returned %T, want *DistinctOn", stamped)
	}
	if s, ok := st.Child.(*SeqScan); !ok || !s.Parallel {
		t.Fatal("stampParallelScan did not reach the scan under the marked partial Unique")
	}
	if got := stampParallelScan(unmarked); got != Node(unmarked) {
		t.Fatal("stampParallelScan descended through an unmarked dedup")
	}

	unstamped := unstampParallelScan(st)
	ut := unstamped.(*DistinctOn)
	if s, ok := ut.Child.(*SeqScan); !ok || s.Parallel {
		t.Fatal("unstampParallelScan did not strip the label under the marked partial Unique")
	}
	if !drivingScanCrossesSort(&DistinctOn{PartialUnique: true, Child: &Sort{Child: scan}}) {
		t.Fatal("drivingScanCrossesSort must see the Sort under the marked partial Unique")
	}
	if drivingScanCrossesSort(&DistinctOn{Child: &Sort{Child: scan}}) {
		t.Fatal("drivingScanCrossesSort descended through an unmarked dedup")
	}
}

// TestUniquePathCostIsCreateUpperUniquePath pins M0146-0005bi against PG
// 18.3's create_upper_unique_path on TPC-DS Q87's leader Unique: the input's
// startup unchanged, and 0.0025 per compared column per input row on top of
// its total (`Gather Merge (cost=19352.41..19738.09 rows=3260)` ->
// `Unique (cost=19352.41..19762.54)` over three columns).
func TestUniquePathCostIsCreateUpperUniquePath(t *testing.T) {
	got := uniquePathCost(19352.41, 19738.09, 3260, 3, defaultCostParams())
	if got.Startup != 19352.41 || math.Abs(got.Total-19762.54) > 0.005 {
		t.Fatalf("Unique cost %+v, want PG's 19352.41..19762.54", got)
	}
}
