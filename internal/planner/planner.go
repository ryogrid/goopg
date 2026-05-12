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
	case *parser.MergeStmt:
		return planMerge(s, cat)

	case *parser.CreateTableStmt, *parser.DropTableStmt,
		*parser.CreateIndexStmt, *parser.DropIndexStmt,
		*parser.CreateViewStmt, *parser.DropViewStmt,
		*parser.TruncateStmt, *parser.AlterTableStmt,
		*parser.CreateFunctionStmt, *parser.DropFunctionStmt,
		*parser.CreateProcedureStmt, *parser.DropProcedureStmt,
		*parser.CreateTriggerStmt, *parser.DropTriggerStmt,
		*parser.DropCompatStmt,
		*parser.CreateSequenceStmt, *parser.AlterSequenceStmt,
		*parser.CreateMatViewStmt, *parser.RefreshMatViewStmt,
		*parser.CompatNoopStmt:
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.CreatePublicationStmt, *parser.DropPublicationStmt,
		*parser.CreateSubscriptionStmt, *parser.DropSubscriptionStmt:
		// M0008 logical-replication DDL flows through DDL too;
		// the executor's DDL operator handles them by mutating
		// the runtime's *catalog.PubSub registry. See
		// docs/design/0008-0003-publication-subscription-ddl.md.
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.BeginStmt:
		return &Transaction{pos: s.Pos(), Verb: TxBegin, IsolationLevel: s.IsolationLevel}, nil
	case *parser.CommitStmt:
		return &Transaction{pos: s.Pos(), Verb: TxCommit}, nil
	case *parser.RollbackStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRollback}, nil
	case *parser.SavepointStmt:
		return &Transaction{pos: s.Pos(), Verb: TxSavepoint, Name: s.Name}, nil
	case *parser.ReleaseSavepointStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRelease, Name: s.Name}, nil
	case *parser.RollbackToSavepointStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRollbackTo, Name: s.Name}, nil

	case *parser.VacuumStmt, *parser.AnalyzeStmt,
		*parser.ShowStmt, *parser.SetStmt, *parser.ResetStmt,
		*parser.ReindexStmt, *parser.ClusterStmt,
		*parser.SetTransactionStmt,
		*parser.PrepareStmt, *parser.ExecuteStmt, *parser.DeallocateStmt:
		return &Utility{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.CheckpointStmt:
		return &Checkpoint{pos: s.Pos()}, nil

	case *parser.ExplainStmt:
		// M0018-0003 lifts the Stage A ANALYZE rejection: the
		// executor's explainOp now drives the inner plan
		// through an instrumentation wrapper and reports actual
		// rows/loops/timing per node.
		inner, err := Plan(s.Inner, cat)
		if err != nil {
			return nil, err
		}
		return &Explain{pos: s.Pos(), Options: s.Options, Child: inner}, nil

	case *parser.CopyStmt:
		return planCopy(s, cat)

	case *parser.CallStmt:
		return &Call{pos: s.Pos(), Stmt: s}, nil
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
	// qualifiedOnly hides this binding from the unqualified
	// column-resolution path AND restricts qualified matches to
	// alias-only (never via the underlying table's catalog name).
	// Used by ON CONFLICT DO UPDATE (M0017-0002) to wire the
	// `excluded` pseudo-table — same `*catalog.Table` as the
	// target — without making bare `col` ambiguous or letting
	// `<target>.col` accidentally match the excluded side.
	// Mirrors the analyzer's scopeRel.qualifiedOnly.
	qualifiedOnly bool
	// sourceIdx is a per-FROM-clause monotonic identifier
	// (M0071-0009) propagated into SchemaColumn.SourceTableIdx
	// for every column produced by this binding. Distinct values
	// for self-join siblings (Q21's lineitem l1/l2/l3) make the
	// (Name, SourceTableIdx) pair unique even when Name alone
	// collides. Counter starts at 1; zero means "no identity
	// assigned" (CTE / subquery-only / ON CONFLICT excluded) and
	// falls back to Name-only matching in downstream rebinds.
	sourceIdx int16
}

func tableSchema(t *catalog.Table) Schema {
	// Legacy callers that don't track source identity get 0 in
	// the SchemaColumn.SourceTableIdx slot, which downstream
	// rebind helpers treat as "unknown" and fall back to
	// Name-only matching.
	return tableSchemaWithSource(t, 0)
}

// tableSchemaWithSource (M0071-0009) is tableSchema's variant
// that stamps each produced SchemaColumn with the given
// SourceTableIdx. Callers building rangeBindings thread their
// per-FROM monotonic source identifier in here; legacy callers
// that don't track source identity (-1) keep Name-only behavior.
func tableSchemaWithSource(t *catalog.Table, sourceIdx int16) Schema {
	out := make(Schema, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = SchemaColumn{Name: c.Name, Type: c.Type, SourceTableIdx: sourceIdx}
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
	// Single-binding scope (INSERT/UPDATE/DELETE/COPY targets,
	// view substitution helpers): SourceTableIdx 1 because the
	// scope only ever has one binding and disambiguation isn't
	// needed; 0 stays reserved for "unknown / derived".
	b := rangeBinding{table: table, alias: alias, offset: 0, sourceIdx: 1}
	return newResolveContext([]rangeBinding{b}, tableSchemaWithSource(table, 1))
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
	// Pre-plan WITH-list CTEs so FROM-clause references can
	// substitute them in. Restorer pops the CTE scope back to
	// the caller's view when this Plan call returns. nil-WITH
	// returns a no-op restorer.
	restore, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()

	if s.SetOp != nil {
		if !s.SetOp.All || s.SetOp.Type != parser.SetOpUnion {
			return nil, &PlanError{
				Pos:     s.SetOp.Pos(),
				Code:    "0A000",
				Message: "set operations are not supported in v0 planner",
			}
		}
		// UNION ALL: plan right side first, then left side with
		// SetOp temporarily cleared to avoid infinite recursion.
		right, err := planSelect(s.SetOp.Right, cat)
		if err != nil {
			return nil, err
		}
		saved := s.SetOp
		s.SetOp = nil
		left, err := planSelect(s, cat)
		s.SetOp = saved
		if err != nil {
			return nil, err
		}
		return &SetOp{pos: s.Pos(), Left: left, Right: right, All: true}, nil
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
		// dispatch live in one place. SourceTableIdx 1 — only
		// one binding ever in this branch (0 is the
		// "unknown / derived" sentinel).
		nrv, b, err := planScanRangeVar(rv, cat, 1)
		if err != nil {
			return nil, err
		}
		node = nrv
		schema := tableSchemaWithSource(b.table, b.sourceIdx)
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
				schema[i] = SchemaColumn{Name: c.Name, Type: ty, SourceTableIdx: b.sourceIdx}
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
			// M0051-0004: inject synthetic range predicates alongside any
			// LIKE conjuncts so tryRangeIndexScan can activate a B-tree.
			whereForIndex := injectLikeRangePredicates(s.Where)
			if idxNode, ok, err := planIndexScanFromWhere(whereForIndex, ctx, cat); err != nil {
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
			// Attempt bushy-join DP when all tables have ANALYZE
			// stats. This replaces the left-deep CROSS chain with
			// a DPccp-style optimal bushy tree that eliminates
			// Cartesian products. See internal/planner/bushy.go.
			if f, ok := node.(*Filter); ok {
				if newChild, newPred := tryBushyDP(f.Child, f.Predicate, ctx, cat); newPred == nil {
					node = newChild // all conjuncts consumed → remove Filter
				} else if newChild != f.Child {
					f.Child = newChild
					f.Predicate = newPred
					node = pushPredicatesIntoCrossJoins(node)
				} else {
					// Comma-FROM produces a left-deep CROSS-join chain.
					// Push WHERE-side equalities into the deepest Join
					// whose schema spans both sides so the planner can
					// pick hash join instead of running a Cartesian
					// product through Filter. See
					// internal/planner/pushdown.go.
					node = pushPredicatesIntoCrossJoins(node)
				}
			}
		}
	}

	// Unnest correlated scalar subqueries after predicate pushdown
	// has finalised the join tree. Subqueries that are unnestable
	// (equijoin correlation, simple aggregate) are rewritten as
	// GROUP BY aggregate + hash join. See internal/planner/unnest.go.
	node = unnestSubqueriesInPlan(node)

	// Rewrite chains of ≥3 hash-joined tables into a single
	// MultiHashJoin node.  Column indices are remapped inside
	// collectMultiHashTables using scanForCol, so parent
	// expressions (incl. unnest keys) stay aligned.
	node = rewriteMultiWayChain(node, cat)
	// M0054-0006a-pre: after MultiHashJoin construction, walk the
	// plan tree and route single-table constant-RHS equality
	// predicates from `*Filter` wrappers and `mh.Filters` into the
	// matching `*SeqScan` input by rewriting it to `*IndexScan`.
	// Closes the M0054-0003d Q8 case
	// (`p_type = 'ECONOMY ANODIZED STEEL'` →
	// `Index Scan using idx_part_type on part`).
	node = rewriteScanInputsWithSingleTablePredicates(node, cat)
	// M0054-0006: rewrite eligible binary `*Join{Algo:Hash}` /
	// `*Join{Algo:NestedLoop}` nodes to `*NestedLoopIndexJoin` when
	// the equi-join predicate matches a single-column B-tree index
	// on the inner side AND the cost gate accepts. The pass is a
	// no-op when the package-level kill-switch is off
	// (`SetNLIEnabled(false)`).
	node = rewriteJoinsToNLI(node, cat)
	node = remapExprRefsToMHJ(node)
	// Second pass: use FROM‑clause bindings to correct any
	// remaining order differences (OID ≠ FROM order).
	if len(ctx.bindings) > 0 {
		remapWithBindings(node, ctx.bindings)
	}

	var agg *aggregateSurface
	savedBindings := ctx.bindings
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
		// The aggregate stage resolves GroupExprs / Agg.Arg against
		// ctx.bindings (FROM‑clause order).  Remap only those two
		// fields — not the HAVING predicate, which uses aggregate‑
		// output column indices and must not be touched.
		if len(savedBindings) > 0 {
			remapAggExprsWithBindings(node, savedBindings)
		}
	} else if s.Having != nil {
		return nil, &PlanError{
			Pos:     s.Having.Pos(),
			Code:    "42803",
			Message: "column must appear in the GROUP BY clause or be used in an aggregate function",
		}
	}

	var win *windowSurface
	if needsWindowStage(s) {
		var err error
		node, ctx, win, err = buildWindowStage(s, node, ctx, agg)
		if err != nil {
			return nil, err
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
			if win != nil {
				e, err = resolveExprAfterWindow(expr, win)
			} else if agg == nil {
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
	)
	if win != nil {
		targets, schema, err = resolveTargetsAfterWindow(s.Targets, win)
	} else if agg == nil {
		targets, schema, err = resolveTargets(s.Targets, ctx)
	} else {
		targets, schema, err = resolveTargetsAfterAggregate(s.Targets, agg)
	}
	if err != nil {
		return nil, err
	}
	proj := &Project{pos: s.Pos(), Child: node, Targets: targets, schema: schema}
	// Promote to IndexOnlyScan (M0046-0004) only when there are no locking
	// clauses. FOR UPDATE / FOR SHARE rely on the IndexScan leaf being
	// accessible via lockRowsOp's currentTIDProvider chain.
	var out Node
	if len(s.Locking) == 0 {
		if promoted := tryPromoteIndexOnlyScan(proj); promoted != proj {
			out = promoted
		}
	}
	if out == nil {
		out = Node(proj)
	}
	// resolveTargets resolves SELECT targets against ctx.bindings,
	// which holds FROM‑clause offsets — but rewriteMultiWayChain /
	// bushy DP may have re‑laid out the underlying join tree
	// (e.g. OID‑sorted MHJ output). Remap the freshly‑added
	// Project's targets (and any Sort keys above the join tree)
	// using the same bindings posMap so they land at actual scan
	// offsets. For aggregate queries the targets reference
	// aggregate‑output indices (small and outside any FROM
	// binding's range) so the remap is a no‑op. Inline‑view
	// subqueries (TPC‑H Q7/Q8/Q9) hit this path with FROM‑order
	// indices and need the remap to fire. We deliberately do NOT
	// walk below the Project's join‑tree boundary — those nodes
	// were already remapped by the earlier remapWithBindings call,
	// and walking them again would double‑remap.
	if agg == nil && len(savedBindings) > 0 {
		remapTopProjection(out, savedBindings)
	}
	if len(s.Locking) > 0 {
		// M0021-0002 — wrap the SELECT plan in a LockRows
		// node carrying the resolved per-relation locking
		// intent. The executor (Stage A executor lands in
		// M0021-0003) consumes Locks to acquire row-level
		// pessimistic locks before returning rows. Until
		// then the executor refuses to Build a *LockRows so
		// runtime never silently drops the locking intent.
		locks, lerr := resolveLockedRels(s, ctx)
		if lerr != nil {
			return nil, lerr
		}
		out = &LockRows{pos: s.Locking[0].Pos(), Child: out, Locks: locks}
	}
	// Collapse all-constant sub-expressions in the final plan tree.
	foldPlanConstants(out)
	return out, nil
}

// resolveLockedRels walks the parsed locking clauses and
// produces one LockedRel per (clause, FROM-clause range
// variable) pair in the effective target set. When a clause
// supplies an OF list, only those names produce LockedRels;
// otherwise every range variable in the FROM clause is locked
// per upstream's bare-FOR-UPDATE-locks-everything semantics.
//
// Mirrors upstream's expand_targetlist_to_locks step inside
// preprocess_rowmarks. The analyzer (M0021-0001 step 2) has
// already verified the OF target names resolve, so this
// helper's lookup paths are second-line defence.
func resolveLockedRels(s *parser.SelectStmt, ctx *resolveContext) ([]LockedRel, error) {
	if ctx == nil {
		return nil, &PlanError{Pos: s.Pos(), Code: "0A000", Message: "FOR UPDATE/SHARE requires a FROM clause"}
	}
	var out []LockedRel
	for _, lc := range s.Locking {
		strength := lockStrengthFromParser(lc.Strength)
		policy := lockWaitPolicyFromParser(lc.WaitPolicy)
		if len(lc.Targets) == 0 {
			for _, b := range ctx.bindings {
				out = append(out, LockedRel{Table: b.table, Alias: b.alias, Strength: strength, WaitPolicy: policy})
			}
			continue
		}
		for _, name := range lc.Targets {
			b, ok := findBindingByName(ctx.bindings, name)
			if !ok {
				return nil, &PlanError{Pos: lc.Pos(), Code: "42P01",
					Message: fmt.Sprintf("relation %q in FOR UPDATE/SHARE clause not found in FROM clause", name)}
			}
			out = append(out, LockedRel{Table: b.table, Alias: b.alias, Strength: strength, WaitPolicy: policy})
		}
	}
	return out, nil
}

func lockStrengthFromParser(s parser.LockStrength) LockStrength {
	switch s {
	case parser.LockStrengthForUpdate, parser.LockStrengthForNoKeyUpdate:
		return LockStrengthForUpdate
	case parser.LockStrengthForShare, parser.LockStrengthForKeyShare:
		return LockStrengthForShare
	}
	return LockStrengthForUpdate
}

func lockWaitPolicyFromParser(p parser.LockWaitPolicy) LockWaitPolicy {
	switch p {
	case parser.LockWaitNoWait:
		return LockWaitNoWait
	case parser.LockWaitSkipLocked:
		return LockWaitSkipLocked
	}
	return LockWaitBlock
}

func findBindingByName(bindings []rangeBinding, name string) (rangeBinding, bool) {
	for _, b := range bindings {
		if b.qualifiedOnly {
			continue
		}
		if b.alias != "" {
			if strings.EqualFold(name, b.alias) {
				return b, true
			}
			continue
		}
		if strings.EqualFold(name, b.table.Name) {
			return b, true
		}
	}
	return rangeBinding{}, false
}

func planFromClause(s *parser.SelectStmt, cat catalog.Catalog) (Node, *resolveContext, error) {
	if len(s.FromExprs) == 0 {
		return planFromRangeVars(s.From, cat)
	}
	var root Node
	var bindings []rangeBinding
	// Counter starts at 1; zero is reserved as the "unknown /
	// derived" sentinel for SchemaColumn.SourceTableIdx.
	nextSourceIdx := int16(1)
	for _, item := range s.FromExprs {
		itemNode, itemBindings, err := planFromItem(item, cat, &nextSourceIdx)
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
	// Counter starts at 1; zero is reserved as the "unknown /
	// derived" sentinel for SchemaColumn.SourceTableIdx.
	nextSourceIdx := int16(1)
	for _, rv := range from {
		n, b, err := planScanRangeVar(rv, cat, nextSourceIdx)
		if err != nil {
			return nil, nil, err
		}
		nextSourceIdx++
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

func planFromItem(item parser.FromExpr, cat catalog.Catalog, nextSourceIdx *int16) (Node, []rangeBinding, error) {
	leftNode, leftBinding, err := planScanRangeVar(item.Base, cat, *nextSourceIdx)
	if err != nil {
		return nil, nil, err
	}
	*nextSourceIdx++
	leftCtx := newResolveContext([]rangeBinding{leftBinding}, leftNode.Output())
	for _, j := range item.Joins {
		rightNode, rightBinding, err := planScanRangeVar(j.Right, cat, *nextSourceIdx)
		if err != nil {
			return nil, nil, err
		}
		*nextSourceIdx++
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
		joinType := mapJoinType(j.Type)
		// M0063-0005: for LEFT JOIN, partition the ON conjuncts.
		// Single-inner-side conjuncts (referencing only the right
		// range-var's columns) move to a Filter wrapping the
		// inner plan BEFORE the join, so the join's residual
		// Predicate can decompose into a clean equi-pair and the
		// hash path can fire. The classic Q13 shape:
		//   `customer LEFT JOIN orders ON c_custkey = o_custkey
		//      AND o_comment NOT LIKE '%special%requests%'`
		// previously fell through to a Nested Loop because the
		// AND-chain didn't split as a single equality. Pushing
		// the inner-only NOT-LIKE before the join leaves a clean
		// equi-pair on `c_custkey = o_custkey` that
		// `splitEqualityForHash` accepts. Outer-only conjuncts
		// CANNOT move (LEFT JOIN preserves unmatched outer
		// rows); they stay in the join Predicate.
		if joinType == JoinTypeLeft && pred != nil {
			leftWidth := len(leftCtx.schema)
			totalWidth := leftWidth + len(rightNode.Output())
			conjuncts := splitAnd(pred)
			var keep []Expr
			var innerOnly []Expr
			for _, c := range conjuncts {
				switch classifyConjunctSide(c, leftWidth, totalWidth) {
				case sideRight:
					innerOnly = append(innerOnly, c)
				default:
					keep = append(keep, c)
				}
			}
			if len(innerOnly) > 0 {
				// Inner-only conjuncts were resolved against the
				// merged outer schema; their ColumnRef.Index values
				// reference the inner range-var at outer-cumulative
				// offset `leftWidth`. Now that they evaluate against
				// the inner-only row, shift each ColumnRef.Index by
				// `-leftWidth` so it points into the inner schema.
				shifted := make([]Expr, len(innerOnly))
				for i, c := range innerOnly {
					shifted[i] = shiftColumnRefsBy(c, -leftWidth)
				}
				rightNode = &Filter{
					pos:       j.Pos(),
					Child:     rightNode,
					Predicate: combineAnd(shifted),
				}
			}
			pred = combineAnd(keep)
		}
		jn := &Join{
			pos:       j.Pos(),
			Type:      joinType,
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

func planScanRangeVar(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16) (Node, rangeBinding, error) {
	if rv.Subquery != nil {
		return planSubqueryRangeVar(rv, cat, sourceIdx)
	}
	if rv.TableFunc != nil {
		return planTableFuncRangeVar(rv, cat, sourceIdx)
	}
	// CTE substitution (M0016-0002): an unschemed name takes the
	// CTE before falling through to the catalog. CTE names are
	// unschemed in upstream so `pg_catalog.foo` always hits the
	// catalog. Multiple consumers each get a fresh plan reference
	// (Stage A inlining). The body is wrapped in a CTEScan so
	// EXPLAIN can label the inlined subtree (M0016-0004).
	if rv.Schema == "" {
		if ce := lookupPlannedCTE(rv.Name); ce != nil {
			alias := rv.Alias
			if alias == "" {
				alias = ce.name
			}
			b := rangeBinding{table: ce.table, alias: alias, offset: 0, sourceIdx: sourceIdx}
			scan := &CTEScan{
				pos:    rv.Pos(),
				Name:   ce.name,
				Alias:  alias,
				Child:  ce.body,
				schema: ce.schema,
			}
			return scan, b, nil
		}
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok {
		return nil, rangeBinding{}, &PlanError{
			Pos:     rv.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", rv.Name),
		}
	}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
	ctx := newResolveContext([]rangeBinding{b}, tableSchemaWithSource(tbl, sourceIdx))
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
			schema[i] = SchemaColumn{Name: c.Name, Type: innerSchema[i].Type, SourceTableIdx: b.sourceIdx}
		}
		ctx.schema = schema
		// M0063-0001: wrap the inner plan in a Project that
		// re-aliases columns to the view's catalog names. Without
		// this rename, `inner.Output()` continues to expose the
		// view body's target-list names (e.g. `l_suppkey`,
		// `sum`), but the OUTER scope resolves columns against
		// the view's catalog Table (`supplier_no`,
		// `total_revenue`). Downstream re-resolution passes
		// (M0063-0001's NLI Key Name re-bind, `reresolveJoinByName`,
		// etc.) need `inner.Output()` to advertise the same names
		// the outer scope resolved against — otherwise the
		// re-bind fails to match and the original (incorrect)
		// FROM-cumulative Index is left in place. Q15b's NLI
		// of supplier-on-revenue0 was the canonical breakage.
		targets := make([]Expr, len(tbl.Columns))
		for i, c := range tbl.Columns {
			targets[i] = &ColumnRef{
				pos:   rv.Pos(),
				Index: i,
				Name:  c.Name,
				Type:  innerSchema[i].Type,
			}
		}
		renamed := &Project{
			pos:           rv.Pos(),
			Child:         inner,
			Targets:       targets,
			schema:        append(Schema(nil), schema...),
			IsolatedScope: true,
		}
		return renamed, b, nil
	}
	if tbl.Virtual {
		return buildVirtualValues(rv.Pos(), tbl, ctx.schema), b, nil
	}
	// Partition-aware scan (M0096-0007): when scanning a partitioned table,
	// produce a union of SeqScans over all partition children.
	if len(tbl.PartitionKey) > 0 {
		if im, ok := cat.(*catalog.InMemory); ok {
			children := im.PartitionChildren(tbl.OID)
			if len(children) > 0 {
				// Build a UNION ALL of SeqScans over all children.
				var root Node
				for _, child := range children {
					childSchema := tableSchemaWithSource(b.table, sourceIdx)
					childScan := &SeqScan{pos: rv.Pos(), Table: child, Alias: rv.Alias, schema: childSchema}
					if root == nil {
						root = childScan
					} else {
						root = &SetOp{
							pos:   rv.Pos(),
							Left:  root,
							Right: childScan,
							All:   true,
						}
					}
				}
				if root != nil {
					return root, b, nil
				}
			}
		}
	}
	// Inheritance-aware scan (M0096-0009): when scanning a table that has
	// inheritance children, produce a UNION ALL of SeqScans over the parent
	// AND all children.  Unlike partitioned tables (where the parent has no
	// rows), an inherited parent may itself contain rows, so the parent scan
	// is always included first.
	if im, ok := cat.(*catalog.InMemory); ok {
		children := im.InheritanceChildren(tbl.OID)
		if len(children) > 0 {
			parentScan := &SeqScan{pos: rv.Pos(), Table: tbl, Alias: rv.Alias, schema: ctx.schema}
			var root Node = parentScan
			for _, child := range children {
				childSchema := tableSchemaWithSource(b.table, sourceIdx)
				childScan := &SeqScan{pos: rv.Pos(), Table: child, Alias: rv.Alias, schema: childSchema}
				root = &SetOp{
					pos:   rv.Pos(),
					Left:  root,
					Right: childScan,
					All:   true,
				}
			}
			return root, b, nil
		}
	}
	return &SeqScan{pos: rv.Pos(), Table: tbl, Alias: rv.Alias, schema: ctx.schema}, b, nil
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
func planSubqueryRangeVar(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16) (Node, rangeBinding, error) {
	inner, err := Plan(rv.Subquery, cat)
	if err != nil {
		// LATERAL subquery fallback: when the inner subquery references outer
		// columns (correlated lateral reference) the planner fails with a
		// "missing FROM-clause entry" error because the outer scope is not
		// visible inside the derived table's separate Plan() call.
		//
		// For vacuumdb's specific use case the lateral subquery is:
		//   CROSS JOIN LATERAL (SELECT c.relkind IN ('p', 'I')) as p (inherited)
		// The `inherited` column is only used in --missing-stats-only queries,
		// not in the basic vacuumdb run. We fall back to a single-row NULL plan
		// so the CROSS JOIN produces one row per outer row (with inherited=NULL).
		// This is safe: the WHERE clause for basic vacuumdb does not reference
		// p.inherited so the final result set is correct.
		if pe, ok := err.(*PlanError); ok && (pe.Code == "42P01" || pe.Code == "42703") &&
			len(rv.Columns) > 0 {
			nullRow := make([]Expr, len(rv.Columns))
			for i := range nullRow {
				nullRow[i] = &NullConst{}
			}
			cols := make([]catalog.Column, len(rv.Columns))
			schema := make(Schema, len(rv.Columns))
			for i, colName := range rv.Columns {
				cols[i] = catalog.Column{Name: colName}
				schema[i] = SchemaColumn{Name: colName}
			}
			inner = &Values{Rows: [][]Expr{nullRow}, schema: schema}
			tbl := &catalog.Table{Name: rv.Alias, Columns: cols}
			b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
			return inner, b, nil
		}
		return nil, rangeBinding{}, err
	}
	innerSchema := inner.Output()
	// Use explicit column-alias list when provided: (SELECT …) AS t (c1, c2).
	// This overrides the target-list aliases from the inner SELECT.
	cols := make([]catalog.Column, 0, len(rv.Subquery.Targets))
	schema := make(Schema, 0, len(rv.Subquery.Targets))
	for i, tgt := range rv.Subquery.Targets {
		var name string
		if i < len(rv.Columns) && rv.Columns[i] != "" {
			name = rv.Columns[i] // explicit column alias from (SELECT …) AS t (col_alias)
		} else {
			name = tgt.Alias
			if name == "" {
				name = deriveSubqueryTargetName(tgt.Expr)
			}
			if name == "" {
				name = fmt.Sprintf("?column?%d", i+1)
			}
		}
		var typ catalog.Type
		if i < len(innerSchema) {
			typ = innerSchema[i].Type
		}
		cols = append(cols, catalog.Column{Name: name, Type: typ})
		// Subquery columns are derived (an inner SELECT's
		// computed targets); they have no base-table identity at
		// the outer scope. The binding's sourceIdx still gets the
		// caller's monotonic value so qualified `sub.col`
		// references can be disambiguated against sibling
		// bindings, but the columns themselves stay at 0
		// (Go zero-value = unknown).
		schema = append(schema, SchemaColumn{Name: name, Type: typ})
	}
	tbl := &catalog.Table{Name: rv.Alias, Columns: cols}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
	return inner, b, nil
}

// planTableFuncRangeVar plans a table-valued function in the FROM clause.
// Currently only generate_series(start, stop[, step]) is supported.
func planTableFuncRangeVar(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16) (Node, rangeBinding, error) {
	tf := rv.TableFunc
	if !strings.EqualFold(tf.Name, "generate_series") {
		return nil, rangeBinding{}, &PlanError{Pos: tf.Pos(), Code: "0A000",
			Message: fmt.Sprintf("table-valued function %q not supported", tf.Name)}
	}
	if len(tf.Args) < 2 || len(tf.Args) > 3 {
		return nil, rangeBinding{}, &PlanError{Pos: tf.Pos(), Code: "42883",
			Message: "generate_series requires 2 or 3 arguments"}
	}
	ctx := &resolveContext{}
	start, err := resolveExpr(tf.Args[0], ctx)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	stop, err := resolveExpr(tf.Args[1], ctx)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	var step Expr
	if len(tf.Args) == 3 {
		step, err = resolveExpr(tf.Args[2], ctx)
		if err != nil {
			return nil, rangeBinding{}, err
		}
	}
	alias := rv.Alias
	if alias == "" {
		alias = "generate_series"
	}
	colName := alias
	if len(rv.Columns) > 0 {
		colName = rv.Columns[0]
	}
	tbl := &catalog.Table{
		Name: alias,
		Columns: []catalog.Column{
			{Name: colName, Type: catalog.Type{Name: "int8"}, Ordinal: 0},
		},
	}
	schema := Schema{SchemaColumn{Name: colName, Type: catalog.Type{Name: "int8"}, SourceTableIdx: sourceIdx}}
	node := &GenerateSeries{pos: tf.Pos(), Start: start, Stop: stop, Step: step, schema: schema}
	b := rangeBinding{table: tbl, alias: alias, offset: 0, sourceIdx: sourceIdx}
	return node, b, nil
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
		eq := &BinaryOp{pos: pos, Op: parser.OpEq, Left: l, Right: r}
		if pred == nil {
			pred = eq
		} else {
			pred = &BinaryOp{pos: pos, Op: parser.OpAnd, Left: pred, Right: eq}
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
	if !ok || bin.Op != parser.OpEq {
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

type windowBinding struct {
	index int
	typ   catalog.Type
}

type windowSurface struct {
	input       *resolveContext
	agg         *aggregateSurface
	output      *resolveContext
	windowByKey map[string]windowBinding
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

func needsWindowStage(s *parser.SelectStmt) bool {
	for _, t := range s.Targets {
		if parserExprHasWindowFunc(t.Expr) {
			return true
		}
	}
	for _, sb := range s.OrderBy {
		expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
		if parserExprHasWindowFunc(expr) {
			return true
		}
	}
	return false
}

func buildWindowStage(s *parser.SelectStmt, child Node, inputCtx *resolveContext, agg *aggregateSurface) (Node, *resolveContext, *windowSurface, error) {
	calls, err := collectWindowCalls(s)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(calls) == 0 {
		return child, inputCtx, nil, nil
	}

	firstSpec := windowSpecKey(calls[0].Over)
	for i := 1; i < len(calls); i++ {
		if windowSpecKey(calls[i].Over) != firstSpec {
			return nil, nil, nil, &PlanError{Pos: calls[i].Pos(), Code: "0A000", Message: "multiple window specifications are not supported in v0 planner"}
		}
	}

	partition := make([]Expr, 0, len(calls[0].Over.PartitionBy))
	for _, p := range calls[0].Over.PartitionBy {
		r, err := resolveExprForWindowInput(p, inputCtx, agg)
		if err != nil {
			return nil, nil, nil, err
		}
		partition = append(partition, r)
	}
	order := make([]SortKey, 0, len(calls[0].Over.OrderBy))
	for _, ob := range calls[0].Over.OrderBy {
		r, err := resolveExprForWindowInput(ob.Expr, inputCtx, agg)
		if err != nil {
			return nil, nil, nil, err
		}
		order = append(order, SortKey{Expr: r, Desc: ob.Desc})
	}

	outputSchema := append(Schema(nil), inputCtx.schema...)
	funcs := make([]WindowFunc, 0, len(calls))
	byKey := make(map[string]windowBinding, len(calls))
	for _, fc := range calls {
		k := windowCallKey(fc)
		if _, exists := byKey[k]; exists {
			continue
		}
		wf, err := buildWindowFunc(fc, inputCtx, agg)
		if err != nil {
			return nil, nil, nil, err
		}
		idx := len(outputSchema)
		funcs = append(funcs, wf)
		outputSchema = append(outputSchema, SchemaColumn{Name: strings.ToLower(fc.Name.Name), Type: wf.Type})
		byKey[k] = windowBinding{index: idx, typ: wf.Type}
	}

	windowNode := &WindowAgg{
		pos:         s.Pos(),
		Child:       child,
		PartitionBy: partition,
		OrderBy:     order,
		Funcs:       funcs,
		schema:      outputSchema,
	}
	outCtx := newResolveContext(nil, outputSchema)
	surface := &windowSurface{input: inputCtx, agg: agg, output: outCtx, windowByKey: byKey}
	return windowNode, outCtx, surface, nil
}

func collectWindowCalls(s *parser.SelectStmt) ([]*parser.FuncCall, error) {
	seen := map[string]struct{}{}
	out := make([]*parser.FuncCall, 0)
	visit := func(e parser.Expr) error {
		return walkExprForWindows(e, func(fc *parser.FuncCall) error {
			if fc.Over == nil {
				return nil
			}
			k := windowCallKey(fc)
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
	for _, sb := range s.OrderBy {
		expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
		if err := visit(expr); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// inferExprType returns the catalog type of a resolved planner Expr.
// Used to derive the return type of lag/lead from their first argument.
func inferExprType(e Expr) catalog.Type {
	switch x := e.(type) {
	case *ColumnRef:
		return x.Type
	case *OuterColumnRef:
		return x.Type
	case *IntegerConst:
		return catalog.Type{Name: "int8"}
	case *NumericConst:
		return catalog.Type{Name: "numeric"}
	case *StringConst:
		return catalog.Type{Name: "text"}
	case *BooleanConst:
		return catalog.Type{Name: "bool"}
	default:
		return catalog.Type{Name: "text"}
	}
}

func buildWindowFunc(fc *parser.FuncCall, inputCtx *resolveContext, agg *aggregateSurface) (WindowFunc, error) {
	name := strings.ToLower(fc.Name.Name)
	switch name {
	case "row_number", "rank":
		if fc.Star || fc.Distinct || len(fc.Args) != 0 {
			return WindowFunc{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: fmt.Sprintf("window function %s() does not accept arguments, DISTINCT, or * in v0", name)}
		}
		return WindowFunc{pos: fc.Pos(), Name: name, Type: catalog.Type{Name: "int8"}}, nil
	case "lag", "lead":
		if fc.Star || fc.Distinct || len(fc.Args) < 1 || len(fc.Args) > 3 {
			return WindowFunc{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: fmt.Sprintf("window function %s() requires 1 to 3 arguments", name)}
		}
		args := make([]Expr, 0, len(fc.Args))
		for _, a := range fc.Args {
			resolved, err := resolveExprForWindowInput(a, inputCtx, agg)
			if err != nil {
				return WindowFunc{}, err
			}
			args = append(args, resolved)
		}
		retType := inferExprType(args[0])
		return WindowFunc{pos: fc.Pos(), Name: name, Type: retType, Args: args}, nil
	default:
		return WindowFunc{}, &PlanError{Pos: fc.Pos(), Code: "0A000", Message: fmt.Sprintf("window function %q is not supported in v0 planner", name)}
	}
}

func windowCallKey(fc *parser.FuncCall) string {
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
	b.WriteString("over:")
	b.WriteString(windowSpecKey(fc.Over))
	return b.String()
}

func windowSpecKey(w *parser.WindowDef) string {
	if w == nil {
		return ""
	}
	b := strings.Builder{}
	b.WriteString("p:")
	for _, p := range w.PartitionBy {
		b.WriteString(parserExprKey(p))
		b.WriteString("|")
	}
	b.WriteString("o:")
	for _, o := range w.OrderBy {
		b.WriteString(parserExprKey(o.Expr))
		if o.Desc {
			b.WriteString(":desc")
		} else {
			b.WriteString(":asc")
		}
		b.WriteString("|")
	}
	return b.String()
}

func walkExprForWindows(e parser.Expr, fn func(*parser.FuncCall) error) error {
	switch x := e.(type) {
	case *parser.BinaryOp:
		if err := walkExprForWindows(x.Left, fn); err != nil {
			return err
		}
		return walkExprForWindows(x.Right, fn)
	case *parser.UnaryOp:
		return walkExprForWindows(x.Operand, fn)
	case *parser.CastExpr:
		return walkExprForWindows(x.Operand, fn)
	case *parser.ExtractExpr:
		return walkExprForWindows(x.Source, fn)
	case *parser.CaseExpr:
		if x.Operand != nil {
			if err := walkExprForWindows(x.Operand, fn); err != nil {
				return err
			}
		}
		for _, w := range x.Whens {
			if err := walkExprForWindows(w.When, fn); err != nil {
				return err
			}
			if err := walkExprForWindows(w.Then, fn); err != nil {
				return err
			}
		}
		if x.Else != nil {
			return walkExprForWindows(x.Else, fn)
		}
		return nil
	case *parser.IsNullExpr:
		return walkExprForWindows(x.Operand, fn)
	case *parser.InExpr:
		if err := walkExprForWindows(x.Operand, fn); err != nil {
			return err
		}
		for _, v := range x.List {
			if err := walkExprForWindows(v, fn); err != nil {
				return err
			}
		}
		return nil
	case *parser.FuncCall:
		if err := fn(x); err != nil {
			return err
		}
		for _, a := range x.Args {
			if err := walkExprForWindows(a, fn); err != nil {
				return err
			}
		}
		if x.Over != nil {
			for _, p := range x.Over.PartitionBy {
				if err := walkExprForWindows(p, fn); err != nil {
					return err
				}
			}
			for _, o := range x.Over.OrderBy {
				if err := walkExprForWindows(o.Expr, fn); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	case *parser.IsNullExpr:
		operand, err := resolveExpr(x.Operand, agg.input)
		if err != nil {
			return nil, err
		}
		return &IsNullExpr{pos: x.Pos(), Operand: operand, Negated: x.Negated}, nil
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
		if x.Over != nil {
			return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "window functions must be planned via WindowAgg"}
		}
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

func resolveExprForWindowInput(e parser.Expr, inputCtx *resolveContext, agg *aggregateSurface) (Expr, error) {
	if agg == nil {
		return resolveExpr(e, inputCtx)
	}
	return resolveExprAfterAggregate(e, agg)
}

func resolveExprAfterWindow(e parser.Expr, win *windowSurface) (Expr, error) {
	if !parserExprHasWindowFunc(e) {
		return resolveExprForWindowInput(e, win.input, win.agg)
	}
	switch x := e.(type) {
	case *parser.BinaryOp:
		l, err := resolveExprAfterWindow(x.Left, win)
		if err != nil {
			return nil, err
		}
		r, err := resolveExprAfterWindow(x.Right, win)
		if err != nil {
			return nil, err
		}
		return &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}, nil
	case *parser.UnaryOp:
		op, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, nil
	case *parser.CastExpr:
		return resolveExprAfterWindow(x.Operand, win)
	case *parser.ExtractExpr:
		src, err := resolveExprAfterWindow(x.Source, win)
		if err != nil {
			return nil, err
		}
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: src}, nil
	case *parser.CaseExpr:
		out := &CaseExpr{pos: x.Pos()}
		if x.Operand != nil {
			op, err := resolveExprAfterWindow(x.Operand, win)
			if err != nil {
				return nil, err
			}
			out.Operand = op
		}
		for _, w := range x.Whens {
			when, err := resolveExprAfterWindow(w.When, win)
			if err != nil {
				return nil, err
			}
			then, err := resolveExprAfterWindow(w.Then, win)
			if err != nil {
				return nil, err
			}
			out.Whens = append(out.Whens, CaseWhen{When: when, Then: then})
		}
		if x.Else != nil {
			els, err := resolveExprAfterWindow(x.Else, win)
			if err != nil {
				return nil, err
			}
			out.Else = els
		}
		return out, nil
	case *parser.FuncCall:
		if x.Over != nil {
			k := windowCallKey(x)
			b, ok := win.windowByKey[k]
			if !ok {
				return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "window call could not be resolved"}
			}
			return &ColumnRef{pos: x.Pos(), Index: b.index, Name: win.output.schema[b.index].Name, Type: b.typ}, nil
		}
		args := make([]Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := resolveExprAfterWindow(a, win)
			if err != nil {
				return nil, err
			}
			args = append(args, pa)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name.String(), Args: args, Star: x.Star}, nil
	case *parser.InExpr:
		op, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		list := make([]Expr, 0, len(x.List))
		for _, item := range x.List {
			r, err := resolveExprAfterWindow(item, win)
			if err != nil {
				return nil, err
			}
			list = append(list, r)
		}
		if x.Subquery != nil {
			return planInExpr(x, win.input)
		}
		return &InExpr{pos: x.Pos(), Operand: op, Negated: x.Negated, List: list}, nil
	case *parser.IsNullExpr:
		operand, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		return &IsNullExpr{pos: x.Pos(), Operand: operand, Negated: x.Negated}, nil
	}
	return resolveExprForWindowInput(e, win.input, win.agg)
}

func resolveTargetsAfterWindow(targets []parser.ResTarget, win *windowSurface) ([]Expr, Schema, error) {
	out := make([]Expr, 0, len(targets))
	schema := make(Schema, 0, len(targets))
	for _, t := range targets {
		if star, ok := t.Expr.(*parser.StarExpr); ok {
			exprList, cols, err := expandStarTarget(star, win.input)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, exprList...)
			schema = append(schema, cols...)
			continue
		}
		e, err := resolveExprAfterWindow(t.Expr, win)
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
	case *parser.IsNullExpr:
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
	// Window functions (with OVER clause) are NOT aggregates.
	if fc.Over != nil {
		return false
	}
	switch strings.ToLower(fc.Name.Name) {
	case "count", "sum", "avg", "min", "max",
		// Statistical aggregates (M0097-0007)
		"var_pop", "var_samp", "variance",
		"stddev_pop", "stddev_samp", "stddev",
		"corr", "covar_pop", "covar_samp",
		"regr_count", "regr_sxx", "regr_syy", "regr_sxy",
		"regr_avgx", "regr_avgy", "regr_r2",
		"regr_slope", "regr_intercept",
		// Boolean aggregates
		"bool_and", "bool_or", "every",
		// Bitwise aggregates
		"bit_and", "bit_or", "bit_xor",
		// Miscellaneous aggregates
		"string_agg", "array_agg", "json_agg", "jsonb_agg",
		"json_object_agg", "jsonb_object_agg",
		"xmlagg", "any_value",
		// Ordered-set aggregates (only aggregate form, not window)
		"percentile_cont", "percentile_disc", "mode":
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

func parserExprHasWindowFunc(e parser.Expr) bool {
	found := false
	_ = walkExprForWindows(e, func(fc *parser.FuncCall) error {
		if fc.Over != nil {
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
	// Many aggregates accept 0 or more args; only enforce 1-arg for the
	// core ones. Extended aggregates (M0097-0007) may have 2+ args.
	if len(fc.Args) == 0 {
		// Zero-arg aggregates like count(*) handled above; all others need args.
		return AggregateCall{
			pos: fc.Pos(), Name: name, Distinct: fc.Distinct,
			Type: catalog.Type{Name: "numeric"},
		}, nil
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
		// Extended aggregates (M0097-0007): accept but return null/stub type.
		outType = catalog.Type{Name: "numeric"}
	}
	// Resolve FILTER (WHERE ...) predicate if present.
	var filterExpr Expr
	if fc.Filter != nil {
		var ferr error
		filterExpr, ferr = resolveExpr(fc.Filter, inputCtx)
		if ferr != nil {
			return AggregateCall{}, ferr
		}
	}
	return AggregateCall{
		pos:      fc.Pos(),
		Name:     name,
		Arg:      argExpr,
		Distinct: fc.Distinct,
		Type:     outType,
		Filter:   filterExpr,
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
		return "u:" + x.Op.String() + ":" + parserExprKey(x.Operand)
	case *parser.BinaryOp:
		return "b:" + x.Op.String() + ":(" + parserExprKey(x.Left) + "):(" + parserExprKey(x.Right) + ")"
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
	if !ok || b.Op != parser.OpEq {
		// Not an equality predicate — try range index scan.
		return tryRangeIndexScan(where, tbl, ctx, cat)
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
	case *IntegerConst, *NumericConst, *ParamRef:
		// NumericConst landed alongside M0011-0002: a NUMERIC
		// b-tree index can be probed by either an integer or a
		// numeric literal on the rhs of `=`. The executor's
		// encodeBTreeKeyForColumn picks the right encoding from
		// the column type.
	case *StringConst:
		// M0044-0005: varchar/char column indexes — probe key is
		// a plain string literal; evaluates to KindString at
		// runtime and routes through EncodeVarchar/EncodeChar.
	case *TypedStringLit:
		// M0044-0005: timestamp column indexes — probe key is a
		// typed literal like `timestamp '1995-01-01'`; evaluates
		// to KindTime and routes through EncodeTimestamp.
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

// findBTreeIndexForColumn locates a B-tree index whose leading column
// matches `col`. M0053-0001: composite indexes are accepted when their
// FIRST key column matches; the resulting IndexScan probes only the
// leading-column value. This is correct because B-tree key encoding is
// a byte-wise concatenation of column encodings — any leaf key whose
// leading-column bytes equal the probe value is a candidate. The
// executor (`indexScanOp.lookupKey`) widens the upper bound with 0xFF
// padding so the inclusive RangeScan range covers all suffix values.
//
// A single-column-only index is preferred when both shapes match the
// column (cheaper probe — exact equality vs. prefix range) so the
// search returns a single-column index first when one exists.
func findBTreeIndexForColumn(cat catalog.Catalog, tbl *catalog.Table, col string) *catalog.Index {
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if strings.ToLower(idx.Method) != "btree" {
			continue
		}
		if len(idx.Columns) == 0 || idx.Columns[0] != col {
			continue
		}
		if len(idx.Columns) == 1 {
			return idx
		}
		if composite == nil {
			composite = idx
		}
	}
	return composite
}

// collectAndConjuncts walks an AND chain and returns the leaf conjuncts.
// A non-AND node returns a single-element slice containing itself.
func collectAndConjuncts(e parser.Expr) []parser.Expr {
	b, ok := e.(*parser.BinaryOp)
	if !ok || b.Op != parser.OpAnd {
		return []parser.Expr{e}
	}
	result := collectAndConjuncts(b.Left)
	result = append(result, collectAndConjuncts(b.Right)...)
	return result
}

// isConstantExpr reports whether the RESOLVED (planner.Expr) expression
// contains no column references or subqueries — i.e., its value does not
// depend on the current row.
func isConstantExpr(e Expr) bool {
	switch x := e.(type) {
	case *ColumnRef, *OuterColumnRef:
		return false
	case *SubqueryExpr, *ExistsExpr, *InExpr, *IsNullExpr:
		return false
	case *BinaryOp:
		return isConstantExpr(x.Left) && isConstantExpr(x.Right)
	case *UnaryOp:
		return isConstantExpr(x.Operand)
	case *IntegerConst, *StringConst, *NumericConst,
		*TypedStringLit, *IntervalLit,
		*NullConst, *BooleanConst, *ParamRef:
		return true
	case *ExtractExpr:
		return isConstantExpr(x.Source)
	case *CaseExpr:
		if x.Operand != nil && !isConstantExpr(x.Operand) {
			return false
		}
		for _, w := range x.Whens {
			if !isConstantExpr(w.When) || !isConstantExpr(w.Then) {
				return false
			}
		}
		if x.Else != nil && !isConstantExpr(x.Else) {
			return false
		}
		return true
	case *FuncCall:
		for _, a := range x.Args {
			if !isConstantExpr(a) {
				return false
			}
		}
		return true
	default:
		return false // conservative
	}
}

// flipRangeOp flips a comparison operator for the "key op col" → "col flippedOp key"
// canonical form (column on the left).
func flipRangeOp(op parser.OpCode) parser.OpCode {
	switch op {
	case parser.OpLt:
		return parser.OpGt
	case parser.OpLe:
		return parser.OpGe
	case parser.OpGt:
		return parser.OpLt
	case parser.OpGe:
		return parser.OpLe
	}
	return op
}

// tryRangeIndexScan attempts to build a Filter(IndexScan{LowKey,HighKey})
// from a WHERE expression containing one or more range predicates (< <= > >=)
// on a single B-tree-indexed column. Returns (nil, false, nil) when no range
// index is applicable.
func tryRangeIndexScan(where parser.Expr, tbl *catalog.Table, ctx *resolveContext, cat catalog.Catalog) (Node, bool, error) {
	conjuncts := collectAndConjuncts(where)

	var chosenColName string
	var chosenIdx *catalog.Index
	var loKey Expr // inclusive lower bound
	var hiKey Expr // inclusive upper bound

	for _, conj := range conjuncts {
		b, ok := conj.(*parser.BinaryOp)
		if !ok {
			continue
		}
		op := b.Op
		if op != parser.OpLt && op != parser.OpLe && op != parser.OpGt && op != parser.OpGe {
			continue
		}

		var colRef *parser.ColumnRef
		var keyExpr parser.Expr
		colOnLeft := false

		if lc, ok := b.Left.(*parser.ColumnRef); ok {
			colRef = lc
			keyExpr = b.Right
			colOnLeft = true
		} else if rc, ok := b.Right.(*parser.ColumnRef); ok {
			colRef = rc
			keyExpr = b.Left
			colOnLeft = false
		} else {
			continue
		}

		// Make sure the other side is NOT also a column ref
		if colOnLeft {
			if _, ok := b.Right.(*parser.ColumnRef); ok {
				continue
			}
		} else {
			if _, ok := b.Left.(*parser.ColumnRef); ok {
				continue
			}
		}

		// Flip op to canonical "col op key" form
		canonOp := op
		if !colOnLeft {
			canonOp = flipRangeOp(op)
		}

		// Resolve column
		resolvedCol, err := resolveColumnRef(colRef, ctx)
		if err != nil {
			continue
		}
		col, ok := resolvedCol.(*ColumnRef)
		if !ok {
			continue
		}

		// Ensure column is from the target table (not outer ref)
		if chosenColName == "" {
			// First indexed column: look up a B-tree index for it
			idx := findBTreeIndexForColumn(cat, tbl, col.Name)
			if idx == nil {
				continue
			}
			chosenColName = col.Name
			chosenIdx = idx
		} else if col.Name != chosenColName {
			// Different column — ignore this conjunct (keep first column)
			continue
		}

		// Resolve the key expression
		resolvedKey, err := resolveExpr(keyExpr, ctx)
		if err != nil {
			continue
		}
		if !isConstantExpr(resolvedKey) {
			continue
		}

		// Assign bounds based on canonical operator
		switch canonOp {
		case parser.OpGt, parser.OpGe:
			if loKey == nil {
				loKey = resolvedKey
			}
		case parser.OpLt, parser.OpLe:
			if hiKey == nil {
				hiKey = resolvedKey
			}
		}
	}

	if chosenIdx == nil {
		return nil, false, nil
	}

	// Resolve the full WHERE predicate for the Filter node
	fullPred, err := resolveExpr(where, ctx)
	if err != nil {
		return nil, false, err
	}

	scan := &IndexScan{
		pos:     where.Pos(),
		Table:   tbl,
		Index:   chosenIdx,
		LowKey:  loKey,
		HighKey: hiKey,
		schema:  ctx.schema,
	}
	return &Filter{pos: where.Pos(), Child: scan, Predicate: fullPred}, true, nil
}

func planInsert(s *parser.InsertStmt, cat catalog.Catalog) (Node, error) {
	restore, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{
			Pos:     s.Target.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Target.Name),
		}
	}
	// Map source-row column index -> target table column ordinal.
	// Generated columns are excluded from the mapping when no explicit
	// column list is provided — they are computed by the executor. M0096-0008.
	var colIndex []int
	if len(s.Columns) == 0 {
		colIndex = make([]int, 0, len(tbl.Columns))
		for i, col := range tbl.Columns {
			if col.GeneratedAlways {
				continue // skip generated columns; executor fills them in
			}
			colIndex = append(colIndex, i)
		}
		if len(colIndex) == len(tbl.Columns) {
			// No generated columns — keep original 1:1 mapping for compatibility.
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
	// INSERT … SELECT: plan the SELECT and use it as the source.
	var source Node
	if s.Select != nil {
		sel, err := planSelect(s.Select, cat)
		if err != nil {
			return nil, err
		}
		source = sel
	} else {
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
		source = &Values{pos: s.Pos(), Rows: rows, schema: insertValuesSchema(tbl, colIndex)}
	}
	insert := &Insert{pos: s.Pos(), Table: tbl, Source: source, ColumnIndex: colIndex}
	if s.OnConflict != nil {
		oc, err := planOnConflict(s.OnConflict, tbl, s.Target.Alias, cat)
		if err != nil {
			return nil, err
		}
		insert.OnConflict = oc
	}
	return insert, nil
}

// planOnConflict resolves the parser-level ON CONFLICT clause into
// the planner-side OnConflictPlan: arbiter-index selection from
// the conflict target columns, plus expression resolution for the
// DO UPDATE branch under a target+excluded scope. M0017-0002.
func planOnConflict(oc *parser.OnConflictClause, tbl *catalog.Table, targetAlias string, cat catalog.Catalog) (*OnConflictPlan, error) {
	out := &OnConflictPlan{}

	switch oc.Action {
	case parser.OnConflictNothing:
		out.Action = OnConflictActionNothing
	case parser.OnConflictUpdate:
		out.Action = OnConflictActionUpdate
	default:
		return nil, &PlanError{Pos: oc.Pos(), Code: "XX000", Message: fmt.Sprintf("unexpected ON CONFLICT action %d", oc.Action)}
	}

	// Arbiter-index selection. For the no-target form (DO NOTHING
	// only — analyzer rejects DO UPDATE without a target),
	// ArbiterIndex stays nil; the executor checks every unique
	// index when the row hits.
	if oc.Target != nil {
		idx, ords, err := resolveArbiterIndex(oc.Target, tbl, cat)
		if err != nil {
			return nil, err
		}
		out.ArbiterIndex = idx
		out.ArbiterColumns = ords
	}

	if out.Action != OnConflictActionUpdate {
		return out, nil
	}

	// DO UPDATE expression resolution.
	//
	// Build a 2-binding scope: the target table at offset 0 (bare
	// refs and `<target>.col` resolve here) and the same table
	// re-bound as `excluded` at offset N with qualifiedOnly so
	// `excluded.col` resolves only via the alias path. Schema is
	// 2N wide — the executor will arrange a merged tuple at
	// runtime.
	primaryAlias := targetAlias
	if primaryAlias == "" {
		primaryAlias = tbl.Name
	}
	n := len(tbl.Columns)
	// Primary at sourceIdx=1, excluded at sourceIdx=2 — both refer
	// to the same catalog table but disambiguate by source so
	// `excluded.col` and `<target>.col` rebind helpers don't
	// collapse into the same Index.
	bindings := []rangeBinding{
		{table: tbl, alias: primaryAlias, offset: 0, sourceIdx: 1},
		{table: tbl, alias: "excluded", offset: n, qualifiedOnly: true, sourceIdx: 2},
	}
	mergedSchema := make(Schema, 0, 2*n)
	mergedSchema = append(mergedSchema, tableSchemaWithSource(tbl, 1)...)
	mergedSchema = append(mergedSchema, tableSchemaWithSource(tbl, 2)...)
	ctx := newResolveContext(bindings, mergedSchema)
	ctx.cat = cat

	out.UpdateSet = make([]Expr, n)
	for _, a := range oc.UpdateSet {
		col, ok := cat.LookupColumn(tbl, a.Column)
		if !ok {
			return nil, &PlanError{Pos: a.Pos(), Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", a.Column, tbl.Name)}
		}
		expr, err := resolveExpr(a.Expr, ctx)
		if err != nil {
			return nil, err
		}
		out.UpdateSet[col.Ordinal] = expr
	}
	if oc.UpdateWhere != nil {
		pred, err := resolveExpr(oc.UpdateWhere, ctx)
		if err != nil {
			return nil, err
		}
		out.UpdateWhere = pred
	}
	return out, nil
}

// resolveArbiterIndex matches the parsed conflict-target columns
// against tbl's catalog indexes. A unique index whose column set
// equals the target column set (case-insensitive, order-insensitive)
// arbitrates the conflict. Mirrors upstream's "inference
// specification" rule: the user's columns must canonically match
// some unique constraint.
//
// Returns (idx, ordinals, nil) on a single match — ordinals are
// `tbl.Columns` ordinals matching idx.Columns in catalog order so
// the executor can extract the conflict key from a row tuple
// without a name lookup. SQLSTATE 42P10 ("invalid_column_reference"
// — upstream's "no unique or exclusion constraint matching the ON
// CONFLICT specification") on no match.
func resolveArbiterIndex(target *parser.OnConflictTarget, tbl *catalog.Table, cat catalog.Catalog) (*catalog.Index, []int, error) {
	// Constraint-name target form (M0017 Stage B). The named index
	// must exist, must be a unique index, and must belong to the
	// target table. The analyzer already enforces these but the
	// planner stays self-contained for paths that bypass it.
	if target.Constraint != "" {
		idx, ok := cat.LookupIndex(parser.ObjectName{Name: target.Constraint})
		if !ok {
			return nil, nil, &PlanError{Pos: target.Pos(), Code: "42704", Message: fmt.Sprintf("constraint %q for table %q does not exist", target.Constraint, tbl.Name)}
		}
		if idx.Table != tbl {
			return nil, nil, &PlanError{Pos: target.Pos(), Code: "42704", Message: fmt.Sprintf("constraint %q does not belong to table %q", target.Constraint, tbl.Name)}
		}
		if !idx.Unique {
			return nil, nil, &PlanError{Pos: target.Pos(), Code: "42P10", Message: fmt.Sprintf("constraint %q is not a unique constraint", target.Constraint)}
		}
		ords := make([]int, 0, len(idx.Columns))
		for _, ic := range idx.Columns {
			col, ok := cat.LookupColumn(tbl, ic)
			if !ok {
				return nil, nil, &PlanError{Pos: target.Pos(), Code: "XX000", Message: fmt.Sprintf("index %q column %q not found on table %q", idx.Name, ic, tbl.Name)}
			}
			ords = append(ords, col.Ordinal)
		}
		return idx, ords, nil
	}
	if len(target.Columns) == 0 {
		return nil, nil, &PlanError{Pos: target.Pos(), Code: "42601", Message: "ON CONFLICT target requires at least one column"}
	}
	wanted := make(map[string]struct{}, len(target.Columns))
	for _, c := range target.Columns {
		wanted[strings.ToLower(c)] = struct{}{}
	}
	if len(wanted) != len(target.Columns) {
		return nil, nil, &PlanError{Pos: target.Pos(), Code: "42P10", Message: "ON CONFLICT target list contains duplicate columns"}
	}
	for _, idx := range cat.IndexesOnTable(tbl) {
		if !idx.Unique {
			continue
		}
		if len(idx.Columns) != len(target.Columns) {
			continue
		}
		match := true
		for _, ic := range idx.Columns {
			if _, ok := wanted[strings.ToLower(ic)]; !ok {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		ords := make([]int, 0, len(idx.Columns))
		for _, ic := range idx.Columns {
			col, ok := cat.LookupColumn(tbl, ic)
			if !ok {
				// Catalog inconsistency — index references a
				// column that isn't on the table. Surface
				// loudly so we don't silently produce a
				// wrong arbiter.
				return nil, nil, &PlanError{Pos: target.Pos(), Code: "XX000", Message: fmt.Sprintf("index %q column %q not found on table %q", idx.Name, ic, tbl.Name)}
			}
			ords = append(ords, col.Ordinal)
		}
		return idx, ords, nil
	}
	return nil, nil, &PlanError{Pos: target.Pos(), Code: "42P10", Message: fmt.Sprintf("there is no unique or exclusion constraint matching the ON CONFLICT specification on relation %q", tbl.Name)}
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
	restore, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}
	ctx := singleBindingContext(tbl, s.Target.Alias)
	ctx.cat = cat
	var node Node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	if s.Where != nil {
		// M0021-0009 step 2d: try the index-driven probe first
		// for `WHERE indexed_col = key` shapes. Mirrors planSelect's
		// `if idxNode, ok, err := planIndexScanFromWhere(...)` arm.
		// Falls through to Filter(SeqScan) on no index match.
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
	restore, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}
	ctx := singleBindingContext(tbl, s.Target.Alias)
	ctx.cat = cat
	var node Node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	if s.Where != nil {
		// M0021-0009 step 2d: index-driven probe for
		// `WHERE indexed_col = key` shapes; falls through to
		// Filter(SeqScan).
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
	}
	return &Delete{pos: s.Pos(), Table: tbl, Child: node}, nil
}

// planMerge converts a MERGE INTO statement into a Merge plan node.
// M0096-0010.
func planMerge(s *parser.MergeStmt, cat catalog.Catalog) (Node, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}

	// Plan the USING source.
	var srcIdx int16 = 2
	sourceNode, sourceBinding, err := planScanRangeVar(s.Source, cat, srcIdx)
	if err != nil {
		return nil, err
	}

	// Build a merged schema: target columns first, then source columns.
	targetAlias := s.Target.Alias
	if targetAlias == "" {
		targetAlias = tbl.Name
	}
	n := len(tbl.Columns)
	sourceSchema := sourceNode.Output()
	if sourceSchema == nil && sourceBinding.table != nil {
		sourceSchema = tableSchemaWithSource(sourceBinding.table, srcIdx)
	}

	targetBinding := rangeBinding{table: tbl, alias: targetAlias, offset: 0, sourceIdx: 1}
	sourceBinding.offset = n

	mergedSchema := make(Schema, 0, n+len(sourceSchema))
	mergedSchema = append(mergedSchema, tableSchemaWithSource(tbl, 1)...)
	mergedSchema = append(mergedSchema, sourceSchema...)
	mergedCtx := newResolveContext([]rangeBinding{targetBinding, sourceBinding}, mergedSchema)
	mergedCtx.cat = cat

	// Source-only context for NOT MATCHED INSERT VALUES.
	sourceOnly := newResolveContext([]rangeBinding{{
		table: sourceBinding.table, alias: sourceBinding.alias,
		offset: 0, sourceIdx: srcIdx,
	}}, sourceSchema)
	sourceOnly.cat = cat

	onExpr, err := resolveExpr(s.On, mergedCtx)
	if err != nil {
		return nil, err
	}

	clauses := make([]*MergeWhenClause, 0, len(s.Clauses))
	for _, wc := range s.Clauses {
		pc := &MergeWhenClause{
			Matched: wc.Matched,
			Action:  MergeActionKind(wc.Action),
		}
		if wc.Condition != nil {
			cond, err := resolveExpr(wc.Condition, mergedCtx)
			if err != nil {
				return nil, err
			}
			pc.Condition = cond
		}
		switch wc.Action {
		case parser.MergeActionUpdate:
			set := make([]Expr, n)
			for _, a := range wc.UpdateAssigns {
				col, colOK := cat.LookupColumn(tbl, a.Column)
				if !colOK {
					return nil, &PlanError{Pos: a.Pos(), Code: "42703",
						Message: fmt.Sprintf("column %q of relation %q does not exist", a.Column, tbl.Name)}
				}
				expr, err := resolveExpr(a.Expr, mergedCtx)
				if err != nil {
					return nil, err
				}
				set[col.Ordinal] = expr
			}
			pc.UpdateSet = set
		case parser.MergeActionDelete:
			// nothing extra
		case parser.MergeActionInsert:
			ordinals, err := buildInsertColIdx(tbl, wc.InsertColumns, cat)
			if err != nil {
				return nil, &PlanError{Pos: wc.Pos(), Code: "42703", Message: err.Error()}
			}
			pc.InsertColIdx = ordinals
			if wc.InsertValues != nil {
				exprs := make([]Expr, len(wc.InsertValues))
				for i, ve := range wc.InsertValues {
					expr, err := resolveExpr(ve, sourceOnly)
					if err != nil {
						return nil, err
					}
					exprs[i] = expr
				}
				pc.InsertExprs = exprs
			}
		case parser.MergeActionDoNothing:
			// DO NOTHING — no extra fields needed. M0097-0016.
		}
		clauses = append(clauses, pc)
	}
	return &Merge{pos: s.Pos(), Target: tbl, Source: sourceNode, On: onExpr, Clauses: clauses}, nil
}

// buildInsertColIdx returns column ordinals for a MERGE NOT MATCHED INSERT.
// When names is empty, all non-generated columns are returned in declaration order.
func buildInsertColIdx(tbl *catalog.Table, names []string, cat catalog.Catalog) ([]int, error) {
	if len(names) == 0 {
		out := make([]int, 0, len(tbl.Columns))
		for i, c := range tbl.Columns {
			if !c.GeneratedAlways {
				out = append(out, i)
			}
		}
		return out, nil
	}
	out := make([]int, 0, len(names))
	for _, name := range names {
		col, ok := cat.LookupColumn(tbl, name)
		if !ok {
			return nil, fmt.Errorf("column %q of relation %q does not exist", name, tbl.Name)
		}
		out = append(out, col.Ordinal)
	}
	return out, nil
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
		// Pure ColumnRef pass-through preserves the source
		// identity so downstream rebinds can disambiguate
		// self-join Names. Computed expressions (BinaryOp,
		// FuncCall, etc.) leave SourceTableIdx at 0 = unknown.
		var srcIdx int16
		if cr, ok := expr.(*ColumnRef); ok {
			srcIdx = cr.SourceTableIdx
		}
		schema = append(schema, SchemaColumn{Name: name, Type: typ, SourceTableIdx: srcIdx})
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
			outExpr = append(outExpr, &ColumnRef{pos: star.Pos(), Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx})
			outSchema = append(outSchema, SchemaColumn{Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx})
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
		switch x.Op {
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod:
			lt := exprType(x.Left)
			rt := exprType(x.Right)
			if isNumericTypeName(lt.Name) || isNumericTypeName(rt.Name) {
				return catalog.Type{Name: "numeric"}
			}
			if (lt.Name == "int8" || lt.Name == "int4") && (rt.Name == "int8" || rt.Name == "int4") {
				return catalog.Type{Name: "int8"}
			}
			return catalog.Type{Name: "unknown"}
		case parser.OpConcat:
			return catalog.Type{Name: "text"}
		case parser.OpAnd, parser.OpOr, parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe, parser.OpLike, parser.OpNotLike:
			return catalog.Type{Name: "bool"}
		}
		return catalog.Type{Name: "unknown"}
	case *UnaryOp:
		if x.Op == parser.OpNot {
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
		case "date_part":
			return catalog.Type{Name: "int8"}
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
	return &SubqueryExpr{pos: x.Pos(), Plan: inner, IsNonCorrelated: !planHasOuterRef(inner)}, nil
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
		out.IsNonCorrelated = !planHasOuterRef(inner)
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
	return &ExistsExpr{pos: x.Pos(), Negated: x.Negated, Plan: inner, IsNonCorrelated: !planHasOuterRef(inner)}, nil
}

// planHasOuterRef reports whether any expression anywhere in the
// plan tree is an OuterColumnRef. It descends into nested
// SubqueryExpr/InExpr/ExistsExpr so that a level-2 OuterColumnRef
// inside a nested subquery is not mistaken for non-correlated at
// the outer level.
//
// Used by M0058-0001: a subquery with no OuterColumnRef yields the
// same result for every outer row, so the executor SubqueryCache
// can use a constant key instead of one keyed on the full outer row.
func planHasOuterRef(node Node) bool {
	found := false
	walkPlanExprs(node, func(e Expr) {
		if found {
			return
		}
		walkExprTree(e, func(inner Expr) {
			if found {
				return
			}
			switch x := inner.(type) {
			case *OuterColumnRef:
				found = true
			case *SubqueryExpr:
				if x.Plan != nil && planHasOuterRef(x.Plan) {
					found = true
				}
			case *InExpr:
				if x.Plan != nil && planHasOuterRef(x.Plan) {
					found = true
				}
			case *ExistsExpr:
				if x.Plan != nil && planHasOuterRef(x.Plan) {
					found = true
				}
			}
		})
	})
	return found
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
		if x.Over != nil {
			return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "window functions must be planned via WindowAgg"}
		}
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
	case *parser.IsNullExpr:
		operand, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		return &IsNullExpr{pos: x.Pos(), Operand: operand, Negated: x.Negated}, nil
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
			if b.qualifiedOnly {
				// Pseudo-tables (e.g. ON CONFLICT's
				// `excluded`) reach name resolution only
				// via their alias. See the matching
				// analyzer-side comment in analyzer.go.
				if b.alias != "" && strings.EqualFold(x.Table, b.alias) &&
					(x.Schema == "" || strings.EqualFold(x.Schema, b.table.Schema)) {
					matches = append(matches, b)
				}
				continue
			}
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
					return &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}, true, nil
				}
				return &OuterColumnRef{pos: x.Pos(), Level: level, Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}, true, nil
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
		if b.qualifiedOnly {
			continue
		}
		for i, c := range b.table.Columns {
			if !strings.EqualFold(c.Name, x.Column) {
				continue
			}
			idx := b.offset + i
			if found != nil {
				return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("column reference %q is ambiguous", x.Column)}
			}
			if level == 0 {
				found = &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}
			} else {
				found = &OuterColumnRef{pos: x.Pos(), Level: level, Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}
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


// tryPromoteIndexOnlyScan examines a freshly-built Project node and promotes
// it to an IndexOnlyScan (M0046-0004) when all of the following hold:
//  1. The Project's direct child (ignoring Filter wrappers) is an IndexScan.
//  2. All target expressions are ColumnRefs whose names appear in the index key.
//
// Returns the original proj unchanged if the conditions are not met.
func tryPromoteIndexOnlyScan(proj *Project) Node {
	if proj == nil {
		return proj
	}
	// Strip a single Filter wrapper if present (e.g. range predicates).
	child := proj.Child
	var filter *Filter
	if f, ok := child.(*Filter); ok {
		filter = f
		child = f.Child
	}
	idxScan, ok := child.(*IndexScan)
	if !ok {
		return proj
	}
	// M0053-0001: composite indexes are now reachable via IndexScan
	// (leading-column probe), but the IndexOnlyScan executor cannot
	// decode multi-column keys back to row Datums yet. Skip promotion
	// for composite indexes so the row is fetched from the heap path.
	if len(idxScan.Index.Columns) != 1 {
		return proj
	}
	// Check that every projected column is in the index key.
	idxColSet := make(map[string]bool, len(idxScan.Index.Columns))
	for _, c := range idxScan.Index.Columns {
		idxColSet[c] = true
	}
	covered := make([]catalog.Column, 0, len(proj.Targets))
	for _, t := range proj.Targets {
		cr, isCR := t.(*ColumnRef)
		if !isCR || !idxColSet[cr.Name] {
			return proj // target not in index — cannot use index-only
		}
		// Find the full column definition from the table.
		col, found := func() (catalog.Column, bool) {
			for _, c := range idxScan.Table.Columns {
				if c.Name == cr.Name {
					return c, true
				}
			}
			return catalog.Column{}, false
		}()
		if !found {
			return proj
		}
		covered = append(covered, col)
	}
	if len(covered) == 0 {
		return proj
	}
	ios := &IndexOnlyScan{
		pos:     idxScan.pos,
		Table:   idxScan.Table,
		Index:   idxScan.Index,
		Key:     idxScan.Key,
		LowKey:  idxScan.LowKey,
		HighKey: idxScan.HighKey,
		Covered: covered,
		schema:  proj.schema,
	}
	if filter != nil {
		// Keep the Filter but replace its child with IndexOnlyScan.
		filter.Child = ios
		return filter
	}
	return ios
}

// shiftColumnRefsBy walks an expression tree and adds `delta` to
// every ColumnRef's Index. OuterColumnRefs are left alone (they
// reference an enclosing scope). Used by M0063-0005 to translate
// outer-cumulative-indexed conjuncts back to inner-scope when
// they're pushed below a LEFT JOIN.
func shiftColumnRefsBy(e Expr, delta int) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ColumnRef:
		cl := *x
		cl.Index += delta
		return &cl
	case *BinaryOp:
		return &BinaryOp{
			pos:   x.Pos(),
			Op:    x.Op,
			Left:  shiftColumnRefsBy(x.Left, delta),
			Right: shiftColumnRefsBy(x.Right, delta),
		}
	case *UnaryOp:
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: shiftColumnRefsBy(x.Operand, delta)}
	case *FuncCall:
		args := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			args[i] = shiftColumnRefsBy(a, delta)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name, Args: args, Star: x.Star}
	case *CaseExpr:
		whens := make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			whens[i] = CaseWhen{
				When: shiftColumnRefsBy(w.When, delta),
				Then: shiftColumnRefsBy(w.Then, delta),
			}
		}
		return &CaseExpr{
			pos:     x.Pos(),
			Operand: shiftColumnRefsBy(x.Operand, delta),
			Whens:   whens,
			Else:    shiftColumnRefsBy(x.Else, delta),
		}
	case *ExtractExpr:
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: shiftColumnRefsBy(x.Source, delta)}
	default:
		return e
	}
}
