package executor

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestEvalBinaryBatchEquivalencePerRowEq pins that
// evalBinaryBatch's OpEq result matches per-row evalBinary
// for the same inputs. (M0074-0001.)
func TestEvalBinaryBatchEquivalencePerRowEq(t *testing.T) {
	rng := rand.New(rand.NewSource(0xa1b2c3d4))
	const n = 200
	left := make([]Datum, n)
	right := make([]Datum, n)
	for i := range left {
		am := rng.Int63n(100) - 50
		bm := rng.Int63n(100) - 50
		left[i] = Datum{Kind: KindInt, Int: am}
		right[i] = Datum{Kind: KindInt, Int: bm}
	}
	// Per-row baseline.
	expected := make([]Datum, n)
	for i := 0; i < n; i++ {
		v, err := evalBinary(parser.OpEq, left[i], right[i], 0, nil)
		if err != nil {
			t.Fatalf("per-row[%d]: %v", i, err)
		}
		expected[i] = v
	}
	// Batch path.
	out := make([]Datum, n)
	if err := evalBinaryBatch(parser.OpEq, left, right, out); err != nil {
		t.Fatalf("batch: %v", err)
	}
	for i := 0; i < n; i++ {
		if expected[i].Kind != out[i].Kind || expected[i].Int != out[i].Int {
			t.Errorf("Eq[%d]: per-row=%v batch=%v", i, expected[i], out[i])
		}
	}
}

// TestEvalBinaryBatchEquivalencePerRowAdd pins per-row
// equivalence for OpAdd (numeric). (M0074-0001.)
func TestEvalBinaryBatchEquivalencePerRowAdd(t *testing.T) {
	rng := rand.New(rand.NewSource(0xfacefeed))
	const n = 200
	left := make([]Datum, n)
	right := make([]Datum, n)
	for i := range left {
		am := rng.Int63n(10000) - 5000
		bm := rng.Int63n(10000) - 5000
		left[i] = Datum{Kind: KindNumeric, Int: am, Scale: 2}
		right[i] = Datum{Kind: KindNumeric, Int: bm, Scale: 2}
	}
	expected := make([]Datum, n)
	for i := 0; i < n; i++ {
		v, err := evalBinary(parser.OpAdd, left[i], right[i], 0, nil)
		if err != nil {
			t.Fatalf("per-row[%d]: %v", i, err)
		}
		expected[i] = v
	}
	out := make([]Datum, n)
	if err := evalBinaryBatch(parser.OpAdd, left, right, out); err != nil {
		t.Fatalf("batch: %v", err)
	}
	for i := 0; i < n; i++ {
		if cmp, _ := numericCmp(expected[i], out[i]); cmp != 0 {
			t.Errorf("Add[%d]: per-row=%v batch=%v", i, expected[i], out[i])
		}
	}
}

// TestEvalBinaryBatchNullPropagation pins three-valued
// logic: NULL operand on either side produces NullDatum
// (mirrors per-row evalBinary). (M0074-0001.)
func TestEvalBinaryBatchNullPropagation(t *testing.T) {
	left := []Datum{
		{Kind: KindInt, Int: 5},
		NullDatum,
		{Kind: KindInt, Int: 7},
		NullDatum,
	}
	right := []Datum{
		{Kind: KindInt, Int: 5},
		{Kind: KindInt, Int: 7},
		NullDatum,
		NullDatum,
	}
	out := make([]Datum, 4)
	if err := evalBinaryBatch(parser.OpEq, left, right, out); err != nil {
		t.Fatalf("batch: %v", err)
	}
	// [0]: 5=5 → true
	if out[0].Kind != KindBool || !out[0].BoolValue() {
		t.Errorf("[0] should be true, got %v", out[0])
	}
	// [1..3]: any NULL → NullDatum
	for i := 1; i < 4; i++ {
		if !out[i].IsNull() {
			t.Errorf("[%d] should be NULL, got %v", i, out[i])
		}
	}
}

// TestEvalBinaryBatchLengthMismatch pins the array-length
// invariant. (M0074-0001.)
func TestEvalBinaryBatchLengthMismatch(t *testing.T) {
	left := []Datum{{Kind: KindInt, Int: 1}}
	right := []Datum{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 2}}
	out := make([]Datum, 1)
	if err := evalBinaryBatch(parser.OpEq, left, right, out); err == nil {
		t.Error("expected length-mismatch error, got nil")
	}
}

// TestM0122VectorBatchRetainsReusedSlotBeforeNext pins the ownership boundary
// a future scan-resident batch path must take. Concrete Slot is deliberately
// reused by opNext-style producers: keeping the Slot itself over another
// producer advance aliases both its Cells slice and its row identity.
//
// The batch first snapshots each tuple with Materialize, then extracts the
// predicate column and calls the existing batch kernel. If a future path drops
// that snapshot, all three operands become the final producer value and this
// equivalence check fails.
func TestM0122VectorBatchRetainsReusedSlotBeforeNext(t *testing.T) {
	producer := &Slot{Cells: make([]Datum, 1), HasRow: true}
	batch := make([]*MaterializedSlot, 0, 3)
	for _, value := range []int64{2, 5, 8} {
		producer.Cells[0] = NewIntDatum(value)
		batch = append(batch, producer.Materialize())
	}

	left := make([]Datum, len(batch))
	right := make([]Datum, len(batch))
	out := make([]Datum, len(batch))
	for i, slot := range batch {
		left[i] = slot.Get(0)
		right[i] = NewIntDatum(5)
	}
	if err := evalBinaryBatch(parser.OpGt, left, right, out); err != nil {
		t.Fatalf("evalBinaryBatch: %v", err)
	}

	wantValues := []int64{2, 5, 8}
	wantKeep := []bool{false, false, true}
	for i := range batch {
		if got := batch[i].Get(0).Int; got != wantValues[i] {
			t.Fatalf("snapshot[%d] = %d, want %d; batch retained reused producer slot", i, got, wantValues[i])
		}
		if out[i].Kind != KindBool || out[i].BoolValue() != wantKeep[i] {
			t.Errorf("predicate[%d] = %v, want %v", i, out[i], wantKeep[i])
		}
	}
}

// reusedBatchInput models an op-node producer: it returns one Slot wrapper
// and overwrites its cells on each Next call.
type reusedBatchInput struct {
	values []int64
	pos    int
	slot   Slot
}

func (o *reusedBatchInput) Open(*Context) error      { o.pos = 0; return nil }
func (o *reusedBatchInput) Close() error             { return nil }
func (o *reusedBatchInput) Schema() optimizer.Schema { return nil }
func (o *reusedBatchInput) Next() (TupleSlot, error) {
	if o.pos == len(o.values) {
		return nil, EOF
	}
	if cap(o.slot.Cells) == 0 {
		o.slot.Cells = make([]Datum, 1)
	}
	o.slot.Cells = o.slot.Cells[:1]
	o.slot.Cells[0] = NewIntDatum(o.values[o.pos])
	o.slot.HasRow = true
	o.pos++
	return &o.slot, nil
}

// TestM0122FilterBatchSnapshotsReusedChild proves the first production batch
// caller both reaches evalBinaryBatch and snapshots a wrapper-reusing child.
func TestM0122FilterBatchSnapshotsReusedChild(t *testing.T) {
	child := &reusedBatchInput{values: []int64{1, 7, 3, 9}}
	pred := &optimizer.BinaryOp{
		Op:    parser.OpGt,
		Left:  &optimizer.ColumnRef{Name: "v", Index: 0},
		Right: &optimizer.IntegerConst{Value: 5},
	}
	o := &filterOp{child: child, pred: pred}
	removed := int64(0)
	o.setFilterRemoveCounter(&removed)
	if err := o.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer o.Close()
	if !o.batchEnabled {
		t.Fatal("simple comparison did not enable the batch path")
	}
	var got []int64
	for {
		slot, err := o.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, slot.Get(0).Int)
	}
	if want := []int64{7, 9}; !slices.Equal(got, want) {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
	if removed != 2 {
		t.Errorf("Rows Removed by Filter = %d, want 2", removed)
	}
}

// TestM0122FilterBatchReusesDatumBuffers forces a second refill and proves
// that operand and result vectors are retained. Rows themselves must not be
// retained this way: those are snapshots of a child-owned slot and have a
// distinct ownership contract.
func TestM0122FilterBatchReusesDatumBuffers(t *testing.T) {
	values := make([]int64, filterBatchSize+1)
	for i := range values {
		values[i] = int64(i)
	}
	o := &filterOp{
		child: &reusedBatchInput{values: values},
		pred: &optimizer.BinaryOp{
			Op:    parser.OpGe,
			Left:  &optimizer.ColumnRef{Name: "v", Index: 0},
			Right: &optimizer.IntegerConst{Value: 0},
		},
	}
	if err := o.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer o.Close()
	if _, err := o.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	left, right, result := &o.batchLeft[0], &o.batchRight[0], &o.batchResults[0]
	for i := 1; i < filterBatchSize; i++ {
		if _, err := o.Next(); err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
	}
	if _, err := o.Next(); err != nil {
		t.Fatalf("second batch Next: %v", err)
	}
	if &o.batchLeft[0] != left || &o.batchRight[0] != right || &o.batchResults[0] != result {
		t.Fatal("second batch allocated replacement datum buffers")
	}
}

// TestM0122SimpleBatchComparisonsRespectScanPrefixBounds pins the hand-off
// contract for a future scan-resident batch caller. Batch eligibility only
// describes expression evaluation; PlanScanQual remains the authority for
// whether the scan has deformed enough of a row to use that evaluator early.
func TestM0122SimpleBatchComparisonsRespectScanPrefixBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pred  optimizer.Expr
		ncols int
		early bool
		max   int
	}{
		{
			name:  "prefix column and constant",
			pred:  &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0}, Right: &optimizer.IntegerConst{Value: 5}},
			ncols: 2,
			early: true,
			max:   1,
		},
		{
			name:  "two prefix columns",
			pred:  &optimizer.BinaryOp{Op: parser.OpEq, Left: &optimizer.ColumnRef{Index: 0}, Right: &optimizer.ColumnRef{Index: 1}},
			ncols: 3,
			early: true,
			max:   2,
		},
		{
			name:  "whole row remains late",
			pred:  &optimizer.BinaryOp{Op: parser.OpLe, Left: &optimizer.ColumnRef{Index: 1}, Right: &optimizer.IntegerConst{Value: 9}},
			ncols: 2,
			early: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !batchFilterEligible(tc.pred) {
				t.Fatal("simple comparison did not admit the batch evaluator")
			}
			got := optimizer.PlanScanQual(tc.pred, tc.ncols)
			if got.Early != tc.early || got.MaxCols != tc.max {
				t.Fatalf("PlanScanQual = %+v, want Early=%v MaxCols=%d", got, tc.early, tc.max)
			}
		})
	}
}

// TestM0122BatchFilterRequiresRowOperand prevents constant-only filters from
// entering the vector path. They have no row-dependent work to amortize, while
// that path must still snapshot every child slot to preserve ownership.
func TestM0122BatchFilterRequiresRowOperand(t *testing.T) {
	for _, tc := range []struct {
		name string
		pred optimizer.Expr
		want bool
	}{
		{
			name: "integer constants",
			pred: &optimizer.BinaryOp{Op: parser.OpEq,
				Left: &optimizer.IntegerConst{Value: 1}, Right: &optimizer.IntegerConst{Value: 1}},
		},
		{
			name: "null and constant",
			pred: &optimizer.BinaryOp{Op: parser.OpEq,
				Left: &optimizer.NullConst{}, Right: &optimizer.StringConst{Value: "x"}},
		},
		{
			name: "column and constant",
			pred: &optimizer.BinaryOp{Op: parser.OpEq,
				Left: &optimizer.ColumnRef{Index: 0}, Right: &optimizer.IntegerConst{Value: 1}},
			want: true,
		},
		{
			name: "two columns",
			pred: &optimizer.BinaryOp{Op: parser.OpEq,
				Left: &optimizer.ColumnRef{Index: 0}, Right: &optimizer.ColumnRef{Index: 1}},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := batchFilterEligible(tc.pred); got != tc.want {
				t.Fatalf("batchFilterEligible(%T) = %v, want %v", tc.pred, got, tc.want)
			}
		})
	}
}

// TestCanVectoriseBinaryWhitelist pins the amenable-op
// whitelist; non-amenable ops return false. (M0074-0001.)
func TestCanVectoriseBinaryWhitelist(t *testing.T) {
	amenable := []parser.OpCode{
		parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe,
		parser.OpAdd, parser.OpSub, parser.OpAnd, parser.OpOr,
	}
	for _, op := range amenable {
		if !canVectoriseBinary(op) {
			t.Errorf("canVectoriseBinary(%v) = false; expected amenable", op)
		}
	}
	excluded := []parser.OpCode{
		parser.OpMul, parser.OpDiv, parser.OpMod,
		parser.OpConcat, parser.OpLike, parser.OpNotLike,
	}
	for _, op := range excluded {
		if canVectoriseBinary(op) {
			t.Errorf("canVectoriseBinary(%v) = true; expected excluded", op)
		}
	}
}

// TestCanVectoriseExpressionTreeWalk pins the recursive
// expression walker — amenable subtrees return true,
// any non-amenable node short-circuits. (M0074-0001.)
func TestCanVectoriseExpressionTreeWalk(t *testing.T) {
	// Amenable: ColumnRef = IntegerConst.
	amenable := &optimizer.BinaryOp{
		Op:    parser.OpEq,
		Left:  &optimizer.ColumnRef{Name: "a", Index: 0},
		Right: &optimizer.IntegerConst{Value: 5},
	}
	if !canVectoriseExpression(amenable) {
		t.Error("simple ColumnRef = IntegerConst should be vectorisable")
	}

	// Amenable: ColumnRef AND ColumnRef (each side itself
	// is a comparison, not just a leaf).
	amenable2 := &optimizer.BinaryOp{
		Op: parser.OpAnd,
		Left: &optimizer.BinaryOp{
			Op:    parser.OpLt,
			Left:  &optimizer.ColumnRef{Name: "a", Index: 0},
			Right: &optimizer.IntegerConst{Value: 100},
		},
		Right: &optimizer.BinaryOp{
			Op:    parser.OpGt,
			Left:  &optimizer.ColumnRef{Name: "b", Index: 1},
			Right: &optimizer.IntegerConst{Value: 50},
		},
	}
	if !canVectoriseExpression(amenable2) {
		t.Error("nested AND of comparisons should be vectorisable")
	}

	// Excluded: contains LIKE.
	excludedLike := &optimizer.BinaryOp{
		Op: parser.OpAnd,
		Left: &optimizer.BinaryOp{
			Op:    parser.OpLike,
			Left:  &optimizer.ColumnRef{Name: "a", Index: 0},
			Right: &optimizer.StringConst{Value: "x%"},
		},
		Right: &optimizer.BinaryOp{
			Op:    parser.OpEq,
			Left:  &optimizer.ColumnRef{Name: "b", Index: 1},
			Right: &optimizer.IntegerConst{Value: 5},
		},
	}
	if canVectoriseExpression(excludedLike) {
		t.Error("expression containing LIKE should not be vectorisable")
	}

	// Excluded: contains FuncCall.
	excludedFunc := &optimizer.FuncCall{Name: "lower", Args: []optimizer.Expr{&optimizer.ColumnRef{Name: "x", Index: 0}}}
	if canVectoriseExpression(excludedFunc) {
		t.Error("FuncCall should not be vectorisable")
	}
}
