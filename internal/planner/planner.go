package planner

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/analyzer"
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
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planSelect(s, cat)
	case *parser.InsertStmt:
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planInsert(s, cat)
	case *parser.UpdateStmt:
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planUpdate(s, cat)
	case *parser.DeleteStmt:
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
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

func toPlanError(err error) error {
	if ae, ok := err.(*analyzer.AnalyzeError); ok {
		return &PlanError{Pos: ae.Pos, Code: ae.Code, Message: ae.Message}
	}
	return err
}

// resolveContext holds the per-statement name-resolution scope.
//
// v0 only supports single-relation FROM clauses, so this is just one
// table plus its emitted Schema; multi-table joins extend this with
// a per-RangeVar list.
type resolveContext struct {
	table  *catalog.Table
	alias  string
	schema Schema // schema produced by the input scan
	// bindings keeps every FROM-clause relation in output-column order.
	bindings []rangeBinding
}

type rangeBinding struct {
	table  *catalog.Table
	alias  string
	offset int // first output-column index for this relation
}

func tableSchema(t *catalog.Table) Schema {
	out := make(Schema, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = SchemaColumn{Name: c.Name, Type: c.Type}
	}
	return out
}

func newResolveContext(bindings []rangeBinding, schema Schema) *resolveContext {
	ctx := &resolveContext{schema: schema, bindings: append([]rangeBinding(nil), bindings...)}
	if len(ctx.bindings) > 0 {
		ctx.table = ctx.bindings[0].table
		ctx.alias = ctx.bindings[0].alias
	}
	return ctx
}

func singleBindingContext(table *catalog.Table, alias string) *resolveContext {
	b := rangeBinding{table: table, alias: alias, offset: 0}
	return newResolveContext([]rangeBinding{b}, tableSchema(table))
}

func appendSchema(left, right Schema) Schema {
	out := make(Schema, 0, len(left)+len(right))
	out = append(out, left...)
	out = append(out, right...)
	return out
}

func hasJoinClauses(items []parser.FromExpr) bool {
	for _, item := range items {
		if len(item.Joins) > 0 {
			return true
		}
	}
	return false
}

func planSelect(s *parser.SelectStmt, cat catalog.Catalog) (Node, error) {
	if s.SetOp != nil {
		return nil, &PlanError{
			Pos:     s.SetOp.Pos(),
			Code:    "0A000",
			Message: "set operations are not supported in v0 planner",
		}
	}
	if s.Distinct {
		return nil, &PlanError{
			Pos:     s.Pos(),
			Code:    "0A000",
			Message: "DISTINCT is not supported in v0 planner",
		}
	}

	isSimpleSingle := len(s.From) == 1 && (len(s.FromExprs) == 0 || (len(s.FromExprs) == 1 && len(s.FromExprs[0].Joins) == 0))

	var node Node
	var ctx *resolveContext

	if len(s.From) == 0 {
		// Constant SELECT — `SELECT 1`. The target list resolves
		// against the empty schema.
		ctx = newResolveContext(nil, nil)
		node = &Values{
			pos:    s.Pos(),
			Rows:   [][]Expr{{}},
			schema: nil,
		}
	} else if isSimpleSingle {
		rv := s.From[0]
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok {
			return nil, &PlanError{
				Pos:     rv.Pos(),
				Code:    "42P01",
				Message: fmt.Sprintf("relation %q does not exist", rv.Name),
			}
		}
		ctx = singleBindingContext(tbl, rv.Alias)
		node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	} else {
		var err error
		node, ctx, err = planFromClause(s, cat)
		if err != nil {
			return nil, err
		}
	}

	if s.Where != nil {
		if isSimpleSingle {
			if idxNode, ok, err := planIndexScanFromWhere(s.Where, ctx, cat); err != nil {
				return nil, err
			} else if ok {
				node = idxNode
			} else {
				pred, err := resolveExpr(s.Where, ctx)
				if err != nil {
					return nil, err
				}
				node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
			}
		} else {
			pred, err := resolveExpr(s.Where, ctx)
			if err != nil {
				return nil, err
			}
			node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
		}
	}

	var agg *aggregateSurface
	if needsAggregateStage(s) {
		var having Expr
		var err error
		node, ctx, agg, having, err = buildAggregateStage(s, node, ctx)
		if err != nil {
			return nil, err
		}
		if having != nil {
			node = &Filter{pos: s.Having.Pos(), Child: node, Predicate: having}
		}
	} else if s.Having != nil {
		return nil, &PlanError{
			Pos:     s.Having.Pos(),
			Code:    "42803",
			Message: "column must appear in the GROUP BY clause or be used in an aggregate function",
		}
	}

	if len(s.OrderBy) > 0 {
		keys := make([]SortKey, 0, len(s.OrderBy))
		for _, sb := range s.OrderBy {
			var e Expr
			var err error
			if agg == nil {
				e, err = resolveExpr(sb.Expr, ctx)
			} else {
				e, err = resolveExprAfterAggregate(sb.Expr, agg)
			}
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

	var (
		targets []Expr
		schema  Schema
		err     error
	)
	if agg == nil {
		targets, schema, err = resolveTargets(s.Targets, ctx)
	} else {
		targets, schema, err = resolveTargetsAfterAggregate(s.Targets, agg)
	}
	if err != nil {
		return nil, err
	}
	return &Project{pos: s.Pos(), Child: node, Targets: targets, schema: schema}, nil
}

func planFromClause(s *parser.SelectStmt, cat catalog.Catalog) (Node, *resolveContext, error) {
	if len(s.FromExprs) == 0 {
		return planFromRangeVars(s.From, cat)
	}
	var root Node
	var bindings []rangeBinding
	for _, item := range s.FromExprs {
		itemNode, itemBindings, err := planFromItem(item, cat)
		if err != nil {
			return nil, nil, err
		}
		if root == nil {
			root = itemNode
			bindings = itemBindings
			continue
		}
		shift := len(root.Output())
		shifted := make([]rangeBinding, len(itemBindings))
		for i := range itemBindings {
			shifted[i] = itemBindings[i]
			shifted[i].offset += shift
		}
		root = &Join{
			pos:    item.Pos(),
			Type:   JoinTypeCross,
			Left:   root,
			Right:  itemNode,
			schema: appendSchema(root.Output(), itemNode.Output()),
		}
		bindings = append(bindings, shifted...)
	}
	if root == nil {
		return nil, nil, &PlanError{Pos: s.Pos(), Code: "42601", Message: "SELECT FROM requires at least one relation"}
	}
	return root, newResolveContext(bindings, root.Output()), nil
}

func planFromRangeVars(from []parser.RangeVar, cat catalog.Catalog) (Node, *resolveContext, error) {
	var root Node
	var bindings []rangeBinding
	for _, rv := range from {
		n, b, err := planScanRangeVar(rv, cat)
		if err != nil {
			return nil, nil, err
		}
		if root == nil {
			root = n
			bindings = append(bindings, b)
			continue
		}
		b.offset += len(root.Output())
		root = &Join{
			pos:    rv.Pos(),
			Type:   JoinTypeCross,
			Left:   root,
			Right:  n,
			schema: appendSchema(root.Output(), n.Output()),
		}
		bindings = append(bindings, b)
	}
	if root == nil {
		return nil, nil, &PlanError{Pos: 0, Code: "42601", Message: "SELECT FROM requires at least one relation"}
	}
	return root, newResolveContext(bindings, root.Output()), nil
}

func planFromItem(item parser.FromExpr, cat catalog.Catalog) (Node, []rangeBinding, error) {
	leftNode, leftBinding, err := planScanRangeVar(item.Base, cat)
	if err != nil {
		return nil, nil, err
	}
	leftCtx := newResolveContext([]rangeBinding{leftBinding}, leftNode.Output())
	for _, j := range item.Joins {
		rightNode, rightBinding, err := planScanRangeVar(j.Right, cat)
		if err != nil {
			return nil, nil, err
		}
		rightBinding.offset = len(leftCtx.schema)

		rightCtx := newResolveContext([]rangeBinding{rightBinding}, appendSchema(leftCtx.schema, rightNode.Output()))
		mergedBindings := make([]rangeBinding, 0, len(leftCtx.bindings)+1)
		mergedBindings = append(mergedBindings, leftCtx.bindings...)
		mergedBindings = append(mergedBindings, rightBinding)
		mergedSchema := appendSchema(leftCtx.schema, rightNode.Output())
		mergedCtx := newResolveContext(mergedBindings, mergedSchema)

		pred, err := planJoinPredicate(j, leftCtx, rightCtx, mergedCtx)
		if err != nil {
			return nil, nil, err
		}
		leftNode = &Join{
			pos:       j.Pos(),
			Type:      mapJoinType(j.Type),
			Left:      leftNode,
			Right:     rightNode,
			Predicate: pred,
			schema:    mergedSchema,
		}
		leftCtx = mergedCtx
	}
	return leftNode, leftCtx.bindings, nil
}

func planScanRangeVar(rv parser.RangeVar, cat catalog.Catalog) (Node, rangeBinding, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok {
		return nil, rangeBinding{}, &PlanError{
			Pos:     rv.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", rv.Name),
		}
	}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0}
	ctx := newResolveContext([]rangeBinding{b}, tableSchema(tbl))
	return &SeqScan{pos: rv.Pos(), Table: tbl, schema: ctx.schema}, b, nil
}

func planJoinPredicate(join parser.JoinExpr, leftCtx, rightCtx, mergedCtx *resolveContext) (Expr, error) {
	if join.Type == parser.JoinCross {
		return nil, nil
	}
	if join.On != nil {
		return resolveExpr(join.On, mergedCtx)
	}
	if len(join.Using) > 0 {
		return buildUsingPredicate(join.Pos(), join.Using, leftCtx, rightCtx)
	}
	if join.Natural {
		cols := naturalJoinColumns(leftCtx, rightCtx)
		return buildUsingPredicate(join.Pos(), cols, leftCtx, rightCtx)
	}
	return nil, &PlanError{Pos: join.Pos(), Code: "42601", Message: "JOIN requires ON or USING"}
}

func buildUsingPredicate(pos int, cols []string, leftCtx, rightCtx *resolveContext) (Expr, error) {
	var pred Expr
	for _, col := range cols {
		l, err := resolveColumnRef(&parser.ColumnRef{Column: col}, leftCtx)
		if err != nil {
			return nil, err
		}
		r, err := resolveColumnRef(&parser.ColumnRef{Column: col}, rightCtx)
		if err != nil {
			return nil, err
		}
		eq := &BinaryOp{pos: pos, Op: "=", Left: l, Right: r}
		if pred == nil {
			pred = eq
		} else {
			pred = &BinaryOp{pos: pos, Op: "AND", Left: pred, Right: eq}
		}
	}
	return pred, nil
}

func naturalJoinColumns(leftCtx, rightCtx *resolveContext) []string {
	left := map[string]struct{}{}
	for _, b := range leftCtx.bindings {
		for _, c := range b.table.Columns {
			left[strings.ToLower(c.Name)] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, b := range rightCtx.bindings {
		for _, c := range b.table.Columns {
			k := strings.ToLower(c.Name)
			if _, ok := left[k]; !ok {
				continue
			}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, c.Name)
		}
	}
	return out
}

func mapJoinType(t parser.JoinType) JoinType {
	switch t {
	case parser.JoinLeft:
		return JoinTypeLeft
	case parser.JoinRight:
		return JoinTypeRight
	case parser.JoinFull:
		return JoinTypeFull
	case parser.JoinCross:
		return JoinTypeCross
	default:
		return JoinTypeInner
	}
}

type aggregateBinding struct {
	index int
	typ   catalog.Type
}

type aggregateSurface struct {
	input           *resolveContext
	output          *resolveContext
	groupByExpr     map[string]int
	groupByInputCol map[int]int
	aggregateByKey  map[string]aggregateBinding
}

func needsAggregateStage(s *parser.SelectStmt) bool {
	if len(s.GroupBy) > 0 {
		return true
	}
	for _, t := range s.Targets {
		if exprHasAggregate(t.Expr) {
			return true
		}
	}
	if s.Having != nil && exprHasAggregate(s.Having) {
		return true
	}
	for _, sb := range s.OrderBy {
		if exprHasAggregate(sb.Expr) {
			return true
		}
	}
	return false
}

func buildAggregateStage(s *parser.SelectStmt, child Node, inputCtx *resolveContext) (Node, *resolveContext, *aggregateSurface, Expr, error) {
	groupExprs := make([]Expr, 0, len(s.GroupBy))
	groupByExpr := map[string]int{}
	groupByInputCol := map[int]int{}
	outputSchema := make(Schema, 0, len(s.GroupBy)+len(s.Targets))

	for _, g := range s.GroupBy {
		if exprHasAggregate(g) {
			return nil, nil, nil, nil, &PlanError{Pos: g.Pos(), Code: "42803", Message: "aggregate functions are not allowed in GROUP BY"}
		}
		r, err := resolveExpr(g, inputCtx)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		idx := len(outputSchema)
		groupByExpr[parserExprKey(g)] = idx
		if c, ok := r.(*ColumnRef); ok {
			groupByInputCol[c.Index] = idx
		}
		groupExprs = append(groupExprs, r)
		outputSchema = append(outputSchema, SchemaColumn{Name: groupExprName(r), Type: exprType(r)})
	}

	aggCalls, err := collectAggregateCalls(s)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	aggByKey := make(map[string]aggregateBinding, len(aggCalls))
	plannedAggs := make([]AggregateCall, 0, len(aggCalls))
	for _, fc := range aggCalls {
		k := aggregateCallKey(fc)
		if _, exists := aggByKey[k]; exists {
			continue
		}
		pa, err := buildAggregateCall(fc, inputCtx)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		idx := len(outputSchema)
		aggByKey[k] = aggregateBinding{index: idx, typ: pa.Type}
		plannedAggs = append(plannedAggs, pa)
		outputSchema = append(outputSchema, SchemaColumn{Name: strings.ToLower(fc.Name.Name), Type: pa.Type})
	}

	aggNode := &Aggregate{
		pos:        s.Pos(),
		Child:      child,
		GroupExprs: groupExprs,
		Aggs:       plannedAggs,
		schema:     outputSchema,
	}

	outputCtx := newResolveContext(nil, outputSchema)
	surface := &aggregateSurface{
		input:           inputCtx,
		output:          outputCtx,
		groupByExpr:     groupByExpr,
		groupByInputCol: groupByInputCol,
		aggregateByKey:  aggByKey,
	}

	var having Expr
	if s.Having != nil {
		having, err = resolveExprAfterAggregate(s.Having, surface)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return aggNode, outputCtx, surface, having, nil
}

func groupExprName(e Expr) string {
	if c, ok := e.(*ColumnRef); ok {
		return c.Name
	}
	return "?column?"
}

func resolveExprAfterAggregate(e parser.Expr, agg *aggregateSurface) (Expr, error) {
	if idx, ok := agg.groupByExpr[parserExprKey(e)]; ok {
		return &ColumnRef{pos: e.Pos(), Index: idx, Name: agg.output.schema[idx].Name, Type: agg.output.schema[idx].Type}, nil
	}
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
		resolved, err := resolveColumnRef(x, agg.input)
		if err != nil {
			return nil, err
		}
		col := resolved.(*ColumnRef)
		idx, ok := agg.groupByInputCol[col.Index]
		if !ok {
			return nil, &PlanError{
				Pos:     x.Pos(),
				Code:    "42803",
				Message: fmt.Sprintf("column %q must appear in the GROUP BY clause or be used in an aggregate function", x.Column),
			}
		}
		return &ColumnRef{pos: x.Pos(), Index: idx, Name: agg.output.schema[idx].Name, Type: agg.output.schema[idx].Type}, nil
	case *parser.BinaryOp:
		l, err := resolveExprAfterAggregate(x.Left, agg)
		if err != nil {
			return nil, err
		}
		r, err := resolveExprAfterAggregate(x.Right, agg)
		if err != nil {
			return nil, err
		}
		return &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}, nil
	case *parser.UnaryOp:
		op, err := resolveExprAfterAggregate(x.Operand, agg)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, nil
	case *parser.FuncCall:
		if isAggregateFunc(x) {
			k := aggregateCallKey(x)
			b, ok := agg.aggregateByKey[k]
			if !ok {
				return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "aggregate call could not be resolved"}
			}
			return &ColumnRef{pos: x.Pos(), Index: b.index, Name: agg.output.schema[b.index].Name, Type: b.typ}, nil
		}
		args := make([]Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := resolveExprAfterAggregate(a, agg)
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

func resolveTargetsAfterAggregate(targets []parser.ResTarget, agg *aggregateSurface) ([]Expr, Schema, error) {
	out := make([]Expr, 0, len(targets))
	schema := make(Schema, 0, len(targets))
	for _, t := range targets {
		if _, ok := t.Expr.(*parser.StarExpr); ok {
			return nil, nil, &PlanError{Pos: t.Pos(), Code: "0A000", Message: "SELECT * with GROUP BY/aggregate is not supported in v0 planner"}
		}
		e, err := resolveExprAfterAggregate(t.Expr, agg)
		if err != nil {
			return nil, nil, err
		}
		name, typ := targetMeta(e, t)
		out = append(out, e)
		schema = append(schema, SchemaColumn{Name: name, Type: typ})
	}
	return out, schema, nil
}

func collectAggregateCalls(s *parser.SelectStmt) ([]*parser.FuncCall, error) {
	seen := map[string]struct{}{}
	out := make([]*parser.FuncCall, 0)
	visit := func(e parser.Expr) error {
		return walkExpr(e, func(fc *parser.FuncCall) error {
			if !isAggregateFunc(fc) {
				return nil
			}
			if len(fc.Args) > 0 {
				for _, a := range fc.Args {
					if exprHasAggregate(a) {
						return &PlanError{Pos: a.Pos(), Code: "42803", Message: "nested aggregate calls are not supported in v0 planner"}
					}
				}
			}
			k := aggregateCallKey(fc)
			if _, ok := seen[k]; ok {
				return nil
			}
			seen[k] = struct{}{}
			out = append(out, fc)
			return nil
		})
	}
	for _, t := range s.Targets {
		if err := visit(t.Expr); err != nil {
			return nil, err
		}
	}
	if s.Having != nil {
		if err := visit(s.Having); err != nil {
			return nil, err
		}
	}
	for _, sb := range s.OrderBy {
		if err := visit(sb.Expr); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func walkExpr(e parser.Expr, fn func(*parser.FuncCall) error) error {
	switch x := e.(type) {
	case *parser.BinaryOp:
		if err := walkExpr(x.Left, fn); err != nil {
			return err
		}
		return walkExpr(x.Right, fn)
	case *parser.UnaryOp:
		return walkExpr(x.Operand, fn)
	case *parser.FuncCall:
		if err := fn(x); err != nil {
			return err
		}
		for _, a := range x.Args {
			if err := walkExpr(a, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func isAggregateFunc(fc *parser.FuncCall) bool {
	switch strings.ToLower(fc.Name.Name) {
	case "count", "sum", "avg", "min", "max":
		return true
	}
	return false
}

func exprHasAggregate(e parser.Expr) bool {
	found := false
	_ = walkExpr(e, func(fc *parser.FuncCall) error {
		if isAggregateFunc(fc) {
			found = true
		}
		return nil
	})
	return found
}

func aggregateCallKey(fc *parser.FuncCall) string {
	b := strings.Builder{}
	b.WriteString(strings.ToLower(fc.Name.String()))
	b.WriteString("|")
	if fc.Star {
		b.WriteString("*")
	}
	if fc.Distinct {
		b.WriteString("distinct|")
	}
	for _, a := range fc.Args {
		b.WriteString(parserExprKey(a))
		b.WriteString("|")
	}
	return b.String()
}

func buildAggregateCall(fc *parser.FuncCall, inputCtx *resolveContext) (AggregateCall, error) {
	name := strings.ToLower(fc.Name.Name)
	if fc.Star {
		if name != "count" || len(fc.Args) != 0 {
			return AggregateCall{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: "only count(*) is supported with * aggregate arguments"}
		}
		return AggregateCall{
			pos:      fc.Pos(),
			Name:     name,
			Star:     true,
			Distinct: fc.Distinct,
			Type:     catalog.Type{Name: "int8"},
		}, nil
	}
	if len(fc.Args) != 1 {
		return AggregateCall{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: fmt.Sprintf("aggregate %s() requires exactly one argument", name)}
	}
	argExpr, err := resolveExpr(fc.Args[0], inputCtx)
	if err != nil {
		return AggregateCall{}, err
	}
	outType := catalog.Type{Name: "unknown"}
	switch name {
	case "count":
		outType = catalog.Type{Name: "int8"}
	case "sum":
		outType = exprType(argExpr)
		if strings.EqualFold(outType.Name, "unknown") || outType.Name == "" {
			outType = catalog.Type{Name: "int8"}
		}
	case "avg":
		outType = catalog.Type{Name: "numeric"}
	case "min", "max":
		outType = exprType(argExpr)
	default:
		return AggregateCall{}, &PlanError{Pos: fc.Pos(), Code: "0A000", Message: fmt.Sprintf("aggregate function %q is not supported", name)}
	}
	return AggregateCall{
		pos:      fc.Pos(),
		Name:     name,
		Arg:      argExpr,
		Distinct: fc.Distinct,
		Type:     outType,
	}, nil
}

func parserExprKey(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return fmt.Sprintf("i:%d", x.Value)
	case *parser.StringConst:
		return "s:" + x.Value
	case *parser.NullConst:
		return "null"
	case *parser.BooleanConst:
		if x.Value {
			return "bool:true"
		}
		return "bool:false"
	case *parser.ParamRef:
		return fmt.Sprintf("p:%d", x.Number)
	case *parser.ColumnRef:
		return "c:" + strings.ToLower(x.Schema) + "." + strings.ToLower(x.Table) + "." + strings.ToLower(x.Column)
	case *parser.UnaryOp:
		return "u:" + strings.ToUpper(x.Op) + ":" + parserExprKey(x.Operand)
	case *parser.BinaryOp:
		return "b:" + strings.ToUpper(x.Op) + ":(" + parserExprKey(x.Left) + "):(" + parserExprKey(x.Right) + ")"
	case *parser.FuncCall:
		k := strings.Builder{}
		k.WriteString("f:")
		k.WriteString(strings.ToLower(x.Name.String()))
		k.WriteString("|")
		if x.Star {
			k.WriteString("*")
		}
		if x.Distinct {
			k.WriteString("distinct|")
		}
		for _, a := range x.Args {
			k.WriteString(parserExprKey(a))
			k.WriteString("|")
		}
		return k.String()
	case *parser.StarExpr:
		return "star:" + strings.ToLower(x.Schema) + "." + strings.ToLower(x.Table)
	}
	return fmt.Sprintf("expr:%T", e)
}

func planIndexScanFromWhere(where parser.Expr, ctx *resolveContext, cat catalog.Catalog) (Node, bool, error) {
	if len(ctx.bindings) != 1 {
		return nil, false, nil
	}
	tbl := ctx.bindings[0].table
	b, ok := where.(*parser.BinaryOp)
	if !ok || b.Op != "=" {
		return nil, false, nil
	}
	leftCol, lIsCol := b.Left.(*parser.ColumnRef)
	rightCol, rIsCol := b.Right.(*parser.ColumnRef)
	if lIsCol == rIsCol {
		return nil, false, nil
	}

	var colRef *parser.ColumnRef
	var keyExpr parser.Expr
	if lIsCol {
		colRef = leftCol
		keyExpr = b.Right
	} else {
		colRef = rightCol
		keyExpr = b.Left
	}

	resolvedCol, err := resolveColumnRef(colRef, ctx)
	if err != nil {
		return nil, false, nil
	}
	col, ok := resolvedCol.(*ColumnRef)
	if !ok {
		return nil, false, nil
	}

	resolvedKey, err := resolveExpr(keyExpr, ctx)
	if err != nil {
		return nil, false, nil
	}
	switch resolvedKey.(type) {
	case *IntegerConst, *ParamRef:
	default:
		return nil, false, nil
	}

	idx := findBTreeIndexForColumn(cat, tbl, col.Name)
	if idx == nil {
		return nil, false, nil
	}
	return &IndexScan{
		pos:    where.Pos(),
		Table:  tbl,
		Index:  idx,
		Key:    resolvedKey,
		schema: ctx.schema,
	}, true, nil
}

func findBTreeIndexForColumn(cat catalog.Catalog, tbl *catalog.Table, col string) *catalog.Index {
	for _, idx := range cat.IndexesOnTable(tbl) {
		if strings.ToLower(idx.Method) != "btree" {
			continue
		}
		if len(idx.Columns) != 1 {
			continue
		}
		if idx.Columns[0] == col {
			return idx
		}
	}
	return nil
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
	ctx := singleBindingContext(tbl, s.Target.Alias)
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
	ctx := singleBindingContext(tbl, s.Target.Alias)
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
			exprList, cols, err := expandStarTarget(star, ctx)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, exprList...)
			schema = append(schema, cols...)
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

func expandStarTarget(star *parser.StarExpr, ctx *resolveContext) ([]Expr, Schema, error) {
	if len(ctx.bindings) == 0 {
		return nil, nil, &PlanError{Pos: star.Pos(), Code: "42601", Message: "SELECT * with no FROM clause"}
	}
	bset := ctx.bindings
	if star.Table != "" || star.Schema != "" {
		matches := make([]rangeBinding, 0, 1)
		for _, b := range ctx.bindings {
			if bindingMatchesRelation(b, star.Table, star.Schema) {
				matches = append(matches, b)
			}
		}
		if len(matches) == 0 {
			return nil, nil, &PlanError{Pos: star.Pos(), Code: "42P01", Message: fmt.Sprintf("missing FROM-clause entry for table %q", star.Table)}
		}
		if len(matches) > 1 {
			return nil, nil, &PlanError{Pos: star.Pos(), Code: "42702", Message: fmt.Sprintf("table reference %q is ambiguous", star.Table)}
		}
		bset = matches
	}
	outExpr := make([]Expr, 0)
	outSchema := make(Schema, 0)
	for _, b := range bset {
		for i, c := range b.table.Columns {
			idx := b.offset + i
			outExpr = append(outExpr, &ColumnRef{pos: star.Pos(), Index: idx, Name: c.Name, Type: c.Type})
			outSchema = append(outSchema, SchemaColumn{Name: c.Name, Type: c.Type})
		}
	}
	return outExpr, outSchema, nil
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
	if len(ctx.bindings) == 0 {
		return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
	}

	if x.Table != "" || x.Schema != "" {
		matches := make([]rangeBinding, 0, 1)
		for _, b := range ctx.bindings {
			if bindingMatchesRelation(b, x.Table, x.Schema) {
				matches = append(matches, b)
			}
		}
		if len(matches) == 0 {
			return nil, &PlanError{Pos: x.Pos(), Code: "42P01", Message: fmt.Sprintf("missing FROM-clause entry for table %q", x.Table)}
		}
		if len(matches) > 1 {
			return nil, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("table reference %q is ambiguous", x.Table)}
		}
		b := matches[0]
		for i, c := range b.table.Columns {
			if strings.EqualFold(c.Name, x.Column) {
				idx := b.offset + i
				return &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type}, nil
			}
		}
		return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
	}

	var found *ColumnRef
	for _, b := range ctx.bindings {
		for i, c := range b.table.Columns {
			if !strings.EqualFold(c.Name, x.Column) {
				continue
			}
			idx := b.offset + i
			if found != nil {
				return nil, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("column reference %q is ambiguous", x.Column)}
			}
			found = &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type}
		}
	}
	if found != nil {
		return found, nil
	}
	return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
}

func bindingMatchesRelation(b rangeBinding, table, schema string) bool {
	if schema != "" && !strings.EqualFold(schema, b.table.Schema) {
		return false
	}
	if table == "" {
		return schema != ""
	}
	if strings.EqualFold(table, b.table.Name) {
		return true
	}
	if b.alias != "" && strings.EqualFold(table, b.alias) {
		return true
	}
	return false
}
