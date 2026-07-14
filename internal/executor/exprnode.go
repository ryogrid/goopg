// exprnode.go — ExprNode tagged-union expression tree (Phase C.3 of M0107-0003).
//
// Compiles planner.Expr trees into a flat []ExprNode slab (exprTreeSlab)
// that lives alongside the opTreeSlab. evalFastExpr dispatches via an
// integer kind-switch rather than interface type assertions, eliminating
// itab overhead on the hot expression-evaluation path.
//
// Recognised kinds (ExprColumnRef, ExprIntConst, ExprBoolConst,
// ExprNullConst, ExprBinaryOp, ExprUnaryOp) are evaluated natively.
// All other expression kinds fall through to ExprAdapter, which
// delegates to the existing evalExprSlot path. This means correctness
// is never compromised by the migration: complex expressions (CastExpr,
// FuncCall, CaseExpr, SubqueryExpr, InExpr, etc.) continue to work
// exactly as before.

package executor

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// Integer-overflow codes carried inline in ExprBinaryOp.payload[1]. The
// compiled fast path (evalFastExpr) must apply the same int2/int4 range
// checks that evalExprSlot does after integer arithmetic; without this the
// fast path silently returns out-of-range results instead of raising
// "smallint/integer out of range" (regression from the M0107-0003 fast
// expression evaluator — see docs/design/0097-0037-fast-path-int-overflow.md).
const (
	ovfNone uint8 = 0 // no range check (int8/text/bool/etc.)
	ovfInt2 uint8 = 1 // result type int2/smallint
	ovfInt4 uint8 = 2 // result type int4/integer
)

// overflowCodeForType maps a BinaryOp ResultType to its inline overflow code.
// Mirrors the int2/int4 cases of the overflow switch in evalExprSlot.
func overflowCodeForType(resultType string) uint8 {
	switch strings.ToLower(resultType) {
	case "int2", "smallint":
		return ovfInt2
	case "int4", "integer", "int":
		return ovfInt4
	}
	return ovfNone
}

// isFloatResultType reports whether a BinaryOp ResultType denotes a
// floating-point type. Float arithmetic needs evalExprSlot's float64 code
// path (the fast path's exact integer/decimal arithmetic diverges from
// PostgreSQL float8 semantics), so such BinaryOps fall back to ExprAdapter.
func isFloatResultType(resultType string) bool {
	switch strings.ToLower(resultType) {
	case "float8", "double precision", "double", "float4", "real", "float":
		return true
	}
	return false
}

// ExprKind discriminates ExprNode kinds in the expression-tree slab.
type ExprKind uint8

const (
	ExprInvalid   ExprKind = iota
	ExprColumnRef          // payload[0:4] = int32 column index
	ExprIntConst           // payload[0:8] = int64 value
	ExprBoolConst          // payload[0] = 0 (false) or 1 (true)
	ExprNullConst          // no payload
	ExprBinaryOp           // payload[0] = uint8(parser.OpCode); childA/childB = operand indices
	ExprUnaryOp            // payload[0] = uint8(parser.OpCode); childA = operand index
	ExprAdapter            // orig = original planner.Expr; delegates to evalExprSlot
)

// noExpr is the sentinel "no expression child" index, analogous to noChild.
const noExpr = int32(-1)

// ExprNode is a tagged-union expression node stored in an exprTreeSlab.
//
// For ExprAdapter, orig holds the original planner.Expr to keep it
// GC-live and to delegate evaluation to evalExprSlot.
//
// For all concrete kinds, data is encoded in payload bytes or child
// indices; orig is nil (no GC pointer cost per row for hot kinds).
type ExprNode struct {
	Kind    ExprKind
	_pad    [3]byte
	childA  int32        // left/only child index; noExpr if none
	childB  int32        // right child index; noExpr if none
	payload [40]byte     // per-Kind inline data (see constants above)
	orig    planner.Expr // non-nil only for ExprAdapter
}

// exprTreeSlab is the flat slice of ExprNode for a single statement.
// It is appended to during BuildFast and immutable thereafter.
type exprTreeSlab []ExprNode

// buildExpr compiles e into the slab and returns its root index.
// Returns noExpr for nil input. Unrecognised expression kinds fall back
// to ExprAdapter (delegates to evalExprSlot at evaluation time, so
// correctness is always preserved).
func (s *exprTreeSlab) buildExpr(e planner.Expr) int32 {
	if e == nil {
		return noExpr
	}
	switch t := e.(type) {
	case *planner.ColumnRef:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprColumnRef})
		binary.LittleEndian.PutUint32((*s)[idx].payload[:], uint32(t.Index))
		return idx

	case *planner.IntegerConst:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprIntConst})
		binary.LittleEndian.PutUint64((*s)[idx].payload[:], uint64(t.Value))
		return idx

	case *planner.BooleanConst:
		idx := int32(len(*s))
		n := ExprNode{Kind: ExprBoolConst}
		if t.Value {
			n.payload[0] = 1
		}
		*s = append(*s, n)
		return idx

	case *planner.NullConst:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprNullConst})
		return idx

	case *planner.BinaryOp:
		// Float-typed arithmetic must use evalExprSlot's float64 path; the
		// fast path's exact arithmetic diverges from PostgreSQL float8 output.
		if isFloatResultType(t.ResultType) {
			idx := int32(len(*s))
			*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
			return idx
		}
		// Row-to-row comparisons (a,b) OP (c,d) must use evalExprSlot's
		// evalRowToRowComparison path which performs element-wise 3-valued-logic
		// (NULL in any element propagates correctly). The fast path would evaluate
		// each RowExpr as a composite text string via evalRowExpr, then compare
		// the strings — producing "(abs,20)" >= "(abs,)" = TRUE instead of NULL.
		// M0097-0128.
		if _, okL := t.Left.(*planner.RowExpr); okL {
			if _, okR := t.Right.(*planner.RowExpr); okR {
				idx := int32(len(*s))
				*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
				return idx
			}
		}
		// Row-constructor = (SELECT ...) must use evalExprSlot's element-wise
		// comparison path (evalRowFuncCallVsSubqueryExpr). The fast path's
		// ExprBinaryOp would evaluate the SubqueryExpr via ExprAdapter and fail
		// with "scalar subquery returned N columns" before reaching evalBinary.
		// M0097-0020.
		if t.Op == parser.OpEq || t.Op == parser.OpNe {
			if rowFc, okL := t.Left.(*planner.FuncCall); okL && strings.EqualFold(rowFc.Name, "row") {
				if _, okR := t.Right.(*planner.SubqueryExpr); okR {
					idx := int32(len(*s))
					*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
					return idx
				}
			}
			if rowFc, okR := t.Right.(*planner.FuncCall); okR && strings.EqualFold(rowFc.Name, "row") {
				if _, okL := t.Left.(*planner.SubqueryExpr); okL {
					idx := int32(len(*s))
					*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
					return idx
				}
			}
		}
		// Reserve this node's slot BEFORE recursing into children so the
		// index is stable even if subsequent appends reallocate the slab.
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprBinaryOp})
		childA := s.buildExpr(t.Left)
		childB := s.buildExpr(t.Right)
		(*s)[idx].childA = childA
		(*s)[idx].childB = childB
		(*s)[idx].payload[0] = uint8(t.Op)
		// payload[1] carries the int2/int4 overflow code so evalFastExpr can
		// apply the same range check evalExprSlot does. M0097 regression fix.
		(*s)[idx].payload[1] = overflowCodeForType(t.ResultType)
		return idx

	case *planner.UnaryOp:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprUnaryOp})
		childA := s.buildExpr(t.Operand)
		(*s)[idx].childA = childA
		(*s)[idx].payload[0] = uint8(t.Op)
		return idx

	default:
		// ExprAdapter: keep the original planner.Expr alive via orig and
		// delegate to evalExprSlot at evaluation time. Covers CastExpr,
		// FuncCall, CaseExpr, SubqueryExpr, InExpr, ExtractExpr, etc.
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
		return idx
	}
}

// evalFastExpr evaluates the expression tree rooted at exprs[idx] against slot.
//
// Dispatches common kinds via integer switch (no interface type assertions)
// and falls back to evalExprSlot for ExprAdapter nodes.
// Returns (NullDatum, nil) when idx == noExpr.
func evalFastExpr(exprs exprTreeSlab, idx int32, slot SlotView, ctx *Context) (Datum, error) {
	if idx == noExpr {
		return NullDatum, nil
	}
	n := &exprs[idx]
	switch n.Kind {
	case ExprColumnRef:
		colIdx := int(int32(binary.LittleEndian.Uint32(n.payload[:])))
		return slot.Get(colIdx), nil

	case ExprIntConst:
		v := int64(binary.LittleEndian.Uint64(n.payload[:]))
		return Datum{Kind: KindInt, Int: v}, nil

	case ExprBoolConst:
		return NewBoolDatum(n.payload[0] != 0), nil

	case ExprNullConst:
		return NullDatum, nil

	case ExprBinaryOp:
		if n.childA == noExpr || n.childB == noExpr {
			return Datum{}, fmt.Errorf("executor: evalFastExpr: BinaryOp node %d missing child", idx)
		}
		op := parser.OpCode(n.payload[0])
		left, err := evalFastExpr(exprs, n.childA, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// Short-circuit boolean operators before evaluating the right side,
		// matching evalExprSlot behaviour.
		switch op {
		case parser.OpAnd:
			if !left.IsNull() && !left.BoolValue() {
				return left, nil
			}
		case parser.OpOr:
			if !left.IsNull() && left.BoolValue() {
				return left, nil
			}
		}
		right, err := evalFastExpr(exprs, n.childB, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// pg_lsn arithmetic: detect before evalBinary (mirrors evalExprSlot). M0097-pg_lsn.
		if (left.Kind == KindString && looksLikePgLSN(left.StringValue())) ||
			(right.Kind == KindString && looksLikePgLSN(right.StringValue())) {
			res, handled, lsnErr := evalPgLSNBinary(op, left, right, 0)
			if lsnErr != nil {
				return Datum{}, lsnErr
			}
			if handled {
				return res, nil
			}
		}
		result, err := evalBinary(op, left, right, 0, ctx)
		if err != nil {
			return Datum{}, err
		}
		// Integer-overflow check, mirroring evalExprSlot. The fast path
		// previously skipped this, silently returning out-of-range int2/int4
		// arithmetic results. M0097 regression fix.
		if result.Kind == KindInt {
			switch n.payload[1] {
			case ovfInt2:
				if result.Int < -32768 || result.Int > 32767 {
					return Datum{}, &ExecError{Code: "22003", Message: "smallint out of range"}
				}
			case ovfInt4:
				if result.Int < -2147483648 || result.Int > 2147483647 {
					return Datum{}, &ExecError{Code: "22003", Message: "integer out of range"}
				}
			}
		}
		return result, nil

	case ExprUnaryOp:
		if n.childA == noExpr {
			return Datum{}, fmt.Errorf("executor: evalFastExpr: UnaryOp node %d missing child", idx)
		}
		op := parser.OpCode(n.payload[0])
		operand, err := evalFastExpr(exprs, n.childA, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalUnary(op, operand, 0)

	case ExprAdapter:
		return evalExprSlot(n.orig, slot, ctx)

	default:
		return Datum{}, fmt.Errorf("executor: evalFastExpr: unknown ExprKind %d at idx %d", n.Kind, idx)
	}
}
