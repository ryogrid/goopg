package executor

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
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
