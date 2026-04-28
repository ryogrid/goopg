// Package analyzer performs semantic validation on parsed SQL trees.
//
// v0 keeps this intentionally small: name resolution plus lightweight
// expression type checks across DML/SELECT statements.
package analyzer

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// AnalyzeError is a structured analyzer failure with SQLSTATE-style code.
type AnalyzeError struct {
	Pos     int
	Code    string
	Message string
}

func (e *AnalyzeError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("analyzer error: %s (byte %d)", e.Message, e.Pos)
	}
	return fmt.Sprintf("%s: %s (byte %d)", e.Code, e.Message, e.Pos)
}

func analyzeError(pos int, code, msg string) *AnalyzeError {
	return &AnalyzeError{Pos: pos, Code: code, Message: msg}
}

type scope struct {
	rels []scopeRel
}

type scopeRel struct {
	table *catalog.Table
	alias string
}

// Analyze validates one parsed statement semantically.
func Analyze(stmt parser.Stmt, cat catalog.Catalog) error {
	switch s := stmt.(type) {
	case *parser.SelectStmt:
		return analyzeSelect(s, cat)
	case *parser.InsertStmt:
		return analyzeInsert(s, cat)
	case *parser.UpdateStmt:
		return analyzeUpdate(s, cat)
	case *parser.DeleteStmt:
		return analyzeDelete(s, cat)
	default:
		return nil
	}
}

func analyzeSelect(s *parser.SelectStmt, cat catalog.Catalog) error {
	if s.Distinct {
		return analyzeError(s.Pos(), "0A000", "DISTINCT is not supported in v0 planner")
	}
	if s.SetOp != nil {
		return analyzeError(s.SetOp.Pos(), "0A000", "set operations are not supported in v0 planner")
	}

	ctx := &scope{}
	if len(s.From) > 0 {
		rels, err := buildSelectScope(s, cat)
		if err != nil {
			return err
		}
		ctx.rels = rels
	}

	if err := analyzeTargets(s.Targets, ctx); err != nil {
		return err
	}
	if err := analyzeWhere(s.Where, ctx); err != nil {
		return err
	}
	for _, g := range s.GroupBy {
		if _, err := analyzeExpr(g, ctx); err != nil {
			return err
		}
	}
	if s.Having != nil {
		typ, err := analyzeExpr(s.Having, ctx)
		if err != nil {
			return err
		}
		if !isBooleanLike(typ) {
			return analyzeError(s.Having.Pos(), "42804", "HAVING condition must be type boolean")
		}
	}
	for _, sb := range s.OrderBy {
		if _, err := analyzeExpr(sb.Expr, ctx); err != nil {
			return err
		}
	}
	if s.Limit != nil {
		typ, err := analyzeExpr(s.Limit, ctx)
		if err != nil {
			return err
		}
		if !isIntegerLike(typ) {
			return analyzeError(s.Limit.Pos(), "42804", "LIMIT must be an integer expression")
		}
	}
	if s.Offset != nil {
		typ, err := analyzeExpr(s.Offset, ctx)
		if err != nil {
			return err
		}
		if !isIntegerLike(typ) {
			return analyzeError(s.Offset.Pos(), "42804", "OFFSET must be an integer expression")
		}
	}
	return nil
}

func analyzeInsert(s *parser.InsertStmt, cat catalog.Catalog) error {
	tbl, err := lookupTable(cat, s.Target)
	if err != nil {
		return err
	}
	if len(s.Returning) > 0 {
		return analyzeError(s.Pos(), "0A000", "RETURNING is not supported in v0 planner")
	}
	if len(s.Rows) == 0 {
		return analyzeError(s.Pos(), "42601", "INSERT requires at least one row")
	}

	targetCols, err := resolveInsertTargetColumns(tbl, cat, s)
	if err != nil {
		return err
	}
	for _, row := range s.Rows {
		if len(row) != len(targetCols) {
			return analyzeError(s.Pos(), "42601", fmt.Sprintf("INSERT row has %d values, target expects %d", len(row), len(targetCols)))
		}
		for i, e := range row {
			typ, err := analyzeExpr(e, nil)
			if err != nil {
				return err
			}
			if !isAssignable(typ, targetCols[i].Type) {
				return analyzeError(e.Pos(), "42804", fmt.Sprintf("column %q has type %q but expression has type %q", targetCols[i].Name, targetCols[i].Type.Name, typ.Name))
			}
		}
	}
	return nil
}

func analyzeUpdate(s *parser.UpdateStmt, cat catalog.Catalog) error {
	tbl, err := lookupTable(cat, s.Target)
	if err != nil {
		return err
	}
	if len(s.Returning) > 0 {
		return analyzeError(s.Pos(), "0A000", "RETURNING is not supported in v0 planner")
	}
	ctx := &scope{rels: []scopeRel{{table: tbl, alias: s.Target.Alias}}}
	if err := analyzeWhere(s.Where, ctx); err != nil {
		return err
	}
	for _, assign := range s.Set {
		col, ok := lookupColumn(tbl, assign.Column)
		if !ok {
			return analyzeError(assign.Pos(), "42703", fmt.Sprintf("column %q of relation %q does not exist", assign.Column, tbl.Name))
		}
		typ, err := analyzeExpr(assign.Expr, ctx)
		if err != nil {
			return err
		}
		if !isAssignable(typ, col.Type) {
			return analyzeError(assign.Expr.Pos(), "42804", fmt.Sprintf("column %q has type %q but expression has type %q", col.Name, col.Type.Name, typ.Name))
		}
	}
	return nil
}

func analyzeDelete(s *parser.DeleteStmt, cat catalog.Catalog) error {
	tbl, err := lookupTable(cat, s.Target)
	if err != nil {
		return err
	}
	if len(s.Returning) > 0 {
		return analyzeError(s.Pos(), "0A000", "RETURNING is not supported in v0 planner")
	}
	ctx := &scope{rels: []scopeRel{{table: tbl, alias: s.Target.Alias}}}
	return analyzeWhere(s.Where, ctx)
}

func analyzeWhere(where parser.Expr, ctx *scope) error {
	if where == nil {
		return nil
	}
	typ, err := analyzeExpr(where, ctx)
	if err != nil {
		return err
	}
	if !isBooleanLike(typ) {
		return analyzeError(where.Pos(), "42804", "WHERE condition must be type boolean")
	}
	return nil
}

func analyzeTargets(targets []parser.ResTarget, ctx *scope) error {
	for _, t := range targets {
		if star, ok := t.Expr.(*parser.StarExpr); ok {
			if err := analyzeStar(star, ctx); err != nil {
				return err
			}
			continue
		}
		if _, err := analyzeExpr(t.Expr, ctx); err != nil {
			return err
		}
	}
	return nil
}

func analyzeStar(star *parser.StarExpr, ctx *scope) error {
	if ctx == nil || len(ctx.rels) == 0 {
		return analyzeError(star.Pos(), "42601", "SELECT * with no FROM clause")
	}
	if star.Table == "" && star.Schema == "" {
		return nil
	}
	matches := 0
	for _, rel := range ctx.rels {
		if scopeRelMatches(rel, star.Table, star.Schema) {
			matches++
		}
	}
	if matches == 0 {
		return analyzeError(star.Pos(), "42P01", fmt.Sprintf("missing FROM-clause entry for table %q", star.Table))
	}
	if matches > 1 {
		return analyzeError(star.Pos(), "42702", fmt.Sprintf("table reference %q is ambiguous", star.Table))
	}
	return nil
}

func analyzeExpr(e parser.Expr, ctx *scope) (catalog.Type, error) {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return catalog.Type{Name: "int8"}, nil
	case *parser.StringConst:
		return catalog.Type{Name: "text"}, nil
	case *parser.NullConst:
		return catalog.Type{Name: "unknown"}, nil
	case *parser.BooleanConst:
		return catalog.Type{Name: "bool"}, nil
	case *parser.ParamRef:
		return catalog.Type{Name: "unknown"}, nil
	case *parser.ColumnRef:
		return resolveColumnRefType(x, ctx)
	case *parser.StarExpr:
		return catalog.Type{}, analyzeError(x.Pos(), "42601", "'*' is not allowed here")
	case *parser.CastExpr:
		// v0 treats `expr::type` as a no-op for type-checking. We
		// recurse into the operand so analysis errors inside it
		// surface, but report `unknown` for the cast result so
		// downstream comparison-compatibility checks pass without
		// us implementing a real type lattice. This is good enough
		// for shapes like `oid=$1::pg_catalog.regclass` where the
		// declared target type isn't a goopg-tracked type at all.
		if _, err := analyzeExpr(x.Operand, ctx); err != nil {
			return catalog.Type{}, err
		}
		return catalog.Type{Name: "unknown"}, nil
	case *parser.UnaryOp:
		opTyp, err := analyzeExpr(x.Operand, ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		switch strings.ToUpper(x.Op) {
		case "+", "-":
			if !isNumericLike(opTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires a numeric operand", x.Op))
			}
			return opTyp, nil
		case "NOT":
			if !isBooleanLike(opTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", "operator NOT requires a boolean operand")
			}
			return catalog.Type{Name: "bool"}, nil
		default:
			return catalog.Type{Name: "unknown"}, nil
		}
	case *parser.BinaryOp:
		leftTyp, err := analyzeExpr(x.Left, ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		rightTyp, err := analyzeExpr(x.Right, ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		switch strings.ToUpper(x.Op) {
		case "AND", "OR":
			if !isBooleanLike(leftTyp) || !isBooleanLike(rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires boolean operands", strings.ToUpper(x.Op)))
			}
			return catalog.Type{Name: "bool"}, nil
		case "=", "<>", "!=", "<", "<=", ">", ">=":
			if !isComparable(leftTyp, rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s has incompatible operand types %q and %q", x.Op, leftTyp.Name, rightTyp.Name))
			}
			return catalog.Type{Name: "bool"}, nil
		case "+", "-", "*", "/", "%":
			if !isNumericLike(leftTyp) || !isNumericLike(rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires numeric operands", x.Op))
			}
			if leftTyp.Name != "unknown" {
				return leftTyp, nil
			}
			return rightTyp, nil
		case "||":
			if !isStringLike(leftTyp) || !isStringLike(rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", "operator || requires string operands")
			}
			return catalog.Type{Name: "text"}, nil
		default:
			return catalog.Type{Name: "unknown"}, nil
		}
	case *parser.FuncCall:
		for _, a := range x.Args {
			if _, err := analyzeExpr(a, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		name := strings.ToLower(x.Name.Name)
		switch name {
		case "count":
			if x.Star {
				if len(x.Args) != 0 {
					return catalog.Type{}, analyzeError(x.Pos(), "42601", "count(*) cannot have extra arguments")
				}
				return catalog.Type{Name: "int8"}, nil
			}
			if len(x.Args) != 1 {
				return catalog.Type{}, analyzeError(x.Pos(), "42601", "count() requires exactly one argument")
			}
			return catalog.Type{Name: "int8"}, nil
		case "sum":
			if x.Star || len(x.Args) != 1 {
				return catalog.Type{}, analyzeError(x.Pos(), "42601", "sum() requires exactly one argument")
			}
			argTyp, err := analyzeExpr(x.Args[0], ctx)
			if err != nil {
				return catalog.Type{}, err
			}
			if !isNumericLike(argTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", "sum() argument must be numeric")
			}
			if argTyp.Name == "unknown" {
				return catalog.Type{Name: "int8"}, nil
			}
			return argTyp, nil
		default:
			return catalog.Type{Name: "unknown"}, nil
		}
	default:
		return catalog.Type{}, analyzeError(e.Pos(), "0A000", fmt.Sprintf("unsupported expression %T", e))
	}
}

func resolveColumnRefType(x *parser.ColumnRef, ctx *scope) (catalog.Type, error) {
	if ctx == nil || len(ctx.rels) == 0 {
		return catalog.Type{}, analyzeError(x.Pos(), "42703", fmt.Sprintf("column %q does not exist", x.Column))
	}
	if x.Table != "" || x.Schema != "" {
		matches := make([]scopeRel, 0, 1)
		for _, rel := range ctx.rels {
			if scopeRelMatches(rel, x.Table, x.Schema) {
				matches = append(matches, rel)
			}
		}
		if len(matches) == 0 {
			return catalog.Type{}, analyzeError(x.Pos(), "42P01", fmt.Sprintf("missing FROM-clause entry for table %q", x.Table))
		}
		if len(matches) > 1 {
			return catalog.Type{}, analyzeError(x.Pos(), "42702", fmt.Sprintf("table reference %q is ambiguous", x.Table))
		}
		col, ok := lookupColumn(matches[0].table, x.Column)
		if !ok {
			return catalog.Type{}, analyzeError(x.Pos(), "42703", fmt.Sprintf("column %q does not exist", x.Column))
		}
		return col.Type, nil
	}

	var found *catalog.Type
	for _, rel := range ctx.rels {
		col, ok := lookupColumn(rel.table, x.Column)
		if !ok {
			continue
		}
		if found != nil {
			return catalog.Type{}, analyzeError(x.Pos(), "42702", fmt.Sprintf("column reference %q is ambiguous", x.Column))
		}
		t := col.Type
		found = &t
	}
	if found == nil {
		return catalog.Type{}, analyzeError(x.Pos(), "42703", fmt.Sprintf("column %q does not exist", x.Column))
	}
	return *found, nil
}

func scopeRelMatches(rel scopeRel, table, schema string) bool {
	if schema != "" && !strings.EqualFold(schema, rel.table.Schema) {
		return false
	}
	if table == "" {
		return schema != ""
	}
	if strings.EqualFold(table, rel.table.Name) {
		return true
	}
	if rel.alias != "" && strings.EqualFold(table, rel.alias) {
		return true
	}
	return false
}

func lookupTable(cat catalog.Catalog, rv parser.RangeVar) (*catalog.Table, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok {
		return nil, analyzeError(rv.Pos(), "42P01", fmt.Sprintf("relation %q does not exist", rv.Name))
	}
	return tbl, nil
}

func buildSelectScope(s *parser.SelectStmt, cat catalog.Catalog) ([]scopeRel, error) {
	if len(s.FromExprs) == 0 {
		rels := make([]scopeRel, 0, len(s.From))
		for _, rv := range s.From {
			tbl, err := lookupTable(cat, rv)
			if err != nil {
				return nil, err
			}
			rels = append(rels, scopeRel{table: tbl, alias: rv.Alias})
		}
		return rels, nil
	}
	rels := make([]scopeRel, 0, len(s.From))
	for _, item := range s.FromExprs {
		tbl, err := lookupTable(cat, item.Base)
		if err != nil {
			return nil, err
		}
		rels = append(rels, scopeRel{table: tbl, alias: item.Base.Alias})
		for _, j := range item.Joins {
			rt, err := lookupTable(cat, j.Right)
			if err != nil {
				return nil, err
			}
			rels = append(rels, scopeRel{table: rt, alias: j.Right.Alias})
		}
	}
	return rels, nil
}

func lookupColumn(tbl *catalog.Table, name string) (*catalog.Column, bool) {
	for i := range tbl.Columns {
		if strings.EqualFold(tbl.Columns[i].Name, name) {
			return &tbl.Columns[i], true
		}
	}
	return nil, false
}

func resolveInsertTargetColumns(tbl *catalog.Table, cat catalog.Catalog, s *parser.InsertStmt) ([]catalog.Column, error) {
	if len(s.Columns) == 0 {
		return append([]catalog.Column(nil), tbl.Columns...), nil
	}
	out := make([]catalog.Column, 0, len(s.Columns))
	for _, name := range s.Columns {
		col, ok := cat.LookupColumn(tbl, name)
		if !ok {
			return nil, analyzeError(s.Target.Pos(), "42703", fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name))
		}
		out = append(out, *col)
	}
	return out, nil
}

func hasJoinClauses(items []parser.FromExpr) bool {
	for _, item := range items {
		if len(item.Joins) > 0 {
			return true
		}
	}
	return false
}

func matchesRangeVarRef(ref string, table *catalog.Table, alias string) bool {
	if strings.EqualFold(ref, table.Name) {
		return true
	}
	if alias != "" && strings.EqualFold(ref, alias) {
		return true
	}
	return false
}

func isUnknownType(t catalog.Type) bool {
	return t.Name == "" || strings.EqualFold(t.Name, "unknown")
}

func isBooleanTypeName(name string) bool {
	return strings.EqualFold(name, "bool") || strings.EqualFold(name, "boolean")
}

func isBooleanLike(t catalog.Type) bool {
	if isUnknownType(t) {
		return true
	}
	return isBooleanTypeName(t.Name)
}

func isIntegerLike(t catalog.Type) bool {
	if isUnknownType(t) {
		return true
	}
	switch strings.ToLower(t.Name) {
	case "int2", "int4", "int8", "integer", "smallint", "bigint":
		return true
	}
	return false
}

func isNumericTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "int2", "int4", "int8", "integer", "smallint", "bigint", "numeric", "float4", "float8", "real", "double":
		return true
	}
	return false
}

func isNumericLike(t catalog.Type) bool {
	if isUnknownType(t) {
		return true
	}
	return isNumericTypeName(t.Name)
}

func isStringTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "text", "varchar", "character varying", "char", "bpchar", "name":
		return true
	}
	return false
}

func isStringLike(t catalog.Type) bool {
	if isUnknownType(t) {
		return true
	}
	return isStringTypeName(t.Name)
}

func isComparable(left, right catalog.Type) bool {
	if isUnknownType(left) || isUnknownType(right) {
		return true
	}
	if strings.EqualFold(left.Name, right.Name) {
		return true
	}
	if isNumericTypeName(left.Name) && isNumericTypeName(right.Name) {
		return true
	}
	if isStringTypeName(left.Name) && isStringTypeName(right.Name) {
		return true
	}
	if isBooleanTypeName(left.Name) && isBooleanTypeName(right.Name) {
		return true
	}
	return false
}

func isAssignable(src, dst catalog.Type) bool {
	if isUnknownType(src) {
		return true
	}
	if strings.EqualFold(src.Name, dst.Name) {
		return true
	}
	if isNumericTypeName(src.Name) && isNumericTypeName(dst.Name) {
		return true
	}
	if isStringTypeName(src.Name) && isStringTypeName(dst.Name) {
		return true
	}
	if isBooleanTypeName(src.Name) && isBooleanTypeName(dst.Name) {
		return true
	}
	return false
}
