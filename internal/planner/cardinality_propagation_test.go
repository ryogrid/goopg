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
	idxScan := &IndexScan{Table: statsTable("t", 1234, 1234)}
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

// TestEstimateRowsMultiHashJoinChain mirrors the *Join arm's method:
// probe rows × per-dim selectivity along the key chain.
// M0126-0002 adds this arm (it was deliberately absent in M0125-0038
// because ancestors read the 0 to decide BuildLeft/algorithm).
func TestEstimateRowsMultiHashJoinChain(t *testing.T) {
	// 2-table chain: probe a (100 rows, nd=100) joined to b (200 rows,
	// nd=200) on a.col0 = b.col0. Binary formula: 100·200/max(100,200)=100.
	a := &SeqScan{Table: statsTable("a", 100, 100)}
	b := &SeqScan{Table: statsTable("b", 200, 200)}
	mhj := &MultiHashJoin{
		Tables:     []Node{a, b},
		Keys:       []MultiHashKey{{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 0}},
		ProbeTable: 0,
	}
	if got := EstimateRows(mhj); got != 100 {
		t.Fatalf("2-table MHJ: EstimateRows = %d, want 100 (l·r/max(nd)=100·200/200)", got)
	}

	// 3-table chain: probe a → b → c.
	c := &SeqScan{Table: statsTable("c", 50, 50)}
	mhj3 := &MultiHashJoin{
		Tables: []Node{a, b, c},
		Keys: []MultiHashKey{
			{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 0},
			{LeftTable: 1, LeftCol: 0, RightTable: 2, RightCol: 0},
		},
		ProbeTable: 0,
	}
	// Step 1: 100·200/200 = 100. Step 2: 100·50/max(200,50)=100·50/200=25.
	if got := EstimateRows(mhj3); got != 25 {
		t.Fatalf("3-table MHJ: EstimateRows = %d, want 25 (chain 100→100→25)", got)
	}

	// Also test that the arm produces > 0 for the simple no-key edge case
	// (an MHJ with a single table).
	single := &MultiHashJoin{Tables: []Node{a}, ProbeTable: 0}
	if got := EstimateRows(single); got <= 0 {
		t.Fatalf("single-table MHJ: EstimateRows = %d, want > 0", got)
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
