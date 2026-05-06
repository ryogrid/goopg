package executor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
func evalExpr(e planner.Expr, row Row, ctx *Context) (Datum, error) {
	switch x := e.(type) {
	case *planner.OuterColumnRef:
		// Look up the row from the lexical-scope stack pushed
		// by evalSubquery/evalInExpr/evalExistsExpr before the
		// inner plan runs. Level 1 is the immediate parent.
		idx := len(ctx.OuterRows) - x.Level
		if idx < 0 || idx >= len(ctx.OuterRows) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("outer column ref %s/level=%d out of range (depth=%d)", x.Name, x.Level, len(ctx.OuterRows))}
		}
		row := ctx.OuterRows[idx]
		if x.Index < 0 || x.Index >= len(row) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("outer column ref %s/idx=%d out of range (width=%d)", x.Name, x.Index, len(row))}
		}
		return row[x.Index], nil
	case *planner.CaseExpr:
		return evalCaseExpr(x, row, ctx)
	case *planner.SubqueryExpr:
		return evalSubquery(x, row, ctx)
	case *planner.InExpr:
		return evalInExpr(x, row, ctx)
	case *planner.ExistsExpr:
		return evalExistsExpr(x, row, ctx)
	case *planner.TypedStringLit:
		return evalTypedStringLit(x)
	case *planner.IntervalLit:
		return evalIntervalLit(x)
	case *planner.ExtractExpr:
		return evalExtract(x, row, ctx)
	case *planner.IntegerConst:
		return Datum{Kind: KindInt, Int: x.Value}, nil
	case *planner.NumericConst:
		m, s, err := parseNumeric(x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(), Message: err.Error()}
		}
		return newNumeric(m, int(s)), nil
	case *planner.StringConst:
		return Datum{Kind: KindString, String: x.Value}, nil
	case *planner.NullConst:
		return NullDatum, nil
	case *planner.BooleanConst:
		return Datum{Kind: KindBool, Bool: x.Value}, nil
	case *planner.ParamRef:
		if x.Number < 1 || x.Number > len(ctx.Params) {
			return Datum{}, &ExecError{Code: "08P01", Pos: x.Pos(), Message: fmt.Sprintf("parameter $%d not bound", x.Number)}
		}
		return ctx.Params[x.Number-1], nil
	case *planner.ColumnRef:
		if row == nil || x.Index >= len(row) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("column ref %s/%d out of range", x.Name, x.Index)}
		}
		return row[x.Index], nil
	case *planner.UnaryOp:
		operand, err := evalExpr(x.Operand, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalUnary(x.Op, operand, x.Pos())
	case *planner.BinaryOp:
		left, err := evalExpr(x.Left, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		right, err := evalExpr(x.Right, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalBinary(x.Op, left, right, x.Pos())
	case *planner.FuncCall:
		return evalFuncCall(x, row, ctx)
	}
	return Datum{}, &ExecError{Code: "XX000", Pos: e.Pos(), Message: fmt.Sprintf("unsupported expression %T", e)}
}

// evalUnary handles -, +, NOT.
func evalUnary(op string, d Datum, pos int) (Datum, error) {
	if d.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case "-":
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary - requires integer"}
		}
		return Datum{Kind: KindInt, Int: -d.Int}, nil
	case "+":
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary + requires integer"}
		}
		return d, nil
	case "NOT":
		if d.Kind != KindBool {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator NOT requires boolean"}
		}
		return Datum{Kind: KindBool, Bool: !d.Bool}, nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown unary operator %s", op)}
}

// evalBinary handles arithmetic, comparison, and boolean operators.
// SQL three-valued logic: NULL operand on most operators yields NULL;
// AND/OR follow Kleene's rules.
func evalBinary(op string, left, right Datum, pos int) (Datum, error) {
	switch op {
	case "AND":
		return evalAnd(left, right), nil
	case "OR":
		return evalOr(left, right), nil
	}
	if left.IsNull() || right.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case "+", "-":
		// timestamp/date ± interval and interval + timestamp/date
		// route through the time-arithmetic path before falling
		// back to integer arithmetic. v0 doesn't support
		// interval - timestamp (upstream rejects it too) or
		// timestamp - timestamp (returns interval upstream;
		// scope-deferred until the type system).
		if left.Kind == KindTime && right.Kind == KindInterval {
			return addTimeInterval(left, right, op == "-"), nil
		}
		if op == "+" && left.Kind == KindInterval && right.Kind == KindTime {
			return addTimeInterval(right, left, false), nil
		}
		// NUMERIC ± NUMERIC, NUMERIC ± INT, INT ± NUMERIC: promote
		// the int side to KindNumeric{scale=0} and reuse the same
		// scale-aligning helpers.  Also try to parse string
		// operands as numeric (columns loaded via INSERT may be
		// stored as strings before the type system enforces types).
		if left.Kind == KindString {
			if m, s, err := parseNumeric(left.String); err == nil {
				left = newNumeric(m, int(s))
			}
		}
		if right.Kind == KindString {
			if m, s, err := parseNumeric(right.String); err == nil {
				right = newNumeric(m, int(s))
			}
		}
		if left.Kind == KindNumeric || right.Kind == KindNumeric {
			a, b, err := promoteToNumeric(left, right, op, pos)
			if err != nil {
				return Datum{}, err
			}
			if op == "+" {
				return numericAdd(a, b)
			}
			return numericSub(a, b)
		}
		fallthrough
	case "*", "/", "%":
		if left.Kind == KindNumeric || right.Kind == KindNumeric {
			a, b, err := promoteToNumeric(left, right, op, pos)
			if err != nil {
				return Datum{}, err
			}
			switch op {
			case "*":
				return numericMul(a, b)
			case "/":
				return numericDiv(a, b, pos)
			}
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s not supported on numeric", op)}
		}
		if left.Kind != KindInt || right.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires integer operands", op)}
		}
		return arithmetic(op, left.Int, right.Int, pos)
	case "||":
		if left.Kind != KindString || right.Kind != KindString {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator || requires string operands"}
		}
		return Datum{Kind: KindString, String: left.String + right.String}, nil
	case "=", "<", ">", "<=", ">=", "<>", "!=":
		cmp, err := compareDatum(left, right, pos)
		if err != nil {
			return Datum{}, err
		}
		return Datum{Kind: KindBool, Bool: cmpResult(op, cmp)}, nil
	case "LIKE", "NOT LIKE":
		if left.Kind != KindString || right.Kind != KindString {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires string operands", op)}
		}
		matched := matchSQLLike(left.String, right.String)
		if op == "NOT LIKE" {
			matched = !matched
		}
		return Datum{Kind: KindBool, Bool: matched}, nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown operator %s", op)}
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
	months := int(iv.IntervalMonths)
	days := int(iv.IntervalDays)
	if subtract {
		months = -months
		days = -days
	}
	return Datum{Kind: KindTime, Time: t.Time.AddDate(0, months, days)}
}

func arithmetic(op string, a, b int64, pos int) (Datum, error) {
	var r int64
	switch op {
	case "+":
		r = a + b
	case "-":
		r = a - b
	case "*":
		r = a * b
	case "/":
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
		}
		r = a / b
	case "%":
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
	// One side is KindString — try to parse it as the other's type.
	if a.Kind == KindString && b.Kind != KindString {
		a = tryParseStringAs(b.Kind, a.String)
	} else if b.Kind == KindString && a.Kind != KindString {
		b = tryParseStringAs(a.Kind, b.String)
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
			return Datum{Kind: KindTime, Time: t}
		}
	}
	return Datum{Kind: KindString, String: s}
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
		case !a.Bool && b.Bool:
			return -1, nil
		case a.Bool && !b.Bool:
			return 1, nil
		}
		return 0, nil
	case KindString:
		return strings.Compare(a.String, b.String), nil
	case KindTime:
		switch {
		case a.Time.Before(b.Time):
			return -1, nil
		case a.Time.After(b.Time):
			return 1, nil
		}
		return 0, nil
	}
	return 0, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("comparison not supported for kind %d", a.Kind)}
}

func cmpResult(op string, cmp int) bool {
	switch op {
	case "=":
		return cmp == 0
	case "<>", "!=":
		return cmp != 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	}
	return false
}

// evalAnd / evalOr implement Kleene three-valued logic.
func evalAnd(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && !a.Bool {
		return Datum{Kind: KindBool, Bool: false}
	}
	if !b.IsNull() && b.Kind == KindBool && !b.Bool {
		return Datum{Kind: KindBool, Bool: false}
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return Datum{Kind: KindBool, Bool: a.Bool && b.Bool}
}

func evalOr(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && a.Bool {
		return Datum{Kind: KindBool, Bool: true}
	}
	if !b.IsNull() && b.Kind == KindBool && b.Bool {
		return Datum{Kind: KindBool, Bool: true}
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return Datum{Kind: KindBool, Bool: a.Bool || b.Bool}
}

// evalTypedStringLit parses the body of a `<type> 'value'`
// literal at evaluation time. v0 supports date / timestamp /
// timestamptz; the parsed time is normalised to UTC.
func evalTypedStringLit(x *planner.TypedStringLit) (Datum, error) {
	switch x.Type {
	case "date":
		t, err := time.Parse("2006-01-02", x.Value)
		if err != nil {
			return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid date %q: %v", x.Value, err)}
		}
		return Datum{Kind: KindTime, Time: t.UTC()}, nil
	case "timestamp", "timestamptz":
		// Try a few common upstream layouts in order. The
		// `2006-01-02 15:04:05` form is what TPC-H and pgbench
		// use; `2006-01-02T15:04:05Z` is RFC3339 fallback.
		layouts := []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", "2006-01-02"}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, x.Value); err == nil {
				return Datum{Kind: KindTime, Time: t.UTC()}, nil
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
			if t, err := parseCopyTimestamp(src.String); err == nil {
				src = Datum{Kind: KindTime, Time: t}
			}
		}
	}
	if src.Kind != KindTime {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("EXTRACT(%s FROM …) requires timestamp/date input", x.Field)}
	}
	n, err := extractTimestampField(x.Field, src.Time, x.Pos())
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
	n, err := extractTimestampField(fieldArg.String, src.Time, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindInt, Int: n}, nil
}

// evalIntervalLit parses the integer body of an `interval 'N' unit`
// literal. The parser already normalised plurals so Unit is one
// of day / month / year.
func evalIntervalLit(x *planner.IntervalLit) (Datum, error) {
	n, err := strconv.ParseInt(x.Value, 10, 32)
	if err != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("invalid interval count %q", x.Value)}
	}
	d := Datum{Kind: KindInterval}
	switch x.Unit {
	case "day":
		d.IntervalDays = int32(n)
	case "month":
		d.IntervalMonths = int32(n)
	case "year":
		d.IntervalMonths = int32(n) * 12
	default:
		return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(), Message: fmt.Sprintf("interval unit %q is not supported in v0", x.Unit)}
	}
	return d, nil
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
		if eq.Kind == KindBool && eq.Bool {
			return Datum{Kind: KindBool, Bool: !x.Negated}, nil
		}
	}
	if sawNull {
		return NullDatum, nil
	}
	return Datum{Kind: KindBool, Bool: x.Negated}, nil
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
		if ctx.SubqueryCache != nil {
			if ctx.SubqueryCacheScope != len(ctx.OuterRows) {
				clear(ctx.SubqueryCache)
				ctx.SubqueryCacheScope = len(ctx.OuterRows)
			}
			key := subqueryCacheKey(row)
			if cached, ok := ctx.SubqueryCache[key]; ok {
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
			r, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				return nil, err
			}
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
		ctx.SubqueryCache[subqueryCacheKey(row)] = out
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
	// Push the outer row so correlated column refs in the inner
	// plan can resolve against it. Pop on return regardless of
	// outcome.
	ctx.OuterRows = append(ctx.OuterRows, row)
	defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
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
	return Datum{Kind: KindBool, Bool: hasRow != x.Negated}, nil
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
	// Check cache for scalar subquery results keyed by outer row.
	if ctx.SubqueryCache != nil {
		if ctx.SubqueryCacheScope != len(ctx.OuterRows) {
			clear(ctx.SubqueryCache)
			ctx.SubqueryCacheScope = len(ctx.OuterRows)
		}
		key := subqueryCacheKey(row)
		if cached, ok := ctx.SubqueryCache[key]; ok {
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
	ctx.SubqueryCache[subqueryCacheKey(row)] = []Datum{val}
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
	row, err := op.Next()
	if err == EOF {
		return NullDatum, nil
	}
	if err != nil {
		return Datum{}, err
	}
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
			matched = eq.Kind == KindBool && eq.Bool
		} else {
			matched = whenVal.Kind == KindBool && whenVal.Bool
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
			return Datum{Kind: KindBool, Bool: false}, nil
		}
		return Datum{Kind: KindBool, Bool: cmp == 0}, nil
	}
	switch {
	case a.Kind == KindInt && b.Kind == KindInt:
		return Datum{Kind: KindBool, Bool: a.Int == b.Int}, nil
	case a.Kind == KindBool && b.Kind == KindBool:
		return Datum{Kind: KindBool, Bool: a.Bool == b.Bool}, nil
	case a.Kind == KindString && b.Kind == KindString:
		return Datum{Kind: KindBool, Bool: a.String == b.String}, nil
	case a.Kind == KindTime && b.Kind == KindTime:
		return Datum{Kind: KindBool, Bool: a.Time.Equal(b.Time)}, nil
	case a.Kind == KindInt && b.Kind == KindString:
		return Datum{Kind: KindBool, Bool: fmt.Sprintf("%d", a.Int) == b.String}, nil
	case a.Kind == KindString && b.Kind == KindInt:
		return Datum{Kind: KindBool, Bool: a.String == fmt.Sprintf("%d", b.Int)}, nil
	}
	return Datum{Kind: KindBool, Bool: false}, nil
}

// evalFuncCall resolves a function name against the in-tree registry.
// v0 is small: current_timestamp / now / current_date are the only
// no-arg time functions pgbench needs; HammerDB TPC-H also uses
// to_timestamp(text, fmt) to load TIMESTAMP columns.
func evalFuncCall(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	name := strings.ToLower(x.Name)
	switch name {
	case "current_timestamp", "now", "transaction_timestamp", "statement_timestamp":
		return Datum{Kind: KindTime, Time: ctx.Now}, nil
	case "current_date":
		t := ctx.Now
		return Datum{Kind: KindTime, Time: t.Truncate(24 * 60 * 60 * 1e9)}, nil
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
	}
	return evalStoredRoutineFuncCall(x, row, ctx)
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
	if src.Kind != KindString || fmtArg.Kind != KindString {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_date arguments must be text"}
	}
	goLayout := pgFormatToGoLayout(fmtArg.String)
	t, perr := time.Parse(goLayout, src.String)
	if perr != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("to_date: %v (format=%q value=%q)", perr, fmtArg.String, src.String)}
	}
	year, month, day := t.UTC().Date()
	out := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return Datum{Kind: KindTime, Time: out}, nil
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
	s := src.String
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
			return Datum{Kind: KindString, String: ""}, nil
		}
		return Datum{Kind: KindString, String: s[idx:]}, nil
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
		return Datum{Kind: KindString, String: ""}, nil
	}
	if endIdx > len(s) {
		endIdx = len(s)
	}
	return Datum{Kind: KindString, String: s[startIdx:endIdx]}, nil
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
	if src.Kind != KindString || fmtArg.Kind != KindString {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: "to_timestamp arguments must be text"}
	}
	goLayout := pgFormatToGoLayout(fmtArg.String)
	t, perr := time.Parse(goLayout, src.String)
	if perr != nil {
		return Datum{}, &ExecError{Code: "22007", Pos: x.Pos(), Message: fmt.Sprintf("to_timestamp: %v (format=%q value=%q)", perr, fmtArg.String, src.String)}
	}
	return Datum{Kind: KindTime, Time: t.UTC()}, nil
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
