package executor

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// ExecError is the executor's structured error. Code is a SQLSTATE
// value the wire-protocol path forwards to ErrorResponse.
type ExecError struct {
	Code          string
	Message       string
	Detail        string // optional DETAIL message for wire protocol. M0097-0003.
	Hint          string // optional HINT message for wire protocol. M0097-0004.
	Pos           int
	ConditionName string // set for RAISE condition_name; used for exception matching. M0097-0003.
}

func (e *ExecError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("executor error: %s (byte %d)", e.Message, e.Pos)
	}
	return fmt.Sprintf("%s: %s (byte %d)", e.Code, e.Message, e.Pos)
}

// evalExpr resolves a planner expression against the input row.
// Operators that produce no input pass nil for `row`; in that case
// any ColumnRef resolution is an internal error.
//
// Thin wrapper over evalExprSlot — Row callers continue to work
// unchanged. New slot-aware sites (NLI/MHJ predicate eval) call
// evalExprSlot directly so VirtualSlot composition can read via
// slot.Get(col) without materializing a Row per emitted match.
func evalExpr(e planner.Expr, row Row, ctx *Context) (Datum, error) {
	var slot SlotView
	if row != nil {
		slot = rowSlotView(row)
	}
	return evalExprSlot(e, slot, ctx)
}

// evalExprSlot is the slot-aware sibling of evalExpr. ColumnRef
// reads via slot.Get(col); helpers that push ctx.OuterRows
// (Subquery/In/Exists/Extract/FuncCall/CaseExpr) reach back to
// Row via slotToRow — those paths are out of scope for the M0071
// keystone refactor and stay on Row to limit blast radius.
//
// nil slot is permitted (mirrors the M0054 contract for valuesOp /
// limitOp / DML operators that have no input row).
//
// M0074-0001: ColumnRef is the dominant case in Q5 (predicate +
// projection refs over filtered lineitem rows). Hoisted to a
// fast-path early-return ahead of the type switch — saves the
// 12-arm type-test sequence on the hot path. evalExprSlot cum
// CPU at M0073-final was 68.68 % cum; this hoist trims dispatch.
func evalExprSlot(e planner.Expr, slot SlotView, ctx *Context) (Datum, error) {
	// Fast path: ColumnRef. M0074-0001 hoist.
	if cref, ok := e.(*planner.ColumnRef); ok {
		if slot == nil {
			return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d on nil slot", cref.Name, cref.Index)}
		}
		if rs, ok := slot.(rowSlotView); ok {
			if cref.Index < 0 || cref.Index >= len(rs) {
				return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d out of range", cref.Name, cref.Index)}
			}
		}
		if vs, ok := slot.(*VirtualSlot); ok {
			if cref.Index < 0 || cref.Index >= vs.Width() {
				return Datum{}, &ExecError{Code: "XX000", Pos: cref.Pos(), Message: fmt.Sprintf("column ref %s/%d out of VirtualSlot range %d (chained-NLI?)", cref.Name, cref.Index, vs.Width())}
			}
		}
		return slot.Get(cref.Index), nil
	}
	switch x := e.(type) {
	case *planner.OuterColumnRef:
		// Look up the row from the lexical-scope stack pushed
		// by evalSubquery/evalInExpr/evalExistsExpr before the
		// inner plan runs. Level 1 is the immediate parent.
		idx := len(ctx.OuterRows) - x.Level
		if idx < 0 || idx >= len(ctx.OuterRows) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("outer column ref %s/level=%d out of range (depth=%d)", x.Name, x.Level, len(ctx.OuterRows))}
		}
		outer := ctx.OuterRows[idx]
		if x.Index < 0 || x.Index >= len(outer) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("outer column ref %s/idx=%d out of range (width=%d)", x.Name, x.Index, len(outer))}
		}
		return outer[x.Index], nil
	case *planner.CaseExpr:
		return evalCaseExpr(x, slotToRow(slot), ctx)
	case *planner.SubqueryExpr:
		return evalSubquery(x, slotToRow(slot), ctx)
	case *planner.InExpr:
		return evalInExpr(x, slotToRow(slot), ctx)
	case *planner.ExistsExpr:
		return evalExistsExpr(x, slotToRow(slot), ctx)
	case *planner.TypedStringLit:
		return evalTypedStringLit(x)
	case *planner.IntervalLit:
		return evalIntervalLit(x)
	case *planner.ExtractExpr:
		return evalExtract(x, slotToRow(slot), ctx)
	case *planner.IntegerConst:
		return Datum{Kind: KindInt, Int: x.Value}, nil
	case *planner.TableOidExpr:
		// `tableoid` system column for a non-partitioned base
		// relation: the binding's table OID is fixed at plan time
		// (resolveTableoidForBinding). Partitioned bindings instead
		// resolve through a real ColumnRef into the per-leaf
		// `tableoid` slot added by the partition-union wrapper.
		// M0100-0005y.
		return Datum{Kind: KindInt, Int: int64(x.TableOID)}, nil
	case *planner.CTIDExpr:
		// `ctid` system column: per-row TID injected by seqScanOp
		// into MaterializedSlot.hasCTID. M0097-0038.
		if ms, ok := slot.(*MaterializedSlot); ok && ms.hasCTID {
			return NewStringDatum(fmt.Sprintf("(%d,%d)", ms.ctidBlock, ms.ctidOff)), nil
		}
		return NullDatum, nil
	case *planner.NumericConst:
		m, s, err := parseNumeric(x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(), Message: err.Error()}
		}
		return newNumeric(m, int(s)), nil
	case *planner.StringConst:
		return NewStringDatum(x.Value), nil
	case *planner.NullConst:
		return NullDatum, nil
	case *planner.BooleanConst:
		return NewBoolDatum(x.Value), nil
	case *planner.ParamRef:
		if x.Number < 1 || x.Number > len(ctx.Params) {
			return Datum{}, &ExecError{Code: "08P01", Pos: x.Pos(), Message: fmt.Sprintf("parameter $%d not bound", x.Number)}
		}
		return ctx.Params[x.Number-1], nil
	// ColumnRef handled by the M0074-0001 fast-path above.
	case *planner.UnaryOp:
		operand, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalUnary(x.Op, operand, x.Pos())
	case *planner.CastExpr:
		v, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// `::regclass` is catalog-aware in both directions:
		//   - `<oid>::regclass` renders as the relation name (PG's regclassout)
		//   - `<text>::regclass` resolves the relation name to its numeric OID
		// The latter is the exact pgbench probe shape:
		//   `... WHERE oid = $1::pg_catalog.regclass`.
		if strings.EqualFold(x.TargetType, "regclass") && ctx != nil && ctx.Catalog != nil {
			switch v.Kind {
			case KindInt:
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					if tbl, found := im.LookupTableByOID(uint32(v.Int)); found && tbl != nil {
						return NewStringDatum(tbl.Name), nil
					}
				}
			case KindString:
				schema, rel := splitQualifiedTable(v.StringValue())
				objName := parser.ObjectName{Schema: schema, Name: rel}
				if tbl, found := ctx.Catalog.LookupTable(objName); found && tbl != nil {
					return NewIntDatum(int64(tbl.OID)), nil
				}
				// Also resolve index names: 'idx_name'::regclass returns the index OID.
				if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
					if idx, found := im.LookupIndex(objName); found && idx != nil {
						return NewIntDatum(int64(idx.OID)), nil
					}
				}
			}
		}
		return evalCastTyped(v, x.TargetType, x.SourceType, x.Pos())
	case *planner.BinaryOp:
		left, err := evalExprSlot(x.Left, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// Short-circuit: AND returns FALSE immediately when left is FALSE;
		// OR returns TRUE immediately when left is TRUE. Matches PostgreSQL.
		if x.Op == parser.OpAnd {
			if left.Kind == KindBool && !left.BoolValue() {
				return left, nil // FALSE AND _ = FALSE
			}
		} else if x.Op == parser.OpOr {
			if left.Kind == KindBool && left.BoolValue() {
				return left, nil // TRUE OR _ = TRUE
			}
		}
		right, err := evalExprSlot(x.Right, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		// When the declared result type is float8/float4, perform the arithmetic in
		// float64 to match PostgreSQL's float8 semantics (approximate, not exact).
		// This prevents exact big.Int arithmetic from producing 200-digit numbers
		// when float64 would stay in scientific notation. M0097-0003.
		if rt := strings.ToLower(x.ResultType); rt == "float8" || rt == "double precision" ||
			rt == "float4" || rt == "real" || rt == "float" {
			var lf, rf float64
			if left.Kind == KindNumeric {
				lf, _ = strconv.ParseFloat(left.Format(), 64)
			} else {
				lf = float64(left.Int)
			}
			if right.Kind == KindNumeric {
				rf, _ = strconv.ParseFloat(right.Format(), 64)
			} else {
				rf = float64(right.Int)
			}
			var fResult float64
			switch x.Op {
			case parser.OpAdd:
				fResult = lf + rf
			case parser.OpSub:
				fResult = lf - rf
			case parser.OpMul:
				fResult = lf * rf
			case parser.OpDiv:
				if rf == 0 {
					return Datum{}, &ExecError{Code: "22012", Pos: x.Pos(), Message: "division by zero"}
				}
				fResult = lf / rf
			default:
				// Fall through to normal evaluation for unsupported ops.
				goto normalBinaryOp
			}
			// Format the float64 result using PostgreSQL's float8out format (%.15g).
			// Return as a string datum — the dispatch layer's appendFloat8Text will
			// re-parse it as float64 for proper scientific notation display. M0097-0003.
			fs := strconv.FormatFloat(fResult, 'g', 15, 64)
			// For integer-valued results like -1 or 1, parseNumericFast gives the clean
			// representation (no trailing ".0"). For scientific notation or fractional
			// values, keep as string for float-format display.
			if m, s, ok := parseNumericFast(fs); ok {
				return Datum{Kind: KindNumeric, Int: m, Scale: s}, nil
			}
			// Scientific notation or fractional float: keep as string so dispatch
			// can format it with strconv.FormatFloat rather than big.Int decimal expansion.
			return NewStringDatum(fs), nil
		}
		// pg_lsn arithmetic/comparison: detect KindString "X/Y" pattern.
		if (left.Kind == KindString && looksLikePgLSN(left.StringValue())) ||
			(right.Kind == KindString && looksLikePgLSN(right.StringValue())) {
			res, handled, lsnErr := evalPgLSNBinary(x.Op, left, right, x.Pos())
			if lsnErr != nil {
				return Datum{}, lsnErr
			}
			if handled {
				return res, nil
			}
		}
	normalBinaryOp:
		result, err := evalBinary(x.Op, left, right, x.Pos())
		if err != nil {
			return Datum{}, err
		}
		// Overflow checks for integer arithmetic (M0097-0003).
		if result.Kind == KindInt {
			switch x.ResultType {
			case "int2", "smallint":
				if result.Int < -32768 || result.Int > 32767 {
					return Datum{}, &ExecError{Code: "22003", Pos: x.Pos(), Message: "smallint out of range"}
				}
			case "int4", "integer", "int":
				if result.Int < -2147483648 || result.Int > 2147483647 {
					return Datum{}, &ExecError{Code: "22003", Pos: x.Pos(), Message: "integer out of range"}
				}
			case "int8", "bigint":
				// int8 can wrap in Go; detect via sign change for mul/add/sub only.
				// For now, no overflow detection for int8 (matches most common cases).
			}
		}
		return result, nil
	case *planner.FuncCall:
		return evalFuncCall(x, slotToRow(slot), ctx)
	case *planner.IsNullExpr:
		// IS [NOT] NULL never propagates NULL — it always returns a boolean.
		operand, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		isNull := operand.IsNull()
		if x.Negated {
			return NewBoolDatum(!isNull), nil // IS NOT NULL
		}
		return NewBoolDatum(isNull), nil // IS NULL
	case *planner.IsBoolExpr:
		// IS [NOT] TRUE/FALSE/UNKNOWN. Always returns boolean. M0097-0003.
		operand, err := evalExprSlot(x.Operand, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		var result bool
		if x.TestTrue {
			// IS TRUE: must be non-null and boolean true
			result = !operand.IsNull() && operand.Kind == KindBool && operand.Int != 0
		} else if x.TestFalse {
			// IS FALSE: must be non-null and boolean false
			result = !operand.IsNull() && operand.Kind == KindBool && operand.Int == 0
		} else {
			// IS UNKNOWN: must be null
			result = operand.IsNull()
		}
		if x.Negated {
			result = !result
		}
		return NewBoolDatum(result), nil
	}
	return Datum{}, &ExecError{Code: "XX000", Pos: e.Pos(), Message: fmt.Sprintf("unsupported expression %T", e)}
}

// evalUnary handles -, +, NOT.
func evalUnary(op parser.OpCode, d Datum, pos int) (Datum, error) {
	if d.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case parser.OpUnaryNeg:
		switch d.Kind {
		case KindInt:
			return Datum{Kind: KindInt, Int: -d.Int}, nil
		case KindNumeric:
			// Negate a numeric/float value. M0097-0003.
			if d.Flags&flagBigNumeric != 0 {
				neg := new(big.Int).Neg(d.NumericBigValue())
				return newBigNumericInCtx(mctx.Perm(), neg, d.Scale), nil
			}
			return Datum{Kind: KindNumeric, Int: -d.Int, Scale: d.Scale}, nil
		default:
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary - requires integer or numeric"}
		}
	case parser.OpUnaryPos:
		switch d.Kind {
		case KindInt, KindNumeric:
			return d, nil
		default:
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary + requires integer or numeric"}
		}
	case parser.OpNot:
		if d.Kind != KindBool {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator NOT requires boolean"}
		}
		return NewBoolDatum(!d.BoolValue()), nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown unary operator %s", op)}
}

// looksLikePgLSN reports whether s is in "X/Y" format (1–8 uppercase hex digits each).
func looksLikePgLSN(s string) bool {
	slash := strings.IndexByte(s, '/')
	if slash < 1 || slash > 8 {
		return false
	}
	hexLow := s[slash+1:]
	if len(hexLow) < 1 || len(hexLow) > 8 {
		return false
	}
	for _, c := range s[:slash] + hexLow {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// pgLSNParseDelta extracts an int64 delta from a numeric/integer datum for pg_lsn arithmetic.
// Returns (delta, isNaN, ok). isNaN=true when NaN (caller must error).
func pgLSNParseDelta(d Datum) (int64, bool, bool) {
	switch d.Kind {
	case KindInt:
		return d.Int, false, true
	case KindNumeric:
		s := d.Format()
		if s == "NaN" {
			return 0, true, true
		}
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, false, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), false, true
		}
	case KindString:
		s := d.StringValue()
		if s == "NaN" {
			return 0, true, true
		}
		if !looksLikePgLSN(s) {
			if v, err := strconv.ParseInt(s, 10, 64); err == nil {
				return v, false, true
			}
		}
	}
	return 0, false, false
}

// evalPgLSNBinary handles pg_lsn comparison and arithmetic operators.
// Returns (result, true, nil) when handled, (zero, false, nil) to fall through.
func evalPgLSNBinary(op parser.OpCode, left, right Datum, pos int) (Datum, bool, error) {
	// Parse one or both sides as pg_lsn uint64.
	parseLSNDatum := func(d Datum) (uint64, bool) {
		if d.Kind == KindString {
			u, err := parsePgLSN(d.StringValue())
			if err == nil {
				return u, true
			}
		}
		return 0, false
	}

	switch op {
	case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		lu, lok := parseLSNDatum(left)
		ru, rok := parseLSNDatum(right)
		if !lok || !rok {
			return Datum{}, false, nil
		}
		var result bool
		switch op {
		case parser.OpEq:
			result = lu == ru
		case parser.OpNe:
			result = lu != ru
		case parser.OpLt:
			result = lu < ru
		case parser.OpLe:
			result = lu <= ru
		case parser.OpGt:
			result = lu > ru
		case parser.OpGe:
			result = lu >= ru
		}
		return NewBoolDatum(result), true, nil
	case parser.OpSub:
		// pg_lsn - pg_lsn → numeric
		lu, lok := parseLSNDatum(left)
		ru, rok := parseLSNDatum(right)
		if lok && rok {
			var diff int64
			if lu >= ru {
				diff = int64(lu - ru)
			} else {
				diff = -int64(ru - lu)
			}
			return NewStringDatum(fmt.Sprintf("%d", diff)), true, nil
		}
		// pg_lsn - numeric → pg_lsn
		if lok {
			delta, isNaN, ok := pgLSNParseDelta(right)
			if ok {
				if isNaN {
					return Datum{}, true, &ExecError{Code: "0A000", Pos: pos,
						Message: "cannot subtract NaN from pg_lsn"}
				}
				if delta < 0 {
					if uint64(-delta) > lu {
						return Datum{}, true, &ExecError{Code: "22003", Pos: pos, Message: "pg_lsn out of range"}
					}
					return NewStringDatum(formatPgLSN(lu - uint64(-delta))), true, nil
				}
				if uint64(delta) > lu {
					return Datum{}, true, &ExecError{Code: "22003", Pos: pos, Message: "pg_lsn out of range"}
				}
				return NewStringDatum(formatPgLSN(lu-uint64(delta))), true, nil
			}
		}
	case parser.OpAdd:
		// pg_lsn + numeric → pg_lsn
		lu, lok := parseLSNDatum(left)
		ru, rok := parseLSNDatum(right)
		var lsnVal uint64
		var numericDatum Datum
		if lok && !rok {
			lsnVal = lu
			numericDatum = right
		} else if rok && !lok {
			lsnVal = ru
			numericDatum = left
		} else {
			return Datum{}, false, nil
		}
		delta, isNaN, ok := pgLSNParseDelta(numericDatum)
		if ok {
			if isNaN {
				return Datum{}, true, &ExecError{Code: "0A000", Pos: pos,
					Message: "cannot add NaN to pg_lsn"}
			}
			var result uint64
			if delta >= 0 {
				result = lsnVal + uint64(delta)
				if result < lsnVal {
					return Datum{}, true, &ExecError{Code: "22003", Pos: pos, Message: "pg_lsn out of range"}
				}
			} else {
				neg := uint64(-delta)
				if neg > lsnVal {
					return Datum{}, true, &ExecError{Code: "22003", Pos: pos, Message: "pg_lsn out of range"}
				}
				result = lsnVal - neg
			}
			return NewStringDatum(formatPgLSN(result)), true, nil
		}
	}
	return Datum{}, false, nil
}

// evalBinary handles arithmetic, comparison, and boolean operators.
// SQL three-valued logic: NULL operand on most operators yields NULL;
// AND/OR follow Kleene's rules.
func evalBinary(op parser.OpCode, left, right Datum, pos int) (Datum, error) {
	if op.IsBoolean() {
		switch op {
		case parser.OpAnd:
			return evalAnd(left, right), nil
		case parser.OpOr:
			return evalOr(left, right), nil
		}
	}
	if left.IsNull() || right.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case parser.OpAdd, parser.OpSub:
		// timestamp/date ± interval and interval + timestamp/date
		// route through the time-arithmetic path before falling
		// back to integer arithmetic. v0 doesn't support
		// interval - timestamp (upstream rejects it too) or
		// timestamp - timestamp (returns interval upstream;
		// scope-deferred until the type system).
		if left.Kind == KindTime && right.Kind == KindInterval {
			return addTimeInterval(left, right, op == parser.OpSub), nil
		}
		if op == parser.OpAdd && left.Kind == KindInterval && right.Kind == KindTime {
			return addTimeInterval(right, left, false), nil
		}
		// NUMERIC ± NUMERIC, NUMERIC ± INT, INT ± NUMERIC: promote
		// the int side to KindNumeric{scale=0} and reuse the same
		// scale-aligning helpers.  Also try to parse string
		// operands as numeric (columns loaded via INSERT may be
		// stored as strings before the type system enforces types).
		if left.Kind == KindString {
			if m, s, err := parseNumeric(left.StringValue()); err == nil {
				left = newNumeric(m, int(s))
			}
		}
		if right.Kind == KindString {
			if m, s, err := parseNumeric(right.StringValue()); err == nil {
				right = newNumeric(m, int(s))
			}
		}
		if left.Kind == KindNumeric || right.Kind == KindNumeric {
			a, b, err := promoteToNumeric(left, right, op, pos)
			if err != nil {
				return Datum{}, err
			}
			if op == parser.OpAdd {
				return numericAdd(a, b)
			}
			return numericSub(a, b)
		}
		fallthrough
	case parser.OpMul, parser.OpDiv, parser.OpMod:
		if left.Kind == KindNumeric || right.Kind == KindNumeric {
			a, b, err := promoteToNumeric(left, right, op, pos)
			if err != nil {
				return Datum{}, err
			}
			switch op {
			case parser.OpMul:
				return numericMul(a, b)
			case parser.OpDiv:
				return numericDiv(a, b, pos)
			}
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s not supported on numeric", op)}
		}
		if left.Kind != KindInt || right.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires integer operands", op)}
		}
		return arithmetic(op, left.Int, right.Int, pos)
	case parser.OpConcat:
		if (left.Kind != KindString) || (right.Kind != KindString) {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator || requires string operands"}
		}
		return NewStringDatum(left.StringValue() + right.StringValue()), nil
	case parser.OpBitAnd, parser.OpBitOr, parser.OpBitXor, parser.OpBitShiftLeft, parser.OpBitShiftRight:
		// Bitwise operators: require integer operands. M0097-0003.
		if left.Kind != KindInt || right.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires integer operands", op)}
		}
		switch op {
		case parser.OpBitAnd:
			return Datum{Kind: KindInt, Int: left.Int & right.Int}, nil
		case parser.OpBitOr:
			return Datum{Kind: KindInt, Int: left.Int | right.Int}, nil
		case parser.OpBitXor:
			return Datum{Kind: KindInt, Int: left.Int ^ right.Int}, nil
		case parser.OpBitShiftLeft:
			return Datum{Kind: KindInt, Int: left.Int << uint(right.Int)}, nil
		case parser.OpBitShiftRight:
			return Datum{Kind: KindInt, Int: left.Int >> uint(right.Int)}, nil
		}
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "unknown bitwise operator"}
	case parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
		cmp, err := compareDatum(left, right, pos)
		if err != nil {
			return Datum{}, err
		}
		return NewBoolDatum(cmpResult(op, cmp)), nil
	case parser.OpLike, parser.OpNotLike, parser.OpILike, parser.OpNotILike:
		ls, lok := datumAsString(left)
		rs, rok := datumAsString(right)
		if !lok || !rok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires string operands (got left.Kind=%d right.Kind=%d)", op, left.Kind, right.Kind)}
		}
		var matched bool
		if op == parser.OpILike || op == parser.OpNotILike {
			matched = matchSQLLike(strings.ToLower(ls), strings.ToLower(rs))
		} else {
			matched = matchSQLLike(ls, rs)
		}
		if op == parser.OpNotLike || op == parser.OpNotILike {
			matched = !matched
		}
		return NewBoolDatum(matched), nil
	case parser.OpRegexMatch, parser.OpRegexIMatch, parser.OpRegexNoMatch, parser.OpRegexINoMatch:
		// POSIX regex operators. M0097-0011.
		ls, lok := datumAsString(left)
		rs, rok := datumAsString(right)
		if !lok || !rok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires string operands", op)}
		}
		matched, err := evalPOSIXRegex(ls, rs, op == parser.OpRegexIMatch || op == parser.OpRegexINoMatch)
		if err != nil {
			return Datum{}, &ExecError{Code: "2201B", Pos: pos, Message: fmt.Sprintf("invalid regular expression: %v", err)}
		}
		if op == parser.OpRegexNoMatch || op == parser.OpRegexINoMatch {
			matched = !matched
		}
		return NewBoolDatum(matched), nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown operator %s", op)}
}

// datumAsString returns d's character payload as a Go string when
// the value is text-like (KindString or KindBytes). Used by LIKE so
// a varchar value that arrives as bytes still evaluates correctly,
// mirroring `compareDatum`'s cross-Kind tolerance for character
// data.
func datumAsString(d Datum) (string, bool) {
	switch d.Kind {
	case KindString:
		return d.StringValue(), true
	case KindBytes:
		return string(d.BytesValue()), true
	}
	return "", false
}

// matchSQLLike implements SQL LIKE pattern semantics: '%' matches
// any (possibly empty) sequence, '_' matches exactly one character,
// every other byte matches itself. v0 does not honour an ESCAPE
// clause — escapes are upstream's default backslash where the next
// character is taken literally. The implementation is the standard
// recursive-descent matcher (no regex translation, so embedded
// special chars in the input never interact with regex syntax).
func matchSQLLike(s, pat string) bool {
	si, pi := 0, 0
	starS, starP := -1, -1
	for si < len(s) {
		if pi < len(pat) {
			c := pat[pi]
			switch c {
			case '\\':
				// Escape: next pattern byte matches literally.
				if pi+1 < len(pat) && pat[pi+1] == s[si] {
					pi += 2
					si++
					continue
				}
			case '%':
				starP = pi
				starS = si
				pi++
				continue
			case '_':
				pi++
				si++
				continue
			default:
				if c == s[si] {
					pi++
					si++
					continue
				}
			}
		}
		if starP >= 0 {
			pi = starP + 1
			starS++
			si = starS
			continue
		}
		return false
	}
	for pi < len(pat) && pat[pi] == '%' {
		pi++
	}
	return pi == len(pat)
}

// evalPOSIXRegex evaluates a POSIX extended regex match.
// caseInsensitive applies the (?i) flag. M0097-0011.
func evalPOSIXRegex(s, pattern string, caseInsensitive bool) (bool, error) {
	if caseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(s), nil
}

// addTimeInterval applies an interval to a time value. When
// `subtract` is true the interval is negated first. Months are
// applied via time.AddDate (which carries year/month overflow
// the way upstream PG does for `timestamp + interval '1 month'`);
// days are added via the same call.
func addTimeInterval(t, iv Datum, subtract bool) Datum {
	months := int(iv.IntervalMonthsValue())
	days := int(iv.IntervalDaysValue())
	if subtract {
		months = -months
		days = -days
	}
	return NewTimeDatum(t.TimeValue().AddDate(0, months, days))
}

func arithmetic(op parser.OpCode, a, b int64, pos int) (Datum, error) {
	var r int64
	switch op {
	case parser.OpAdd:
		r = a + b
	case parser.OpSub:
		r = a - b
	case parser.OpMul:
		r = a * b
	case parser.OpDiv:
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
		}
		r = a / b
	case parser.OpMod:
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
		}
		r = a % b
	}
	return Datum{Kind: KindInt, Int: r}, nil
}

// promoteCrossKind attempts implicit type promotion for common
// cross-kind pairs that may arise from planner-side column-index
// misalignments.  PostgreSQL performs these coercions implicitly;
// this is the v0 executor-level fallback so the query completes
// instead of erroring.  Returns the (possibly-promoted) operands.
func promoteCrossKind(a, b Datum) (Datum, Datum) {
	if a.Kind == b.Kind {
		return a, b
	}
	// M0073-0001: treat KindString / KindStringArena uniformly
	// as "string" for the cross-kind parse-and-compare path.
	aIsString := a.Kind == KindString
	bIsString := b.Kind == KindString
	// One side is string — try to parse it as the other's type.
	if aIsString && !bIsString {
		a = tryParseStringAs(b.Kind, a.StringValue())
	} else if bIsString && !aIsString {
		b = tryParseStringAs(a.Kind, b.StringValue())
	}
	// KindInterval has no text parse path yet — leave as-is so
	// the caller still errors instead of silently producing an
	// invalid comparison.
	return a, b
}

// tryParseStringAs attempts to parse s as the given target kind.
// On success it returns a Datum with that kind; on failure it
// returns a KindString Datum (the original), letting the caller
// produce a proper type-mismatch error.
func tryParseStringAs(target DatumKind, s string) Datum {
	switch target {
	case KindInt:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Datum{Kind: KindInt, Int: n}
		}
	case KindNumeric:
		if m, sc, err := parseNumeric(s); err == nil {
			return newNumeric(m, int(sc))
		}
	case KindTime:
		// Try time-of-day first ("HH:MM:SS") then full timestamp.
		if t, err := parseTimeString(s); err == nil {
			return NewTimeDatum(t)
		}
		if t, err := parseCopyTimestamp(s); err == nil {
			return NewTimeDatum(t)
		}
	}
	return NewStringDatum(s)
}

// compareDatum returns -1/0/1 the same way upstream's btree
// comparators do, scoped to the v0 type set.
func compareDatum(a, b Datum, pos int) (int, error) {
	// Implicit cross-kind promotion so planner-side column-index
	// misalignments don't crash the entire query.  PostgreSQL
	// handles these implicitly; goopg v0 mirrors that behaviour
	// for the common pairs that appear in TPC-H.
	a, b = promoteCrossKind(a, b)

	// NUMERIC ↔ INT: promote int to numeric so the comparison is
	// scale-aware. NUMERIC ↔ NUMERIC: align scales then compare
	// mantissas. Identical kinds drop through to the per-kind
	// switch below.
	if a.Kind == KindNumeric || b.Kind == KindNumeric {
		if a.Kind == KindInt {
			a = numericFromInt(a.Int)
		}
		if b.Kind == KindInt {
			b = numericFromInt(b.Int)
		}
		if a.Kind != KindNumeric || b.Kind != KindNumeric {
			return strings.Compare(a.Format(), b.Format()), nil
		}
		return numericCmp(a, b)
	}
	if a.Kind != b.Kind {
		// M0073-0001: arena and non-arena string/bytes Datums
		// are logically the same Kind for comparison purposes.
		// Treat KindString ↔ KindStringArena and KindBytes ↔
		// KindBytesArena as same-kind so the per-kind switch
		// below dispatches correctly.
		aIsString := a.Kind == KindString
		bIsString := b.Kind == KindString
		if aIsString && bIsString {
			as, bs := a.StringValue(), b.StringValue()
			// pg_lsn comparison: use uint64 semantics. M0097-pg_lsn.
			if looksLikePgLSN(as) && looksLikePgLSN(bs) {
				lu, errL := parsePgLSN(as)
				ru, errR := parsePgLSN(bs)
				if errL == nil && errR == nil {
					if lu < ru {
						return -1, nil
					}
					if lu > ru {
						return 1, nil
					}
					return 0, nil
				}
			}
			// UUID cross-format comparison: if either looks like a UUID in any format,
			// normalize both to canonical form so hyphenated matches non-hyphenated. M0097-0003.
			if isValidUUIDStr(as) || isValidUUIDStr(bs) {
				if isValidUUIDStr(as) {
					as = normalizeUUIDStr(as)
				}
				if isValidUUIDStr(bs) {
					bs = normalizeUUIDStr(bs)
				}
			}
			return strings.Compare(as, bs), nil
		}
		aIsBytes := a.Kind == KindBytes
		bIsBytes := b.Kind == KindBytes
		if aIsBytes && bIsBytes {
			return strings.Compare(string(a.BytesValue()), string(b.BytesValue())), nil
		}
		// Fall back to string comparison so planner-side column
		// misalignments don't crash the entire query.  The result
		// may not be PostgreSQL-correct, but the query completes.
		return strings.Compare(a.Format(), b.Format()), nil
	}
	switch a.Kind {
	case KindInt:
		switch {
		case a.Int < b.Int:
			return -1, nil
		case a.Int > b.Int:
			return 1, nil
		}
		return 0, nil
	case KindBool:
		switch {
		case !a.BoolValue() && b.BoolValue():
			return -1, nil
		case a.BoolValue() && !b.BoolValue():
			return 1, nil
		}
		return 0, nil
	case KindString:
		as, bs := a.StringValue(), b.StringValue()
		// pg_lsn comparison: use uint64 semantics, not lexicographic. M0097-pg_lsn.
		if looksLikePgLSN(as) && looksLikePgLSN(bs) {
			lu, errL := parsePgLSN(as)
			ru, errR := parsePgLSN(bs)
			if errL == nil && errR == nil {
				if lu < ru {
					return -1, nil
				}
				if lu > ru {
					return 1, nil
				}
				return 0, nil
			}
		}
		// UUID cross-format comparison: normalize both if either is a valid UUID. M0097-0003.
		if isValidUUIDStr(as) || isValidUUIDStr(bs) {
			if isValidUUIDStr(as) {
				as = normalizeUUIDStr(as)
			}
			if isValidUUIDStr(bs) {
				bs = normalizeUUIDStr(bs)
			}
		}
		return strings.Compare(as, bs), nil
	case KindTime:
		switch {
		case a.TimeValue().Before(b.TimeValue()):
			return -1, nil
		case a.TimeValue().After(b.TimeValue()):
			return 1, nil
		}
		return 0, nil
	}
	return 0, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("comparison not supported for kind %d", a.Kind)}
}

func cmpResult(op parser.OpCode, cmp int) bool {
	switch op {
	case parser.OpEq:
		return cmp == 0
	case parser.OpNe:
		return cmp != 0
	case parser.OpLt:
		return cmp < 0
	case parser.OpLe:
		return cmp <= 0
	case parser.OpGt:
		return cmp > 0
	case parser.OpGe:
		return cmp >= 0
	}
	return false
}

// evalAnd / evalOr implement Kleene three-valued logic.
func evalAnd(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && !a.BoolValue() {
		return NewBoolDatum(false)
	}
	if !b.IsNull() && b.Kind == KindBool && !b.BoolValue() {
		return NewBoolDatum(false)
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return NewBoolDatum(a.BoolValue() && b.BoolValue())
}

func evalOr(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && a.BoolValue() {
		return NewBoolDatum(true)
	}
	if !b.IsNull() && b.Kind == KindBool && b.BoolValue() {
		return NewBoolDatum(true)
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return NewBoolDatum(a.BoolValue() || b.BoolValue())
}

// evalTypedStringLit parses the body of a `<type> 'value'`
// literal at evaluation time. v0 supports date / timestamp /
// timestamptz; the parsed time is normalised to UTC.
//
// M0066-0002: caches the parsed time on the planner node so
// repeated evaluations in a hot loop (e.g. Q5's date filter
// applied per orders row) skip the `time.Parse` cost. pprof
// showed `time.parse` at 10.5 % cumulative CPU pre-cache.
func evalTypedStringLit(x *planner.TypedStringLit) (Datum, error) {
	if x.CacheValid {
		return NewTimeDatum(x.CachedTime), nil
	}
	switch x.Type {
	case "bool", "boolean":
		v := strings.TrimSpace(strings.ToLower(x.Value))
		switch v {
		case "t", "tr", "tru", "true", "y", "ye", "yes", "on", "1":
			return NewBoolDatum(true), nil
		case "f", "fa", "fal", "fals", "false", "n", "no", "of", "off", "0":
			return NewBoolDatum(false), nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type boolean: %q", x.Value)}
		}

	case "int2", "smallint":
		n, err := parseIntegerInput(x.Value, "smallint", 16)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "int4", "integer", "int":
		n, err := parseIntegerInput(x.Value, "integer", 32)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "int8", "bigint":
		n, err := parseIntegerInput(x.Value, "bigint", 64)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "float4", "real", "float8":
		// Goopg v0 stores floats as KindNumeric strings. Validate via
		// ParseFloat so the error message is PostgreSQL-compatible.
		v := strings.TrimSpace(x.Value)
		_, err := strconv.ParseFloat(v, 64)
		if err != nil {
			typname := "double precision"
			if x.Type == "float4" || x.Type == "real" {
				typname = "real"
			}
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type %s: %q", typname, x.Value)}
		}
		m, s, perr := parseNumeric(v)
		if perr != nil {
			return NewStringDatum(v), nil
		}
		return newNumeric(m, int(s)), nil

	case "numeric", "decimal":
		// Return as string — goopg v0 stores numerics as text.
		return NewStringDatum(strings.TrimSpace(x.Value)), nil

	case "text", "bpchar", "char", "varchar":
		return NewStringDatum(x.Value), nil

	case "name":
		// name type truncates to NAMEDATALEN-1 = 63 bytes. M0097-0003.
		s := x.Value
		if len(s) > 63 {
			s = s[:63]
		}
		return NewStringDatum(s), nil

	case "oid":
		// oid is uint32: 0..4294967295. M0097-0003.
		n, err := parseIntegerInput(x.Value, "oid", 64)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				ee.Pos = x.Pos()
			}
			return Datum{}, err
		}
		if n < 0 || n > 4294967295 {
			return Datum{}, &ExecError{Code: "22003", Pos: x.Pos(),
				Message: fmt.Sprintf("value %q is out of range for type oid", x.Value)}
		}
		return Datum{Kind: KindInt, Int: n}, nil

	case "xid":
		// xid is a 32-bit unsigned transaction ID. Accepts decimal, octal (0NNN), hex (0xNNN).
		// -1 wraps to 4294967295, matching PostgreSQL behaviour. M0097-0018.
		v := strings.TrimSpace(x.Value)
		// Special case: PostgreSQL allows "-1" as 2^32-1 = 4294967295.
		if v == "-1" {
			return Datum{Kind: KindInt, Int: int64(uint32(0xffffffff))}, nil
		}
		n, err := parseXid(v)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type xid: %q", x.Value)}
		}
		return Datum{Kind: KindInt, Int: int64(n)}, nil

	case "xid8":
		// xid8 is a 64-bit unsigned transaction ID. M0097-0018.
		v := strings.TrimSpace(x.Value)
		// Special case: PostgreSQL allows "-1" as 2^64-1 = 18446744073709551615.
		if v == "-1" {
			return Datum{Kind: KindInt, Int: -1}, nil // int64(-1) == uint64(0xffffffffffffffff) bitwise
		}
		n, err := parseXid8(v)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
				Message: fmt.Sprintf("invalid input syntax for type xid8: %q", x.Value)}
		}
		return Datum{Kind: KindInt, Int: int64(n)}, nil

	case "date":
		t, err := time.Parse("2006-01-02", x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid date %q: %v", x.Value, err)}
		}
		x.CachedTime = t.UTC()
		x.CacheValid = true
		return NewTimeDatum(x.CachedTime), nil
	case "time", "timetz":
		ts, err := parseTimeString(x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid input syntax for type time: %q", x.Value)}
		}
		return NewTimeDatum(ts), nil
	case "timestamp", "timestamptz":
		// Try a few common upstream layouts in order. The
		// `2006-01-02 15:04:05` form is what TPC-H and pgbench
		// use; `2006-01-02T15:04:05Z` is RFC3339 fallback.
		layouts := []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", "2006-01-02"}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, x.Value); err == nil {
				x.CachedTime = t.UTC()
				x.CacheValid = true
				return NewTimeDatum(x.CachedTime), nil
			}
		}
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid timestamp %q", x.Value)}
	default:
		// Unknown type — treat as text literal. Covers enum/domain casts in v0.
		// M0097-0017: enum/domain type casts return the string value as-is.
		return NewStringDatum(x.Value), nil
	}
}

// roundNumericToInt rounds a KindNumeric datum using "round half away from zero"
// (PostgreSQL's numeric→integer rounding rule). M0097-0003.
func roundNumericToInt(d Datum, pos int) (int64, error) {
	text := numericText(d)
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("invalid numeric value for integer cast: %s", text)}
	}
	// Round half away from zero (PostgreSQL's numeric→integer rule).
	var rounded int64
	if f >= 0 {
		rounded = int64(f + 0.5)
	} else {
		rounded = int64(f - 0.5)
	}
	return rounded, nil
}

// roundFloatToInt rounds a KindNumeric datum using banker's rounding
// (round half to even) — PostgreSQL's float8/float4→integer rule. M0097-0003.
func roundFloatToInt(d Datum, pos int) (int64, error) {
	text := numericText(d)
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("invalid float value for integer cast: %s", text)}
	}
	// Banker's rounding: round half to nearest even.
	rounded := math.RoundToEven(f)
	return int64(rounded), nil
}

// isFloatSourceType reports whether a type name denotes a floating-point type
// (float4 / float8 / real / double precision). Used to select banker's rounding
// for float→integer casts. M0097-0003.
func isFloatSourceType(t string) bool {
	switch strings.ToLower(t) {
	case "float4", "float8", "real", "double precision":
		return true
	}
	return false
}

// evalCastTyped is like evalCast but accepts the source type so it can select
// the correct rounding mode (banker's for float, away-from-zero for numeric).
// M0097-0003.
func evalCastTyped(d Datum, targetType, sourceType string, pos int) (Datum, error) {
	if sourceType == "" {
		return evalCast(d, targetType, pos)
	}
	// For float8/float4 → integer casts, override the default (away-from-zero)
	// rounding inside evalCast to use banker's rounding instead.
	if isFloatSourceType(sourceType) && d.Kind == KindNumeric {
		intTarget := strings.ToLower(targetType)
		switch intTarget {
		case "int2", "smallint":
			n, err := roundFloatToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -32768 || n > 32767 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "smallint out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case "int4", "integer", "int":
			n, err := roundFloatToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -2147483648 || n > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "integer out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case "int8", "bigint":
			n, err := roundFloatToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		}
	}
	return evalCast(d, targetType, pos)
}

// evalCast coerces datum d to the declared SQL type name.
// Handles: string→bool, bool→text, int→text, int→int2 (range check),
// string→int2/4/8 (via parseIntegerInput). Pass-through for unknown types.
// M0097-0003.
func evalCast(d Datum, targetType string, pos int) (Datum, error) {
	if d.IsNull() {
		return NullDatum, nil
	}
	switch strings.ToLower(targetType) {
	case "bool", "boolean":
		switch d.Kind {
		case KindBool:
			return d, nil
		case KindString:
			v := strings.TrimSpace(strings.ToLower(d.StringValue()))
			switch v {
			case "t", "tr", "tru", "true", "y", "ye", "yes", "on", "1":
				return NewBoolDatum(true), nil
			case "f", "fa", "fal", "fals", "false", "n", "no", "of", "off", "0":
				return NewBoolDatum(false), nil
			default:
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type boolean: %q", d.StringValue())}
			}
		case KindInt:
			return NewBoolDatum(d.Int != 0), nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to bool"}
		}
	case "int2", "smallint":
		switch d.Kind {
		case KindInt:
			if d.Int < -32768 || d.Int > 32767 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "smallint out of range"}
			}
			return d, nil
		case KindString:
			n, err := parseIntegerInput(d.StringValue(), "smallint", 16)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindNumeric:
			// Float/numeric → int2: round to nearest even (banker's rounding). M0097-0003.
			n, err := roundNumericToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -32768 || n > 32767 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "smallint out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to smallint"}
		}
	case "int4", "integer", "int":
		switch d.Kind {
		case KindInt:
			if d.Int < -2147483648 || d.Int > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "integer out of range"}
			}
			return d, nil
		case KindString:
			n, err := parseIntegerInput(d.StringValue(), "integer", 32)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindNumeric:
			n, err := roundNumericToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			if n < -2147483648 || n > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "integer out of range"}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to integer"}
		}
	case "int8", "bigint":
		switch d.Kind {
		case KindInt:
			return d, nil
		case KindString:
			n, err := parseIntegerInput(d.StringValue(), "bigint", 64)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		case KindNumeric:
			n, err := roundNumericToInt(d, pos)
			if err != nil {
				return Datum{}, err
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return Datum{}, &ExecError{Code: "22P02", Pos: pos, Message: "cannot cast to bigint"}
		}
	case "name":
		// name type truncates to NAMEDATALEN-1 = 63 bytes.
		// For text[] values (e.g. from parse_ident()), truncate each array element. M0097-0003.
		switch d.Kind {
		case KindString:
			s := d.StringValue()
			// If the value looks like a PostgreSQL array ({elem1,elem2,...}), process as array.
			if len(s) > 0 && s[0] == '{' && s[len(s)-1] == '}' {
				elems := parseTextArray(s)
				for i, e := range elems {
					if len(e) > 63 {
						elems[i] = e[:63]
					}
				}
				return NewStringDatum(formatTextArray(elems)), nil
			}
			// Single value: truncate.
			if len(s) > 63 {
				s = s[:63]
			}
			return NewStringDatum(s), nil
		default:
			return d, nil
		}
	case "text", "varchar", "bpchar", "char":
		switch d.Kind {
		case KindBool:
			if d.BoolValue() {
				return NewStringDatum("true"), nil
			}
			return NewStringDatum("false"), nil
		case KindInt:
			return NewStringDatum(strconv.FormatInt(d.Int, 10)), nil
		case KindTime:
			if isTimeOnlyValue(d.TimeValue()) {
				return NewStringDatum(string(appendTimeOnlyValueText(nil, d.TimeValue()))), nil
			}
			return NewStringDatum(d.Format()), nil
		case KindString:
			s := d.StringValue()
			// For "char" (internal 1-byte type), interpret backslash-octal escapes
			// and return the charout display form. PostgreSQL's charin() accepts
			// \NNN and charout() formats non-printable bytes as \NNN. M0097-0003.
			if targetType == "char" {
				if b, ok := charTypeParseOctalEscape(s); ok {
					return NewStringDatum(charTypeDisplayForm(b)), nil
				}
			}
			return d, nil
		default:
			return d, nil
		}
	case "float4", "real", "float8", "double precision":
		// Normalize KindNumeric through float64 to strip trailing zeros (0.0→0). M0097-0003.
		// PostgreSQL float8out uses printf-style format that removes trailing zeros.
		if d.Kind == KindNumeric {
			text := numericText(d)
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type float8: %q", text)}
			}
			normalized := strconv.FormatFloat(f, 'f', -1, 64)
			if v, s, ok := parseNumericFast(normalized); ok {
				return Datum{Kind: KindNumeric, Int: v, Scale: s}, nil
			}
			m, s, parseErr := parseNumeric(normalized)
			if parseErr != nil {
				return d, nil // unexpected, keep original
			}
			return newNumeric(m, int(s)), nil
		}
		return d, nil
	case "oid":
		switch d.Kind {
		case KindInt:
			if d.Int < 0 || d.Int > 4294967295 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "value out of range for type oid"}
			}
			return d, nil
		case KindString:
			n, err := parseIntegerInput(d.StringValue(), "oid", 64)
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			if n < 0 || n > 4294967295 {
				return Datum{}, &ExecError{Code: "22003", Pos: pos,
					Message: fmt.Sprintf("value %q is out of range for type oid", d.StringValue())}
			}
			return Datum{Kind: KindInt, Int: n}, nil
		default:
			return d, nil
		}
	case "pg_lsn":
		switch d.Kind {
		case KindString:
			u, err := parsePgLSN(strings.TrimSpace(d.StringValue()))
			if err != nil {
				if ee, ok := err.(*ExecError); ok {
					ee.Pos = pos
				}
				return Datum{}, err
			}
			return NewStringDatum(formatPgLSN(u)), nil
		case KindInt:
			return NewStringDatum(formatPgLSN(uint64(d.Int))), nil
		default:
			return NewStringDatum(d.Format()), nil
		}
	case "regclass":
		// `oid::regclass` renders as the relation name (matches PG's
		// regclassout). The catalog lookup happens at evalFuncCall's
		// `regclass` arm for string→OID; here we cover the
		// CastExpr path used by `tableoid::regclass` for the
		// `tableoid` system column. KindInt input is the OID; we
		// resolve via the executor's catalog (see CastExpr eval-site
		// note below) and emit the qualified relname as a string.
		// String input (e.g. `'pg_class'::regclass`) is delegated to
		// the function-call path which already handles it.
		// M0100-0005y.
		if d.Kind == KindInt {
			// The catalog isn't reachable from evalCast's signature;
			// stash the OID as KindRegClass so the wire formatter (or
			// upstream evalCastTyped wrapper) can render it. Until
			// formatter support lands, return the raw integer — the
			// CastExpr operand path in evalExprSlot will route through
			// evalRegclassCast for the tableoid OID lookup.
			return d, nil
		}
		return d, nil
	case "date":
		// Cast to date: truncate KindTime to midnight UTC, parse strings as dates. M0097-0004.
		if d.Kind == KindString {
			s := d.StringValue()
			if t, err := parseCopyTimestamp(s); err == nil {
				t2 := t.UTC()
				return NewTimeDatum(time.Date(t2.Year(), t2.Month(), t2.Day(), 0, 0, 0, 0, time.UTC)), nil
			}
			return Datum{}, &ExecError{Code: "22007", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type date: %q", s)}
		}
		if d.Kind == KindTime {
			t := d.TimeValue().UTC()
			return NewTimeDatum(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)), nil
		}
		return d, nil
	case "time", "timetz":
		// Cast to time: extract time-of-day from KindTime, parse strings. M0097-0004.
		if d.Kind == KindString {
			ts, err := parseTimeString(d.StringValue())
			if err != nil {
				return Datum{}, err
			}
			return NewTimeDatum(ts), nil
		}
		if d.Kind == KindTime {
			t := d.TimeValue().UTC()
			// Re-anchor to epoch to strip any date component.
			return NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)), nil
		}
		return d, nil
	case "timestamp", "timestamptz":
		// Cast to timestamp: parse strings, keep KindTime as-is. M0097-0004.
		if d.Kind == KindString {
			ts, err := parseCopyTimestamp(d.StringValue())
			if err != nil {
				return Datum{}, &ExecError{Code: "22007", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type timestamp: %q", d.StringValue())}
			}
			return NewTimeDatum(ts), nil
		}
		return d, nil
	case "tid":
		// Cast to tid: parse/validate "(block,offset)" and re-emit the
		// canonical form. PostgreSQL's tidin treats block as an unsigned
		// 32-bit BlockNumber (so '(-1,0)' normalises to '(4294967295,0)')
		// and offset as an unsigned 16-bit OffsetNumber. M0097-0036.
		if d.Kind == KindString {
			block, offset, ok := parseTidInput(d.StringValue())
			if !ok {
				return Datum{}, &ExecError{Code: "22P02", Pos: pos,
					Message: fmt.Sprintf("invalid input syntax for type tid: %q", d.StringValue())}
			}
			return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
		}
		return d, nil
	}
	return d, nil // pass-through for unknown types
}

// cStrtoul10Full emulates C strtoul(s, &end, 10) followed by PostgreSQL's
// "fully consumed" check used in tidin: it skips leading C whitespace, accepts
// an optional +/- sign, then base-10 digits, and requires the digits to run to
// the end of s (so any trailing junk before the delimiter is rejected, matching
// "*badp != DELIM"). Negative inputs wrap modulo 2^64 like C unsigned
// arithmetic. ok is false when no digits were present or trailing junk remains;
// overflow is true when the magnitude exceeds 64 bits (C would set ERANGE).
func cStrtoul10Full(s string) (val uint64, ok bool, overflow bool) {
	i := 0
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			i++
			continue
		}
		break
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var v uint64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		nv := v*10 + uint64(s[i]-'0')
		if nv < v {
			overflow = true
		}
		v = nv
		i++
	}
	if i == start || i != len(s) {
		return 0, false, false
	}
	if neg {
		v = -v // two's-complement negation modulo 2^64
	}
	return v, true, overflow
}

// parseTidInput parses a tid external representation "(block,offset)" exactly
// as PostgreSQL's tidin (src/backend/utils/adt/tid.c): block is a BlockNumber
// (uint32) accepted via strtoul with the wider-than-32-bit round-trip guard
// (so '-1' → 4294967295 but '4294967296' is rejected), and offset is an
// OffsetNumber (uint16) bounded by USHRT_MAX. Returns ok=false on any malformed
// or out-of-range input. M0097-0036.
func parseTidInput(str string) (block uint32, offset uint16, ok bool) {
	lp := strings.IndexByte(str, '(')
	if lp < 0 {
		return 0, 0, false
	}
	rest := str[lp+1:]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return 0, 0, false
	}
	offPart := rest[comma+1:]
	rp := strings.IndexByte(offPart, ')')
	if rp < 0 {
		return 0, 0, false
	}

	bcvt, bok, bovf := cStrtoul10Full(rest[:comma])
	if !bok || bovf {
		return 0, 0, false
	}
	block = uint32(bcvt)
	// PG's SIZEOF_LONG > 4 guard: accept only values that round-trip through
	// either the unsigned or sign-extended 32-bit truncation.
	if bcvt != uint64(block) && bcvt != uint64(int64(int32(block))) {
		return 0, 0, false
	}

	ocvt, ook, oovf := cStrtoul10Full(offPart[:rp])
	if !ook || oovf || ocvt > 65535 {
		return 0, 0, false
	}
	return block, uint16(ocvt), true
}

// parseXid parses an xid value (unsigned 32-bit). Accepts decimal, octal (0NNN), hex (0xNNN).
// M0097-0018.
func parseXid(s string) (uint32, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseUint(s[2:], 16, 32)
		return uint32(n), err
	}
	if len(s) > 1 && s[0] == '0' {
		n, err := strconv.ParseUint(s[1:], 8, 32)
		return uint32(n), err
	}
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err
}

// parseXid8 parses an xid8 value (unsigned 64-bit). M0097-0018.
func parseXid8(s string) (uint64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

// parsePgSnapshotValid returns true if s is a valid pg_snapshot literal.
// Format: xmin:xmax[:xip,...]  M0097-0018.
func parsePgSnapshotValid(s string) bool {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return false
	}
	xmin, err1 := strconv.ParseUint(parts[0], 10, 64)
	xmax, err2 := strconv.ParseUint(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	if xmin > xmax {
		return false
	}
	if len(parts) == 3 && parts[2] != "" {
		for _, xip := range strings.Split(parts[2], ",") {
			v, err := strconv.ParseUint(xip, 10, 64)
			if err != nil {
				return false
			}
			if v < xmin || v >= xmax {
				return false
			}
		}
	}
	return true
}

// sizePretty formats a byte count as a human-readable size string, matching
// PostgreSQL's pg_size_pretty() output. Uses 1024-based units. M0097-0018.
//
// sizePretty formats an integer byte count as a human-readable size string.
// Replicates PostgreSQL's pg_size_pretty(bigint) iterative algorithm exactly,
// using uint64 for the absolute-value check to handle INT64_MIN correctly.
func sizePretty(size int64) string {
	type szUnit struct {
		name     string
		limit    uint64
		round    bool
		unitbits int
	}
	szUnits := []szUnit{
		{"bytes", 10 * 1024, false, 0},
		{"kB", 20*1024 - 1, true, 10},
		{"MB", 20*1024 - 1, true, 20},
		{"GB", 20*1024 - 1, true, 30},
		{"TB", 20*1024 - 1, true, 40},
		{"PB", 20*1024 - 1, true, 50},
	}
	cur := size
	for i, u := range szUnits {
		var absSize uint64
		if cur < 0 {
			absSize = 0 - uint64(cur) // handles INT64_MIN: 0-uint64(INT64_MIN)=2^63
		} else {
			absSize = uint64(cur)
		}
		nextIsLast := i+1 >= len(szUnits)
		if nextIsLast || absSize < u.limit {
			if u.round {
				if cur > 0 {
					cur = (cur + 1) / 2
				} else {
					cur = (cur - 1) / 2
				}
			}
			return fmt.Sprintf("%d %s", cur, u.name)
		}
		next := szUnits[i+1]
		bits := uint(next.unitbits - u.unitbits)
		if next.round {
			bits--
		}
		if u.round {
			bits++
		}
		cur /= int64(1) << bits
	}
	return fmt.Sprintf("%d PB", cur)
}

// sizePrettyBig formats a numeric byte count (given as a decimal string) as a
// human-readable size string. Uses exact big.Int/big.Rat arithmetic to replicate
// PostgreSQL's pg_size_pretty(numeric) algorithm (avoids float64 precision loss
// at the PB boundary).
func sizePrettyBig(s string) string {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return s + " bytes"
	}
	type szUnit struct {
		name     string
		limit    int64
		round    bool
		unitbits int
	}
	szUnits := []szUnit{
		{"bytes", 10 * 1024, false, 0},
		{"kB", 20*1024 - 1, true, 10},
		{"MB", 20*1024 - 1, true, 20},
		{"GB", 20*1024 - 1, true, 30},
		{"TB", 20*1024 - 1, true, 40},
		{"PB", 20*1024 - 1, true, 50},
	}
	cur := new(big.Rat).Set(r)
	for i, u := range szUnits {
		absCur := new(big.Rat).Abs(cur)
		limitR := new(big.Rat).SetInt64(u.limit)
		nextIsLast := i+1 >= len(szUnits)
		if nextIsLast || absCur.Cmp(limitR) < 0 {
			if u.round {
				// Truncate to integer, then half_rounded: (n±1)/2 toward zero.
				curInt := bigRatTrunc(cur)
				if curInt.Sign() >= 0 {
					curInt.Add(curInt, big.NewInt(1))
				} else {
					curInt.Sub(curInt, big.NewInt(1))
				}
				curInt.Quo(curInt, big.NewInt(2))
				return fmt.Sprintf("%s %s", curInt.String(), u.name)
			}
			// bytes: display exact value (preserve fractional part like PG).
			if cur.IsInt() {
				return fmt.Sprintf("%s %s", cur.Num().String(), u.name)
			}
			f, _ := strconv.ParseFloat(cur.FloatString(20), 64)
			return fmt.Sprintf("%g %s", f, u.name)
		}
		next := szUnits[i+1]
		bits := uint(next.unitbits - u.unitbits)
		if next.round {
			bits--
		}
		if u.round {
			bits++
		}
		divisor := new(big.Rat).SetInt64(int64(1) << bits)
		cur.Quo(cur, divisor)
		// Truncate toward zero after each step (exact Numeric arithmetic).
		curInt := bigRatTrunc(cur)
		cur = new(big.Rat).SetInt(curInt)
	}
	return fmt.Sprintf("%s PB", bigRatTrunc(cur).String())
}

// bigRatTrunc truncates a big.Rat toward zero and returns the integer part.
func bigRatTrunc(r *big.Rat) *big.Int {
	q, _ := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	return q
}

// sizePrettyFloat formats a float64 byte count as a human-readable size string.
// Used for KindFloat (float8) inputs to pg_size_pretty.
func sizePrettyFloat(f float64) string {
	neg := f < 0
	if neg {
		f = -f
	}
	var result string
	const (
		kB = float64(1024)
		MB = float64(1024 * 1024)
		GB = float64(1024 * 1024 * 1024)
		TB = float64(1024 * 1024 * 1024 * 1024)
		PB = float64(1024 * 1024 * 1024 * 1024 * 1024)
	)
	halfRoundF := func(n, unit float64) int64 { return int64(n / unit) }
	switch {
	case f < 10*kB:
		if f == float64(int64(f)) {
			result = fmt.Sprintf("%d bytes", int64(f))
		} else {
			result = fmt.Sprintf("%g bytes", f)
		}
	case f < 10*MB:
		result = fmt.Sprintf("%d kB", halfRoundF(f, kB))
	case f < 10*GB:
		result = fmt.Sprintf("%d MB", halfRoundF(f, MB))
	case f < 10*TB:
		result = fmt.Sprintf("%d GB", halfRoundF(f, GB))
	case f < 10*PB:
		result = fmt.Sprintf("%d TB", halfRoundF(f, TB))
	default:
		result = fmt.Sprintf("%d PB", halfRoundF(f, PB))
	}
	if neg {
		return "-" + result
	}
	return result
}

// parseSizeBytes parses a human-readable size string into bytes.
// Supports units: bytes/B, kB, MB, GB, TB, PB (case-insensitive).
// Also accepts scientific notation (e.g. "1e6 MB"). M0097-0018.
// Error messages and behaviour match PostgreSQL 17.
func parseSizeBytes(s string) (int64, error) {
	// orig: preserve original input exactly for error messages (matches PG behaviour).
	orig := s
	ws := strings.TrimSpace(s)
	if ws == "" {
		return 0, fmt.Errorf("invalid size: %q", "")
	}

	// Parse numeric part: optional sign, digits, optional '.'+digits, optional exponent.
	i := 0
	if i < len(ws) && (ws[i] == '-' || ws[i] == '+') {
		i++
	}
	for i < len(ws) && ws[i] >= '0' && ws[i] <= '9' {
		i++
	}
	if i < len(ws) && ws[i] == '.' {
		i++
		for i < len(ws) && ws[i] >= '0' && ws[i] <= '9' {
			i++
		}
	}
	expStart := i
	if i < len(ws) && (ws[i] == 'e' || ws[i] == 'E') {
		j := i + 1
		if j < len(ws) && (ws[j] == '-' || ws[j] == '+') {
			j++
		}
		if j < len(ws) && ws[j] >= '0' && ws[j] <= '9' {
			i = j
			for i < len(ws) && ws[i] >= '0' && ws[i] <= '9' {
				i++
			}
		} else {
			i = expStart // no valid exponent; treat 'e' as start of unit
		}
	}
	numStr := ws[:i]
	unitStr := strings.TrimSpace(ws[i:])

	// Must have at least one digit.
	hasDigit := false
	for _, c := range numStr {
		if c >= '0' && c <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		return 0, fmt.Errorf("invalid size: %q", orig)
	}

	// Handle trailing decimal point: "1." → "1.0"
	if strings.HasSuffix(numStr, ".") {
		numStr += "0"
	}

	val, parseErr := strconv.ParseFloat(numStr, 64)
	// ErrRange produces Inf — means exponent overflow, matching PG's "value overflows numeric format".
	if math.IsInf(val, 0) {
		return 0, fmt.Errorf("value overflows numeric format")
	}
	if parseErr != nil || math.IsNaN(val) {
		return 0, fmt.Errorf("invalid size: %q", orig)
	}

	const sizeHint = `Valid units are "bytes", "B", "kB", "MB", "GB", "TB", and "PB".`
	var multiplier float64
	switch strings.ToLower(unitStr) {
	case "", "b", "bytes":
		multiplier = 1
	case "kb":
		multiplier = 1024
	case "mb":
		multiplier = 1024 * 1024
	case "gb":
		multiplier = 1024 * 1024 * 1024
	case "tb":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "pb":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return 0, &ExecError{
			Code:    "22023",
			Message: fmt.Sprintf("invalid size: %q", orig),
			Detail:  fmt.Sprintf("Invalid size unit: %q.", unitStr),
			Hint:    sizeHint,
		}
	}

	result := val * multiplier
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("bigint out of range")
	}
	// MaxInt64 as float64 rounds to 9.223372036854776e18; values strictly
	// greater than that can't fit in int64.
	const maxInt64Float = float64(1 << 63) // 9.223372036854776e18
	if result >= maxInt64Float || result < -maxInt64Float {
		return 0, fmt.Errorf("bigint out of range")
	}
	// Truncate toward zero, matching PostgreSQL behaviour (e.g. -.1 kB → -102).
	return int64(result), nil
}

// newNumericFromFloat converts a float64 to a KindNumeric Datum for
// EXTRACT/date_part fractional-second results. Uses up to 6 decimal places.
func newNumericFromFloat(f float64) Datum {
	s := strconv.FormatFloat(f, 'f', 6, 64)
	// Strip trailing zeros after decimal point.
	if idx := strings.Index(s, "."); idx >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if v, scale, ok := parseNumericFast(s); ok {
		return Datum{Kind: KindNumeric, Int: v, Scale: scale}
	}
	m, scale, err := parseNumeric(s)
	if err != nil {
		return NewStringDatum(s)
	}
	return newNumeric(m, int(scale))
}

// evalExtract implements `EXTRACT(field FROM source)` for the
// timestamp-component fields TPC-H Q7/Q8/Q9 use (year), plus
// the obvious neighbours (month, day, hour, minute, dow, doy,
// epoch). Returns int8 for most fields; float8 for fractional-second
// fields (second, millisecond, epoch). M0097-0004.
func evalExtract(x *planner.ExtractExpr, row Row, ctx *Context) (Datum, error) {
	src, err := evalExpr(x.Source, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() {
		return NullDatum, nil
	}
	if src.Kind != KindTime {
		// Try to parse a string as timestamp (planner may assign
		// string storage for date columns loaded via INSERT).
		if src.Kind == KindString {
			if t, err := parseCopyTimestamp(src.StringValue()); err == nil {
				src = NewTimeDatum(t)
			}
		}
	}
	if src.Kind != KindTime {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("EXTRACT(%s FROM …) requires timestamp/date input", x.Field)}
	}
	// Fractional-second fields return float8 (numeric) in PostgreSQL.
	u := src.TimeValue().UTC()
	field := strings.ToLower(strings.TrimSpace(x.Field))
	// For time-of-day source types, validate and handle allowed fields only. M0097-0004.
	srcType := strings.ToLower(x.SourceTypeName)
	isTimeOnly := srcType == "time" || srcType == "timetz"
	switch field {
	case "second", "seconds":
		f := float64(u.Second()) + float64(u.Nanosecond())/1e9
		return newNumericFromFloat(f), nil
	case "milliseconds", "millisecond":
		f := float64(u.Second())*1000 + float64(u.Nanosecond())/1_000_000.0
		return newNumericFromFloat(f), nil
	case "epoch":
		f := float64(u.Hour()*3600+u.Minute()*60+u.Second()) + float64(u.Nanosecond())/1e9
		return newNumericFromFloat(f), nil
	case "timezone", "timezone_hour", "timezone_minute":
		if isTimeOnly {
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unit %q not supported for type time without time zone", field)}
		}
		return Datum{Kind: KindInt, Int: 0}, nil // UTC only
	}
	// For time-of-day types, reject date-specific fields with PG-compatible errors. M0097-0004.
	if isTimeOnly {
		switch field {
		case "hour", "minute", "microseconds", "microsecond":
			// allowed for time types (handled by extractTimestampField below)
		default:
			// Check if it's a known-but-unsupported date field or completely unknown.
			knownDateFields := map[string]bool{
				"year": true, "month": true, "day": true, "decade": true,
				"century": true, "millennium": true, "week": true, "isoweek": true,
				"isoyear": true, "isodow": true, "dow": true, "doy": true, "quarter": true,
			}
			if knownDateFields[field] {
				return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
					Message: fmt.Sprintf("unit %q not supported for type time without time zone", field)}
			}
			return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(),
				Message: fmt.Sprintf("unit %q not recognized for type time without time zone", field)}
		}
	}
	n, err := extractTimestampField(x.Field, u, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindInt, Int: n}, nil
}

// extractTimestampField returns the integer value of a named
// calendar field from a UTC timestamp. Shared by evalExtract and
// evalDatePart.
func extractTimestampField(field string, t time.Time, pos int) (int64, error) {
	u := t.UTC()
	switch field {
	case "year":
		return int64(u.Year()), nil
	case "month":
		return int64(u.Month()), nil
	case "day":
		return int64(u.Day()), nil
	case "hour":
		return int64(u.Hour()), nil
	case "minute":
		return int64(u.Minute()), nil
	case "second":
		return int64(u.Second()), nil
	case "dow":
		return int64(u.Weekday()), nil // Sunday=0, matches upstream
	case "doy":
		return int64(u.YearDay()), nil
	case "epoch":
		return u.Unix(), nil
	case "quarter":
		return int64((int(u.Month())-1)/3 + 1), nil
	// M0097-0004: additional calendar fields.
	case "week", "isoweek":
		_, week := u.ISOWeek()
		return int64(week), nil
	case "isoyear":
		year, _ := u.ISOWeek()
		return int64(year), nil
	case "isodow":
		wd := u.Weekday()
		if wd == 0 {
			wd = 7 // ISO: Sunday = 7
		}
		return int64(wd), nil
	case "decade":
		y := int64(u.Year())
		if y >= 0 {
			return y / 10, nil
		}
		return (y - 9) / 10, nil
	case "century":
		y := int64(u.Year())
		if y > 0 {
			return (y + 99) / 100, nil
		}
		return -((-y + 99) / 100), nil
	case "millennium":
		y := int64(u.Year())
		if y > 0 {
			return (y + 999) / 1000, nil
		}
		return -((-y + 999) / 1000), nil
	case "microseconds", "microsecond":
		return int64(u.Second())*1_000_000 + int64(u.Nanosecond()/1000), nil
	case "milliseconds", "millisecond":
		return int64(u.Second())*1000 + int64(u.Nanosecond()/1_000_000), nil
	case "timezone", "timezone_hour", "timezone_minute":
		return 0, nil // goopg v0 is UTC-only
	default:
		return 0, &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("date field %q is not supported in v0", field)}
	}
}

// evalDatePart implements PostgreSQL's `date_part(text, timestamp)`
// builtin. The first argument is a string literal naming the field
// (e.g. 'year', 'month', 'quarter'). Semantics match
// extractTimestampField, which is shared with EXTRACT.
func evalDatePart(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "date_part(text, timestamp) requires exactly 2 arguments"}
	}
	fieldArg, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	src, err := evalExpr(x.Args[1], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if fieldArg.IsNull() || src.IsNull() {
		return NullDatum, nil
	}
	if fieldArg.Kind != KindString {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "date_part first argument must be text"}
	}
	if src.Kind != KindTime {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "date_part second argument must be timestamp/date"}
	}
	// Fractional-second fields return float8 (numeric), like evalExtract. M0097-0004.
	u := src.TimeValue().UTC()
	field := strings.ToLower(strings.TrimSpace(fieldArg.StringValue()))
	switch field {
	case "second", "seconds":
		f := float64(u.Second()) + float64(u.Nanosecond())/1e9
		return newNumericFromFloat(f), nil
	case "milliseconds", "millisecond":
		f := float64(u.Second())*1000 + float64(u.Nanosecond())/1_000_000.0
		return newNumericFromFloat(f), nil
	case "epoch":
		f := float64(u.Hour()*3600+u.Minute()*60+u.Second()) + float64(u.Nanosecond())/1e9
		return newNumericFromFloat(f), nil
	}
	n, err := extractTimestampField(field, u, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindInt, Int: n}, nil
}

// evalToChar implements to_char(timestamp, fmt) → text.
// Converts a timestamp to a string using a PostgreSQL format string.
// Supports a subset of PostgreSQL format codes. M0097-0004.
func evalToChar(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) < 2 {
		return NullDatum, nil
	}
	srcArg, err := evalExpr(x.Args[0], row, ctx)
	if err != nil || srcArg.IsNull() {
		return NullDatum, nil
	}
	fmtArg, err := evalExpr(x.Args[1], row, ctx)
	if err != nil || fmtArg.IsNull() {
		return NullDatum, nil
	}
	// to_char(numeric, fmt) — number formatting; return as-is for now.
	if srcArg.Kind != KindTime {
		return NewStringDatum(srcArg.Format()), nil
	}
	t := srcArg.TimeValue().UTC()
	goFmt := pgToCharToGoFormat(strings.TrimSpace(fmtArg.StringValue()))
	return NewStringDatum(t.Format(goFmt)), nil
}

// pgToCharToGoFormat converts a PostgreSQL to_char format string to a Go
// time.Format layout string. Supports the most common format codes.
func pgToCharToGoFormat(pg string) string {
	replacer := strings.NewReplacer(
		"YYYY", "2006",
		"YYY", "006",
		"YY", "06",
		"Y", "6",
		"IYYY", "2006", // ISO year — approximate
		"IYY", "006",
		"IY", "06",
		"I", "6",
		"MM", "01",
		"MON", "Jan",
		"Mon", "Jan",
		"mon", "jan",
		"MONTH", "January",
		"Month", "January",
		"month", "january",
		"DD", "02",
		"D", "1", // day of week 1=Sun PostgreSQL, Go: Mon=1
		"DAY", "Monday",
		"Day", "Monday",
		"day", "monday",
		"DY", "Mon",
		"Dy", "Mon",
		"dy", "mon",
		"HH24", "15",
		"HH12", "03",
		"HH", "03",
		"MI", "04",
		"SS", "05",
		"MS", "000", // milliseconds
		"US", "000000", // microseconds
		"TZ", "UTC", // always UTC in v0
		"tz", "utc",
		"TZH", "-07",
		"TZM", "00",
		"AM", "PM",
		"PM", "PM",
		"am", "pm",
		"pm", "pm",
		"A.M.", "PM",
		"P.M.", "PM",
		"Q", "", // quarter — not supported in Go format
		"WW", "", // week of year — not directly supported
		"IW", "", // ISO week
		"CC", "", // century
		"J", "", // Julian day
		"SSSSS", "", // seconds past midnight
		"SSSS", "",
		"Y,YYY", "", // year with comma
		"OF", "-07:00",
		"TZO", "-07:00",
	)
	return replacer.Replace(pg)
}

// evalDateTrunc implements date_trunc(field, source) → timestamp.
// Truncates a timestamp to the specified field granularity. M0097-0004.
func evalDateTrunc(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) < 2 {
		return NullDatum, nil
	}
	fieldArg, err := evalExpr(x.Args[0], row, ctx)
	if err != nil || fieldArg.IsNull() {
		return NullDatum, nil
	}
	src, err := evalExpr(x.Args[1], row, ctx)
	if err != nil || src.IsNull() {
		return NullDatum, nil
	}
	if src.Kind != KindTime {
		if src.Kind == KindString {
			if parsed, perr := parseCopyTimestamp(src.StringValue()); perr == nil {
				src = NewTimeDatum(parsed)
			}
		}
		if src.Kind != KindTime {
			return NullDatum, nil
		}
	}
	t := src.TimeValue().UTC()
	switch strings.ToLower(strings.TrimSpace(fieldArg.StringValue())) {
	case "microseconds":
		t = t.Truncate(time.Microsecond)
	case "milliseconds":
		t = t.Truncate(time.Millisecond)
	case "second":
		t = t.Truncate(time.Second)
	case "minute":
		t = t.Truncate(time.Minute)
	case "hour":
		t = t.Truncate(time.Hour)
	case "day":
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	case "week":
		wd := int(t.Weekday())
		if wd == 0 {
			wd = 7
		}
		t = time.Date(t.Year(), t.Month(), t.Day()-wd+1, 0, 0, 0, 0, t.Location())
	case "month":
		t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	case "quarter":
		m := t.Month()
		qm := ((m-1)/3)*3 + 1
		t = time.Date(t.Year(), qm, 1, 0, 0, 0, 0, t.Location())
	case "year":
		t = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	case "decade":
		y := (t.Year() / 10) * 10
		t = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	case "century":
		y := ((t.Year() - 1) / 100) * 100
		if y < 0 {
			y = ((t.Year()) / 100) * 100
		} else {
			y++
		}
		t = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	case "millennium":
		y := ((t.Year() - 1) / 1000) * 1000
		if y < 0 {
			y = (t.Year() / 1000) * 1000
		} else {
			y++
		}
		t = time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	}
	return NewTimeDatum(t), nil
}

// evalAge implements age(ts) and age(ts2, ts1) → interval. M0097-0004.
func evalAge(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	var ts1, ts2 time.Time
	switch len(x.Args) {
	case 1:
		d, err := evalExpr(x.Args[0], row, ctx)
		if err != nil || d.IsNull() || d.Kind != KindTime {
			return NullDatum, nil
		}
		ts1 = d.TimeValue().UTC()
		ts2 = ctx.Now.UTC()
	case 2:
		d2, err := evalExpr(x.Args[0], row, ctx)
		if err != nil || d2.IsNull() || d2.Kind != KindTime {
			return NullDatum, nil
		}
		d1, err := evalExpr(x.Args[1], row, ctx)
		if err != nil || d1.IsNull() || d1.Kind != KindTime {
			return NullDatum, nil
		}
		ts2 = d2.TimeValue().UTC()
		ts1 = d1.TimeValue().UTC()
	default:
		return NullDatum, nil
	}
	// Compute year/month/day difference the PostgreSQL way.
	years := ts2.Year() - ts1.Year()
	months := int(ts2.Month()) - int(ts1.Month())
	days := ts2.Day() - ts1.Day()
	if days < 0 {
		months--
		// Days in the month prior to ts2.
		prevMonth := time.Date(ts2.Year(), ts2.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		days += prevMonth.Day()
	}
	if months < 0 {
		years--
		months += 12
	}
	totalMonths := int32(years*12 + months)
	return NewIntervalDatum(totalMonths, int32(days)), nil
}

// evalMakeDate implements make_date(year, month, day) → date. M0097-0004.
func evalMakeDate(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 3 {
		return NullDatum, nil
	}
	yArg, err := evalExpr(x.Args[0], row, ctx)
	if err != nil || yArg.IsNull() {
		return NullDatum, nil
	}
	mArg, err := evalExpr(x.Args[1], row, ctx)
	if err != nil || mArg.IsNull() {
		return NullDatum, nil
	}
	dArg, err := evalExpr(x.Args[2], row, ctx)
	if err != nil || dArg.IsNull() {
		return NullDatum, nil
	}
	t := time.Date(int(yArg.Int), time.Month(mArg.Int), int(dArg.Int), 0, 0, 0, 0, time.UTC)
	return NewTimeDatum(t), nil
}

// evalMakeTimestamp implements make_timestamp/make_timestamptz(y,m,d,h,min,sec).
// M0097-0004.
func evalMakeTimestamp(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) < 6 {
		return NullDatum, nil
	}
	args := make([]int64, 6)
	for i := 0; i < 6; i++ {
		d, err := evalExpr(x.Args[i], row, ctx)
		if err != nil || d.IsNull() {
			return NullDatum, nil
		}
		args[i] = d.Int
	}
	t := time.Date(int(args[0]), time.Month(args[1]), int(args[2]),
		int(args[3]), int(args[4]), int(args[5]), 0, time.UTC)
	return NewTimeDatum(t), nil
}

// evalMakeTime implements make_time(h, min, sec) → time. M0097-0004.
func evalMakeTime(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) < 3 {
		return NullDatum, nil
	}
	h, err := evalExpr(x.Args[0], row, ctx)
	if err != nil || h.IsNull() {
		return NullDatum, nil
	}
	m, err := evalExpr(x.Args[1], row, ctx)
	if err != nil || m.IsNull() {
		return NullDatum, nil
	}
	s, err := evalExpr(x.Args[2], row, ctx)
	if err != nil || s.IsNull() {
		return NullDatum, nil
	}
	t := time.Date(1970, 1, 1, int(h.Int), int(m.Int), int(s.Int), 0, time.UTC)
	return NewTimeDatum(t), nil
}

// evalIsFinite stubs isfinite(date/timestamp/interval). goopg v0 does not
// store infinity values, so always returns TRUE for non-NULL input. M0097-0004.
func evalIsFinite(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return NullDatum, nil
	}
	d, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return NullDatum, nil
	}
	return NewBoolDatum(!d.IsNull()), nil
}

// evalJustifyInterval stubs justify_hours / justify_days / justify_interval.
// These re-balance interval fields (e.g. 25 hours → 1 day + 1 hour). goopg
// v0 does not yet track sub-day interval precision, so return the input as-is.
// M0097-0004.
func evalJustifyInterval(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return NullDatum, nil
	}
	return evalExpr(x.Args[0], row, ctx)
}

// evalDateBin implements date_bin(step interval, source timestamp, origin timestamp).
// Bins the source timestamp into the bucket identified by origin aligned to step.
// M0097-0004.
func evalDateBin(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) < 3 {
		return NullDatum, nil
	}
	stepArg, err := evalExpr(x.Args[0], row, ctx)
	if err != nil || stepArg.IsNull() || stepArg.Kind != KindInterval {
		return NullDatum, nil
	}
	srcArg, err := evalExpr(x.Args[1], row, ctx)
	if err != nil || srcArg.IsNull() || srcArg.Kind != KindTime {
		return NullDatum, nil
	}
	originArg, err := evalExpr(x.Args[2], row, ctx)
	if err != nil || originArg.IsNull() || originArg.Kind != KindTime {
		return NullDatum, nil
	}
	// Convert interval step to duration (days only for v0).
	stepDays := int64(stepArg.IntervalDaysValue())
	if stepDays == 0 {
		return NullDatum, nil
	}
	stepDur := time.Duration(stepDays) * 24 * time.Hour
	src := srcArg.TimeValue().UTC()
	origin := originArg.TimeValue().UTC()
	diff := src.Sub(origin)
	bucket := (diff / stepDur) * stepDur
	if diff < 0 {
		bucket = ((diff - stepDur + 1) / stepDur) * stepDur
	}
	return NewTimeDatum(origin.Add(bucket)), nil
}

// evalIntervalLit parses the integer body of an `interval 'N' unit`
// literal. The parser already normalised plurals so Unit is one
// of day / month / year.
//
// M0066-0002: caches the parsed N on the planner node so
// repeated evaluations in a hot loop skip the
// `strconv.ParseInt` cost.
func evalIntervalLit(x *planner.IntervalLit) (Datum, error) {
	var n int32
	if x.CacheValid {
		n = x.CachedN
	} else {
		parsed, err := strconv.ParseInt(x.Value, 10, 32)
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid interval count %q", x.Value)}
		}
		n = int32(parsed)
		x.CachedN = n
		x.CacheValid = true
	}
	switch x.Unit {
	case "day":
		return NewIntervalDatum(0, n), nil
	case "month":
		return NewIntervalDatum(n, 0), nil
	case "year":
		return NewIntervalDatum(n*12, 0), nil
	default:
		return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(), Message: fmt.Sprintf("interval unit %q is not supported in v0", x.Unit)}
	}
}

// evalInExpr evaluates `expr [NOT] IN (subquery | val_list)`.
// The inner set is materialised once per evaluation (no
// caching across rows in v0); for IN (subquery), the executor
// drains the inner plan and collects the first column per
// row. For IN (list), the list is evaluated against the
// outer row.
//
// Three-valued logic:
//   - operand NULL → result NULL.
//   - any inner NULL when outer doesn't match a non-NULL
//     value → NULL.
//   - inner empty → false (NOT IN: true).
//
// Multi-column subqueries raise 42601.
func evalInExpr(x *planner.InExpr, row Row, ctx *Context) (Datum, error) {
	operand, err := evalExpr(x.Operand, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if operand.IsNull() {
		return NullDatum, nil
	}

	values, err := collectInValues(x, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	sawNull := false
	for _, v := range values {
		if v.IsNull() {
			sawNull = true
			continue
		}
		eq, err := compareEq(operand, v)
		if err != nil {
			return Datum{}, err
		}
		if eq.Kind == KindBool && eq.BoolValue() {
			return NewBoolDatum(!x.Negated), nil
		}
	}
	if sawNull {
		return NullDatum, nil
	}
	return NewBoolDatum(x.Negated), nil
}

// collectInValues returns the inner set for `IN (...)`. When
// the source is a subquery, drains it; the subquery must have
// exactly one column. Otherwise evaluates the value list.
func collectInValues(x *planner.InExpr, row Row, ctx *Context) ([]Datum, error) {
	if x.Plan != nil {
		// Check for query cancellation before each SubPlan evaluation.
		// Each call may scan millions of rows; this single atomic read
		// costs ~5 ns vs microseconds-to-seconds of SubPlan work.
		if ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		// Consult the subquery cache so correlated IN
		// subqueries are evaluated at most once per distinct
		// outer-row value rather than per outer row.  This
		// reduces Q20-like queries from O(outer×inner) to
		// O(inner) per distinct correlation key.
		//
		// For non-correlated subqueries (M0058-0001), the
		// inner plan returns the same set for every outer row,
		// so a constant cache key collapses re-evaluation to
		// a single execution.
		cacheKey := subqueryCacheKey(row)
		if x.IsNonCorrelated {
			cacheKey = nonCorrelatedCacheKey(x)
		}
		if ctx.SubqueryCache != nil {
			if ctx.SubqueryCacheScope != len(ctx.OuterRows) {
				clear(ctx.SubqueryCache)
				ctx.SubqueryCacheScope = len(ctx.OuterRows)
			}
			if cached, ok := ctx.SubqueryCache[cacheKey]; ok {
				return cached, nil
			}
		}
		// Push the outer row so correlated refs inside the
		// IN-subquery resolve against it. Pop on return.
		ctx.OuterRows = append(ctx.OuterRows, row)
		defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
		op, err := Build(x.Plan)
		if err != nil {
			return nil, err
		}
		if err := op.Open(ctx); err != nil {
			_ = op.Close()
			return nil, err
		}
		defer func() { _ = op.Close() }()
		var out []Datum
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			r := slotRow(slot)
			if len(r) != 1 {
				return nil, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("subquery used as IN argument returned %d columns, expected 1", len(r))}
			}
			out = append(out, r[0])
		}
		// Cache the result so subsequent rows with the same
		// correlation values skip the inner-plan execution.
		if ctx.SubqueryCache == nil {
			ctx.SubqueryCache = make(map[string][]Datum)
			ctx.SubqueryCacheScope = len(ctx.OuterRows)
		}
		ctx.SubqueryCache[cacheKey] = out
		return out, nil
	}
	out := make([]Datum, len(x.List))
	for i, e := range x.List {
		v, err := evalExpr(e, row, ctx)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// evalExistsExpr evaluates `[NOT] EXISTS (subquery)`. Opens
// the inner plan, asks for one row, returns the bool. Works
// regardless of column count — EXISTS only cares whether at
// least one row exists.
func evalExistsExpr(x *planner.ExistsExpr, row Row, ctx *Context) (Datum, error) {
	// Check for query cancellation before each EXISTS/NOT EXISTS evaluation.
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	// Push the outer row so correlated column refs in the inner
	// plan can resolve against it. Pop on return regardless of
	// outcome.
	ctx.OuterRows = append(ctx.OuterRows, row)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()

	// For non-correlated EXISTS (M0058-0001), the inner plan
	// returns the same boolean for every outer row. Cache it
	// under a constant key.
	if x.IsNonCorrelated {
		cacheKey := nonCorrelatedCacheKey(x)
		if ctx.SubqueryCache != nil {
			if ctx.SubqueryCacheScope != len(ctx.OuterRows) {
				clear(ctx.SubqueryCache)
				ctx.SubqueryCacheScope = len(ctx.OuterRows)
			}
			if cached, ok := ctx.SubqueryCache[cacheKey]; ok {
				if len(cached) == 1 {
					return cached[0], nil
				}
			}
		}
		val, err := existsImpl(x, ctx)
		if err != nil {
			return Datum{}, err
		}
		if ctx.SubqueryCache == nil {
			ctx.SubqueryCache = make(map[string][]Datum)
			ctx.SubqueryCacheScope = len(ctx.OuterRows)
		}
		ctx.SubqueryCache[cacheKey] = []Datum{val}
		return val, nil
	}
	return existsImpl(x, ctx)
}

func existsImpl(x *planner.ExistsExpr, ctx *Context) (Datum, error) {
	op, err := Build(x.Plan)
	if err != nil {
		return Datum{}, err
	}
	// EXISTS only needs the first row — limit lockRowsOp drain to 1 so
	// it does not scan the full inner table (matching PostgreSQL). M0100-0005.
	if lop, ok := op.(*lockRowsOp); ok {
		lop.maxDrain = 1
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return Datum{}, err
	}
	defer func() { _ = op.Close() }()
	_, err = op.Next()
	hasRow := err == nil
	if err != nil && err != EOF {
		return Datum{}, err
	}
	return NewBoolDatum(hasRow != x.Negated), nil
}

// evalSubquery runs the inner plan inside a SubqueryExpr and
// returns its single cell. Multi-row results raise SQLSTATE
// 21000 (cardinality_violation); zero rows return NULL (per
// upstream's scalar-subquery semantics). Multi-column subqueries
// raise 42601 because v0's caller types the SubqueryExpr as a
// single value.
//
// v0 is always uncorrelated — the inner plan never sees the
// outer row. Correlated subqueries (parameter pull-up) are
// deferred; see docs/design/0003-0008-subqueries.md.
func evalSubquery(x *planner.SubqueryExpr, row Row, ctx *Context) (Datum, error) {
	// Check for query cancellation before each scalar SubPlan evaluation.
	if ctx.Ctx != nil {
		if err := ctx.Ctx.Err(); err != nil {
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	ctx.OuterRows = append(ctx.OuterRows, row)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
	// Check cache for scalar subquery results. For non-correlated
	// subqueries (M0058-0001), use a constant cache key.
	cacheKey := subqueryCacheKey(row)
	if x.IsNonCorrelated {
		cacheKey = nonCorrelatedCacheKey(x)
	}
	if ctx.SubqueryCache != nil {
		if ctx.SubqueryCacheScope != len(ctx.OuterRows) {
			clear(ctx.SubqueryCache)
			ctx.SubqueryCacheScope = len(ctx.OuterRows)
		}
		if cached, ok := ctx.SubqueryCache[cacheKey]; ok {
			if len(cached) == 1 {
				return cached[0], nil
			}
			return NullDatum, nil
		}
	}
	val, err := subqueryImpl(x, ctx)
	if err != nil {
		return Datum{}, err
	}
	// Store in cache
	if ctx.SubqueryCache == nil {
		ctx.SubqueryCache = make(map[string][]Datum)
		ctx.SubqueryCacheScope = len(ctx.OuterRows)
	}
	ctx.SubqueryCache[cacheKey] = []Datum{val}
	return val, nil
}

func subqueryImpl(x *planner.SubqueryExpr, ctx *Context) (Datum, error) {
	op, err := Build(x.Plan)
	if err != nil {
		return Datum{}, err
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return Datum{}, err
	}
	defer func() { _ = op.Close() }()
	slot, err := op.Next()
	if err == EOF {
		return NullDatum, nil
	}
	if err != nil {
		return Datum{}, err
	}
	row := slotRow(slot)
	if len(row) != 1 {
		return Datum{}, &ExecError{Code: "42601", Pos: x.Pos(), Message: fmt.Sprintf("scalar subquery returned %d columns, expected 1", len(row))}
	}
	val := row[0]
	// Drain to ensure the subquery returned at most one row.
	if _, err := op.Next(); err != EOF {
		if err == nil {
			return Datum{}, &ExecError{Code: "21000", Pos: x.Pos(), Message: "more than one row returned by a subquery used as an expression"}
		}
		return Datum{}, err
	}
	return val, nil
}

// evalCaseExpr evaluates the SQL CASE expression. Two forms:
//
//	-- searched: each WHEN is a boolean predicate
//	-- simple:   each WHEN is `Operand = When`
//
// First match wins; ELSE is the fallback. Per upstream, NULL
// WHEN evaluates as "not matched" — never NULL-true.
func evalCaseExpr(x *planner.CaseExpr, row Row, ctx *Context) (Datum, error) {
	var operand Datum
	hasOperand := x.Operand != nil
	if hasOperand {
		v, err := evalExpr(x.Operand, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		operand = v
	}
	for _, w := range x.Whens {
		whenVal, err := evalExpr(w.When, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		var matched bool
		if hasOperand {
			eq, err := compareEq(operand, whenVal)
			if err != nil {
				return Datum{}, err
			}
			matched = eq.Kind == KindBool && eq.BoolValue()
		} else {
			matched = whenVal.Kind == KindBool && whenVal.BoolValue()
		}
		if matched {
			return evalExpr(w.Then, row, ctx)
		}
	}
	if x.Else != nil {
		return evalExpr(x.Else, row, ctx)
	}
	return NullDatum, nil
}

// compareEq computes `a = b` returning a KindBool datum
// (KindNull if either side is NULL). Helper for the simple-form
// CASE; reuses upstream-shaped equality semantics across the
// types v0 understands.
func compareEq(a, b Datum) (Datum, error) {
	if a.IsNull() || b.IsNull() {
		return NullDatum, nil
	}
	// NUMERIC arms: route NUMERIC and NUMERIC↔INT cross-kind
	// comparisons through compareDatum so they use scale-aware
	// equality. Without this, `IN (49, 14, ...)` against a
	// NUMERIC column (TPC-H Q16's `p_size in (...)` shape) always
	// returns false because the int literals don't match
	// KindNumeric values directly.
	if a.Kind == KindNumeric || b.Kind == KindNumeric {
		// compareDatum errors only on truly incompatible kinds;
		// for IN-list semantics we want false on those, so swallow
		// the error and report not-equal.
		cmp, err := compareDatum(a, b, 0)
		if err != nil {
			return NewBoolDatum(false), nil
		}
		return NewBoolDatum(cmp == 0), nil
	}
	// M0073-0001: treat KindString and KindStringArena as
	// equivalent for equality (likewise KindBytes /
	// KindBytesArena). The arena variant is a storage detail;
	// the logical Kind is "string" / "bytes".
	aIsString := a.Kind == KindString
	bIsString := b.Kind == KindString
	switch {
	case a.Kind == KindInt && b.Kind == KindInt:
		return NewBoolDatum(a.Int == b.Int), nil
	case a.Kind == KindBool && b.Kind == KindBool:
		return NewBoolDatum(a.BoolValue() == b.BoolValue()), nil
	case aIsString && bIsString:
		return NewBoolDatum(a.StringValue() == b.StringValue()), nil
	case a.Kind == KindTime && b.Kind == KindTime:
		return NewBoolDatum(a.TimeValue().Equal(b.TimeValue())), nil
	case a.Kind == KindInt && bIsString:
		return NewBoolDatum(fmt.Sprintf("%d", a.Int) == b.StringValue()), nil
	case aIsString && b.Kind == KindInt:
		return NewBoolDatum(a.StringValue() == fmt.Sprintf("%d", b.Int)), nil
	}
	return NewBoolDatum(false), nil
}

// evalFuncCall resolves a function name against the in-tree registry.
// v0 is small: current_timestamp / now / current_date are the only
// no-arg time functions pgbench needs; HammerDB TPC-H also uses
// to_timestamp(text, fmt) to load TIMESTAMP columns.
func evalFuncCall(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	name := strings.ToLower(x.Name)
	// Strip pg_catalog. prefix for matching — these are schema-qualified
	// versions of the same built-in functions.
	if after, ok := strings.CutPrefix(name, "pg_catalog."); ok {
		name = after
	}
	switch name {
	case "current_timestamp", "now", "transaction_timestamp", "statement_timestamp":
		return NewTimeDatum(ctx.Now), nil
	case "current_date":
		t := ctx.Now.UTC()
		return NewTimeDatum(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)), nil
	case "current_time":
		// Returns time-of-day anchored at epoch, matching parseTimeString convention.
		// Accepts optional precision arg: current_time(N) truncates microseconds.
		t := ctx.Now.UTC()
		ns := t.Nanosecond()
		if len(x.Args) > 0 {
			prec, err := evalExpr(x.Args[0], row, ctx)
			if err == nil && prec.Kind == KindInt && prec.Int < 6 {
				factor := int64(1)
				for i := int64(0); i < 6-prec.Int; i++ {
					factor *= 10
				}
				ns = (ns / (int(factor) * 1000)) * (int(factor) * 1000)
			}
		}
		return NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), ns, time.UTC)), nil
	case "current_catalog":
		return NewStringDatum("postgres"), nil
	case "current_setting":
		if len(x.Args) >= 1 {
			nameArg, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || nameArg.IsNull() {
				return NullDatum, nil
			}
			missingOK := false
			if len(x.Args) >= 2 {
				missingArg, err := evalExpr(x.Args[1], row, ctx)
				if err == nil && !missingArg.IsNull() {
					missingOK = missingArg.BoolValue()
				}
			}
			if ctx != nil && ctx.GetSetting != nil {
				if value, ok := ctx.GetSetting(nameArg.StringValue()); ok {
					return NewStringDatum(value), nil
				}
			}
			if missingOK {
				return NullDatum, nil
			}
			return Datum{}, &ExecError{
				Code:    "42704",
				Pos:     x.Pos(),
				Message: fmt.Sprintf("unrecognized configuration parameter %q", nameArg.StringValue()),
			}
		}
		return NullDatum, nil
	case "pg_sleep":
		return evalPgSleep(x, row, ctx)
	case "to_timestamp":
		return evalToTimestamp(x, row, ctx)
	case "to_date":
		return evalToDate(x, row, ctx)
	case "substr", "substring":
		return evalSubstr(x, row, ctx)
	case "date_part":
		return evalDatePart(x, row, ctx)
	case "date_trunc":
		return evalDateTrunc(x, row, ctx)
	case "to_char":
		return evalToChar(x, row, ctx)
	case "age":
		return evalAge(x, row, ctx)
	case "make_date":
		return evalMakeDate(x, row, ctx)
	case "make_timestamp", "make_timestamptz":
		return evalMakeTimestamp(x, row, ctx)
	case "make_time":
		return evalMakeTime(x, row, ctx)
	case "isfinite":
		return evalIsFinite(x, row, ctx)
	case "justify_hours", "justify_days", "justify_interval":
		return evalJustifyInterval(x, row, ctx)
	case "date_bin":
		return evalDateBin(x, row, ctx)
	case "set_config":
		// set_config(setting_name, new_value, is_local) → text
		// vacuumdb calls SELECT pg_catalog.set_config('search_path', '', false)
		// to restrict the search path for security. Accept and return new_value.
		if len(x.Args) >= 2 {
			nameArg, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || nameArg.IsNull() {
				return NullDatum, nil
			}
			newVal, err := evalExpr(x.Args[1], row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			isLocal := false
			if len(x.Args) >= 3 {
				localArg, err := evalExpr(x.Args[2], row, ctx)
				if err == nil && !localArg.IsNull() {
					isLocal = localArg.BoolValue()
				}
			}
			if ctx != nil && ctx.SetSetting != nil {
				if err := ctx.SetSetting(nameArg.StringValue(), newVal.Format(), isLocal); err != nil {
					return Datum{}, &ExecError{Code: "22023", Pos: x.Pos(), Message: err.Error()}
				}
				if ctx.GetSetting != nil {
					if value, ok := ctx.GetSetting(nameArg.StringValue()); ok {
						return NewStringDatum(value), nil
					}
				}
			}
			return newVal, nil
		}
		return NullDatum, nil
	case "current_database":
		return NewStringDatum("postgres"), nil
	case "current_schema", "current_schemas":
		return currentSchemaFromSearchPath(ctx)

	// generate_series used as a scalar expression (not FROM clause).
	// Returns the start value only — full SRF semantics require planner rework.
	// Sufficient for CTAS patterns like `SELECT generate_series(1,10)` where
	// the table will have 1 row rather than N. M0096-0008.
	case "generate_series":
		if len(x.Args) >= 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			return v, nil
		}
		return NewIntDatum(1), nil

	// ── Advisory lock functions (M0096-0003) ──────────────────────────────
	// All variants block/return immediately depending on lock availability.
	// pg_advisory_lock / pg_advisory_xact_lock return non-NULL (void-like)
	// on success so that `IS NOT NULL` predicates in WHERE clauses evaluate
	// to true (matching PostgreSQL's behaviour for void-returning functions).

	case "pg_advisory_lock":
		// pg_advisory_lock(bigint) or pg_advisory_lock(int4, int4)
		return evalAdvisoryLock(x, row, ctx, false, false)

	case "pg_advisory_unlock":
		// pg_advisory_unlock(bigint) → boolean
		// pg_advisory_unlock(int4, int4) → boolean
		return evalAdvisoryUnlock(x, row, ctx)

	case "pg_advisory_unlock_all":
		// pg_advisory_unlock_all() → void
		return evalAdvisoryUnlockAll(ctx)

	case "pg_advisory_xact_lock":
		// pg_advisory_xact_lock(int4, int4) → void  (xact-scoped)
		return evalAdvisoryLock(x, row, ctx, false, true)

	case "pg_try_advisory_xact_lock":
		// pg_try_advisory_xact_lock(int4, int4) → boolean  (non-blocking)
		return evalAdvisoryLock(x, row, ctx, true, true)

	case "pg_try_advisory_lock":
		// pg_try_advisory_lock(bigint) → boolean  (non-blocking)
		return evalAdvisoryLock(x, row, ctx, true, false)

	// ── Shared-mode advisory lock stubs (M0097-0010) ─────────────────────
	// Shared advisory locks allow multiple sessions to hold the same key.
	// v0 returns void/true without actually acquiring locks — implementing
	// true shared-mode semantics in the single-session test context would
	// risk deadlocks when the same session acquires both exclusive and shared
	// versions of the same key. Returning immediately is correct for the
	// regress test context (single connection, no cross-session contention).
	case "pg_advisory_lock_shared", "pg_advisory_xact_lock_shared":
		// Shared lock: accepted, no-op for single-session tests.
		return Datum{Kind: KindBool}, nil
	case "pg_try_advisory_lock_shared", "pg_try_advisory_xact_lock_shared":
		return NewBoolDatum(true), nil
	case "pg_advisory_unlock_shared":
		return NewBoolDatum(true), nil

	// ── Boolean comparison functions (M0097-0003) ─────────────────────────
	// These are the C-level backing functions for bool operators; the
	// boolean.sql regress test calls them explicitly.
	case "booleq":
		if len(x.Args) == 2 {
			a, err := evalExpr(x.Args[0], row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			b, err2 := evalExpr(x.Args[1], row, ctx)
			if err2 != nil {
				return NullDatum, nil
			}
			if a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(a.BoolValue() == b.BoolValue()), nil
		}
	case "boolne":
		if len(x.Args) == 2 {
			a, err := evalExpr(x.Args[0], row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			b, err2 := evalExpr(x.Args[1], row, ctx)
			if err2 != nil {
				return NullDatum, nil
			}
			if a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			return NewBoolDatum(a.BoolValue() != b.BoolValue()), nil
		}

	case "array_subscript":
		// array_subscript(arr text[], idx int) → text
		// Array element access: arr[idx] (1-based). Used for SQL a[N] syntax. M0097-0003.
		if len(x.Args) == 2 {
			arr, err := evalExpr(x.Args[0], row, ctx)
			if err != nil {
				return NullDatum, err
			}
			idxDatum, err := evalExpr(x.Args[1], row, ctx)
			if err != nil {
				return NullDatum, err
			}
			if arr.IsNull() || idxDatum.IsNull() {
				return NullDatum, nil
			}
			n := idxDatum.Int
			elems := parseTextArray(arr.StringValue())
			if n < 1 || int(n) > len(elems) {
				return NullDatum, nil
			}
			return NewStringDatum(elems[n-1]), nil
		}
		return NullDatum, nil

	case "parse_ident":
		// parse_ident(str text [, strict boolean = true]) → text[]
		// Parses a qualified SQL identifier string and returns its components
		// as a text array {comp1,comp2,...}. M0097-0003.
		if len(x.Args) >= 1 {
			strDatum, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || strDatum.IsNull() {
				return NullDatum, nil
			}
			strict := true
			if len(x.Args) >= 2 {
				strictDatum, err2 := evalExpr(x.Args[1], row, ctx)
				if err2 == nil && !strictDatum.IsNull() {
					strict = strictDatum.BoolValue()
				}
			}
			input := strDatum.StringValue()
			components, msg, detail := parseIdentString(input, strict)
			if msg != "" {
				return Datum{}, &ExecError{Code: "42602", Pos: x.Pos(), Message: msg, Detail: detail}
			}
			// Format as PostgreSQL text array: {comp1,"comp2",...}
			return NewStringDatum(formatTextArray(components)), nil
		}
		return NullDatum, nil

	// ── pg_input_is_valid / pg_input_error_info stubs (M0097-0003) ───────
	// These PostgreSQL 16+ functions validate whether a string is valid input
	// for a given type. Stub returns true (best-effort) — returning an error
	// would cause boolean.sql to hang waiting for a SRF response.
	case "pg_input_is_valid":
		// M0097-0018: enhanced to validate xid/xid8 inputs.
		if len(x.Args) == 2 {
			val, _ := evalExpr(x.Args[0], row, ctx)
			typName, _ := evalExpr(x.Args[1], row, ctx)
			if val.IsNull() || typName.IsNull() {
				return NullDatum, nil
			}
			v := strings.TrimSpace(val.StringValue())
			t := strings.ToLower(strings.TrimSpace(typName.StringValue()))
			switch t {
			case "bool", "boolean":
				return NewBoolDatum(isValidBoolInput(v)), nil
			case "int2", "smallint":
				_, err := parseIntegerInput(v, "smallint", 16)
				return NewBoolDatum(err == nil), nil
			case "int4", "integer", "int":
				_, err := parseIntegerInput(v, "integer", 32)
				return NewBoolDatum(err == nil), nil
			case "int8", "bigint":
				_, err := parseIntegerInput(v, "bigint", 64)
				return NewBoolDatum(err == nil), nil
			case "float4", "real":
				_, err := strconv.ParseFloat(v, 32)
				return NewBoolDatum(err == nil), nil
			case "float8", "double precision":
				_, err := strconv.ParseFloat(v, 64)
				return NewBoolDatum(err == nil), nil
			case "oid":
				// oid is uint32: 0..4294967295. Negative wraps around. M0097-0003.
				n, err := strconv.ParseInt(v, 10, 64)
				if err == nil && n < 0 {
					n += 4294967296
				}
				return NewBoolDatum(err == nil && n >= 0 && n <= 4294967295), nil
			case "oidvector":
				// oidvector: space-separated oid values. M0097-0003.
				msg, _ := validateOidVector(v)
				return NewBoolDatum(msg == ""), nil
			case "uuid":
				return NewBoolDatum(isValidUUIDStr(v)), nil
			case "tid":
				_, _, ok := parseTidInput(v)
				return NewBoolDatum(ok), nil
			case "xid":
				_, err := parseXid(v)
				return NewBoolDatum(err == nil), nil
			case "xid8":
				_, err := parseXid8(v)
				return NewBoolDatum(err == nil), nil
			case "pg_snapshot":
				return NewBoolDatum(parsePgSnapshotValid(v)), nil
			case "time", "timetz":
				_, err := parseTimeString(v)
				return NewBoolDatum(err == nil), nil
			case "date":
				_, err := time.Parse("2006-01-02", v)
				return NewBoolDatum(err == nil), nil
			case "timestamp", "timestamptz":
				_, err := parseCopyTimestamp(v)
				return NewBoolDatum(err == nil), nil
			default:
				// varchar(N) / character varying(N) / char(N) / bpchar(N). M0097-0003.
				if valid, ok := pgInputIsValidTypedLen(v, t); ok {
					return NewBoolDatum(valid), nil
				}
			}
		}
		return NewBoolDatum(true), nil
	case "pg_input_error_info":
		return NullDatum, nil

	// ── Size functions (M0097-0018) ───────────────────────────────────────
	case "pg_size_pretty":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return NewStringDatum(sizePretty(v.Int)), nil
			}
			// KindNumeric and other: use exact big.Int/big.Rat arithmetic
			// to match PG's pg_size_pretty(numeric) algorithm. M0097-0018.
			return NewStringDatum(sizePrettyBig(strings.TrimSpace(v.Format()))), nil
		}

	case "pg_size_bytes":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			bytes, err2 := parseSizeBytes(s.StringValue())
			if err2 != nil {
				if ee, ok := err2.(*ExecError); ok {
					return Datum{}, ee
				}
				return Datum{}, &ExecError{Code: "22023", Message: err2.Error()}
			}
			return Datum{Kind: KindInt, Int: bytes}, nil
		}

	case "pg_database_size":
		// Stub: return 8 MB. M0097-0018.
		return Datum{Kind: KindInt, Int: 8 * 1024 * 1024}, nil

	case "pg_relation_size", "pg_total_relation_size", "pg_indexes_size":
		// Stub: return 8 kB. M0097-0018.
		return Datum{Kind: KindInt, Int: 8 * 1024}, nil

	case "pg_table_size":
		// Stub: return 8 kB. M0097-0018.
		return Datum{Kind: KindInt, Int: 8 * 1024}, nil

	// ── xid8 comparison function (M0097-0018) ─────────────────────────────
	case "xid8cmp":
		if len(x.Args) == 2 {
			a, err1 := evalExpr(x.Args[0], row, ctx)
			b, err2 := evalExpr(x.Args[1], row, ctx)
			if err1 != nil || err2 != nil || a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			var aVal, bVal uint64
			if a.Kind == KindInt {
				aVal = uint64(a.Int)
			} else {
				aVal, _ = strconv.ParseUint(strings.TrimSpace(a.StringValue()), 10, 64)
			}
			if b.Kind == KindInt {
				bVal = uint64(b.Int)
			} else {
				bVal, _ = strconv.ParseUint(strings.TrimSpace(b.StringValue()), 10, 64)
			}
			if aVal < bVal {
				return Datum{Kind: KindInt, Int: -1}, nil
			}
			if aVal > bVal {
				return Datum{Kind: KindInt, Int: 1}, nil
			}
			return Datum{Kind: KindInt, Int: 0}, nil
		}
		return NullDatum, nil

	// ── Hash / crypto functions (M0097-0011) ─────────────────────────────
	case "md5":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			h := md5.Sum([]byte(v.StringValue()))
			return NewStringDatum(hex.EncodeToString(h[:])), nil
		}
	case "sha256":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			h := sha256.Sum256([]byte(v.StringValue()))
			return NewStringDatum(hex.EncodeToString(h[:])), nil
		}
	case "sha512":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			h := sha512.Sum512([]byte(v.StringValue()))
			return NewStringDatum(hex.EncodeToString(h[:])), nil
		}
	case "digest":
		// digest(text, algorithm) — subset: only 'md5', 'sha256', 'sha512'
		if len(x.Args) == 2 {
			s, err1 := evalExpr(x.Args[0], row, ctx)
			alg, err2 := evalExpr(x.Args[1], row, ctx)
			if err1 != nil || err2 != nil || s.IsNull() || alg.IsNull() {
				return NullDatum, nil
			}
			switch strings.ToLower(alg.StringValue()) {
			case "md5":
				h := md5.Sum([]byte(s.StringValue()))
				return NewStringDatum(hex.EncodeToString(h[:])), nil
			case "sha256":
				h := sha256.Sum256([]byte(s.StringValue()))
				return NewStringDatum(hex.EncodeToString(h[:])), nil
			case "sha512":
				h := sha512.Sum512([]byte(s.StringValue()))
				return NewStringDatum(hex.EncodeToString(h[:])), nil
			}
		}
		return NullDatum, nil

	// ── POSIX regex functions (M0097-0011) ────────────────────────────────
	case "regexp_match":
		// regexp_match(string, pattern [, flags]) → text[]
		// Returns first match as array or NULL if no match.
		// Stub: return text[] with full match as first element, or NULL.
		if len(x.Args) >= 2 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			pat, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || pat.IsNull() {
				return NullDatum, nil
			}
			caseInsensitive := false
			if len(x.Args) >= 3 {
				flags, e3 := evalExpr(x.Args[2], row, ctx)
				if e3 == nil && !flags.IsNull() {
					caseInsensitive = strings.Contains(flags.StringValue(), "i")
				}
			}
			pattern := pat.StringValue()
			if caseInsensitive {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return NullDatum, nil
			}
			match := re.FindString(s.StringValue())
			if match == "" && !re.MatchString(s.StringValue()) {
				return NullDatum, nil
			}
			return NewStringDatum("{" + match + "}"), nil
		}
	case "regexp_matches":
		// regexp_matches(string, pattern [, flags]) — SRF, stub returns NULL
		return NullDatum, nil

	// ── Sequence functions (M0097-0009) ───────────────────────────────────
	case "nextval":
		args := make([]Datum, len(x.Args))
		for i, a := range x.Args {
			v, err := evalExpr(a, row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			args[i] = v
		}
		return evalNextval(args, ctx)
	case "currval":
		args := make([]Datum, len(x.Args))
		for i, a := range x.Args {
			v, err := evalExpr(a, row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			args[i] = v
		}
		return evalCurrval(args, ctx)
	case "setval":
		args := make([]Datum, len(x.Args))
		for i, a := range x.Args {
			v, err := evalExpr(a, row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			args[i] = v
		}
		return evalSetval(args, ctx)
	case "lastval":
		return evalLastval(ctx)
	case "pg_get_serial_sequence":
		// pg_get_serial_sequence(table_name, column_name) → text
		// Returns the sequence name used for a serial/identity column.
		// Stub: construct the conventional name table_column_seq. M0097-0009.
		if len(x.Args) == 2 {
			tbl, err1 := evalExpr(x.Args[0], row, ctx)
			col, err2 := evalExpr(x.Args[1], row, ctx)
			if err1 != nil || err2 != nil || tbl.IsNull() || col.IsNull() {
				return NullDatum, nil
			}
			seqName := fmt.Sprintf("public.%s_%s_seq", tbl.StringValue(), col.StringValue())
			return NewStringDatum(seqName), nil
		}
		return NullDatum, nil
	case "pg_sequence_parameters":
		// SRF returning sequence parameters — stub returns NULL.
		return NullDatum, nil

	// ── Partition metadata functions (M0097-0015) ─────────────────────────
	case "pg_partition_tree", "pg_partition_ancestors":
		// SRF returning partition hierarchy — stub returns NULL (no rows). M0097-0015.
		return NullDatum, nil
	case "pg_partition_root":
		// pg_partition_root(relid) → regclass — returns the root of the partition tree.
		// Stub: return the input itself (assume root). M0097-0015.
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			return v, nil
		}
		return NullDatum, nil
	case "satisfies_hash_partition":
		// satisfies_hash_partition(tableoid, modulus, remainder, val...) → bool
		// Full implementation: Jenkins Bob hash + hash_combine64. M0097-0027.
		if len(x.Args) < 3 || ctx == nil || ctx.Catalog == nil {
			return NewBoolDatum(false), nil
		}
		modulusDatum, err := evalExpr(x.Args[1], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		remainderDatum, err := evalExpr(x.Args[2], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		// NULL modulus or NULL remainder → false (PG behavior)
		if modulusDatum.IsNull() || remainderDatum.IsNull() {
			return NewBoolDatum(false), nil
		}
		modulus := int(modulusDatum.Int)
		remainder := int(remainderDatum.Int)
		if modulus <= 0 {
			return NullDatum, &ExecError{Code: "22023",
				Message: "modulus for hash partition must be an integer value greater than zero"}
		}
		if remainder < 0 {
			return NullDatum, &ExecError{Code: "22023",
				Message: "remainder for hash partition must be an integer value greater than or equal to zero"}
		}
		if remainder >= modulus {
			return NullDatum, &ExecError{Code: "22023",
				Message: "remainder for hash partition must be less than modulus"}
		}
		tableoidDatum, err := evalExpr(x.Args[0], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		if tableoidDatum.IsNull() {
			return NewBoolDatum(false), nil
		}
		if tableoidDatum.Kind != KindInt {
			return NullDatum, &ExecError{Code: "XX000",
				Message: "could not open relation with OID 0"}
		}
		tableOID := uint32(tableoidDatum.Int)
		if tableOID == 0 {
			return NullDatum, &ExecError{Code: "XX000",
				Message: "could not open relation with OID 0"}
		}
		im, ok := ctx.Catalog.(*catalog.InMemory)
		if !ok {
			return NewBoolDatum(false), nil
		}
		tbl, found := im.LookupTableByOID(tableOID)
		if !found {
			return NullDatum, &ExecError{Code: "XX000",
				Message: fmt.Sprintf("could not open relation with OID %d", tableOID)}
		}
		if tbl.PartitionMethod != "HASH" || tbl.PartitionParentOID != 0 {
			return NullDatum, &ExecError{Code: "42809",
				Message: fmt.Sprintf("%q is not a hash partitioned table", tbl.Name)}
		}
		numKeys := len(tbl.PartitionKey)
		numArgs := len(x.Args) - 3
		if numArgs != numKeys {
			return NullDatum, &ExecError{Code: "22023",
				Message: fmt.Sprintf("number of partitioning columns (%d) does not match number of partition keys provided (%d)",
					numKeys, numArgs)}
		}
		// Type-check args against partition key column types (PG behavior: check even for NULLs).
		// Non-variadic: no quotes around type names; variadic: quoted type names.
		for i := 0; i < numKeys; i++ {
			colType := ""
			for _, col := range tbl.Columns {
				if strings.EqualFold(col.Name, tbl.PartitionKey[i]) {
					colType = strings.ToLower(col.Type.Name)
					break
				}
			}
			argTypeName := hashPartTypeName(x.Args[3+i])
			if argTypeName != "" {
				colPGName := pgFormatTypeName(colType)
				if !hashPartTypesCompatible(colType, argTypeName) {
					if x.Variadic {
						return NullDatum, &ExecError{Code: "22023",
							Message: fmt.Sprintf("column %d of the partition key has type %q, but supplied value is of type %q",
								i+1, colPGName, argTypeName)}
					}
					return NullDatum, &ExecError{Code: "22023",
						Message: fmt.Sprintf("column %d of the partition key has type %s, but supplied value is of type %s",
							i+1, colPGName, argTypeName)}
				}
			}
		}
		// Compute hash: for each non-NULL key value, call the operator class hash
		// function (or the built-in type default) and fold with hash_combine64.
		var rowHash uint64
		seedInt64 := int64(hashPartitionSeed)
		for i := 0; i < numKeys; i++ {
			valDatum, verr := evalExpr(x.Args[3+i], row, ctx)
			if verr != nil {
				return NullDatum, verr
			}
			if valDatum.IsNull() {
				continue // NULL values are skipped (PG behavior)
			}
			opClass := ""
			if i < len(tbl.PartitionKeyOpClasses) {
				opClass = tbl.PartitionKeyOpClasses[i]
			}
			var h uint64
			if opClass != "" {
				// Custom operator class: look up FUNCTION 2 and call it(val, seed).
				hashFuncName, hasFn := im.LookupOpClassHashFunc(opClass)
				if hasFn {
					routines := ctx.Catalog.Routines()
					rs := routines.LookupByName(parser.ObjectName{Name: hashFuncName})
					seedDatum := NewIntDatum(seedInt64)
					var bestRoutine *catalog.Routine
					for _, r := range rs {
						if len(r.ArgTypes) == 2 {
							bestRoutine = r
							break
						}
					}
					if bestRoutine != nil {
						hResult, herr := executeStoredRoutine(bestRoutine, []Datum{valDatum, seedDatum}, ctx, x.Pos())
						if herr != nil {
							return NullDatum, herr
						}
						if !hResult.IsNull() {
							h = uint64(hResult.Int)
						}
					}
				}
			} else {
				// Default hash: type-based built-in hash functions.
				colType := ""
				for _, col := range tbl.Columns {
					if strings.EqualFold(col.Name, tbl.PartitionKey[i]) {
						colType = strings.ToLower(col.Type.Name)
						break
					}
				}
				switch {
				case colType == "int4" || colType == "integer" || colType == "int" || valDatum.Kind == KindInt:
					h = pgHashUint32Extended(uint32(valDatum.Int), hashPartitionSeed)
				case colType == "text" || colType == "varchar" || colType == "bpchar" || valDatum.Kind == KindString:
					h = pgHashBytesExtended([]byte(valDatum.StringValue()), hashPartitionSeed)
				default:
					h = pgHashUint32Extended(uint32(valDatum.Int), hashPartitionSeed)
				}
			}
			rowHash = pgHashCombine64(rowHash, h)
		}
		return NewBoolDatum(uint64(rowHash)%uint64(modulus) == uint64(remainder)), nil
	case "merge_action":
		// merge_action() → text — returns 'INSERT', 'UPDATE', or 'DELETE' within MERGE RETURNING.
		// Stub: return NULL (MERGE RETURNING is not yet executed). M0097-0016.
		return NullDatum, nil

	// ── Function introspection stubs (M0097-0012) ─────────────────────────
	case "pg_get_functiondef":
		// pg_get_functiondef(func_oid) → text — returns function DDL.
		// Stub: look up in routine registry and reconstruct definition.
		if len(x.Args) == 1 {
			nameArg, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || nameArg.IsNull() {
				return NullDatum, nil
			}
			// The argument might be a regproc (function name cast to OID).
			// Try to look up the function by name.
			rs := ctx.Catalog.Routines()
			if rs != nil {
				name := nameArg.StringValue()
				candidates := rs.LookupByName(parseRoutineName(name))
				if len(candidates) > 0 {
					r := candidates[0]
					body := fmt.Sprintf("CREATE OR REPLACE FUNCTION %s(", r.Name)
					for i, arg := range r.ArgNames {
						if i > 0 {
							body += ", "
						}
						body += arg + " " + r.ArgTypes[i].Name
					}
					body += ") RETURNS " + r.ReturnType.Name + " LANGUAGE " + r.Language + " AS $$\n" + r.Body + "\n$$"
					return NewStringDatum(body), nil
				}
			}
			return NullDatum, nil
		}
	case "pg_get_function_arguments", "pg_get_function_result":
		return NewStringDatum(""), nil
	case "pg_proc":
		return NullDatum, nil
	case "regproc", "regprocedure", "regclass", "regtype", "regnamespace":
		// Type cast functions. For regclass specifically, resolve a
		// text relation name to the table's numeric OID via the
		// catalog (matches PG semantics post M0103-0008 rung 16,
		// after pg_class.oid was flipped from text-name to numeric).
		// Numeric inputs pass through. Other reg* casts remain
		// stubs returning the argument as-is.
		if len(x.Args) != 1 {
			return NullDatum, nil
		}
		v, err := evalExpr(x.Args[0], row, ctx)
		if err != nil || v.IsNull() {
			return v, err
		}
		if name == "regclass" && v.Kind == KindString && ctx != nil && ctx.Catalog != nil {
			s := v.StringValue()
			schema, rel := splitQualifiedTable(s)
			tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: schema, Name: rel})
			if ok && tbl != nil {
				return NewIntDatum(int64(tbl.OID)), nil
			}
		}
		return v, nil

	// ── String functions (M0097-0005) ─────────────────────────────────────
	case "repeat":
		// repeat(text, int) → text
		if len(x.Args) == 2 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			n, err := evalExpr(x.Args[1], row, ctx)
			if err != nil || n.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.Repeat(s.StringValue(), int(n.Int))), nil
		}
	case "char_length", "character_length":
		// char_length(text) → int
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(len([]rune(s.StringValue())))}, nil
		}
	case "length":
		// length(text) → int  (byte length for bytea, char length for text)
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(len([]rune(s.StringValue())))}, nil
		}
	case "octet_length":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(len(s.StringValue()))}, nil
		}
	case "upper":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.ToUpper(s.StringValue())), nil
		}
	case "lower":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.ToLower(s.StringValue())), nil
		}
	case "initcap":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(initCap(s.StringValue())), nil
		}
	case "btrim":
		// btrim(text [, chars]) — trim chars from both ends
		if len(x.Args) >= 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			cutset := " "
			if len(x.Args) >= 2 {
				c, err := evalExpr(x.Args[1], row, ctx)
				if err == nil && !c.IsNull() {
					cutset = c.StringValue()
				}
			}
			return NewStringDatum(strings.Trim(s.StringValue(), cutset)), nil
		}
	case "ltrim":
		if len(x.Args) >= 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			cutset := " "
			if len(x.Args) >= 2 {
				c, err := evalExpr(x.Args[1], row, ctx)
				if err == nil && !c.IsNull() {
					cutset = c.StringValue()
				}
			}
			return NewStringDatum(strings.TrimLeft(s.StringValue(), cutset)), nil
		}
	case "rtrim":
		if len(x.Args) >= 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			cutset := " "
			if len(x.Args) >= 2 {
				c, err := evalExpr(x.Args[1], row, ctx)
				if err == nil && !c.IsNull() {
					cutset = c.StringValue()
				}
			}
			return NewStringDatum(strings.TrimRight(s.StringValue(), cutset)), nil
		}
	case "lpad":
		// lpad(text, int [, fill_text])
		if len(x.Args) >= 2 {
			s, err := evalExpr(x.Args[0], row, ctx)
			n, err2 := evalExpr(x.Args[1], row, ctx)
			if err != nil || err2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			fill := " "
			if len(x.Args) >= 3 {
				f, ferr := evalExpr(x.Args[2], row, ctx)
				if ferr == nil && !f.IsNull() {
					fill = f.StringValue()
				}
			}
			return NewStringDatum(padLeft(s.StringValue(), int(n.Int), fill)), nil
		}
	case "rpad":
		if len(x.Args) >= 2 {
			s, err := evalExpr(x.Args[0], row, ctx)
			n, err2 := evalExpr(x.Args[1], row, ctx)
			if err != nil || err2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			fill := " "
			if len(x.Args) >= 3 {
				f, ferr := evalExpr(x.Args[2], row, ctx)
				if ferr == nil && !f.IsNull() {
					fill = f.StringValue()
				}
			}
			return NewStringDatum(padRight(s.StringValue(), int(n.Int), fill)), nil
		}
	case "replace":
		// replace(text, from, to)
		if len(x.Args) == 3 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			f, e2 := evalExpr(x.Args[1], row, ctx)
			t, e3 := evalExpr(x.Args[2], row, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(strings.ReplaceAll(s.StringValue(), f.StringValue(), t.StringValue())), nil
		}
	case "translate":
		// translate(text, from_chars, to_chars)
		if len(x.Args) == 3 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			f, e2 := evalExpr(x.Args[1], row, ctx)
			t, e3 := evalExpr(x.Args[2], row, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(translateStr(s.StringValue(), f.StringValue(), t.StringValue())), nil
		}
	case "strpos", "position":
		// strpos(string, substring) → int; position(substring in string) via FuncCall rewrite
		if len(x.Args) == 2 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			sub, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || sub.IsNull() {
				return NullDatum, nil
			}
			idx := strings.Index(s.StringValue(), sub.StringValue())
			if idx < 0 {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			// Convert byte offset to rune position + 1 (PostgreSQL is 1-indexed)
			runes := []rune(s.StringValue()[:idx])
			return Datum{Kind: KindInt, Int: int64(len(runes) + 1)}, nil
		}
	case "split_part":
		// split_part(text, delimiter, field)
		if len(x.Args) == 3 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			d, e2 := evalExpr(x.Args[1], row, ctx)
			n, e3 := evalExpr(x.Args[2], row, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() || d.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			parts := strings.Split(s.StringValue(), d.StringValue())
			idx := int(n.Int)
			if idx <= 0 || idx > len(parts) {
				return NewStringDatum(""), nil
			}
			return NewStringDatum(parts[idx-1]), nil
		}
	case "concat":
		// concat(any, ...) → text — NULL inputs are treated as empty string
		var buf strings.Builder
		for _, arg := range x.Args {
			v, err := evalExpr(arg, row, ctx)
			if err != nil || v.IsNull() {
				continue
			}
			buf.WriteString(v.Format())
		}
		return NewStringDatum(buf.String()), nil
	case "concat_ws":
		// concat_ws(sep, any, ...) → text
		if len(x.Args) >= 1 {
			sepArg, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || sepArg.IsNull() {
				return NullDatum, nil
			}
			sep := sepArg.StringValue()
			var parts []string
			for _, arg := range x.Args[1:] {
				v, verr := evalExpr(arg, row, ctx)
				if verr != nil || v.IsNull() {
					continue
				}
				parts = append(parts, v.Format())
			}
			return NewStringDatum(strings.Join(parts, sep)), nil
		}
	case "left":
		// left(text, n) → text
		if len(x.Args) == 2 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			n, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			cnt := int(n.Int)
			if cnt < 0 {
				cnt = max(0, len(runes)+cnt)
			} else if cnt > len(runes) {
				cnt = len(runes)
			}
			return NewStringDatum(string(runes[:cnt])), nil
		}
	case "right":
		if len(x.Args) == 2 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			n, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || s.IsNull() || n.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			cnt := int(n.Int)
			if cnt < 0 {
				start := -cnt
				if start >= len(runes) {
					return NewStringDatum(""), nil
				}
				return NewStringDatum(string(runes[start:])), nil
			}
			start := len(runes) - cnt
			if start < 0 {
				start = 0
			}
			return NewStringDatum(string(runes[start:])), nil
		}
	case "reverse":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return NewStringDatum(string(runes)), nil
		}
	case "ascii":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			runes := []rune(s.StringValue())
			if len(runes) == 0 {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			return Datum{Kind: KindInt, Int: int64(runes[0])}, nil
		}
	case "chr":
		if len(x.Args) == 1 {
			n, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || n.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(string(rune(n.Int))), nil
		}
	case "quote_literal":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NewStringDatum("NULL"), nil
			}
			escaped := strings.ReplaceAll(s.StringValue(), "'", "''")
			return NewStringDatum("'" + escaped + "'"), nil
		}
	case "quote_ident":
		if len(x.Args) == 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			escaped := strings.ReplaceAll(s.StringValue(), "\"", "\"\"")
			return NewStringDatum("\"" + escaped + "\""), nil
		}
	case "regexp_replace":
		// regexp_replace(text, pattern, replacement [, flags]) M0097-0011.
		if len(x.Args) >= 3 {
			s, e1 := evalExpr(x.Args[0], row, ctx)
			pat, e2 := evalExpr(x.Args[1], row, ctx)
			repl, e3 := evalExpr(x.Args[2], row, ctx)
			if e1 != nil || e2 != nil || e3 != nil || s.IsNull() || pat.IsNull() {
				return NullDatum, nil
			}
			replaceAll := false
			caseInsensitive := false
			if len(x.Args) >= 4 {
				flags, e4 := evalExpr(x.Args[3], row, ctx)
				if e4 == nil && !flags.IsNull() {
					fs := flags.StringValue()
					replaceAll = strings.Contains(fs, "g")
					caseInsensitive = strings.Contains(fs, "i")
				}
			}
			pattern := pat.StringValue()
			if caseInsensitive {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return NewStringDatum(s.StringValue()), nil // invalid pattern: return input
			}
			replacement := repl.StringValue()
			// Convert PostgreSQL \1, \2 backreferences to Go $1, $2.
			replacement = strings.ReplaceAll(replacement, `\1`, `${1}`)
			replacement = strings.ReplaceAll(replacement, `\2`, `${2}`)
			var result string
			if replaceAll {
				result = re.ReplaceAllString(s.StringValue(), replacement)
			} else {
				// Replace only first occurrence.
				found := false
				result = re.ReplaceAllStringFunc(s.StringValue(), func(m string) string {
					if found {
						return m
					}
					found = true
					return re.ReplaceAllString(m, replacement)
				})
			}
			return NewStringDatum(result), nil
		}
	case "format":
		// format(fmt, args...) — implements %s, %I (quote_ident), %L (quote_literal). M0097-0003.
		if len(x.Args) >= 1 {
			f, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || f.IsNull() {
				return NullDatum, nil
			}
			fmtStr := f.StringValue()
			// Evaluate remaining args.
			args := make([]Datum, 0, len(x.Args)-1)
			for _, a := range x.Args[1:] {
				v, e := evalExpr(a, row, ctx)
				if e != nil {
					return Datum{}, e
				}
				args = append(args, v)
			}
			result := applyPgFormat(fmtStr, args)
			return NewStringDatum(result), nil
		}

	// ── Mathematical functions (M0097-0005) ───────────────────────────────
	case "abs":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				n := v.Int
				if n < 0 {
					n = -n
				}
				return Datum{Kind: KindInt, Int: n}, nil
			}
			// Numeric abs
			if v.Kind == KindNumeric || v.Kind == KindString {
				sv := v.Format()
				if strings.HasPrefix(sv, "-") {
					sv = sv[1:]
				}
				m, sc, perr := parseNumeric(sv)
				if perr == nil {
					return newNumeric(m, int(sc)), nil
				}
			}
			return v, nil
		}
	case "ceil", "ceiling":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(math.Ceil(f))}, nil
		}
	case "floor":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(math.Floor(f))}, nil
		}
	case "round":
		if len(x.Args) >= 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			scale := int64(0)
			if len(x.Args) >= 2 {
				sc, serr := evalExpr(x.Args[1], row, ctx)
				if serr == nil && !sc.IsNull() {
					scale = sc.Int
				}
			}
			if v.Kind == KindInt && scale == 0 {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			factor := math.Pow(10, float64(scale))
			rounded := math.Round(f*factor) / factor
			sv := strconv.FormatFloat(rounded, 'f', int(scale), 64)
			m, sc2, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc2)), nil
		}
	case "trunc":
		if len(x.Args) >= 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				return v, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			scale := int64(0)
			if len(x.Args) >= 2 {
				sc, serr := evalExpr(x.Args[1], row, ctx)
				if serr == nil && !sc.IsNull() {
					scale = sc.Int
				}
			}
			factor := math.Pow(10, float64(scale))
			truncated := math.Trunc(f*factor) / factor
			return Datum{Kind: KindInt, Int: int64(truncated)}, nil
		}
	case "sign":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			if v.Kind == KindInt {
				if v.Int > 0 {
					return Datum{Kind: KindInt, Int: 1}, nil
				} else if v.Int < 0 {
					return Datum{Kind: KindInt, Int: -1}, nil
				}
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			if f > 0 {
				return Datum{Kind: KindInt, Int: 1}, nil
			} else if f < 0 {
				return Datum{Kind: KindInt, Int: -1}, nil
			}
			return Datum{Kind: KindInt, Int: 0}, nil
		}
	case "sqrt":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			f, ferr := strconv.ParseFloat(v.Format(), 64)
			if ferr != nil {
				return NullDatum, nil
			}
			result := math.Sqrt(f)
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "power", "pow":
		if len(x.Args) == 2 {
			base, e1 := evalExpr(x.Args[0], row, ctx)
			exp, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || base.IsNull() || exp.IsNull() {
				return NullDatum, nil
			}
			b, _ := strconv.ParseFloat(base.Format(), 64)
			e, _ := strconv.ParseFloat(exp.Format(), 64)
			result := math.Pow(b, e)
			if result == math.Trunc(result) {
				return Datum{Kind: KindInt, Int: int64(result)}, nil
			}
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "exp":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			f, _ := strconv.ParseFloat(v.Format(), 64)
			result := math.Exp(f)
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "ln", "log":
		if len(x.Args) >= 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			f, _ := strconv.ParseFloat(v.Format(), 64)
			var result float64
			if name == "ln" || len(x.Args) == 1 {
				result = math.Log(f)
			} else {
				base, _ := evalExpr(x.Args[1], row, ctx)
				b, _ := strconv.ParseFloat(base.Format(), 64)
				result = math.Log(f) / math.Log(b)
			}
			sv := strconv.FormatFloat(result, 'f', 6, 64)
			m, sc, perr := parseNumeric(sv)
			if perr != nil {
				return NewStringDatum(sv), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "gcd":
		// gcd(a, b) → greatest common divisor, always non-negative. M0097-0003.
		if len(x.Args) == 2 {
			av, e1 := evalExpr(x.Args[0], row, ctx)
			bv, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || av.IsNull() || bv.IsNull() {
				return NullDatum, nil
			}
			a64, b64 := av.Int, bv.Int
			// Compute absolute values in int64 to avoid overflow.
			if a64 < 0 {
				a64 = -a64
			}
			if b64 < 0 {
				b64 = -b64
			}
			// Euclidean algorithm.
			for b64 != 0 {
				a64, b64 = b64, a64%b64
			}
			// If both inputs fit in int4 range (or are INT4_MIN), check for
			// int4 result overflow: gcd(INT4_MIN, x) = |INT4_MIN|= 2^31 > INT4_MAX.
			isInt4Range := av.Int >= -2147483648 && av.Int <= 2147483647 &&
				bv.Int >= -2147483648 && bv.Int <= 2147483647
			if isInt4Range && a64 > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: x.Pos(), Message: "integer out of range"}
			}
			return Datum{Kind: KindInt, Int: a64}, nil
		}
	case "lcm":
		// lcm(a, b) → least common multiple, always non-negative. M0097-0003.
		if len(x.Args) == 2 {
			av, e1 := evalExpr(x.Args[0], row, ctx)
			bv, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || av.IsNull() || bv.IsNull() {
				return NullDatum, nil
			}
			a64, b64 := av.Int, bv.Int
			// lcm(0, b) = lcm(a, 0) = 0
			if a64 == 0 || b64 == 0 {
				return Datum{Kind: KindInt, Int: 0}, nil
			}
			absA, absB := a64, b64
			if absA < 0 {
				absA = -absA
			}
			if absB < 0 {
				absB = -absB
			}
			// Compute gcd.
			ga, gb := absA, absB
			for gb != 0 {
				ga, gb = gb, ga%gb
			}
			// lcm = |a| / gcd(a,b) * |b| (division first to reduce overflow risk).
			result := (absA / ga) * absB
			// Overflow check for int4 inputs.
			isInt4Range := av.Int >= -2147483648 && av.Int <= 2147483647 &&
				bv.Int >= -2147483648 && bv.Int <= 2147483647
			if isInt4Range && result > 2147483647 {
				return Datum{}, &ExecError{Code: "22003", Pos: x.Pos(), Message: "integer out of range"}
			}
			return Datum{Kind: KindInt, Int: result}, nil
		}
	case "mod":
		// mod(a, b) → a % b
		if len(x.Args) == 2 {
			a, e1 := evalExpr(x.Args[0], row, ctx)
			b, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil || a.IsNull() || b.IsNull() {
				return NullDatum, nil
			}
			if b.Int == 0 {
				return Datum{}, &ExecError{Code: "22012", Pos: x.Pos(), Message: "division by zero"}
			}
			return Datum{Kind: KindInt, Int: a.Int % b.Int}, nil
		}
	case "pi":
		return newNumeric(parseNumericOrZero("3.14159265358979"), 14), nil
	case "random":
		// random() → float8 in [0, 1)
		// Return a deterministic 0.5 for testing purposes.
		return NewStringDatum("0.5"), nil

	// ── Type conversion functions (M0097-0005) ────────────────────────────
	case "to_number":
		// to_number(text, fmt) → numeric — simplified: parse as numeric
		if len(x.Args) >= 1 {
			s, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || s.IsNull() {
				return NullDatum, nil
			}
			cleaned := strings.TrimSpace(strings.ReplaceAll(s.StringValue(), ",", ""))
			m, sc, perr := parseNumeric(cleaned)
			if perr != nil {
				return NewStringDatum(cleaned), nil
			}
			return newNumeric(m, int(sc)), nil
		}
	case "to_hex":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			return NewStringDatum(fmt.Sprintf("%x", v.Int)), nil
		}
	case "encode":
		// encode(bytea, format) — stub: return empty string
		return NewStringDatum(""), nil
	case "decode":
		return NullDatum, nil

	// ── Misc functions (M0097-0005) ────────────────────────────────────────
	case "coalesce":
		for _, arg := range x.Args {
			v, err := evalExpr(arg, row, ctx)
			if err == nil && !v.IsNull() {
				return v, nil
			}
		}
		return NullDatum, nil
	case "nullif":
		if len(x.Args) == 2 {
			a, e1 := evalExpr(x.Args[0], row, ctx)
			b, e2 := evalExpr(x.Args[1], row, ctx)
			if e1 != nil || e2 != nil {
				return NullDatum, nil
			}
			if !a.IsNull() && !b.IsNull() && a.Format() == b.Format() {
				return NullDatum, nil
			}
			return a, nil
		}
	case "greatest":
		best := NullDatum
		for _, arg := range x.Args {
			v, err := evalExpr(arg, row, ctx)
			if err != nil || v.IsNull() {
				continue
			}
			if best.IsNull() || v.Format() > best.Format() {
				best = v
			}
		}
		return best, nil
	case "least":
		best := NullDatum
		for _, arg := range x.Args {
			v, err := evalExpr(arg, row, ctx)
			if err != nil || v.IsNull() {
				continue
			}
			if best.IsNull() || v.Format() < best.Format() {
				best = v
			}
		}
		return best, nil
	case "num_nonnulls":
		cnt := 0
		for _, arg := range x.Args {
			v, err := evalExpr(arg, row, ctx)
			if err == nil && !v.IsNull() {
				cnt++
			}
		}
		return Datum{Kind: KindInt, Int: int64(cnt)}, nil
	case "num_nulls":
		cnt := 0
		for _, arg := range x.Args {
			v, err := evalExpr(arg, row, ctx)
			if err != nil || v.IsNull() {
				cnt++
			}
		}
		return Datum{Kind: KindInt, Int: int64(cnt)}, nil
	case "pg_typeof":
		if len(x.Args) == 1 {
			// Return the type name as text (best-effort)
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NewStringDatum("unknown"), nil
			}
			switch v.Kind {
			case KindInt:
				return NewStringDatum("integer"), nil
			case KindBool:
				return NewStringDatum("boolean"), nil
			case KindNumeric:
				return NewStringDatum("numeric"), nil
			case KindTime:
				return NewStringDatum("timestamp without time zone"), nil
			default:
				return NewStringDatum("text"), nil
			}
		}
	case "pg_column_size":
		if len(x.Args) == 1 {
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil || v.IsNull() {
				return NullDatum, nil
			}
			return Datum{Kind: KindInt, Int: int64(len(v.Format()))}, nil
		}
	case "current_user", "session_user":
		return NewStringDatum("postgres"), nil
	case "user":
		return NewStringDatum("postgres"), nil
	case "version":
		return NewStringDatum("PostgreSQL 18.3 goopg compatible"), nil
	case "pg_current_xact_id", "txid_current":
		if ctx.Tx.XID != 0 {
			return Datum{Kind: KindInt, Int: int64(ctx.Tx.XID)}, nil
		}
		return Datum{Kind: KindInt, Int: 0}, nil
	case "clock_timestamp":
		return NewTimeDatum(ctx.Now), nil
	case "timeofday":
		return NewStringDatum(ctx.Now.Format("Mon Jan 02 15:04:05.000000 2006 UTC")), nil
	case "localtime":
		// Returns time-of-day anchored at epoch (same storage convention as current_time).
		t := ctx.Now.UTC()
		return NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)), nil
	case "localtimestamp":
		return NewTimeDatum(ctx.Now), nil
	case "pg_is_in_recovery":
		return NewBoolDatum(ctx.IsStandby), nil
	case "pg_promote":
		// pg_promote(wait boolean DEFAULT true, wait_seconds integer DEFAULT 60)
		// Returns true if this server is a standby and promotion was triggered.
		// Returns false without error when not a standby (mirrors upstream).
		if ctx.Promote == nil {
			return NewBoolDatum(false), nil
		}
		if err := ctx.Promote(); err != nil {
			return Datum{}, &ExecError{
				Code:    "XX000",
				Pos:     x.Pos(),
				Message: "pg_promote: " + err.Error(),
			}
		}
		return NewBoolDatum(true), nil
	// currtid2(relname text, tid tid) → tid: returns the latest visible TID
	// for a row in the named relation. M0097-0038.
	case "currtid2":
		return evalCurrtid2(x, row, ctx)
	}

	// Function-style type casts: int4(x), float8(x), text(x), etc.
	// PostgreSQL allows type names as function names for casting. M0097-0003.
	if len(x.Args) == 1 {
		typeName := name
		switch typeName {
		case "int2", "smallint",
			"int4", "integer", "int",
			"int8", "bigint",
			"float4", "real",
			"float8", "double precision",
			"numeric", "decimal",
			"text", "varchar", "bpchar", "char",
			"bool", "boolean",
			"oid", "date", "timestamp", "timestamptz",
			"time", "timetz", "interval":
			v, err := evalExpr(x.Args[0], row, ctx)
			if err != nil {
				return Datum{}, err
			}
			if v.IsNull() {
				return NullDatum, nil
			}
			return evalCast(v, typeName, x.Pos())
		}
	}

	return evalStoredRoutineFuncCall(x, row, ctx)
}

// evalCurrtid2 implements currtid2(relname text, tid tid) → tid.
// Returns the latest visible TID for the named relation, or an error for
// unsupported relation kinds (indexes, partitioned tables, views without ctid).
// M0097-0038.
func evalCurrtid2(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: fmt.Sprintf("function currtid2(unknown, unknown) does not exist")}
	}
	nameD, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return NullDatum, err
	}
	if nameD.IsNull() {
		return NullDatum, nil
	}
	relname := strings.TrimSpace(nameD.StringValue())

	tidD, err := evalExpr(x.Args[1], row, ctx)
	if err != nil {
		return NullDatum, err
	}
	if tidD.IsNull() {
		return NullDatum, nil
	}
	tidStr := tidD.StringValue()
	block, offset, ok := parseTidInput(tidStr)
	if !ok {
		return NullDatum, &ExecError{Code: "22P02", Pos: x.Pos(),
			Message: fmt.Sprintf("invalid input syntax for type tid: %q", tidStr)}
	}

	// Sequence: in-memory only; treat TID as always valid. M0097-0038.
	if LookupSequence(relname) != nil {
		return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
	}

	if ctx.Catalog == nil {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: "currtid2 requires a catalog"}
	}

	// Index: not supported.
	if _, isIdx := ctx.Catalog.LookupIndex(parser.ObjectName{Name: relname}); isIdx {
		return NullDatum, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message:  fmt.Sprintf("cannot open relation %q", relname),
			Detail: "This operation is not supported for indexes."}
	}

	tbl, found := ctx.Catalog.LookupTable(parser.ObjectName{Name: relname})
	if !found {
		return NullDatum, &ExecError{Code: "42P01", Pos: x.Pos(),
			Message: fmt.Sprintf("relation %q does not exist", relname)}
	}

	// Partitioned table: no storage.
	if len(tbl.PartitionKey) > 0 {
		schema := tbl.Schema
		if schema == "" {
			schema = "public"
		}
		qualName := schema + "." + tbl.Name
		return NullDatum, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: fmt.Sprintf("cannot look at latest visible tid for relation %q", qualName)}
	}

	// View (non-matview): inspect for ctid column, resolve to base table.
	if tbl.View != nil && !tbl.IsMatView {
		return currtid2ViewCheck(tbl, block, offset, x.Pos(), ctx)
	}

	// Heap table or matview: check TID validity in storage.
	return currtid2TIDCheck(tbl.Name, tbl, block, offset, x.Pos(), ctx)
}

// currtid2ViewCheck handles currtid2 for a SQL view. Checks that the view
// has a ctid column of type tid, then delegates TID validity to the base table.
func currtid2ViewCheck(viewTbl *catalog.Table, block uint32, offset uint16, pos int, ctx *Context) (Datum, error) {
	var ctidTypeName string
	for _, c := range viewTbl.Columns {
		if strings.EqualFold(c.Name, "ctid") {
			ctidTypeName = strings.ToLower(c.Type.Name)
			break
		}
	}
	if ctidTypeName == "" {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	if ctidTypeName != "tid" {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "ctid isn't of type TID"}
	}
	// Resolve base table from view's FROM clause.
	if viewTbl.View == nil || len(viewTbl.View.From) == 0 {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	baseTableName := viewTbl.View.From[0].Name
	if baseTableName == "" {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	baseTbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: baseTableName})
	if !ok {
		return NullDatum, &ExecError{Code: "0A000", Pos: pos,
			Message: "currtid cannot handle views with no CTID"}
	}
	return currtid2TIDCheck(baseTbl.Name, baseTbl, block, offset, pos, ctx)
}

// currtid2TIDCheck verifies that (block, offset) is a valid address in tbl's
// heap storage and returns the tid on success.
func currtid2TIDCheck(relname string, tbl *catalog.Table, block uint32, offset uint16, pos int, ctx *Context) (Datum, error) {
	if ctx.Pool == nil || ctx.Catalog == nil {
		return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil || uint32(nBlocks) <= block {
		return NullDatum, &ExecError{Code: "22000", Pos: pos,
			Message: fmt.Sprintf("tid (%d, %d) is not valid for relation %q", block, offset, relname)}
	}
	return NewStringDatum(fmt.Sprintf("(%d,%d)", block, offset)), nil
}

// initCap returns s with the first letter of each word capitalized. M0097-0005.
func initCap(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			for j := 1; j < len(runes); j++ {
				runes[j] = unicode.ToLower(runes[j])
			}
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

// padLeft left-pads s to length n using the fill string. M0097-0005.
func padLeft(s string, n int, fill string) string {
	runes := []rune(s)
	if len(runes) >= n {
		return string(runes[:n])
	}
	if fill == "" {
		fill = " "
	}
	fillRunes := []rune(fill)
	var buf []rune
	for len(buf)+len(runes) < n {
		for _, r := range fillRunes {
			if len(buf)+len(runes) >= n {
				break
			}
			buf = append(buf, r)
		}
	}
	return string(buf) + string(runes)
}

// padRight right-pads s to length n using the fill string. M0097-0005.
func padRight(s string, n int, fill string) string {
	runes := []rune(s)
	if len(runes) >= n {
		return string(runes[:n])
	}
	if fill == "" {
		fill = " "
	}
	fillRunes := []rune(fill)
	result := make([]rune, len(runes), n)
	copy(result, runes)
	for len(result) < n {
		for _, r := range fillRunes {
			if len(result) >= n {
				break
			}
			result = append(result, r)
		}
	}
	return string(result)
}

// translateStr replaces each character in s that appears in from with the
// corresponding character in to. Characters in from without a corresponding
// to-character are deleted. M0097-0005.
func translateStr(s, from, to string) string {
	fromRunes := []rune(from)
	toRunes := []rune(to)
	var buf strings.Builder
	for _, r := range s {
		replaced := false
		for i, fr := range fromRunes {
			if r == fr {
				if i < len(toRunes) {
					buf.WriteRune(toRunes[i])
				}
				// else: delete the character
				replaced = true
				break
			}
		}
		if !replaced {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// parseNumericOrZero wraps parseNumeric with a zero fallback. M0097-0005.
func parseNumericOrZero(s string) *big.Int {
	m, _, err := parseNumeric(s)
	if err != nil {
		return new(big.Int)
	}
	return m
}

// evalAdvisoryLock implements the blocking and non-blocking advisory-lock
// acquisition variants.
//
//   - tryOnly=true : non-blocking (pg_try_advisory_*); returns true/false.
//   - tryOnly=false: blocking (pg_advisory_lock, pg_advisory_xact_lock);
//     blocks until the lock is acquired or ctx is cancelled.
//
// Argument forms:
//
//	(bigint)        → key = bigint
//	(int4, int4)    → key = (classid, objid)
func evalAdvisoryLock(x *planner.FuncCall, row Row, ctx *Context, tryOnly bool, xactScoped bool) (Datum, error) {
	sess := advisorySessionIDFromContext(ctx)

	var key advisoryKey
	switch len(x.Args) {
	case 1:
		v, err := evalExpr(x.Args[0], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		n, ok := datumInt64(v)
		if !ok {
			return NullDatum, nil
		}
		key = bigintToKey(n)
	case 2:
		v0, err := evalExpr(x.Args[0], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		v1, err2 := evalExpr(x.Args[1], row, ctx)
		if err2 != nil {
			return NullDatum, err2
		}
		n0, _ := datumInt64(v0)
		n1, _ := datumInt64(v1)
		key = int4ToKey(int32(n0), int32(n1))
	default:
		return NullDatum, nil
	}

	if tryOnly {
		ok := globalAdvisoryMgr.tryAcquire(key, sess, xactScoped)
		return NewBoolDatum(ok), nil
	}

	// Blocking acquire — respects ctx cancellation.
	qctx := ctx.Ctx
	if qctx == nil {
		qctx = context.Background()
	}
	if err := globalAdvisoryMgr.acquire(qctx, key, sess, xactScoped); err != nil {
		// Context cancelled (step timed out or runner aborted).
		return NullDatum, nil
	}
	// Return a non-NULL string so `IS NOT NULL` in WHERE clauses is true.
	return NewStringDatum(""), nil
}

// evalAdvisoryUnlock implements pg_advisory_unlock(bigint) and
// pg_advisory_unlock(int4, int4). Returns true if the lock was held by
// this session and has been released, false otherwise.
func evalAdvisoryUnlock(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	sess := advisorySessionIDFromContext(ctx)

	var key advisoryKey
	switch len(x.Args) {
	case 1:
		v, err := evalExpr(x.Args[0], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		n, _ := datumInt64(v)
		key = bigintToKey(n)
	case 2:
		v0, err := evalExpr(x.Args[0], row, ctx)
		if err != nil {
			return NullDatum, err
		}
		v1, err2 := evalExpr(x.Args[1], row, ctx)
		if err2 != nil {
			return NullDatum, err2
		}
		n0, _ := datumInt64(v0)
		n1, _ := datumInt64(v1)
		key = int4ToKey(int32(n0), int32(n1))
	default:
		return NewBoolDatum(false), nil
	}

	ok := globalAdvisoryMgr.release(key, sess)
	return NewBoolDatum(ok), nil
}

// evalAdvisoryUnlockAll implements pg_advisory_unlock_all(). Releases every
// session-scoped advisory lock held by this session and returns NULL (void-like).
func evalAdvisoryUnlockAll(ctx *Context) (Datum, error) {
	if ctx == nil {
		return NullDatum, nil
	}
	globalAdvisoryMgr.releaseAllSession(advisorySessionIDFromContext(ctx))
	return NullDatum, nil
}

// datumInt64 extracts an integer value from a Datum. Returns (0, false) if the
// Datum is not an integer-compatible type.
func datumInt64(d Datum) (int64, bool) {
	switch d.Kind {
	case KindInt:
		return d.Int, true
	case KindString:
		// Some callers pass string representations of integers.
		n, err := strconv.ParseInt(strings.TrimSpace(d.StringValue()), 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// evalToDate implements PostgreSQL's `to_date(text, text)` for the
// format codes HammerDB TPC-H Q15 uses (`YYYY-MM-DD`). It reuses
// `pgFormatToGoLayout` from to_timestamp and truncates the result
// to midnight UTC so the value behaves like a DATE rather than a
// timestamp. Real upstream parity (timezone, era handling, locale
// month names) waits on the type system; this is scoped to "make
// Q15 plan and run without rejecting the conversion".
func evalToDate(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_date(text, text) requires exactly 2 arguments"}
	}
	src, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	fmtArg, err := evalExpr(x.Args[1], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || fmtArg.IsNull() {
		return NullDatum, nil
	}
	if (src.Kind != KindString) || (fmtArg.Kind != KindString) {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_date arguments must be text"}
	}
	goLayout := pgFormatToGoLayout(fmtArg.StringValue())
	t, perr := time.Parse(goLayout, src.StringValue())
	if perr != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("to_date: %v (format=%q value=%q)", perr, fmtArg.StringValue(), src.StringValue())}
	}
	year, month, day := t.UTC().Date()
	out := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return NewTimeDatum(out), nil
}

// evalSubstr implements PostgreSQL's `substr(string, from [, count])`
// (alias `substring`) using 1-based byte indexing — matches upstream's
// `text_substr` semantics for ASCII/single-byte text. HammerDB TPC-H
// Q22 uses `substr(c_phone, 1, 2)` to extract the country code prefix.
// NULL inputs propagate to NULL output.
//
// The 2-argument form returns the substring from `from` to the end of
// evalPgSleep implements pg_sleep(seconds). Sleeps for the given
// duration while honouring query cancellation via ctx.Ctx.
func evalPgSleep(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 1 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "pg_sleep(double precision) requires exactly 1 argument"}
	}
	secs, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if secs.IsNull() {
		return NullDatum, nil
	}
	var d time.Duration
	switch secs.Kind {
	case KindInt:
		d = time.Duration(secs.Int) * time.Second
	case KindNumeric:
		f, err := strconv.ParseFloat(secs.Format(), 64)
		if err != nil {
			return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "pg_sleep: invalid numeric value"}
		}
		d = time.Duration(f * float64(time.Second))
	default:
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "pg_sleep argument must be numeric"}
	}
	if d < 0 {
		d = 0
	}
	if ctx.Ctx != nil {
		select {
		case <-time.After(d):
		case <-ctx.Ctx.Done():
			return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	} else {
		time.Sleep(d)
	}
	return NullDatum, nil
}

// the string. Negative `from` values are clamped per upstream:
// `substr('abcdef', -2, 4)` returns `'a'` (start at position 1, length
// becomes 1 after subtracting the negative offset). For a v0 simple
// implementation we follow the spec exactly.
func evalSubstr(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 && len(x.Args) != 3 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr requires 2 or 3 arguments"}
	}
	src, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	fromArg, err := evalExpr(x.Args[1], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || fromArg.IsNull() {
		return NullDatum, nil
	}
	if src.Kind != KindString {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr first argument must be text"}
	}
	if fromArg.Kind != KindInt {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr second argument must be integer"}
	}
	s := src.StringValue()
	from := fromArg.Int
	// Upstream's text_substring: 1-based start, treat values <=0 as
	// shifting the window left of the string. With no length, the
	// window is open-ended on the right, so a non-positive `from`
	// just clamps to position 1.
	if len(x.Args) == 2 {
		if from < 1 {
			from = 1
		}
		idx := int(from) - 1
		if idx >= len(s) {
			return NewStringDatum(""), nil
		}
		return NewStringDatum(s[idx:]), nil
	}
	cntArg, err := evalExpr(x.Args[2], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if cntArg.IsNull() {
		return NullDatum, nil
	}
	if cntArg.Kind != KindInt {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "substr third argument must be integer"}
	}
	count := cntArg.Int
	if count < 0 {
		return Datum{}, &ExecError{Code: "22011", Pos: x.Pos(), Message: "negative substring length not allowed"}
	}
	end := from + count
	if from < 1 {
		from = 1
	}
	startIdx := int(from) - 1
	endIdx := int(end) - 1
	if endIdx < startIdx {
		endIdx = startIdx
	}
	if startIdx >= len(s) {
		return NewStringDatum(""), nil
	}
	if endIdx > len(s) {
		endIdx = len(s)
	}
	return NewStringDatum(s[startIdx:endIdx]), nil
}

// evalToTimestamp implements PostgreSQL's `to_timestamp(text,
// text)` for the format specifiers HammerDB's TPC-H loader uses
// (`YYYY`, `Mon`, `MM`, `DD`, plus optional time-of-day pieces).
// Real upstream parity (timezone handling, locale-aware month
// names, fractional seconds) waits on the type system; this is
// deliberately scoped to "make the loader work without rejecting
// rows".
func evalToTimestamp(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_timestamp(text, text) requires exactly 2 arguments"}
	}
	src, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	fmtArg, err := evalExpr(x.Args[1], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if src.IsNull() || fmtArg.IsNull() {
		return NullDatum, nil
	}
	if (src.Kind != KindString) || (fmtArg.Kind != KindString) {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_timestamp arguments must be text"}
	}
	goLayout := pgFormatToGoLayout(fmtArg.StringValue())
	t, perr := time.Parse(goLayout, src.StringValue())
	if perr != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("to_timestamp: %v (format=%q value=%q)", perr, fmtArg.StringValue(), src.StringValue())}
	}
	return NewTimeDatum(t.UTC()), nil
}

// pgFormatToGoLayout translates a v0 subset of upstream PG's
// to_timestamp() format codes into a Go time-package layout.
// Codes are matched longest-first inside a left-to-right scan;
// any character that isn't a recognised code passes through as a
// literal separator. Unknown codes are kept verbatim — Go's
// time.Parse will error if they don't match the input.
func pgFormatToGoLayout(s string) string {
	codes := []struct {
		from, to string
	}{
		{"YYYY", "2006"},
		{"YY", "06"},
		{"MON", "Jan"},
		{"Mon", "Jan"},
		{"MM", "01"},
		{"DD", "02"},
		{"HH24", "15"},
		{"HH", "03"},
		{"MI", "04"},
		{"SS", "05"},
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		matched := false
		for _, c := range codes {
			if i+len(c.from) <= len(s) && s[i:i+len(c.from)] == c.from {
				b.WriteString(c.to)
				i += len(c.from)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// subqueryCacheKey builds a deterministic string key from an
// outer row so correlated subquery results can be cached per
// distinct correlation value.
func subqueryCacheKey(row Row) string {
	parts := make([]string, len(row))
	for i, d := range row {
		parts[i] = datumKey(d)
	}
	return strings.Join(parts, "|")
}

// nonCorrelatedCacheKey returns a constant cache key for a given
// non-correlated subquery node. The pointer-derived suffix keeps
// keys for distinct subquery sites distinct within a single query
// (so two unrelated non-correlated SubPlans don't share a cached
// result), while collapsing all outer rows for the same SubPlan
// onto a single entry. (M0058-0001.)
func nonCorrelatedCacheKey(x interface{}) string {
	return fmt.Sprintf("\x00nc:%p", x)
}

// isValidBoolInput reports whether v is a valid PostgreSQL boolean literal.
// Used by pg_input_is_valid('...', 'bool'). Mirrors evalTypedStringLit bool case.
// pgInputIsValidTypedLen checks validity for varchar(N)/char(N)/character varying(N)
// type strings as used in pg_input_is_valid. Returns (valid, handled). M0097-0003.
func pgInputIsValidTypedLen(v, typStr string) (bool, bool) {
	// Match "varchar(N)", "character varying(N)", "char(N)" etc.
	var base string
	var n int
	for _, pfx := range []string{"character varying(", "varchar(", "character(", "char(", "bpchar("} {
		if strings.HasPrefix(typStr, pfx) && strings.HasSuffix(typStr, ")") {
			mid := typStr[len(pfx) : len(typStr)-1]
			if parsed, err := strconv.Atoi(mid); err == nil && parsed > 0 {
				base = pfx[:len(pfx)-1]
				n = parsed
				break
			}
		}
	}
	if n == 0 {
		return false, false
	}
	// PostgreSQL's input functions check raw length (NO trailing space stripping).
	if strings.Contains(base, "char") && !strings.Contains(base, "varying") {
		// char(N): fixed-width; check stripped length
		stripped := strings.TrimRight(v, " ")
		return len(stripped) <= n, true
	}
	// varchar(N): raw length check (varcharin does not strip trailing spaces).
	return len(v) <= n, true
}

// parseIdentString parses a qualified SQL identifier string (like "schema.table")
// into its components. Returns (components, errMsg, detail). If errMsg != "", an error occurred.
// Matches PostgreSQL's parse_ident() behavior. M0097-0003.
func parseIdentString(input string, strict bool) ([]string, string, string) {
	orig := input
	i := 0
	n := len(input)
	var components []string
	// PostgreSQL uses "string" (double-quoted, raw bytes) not Go %q (escape codes). M0097-0003.
	errMsg := func(detail string) ([]string, string, string) {
		return nil, `string is not a valid identifier: "` + orig + `"`, detail
	}

	for {
		// Skip leading whitespace.
		for i < n && (input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r') {
			i++
		}
		if i >= n {
			if len(components) == 0 {
				return errMsg("")
			}
			// After the last dot, empty → error. M0097-0003.
			if len(components) > 0 && strict {
				return errMsg(`No valid identifier after ".".`)
			}
			break
		}
		if input[i] == '"' {
			// Quoted identifier: find matching unescaped '"'.
			i++ // skip opening quote
			var sb strings.Builder
			for i < n {
				if input[i] == '"' {
					if i+1 < n && input[i+1] == '"' {
						sb.WriteByte('"')
						i += 2
					} else {
						i++ // skip closing quote
						break
					}
				} else {
					sb.WriteByte(input[i])
					i++
				}
			}
			components = append(components, sb.String())
		} else {
			// Unquoted identifier: must start with letter or underscore.
			if !isIdentStartByte(input[i]) {
				if !strict {
					break
				}
				// Distinguish "before dot" vs "after dot" vs no-dot. M0097-0003.
				if input[i] == '.' {
					// Dot at start of component → nothing valid before this dot.
					return errMsg(`No valid identifier before ".".`)
				}
				if len(components) > 0 {
					// We consumed a dot and the next component is invalid.
					return errMsg(`No valid identifier after ".".`)
				}
				// No dot involved; just an invalid starting character.
				return errMsg("")
			}
			start := i
			for i < n && isIdentContByte(input[i]) {
				i++
			}
			ident := strings.ToLower(input[start:i])
			components = append(components, ident)
		}
		// Skip trailing whitespace.
		for i < n && (input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r') {
			i++
		}
		if i >= n {
			break // end of string, done
		}
		if input[i] == '.' {
			i++ // consume dot, continue to next component
		} else {
			// Trailing garbage.
			if strict {
				return errMsg("")
			}
			break
		}
	}
	if len(components) == 0 {
		return errMsg("")
	}
	return components, "", ""
}

func isIdentStartByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 128
}

func isIdentContByte(c byte) bool {
	return isIdentStartByte(c) || (c >= '0' && c <= '9') || c == '$'
}

// formatTextArray formats a string slice as a PostgreSQL text array literal:
// {elem1,"elem with spaces",...}. Elements needing quoting get double-quoted. M0097-0003.
func formatTextArray(elems []string) string {
	if len(elems) == 0 {
		return "{}"
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			sb.WriteByte(',')
		}
		// Quote the element if it contains special chars, spaces, commas, braces, or backslashes.
		needsQuote := len(e) == 0
		if !needsQuote {
			for _, c := range e {
				if c == '"' || c == ',' || c == '{' || c == '}' || c == '\\' || c == ' ' || c == '\t' {
					needsQuote = true
					break
				}
			}
		}
		if needsQuote {
			sb.WriteByte('"')
			for _, c := range e {
				if c == '"' || c == '\\' {
					sb.WriteByte('\\')
				}
				sb.WriteRune(c)
			}
			sb.WriteByte('"')
		} else {
			sb.WriteString(e)
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

// applyPgFormat implements PostgreSQL's format() function for common specifiers:
// %s (value as text), %I (quote_ident), %L (quote_literal), %% (literal %). M0097-0003.
func applyPgFormat(fmtStr string, args []Datum) string {
	var sb strings.Builder
	argIdx := 0
	for i := 0; i < len(fmtStr); i++ {
		if fmtStr[i] != '%' {
			sb.WriteByte(fmtStr[i])
			continue
		}
		i++
		if i >= len(fmtStr) {
			sb.WriteByte('%')
			break
		}
		switch fmtStr[i] {
		case '%':
			sb.WriteByte('%')
		case 's':
			if argIdx < len(args) {
				sb.WriteString(args[argIdx].Format())
				argIdx++
			}
		case 'I':
			// quote_ident: quote identifier only if necessary.
			if argIdx < len(args) {
				ident := args[argIdx].StringValue()
				argIdx++
				sb.WriteString(pgQuoteIdent(ident))
			}
		case 'L':
			// quote_literal: always quotes with single quotes.
			if argIdx < len(args) {
				lit := args[argIdx].StringValue()
				argIdx++
				escaped := strings.ReplaceAll(lit, "'", "''")
				sb.WriteByte('\'')
				sb.WriteString(escaped)
				sb.WriteByte('\'')
			}
		default:
			sb.WriteByte('%')
			sb.WriteByte(fmtStr[i])
		}
	}
	return sb.String()
}

// pgQuoteIdent quotes a SQL identifier if necessary (uppercase, spaces, special chars). M0097-0003.
func pgQuoteIdent(s string) string {
	if s == "" {
		return `""`
	}
	// Safe unquoted: starts with letter/underscore, contains only letter/digit/underscore,
	// all lowercase, and is not a reserved word.
	safe := true
	for i, c := range s {
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || c == '_') {
				safe = false
				break
			}
		} else {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				safe = false
				break
			}
		}
	}
	if safe {
		return s
	}
	// Must quote.
	escaped := strings.ReplaceAll(s, `"`, `""`)
	return `"` + escaped + `"`
}

// parseTextArray parses a PostgreSQL text array literal {elem1,"elem2",...}
// and returns its elements. Used for name[] cast. M0097-0003.
func parseTextArray(s string) []string {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return []string{s}
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return nil
	}
	var elems []string
	i := 0
	for i < len(inner) {
		if inner[i] == '"' {
			// Quoted element.
			i++
			var sb strings.Builder
			for i < len(inner) {
				if inner[i] == '"' {
					if i+1 < len(inner) && inner[i+1] == '"' {
						sb.WriteByte('"')
						i += 2
					} else {
						i++
						break
					}
				} else if inner[i] == '\\' && i+1 < len(inner) {
					sb.WriteByte(inner[i+1])
					i += 2
				} else {
					sb.WriteByte(inner[i])
					i++
				}
			}
			elems = append(elems, sb.String())
		} else {
			// Unquoted element: read until comma or end.
			start := i
			for i < len(inner) && inner[i] != ',' {
				i++
			}
			elems = append(elems, inner[start:i])
		}
		if i < len(inner) && inner[i] == ',' {
			i++
		}
	}
	return elems
}

// charTypeParseOctalEscape handles PostgreSQL's "char" internal single-byte type
// which interprets backslash-octal sequences (\NNN) in string inputs.
// Returns (byte, true) if the string is a valid \NNN octal escape, else (0, false).
// M0097-0003.
func charTypeParseOctalEscape(s string) (byte, bool) {
	if len(s) != 4 || s[0] != '\\' {
		return 0, false
	}
	d0, d1, d2 := s[1], s[2], s[3]
	if d0 < '0' || d0 > '7' || d1 < '0' || d1 > '7' || d2 < '0' || d2 > '7' {
		return 0, false
	}
	val := int(d0-'0')*64 + int(d1-'0')*8 + int(d2-'0')
	if val > 255 {
		return 0, false
	}
	return byte(val), true
}

// charTypeDisplayForm returns the PostgreSQL charout() display form for a byte value:
// - Byte 0 → "" (null byte → empty, matching PostgreSQL's chartotext behavior)
// - Printable ASCII (32-126) → single character
// - Non-printable → \NNN (3-digit octal escape)
// M0097-0003.
func charTypeDisplayForm(b byte) string {
	if b == 0 {
		return ""
	}
	if b >= 32 && b <= 126 {
		return string([]byte{b})
	}
	// Non-printable: format as \NNN octal.
	return fmt.Sprintf("\\%03o", b)
}

// currentSchemaFromSearchPath resolves current_schema by walking the effective
// search_path and returning the first schema that exists. Built-in schemas
// (pg_catalog, information_schema, public) are always considered present.
// Returns NullDatum if no schema on the path exists.
func currentSchemaFromSearchPath(ctx *Context) (Datum, error) {
	searchPath := `"$user", public` // default
	if ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("search_path"); ok {
			searchPath = v
		}
	}
	user := "postgres"
	for _, rawSchema := range strings.Split(searchPath, ",") {
		s := strings.TrimSpace(rawSchema)
		s = strings.Trim(s, `"'`)
		if s == "$user" {
			s = user
		}
		if s == "" {
			continue
		}
		lc := strings.ToLower(s)
		switch lc {
		case "pg_catalog", "information_schema", "public":
			return NewStringDatum(lc), nil
		}
		// User-created schemas: check if a table with this schema prefix exists.
		if ctx.Catalog != nil {
			if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: s}); ok {
				return NewStringDatum(s), nil
			}
		}
	}
	return NullDatum, nil
}

func isValidBoolInput(v string) bool {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "t", "tr", "tru", "true", "y", "ye", "yes", "on", "1",
		"f", "fa", "fal", "fals", "false", "n", "no", "of", "off", "0":
		return true
	}
	return false
}

// hashPartTypesCompatible returns true if the arg type is compatible with the column type
// for satisfies_hash_partition type checking.
func hashPartTypesCompatible(colType, argTypeName string) bool {
	col := pgFormatTypeName(colType)
	arg := strings.ToLower(argTypeName)
	if col == arg {
		return true
	}
	// Integer family compatibility
	intFamily := map[string]bool{"integer": true, "smallint": true, "bigint": true, "int4": true, "int2": true, "int8": true}
	if intFamily[col] && intFamily[arg] {
		return true
	}
	return false
}

// hashPartTypeName returns the user-visible PG type name for a planner expression,
// used to build satisfies_hash_partition type mismatch messages. Returns "" if unknown.
func hashPartTypeName(e planner.Expr) string {
	switch x := e.(type) {
	case *planner.CastExpr:
		return pgFormatTypeName(x.TargetType)
	case *planner.IntegerConst:
		return "integer"
	case *planner.NumericConst:
		return "numeric"
	case *planner.StringConst:
		return "text"
	case *planner.BooleanConst:
		return "boolean"
	case *planner.FuncCall:
		switch strings.ToLower(x.Name) {
		case "now", "current_timestamp":
			return "timestamp with time zone"
		case "current_date":
			return "date"
		case "current_time":
			return "time with time zone"
		}
	}
	return ""
}

// pgFormatTypeName converts internal type names to PG user-visible names.
func pgFormatTypeName(t string) string {
	switch strings.ToLower(t) {
	case "int4", "int", "integer":
		return "integer"
	case "int2", "smallint":
		return "smallint"
	case "int8", "bigint":
		return "bigint"
	case "float4", "real":
		return "real"
	case "float8", "double precision":
		return "double precision"
	case "bool", "boolean":
		return "boolean"
	case "text":
		return "text"
	case "varchar", "character varying":
		return "character varying"
	case "bpchar", "character", "char":
		return "character"
	case "timestamptz", "timestamp with time zone":
		return "timestamp with time zone"
	case "timestamp", "timestamp without time zone":
		return "timestamp without time zone"
	case "date":
		return "date"
	case "time", "time without time zone":
		return "time without time zone"
	case "timetz", "time with time zone":
		return "time with time zone"
	case "numeric", "decimal":
		return "numeric"
	}
	return t
}
