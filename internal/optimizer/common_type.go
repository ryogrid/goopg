package optimizer

import (
	"fmt"
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
	types := make([]string, len(fc.Args))
	ptype := ""
	var pcategory byte
	ppreferred := false
	for i, a := range fc.Args {
		if isUnknownTypedArg(a) {
			continue
		}
		et := exprType(a)
		if et.IsArray {
			return nil // arrays: unmodelled
		}
		t := commonTypeCanonical(et.Name)
		cat, pref, ok := commonTypeCategory(t)
		if !ok {
			return nil // unmodelled type: keep goopg's previous behaviour
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
			return &PlanError{Pos: a.Pos(), Code: "42804",
				Message: fmt.Sprintf("%s types %s and %s cannot be matched", context,
					commonTypeDisplayName(ptype), commonTypeDisplayName(t))}
		}
		if !ppreferred && implicitlyCoercible(ptype, t) && !implicitlyCoercible(t, ptype) {
			ptype, ppreferred = t, pref
		}
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
		if types[i] != "" && !implicitlyCoercible(types[i], ptype) {
			// coerce_to_common_type: "COALESCE could not convert type %s to %s".
			return &PlanError{Pos: a.Pos(), Code: "42846",
				Message: fmt.Sprintf("%s could not convert type %s to %s", context,
					commonTypeDisplayName(types[i]), commonTypeDisplayName(ptype))}
		}
		source := types[i]
		if source == "" {
			source = "unknown"
		}
		fc.Args[i] = &CastExpr{pos: a.Pos(), Operand: a, TargetType: ptype, SourceType: source}
	}
	return nil
}
