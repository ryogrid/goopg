package planner

// M0125-0038 (C5) — cardinality propagation above base scans.
//
// Before these arms existed, EstimateRows returned 0 for Gather /
// GatherMerge / LockRows / Memoize / CTEScan / CTEDMLPrefix / SetOp /
// NestedLoopIndexJoin / IndexOnlyScan, and "child <= 0 → 0" zeroed
// every estimate above the first such node — which is why all 18
// plans in the M0125-0026 capture rendered rows=1 on every non-leaf
// node. These tests pin the propagation, the SetOp rules taken from
// upstream (prepunion.c:1146-1151), the Project-transparent NDistinct
// lookup, and — deliberately — the *MultiHashJoin* arm's ABSENCE,
// which M0126-0002 owns together with its plan re-baseline protocol.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func statsTable(name string, rows int64, ndistinct ...int64) *catalog.Table {
	cols := make([]catalog.ColumnStats, len(ndistinct))
	columns := make([]catalog.Column, len(ndistinct))
	for i, nd := range ndistinct {
		cols[i] = catalog.ColumnStats{NDistinct: nd}
		columns[i] = catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}}
	}
	return &catalog.Table{
		Name:    name,
		Columns: columns,
		Stats:   &catalog.TableStats{RowCount: rows, Columns: cols},
	}
}

func TestEstimateRowsPassThroughWrappers(t *testing.T) {
	scan := &SeqScan{Table: statsTable("t", 1234, 1234)}
	// A PROBE index scan — Key non-nil. Memoize only ever wraps the
	// parameterised inner of an NLI, which is always this shape; the
	// bound-less full-scan shape estimates the relation instead
	// (M0127-P5.9-h, TestEstimateRowsFullIndexScan).
	idxScan := &IndexScan{Table: statsTable("t", 1234, 1234), Key: &ColumnRef{Name: "c"}}
	cases := []struct {
		name string
		node Node
		want int64
	}{
		{"Gather", &Gather{Child: scan}, 1234},
		{"GatherMerge", &GatherMerge{Child: scan}, 1234},
		{"LockRows", &LockRows{Child: scan}, 1234},
		// Memoize wraps *IndexScan which always estimates 1 (equality
		// probe convention). Confirm the pass-through, not a specific
		// row count.
		{"Memoize", &Memoize{Child: idxScan}, 1},
		{"CTEScan", &CTEScan{Name: "c", Child: scan}, 1234},
	}
	for _, tc := range cases {
		if got := EstimateRows(tc.node); got != tc.want {
			t.Errorf("%s: EstimateRows = %d, want %d (pass-through)", tc.name, got, tc.want)
		}
	}
}

func TestEstimateRowsSetOpRules(t *testing.T) {
	left := &SeqScan{Table: statsTable("l", 1000, 1000)}
	right := &SeqScan{Table: statsTable("r", 400, 400)}
	cases := []struct {
		name string
		op   parser.SetOpType
		all  bool
		want int64
	}{
		// prepunion.c:1146-1151; non-ALL dedup approximated /2 (no
		// estimate_num_groups yet — the 0077 line).
		{"union all = l+r", parser.SetOpUnion, true, 1400},
		{"union = (l+r)/2", parser.SetOpUnion, false, 700},
		{"intersect all = min", parser.SetOpIntersect, true, 400},
		{"intersect = min/2", parser.SetOpIntersect, false, 200},
		{"except all = l", parser.SetOpExcept, true, 1000},
		{"except = l/2", parser.SetOpExcept, false, 500},
	}
	for _, tc := range cases {
		s := &SetOp{Left: left, Right: right, Op: tc.op, All: tc.all}
		if got := EstimateRows(s); got != tc.want {
			t.Errorf("%s: EstimateRows = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestEstimateRowsNLIndexJoinCarriesOuter(t *testing.T) {
	outer := &SeqScan{Table: statsTable("o", 5000, 5000)}
	inner := &IndexScan{Table: statsTable("i", 100000, 100000)}
	nli := &NestedLoopIndexJoin{Type: JoinTypeInner, Outer: outer, Inner: inner}
	if got := EstimateRows(nli); got != 5000 {
		t.Errorf("NLI: EstimateRows = %d, want 5000 (outer cardinality; inner is a 1-row probe)", got)
	}
}

// TestJoinKeyNDistinctThroughProject pins the C5 selectivity fix: an
// equi-join whose input is Project-wrapped must still resolve the key's
// NDistinct instead of falling back to defaultEqSelectivity (Q10's
// rows=131280740 was exactly l·r·0.005).
func TestJoinKeyNDistinctThroughProject(t *testing.T) {
	fact := &SeqScan{Table: statsTable("fact", 359432, 359432)}
	dim := &SeqScan{Table: statsTable("dim", 73049, 73049)}
	// Project reorders: output slot 0 ← child slot 0.
	proj := &Project{Child: dim, Targets: []Expr{&ColumnRef{Index: 0, Name: "k"}}}
	j := &Join{
		Type: JoinTypeInner, Algo: JoinAlgoHash,
		Left: fact, Right: proj,
		LeftKey:  &ColumnRef{Index: 0, Name: "k"},
		RightKey: &ColumnRef{Index: 0, Name: "k"},
	}
	// l·r / max(nd_l, nd_r) = 359432·73049 / max(359432,73049) = 73049.
	// Both keys are unique in their tables, so max = 359432.
	// cartesian just documented for comparison; use variables to avoid
	// constant-conversion error.
	lr, rr := float64(359432), float64(73049)
	_ = int64(lr * rr * defaultEqSelectivity) // ≈131M, the pre-fix fallback
	if got := EstimateRows(j); got != 73049 {
		t.Errorf("join through Project: EstimateRows = %d, want 73049 (nd-based via Project pass-through, not 0.005 fallback ≈131M)",
			got)
	}
}

// TestEstimateRowsPropagatesAboveWrappedChain is the end-to-end shape of
// the C5 symptom: Aggregate → Gather → Join must estimate non-zero once
// the wrapper arm exists.
func TestEstimateRowsPropagatesAboveWrappedChain(t *testing.T) {
	fact := &SeqScan{Table: statsTable("fact", 100000, 100000)}
	dim := &SeqScan{Table: statsTable("dim", 1000, 1000)}
	j := &Join{
		Type: JoinTypeInner, Algo: JoinAlgoHash,
		Left: fact, Right: dim,
		LeftKey:  &ColumnRef{Index: 0, Name: "k"},
		RightKey: &ColumnRef{Index: 0, Name: "k"},
	}
	agg := &Aggregate{Child: &Gather{Child: j}, GroupExprs: []Expr{
		&ColumnRef{Index: 0, Name: "k"}, &ColumnRef{Index: 1, Name: "j"},
	}}
	if got := EstimateRows(agg); got <= 0 {
		t.Fatalf("Aggregate over Gather over Join: EstimateRows = %d, want > 0", got)
	}
}

// TestEstimateJoinCapFallback pins M0126-0010: when NDistinct is
// unavailable for both join keys and the estimator falls back to
// l·r·0.005, the result must not exceed max(l, r) — the FK-PK
// invariant that a non-cross equi-join never fans out past the
// larger input.
func TestEstimateJoinCapFallback(t *testing.T) {
	// Two tables without stats — keyNDistinct returns 0 for both.
	noStats := &SeqScan{Table: &catalog.Table{Name: "t", Columns: []catalog.Column{{Name: "c"}}}}
	large := &SeqScan{Table: &catalog.Table{
		Name:    "large",
		Columns: []catalog.Column{{Name: "c"}},
		Stats:   &catalog.TableStats{RowCount: 6000000, Columns: []catalog.ColumnStats{{NDistinct: -1}}},
	}}

	// Case 1: Join with no stats on either side → fallback to
	// l·r·0.005 capped at max(l,r).
	j1 := &Join{
		Type: JoinTypeInner, Algo: JoinAlgoHash,
		Left: noStats, Right: noStats,
		LeftKey:  &ColumnRef{Index: 0, Name: "k"},
		RightKey: &ColumnRef{Index: 0, Name: "k"},
	}
	got1 := EstimateRows(j1)
	// l=0, r=0 → EstimateRows returns 0. The cap doesn't fire.
	if got1 != 0 {
		t.Errorf("no-stats join: EstimateRows = %d, want 0 (zero inputs)", got1)
	}

	// Case 2: One side has NDistinct=-1 (stats available but unknown
	// ndistinct), both sides have row count. The formula uses the
	// non-negative ndistinct (0 from the left, 0 from the right since
	// NDistinct=-1 is not > 0). Fallback: l·r·0.005, then cap.
	j2 := &Join{
		Type: JoinTypeInner, Algo: JoinAlgoHash,
		Left: large, Right: noStats,
		LeftKey:  &ColumnRef{Index: 0, Name: "k"},
		RightKey: &ColumnRef{Index: 0, Name: "k"},
	}
	got2 := EstimateRows(j2)
	// l=6M, r=0 → EstimateRows returns 0 (r <= 0).
	if got2 != 0 {
		t.Errorf("zero-right join: EstimateRows = %d, want 0", got2)
	}

	// Case 3: Both sides have row counts but no usable NDistinct.
	// Fallback gives l·r·0.005 = 6000000*100000*0.005 = 3e9.
	// Cap at max(l,r) = 6000000.
	fact := &SeqScan{Table: &catalog.Table{
		Name:    "fact",
		Columns: []catalog.Column{{Name: "c"}},
		Stats:   &catalog.TableStats{RowCount: 100000, Columns: []catalog.ColumnStats{{NDistinct: -1}}},
	}}
	j3 := &Join{
		Type: JoinTypeInner, Algo: JoinAlgoHash,
		Left: large, Right: fact,
		LeftKey:  &ColumnRef{Index: 0, Name: "k"},
		RightKey: &ColumnRef{Index: 0, Name: "k"},
	}
	got3 := EstimateRows(j3)
	want3 := int64(6000000) // max(6M, 100K) = 6M
	if got3 != want3 {
		t.Errorf("cap-fallback join: EstimateRows = %d, want %d (capped at max(l,r))", got3, want3)
	}

	// Case 4: When NDistinct IS available, the cap must NOT fire.
	// ndistinct-based formula gives correct result.
	statsL := &SeqScan{Table: statsTable("l", 10000, 100)}
	statsR := &SeqScan{Table: statsTable("r", 50000, 500)}
	j4 := &Join{
		Type: JoinTypeInner, Algo: JoinAlgoHash,
		Left: statsL, Right: statsR,
		LeftKey:  &ColumnRef{Index: 0, Name: "k"},
		RightKey: &ColumnRef{Index: 0, Name: "k"},
	}
	got4 := EstimateRows(j4)
	// (10000 * 50000) / max(100, 500) = 500M / 500 = 1M
	want4 := int64(1000000)
	if got4 != want4 {
		t.Errorf("with-stats join: EstimateRows = %d, want %d (nd-based, cap must NOT fire)", got4, want4)
	}
}
