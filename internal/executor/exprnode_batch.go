package executor

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/parser"
)

// evalFastExprBatch is evalFastExpr over many rows at once (M0122-0012; design
// docs/design/0100-0149/0122-0012b-scan-batch-qual.md).
//
// It evaluates exprs[idx] for each row rows[sel[k]] and writes the result to
// out[k]. views[i] is a SlotView over rows[i], boxed once by the caller, so the
// generic arm can hand evalFastExpr a slot without allocating per row.
//
// The point is the tree walk: evalFastExpr dispatches every node once per row,
// and after slices 2-5 that dispatch was the largest remaining cost of a
// filtered scan (slice 6). Here each node is dispatched once per batch and its
// per-row work is a tight loop.
//
// Semantics are evalFastExpr's, element for element:
//
//   - Leaves (column refs, constants) produce exactly the Datum evalFastExpr
//     returns. A column ref out of range is not handled here; it returns an
//     error so the caller re-evaluates per row and gets evalFastExpr's exact
//     error.
//   - BinaryOp calls evalFastBinaryApply, the same function the per-row arm
//     calls. AND/OR evaluate the right operand only on rows whose left value
//     the per-row arm would not short-circuit (Kind == KindBool and false for
//     AND, true for OR). So a right side that would raise on a short-circuited
//     row is never evaluated for it, as per row.
//   - Every other kind is evaluated per row through evalFastExpr itself.
//
// Errors: the first error encountered is returned and out is then undefined.
// The batch evaluates node by node, not row by row, so which row's error comes
// first can differ from the per-row order. Callers therefore treat any error as
// "re-evaluate this batch per row", which raises the per-row path's error at
// the per-row path's row. That is sound only because the expressions callers
// admit are pure (the scan prefilter's PlanScanQual excludes functions,
// subqueries and params), so an abandoned batch attempt has no side effects.
func evalFastExprBatch(exprs exprTreeSlab, idx int32, rows []Row, views []SlotView, sel []int32, ctx *Context, sc *exprBatchScratch, out []Datum) error {
	out = out[:len(sel)]
	if idx == noExpr {
		for k := range out {
			out[k] = NullDatum
		}
		return nil
	}
	n := &exprs[idx]
	switch n.Kind {
	case ExprConstVal:
		v := *n.constVal
		for k := range out {
			out[k] = v
		}
		return nil

	case ExprIntConst:
		v := Datum{Kind: KindInt, Int: int64(binary.LittleEndian.Uint64(n.payload[:]))}
		for k := range out {
			out[k] = v
		}
		return nil

	case ExprBoolConst:
		v := NewBoolDatum(n.payload[0] != 0)
		for k := range out {
			out[k] = v
		}
		return nil

	case ExprNullConst:
		for k := range out {
			out[k] = NullDatum
		}
		return nil

	case ExprColumnRef:
		colIdx := int(int32(binary.LittleEndian.Uint32(n.payload[:])))
		for k, r := range sel {
			row := rows[r]
			if colIdx < 0 || colIdx >= len(row) {
				return fmt.Errorf("executor: evalFastExprBatch: column %d out of row width %d", colIdx, len(row))
			}
			checkDeformPoison(row[colIdx])
			out[k] = row[colIdx]
		}
		return nil

	case ExprBinaryOp:
		if n.childA == noExpr || n.childB == noExpr {
			return fmt.Errorf("executor: evalFastExprBatch: BinaryOp node %d missing child", idx)
		}
		op := parser.OpCode(n.payload[0])
		left := sc.push(len(sel))
		defer sc.pop()
		if err := evalFastExprBatch(exprs, n.childA, rows, views, sel, ctx, sc, left); err != nil {
			return err
		}
		if op == parser.OpAnd || op == parser.OpOr {
			// The rows the per-row arm would evaluate the right side for.
			// pos[j] is the position in sel/out of the j-th such row.
			short := op == parser.OpOr
			sub := sc.pushSel(len(sel))
			pos := sc.pushSel(len(sel))
			defer sc.popSel(2)
			sub, pos = sub[:0], pos[:0]
			for k, l := range left {
				if l.Kind == KindBool && l.BoolValue() == short {
					out[k] = l
					continue
				}
				sub = append(sub, sel[k])
				pos = append(pos, int32(k))
			}
			if len(sub) == 0 {
				return nil
			}
			right := sc.push(len(sub))
			defer sc.pop()
			if err := evalFastExprBatch(exprs, n.childB, rows, views, sub, ctx, sc, right); err != nil {
				return err
			}
			for j, k := range pos {
				if err := evalFastBinaryApply(n, op, &left[k], &right[j], ctx, &out[k]); err != nil {
					return err
				}
			}
			return nil
		}
		right := sc.push(len(sel))
		defer sc.pop()
		if err := evalFastExprBatch(exprs, n.childB, rows, views, sel, ctx, sc, right); err != nil {
			return err
		}
		for k := range out {
			if err := evalFastBinaryApply(n, op, &left[k], &right[k], ctx, &out[k]); err != nil {
				return err
			}
		}
		return nil
	}

	// Every other kind (UnaryOp, ExprAdapter, ...): per row, through the
	// per-row evaluator itself.
	for k, r := range sel {
		v, err := evalFastExpr(exprs, idx, views[r], ctx)
		if err != nil {
			return err
		}
		out[k] = v
	}
	return nil
}

// exprBatchScratch is evalFastExprBatch's operand storage: a stack of Datum
// vectors and selection vectors, one frame per BinaryOp level being evaluated,
// reused across batches so a steady-state scan allocates nothing.
type exprBatchScratch struct {
	datums [][]Datum
	dTop   int
	sels   [][]int32
	sTop   int
}

func (sc *exprBatchScratch) push(n int) []Datum {
	if sc.dTop == len(sc.datums) {
		sc.datums = append(sc.datums, nil)
	}
	if cap(sc.datums[sc.dTop]) < n {
		sc.datums[sc.dTop] = make([]Datum, n)
	}
	v := sc.datums[sc.dTop][:n]
	sc.dTop++
	return v
}

func (sc *exprBatchScratch) pop() { sc.dTop-- }

func (sc *exprBatchScratch) pushSel(n int) []int32 {
	if sc.sTop == len(sc.sels) {
		sc.sels = append(sc.sels, nil)
	}
	if cap(sc.sels[sc.sTop]) < n {
		sc.sels[sc.sTop] = make([]int32, n)
	}
	v := sc.sels[sc.sTop][:n]
	sc.sTop++
	return v
}

func (sc *exprBatchScratch) popSel(k int) { sc.sTop -= k }
