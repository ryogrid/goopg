package optimizer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
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
