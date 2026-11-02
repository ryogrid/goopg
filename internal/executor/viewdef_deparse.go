package executor

import (
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// viewdef_deparse.go — M0148-0001g: pg_get_viewdef as PG's ruleutils.c
// prints it, for the subset of view queries this file understands.
//
// PG stores a view's analysed Query and deparses it on demand
// (pg_get_viewdef_worker -> make_viewdef -> get_query_def with
// PRETTYFLAG_INDENT and WRAP_COLUMN_DEFAULT = 0). goopg keeps the view body's
// source text; this deparser re-parses it and prints the PG form:
// upper-case keywords, PG's pretty-indent layout (appendContextKeyword),
// column references qualified when the query's range table holds more than
// one entry, output names (`AS aggfns`), FROM-item aliases and column-alias
// lists, VALUES constants labelled with their resolved column type, and
// aggregate DISTINCT / ORDER BY / USING / FILTER clauses.
//
// Anything outside the subset (joins, set operations, CTEs, window functions,
// constants whose type depends on coercion this file does not model, …)
// reports ok=false and pg_get_viewdef keeps echoing the stored text.

const (
	prettyIndentStd   = 8 // PRETTYINDENT_STD
	prettyIndentVar   = 4 // PRETTYINDENT_VAR
	prettyIndentLimit = 40
)

// viewDeparser is ruleutils' deparse_context for one query level.
type viewDeparser struct {
	buf         []byte
	indentLevel int
	cat         *catalog.InMemory
	pretty      bool
	rtes        []*vdRTE
	useprefix   bool
	ok          bool
}

// vdRTE is one FROM item: its printed refname and output columns.
type vdRTE struct {
	refname string
	cols    []string
	types   []string // catalog type names, relation RTEs only
}

// deparseViewDef returns view's pg_get_viewdef text (with the trailing ';'),
// or ok=false when the stored body falls outside the supported subset.
// pretty is pg_get_viewdef's second argument (psql's \d+ passes true):
// PRETTYFLAG_PAREN elides operator parentheses by precedence (isSimpleNode),
// which this file does not model yet, so a pretty request is served only for
// queries without operator expressions — where both modes print the same.
func deparseViewDef(view *catalog.Table, cat *catalog.InMemory, pretty bool) (string, bool) {
	if view == nil || view.ViewDef == "" || cat == nil {
		return "", false
	}
	stmts, err := parser.Parse(view.ViewDef)
	if err != nil || len(stmts) != 1 {
		return "", false
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		return "", false
	}
	var colnames []string
	for _, c := range view.Columns {
		colnames = append(colnames, c.Name)
	}
	d := &viewDeparser{cat: cat, ok: true, pretty: pretty}
	d.selectQuery(sel, colnames)
	if !d.ok {
		return "", false
	}
	return string(d.buf) + ";", true
}

func (d *viewDeparser) fail() { d.ok = false }

func (d *viewDeparser) appendString(s string) { d.buf = append(d.buf, s...) }

// removeTrailingSpaces is removeStringInfoSpaces.
func (d *viewDeparser) removeTrailingSpaces() {
	for len(d.buf) > 0 && d.buf[len(d.buf)-1] == ' ' {
		d.buf = d.buf[:len(d.buf)-1]
	}
}

// contextKeyword is appendContextKeyword under PRETTYFLAG_INDENT.
func (d *viewDeparser) contextKeyword(s string, indentBefore, indentAfter, indentPlus int) {
	d.indentLevel += indentBefore
	d.removeTrailingSpaces()
	d.buf = append(d.buf, '\n')
	var amount int
	if d.indentLevel < prettyIndentLimit {
		amount = max(d.indentLevel, 0) + indentPlus
	} else {
		amount = prettyIndentLimit + (d.indentLevel-prettyIndentLimit)/(prettyIndentStd/2)
		amount %= prettyIndentLimit
		amount += indentPlus
	}
	d.buf = append(d.buf, strings.Repeat(" ", amount)...)
	d.buf = append(d.buf, s...)
	d.indentLevel += indentAfter
	if d.indentLevel < 0 {
		d.indentLevel = 0
	}
}

// selectQuery is get_query_def -> get_select_query_def for a plain SELECT.
// colnames are the result descriptor's names (the view's columns) or nil.
func (d *viewDeparser) selectQuery(sel *parser.SelectStmt, colnames []string) {
	if sel.With != nil || sel.SetOp != nil || sel.SetOpOperand != nil || len(sel.Locking) > 0 ||
		len(sel.WindowClause) > 0 || len(sel.DistinctOn) > 0 || sel.WithTies {
		d.fail()
		return
	}
	for _, fe := range sel.FromExprs {
		if len(fe.Joins) > 0 {
			d.fail()
			return
		}
	}
	// get_basic_select_query: PRETTY_INDENT adds PRETTYINDENT_STD and a space.
	d.indentLevel += prettyIndentStd
	d.appendString(" ")
	if sel.ValuesRows != nil {
		// get_simple_values_rte: a bare VALUES prints as such only while the
		// result columns keep the VALUES RTE's own names (column1, …);
		// renamed view columns print the SELECT over "*VALUES*" form.
		simple := true
		for i, n := range colnames {
			if n != "column"+strconv.Itoa(i+1) {
				simple = false
			}
		}
		if simple {
			d.valuesDef(sel.ValuesRows)
			return
		}
		if len(sel.ValuesRows) == 0 || len(colnames) != len(sel.ValuesRows[0]) {
			d.fail()
			return
		}
		d.appendString("SELECT")
		var targets []parser.ResTarget
		for i := range colnames {
			targets = append(targets, parser.ResTarget{Expr: &parser.ColumnRef{Column: "column" + strconv.Itoa(i+1)}})
		}
		d.rtes = []*vdRTE{{refname: "*VALUES*"}}
		for i := range colnames {
			d.rtes[0].cols = append(d.rtes[0].cols, "column"+strconv.Itoa(i+1))
		}
		d.targetList(targets, colnames)
		d.contextKeyword(" FROM ", -prettyIndentStd, prettyIndentStd, 2)
		d.appendString("(")
		d.valuesDef(sel.ValuesRows)
		d.appendString(") " + pgQuoteIdent("*VALUES*"))
		return
	}
	d.buildRangeTable(sel.From)
	if !d.ok {
		return
	}
	d.appendString("SELECT")
	if sel.Distinct {
		d.appendString(" DISTINCT")
	}
	d.targetList(sel.Targets, colnames)
	d.fromClause(sel.From)
	if sel.Where != nil {
		d.contextKeyword(" WHERE ", -prettyIndentStd, prettyIndentStd, 1)
		d.expr(sel.Where)
	}
	if len(sel.GroupBy) > 0 {
		d.contextKeyword(" GROUP BY ", -prettyIndentStd, prettyIndentStd, 1)
		for i, g := range sel.GroupBy {
			if i > 0 {
				d.appendString(", ")
			}
			d.sortGroupExpr(g, sel.Targets)
		}
	}
	if sel.Having != nil {
		d.contextKeyword(" HAVING ", -prettyIndentStd, prettyIndentStd, 0)
		d.expr(sel.Having)
	}
	if len(sel.OrderBy) > 0 {
		d.contextKeyword(" ORDER BY ", -prettyIndentStd, prettyIndentStd, 1)
		for i, sb := range sel.OrderBy {
			if i > 0 {
				d.appendString(", ")
			}
			d.sortGroupExpr(sb.Expr, sel.Targets)
			d.sortOptions(sb)
		}
	}
	if sel.Offset != nil {
		d.contextKeyword(" OFFSET ", -prettyIndentStd, prettyIndentStd, 0)
		d.expr(sel.Offset)
	}
	if sel.Limit != nil {
		d.contextKeyword(" LIMIT ", -prettyIndentStd, prettyIndentStd, 0)
		d.expr(sel.Limit)
	}
}

// buildRangeTable resolves each FROM item's refname and column names, and
// the varprefix rule: columns are qualified when the range table does not
// hold exactly one entry (get_query_def: `list_length(query->rtable) != 1`).
func (d *viewDeparser) buildRangeTable(from []parser.RangeVar) {
	for _, rv := range from {
		r := &vdRTE{}
		switch {
		case rv.Subquery != nil:
			r.refname = rv.Alias
			if r.refname == "" {
				d.fail()
				return
			}
			if len(rv.Columns) > 0 {
				r.cols = append([]string(nil), rv.Columns...)
			} else if rv.Subquery.ValuesRows != nil {
				for i := range rv.Subquery.ValuesRows[0] {
					r.cols = append(r.cols, "column"+strconv.Itoa(i+1))
				}
			} else {
				for _, t := range rv.Subquery.Targets {
					r.cols = append(r.cols, figureColname(t))
				}
			}
		case rv.TableFunc != nil:
			tf := rv.TableFunc
			// A function returning a scalar names its one column after the
			// alias; a composite result lists its attributes instead. Only
			// generate_series is known scalar here.
			if tf.WithOrdinality || tf.RowsFuncs != nil || len(rv.Columns) > 1 || tf.Name != "generate_series" {
				d.fail()
				return
			}
			r.refname = rv.Alias
			if r.refname == "" {
				r.refname = tf.Name
			}
			col := r.refname
			if len(rv.Columns) == 1 {
				col = rv.Columns[0]
			}
			r.cols = []string{col}
		default:
			if rv.Only || rv.TableSample != nil || len(rv.Columns) > 0 {
				d.fail()
				return
			}
			tbl, found := d.cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
			// generate_relation_name qualifies a relation the search path
			// does not reach; without the session's path here, anything
			// outside public falls back.
			if !found || (tbl.Schema != "" && tbl.Schema != "public") {
				d.fail()
				return
			}
			r.refname = rv.Alias
			if r.refname == "" {
				r.refname = strings.ToLower(rv.Name)
			}
			for _, c := range tbl.Columns {
				r.cols = append(r.cols, c.Name)
				typ := c.Type.Name
				if c.Type.IsArray {
					typ = ""
				}
				r.types = append(r.types, typ)
			}
		}
		d.rtes = append(d.rtes, r)
	}
	d.useprefix = len(d.rtes) != 1
}

// figureColname is FigureColname's default output name for a target.
func figureColname(t parser.ResTarget) string {
	if t.Alias != "" {
		return t.Alias
	}
	switch e := t.Expr.(type) {
	case *parser.ColumnRef:
		return e.Column
	case *parser.FuncCall:
		return e.Name.Name
	}
	return "?column?"
}

// targetList is get_target_list with WRAP_COLUMN_DEFAULT (every target after
// the first starts a new line at PRETTYINDENT_VAR).
func (d *viewDeparser) targetList(targets []parser.ResTarget, colnames []string) {
	sep := " "
	for i, t := range targets {
		if _, isStar := t.Expr.(*parser.StarExpr); isStar {
			d.fail()
			return
		}
		d.appendString(sep)
		sep = ", "
		mark := len(d.buf)
		attname := ""
		isVar := false
		if c, ok := t.Expr.(*parser.ColumnRef); ok {
			d.columnRef(c)
			attname, isVar = c.Column, true
		} else {
			d.expr(t.Expr)
		}
		colname := figureColname(t)
		if i < len(colnames) {
			colname = colnames[i]
		}
		if !isVar || attname != colname {
			d.appendString(" AS " + pgQuoteIdent(colname))
		}
		if i > 0 {
			// colno > 1 with wrapColumn 0: always wrap. The target text was
			// appended already; move it behind the line break.
			item := append([]byte(nil), d.buf[mark:]...)
			d.buf = d.buf[:mark]
			d.contextKeyword("", -prettyIndentStd, prettyIndentStd, prettyIndentVar)
			d.buf = append(d.buf, item...)
		}
	}
}

// fromClause is get_from_clause: the first item after " FROM " on a new
// line, every later one on its own line at PRETTYINDENT_VAR.
func (d *viewDeparser) fromClause(from []parser.RangeVar) {
	for i, rv := range from {
		if i == 0 {
			d.contextKeyword(" FROM ", -prettyIndentStd, prettyIndentStd, 2)
			d.fromItem(rv, d.rtes[i])
			continue
		}
		d.appendString(", ")
		d.contextKeyword("", -prettyIndentStd, prettyIndentStd, prettyIndentVar)
		d.fromItem(rv, d.rtes[i])
	}
}

// fromItem is get_from_clause_item + get_rte_alias + get_column_alias_list.
func (d *viewDeparser) fromItem(rv parser.RangeVar, r *vdRTE) {
	switch {
	case rv.Subquery != nil:
		d.appendString("(")
		sub := &viewDeparser{cat: d.cat, ok: true, pretty: d.pretty, indentLevel: d.indentLevel}
		sub.selectQuery(rv.Subquery, nil)
		if !sub.ok {
			d.fail()
			return
		}
		d.buf = append(d.buf, sub.buf...)
		d.appendString(")")
		// A subquery always prints its alias; its column aliases print when
		// the user wrote them (set_relation_column_names).
		d.appendString(" " + pgQuoteIdent(r.refname))
		if len(rv.Columns) > 0 {
			d.columnAliasList(r.cols)
		}
	case rv.TableFunc != nil:
		d.appendString(rv.TableFunc.Name + "(")
		for i, a := range rv.TableFunc.Args {
			if i > 0 {
				d.appendString(", ")
			}
			d.funcArg(a)
		}
		d.appendString(")")
		// A function RTE always prints its alias and a full column-alias list.
		d.appendString(" " + pgQuoteIdent(r.refname))
		d.columnAliasList(r.cols)
	default:
		d.appendString(pgQuoteIdent(strings.ToLower(rv.Name)))
		if rv.Alias != "" {
			d.appendString(" " + pgQuoteIdent(rv.Alias))
		}
	}
}

func (d *viewDeparser) columnAliasList(cols []string) {
	d.appendString("(")
	for i, c := range cols {
		if i > 0 {
			d.appendString(", ")
		}
		d.appendString(pgQuoteIdent(c))
	}
	d.appendString(")")
}

// vdType is the resolved type of a VALUES column (select_common_type over
// the literals this deparser supports).
type vdType int

const (
	vdUnknown vdType = iota
	vdInt
	vdNumeric
	vdText
)

// valuesDef is get_values_def: rows separated by ", ", columns by ",", each
// constant printed as its column's resolved type labels it.
func (d *viewDeparser) valuesDef(rows [][]parser.Expr) {
	if len(rows) == 0 {
		d.fail()
		return
	}
	ncol := len(rows[0])
	types := make([]vdType, ncol)
	for c := 0; c < ncol; c++ {
		t := vdUnknown
		for _, row := range rows {
			if len(row) != ncol {
				d.fail()
				return
			}
			var lt vdType
			switch e := row[c].(type) {
			case *parser.NullConst:
				continue
			case *parser.IntegerConst:
				if e.Value < -2147483648 || e.Value > 2147483647 {
					d.fail()
					return
				}
				lt = vdInt
			case *parser.NumericConst:
				lt = vdNumeric
			case *parser.StringConst:
				lt = vdText
			default:
				d.fail()
				return
			}
			switch {
			case t == vdUnknown:
				t = lt
			case t == lt:
			case (t == vdInt && lt == vdNumeric) || (t == vdNumeric && lt == vdInt):
				t = vdNumeric
			default:
				d.fail()
				return
			}
		}
		if t == vdUnknown {
			t = vdText // an all-NULL / unknown column resolves to text
		}
		types[c] = t
	}
	d.appendString("VALUES ")
	for r, row := range rows {
		if r > 0 {
			d.appendString(", ")
		}
		d.appendString("(")
		for c, e := range row {
			if c > 0 {
				d.appendString(",")
			}
			d.typedConst(e, types[c])
		}
		d.appendString(")")
	}
}

// typedConst is get_const_expr for a constant coerced to t.
func (d *viewDeparser) typedConst(e parser.Expr, t vdType) {
	switch x := e.(type) {
	case *parser.NullConst:
		d.appendString("NULL::" + vdTypeName(t))
	case *parser.IntegerConst:
		switch t {
		case vdInt:
			if x.Value < 0 {
				d.appendString("'" + strconv.FormatInt(x.Value, 10) + "'::integer")
			} else {
				d.appendString(strconv.FormatInt(x.Value, 10))
			}
		case vdNumeric:
			// A numeric constant without '.'/'e' needs its label.
			d.appendString(strconv.FormatInt(x.Value, 10) + "::numeric")
		default:
			d.fail()
		}
	case *parser.NumericConst:
		if t != vdNumeric || strings.HasPrefix(x.Value, "-") {
			d.fail()
			return
		}
		d.appendString(x.Value)
	case *parser.StringConst:
		if t != vdText || strings.Contains(x.Value, `\`) {
			d.fail()
			return
		}
		d.appendString("'" + strings.ReplaceAll(x.Value, "'", "''") + "'::text")
	default:
		d.fail()
	}
}

func vdTypeName(t vdType) string {
	switch t {
	case vdInt:
		return "integer"
	case vdNumeric:
		return "numeric"
	}
	return "text"
}

// columnRef is get_variable: the column, qualified by its RTE's refname when
// the range table holds more than one entry.
func (d *viewDeparser) columnRef(c *parser.ColumnRef) {
	if c.Schema != "" || c.Column == "" || c.Column == "*" {
		d.fail()
		return
	}
	var owner *vdRTE
	if c.Table != "" {
		for _, r := range d.rtes {
			if r.refname == c.Table {
				owner = r
				break
			}
		}
	} else {
		for _, r := range d.rtes {
			for _, col := range r.cols {
				if col == c.Column {
					if owner != nil && owner != r {
						d.fail() // ambiguous; the analyzer would have refused it
						return
					}
					owner = r
				}
			}
		}
	}
	if owner == nil {
		d.fail()
		return
	}
	if d.useprefix {
		d.appendString(pgQuoteIdent(owner.refname) + ".")
	}
	d.appendString(pgQuoteIdent(c.Column))
}

// expr is get_rule_expr for the supported node kinds (non-pretty
// parenthesisation: every operator expression is wrapped).
func (d *viewDeparser) expr(e parser.Expr) {
	if !d.ok {
		return
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		d.columnRef(x)
	case *parser.IntegerConst:
		if x.Value < 0 || x.Value > 2147483647 {
			d.fail()
			return
		}
		d.appendString(strconv.FormatInt(x.Value, 10))
	case *parser.BinaryOp:
		if d.pretty {
			d.fail()
			return
		}
		switch x.Op {
		case parser.OpAnd, parser.OpOr:
			// BoolExpr: PG flattens an AND/OR chain into one list.
			var args []parser.Expr
			var flatten func(parser.Expr)
			flatten = func(n parser.Expr) {
				if b, ok := n.(*parser.BinaryOp); ok && b.Op == x.Op {
					flatten(b.Left)
					flatten(b.Right)
					return
				}
				args = append(args, n)
			}
			flatten(x)
			d.appendString("(")
			for i, a := range args {
				if i > 0 {
					d.appendString(" " + x.Op.String() + " ")
				}
				d.expr(a)
			}
			d.appendString(")")
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod,
			parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
			if !vdOperandOK(x.Left) || !vdOperandOK(x.Right) {
				d.fail()
				return
			}
			d.appendString("(")
			d.expr(x.Left)
			d.appendString(" " + x.Op.String() + " ")
			d.expr(x.Right)
			d.appendString(")")
		default:
			d.fail()
		}
	case *parser.FuncCall:
		d.funcCall(x)
	default:
		d.fail()
	}
}

// vdOperandOK limits operator operands to forms whose deparse needs no type
// coercion knowledge: columns, non-negative int4 literals, nested
// operators and calls over those.
func vdOperandOK(e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.ColumnRef, *parser.BinaryOp, *parser.FuncCall:
		return true
	case *parser.IntegerConst:
		return x.Value >= 0 && x.Value <= 2147483647
	}
	return false
}

// funcArg prints a function argument: get_rule_expr, with integer literals
// bare (generate_series(1, 3)); other literal kinds need the function's
// argument types to label and are not supported.
func (d *viewDeparser) funcArg(a parser.Expr) {
	d.expr(a)
}

// funcCall is get_func_expr / get_agg_expr for a plain or aggregate call.
func (d *viewDeparser) funcCall(f *parser.FuncCall) {
	if f.Over != nil || f.Name.Schema != "" {
		d.fail()
		return
	}
	if len(f.WithinGroup) > 0 {
		d.orderedSetCall(f)
		return
	}
	for _, v := range f.Variadic {
		if v {
			d.fail()
			return
		}
	}
	d.appendString(pgQuoteIdent(f.Name.Name) + "(")
	if f.Star {
		d.appendString("*")
	} else {
		if f.Distinct {
			d.appendString("DISTINCT ")
		}
		for i, a := range f.Args {
			if i > 0 {
				d.appendString(", ")
			}
			d.funcArg(a)
		}
	}
	if len(f.OrderBy) > 0 {
		d.appendString(" ORDER BY ")
		for i, sb := range f.OrderBy {
			if i > 0 {
				d.appendString(", ")
			}
			d.expr(sb.Expr)
			d.sortOptions(sb)
		}
	}
	d.appendString(")")
	if f.Filter != nil {
		d.appendString(" FILTER (WHERE ")
		d.expr(f.Filter)
		d.appendString(")")
	}
}

// sortOptions is get_rule_orderby's decoration: ASC default prints nothing
// (NULLS FIRST if asked), DESC prints " DESC" (NULLS LAST if asked), and a
// USING operator other than the type's default < / > always states its
// nulls placement.
func (d *viewDeparser) sortOptions(sb parser.SortBy) {
	switch {
	case sb.UsingOp == "" || sb.UsingOp == "<":
		if sb.Desc {
			d.appendString(" DESC")
			if sb.NullsFirst != nil && !*sb.NullsFirst {
				d.appendString(" NULLS LAST")
			}
		} else if sb.NullsFirst != nil && *sb.NullsFirst {
			d.appendString(" NULLS FIRST")
		}
	case sb.UsingOp == ">":
		d.appendString(" DESC")
		if sb.NullsFirst != nil && !*sb.NullsFirst {
			d.appendString(" NULLS LAST")
		}
	default:
		d.appendString(" USING " + sb.UsingOp)
		if sb.NullsFirst != nil && *sb.NullsFirst {
			d.appendString(" NULLS FIRST")
		} else {
			d.appendString(" NULLS LAST")
		}
	}
}

// sortGroupExpr is get_rule_sortgroupclause: a positional reference names a
// target entry, printed as its expression.
func (d *viewDeparser) sortGroupExpr(e parser.Expr, targets []parser.ResTarget) {
	if ic, ok := e.(*parser.IntegerConst); ok {
		if ic.Value < 1 || int(ic.Value) > len(targets) {
			d.fail()
			return
		}
		e = targets[ic.Value-1].Expr
		if _, isConst := e.(*parser.IntegerConst); isConst {
			d.fail() // PG prints a constant sort key specially
			return
		}
	}
	d.expr(e)
}

// orderedSetCall is get_agg_expr for an ordered-set aggregate:
// `name(direct args) WITHIN GROUP (ORDER BY …) [FILTER (WHERE …)]`. The
// direct arguments print as the analyser coerced them: percentile_disc /
// percentile_cont take double precision, so a numeric or integer literal is
// an implicit cast shown with its label (`(0.5)::double precision` —
// get_func_expr prints arguments with showimplicit); a hypothetical-set
// aggregate's direct arguments take their ORDER BY column's type, so an
// unknown literal resolves to it (`'AZZZZ'::name`) and an int4 literal
// against an int4 column stays bare.
func (d *viewDeparser) orderedSetCall(f *parser.FuncCall) {
	if f.Distinct || f.Star || len(f.OrderBy) > 0 {
		d.fail()
		return
	}
	name := strings.ToLower(f.Name.Name)
	d.appendString(pgQuoteIdent(f.Name.Name) + "(")
	switch name {
	case "percentile_disc", "percentile_cont":
		if len(f.Args) != 1 || len(f.WithinGroup) != 1 {
			d.fail()
			return
		}
		switch a := f.Args[0].(type) {
		case *parser.NumericConst:
			if strings.HasPrefix(a.Value, "-") {
				d.fail()
				return
			}
			d.appendString("(" + a.Value + ")::double precision")
		case *parser.IntegerConst:
			if a.Value < 0 {
				d.fail()
				return
			}
			d.appendString("(" + strconv.FormatInt(a.Value, 10) + ")::double precision")
		default:
			d.fail()
			return
		}
	case "rank", "dense_rank", "percent_rank", "cume_dist":
		if len(f.Args) != len(f.WithinGroup) {
			d.fail()
			return
		}
		for i, a := range f.Args {
			if i > 0 {
				d.appendString(", ")
			}
			typ := d.exprColumnType(f.WithinGroup[i].Expr)
			switch x := a.(type) {
			case *parser.IntegerConst:
				if pgFormatTypeName(typ) != "integer" || x.Value < 0 || x.Value > 2147483647 {
					d.fail()
					return
				}
				d.appendString(strconv.FormatInt(x.Value, 10))
			case *parser.StringConst:
				if typ == "" || strings.Contains(x.Value, `\`) {
					d.fail()
					return
				}
				d.appendString("'" + strings.ReplaceAll(x.Value, "'", "''") + "'::" + pgFormatTypeName(typ))
			default:
				d.fail()
				return
			}
		}
	default:
		d.fail()
		return
	}
	d.appendString(") WITHIN GROUP (ORDER BY ")
	for i, sb := range f.WithinGroup {
		if i > 0 {
			d.appendString(", ")
		}
		d.expr(sb.Expr)
		d.sortOptions(sb)
	}
	d.appendString(")")
	if f.Filter != nil {
		d.appendString(" FILTER (WHERE ")
		d.expr(f.Filter)
		d.appendString(")")
	}
}

// exprColumnType is the catalog type name of a column reference to a
// relation RTE, or "" when e is not one.
func (d *viewDeparser) exprColumnType(e parser.Expr) string {
	c, ok := e.(*parser.ColumnRef)
	if !ok || c.Schema != "" {
		return ""
	}
	for _, r := range d.rtes {
		if c.Table != "" && r.refname != c.Table {
			continue
		}
		for i, col := range r.cols {
			if col == c.Column && i < len(r.types) {
				return strings.ToLower(r.types[i])
			}
		}
	}
	return ""
}
