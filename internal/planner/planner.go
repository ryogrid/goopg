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
		*parser.CreateViewStmt, *parser.DropViewStmt,
		*parser.TruncateStmt, *parser.AlterTableStmt:
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.CreatePublicationStmt, *parser.DropPublicationStmt,
		*parser.CreateSubscriptionStmt, *parser.DropSubscriptionStmt:
		// M0008 logical-replication DDL flows through DDL too;
		// the executor's DDL operator handles them by mutating
		// the runtime's *catalog.PubSub registry. See
		// docs/design/0008-0003-publication-subscription-ddl.md.
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

	case *parser.CheckpointStmt:
		return &Checkpoint{pos: s.Pos()}, nil

	case *parser.ExplainStmt:
		inner, err := Plan(s.Inner, cat)
		if err != nil {
			return nil, err
		}
		return &Explain{pos: s.Pos(), Child: inner}, nil

	case *parser.CopyStmt:
		return planCopy(s, cat)
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
	// cat threads the catalog through so subexpression rewrites
	// (currently subquery planning) can recurse into Plan() without
	// every helper taking it as a separate argument. Populated by
	// the top-level planSelect; nil for utility contexts that
	// don't need catalog access.
	cat catalog.Catalog
	// parent is set when this resolveContext is for a subquery —
	// used by resolveColumnRef to walk up the lexical scope when
	// a column doesn't resolve locally. Implements upstream's
	// Var.varlevelsup by counting how many parent links to walk.
	// nil for the top-level SELECT.
	parent *resolveContext
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
		// Delegate the simple-single-table case to
		// planScanRangeVar so view substitution / virtual-rows
		// dispatch live in one place.
		nrv, b, err := planScanRangeVar(rv, cat)
		if err != nil {
			return nil, err
		}
		node = nrv
		schema := tableSchema(b.table)
		// View substitution may have rewritten the schema to
		// merge the view's column names with the inner plan's
		// types — preserve it.
		if b.table.View != nil {
			schema = make(Schema, len(b.table.Columns))
			innerOut := node.Output()
			for i, c := range b.table.Columns {
				ty := c.Type
				if i < len(innerOut) {
					ty = innerOut[i].Type
				}
				schema[i] = SchemaColumn{Name: c.Name, Type: ty}
			}
		}
		ctx = newResolveContext([]rangeBinding{b}, schema)
	} else {
		// Cost-based join-order reordering: when every comma-FROM
		// table has ANALYZE statistics, permute the FROM list so
		// small-cardinality relations join first. Operates on a
		// SelectStmt copy so we don't mutate the caller's parser
		// AST. Falls through to source order when stats are
		// missing or the FROM list isn't a pure comma chain.
		// See docs/design/0003-0016-join-order-reordering.md.
		stmt := s
		if reFE, reFR, rewrote := reorderCommaFromByCardinality(s, cat); rewrote {
			c := *s
			c.FromExprs = reFE
			c.From = reFR
			stmt = &c
		}
		var err error
		node, ctx, err = planFromClause(stmt, cat)
		if err != nil {
			return nil, err
		}
	}
	// Make the catalog reachable from every resolveExpr call in
	// this SELECT — subquery planning recurses into Plan(inner,
	// cat) by reading ctx.cat. Set unconditionally so the
	// FROM-less and FROM-bearing branches converge.
	//
	// planParent (set by planSelectWithParent) wires the
	// lexical-scope parent for correlated subqueries — the
	// inner SELECT's top-level ctx points up to the outer
	// SELECT so resolveColumnRef can walk up.
	if ctx != nil {
		ctx.cat = cat
		ctx.parent = planParent
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
			// Comma-FROM produces a left-deep CROSS-join chain.
			// Push WHERE-side equalities into the deepest Join
			// whose schema spans both sides so the planner can
			// pick hash join instead of running a Cartesian
			// product through Filter. See
			// internal/planner/pushdown.go.
			node = pushPredicatesIntoCrossJoins(node)
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
			// SQL allows ORDER BY to reference target-list aliases
			// (`SELECT sum(x) AS revenue ... ORDER BY revenue`)
			// and positional indices (`ORDER BY 1, 2`). Resolve
			// those substitutions BEFORE feeding the expression
			// into the column-resolver so the post-aggregate path
			// can see the same parser.Expr that built the
			// aggregate stage's target. TPC-H Q3, Q5, Q9, Q10,
			// Q21 all use ORDER BY <alias> shapes.
			expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
			var e Expr
			var err error
			if agg == nil {
				e, err = resolveExpr(expr, ctx)
			} else {
				e, err = resolveExprAfterAggregate(expr, agg)
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
		jn := &Join{
			pos:       j.Pos(),
			Type:      mapJoinType(j.Type),
			Left:      leftNode,
			Right:     rightNode,
			Predicate: pred,
			schema:    mergedSchema,
		}
		// Pick a specialised equality join algorithm when the
		// predicate decomposes into disjoint-side keys:
		//   - INNER / LEFT: hash join (M0003 rule); cost-driven
		//     override for INNER when stats are present (M0006).
		//   - RIGHT / FULL: merge join (semantics-driven; stays
		//     rules-based regardless of stats).
		// CROSS and non-equality predicates stay on nested-loop.
		leftWidth := len(leftCtx.schema)
		if lk, rk, ok := splitEqualityForHash(pred, leftWidth); ok {
			jn.LeftKey = lk
			jn.RightKey = rk
			switch jn.Type {
			case JoinTypeInner, JoinTypeLeft:
				jn.Algo = JoinAlgoHash
			case JoinTypeRight, JoinTypeFull:
				jn.Algo = JoinAlgoMerge
			}
			// INNER joins: let the cost model pick when both sides
			// have row estimates. Hash stays the M0003 fallback
			// when either side is unanalysed.
			if jn.Type == JoinTypeInner {
				lRows := EstimateRows(jn.Left)
				rRows := EstimateRows(jn.Right)
				if algo, ok := chooseInnerJoinAlgo(lRows, rRows); ok {
					jn.Algo = algo
				}
				// Build-side selection: when the cost-driven (or
				// rule-driven) algorithm landed on hash, build on
				// the smaller side. LEFT joins keep the right-as-
				// build default because the executor's outer-row
				// emission walks the left (preserved) side as the
				// probe stream.
				if jn.Algo == JoinAlgoHash && lRows > 0 && rRows > 0 && lRows < rRows {
					jn.BuildLeft = true
				}
			}
		}
		leftNode = jn
		leftCtx = mergedCtx
	}
	return leftNode, leftCtx.bindings, nil
}

func planScanRangeVar(rv parser.RangeVar, cat catalog.Catalog) (Node, rangeBinding, error) {
	if rv.Subquery != nil {
		return planSubqueryRangeVar(rv, cat)
	}
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
	// View: plan the stored inner SELECT and substitute its
	// node. The outer ctx's schema (built from the view's
	// catalog Table) takes precedence for downstream name
	// resolution — the inner SELECT's target-list names are
	// only relevant when the view didn't supply an explicit
	// alias list. Column count must match what the view
	// declared; v0 reports a planner error otherwise.
	if tbl.View != nil {
		inner, err := Plan(tbl.View, cat)
		if err != nil {
			return nil, rangeBinding{}, err
		}
		innerSchema := inner.Output()
		if len(innerSchema) != len(tbl.Columns) {
			return nil, rangeBinding{}, &PlanError{
				Pos:     rv.Pos(),
				Code:    "42P10",
				Message: fmt.Sprintf("view %q has %d columns but its body produces %d", tbl.QualifiedName(), len(tbl.Columns), len(innerSchema)),
			}
		}
		// Rebuild the outer ctx schema using the view's column
		// names but the inner plan's column types — types
		// flow from the planned inner so downstream
		// comparisons see the right type tag.
		schema := make(Schema, len(tbl.Columns))
		for i, c := range tbl.Columns {
			schema[i] = SchemaColumn{Name: c.Name, Type: innerSchema[i].Type}
		}
		ctx.schema = schema
		return inner, b, nil
	}
	if tbl.Virtual {
		return buildVirtualValues(rv.Pos(), tbl, ctx.schema), b, nil
	}
	return &SeqScan{pos: rv.Pos(), Table: tbl, schema: ctx.schema}, b, nil
}

// buildVirtualValues materialises a virtual table's current rows as
// a Values plan node. The catalog provides the rows as text; we wrap
// each cell in a planner.StringConst so downstream Filter/Project
// nodes can apply WHERE/SELECT predicates exactly as they do over a
// SeqScan.
func buildVirtualValues(pos int, tbl *catalog.Table, schema Schema) Node {
	var rows [][]Expr
	if tbl.VirtualRows != nil {
		raw := tbl.VirtualRows()
		rows = make([][]Expr, len(raw))
		for i, r := range raw {
			cells := make([]Expr, len(tbl.Columns))
			for j := range tbl.Columns {
				if j < len(r) {
					cells[j] = &StringConst{pos: pos, Value: r[j]}
				} else {
					cells[j] = &NullConst{pos: pos}
				}
			}
			rows[i] = cells
		}
	}
	return &Values{pos: pos, Rows: rows, schema: schema}
}

// planSubqueryRangeVar plans a `(SELECT …) AS alias` derived
// table. The inner SELECT is planned with the same catalog so
// nested derived tables / view refs / subqueries work; the
// resulting plan node's Output() schema becomes the binding's
// columns. Names come from the subquery's target-list aliases
// or v0's deriveTargetName fallback (mirrors the analyzer's
// synthesizeSubqueryTable). The synthetic *catalog.Table is
// never registered in the catalog — it lives only to satisfy
// the rangeBinding contract that downstream column resolution
// uses.
func planSubqueryRangeVar(rv parser.RangeVar, cat catalog.Catalog) (Node, rangeBinding, error) {
	inner, err := Plan(rv.Subquery, cat)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	innerSchema := inner.Output()
	cols := make([]catalog.Column, 0, len(rv.Subquery.Targets))
	schema := make(Schema, 0, len(rv.Subquery.Targets))
	for i, tgt := range rv.Subquery.Targets {
		name := tgt.Alias
		if name == "" {
			name = deriveSubqueryTargetName(tgt.Expr)
		}
		if name == "" {
			name = fmt.Sprintf("?column?%d", i+1)
		}
		var typ catalog.Type
		if i < len(innerSchema) {
			typ = innerSchema[i].Type
		}
		cols = append(cols, catalog.Column{Name: name, Type: typ})
		schema = append(schema, SchemaColumn{Name: name, Type: typ})
	}
	tbl := &catalog.Table{Name: rv.Alias, Columns: cols}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0}
	return inner, b, nil
}

func deriveSubqueryTargetName(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.ColumnRef:
		return x.Column
	case *parser.FuncCall:
		return strings.ToLower(x.Name.Name)
	}
	return ""
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

// splitEqualityForHash inspects a join predicate and reports
// whether it's a single equality whose two sides reference
// disjoint join inputs (one only the left, the other only the
// right). leftWidth is the column count of the left input; any
// ColumnRef with Index < leftWidth is "left-side", >= leftWidth
// is "right-side".
//
// Returns (leftKey, rightKey, true) when the equality can be
// turned into hash keys. The returned keys are oriented so
// `leftKey` references the left input only and `rightKey` the
// right; a `right.col = left.col` predicate is silently flipped
// at this layer so the executor can stay one-direction.
//
// resolveOrderBySubstitution rewrites an ORDER BY expression to
// the underlying target-list expression when the user wrote a
// bare alias or a positional index. Returns `expr` unchanged
// when neither rewrite applies. This mirrors upstream's
// `transformSortClause` lookup order: positional, then alias,
// then full expression resolution. Qualified column references
// (`t.col`) are NOT substituted — those refer to FROM-clause
// columns even if a target shares the bare name. TPC-H Q3, Q5,
// Q9, Q10, Q21 use the alias form (`ORDER BY revenue DESC`).
func resolveOrderBySubstitution(expr parser.Expr, targets []parser.ResTarget) parser.Expr {
	// Positional: `ORDER BY 1` → targets[0].Expr.
	if ic, ok := expr.(*parser.IntegerConst); ok {
		idx := int(ic.Value) - 1
		if idx >= 0 && idx < len(targets) {
			return targets[idx].Expr
		}
		return expr
	}
	// Alias: bare ColumnRef whose Column matches a target's Alias.
	if cr, ok := expr.(*parser.ColumnRef); ok && cr.Schema == "" && cr.Table == "" {
		for _, tgt := range targets {
			if tgt.Alias != "" && strings.EqualFold(tgt.Alias, cr.Column) {
				return tgt.Expr
			}
		}
	}
	return expr
}

// AND-of-equalities, OR, range predicates, and equalities whose
// operands span both sides all fall back to (nil, nil, false)
// — the planner keeps the nested-loop algorithm for those.
func splitEqualityForHash(pred Expr, leftWidth int) (Expr, Expr, bool) {
	bin, ok := pred.(*BinaryOp)
	if !ok || bin.Op != "=" {
		return nil, nil, false
	}
	lSide := exprSide(bin.Left, leftWidth)
	rSide := exprSide(bin.Right, leftWidth)
	switch {
	case lSide == sideLeft && rSide == sideRight:
		return bin.Left, bin.Right, true
	case lSide == sideRight && rSide == sideLeft:
		return bin.Right, bin.Left, true
	}
	return nil, nil, false
}

type joinSide int

const (
	sideUnknown joinSide = iota
	sideLeft
	sideRight
	sideMixed
)

// exprSide classifies which join input(s) e references. Pure
// constants resolve as sideUnknown and combine with anything;
// references to the left input are sideLeft, to the right are
// sideRight; if both appear the result is sideMixed.
func exprSide(e Expr, leftWidth int) joinSide {
	switch x := e.(type) {
	case *ColumnRef:
		if x.Index < leftWidth {
			return sideLeft
		}
		return sideRight
	case *IntegerConst, *NumericConst, *StringConst, *NullConst, *BooleanConst, *ParamRef, *TypedStringLit, *IntervalLit:
		return sideUnknown
	case *BinaryOp:
		return mergeSides(exprSide(x.Left, leftWidth), exprSide(x.Right, leftWidth))
	case *UnaryOp:
		return exprSide(x.Operand, leftWidth)
	case *FuncCall:
		side := sideUnknown
		for _, a := range x.Args {
			side = mergeSides(side, exprSide(a, leftWidth))
		}
		return side
	case *CaseExpr:
		side := sideUnknown
		if x.Operand != nil {
			side = mergeSides(side, exprSide(x.Operand, leftWidth))
		}
		for _, w := range x.Whens {
			side = mergeSides(side, exprSide(w.When, leftWidth))
			side = mergeSides(side, exprSide(w.Then, leftWidth))
		}
		if x.Else != nil {
			side = mergeSides(side, exprSide(x.Else, leftWidth))
		}
		return side
	case *ExtractExpr:
		return exprSide(x.Source, leftWidth)
	}
	return sideMixed
}

func mergeSides(a, b joinSide) joinSide {
	if a == sideUnknown {
		return b
	}
	if b == sideUnknown {
		return a
	}
	if a == b {
		return a
	}
	return sideMixed
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
		// GROUP BY accepts target-list aliases and positional
		// indices (PG extension; TPC-H Q7 leans on it). Run the
		// same substitution as ORDER BY so the resolved
		// expression — and the parserExprKey we record below —
		// matches what the target list and ORDER BY look up.
		g = resolveOrderBySubstitution(g, s.Targets)
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
	case *parser.NumericConst:
		return &NumericConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.StringConst:
		return &StringConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.TypedStringLit:
		return &TypedStringLit{pos: x.Pos(), Type: x.Type, Value: x.Value}, nil
	case *parser.IntervalLit:
		return &IntervalLit{pos: x.Pos(), Value: x.Value, Unit: x.Unit}, nil
	case *parser.SubqueryExpr:
		return planSubqueryExpr(x, agg.input)
	case *parser.InExpr:
		return planInExpr(x, agg.input)
	case *parser.ExistsExpr:
		return planExistsExpr(x, agg.input)
	case *parser.ExtractExpr:
		src, err := resolveExpr(x.Source, agg.input)
		if err != nil {
			return nil, err
		}
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: src}, nil
	case *parser.NullConst:
		return &NullConst{pos: x.Pos()}, nil
	case *parser.BooleanConst:
		return &BooleanConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.ParamRef:
		return &ParamRef{pos: x.Pos(), Number: x.Number}, nil
	case *parser.CaseExpr:
		return resolveCaseExpr(x, agg.input)
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
	case *parser.CastExpr:
		// See resolveExpr: cast is a no-op in v0.
		return resolveExprAfterAggregate(x.Operand, agg)
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
	case *parser.NumericConst:
		return "n:" + x.Value
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
	case *parser.CastExpr:
		return "cast:" + strings.ToLower(x.Type.String()) + ":(" + parserExprKey(x.Operand) + ")"
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
	switch x := e.(type) {
	case *ColumnRef:
		return x.Type
	case *NumericConst:
		return catalog.Type{Name: "numeric"}
	case *IntegerConst:
		return catalog.Type{Name: "int8"}
	case *StringConst:
		return catalog.Type{Name: "text"}
	case *BooleanConst:
		return catalog.Type{Name: "bool"}
	case *NullConst:
		return catalog.Type{Name: "unknown"}
	case *BinaryOp:
		// Arithmetic on numeric promotes to numeric. Comparison /
		// boolean ops return bool. String concat returns text. The
		// rule mirrors the executor's actual behaviour so the wire
		// layer advertises a TypeOID consistent with the formatted
		// cell text — without this, sum(numeric * numeric) lands as
		// int8 and libpq's Go driver fails ParseInt on `20667.0000`.
		switch strings.ToUpper(x.Op) {
		case "+", "-", "*", "/", "%":
			lt := exprType(x.Left)
			rt := exprType(x.Right)
			if isNumericTypeName(lt.Name) || isNumericTypeName(rt.Name) {
				return catalog.Type{Name: "numeric"}
			}
			if (lt.Name == "int8" || lt.Name == "int4") && (rt.Name == "int8" || rt.Name == "int4") {
				return catalog.Type{Name: "int8"}
			}
			return catalog.Type{Name: "unknown"}
		case "||":
			return catalog.Type{Name: "text"}
		case "AND", "OR", "=", "<>", "!=", "<", "<=", ">", ">=", "LIKE", "NOT LIKE":
			return catalog.Type{Name: "bool"}
		}
		return catalog.Type{Name: "unknown"}
	case *UnaryOp:
		if strings.ToUpper(x.Op) == "NOT" {
			return catalog.Type{Name: "bool"}
		}
		return exprType(x.Operand)
	case *CaseExpr:
		// All Whens unify to a single result type during analysis;
		// take the first branch's type as the representative.
		if len(x.Whens) > 0 {
			t := exprType(x.Whens[0].Then)
			if t.Name != "unknown" && t.Name != "" {
				return t
			}
		}
		if x.Else != nil {
			return exprType(x.Else)
		}
		return catalog.Type{Name: "unknown"}
	case *ExtractExpr:
		return catalog.Type{Name: "int8"}
	case *FuncCall:
		// Aggregates carry their own Type field (set by
		// buildAggregateCall) on the AggregateCall path, but free
		// FuncCalls reach here with no type carried. Match the
		// known-typed builtins; everything else stays unknown.
		switch strings.ToLower(x.Name) {
		case "count":
			return catalog.Type{Name: "int8"}
		case "current_timestamp", "now", "transaction_timestamp", "statement_timestamp":
			return catalog.Type{Name: "timestamp"}
		case "current_date", "to_date":
			return catalog.Type{Name: "date"}
		case "to_timestamp":
			return catalog.Type{Name: "timestamp"}
		case "substr", "substring":
			return catalog.Type{Name: "text"}
		}
		return catalog.Type{Name: "unknown"}
	}
	return catalog.Type{Name: "unknown"}
}

// isNumericTypeName reports whether name refers to a numeric type
// (NUMERIC / DECIMAL family). Used by exprType to promote arithmetic
// to numeric whenever any operand is numeric.
func isNumericTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "numeric", "decimal":
		return true
	}
	return false
}

// planSubqueryExpr plans the SELECT inside a parser
// SubqueryExpr against the supplied catalog and wraps the
// resulting Node in a planner.SubqueryExpr. The outer
// resolveContext is passed in as the inner SELECT's parent
// so column references in the subquery can resolve up the
// lexical scope (correlated subqueries).
func planSubqueryExpr(x *parser.SubqueryExpr, parent *resolveContext) (Expr, error) {
	if parent == nil || parent.cat == nil {
		return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "subqueries are not supported in this context"}
	}
	inner, err := planSelectWithParent(x.Inner, parent.cat, parent)
	if err != nil {
		return nil, err
	}
	return &SubqueryExpr{pos: x.Pos(), Plan: inner}, nil
}

// planInExpr resolves the operand and either plans the inner
// subquery (passing the outer ctx as parent for correlated
// references) or recursively resolves the value list,
// depending on which the parser produced.
func planInExpr(x *parser.InExpr, ctx *resolveContext) (Expr, error) {
	op, err := resolveExpr(x.Operand, ctx)
	if err != nil {
		return nil, err
	}
	out := &InExpr{pos: x.Pos(), Operand: op, Negated: x.Negated}
	if x.Subquery != nil {
		if ctx == nil || ctx.cat == nil {
			return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "IN (subquery) not supported in this context"}
		}
		inner, err := planSelectWithParent(x.Subquery, ctx.cat, ctx)
		if err != nil {
			return nil, err
		}
		out.Plan = inner
	} else {
		out.List = make([]Expr, len(x.List))
		for i, e := range x.List {
			r, err := resolveExpr(e, ctx)
			if err != nil {
				return nil, err
			}
			out.List[i] = r
		}
	}
	return out, nil
}

// planExistsExpr plans the inner subquery and wraps in an
// ExistsExpr. The outer ctx becomes the inner SELECT's
// parent for column-reference walk-up.
func planExistsExpr(x *parser.ExistsExpr, parent *resolveContext) (Expr, error) {
	if parent == nil || parent.cat == nil {
		return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "EXISTS not supported in this context"}
	}
	inner, err := planSelectWithParent(x.Subquery, parent.cat, parent)
	if err != nil {
		return nil, err
	}
	return &ExistsExpr{pos: x.Pos(), Negated: x.Negated, Plan: inner}, nil
}

// planSelectWithParent plans an inner SELECT with the supplied
// resolveContext as the lexical-scope parent. Used by
// SubqueryExpr / InExpr / ExistsExpr to enable correlated
// references. The resulting plan tree may contain
// OuterColumnRef nodes that the executor resolves against its
// outer-row stack at runtime.
//
// Two parent channels are wired: planParent (planner-side, so
// resolveColumnRef can walk up the resolveContext chain) and
// the analyzer's outer-scope channel (so the recursive
// Analyze pass that Plan() invokes also sees the outer
// scope). Both are restored on return.
func planSelectWithParent(stmt *parser.SelectStmt, cat catalog.Catalog, parent *resolveContext) (Node, error) {
	prevParent := planParent
	planParent = parent
	defer func() { planParent = prevParent }()

	// Build the analyzer-side OuterScope chain mirroring the
	// resolveContext chain.
	if scope := buildAnalyzerOuterScope(parent); scope != nil {
		restore := analyzer.SetOuterScope(scope)
		defer restore()
	}
	return Plan(stmt, cat)
}

// buildAnalyzerOuterScope walks a resolveContext chain and
// produces the analyzer's parallel OuterScope chain so the
// recursive Analyze call sees the same outer FROM-clause
// relations the planner sees. The inner-most scope is
// returned; analyzer.SetOuterScope is the consumer.
func buildAnalyzerOuterScope(ctx *resolveContext) *analyzer.OuterScope {
	if ctx == nil || ctx.cat == nil {
		return nil
	}
	parent := buildAnalyzerOuterScope(ctx.parent)
	rels := make([]analyzer.OuterRelation, 0, len(ctx.bindings))
	for _, b := range ctx.bindings {
		rels = append(rels, analyzer.OuterRelation{Table: b.table, Alias: b.alias})
	}
	return analyzer.NewOuterScope(ctx.cat, rels, parent)
}

// planParent is the goroutine-thread-unsafe "current outer
// resolveContext" used by planSelect to seed its top-level
// resolveContext's parent field. This is a v0 simplification
// — Plan() / planSelect / planFromClause take cat as a
// parameter, and re-threading a parent argument through every
// helper would be more invasive than the package-level
// channel. Each public Plan() call is sequential at the
// boundary, so the outer goroutine is the only writer.
//
// Future cleanup: add explicit parent params to Plan() / the
// internal helpers.
var planParent *resolveContext

// resolveCaseExpr translates a parser CaseExpr into a planner
// CaseExpr, recursively resolving each Operand / When / Then /
// Else expression in `ctx`.
func resolveCaseExpr(x *parser.CaseExpr, ctx *resolveContext) (Expr, error) {
	out := &CaseExpr{pos: x.Pos()}
	if x.Operand != nil {
		operand, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		out.Operand = operand
	}
	for _, w := range x.Whens {
		when, err := resolveExpr(w.When, ctx)
		if err != nil {
			return nil, err
		}
		then, err := resolveExpr(w.Then, ctx)
		if err != nil {
			return nil, err
		}
		out.Whens = append(out.Whens, CaseWhen{When: when, Then: then})
	}
	if x.Else != nil {
		els, err := resolveExpr(x.Else, ctx)
		if err != nil {
			return nil, err
		}
		out.Else = els
	}
	return out, nil
}

// resolveExpr walks a parser.Expr and replaces ColumnRef nodes with
// indexed planner ColumnRefs. Other node types are translated 1:1.
func resolveExpr(e parser.Expr, ctx *resolveContext) (Expr, error) {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return &IntegerConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.NumericConst:
		return &NumericConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.StringConst:
		return &StringConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.TypedStringLit:
		return &TypedStringLit{pos: x.Pos(), Type: x.Type, Value: x.Value}, nil
	case *parser.IntervalLit:
		return &IntervalLit{pos: x.Pos(), Value: x.Value, Unit: x.Unit}, nil
	case *parser.SubqueryExpr:
		return planSubqueryExpr(x, ctx)
	case *parser.InExpr:
		return planInExpr(x, ctx)
	case *parser.ExistsExpr:
		return planExistsExpr(x, ctx)
	case *parser.ExtractExpr:
		src, err := resolveExpr(x.Source, ctx)
		if err != nil {
			return nil, err
		}
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: src}, nil
	case *parser.NullConst:
		return &NullConst{pos: x.Pos()}, nil
	case *parser.BooleanConst:
		return &BooleanConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.ParamRef:
		return &ParamRef{pos: x.Pos(), Number: x.Number}, nil
	case *parser.CaseExpr:
		return resolveCaseExpr(x, ctx)
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
	case *parser.CastExpr:
		// v0 treats `expr::type` as a no-op — the executor doesn't yet
		// enforce typing, and pgbench's `oid=$1::pg_catalog.regclass`
		// shape just needs the operand expression to plan. The
		// declared target type is preserved on the parser AST for
		// future loops that wire up real type coercion.
		return resolveExpr(x.Operand, ctx)
	}
	return nil, &PlanError{Pos: e.Pos(), Code: "0A000", Message: fmt.Sprintf("unsupported expression %T", e)}
}

func resolveColumnRef(x *parser.ColumnRef, ctx *resolveContext) (Expr, error) {
	// Walk the lexical scope chain. level=0 is the local
	// resolveContext; level=N is N parents up — matches
	// upstream's Var.varlevelsup. Found-but-ambiguous in the
	// local scope is an error; ambiguity at a parent level
	// shadows the further parents (no error). Found at parent
	// level produces an OuterColumnRef.
	level := 0
	for cur := ctx; cur != nil; cur = cur.parent {
		ref, ok, err := resolveColumnRefAt(x, cur, level)
		if err != nil {
			return nil, err
		}
		if ok {
			return ref, nil
		}
		level++
	}
	return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
}

// resolveColumnRefAt tries to resolve x against a single
// resolveContext level. Returns (nil, false, nil) on miss so
// the caller walks up; (expr, true, nil) on hit; or an error
// for in-scope ambiguity / missing-FROM diagnostics.
func resolveColumnRefAt(x *parser.ColumnRef, ctx *resolveContext, level int) (Expr, bool, error) {
	if len(ctx.bindings) == 0 {
		return nil, false, nil
	}

	if x.Table != "" || x.Schema != "" {
		matches := make([]rangeBinding, 0, 1)
		for _, b := range ctx.bindings {
			if bindingMatchesRelation(b, x.Table, x.Schema) {
				matches = append(matches, b)
			}
		}
		if len(matches) == 0 {
			// Not in this level — caller walks up.
			return nil, false, nil
		}
		if len(matches) > 1 {
			return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("table reference %q is ambiguous", x.Table)}
		}
		b := matches[0]
		for i, c := range b.table.Columns {
			if strings.EqualFold(c.Name, x.Column) {
				idx := b.offset + i
				if level == 0 {
					return &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type}, true, nil
				}
				return &OuterColumnRef{pos: x.Pos(), Level: level, Index: idx, Name: c.Name, Type: c.Type}, true, nil
			}
		}
		// The qualifier matched a binding at this level but the
		// column didn't — that's a hard error (no point walking
		// up; an outer-scope `t.c` for a different `t` would be
		// caught by the qualifier mismatch instead).
		return nil, false, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
	}

	var found Expr
	for _, b := range ctx.bindings {
		for i, c := range b.table.Columns {
			if !strings.EqualFold(c.Name, x.Column) {
				continue
			}
			idx := b.offset + i
			if found != nil {
				return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("column reference %q is ambiguous", x.Column)}
			}
			if level == 0 {
				found = &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type}
			} else {
				found = &OuterColumnRef{pos: x.Pos(), Level: level, Index: idx, Name: c.Name, Type: c.Type}
			}
		}
	}
	if found != nil {
		return found, true, nil
	}
	return nil, false, nil
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
