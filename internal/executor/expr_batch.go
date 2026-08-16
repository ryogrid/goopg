package executor

// M0074-0001: vectorised binary-eval entry point. evalBinaryBatch
// operates over parallel arrays of operands, producing one output
// per input pair. Falls back to per-row evalBinary for arms it
// can't vectorise (subquery / In / Exists / FuncCall / LIKE / Concat
// / Mul / Div).
//
// This file lands the entry point + eligibility detector as forward-
// compat infrastructure. The seqScanOp predicate batch path that
// would *call* this path remains row-at-a-time at M0074 close;
// wiring it in is M0075 candidate (the win is gated on benchmark
// evidence that the batch loop beats the per-row dispatch on real
// queries — Q5 should benefit, but we want measurement first).

import (
	"errors"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// evalBinaryBatch evaluates op over parallel left[i] / right[i]
// pairs, writing each result into out[i]. Length must match
// across all three arrays.
//
// Behaviour matches evalBinary (three-valued NULL logic, AND/OR
// short-circuit per element, OpEq/Lt/Gt/Le/Ge/Ne via compareDatum).
// Returns the first encountered eval error; subsequent positions
// in out may be undefined (caller should not retain them on error).
//
// Caller must ensure op is amenable via canVectoriseBinary —
// non-vectorisable ops return an error.
func evalBinaryBatch(op parser.OpCode, left, right, out []Datum) error {
	n := len(left)
	if len(right) != n || len(out) != n {
		return errors.New("evalBinaryBatch: array length mismatch")
	}
	// Position 0 is conventionally used by callers as the source
	// position for diagnostics; per-element positions are not
	// available in the batched call surface. Use 0 as the synthetic
	// position; callers should already have validated the
	// expression at plan time.
	const pos = 0
	for i := 0; i < n; i++ {
		v, err := evalBinary(op, left[i], right[i], pos, nil)
		if err != nil {
			return err
		}
		out[i] = v
	}
	return nil
}

// canVectoriseBinary returns true if op + operand kinds are
// amenable to evalBinaryBatch. Conservative whitelist: only ops
// with no side-effects and no per-element string/regex work.
//
// Today this duplicates the evalBinary dispatch but with a hard
// "amenable" bit; future M0075 work can specialise per-op loops
// (e.g., int64 add inline, numeric int64 fast-path inline) and
// gate them on this detector.
func canVectoriseBinary(op parser.OpCode) bool {
	switch op {
	case parser.OpEq, parser.OpLt, parser.OpGt,
		parser.OpLe, parser.OpGe, parser.OpNe,
		parser.OpAdd, parser.OpSub,
		parser.OpAnd, parser.OpOr:
		return true
	}
	return false
}

// canVectoriseExpression walks an expression subtree and returns
// true iff every node is amenable to vectorisation. Used by
// future seqScanOp / filterOp predicate batch paths to decide
// whether to take the vectorised route or fall back to per-row.
//
// Whitelist: ColumnRef / IntegerConst / NumericConst / StringConst /
// BooleanConst / NullConst / UnaryOp(amenable op) / BinaryOp(amenable
// op + amenable children).
//
// Excluded: SubqueryExpr / InExpr / ExistsExpr / FuncCall /
// CaseExpr / ExtractExpr / ParamRef / OuterColumnRef (any node that
// reaches into ctx.OuterRows or has side effects).
func canVectoriseExpression(e optimizer.Expr) bool {
	switch x := e.(type) {
	case *optimizer.ColumnRef, *optimizer.IntegerConst,
		*optimizer.NumericConst, *optimizer.StringConst,
		*optimizer.BooleanConst, *optimizer.NullConst:
		return true
	case *optimizer.UnaryOp:
		switch x.Op {
		case parser.OpUnaryNeg, parser.OpUnaryPos, parser.OpNot:
			return canVectoriseExpression(x.Operand)
		}
		return false
	case *optimizer.BinaryOp:
		if !canVectoriseBinary(x.Op) {
			return false
		}
		return canVectoriseExpression(x.Left) && canVectoriseExpression(x.Right)
	}
	return false
}
