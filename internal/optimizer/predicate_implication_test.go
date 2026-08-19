package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// colConst builds a resolved `col op const` BinaryOp for testing
// provePartialIndexPredicate directly, without going through the full
// planner. name is the column, op the operator, val the integer constant.
func colConst(name string, op parser.OpCode, val int64) *BinaryOp {
	return &BinaryOp{Op: op, Left: &ColumnRef{Name: name}, Right: &IntegerConst{Value: val}}
}

func constCol(val int64, op parser.OpCode, name string) *BinaryOp {
	return &BinaryOp{Op: op, Left: &IntegerConst{Value: val}, Right: &ColumnRef{Name: name}}
}

// TestProvePartialIndexPredicate_IdenticalClauseAccepted is the headline
// case: predicate and query clause are the exact same `Var op Const` shape.
func TestProvePartialIndexPredicate_IdenticalClauseAccepted(t *testing.T) {
	pred := colConst("seqno", parser.OpEq, 9999)
	clause := colConst("seqno", parser.OpEq, 9999)
	if !provePartialIndexPredicate(pred, clause) {
		t.Fatal("identical Var-op-Const clauses were not proven implied")
	}
}

// TestProvePartialIndexPredicate_ConstColOrderingAccepted covers BOTH clause
// orderings: `const op col` on either side must be normalized (with the
// operator mirrored) before comparison.
func TestProvePartialIndexPredicate_ConstColOrderingAccepted(t *testing.T) {
	pred := constCol(9999, parser.OpEq, "seqno") // 9999 = seqno
	clause := colConst("seqno", parser.OpEq, 9999)
	if !provePartialIndexPredicate(pred, clause) {
		t.Fatal("const-op-col predicate ordering was not proven implied")
	}

	predLt := constCol(20, parser.OpLt, "unique1")   // 20 < unique1  <=>  unique1 > 20
	clauseGt := colConst("unique1", parser.OpGt, 20) // unique1 > 20
	if !provePartialIndexPredicate(predLt, clauseGt) {
		t.Fatal("const-op-col with a non-symmetric operator (< mirrored to >) was not proven implied")
	}
}

// TestProvePartialIndexPredicate_DifferingOperatorRefused: same column, same
// constant, different operator (< vs =) must not be proven implied.
func TestProvePartialIndexPredicate_DifferingOperatorRefused(t *testing.T) {
	pred := colConst("seqno", parser.OpLt, 9999)
	clause := colConst("seqno", parser.OpEq, 9999)
	if provePartialIndexPredicate(pred, clause) {
		t.Fatal("differing operators (< vs =) were proven implied")
	}
}

// TestProvePartialIndexPredicate_DifferingConstantRefused: same column, same
// operator, different constant must not be proven implied.
func TestProvePartialIndexPredicate_DifferingConstantRefused(t *testing.T) {
	pred := colConst("seqno", parser.OpEq, 9999)
	clause := colConst("seqno", parser.OpEq, 1)
	if provePartialIndexPredicate(pred, clause) {
		t.Fatal("differing constants were proven implied")
	}
}

// TestProvePartialIndexPredicate_DifferingColumnRefused: same operator, same
// constant, different column must not be proven implied.
func TestProvePartialIndexPredicate_DifferingColumnRefused(t *testing.T) {
	pred := colConst("seqno", parser.OpEq, 9999)
	clause := colConst("random", parser.OpEq, 9999)
	if provePartialIndexPredicate(pred, clause) {
		t.Fatal("differing columns were proven implied")
	}
}

// TestProvePartialIndexPredicate_NonConstRHSRefused: a ColumnRef, a ParamRef,
// or a function call on the non-Var side must refuse — provePartialIndexPredicate
// only ever reasons about a literal Const, matching upstream's
// operator_predicate_proof leaf case.
func TestProvePartialIndexPredicate_NonConstRHSRefused(t *testing.T) {
	pred := colConst("seqno", parser.OpEq, 9999)

	twoColumn := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Name: "seqno"}, Right: &ColumnRef{Name: "other"}}
	if provePartialIndexPredicate(pred, twoColumn) {
		t.Fatal("a two-column comparison was proven implied")
	}

	paramRHS := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Name: "seqno"}, Right: &ParamRef{Number: 1}}
	if provePartialIndexPredicate(pred, paramRHS) {
		t.Fatal("a ParamRef RHS was proven implied")
	}

	funcCallRHS := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Name: "seqno"}, Right: &FuncCall{Name: "abs"}}
	if provePartialIndexPredicate(pred, funcCallRHS) {
		t.Fatal("a function-call RHS was proven implied")
	}
}

// TestProvePartialIndexPredicate_ISNullRefused: `IS NULL` is not a BinaryOp
// at all — the recognizer must refuse it outright rather than panic or
// mis-shape it into a comparison.
func TestProvePartialIndexPredicate_ISNullRefused(t *testing.T) {
	pred := colConst("seqno", parser.OpEq, 9999)
	isNull := &IsNullExpr{Operand: &ColumnRef{Name: "seqno"}, Negated: false}
	if provePartialIndexPredicate(pred, isNull) {
		t.Fatal("an IS NULL clause was proven implied")
	}
	// Also check it as the predicate side.
	if provePartialIndexPredicate(isNull, colConst("seqno", parser.OpEq, 9999)) {
		t.Fatal("an IS NULL predicate was proven implied")
	}
}

// TestProvePartialIndexPredicate_ANDORTreeRefused pins the load-bearing
// non-regression shape: an OR of two ranges (the `onek2_u1_prtl` predicate
// that produced 0 rows where 1 exists — deferral ledger, 2026-08-07,
// `M0127 S7 gate / AI-20260806-232940-001,-002`) must never be proven
// implied by a single equality clause. An AND tree is refused the same way
// (this helper only ever unwraps a single leaf BinaryOp).
func TestProvePartialIndexPredicate_ANDORTreeRefused(t *testing.T) {
	orPred := &BinaryOp{
		Op:    parser.OpOr,
		Left:  colConst("unique1", parser.OpLt, 20),
		Right: colConst("unique1", parser.OpGt, 980),
	}
	clause := colConst("unique1", parser.OpEq, 50)
	if provePartialIndexPredicate(orPred, clause) {
		t.Fatal("an OR-of-ranges predicate was proven implied by an unrelated equality — this is the 2026-08-07 wrong-answer shape")
	}

	andPred := &BinaryOp{
		Op:    parser.OpAnd,
		Left:  colConst("seqno", parser.OpGe, 0),
		Right: colConst("seqno", parser.OpLe, 9999),
	}
	if provePartialIndexPredicate(andPred, colConst("seqno", parser.OpEq, 9999)) {
		t.Fatal("an AND-tree predicate was proven implied")
	}
}

// TestProvePartialIndexPredicate_NilRefused: nil predicate or clause must
// refuse rather than panic.
func TestProvePartialIndexPredicate_NilRefused(t *testing.T) {
	if provePartialIndexPredicate(nil, colConst("seqno", parser.OpEq, 9999)) {
		t.Fatal("nil predicate was proven implied")
	}
	if provePartialIndexPredicate(colConst("seqno", parser.OpEq, 9999), nil) {
		t.Fatal("nil clause was proven implied")
	}
}
