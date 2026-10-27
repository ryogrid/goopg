// exprnode.go — ExprNode tagged-union expression tree (Phase C.3 of M0107-0003).
//
// Compiles planner.Expr trees into a flat []ExprNode slab (exprTreeSlab)
// that lives alongside the opTreeSlab. evalFastExpr dispatches via an
// integer kind-switch rather than interface type assertions, eliminating
// itab overhead on the hot expression-evaluation path.
//
// Recognised kinds (ExprColumnRef, ExprIntConst, ExprBoolConst,
// ExprNullConst, ExprBinaryOp, ExprUnaryOp, ExprConstVal, ExprCase,
// ExprNullTest) are evaluated natively.
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
	"unsafe"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
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
	ExprInvalid        ExprKind = iota
	ExprColumnRef               // payload[0:4] = int32 column index
	ExprIntConst                // payload[0:8] = int64 value
	ExprBoolConst               // payload[0] = 0 (false) or 1 (true)
	ExprNullConst               // no payload
	ExprBinaryOp                // payload[0] = op, [1] = overflow code, [4:8] = int32 pos; childA/childB = operands
	ExprUnaryOp                 // payload[0] = uint8(parser.OpCode), [4:8] = int32 pos; childA = operand index
	ExprAdapter                 // orig = original planner.Expr; delegates to evalExprSlot
	ExprConstVal                // constVal = a subtree folded to a value at build time
	ExprCase                    // caseInfo = the CASE's operand / WHEN / THEN / ELSE nodes
	ExprNullTest                // payload[0] = 1 for IS NOT NULL; childA = operand
	ExprCoalesce                // childA = first argument, childB = the rest (another ExprCoalesce, or the last argument)
	ExprOuterColumnRef          // payload[0:4] = int32 level, [4:8] = int32 column index; orig kept for the error path
	ExprExecParamRef            // payload[0:4] = int32 PARAM_EXEC slot; orig kept for the error path
)

// noExpr is the sentinel "no expression child" index, analogous to noChild.
const noExpr = int32(-1)

// ExprNode is a tagged-union expression node stored in an exprTreeSlab.
//
// For ExprAdapter, orig holds the original planner.Expr to keep it
// GC-live and to delegate evaluation to evalExprSlot.
//
// For all concrete kinds except ExprColumnRef, data is encoded in payload
// bytes or child indices and orig is nil. ExprColumnRef also keeps orig:
// evalFastExpr's out-of-range arm delegates to evalExprSlot so the raised
// error is byte-identical to the interpreted twin's (M0127-PS6.1; see the
// bounds-check comment in evalFastExpr). The pointer is read on the error
// path only, and the planner.Expr tree is retained by the plan regardless,
// so it adds no retention.
type ExprNode struct {
	Kind    ExprKind
	_pad    [3]byte
	childA  int32        // left/only child index; noExpr if none
	childB  int32        // right child index; noExpr if none
	payload [40]byte     // per-Kind inline data (see constants above)
	orig    optimizer.Expr // non-nil only for ExprAdapter
	// constVal is the folded value for ExprConstVal; for an ExprBinaryOp
	// comparison it is the bare literal operand coerced once to the kind in
	// payload[17] (unknownLitCoercion, unknown_literal.go); nil otherwise. A Datum is 48 bytes against payload's 40, so it cannot ride inline;
	// a pointer costs 8 bytes on every node instead of growing payload by 48.
	constVal *Datum
	// caseInfo is the ExprCase node's arms (evalFastCase), nil for every
	// other kind.
	caseInfo *exprCaseInfo
}

// exprTreeSlab is the flat slice of ExprNode for a single statement.
// It is appended to during BuildFast and immutable thereafter.
type exprTreeSlab []ExprNode

// buildExpr compiles e into the slab and returns its root index.
// Returns noExpr for nil input. Unrecognised expression kinds fall back
// to ExprAdapter (delegates to evalExprSlot at evaluation time, so
// correctness is always preserved).
func (s *exprTreeSlab) buildExpr(e optimizer.Expr) int32 {
	return s.buildExprCtx(e, nil)
}

// buildExprCtx is buildExpr with optional CONSTANT FOLDING.
//
// With ctx == nil the result is byte-identical to buildExpr's, so the existing
// call sites cannot change behaviour. A caller that passes a real Context opts
// in: any subtree that reads no column and is deterministic is evaluated ONCE
// here, through evalExprSlot — the very evaluator that would otherwise run it
// per row — and replaced by its value.
//
// Why this matters: TPC-H Q6's predicate contains
// `'1994-01-01'::date + '1 year'::interval`, which goopg re-evaluated six
// million times per query (4.48 % of the whole query's CPU in addTimeInterval
// alone). PostgreSQL folds the same expression in eval_const_expressions at
// plan time. See docs/design/not_ralph/tpch-q6-numeric-decode/design-take7.md.
//
// Folding declines rather than reasons whenever it is unsure:
//   - exprFoldable is a WHITELIST, so an expression kind it does not name is
//     never folded;
//   - a subtree whose evaluation ERRORS is left uncompiled, so the error is
//     still raised per row with the same message, position and row ordering.
//     Folding a failing constant would move a runtime error to build time and
//     change whether earlier rows are returned first.
func (s *exprTreeSlab) buildExprCtx(e optimizer.Expr, ctx *Context) int32 {
	if e == nil {
		return noExpr
	}
	if ctx != nil && exprFoldable(e) {
		if d, ok := foldConstant(e, ctx); ok {
			idx := int32(len(*s))
			v := d
			*s = append(*s, ExprNode{Kind: ExprConstVal, constVal: &v})
			return idx
		}
	}
	switch t := e.(type) {
	case *optimizer.ColumnRef:
		idx := int32(len(*s))
		// orig is kept so the bounds-check arm of evalFastExpr can raise the
		// interpreted twin's exact error rather than a second, diverging one.
		*s = append(*s, ExprNode{Kind: ExprColumnRef, orig: e})
		binary.LittleEndian.PutUint32((*s)[idx].payload[:], uint32(t.Index))
		return idx

	case *optimizer.OuterColumnRef:
		// A correlated sublink's reference to its parent's row (PG: a Var of
		// varlevelsup > 0 turned into a PARAM_EXEC, read by EEOP_PARAM_EXEC).
		// It was an ExprAdapter, so TPC-DS Q1's `ctr1.ctr_store_sk =
		// ctr_store_sk` paid an interpreted call per inner row — 12 881
		// SubPlan executions × 12 881 CTE rows (M0122-0012 slice 75). orig
		// is kept so an out-of-range read raises the interpreted twin's error.
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprOuterColumnRef, orig: e})
		binary.LittleEndian.PutUint32((*s)[idx].payload[0:], uint32(int32(t.Level)))
		binary.LittleEndian.PutUint32((*s)[idx].payload[4:], uint32(int32(t.Index)))
		return idx

	case *optimizer.ExecParamRef:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprExecParamRef, orig: e})
		binary.LittleEndian.PutUint32((*s)[idx].payload[0:], uint32(int32(t.ID)))
		return idx

	case *optimizer.IntegerConst:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprIntConst})
		binary.LittleEndian.PutUint64((*s)[idx].payload[:], uint64(t.Value))
		return idx

	case *optimizer.BooleanConst:
		idx := int32(len(*s))
		n := ExprNode{Kind: ExprBoolConst}
		if t.Value {
			n.payload[0] = 1
		}
		*s = append(*s, n)
		return idx

	case *optimizer.NullConst:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprNullConst})
		return idx

	case *optimizer.StringConst:
		// A bare string literal is evalExprSlot's NewStringDatum(Value) — a
		// constant — so it compiles to ExprConstVal even without a Context
		// (buildExpr; the fold above needs one). It was an ExprAdapter, an
		// interpreted call per row: TPC-DS Q59's seven `d_day_name =
		// 'Sunday'`-style CASE arms inside aggregate arguments made seven of
		// them per row (M0122-0012 slice 64). A BinaryOp still reads the
		// literal-ness of its operands from the planner expression (flags),
		// not from this node.
		idx := int32(len(*s))
		v := NewStringDatum(t.Value)
		*s = append(*s, ExprNode{Kind: ExprConstVal, constVal: &v})
		return idx

	case *optimizer.BinaryOp:
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
		// ROW(...) calls take the same path (M0146-0080).
		if _, okL := rowCtorElems(t.Left); okL {
			if _, okR := rowCtorElems(t.Right); okR && isRowCompareOp(t.Op) {
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
			if rowFc, okL := t.Left.(*optimizer.FuncCall); okL && strings.EqualFold(rowFc.Name, "row") {
				if _, okR := t.Right.(*optimizer.SubqueryExpr); okR {
					idx := int32(len(*s))
					*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
					return idx
				}
			}
			if rowFc, okR := t.Right.(*optimizer.FuncCall); okR && strings.EqualFold(rowFc.Name, "row") {
				if _, okL := t.Left.(*optimizer.SubqueryExpr); okL {
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
		childA := s.buildExprCtx(t.Left, ctx)
		childB := s.buildExprCtx(t.Right, ctx)
		(*s)[idx].childA = childA
		(*s)[idx].childB = childB
		(*s)[idx].payload[0] = uint8(t.Op)
		// payload[1] carries the int2/int4 overflow code so evalFastExpr can
		// apply the same range check evalExprSlot does. M0097 regression fix.
		(*s)[idx].payload[1] = overflowCodeForType(t.ResultType)
		// payload[4:8] carries the source position. The interpreted twin passes
		// x.Pos() to evalBinary/evalPgLSNBinary and stamps it on every ExecError
		// it raises; the compiled twin passed a literal 0, so the SAME failing
		// expression carried a position on one evaluator and none on the other.
		// It is compiled in at build time rather than read from orig because
		// only ExprColumnRef keeps orig. M0127-PS6.2 sibling audit.
		binary.LittleEndian.PutUint32((*s)[idx].payload[4:], uint32(int32(t.Pos())))
		// payload[8:12] / [12:16] carry each operand's declared bpchar width,
		// compiled in here because this is the last point at which the operand
		// EXPRESSION still exists — evalFastExpr only ever sees Datums, and a
		// blank-padded bpchar datum is indistinguishable from a text value that
		// genuinely ends in spaces. `||` and the comparison operators consume
		// them (concatOperandsAsText / comparisonOperandsAsBpchar); storing
		// them unconditionally keeps the build arm free of an op test.
		// payload[16] bits 0/1 flag an untyped string literal on the
		// left/right — PG's unknown-literal arm of the same rule. Sibling of
		// the interpreted twin's direct declaredBpcharTypmod /
		// isBareStringLit calls in expr.go's normalBinaryOp arm — the two
		// MUST agree, which is why they share one helper.
		binary.LittleEndian.PutUint32((*s)[idx].payload[8:], uint32(declaredBpcharTypmod(t.Left)))
		binary.LittleEndian.PutUint32((*s)[idx].payload[12:], uint32(declaredBpcharTypmod(t.Right)))
		if isBareStringLit(t.Left) {
			(*s)[idx].payload[16] |= 1
		}
		if isBareStringLit(t.Right) {
			(*s)[idx].payload[16] |= 2
		}
		// Bit 2: a comparison of two character-string operands, which
		// compares as text whatever the values look like (M0146-0053;
		// sibling of the interpreted twin's exprIsCharacterString test).
		if isComparisonOpCode(t.Op) && exprIsCharacterString(t.Left) && exprIsCharacterString(t.Right) {
			(*s)[idx].payload[16] |= 4
		}
		// constVal / payload[17]: a bare literal compared with a typed
		// operand, coerced once here rather than per row by compareDatum
		// (M0122-0012 slice 43; PG coerces the unknown literal at parse
		// time). Applied by coerceUnknownLit in both evaluators.
		if lit, kind, ok := unknownLitCoercion(t, (*s)[idx].payload[16]); ok {
			(*s)[idx].constVal = &lit
			(*s)[idx].payload[17] = uint8(kind)
		}
		// Bits 8 / 16: `||` resolved to textcat / jsonb_concat from the
		// static operand types (M0146-0074; the interpreted twin calls
		// concatModeOf directly).
		if t.Op == parser.OpConcat {
			switch concatModeOf(t.Left, t.Right) {
			case concatText:
				(*s)[idx].payload[16] |= 8
			case concatJSONB:
				(*s)[idx].payload[16] |= 16
			case concatArrayCat:
				(*s)[idx].payload[16] |= 32
			case concatArrayAppend:
				(*s)[idx].payload[16] |= 64
			case concatArrayPrepend:
				(*s)[idx].payload[16] |= 128
			}
		}
		return idx

	case *optimizer.CaseExpr:
		// M0122-0012 slice 47: CASE compiles, so a CASE inside an aggregate
		// argument, a projection or a qual no longer flattens the slot into a
		// Row and re-boxes it for every branch it evaluates (evalCaseExpr's
		// path through evalExprSlot). A malformed arm keeps the adapter.
		for _, w := range t.Whens {
			if w.When == nil || w.Then == nil {
				idx := int32(len(*s))
				*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
				return idx
			}
		}
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprCase})
		ci := &exprCaseInfo{operand: noExpr, els: noExpr}
		if t.Operand != nil {
			ci.operand = s.buildExprCtx(t.Operand, ctx)
			ci.opBP = declaredBpcharTypmod(t.Operand)
			ci.opLit = isBareStringLit(t.Operand)
		}
		for _, w := range t.Whens {
			ci.whens = append(ci.whens, s.buildExprCtx(w.When, ctx))
			ci.thens = append(ci.thens, s.buildExprCtx(w.Then, ctx))
			ci.whenBP = append(ci.whenBP, declaredBpcharTypmod(w.When))
			ci.whenLit = append(ci.whenLit, isBareStringLit(w.When))
		}
		if t.Else != nil {
			ci.els = s.buildExprCtx(t.Else, ctx)
		}
		(*s)[idx].caseInfo = ci
		return idx

	case *optimizer.IsNullExpr:
		// M0122-0012 slice 59: IS [NOT] NULL compiles, so a CASE arm or a qual
		// testing a column no longer drops to evalExprSlot for every row
		// (TPC-DS Q97's `ssci.customer_sk IS NOT NULL AND csci.customer_sk IS
		// NULL`). A row-valued operand keeps the adapter: its field-wise rule
		// (evalRowNullTest) is not the scalar test.
		if _, isRow := t.Operand.(*optimizer.RowExpr); isRow || t.Operand == nil {
			idx := int32(len(*s))
			*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
			return idx
		}
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprNullTest})
		childA := s.buildExprCtx(t.Operand, ctx)
		(*s)[idx].childA = childA
		if t.Negated {
			(*s)[idx].payload[0] = 1
		}
		return idx

	case *optimizer.FuncCall:
		// M0122-0012 slice 70: COALESCE compiles, so a projection such as
		// TPC-DS Q75's `cs_quantity - COALESCE(cr_return_quantity, 0)` no
		// longer drops the whole target to evalExprSlot per row. evalFuncCall's
		// "coalesce" arm is the reference: the first non-NULL argument as is,
		// arguments evaluated in order and an argument's error raised. That is
		// coalesce(a, coalesce(b, ...)), so the arguments chain through childB.
		// Every other function keeps the adapter.
		if strings.EqualFold(t.Name, "coalesce") && len(t.Args) > 0 {
			return s.buildCoalesce(t.Args, ctx)
		}
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprAdapter, orig: e})
		return idx

	case *optimizer.UnaryOp:
		idx := int32(len(*s))
		*s = append(*s, ExprNode{Kind: ExprUnaryOp})
		childA := s.buildExprCtx(t.Operand, ctx)
		(*s)[idx].childA = childA
		(*s)[idx].payload[0] = uint8(t.Op)
		binary.LittleEndian.PutUint32((*s)[idx].payload[4:], uint32(int32(t.Pos())))
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

// buildCoalesce compiles COALESCE(args...) as an ExprCoalesce chain: the
// first argument and the COALESCE of the rest; a single argument is itself.
func (s *exprTreeSlab) buildCoalesce(args []optimizer.Expr, ctx *Context) int32 {
	if len(args) == 1 {
		return s.buildExprCtx(args[0], ctx)
	}
	idx := int32(len(*s))
	*s = append(*s, ExprNode{Kind: ExprCoalesce})
	childA := s.buildExprCtx(args[0], ctx)
	childB := s.buildCoalesce(args[1:], ctx)
	(*s)[idx].childA = childA
	(*s)[idx].childB = childB
	return idx
}

// evalFastColumnRefErr raises the out-of-range (or nil-slot) ColumnRef error
// for the compiled evaluator. It delegates to evalExprSlot on the original
// planner.Expr so the message is exactly the interpreted twin's — including
// the column name and the per-concrete-type wording — and only synthesises
// its own text when a node was built without orig. M0127-PS6.1.
func evalFastColumnRefErr(n *ExprNode, slot SlotView, ctx *Context, colIdx, width int) (Datum, error) {
	if n.orig != nil {
		d, err := evalExprSlot(n.orig, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// evalExprSlot accepted an index this arm rejected: the two
		// evaluators disagree about what is in range, which is a defect in
		// this guard, not in the plan. Fail loudly rather than papering over
		// a divergence the sibling rule exists to catch.
		return d, &ExecError{Code: "XX000", Message: fmt.Sprintf(
			"executor: compiled/interpreted ColumnRef bounds disagree at index %d (width %d)", colIdx, width)}
	}
	return Datum{}, &ExecError{Code: "XX000", Message: fmt.Sprintf("column ref %d out of slot range %d", colIdx, width)}
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
	case ExprConstVal:
		// Folded at build time by buildExprCtx. The value is whatever
		// evalExprSlot produced for this subtree, so no evaluation semantics
		// live here — there is nothing to keep in sync with the interpreter.
		return *n.constVal, nil

	case ExprColumnRef:
		colIdx := int(int32(binary.LittleEndian.Uint32(n.payload[:])))
		// Sibling-path guard (M0127-PS6.1, 09 §1). The interpreted twin
		// bounds-checks every ColumnRef before Get and raises XX000
		// (expr.go:353-393); that check exists because a raw index panic
		// escaped the hash-join build-side drain — which gatherOp.Open runs
		// in the LEADER goroutine, outside ParallelGroup.Go's recover — and
		// closed the client socket (TPC-DS Q8). PS6.1 puts this evaluator on
		// that exact seam, so the compiled twin must make the same promise:
		// an ERROR kills the statement, never the backend.
		//
		// The check is a CONCRETE type switch, not an assertion to a
		// `Width() int` capability interface. Measured (BenchmarkJoinKeyEval):
		// the capability assertion is an itab lookup and costs ~1.4 ns/eval,
		// which alone made the compiled key arm SLOWER than the interpreter it
		// replaces. A concrete switch is a type-descriptor compare, and it
		// also lets each arm call its own Get directly — so the guard is added
		// while the interface dispatch on Get is removed.
		//
		// An out-of-range index delegates to evalExprSlot so the raised error
		// is byte-identical to the interpreted twin's per-type text rather
		// than a second, diverging one. It is the error path; cost is moot.
		switch s := slot.(type) {
		case *MaterializedSlot:
			if colIdx < 0 || colIdx >= len(s.row) {
				return evalFastColumnRefErr(n, slot, ctx, colIdx, len(s.row))
			}
			// EX1-01 tail-poison (see evalExprSlot's ColumnRef arm).
			d := s.Get(colIdx)
			checkDeformPoison(d)
			return d, nil
		case *VirtualSlot:
			if colIdx < 0 || colIdx >= len(s.cols) {
				return evalFastColumnRefErr(n, slot, ctx, colIdx, len(s.cols))
			}
			d := s.Get(colIdx)
			checkDeformPoison(d)
			return d, nil
		case *Slot:
			if colIdx < 0 || colIdx >= len(s.Cells) {
				return evalFastColumnRefErr(n, slot, ctx, colIdx, len(s.Cells))
			}
			d := s.Get(colIdx)
			checkDeformPoison(d)
			return d, nil
		case *PackedSlot:
			// R-0 site 2 (04 §9.1), D-03. Without an arm a *PackedSlot falls
			// past the switch to the unchecked `slot.Get(colIdx)` below, which
			// loses the bounds guard this whole switch exists to provide — and
			// an out-of-range index there panics raw in a goroutine outside
			// ParallelGroup.Go's recover (the TPC-DS Q8 post-mortem above).
			// Width() is the descriptor's width, not the deform watermark, so
			// the comparison is the same one every other arm makes.
			if colIdx < 0 || colIdx >= s.Width() {
				return evalFastColumnRefErr(n, slot, ctx, colIdx, s.Width())
			}
			d := s.Get(colIdx)
			checkDeformPoison(d)
			return d, nil
		case rowSlotView:
			if colIdx < 0 || colIdx >= len(s) {
				return evalFastColumnRefErr(n, slot, ctx, colIdx, len(s))
			}
			d := s.Get(colIdx)
			checkDeformPoison(d)
			return d, nil
		case nil:
			return evalFastColumnRefErr(n, slot, ctx, colIdx, 0)
		}
		// A SlotView implementation this switch does not know: no width is
		// reachable, so behave as the pre-PS6.1 arm did.
		d := slot.Get(colIdx)
		checkDeformPoison(d)
		return d, nil

	case ExprOuterColumnRef, ExprExecParamRef:
		if p := fastRefOperand(n, ctx); p != nil {
			checkDeformPoison(*p)
			return *p, nil
		}
		return evalExprSlot(n.orig, slot, ctx)

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
		// A comparison of two operands that live somewhere already — a
		// column of a materialized row, an outer row's column, a SubPlan
		// parameter, a folded constant — compares them where they lie, as
		// PG's EEOP_SCAN_VAR / EEOP_PARAM_EXEC steps hand int4eq the 8-byte
		// Datums in place. Here each operand is a 48-byte Datum, and the two
		// recursive calls below copied both out and back per row: in TPC-DS
		// Q1's SubPlan filter those copies were most of evalFastExpr's
		// samples (M0122-0012 slice 75). The conditions are exactly the ones
		// the general path reaches fastCompareDatum under (no literal
		// coercion, fastBinarySimple, two non-NULL values); anything else
		// falls through to it, and re-reading a pure operand there is free.
		// fastBinarySimple's static half is tested before any operand is
		// read: a character-string comparison never qualifies, and finding
		// that out per row cost TPC-DS Q81's `ctr1.ctr_state = ctr_state`
		// 9 % more instructions.
		if n.constVal == nil && op.IsComparison() && n.payload[16]&4 == 0 &&
			binary.LittleEndian.Uint64(n.payload[8:16]) == 0 {
			if lp := fastOperandPtr(exprs, n.childA, slot, ctx); lp != nil && !lp.IsNull() && lp.Kind != KindString {
				if rp := fastOperandPtr(exprs, n.childB, slot, ctx); rp != nil && !rp.IsNull() && fastBinarySimple(n, op, lp, rp) {
					checkDeformPoison(*lp)
					checkDeformPoison(*rp)
					if c, ok := fastCompareDatum(lp, rp); ok {
						return NewBoolDatum(cmpResult(op, c)), nil
					}
				}
			}
		}
		// A character-string comparison of two operands that live somewhere
		// already compares them in place when both are plain strings: that
		// is evalFastBinaryApply's plain-string branch (slice 30), reached
		// without copying four Datums through the two operand calls and the
		// apply. The general path gets there the same way — two strings fail
		// fastBinarySimple, and coerceUnknownLit replaces a literal only
		// opposite an operand of the coerced kind, which a string is not
		// when that kind is not KindString. TPC-DS Q2's and Q59's seven
		// `CASE WHEN d_day_name = '<day>'` arms per row went the long way
		// (M0122-0012 slice 89).
		if op.IsComparison() && n.payload[16]&4 != 0 && (n.constVal == nil || DatumKind(n.payload[17]) != KindString) {
			if lp := fastOperandPtr(exprs, n.childA, slot, ctx); lp != nil && lp.Kind == KindString {
				if rp := fastOperandPtr(exprs, n.childB, slot, ctx); rp != nil && rp.Kind == KindString {
					if ls, rs := stringValueInline(lp), stringValueInline(rp); plainCompareString(ls) && plainCompareString(rs) {
						if lbp, rbp := binary.LittleEndian.Uint32(n.payload[8:]), binary.LittleEndian.Uint32(n.payload[12:]); lbp > 0 || rbp > 0 {
							if lbp > 0 || n.payload[16]&1 != 0 {
								ls = bcTrimRight(ls)
							}
							if rbp > 0 || n.payload[16]&2 != 0 {
								rs = bcTrimRight(rs)
							}
						}
						return NewBoolDatum(cmpResult(op, strings.Compare(ls, rs))), nil
					}
				}
			}
		}
		left, err := evalFastExpr(exprs, n.childA, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// Short-circuit boolean operators before evaluating the right side,
		// matching evalExprSlot behaviour.
		//
		// The gate is `Kind == KindBool`, NOT `!IsNull()` (M0127-PS6.2). Datum
		// .BoolValue() is `Int != 0` on ANY Kind, so `!IsNull()` short-circuits
		// on non-boolean left operands too — and then RETURNS THAT OPERAND as
		// the result of the AND/OR. The interpreted twin cannot do that: it
		// declines to short-circuit and evalAnd/evalOr (expr.go) always yield
		// KindBool or NULL. Two ways that mattered: a KindString left operand
		// made `s AND x` evaluate to the string, and for an ARENA-backed string
		// Datum.Int is the mctx coordinate (offset<<32|length), so BoolValue()
		// — and hence which branch was taken — depended on the arena offset.
		// Found by the PS6.2 parity corpus (619 diffs, 2 root causes).
		switch op {
		case parser.OpAnd:
			if left.Kind == KindBool && !left.BoolValue() {
				return left, nil
			}
		case parser.OpOr:
			if left.Kind == KindBool && left.BoolValue() {
				return left, nil
			}
		}
		right, err := evalFastExpr(exprs, n.childB, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// AND / OR of two boolean (or NULL) operands is Kleene logic
		// applied here, as PG's EEOP_BOOL_AND_STEP / EEOP_BOOL_OR_STEP
		// steps do, instead of through evalBinary's dispatch: TPC-DS Q28's
		// six `ss_quantity BETWEEN … AND (… OR … OR …)` scan quals called
		// evalAnd / evalOr per row through it (M0122-0012 slice 84).
		// evalBinary sends OpAnd / OpOr straight to evalAnd / evalOr, and
		// for bool / NULL operands nothing on the general path (literal
		// coercion, bpchar, pg_lsn) applies, so the result is the same.
		if (op == parser.OpAnd || op == parser.OpOr) && n.constVal == nil &&
			(left.Kind == KindBool || left.IsNull()) && (right.Kind == KindBool || right.IsNull()) {
			if op == parser.OpAnd {
				return evalAnd(left, right), nil
			}
			return evalOr(left, right), nil
		}
		if n.constVal != nil {
			coerceUnknownLit(n, &left, &right)
		}
		// The common shape is evaluated inline: the shared function's call
		// cost was 14-26% of a per-row qual (M0122-0012 slice 7). When
		// fastBinarySimple holds, every preamble branch of
		// evalFastBinaryApply is a no-op, and what is left is exactly this.
		if fastBinarySimple(n, op, &left, &right) {
			// M0122-0012 slice 27: a comparison of two non-NULL values of
			// one kind compares them in place (fastCompareDatum), skipping
			// evalBinary's dispatch and compareDatum's cross-kind
			// promotion — and, for date/timestamp, the time.Time each side
			// was built into. Anything else takes evalBinary below.
			if op.IsComparison() && !left.IsNull() && !right.IsNull() {
				if c, ok := fastCompareDatum(&left, &right); ok {
					return NewBoolDatum(cmpResult(op, c)), nil
				}
			}
			result, err := evalBinary(op, left, right, int(int32(binary.LittleEndian.Uint32(n.payload[4:]))), ctx)
			if err != nil {
				return Datum{}, err
			}
			if err := fastIntOverflow(n, &result); err != nil {
				return Datum{}, err
			}
			return result, nil
		}
		var result Datum
		err = evalFastBinaryApply(n, op, &left, &right, ctx, &result)
		return result, err

	case ExprUnaryOp:
		if n.childA == noExpr {
			return Datum{}, fmt.Errorf("executor: evalFastExpr: UnaryOp node %d missing child", idx)
		}
		op := parser.OpCode(n.payload[0])
		operand, err := evalFastExpr(exprs, n.childA, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalUnary(op, operand, int(int32(binary.LittleEndian.Uint32(n.payload[4:]))))

	case ExprAdapter:
		return evalExprSlot(n.orig, slot, ctx)

	case ExprNullTest:
		// evalExprSlot's scalar IsNullExpr arm: the test never yields NULL.
		operand, err := evalFastExpr(exprs, n.childA, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return NewBoolDatum(operand.IsNull() != (n.payload[0] == 1)), nil

	case ExprCase:
		return evalFastCase(exprs, n.caseInfo, slot, ctx)

	case ExprCoalesce:
		// evalFuncCall's "coalesce" arm: the first non-NULL argument, an
		// argument's error raised, the rest left unevaluated.
		v, err := evalFastExpr(exprs, n.childA, slot, ctx)
		if err != nil || !v.IsNull() {
			return v, err
		}
		return evalFastExpr(exprs, n.childB, slot, ctx)

	default:
		return Datum{}, fmt.Errorf("executor: evalFastExpr: unknown ExprKind %d at idx %d", n.Kind, idx)
	}
}

// evalFastBinaryApply is the BinaryOp arm's work once both operands are in
// hand: the character-string comparison, pg_lsn arithmetic, the bpchar and
// concat operand coercions, evalBinary / evalConcat and the int2/int4
// overflow checks. The per-row arm and evalFastExprBatch both call it, so the
// two agree element for element by construction (M0122-0012).
func evalFastBinaryApply(n *ExprNode, op parser.OpCode, lp, rp *Datum, ctx *Context, out *Datum) error {
	var err error
	left, right := *lp, *rp
	// The batch evaluator reaches here without the per-row arm's
	// coercion; once the literal is coerced, the comparison is the per-row
	// arm's inline fastCompareDatum case.
	if n.constVal != nil && coerceUnknownLit(n, &left, &right) && !left.IsNull() && !right.IsNull() {
		if c, ok := fastCompareDatum(&left, &right); ok {
			*out = NewBoolDatum(cmpResult(op, c))
			return nil
		}
	}
	// The compiled position (payload[4:8]), not a literal 0: every error
	// these three helpers raise is stamped with it on the interpreted twin.
	// M0127-PS6.2.
	pos := int(int32(binary.LittleEndian.Uint32(n.payload[4:])))
	// M0122-0012 slice 30: a comparison of two plain strings compares them
	// in place. compareDatum's string arm sniffs values — pg_lsn shapes,
	// UUIDs, row and array literals — before falling back to
	// strings.Compare, and the pg_lsn check below runs first too; when
	// neither string can match any sniff (plainCompareString) every one of
	// those branches is skipped, so the result is strings.Compare of the
	// bpchar-normalised operands, exactly what evalBinary would return.
	// TPC-H Q10's `l_returnflag = 'R'` took the whole path per row.
	// M0122-0012 slice 64: the plain pair is decided before the
	// character-string ambiguity test below — two plain strings are never
	// textShapeAmbiguous (a uuid needs 32+ bytes, a pg_lsn a '/', a row or
	// array a leading '(' / '{'), so for them that test, two isValidUUIDStr
	// scans and a pg_lsn sniff per row, could only answer false. TPC-DS
	// Q59's seven char(9) `d_day_name = 'Sunday'` arms paid it per row.
	if op.IsComparison() && left.Kind == KindString && right.Kind == KindString {
		if ls, rs := stringValueInline(&left), stringValueInline(&right); plainCompareString(ls) && plainCompareString(rs) {
			// comparisonOperandsAsBpchar's rule on the strings themselves:
			// with either side bpchar-typed, a bpchar side or a bare
			// literal drops its trailing blanks.
			if lbp, rbp := int64(binary.LittleEndian.Uint32(n.payload[8:])), int64(binary.LittleEndian.Uint32(n.payload[12:])); lbp > 0 || rbp > 0 {
				if lbp > 0 || n.payload[16]&1 != 0 {
					ls = bcTrimRight(ls)
				}
				if rbp > 0 || n.payload[16]&2 != 0 {
					rs = bcTrimRight(rs)
				}
			}
			*out = NewBoolDatum(cmpResult(op, strings.Compare(ls, rs)))
			return nil
		}
	}
	if n.payload[16]&4 != 0 && textShapeAmbiguous(left, right) {
		*out = binaryTextComparison(op, left, right,
			int64(binary.LittleEndian.Uint32(n.payload[8:])), int64(binary.LittleEndian.Uint32(n.payload[12:])),
			n.payload[16]&1 != 0, n.payload[16]&2 != 0)
		return nil
	}
	// pg_lsn arithmetic: detect before evalBinary (mirrors evalExprSlot). M0097-pg_lsn.
	if (left.Kind == KindString && looksLikePgLSN(left.StringValue())) ||
		(right.Kind == KindString && looksLikePgLSN(right.StringValue())) {
		res, handled, lsnErr := evalPgLSNBinary(op, left, right, pos)
		if lsnErr != nil {
			return lsnErr
		}
		if handled {
			*out = res
			return nil
		}
	}
	lbp := int64(binary.LittleEndian.Uint32(n.payload[8:]))
	rbp := int64(binary.LittleEndian.Uint32(n.payload[12:]))
	// M0122-0012: both helpers return their operands unchanged unless
	// their own preconditions hold (a concat; a bpchar-typed side), so
	// those preconditions are tested here first. A scan qual's numeric
	// or integer comparison then skips two calls that each copy four
	// Datums: about 9% of a filtered TPC-DS store_sales scan.
	if op == parser.OpConcat {
		left, right = concatOperandsAsText(op, left, right, lbp, rbp)
	}
	if lbp > 0 || rbp > 0 {
		left, right = comparisonOperandsAsBpchar(op, left, right, lbp, rbp,
			n.payload[16]&1 != 0, n.payload[16]&2 != 0)
	}
	var result Datum
	if op == parser.OpConcat {
		mode := concatGuess
		switch {
		case n.payload[16]&8 != 0:
			mode = concatText
		case n.payload[16]&16 != 0:
			mode = concatJSONB
		case n.payload[16]&32 != 0:
			mode = concatArrayCat
		case n.payload[16]&64 != 0:
			mode = concatArrayAppend
		case n.payload[16]&128 != 0:
			mode = concatArrayPrepend
		}
		result, err = evalConcat(left, right, pos, ctx, mode)
	} else {
		result, err = evalBinary(op, left, right, pos, ctx)
	}
	if err != nil {
		return err
	}
	// Integer-overflow check, mirroring evalExprSlot. The fast path
	// previously skipped this, silently returning out-of-range int2/int4
	// arithmetic results. M0097 regression fix.
	//
	// No Pos on either raise site: this mirrors expr.go's evalExprSlot
	// BinaryOp overflow check (interpreted twin) — int2pl/int4pl etc.
	// (postgres/src/backend/utils/adt/int.c) never call errposition, so
	// PG never attaches LINE-context to a runtime arithmetic overflow
	// regardless of whether an operand was a literal or a column.
	// M0134-0070; the interpreted twin was fixed in the same round —
	// leaving only one twin Pos-less would silently reintroduce the
	// sibling divergence M0127-PS6.2 fixed.
	if err := fastIntOverflow(n, &result); err != nil {
		return err
	}
	*out = result
	return nil
}

// fastOperandPtr returns where the value of node idx already lies, or nil when
// it must be evaluated: an in-range column of a *MaterializedSlot, an in-range
// outer-row column or a set SubPlan parameter (fastRefOperand), or a folded
// constant. Every value it returns is the one evalFastExpr would return for
// the node; a nil leaves the node to evalFastExpr, errors included.
func fastOperandPtr(exprs exprTreeSlab, idx int32, slot SlotView, ctx *Context) *Datum {
	if idx == noExpr {
		return nil
	}
	n := &exprs[idx]
	switch n.Kind {
	case ExprColumnRef:
		if s, ok := slot.(*MaterializedSlot); ok {
			colIdx := int(int32(binary.LittleEndian.Uint32(n.payload[:])))
			if colIdx >= 0 && colIdx < len(s.row) {
				return &s.row[colIdx]
			}
		}
	case ExprOuterColumnRef, ExprExecParamRef:
		return fastRefOperand(n, ctx)
	case ExprConstVal:
		return n.constVal
	}
	return nil
}

// fastRefOperand is the in-range read of evalExprSlot's OuterColumnRef and
// ExecParamRef arms (expr.go), or nil wherever those arms raise an error —
// the caller then delegates to them so the error text is theirs.
func fastRefOperand(n *ExprNode, ctx *Context) *Datum {
	if ctx == nil {
		return nil
	}
	if n.Kind == ExprExecParamRef {
		id := int(int32(binary.LittleEndian.Uint32(n.payload[0:])))
		if id < 0 || id >= len(ctx.ParamExec) || !ctx.ParamSet[id] {
			return nil
		}
		return &ctx.ParamExec[id]
	}
	level := int(int32(binary.LittleEndian.Uint32(n.payload[0:])))
	i := len(ctx.OuterRows) - level
	if i < 0 || i >= len(ctx.OuterRows) {
		return nil
	}
	outer := ctx.OuterRows[i]
	col := int(int32(binary.LittleEndian.Uint32(n.payload[4:])))
	if col < 0 || col >= len(outer) {
		return nil
	}
	return &outer[col]
}

// fastBinarySimple reports that every preamble branch of evalFastBinaryApply
// is a no-op for these operands: no ambiguous character-string comparison,
// no string operand (the pg_lsn probe), not a concat, no bpchar-typed side.
// The BinaryOp result is then evalBinary's plus fastIntOverflow, which is
// what the per-row arm computes inline.
func fastBinarySimple(n *ExprNode, op parser.OpCode, left, right *Datum) bool {
	return n.payload[16]&4 == 0 && left.Kind != KindString && right.Kind != KindString &&
		op != parser.OpConcat && binary.LittleEndian.Uint64(n.payload[8:16]) == 0
}

// plainCompareString reports that s can match none of the value sniffs a
// string comparison makes (compareDatum's KindString arm and
// evalFastBinaryApply's pg_lsn check): looksLikePgLSN needs a '/',
// isValidUUIDStr accepts only 32, 36 or 38 bytes, and the row / array
// comparisons need a leading '(' or '{'. Trimming trailing blanks (the bpchar
// normalisation) cannot make a plain string unplain.
//
// The '/' test is a byte loop, not strings.IndexByte: the string is under 32
// bytes here, and the call into IndexByte's vector routine cost more than the
// scan — 26 % of TPC-DS Q81, whose correlated subplan compares two-letter
// state codes per inner row (M0122-0012 slice 90). The loop lets the function
// inline.
func plainCompareString(s string) bool {
	if len(s) >= 32 || (len(s) > 0 && (s[0] == '(' || s[0] == '{')) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return false
		}
	}
	return true
}

// stringValueInline is d.StringValue() with the owned-buffer arm inlined
// at the caller: StringValue's arena arm puts it over the inlining budget, and
// the plain-string compares call it twice per comparison (M0122-0012
// slice 90). An arena-backed string takes StringValue itself.
func stringValueInline(d *Datum) string {
	if d.ArenaID != 0 {
		return d.StringValue()
	}
	// unsafe.String(nil, 0) is "", StringValue's answer for an empty Buf.
	return unsafe.String(unsafe.SliceData(d.Buf), len(d.Buf))
}

// fastCompareDatum is compareDatum for the kind pairs whose ordering is a
// plain integer compare, or ok=false. Each arm is exactly the arm compareDatum
// reaches for that pair:
//   - int / int: promoteCrossKind leaves equal kinds alone, and the KindInt
//     arm compares Int;
//   - time / time, neither a timetz (Scale == 0): the KindTime arm compares
//     the instants, and Int is the instant's UnixNano — the ±infinity
//     sentinels sit at the int64 extremes, so they order the same way;
//   - numeric / numeric, both on the int64 lane: numericCmp, as compareDatum
//     calls it.
func fastCompareDatum(a, b *Datum) (int, bool) {
	if a.Kind != b.Kind {
		// numeric / int on the int64 lane: compareDatum's NUMERIC ↔ INT arm
		// (promoteCrossKind leaves the pair alone, the int becomes
		// numericFromInt, numericCmp compares) — TPC-DS Q28's
		// `ss_list_price BETWEEN 107 AND 117` compared through evalBinary
		// per row (M0122-0012 slice 84).
		switch {
		case a.Kind == KindNumeric && b.Kind == KindInt && !isBigNumeric(*a):
			c, err := numericCmp(*a, numericFromInt(b.Int))
			return c, err == nil
		case a.Kind == KindInt && b.Kind == KindNumeric && !isBigNumeric(*b):
			c, err := numericCmp(numericFromInt(a.Int), *b)
			return c, err == nil
		}
		return 0, false
	}
	switch a.Kind {
	case KindInt:
	case KindTime:
		if a.Scale != 0 || b.Scale != 0 {
			return 0, false
		}
	case KindNumeric:
		if isBigNumeric(*a) || isBigNumeric(*b) {
			return 0, false
		}
		c, err := numericCmp(*a, *b)
		return c, err == nil
	default:
		return 0, false
	}
	switch {
	case a.Int < b.Int:
		return -1, true
	case a.Int > b.Int:
		return 1, true
	}
	return 0, true
}

// fastIntOverflow is the int2/int4 overflow check shared by the per-row
// BinaryOp arm and evalFastBinaryApply.
//
// No Pos on either raise site: this mirrors expr.go's evalExprSlot BinaryOp
// overflow check (the interpreted twin). int2pl/int4pl etc.
// (postgres/src/backend/utils/adt/int.c) never call errposition, so PG never
// attaches LINE context to a runtime arithmetic overflow, whether an operand
// was a literal or a column. M0134-0070.
func fastIntOverflow(n *ExprNode, result *Datum) error {
	if result.Kind == KindInt {
		switch n.payload[1] {
		case ovfInt2:
			if result.Int < -32768 || result.Int > 32767 {
				return &ExecError{Code: "22003", Message: "smallint out of range"}
			}
		case ovfInt4:
			if result.Int < -2147483648 || result.Int > 2147483647 {
				return &ExecError{Code: "22003", Message: "integer out of range"}
			}
		}
	}
	return nil
}

// exprFoldable reports whether e can be evaluated once at build time and
// replaced by its value — i.e. it reads no column and is deterministic.
//
// This is a WHITELIST, and the direction is deliberate. An expression kind it
// does not name is simply not folded, costing performance; a kind wrongly
// admitted would be evaluated against the wrong row or at the wrong time,
// costing correctness. goopg has a documented history of expression walkers
// that silently miss an arm (internal/optimizer/exprwalk_inventory_test.go),
// so this one is built to miss safely.
//
// Excluded on purpose:
//
//   - ColumnRef / OuterColumnRef — read a row, which is the whole point.
//   - ParamRef / ExecParamRef — bound per execution, not per plan.
//   - FuncCall — volatility is not resolved here, so now(), random() and any
//     user function are all excluded wholesale rather than judged individually.
//   - SubqueryExpr / ExistsExpr / InExpr / ArraySubqueryExpr — open a plan.
//   - CTIDExpr / TableOidExpr / MergeActionExpr / MergeWholeRowRef / RowExpr —
//     read the tuple or row as a whole.
//   - CollateExpr / ExtractExpr / CaseExpr / LikeEscapePattern — plausible but
//     left out rather than reasoned about loosely; they are not on any hot
//     path this targets.
//
// Note the *values* of the literal kinds below can depend on session GUCs
// (a timestamptz TypedStringLit reads TimeZone). That is safe because folding
// happens in the operator's Open against the same Context that will evaluate
// the rows, and a re-Open rebuilds the slab.
func exprFoldable(e optimizer.Expr) bool {
	switch x := e.(type) {
	case nil:
		return false

	case *optimizer.IntegerConst, *optimizer.StringConst, *optimizer.NumericConst,
		*optimizer.TypedStringLit, *optimizer.IntervalLit, *optimizer.NullConst,
		*optimizer.BooleanConst:
		return true

	case *optimizer.BinaryOp:
		return exprFoldable(x.Left) && exprFoldable(x.Right)
	case *optimizer.UnaryOp:
		return exprFoldable(x.Operand)
	case *optimizer.CastExpr:
		return exprFoldable(x.Operand)
	case *optimizer.IsNullExpr:
		return exprFoldable(x.Operand)
	case *optimizer.IsBoolExpr:
		return exprFoldable(x.Operand)
	case *optimizer.IsDistinctFromExpr:
		return exprFoldable(x.Left) && exprFoldable(x.Right)

	default:
		return false
	}
}

// foldConstant evaluates a column-free subtree once and reports whether the
// result is safe to freeze into the plan.
//
// It declines in two cases, and the second is the one that matters:
//
//  1. The evaluation ERRORS. Folding a failing constant would move the error
//     from row time to build time, changing when the statement fails and
//     whether earlier rows were already returned. Left uncompiled, it raises
//     per row exactly as before.
//
//  2. The value DEPENDS ON SESSION SETTINGS. This is a parallel-only
//     wrong-answer hazard and it is not hypothetical: gatherOp Opens the
//     leader's child with the session Context but each worker with a
//     NewWorkerContext whose GetSetting is deliberately nil
//     (parallel_worker_ctx.go). timeZoneFromCtx returns "" for such a
//     Context, so a zone-less TIMESTAMPTZ literal reads as the session
//     TimeZone in the leader and as the default zone in every worker — and
//     folding would freeze those two DIFFERENT constants into two plans
//     evaluating the same query.
//
// Rather than enumerate which types consult the session — a list that would
// drift — the check is self-verifying: evaluate once with the caller's
// Context and once with a settings-blind copy of it, which is precisely what
// a worker sees. Fold only if both agree. Anything session-sensitive
// disagrees (or errors) and is declined.
//
// This mirrors the rule evalTypedStringLit already applies to its own cache:
// "when usedSession fired … it must not be written to x.CachedTime /
// x.CacheValid, or a different session with a different TimeZone reusing this
// plan node would get the wrong instant" (expr.go, M0134-0026).
func foldConstant(e optimizer.Expr, ctx *Context) (Datum, bool) {
	// nil slot: exprFoldable has already established there is no ColumnRef to
	// read, so no row is needed.
	withSession, err := evalExprSlot(e, nil, ctx)
	if err != nil {
		return Datum{}, false
	}
	blind := *ctx
	blind.GetSetting = nil
	blindVal, berr := evalExprSlot(e, nil, &blind)
	if berr != nil {
		return Datum{}, false
	}
	if !foldedValuesIdentical(withSession, blindVal) {
		return Datum{}, false
	}
	return withSession, true
}

// foldedValuesIdentical compares two trial evaluations of the same constant.
//
// It compares the RAW representation, not Format(). Format() is display
// oriented and renders a KindTime as wall-clock text, which hides exactly the
// difference this function exists to find: a zone-less TIMESTAMPTZ read in
// Asia/Tokyo and the same literal read with no session both print
// "2026-08-30 12:00:00" while denoting instants nine hours apart. Comparing
// rendered text would have silently declared them identical and folded the
// wrong constant into every parallel worker.
func foldedValuesIdentical(a, b Datum) bool {
	if a.Kind != b.Kind || a.IsNull() != b.IsNull() {
		return false
	}
	if a.IsNull() {
		return true
	}
	// Int carries the instant for KindTime, the mantissa for KindNumeric, and
	// the arena coordinate for arena-backed strings; TimeSub distinguishes
	// date / timestamp / timestamptz; Scale and Flags complete the scalar form.
	if a.Int != b.Int || a.Scale != b.Scale || a.TimeSub != b.TimeSub || a.Flags != b.Flags {
		return false
	}
	// Payload-bearing kinds: compare the bytes a consumer would read.
	switch a.Kind {
	case KindString, KindBytes:
		return a.StringValue() == b.StringValue()
	}
	return true
}

// exprCaseInfo is a compiled CASE: the slab indices of its operand (noExpr
// for the searched form), its WHEN/THEN pairs and its ELSE (noExpr when
// absent), plus the declared-type facts the simple form's implicit `=` needs
// — compiled here because evaluation only sees Datums (the same facts
// evalCaseExpr reads from the expressions).
type exprCaseInfo struct {
	operand      int32
	whens, thens []int32
	els          int32
	opBP         int64
	opLit        bool
	whenBP       []int64
	whenLit      []bool
}

// evalFastCase is evalCaseExpr's compiled twin and must agree with it arm
// for arm: the operand is evaluated once; each WHEN in order is a boolean
// predicate (searched form) or `operand = WHEN` resolved through
// comparisonOperandsAsBpchar and compareEq (simple form), a NULL or non-bool
// result counting as no match; the first match's THEN is the result, else the
// ELSE, else NULL. Only the matched THEN (or the ELSE) is evaluated.
func evalFastCase(exprs exprTreeSlab, ci *exprCaseInfo, slot SlotView, ctx *Context) (Datum, error) {
	var operand Datum
	hasOperand := ci.operand != noExpr
	if hasOperand {
		v, err := evalFastExpr(exprs, ci.operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		operand = v
	}
	for i, w := range ci.whens {
		whenVal, err := evalFastExpr(exprs, w, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		var matched bool
		if hasOperand {
			o, vv := comparisonOperandsAsBpchar(parser.OpEq, operand, whenVal,
				ci.opBP, ci.whenBP[i], ci.opLit, ci.whenLit[i])
			eq, err := compareEq(o, vv)
			if err != nil {
				return Datum{}, err
			}
			matched = eq.Kind == KindBool && eq.BoolValue()
		} else {
			matched = whenVal.Kind == KindBool && whenVal.BoolValue()
		}
		if matched {
			return evalFastExpr(exprs, ci.thens[i], slot, ctx)
		}
	}
	if ci.els != noExpr {
		return evalFastExpr(exprs, ci.els, slot, ctx)
	}
	return NullDatum, nil
}
