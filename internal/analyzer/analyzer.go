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

// orderBySubstitution rewrites an ORDER BY expression to the
// underlying target-list expression when the user wrote a bare
// alias (`ORDER BY revenue`) or a positional index (`ORDER BY 1`).
// Returns expr unchanged when neither rewrite applies. Mirrors
// the planner's resolveOrderBySubstitution so analyzer and
// planner stay in lockstep on what counts as a valid ORDER BY
// reference.
func orderBySubstitution(expr parser.Expr, targets []parser.ResTarget) parser.Expr {
	if ic, ok := expr.(*parser.IntegerConst); ok {
		idx := int(ic.Value) - 1
		if idx >= 0 && idx < len(targets) {
			// Do not substitute when the target is a bare star expansion:
			// `SELECT * FROM t … ORDER BY 1` in a set-op context has the
			// ORDER BY owned by the whole set-op (not the left branch), and
			// targets[0] is a StarExpr that hasn't been expanded yet.
			// Substituting StarExpr here causes analyzeExpr to reject it;
			// keep the integer constant so analysis treats it as a number
			// rather than an unresolved star. M0097-0042.
			if _, isStar := targets[idx].Expr.(*parser.StarExpr); !isStar {
				return targets[idx].Expr
			}
		}
		return expr
	}
	if cr, ok := expr.(*parser.ColumnRef); ok && cr.Schema == "" && cr.Table == "" {
		for _, tgt := range targets {
			if tgt.Alias != "" && strings.EqualFold(tgt.Alias, cr.Column) {
				return tgt.Expr
			}
		}
	}
	return expr
}

// AnalyzeError is a structured analyzer failure with SQLSTATE-style code.
type AnalyzeError struct {
	Pos     int
	Code    string
	Message string
	Hint    string // optional hint; propagated to PlanError.Hint by toPlanError. M0097-0004.
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

func analyzeErrorWithHint(pos int, code, msg, hint string) *AnalyzeError {
	return &AnalyzeError{Pos: pos, Code: code, Message: msg, Hint: hint}
}

// pgTimeName returns the PostgreSQL display name for a time/timestamp type
// as used in "operator is not unique" error messages. M0097-0004.
func pgTimeName(typName string) string {
	switch strings.ToLower(typName) {
	case "time":
		return "time without time zone"
	case "timetz":
		return "time with time zone"
	case "timestamp":
		return "timestamp without time zone"
	case "timestamptz":
		return "timestamp with time zone"
	case "date":
		return "date"
	}
	return typName
}

type scope struct {
	rels []scopeRel
	// parent is the lexical-scope parent for correlated
	// subqueries — set when analyzing a subquery's inner
	// SELECT so column refs that don't resolve locally walk
	// up. nil for the top-level SELECT.
	parent *scope
	// cat is the catalog. Threaded via the scope so the
	// SubqueryExpr / ExistsExpr / InExpr handlers can recurse
	// into analyzeSelectWithParent without re-piping cat as a
	// separate parameter through every helper.
	cat catalog.Catalog
	// ctes maps CTE name → synthetic *catalog.Table built from
	// the inner SELECT's target list. Populated by analyzeWith
	// when a statement carries a WITH clause; consulted by
	// resolveTable before falling through to the catalog. The
	// scope chain is walked head-first so an inner WITH shadows
	// an outer-scope CTE of the same name (mirrors PostgreSQL).
	// nil for pre-M0016 callers — every existing test path goes
	// straight through to lookupTable. See M0016-0001.
	ctes map[string]*catalog.Table
}

type scopeRel struct {
	table *catalog.Table
	alias string
	// qualifiedOnly hides this rel from the unqualified
	// column-resolution path. Used by ON CONFLICT DO UPDATE
	// (M0017-0001 step 2) to wire `excluded` into the scope as a
	// pseudo-table without creating a bare-`col` ambiguity with
	// the target table — `excluded.col` resolves only via the
	// fully-qualified path. Mirrors upstream's name-resolution
	// rule for the EXCLUDED pseudo-relation.
	qualifiedOnly bool
	// blockOriginalName, when set alongside an alias, causes a
	// deferred error when the underlying table name is used as a
	// qualifier — only the alias is valid. The error is deferred
	// (not immediate) so a qualifiedOnly binding with the same name
	// (e.g. the excluded pseudo-table) can still match first.
	blockOriginalName bool
	// usingHidden lists column names hidden from unqualified lookup
	// because they appear in a JOIN USING clause. The left table's
	// copy of these columns is canonical. M0097-0003.
	usingHidden []string
}

// outerScope is the goroutine-thread-unsafe lexical-scope
// channel set by the planner before invoking Analyze on a
// subquery's inner SELECT. Mirrors planner.planParent. nil
// for top-level statements.
var outerScope *scope

// OuterRelation describes one FROM-clause range variable in
// an outer scope. The planner constructs these from its
// resolveContext bindings before recursively planning a
// subquery.
type OuterRelation struct {
	Table *catalog.Table
	Alias string
}

// OuterScope is an opaque handle to a lexical-scope parent
// chain the planner threads into the analyzer for correlated
// subquery analysis. Constructed via NewOuterScope; consumed
// via SetOuterScope.
type OuterScope struct{ inner *scope }

// NewOuterScope builds an OuterScope from a flat list of
// FROM-clause relations and an enclosing scope (or nil for
// the outermost level). The cat is captured so the analyzer
// can recurse into nested subqueries from this level.
func NewOuterScope(cat catalog.Catalog, rels []OuterRelation, parent *OuterScope) *OuterScope {
	rs := make([]scopeRel, len(rels))
	for i, r := range rels {
		rs[i] = scopeRel{table: r.Table, alias: r.Alias}
	}
	s := &scope{rels: rs, cat: cat}
	if parent != nil {
		s.parent = parent.inner
	}
	return &OuterScope{inner: s}
}

// SetOuterScope wires the lexical-scope parent for the next
// Analyze call. Returns a restorer; defer it to guarantee
// cleanup. Mirrors planner.planParent's design.
func SetOuterScope(parent *OuterScope) func() {
	prev := outerScope
	if parent != nil {
		outerScope = parent.inner
	} else {
		outerScope = nil
	}
	return func() { outerScope = prev }
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
		// DDL statements (CreateTableStmt, CreateFunctionStmt,
		// DropFunctionStmt, etc.) flow straight through Plan to the
		// DDL executor. Stage A step 3 dropped the
		// CREATE/DROP FUNCTION rejection that step 1 added — the
		// catalog Routines() registry now backs the executor's
		// CREATE FUNCTION / DROP FUNCTION operators.
		_ = s
		return nil
	}
}

func analyzeSelect(s *parser.SelectStmt, cat catalog.Catalog) error {
	return analyzeSelectWithParent(s, cat, outerScope)
}

// lockingClauseName returns the SQL name of a locking strength (FOR UPDATE, etc.)
// for use in error messages. M0097-0042.
func lockingClauseName(s parser.LockStrength) string {
	switch s {
	case parser.LockStrengthForUpdate:
		return "FOR UPDATE"
	case parser.LockStrengthForNoKeyUpdate:
		return "FOR NO KEY UPDATE"
	case parser.LockStrengthForShare:
		return "FOR SHARE"
	case parser.LockStrengthForKeyShare:
		return "FOR KEY SHARE"
	default:
		return "FOR UPDATE"
	}
}

// analyzeSelectWithParent analyzes a SELECT with the supplied
// scope as lexical-scope parent. Used by SubqueryExpr /
// InExpr / ExistsExpr handlers when recursing into inner
// SELECTs so column refs can resolve against the outer scope.
func analyzeSelectWithParent(s *parser.SelectStmt, cat catalog.Catalog, parent *scope) error {
	// s.Distinct is now supported via the planner's Distinct node. M0097-0005.
	if s.SetOp != nil {
		// FOR UPDATE/NO KEY UPDATE is not allowed on any branch of a set-op.
		// Check the right branch (directly) and the outer left side. M0097-0042.
		if len(s.SetOp.Right.Locking) > 0 {
			lc := s.SetOp.Right.Locking[0]
			return analyzeError(lc.Pos(), "0A000",
				lockingClauseName(lc.Strength)+" is not allowed with UNION/INTERSECT/EXCEPT")
		}
		if len(s.Locking) > 0 {
			lc := s.Locking[0]
			return analyzeError(lc.Pos(), "0A000",
				lockingClauseName(lc.Strength)+" is not allowed with UNION/INTERSECT/EXCEPT")
		}
		// UNION / INTERSECT / EXCEPT (with optional ALL) are all
		// supported. Analyze the right side first (innermost first),
		// then the left side with SetOp temporarily cleared to avoid
		// infinite recursion. M0097-0024.
		//
		// If the outermost set-op statement carries a WITH clause (e.g.
		// `WITH cte AS (...) SELECT … UNION SELECT FROM cte`), the CTE
		// names must be visible to ALL branches. Build a scope with the
		// CTEs registered BEFORE recursing into the branches so that
		// both the right and left sides can reference them. M0097-0047.
		setOpParent := parent
		if s.With != nil {
			ctxForSetOp := &scope{parent: parent, cat: cat}
			if err := analyzeWith(s.With, ctxForSetOp); err != nil {
				return err
			}
			setOpParent = ctxForSetOp
		}
		if err := analyzeSelectWithParent(s.SetOp.Right, cat, setOpParent); err != nil {
			return err
		}
		saved := s.SetOp
		s.SetOp = nil
		savedWith := s.With
		s.With = nil // already processed above; don't re-register in the left-branch recursion
		err := analyzeSelectWithParent(s, cat, setOpParent)
		s.SetOp = saved
		s.With = savedWith
		if err != nil {
			return err
		}
		return nil
	}

	ctx := &scope{parent: parent, cat: cat}
	// CTE definitions are visible to the SELECT body's FROM
	// clause and to subsequent CTEs in the same WITH list. Build
	// the CTE map first so resolveTable can find them when
	// buildSelectScope walks the From list. See
	// docs/design/0016-0001-with-parser-ast-and-name-resolution.md.
	if s.With != nil {
		if err := analyzeWith(s.With, ctx); err != nil {
			return err
		}
	}
	if len(s.From) > 0 {
		rels, err := buildSelectScopeIn(s, ctx)
		if err != nil {
			return err
		}
		ctx.rels = rels
	}

	if err := resolveNamedWindowRefs(s); err != nil {
		return err
	}
	if err := analyzeTargets(s.Targets, ctx); err != nil {
		return err
	}
	if s.Where != nil && exprHasWindowFunc(s.Where) {
		return analyzeError(s.Where.Pos(), "0A000", "window functions are not allowed in WHERE")
	}
	if err := analyzeWhere(s.Where, ctx); err != nil {
		return err
	}
	for _, g := range s.GroupBy {
		// GROUP BY (PG-extension) may reference target-list
		// aliases and positional indices the same way ORDER BY
		// does. TPC-H Q7 leans on this: `extract(year FROM
		// l_shipdate) AS l_year ... GROUP BY l_year`. Run the
		// substitution before type-checking so the alias doesn't
		// trip the undefined-column check.
		expr := orderBySubstitution(g, s.Targets)
		if exprHasWindowFunc(expr) {
			return analyzeError(expr.Pos(), "0A000", "window functions are not allowed in GROUP BY")
		}
		if _, err := analyzeExpr(expr, ctx); err != nil {
			return err
		}
	}
	if s.Having != nil {
		if exprHasWindowFunc(s.Having) {
			return analyzeError(s.Having.Pos(), "0A000", "window functions are not allowed in HAVING")
		}
		typ, err := analyzeExpr(s.Having, ctx)
		if err != nil {
			return err
		}
		if !isBooleanLike(typ) {
			return analyzeError(s.Having.Pos(), "42804", "HAVING condition must be type boolean")
		}
	}
	for _, sb := range s.OrderBy {
		// SQL ORDER BY may reference target-list aliases or
		// positional indices. Substitute the underlying target
		// expression before type-checking so a bare alias like
		// `ORDER BY revenue DESC` (Q3) doesn't trip the
		// undefined-column check.
		expr := orderBySubstitution(sb.Expr, s.Targets)
		if _, err := analyzeExpr(expr, ctx); err != nil {
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
	if len(s.Locking) > 0 {
		if err := analyzeLockingClauses(s, ctx); err != nil {
			return err
		}
	}
	return nil
}

// analyzeLockingClauses validates the parsed FOR UPDATE / FOR
// SHARE tail (M0021-0001 step 2). Mirrors upstream's
// transformLockingClause / preprocess_rowmarks rejection set:
//
//   - Locking is meaningless without a FROM clause — SQLSTATE
//     0A000 ("FOR UPDATE/SHARE is not allowed in this context").
//   - Locking conflicts with aggregation: GROUP BY and HAVING
//     produce grouped rows that don't map back to individual
//     storage tuples. Reject with 0A000.
//   - Each `OF target_name` must resolve to a FROM-clause range
//     variable (alias if present, otherwise table name).
//     Mismatched names surface 42P01 (the canonical
//     "table not found" diagnostic).
//
// Wait-policy modifiers (NOWAIT, SKIP LOCKED) are accepted by
// the analyzer for AST stability across stages — the planner /
// executor narrow the supported runtime subset later.
func analyzeLockingClauses(s *parser.SelectStmt, ctx *scope) error {
	first := s.Locking[0]
	// SKIP LOCKED and WITH TIES cannot be combined. M0097-0042.
	if s.WithTies {
		for _, lc := range s.Locking {
			if lc.WaitPolicy == parser.LockWaitSkipLocked {
				return analyzeError(lc.Pos(), "0A000",
					"SKIP LOCKED and WITH TIES options cannot be used together")
			}
		}
	}
	if len(s.From) == 0 {
		return analyzeError(first.Pos(), "0A000",
			"FOR UPDATE/SHARE is not allowed in this context")
	}
	if len(s.GroupBy) > 0 {
		return analyzeError(first.Pos(), "0A000",
			"FOR UPDATE is not allowed with GROUP BY clause")
	}
	if s.Having != nil {
		return analyzeError(first.Pos(), "0A000",
			"FOR UPDATE/SHARE is not allowed with HAVING clause")
	}
	for _, t := range s.Targets {
		if targetHasBareAggregate(t.Expr) {
			return analyzeError(first.Pos(), "0A000",
				"FOR UPDATE is not allowed with aggregate functions")
		}
	}
	for _, lc := range s.Locking {
		for _, name := range lc.Targets {
			if !lockingTargetMatches(name, ctx.rels) {
				return analyzeError(lc.Pos(), "42P01",
					fmt.Sprintf("relation %q in FOR UPDATE/SHARE clause not found in FROM clause", name))
			}
		}
	}
	return nil
}

// lockingTargetMatches reports whether name matches one of the
// FROM-clause relations: by alias when set, otherwise by table
// name. Case-insensitive — matches scopeRelMatches's identifier
// comparison.
func lockingTargetMatches(name string, rels []scopeRel) bool {
	for _, rel := range rels {
		if rel.qualifiedOnly {
			continue
		}
		if rel.alias != "" {
			if strings.EqualFold(name, rel.alias) {
				return true
			}
			continue
		}
		if strings.EqualFold(name, rel.table.Name) {
			return true
		}
	}
	return false
}

// targetHasBareAggregate reports whether expr contains a call to a
// standard PostgreSQL aggregate function that is not itself a window
// function (OVER-less). Used by analyzeLockingClauses to reject
// `SELECT count(*) FROM t FOR UPDATE`-style queries the way upstream's
// CheckSelectLocking does via qry->hasAggs — mirrors
// parser.exprContainsAggregateCall / planner.isAggregateFunc; kept
// local since the analyzer does not otherwise know "aggregate" as a
// category. M0021-0002.
func targetHasBareAggregate(e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.FuncCall:
		if x.Over == nil && isAnalyzerAggregateName(x.Name.Name) {
			return true
		}
		for _, a := range x.Args {
			if targetHasBareAggregate(a) {
				return true
			}
		}
	case *parser.BinaryOp:
		return targetHasBareAggregate(x.Left) || targetHasBareAggregate(x.Right)
	case *parser.UnaryOp:
		return targetHasBareAggregate(x.Operand)
	case *parser.CastExpr:
		return targetHasBareAggregate(x.Operand)
	case *parser.IsNullExpr:
		return targetHasBareAggregate(x.Operand)
	case *parser.IsBoolExpr:
		return targetHasBareAggregate(x.Operand)
	case *parser.IndirectionStar:
		return targetHasBareAggregate(x.Source)
	}
	return false
}

// isAnalyzerAggregateName mirrors parser.isParserAggregateName /
// planner.isAggregateFunc's standard-aggregate name set.
func isAnalyzerAggregateName(name string) bool {
	switch strings.ToLower(name) {
	case "count", "sum", "avg", "min", "max",
		"var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev",
		"corr", "covar_pop", "covar_samp",
		"regr_count", "regr_sxx", "regr_syy", "regr_sxy",
		"regr_avgx", "regr_avgy", "regr_r2", "regr_slope", "regr_intercept",
		"bool_and", "bool_or", "every",
		"bit_and", "bit_or", "bit_xor",
		"string_agg", "array_agg", "json_agg", "jsonb_agg",
		"json_object_agg", "jsonb_object_agg",
		"xmlagg", "any_value",
		"percentile_cont", "percentile_disc", "mode",
		"rank", "dense_rank", "cume_dist", "percent_rank":
		return true
	}
	return false
}

func analyzeInsert(s *parser.InsertStmt, cat catalog.Catalog) error {
	// Build CTE scope so the INSERT's SELECT body (including subqueries)
	// can reference modifying CTEs by name. M0100-0010.
	var cteCtx *scope
	if s.With != nil {
		cteCtx = &scope{cat: cat}
		if err := analyzeWith(s.With, cteCtx); err != nil {
			return err
		}
	}
	tbl, err := lookupTable(cat, s.Target)
	if err != nil {
		return err
	}
	// INSERT … SELECT: analyze the SELECT sub-statement and skip VALUES checks.
	// Pass cteCtx as parent so the SELECT sees modifying CTE names.
	if s.Select != nil {
		return analyzeSelectWithParent(s.Select, cat, cteCtx)
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
	if s.OnConflict != nil {
		if err := analyzeOnConflict(s.OnConflict, tbl, cat, s.Target.Alias); err != nil {
			return err
		}
	}
	return nil
}

// analyzeOnConflict validates an `ON CONFLICT …` clause against the
// INSERT target's catalog metadata (M0017-0001 step 2). The shapes
// the parser accepts but Stage A doesn't yet support — `ON
// CONSTRAINT name` and `ON CONFLICT DO UPDATE` without a target —
// are rejected here with deterministic SQLSTATE codes so callers
// see the same diagnostic regardless of when feature gating moves
// (Stage B, planner step, etc.).
//
// For `DO UPDATE SET …`, expressions on the RHS of each assignment
// (and the optional `WHERE` predicate) are analyzed against a scope
// that includes both the target table (resolvable bare or by its
// table name / alias) and the `excluded` pseudo-table — the
// inserted-row "view" upstream exposes via the EXCLUDED keyword.
// The pseudo-table is registered as `qualifiedOnly` so bare column
// references continue to resolve unambiguously to the target side.
func analyzeOnConflict(oc *parser.OnConflictClause, tbl *catalog.Table, cat catalog.Catalog, targetAlias string) error {
	// `ON CONFLICT DO UPDATE …` requires a conflict target. Mirrors
	// upstream's "ON CONFLICT DO UPDATE requires inference
	// specification or constraint name" diagnostic (42601 there).
	if oc.Action == parser.OnConflictUpdate && oc.Target == nil {
		return analyzeErrorWithHint(oc.Pos(), "42601",
			"ON CONFLICT DO UPDATE requires inference specification or constraint name",
			"For example, ON CONFLICT (column_name).")
	}

	// Validate the conflict target shape:
	//   - column-list form: each named column must exist on the
	//     target table; the planner picks the unique-arbiter index
	//     in M0017-0002.
	//   - constraint-name form (M0017 Stage B): the named constraint
	//     must exist, must be a unique index, and must belong to
	//     the target table. Mirrors upstream's
	//     "constraint X for table Y does not exist" / "is not a
	//     unique constraint" diagnostics.
	if oc.Target != nil {
		switch {
		case oc.Target.Constraint != "":
			idx, ok := cat.LookupIndex(parser.ObjectName{Name: oc.Target.Constraint})
			if !ok {
				return analyzeError(oc.Target.Pos(), "42704",
					fmt.Sprintf("constraint %q for table %q does not exist", oc.Target.Constraint, tbl.Name))
			}
			if idx.Table != tbl {
				return analyzeError(oc.Target.Pos(), "42704",
					fmt.Sprintf("constraint %q does not belong to table %q", oc.Target.Constraint, tbl.Name))
			}
			if !idx.Unique {
				return analyzeError(oc.Target.Pos(), "42P10",
					fmt.Sprintf("constraint %q is not a unique constraint", oc.Target.Constraint))
			}
		default:
			if len(oc.Target.Columns) == 0 {
				return analyzeError(oc.Target.Pos(), "42601",
					"ON CONFLICT target requires at least one column")
			}
			for _, col := range oc.Target.Columns {
				if col == "" {
					// Expression column (e.g. lower(key)) — existence
					// check deferred to the planner's arbiter-index
					// resolution.
					continue
				}
				if _, ok := lookupColumn(tbl, col); !ok {
					// PG uses the bare "column X does not exist" form
					// for conflict-inference columns (no "of relation").
					ae := analyzeError(oc.Target.Pos(), "42703",
						fmt.Sprintf("column %q does not exist", col))
					// Add a "did you mean" hint suggesting similar column names from
					// both the target table and the excluded pseudo-table (like PG).
					if h := suggestConflictColumnHint(tbl.Columns, tbl.Name, col); h != "" {
						ae.Hint = h
					}
					return ae
				}
			}
		}
	}

	if oc.Action != parser.OnConflictUpdate {
		return nil
	}

	// Build the DO UPDATE scope:
	//   - target table — primary, bare refs resolve here. Carries
	//     the user-provided alias (if any) so `t.col` works for
	//     `INSERT INTO foo AS t … DO UPDATE SET … = t.col`.
	//   - excluded — pseudo-table with the same column shape;
	//     qualifiedOnly so bare refs don't become ambiguous.
	primaryAlias := targetAlias
	if primaryAlias == "" {
		primaryAlias = tbl.Name
	}
	primaryRel := scopeRel{table: tbl, alias: primaryAlias}
	if targetAlias != "" {
		// When the target has an alias the original table name must
		// not qualify the primary rel — only the alias is valid.
		// Deferred (not immediate) so a qualifiedOnly binding with
		// the same name (the excluded pseudo-table) can still match.
		primaryRel.blockOriginalName = true
	}
	ctx := &scope{
		cat: cat,
		rels: []scopeRel{
			primaryRel,
			{table: tbl, alias: "excluded", qualifiedOnly: true},
		},
	}
	for _, assign := range oc.UpdateSet {
		if len(assign.Columns) > 0 {
			// Multi-column tuple form: validate each target column name.
			// RHS expression analysis is deferred to the planner because
			// the RHS may reference CTEs from an outer WITH clause that
			// are not visible in this analyzer scope (they live in the
			// planner's global planCTEs map). The planner validates the
			// RHS via planSelectWithParent / resolveExpr.
			for _, colName := range assign.Columns {
				if _, ok := lookupColumn(tbl, colName); !ok {
					return analyzeError(assign.Pos(), "42703",
						fmt.Sprintf("column %q of relation %q does not exist", colName, tbl.Name))
				}
			}
			continue
		}
		col, ok := lookupColumn(tbl, assign.Column)
		if !ok {
			return analyzeError(assign.Pos(), "42703",
				fmt.Sprintf("column %q of relation %q does not exist", assign.Column, tbl.Name))
		}
		typ, err := analyzeExpr(assign.Expr, ctx)
		if err != nil {
			return err
		}
		if !isAssignable(typ, col.Type) {
			return analyzeError(assign.Expr.Pos(), "42804",
				fmt.Sprintf("column %q has type %q but expression has type %q", col.Name, col.Type.Name, typ.Name))
		}
	}
	if oc.UpdateWhere != nil {
		if err := analyzeWhere(oc.UpdateWhere, ctx); err != nil {
			return err
		}
	}
	return nil
}

func analyzeUpdate(s *parser.UpdateStmt, cat catalog.Catalog) error {
	tbl, err := lookupTable(cat, s.Target)
	if err != nil {
		return err
	}
	ctx := &scope{rels: []scopeRel{{table: tbl, alias: s.Target.Alias}}, cat: cat}
	// Register CTEs first so FROM-clause tables can reference them. M0100-0010.
	if s.With != nil {
		if err := analyzeWith(s.With, ctx); err != nil {
			return err
		}
	}
	// Add FROM-clause tables to scope so WHERE / SET expressions can reference
	// their columns (e.g. `UPDATE t SET i = b.j FROM other b WHERE ...`). M0097-0065.
	// Use resolveTable (CTE-aware) so modifying CTEs in WITH are found.
	for _, rv := range s.From {
		fromTbl, err := resolveTable(ctx, rv)
		if err != nil {
			return err
		}
		alias := rv.Alias
		if alias == "" {
			alias = rv.Name
		}
		ctx.rels = append(ctx.rels, scopeRel{table: fromTbl, alias: alias})
	}
	if err := analyzeWhere(s.Where, ctx); err != nil {
		return err
	}
	for _, assign := range s.Set {
		if len(assign.Columns) > 0 {
			// Multi-column tuple form: (c1, c2, …) = (e1, e2, …).
			// Validate each target column and analyse the RHS expression.
			for _, colName := range assign.Columns {
				if _, ok := lookupColumn(tbl, colName); !ok {
					return analyzeError(assign.Pos(), "42703", fmt.Sprintf("column %q of relation %q does not exist", colName, tbl.Name))
				}
			}
			if _, err := analyzeExpr(assign.Expr, ctx); err != nil {
				return err
			}
			continue
		}
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
	ctx := &scope{rels: []scopeRel{{table: tbl, alias: s.Target.Alias}}, cat: cat}
	// Register CTEs first so USING-clause tables can reference them. M0100-0010.
	if s.With != nil {
		if err := analyzeWith(s.With, ctx); err != nil {
			return err
		}
	}
	// Add USING-clause tables to scope so WHERE / RETURNING expressions
	// can reference their columns. Mirrors analyzeUpdate's FROM handling.
	// M0097-0076. Use resolveTable (CTE-aware) so modifying CTEs in WITH are found.
	for _, rv := range s.Using {
		useTbl, err := resolveTable(ctx, rv)
		if err != nil {
			return err
		}
		alias := rv.Alias
		if alias == "" {
			alias = rv.Name
		}
		ctx.rels = append(ctx.rels, scopeRel{table: useTbl, alias: alias})
	}
	if err := analyzeWhere(s.Where, ctx); err != nil {
		return err
	}
	return analyzeTargets(s.Returning, ctx)
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

// resolveNamedWindowRefs resolves every window reference in s — both a
// bare `OVER window_name` and a combining `OVER (window_name ...)` /
// `WINDOW w2 AS (w1 ...)` form — against s.WindowClause (M0020 named-window
// slice plus M0122-0004's combining-forms follow-up), merging the
// referenced definition into the referencing WindowDef in place via
// mergeWindowDef. s.WindowClause is processed in order so a later entry may
// reference an earlier one (chained named windows); each entry's Def is
// mutated to hold its final, fully-merged PartitionBy/OrderBy/Frame before
// being registered, so a third-level reference sees the resolved values,
// not the raw parsed ones. Downstream consumers (planner, executor) read
// FuncCall.Over.PartitionBy/OrderBy/Frame directly and need no awareness of
// RefName — this is the only place any reference is resolved. Raises 42704
// ("window %q does not exist") for a name with no matching (already
// registered) WINDOW clause item, and 42P20 for a duplicate name or an
// invalid override (see mergeWindowDef).
func resolveNamedWindowRefs(s *parser.SelectStmt) error {
	defs := make(map[string]*parser.WindowDef, len(s.WindowClause))
	for _, nw := range s.WindowClause {
		lname := strings.ToLower(nw.Name)
		if _, dup := defs[lname]; dup {
			return analyzeError(nw.Def.Pos(), "42P20", fmt.Sprintf("window %q is already defined", nw.Name))
		}
		if nw.Def.RefName != "" {
			// A WINDOW-clause entry may only reference an EARLIER entry in
			// the same clause (mirrors parse_clause.c's transformWindowDefinitions,
			// which looks up refname in the windows already processed) — a
			// forward or self reference reports the same "does not exist"
			// a genuinely undefined name would.
			refwc, ok := defs[strings.ToLower(nw.Def.RefName)]
			if !ok {
				return analyzeError(nw.Def.Pos(), "42704", fmt.Sprintf("window %q does not exist", nw.Def.RefName))
			}
			if err := mergeWindowDef(nw.Def, refwc, true); err != nil {
				return err
			}
		}
		defs[lname] = nw.Def
	}
	for _, rt := range s.Targets {
		if err := resolveWindowRefsInExpr(rt.Expr, defs); err != nil {
			return err
		}
	}
	for _, g := range s.GroupBy {
		if err := resolveWindowRefsInExpr(g, defs); err != nil {
			return err
		}
	}
	if s.Having != nil {
		if err := resolveWindowRefsInExpr(s.Having, defs); err != nil {
			return err
		}
	}
	for _, sb := range s.OrderBy {
		if err := resolveWindowRefsInExpr(sb.Expr, defs); err != nil {
			return err
		}
	}
	return nil
}

// mergeWindowDef applies windef's reference to refwc (SQL:2008 7.11 <window
// clause> syntax rule 10 / general rule 1, parse_clause.c's
// transformWindowDefinitions): windef inherits refwc's PARTITION BY outright
// — declaring its own is a hard error — may add its own ORDER BY only if
// refwc has none, and always keeps its own frame clause (never inherited),
// but refwc having any non-default frame clause is itself an error even
// though windef isn't asking to inherit it (SQL:2008's asymmetric windowing
// rule: "OVER foo" must throw if foo has a frame clause). isNamedWindow
// distinguishes a `WINDOW name AS (...)` entry from an inline `OVER (...)`
// clause, matching upstream's differing wording/hint for the latter's bare
// `OVER (foo)` shape.
func mergeWindowDef(windef *parser.WindowDef, refwc *parser.WindowDef, isNamedWindow bool) error {
	ownOrderBy := len(windef.OrderBy) > 0
	ownFrame := windef.Frame != nil
	if len(windef.PartitionBy) > 0 {
		return analyzeError(windef.Pos(), "42P20",
			fmt.Sprintf("cannot override PARTITION BY clause of window %q", windef.RefName))
	}
	windef.PartitionBy = refwc.PartitionBy
	if ownOrderBy {
		if len(refwc.OrderBy) > 0 {
			return analyzeError(windef.Pos(), "42P20",
				fmt.Sprintf("cannot override ORDER BY clause of window %q", windef.RefName))
		}
	} else {
		windef.OrderBy = refwc.OrderBy
	}
	if refwc.Frame != nil {
		msg := fmt.Sprintf("cannot copy window %q because it has a frame clause", windef.RefName)
		if isNamedWindow || ownOrderBy || ownFrame {
			return analyzeError(windef.Pos(), "42P20", msg)
		}
		return analyzeErrorWithHint(windef.Pos(), "42P20", msg, "Omit the parentheses in this OVER clause.")
	}
	return nil
}

// resolveWindowRefsInExpr mirrors exprHasWindowFunc's traversal shape
// (pattern_sibling_paths_must_agree) but resolves a bare `OVER name`
// reference against defs instead of merely detecting a window func's
// presence.
func resolveWindowRefsInExpr(e parser.Expr, defs map[string]*parser.WindowDef) error {
	switch x := e.(type) {
	case *parser.BinaryOp:
		if err := resolveWindowRefsInExpr(x.Left, defs); err != nil {
			return err
		}
		return resolveWindowRefsInExpr(x.Right, defs)
	case *parser.UnaryOp:
		return resolveWindowRefsInExpr(x.Operand, defs)
	case *parser.CastExpr:
		return resolveWindowRefsInExpr(x.Operand, defs)
	case *parser.ExtractExpr:
		return resolveWindowRefsInExpr(x.Source, defs)
	case *parser.CaseExpr:
		if x.Operand != nil {
			if err := resolveWindowRefsInExpr(x.Operand, defs); err != nil {
				return err
			}
		}
		for _, w := range x.Whens {
			if err := resolveWindowRefsInExpr(w.When, defs); err != nil {
				return err
			}
			if err := resolveWindowRefsInExpr(w.Then, defs); err != nil {
				return err
			}
		}
		if x.Else != nil {
			return resolveWindowRefsInExpr(x.Else, defs)
		}
		return nil
	case *parser.IsNullExpr:
		return resolveWindowRefsInExpr(x.Operand, defs)
	case *parser.IsBoolExpr:
		return resolveWindowRefsInExpr(x.Operand, defs)
	case *parser.CollateExpr:
		return resolveWindowRefsInExpr(x.Operand, defs)
	case *parser.IsDistinctFromExpr:
		if err := resolveWindowRefsInExpr(x.Left, defs); err != nil {
			return err
		}
		return resolveWindowRefsInExpr(x.Right, defs)
	case *parser.InExpr:
		if err := resolveWindowRefsInExpr(x.Operand, defs); err != nil {
			return err
		}
		for _, v := range x.List {
			if err := resolveWindowRefsInExpr(v, defs); err != nil {
				return err
			}
		}
		return nil
	case *parser.FuncCall:
		if x.Over != nil && x.Over.RefName != "" {
			def, ok := defs[strings.ToLower(x.Over.RefName)]
			if !ok {
				return analyzeError(x.Over.Pos(), "42704", fmt.Sprintf("window %q does not exist", x.Over.RefName))
			}
			if x.Over.IsBareRef {
				// Bare `OVER name` (no parens) is a transparent alias —
				// parser guarantees no own PartitionBy/OrderBy/Frame, so
				// this reuses def's fully-merged fields wholesale with none
				// of mergeWindowDef's override validation (matches
				// parse_agg.c's transformWindowFuncCall bare-name lookup,
				// which never calls transformWindowDefinitions for this
				// case at all).
				x.Over.PartitionBy = def.PartitionBy
				x.Over.OrderBy = def.OrderBy
				x.Over.Frame = def.Frame
			} else if err := mergeWindowDef(x.Over, def, false); err != nil {
				return err
			}
		}
		for _, a := range x.Args {
			if err := resolveWindowRefsInExpr(a, defs); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func exprHasWindowFunc(e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.BinaryOp:
		return exprHasWindowFunc(x.Left) || exprHasWindowFunc(x.Right)
	case *parser.UnaryOp:
		return exprHasWindowFunc(x.Operand)
	case *parser.CastExpr:
		return exprHasWindowFunc(x.Operand)
	case *parser.ExtractExpr:
		return exprHasWindowFunc(x.Source)
	case *parser.CaseExpr:
		if x.Operand != nil && exprHasWindowFunc(x.Operand) {
			return true
		}
		for _, w := range x.Whens {
			if exprHasWindowFunc(w.When) || exprHasWindowFunc(w.Then) {
				return true
			}
		}
		if x.Else != nil && exprHasWindowFunc(x.Else) {
			return true
		}
		return false
	case *parser.IsNullExpr:
		return exprHasWindowFunc(x.Operand)
	case *parser.IsBoolExpr:
		return exprHasWindowFunc(x.Operand)
	case *parser.CollateExpr:
		return exprHasWindowFunc(x.Operand)
	case *parser.IsDistinctFromExpr:
		return exprHasWindowFunc(x.Left) || exprHasWindowFunc(x.Right)
	case *parser.InExpr:
		if exprHasWindowFunc(x.Operand) {
			return true
		}
		for _, v := range x.List {
			if exprHasWindowFunc(v) {
				return true
			}
		}
		return false
	case *parser.FuncCall:
		if x.Over != nil {
			return true
		}
		for _, a := range x.Args {
			if exprHasWindowFunc(a) {
				return true
			}
		}
		return false
	default:
		return false
	}
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
	case *parser.NumericConst:
		return catalog.Type{Name: "numeric"}, nil
	case *parser.StringConst:
		// A bare string literal has no type of its own — PostgreSQL types it
		// as the `unknown` pseudo-type (UNKNOWNOID) and resolves it against the
		// context (the other operand of a comparison, the target column of an
		// assignment, a function's parameter type). Mirror that here instead of
		// hard-typing it as `text`, so e.g. `bigint_col = '1'` type-checks: the
		// `unknown` short-circuits in isComparable / isAssignable let the literal
		// coerce to the concrete side, and the runtime (promoteCrossKind /
		// tryParseStringAs) parses the string into that type. Genuine `text`
		// columns stay `text`, so real mismatches like `text_col = 1` still error.
		// Upstream: postgres/src/backend/parser/parse_coerce.c (coerce_type of
		// UNKNOWNOID) and parse_oper.c (oper_select_candidate). Matches the
		// existing NullConst/ParamRef/CastExpr cases, which already return unknown.
		return catalog.Type{Name: "unknown"}, nil
	case *parser.TypedStringLit:
		return catalog.Type{Name: x.Type}, nil
	case *parser.IntervalLit:
		return catalog.Type{Name: "interval"}, nil
	case *parser.ExtractExpr:
		// EXTRACT(field FROM ts) returns numeric upstream
		// (NUMERIC for fractional seconds, integer otherwise).
		// v0 returns int8 — covers year/month/day/etc.; the
		// fractional-second fields (second/millisecond) are
		// listed as deferred in the design doc.
		if _, err := analyzeExpr(x.Source, ctx); err != nil {
			return catalog.Type{}, err
		}
		return catalog.Type{Name: "int8"}, nil
	case *parser.GroupingCall:
		// GROUPING(a, b, ...) — its value depends only on which grouping
		// set produced the current row (resolved to a literal by the
		// planner's grouping-sets rewrite, M0122-0004), but the args still
		// need scope resolution here so an unknown-column reference is
		// caught the same way it would be for a plain SELECT-list column.
		for _, a := range x.Args {
			if _, err := analyzeExpr(a, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "int4"}, nil
	case *parser.CaseExpr:
		return analyzeCaseExpr(x, ctx)
	case *parser.CollateExpr:
		// CollateExpr is a pass-through; analyze operand for correlated refs. M0097-0127.
		return analyzeExpr(x.Operand, ctx)
	case *parser.ArraySubqueryExpr:
		// Analyze inner SELECT for correlated refs; result is text[]. M0097-0127.
		if ctx != nil && ctx.cat != nil {
			if err := analyzeSelectWithParent(x.Inner, ctx.cat, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "text[]"}, nil
	case *parser.SubqueryExpr:
		// Recursively analyze the inner SELECT with the current
		// scope as lexical parent so correlated column refs
		// (`o.col` from outer scope) resolve through the
		// scope-chain walker. Result is `unknown` — real
		// type-inference for scalar subqueries waits on the
		// type system. cat is required.
		if ctx != nil && ctx.cat != nil {
			if err := analyzeSelectWithParent(x.Inner, ctx.cat, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "unknown"}, nil
	case *parser.InExpr:
		// Operand and list/subquery are checked at planner / runtime
		// rather than here — IN's type-coercion rules are
		// upstream's "anyelement = anyelement" generic. v0 just
		// validates each side's expression sub-tree resolves and
		// reports bool.
		if _, err := analyzeExpr(x.Operand, ctx); err != nil {
			return catalog.Type{}, err
		}
		for _, e := range x.List {
			if _, err := analyzeExpr(e, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		if x.Subquery != nil && ctx != nil && ctx.cat != nil {
			if err := analyzeSelectWithParent(x.Subquery, ctx.cat, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "bool"}, nil
	case *parser.ExistsExpr:
		if x.Subquery != nil && ctx != nil && ctx.cat != nil {
			if err := analyzeSelectWithParent(x.Subquery, ctx.cat, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "bool"}, nil
	case *parser.IsNullExpr:
		// IS [NOT] NULL always returns bool. Recurse into operand to catch errors.
		if _, err := analyzeExpr(x.Operand, ctx); err != nil {
			return catalog.Type{}, err
		}
		return catalog.Type{Name: "bool"}, nil
	case *parser.IsBoolExpr:
		// IS [NOT] TRUE/FALSE/UNKNOWN always returns bool. M0097-0003.
		if _, err := analyzeExpr(x.Operand, ctx); err != nil {
			return catalog.Type{}, err
		}
		return catalog.Type{Name: "bool"}, nil
	case *parser.IsDistinctFromExpr:
		// IS [NOT] DISTINCT FROM always returns bool (null-safe equality).
		if _, err := analyzeExpr(x.Left, ctx); err != nil {
			return catalog.Type{}, err
		}
		if _, err := analyzeExpr(x.Right, ctx); err != nil {
			return catalog.Type{}, err
		}
		return catalog.Type{Name: "bool"}, nil
	case *parser.NullConst:
		return catalog.Type{Name: "unknown"}, nil
	case *parser.BooleanConst:
		return catalog.Type{Name: "bool"}, nil
	case *parser.ParamRef:
		return catalog.Type{Name: "unknown"}, nil
	case *parser.ColumnRef:
		return resolveColumnRefType(x, ctx)
	case *parser.StarExpr:
		if x.Table == "" && x.Schema == "" {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", "'*' is not allowed here")
		}
		// Table-qualified t.* is a whole-row reference; type resolution
		// is deferred to the planner's expandQualifiedStarToRowExpr.
		return catalog.Type{Name: "record"}, nil
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
		switch x.Op {
		case parser.OpUnaryNeg:
			// Unary minus accepts numeric operands and also `interval`
			// (interval_um, unimplemented_feat #5(d-iv)); PG has no unary
			// `+ interval` operator, so OpUnaryPos below stays numeric-only.
			if !isNumericLike(opTyp) && !strings.EqualFold(opTyp.Name, "interval") {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires a numeric operand", x.Op))
			}
			return opTyp, nil
		case parser.OpUnaryPos:
			if !isNumericLike(opTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires a numeric operand", x.Op))
			}
			return opTyp, nil
		case parser.OpNot:
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
		switch x.Op {
		case parser.OpAnd, parser.OpOr:
			if !isBooleanLike(leftTyp) || !isBooleanLike(rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires boolean operands", x.Op))
			}
			return catalog.Type{Name: "bool"}, nil
		case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
			if !isComparable(leftTyp, rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s has incompatible operand types %q and %q", x.Op, leftTyp.Name, rightTyp.Name))
			}
			return catalog.Type{Name: "bool"}, nil
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod:
			// timestamp/date ± interval → timestamp. TPC-H Q1
			// (`l_shipdate <= date '...' - interval '90' day`)
			// hits this; v0 returns the result as `timestamp`
			// since date is internally a timestamp.
			if (x.Op == parser.OpAdd || x.Op == parser.OpSub) &&
				isTimestampLike(leftTyp) && strings.EqualFold(rightTyp.Name, "interval") {
				return catalog.Type{Name: "timestamp"}, nil
			}
			if x.Op == parser.OpAdd &&
				strings.EqualFold(leftTyp.Name, "interval") && isTimestampLike(rightTyp) {
				return catalog.Type{Name: "timestamp"}, nil
			}
			// time + time / timestamp + timestamp → operator error.
			// timetz+timetz: "operator does not exist" (42883) — no such operator in PG.
			// time+time / etc.: "operator is not unique" (42725) — multiple candidates.
			// Note: only trigger when BOTH sides are concrete time/timestamp types
			// (not "unknown", which covers untyped string literals).
			if (x.Op == parser.OpAdd || x.Op == parser.OpSub) &&
				isConcreteTimestampLike(leftTyp) && isConcreteTimestampLike(rightTyp) {
				lname := pgTimeName(leftTyp.Name)
				rname := pgTimeName(rightTyp.Name)
				// timetz has no + or - operator at all (upstream defines neither),
				// so both directions raise "operator does not exist" (42883).
				if strings.EqualFold(leftTyp.Name, "timetz") || strings.EqualFold(rightTyp.Name, "timetz") {
					ae := analyzeError(x.Pos(), "42883",
						fmt.Sprintf("operator does not exist: %s %s %s", lname, x.Op, rname))
					ae.Hint = "No operator matches the given name and argument types. You might need to add explicit type casts."
					return catalog.Type{}, ae
				}
				// Subtraction of two temporal values → interval (timestamp_mi /
				// time_mi). goopg represents DATE internally as a timestamp, so
				// date − date also yields an interval here rather than upstream's
				// integer day count (date_mi) — a deliberate, documented
				// divergence deferred to the type system (see deferral_ledger.md).
				// Executor: subTimeTime in internal/executor/expr.go.
				if x.Op == parser.OpSub {
					return catalog.Type{Name: "interval"}, nil
				}
				// Addition of two temporal values is not defined in PG:
				// "operator is not unique" (42725) — multiple candidates.
				ae := analyzeError(x.Pos(), "42725",
					fmt.Sprintf("operator is not unique: %s %s %s", lname, x.Op, rname))
				ae.Hint = "Could not choose a best candidate operator. You might need to add explicit type casts."
				return catalog.Type{}, ae
			}
			// pg_lsn arithmetic: pg_lsn - pg_lsn → int8;
			// pg_lsn +/- numeric/int* → pg_lsn. M0097-pg_lsn.
			isPgLSN := func(t catalog.Type) bool {
				return strings.EqualFold(t.Name, "pg_lsn")
			}
			if isPgLSN(leftTyp) && isPgLSN(rightTyp) && x.Op == parser.OpSub {
				return catalog.Type{Name: "int8"}, nil
			}
			if isPgLSN(leftTyp) && (isNumericLike(rightTyp) || isUnknownType(rightTyp)) &&
				(x.Op == parser.OpAdd || x.Op == parser.OpSub) {
				return catalog.Type{Name: "pg_lsn"}, nil
			}
			if isPgLSN(rightTyp) && (isNumericLike(leftTyp) || isUnknownType(leftTyp)) &&
				x.Op == parser.OpAdd {
				return catalog.Type{Name: "pg_lsn"}, nil
			}
			// interval ± interval → interval (interval_pl / interval_mi).
			// Executor: addIntervalInterval in internal/executor/expr.go.
			if strings.EqualFold(leftTyp.Name, "interval") && strings.EqualFold(rightTyp.Name, "interval") &&
				(x.Op == parser.OpAdd || x.Op == parser.OpSub) {
				return catalog.Type{Name: "interval"}, nil
			}
			if !isNumericLike(leftTyp) || !isNumericLike(rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires numeric operands", x.Op))
			}
			// Return the wider type per the coercion lattice
			// (int2→int4→int8→numeric→float4→float8), so that
			// `int8 + numeric` correctly reports numeric, not int8.
			if promoted := PromoteNumericType(leftTyp, rightTyp); promoted.Name != "" {
				return promoted, nil
			}
			if !isUnknownType(leftTyp) {
				return leftTyp, nil
			}
			return rightTyp, nil
		case parser.OpConcat:
			// Require at least one string-like (or unknown) operand.
			// When one side is non-string but the other is string-like,
			// PostgreSQL implicitly casts the non-string side to text
			// (e.g. `1 || '/'`). If both sides are non-string, error.
			// Match PG's "operator does not exist: TYPE || TYPE" format. M0097-0063.
			leftStr := isStringLike(leftTyp) || isUnknownType(leftTyp)
			rightStr := isStringLike(rightTyp) || isUnknownType(rightTyp)
			if !leftStr && !rightStr {
				return catalog.Type{}, analyzeError(x.Pos(), "42883",
					fmt.Sprintf("operator does not exist: %s || %s",
						pgDisplayTypeName(leftTyp.Name), pgDisplayTypeName(rightTyp.Name)))
			}
			return catalog.Type{Name: "text"}, nil
		case parser.OpLike, parser.OpNotLike:
			// Both operands must be string-like (text/varchar/char/
			// bpchar/unknown). Pattern can be a literal or column.
			if !isStringLike(leftTyp) || !isStringLike(rightTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", fmt.Sprintf("operator %s requires string operands", x.Op))
			}
			return catalog.Type{Name: "bool"}, nil
		default:
			return catalog.Type{Name: "unknown"}, nil
		}
	case *parser.FuncCall:
		if x.Over != nil {
			return analyzeWindowFuncCall(x, ctx)
		}
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
			// Skip argument count check when WITHIN GROUP is present;
			// the planner validates ordered-set aggregate usage. M0097-0035.
			if (x.Star || len(x.Args) != 1) && len(x.WithinGroup) == 0 {
				return catalog.Type{}, analyzeError(x.Pos(), "42601", "sum() requires exactly one argument")
			}
			if len(x.WithinGroup) > 0 {
				return catalog.Type{Name: "unknown"}, nil
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
	case *parser.ArraySubscriptExpr:
		// Array element access: expr[index] → element type. For text[] the element is text.
		// When base type is unknown (e.g. a parameter $N), return "unknown" so arithmetic
		// on the subscript passes the type check and is resolved at runtime. M0097-0022.
		baseTyp, err := analyzeExpr(x.Base, ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		if _, err := analyzeExpr(x.Index, ctx); err != nil {
			return catalog.Type{}, err
		}
		// Infer element type from array type (strip trailing []).
		if strings.HasSuffix(baseTyp.Name, "[]") {
			return catalog.Type{Name: baseTyp.Name[:len(baseTyp.Name)-2]}, nil
		}
		// Unknown base type → return unknown so arithmetic proceeds.
		if baseTyp.Name == "" || strings.EqualFold(baseTyp.Name, "unknown") {
			return catalog.Type{Name: "unknown"}, nil
		}
		// Vector types: subscript returns the scalar element type.
		switch strings.ToLower(baseTyp.Name) {
		case "int2vector":
			return catalog.Type{Name: "int2"}, nil
		case "oidvector":
			return catalog.Type{Name: "oid"}, nil
		case "point":
			// point[i] (0-based) returns the i-th coordinate as float8
			// (point[0]=x, point[1]=y) — PostgreSQL geometric subscripting.
			return catalog.Type{Name: "float8"}, nil
		}
		return catalog.Type{Name: "text"}, nil
	case *parser.IndirectionStar:
		// `(expr).*` — record/composite star expansion. The planner
		// rewrites this into a FROM-clause SRF; the analyzer only
		// needs to type-walk the source so downstream errors surface
		// here. Returns a synthetic "record" type. M0103-0008.
		if _, err := analyzeExpr(x.Source, ctx); err != nil {
			return catalog.Type{}, err
		}
		return catalog.Type{Name: "record"}, nil
	case *parser.RowExpr:
		// Row constructor (a, b, c): validate each element and return text.
		// Used in `(a,b) IN (VALUES ...)` expansion. M0097-0020.
		for _, el := range x.Elems {
			if _, err := analyzeExpr(el, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "text"}, nil
	case *parser.ArrayConstructorExpr:
		// ARRAY[e1, e2, ...] constructor — walk each element for analysis
		// errors and return a generic text[] type. The planner's resolveExpr
		// converts this to FuncCall{Name:"array_construct"} which the executor
		// evaluates as a {v1,v2,...} text representation. M0097-0065.
		for _, el := range x.Elements {
			if _, err := analyzeExpr(el, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		return catalog.Type{Name: "text[]"}, nil
	default:
		return catalog.Type{}, analyzeError(e.Pos(), "0A000", fmt.Sprintf("unsupported expression %T", e))
	}
}

func analyzeWindowFuncCall(x *parser.FuncCall, ctx *scope) (catalog.Type, error) {
	name := strings.ToLower(x.Name.Name)
	var retType catalog.Type
	switch name {
	case "row_number", "rank", "dense_rank":
		if x.Star || x.Distinct || len(x.Args) != 0 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("window function %s() does not accept arguments, DISTINCT, or * in v0", name))
		}
		retType = catalog.Type{Name: "int8"}
	case "lag", "lead":
		if x.Star || x.Distinct {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("window function %s() does not accept DISTINCT or * in v0", name))
		}
		if len(x.Args) < 1 || len(x.Args) > 3 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("window function %s() requires 1 to 3 arguments", name))
		}
		valueTyp, err := analyzeExpr(x.Args[0], ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		retType = valueTyp
		if len(x.Args) >= 2 {
			if _, err := analyzeExpr(x.Args[1], ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		if len(x.Args) >= 3 {
			if _, err := analyzeExpr(x.Args[2], ctx); err != nil {
				return catalog.Type{}, err
			}
		}
	case "sum", "count", "avg", "min", "max":
		// DISTINCT / ORDER BY within the aggregate's argument list are
		// real PostgreSQL restrictions on aggregate window functions, not
		// a v0 gap — see parse_func.c's transformAggregateCall for the
		// exact wording/errcode this mirrors.
		if x.Distinct {
			return catalog.Type{}, analyzeError(x.Pos(), "0A000", "DISTINCT is not implemented for window functions")
		}
		if len(x.OrderBy) > 0 {
			return catalog.Type{}, analyzeError(x.Pos(), "0A000", "aggregate ORDER BY is not implemented for window functions")
		}
		if x.Filter != nil {
			if _, err := analyzeExpr(x.Filter, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		if x.Star {
			if name != "count" {
				return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("%s(*) is not supported", name))
			}
			retType = catalog.Type{Name: "int8"}
			break
		}
		if len(x.Args) != 1 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("%s() requires exactly one argument", name))
		}
		argTyp, err := analyzeExpr(x.Args[0], ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		switch name {
		case "count":
			retType = catalog.Type{Name: "int8"}
		case "sum":
			if !isNumericLike(argTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", "sum() argument must be numeric")
			}
			if argTyp.Name == "unknown" {
				retType = catalog.Type{Name: "int8"}
			} else {
				retType = argTyp
			}
		case "avg":
			if !isNumericLike(argTyp) {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", "avg() argument must be numeric")
			}
			switch strings.ToLower(argTyp.Name) {
			case "float4", "float8", "real", "double precision", "double", "float":
				retType = catalog.Type{Name: "float8"}
			default:
				retType = catalog.Type{Name: "numeric"}
			}
		case "min", "max":
			retType = argTyp
		}
	case "first_value", "last_value":
		if x.Star || x.Distinct || len(x.Args) != 1 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("window function %s() requires exactly one argument", name))
		}
		valueTyp, err := analyzeExpr(x.Args[0], ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		retType = valueTyp
	case "nth_value":
		if x.Star || x.Distinct || len(x.Args) != 2 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", "window function nth_value() requires exactly two arguments")
		}
		valueTyp, err := analyzeExpr(x.Args[0], ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		if _, err := analyzeExpr(x.Args[1], ctx); err != nil {
			return catalog.Type{}, err
		}
		retType = valueTyp
	case "cume_dist", "percent_rank":
		if x.Star || x.Distinct || len(x.Args) != 0 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", fmt.Sprintf("window function %s() does not accept arguments, DISTINCT, or * in v0", name))
		}
		retType = catalog.Type{Name: "float8"}
	case "ntile":
		if x.Star || x.Distinct || len(x.Args) != 1 {
			return catalog.Type{}, analyzeError(x.Pos(), "42601", "window function ntile() requires exactly one argument")
		}
		if _, err := analyzeExpr(x.Args[0], ctx); err != nil {
			return catalog.Type{}, err
		}
		retType = catalog.Type{Name: "int4"}
	default:
		return catalog.Type{}, analyzeError(x.Pos(), "0A000", fmt.Sprintf("window function %q is not supported in v0 analyzer", name))
	}
	for _, pe := range x.Over.PartitionBy {
		if exprHasWindowFunc(pe) {
			return catalog.Type{}, analyzeError(pe.Pos(), "0A000", "nested window functions are not supported")
		}
		if _, err := analyzeExpr(pe, ctx); err != nil {
			return catalog.Type{}, err
		}
	}
	for _, ob := range x.Over.OrderBy {
		if exprHasWindowFunc(ob.Expr) {
			return catalog.Type{}, analyzeError(ob.Expr.Pos(), "0A000", "nested window functions are not supported")
		}
		if _, err := analyzeExpr(ob.Expr, ctx); err != nil {
			return catalog.Type{}, err
		}
	}
	if err := validateWindowFrame(x.Over.Frame, x.Over.Pos(), len(x.Over.OrderBy), ctx); err != nil {
		return catalog.Type{}, err
	}
	return retType, nil
}

// validateWindowFrame validates a parsed window frame clause's mode
// and bound ordering (SQL:2003 <window frame clause>), mirroring
// gram.y's frame_extent/frame_bound reduce-time checks — all
// ERRCODE_WINDOWING_ERROR (42P20) — plus this v0's RANGE-with-offset
// scope limitation (0A000; ROWS, GROUPS, and RANGE with only
// UNBOUNDED/CURRENT ROW bounds are implemented, see
// internal/executor/operators_window.go). Returns nil for a nil frame
// (no explicit frame clause was written — the default frame applies).
// Also type-checks (but does not range-check) any offset expressions;
// the executor mirrors LIMIT's pattern of range/null-checking a
// once-evaluated constant expression at runtime (22004/22013), since
// an offset can't be validated until it's evaluated.
func validateWindowFrame(fr *parser.WindowFrame, pos int, orderByLen int, ctx *scope) error {
	if fr == nil {
		return nil
	}
	switch fr.Mode {
	case parser.FrameModeRows:
		// No additional mode-specific restriction.
	case parser.FrameModeGroups:
		// Per spec (and gram.y's post-parse check in parse_clause.c),
		// GROUPS mode requires an ORDER BY clause in the window
		// definition — its frame bounds are counted in ORDER BY peer
		// groups, which are undefined without one.
		if orderByLen == 0 {
			return analyzeError(pos, "42P20", "GROUPS mode requires an ORDER BY clause")
		}
	case parser.FrameModeRange:
		// RANGE with a value offset bound (RANGE BETWEEN n PRECEDING /
		// FOLLOWING) compares the ORDER BY column value against
		// value±offset, which needs type-aware +/-/< operator lookup on
		// the single ORDER BY column (still deferred — see the ledger).
		// RANGE with only UNBOUNDED and CURRENT ROW bounds is purely
		// peer-based (CURRENT ROW means "the current row and all its
		// ORDER BY peers"), identical to the default frame's semantics
		// and to GROUPS mode's non-offset behavior, so it is supported.
		if fr.StartKind == parser.FrameBoundOffsetPreceding || fr.StartKind == parser.FrameBoundOffsetFollowing ||
			fr.EndKind == parser.FrameBoundOffsetPreceding || fr.EndKind == parser.FrameBoundOffsetFollowing {
			// A RANGE value offset bound (RANGE BETWEEN n PRECEDING /
			// FOLLOWING) compares the ORDER BY column value against
			// value±offset, so the comparison target must be unambiguous:
			// exactly one ORDER BY column is required. parse_clause.c's
			// transformFrameOffset enforces the same check (42P20).
			if orderByLen != 1 {
				return analyzeError(pos, "42P20", "RANGE with offset PRECEDING/FOLLOWING requires exactly one ORDER BY column")
			}
		}
	default:
		return analyzeError(pos, "0A000", "unsupported window frame mode")
	}
	if fr.StartKind == parser.FrameBoundUnboundedFollowing {
		return analyzeError(pos, "42P20", "frame start cannot be UNBOUNDED FOLLOWING")
	}
	if fr.HasBetween {
		if fr.EndKind == parser.FrameBoundUnboundedPreceding {
			return analyzeError(pos, "42P20", "frame end cannot be UNBOUNDED PRECEDING")
		}
		if fr.StartKind == parser.FrameBoundCurrentRow && fr.EndKind == parser.FrameBoundOffsetPreceding {
			return analyzeError(pos, "42P20", "frame starting from current row cannot have preceding rows")
		}
		if fr.StartKind == parser.FrameBoundOffsetFollowing && (fr.EndKind == parser.FrameBoundOffsetPreceding || fr.EndKind == parser.FrameBoundCurrentRow) {
			return analyzeError(pos, "42P20", "frame starting from following row cannot have preceding rows")
		}
	} else if fr.StartKind == parser.FrameBoundOffsetFollowing {
		return analyzeError(pos, "42P20", "frame starting from following row cannot end with current row")
	}
	if fr.StartOffset != nil {
		if _, err := analyzeExpr(fr.StartOffset, ctx); err != nil {
			return err
		}
	}
	if fr.EndOffset != nil {
		if _, err := analyzeExpr(fr.EndOffset, ctx); err != nil {
			return err
		}
	}
	return nil
}

func resolveColumnRefType(x *parser.ColumnRef, ctx *scope) (catalog.Type, error) {
	if ctx == nil {
		return catalog.Type{}, analyzeError(x.Pos(), "42703", fmt.Sprintf("column %q does not exist", x.Column))
	}
	// Walk lexical scopes — local first, then parent chain
	// for correlated subqueries.
	for cur := ctx; cur != nil; cur = cur.parent {
		ty, ok, err := resolveColumnRefTypeAt(x, cur)
		if err != nil {
			return catalog.Type{}, err
		}
		if ok {
			return ty, nil
		}
	}
	if x.Table != "" {
		return catalog.Type{}, analyzeError(x.Pos(), "42P01", fmt.Sprintf("missing FROM-clause entry for table %q", x.Table))
	}
	return catalog.Type{}, analyzeError(x.Pos(), "42703", fmt.Sprintf("column %q does not exist", x.Column))
}

// resolveColumnRefTypeAt tries to resolve x at a single scope
// level. Returns (type, true, nil) on hit, (_, false, nil) on
// miss (caller walks up), or an error for in-scope ambiguity /
// missing-column-on-matched-relation.
func resolveColumnRefTypeAt(x *parser.ColumnRef, ctx *scope) (catalog.Type, bool, error) {
	if len(ctx.rels) == 0 {
		return catalog.Type{}, false, nil
	}
	if x.Table != "" || x.Schema != "" {
		matches := make([]scopeRel, 0, 1)
		var deferredBlockErr error
		for _, rel := range ctx.rels {
			if rel.qualifiedOnly {
				// Pseudo-tables (e.g. ON CONFLICT's
				// `excluded`) reach name resolution
				// only via their alias — never via the
				// underlying table's catalog name —
				// because the alias is the user-facing
				// identity. Otherwise a target table
				// named `t` plus an `excluded` rel
				// pointing at the same `*catalog.Table`
				// would make `t.col` ambiguous.
				if rel.alias != "" && strings.EqualFold(x.Table, rel.alias) &&
					(x.Schema == "" || strings.EqualFold(x.Schema, rel.table.Schema)) {
					matches = append(matches, rel)
				}
				continue
			}
			if rel.blockOriginalName && rel.alias != "" &&
				strings.EqualFold(x.Table, rel.table.Name) {
				// Defer the error: allow a qualifiedOnly binding
				// (e.g. excluded pseudo-table) to match first.
				deferredBlockErr = analyzeErrorWithHint(x.Pos(), "42712",
					fmt.Sprintf("invalid reference to FROM-clause entry for table %q", x.Table),
					fmt.Sprintf("Perhaps you meant to reference the table alias %q.", rel.alias))
				continue
			}
			if scopeRelMatches(rel, x.Table, x.Schema) {
				matches = append(matches, rel)
			}
		}
		if len(matches) == 0 {
			if deferredBlockErr != nil {
				return catalog.Type{}, false, deferredBlockErr
			}
			return catalog.Type{}, false, nil
		}
		if len(matches) > 1 {
			return catalog.Type{}, false, analyzeError(x.Pos(), "42702", fmt.Sprintf("table reference %q is ambiguous", x.Table))
		}
		matched := matches[0]
		col, ok := lookupColumn(matched.table, x.Column)
		if !ok {
			// `<rel>.tableoid` system-column resolution. M0100-0005y.
			if strings.EqualFold(x.Column, "tableoid") {
				return catalog.Type{Name: "oid"}, true, nil
			}
			// `<rel>.ctid` system-column resolution — NOT allowed for the
			// `excluded` pseudo-table (qualifiedOnly) since EXCLUDED is not
			// a stored tuple and has no system columns. M0097-0038.
			if strings.EqualFold(x.Column, "ctid") && !matched.qualifiedOnly {
				return catalog.Type{Name: "tid"}, true, nil
			}
			// DML CTE tables have no columns registered; the planner
			// resolves column types at execution time from the
			// materialized CTE result. Accept any column ref.
			if matched.table.IsDMLCTE {
				return catalog.Type{}, true, nil
			}
			// Use qualified "table.col" format to match PG output.
			qualifier := matched.alias
			if qualifier == "" {
				qualifier = matched.table.Name
			}
			ae := analyzeError(x.Pos(), "42703", fmt.Sprintf("column %s.%s does not exist", qualifier, x.Column))
			if hint := suggestAnalyzerColumnHint(matched.table.Columns, qualifier, x.Column); hint != "" {
				ae.Hint = hint
			}
			return catalog.Type{}, false, ae
		}
		return col.Type, true, nil
	}

	var found *catalog.Type
	for _, rel := range ctx.rels {
		if rel.qualifiedOnly {
			continue
		}
		col, ok := lookupColumn(rel.table, x.Column)
		if !ok {
			// DML CTE tables have no columns registered; accept any unqualified column ref.
			if rel.table.IsDMLCTE {
				if found != nil {
					return catalog.Type{}, false, analyzeError(x.Pos(), "42702", fmt.Sprintf("column reference %q is ambiguous", x.Column))
				}
				t := catalog.Type{}
				found = &t
			}
			continue
		}
		// Skip USING-hidden columns from right side of JOIN USING. M0097-0003.
		hiddenByUsing := false
		for _, uh := range rel.usingHidden {
			if strings.EqualFold(uh, x.Column) {
				hiddenByUsing = true
				break
			}
		}
		if hiddenByUsing {
			continue
		}
		if found != nil {
			return catalog.Type{}, false, analyzeError(x.Pos(), "42702", fmt.Sprintf("column reference %q is ambiguous", x.Column))
		}
		t := col.Type
		found = &t
	}
	if found == nil {
		// Unqualified `tableoid` system column. PG raises ambiguous
		// when more than one binding could supply it; for a single
		// non-qualified rel we resolve it to that binding's tableoid.
		// M0100-0005y.
		if strings.EqualFold(x.Column, "tableoid") {
			var match *scopeRel
			for i := range ctx.rels {
				if ctx.rels[i].qualifiedOnly {
					continue
				}
				if match != nil {
					return catalog.Type{}, false, analyzeError(x.Pos(), "42702", fmt.Sprintf("column reference %q is ambiguous", x.Column))
				}
				match = &ctx.rels[i]
			}
			if match != nil {
				return catalog.Type{Name: "oid"}, true, nil
			}
		}
		// Unqualified `ctid` system column. M0097-0038.
		if strings.EqualFold(x.Column, "ctid") {
			var match *scopeRel
			for i := range ctx.rels {
				if ctx.rels[i].qualifiedOnly {
					continue
				}
				if match != nil {
					return catalog.Type{}, false, analyzeError(x.Pos(), "42702", fmt.Sprintf("column reference %q is ambiguous", x.Column))
				}
				match = &ctx.rels[i]
			}
			if match != nil {
				return catalog.Type{Name: "tid"}, true, nil
			}
		}
		// Whole-row variable: unqualified column name matches a binding alias → composite (text). M0097-0020.
		for _, rel := range ctx.rels {
			if rel.qualifiedOnly {
				continue
			}
			name := rel.alias
			if name == "" {
				name = rel.table.Name
			}
			if strings.EqualFold(x.Column, name) {
				return catalog.Type{Name: "text"}, true, nil
			}
		}
		return catalog.Type{}, false, nil
	}
	return *found, true, nil
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
	if rv.Subquery != nil {
		return synthesizeSubqueryTable(cat, rv, nil)
	}
	// Table-valued function (M0096-0006): produce a synthetic table.
	// The planner is the source of truth for SRF return shapes; this
	// analyzer-side helper mirrors the planner's per-function dispatch
	// (see planTableFuncRangeVar in internal/planner/planner.go) so a
	// derived subquery wrapping an SRF — e.g.
	// `( SELECT (pg_get_publication_tables('p')).* ) AS gpt` —
	// surfaces the real column list (relid/attrs/qual) to the outer
	// scope rather than the generate_series-shaped single-int8-column
	// default. M0103-0008 (IndirectionStar derived-subquery propagation).
	if rv.TableFunc != nil {
		alias := rv.Alias
		if alias == "" {
			alias = rv.TableFunc.Name
		}
		cols := tableFuncColumns(rv.TableFunc, alias, rv.Columns)
		return &catalog.Table{Name: alias, Columns: cols}, nil
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok {
		return nil, analyzeError(rv.Pos(), "42P01", fmt.Sprintf("relation %q does not exist", rv.Name))
	}
	return tbl, nil
}

// tableFuncColumns returns the column list for a FROM-clause table-valued
// function. Mirrors the planner's planTableFuncRangeVar dispatch so the
// analyzer's synthesizeSubqueryTable can hand the outer scope the same
// column list the planner will produce at execution time. Unknown
// functions fall back to a single column named after the alias (the
// pre-M0103-0008 behaviour, sufficient for generate_series).
//
// WITH ORDINALITY appends a trailing int8 column, named from the last
// explicit column alias when given (else "ordinality") — mirrors
// wrapOrdinality (internal/planner/planner.go). The trailing alias is
// stripped before dispatch so per-function cases below see only the
// base-column aliases, same as the planner's colAliases[:len-1] slices.
func tableFuncColumns(tf *parser.TableFuncRef, alias string, colAliases []string) []catalog.Column {
	ordColName := "ordinality"
	if tf.WithOrdinality && len(colAliases) > 0 {
		ordColName = colAliases[len(colAliases)-1]
		colAliases = colAliases[:len(colAliases)-1]
	}
	cols := tableFuncBaseColumns(tf, alias, colAliases)
	if tf.WithOrdinality {
		cols = append(cols, catalog.Column{Name: ordColName, Type: catalog.Type{Name: "int8"}, Ordinal: len(cols)})
	}
	return cols
}

// tableFuncBaseColumns dispatches on function name for tableFuncColumns,
// before any WITH ORDINALITY column is appended.
func tableFuncBaseColumns(tf *parser.TableFuncRef, alias string, colAliases []string) []catalog.Column {
	switch strings.ToLower(tf.Name) {
	case "pg_get_publication_tables":
		names := []string{"relid", "attrs", "qual"}
		types := []string{"oid", "text", "text"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: types[i]}, Ordinal: i}
		}
		return cols
	case "pg_input_error_info":
		names := []string{"message", "detail", "hint", "sql_error_code"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: "text"}, Ordinal: i}
		}
		return cols
	case "parse_ident":
		colName := alias
		if len(colAliases) > 0 && colAliases[0] != "" {
			colName = colAliases[0]
		}
		return []catalog.Column{{Name: colName, Type: catalog.Type{Name: "text[]"}, Ordinal: 0}}
	case "pg_partition_tree":
		// pg_partition_tree(regclass) → (relid, parentrelid, isleaf, level). M0097-0023.
		names := []string{"relid", "parentrelid", "isleaf", "level"}
		types := []string{"oid", "oid", "bool", "int4"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: types[i]}, Ordinal: i}
		}
		return cols
	case "pg_partition_ancestors":
		// pg_partition_ancestors(regclass) → SETOF regclass, output column named "relid". M0097-0023.
		colName := "relid"
		if len(colAliases) > 0 && colAliases[0] != "" {
			colName = colAliases[0]
		}
		return []catalog.Column{{Name: colName, Type: catalog.Type{Name: "oid"}, Ordinal: 0}}
	case "pg_options_to_table":
		// pg_options_to_table(text[]) → (option_name text, option_value text).
		// Mirrors planPgOptionsToTable. DU-002 slice 17 (M0110-0001).
		names := []string{"option_name", "option_value"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: "text"}, Ordinal: i}
		}
		return cols
	case "pg_get_sequence_data":
		// pg_get_sequence_data(regclass) → (last_value int8, is_called bool).
		// Mirrors planPgGetSequenceData; pg_dump's getSequences comma-joins it
		// with pg_catalog.pg_sequence. DU-002 slice 32 (M0110-0001).
		names := []string{"last_value", "is_called"}
		types := []string{"int8", "bool"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: types[i]}, Ordinal: i}
		}
		return cols
	case "ts_token_type":
		// ts_token_type(parser_oid) → (tokid int4, alias text, description
		// text). Mirrors planTSTokenType; pg_dump's dumpTSConfig selects the
		// bare `alias` column from a correlated scalar subquery over this SRF.
		// DU-002 slice 446 (M0119-0004).
		names := []string{"tokid", "alias", "description"}
		types := []string{"int4", "text", "text"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: types[i]}, Ordinal: i}
		}
		return cols
	case "verify_heapam":
		// verify_heapam(regclass, ...) → (blkno int8, offnum int8, attnum int4,
		// msg text). Mirrors planVerifyHeapam. M0110-0003.
		names := []string{"blkno", "offnum", "attnum", "msg"}
		types := []string{"int8", "int8", "int4", "text"}
		for i := range names {
			if i < len(colAliases) && colAliases[i] != "" {
				names[i] = colAliases[i]
			}
		}
		cols := make([]catalog.Column, len(names))
		for i := range names {
			cols[i] = catalog.Column{Name: names[i], Type: catalog.Type{Name: types[i]}, Ordinal: i}
		}
		return cols
	case "unnest":
		// unnest(arr1[, arr2, ...]) zips N arrays into N columns. Mirrors
		// planFromUnnest; the analyzer has no expression-type resolution
		// available yet at this point in scope-building (lookupTable runs
		// before the FROM scope exists), so element types fall back to
		// "text" rather than the array's real element type — sufficient
		// for column-name resolution (42703), imprecise for typing.
		n := len(tf.Args)
		if n == 0 {
			n = 1
		}
		cols := make([]catalog.Column, n)
		for i := 0; i < n; i++ {
			name := "unnest"
			if n == 1 {
				name = alias
			}
			if i < len(colAliases) && colAliases[i] != "" {
				name = colAliases[i]
			}
			cols[i] = catalog.Column{Name: name, Type: catalog.Type{Name: "text"}, Ordinal: i}
		}
		return cols
	case "regexp_matches":
		// regexp_matches(string, pattern[, flags]) → single text[] column.
		// Mirrors planFromRegexpMatches. M0122-0004 WITH-ORDINALITY fix.
		colName := alias
		if len(colAliases) > 0 && colAliases[0] != "" {
			colName = colAliases[0]
		}
		return []catalog.Column{{Name: colName, Type: catalog.Type{Name: "text[]"}, Ordinal: 0}}
	default:
		// generate_series and unknown SRFs: 1 int8 column named after
		// the alias. Preserves pre-M0103-0008 behaviour.
		colName := alias
		if len(colAliases) > 0 && colAliases[0] != "" {
			colName = colAliases[0]
		}
		return []catalog.Column{{Name: colName, Type: catalog.Type{Name: "int8"}, Ordinal: 0}}
	}
}

// compositeFuncColumns returns the composite return-column shape for a
// SRF that is known to expand into multiple columns via `(srf(...)).*`
// in target-list position. Returns nil for SRFs without a composite
// return type (the caller falls back to the generic analyzeExpr path).
// Mirrors planner.projectSetCompositeSchema so the analyzer's
// derived-subquery synthesis sees the same columns the planner will
// produce at execution time. M0103-0008 rung 7.
func compositeFuncColumns(funcName string) []catalog.Column {
	switch strings.ToLower(funcName) {
	case "pg_get_publication_tables":
		return []catalog.Column{
			{Name: "relid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
			{Name: "attrs", Type: catalog.Type{Name: "text"}, Ordinal: 1},
			{Name: "qual", Type: catalog.Type{Name: "text"}, Ordinal: 2},
		}
	}
	return nil
}

// resolveTable is the scope-aware variant of lookupTable used by
// FROM-clause resolution. It walks the scope chain head-first
// looking for a matching CTE name (so an inner WITH shadows an
// outer one), and only falls through to the catalog when no CTE
// matches. Schema-qualified names (`pg_catalog.foo`) bypass CTE
// resolution since CTE names are unschemed in upstream.
//
// Used only by buildSelectScope. Target-relation paths (INSERT INTO
// / UPDATE / DELETE FROM) keep using lookupTable directly so that
// `INSERT INTO cte_name VALUES ...` continues to error on the
// non-existent base relation rather than mistakenly aliasing the
// CTE — Stage A scope explicitly forbids data-modifying CTE chains.
func resolveTable(ctx *scope, rv parser.RangeVar) (*catalog.Table, error) {
	if rv.Subquery == nil && rv.Schema == "" && rv.Name != "" {
		for s := ctx; s != nil; s = s.parent {
			if s.ctes == nil {
				continue
			}
			if tbl, ok := s.ctes[strings.ToLower(rv.Name)]; ok {
				// CTE found — clone with the alias from the FROM
				// reference (or fall back to the CTE name) so the
				// scopeRel carries the right alias for column
				// qualification (`alias.col` lookups).
				cp := *tbl
				if rv.Alias == "" {
					cp.Name = tbl.Name
				}
				return &cp, nil
			}
		}
	}
	return lookupTable(ctx.cat, rv)
}

// analyzeWith populates ctx.ctes from a WITH clause. Each CTE's
// inner SELECT is recursively analyzed under ctx so a later CTE
// in the same list can reference an earlier one (left-to-right
// visibility, matching PostgreSQL). The synthetic *catalog.Table
// for each CTE mirrors synthesizeSubqueryTable's column-naming
// chain — explicit alias → derived name → `?column?N` — overridden
// by the optional `(col, ...)` alias list when present.
//
// Stage A restrictions enforced here:
//   - WITH RECURSIVE → analyzed with the CTE self-reference allowed
//     (planner enforces UNION ALL requirement).
//   - Duplicate CTE name within the same WITH list → 42710.
//   - Column-alias arity mismatch → 42P10 (invalid_column_reference).

// analyzeRecursiveCTE analyzes a WITH RECURSIVE CTE body. It analyzes
// the anchor (left side of UNION ALL) first to determine the output
// columns, registers the CTE in the scope, then analyzes the recursive
// member (right side) with the CTE self-reference visible.
func analyzeRecursiveCTE(cte *parser.CommonTableExpr, ctx *scope) error {
	// PostgreSQL rejects DML inside a WITH RECURSIVE body with
	// "recursive query must not contain data-modifying statements" (42P19).
	if cte.DMLBody != nil {
		return analyzeError(cte.Pos(), "42P19",
			fmt.Sprintf("recursive query %q must not contain data-modifying statements", cte.Name))
	}
	body := cte.Query
	// PostgreSQL allows both UNION and UNION ALL for recursive CTEs.
	// Only fall back to the non-recursive path when there is no set operation
	// (body.SetOp == nil) or the set operation is not UNION-family (Intersect/Except).
	if body.SetOp == nil || body.SetOp.Type != parser.SetOpUnion {
		// No recursive self-join — just analyse the body as a
		// regular CTE.
		if err := analyzeSelectWithParent(body, ctx.cat, ctx); err != nil {
			return err
		}
		return registerAnalyzedCTE(cte, ctx)
	}

	// Analyze the anchor (left side) without the CTE in scope.
	saved := body.SetOp
	body.SetOp = nil
	if err := analyzeSelectWithParent(body, ctx.cat, ctx); err != nil {
		body.SetOp = saved
		return err
	}
	body.SetOp = saved

	// Build columns from the anchor's target list.
	innerCtx := &scope{cat: ctx.cat, parent: ctx}
	var rels []scopeRel
	if len(body.From) > 0 || len(body.FromExprs) > 0 {
		var err error
		rels, err = buildSelectScopeIn(body, innerCtx)
		if err != nil {
			return err
		}
		innerCtx.rels = rels
	}
	var cols []catalog.Column
	if len(body.Targets) == 0 && len(body.ValuesRows) > 0 {
		// VALUES anchor (e.g. VALUES (1) UNION ALL SELECT n+1 ...): no Targets,
		// so infer columns from the first row. Names are "column1", "column2", ...
		// and types are "unknown" (the planner resolves exact types). M0097-0062.
		nCols := len(body.ValuesRows[0])
		cols = make([]catalog.Column, nCols)
		for i := 0; i < nCols; i++ {
			cols[i] = catalog.Column{Name: fmt.Sprintf("column%d", i+1), Type: catalog.Type{Name: "unknown"}, Ordinal: i}
		}
	} else {
		cols = make([]catalog.Column, 0, len(body.Targets))
		for _, tgt := range body.Targets {
			// A top-level `*` / `t.*` must be expanded to concrete columns —
			// analyzeExpr rejects a bare StarExpr. Mirrors registerAnalyzedCTE.
			if star, ok := tgt.Expr.(*parser.StarExpr); ok {
				cols = append(cols, expandInnerStarColumns(star, innerCtx)...)
				continue
			}
			name := tgt.Alias
			if name == "" {
				name = deriveAnalyzerTargetName(tgt.Expr)
			}
			if name == "" {
				name = fmt.Sprintf("?column?%d", len(cols)+1)
			}
			typ, err := analyzeExpr(tgt.Expr, innerCtx)
			if err != nil {
				return err
			}
			cols = append(cols, catalog.Column{Name: name, Type: typ, Ordinal: len(cols)})
		}
	}
	// Apply explicit column alias list (the `(col, ...)` after the CTE name),
	// matching what registerAnalyzedCTE does for non-recursive CTEs.
	// Without this, `WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL ...)` registers
	// the CTE with column name "?column?1" instead of "n", causing the recursive
	// member to fail with "column n does not exist".
	if len(cte.Columns) > 0 {
		// PG (parse_cte.c analyzeCTE): the alias list must be empty or no
		// LONGER than the output column set; a SHORTER list is allowed and the
		// trailing columns keep their query-derived names. Only over-aliasing
		// is the 42P10 error.
		if len(cte.Columns) > len(cols) {
			return analyzeError(cte.Pos(), "42P10",
				fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(cols)))
		}
		for i, alias := range cte.Columns {
			cols[i].Name = alias
		}
	}
	colAliases := make([]string, len(cols))
	for i, c := range cols {
		colAliases[i] = c.Name
	}
	ctx.ctes[strings.ToLower(cte.Name)] = &catalog.Table{
		Name:              cte.Name,
		Columns:           cols,
		Virtual:           true,
		ViewColumnAliases: colAliases,
	}

	// Analyze the recursive member (right side) with the CTE visible.
	if err := analyzeSelectWithParent(body.SetOp.Right, ctx.cat, ctx); err != nil {
		return err
	}
	return nil
}

func analyzeWith(with *parser.WithClause, ctx *scope) error {
	if with == nil {
		return nil
	}
	if ctx.ctes == nil {
		ctx.ctes = make(map[string]*catalog.Table, len(with.CTEs))
	}
	for _, cte := range with.CTEs {
		nameKey := strings.ToLower(cte.Name)
		if _, exists := ctx.ctes[nameKey]; exists {
			return analyzeError(cte.Pos(), "42710", fmt.Sprintf("duplicate CTE name %q", cte.Name))
		}

		if with.Recursive {
			// For WITH RECURSIVE, the CTE body must be UNION ALL.
			// Register the CTE name before analyzing so the
			// recursive member's self-reference resolves.
			if err := analyzeRecursiveCTE(cte, ctx); err != nil {
				return err
			}
			continue
		}

		if cte.DMLBody != nil {
			// Data-modifying CTE (INSERT/UPDATE/DELETE/MERGE).
			// Analysis is handled by the DML-specific planner when
			// the CTE is planned; register with an empty table so
			// the outer query knows the name exists. IsDMLCTE lets
			// resolveColumnRefTypeAt accept any column ref on this
			// table without strict validation.
			if ctx.ctes == nil {
				ctx.ctes = make(map[string]*catalog.Table)
			}
			ctx.ctes[strings.ToLower(cte.Name)] = &catalog.Table{Name: cte.Name, IsDMLCTE: true}
			continue
		}

		// Recurse into the CTE's inner SELECT under the current
		// scope so an earlier CTE in the same WITH list is
		// visible to a later one. The inner SELECT can also have
		// its own nested WITH (left-recursive call into
		// analyzeSelectWithParent which calls analyzeWith again).
		if err := analyzeSelectWithParent(cte.Query, ctx.cat, ctx); err != nil {
			return err
		}
		if err := registerAnalyzedCTE(cte, ctx); err != nil {
			return err
		}
	}
	return nil
}

func registerAnalyzedCTE(cte *parser.CommonTableExpr, ctx *scope) error {
	innerCtx := &scope{cat: ctx.cat, parent: ctx}
	// If the CTE body has its own WITH clause, register those inner
	// CTEs in innerCtx so they are visible when resolving the body's
	// FROM clause below. Without this, `WITH w6 AS (WITH w8 AS
	// (SELECT 1) SELECT * FROM w8)` fails because w8 is not in scope
	// when registerAnalyzedCTE tries to resolve FROM w8.
	if cte.Query.With != nil {
		if err := analyzeWith(cte.Query.With, innerCtx); err != nil {
			return err
		}
	}
	if len(cte.Query.From) > 0 || len(cte.Query.FromExprs) > 0 {
		rels, err := buildSelectScopeIn(cte.Query, innerCtx)
		if err != nil {
			return err
		}
		innerCtx.rels = rels
	}
	innerCols := make([]catalog.Column, 0, len(cte.Query.Targets))
	if len(cte.Query.Targets) == 0 && len(cte.Query.ValuesRows) > 0 {
		// VALUES-list CTE body (e.g. `cte (a, b) AS (VALUES (1,'x'),
		// (2,'y'))`): there are no Targets, so the column count comes
		// from the first VALUES row. Default names are "column1",
		// "column2", … and types are "unknown" (the planner resolves
		// exact types from the row literals). This mirrors the VALUES
		// anchor handling in analyzeRecursiveCTE — keep the two in sync.
		// pg_amcheck's database-resolution query relies on this shape
		// (`include_raw (pattern_id, rgx) AS (VALUES (0,'^(x)$'), …)`).
		// M0110-0003 / AC-002.
		nCols := len(cte.Query.ValuesRows[0])
		for i := 0; i < nCols; i++ {
			innerCols = append(innerCols, catalog.Column{Name: fmt.Sprintf("column%d", i+1), Type: catalog.Type{Name: "unknown"}, Ordinal: i})
		}
	} else {
		for _, tgt := range cte.Query.Targets {
			// A top-level `*` / `t.*` in the CTE body must materialise into
			// the inner scope's concrete columns — analyzeExpr rejects a
			// bare StarExpr ("'*' is not allowed here"). Mirrors
			// synthesizeSubqueryTable's derived-table star handling; keep
			// the two in sync. M0097-0003.
			if star, ok := tgt.Expr.(*parser.StarExpr); ok {
				innerCols = append(innerCols, expandInnerStarColumns(star, innerCtx)...)
				continue
			}
			name := tgt.Alias
			if name == "" {
				name = deriveAnalyzerTargetName(tgt.Expr)
			}
			if name == "" {
				name = fmt.Sprintf("?column?%d", len(innerCols)+1)
			}
			typ, err := analyzeExpr(tgt.Expr, innerCtx)
			if err != nil {
				return err
			}
			innerCols = append(innerCols, catalog.Column{Name: name, Type: typ})
		}
	}
	if len(cte.Columns) > 0 {
		// PG (parse_cte.c analyzeCTE): a column-alias list shorter than the
		// inner query's output is allowed — the extra trailing columns keep
		// their query names. Only over-aliasing is the 42P10 error. pg_amcheck
		// relies on this for its `exclude_raw (pattern_id, rgx) AS (SELECT
		// NULL, NULL, NULL WHERE false)` empty-pattern CTE (AC-002).
		if len(cte.Columns) > len(innerCols) {
			return analyzeError(cte.Pos(), "42P10",
				fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(innerCols)))
		}
		for i, alias := range cte.Columns {
			innerCols[i].Name = alias
		}
	}
	ctx.ctes[strings.ToLower(cte.Name)] = &catalog.Table{Name: cte.Name, Columns: innerCols}
	return nil
}

// buildSelectScopeIn is the scope-aware variant of buildSelectScope
// — used by analyzeWith when validating an inner CTE body's column
// types. Resolves FROM-clause range-vars through the supplied
// scope (so a CTE body can reference an earlier sibling CTE).
func buildSelectScopeIn(s *parser.SelectStmt, ctx *scope) ([]scopeRel, error) {
	if len(s.FromExprs) == 0 {
		rels := make([]scopeRel, 0, len(s.From))
		for _, rv := range s.From {
			tbl, err := resolveTable(ctx, rv)
			if err != nil {
				return nil, err
			}
			// M0097-0058: apply column alias renaming from
			// FROM tbl alias (col1, col2, ...) so that the
			// analyzer scope uses the aliased names. Without
			// this, resolving `alias.col1` fails with "column
			// does not exist" because the scope still has the
			// original catalog column names.
			tbl = applyRangeVarColumnAliases(rv, tbl)
			rels = append(rels, scopeRel{table: tbl, alias: rv.Alias})
		}
		return rels, nil
	}
	rels := make([]scopeRel, 0, len(s.From))
	for _, item := range s.FromExprs {
		var tbl *catalog.Table
		var err error
		if item.Base.Subquery != nil {
			// Derived-table base item: pass accumulated left-side rels as
			// lateral outer scope so the inner SELECT can resolve correlated
			// references to earlier FROM items AND to outer-query columns
			// (via ctx.parent). This mirrors the JOIN lateral path below and
			// fixes cases like:
			//   FROM (VALUES(1)) AS x, (SELECT … OFFSET s-1) AS y
			// where s is a column from an enclosing query. M0097-0065.
			lateralCtx := &scope{parent: ctx, cat: ctx.cat, rels: append([]scopeRel(nil), rels...)}
			tbl, err = synthesizeSubqueryTable(ctx.cat, item.Base, lateralCtx)
		} else {
			tbl, err = resolveTable(ctx, item.Base)
		}
		if err != nil {
			return nil, err
		}
		tbl = applyRangeVarColumnAliases(item.Base, tbl)
		rels = append(rels, scopeRel{table: tbl, alias: item.Base.Alias})
		for _, j := range item.Joins {
			var rt *catalog.Table
			if j.Right.Subquery != nil {
				// LATERAL subquery in a JOIN: pass the accumulated left-side
				// relations as the outer scope so the inner SELECT can
				// resolve correlated references like t1.col where t1 is the
				// left side of the JOIN. M0097-0064.
				lateralCtx := &scope{parent: ctx, cat: ctx.cat, rels: append([]scopeRel(nil), rels...)}
				rt, err = synthesizeSubqueryTable(ctx.cat, j.Right, lateralCtx)
			} else {
				rt, err = resolveTable(ctx, j.Right)
			}
			if err != nil {
				return nil, err
			}
			rt = applyRangeVarColumnAliases(j.Right, rt)
			rels = append(rels, scopeRel{table: rt, alias: j.Right.Alias, usingHidden: j.Using})
		}
	}
	return rels, nil
}

// applyRangeVarColumnAliases returns a shallow copy of tbl with
// column names replaced by the alias list from rv.Columns, or
// tbl itself when rv.Columns is empty or the table came from a
// subquery (subqueries handle their own renaming). M0097-0058.
func applyRangeVarColumnAliases(rv parser.RangeVar, tbl *catalog.Table) *catalog.Table {
	if len(rv.Columns) == 0 || rv.Subquery != nil {
		return tbl
	}
	cp := *tbl
	cols := make([]catalog.Column, len(tbl.Columns))
	copy(cols, tbl.Columns)
	for i, alias := range rv.Columns {
		if i < len(cols) {
			cols[i].Name = alias
		}
	}
	cp.Columns = cols
	return &cp
}

// synthesizeSubqueryTable analyzes a derived table — the
// `(SELECT …) AS alias` form parsed into rv.Subquery — and
// builds an in-memory *catalog.Table whose columns mirror the
// inner SELECT's target list. The table is alias-named so name
// resolution in the outer query treats `alias.col` correctly,
// and lives only for the duration of analysis (never registered
// in the catalog).
//
// Column naming follows upstream's `transformTargetEntry`
// fallback chain: explicit `AS alias` first, then derived names
// (bare ColumnRef → column name; FuncCall → function name);
// otherwise `?column?N`. Types come from analyzeExpr against an
// inner scope built from the subquery's own FROM clause.
//
// v0 does not support full LATERAL analysis, but accepts the LATERAL
// keyword and falls back to a null-typed table when the inner subquery
// references outer-scope columns (correlated lateral reference). The
// fallback is safe for vacuumdb's CROSS JOIN LATERAL use case where the
// produced column (inherited) is never referenced in the WHERE clause
// for basic vacuum runs. See docs/design/0003-0014-derived-tables.md.
// expandInnerStarColumns materialises a target-list StarExpr into the
// concrete column list of an inner SELECT's FROM scope. The analyzer uses
// this when synthesising the columns of a derived relation — a derived
// table (subquery) or a CTE body — where a top-level `*` must become real
// columns rather than be type-checked as a scalar (analyzeExpr rejects a
// bare StarExpr). An unqualified `*` expands every in-scope relation; a
// qualified `t.*` expands only the matching relation. M0097-0003.
func expandInnerStarColumns(star *parser.StarExpr, innerCtx *scope) []catalog.Column {
	if innerCtx == nil {
		return nil
	}
	qualified := star.Table != "" || star.Schema != ""
	var cols []catalog.Column
	for _, rel := range innerCtx.rels {
		if qualified && !scopeRelMatches(rel, star.Table, star.Schema) {
			continue
		}
		for _, col := range rel.table.Columns {
			cols = append(cols, catalog.Column{Name: col.Name, Type: col.Type})
		}
	}
	return cols
}

func synthesizeSubqueryTable(cat catalog.Catalog, rv parser.RangeVar, outerCtx *scope) (*catalog.Table, error) {
	// VALUES subquery: FROM (VALUES (r1), ...) AS t(c1, c2).
	// The inner SelectStmt has no Targets; build the column list from the
	// explicit alias list (rv.Columns) or synthetic names. M0097-0003.
	if len(rv.Subquery.ValuesRows) > 0 {
		nCols := len(rv.Subquery.ValuesRows[0])
		cols := make([]catalog.Column, nCols)
		for i := 0; i < nCols; i++ {
			name := fmt.Sprintf("column%d", i+1)
			if i < len(rv.Columns) && rv.Columns[i] != "" {
				name = rv.Columns[i]
			}
			// Use "unknown" so that numeric/arithmetic operations on VALUES
			// columns pass type checking (isNumericLike("unknown") = true). M0097-0003.
			cols[i] = catalog.Column{Name: name, Type: catalog.Type{Name: "unknown"}}
		}
		return &catalog.Table{Name: rv.Alias, Columns: cols}, nil
	}
	// LATERAL subquery analysis: pass outerCtx as the lexical-scope parent so
	// the inner SELECT can resolve correlated references to outer FROM-clause
	// columns (e.g. t1.tenthous when t1 is the left side of a JOIN LATERAL).
	// M0097-0064.
	if err := analyzeSelectWithParent(rv.Subquery, cat, outerCtx); err != nil {
		// LATERAL fallback: correlated reference to outer table fails analysis.
		// When explicit column aliases are provided (rv.Columns), produce a
		// synthetic table with those column names and unknown (text) types.
		// This allows the outer query to proceed; column values are NULL at
		// execution time (planSubqueryRangeVar also handles this case).
		ae, isAnalyzeErr := err.(*AnalyzeError)
		if isAnalyzeErr && (ae.Code == "42P01" || ae.Code == "42703") && len(rv.Columns) > 0 {
			cols := make([]catalog.Column, len(rv.Columns))
			for i, colName := range rv.Columns {
				cols[i] = catalog.Column{Name: colName, Type: catalog.Type{Name: "text"}}
			}
			return &catalog.Table{Name: rv.Alias, Columns: cols}, nil
		}
		return nil, err
	}
	innerCtx := &scope{cat: cat, parent: outerCtx}
	// When the subquery has a WITH clause, register the CTEs in innerCtx so
	// that buildSelectScopeIn can find CTE-named tables via resolveTable
	// (which walks the scope chain). buildSelectScope uses lookupTable which
	// is catalog-only and silently drops CTE names. M0097-0098.
	if rv.Subquery.With != nil {
		_ = analyzeWith(rv.Subquery.With, innerCtx)
	}
	if len(rv.Subquery.From) > 0 || len(rv.Subquery.FromExprs) > 0 {
		rels, err := buildSelectScopeIn(rv.Subquery, innerCtx)
		if err != nil {
			return nil, err
		}
		innerCtx.rels = rels
	}
	cols := make([]catalog.Column, 0, len(rv.Subquery.Targets))
	for _, tgt := range rv.Subquery.Targets {
		// Star expression in inner SELECT (e.g. TABLE tablename → SELECT * FROM tablename).
		// Expand to all columns from the inner scope. M0097-0003.
		if star, ok := tgt.Expr.(*parser.StarExpr); ok {
			cols = append(cols, expandInnerStarColumns(star, innerCtx)...)
			continue
		}
		// `(srf(...)).*` inside a derived subquery — expand to the SRF's
		// composite columns so outer-scope references resolve. Mirrors
		// the planner's ProjectSet lowering
		// (planner.projectSetCompositeSchema). Currently only
		// pg_get_publication_tables is recognised as composite; other
		// IndirectionStar sources fall through to the generic
		// analyzeExpr path. M0103-0008 rung 7.
		if is, ok := tgt.Expr.(*parser.IndirectionStar); ok {
			if fc, ok2 := is.Source.(*parser.FuncCall); ok2 {
				if comp := compositeFuncColumns(fc.Name.Name); comp != nil {
					for _, c := range comp {
						cols = append(cols, catalog.Column{Name: c.Name, Type: c.Type})
					}
					continue
				}
			}
		}
		name := tgt.Alias
		if name == "" {
			name = deriveAnalyzerTargetName(tgt.Expr)
		}
		if name == "" {
			name = fmt.Sprintf("?column?%d", len(cols)+1)
		}
		typ, err := analyzeExpr(tgt.Expr, innerCtx)
		if err != nil {
			return nil, err
		}
		cols = append(cols, catalog.Column{Name: name, Type: typ})
	}
	// Validate and apply explicit column aliases (rv.Columns). M0097-0003.
	if len(rv.Columns) > 0 {
		if len(rv.Columns) != len(cols) {
			return nil, analyzeError(rv.Pos(), "42P01",
				fmt.Sprintf("table %q has %d columns available but %d columns specified",
					rv.Alias, len(cols), len(rv.Columns)))
		}
		for i := range cols {
			cols[i].Name = rv.Columns[i]
		}
	}
	return &catalog.Table{Name: rv.Alias, Columns: cols}, nil
}

// deriveAnalyzerTargetName mirrors executor.deriveTargetName for
// the analyzer's synthetic-table flow. Bare ColumnRef returns
// its column name; FuncCall returns its lower-cased function
// name; everything else returns the empty string and the
// caller falls back to `?column?N`.
func deriveAnalyzerTargetName(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.ColumnRef:
		return x.Column
	case *parser.FuncCall:
		return strings.ToLower(x.Name.Name)
	}
	return ""
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
			rels = append(rels, scopeRel{table: rt, alias: j.Right.Alias, usingHidden: j.Using})
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
		// Skip GENERATED ALWAYS AS … STORED columns — they are computed by
		// the executor, not supplied by the INSERT statement. M0096-0008.
		out := make([]catalog.Column, 0, len(tbl.Columns))
		for _, col := range tbl.Columns {
			if col.GeneratedAlways {
				continue
			}
			out = append(out, col)
		}
		if len(out) == len(tbl.Columns) {
			// No generated columns — return all columns for backward compat.
			return append([]catalog.Column(nil), tbl.Columns...), nil
		}
		return out, nil
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
	case "int", "int2", "int4", "int8", "integer", "smallint", "bigint", "numeric", "decimal",
		"float", "float4", "float8", "real", "double", "double precision",
		// SERIAL family are not real types: PostgreSQL resolves them to
		// int4/int8/int2 (pg_typeof reports "integer"). Treat them as their
		// integer base so `serial_col = 1` / `serial_col + 1` type-check the
		// same as int columns. The stored catalog type stays "serial" because
		// the INSERT auto-increment path (operators_storage.go) keys off it.
		"serial", "bigserial", "smallserial",
		// PostgreSQL aliases: serial2=smallserial, serial4=serial, serial8=bigserial
		"serial2", "serial4", "serial8":
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

// isTimestampLike reports whether a type is one of the v0
// timestamp / date kinds. They share an internal representation
// (KindTime), so timestamp ± interval and date ± interval go
// through the same evaluator path.
func isTimestampLike(t catalog.Type) bool {
	if isUnknownType(t) {
		return true
	}
	switch strings.ToLower(t.Name) {
	case "timestamp", "timestamptz", "date", "time", "timetz":
		return true
	}
	return false
}

// isConcreteTimestampLike is like isTimestampLike but returns false for
// "unknown" type (untyped string literals). Used to avoid false-positive
// "operator is not unique" errors when a string literal participates in
// arithmetic. M0097-0004.
func isConcreteTimestampLike(t catalog.Type) bool {
	switch strings.ToLower(t.Name) {
	case "timestamp", "timestamptz", "date", "time", "timetz":
		return true
	}
	return false
}

// isComparableTime allows timestamp/date columns to be compared
// against each other and against unknown literals. Used by the
// CASE branch unifier.
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
	if isTimestampLike(left) && isTimestampLike(right) {
		return true
	}
	// String literals (text) are comparable with date/time types via implicit cast.
	// PostgreSQL resolves text→timestamp/date/time at runtime. M0097-0004.
	if isStringTypeName(left.Name) && isTimestampLike(right) {
		return true
	}
	if isTimestampLike(left) && isStringTypeName(right.Name) {
		return true
	}
	// uuid, name, oid and other text-backed types are comparable with text/varchar. M0097-0003.
	// tid is also text-backed (string representation "(block,offset)"); ctid = '(0,1)' must work. M0097-0062.
	// isOIDFamily covers oid and its "reg*" aliases (regproc, regclass,
	// regtype, ...): PostgreSQL stores them with the same on-disk
	// representation (just a different I/O function), so e.g.
	// `pg_aggregate.aggfnoid = pg_proc.oid` (aggfnoid is regproc) type-checks
	// without an explicit cast — pg_dump's own dumpAgg prepared query relies
	// on exactly this join. DU-002 slice 405.
	isOIDFamily := func(t catalog.Type) bool {
		switch strings.ToLower(t.Name) {
		case "oid", "regproc", "regprocedure", "regoper", "regoperator", "regclass",
			"regtype", "regconfig", "regdictionary", "regnamespace", "regrole",
			"regcollation":
			return true
		}
		return false
	}
	isTextBacked := func(t catalog.Type) bool {
		if isOIDFamily(t) {
			return true
		}
		switch strings.ToLower(t.Name) {
		case "uuid", "name", "oidvector", "int2vector", "pg_lsn", "tid":
			return true
		}
		return false
	}
	if (isTextBacked(left) || isStringTypeName(left.Name)) && (isTextBacked(right) || isStringTypeName(right.Name)) {
		return true
	}
	// oid ↔ integer: PostgreSQL has implicit oid↔int4 casts. M0097-0003.
	if isOIDFamily(left) && isNumericTypeName(right.Name) {
		return true
	}
	if isNumericTypeName(left.Name) && isOIDFamily(right) {
		return true
	}
	// User-defined types (enum, domain, composite) ↔ string: allow comparison
	// since enums are stored as text and string literals are cast at runtime. M0097-enum.
	if !isKnownBuiltinType(left.Name) && isStringTypeName(right.Name) {
		return true
	}
	if isStringTypeName(left.Name) && !isKnownBuiltinType(right.Name) {
		return true
	}
	return false
}

// analyzeCaseExpr type-checks a CASE expression and returns the
// type of the value it produces. v0 unifies WHEN/ELSE result
// types loosely: if all branches resolve to the same type, that's
// the result; otherwise we fall back to `unknown` (which makes
// it assignable to any column). Real type unification (numeric
// promotion, text/varchar coalescing) waits on the type system.
func analyzeCaseExpr(x *parser.CaseExpr, ctx *scope) (catalog.Type, error) {
	// Operand of the simple form just needs to be a valid
	// expression; type-checking the comparison is deferred until
	// the type system can do real coercions.
	if x.Operand != nil {
		if _, err := analyzeExpr(x.Operand, ctx); err != nil {
			return catalog.Type{}, err
		}
	}
	var resultType catalog.Type
	for _, w := range x.Whens {
		if x.Operand == nil {
			// Searched form: WHEN must be boolean-like.
			whenType, err := analyzeExpr(w.When, ctx)
			if err != nil {
				return catalog.Type{}, err
			}
			if !isBooleanLike(whenType) {
				return catalog.Type{}, analyzeError(w.When.Pos(), "42804", "CASE WHEN clause must be boolean")
			}
		} else {
			if _, err := analyzeExpr(w.When, ctx); err != nil {
				return catalog.Type{}, err
			}
		}
		thenType, err := analyzeExpr(w.Then, ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		if resultType.Name == "" {
			resultType = thenType
		} else if !sameOrCompatible(resultType, thenType) {
			resultType = catalog.Type{Name: "unknown"}
		}
	}
	if x.Else != nil {
		elseType, err := analyzeExpr(x.Else, ctx)
		if err != nil {
			return catalog.Type{}, err
		}
		if resultType.Name == "" {
			resultType = elseType
		} else if !sameOrCompatible(resultType, elseType) {
			resultType = catalog.Type{Name: "unknown"}
		}
	}
	return resultType, nil
}

// sameOrCompatible reports whether two CASE branch types should
// be merged into the same result type without falling back to
// unknown. Mirrors `isComparable` but limited to types CASE
// branches commonly mix (int8 + numeric, text + varchar, etc.).
func sameOrCompatible(a, b catalog.Type) bool {
	if isUnknownType(a) || isUnknownType(b) {
		return true
	}
	if strings.EqualFold(a.Name, b.Name) {
		return true
	}
	if isNumericTypeName(a.Name) && isNumericTypeName(b.Name) {
		return true
	}
	if isStringTypeName(a.Name) && isStringTypeName(b.Name) {
		return true
	}
	return false
}

func isAssignable(src, dst catalog.Type) bool {
	if isUnknownType(src) {
		return true
	}
	// An array column (e.g. `p int4[]`, dst.IsArray) accepts any array-typed
	// source: the ARRAY[...] constructor (analyzed as "text[]") or another
	// array column/expression. Element-type validation happens at runtime in
	// the array codec, mirroring PG's reliance on array_in to reject bad
	// element text. M0118-0002.
	if dst.IsArray && (src.IsArray || strings.HasSuffix(src.Name, "[]")) {
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
	// Upstream PG accepts bare string literals ('123') for any column type
	// because literals are typed `unknown` until inferred at the assignment
	// site. goopg types them as `text` and recovers compatibility by
	// allowing text → numeric, integer, and float column types. The executor
	// validates and converts the value at runtime, giving a proper
	// "invalid input syntax" (22P02) error for malformed inputs.
	//
	// This enables test_setup.sql INSERTs like:
	//   INSERT INTO INT2_TBL(f1) VALUES ('1234'), ('-1234');
	// to populate the shared tables needed by int2/int4/int8/float tests.
	// M0097-0003.
	if isStringTypeName(src.Name) && isNumericOrIntegerTarget(dst.Name) {
		return true
	}
	// String literals are assignable to oid and uuid columns; the
	// executor validates the value at runtime and gives a proper
	// "invalid input syntax" error for malformed inputs. M0097-0003.
	if isStringTypeName(src.Name) && isOidOrUUIDTarget(dst.Name) {
		return true
	}
	// String literals assignable to date/time column types; encodeValue
	// parses and validates the string at runtime. M0097-0004.
	if isStringTypeName(src.Name) && isDateTimeTarget(dst.Name) {
		return true
	}
	// PostgreSQL allows integer/numeric values to be inserted into text/varchar
	// columns via implicit cast. M0097-0003.
	if isNumericTypeName(src.Name) && isStringTypeName(dst.Name) {
		return true
	}
	// User-defined types (enums, domains, composites) are stored as text.
	// Allow string → user-defined type assignment; the executor validates
	// the enum label at runtime via the ::enum cast path. M0097-enum.
	if isStringTypeName(src.Name) && !isKnownBuiltinType(dst.Name) {
		return true
	}
	return false
}

// isKnownBuiltinType reports whether name is a known built-in scalar type.
// Used to distinguish user-defined types (enum, domain, composite) from built-ins.
func isKnownBuiltinType(name string) bool {
	switch strings.ToLower(name) {
	case "text", "varchar", "char", "bpchar", "character varying",
		"name", "unknown", "citext",
		"int2", "smallint", "int4", "integer", "int", "int8", "bigint",
		"serial", "smallserial", "bigserial",
		"float4", "real", "float8", "double precision", "double", "float",
		"numeric", "decimal",
		"bool", "boolean",
		"date", "time", "timetz", "timestamp", "timestamptz", "interval",
		"oid", "uuid", "pg_lsn", "xid", "xid8", "cid", "regproc",
		"bytea", "varbit", "bit", "json", "jsonb",
		"tid", "money":
		return true
	}
	return false
}

// isOidOrUUIDTarget reports whether dst is a column type whose codec
// accepts string values by parsing them at runtime (oid, uuid, pg_lsn).
func isOidOrUUIDTarget(name string) bool {
	switch strings.ToLower(name) {
	case "oid", "uuid", "pg_lsn":
		return true
	}
	return false
}

// isDateTimeTarget reports whether dst is a date/time column type that
// accepts string values (parsed by encodeValue at runtime). M0097-0004.
func isDateTimeTarget(name string) bool {
	switch strings.ToLower(name) {
	case "date", "time", "timetz", "timestamp", "timestamptz":
		return true
	}
	return false
}

// isNumericOrIntegerTarget reports whether dst is a numeric, integer,
// or float column type that can accept string literals at runtime.
// Used by isAssignable. M0097-0003.
func isNumericOrIntegerTarget(name string) bool {
	switch strings.ToLower(name) {
	case "numeric", "decimal",
		"int2", "smallint", "serial2", "smallserial",
		"int4", "integer", "int", "serial4", "serial",
		"int8", "bigint", "serial8", "bigserial",
		"float", "float4", "real",
		"float8", "double precision", "double":
		return true
	}
	return false
}

// isExactNumericTextTarget reports whether dst is a column type
// whose v0 codec accepts string datums (NUMERIC / DECIMAL). Used
// by isAssignable to permit the HammerDB-shape INSERT pattern
// without weakening the assignment check for integer columns.
// Deprecated: use isNumericOrIntegerTarget which covers more types.
func isExactNumericTextTarget(name string) bool {
	switch strings.ToLower(name) {
	case "numeric", "decimal":
		return true
	}
	return false
}

// PGDisplayTypeName is the exported form of pgDisplayTypeName, for use by
// callers outside the analyzer package (e.g. function body validation).
func PGDisplayTypeName(name string) string { return pgDisplayTypeName(name) }

// pgDisplayTypeName converts internal type names to the PG-compatible
// display form used in error messages (e.g. "int8" → "integer"). M0097-0063.
// suggestConflictColumnHint returns a PG-style hint for an ON CONFLICT
// inference column that doesn't exist. It looks for a similar column in both
// the target table (qualified with tblName) and the excluded pseudo-table,
// producing e.g. `Perhaps you meant to reference the column "t.key" or the
// column "excluded.key".`
func suggestConflictColumnHint(cols []catalog.Column, tblName, want string) string {
	wl := strings.ToLower(want)
	var tblMatch, exclMatch string
	for _, c := range cols {
		cl := strings.ToLower(c.Name)
		if cl == wl {
			return ""
		}
		if analyzerColumnEditDistance1(cl, wl) {
			if tblMatch == "" {
				tblMatch = fmt.Sprintf("%q", tblName+"."+c.Name)
			}
			if exclMatch == "" {
				exclMatch = fmt.Sprintf("%q", "excluded."+c.Name)
			}
		}
	}
	if tblMatch == "" {
		return ""
	}
	if exclMatch != "" && exclMatch != tblMatch {
		return fmt.Sprintf("Perhaps you meant to reference the column %s or the column %s.", tblMatch, exclMatch)
	}
	return fmt.Sprintf("Perhaps you meant to reference the column %s.", tblMatch)
}

// suggestAnalyzerColumnHint returns a HINT when a column in cols is within
// edit distance 1 of want. Mirrors suggestColumnHint in the planner.
func suggestAnalyzerColumnHint(cols []catalog.Column, qualifier, want string) string {
	wl := strings.ToLower(want)
	for _, c := range cols {
		cl := strings.ToLower(c.Name)
		if cl == wl {
			return ""
		}
		if analyzerColumnEditDistance1(cl, wl) {
			return fmt.Sprintf("Perhaps you meant to reference the column %q.", qualifier+"."+c.Name)
		}
	}
	return ""
}

func analyzerColumnEditDistance1(a, b string) bool {
	la, lb := len(a), len(b)
	if la == lb {
		diff := 0
		for i := range a {
			if a[i] != b[i] {
				diff++
			}
		}
		return diff == 1
	}
	if la > lb {
		a, b, la, lb = b, a, lb, la
	}
	if lb-la > 1 {
		return false
	}
	for i := range b {
		candidate := b[:i] + b[i+1:]
		if candidate == a {
			return true
		}
	}
	return false
}

func pgDisplayTypeName(name string) string {
	switch strings.ToLower(name) {
	case "int2", "smallint":
		return "smallint"
	case "int4", "int", "integer":
		return "integer"
	case "int8", "bigint":
		return "integer" // PG treats unqualified integer literals as "integer" in errors
	case "float4", "real":
		return "real"
	case "float8", "double precision", "double":
		return "double precision"
	case "bool", "boolean":
		return "boolean"
	case "text", "varchar", "bpchar", "char":
		return "text"
	default:
		return name
	}
}
