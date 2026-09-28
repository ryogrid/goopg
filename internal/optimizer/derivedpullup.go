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
// Slice 1 covered the shape whose `pullup_replace_vars` is a pure renaming;
// slice 2 (M0146-0028b) admits expression targets and the lone FROM item;
// slice 3 (M0146-0028c) admits INNER / CROSS joins inside the body and
// slice 4 (M0146-0028d) outer joins inside it:
//
//   - the parent FROM is a comma list, none of its items LATERAL or a table
//     function, and the statement takes no row locks;
//   - the derived item is a non-LATERAL `(SELECT …) alias` with no column
//     alias list and no JOIN attached to it;
//   - its body is a bare SELECT (no WITH, set operation, grouping, HAVING,
//     DISTINCT, ORDER BY, LIMIT/OFFSET, window clause, VALUES or locking)
//     over a comma list of plain relation names (tables or CTE references),
//     each optionally joined to more of them (INNER, CROSS, LEFT, RIGHT,
//     FULL; no USING/NATURAL);
//   - every body target is an expression with no function call and no
//     sublink — which excludes, without a catalog lookup, everything PG's
//     is_simple_subquery refuses in a target list (aggregates, window
//     functions, set-returning and volatile functions) — output names
//     unique. With the parent a plain comma list no outer join can null the
//     derived item's outputs, so `pullup_replace_vars` needs no
//     PlaceHolderVar: an output expression is substituted as is;
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
	// cols is each output's resolved body expression, parent coordinates.
	// A reference receives its own deep copy (pulledDerivedRef).
	cols []Expr
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
	if s == nil || len(s.FromExprs) == 0 || len(s.Locking) > 0 {
		return false
	}
	// PG pulls up under grouping sets too, but wraps every substituted
	// output in a PlaceHolderVar (pull_up_simple_subquery:
	// `if (parse->groupingSets) rvcontext.wrap_option = REPLACE_WRAP_ALL`),
	// so that two outputs renaming one column stay two grouping columns.
	// goopg has no PlaceHolderVar; substituting bare columns there merges
	// them (regress groupingsets: `select four as x, four as y … grouping
	// sets (x, y)`).
	if s.GroupingSets != nil {
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

// simpleDerivedPullupBody is the `is_simple_subquery` gate for one FROM item:
// it returns the body when the item may be pulled up.
func simpleDerivedPullupBody(it parser.FromExpr) (*parser.SelectStmt, bool) {
	rv := it.Base
	sub := rv.Subquery
	if sub == nil || len(it.Joins) > 0 || rv.Lateral || rv.Alias == "" || len(rv.Columns) > 0 ||
		rv.TableFunc != nil || rv.TableSample != nil {
		return nil, false
	}
	if sub.With != nil || sub.SetOp != nil || sub.SetOpOperand != nil ||
		len(sub.GroupBy) > 0 || sub.GroupingSets != nil || sub.Having != nil ||
		sub.Distinct || len(sub.DistinctOn) > 0 || len(sub.OrderBy) > 0 ||
		sub.Limit != nil || sub.Offset != nil || sub.WithTies ||
		len(sub.WindowClause) > 0 || len(sub.ValuesRows) > 0 || len(sub.Locking) > 0 ||
		len(sub.FromExprs) == 0 || len(sub.Targets) == 0 {
		return nil, false
	}
	for _, f := range sub.FromExprs {
		if !pullupPlainRelation(f.Base) {
			return nil, false
		}
		// M0146-0028c/d: joins inside the body. PG pulls every join tree up
		// with the body — only an outer join AROUND the subquery needs
		// PlaceHolderVars. An inner join's ON clause is a WHERE qual in all
		// but name; an outer join (slice d) is demoted and reduced against
		// the BODY's WHERE (planFromClauseItems), exactly the quals that
		// stood above it before the pull-up. USING and NATURAL merge
		// columns through per-join resolve contexts the body re-resolution
		// does not rebuild.
		for _, j := range f.Joins {
			switch j.Type {
			case parser.JoinInner, parser.JoinCross, parser.JoinLeft, parser.JoinRight, parser.JoinFull:
			default:
				return nil, false
			}
			if j.Natural || len(j.Using) > 0 || !pullupPlainRelation(j.Right) {
				return nil, false
			}
			if j.On != nil && (!collectExprColumnNames(j.On, map[string]bool{}) ||
				parserExprHasNode(reflect.ValueOf(j.On), 0, parserNodeIsSelect)) {
				return nil, false
			}
		}
	}
	for _, t := range sub.Targets {
		if t.Expr == nil {
			return nil, false
		}
		if _, star := t.Expr.(*parser.StarExpr); star {
			return nil, false
		}
		if cr, ok := t.Expr.(*parser.ColumnRef); ok && (cr.Column == "" || cr.Column == "*") {
			return nil, false
		}
		if !collectExprColumnNames(t.Expr, map[string]bool{}) ||
			parserExprHasNode(reflect.ValueOf(t.Expr), 0, parserNodeIsSelectOrCall) {
			return nil, false
		}
	}
	if sub.Where != nil && (!collectExprColumnNames(sub.Where, map[string]bool{}) ||
		parserExprHasNode(reflect.ValueOf(sub.Where), 0, parserNodeIsSelect)) {
		return nil, false
	}
	return sub, true
}

// pulledCandidateOwning returns the candidate whose expanded body items
// include item index i, or nil.
func pulledCandidateOwning(cands []*derivedPullupCandidate, i int) *derivedPullupCandidate {
	for _, c := range cands {
		if i >= c.itemLo && i < c.itemHi {
			return c
		}
	}
	return nil
}

// pullupPlainRelation reports whether a body FROM item is a plain relation
// name (a table or a CTE reference) — the only leaf kind the pull-up splices.
func pullupPlainRelation(b parser.RangeVar) bool {
	return b.Subquery == nil && b.TableFunc == nil && b.TableSample == nil && !b.Lateral &&
		len(b.Columns) == 0 && !b.GroupedJoinUnaliased && b.Name != ""
}

func parserNodeIsSelect(x any) bool {
	_, ok := x.(*parser.SelectStmt)
	return ok
}

func parserNodeIsSelectOrCall(x any) bool {
	if _, ok := x.(*parser.FuncCall); ok {
		return true
	}
	return parserNodeIsSelect(x)
}

// parserExprHasNode reports whether a pointer node matching `want` is
// reachable from v through exported fields — a nested SELECT (any sublink
// form) or a function call. Fails closed past the depth bound. Sublink-bearing
// bodies stay ordinary derived leaves: PG pulls them up and lets
// pull_up_sublinks see them in the parent, which this pull-up does not rebase.
func parserExprHasNode(v reflect.Value, depth int, want func(any) bool) bool {
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
		if v.Kind() == reflect.Ptr && v.CanInterface() && want(v.Interface()) {
			return true
		}
		return parserExprHasNode(v.Elem(), depth+1, want)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanInterface() && parserExprHasNode(f, depth+1, want) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if parserExprHasNode(v.Index(i), depth+1, want) {
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
func expandDerivedPullups(s *parser.SelectStmt, mode derivedPullupMode) ([]parser.FromExpr, []*derivedPullupCandidate, []parser.Expr) {
	if mode == derivedPullupOff || !parentFromAdmitsDerivedPullup(s) {
		return s.FromExprs, nil, nil
	}
	// M0146-0028e: an all-INNER join chain carrying a pullable derived
	// operand is split into comma items first; its ON clauses become
	// parent-level quals (splitInnerJoinChainForPullup).
	var onQuals []parser.Expr
	var flat []parser.FromExpr
	for _, it := range s.FromExprs {
		if pieces, ons, ok := splitInnerJoinChainForPullup(it); ok {
			flat = append(flat, pieces...)
			onQuals = append(onQuals, ons...)
			continue
		}
		flat = append(flat, it)
	}
	var out []parser.FromExpr
	var cands []*derivedPullupCandidate
	for _, it := range flat {
		body, ok := simpleDerivedPullupBody(it)
		if !ok {
			out = append(out, it)
			continue
		}
		c := &derivedPullupCandidate{alias: it.Base.Alias, body: body, itemLo: len(out)}
		out = append(out, body.FromExprs...)
		c.itemHi = len(out)
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return s.FromExprs, nil, nil
	}
	return out, cands, onQuals
}

// splitInnerJoinChainForPullup is M0146-0028e: PG pulls a simple subquery up
// wherever it sits in the jointree, including as an operand of an explicit
// INNER JOIN, and an inner join's ON clause is a WHERE qual in all but name
// (deconstruct_jointree distributes both alike). goopg's pull-up splices FROM
// items, so an item `a JOIN (SELECT …) s ON c1 JOIN b ON c2` whose chain is
// INNER/CROSS only is split into the comma items a, s, b with c1, c2 moved to
// the statement's quals — only when at least one operand is a pullable
// derived table, so every other chain keeps its written form.
//
// Declined: any outer, USING or NATURAL link (the chain is not a pure inner
// product then); an ON clause with a sublink; and an ON clause with an
// UNQUALIFIED column reference, because an ON clause sees only its join's
// inputs while a WHERE qual sees every FROM item — moving `x = 1` could turn
// a unique name ambiguous or bind it to a later item.
func splitInnerJoinChainForPullup(it parser.FromExpr) ([]parser.FromExpr, []parser.Expr, bool) {
	if len(it.Joins) == 0 {
		return nil, nil, false
	}
	operands := []parser.RangeVar{it.Base}
	var ons []parser.Expr
	for _, j := range it.Joins {
		if (j.Type != parser.JoinInner && j.Type != parser.JoinCross) || j.Natural || len(j.Using) > 0 {
			return nil, nil, false
		}
		if j.On != nil {
			if !collectExprColumnNames(j.On, map[string]bool{}) ||
				parserExprHasNode(reflect.ValueOf(j.On), 0, parserNodeIsSelect) ||
				parserExprHasNode(reflect.ValueOf(j.On), 0, parserNodeIsUnqualifiedColumn) {
				return nil, nil, false
			}
			ons = append(ons, j.On)
		}
		operands = append(operands, j.Right)
	}
	anyDerived := false
	for _, rv := range operands {
		if rv.Lateral || rv.TableFunc != nil || rv.GroupedJoinUnaliased {
			return nil, nil, false
		}
		if _, ok := simpleDerivedPullupBody(parser.FromExpr{Base: rv}); ok {
			anyDerived = true
		}
	}
	if !anyDerived {
		return nil, nil, false
	}
	pieces := make([]parser.FromExpr, 0, len(operands))
	for _, rv := range operands {
		pieces = append(pieces, parser.FromExpr{Base: rv})
	}
	return pieces, ons, true
}

func parserNodeIsUnqualifiedColumn(x any) bool {
	cr, ok := x.(*parser.ColumnRef)
	return ok && cr.Table == "" && cr.Schema == ""
}

// resolvePulledDerived builds, for every candidate, the parent-level view
// (pulledDerivedRel) and the body's resolved WHERE conjuncts. The body is
// resolved against its own leaf bindings only — the parent's other FROM items
// are invisible to a non-LATERAL subquery — with the enclosing query levels as
// its parent chain, at the parent's level (pull-up flattens one level away).
// Output names are targetMeta's — the names the body's own projection would
// give its columns when planned as a separate scope. Any resolution failure,
// or a duplicate output name, returns ok=false and the caller re-plans
// without pull-up.
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
		rel := &pulledDerivedRel{alias: c.alias, firstBinding: c.bindLo, body: c.body}
		seen := map[string]bool{}
		for _, t := range c.body.Targets {
			e, err := resolveExpr(t.Expr, bodyCtx)
			if err != nil {
				return nil, nil, false
			}
			if e, err = foldQualConstants(e); err != nil {
				return nil, nil, false
			}
			name, _ := targetMeta(e, t)
			key := strings.ToLower(name)
			if seen[key] {
				return nil, nil, false
			}
			seen[key] = true
			rel.names = append(rel.names, name)
			rel.cols = append(rel.cols, e)
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

// pulledDerivedRef is the parent-level reference to one derived output — the
// body expression it stands for (`pullup_replace_vars`), as a fresh copy, at
// the requested query level: a reference from a sublink `level` scopes below
// reads every body column as an OuterColumnRef of that level. A bare column
// keeps the body column's own name and source identity, because the plan's
// schema at that slot is the body column's; the output name the user wrote is
// restored by targetMeta from the written reference.
func pulledDerivedRef(e Expr, pos, level int) Expr {
	if cr, ok := e.(*ColumnRef); ok {
		if level == 0 {
			c := *cr
			c.pos = pos
			return &c
		}
		return &OuterColumnRef{pos: pos, Level: level, Index: cr.Index, Name: cr.Name, Type: cr.Type, SourceTableIdx: cr.SourceTableIdx}
	}
	out, ok := cloneExprRefs(e, scopeIgnore, exprRewriter{Rewrite: func(x Expr) Expr {
		if cr, isCol := x.(*ColumnRef); isCol && level > 0 {
			return &OuterColumnRef{pos: cr.pos, Level: level, Index: cr.Index, Name: cr.Name, Type: cr.Type, SourceTableIdx: cr.SourceTableIdx}
		}
		return x
	}})
	if !ok {
		// resolvePulledDerived only admits call-free, sublink-free
		// expressions, which the exhaustive walker always enumerates.
		panic("pulledDerivedRef: derived output expression is not cloneable")
	}
	return out
}

// pulledDerivedSchemaColumn is the schema entry `SELECT *` gives an output.
func pulledDerivedSchemaColumn(name string, e Expr) SchemaColumn {
	sc := SchemaColumn{Name: name, Type: exprType(e)}
	if cr, ok := e.(*ColumnRef); ok {
		sc.SourceTableIdx = cr.SourceTableIdx
	}
	return sc
}

// pulledDerivedWholeRow is the whole-row reference to a pulled-up derived
// table (`SELECT y FROM (…) y, …`): a row of its outputs.
func pulledDerivedWholeRow(r *pulledDerivedRel, pos, level int) Expr {
	elems := make([]Expr, len(r.cols))
	types := make([]catalog.Type, len(r.cols))
	for i, e := range r.cols {
		elems[i] = pulledDerivedRef(e, pos, level)
		types[i] = exprType(e)
	}
	return &RowExpr{pos: pos, Elems: elems, Types: types}
}

// pulledDerivedTable is the derived table as a catalog-shaped relation, for
// consumers that take relation column lists (the analyzer's outer scope).
func pulledDerivedTable(r *pulledDerivedRel) *catalog.Table {
	cols := make([]catalog.Column, len(r.names))
	for i, n := range r.names {
		cols[i] = catalog.Column{Name: n, Type: exprType(r.cols[i])}
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
			for _, e := range r.cols {
				walkExprTree(e, func(x Expr) {
					if cr, ok := x.(*ColumnRef); ok {
						ctx.outputCols[cr.Name] = true
					}
				})
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
