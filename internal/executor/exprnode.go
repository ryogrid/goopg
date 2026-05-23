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

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

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
		// Reserve this node's slot BEFORE recursing into children so the
		// index is stable even if subsequent appends reallocate the slab.
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprBinaryOp})
		childA := s.buildExpr(t.Left)
		childB := s.buildExpr(t.Right)
		(*s)[idx].childA = childA
		(*s)[idx].childB = childB
		(*s)[idx].payload[0] = uint8(t.Op)
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
		return evalBinary(op, left, right, 0)

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
