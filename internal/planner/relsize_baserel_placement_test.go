package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M0127-P5.6's re-evaluation of M0125-0003 "stage 3" against 04 §2's rows-once
// discipline. Stage 3 was filed as a fourth staged consumer that would make
// `estimateBaseRelInfo.baseRows` positive on a cold server; under the old DP
// that would have SHADOWED the stage-2 seed tier, which is why it needed its
// own flag stage to sequence. The new search reads a base relation's
// cardinality exactly once — `initialRelRows` → `baseRelInfo.filteredRows` —
// so there is no second consumer left to shadow, and the placement stage 3
// described is simply where `applyRelSizeFallback` puts it, at the stage the
// seam already runs at.
//
// These tests pin the two halves of that verdict: the placement is a no-op in
// the S-cold state the seam was built for, and it is load-bearing in the
// post-restart state where column statistics outlive the row count.

// newPlacementFixture builds a leaf whose live block count is known to the
// catalog but whose `TableStats` are supplied by the caller — the axis the
// placement question turns on. The predicate is `c = 'x'`, the shape
// `eqOpSelectivityWithSource` can price from an MCV list and cannot price
// without one.
func newPlacementFixture(stats *catalog.TableStats) (*catalog.InMemory, rangeBinding, Node, Expr) {
	cat := catalog.NewInMemory()
	cat.SetRelationSizer(func(storage.RelFileNode) (int64, bool) { return 100, true })
	tbl := &catalog.Table{
		Name:    "restored",
		Columns: []catalog.Column{{Name: "c", Type: catalog.Type{Name: "char"}}},
		Stats:   stats,
	}
	scan := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	pred := &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: "char"}},
		Right: &StringConst{Value: "x"},
	}
	return cat, rangeBinding{table: tbl, offset: 0}, scan, pred
}

// TestRelSizeFallbackPlacementIdenticalWhenStatsAbsent is the half of the
// verdict that makes this a re-derivation rather than a behaviour change: in
// the S-cold state (`Stats == nil`) the post-filter placement and the
// pre-filter stamping it replaces produce the same number, because
// `columnStatsForChild` answers nil for every column of a stats-less table, so
// every clause reports `reliable=false` and the selectivity is never applied.
//
// The earlier seam comment justified stamping both fields pre-filter with "a
// server with no row count has no column statistics either, so scaling it by a
// selectivity invents precision". That premise is true here and is now enforced
// by the reliability gate instead of by refusing to scale — which is what lets
// the same code be right in the state below, where the premise is false.
func TestRelSizeFallbackPlacementIdenticalWhenStatsAbsent(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	SetRelSizeFallbackStage(2)

	cat, binding, scan, pred := newPlacementFixture(nil)

	// The selectivity gate is the mechanism the equality rests on — assert it
	// directly, so a future change that starts trusting default selectivities
	// fails HERE with its reason rather than in a plan diff.
	sel := clauseSelectivityWithSource(localizeExprToLeaf(pred, binding), scan)
	if sel.reliable {
		t.Fatalf("a stats-less table must price no clause reliably; got reliable=%v value=%v", sel.reliable, sel.value)
	}

	want := estimateTableRowsFallback(cat, binding.table)
	if want <= 1 {
		t.Fatalf("fixture is not exercising the fallback: estimate = %d", want)
	}

	info := estimateBaseRelInfo(binding, scan, pred)
	if info.filteredRows != 0 {
		t.Fatalf("precondition: a stats-less table must estimate 0 before the fallback, got %d", info.filteredRows)
	}
	applyRelSizeFallback(&info, binding, scan, pred, cat)

	if info.baseRows != want {
		t.Errorf("baseRows = %d; want the block-derived estimate %d", info.baseRows, want)
	}
	// The load-bearing assertion: UNSCALED, i.e. byte-identical to the
	// pre-filter stamping. S-cold plans cannot move on this change.
	if info.filteredRows != want {
		t.Errorf("filteredRows = %d; want %d unscaled (pre- and post-filter placements must coincide S-cold)", info.filteredRows, want)
	}
}

// TestRelSizeFallbackPlacementScalesRestoredColumnStats is the half that makes
// the placement load-bearing. `loadStatisticsFromHeap` restores per-column
// statistics across a restart while `TableStats.RowCount` does not survive
// (ledger pq-P6), so `Analyzed=true, Columns populated, RowCount=0` is the
// state a restarted goopg is actually in — not an edge case. Upstream sizes
// such a relation by feeding `estimate_rel_size`'s block-derived `tuples` to
// `set_baserel_size_estimates`, which multiplies by
// `clauselist_selectivity(baserestrictinfo)`; the pre-filter stamping this
// replaces threw the restored MCV list away and handed the search the whole
// relation.
func TestRelSizeFallbackPlacementScalesRestoredColumnStats(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	SetRelSizeFallbackStage(2)

	cat, binding, scan, pred := newPlacementFixture(&catalog.TableStats{
		Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 10, MCV: []catalog.MCVEntry{{Value: "x", Frequency: 0.1}}},
		},
	})

	base := estimateTableRowsFallback(cat, binding.table)
	if base <= 10 {
		t.Fatalf("fixture is not exercising the fallback: estimate = %d", base)
	}

	info := estimateBaseRelInfo(binding, scan, pred)
	if info.filteredRows != 0 {
		t.Fatalf("precondition: RowCount=0 must estimate 0 before the fallback, got %d", info.filteredRows)
	}
	applyRelSizeFallback(&info, binding, scan, pred, cat)

	if info.baseRows != base {
		t.Errorf("baseRows = %d; want the block-derived estimate %d", info.baseRows, base)
	}
	want := scaleByFloat(base, 0.1)
	if info.filteredRows != want {
		t.Errorf("filteredRows = %d; want %d (base %d × restored MCV frequency 0.1)", info.filteredRows, want, base)
	}
	if info.filteredRows >= info.baseRows {
		t.Errorf("the restored MCV list must reduce the estimate: filtered %d >= base %d", info.filteredRows, info.baseRows)
	}
}

// TestRelSizeFallbackPlacementInertWhenRowCountKnown pins the tier ORDER, which
// the move from a pre-filter to a post-filter placement must not disturb: a
// relation whose post-filter count is already positive is untouched, so a warm
// server never reads a block-derived estimate over its own ANALYZEd one. The
// nil-table and disabled-flag directions are the "no estimate means keep the
// old answer" contract every staged consumer owes.
func TestRelSizeFallbackPlacementInertWhenRowCountKnown(t *testing.T) {
	defer SetRelSizeFallbackStage(SetRelSizeFallbackStage(0))
	SetRelSizeFallbackStage(2)

	cat, binding, scan, pred := newPlacementFixture(&catalog.TableStats{
		RowCount: 4242,
		Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 10, MCV: []catalog.MCVEntry{{Value: "x", Frequency: 0.1}}},
		},
	})

	warm := estimateBaseRelInfo(binding, scan, pred)
	before := warm
	applyRelSizeFallback(&warm, binding, scan, pred, cat)
	if warm != before {
		t.Errorf("a relation with a positive post-filter count must be untouched: %+v -> %+v", before, warm)
	}
	if warm.baseRows != 4242 {
		t.Errorf("baseRows = %d; want the ANALYZEd 4242", warm.baseRows)
	}

	// Flag off: the seam must fall back to the pre-M0125-0003 answer.
	SetRelSizeFallbackStage(1)
	cold, coldBinding, coldScan, coldPred := newPlacementFixture(nil)
	info := estimateBaseRelInfo(coldBinding, coldScan, coldPred)
	applyRelSizeFallback(&info, coldBinding, coldScan, coldPred, cold)
	if info.baseRows != 0 || info.filteredRows != 0 {
		t.Errorf("below stage 2 the fallback must not fire: base=%d filtered=%d", info.baseRows, info.filteredRows)
	}

	// A nil info must not panic the planner (the seam always passes a real
	// pointer; this is the contract, not a reachable call).
	applyRelSizeFallback(nil, coldBinding, coldScan, coldPred, cold)
}

// TestApplyLocalFilterSelectivityMatchesEstimateBaseRelInfo is the hard-won
// rule #2 check on the factoring itself: `applyLocalFilterSelectivity` was
// lifted out of `estimateBaseRelInfo` so the fallback could reuse it, and the
// two must not drift. Every arm of the extracted function is covered — no
// filter, no scan, non-positive base, unreliable selectivity, reliable
// selectivity, and the 1-row floor.
func TestApplyLocalFilterSelectivityMatchesEstimateBaseRelInfo(t *testing.T) {
	mkTable := func(rows int64, cols []catalog.ColumnStats) *catalog.Table {
		var stats *catalog.TableStats
		if cols != nil || rows > 0 {
			stats = &catalog.TableStats{RowCount: rows, Columns: cols}
		}
		return &catalog.Table{
			Name:    "t",
			Columns: []catalog.Column{{Name: "c", Type: catalog.Type{Name: "char"}}},
			Stats:   stats,
		}
	}
	eq := func(v string) Expr {
		return &BinaryOp{
			Op:    parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: "char"}},
			Right: &StringConst{Value: v},
		}
	}
	mcv := []catalog.ColumnStats{{NDistinct: 10, MCV: []catalog.MCVEntry{{Value: "x", Frequency: 0.25}}}}

	cases := []struct {
		name    string
		tbl     *catalog.Table
		pred    Expr
		noScan  bool
		want    int64
		wantSel bool // whether the selectivity is expected to have been applied
	}{
		{name: "no filter", tbl: mkTable(1000, mcv), pred: nil, want: 1000},
		{name: "no scan", tbl: mkTable(1000, mcv), pred: eq("x"), noScan: true, want: 1000},
		{name: "no base rows", tbl: mkTable(0, nil), pred: eq("x"), want: 0},
		{name: "unreliable selectivity", tbl: mkTable(1000, nil), pred: eq("x"), want: 1000},
		{name: "reliable selectivity", tbl: mkTable(1000, mcv), pred: eq("x"), want: 250, wantSel: true},
		{name: "one-row floor", tbl: mkTable(2, mcv), pred: eq("x"), want: 1, wantSel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := rangeBinding{table: tc.tbl, offset: 0}
			var scan Node
			if !tc.noScan {
				scan = &SeqScan{Table: tc.tbl, schema: tableSchema(tc.tbl)}
			}
			info := estimateBaseRelInfo(binding, scan, tc.pred)
			if info.filteredRows != tc.want {
				t.Errorf("estimateBaseRelInfo filteredRows = %d; want %d", info.filteredRows, tc.want)
			}
			direct := applyLocalFilterSelectivity(info.baseRows, binding, scan, tc.pred)
			if direct != info.filteredRows {
				t.Errorf("applyLocalFilterSelectivity = %d but estimateBaseRelInfo = %d — the twins drifted", direct, info.filteredRows)
			}
			if got := direct != info.baseRows; got != tc.wantSel {
				t.Errorf("selectivity applied = %v; want %v (base %d, filtered %d)", got, tc.wantSel, info.baseRows, direct)
			}
		})
	}
}
