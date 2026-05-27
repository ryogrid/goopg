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
			return targets[idx].Expr
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

// analyzeSelectWithParent analyzes a SELECT with the supplied
// scope as lexical-scope parent. Used by SubqueryExpr /
// InExpr / ExistsExpr handlers when recursing into inner
// SELECTs so column refs can resolve against the outer scope.
func analyzeSelectWithParent(s *parser.SelectStmt, cat catalog.Catalog, parent *scope) error {
	// s.Distinct is now supported via the planner's Distinct node. M0097-0005.
	if s.SetOp != nil {
		// UNION / INTERSECT / EXCEPT (with optional ALL) are all
		// supported. Analyze the right side first (innermost first),
		// then the left side with SetOp temporarily cleared to avoid
		// infinite recursion. M0097-0024.
		if err := analyzeSelectWithParent(s.SetOp.Right, cat, parent); err != nil {
			return err
		}
		saved := s.SetOp
		s.SetOp = nil
		err := analyzeSelectWithParent(s, cat, parent)
		s.SetOp = saved
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

func analyzeInsert(s *parser.InsertStmt, cat catalog.Catalog) error {
	if s.With != nil {
		// Stage A: validate the CTE definitions (so type errors
		// inside the CTE bodies surface even though the INSERT
		// VALUES path doesn't yet consume them). The planner /
		// executor integration for WITH-prefixed INSERTs lands in
		// 0016-0002.
		ctx := &scope{cat: cat}
		if err := analyzeWith(s.With, ctx); err != nil {
			return err
		}
	}
	tbl, err := lookupTable(cat, s.Target)
	if err != nil {
		return err
	}
	// INSERT … SELECT: analyze the SELECT sub-statement and skip VALUES checks.
	if s.Select != nil {
		return analyzeSelectWithParent(s.Select, cat, nil)
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
		return analyzeError(oc.Pos(), "42601",
			"ON CONFLICT DO UPDATE requires inference specification or constraint name")
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
					return analyzeError(oc.Target.Pos(), "42703",
						fmt.Sprintf("column %q of relation %q does not exist", col, tbl.Name))
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
	ctx := &scope{
		cat: cat,
		rels: []scopeRel{
			{table: tbl, alias: primaryAlias},
			{table: tbl, alias: "excluded", qualifiedOnly: true},
		},
	}
	for _, assign := range oc.UpdateSet {
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
	if s.With != nil {
		if err := analyzeWith(s.With, ctx); err != nil {
			return err
		}
	}
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
	ctx := &scope{rels: []scopeRel{{table: tbl, alias: s.Target.Alias}}, cat: cat}
	if s.With != nil {
		if err := analyzeWith(s.With, ctx); err != nil {
			return err
		}
	}
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
		return catalog.Type{Name: "text"}, nil
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
	case *parser.CaseExpr:
		return analyzeCaseExpr(x, ctx)
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
		switch x.Op {
		case parser.OpUnaryPos, parser.OpUnaryNeg:
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
				// timetz has no + or - operator at all; other time types are "not unique".
				if strings.EqualFold(leftTyp.Name, "timetz") || strings.EqualFold(rightTyp.Name, "timetz") {
					ae := analyzeError(x.Pos(), "42883",
						fmt.Sprintf("operator does not exist: %s %s %s", lname, x.Op, rname))
					ae.Hint = "No operator matches the given name and argument types. You might need to add explicit type casts."
					return catalog.Type{}, ae
				}
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
			// M0097-pg_lsn.
			leftStr := isStringLike(leftTyp) || isUnknownType(leftTyp)
			rightStr := isStringLike(rightTyp) || isUnknownType(rightTyp)
			if !leftStr && !rightStr {
				return catalog.Type{}, analyzeError(x.Pos(), "42804", "operator || requires string operands")
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
	case *parser.ArraySubscriptExpr:
		// Array element access: expr[index] → element type. For text[] the element is text.
		// Just analyze sub-expressions and return text (most common case). M0097-0003.
		if _, err := analyzeExpr(x.Base, ctx); err != nil {
			return catalog.Type{}, err
		}
		if _, err := analyzeExpr(x.Index, ctx); err != nil {
			return catalog.Type{}, err
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
	default:
		return catalog.Type{}, analyzeError(e.Pos(), "0A000", fmt.Sprintf("unsupported expression %T", e))
	}
}

func analyzeWindowFuncCall(x *parser.FuncCall, ctx *scope) (catalog.Type, error) {
	name := strings.ToLower(x.Name.Name)
	var retType catalog.Type
	switch name {
	case "row_number", "rank":
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
	return retType, nil
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
			if scopeRelMatches(rel, x.Table, x.Schema) {
				matches = append(matches, rel)
			}
		}
		if len(matches) == 0 {
			return catalog.Type{}, false, nil
		}
		if len(matches) > 1 {
			return catalog.Type{}, false, analyzeError(x.Pos(), "42702", fmt.Sprintf("table reference %q is ambiguous", x.Table))
		}
		col, ok := lookupColumn(matches[0].table, x.Column)
		if !ok {
			// `<rel>.tableoid` system-column resolution. M0100-0005y.
			if strings.EqualFold(x.Column, "tableoid") {
				return catalog.Type{Name: "oid"}, true, nil
			}
			// `<rel>.ctid` system-column resolution. M0097-0038.
			if strings.EqualFold(x.Column, "ctid") {
				return catalog.Type{Name: "tid"}, true, nil
			}
			return catalog.Type{}, false, analyzeError(x.Pos(), "42703", fmt.Sprintf("column %q does not exist", x.Column))
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
		return synthesizeSubqueryTable(cat, rv)
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
		cols := tableFuncColumns(rv.TableFunc.Name, alias, rv.Columns)
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
func tableFuncColumns(funcName, alias string, colAliases []string) []catalog.Column {
	switch strings.ToLower(funcName) {
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
	body := cte.Query
	if body.SetOp == nil || body.SetOp.Type != parser.SetOpUnion || !body.SetOp.All {
		// No recursive self-join — just analyse the body as a
		// regular CTE.  The planner enforces the UNION ALL
		// requirement for true recursive CTEs.
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
	cols := make([]catalog.Column, 0, len(body.Targets))
	for i, tgt := range body.Targets {
		name := tgt.Alias
		if name == "" {
			name = deriveAnalyzerTargetName(tgt.Expr)
		}
		if name == "" {
			name = fmt.Sprintf("?column?%d", i+1)
		}
		typ, err := analyzeExpr(tgt.Expr, innerCtx)
		if err != nil {
			return err
		}
		cols = append(cols, catalog.Column{Name: name, Type: typ, Ordinal: i})
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
			// the outer query knows the name exists.
			if ctx.ctes == nil {
				ctx.ctes = make(map[string]*catalog.Table)
			}
			ctx.ctes[strings.ToLower(cte.Name)] = &catalog.Table{Name: cte.Name}
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
	if len(cte.Query.From) > 0 || len(cte.Query.FromExprs) > 0 {
		rels, err := buildSelectScopeIn(cte.Query, innerCtx)
		if err != nil {
			return err
		}
		innerCtx.rels = rels
	}
	innerCols := make([]catalog.Column, 0, len(cte.Query.Targets))
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
	if len(cte.Columns) > 0 {
		if len(cte.Columns) != len(innerCols) {
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
			rels = append(rels, scopeRel{table: tbl, alias: rv.Alias})
		}
		return rels, nil
	}
	rels := make([]scopeRel, 0, len(s.From))
	for _, item := range s.FromExprs {
		tbl, err := resolveTable(ctx, item.Base)
		if err != nil {
			return nil, err
		}
		rels = append(rels, scopeRel{table: tbl, alias: item.Base.Alias})
		for _, j := range item.Joins {
			rt, err := resolveTable(ctx, j.Right)
			if err != nil {
				return nil, err
			}
			rels = append(rels, scopeRel{table: rt, alias: j.Right.Alias, usingHidden: j.Using})
		}
	}
	return rels, nil
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

func synthesizeSubqueryTable(cat catalog.Catalog, rv parser.RangeVar) (*catalog.Table, error) {
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
	if err := analyzeSelectWithParent(rv.Subquery, cat, nil); err != nil {
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
	innerCtx := &scope{cat: cat}
	if len(rv.Subquery.From) > 0 || len(rv.Subquery.FromExprs) > 0 {
		rels, err := buildSelectScope(rv.Subquery, cat)
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
		"float4", "float8", "real", "double", "double precision",
		// SERIAL family are not real types: PostgreSQL resolves them to
		// int4/int8/int2 (pg_typeof reports "integer"). Treat them as their
		// integer base so `serial_col = 1` / `serial_col + 1` type-check the
		// same as int columns. The stored catalog type stays "serial" because
		// the INSERT auto-increment path (operators_storage.go) keys off it.
		"serial", "bigserial", "smallserial":
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
	isTextBacked := func(t catalog.Type) bool {
		switch strings.ToLower(t.Name) {
		case "uuid", "name", "oid", "oidvector", "int2vector", "pg_lsn":
			return true
		}
		return false
	}
	if (isTextBacked(left) || isStringTypeName(left.Name)) && (isTextBacked(right) || isStringTypeName(right.Name)) {
		return true
	}
	// oid ↔ integer: PostgreSQL has implicit oid↔int4 casts. M0097-0003.
	if strings.EqualFold(left.Name, "oid") && isNumericTypeName(right.Name) {
		return true
	}
	if isNumericTypeName(left.Name) && strings.EqualFold(right.Name, "oid") {
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
		"int2", "smallint",
		"int4", "integer", "int",
		"int8", "bigint",
		"float4", "real",
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
