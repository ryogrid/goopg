package optimizer

import (
	"reflect"
	"sort"
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
	alias string
	// columns is the item's column-alias list (`AS x(c1, c2)`), naming the
	// first len(columns) outputs; nil when absent (M0146-0028g).
	columns []string
	// lateral marks a LATERAL item of the statement's own FROM list
	// (M0146-0028h): its body resolves with the items to its left as the
	// enclosing scope, and those references become plain columns.
	lateral bool
	body    *parser.SelectStmt
	itemLo  int // first expanded item of the body
	itemHi  int // one past the last
	bindLo  int
	bindHi  int
	// cte is set when the item was a reference to an inlinable CTE,
	// presented as its body (M0146-0007e, ctepullup.go).
	cte *plannedCTE
	// parent is the pulled body whose FROM list held this item (nil for an
	// item of the statement's own FROM list), and depth its nesting level.
	// pull_up_simple_subquery flattens the subquery's own simple subqueries
	// before splicing it, so a pulled body's FROM items are expanded in
	// turn (M0146-0007i): its items' ranges nest inside the parent's.
	parent *derivedPullupCandidate
	depth  int
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
		// M0146-0028h: a join-free LATERAL subquery item may itself be
		// pulled up; expandDerivedPullups declines the whole statement
		// when one is not.
		if it.Base.TableFunc != nil || (it.Base.Lateral && (it.Base.Subquery == nil || len(it.Joins) > 0)) {
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
func simpleDerivedPullupBody(it parser.FromExpr, cat catalog.Catalog) (*parser.SelectStmt, bool) {
	rv := it.Base
	sub := rv.Subquery
	if sub == nil || len(it.Joins) > 0 || rv.Lateral || rv.Alias == "" ||
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
		// M0146-0007i: a join-free derived item that is itself simple is
		// flattened with the body (pull_up_simple_subquery recursing through
		// pull_up_subqueries first).
		if f.Base.Subquery != nil && len(f.Joins) == 0 {
			if _, ok := simpleDerivedPullupBody(f, cat); ok {
				continue
			}
			// M0146-0065: a simple UNION ALL item is flattened into an
			// appendrel by that same recursion (pull_up_simple_union_all),
			// so it does not make the body unsimple. It stays a derived
			// leaf of the parent, planned as the appendrel it is.
			if pullupUnionAllLeaf(f.Base) {
				continue
			}
		}
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
	// M0146-0119: `*` and `alias.*` stand for the columns parse analysis
	// expands them to (transformTargetList / ExpandColumnRefStar), so the
	// body PG pulls up has no star left in it.
	if expanded, ok := expandPullupBodyStars(sub, cat); !ok {
		return nil, false
	} else if expanded != nil {
		cp := *sub
		cp.Targets = expanded
		sub = &cp
	}
	// M0146-0028g: a column-alias list renames the first outputs
	// (resolvePulledDerived). More aliases than outputs is the parse-analysis
	// error PG raises; the unpulled path reports it.
	if len(rv.Columns) > len(sub.Targets) {
		return nil, false
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
			parserExprHasNode(reflect.ValueOf(t.Expr), 0, func(x any) bool {
				if fc, ok := x.(*parser.FuncCall); ok {
					return !pullupSafeTargetCall(fc, cat)
				}
				return parserNodeIsSelect(x)
			}) {
			return nil, false
		}
	}
	// M0146-0028f: a sublink in the body WHERE no longer declines.
	// pull_up_simple_subquery runs pull_up_sublinks on the subquery before
	// splicing it (prepjointree.c), so its sublinks reach the parent as
	// ordinary quals; here the body WHERE is resolved in the body's context
	// (its correlated references bind to the pulled leaves) and ANDed into
	// the parent's WHERE, where the statement's own sublink processing
	// applies to it. Targets and ON clauses keep the decline. The body WHERE
	// need not be name-enumerable: the only name-based consumer of it,
	// addPulledBodyColumnNames, turns the needed-column set unknown (no
	// index-only pruning) when collectStmtColumnNames cannot enumerate it.
	return sub, true
}

// expandPullupBodyStars replaces each `*` / `alias.*` target of a pulled
// body with qualified column references to the FROM items it stands for, in
// FROM order: a table's live columns from the catalog, a derived item's (or
// an inlinable CTE reference's) output names. It returns nil, true when the
// body has no star, and false — the body stays unpulled — when an item's
// columns cannot be named without resolving it: an unaliased or unpullable
// derived item, an output that is neither aliased nor a plain column, a
// NATURAL / USING join (they merge columns), or an unknown relation.
func expandPullupBodyStars(sub *parser.SelectStmt, cat catalog.Catalog) ([]parser.ResTarget, bool) {
	hasStar := false
	for _, t := range sub.Targets {
		if isPullupStarTarget(t.Expr) {
			hasStar = true
		}
	}
	if !hasStar {
		return nil, true
	}
	if cat == nil {
		return nil, false
	}
	type fromItem struct {
		qual string
		cols []string
	}
	var items []fromItem
	add := func(rv parser.RangeVar) bool {
		item := parser.FromExpr{Base: rv}
		if conv, _, ok := cteAsDerivedItem(item); ok {
			item = conv
		} else if e := planCTEs[strings.ToLower(rv.Name)]; rv.Subquery == nil && rv.Schema == "" && e != nil {
			// A CTE that is not inlined here — a shared or MATERIALIZED one,
			// or the worktable of an enclosing recursive CTE — reads as a
			// CTE scan, whose `*` expands to the CTE's planned columns
			// (alias-renamed, plannedCTE.table). M0146-0145: regress
			// subselect's `with z as not materialized (select * from x)`
			// inside a recursive term is pulled up onto the WorkTable Scan,
			// as PG's inline_cte + pull_up_simple_subquery do.
			if e.table == nil || len(rv.Columns) > 0 {
				return false
			}
			qual := rv.Alias
			if qual == "" {
				qual = rv.Name
			}
			cols := make([]string, 0, len(e.table.Columns))
			for _, c := range e.table.Columns {
				cols = append(cols, c.Name)
			}
			items = append(items, fromItem{qual: qual, cols: cols})
			return true
		}
		b := item.Base
		qual := b.Alias
		if b.Subquery != nil {
			body, ok := simpleDerivedPullupBody(item, cat)
			if !ok || qual == "" {
				return false
			}
			var cols []string
			for i, t := range body.Targets {
				switch {
				case i < len(b.Columns):
					cols = append(cols, b.Columns[i])
				case t.Alias != "":
					cols = append(cols, t.Alias)
				default:
					cr, ok := t.Expr.(*parser.ColumnRef)
					if !ok || cr.Column == "" || cr.Column == "*" {
						return false
					}
					cols = append(cols, cr.Column)
				}
			}
			items = append(items, fromItem{qual: qual, cols: cols})
			return true
		}
		if !pullupPlainRelation(b) {
			return false
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: b.Schema, Name: b.Name})
		if !ok || tbl == nil {
			return false
		}
		if qual == "" {
			qual = b.Name
		}
		var cols []string
		for _, c := range tbl.Columns {
			if !c.Dropped {
				cols = append(cols, c.Name)
			}
		}
		items = append(items, fromItem{qual: qual, cols: cols})
		return true
	}
	for _, f := range sub.FromExprs {
		if !add(f.Base) {
			return nil, false
		}
		for _, j := range f.Joins {
			if j.Natural || len(j.Using) > 0 || !add(j.Right) {
				return nil, false
			}
		}
	}
	var out []parser.ResTarget
	for _, t := range sub.Targets {
		if !isPullupStarTarget(t.Expr) {
			out = append(out, t)
			continue
		}
		want := pullupStarQualifier(t.Expr)
		matched := false
		for _, it := range items {
			if want != "" && !strings.EqualFold(it.qual, want) {
				continue
			}
			matched = true
			for _, c := range it.cols {
				out = append(out, parser.ResTarget{Expr: &parser.ColumnRef{Table: it.qual, Column: c}})
			}
		}
		if !matched {
			return nil, false
		}
	}
	return out, true
}

// isPullupStarTarget reports whether a target is `*` or `alias.*` (a schema-
// qualified star is not expanded here).
func isPullupStarTarget(e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.StarExpr:
		return x.Schema == ""
	case *parser.ColumnRef:
		return x.Column == "*" && x.Schema == ""
	}
	return false
}

// pullupStarQualifier is the alias a star target is restricted to, "" for a
// bare `*`.
func pullupStarQualifier(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.StarExpr:
		return x.Table
	case *parser.ColumnRef:
		return x.Table
	}
	return ""
}

// pulledCandidateOwning returns the innermost candidate whose expanded body
// items include item index i, or nil.
func pulledCandidateOwning(cands []*derivedPullupCandidate, i int) *derivedPullupCandidate {
	var owner *derivedPullupCandidate
	for _, c := range cands {
		if i >= c.itemLo && i < c.itemHi && (owner == nil || c.depth > owner.depth) {
			owner = c
		}
	}
	return owner
}

// pulledCandidateOwningBinding is pulledCandidateOwning over binding indices.
func pulledCandidateOwningBinding(cands []*derivedPullupCandidate, bi int) *derivedPullupCandidate {
	var owner *derivedPullupCandidate
	for _, c := range cands {
		if bi >= c.bindLo && bi < c.bindHi && (owner == nil || c.depth > owner.depth) {
			owner = c
		}
	}
	return owner
}

// maxDerivedPullupDepth bounds the nested expansion; deeper bodies stay
// ordinary derived leaves.
const maxDerivedPullupDepth = 8

// pullupPlainRelation reports whether a body FROM item is a plain relation
// name (a table or a CTE reference) — the only leaf kind the pull-up splices.
func pullupPlainRelation(b parser.RangeVar) bool {
	return b.Subquery == nil && b.TableFunc == nil && b.TableSample == nil && !b.Lateral &&
		len(b.Columns) == 0 && !b.GroupedJoinUnaliased && b.Name != ""
}

// pullupUnionAllLeaf reports whether a body FROM item is a non-LATERAL
// `(A UNION ALL B …)` subquery that is_simple_union_all admits — the item
// the derived-FROM path plans as an appendrel (planFromSubquery's
// appendrelSubquery mark), and so a leaf the pull-up may splice as is.
func pullupUnionAllLeaf(b parser.RangeVar) bool {
	return b.Subquery != nil && b.TableFunc == nil && b.TableSample == nil && !b.Lateral &&
		len(b.Columns) == 0 && !b.GroupedJoinUnaliased && subqueryChainIsSimpleUnionAll(b.Subquery)
}

// pullupSafeTargetCall is is_simple_subquery's target-list test for one
// function call (M0146-0028f): PG refuses a subquery whose targets carry an
// aggregate (hasAggs), a window function (hasWindowFuncs), a set-returning
// function (hasTargetSRFs) or a volatile function
// (contain_volatile_functions, prepjointree.c:1926). The call must also be
// known: a PG built-in (pg_proc.dat, catalog.IsBuiltinProcName) or a
// registered routine. Flags come from the generated built-in sets
// (catalog.BuiltinProcReturnsSet / BuiltinProcIsVolatile) and, for routines,
// from the catalog (ReturnsSet; Volatile "v" or unmarked). Any doubt declines.
func pullupSafeTargetCall(fc *parser.FuncCall, cat catalog.Catalog) bool {
	if fc == nil || fc.Over != nil || len(fc.WithinGroup) > 0 || fc.Filter != nil ||
		fc.Star || fc.Distinct || len(fc.OrderBy) > 0 {
		return false
	}
	if isAggregateFuncName(fc) || isUserAggregateFunc(fc, cat) {
		return false
	}
	name := strings.ToLower(fc.Name.Name)
	known := catalog.IsBuiltinProcName(name)
	if catalog.BuiltinProcReturnsSet(name) || catalog.BuiltinProcIsVolatile(name) || pullupVolatileBuiltins[name] {
		return false
	}
	if cat != nil {
		if rs := cat.Routines(); rs != nil {
			for _, r := range rs.LookupByName(parser.ObjectName{Name: name}) {
				known = true
				if r.ReturnsSet || r.Volatile == "v" || r.Volatile == "" {
					return false
				}
			}
		}
	}
	return known
}

func parserNodeIsSelect(x any) bool {
	_, ok := x.(*parser.SelectStmt)
	return ok
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
func expandDerivedPullups(s *parser.SelectStmt, mode derivedPullupMode, cat catalog.Catalog) ([]parser.FromExpr, []*derivedPullupCandidate, []parser.Expr) {
	if mode == derivedPullupOff || !parentFromAdmitsDerivedPullup(s) {
		return s.FromExprs, nil, nil
	}
	// M0146-0028e: an all-INNER join chain carrying a pullable derived
	// operand is split into comma items first; its ON clauses become
	// parent-level quals (splitInnerJoinChainForPullup).
	var onQuals []parser.Expr
	var flat []parser.FromExpr
	for _, it := range s.FromExprs {
		if pieces, ons, ok := splitInnerJoinChainForPullup(it, cat); ok {
			flat = append(flat, pieces...)
			onQuals = append(onQuals, ons...)
			continue
		}
		flat = append(flat, it)
	}
	var out []parser.FromExpr
	var cands []*derivedPullupCandidate
	declineAll := false
	// M0146-0007i: a pulled body's own join-free items are expanded the same
	// way, so a simple subquery or inlinable CTE inside it is flattened too.
	var expand func(items []parser.FromExpr, parent *derivedPullupCandidate, depth int)
	expand = func(items []parser.FromExpr, parent *derivedPullupCandidate, depth int) {
		for _, it := range items {
			src := it
			var cteEntry *plannedCTE
			if conv, e, ok := cteAsDerivedItem(it); ok {
				src, cteEntry = conv, e
			}
			lateral := depth == 0 && src.Base.Lateral && cteEntry == nil
			probe := src
			if lateral {
				probe.Base.Lateral = false
			}
			body, ok := simpleDerivedPullupBody(probe, cat)
			if ok && lateral && body.Where != nil &&
				parserExprHasNode(reflect.ValueOf(body.Where), 0, parserNodeIsSelect) {
				// A sublink in a LATERAL body would need its own outer
				// references re-levelled inside its plan; not carried.
				ok = false
			}
			if !ok || depth > maxDerivedPullupDepth {
				if lateral {
					// An unpulled LATERAL item resolves its references
					// through the plain FROM bindings, which cannot name a
					// pulled-up alias: keep the statement unpulled.
					declineAll = true
					return
				}
				out = append(out, it)
				continue
			}
			c := &derivedPullupCandidate{alias: src.Base.Alias, columns: src.Base.Columns, lateral: lateral, body: body,
				itemLo: len(out), cte: cteEntry, parent: parent, depth: depth}
			cands = append(cands, c)
			expand(body.FromExprs, c, depth+1)
			c.itemHi = len(out)
		}
	}
	expand(flat, nil, 0)
	if len(cands) == 0 || declineAll {
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
func splitInnerJoinChainForPullup(it parser.FromExpr, cat catalog.Catalog) ([]parser.FromExpr, []parser.Expr, bool) {
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
		item := parser.FromExpr{Base: rv}
		// M0146-0007f: a reference to an inlinable CTE is the derived table
		// inline_cte makes of it (cteAsDerivedItem); the pieces loop below
		// converts it the same way.
		if conv, _, ok := cteAsDerivedItem(item); ok {
			item = conv
		}
		if _, ok := simpleDerivedPullupBody(item, cat); ok {
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
func resolvePulledDerived(cands []*derivedPullupCandidate, bindings []rangeBinding, schema Schema, cat catalog.Catalog, ps PlannerSettings, scope *rtableScope) ([]*pulledDerivedRel, []Expr, map[Expr]*resolveContext, bool) {
	var rels []*pulledDerivedRel
	var quals []Expr
	var qualCtx map[Expr]*resolveContext
	// M0146-0007i: innermost bodies first. A body sees its own leaves and,
	// through their aliases, its direct children's outputs (already resolved
	// in the same parent coordinates); a grandchild's leaves stay hidden.
	// Only the statement's own items' rels are returned for parent-level
	// name resolution; every level's WHERE joins the scope's quals.
	order := make([]*derivedPullupCandidate, len(cands))
	copy(order, cands)
	sort.SliceStable(order, func(i, j int) bool { return order[i].depth > order[j].depth })
	relOf := make(map[*derivedPullupCandidate]*pulledDerivedRel, len(cands))
	qualOf := make(map[*derivedPullupCandidate]Expr, len(cands))
	for _, c := range order {
		bodyBindings := make([]rangeBinding, 0, c.bindHi-c.bindLo)
		for bi := c.bindLo; bi < c.bindHi; bi++ {
			b := bindings[bi]
			if pulledCandidateOwningBinding(cands, bi) == c {
				b.pulledHidden = false
			}
			bodyBindings = append(bodyBindings, b)
		}
		bodyCtx := newResolveContext(bodyBindings, schema, ps)
		bodyCtx.cat = cat
		bodyCtx.rtScope = scope
		bodyCtx.parent = planParent
		if c.lateral {
			// The items to the left are the LATERAL body's enclosing scope,
			// searched after its own (PG's namespace order); they share the
			// parent's coordinates, so a reference resolved there is
			// rewritten to a plain column below (pullup_replace_vars
			// lowering varlevelsup).
			left := make([]rangeBinding, c.bindLo)
			copy(left, bindings[:c.bindLo])
			leftCtx := newResolveContext(left, schema, ps)
			leftCtx.cat, leftCtx.rtScope, leftCtx.parent = cat, scope, planParent
			for _, prev := range cands {
				if prev.parent == nil && prev.bindHi <= c.bindLo && relOf[prev] != nil {
					leftCtx.pulledDerived = append(leftCtx.pulledDerived, relOf[prev])
				}
			}
			bodyCtx.parent = leftCtx
		}
		lower := func(e Expr) (Expr, bool) {
			if !c.lateral {
				return e, true
			}
			return lowerLateralRefs(e)
		}
		for _, child := range cands {
			if child.parent == c {
				bodyCtx.pulledDerived = append(bodyCtx.pulledDerived, relOf[child])
			}
		}
		rel := &pulledDerivedRel{alias: c.alias, firstBinding: c.bindLo, body: c.body}
		seen := map[string]bool{}
		for ti, t := range c.body.Targets {
			e, err := resolveExpr(t.Expr, bodyCtx)
			if err != nil {
				return nil, nil, nil, false
			}
			if e, err = foldQualConstants(e); err != nil {
				return nil, nil, nil, false
			}
			lowered, lok := lower(e)
			if !lok {
				return nil, nil, nil, false
			}
			e = lowered
			name, _ := targetMeta(e, t)
			if ti < len(c.columns) {
				// The alias list renames the output, as the RTE's eref
				// colnames do (addRangeTableEntryForSubquery).
				name = c.columns[ti]
			}
			key := strings.ToLower(name)
			if seen[key] {
				return nil, nil, nil, false
			}
			seen[key] = true
			rel.names = append(rel.names, name)
			rel.cols = append(rel.cols, e)
		}
		if c.body.Where != nil {
			q, err := resolveExpr(canonicalizeQual(c.body.Where), bodyCtx)
			if err != nil {
				return nil, nil, nil, false
			}
			// The same constant folding the parent's own WHERE gets
			// (planSelect): a range bound written `1195 + 11` must reach
			// the estimator as a constant.
			if q, err = foldQualConstants(q); err != nil {
				return nil, nil, nil, false
			}
			lowered, lok := lower(q)
			if !lok {
				return nil, nil, nil, false
			}
			q = lowered
			qualOf[c] = q
			// M0146-0028f: remember the body context per conjunct for the
			// jointree sublink pull-up (resolveContext.pulledQualCtx).
			if qualCtx == nil {
				qualCtx = map[Expr]*resolveContext{}
			}
			for _, conj := range splitAnd(q) {
				qualCtx[conj] = bodyCtx
			}
		}
		relOf[c] = rel
	}
	for _, c := range cands {
		if c.parent == nil {
			rels = append(rels, relOf[c])
		}
	}
	// M0146-0116: the quals come back in deconstruct_jointree's order
	// (initsplan.c deconstruct_recurse): a FromExpr's items are processed
	// left to right before its own quals, so a pulled body's WHERE follows
	// its children's and precedes its right-hand siblings'. The order is
	// the equivalence classes' member order, which picks the members a
	// join clause equates (generate_join_implied_equalities_normal).
	var emit func(parent *derivedPullupCandidate)
	emit = func(parent *derivedPullupCandidate) {
		var kids []*derivedPullupCandidate
		for _, c := range cands {
			if c.parent == parent {
				kids = append(kids, c)
			}
		}
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].itemLo < kids[j].itemLo })
		for _, c := range kids {
			emit(c)
			if q := qualOf[c]; q != nil {
				quals = append(quals, q)
			}
		}
	}
	emit(nil)
	return rels, quals, qualCtx, true
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

// lowerLateralRefs is pullup_replace_vars' varlevelsup adjustment for a
// pulled-up LATERAL body (M0146-0028h): the body was resolved one scope below
// its left-hand FROM items, so a level-1 outer reference names one of them —
// in the parent's own coordinates — and becomes a plain column, and a deeper
// one moves one level up. A sublink inside would carry its own levels; the
// caller has declined those bodies, and one found here declines too.
func lowerLateralRefs(e Expr) (Expr, bool) {
	good := true
	out, ok := cloneExprRefs(e, scopeIgnore, exprRewriter{Rewrite: func(n Expr) Expr {
		if len(ExprSubplans(n)) > 0 {
			good = false
			return n
		}
		o, isOuter := n.(*OuterColumnRef)
		if !isOuter {
			return n
		}
		if o.Level == 1 {
			return &ColumnRef{pos: o.pos, Index: o.Index, Name: o.Name, Type: o.Type, SourceTableIdx: o.SourceTableIdx}
		}
		c := *o
		c.Level--
		return &c
	}})
	if !ok || !good {
		return nil, false
	}
	return out, true
}
