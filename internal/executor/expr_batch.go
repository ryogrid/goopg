package executor

// M0074-0001: vectorised binary-eval entry point. evalBinaryBatch
// operates over parallel arrays of operands, producing one output
// per input pair. Falls back to per-row evalBinary for arms it
// can't vectorise (subquery / In / Exists / FuncCall / LIKE / Concat
// / Mul / Div).
//
// filterOp is its first production caller for simple non-scan comparisons.
// seqScanOp still evaluates absorbed predicates row-at-a-time; changing that
// path requires a separate ordering and partial-deform contract.

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
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

// batchFilterEligible intentionally admits a smaller set than
// canVectoriseExpression. The first production caller batches only a single
// row-dependent comparison whose operands are slots or constants. Constant-only
// predicates stay on the established per-row path: batching them would snapshot
// every child row without making their evaluation depend on any row. Compound
// boolean trees and arithmetic also remain on the per-row path until their
// error and short-circuit ordering have dedicated coverage.
func batchFilterEligible(e optimizer.Expr) bool {
	b, ok := e.(*optimizer.BinaryOp)
	if !ok {
		return false
	}
	switch b.Op {
	case parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
		return batchFilterOperandEligible(b.Left) && batchFilterOperandEligible(b.Right) &&
			(batchFilterColumnOperand(b.Left) || batchFilterColumnOperand(b.Right))
	}
	return false
}

func batchFilterColumnOperand(e optimizer.Expr) bool {
	_, ok := e.(*optimizer.ColumnRef)
	return ok
}

func batchFilterOperandEligible(e optimizer.Expr) bool {
	switch e.(type) {
	case *optimizer.ColumnRef, *optimizer.IntegerConst, *optimizer.NumericConst,
		*optimizer.StringConst, *optimizer.BooleanConst, *optimizer.NullConst:
		return true
	}
	return false
}

// evalFilterBatch evaluates the deliberately narrow first production batch
// shape. The caller has already checked batchFilterEligible and owns all three
// datum buffers, so consecutive batches do not allocate transient operands.
func evalFilterBatch(pred optimizer.Expr, slots []*MaterializedSlot, ctx *Context, left, right, out []Datum) error {
	b := pred.(*optimizer.BinaryOp)
	if len(left) < len(slots) || len(right) < len(slots) || len(out) < len(slots) {
		return fmt.Errorf("filter batch buffers shorter than slots: left=%d right=%d out=%d slots=%d", len(left), len(right), len(out), len(slots))
	}
	left = left[:len(slots)]
	right = right[:len(slots)]
	for i, slot := range slots {
		var err error
		if left[i], err = evalExprSlot(b.Left, slot, ctx); err != nil {
			return err
		}
		if right[i], err = evalExprSlot(b.Right, slot, ctx); err != nil {
			return err
		}
	}
	return evalBinaryBatch(b.Op, left, right, out[:len(slots)])
}

// snapshotBatchSlot copies both the row and the slot identity. Calling
// Materialize on an arbitrary TupleSlot is insufficient: a producer may reuse
// the wrapper itself, not just its row buffer.
func snapshotBatchSlot(slot TupleSlot) *MaterializedSlot {
	out := SlotFromRow(slot.Schema(), cloneRowOwned(slot.Row()))
	if block, off, ok := slot.TID(); ok {
		out.hasCTID, out.ctidBlock, out.ctidOff = true, block, off
	}
	return out
}
