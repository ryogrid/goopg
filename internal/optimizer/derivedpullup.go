package optimizer

import (
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0028 — pull simple FROM-clause subqueries into the parent join search
// (slice 1). Design: docs/design/0100-0149/m0146-0028-from-subquery-pullup.md.
//
// PG's `pull_up_subqueries` → `pull_up_simple_subquery` (prepjointree.c)
// replaces an `is_simple_subquery` RTE_SUBQUERY by the subquery's own FROM
// items and quals, and rewrites every reference to the subquery's outputs
// through `pullup_replace_vars`. The parent's join search then sees the
// body's relations as its own: TPC-DS Q59 joins subquery x's `wss ⋈ store`
// to the whole of subquery y and adds x's `date_dim` last, an order goopg
// could not reach while each subquery was planned as a separate problem.
//
// Slice 1 covers the shape whose `pullup_replace_vars` is a pure renaming:
//
//   - the parent FROM is a comma list with at least two items, none LATERAL
//     and none a table function, and the statement takes no row locks;
//   - the derived item is a non-LATERAL `(SELECT …) alias` with no column
//     alias list and no JOIN attached to it;
//   - its body is a bare SELECT (no WITH, set operation, grouping, HAVING,
//     DISTINCT, ORDER BY, LIMIT/OFFSET, window clause, VALUES or locking)
//     over a comma list of plain relation names (tables or CTE references);
//   - every body target is a bare column reference, output names unique;
//   - the body WHERE is made only of expression kinds collectExprColumnNames
//     enumerates (no sublinks).
//
// Mechanism. The body's FROM items replace the derived item in the list the
// FROM walk plans, so they become ordinary leaves of the parent's search.
// Their bindings are `pulledHidden`: invisible to parent-level name
// resolution, as PG's pulled-up RTEs are unreachable by name from the
// parent. The derived alias survives as a `pulledDerivedRel` on the resolve
// context, which maps each output name to the body column it renames; the
// body WHERE is resolved against the body's own bindings and ANDed into the
// parent's WHERE. Anything that fails to resolve abandons the pull-up and the
// FROM clause is planned as before.

// pulledDerivedRel is a pulled-up derived table as parent-level name
// resolution sees it: the alias, and per output column the resolved body
// column it stands for (parent coordinates).
type pulledDerivedRel struct {
	alias string
	names []string
	cols  []*ColumnRef
	// firstBinding is the index of the body's first leaf binding in the
	// parent's bindings: `SELECT *` emits the derived columns there, in
	// FROM order.
	firstBinding int
	body         *parser.SelectStmt
}

// derivedPullupCandidate records one derived item chosen for pull-up while
// the FROM list is being expanded.
type derivedPullupCandidate struct {
	alias    string
	body     *parser.SelectStmt
	itemLo   int // first expanded item of the body
	itemHi   int // one past the last
	bindLo   int
	bindHi   int
	outNames []string
}

// derivedPullupDisabled is the per-call fallback switch: planFromClause
// re-plans with it set when a pull-up attempt fails to resolve.
type derivedPullupMode int

const (
	derivedPullupOn derivedPullupMode = iota
	derivedPullupOff
)

// parentFromAdmitsDerivedPullup is slice 1's parent-side gate.
func parentFromAdmitsDerivedPullup(s *parser.SelectStmt) bool {
	if s == nil || len(s.FromExprs) < 2 || len(s.Locking) > 0 {
		return false
	}
	for _, it := range s.FromExprs {
		if it.Base.Lateral || it.Base.TableFunc != nil {
			return false
		}
		for _, j := range it.Joins {
			if j.Right.Lateral || j.Right.TableFunc != nil {
				return false
			}
		}
	}
	return true
}

// simpleDerivedPullupBody is slice 1's `is_simple_subquery` for one FROM item:
// it returns the body and its output names when the item may be pulled up.
func simpleDerivedPullupBody(it parser.FromExpr) (*parser.SelectStmt, []string, bool) {
	rv := it.Base
	sub := rv.Subquery
	if sub == nil || len(it.Joins) > 0 || rv.Lateral || rv.Alias == "" || len(rv.Columns) > 0 ||
		rv.TableFunc != nil || rv.TableSample != nil {
		return nil, nil, false
	}
	if sub.With != nil || sub.SetOp != nil || sub.SetOpOperand != nil ||
		len(sub.GroupBy) > 0 || sub.GroupingSets != nil || sub.Having != nil ||
		sub.Distinct || len(sub.DistinctOn) > 0 || len(sub.OrderBy) > 0 ||
		sub.Limit != nil || sub.Offset != nil || sub.WithTies ||
		len(sub.WindowClause) > 0 || len(sub.ValuesRows) > 0 || len(sub.Locking) > 0 ||
		len(sub.FromExprs) == 0 || len(sub.Targets) == 0 {
		return nil, nil, false
	}
	for _, f := range sub.FromExprs {
		b := f.Base
		if len(f.Joins) > 0 || b.Subquery != nil || b.TableFunc != nil || b.TableSample != nil ||
			b.Lateral || len(b.Columns) > 0 || b.GroupedJoinUnaliased || b.Name == "" {
			return nil, nil, false
		}
	}
	names := make([]string, 0, len(sub.Targets))
	seen := make(map[string]bool, len(sub.Targets))
	for _, t := range sub.Targets {
		cr, ok := t.Expr.(*parser.ColumnRef)
		if !ok || cr.Column == "" || cr.Column == "*" {
			return nil, nil, false
		}
		name := t.Alias
		if name == "" {
			name = cr.Column
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, nil, false
		}
		seen[key] = true
		names = append(names, name)
	}
	if sub.Where != nil && (!collectExprColumnNames(sub.Where, map[string]bool{}) || parserExprHasSelect(reflect.ValueOf(sub.Where), 0)) {
		return nil, nil, false
	}
	return sub, names, true
}

// parserExprHasSelect reports whether a nested SELECT (any sublink form) is
// reachable from v through exported fields. Fails closed past the depth
// bound. Slice 1 leaves sublink-bearing bodies to the ordinary derived leaf:
// PG pulls them up and lets pull_up_sublinks see them in the parent, which
// this slice does not rebase.
func parserExprHasSelect(v reflect.Value, depth int) bool {
	if depth > 64 {
		return true
	}
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		if v.IsNil() {
			return false
		}
		if v.Kind() == reflect.Ptr && v.CanInterface() {
			if _, ok := v.Interface().(*parser.SelectStmt); ok {
				return true
			}
		}
		return parserExprHasSelect(v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanInterface() && parserExprHasSelect(f, depth+1) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if parserExprHasSelect(v.Index(i), depth+1) {
				return true
			}
		}
	}
	return false
}

// expandDerivedPullups returns the FROM list the walk should plan — each
// admitted derived item replaced by its body's FROM items — and the
// candidates in expanded-item coordinates. With nothing admitted it returns
// the input list and nil.
func expandDerivedPullups(s *parser.SelectStmt, mode derivedPullupMode) ([]parser.FromExpr, []*derivedPullupCandidate) {
	if mode == derivedPullupOff || !parentFromAdmitsDerivedPullup(s) {
		return s.FromExprs, nil
	}
	var out []parser.FromExpr
	var cands []*derivedPullupCandidate
	for _, it := range s.FromExprs {
		body, names, ok := simpleDerivedPullupBody(it)
		if !ok {
			out = append(out, it)
			continue
		}
		c := &derivedPullupCandidate{alias: it.Base.Alias, body: body, itemLo: len(out), outNames: names}
		out = append(out, body.FromExprs...)
		c.itemHi = len(out)
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return s.FromExprs, nil
	}
	return out, cands
}

// resolvePulledDerived builds, for every candidate, the parent-level view
// (pulledDerivedRel) and the body's resolved WHERE conjuncts. The body is
// resolved against its own leaf bindings only — the parent's other FROM items
// are invisible to a non-LATERAL subquery — with the enclosing query levels as
// its parent chain, at the parent's level (pull-up flattens one level away).
// Any resolution failure, or a target that does not resolve to a plain
// current-level column, returns ok=false and the caller re-plans without
// pull-up.
func resolvePulledDerived(cands []*derivedPullupCandidate, bindings []rangeBinding, schema Schema, cat catalog.Catalog, ps PlannerSettings, scope *rtableScope) ([]*pulledDerivedRel, []Expr, bool) {
	var rels []*pulledDerivedRel
	var quals []Expr
	for _, c := range cands {
		bodyBindings := make([]rangeBinding, 0, c.bindHi-c.bindLo)
		for _, b := range bindings[c.bindLo:c.bindHi] {
			b.pulledHidden = false
			bodyBindings = append(bodyBindings, b)
		}
		bodyCtx := newResolveContext(bodyBindings, schema, ps)
		bodyCtx.cat = cat
		bodyCtx.rtScope = scope
		bodyCtx.parent = planParent
		rel := &pulledDerivedRel{alias: c.alias, names: c.outNames, firstBinding: c.bindLo, body: c.body}
		for _, t := range c.body.Targets {
			e, err := resolveExpr(t.Expr, bodyCtx)
			if err != nil {
				return nil, nil, false
			}
			cr, ok := e.(*ColumnRef)
			if !ok {
				return nil, nil, false
			}
			rel.cols = append(rel.cols, cr)
		}
		if c.body.Where != nil {
			q, err := resolveExpr(canonicalizeQual(c.body.Where), bodyCtx)
			if err != nil {
				return nil, nil, false
			}
			// The same constant folding the parent's own WHERE gets
			// (planSelect): a range bound written `1195 + 11` must reach
			// the estimator as a constant.
			if q, err = foldQualConstants(q); err != nil {
				return nil, nil, false
			}
			quals = append(quals, q)
		}
		rels = append(rels, rel)
	}
	return rels, quals, true
}

// resolvePulledDerivedColumn is resolveColumnRefAt's arm for pulled-up
// derived tables. It reports every match — the caller folds them into its
// own ambiguity accounting — for a qualified reference (`y.col`, which can
// match only the alias) or an unqualified one.
func resolvePulledDerivedColumn(x *parser.ColumnRef, ctx *resolveContext, level int) []Expr {
	var out []Expr
	for _, r := range ctx.pulledDerived {
		if x.Schema != "" {
			continue
		}
		if x.Table != "" && !strings.EqualFold(x.Table, r.alias) {
			continue
		}
		for i, n := range r.names {
			if strings.EqualFold(n, x.Column) {
				out = append(out, pulledDerivedRef(r.cols[i], x.Pos(), level))
			}
		}
	}
	return out
}

// pulledDerivedAliasMatches reports whether a qualifier names a pulled-up
// derived table at this level.
func pulledDerivedAliasMatches(ctx *resolveContext, table, schema string) *pulledDerivedRel {
	if schema != "" {
		return nil
	}
	for _, r := range ctx.pulledDerived {
		if strings.EqualFold(table, r.alias) {
			return r
		}
	}
	return nil
}

// pulledDerivedRef is the parent-level reference to one derived output: the
// body column it renames, at the requested query level. It keeps the body
// column's own name and source identity, because the plan's schema at that
// slot is the body column's; the output name the user wrote is restored by
// targetMeta from the written reference.
func pulledDerivedRef(cr *ColumnRef, pos, level int) Expr {
	if level == 0 {
		c := *cr
		c.pos = pos
		return &c
	}
	return &OuterColumnRef{pos: pos, Level: level, Index: cr.Index, Name: cr.Name, Type: cr.Type, SourceTableIdx: cr.SourceTableIdx}
}

// pulledDerivedWholeRow is the whole-row reference to a pulled-up derived
// table (`SELECT y FROM (…) y, …`): a row of its outputs.
func pulledDerivedWholeRow(r *pulledDerivedRel, pos, level int) Expr {
	elems := make([]Expr, len(r.cols))
	types := make([]catalog.Type, len(r.cols))
	for i, cr := range r.cols {
		elems[i] = pulledDerivedRef(cr, pos, level)
		types[i] = cr.Type
	}
	return &RowExpr{pos: pos, Elems: elems, Types: types}
}

// pulledDerivedTable is the derived table as a catalog-shaped relation, for
// consumers that take relation column lists (the analyzer's outer scope).
func pulledDerivedTable(r *pulledDerivedRel) *catalog.Table {
	cols := make([]catalog.Column, len(r.names))
	for i, n := range r.names {
		cols[i] = catalog.Column{Name: n, Type: r.cols[i].Type}
	}
	return &catalog.Table{Name: r.alias, Columns: cols}
}

// addPulledBodyColumnNames widens the name-based needed/output column sets
// (pathindexonlyneed.go) with each pulled body's names: after the pull-up the
// body's relations are the parent's leaves, and their index-only paths must
// cover what the body reads. Over-stating is the safe direction there.
func addPulledBodyColumnNames(ctx *resolveContext) {
	for _, r := range ctx.pulledDerived {
		if ctx.neededColsKnown {
			if !collectStmtColumnNames(r.body, ctx.neededCols) {
				ctx.neededColsKnown = false
			}
		}
		if ctx.outputColsKnown {
			for _, cr := range r.cols {
				ctx.outputCols[cr.Name] = true
			}
		}
	}
}

// ordinalThroughStarTargets resolves a 1-based ORDER BY position that lands
// inside a star target: it walks the target list, expanding each star against
// ctx, and returns the expanded expression at that output position. ok=false
// when the position is not inside a star (the caller's substitution already
// handles plain targets) or an expansion fails.
func ordinalThroughStarTargets(pos int, targets []parser.ResTarget, ctx *resolveContext) (Expr, bool) {
	if pos < 1 {
		return nil, false
	}
	n := 0
	for _, t := range targets {
		star, isStar := t.Expr.(*parser.StarExpr)
		if !isStar {
			n++
			if n == pos {
				return nil, false
			}
			continue
		}
		exprs, _, err := expandStarTarget(star, ctx)
		if err != nil {
			return nil, false
		}
		if pos <= n+len(exprs) {
			return exprs[pos-n-1], true
		}
		n += len(exprs)
	}
	return nil, false
}
