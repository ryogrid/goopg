package optimizer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// FieldSelectFuncName is the internal function the planner emits for a
// composite field selection it cannot resolve statically: the executor
// parses the operand's record text and returns field Args[1] (0-based),
// cast to the call's ReturnType. Args[2] carries the field name for
// EXPLAIN, which renders the call as `(arg).field` (M0146-0047b).
const FieldSelectFuncName = "__goopg_field_select"

// resolveFieldSelect resolves `(expr).field` — PG's transformIndirection
// handing one attr_name step to ParseFuncOrColumn, which builds a
// FieldSelect against the operand's composite type (parse_func.c). The
// cases, most specific first:
//
//   - a whole-row reference `(b).x` / `(b.*).x` (the operand resolves to the
//     relation's RowExpr): the column itself — PG's FieldSelect over a
//     whole-row Var, which the planner reduces to the Var;
//   - an anonymous row constructor `(ROW(1,2)).f1`: the element; an
//     anonymous record's fields are f1..fN;
//   - a row constructor cast to a composite type `(ROW(1,'z')::t).q`: the
//     element, cast to the field's type;
//   - any other composite-typed value (a column, a function result): a
//     run-time extraction (FieldSelectFuncName).
//
// The resolver's own precedence decides what a bare `(b)` names: a column
// first, then a relation (transformColumnRef), as for any other reference.
func resolveFieldSelect(x *parser.FieldSelect, ctx *resolveContext) (Expr, error) {
	field := strings.ToLower(x.Field)
	arg, err := resolveExpr(x.Arg, ctx)
	if err != nil {
		return nil, err
	}

	// Whole-row reference to a relation: the RowExpr's elements are the
	// relation's columns.
	if relName, ok := fieldSelectRelationName(x.Arg, ctx); ok {
		if row, isRow := arg.(*RowExpr); isRow {
			for _, el := range row.Elems {
				if strings.EqualFold(fieldSelectElemName(el), field) {
					return el, nil
				}
			}
			return nil, &PlanError{Pos: fieldSelectErrPos(x), Code: "42703",
				Message: fmt.Sprintf("column %q not found in data type %s", field, relName)}
		}
	}

	return fieldSelectOnArg(x, arg, ctx)
}

// resolveFieldSelectAfterAggregate is resolveFieldSelect over an aggregate's
// output: `(b).x` on a relation is the qualified column `b.x`, which the
// grouped-output resolution already maps onto its GROUP BY slot; any other
// operand resolves through the aggregate surface first.
func resolveFieldSelectAfterAggregate(x *parser.FieldSelect, agg *aggregateSurface) (Expr, error) {
	if _, ok := fieldSelectRelationName(x.Arg, agg.input); ok {
		if row, err := resolveExpr(x.Arg, agg.input); err == nil {
			if _, isRow := row.(*RowExpr); isRow {
				parts := []string{fieldSelectRelationRef(x.Arg), strings.ToLower(x.Field)}
				if sx, ok := x.Arg.(*parser.StarExpr); ok && sx.Schema != "" {
					parts = append([]string{sx.Schema}, parts...)
				}
				return resolveExprAfterAggregate(parser.NewColumnRef(x.Pos(), parts), agg)
			}
		}
	}
	arg, err := resolveExprAfterAggregate(x.Arg, agg)
	if err != nil {
		return nil, err
	}
	return fieldSelectOnArg(x, arg, agg.input)
}

// fieldSelectRelationRef is the relation name a bare `(b)` / `(b.*)` operand
// is written with.
func fieldSelectRelationRef(arg parser.Expr) string {
	switch a := arg.(type) {
	case *parser.ColumnRef:
		return a.Column
	case *parser.StarExpr:
		return a.Table
	}
	return ""
}

// fieldSelectOnArg selects the field from an already-resolved operand that is
// not a relation's whole-row reference.
func fieldSelectOnArg(x *parser.FieldSelect, arg Expr, ctx *resolveContext) (Expr, error) {
	field := strings.ToLower(x.Field)
	// Anonymous row constructor: fields f1..fN.
	if elems, ok := anonymousRowElems(arg); ok {
		if n, ok := anonymousRecordFieldOrdinal(field); ok && n <= len(elems) {
			return elems[n-1], nil
		}
		return nil, &PlanError{Pos: fieldSelectErrPos(x), Code: "42703",
			Message: fmt.Sprintf("could not identify column %q in record data type", field)}
	}

	typ := exprType(arg)
	fields, ok := compositeTypeFields(ctx.cat, typ)
	if !ok {
		display := typ.Name
		if display == "" {
			display = "unknown"
		}
		if strings.EqualFold(display, "record") {
			return nil, &PlanError{Pos: fieldSelectErrPos(x), Code: "42703",
				Message: fmt.Sprintf("could not identify column %q in record data type", field)}
		}
		return nil, &PlanError{Pos: fieldSelectErrPos(x), Code: "42809",
			Message: fmt.Sprintf("column notation .%s applied to type %s, which is not a composite type",
				field, catalog.ArgTypeDisplayAlias(strings.ToLower(display)))}
	}
	idx := -1
	for i, f := range fields {
		if strings.EqualFold(f.name, field) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, &PlanError{Pos: fieldSelectErrPos(x), Code: "42703",
			Message: fmt.Sprintf("column %q not found in data type %s", field, strings.ToLower(typ.Name))}
	}
	ft := fields[idx].typ

	// A row constructor cast to the composite type: the element, cast to
	// the field's type.
	if c, isCast := arg.(*CastExpr); isCast {
		if elems, ok := anonymousRowElems(c.Operand); ok && idx < len(elems) {
			el := elems[idx]
			return &CastExpr{pos: x.Pos(), Operand: el, TargetType: typeNameWithArray(ft),
				SourceType: exprType(el).Name}, nil
		}
	}

	return &FuncCall{pos: x.Pos(), Name: FieldSelectFuncName, ReturnType: typeNameWithArray(ft),
		Args: []Expr{arg, &IntegerConst{pos: x.Pos(), Value: int64(idx)}, &StringConst{pos: x.Pos(), Value: field}}}, nil
}

// fieldSelectRelationName reports the relation a bare `(b)` / `(b.*)`
// operand names, when it names one in scope and no column of that name
// exists (a column wins, as in transformColumnRef).
func fieldSelectRelationName(arg parser.Expr, ctx *resolveContext) (string, bool) {
	var name string
	switch a := arg.(type) {
	case *parser.ColumnRef:
		if a.Table != "" || a.Schema != "" {
			return "", false
		}
		name = a.Column
	case *parser.StarExpr:
		if a.Table == "" {
			return "", false
		}
		name = a.Table
	default:
		return "", false
	}
	for cur := ctx; cur != nil; cur = cur.parent {
		for _, b := range cur.bindings {
			alias := b.alias
			if alias == "" && b.table != nil {
				alias = b.table.Name
			}
			if !strings.EqualFold(alias, name) {
				continue
			}
			if b.table != nil && b.table.Name != "" {
				return strings.ToLower(b.table.Name), true
			}
			return strings.ToLower(name), true
		}
	}
	return "", false
}

// fieldSelectElemName is the column name a whole-row element reads. A shape
// check on one node, not a walker.
func fieldSelectElemName(e Expr) string {
	if c, ok := e.(*ColumnRef); ok {
		return c.Name
	}
	if c, ok := e.(*OuterColumnRef); ok {
		return c.Name
	}
	return ""
}

// anonymousRowElems returns the elements of an anonymous row constructor:
// the implicit `(a, b)` (RowExpr) or `ROW(a, b)` (a call named row).
// A shape check on one node, not a walker.
func anonymousRowElems(e Expr) ([]Expr, bool) {
	if r, ok := e.(*RowExpr); ok {
		return r.Elems, true
	}
	if f, ok := e.(*FuncCall); ok && strings.EqualFold(f.Name, "row") {
		return f.Args, true
	}
	return nil, false
}

// anonymousRecordFieldOrdinal parses an anonymous record's field name fN.
func anonymousRecordFieldOrdinal(field string) (int, bool) {
	if len(field) < 2 || field[0] != 'f' {
		return 0, false
	}
	n, err := strconv.Atoi(field[1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

type compositeField struct {
	name string
	typ  catalog.Type
}

// compositeTypeFields lists typ's fields when it is a composite type: a
// CREATE TYPE ... AS (...) type, or a table's row type.
func compositeTypeFields(cat catalog.Catalog, typ catalog.Type) ([]compositeField, bool) {
	if cat == nil || typ.IsArray || typ.Name == "" {
		return nil, false
	}
	name := strings.ToLower(typ.Name)
	if fs := cat.LookupCompositeTypeFields(name); len(fs) > 0 {
		out := make([]compositeField, len(fs))
		for i, f := range fs {
			out[i] = compositeField{name: strings.ToLower(f.Name), typ: compositeFieldType(f.ColType)}
		}
		return out, true
	}
	if tbl, ok := cat.LookupTable(parser.ObjectName{Name: name}); ok && tbl != nil && tbl.View == nil {
		out := make([]compositeField, 0, len(tbl.Columns))
		for _, c := range tbl.Columns {
			out = append(out, compositeField{name: strings.ToLower(c.Name), typ: c.Type})
		}
		return out, true
	}
	return nil, false
}

// compositeFieldType converts a CompositeField.ColType string (parser tokens
// joined by spaces: "integer", "numeric ( 10 , 2 )", "text [ ]") into a
// catalog type: the base name, single-spaced, plus the array flag.
func compositeFieldType(colType string) catalog.Type {
	s := strings.ToLower(strings.TrimSpace(colType))
	isArray := false
	if i := strings.IndexByte(s, '['); i >= 0 {
		isArray = true
		s = s[:i]
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	return catalog.Type{Name: strings.Join(strings.Fields(s), " "), IsArray: isArray}
}

// typeNameWithArray spells t as a cast target name.
func typeNameWithArray(t catalog.Type) string {
	if t.IsArray {
		return t.Name + "[]"
	}
	return t.Name
}

// fieldSelectErrPos is where PG's caret points for a field-selection error:
// the operand inside the parentheses (`(c).nope` marks the c), not the '('.
func fieldSelectErrPos(x *parser.FieldSelect) int {
	if x.Arg != nil {
		return x.Arg.Pos()
	}
	return x.Pos()
}
