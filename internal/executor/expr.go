package executor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// ExecError is the executor's structured error. Code is a SQLSTATE
// value the wire-protocol path forwards to ErrorResponse.
type ExecError struct {
	Code    string
	Message string
	Pos     int
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
	case *planner.BinaryOp:
		left, err := evalExprSlot(x.Left, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		right, err := evalExprSlot(x.Right, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalBinary(x.Op, left, right, x.Pos())
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
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary - requires integer"}
		}
		return Datum{Kind: KindInt, Int: -d.Int}, nil
	case parser.OpUnaryPos:
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary + requires integer"}
		}
		return d, nil
	case parser.OpNot:
		if d.Kind != KindBool {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator NOT requires boolean"}
		}
		return NewBoolDatum(!d.BoolValue()), nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown unary operator %s", op)}
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
		if (left.Kind == KindString || left.Kind == KindStringArena) {
			if m, s, err := parseNumeric(left.StringValue()); err == nil {
				left = newNumeric(m, int(s))
			}
		}
		if (right.Kind == KindString || right.Kind == KindStringArena) {
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
		if (left.Kind != KindString && left.Kind != KindStringArena) || (right.Kind != KindString && right.Kind != KindStringArena) {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator || requires string operands"}
		}
		return NewStringDatum(left.StringValue() + right.StringValue()), nil
	case parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
		cmp, err := compareDatum(left, right, pos)
		if err != nil {
			return Datum{}, err
		}
		return NewBoolDatum(cmpResult(op, cmp)), nil
	case parser.OpLike, parser.OpNotLike:
		// M0062-followup: accept KindBytes operands as UTF-8 text so a
		// varchar that arrives as bytes (e.g. via a row-reshaping path
		// that drops Kind) still evaluates correctly. Mirrors
		// `compareDatum`'s cross-Kind tolerance and aligns LIKE with
		// the comparators it sits next to. The error message includes
		// the actual Datum kinds so any residual non-string-non-bytes
		// case is diagnosable in one run instead of needing a server
		// log dive.
		ls, lok := datumAsString(left)
		rs, rok := datumAsString(right)
		if !lok || !rok {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires string operands (got left.Kind=%d right.Kind=%d)", op, left.Kind, right.Kind)}
		}
		matched := matchSQLLike(ls, rs)
		if op == parser.OpNotLike {
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
	case KindString, KindStringArena:
		return d.StringValue(), true
	case KindBytes, KindBytesArena:
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
	aIsString := a.Kind == KindString || a.Kind == KindStringArena
	bIsString := b.Kind == KindString || b.Kind == KindStringArena
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
		aIsString := a.Kind == KindString || a.Kind == KindStringArena
		bIsString := b.Kind == KindString || b.Kind == KindStringArena
		if aIsString && bIsString {
			return strings.Compare(a.StringValue(), b.StringValue()), nil
		}
		aIsBytes := a.Kind == KindBytes || a.Kind == KindBytesArena
		bIsBytes := b.Kind == KindBytes || b.Kind == KindBytesArena
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
	case KindString, KindStringArena:
		return strings.Compare(a.StringValue(), b.StringValue()), nil
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
	case "date":
		t, err := time.Parse("2006-01-02", x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid date %q: %v", x.Value, err)}
		}
		x.CachedTime = t.UTC()
		x.CacheValid = true
		return NewTimeDatum(x.CachedTime), nil
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
	}
	return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(), Message: fmt.Sprintf("typed-string literal with type %q is not supported", x.Type)}
}

// evalExtract implements `EXTRACT(field FROM source)` for the
// timestamp-component fields TPC-H Q7/Q8/Q9 use (year), plus
// the obvious neighbours (month, day, hour, minute, dow, doy,
// epoch). Returns int8; fractional-second fields wait on the
// type system.
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
	n, err := extractTimestampField(x.Field, src.TimeValue(), x.Pos())
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
	n, err := extractTimestampField(fieldArg.StringValue(), src.TimeValue(), x.Pos())
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindInt, Int: n}, nil
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
	aIsString := a.Kind == KindString || a.Kind == KindStringArena
	bIsString := b.Kind == KindString || b.Kind == KindStringArena
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
		t := ctx.Now
		return NewTimeDatum(t.Truncate(24 * 60 * 60 * 1e9)), nil
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
	case "set_config":
		// set_config(setting_name, new_value, is_local) → text
		// vacuumdb calls SELECT pg_catalog.set_config('search_path', '', false)
		// to restrict the search path for security. Accept and return new_value.
		if len(x.Args) >= 2 {
			newVal, err := evalExpr(x.Args[1], row, ctx)
			if err != nil {
				return NullDatum, nil
			}
			return newVal, nil
		}
		return NullDatum, nil
	case "current_database":
		return NewStringDatum("postgres"), nil
	case "current_schema", "current_schemas":
		return NewStringDatum("public"), nil

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
		// Treated as session-scoped for v0; released by pg_advisory_unlock_all.
		return evalAdvisoryLock(x, row, ctx, false, false)

	case "pg_try_advisory_xact_lock":
		// pg_try_advisory_xact_lock(int4, int4) → boolean  (non-blocking)
		return evalAdvisoryLock(x, row, ctx, true, false)

	case "pg_try_advisory_lock":
		// pg_try_advisory_lock(bigint) → boolean  (non-blocking)
		return evalAdvisoryLock(x, row, ctx, true, false)
	}
	return evalStoredRoutineFuncCall(x, row, ctx)
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
func evalAdvisoryLock(x *planner.FuncCall, row Row, ctx *Context, tryOnly bool, _ bool) (Datum, error) {
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
		ok := globalAdvisoryMgr.tryAcquire(key, sess)
		return NewBoolDatum(ok), nil
	}

	// Blocking acquire — respects ctx cancellation.
	qctx := ctx.Ctx
	if qctx == nil {
		qctx = context.Background()
	}
	if err := globalAdvisoryMgr.acquire(qctx, key, sess); err != nil {
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
// advisory lock held by this session and returns NULL (void-like).
func evalAdvisoryUnlockAll(ctx *Context) (Datum, error) {
	if ctx == nil {
		return NullDatum, nil
	}
	globalAdvisoryMgr.releaseAll(advisorySessionIDFromContext(ctx))
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
	if (src.Kind != KindString && src.Kind != KindStringArena) || (fmtArg.Kind != KindString && fmtArg.Kind != KindStringArena) {
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
	if src.Kind != KindString && src.Kind != KindStringArena {
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
	if (src.Kind != KindString && src.Kind != KindStringArena) || (fmtArg.Kind != KindString && fmtArg.Kind != KindStringArena) {
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
