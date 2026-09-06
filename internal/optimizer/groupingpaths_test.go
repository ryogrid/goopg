package optimizer

// C-15 (P4-06) — the GROUP_AGG upper rel's `cost_agg` port and producer.
// What is pinned: the three arms' exact terms (trans/final/grouping/output),
// the sorted-vs-hashed startup-only difference, the spill accrue directions
// (writes → startup+total, reads → total-only) with the unknown-width
// suppression, the producer's offering (hashed disabled-not-skipped under
// enable_hashagg=off, GUC-on PK-FD stays hash), the single-candidate-per-
// shape invariant, the nil-aggregate error, and the C-10c Sort arm one node
// up. Migrated rule-shape tests live on in their own files, unchanged.

import (
	"math"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor/hashsize"
)

// TestCostAggArmsExactTerms pins cost_agg term for term: trans per input
// tuple per agg, grouping comparisons per input tuple per group col, final
// per group, emit per group; sorted streams (startup = input startup),
// hashed blocks (startup = input total).
func TestCostAggArmsExactTerms(t *testing.T) {
	cp := defaultCostParams()
	const (
		rows   = 100000.0
		inStart = 500.0
		inTotal = 1500.0
		ncols   = 2
		groups  = 1000.0
		naggs   = 2
	)
	trans := cp.cpuOperatorCost * naggs * rows
	cmp := cp.cpuOperatorCost * ncols * rows
	fin := cp.cpuOperatorCost * naggs * groups
	emit := cp.cpuTupleCost * groups

	sorted := costAgg(cp, AggStrategySorted, rows, inStart, inTotal, ncols, groups, naggs, 8, 0)
	if !approx(sorted.Startup, inStart) {
		t.Fatalf("sorted startup = %v, want input startup %v (streams)", sorted.Startup, inStart)
	}
	if want := inTotal + trans + cmp + fin + emit; !approx(sorted.Total, want) {
		t.Fatalf("sorted total = %v, want %v", sorted.Total, want)
	}

	hashed := costAgg(cp, AggStrategyHashed, rows, inStart, inTotal, ncols, groups, naggs, 8, 0)
	if want := inTotal + trans + cmp; !approx(hashed.Startup, want) {
		t.Fatalf("hashed startup = %v, want %v (blocking)", hashed.Startup, want)
	}
	if want := hashed.Startup + fin + emit; !approx(hashed.Total, want) {
		t.Fatalf("hashed total = %v, want %v", hashed.Total, want)
	}
}

// TestCostAggSortedHashedShareTotalCpu pins costsize.c:2720-2732: with the
// same terms both arms cost the same total CPU — sorted wins on startup
// alone, i.e. iff the input is already ordered. A test that only asserts
// "sorted startup < hashed startup" would pass with a broken total too.
func TestCostAggSortedHashedShareTotalCpu(t *testing.T) {
	cp := defaultCostParams()
	sorted := costAgg(cp, AggStrategySorted, 50000, 200, 800, 3, 500, 1, 8, 0)
	hashed := costAgg(cp, AggStrategyHashed, 50000, 200, 800, 3, 500, 1, 8, 0)
	if sorted.Total != hashed.Total {
		t.Fatalf("sorted total %v != hashed total %v: the arms must share total CPU exactly (roundoff breaks the startup-only rule)", sorted.Total, hashed.Total)
	}
	if !(sorted.Startup < hashed.Startup) {
		t.Fatalf("sorted startup %v not below hashed %v", sorted.Startup, hashed.Startup)
	}
}

// TestCostAggHashedNeverChargesSpill pins the executor-faithful omission:
// `aggregateOp` performs grouped aggregation IN MEMORY with no spill path,
// so even a group count overflowing memory prices exactly the in-memory
// terms — trans + grouping comparisons on startup over the input total,
// final + emit per group on total. A spill charge here would be I/O that
// never happens; measured, it flipped Q3/Q10/Q13/Q18 to sorted (Q13
// 5.67 s → 8.71 s), all four away from PG's hash. Resume WITH executor
// spill support (cost_funcs.go names the terms).
func TestCostAggHashedNeverChargesSpill(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 16
	// Group footprint 4x over budget: WOULD spill if the arm existed.
	groups := math.Ceil(4.0 * float64(cp.workMem) / hashsize.EntryBytes(ncols, 0))
	rows := groups * 10
	if hashsize.Choose(groups, ncols, 0, cp.workMem).NBatch <= 1 {
		t.Fatalf("fixture does not overflow memory: NBatch = 1 (test setup broken)")
	}
	got := costAgg(cp, AggStrategyHashed, rows, 100, 500, ncols, groups, 1, ncols, 0)
	trans := cp.cpuOperatorCost * rows
	cmp := cp.cpuOperatorCost * ncols * rows
	fin := cp.cpuOperatorCost * groups
	emit := cp.cpuTupleCost * groups
	if want := 500 + trans + cmp; !approx(got.Startup, want) {
		t.Fatalf("startup = %v, want in-memory %v (no spill charge)", got.Startup, want)
	}
	if want := got.Startup + fin + emit; !approx(got.Total, want) {
		t.Fatalf("total = %v, want %v", got.Total, want)
	}
}

// groupingTestAgg builds a minimal grouped Aggregate over a priced child
// for direct producer drives: one int4 group col, one bare aggregate.
func groupingTestAgg(child Node) *Aggregate {
	return &Aggregate{
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "g", Type: catalog.Type{Name: "int4"}}},
		Aggs:       []AggregateCall{{Name: "sum", Arg: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "int4"}}}},
		Child:      child,
		Mode:       AggModeSimple,
		Strategy:   AggStrategyHashed,
	}
}

// groupingTestSeed wraps child in a sized GROUP_AGG input seed the way
// createGroupingPaths does (input rows + legacy cost, not grouped rows).
func groupingTestSeed(t *testing.T, agg *Aggregate) (*RelOptInfo, *Path) {
	t.Helper()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	sizeGroupingRelFromAgg(grouped, agg)
	seed := newPrebuiltPath(grouped, agg.Child)
	seed.Rows = float64(EstimateRows(agg.Child))
	seed.Cost = Cost{Total: 100}
	return grouped, seed
}

// TestAddGroupingPathsSingleCandidatePerShape pins the §5 negative: one
// hashed + one sorted candidate, never two of a kind, on a plain grouped
// aggregate with the GUC on.
func TestAddGroupingPathsSingleCandidatePerShape(t *testing.T) {
	cp := defaultCostParams()
	agg := groupingTestAgg(upperOrderedInput(1000))
	grouped, seed := groupingTestSeed(t, agg)
	addGroupingPaths(grouped, seed, agg, agg.Child, nil, cp, DefaultPlannerSettings())
	if len(grouped.Pathlist) != 2 {
		t.Fatalf("pathlist holds %d paths, want exactly 2 (one hashed, one sorted)", len(grouped.Pathlist))
	}
	seen := map[AggStrategy]int{}
	for _, p := range grouped.Pathlist {
		if p.Kind != PathAgg {
			t.Fatalf("path kind %d, want PathAgg", p.Kind)
		}
		seen[p.AggStrategy]++
	}
	if seen[AggStrategyHashed] != 1 || seen[AggStrategySorted] != 1 {
		t.Fatalf("strategies %v, want exactly one hashed and one sorted", seen)
	}
}

// TestCreateGroupingPathsOffersHashedDisabled pins the enable_hashagg=off
// migration: the hashed path is offered with disabled=1 (B-17a preference,
// never skip) and the winner is the sorted Sort+GroupAggregate — the old
// forced shape, now with a real cost_agg price.
func TestCreateGroupingPathsOffersHashedDisabled(t *testing.T) {
	lines := captureTrace(t, func() {
		cat := presortedAggCatalog(t)
		stmt := parseOne(t, "select sum(unique1) from tenk1 group by ten")
		if _, err := PlanWithSettings(stmt, cat, hashAggSettings(false)); err != nil {
			t.Fatal(err)
		}
	})
	var hashed, sorted string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "producer="+groupAggHashedProducer):
			hashed = l
		case strings.Contains(l, "producer="+groupAggSortedProducer):
			sorted = l
		}
	}
	if hashed == "" {
		t.Fatalf("no %s line: the hashed arm was skipped instead of disabled", groupAggHashedProducer)
	}
	if !strings.Contains(hashed, "disabled=1 ") {
		t.Fatalf("hashed line = %q, want disabled=1", hashed)
	}
	if sorted == "" {
		t.Fatalf("no %s line: the sorted winner was never offered", groupAggSortedProducer)
	}
}

// TestCreateGroupingPathsGucOnPkFdStaysHash is the §3.2 gate item: with
// enable_hashagg ON and group keys matching a btree leading prefix, the
// price competition must still pick the hash — the deleted proxy gate may
// not smuggle an uncalibrated index win into GUC-on plans. If this fails,
// the matcher stays GUC-gated and the failure is a B-15 witness.
func TestCreateGroupingPathsGucOnPkFdStaysHash(t *testing.T) {
	cat, _, _ := btgIndexOrderCatalog(t)
	stmt := parseOne(t, "select count(*) from btg group by y, x")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(true))
	if err != nil {
		t.Fatal(err)
	}
	a := indexOrderAggPlan(t, node)
	if a.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed (GUC-on PK-prefix must stay hash)", a.Strategy)
	}
	if _, ok := a.Child.(*Sort); ok {
		t.Fatalf("GUC-on PK-prefix gained a Sort child: the index variant stole a hash plan")
	}
}

// TestGroupingPathsC10cReassert is C-10c's per-item re-assert for C-15
// (DESIGN §7): the Sort the GROUP_AGG rel emits sits below the Aggregate
// exactly where the rules' Sorts sat, so
// pushSingleSideQualsIntoInnerJoinInputs' *Sort arm still descends through
// it. Mirrors the C-12 ORDERED test one node up. Driven with
// enable_hashagg=off so the SORTED candidate actually wins (with the GUC
// on, hashed is strictly cheaper than sorted-plus-Sort and the *Sort arm
// under test would never be exercised — the vacuous-pass trap).
func TestGroupingPathsC10cReassert(t *testing.T) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	resid := &Filter{Child: j, Predicate: srcGt(0, "id", 1, 7)}
	agg := &Aggregate{
		GroupExprs: []Expr{&ColumnRef{Index: 1, Name: "name", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1}},
		Aggs:       []AggregateCall{{Name: "sum", Arg: &ColumnRef{Index: 3, Name: "amount", Type: catalog.Type{Name: "int4"}}}},
		Child:      resid,
		Mode:       AggModeSimple,
		Strategy:   AggStrategyHashed,
	}
	u := newUpperRels()
	// Force the sorted candidate to win (hashed disabled): the Sort arm
	// below is what this test re-asserts.
	got, err := createGroupingPaths(u, agg, nil, hashAggSettings(false), 0)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := got.(*Aggregate)
	if !ok {
		t.Fatalf("producer returned %T, want *Aggregate", got)
	}
	if _, isSort := root.Child.(*Sort); !isSort {
		t.Fatalf("Aggregate.Child is %T, want *Sort (sorted must win GUC-off)", root.Child)
	}
	moved := pushSingleSideQualsIntoInnerJoinInputs(root)
	rj, ok := moved.(*Aggregate)
	if !ok {
		t.Fatalf("pass returned %T, want the *Aggregate root back", moved)
	}
	// The Sort stays (it is the aggregate's input); the residual Filter
	// passes THROUGH it to the Join below.
	srt, ok := rj.Child.(*Sort)
	if !ok {
		t.Fatalf("Aggregate.Child is %T, want the *Sort input", rj.Child)
	}
	nj, ok := srt.Child.(*Join)
	if !ok {
		t.Fatalf("Sort.Child is %T, want the *Join (residual spliced through the Sort arm)", srt.Child)
	}
	if _, placed := nj.Left.(*Filter); !placed {
		t.Errorf("Join.Left is %T, want the placed *Filter on the preserved input", nj.Left)
	}
	if _, planted := nj.Right.(*Filter); planted {
		t.Errorf("a Filter reached the NULLABLE input for a preserved-side qual")
	}
}

// TestCreateGroupingPathsNilAgg pins the defensive error: nil spec errors,
// never a nil node.
func TestCreateGroupingPathsNilAgg(t *testing.T) {
	if _, err := createGroupingPaths(nil, nil, nil, DefaultPlannerSettings(), 0); err == nil {
		t.Fatalf("nil aggregate: want an error, got nil")
	}
}
