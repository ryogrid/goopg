package planner

// Grouping-sets source sharing (M0125-0040, "C6").
//
// rewriteGroupingSets expands GROUP BY ROLLUP/CUBE/GROUPING SETS into the
// SQL:1999 §7.9 UNION ALL of one plain-GROUP-BY branch per set. That
// expansion is semantically exact and stays — but every branch carries its
// own copy of the query's FROM and WHERE, so an N-set construct plans and
// EXECUTES the whole join subtree N times. Measured at TPC-DS SF=0.5
// (analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0037/):
//
//	Q18 — 4 branches, 5 Multi-Way Hash Joins, 5 full catalog_sales scans
//	      (720,657 rows each)
//	Q67 — 8 branches, 9 MHJs, 9 full store_sales scans (1,439,608 rows each)
//
// PostgreSQL computes every level in ONE pass over ONE scan: nodeAgg.c's
// AGG_MIXED / AGG_SORTED phases (planner side preprocess_grouping_sets /
// consider_groupingsets_paths in postgres/src/backend/optimizer/plan/planner.c)
// keep one hash table per hashable set and feed them all from a single input
// stream. goopg has no such executor node.
//
// This file implements fix candidate (a) of the item's two — "materialize the
// common subtree once and let the N branches read it" — and it does so by
// reusing a mechanism goopg already has rather than adding an executor node:
// a non-recursive CTE is materialized on first reference and REPLAYED from
// ctx.CTERowCache by every later reference (internal/executor/operators_cte_dml.go,
// cteScanOp.Open). So the rewrite hoists FROM+WHERE into a synthetic CTE and
// points every generated branch at it:
//
//	SELECT i_item_id, ca_state, avg(x)      WITH __gs_src_42 AS (
//	FROM a, b, c                       =>     SELECT a.i_item_id AS __gs_c0, ...
//	WHERE <join+filter quals>                 FROM a, b, c WHERE <quals>)
//	GROUP BY ROLLUP(i_item_id, ca_state)    SELECT __gs_c0, __gs_c1, avg(__gs_c2)
//	                                        FROM __gs_src_42 GROUP BY __gs_c0, __gs_c1
//	                                        UNION ALL ... (one branch per set)
//
// The join then runs once and each branch aggregates over the cached rows.
// The CTE projects EXACTLY the columns the branches reference (never `*`), so
// the materialized footprint is the narrow projection, not the join's full
// width.
//
// It is deliberately NOT the faithful answer — that is fix candidate (b), a
// real multi-level grouping-sets aggregate, which also fixes the
// `Group Key: ()` plan shape and needs no materialization at all. See
// docs/design/0125-0040-grouping-sets-source-sharing.md §5 for why (b) is
// still owed and what it would replace here.
//
// FAIL-CLOSED is the governing rule of this file. Every helper returns a
// `ok bool` that is false for any shape it has not explicitly proven safe,
// and the caller then leaves the statement completely untouched — the query
// still runs, just with today's N-scan expansion. The hazards being closed
// off this way are:
//
//   - CORRELATION. If the grouping-sets SELECT is itself a correlated
//     subquery, its WHERE references an outer relation; a CTE body cannot be
//     correlated, so hoisting WHERE would change the meaning. Closed by
//     requiring every column reference outside the CTE body to resolve to a
//     FROM-clause base table via the catalog: an outer reference resolves
//     nowhere and aborts the rewrite.
//   - NAME SHADOWING. Sublinks in the target list or HAVING bring their own
//     FROM scope, in which an unqualified name may mean something else.
//     Closed by refusing any statement whose rewritten expressions contain a
//     subquery of any kind.
//   - UNKNOWN EXPRESSION SHAPES. A node type this file does not know how to
//     rewrite could hide a column reference, which after the hoist would
//     resolve against the CTE and fail (or, worse, bind to a same-named CTE
//     column). Closed by an exhaustive type switch with no default recursion:
//     an unhandled node aborts the rewrite instead of being passed through.

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// gsShareSource gates the rewrite. It ships ON — the N-scan expansion is a
// defect, not a tuning choice — but the knob stays so the two arms can be
// measured against each other on the benchmark clusters, which is the only
// shape that can express an A/B for a planner decision read from the server's
// environment (there is no GUC for it). `GOOPG_GS_SHARE_SOURCE=0` (or
// off/false/no) restores the pre-M0125-0040 plans exactly.
var gsShareSource atomic.Bool

func init() {
	gsShareSource.Store(parseGSShareSource(os.Getenv("GOOPG_GS_SHARE_SOURCE")))
}

// parseGSShareSource maps GOOPG_GS_SHARE_SOURCE to on/off. Only the explicit
// off-words disable it; everything else — including the empty string and any
// unparseable value — is the shipped default, so a typo cannot silently give
// an operator a planner production does not run (the M0125-0005 convention).
func parseGSShareSource(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no":
		return false
	default:
		return true
	}
}

// gsSourceColPrefix names the CTE's projected columns. The generated names
// are positional and internal; nothing outside this file may depend on them,
// and they can never collide with a user column because a user cannot write
// a leading `__` identifier through the grouping-sets path without also
// having written it in the FROM clause, where it would resolve to itself.
const gsSourceColPrefix = "__gs_c"

// gsRef is one distinct base-table column referenced by the parts of the
// statement that stay ABOVE the hoisted source (targets, HAVING, DISTINCT ON,
// and the grouping sets themselves).
type gsRef struct {
	qualifier string // FROM-clause alias (or table name when unaliased)
	column    string // column name as spelled in the catalog
	alias     string // generated __gs_cN name in the CTE's output
}

// shareGroupingSetsSource hoists s's FROM and WHERE into a synthetic
// materialized CTE and rewrites every expression that survives above it to
// read the CTE's columns, so the N branches rewriteGroupingSets is about to
// generate share ONE execution of the source.
//
// It reports whether the rewrite was applied. When it returns false, s is
// unmodified in every field — the guards all run before the first mutation,
// and the expression rewrite is performed into fresh slices that are only
// installed once every part has succeeded.
//
// Must be called from rewriteGroupingSets BEFORE the per-set branches are
// built and before the `universe`/`active` key sets are computed, so that
// those keys are derived from the already-rewritten (CTE-column) expressions
// and match the rewritten target list.
func shareGroupingSetsSource(s *parser.SelectStmt, spec *parser.GroupingSetsSpec, cat catalog.Catalog) bool {
	if !gsShareSource.Load() {
		return false
	}
	// One set is a plain GROUP BY after expansion — a single branch, a single
	// scan, nothing to share. Sharing it would only add a materialization.
	if spec == nil || len(spec.Sets) < 2 {
		return false
	}
	if cat == nil {
		return false
	}
	// Shapes whose interaction with the hoist has not been established.
	// ValuesRows/SetOpOperand have no FROM to hoist; an existing SetOp means
	// this statement is already a set-op branch and rewriteGroupingSets would
	// be splicing into a live chain; Locking must stay attached to the base
	// relations, which the hoist would hide behind a CTE; a WINDOW clause
	// puts window functions above the aggregate, and those are refused by the
	// expression rewriter anyway — refuse early and say so here.
	if s.ValuesRows != nil || s.SetOp != nil || s.SetOpOperand != nil ||
		len(s.Locking) > 0 || len(s.WindowClause) > 0 {
		return false
	}
	// The parser fills BOTH From (flattened range-var list) and FromExprs
	// (explicit JOIN structure) — a comma-FROM lands in FromExprs as one
	// join-free entry per item. Only the comma form is handled for now: it is
	// what the TPC-DS ROLLUP queries write, and it keeps alias/column
	// resolution to the single From list. An explicit-JOIN statement (any
	// FromExpr carrying Joins, or a FromExprs list that does not mirror From
	// one-for-one) keeps today's expansion.
	if len(s.From) == 0 {
		return false
	}
	if len(s.FromExprs) > 0 {
		if len(s.FromExprs) != len(s.From) {
			return false
		}
		for i, fe := range s.FromExprs {
			if len(fe.Joins) > 0 {
				return false
			}
			if fe.Base.Name != s.From[i].Name || fe.Base.Alias != s.From[i].Alias {
				return false
			}
		}
	}

	// Resolve the FROM list to catalog tables. Anything that is not a plain
	// base-table reference — a derived table, a table function, a LATERAL
	// item, a CTE reference, an unknown name — aborts, because the column
	// resolution below can only be done against a catalog table.
	type fromTable struct {
		qualifier string
		table     *catalog.Table
	}
	withNames := map[string]bool{}
	if s.With != nil {
		for _, c := range s.With.CTEs {
			withNames[strings.ToLower(c.Name)] = true
		}
	}
	tables := make([]fromTable, 0, len(s.From))
	seenQualifier := map[string]bool{}
	for _, rv := range s.From {
		if rv.Name == "" || rv.Subquery != nil || rv.TableFunc != nil || rv.Lateral {
			return false
		}
		if withNames[strings.ToLower(rv.Name)] || lookupPlannedCTE(rv.Name) != nil {
			return false
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok || tbl == nil {
			return false
		}
		q := rv.Alias
		if q == "" {
			q = rv.Name
		}
		q = strings.ToLower(q)
		if seenQualifier[q] {
			// Two FROM items sharing a qualifier is either a self-join
			// without aliases or a duplicate alias; unqualified resolution
			// below cannot tell them apart. Refuse.
			return false
		}
		seenQualifier[q] = true
		tables = append(tables, fromTable{qualifier: q, table: tbl})
	}

	// Collect every column reference that must survive above the hoist, and
	// resolve each to (qualifier, column). Order of discovery fixes the
	// generated column order, so the rewrite is deterministic for a given
	// statement text — plan snapshots stay comparable across runs.
	refs := map[string]*gsRef{}
	var order []*gsRef
	// register=false only checks that the reference resolves to a FROM-clause
	// table, without allocating a projected column. That is what WHERE needs:
	// it stays INSIDE the CTE body, where the base tables are still in scope,
	// so it must not be rewritten — but it must be proven free of outer
	// references, since a CTE body cannot be correlated.
	var resolveRef func(cr *parser.ColumnRef, register bool) (*gsRef, bool)
	resolveRef = func(cr *parser.ColumnRef, register bool) (*gsRef, bool) {
		if cr.Column == "" {
			return nil, false
		}
		var qualifier, column string
		if cr.Table != "" {
			q := strings.ToLower(cr.Table)
			var found *fromTable
			for i := range tables {
				if tables[i].qualifier == q {
					found = &tables[i]
					break
				}
			}
			if found == nil {
				return nil, false // outer reference, or an alias we do not model
			}
			col, ok := cat.LookupColumn(found.table, cr.Column)
			if !ok || col == nil {
				return nil, false
			}
			qualifier, column = found.qualifier, col.Name
		} else {
			var found *fromTable
			var foundCol *catalog.Column
			for i := range tables {
				col, ok := cat.LookupColumn(tables[i].table, cr.Column)
				if !ok || col == nil {
					continue
				}
				if found != nil {
					return nil, false // ambiguous — let the normal path report it
				}
				found, foundCol = &tables[i], col
			}
			if found == nil {
				return nil, false // resolves to nothing here => outer reference
			}
			qualifier, column = found.qualifier, foundCol.Name
		}
		if !register {
			return nil, true
		}
		key := qualifier + "." + strings.ToLower(column)
		if r, ok := refs[key]; ok {
			return r, true
		}
		r := &gsRef{
			qualifier: qualifier,
			column:    column,
			alias:     fmt.Sprintf("%s%d", gsSourceColPrefix, len(order)),
		}
		refs[key] = r
		order = append(order, r)
		return r, true
	}

	resolve := func(cr *parser.ColumnRef) (*gsRef, bool) { return resolveRef(cr, true) }
	verify := func(cr *parser.ColumnRef) (*gsRef, bool) { return resolveRef(cr, false) }

	// WHERE is moved into the CTE body verbatim, so it is not rewritten — but
	// it is walked with the verifying resolver, because a reference in it that
	// resolves to no FROM table is a correlation to an enclosing query, and a
	// CTE body cannot be correlated. Without this the hoist would turn a
	// working correlated subquery into a plan error. The walk also fails
	// closed on any sublink in WHERE, whose own correlation this cannot see.
	if s.Where != nil {
		if _, ok := rewriteGSExpr(s.Where, verify); !ok {
			return false
		}
	}

	// Rewrite into fresh slices; nothing is installed on s until all of it
	// succeeded.
	newTargets := make([]parser.ResTarget, len(s.Targets))
	for i, t := range s.Targets {
		e, ok := rewriteGSExpr(t.Expr, resolve)
		if !ok {
			return false
		}
		nt := t
		nt.Expr = e
		// A target with no AS clause takes its output name from the
		// expression, and for a (possibly cast/collated) bare column that
		// name IS the column's — which the rewrite has just replaced with a
		// generated one. Pin the original name explicitly, or every such
		// output column would be renamed to __gs_cN and the statement's own
		// ORDER BY would stop resolving. PG's rule is
		// FigureColname (postgres/src/backend/parser/parse_target.c), which
		// descends exactly these wrappers to find the name.
		if nt.Alias == "" {
			if name := gsImplicitTargetName(t.Expr); name != "" {
				nt.Alias = name
			}
		}
		newTargets[i] = nt
	}
	var newHaving parser.Expr
	if s.Having != nil {
		e, ok := rewriteGSExpr(s.Having, resolve)
		if !ok {
			return false
		}
		newHaving = e
	}
	newDistinctOn := make([]parser.Expr, len(s.DistinctOn))
	for i, d := range s.DistinctOn {
		e, ok := rewriteGSExpr(d, resolve)
		if !ok {
			return false
		}
		newDistinctOn[i] = e
	}
	newSets := make([][]parser.Expr, len(spec.Sets))
	for i, set := range spec.Sets {
		ns := make([]parser.Expr, len(set))
		for j, e := range set {
			re, ok := rewriteGSExpr(e, resolve)
			if !ok {
				return false
			}
			ns[j] = re
		}
		newSets[i] = ns
	}
	newGroupBy := make([]parser.Expr, len(s.GroupBy))
	for i, e := range s.GroupBy {
		re, ok := rewriteGSExpr(e, resolve)
		if !ok {
			return false
		}
		newGroupBy[i] = re
	}
	// Nothing to project means nothing to share (e.g. `GROUP BY CUBE()` over
	// count(*) only). The hoist would still cut the scan count, but a
	// zero-column CTE is a shape no other planner path produces, so refuse it
	// rather than special-case it downstream.
	if len(order) == 0 {
		return false
	}

	// The synthetic source. Its WHERE is s's, verbatim: every reference in it
	// is to a base table that stays inside this body, so it needs no rewrite.
	cteName := fmt.Sprintf("__gs_src_%d", spec.Pos())
	cteTargets := make([]parser.ResTarget, len(order))
	for i, r := range order {
		cteTargets[i] = parser.ResTarget{
			Alias: r.alias,
			Expr:  &parser.ColumnRef{Table: r.qualifier, Column: r.column},
		}
	}
	cteQuery := &parser.SelectStmt{
		Targets:   cteTargets,
		From:      s.From,
		FromExprs: s.FromExprs,
		Where:     s.Where,
	}
	cte := &parser.CommonTableExpr{
		Name:  cteName,
		Query: cteQuery,
		// Explicit even though goopg materializes every non-recursive CTE
		// today: sharing is this rewrite's whole purpose, so it must not
		// become an inlining candidate if that policy ever changes.
		Materialized: "materialized",
	}

	// Install. From here on s is modified, and every branch of the guard
	// chain above has already passed.
	if s.With == nil {
		s.With = &parser.WithClause{}
	}
	s.With.CTEs = append(s.With.CTEs, cte)
	s.From = []parser.RangeVar{{Name: cteName}}
	s.FromExprs = nil
	s.Where = nil
	s.Targets = newTargets
	s.Having = newHaving
	s.DistinctOn = newDistinctOn
	s.GroupBy = newGroupBy
	spec.Sets = newSets
	return true
}

// rewriteGSExpr returns e with every base-table column reference replaced by
// an unqualified reference to the hoisted source's generated column, or
// ok=false if e contains anything this rewrite has not proven safe to move
// above a materialized CTE.
//
// The switch is exhaustive over parser's Expr implementations by design and
// has NO pass-through default: a node type added later fails closed here
// (the statement keeps today's N-scan expansion) instead of silently
// carrying a base-table reference into a scope where that table is no longer
// visible. The three refusals that are not merely "not yet handled":
//
//   - SubqueryExpr / ExistsExpr / ArraySubqueryExpr / InExpr-with-subquery:
//     a sublink introduces its own FROM scope, so an unqualified name inside
//     it may not mean what resolve() would decide, and a correlated one
//     references the very relations being hoisted.
//   - StarExpr / IndirectionStar: `*` expands against the FROM clause, which
//     the hoist replaces.
//   - FuncCall with Over != nil: a window function is evaluated above the
//     aggregate; combining one with grouping sets is outside what this
//     rewrite has been shown correct for.
func rewriteGSExpr(e parser.Expr, resolve func(*parser.ColumnRef) (*gsRef, bool)) (parser.Expr, bool) {
	if e == nil {
		return nil, true
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		r, ok := resolve(x)
		if !ok {
			return nil, false
		}
		if r == nil {
			// Verify-only mode (WHERE): the reference resolved, but it stays
			// inside the CTE body and must keep its original spelling.
			return e, true
		}
		return &parser.ColumnRef{Column: r.alias}, true

	// Literals and parameters carry no references.
	case *parser.IntegerConst, *parser.NumericConst, *parser.StringConst,
		*parser.BooleanConst, *parser.NullConst, *parser.ParamRef,
		*parser.IntervalLit, *parser.TypedStringLit, *parser.DefaultMarker:
		return e, true

	case *parser.BinaryOp:
		l, ok := rewriteGSExpr(x.Left, resolve)
		if !ok {
			return nil, false
		}
		r, ok := rewriteGSExpr(x.Right, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Left, c.Right = l, r
		return &c, true
	case *parser.UnaryOp:
		o, ok := rewriteGSExpr(x.Operand, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Operand = o
		return &c, true
	case *parser.CastExpr:
		o, ok := rewriteGSExpr(x.Operand, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Operand = o
		return &c, true
	case *parser.CollateExpr:
		o, ok := rewriteGSExpr(x.Operand, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Operand = o
		return &c, true
	case *parser.IsNullExpr:
		o, ok := rewriteGSExpr(x.Operand, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Operand = o
		return &c, true
	case *parser.IsBoolExpr:
		o, ok := rewriteGSExpr(x.Operand, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Operand = o
		return &c, true
	case *parser.IsDistinctFromExpr:
		l, ok := rewriteGSExpr(x.Left, resolve)
		if !ok {
			return nil, false
		}
		r, ok := rewriteGSExpr(x.Right, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Left, c.Right = l, r
		return &c, true
	case *parser.ExtractExpr:
		src, ok := rewriteGSExpr(x.Source, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Source = src
		return &c, true
	case *parser.ArraySubscriptExpr:
		b, ok := rewriteGSExpr(x.Base, resolve)
		if !ok {
			return nil, false
		}
		i, ok := rewriteGSExpr(x.Index, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Base, c.Index = b, i
		return &c, true
	case *parser.RowExpr:
		elems, ok := rewriteGSExprList(x.Elems, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Elems = elems
		return &c, true
	case *parser.ArrayConstructorExpr:
		elems, ok := rewriteGSExprList(x.Elements, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Elements = elems
		return &c, true
	case *parser.CaseExpr:
		c := *x
		if x.Operand != nil {
			o, ok := rewriteGSExpr(x.Operand, resolve)
			if !ok {
				return nil, false
			}
			c.Operand = o
		}
		whens := make([]parser.CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			wh, ok := rewriteGSExpr(w.When, resolve)
			if !ok {
				return nil, false
			}
			th, ok := rewriteGSExpr(w.Then, resolve)
			if !ok {
				return nil, false
			}
			whens[i] = parser.CaseWhen{When: wh, Then: th}
		}
		c.Whens = whens
		if x.Else != nil {
			el, ok := rewriteGSExpr(x.Else, resolve)
			if !ok {
				return nil, false
			}
			c.Else = el
		}
		return &c, true
	case *parser.GroupingCall:
		args, ok := rewriteGSExprList(x.Args, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Args = args
		return &c, true
	case *parser.InExpr:
		if x.Subquery != nil {
			return nil, false
		}
		op, ok := rewriteGSExpr(x.Operand, resolve)
		if !ok {
			return nil, false
		}
		list, ok := rewriteGSExprList(x.List, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Operand, c.List = op, list
		return &c, true
	case *parser.FuncCall:
		if x.Over != nil {
			return nil, false
		}
		args, ok := rewriteGSExprList(x.Args, resolve)
		if !ok {
			return nil, false
		}
		c := *x
		c.Args = args
		if x.Filter != nil {
			f, ok := rewriteGSExpr(x.Filter, resolve)
			if !ok {
				return nil, false
			}
			c.Filter = f
		}
		ob, ok := rewriteGSSortBys(x.OrderBy, resolve)
		if !ok {
			return nil, false
		}
		c.OrderBy = ob
		wg, ok := rewriteGSSortBys(x.WithinGroup, resolve)
		if !ok {
			return nil, false
		}
		c.WithinGroup = wg
		return &c, true

	default:
		// Sublinks, stars, and anything added to the AST after this was
		// written. Fail closed — see the doc comment.
		return nil, false
	}
}

// gsImplicitTargetName returns the output column name a target with no AS
// clause would take from e, but ONLY for the shapes whose name comes from a
// column reference — those are the ones the hoist would rename. Everything
// else (function calls, operators, literals) takes a name that does not
// depend on the columns underneath, so it survives the rewrite untouched and
// this returns "" for it.
//
// Mirrors the ColumnRef / TypeCast / CollateClause arms of PostgreSQL's
// FigureColname (postgres/src/backend/parser/parse_target.c).
func gsImplicitTargetName(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.ColumnRef:
		return x.Column
	case *parser.CastExpr:
		return gsImplicitTargetName(x.Operand)
	case *parser.CollateExpr:
		return gsImplicitTargetName(x.Operand)
	default:
		return ""
	}
}

func rewriteGSExprList(in []parser.Expr, resolve func(*parser.ColumnRef) (*gsRef, bool)) ([]parser.Expr, bool) {
	if in == nil {
		return nil, true
	}
	out := make([]parser.Expr, len(in))
	for i, e := range in {
		re, ok := rewriteGSExpr(e, resolve)
		if !ok {
			return nil, false
		}
		out[i] = re
	}
	return out, true
}

func rewriteGSSortBys(in []parser.SortBy, resolve func(*parser.ColumnRef) (*gsRef, bool)) ([]parser.SortBy, bool) {
	if in == nil {
		return nil, true
	}
	out := make([]parser.SortBy, len(in))
	for i, sb := range in {
		re, ok := rewriteGSExpr(sb.Expr, resolve)
		if !ok {
			return nil, false
		}
		out[i] = sb
		out[i].Expr = re
	}
	return out, true
}
