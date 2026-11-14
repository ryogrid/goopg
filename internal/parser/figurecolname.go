package parser

// FigureIndexColname is parse_target.c FigureIndexColname: the name
// transformIndexStmt gives an expression index column (indexcolname), which
// ChooseIndexColumnNames then uses for the index's attribute names and its
// generated name — `create index on t (abs(b))` is `t_abs_idx`. It returns
// "" where PG's FigureColnameInternal has no name (the caller falls back to
// "expr", ChooseIndexColumnNames' default).
//
// The arms follow FigureColnameInternal over goopg's raw AST; constructs
// goopg parses as a plain function call (coalesce, nullif, greatest, least,
// the SQL value functions) take the FuncCall arm, which names them the
// same way PG's dedicated arms do.
func FigureIndexColname(e Expr) string {
	name, _ := figureColnameInternal(e)
	return name
}

// figureColnameInternal returns the name and PG's strength: 0 no
// information, 1 second-best choice, 2 good choice.
func figureColnameInternal(e Expr) (string, int) {
	switch x := e.(type) {
	case nil:
		return "", 0
	case *ColumnRef:
		if x.Column != "" {
			return x.Column, 2
		}
	case *FieldSelect:
		if x.Field != "" {
			return x.Field, 2
		}
		return figureColnameInternal(x.Arg)
	case *FuncCall:
		return x.Name.Name, 2
	case *CastExpr:
		name, strength := figureColnameInternal(x.Operand)
		if strength <= 1 && x.Type.Name != "" {
			return systemTypeColname(x.Type), 1
		}
		return name, strength
	case *CollateExpr:
		return figureColnameInternal(x.Operand)
	case *GroupingCall:
		return "grouping", 2
	case *ExistsExpr:
		return "exists", 2
	case *ArraySubqueryExpr, *ArrayConstructorExpr:
		return "array", 2
	case *SubqueryExpr:
		// EXPR_SUBLINK: the subquery's single target's name.
		if x.Inner != nil && len(x.Inner.Targets) > 0 {
			t := x.Inner.Targets[0]
			if t.Alias != "" {
				return t.Alias, 2
			}
			if name, strength := figureColnameInternal(t.Expr); strength > 0 {
				return name, 2
			}
			return "?column?", 2
		}
	case *CaseExpr:
		name, strength := figureColnameInternal(x.Else)
		if strength <= 1 {
			return "case", 1
		}
		return name, strength
	case *RowExpr:
		return "row", 2
	}
	return "", 0
}

// systemTypeColname is the last name of a cast's TypeName as PG's grammar
// builds it: the SQL-standard type keywords become SystemTypeName("int4")
// and the like (gram.y Numeric / Character / Bit rules), so `1::integer`
// names its column int4.
func systemTypeColname(t ObjectName) string {
	if t.Schema != "" {
		return t.Name
	}
	switch t.Name {
	case "int", "integer":
		return "int4"
	case "smallint":
		return "int2"
	case "bigint":
		return "int8"
	case "real":
		return "float4"
	case "float", "double precision":
		return "float8"
	case "decimal", "dec":
		return "numeric"
	case "boolean":
		return "bool"
	case "char", "character", "nchar", "national character":
		return "bpchar"
	case "character varying", "char varying", "national character varying", "nchar varying":
		return "varchar"
	}
	return t.Name
}
