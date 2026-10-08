package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestHashQualCostChargesExpressionKeys pins M0146-0116 against
// final_cost_hashjoin's bucket walk, `hash_qual_cost.per_tuple * outer_rows *
// clamp_row_est(inner_rows * innerbucketsize) * 0.5`, where hash_qual_cost is
// cost_qual_eval over the hash clauses. TPC-DS Q2's `(wswscs_1.d_week_seq - 53)
// = date_dim.d_week_seq` is two operators per comparison; goopg charged one per
// clause, so the join it probes was priced too cheap and won the search.
func TestHashQualCostChargesExpressionKeys(t *testing.T) {
	cp := defaultCostParams()
	col := func(i int) Expr { return &ColumnRef{Index: i} }
	plain := &restrictInfo{clause: &BinaryOp{Op: parser.OpEq, Left: col(0), Right: col(1)}}
	shifted := &restrictInfo{clause: &BinaryOp{Op: parser.OpEq,
		Left:  &BinaryOp{Op: parser.OpSub, Left: col(0), Right: &IntegerConst{Value: 53}},
		Right: col(1)}}
	if got := hashClausesPerTuple(cp, []*restrictInfo{plain}); !approx(got, cp.cpuOperatorCost) {
		t.Fatalf("a = b: %v, want %v", got, cp.cpuOperatorCost)
	}
	if got := hashClausesPerTuple(cp, []*restrictInfo{shifted}); !approx(got, 2*cp.cpuOperatorCost) {
		t.Fatalf("(a - 53) = b: %v, want %v", got, 2*cp.cpuOperatorCost)
	}

	in := hashJoinInputs{
		outer: Cost{Total: 200}, inner: Cost{Total: 1400},
		outerRows: 10000, innerRows: 360,
		outputRows: 360, numHashClauses: 1,
		innerBucketSize: 0.02,
		outerCols:       2, innerCols: 2,
	}
	base := hashJoinCost(cp, in)
	in.hashQualCost = hashClausesPerTuple(cp, []*restrictInfo{shifted})
	withExpr := hashJoinCost(cp, in)
	// One more operator per comparison: outer 10000 × bucket 7.2 × 0.5.
	if want := cp.cpuOperatorCost * 10000 * clampRowEst(360*0.02) * 0.5; !approx(withExpr.Total-base.Total, want) {
		t.Fatalf("expression key adds %v, want %v", withExpr.Total-base.Total, want)
	}
	if !approx(withExpr.Startup, base.Startup) {
		t.Fatalf("the bucket walk is run cost: startup %v vs %v", withExpr.Startup, base.Startup)
	}
}

// TestJoinQualPerTupleCountsOperators pins M0146-0118 against final_cost_*'s
// `qp_qual_cost`, cost_qual_eval over the join's residual quals: every
// operator a conjunct evaluates is charged, so TPC-DS Q4/Q11's CASE-laden
// Join Filters cost what PG charges for them. goopg charged one
// cpu_operator_cost per conjunct regardless of its contents.
func TestJoinQualPerTupleCountsOperators(t *testing.T) {
	cp := defaultCostParams()
	col := func(i int) Expr { return &ColumnRef{Index: i} }
	op := func(o parser.OpCode, l, r Expr) Expr { return &BinaryOp{Op: o, Left: l, Right: r} }
	ratio := &CaseExpr{
		Whens: []CaseWhen{{When: op(parser.OpGt, col(0), &IntegerConst{Value: 0}), Then: op(parser.OpDiv, col(1), col(0))}},
		Else:  &NullConst{},
	}
	for _, tc := range []struct {
		name  string
		quals []Expr
		ops   float64
	}{
		{"a < b", []Expr{op(parser.OpLt, col(0), col(1))}, 1},
		{"(a + b) > c", []Expr{op(parser.OpGt, op(parser.OpAdd, col(0), col(1)), col(2))}, 2},
		{"CASE WHEN a > 0 THEN b / a END > c", []Expr{op(parser.OpGt, ratio, col(2))}, 3},
		{"two conjuncts", []Expr{op(parser.OpLt, col(0), col(1)), op(parser.OpNe, col(1), col(2))}, 2},
	} {
		ris := make([]*restrictInfo, len(tc.quals))
		for i, q := range tc.quals {
			ris[i] = &restrictInfo{clause: q}
		}
		if got, want := joinQualPerTuple(cp, ris), tc.ops*cp.cpuOperatorCost; !approx(got, want) {
			t.Errorf("%s: %v, want %v", tc.name, got, want)
		}
	}
}
