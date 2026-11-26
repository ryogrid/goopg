package optimizer

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// commonTypeCanonical maps the spellings exprType returns (SQL-standard
// aliases included: integer, bigint, double precision, character varying,
// timestamp with time zone, ...) to the internal type name.
func commonTypeCanonical(name string) string {
	switch n := strings.ToLower(strings.TrimSpace(name)); n {
	case "smallint", "smallserial", "serial2":
		return "int2"
	case "integer", "int", "serial", "serial4":
		return "int4"
	case "bigint", "bigserial", "serial8":
		return "int8"
	case "decimal":
		return "numeric"
	case "real":
		return "float4"
	case "double precision", "float":
		return "float8"
	case "character varying":
		return "varchar"
	case "character", "char":
		return "bpchar"
	case "boolean":
		return "bool"
	case "timestamp without time zone":
		return "timestamp"
	case "timestamp with time zone":
		return "timestamptz"
	case "time without time zone":
		return "time"
	case "time with time zone":
		return "timetz"
	default:
		return n
	}
}

// commonTypeCategory is pg_type.typcategory for the types select_common_type
// is modelled over here, with the category's preferred type.
func commonTypeCategory(name string) (category byte, preferred bool, ok bool) {
	switch strings.ToLower(name) {
	case "int2", "int4", "int8", "numeric", "float4":
		return 'N', false, true
	case "float8":
		return 'N', true, true
	case "text":
		return 'S', true, true
	case "varchar", "bpchar", "name":
		return 'S', false, true
	case "date", "timestamp", "time", "timetz":
		return 'D', false, true
	case "timestamptz":
		return 'D', true, true
	case "interval":
		return 'T', true, true
	case "bool":
		return 'B', true, true
	}
	return 0, false, false
}

// implicitlyCoercible reports pg_cast's implicit (or binary-coercible)
// conversions among the modelled types — can_coerce_type(..., COERCION_IMPLICIT).
func implicitlyCoercible(from, to string) bool {
	from, to = strings.ToLower(from), strings.ToLower(to)
	if from == to {
		return true
	}
	if fr, tr := setOpNumericRank(from), setOpNumericRank(to); fr > 0 && tr > 0 {
		return fr < tr // int2 < int4 < int8 < numeric < float4 < float8
	}
	switch from {
	case "varchar", "bpchar", "name":
		return to == "text" || to == "varchar" || to == "bpchar" || to == "name"
	case "text":
		return to == "varchar" || to == "bpchar" || to == "name"
	case "date":
		return to == "timestamp" || to == "timestamptz"
	case "timestamp":
		return to == "timestamptz"
	case "time":
		return to == "timetz" || to == "interval"
	}
	return false
}

// commonTypeDisplayName is format_type_be's spelling for the error text.
func commonTypeDisplayName(name string) string {
	switch strings.ToLower(name) {
	case "int2":
		return "smallint"
	case "int4":
		return "integer"
	case "int8":
		return "bigint"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "varchar":
		return "character varying"
	case "bpchar":
		return "character"
	case "bool":
		return "boolean"
	case "timestamp":
		return "timestamp without time zone"
	case "timestamptz":
		return "timestamp with time zone"
	case "time":
		return "time without time zone"
	case "timetz":
		return "time with time zone"
	}
	return strings.ToLower(name)
}

// isUnknownTypedArg reports a NULL or an untyped string literal — the
// arguments select_common_type skips (type unknown).
func isUnknownTypedArg(e Expr) bool {
	if _, ok := e.(*NullConst); ok {
		return true
	}
	_, ok := e.(*StringConst)
	return ok
}

// selectCommonType is select_common_type (parse_coerce.c) over exprs. It returns
// the common type, "" when every expression is untyped, and each expression's
// canonical type. modelled is false when a type lies outside the categories goopg
// models, in which case the caller keeps its own behaviour.
func selectCommonType(context string, exprs []Expr) (ptype string, types []string, modelled bool, err error) {
	types = make([]string, len(exprs))
	var pcategory byte
	ppreferred := false
	for i, a := range exprs {
		if isUnknownTypedArg(a) {
			continue
		}
		et := exprType(a)
		if et.IsArray {
			return "", nil, false, nil // arrays: unmodelled
		}
		t := commonTypeCanonical(et.Name)
		cat, pref, ok := commonTypeCategory(t)
		if !ok {
			return "", nil, false, nil
		}
		types[i] = t
		if ptype == "" {
			ptype, pcategory, ppreferred = t, cat, pref
			continue
		}
		if t == ptype {
			continue
		}
		if cat != pcategory {
			return "", nil, false, &PlanError{Pos: exprStartPos(a), Code: "42804",
				Message: fmt.Sprintf("%s types %s and %s cannot be matched", context,
					commonTypeDisplayName(ptype), commonTypeDisplayName(t))}
		}
		if !ppreferred && implicitlyCoercible(ptype, t) && !implicitlyCoercible(t, ptype) {
			ptype, ppreferred = t, pref
		}
	}
	return ptype, types, true, nil
}

// exprStartPos is exprLocation for a cast, which reports its operand's start.
func exprStartPos(e Expr) int {
	if c, ok := e.(*CastExpr); ok {
		return exprStartPos(c.Operand)
	}
	return e.Pos()
}

// checkCommonTypeCoercion is the failure arm of coerce_to_common_type.
func checkCommonTypeCoercion(context string, e Expr, from, to string) error {
	if from == "" || from == to || implicitlyCoercible(from, to) {
		return nil
	}
	return &PlanError{Pos: exprStartPos(e), Code: "42846",
		Message: fmt.Sprintf("%s could not convert type %s to %s", context,
			commonTypeDisplayName(from), commonTypeDisplayName(to))}
}

// coerceCommonTypeArgs is transformCoalesceExpr / transformMinMaxExpr
// (parse_expr.c) for COALESCE, GREATEST and LEAST: select_common_type
// (parse_coerce.c) over the arguments, then coerce_to_common_type on each
// one. goopg took the first typed argument's type and coerced nothing, so
// coalesce(1, 2.5) was integer and `coalesce(a, 2.5) / 3` divided in
// integers (M0146-0126).
//
// The common type follows PG's walk: the first typed argument starts it; a
// later argument of another category is an error ("COALESCE types integer and
// text cannot be matched", positioned at that argument); within a category the
// type moves to a later argument's when the current one is not the category's
// preferred type and converts to it implicitly but not back. Arguments of a
// different type — untyped literals included, as PG coerces them at parse
// time — are wrapped in a planner-inserted cast. All-untyped arguments
// resolve to text. Types outside the modelled categories leave the call as it
// was.
func coerceCommonTypeArgs(fc *FuncCall) error {
	var context string
	switch strings.ToLower(fc.Name) {
	case "nullif":
		if len(fc.Args) == 2 {
			fc.Args[0], fc.Args[1] = coerceTimestampOperands(fc.Args[0], fc.Args[1])
		}
		return nil
	case "coalesce":
		context = "COALESCE"
	case "greatest":
		context = "GREATEST"
	case "least":
		context = "LEAST"
	default:
		return nil
	}
	ptype, types, modelled, err := selectCommonType(context, fc.Args)
	if err != nil || !modelled {
		return err
	}
	if ptype == "" {
		// Every argument untyped: select_common_type picks text. With only
		// NULLs there is no literal to cast, so the first NULL carries it.
		ptype = "text"
		onlyNulls := true
		for _, a := range fc.Args {
			if _, isNull := a.(*NullConst); !isNull {
				onlyNulls = false
				break
			}
		}
		if onlyNulls && len(fc.Args) > 0 {
			fc.Args[0] = &CastExpr{pos: fc.Args[0].Pos(), Operand: fc.Args[0], TargetType: "text", SourceType: "unknown"}
			return nil
		}
	}
	for i, a := range fc.Args {
		if _, isNull := a.(*NullConst); isNull {
			continue
		}
		if types[i] == ptype {
			continue
		}
		if err := checkCommonTypeCoercion(context, a, types[i], ptype); err != nil {
			return err
		}
		source := types[i]
		if source == "" {
			source = "unknown"
		}
		fc.Args[i] = &CastExpr{pos: a.Pos(), Operand: a, TargetType: ptype, SourceType: source}
	}
	return nil
}

// generateSeriesType is the overload function resolution picks for
// generate_series: its int4, int8, numeric, timestamp or timestamptz form. An
// interval step or a zone argument selects the timestamp forms, where a timestamp
// argument wins unless a timestamptz is present or a zone is given, and otherwise
// the preferred type timestamptz applies, dates and unknown literals included.
func generateSeriesType(args []Expr) string {
	var plain, zoned, datetime, numeric, wide bool
	for i, a := range args {
		if isUnknownTypedArg(a) {
			continue
		}
		if c, ok := a.(*IntegerConst); ok {
			wide = wide || c.Value > math.MaxInt32 || c.Value < math.MinInt32
			continue
		}
		switch commonTypeCanonical(exprType(a).Name) {
		case "timestamp":
			plain, datetime = true, true
		case "timestamptz":
			zoned, datetime = true, true
		case "date":
			datetime = true
		case "interval":
			datetime = datetime || i == 2
		case "numeric", "float4", "float8":
			numeric = true
		case "int8":
			wide = true
		}
	}
	switch {
	case datetime || len(args) == 4:
		if plain && !zoned && len(args) != 4 {
			return "timestamp"
		}
		return "timestamptz"
	case numeric:
		return "numeric"
	case wide:
		return "int8"
	}
	return "int4"
}

// checkGenerateSeriesArgs rejects the call function resolution cannot settle:
// three untyped literals fit both timestamp forms and nothing prefers one.
func checkGenerateSeriesArgs(args []Expr, pos int) error {
	if len(args) != 3 {
		return nil
	}
	for _, a := range args {
		if _, ok := a.(*StringConst); !ok {
			return nil
		}
	}
	return &PlanError{Pos: pos, Code: "42725",
		Message: "function generate_series(unknown, unknown, unknown) is not unique",
		Hint:    "Could not choose a best candidate function. You might need to add explicit type casts."}
}

// coerceGenerateSeriesLiteral gives a literal argument the parameter's type, as
// parse analysis does, so EXPLAIN shows it that way.
func coerceGenerateSeriesLiteral(e Expr, argIdx int, typeName string) Expr {
	if x, ok := e.(*StringConst); ok && (typeName == "numeric" || (argIdx < 2 && castTargetTakesStringLiteral(typeName))) {
		return &TypedStringLit{pos: x.Pos(), Type: typeName, Value: x.Value}
	}
	if x, ok := e.(*IntegerConst); ok && typeName == "numeric" {
		return &TypedStringLit{pos: x.Pos(), Type: typeName, Value: strconv.FormatInt(x.Value, 10)}
	}
	return e
}

// newGenerateSeriesCol builds the target-list form from resolved arguments.
func newGenerateSeriesCol(colIdx int, args []Expr) SrfCol {
	typeName := generateSeriesType(args)
	args = append([]Expr(nil), args...)
	for i, a := range args {
		args[i] = coerceGenerateSeriesLiteral(a, i, typeName)
	}
	sc := SrfCol{ColIdx: colIdx, Start: args[0], Stop: args[1], Type: typeName}
	if len(args) > 2 {
		sc.Step = args[2]
	}
	if len(args) > 3 {
		sc.Zone = args[3]
	}
	return sc
}

// timestampLiteralType is the type a bare literal takes opposite e: timestamp or
// timestamptz, or "" for any other operand. Those two need it at plan time. Left as
// text, a literal would be read without the session zone for timestamptz and with
// its own zone for timestamp, where PG reads and discards that zone.
func timestampLiteralType(e Expr) string {
	et := exprType(e)
	if et.IsArray {
		return ""
	}
	switch t := commonTypeCanonical(et.Name); t {
	case "timestamp", "timestamptz":
		return t
	}
	return ""
}

// typedTimestampLiteral is coerce_type for an unknown literal of that type.
func typedTimestampLiteral(e Expr, typeName string) Expr {
	if lit, ok := e.(*StringConst); ok && typeName != "" {
		return &TypedStringLit{pos: lit.Pos(), Type: typeName, Value: lit.Value}
	}
	return e
}

// coerceDatetimeArrayOperands casts a bare literal opposite a date or time array to
// that array type, so its elements are read as the element type.
func coerceDatetimeArrayOperands(l, r Expr) (Expr, Expr) {
	cast := func(lit, other Expr) Expr {
		s, ok := lit.(*StringConst)
		t := exprType(other)
		if !ok || !t.IsArray {
			return lit
		}
		switch n := commonTypeCanonical(strings.TrimSuffix(t.Name, "[]")); n {
		case "date", "time", "timetz", "timestamp", "timestamptz", "interval":
			return &CastExpr{pos: s.Pos(), Operand: s, TargetType: n + "[]", SourceType: "unknown"}
		case "":
			return lit
		default:
			// Any other element type: the unknown literal takes the array
			// type (array_in), so `a = '{1, 2, 3}'` compares the
			// normalised array, not the literal's spelling — EXPLAIN on PG
			// shows `'{1,2,3}'::integer[]`. M0149-0046k.
			return &CastExpr{pos: s.Pos(), Operand: s, TargetType: n + "[]", SourceType: "unknown"}
		}
	}
	return cast(l, r), cast(r, l)
}

// coerceTimestampOperands types a bare literal opposite a timestamp or timestamptz.
func coerceTimestampOperands(l, r Expr) (Expr, Expr) {
	if lr, ok := l.(*RowExpr); ok {
		if rr, ok := r.(*RowExpr); ok && len(lr.Elems) == len(rr.Elems) {
			for i := range lr.Elems {
				lr.Elems[i], rr.Elems[i] = coerceTimestampOperands(lr.Elems[i], rr.Elems[i])
				lr.Types[i], rr.Types[i] = exprType(lr.Elems[i]), exprType(rr.Elems[i])
			}
		}
		return l, r
	}
	if t := timestampLiteralType(l); t != "" {
		return l, typedTimestampLiteral(r, t)
	}
	return typedTimestampLiteral(l, timestampLiteralType(r)), r
}

// timeArithCall resolves the time and timetz operators of pg_operator to their
// functions, as moneyArithCall does for money. An untyped literal takes the type
// PG's operator resolution picks: interval, except that time - 'x' is time.
func timeArithCall(pos int, op parser.OpCode, l, r Expr) (Expr, bool, error) {
	kind := func(e Expr) string {
		if _, ok := e.(*StringConst); ok {
			return "unknown"
		}
		if _, ok := e.(*NullConst); ok {
			return "unknown"
		}
		if c, ok := e.(*IntegerConst); ok && c.Value >= math.MinInt32 && c.Value <= math.MaxInt32 {
			return "int4" // an integer literal is int4 when it fits (make_const)
		}
		t := exprType(e)
		if t.IsArray {
			return ""
		}
		return commonTypeCanonical(t.Name)
	}
	lk, rk := kind(l), kind(r)
	result, status := parser.ResolveTimeOperator(op, lk, rk)
	switch status {
	case parser.TimeOperatorNone:
		return nil, false, nil
	case parser.TimeOperatorMissing, parser.TimeOperatorAmbiguous:
		code, message, hint := parser.TimeOperatorError(status, op, lk, rk)
		return nil, false, &PlanError{Pos: pos, Code: code, Message: message, Hint: hint}
	}
	sub := op == parser.OpSub
	// An untyped operand is time in time - unknown and unknown - time, and
	// interval everywhere else.
	literalType := "interval"
	if sub && (lk == "time" || rk == "time") {
		literalType = "time"
	}
	literal := func(e Expr) Expr {
		lit, ok := e.(*StringConst)
		if !ok {
			return e // NULL needs no conversion: every function here is strict.
		}
		if castTargetTakesStringLiteral(literalType) {
			return &TypedStringLit{pos: lit.Pos(), Type: literalType, Value: lit.Value}
		}
		return &CastExpr{pos: lit.Pos(), Operand: lit, TargetType: literalType, SourceType: "unknown"}
	}
	if lk == "unknown" {
		l, lk = literal(l), literalType
	}
	if rk == "unknown" {
		r, rk = literal(r), literalType
	}
	asInterval := func(e Expr) Expr {
		return &CastExpr{pos: e.Pos(), Operand: e, TargetType: "interval", SourceType: "time"}
	}
	isTime := func(n string) bool { return n == "time" || n == "timetz" }
	// timedate_pl, timetzdate_pl and the interval + x operators are SQL functions
	// that PG inlines with their arguments reversed, so EXPLAIN shows them that way.
	if !sub && isTime(lk) && rk == "date" {
		l, r, lk, rk = r, l, rk, lk
	}
	if !sub && lk == "time" && rk != "interval" {
		l, lk = asInterval(l), "interval"
	}
	if rk == "time" && lk != "time" && (sub || (lk != "date" && lk != "interval")) {
		r, rk = asInterval(r), "interval"
	}
	if !sub && lk == "interval" && rk != "interval" {
		l, r, lk, rk = r, l, rk, lk
	}
	fn := ""
	switch {
	case isTime(lk) && rk == "interval" && sub:
		fn = lk + "_mi_interval"
	case isTime(lk) && rk == "interval":
		fn = lk + "_pl_interval"
	case lk == "time" && rk == "time":
		fn = "time_mi_time"
	case lk == "date" && rk == "time" && !sub:
		fn = "datetime_pl"
	case lk == "date" && rk == "timetz" && !sub:
		fn = "datetimetz_pl"
	default:
		// Only the implicit cast was needed: an ordinary date/time operator remains.
		return &BinaryOp{pos: pos, Op: op, Left: l, Right: r, ResultType: result}, true, nil
	}
	return &FuncCall{pos: pos, Name: fn, Args: []Expr{l, r}, ReturnType: result, Operator: op.String()}, true, nil
}

// dateTimeLiteralCall resolves operators on timestamp, timestamptz, date and
// interval with an untyped literal operand, and an interval multiplied or
// divided by a number, through parser.ResolveDateTimeLiteralOperator. The
// analyzer counterpart is dateTimeLiteralResultType.
func dateTimeLiteralCall(pos int, op parser.OpCode, l, r Expr) (Expr, bool, error) {
	untyped := func(e Expr) bool {
		if _, ok := e.(*StringConst); ok {
			return true
		}
		_, ok := e.(*NullConst)
		return ok
	}
	name := func(e Expr) string {
		if untyped(e) {
			return "unknown"
		}
		if c, ok := e.(*IntegerConst); ok && c.Value >= math.MinInt32 && c.Value <= math.MaxInt32 {
			return "int4"
		}
		t := exprType(e)
		if t.IsArray {
			return ""
		}
		return commonTypeCanonical(t.Name)
	}
	kind := func(n string) string {
		switch n {
		case "int2", "int4", "int8", "numeric", "float4", "float8":
			return "number"
		}
		return n
	}
	ln, rn := name(l), name(r)
	result, lit, status := parser.ResolveDateTimeLiteralOperator(op, kind(ln), kind(rn))
	switch status {
	case parser.TimeOperatorNone:
		return nil, false, nil
	case parser.TimeOperatorMissing, parser.TimeOperatorAmbiguous:
		code, message, hint := parser.TimeOperatorError(status, op, ln, rn)
		return nil, false, &PlanError{Pos: pos, Code: code, Message: message, Hint: hint}
	}
	literal := func(e Expr) Expr {
		sc, ok := e.(*StringConst)
		if !ok || lit == "" {
			return e
		}
		if castTargetTakesStringLiteral(lit) {
			return &TypedStringLit{pos: sc.Pos(), Type: lit, Value: sc.Value}
		}
		return &CastExpr{pos: sc.Pos(), Operand: sc, TargetType: lit, SourceType: "unknown"}
	}
	l, r = literal(l), literal(r)
	if op == parser.OpMul && rn == "interval" {
		// mul_d_interval: the number, as float8, times the interval. A
		// constant number is a float8 Const after parse analysis, and EXPLAIN
		// prints it so; a column's implicit cast stays hidden.
		_, isInt := l.(*IntegerConst)
		_, isNum := l.(*NumericConst)
		if isInt || isNum {
			l = &CastExpr{pos: l.Pos(), Operand: l, TargetType: "float8", SourceType: ln, Explicit: true}
		}
		return &FuncCall{pos: pos, Name: "mul_d_interval", Args: []Expr{l, r}, ReturnType: "interval", Operator: "*"}, true, nil
	}
	return &BinaryOp{pos: pos, Op: op, Left: l, Right: r, ResultType: result}, true, nil
}

