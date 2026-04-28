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
	case *planner.CaseExpr:
		return evalCaseExpr(x, row, ctx)
	case *planner.SubqueryExpr:
		return evalSubquery(x, ctx)
	case *planner.InExpr:
		return evalInExpr(x, row, ctx)
	case *planner.ExistsExpr:
		return evalExistsExpr(x, ctx)
	case *planner.TypedStringLit:
		return evalTypedStringLit(x)
	case *planner.IntervalLit:
		return evalIntervalLit(x)
	case *planner.ExtractExpr:
		return evalExtract(x, row, ctx)
	case *planner.IntegerConst:
		return Datum{Kind: KindInt, Int: x.Value}, nil
	case *planner.NumericConst:
		// v0 surfaces the literal as a string Datum so the
		// codec stores it verbatim in the varlen text frame
		// shared with VARCHAR/CHAR. Real numeric arithmetic
		// waits on the type system.
		return Datum{Kind: KindString, String: x.Value}, nil
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
		fallthrough
	case "*", "/", "%":
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
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown operator %s", op)}
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

// compareDatum returns -1/0/1 the same way upstream's btree
// comparators do, scoped to the v0 type set.
func compareDatum(a, b Datum, pos int) (int, error) {
	if a.Kind != b.Kind {
		return 0, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("comparison across kinds %d vs %d", a.Kind, b.Kind)}
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
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("EXTRACT(%s FROM …) requires timestamp/date input", x.Field)}
	}
	t := src.Time.UTC()
	var n int64
	switch x.Field {
	case "year":
		n = int64(t.Year())
	case "month":
		n = int64(t.Month())
	case "day":
		n = int64(t.Day())
	case "hour":
		n = int64(t.Hour())
	case "minute":
		n = int64(t.Minute())
	case "second":
		n = int64(t.Second())
	case "dow":
		n = int64(t.Weekday()) // Sunday=0, matches upstream
	case "doy":
		n = int64(t.YearDay())
	case "epoch":
		n = t.Unix()
	case "quarter":
		n = int64((int(t.Month())-1)/3 + 1)
	default:
		return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(), Message: fmt.Sprintf("EXTRACT field %q is not supported in v0", x.Field)}
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
func evalExistsExpr(x *planner.ExistsExpr, ctx *Context) (Datum, error) {
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
func evalSubquery(x *planner.SubqueryExpr, ctx *Context) (Datum, error) {
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
	case "to_timestamp":
		return evalToTimestamp(x, row, ctx)
	}
	return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("function %s does not exist", x.Name)}
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
