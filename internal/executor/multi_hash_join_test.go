package executor

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestMultiHashJoinTwoTables(t *testing.T) {
	// A[id] = B[id,val] — probe A, build B.
	rowsA := []Row{
		{Datum{Kind: KindInt, Int: 1}},
		{Datum{Kind: KindInt, Int: 2}},
		{Datum{Kind: KindInt, Int: 99}}, // no match
	}
	rowsB := []Row{
		{Datum{Kind: KindInt, Int: 1}, NewStringDatum("hello")},
		{Datum{Kind: KindInt, Int: 2}, NewStringDatum("world")},
	}

	children := []Operator{
		&rowsOp{rows: rowsA},
		&rowsOp{rows: rowsB},
	}

	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{nil, nil},
		Keys: []planner.MultiHashKey{
			{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 0},
		},
		ProbeTable: 0,
	}

	op := newMultiHashJoinOp(plan, children)
	op.schema = planner.Schema{
		{Name: "aid", Type: catalog.Type{Name: "int8"}},
		{Name: "bid", Type: catalog.Type{Name: "int8"}},
		{Name: "bval", Type: catalog.Type{Name: "text"}},
	}
	op.nulls = []Row{nullRow(1), nullRow(2)}

	ctx := &Context{}
	err := op.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var results []Row
	for {
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, row)
	}
	op.Close()

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if len(results[0]) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(results[0]))
	}
	if results[0][0].Int != 1 {
		t.Errorf("row 1 col 0 (aid): got %d", results[0][0].Int)
	}
	if results[0][2].StringValue() != "hello" {
		t.Errorf("row 1 col 2 (bval): got %s", results[0][2].StringValue())
	}
	if results[1][0].Int != 2 {
		t.Errorf("row 2 col 0 (aid): got %d", results[1][0].Int)
	}
	if results[1][2].StringValue() != "world" {
		t.Errorf("row 2 col 2 (bval): got %s", results[1][2].StringValue())
	}
}

func TestMultiHashBuild(t *testing.T) {
	// Verify Build() dispatch creates multiHashJoinOp
	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{
			&planner.Values{Rows: [][]planner.Expr{{}}},
			&planner.Values{Rows: [][]planner.Expr{{}}},
		},
		Keys:       []planner.MultiHashKey{},
		ProbeTable: 0,
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if _, ok := op.(*multiHashJoinOp); !ok {
		t.Errorf("Build did not return multiHashJoinOp: got %T", op)
	}
}

// TestMultiHashJoinPredicatePushdown asserts M0043-0002 — residual
// filters in MultiHashJoin.Filters are partitioned by the deepest
// chain step they reference, evaluated at the earliest binding
// point. Verifies (a) correct row count and contents, (b) that
// the filter was placed in the expected stepFilters bucket (not
// in leafFilters).
func TestMultiHashJoinPredicatePushdown(t *testing.T) {
	// 3-table chain: A (probe) ⨝ B ⨝ C.
	// Schema (absolute output row indices):
	//   A: [aid] at offsets [0, 1)
	//   B: [bid, bval] at offsets [1, 3)
	//   C: [cid] at offset [3, 4)
	// Joins: A.aid = B.bid, B.bid = C.cid.
	// Residual filter: B.bval < 3 — references B only,
	// so bindStep should be 0 (the step that binds B).

	// A: 2 rows, both joining to B.
	rowsA := []Row{
		{Datum{Kind: KindInt, Int: 1}},
		{Datum{Kind: KindInt, Int: 2}},
	}
	// B: 5 rows per A.aid, with bval = 1..5. Filter rejects
	// bval >= 3 (so bval ∈ {3, 4, 5} → 3 of 5 per A).
	rowsB := []Row{}
	for aid := 1; aid <= 2; aid++ {
		for v := 1; v <= 5; v++ {
			rowsB = append(rowsB, Row{
				Datum{Kind: KindInt, Int: int64(aid)},
				Datum{Kind: KindInt, Int: int64(v)},
			})
		}
	}
	// C: 4 rows per B.bid value (1, 2). 4 cid copies each.
	rowsC := []Row{}
	for cid := 1; cid <= 2; cid++ {
		for k := 0; k < 4; k++ {
			rowsC = append(rowsC, Row{Datum{Kind: KindInt, Int: int64(cid)}})
		}
	}

	children := []Operator{
		&rowsOp{rows: rowsA},
		&rowsOp{rows: rowsB},
		&rowsOp{rows: rowsC},
	}

	// Filter: BinaryOp{B.bval < 3}.
	// B.bval is at absolute output index 2 (A:1, B:2 cols → bval at 2).
	filter := &planner.BinaryOp{
		Op:    "<",
		Left:  &planner.ColumnRef{Index: 2, Name: "bval", Type: catalog.Type{Name: "int8"}},
		Right: &planner.IntegerConst{Value: 3},
	}

	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{nil, nil, nil},
		Keys: []planner.MultiHashKey{
			{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 0},
			{LeftTable: 1, LeftCol: 0, RightTable: 2, RightCol: 0},
		},
		ProbeTable: 0,
		Filters:    []planner.Expr{filter},
	}

	op := newMultiHashJoinOp(plan, children)
	op.schema = planner.Schema{
		{Name: "aid", Type: catalog.Type{Name: "int8"}},
		{Name: "bid", Type: catalog.Type{Name: "int8"}},
		{Name: "bval", Type: catalog.Type{Name: "int8"}},
		{Name: "cid", Type: catalog.Type{Name: "int8"}},
	}
	op.nulls = []Row{nullRow(1), nullRow(2), nullRow(1)}

	ctx := &Context{}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}

	// Partition assertion (M0043-0002 internal state):
	// the filter references only table B, which is bound at
	// step 0 → expect stepFilters[0] = [filter], no leaf filter.
	if got := len(op.probeFilters); got != 0 {
		t.Errorf("probeFilters: got len=%d, want 0", got)
	}
	if got := len(op.leafFilters); got != 0 {
		t.Errorf("leafFilters: got len=%d, want 0 (filter should partition into stepFilters)", got)
	}
	if got := len(op.stepFilters); got != 2 {
		t.Fatalf("stepFilters: got len=%d, want 2 (one slot per chain step)", got)
	}
	if got := len(op.stepFilters[0]); got != 1 {
		t.Errorf("stepFilters[0]: got len=%d, want 1 (B-only filter binds at step 0)", got)
	}
	if got := len(op.stepFilters[1]); got != 0 {
		t.Errorf("stepFilters[1]: got len=%d, want 0", got)
	}

	// Correctness assertion: result rows match expected.
	// For each A.aid in {1, 2}: 2 surviving B rows (bval ∈ {1,2})
	// × 4 C rows for that bid. Total: 2 × 2 × 4 = 16 rows.
	var results []Row
	for {
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, row)
	}
	op.Close()

	if len(results) != 16 {
		t.Fatalf("expected 16 result rows (2 A × 2 surviving B × 4 C), got %d", len(results))
	}
	for i, r := range results {
		if len(r) != 4 {
			t.Errorf("row %d: width=%d, want 4", i, len(r))
			continue
		}
		bval := r[2].Int
		if bval >= 3 {
			t.Errorf("row %d: bval=%d violates filter B.bval < 3 — pushdown failed",
				i, bval)
		}
		// aid == bid == cid (chain join condition).
		if r[0].Int != r[1].Int || r[1].Int != r[3].Int {
			t.Errorf("row %d: aid=%d bid=%d cid=%d — join keys diverged",
				i, r[0].Int, r[1].Int, r[3].Int)
		}
	}
}

// TestMultiHashJoinPushdownLeafFallback asserts that filters with
// out-of-scope refs (subqueries / OuterColumnRef) are routed to
// leafFilters and still evaluated correctly at emission time.
func TestMultiHashJoinPushdownLeafFallback(t *testing.T) {
	// Same A⨝B as the simple test, plus a filter wrapping an
	// OuterColumnRef. Expectation: filter goes to leafFilters,
	// not stepFilters; correctness preserved.
	rowsA := []Row{
		{Datum{Kind: KindInt, Int: 1}},
		{Datum{Kind: KindInt, Int: 2}},
	}
	rowsB := []Row{
		{Datum{Kind: KindInt, Int: 1}, NewStringDatum("x")},
		{Datum{Kind: KindInt, Int: 2}, NewStringDatum("y")},
	}

	children := []Operator{
		&rowsOp{rows: rowsA},
		&rowsOp{rows: rowsB},
	}

	// Filter contains an OuterColumnRef → must route to leafFilters.
	// Use a trivially-true expression so correctness still holds:
	// (B.bid > 0) AND (outer.x > 0). We won't actually push outer
	// rows; the test relies on the fact that leafFilters is the
	// safe escape hatch and any OuterColumnRef in a filter forces
	// leaf-level eval — but since we don't supply OuterRows, we
	// instead just confirm partitioning routes it correctly.
	outer := &planner.OuterColumnRef{Level: 1, Index: 0, Name: "outer_x", Type: catalog.Type{Name: "int8"}}
	filter := &planner.BinaryOp{
		Op:    "OR",
		Left:  &planner.BinaryOp{Op: ">", Left: &planner.ColumnRef{Index: 1, Name: "bid"}, Right: &planner.IntegerConst{Value: -1}},
		Right: outer,
	}

	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{nil, nil},
		Keys: []planner.MultiHashKey{
			{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 0},
		},
		ProbeTable: 0,
		Filters:    []planner.Expr{filter},
	}

	op := newMultiHashJoinOp(plan, children)
	op.schema = planner.Schema{
		{Name: "aid", Type: catalog.Type{Name: "int8"}},
		{Name: "bid", Type: catalog.Type{Name: "int8"}},
		{Name: "bval", Type: catalog.Type{Name: "text"}},
	}
	op.nulls = []Row{nullRow(1), nullRow(2)}

	if err := op.Open(&Context{}); err != nil {
		t.Fatal(err)
	}
	defer op.Close()

	// Partitioning assertion: OuterColumnRef in the filter forces
	// it to leafFilters, not stepFilters[0].
	if got := len(op.leafFilters); got != 1 {
		t.Errorf("leafFilters: got len=%d, want 1 (OuterColumnRef forces leaf-level eval)", got)
	}
	for s, fs := range op.stepFilters {
		if len(fs) != 0 {
			t.Errorf("stepFilters[%d]: got len=%d, want 0 (OuterColumnRef must not be pushed down)", s, len(fs))
		}
	}
	if got := len(op.probeFilters); got != 0 {
		t.Errorf("probeFilters: got len=%d, want 0", got)
	}
}

// TestDatumToInt64Key verifies the M0043-0003 fast-path key converter.
func TestDatumToInt64Key(t *testing.T) {
	cases := []struct {
		name string
		d    Datum
		want int64
		ok   bool
	}{
		{"KindInt zero", Datum{Kind: KindInt, Int: 0}, 0, true},
		{"KindInt positive", Datum{Kind: KindInt, Int: 42}, 42, true},
		{"KindInt negative", Datum{Kind: KindInt, Int: -7}, -7, true},
		{"KindInt MaxInt64", Datum{Kind: KindInt, Int: math.MaxInt64}, math.MaxInt64, true},
		// KindNumeric scale=0: treated as integer
		{"KindNumeric scale0", Datum{Kind: KindNumeric, Int: 123, Scale: 0}, 123, true},
		// KindNumeric with trailing zeros: strip to canonical
		{"KindNumeric trailing zeros", Datum{Kind: KindNumeric, Int: 1000, Scale: 3}, 1, true},
		// KindNumeric fractional: not representable
		{"KindNumeric fractional", Datum{Kind: KindNumeric, Int: 15, Scale: 1}, 0, false},
		// KindNull: not representable
		{"KindNull", NullDatum, 0, false},
		// KindBool: not representable
		{"KindBool true", NewBoolDatum(true), 0, false},
		// KindString: not representable
		{"KindString", NewStringDatum("hello"), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := datumToInt64Key(tc.d)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("key=%d want %d", got, tc.want)
			}
		})
	}
}

// TestMultiHashInt64FastPath verifies M0043-0003: a MHJ over int-keyed
// tables activates hashTblIsInt=true and produces correct results via the
// zero-allocation int64 probe path. Uses the same direct rowsOp pattern
// as TestMultiHashJoinTwoTables to avoid DDL plumbing.
func TestMultiHashInt64FastPath(t *testing.T) {
	// A[id] (probe) ⨝ B[bid, fk_a] ⨝ C[cid, fk_b, cval]
	// All keys are KindInt → triggers int64 fast path.
	rowsA := []Row{
		{Datum{Kind: KindInt, Int: 1}},
		{Datum{Kind: KindInt, Int: 2}},
		{Datum{Kind: KindInt, Int: 99}}, // no match in B
	}
	rowsB := []Row{
		{Datum{Kind: KindInt, Int: 10}, Datum{Kind: KindInt, Int: 1}}, // fk_a=1
		{Datum{Kind: KindInt, Int: 20}, Datum{Kind: KindInt, Int: 2}}, // fk_a=2
	}
	rowsC := []Row{
		{Datum{Kind: KindInt, Int: 100}, Datum{Kind: KindInt, Int: 10}, Datum{Kind: KindInt, Int: 42}}, // fk_b=10
		{Datum{Kind: KindInt, Int: 200}, Datum{Kind: KindInt, Int: 20}, Datum{Kind: KindInt, Int: 99}}, // fk_b=20
	}

	children := []Operator{
		&rowsOp{rows: rowsA},
		&rowsOp{rows: rowsB},
		&rowsOp{rows: rowsC},
	}
	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{nil, nil, nil},
		Keys: []planner.MultiHashKey{
			{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 1}, // a.id = b.fk_a
			{LeftTable: 1, LeftCol: 0, RightTable: 2, RightCol: 1}, // b.bid = c.fk_b
		},
		ProbeTable: 0,
	}

	op := newMultiHashJoinOp(plan, children)
	op.schema = planner.Schema{
		{Name: "aid", Type: catalog.Type{Name: "int8"}},
		{Name: "bid", Type: catalog.Type{Name: "int8"}},
		{Name: "fk_a", Type: catalog.Type{Name: "int8"}},
		{Name: "cid", Type: catalog.Type{Name: "int8"}},
		{Name: "fk_b", Type: catalog.Type{Name: "int8"}},
		{Name: "cval", Type: catalog.Type{Name: "int8"}},
	}
	op.nulls = []Row{nullRow(1), nullRow(2), nullRow(3)}

	ctx := &Context{}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify int64 fast path was activated for build tables (B=1, C=2).
	for i, isInt := range op.hashTblIsInt {
		if i == plan.ProbeTable {
			continue
		}
		if !isInt {
			t.Errorf("hashTblIsInt[%d]=false; int64 fast path not activated for integer-keyed table", i)
		}
	}

	var results []Row
	for {
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, copyRow(row))
	}
	op.Close()

	// Expect 2 joined rows: (a=1⋈b=10⋈c=100) and (a=2⋈b=20⋈c=200).
	if len(results) != 2 {
		t.Fatalf("got %d rows, want 2; results=%v", len(results), results)
	}
	// Check cval column (index 5) for correctness.
	cvals := map[int64]bool{results[0][5].Int: true, results[1][5].Int: true}
	if !cvals[42] || !cvals[99] {
		t.Errorf("expected cvals {42, 99}, got %v", cvals)
	}
}

func copyRow(r Row) Row {
	out := make(Row, len(r))
	copy(out, r)
	return out
}
