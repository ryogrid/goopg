package planner

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// PlanError is the planner's structured error. SQLSTATE-style codes
// align with upstream's `errcodes.txt`; the analyzer/executor passes
// them through to the wire-protocol ErrorResponse encoder.
type PlanError struct {
	Pos     int
	Code    string
	Message string
}

func (e *PlanError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("planner error: %s (byte %d)", e.Message, e.Pos)
	}
	return fmt.Sprintf("%s: %s (byte %d)", e.Code, e.Message, e.Pos)
}

// Plan converts a parser statement into a plan tree. The catalog is
// consulted for name resolution; DDL statements pass through to the
// executor without decomposing here (catalog mutation happens at
// execute time).
func Plan(stmt parser.Stmt, cat catalog.Catalog) (Node, error) {
	switch s := stmt.(type) {
	case *parser.SelectStmt:
		return planSelect(s, cat)
	case *parser.InsertStmt:
		return planInsert(s, cat)
	case *parser.UpdateStmt:
		return planUpdate(s, cat)
	case *parser.DeleteStmt:
		return planDelete(s, cat)

	case *parser.CreateTableStmt, *parser.DropTableStmt,
		*parser.CreateIndexStmt, *parser.DropIndexStmt,
		*parser.TruncateStmt, *parser.AlterTableStmt:
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.BeginStmt:
		return &Transaction{pos: s.Pos(), Verb: TxBegin}, nil
	case *parser.CommitStmt:
		return &Transaction{pos: s.Pos(), Verb: TxCommit}, nil
	case *parser.RollbackStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRollback}, nil

	case *parser.VacuumStmt, *parser.AnalyzeStmt,
		*parser.ShowStmt, *parser.SetStmt, *parser.ResetStmt:
		return &Utility{pos: stmt.Pos(), Stmt: stmt}, nil
	}
	return nil, &PlanError{
		Pos:     stmt.Pos(),
		Code:    "0A000", // feature_not_supported
		Message: fmt.Sprintf("unsupported statement type %T", stmt),
	}
}

// resolveContext holds the per-statement name-resolution scope.
//
// v0 only supports single-relation FROM clauses, so this is just one
// table plus its emitted Schema; multi-table joins extend this with
// a per-RangeVar list.
type resolveContext struct {
	table  *catalog.Table
	schema Schema // schema produced by the input scan
}

func tableSchema(t *catalog.Table) Schema {
	out := make(Schema, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = SchemaColumn{Name: c.Name, Type: c.Type}
	}
	return out
}

func planSelect(s *parser.SelectStmt, cat catalog.Catalog) (Node, error) {
	if len(s.From) == 0 {
		// Constant SELECT — `SELECT 1`. The target list resolves
		// against the empty schema.
		ctx := &resolveContext{}
		targets, schema, err := resolveTargets(s.Targets, ctx)
		if err != nil {
			return nil, err
		}
		// Synthesise a single-row Values feeding a Project.
		values := &Values{
			pos:    s.Pos(),
			Rows:   [][]Expr{{}},
			schema: nil,
		}
		return &Project{pos: s.Pos(), Child: values, Targets: targets, schema: schema}, nil
	}
	if len(s.From) > 1 {
		return nil, &PlanError{
			Pos:     s.Pos(),
			Code:    "0A000",
			Message: "multi-table FROM is not supported in v0",
		}
	}
	rv := s.From[0]
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok {
		return nil, &PlanError{
			Pos:     rv.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", rv.Name),
		}
	}
	ctx := &resolveContext{table: tbl, schema: tableSchema(tbl)}
	scan := &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	var node Node = scan
	if s.Where != nil {
		pred, err := resolveExpr(s.Where, ctx)
		if err != nil {
			return nil, err
		}
		node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
	}
	if len(s.OrderBy) > 0 {
		keys := make([]SortKey, 0, len(s.OrderBy))
		for _, sb := range s.OrderBy {
			e, err := resolveExpr(sb.Expr, ctx)
			if err != nil {
				return nil, err
			}
			keys = append(keys, SortKey{Expr: e, Desc: sb.Desc})
		}
		node = &Sort{pos: s.Pos(), Child: node, Keys: keys}
	}
	if s.Limit != nil || s.Offset != nil {
		var lim, off Expr
		if s.Limit != nil {
			e, err := resolveExpr(s.Limit, ctx)
			if err != nil {
				return nil, err
			}
			lim = e
		}
		if s.Offset != nil {
			e, err := resolveExpr(s.Offset, ctx)
			if err != nil {
				return nil, err
			}
			off = e
		}
		node = &Limit{pos: s.Pos(), Child: node, Limit: lim, Offset: off}
	}
	targets, schema, err := resolveTargets(s.Targets, ctx)
	if err != nil {
		return nil, err
	}
	return &Project{pos: s.Pos(), Child: node, Targets: targets, schema: schema}, nil
}

func planInsert(s *parser.InsertStmt, cat catalog.Catalog) (Node, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{
			Pos:     s.Target.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Target.Name),
		}
	}
	// Map source-row column index -> target table column ordinal.
	var colIndex []int
	if len(s.Columns) == 0 {
		colIndex = make([]int, len(tbl.Columns))
		for i := range tbl.Columns {
			colIndex[i] = i
		}
	} else {
		colIndex = make([]int, 0, len(s.Columns))
		for _, name := range s.Columns {
			col, ok := cat.LookupColumn(tbl, name)
			if !ok {
				return nil, &PlanError{
					Pos:     s.Target.Pos(),
					Code:    "42703",
					Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name),
				}
			}
			colIndex = append(colIndex, col.Ordinal)
		}
	}
	// Validate row arity and build planner expressions.
	if len(s.Rows) == 0 {
		return nil, &PlanError{Pos: s.Pos(), Code: "42601", Message: "INSERT requires at least one row"}
	}
	rows := make([][]Expr, 0, len(s.Rows))
	for _, r := range s.Rows {
		if len(r) != len(colIndex) {
			return nil, &PlanError{
				Pos:     s.Pos(),
				Code:    "42601",
				Message: fmt.Sprintf("INSERT row has %d values, target expects %d", len(r), len(colIndex)),
			}
		}
		row := make([]Expr, 0, len(r))
		ctx := &resolveContext{} // VALUES rows have no input columns
		for _, e := range r {
			pe, err := resolveExpr(e, ctx)
			if err != nil {
				return nil, err
			}
			row = append(row, pe)
		}
		rows = append(rows, row)
	}
	values := &Values{pos: s.Pos(), Rows: rows, schema: insertValuesSchema(tbl, colIndex)}
	return &Insert{pos: s.Pos(), Table: tbl, Source: values, ColumnIndex: colIndex}, nil
}

func insertValuesSchema(tbl *catalog.Table, colIndex []int) Schema {
	out := make(Schema, len(colIndex))
	for i, ord := range colIndex {
		col := tbl.Columns[ord]
		out[i] = SchemaColumn{Name: col.Name, Type: col.Type}
	}
	return out
}

func planUpdate(s *parser.UpdateStmt, cat catalog.Catalog) (Node, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}
	ctx := &resolveContext{table: tbl, schema: tableSchema(tbl)}
	var node Node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	if s.Where != nil {
		pred, err := resolveExpr(s.Where, ctx)
		if err != nil {
			return nil, err
		}
		node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
	}
	set := make([]Expr, len(tbl.Columns))
	for _, a := range s.Set {
		col, ok := cat.LookupColumn(tbl, a.Column)
		if !ok {
			return nil, &PlanError{Pos: a.Pos(), Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", a.Column, tbl.Name)}
		}
		expr, err := resolveExpr(a.Expr, ctx)
		if err != nil {
			return nil, err
		}
		set[col.Ordinal] = expr
	}
	return &Update{pos: s.Pos(), Table: tbl, Child: node, Set: set}, nil
}

func planDelete(s *parser.DeleteStmt, cat catalog.Catalog) (Node, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}
	ctx := &resolveContext{table: tbl, schema: tableSchema(tbl)}
	var node Node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	if s.Where != nil {
		pred, err := resolveExpr(s.Where, ctx)
		if err != nil {
			return nil, err
		}
		node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
	}
	return &Delete{pos: s.Pos(), Table: tbl, Child: node}, nil
}

// resolveTargets expands a parser target list into planner Expr's
// plus the resulting Schema. `*` and qualified `t.*` expand into one
// ColumnRef per source column.
func resolveTargets(targets []parser.ResTarget, ctx *resolveContext) ([]Expr, Schema, error) {
	out := make([]Expr, 0, len(targets))
	schema := make(Schema, 0, len(targets))
	for _, t := range targets {
		if star, ok := t.Expr.(*parser.StarExpr); ok {
			if ctx.table == nil {
				return nil, nil, &PlanError{Pos: star.Pos(), Code: "42601", Message: "SELECT * with no FROM clause"}
			}
			for i, c := range ctx.table.Columns {
				out = append(out, &ColumnRef{pos: star.Pos(), Index: i, Name: c.Name, Type: c.Type})
				schema = append(schema, SchemaColumn{Name: c.Name, Type: c.Type})
			}
			continue
		}
		expr, err := resolveExpr(t.Expr, ctx)
		if err != nil {
			return nil, nil, err
		}
		name, typ := targetMeta(expr, t)
		out = append(out, expr)
		schema = append(schema, SchemaColumn{Name: name, Type: typ})
	}
	return out, schema, nil
}

// targetMeta picks the output name and type for a target. The alias
// wins; otherwise we use the underlying ColumnRef's name; otherwise a
// synthetic "?column?" matching upstream.
func targetMeta(e Expr, t parser.ResTarget) (string, catalog.Type) {
	if t.Alias != "" {
		return t.Alias, exprType(e)
	}
	if cr, ok := e.(*ColumnRef); ok {
		return cr.Name, cr.Type
	}
	return "?column?", exprType(e)
}

// exprType returns the planner-level type tag for an expression. v0
// only knows what ColumnRef carries; everything else gets the
// "unknown" tag the executor coerces at runtime.
func exprType(e Expr) catalog.Type {
	if cr, ok := e.(*ColumnRef); ok {
		return cr.Type
	}
	return catalog.Type{Name: "unknown"}
}

// resolveExpr walks a parser.Expr and replaces ColumnRef nodes with
// indexed planner ColumnRefs. Other node types are translated 1:1.
func resolveExpr(e parser.Expr, ctx *resolveContext) (Expr, error) {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return &IntegerConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.StringConst:
		return &StringConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.NullConst:
		return &NullConst{pos: x.Pos()}, nil
	case *parser.BooleanConst:
		return &BooleanConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.ParamRef:
		return &ParamRef{pos: x.Pos(), Number: x.Number}, nil
	case *parser.ColumnRef:
		return resolveColumnRef(x, ctx)
	case *parser.BinaryOp:
		l, err := resolveExpr(x.Left, ctx)
		if err != nil {
			return nil, err
		}
		r, err := resolveExpr(x.Right, ctx)
		if err != nil {
			return nil, err
		}
		return &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}, nil
	case *parser.UnaryOp:
		op, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, nil
	case *parser.FuncCall:
		args := make([]Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := resolveExpr(a, ctx)
			if err != nil {
				return nil, err
			}
			args = append(args, pa)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name.String(), Args: args, Star: x.Star}, nil
	case *parser.StarExpr:
		return nil, &PlanError{Pos: x.Pos(), Code: "42601", Message: "'*' is not allowed here"}
	}
	return nil, &PlanError{Pos: e.Pos(), Code: "0A000", Message: fmt.Sprintf("unsupported expression %T", e)}
}

func resolveColumnRef(x *parser.ColumnRef, ctx *resolveContext) (Expr, error) {
	if ctx.table == nil {
		return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
	}
	if x.Table != "" && x.Table != ctx.table.Name {
		return nil, &PlanError{Pos: x.Pos(), Code: "42P01", Message: fmt.Sprintf("missing FROM-clause entry for table %q", x.Table)}
	}
	for i, c := range ctx.table.Columns {
		if c.Name == x.Column {
			return &ColumnRef{pos: x.Pos(), Index: i, Name: c.Name, Type: c.Type}, nil
		}
	}
	return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
}
