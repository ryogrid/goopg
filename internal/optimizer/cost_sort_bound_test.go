package optimizer

// C-13b (P4-04 cost arm) — `cost_tuplesort`'s `limit_tuples` middle branch
// (costsize.c:1898-1982). What is pinned: the bound's derivation
// (count+offset, WITH TIES / non-constant / OFFSET-only all decline to -1),
// the bounded-heap price sitting strictly below the unbounded one with the
// exact `N log2 K` formula, continuity at the `tuples == 2*output` crossover,
// useless bounds pricing identically to no bound, the disk arm still firing
// when the BOUND output itself spills (and NOT firing when only the input
// would), no heap price from an unknown width, and the ORDERED producer
// actually carrying the bound to the Sort (plus the merge side defaulting to
// unbounded).

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func boundForSQL(t *testing.T, sql string) float64 {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: got %T, want *parser.SelectStmt", sql, stmts[0])
	}
	// Literals resolve without touching the context; the empty context
	// proves it — a literal that read bindings would fail here.
	return limitTuplesForOrderedSort(sel, newResolveContext(nil, nil, DefaultPlannerSettings()))
}

// TestLimitTuplesForOrderedSort pins the resolver end to end on parsed SQL:
// constants produce count+offset, LIMIT 0 clamps to a bound of 1 through
// limitClauseEstimate, and WITH TIES / parameters / subqueries / OFFSET-only
// all decline to -1.
func TestLimitTuplesForOrderedSort(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want float64
	}{
		{"SELECT a FROM t ORDER BY a LIMIT 10", 10},
		{"SELECT a FROM t ORDER BY a LIMIT 10 OFFSET 5", 15},
		{"SELECT a FROM t ORDER BY a LIMIT 0", 1},
		{"SELECT a FROM t ORDER BY a", -1},
		{"SELECT a FROM t ORDER BY a OFFSET 5", -1},
		{"SELECT a FROM t ORDER BY a FETCH FIRST 10 ROWS WITH TIES", -1},
		{"SELECT a FROM t ORDER BY a LIMIT $1", -1},
		{"SELECT a FROM t ORDER BY a LIMIT (SELECT 10)", -1},
		{"SELECT a FROM t ORDER BY a LIMIT 10 + 5", -1},
	} {
		if got := boundForSQL(t, tc.sql); got != tc.want {
			t.Errorf("%s: bound = %v, want %v", tc.sql, got, tc.want)
		}
	}
	if got := limitTuplesForOrderedSort(nil, newResolveContext(nil, nil, DefaultPlannerSettings())); got != -1 {
		t.Errorf("nil statement: bound = %v, want -1", got)
	}
}

func TestLimitTuplesFromEstimates(t *testing.T) {
	for _, tc := range []struct {
		name             string
		count, offset    int64
		withTies         bool
		want             float64
	}{
		{"limit only", 10, 0, false, 10},
		{"limit plus offset skips must be produced", 10, 5, false, 15},
		{"offset without limit is not a bound", 0, 5, false, -1},
		{"neither clause", 0, 0, false, -1},
		{"with ties can exceed the count", 10, 0, true, -1},
		{"with ties plus offset", 10, 5, true, -1},
		{"non-constant count is a fraction not a bound", -1, 0, false, -1},
		{"non-constant offset", 10, -1, false, -1},
	} {
		if got := limitTuplesFromEstimates(tc.count, tc.offset, tc.withTies); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCostSortRun_BoundPricesBelowUnbounded pins the middle branch: with a
// useful bound the price is the exact `comparison * N * log2(2K)` heap term,
// strictly below the quicksort price.
func TestCostSortRun_BoundPricesBelowUnbounded(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 8
	// 0.5x budget: input fits, so the unbounded arm is pure quicksort and
	// any difference is the bound alone.
	rows := sortRowsFillingBudget(cp.workMem, ncols, 0.5)
	const bound = 100.0
	if rows <= 2*bound {
		t.Fatalf("fixture rows=%v do not clear the 2*K crossover (K=%v)", rows, bound)
	}

	bounded := costSortRun(cp, rows, ncols, 0, bound)
	plain := costSortRun(cp, rows, ncols, 0, -1)
	if !(bounded.Startup < plain.Startup) {
		t.Fatalf("bounded startup %v not below unbounded %v (N=%v K=%v)", bounded.Startup, plain.Startup, rows, bound)
	}
	wantStartup := 2 * cp.cpuOperatorCost * rows * math.Log2(2.0*bound)
	if !approx(bounded.Startup, wantStartup) {
		t.Fatalf("bounded startup = %v, want N log2 K = %v", bounded.Startup, wantStartup)
	}
	// The run cost still charges every INPUT tuple: the upper LIMIT
	// pro-rates it, so charging output here would double-count (costsize.c).
	if !approx(bounded.Total-bounded.Startup, cp.cpuOperatorCost*rows) {
		t.Fatalf("bounded run = %v, want cpu_operator_cost per input row", bounded.Total-bounded.Startup)
	}
}

// TestCostSortRun_BoundContinuityAtCrossover pins PG's continuity tweak: at
// tuples == 2*output the heap term prices identically to quicksort.
func TestCostSortRun_BoundContinuityAtCrossover(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 8
	rows := sortRowsFillingBudget(cp.workMem, ncols, 0.5)
	bound := rows / 2

	bounded := costSortRun(cp, rows, ncols, 0, bound)
	plain := costSortRun(cp, rows, ncols, 0, -1)
	if !approx(bounded.Startup, plain.Startup) {
		t.Fatalf("at N == 2K: bounded %v != quicksort %v — the curve is discontinuous", bounded.Startup, plain.Startup)
	}
}

// TestCostSortRun_UselessBoundPricesAsUnbounded: a bound at or above the
// input size, zero, or negative is not useful (`limit_tuples < tuples`
// fails) and must price byte-identically to no bound.
func TestCostSortRun_UselessBoundPricesAsUnbounded(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 8
	rows := sortRowsFillingBudget(cp.workMem, ncols, 0.5)
	plain := costSortRun(cp, rows, ncols, 0, -1)
	for _, bound := range []float64{0, -1, rows, rows * 4} {
		if got := costSortRun(cp, rows, ncols, 0, bound); got != plain {
			t.Errorf("bound %v: got %+v, want the unbounded price %+v", bound, got, plain)
		}
	}
}

// TestCostSortRun_BoundDiskArmFollowsOutputBytes: the spill branch is chosen
// on the OUTPUT size. A huge input with a tiny fitting bound takes the heap
// arm (no I/O invented for rows that never materialize); a bound whose own
// output spills still takes the disk arm.
func TestCostSortRun_BoundDiskArmFollowsOutputBytes(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 16
	big := sortRowsFillingBudget(cp.workMem, ncols, 4.0) // input spills 4x over

	// Tiny bound, fitting output: heap price, far below the unbounded disk price.
	small := costSortRun(cp, big, ncols, 0, 100)
	plain := costSortRun(cp, big, ncols, 0, -1)
	if !(small.Startup < plain.Startup) {
		t.Fatalf("bounded-fitting startup %v not below spilling-unbounded %v", small.Startup, plain.Startup)
	}
	// The heap price must carry NO page term: exactly the N log2 K formula.
	if want := 2 * cp.cpuOperatorCost * big * math.Log2(200); !approx(small.Startup, want) {
		t.Fatalf("bounded-fitting startup = %v, want pure heap term %v (input-sized I/O leaked in)", small.Startup, want)
	}

	// Bound whose output itself spills 2x over: disk arm, above the heap term.
	spilling := sortRowsFillingBudget(cp.workMem, ncols, 2.0)
	got := costSortRun(cp, big, ncols, 0, spilling)
	heapTerm := 2 * cp.cpuOperatorCost * big * math.Log2(2.0*spilling)
	if !(got.Startup > heapTerm) {
		t.Fatalf("bound-output-spilling startup %v not above the heap term %v: the disk arm did not fire", got.Startup, heapTerm)
	}
	// Exact identity: a spilling bound takes the SAME disk branch as no
	// bound — the page/run math sizes the input in both, and the comparison
	// term is N log2 N in both. The bound changes nothing once the output
	// itself spills.
	if want := costSortRun(cp, big, ncols, 0, -1); got != want {
		t.Fatalf("bound-output-spilling = %+v, want the unbounded disk price %+v", got, want)
	}
}

// TestCostSortRun_BoundWithUnknownWidthKeepsQuicksort: at ncols == 0 the
// output_bytes are unknowable, so even a useful bound must not invent a heap
// price from a guessed width — the quicksort number stands.
func TestCostSortRun_BoundWithUnknownWidthKeepsQuicksort(t *testing.T) {
	cp := defaultCostParams()
	rows := 100000.0
	plain := costSortRun(cp, rows, 0, 0, -1)
	if got := costSortRun(cp, rows, 0, 0, 100); got != plain {
		t.Errorf("unknown width with bound: got %+v, want the unbounded price %+v", got, plain)
	}
}

// TestCreateOrderedPathsCarriesBoundToSort is the plumbing pin: the bound
// argument reaches the emitted Sort's price, and -1 reproduces the unbounded
// number exactly.
func TestCreateOrderedPathsCarriesBoundToSort(t *testing.T) {
	cp := defaultCostParams()
	u := newUpperRels()
	in := upperOrderedInput(100000)
	keys := upperOrderedKeys()

	bounded := createOrderedPaths(u, in, keys, 0, cp, 0, 100).(*Sort)
	bpc, _ := bounded.PlanCostInfo()
	rel := fetchUpperRel(u, UpperOrdered, 0, 0)
	want := costSortRun(cp, 100000, relNCols(rel), relAvgVarBytes(rel), 100)
	if !approx(bpc.StartupCost, 100+want.Startup) || !approx(bpc.TotalCost, 100+want.Total) {
		t.Fatalf("bounded Sort cost = (%v, %v), want input 100 + costSortRun(K=100) (%v, %v)",
			bpc.StartupCost, bpc.TotalCost, 100+want.Startup, 100+want.Total)
	}

	plain := createOrderedPaths(newUpperRels(), upperOrderedInput(100000), keys, 0, cp, 0, -1).(*Sort)
	ppc, _ := plain.PlanCostInfo()
	unbounded := costSortRun(cp, 100000, relNCols(rel), relAvgVarBytes(rel), -1)
	if !approx(ppc.StartupCost, 100+unbounded.Startup) {
		t.Fatalf("unbounded Sort cost = %v, want input 100 + unbounded costSortRun %v", ppc.StartupCost, 100+unbounded.Startup)
	}
}

// TestSortPathForBoundedDefaultsToUnbounded pins the merge side: sortPathFor
// (no bound) is sortPathForBounded with -1, so no merge caller moves.
func TestSortPathForBoundedDefaultsToUnbounded(t *testing.T) {
	cp := defaultCostParams()
	seed := func() *Path {
		u := newUpperRels()
		rel := fetchUpperRel(u, UpperOrdered, 0, 0)
		sizeUpperRelFromNode(rel, upperOrderedInput(1000))
		p := newPrebuiltPath(rel, upperOrderedInput(1000))
		p.Cost = Cost{Total: 100}
		return p
	}
	keys := pathkeysForSortKeys(upperOrderedKeys())
	if a, b := sortPathFor(seed(), keys, cp), sortPathForBounded(seed(), keys, cp, -1); a.Cost.Startup != b.Cost.Startup || a.Cost.Total != b.Cost.Total {
		t.Fatalf("sortPathFor != sortPathForBounded(-1): %+v vs %+v", a.Cost, b.Cost)
	}
}
