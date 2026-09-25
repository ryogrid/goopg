package optimizer

// TestAggInputWidthUsesInputTargetWhenKnown pins M0141-S2a-fix1: `aggInputWidth`
// must compute (ncols, avgVarBytes) over agg.InputTarget's KEPT columns when
// agg.InputTargetKnown, not the full child row — this is the B2 absorption
// substituting PG's subpath->pathtarget->width / outerplan->plan_width
// currency (see docs/design/0100-0149/m0141-s2a-fix-scoping-recon.md).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// awChild builds a 4-column child: "g" (int4, group key, kept), "v" (text,
// agg arg, kept) and two unreferenced variable-width columns that a real
// narrowing pass would drop. Two variable-width KEPT/DROPPED columns (rather
// than one) means a test that accidentally kept everything or dropped
// everything both produce a visibly wrong avgVarBytes, not a coincidentally
// matching one.
func awChild() *noNode {
	return &noNode{sch: Schema{
		{Name: "g", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"}},
		{Name: "junk1", Type: catalog.Type{Name: "text"}},
		{Name: "junk2", Type: catalog.Type{Name: "text"}},
	}}
}

func awAgg(child Node) *Aggregate {
	return &Aggregate{
		Child:      child,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "g"}},
		Aggs:       []AggregateCall{{Name: "sum", Arg: &ColumnRef{Index: 1, Name: "v"}}},
		Mode:       AggModeSimple,
	}
}

// TestAggInputWidthFallsBackWhenTargetUnknown pins the "no narrowing
// derivable" side of the branch: InputTargetKnown == false (the zero value —
// no stamp run) reads the identical (ncols, avgVarBytes) aggInputWidth always
// computed before this task, over the full un-narrowed child row.
func TestAggInputWidthFallsBackWhenTargetUnknown(t *testing.T) {
	child := awChild()
	agg := awAgg(child)
	if agg.InputTargetKnown {
		t.Fatalf("test setup: expected a fresh Aggregate to have InputTargetKnown == false")
	}

	gotNcols, gotAvgVar := aggInputWidth(child, agg)
	wantNcols, wantAvgVar := aggInputWidth(child, nil)
	if gotNcols != wantNcols || gotAvgVar != wantAvgVar {
		t.Fatalf("unknown target: aggInputWidth(child, agg) = (%d, %v), want the nil-agg fallback (%d, %v)",
			gotNcols, gotAvgVar, wantNcols, wantAvgVar)
	}
	if gotNcols != 4 {
		t.Fatalf("unknown target: ncols = %d, want 4 (full row)", gotNcols)
	}
	// v, junk1, junk2 are all variable-width: 3 * varlenaDefaultWidth.
	if want := 3 * float64(varlenaDefaultWidth); gotAvgVar != want {
		t.Fatalf("unknown target: avgVarBytes = %v, want %v (3 variable-width columns)", gotAvgVar, want)
	}
}

// TestAggInputWidthNarrowsWhenTargetKnown pins the M0141-S2a-fix1 branch
// itself: once stampAggregateInputTarget has run, aggInputWidth must read
// (g, v) only — dropping junk1/junk2 — even though narrowAggregateInput
// (the REAL, executor-side commit) has not run at all. This is the
// separation the design doc's "Correctness note" requires: the cost-time
// preview and the executor's actually-committed-or-declined narrowing are
// deliberately two different mechanisms, pinned here as two different call
// results for the identical (child, agg) pair.
func TestAggInputWidthNarrowsWhenTargetKnown(t *testing.T) {
	child := awChild()
	agg := awAgg(child)
	stampAggregateInputTarget(agg, nil)
	if !agg.InputTargetKnown {
		t.Fatalf("test setup: expected stampAggregateInputTarget to derive a known target for this agg (no Filter/Passthrough)")
	}
	if got := len(agg.InputTarget); got != 2 {
		t.Fatalf("test setup: InputTarget = %v (len %d), want 2 kept columns (g, v)", agg.InputTarget, got)
	}

	gotNcols, gotAvgVar := aggInputWidth(child, agg)
	if gotNcols != 2 {
		t.Fatalf("known target: ncols = %d, want 2 (g, v only — junk1/junk2 dropped)", gotNcols)
	}
	// Only "v" survives narrowing and is variable-width: 1 * varlenaDefaultWidth.
	if want := float64(varlenaDefaultWidth); gotAvgVar != want {
		t.Fatalf("known target: avgVarBytes = %v, want %v (one surviving variable-width column)", gotAvgVar, want)
	}

	// The full-row computation must differ from the narrowed one for this
	// fixture — otherwise the assertions above would pass by coincidence
	// even if the InputTarget branch were dead code.
	fullNcols, fullAvgVar := aggInputWidth(child, nil)
	if fullNcols == gotNcols && fullAvgVar == gotAvgVar {
		t.Fatalf("fixture is not discriminating: narrowed result (%d, %v) equals the full-row result (%d, %v)",
			gotNcols, gotAvgVar, fullNcols, fullAvgVar)
	}
}

// TestSortSeedNarrowsWithTheAggregate is M0144-0003c: the Sort beneath a
// grouping candidate must be priced on the SAME narrowed row `costAgg` is
// charged on, not on the input rel's full row.
//
// PG never splits the two — `make_group_input_target` narrows once
// (postgres/src/backend/optimizer/plan/planner.c:1676-1744),
// `set_pathtarget_cost_width` finalises the width (costsize.c:6367), and
// `cost_sort` reads that same `pathtarget->width` (costsize.c:2328). goopg
// priced the aggregate through `aggInputWidth` and the Sort through the input
// rel, so one input had two widths.
//
// The test drives `sortPathForBounded` directly with the full-width seed and
// with the narrowed copy `addGroupingPaths` now builds, and asserts the
// narrowed one is STRICTLY CHEAPER. Asserting the direction rather than an
// exact figure keeps it a pin on the wiring, not on `costSortRunWithWidth`'s
// arithmetic — which has its own tests and may legitimately be re-tuned.
func TestSortSeedNarrowsWithTheAggregate(t *testing.T) {
	cp := defaultCostParams()
	child := awChild()
	agg := awAgg(child)
	// The keep-set stampAggregateInputTarget would derive: group key + agg
	// arg, dropping the two unreferenced text columns.
	agg.InputTarget, agg.InputTargetKnown = []int{0, 1}, true

	inNcols, inAvgVar := aggInputWidth(child, agg)
	if inNcols != 2 {
		t.Fatalf("fixture: aggInputWidth kept %d cols, want 2", inNcols)
	}

	// The width term only reaches the PRICE through the spill branch
	// (`nruns := inputBytes / work_mem`, cost_funcs.go) — an in-memory sort
	// costs the same at any width, correctly. So the fixture must be large
	// enough that the FULL row spills, which is exactly the case the
	// narrowing is supposed to change.
	const rows = 5_000_000
	rel := newRelOptInfo(relsetOf(0), rows, 64)
	rel.NCols = len(child.Output())
	rel.AvgVarBytes = nodeAvgVarBytes(child.Output())
	seed := &Path{Kind: PathPrebuilt, Rel: rel, Rows: rows, Cost: Cost{Total: 1000}}

	narrowed := *seed
	narrowed.NCols, narrowed.AvgVarBytes = inNcols, inAvgVar

	keys := []PathKey{{Expr: &ColumnRef{Index: 0, Name: "g"}, SortAsc: true}}
	full := sortPathForBounded(seed, keys, cp, -1)
	narrow := sortPathForBounded(&narrowed, keys, cp, -1)

	if pathNCols(seed) != len(child.Output()) {
		t.Fatalf("fixture: full seed reads %d cols, want the un-narrowed %d",
			pathNCols(seed), len(child.Output()))
	}
	if pathNCols(&narrowed) != inNcols {
		t.Fatalf("narrowed seed reads %d cols, want the aggregate's %d",
			pathNCols(&narrowed), inNcols)
	}
	if !(narrow.Cost.Total < full.Cost.Total) {
		t.Fatalf("narrowed sort priced %v, not below the full-width %v — the "+
			"per-path NCols override is not reaching sortPathForBounded",
			narrow.Cost.Total, full.Cost.Total)
	}
}

// TestSortSeedKeepsFullWidthWhenNoNarrowingIsDerivable: with no keep-set
// stamped, `addGroupingPaths` must hand the seed through unchanged. The
// override is an absorption of a derived quantity, not a licence to invent a
// narrower row when none was derived.
func TestSortSeedKeepsFullWidthWhenNoNarrowingIsDerivable(t *testing.T) {
	child := awChild()
	agg := awAgg(child) // InputTargetKnown stays false
	inNcols, _ := aggInputWidth(child, agg)
	if inNcols != len(child.Output()) {
		t.Fatalf("with no keep-set derived, aggInputWidth must read the full row: got %d want %d",
			inNcols, len(child.Output()))
	}
}
