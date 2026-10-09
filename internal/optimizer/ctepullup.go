package optimizer

import (
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0007e — an inlined CTE's reference site pulls its body up.
//
// PG's inline_cte (subselect.c) turns a single-reference CTE into an ordinary
// RTE_SUBQUERY before pull_up_subqueries runs, so a simple body is flattened
// into the referencing query exactly like a FROM-clause derived table: its
// relations join the parent's search and the parent's quals reach them.
// TPC-DS Q47/Q57's `v2` (a join of three `v1` references) is such a CTE; goopg
// planned it once at the WITH and kept a `Subquery Scan on v2` with the outer
// WHERE as its filter.
//
// goopg's FROM-subquery pull-up (M0146-0028, derivedpullup.go) is reused: a
// FROM item naming an inlinable CTE is presented to it as the derived table
// `(<CTE body>) <alias>`, and the same is_simple_subquery gate decides.

// countCTEReferences is parse analysis' cterefcount for `name` over the
// statement that owns the WITH: every relation reference with that
// (unqualified) name anywhere in the statement — FROM lists, set-operation
// branches, sublinks, other CTE bodies (SelectStmt.From, the flattened copy
// of FromExprs, is skipped). A nested WITH that shadows the name is
// NOT subtracted, and an AST too deep to walk reports a large count: both only
// overcount, which declines the pull-up.
func countCTEReferences(s *parser.SelectStmt, name string) int {
	n := 0
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if n > 1 {
			return
		}
		if depth > 96 {
			n = 1 << 20
			return
		}
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Ptr:
			if !v.IsNil() {
				walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			if v.CanInterface() {
				if rv, ok := v.Interface().(parser.RangeVar); ok && rv.Subquery == nil &&
					rv.Schema == "" && strings.EqualFold(rv.Name, name) {
					n++
				}
			}
			// SelectStmt.From is the v0 planner's flattened copy of
			// FromExprs; walking both would count every FROM reference
			// twice.
			skipFrom := false
			if v.CanInterface() {
				if sel, ok := v.Interface().(parser.SelectStmt); ok && len(sel.FromExprs) > 0 {
					skipFrom = true
				}
			}
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if skipFrom && t.Field(i).Name == "From" {
					continue
				}
				if f := v.Field(i); f.CanInterface() {
					walk(f, depth+1)
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(s), 0)
	return n
}

// stampCTEReferenceCounts records astRefs on every entry the statement's own
// WITH list declared (a recursive WITH is never inlined and is skipped).
func stampCTEReferenceCounts(s *parser.SelectStmt) {
	if s == nil || s.With == nil || s.With.Recursive {
		return
	}
	for _, cte := range s.With.CTEs {
		if e := planCTEs[strings.ToLower(cte.Name)]; e != nil {
			e.astRefs = countCTEReferences(s, cte.Name)
		}
	}
}

// cteAsDerivedItem returns `it` rewritten as the derived table PG's
// inline_cte makes of a CTE reference, when the CTE is inline_cte's single
// reference (inlinable's gates, with the AST reference count in place of the
// planning-time one) or a multiply-referenced NOT MATERIALIZED CTE
// (inlinesEachReference), and its body could be pulled up at all. The
// returned entry is the CTE whose preplanned body the pull-up replaces.
func cteAsDerivedItem(it parser.FromExpr) (parser.FromExpr, *plannedCTE, bool) {
	rv := it.Base
	if rv.Subquery != nil || rv.Schema != "" || rv.Name == "" || rv.Lateral ||
		rv.TableFunc != nil || rv.TableSample != nil || len(rv.Columns) > 0 || len(it.Joins) > 0 {
		return it, nil, false
	}
	e := planCTEs[strings.ToLower(rv.Name)]
	singleRef := e != nil && e.astRefs == 1 && e.inlineEligible && e.selectOwned &&
		e.materialized != "materialized" && !e.volatile && !e.isDML
	if e == nil || !(singleRef || e.inlinesEachReference()) || e.query == nil ||
		len(e.aliasColumns) > 0 || !cteBodyNamesResolveAsDeclared(e) {
		return it, nil, false
	}
	alias := rv.Alias
	if alias == "" {
		alias = rv.Name
	}
	out := it
	out.Base = parser.RangeVar{Name: "", Alias: alias, Subquery: e.query}
	return out, e, true
}

// takeBackPulledBodyRefs is called once the reference site's pull-up of e
// succeeded: the preplanned body is dead, and the parent has just re-planned
// the references it held (bodyRefDeltas).
func takeBackPulledBodyRefs(e *plannedCTE) {
	if e == nil || e.pulledUp {
		return
	}
	e.pulledUp = true
	for ref, d := range e.bodyRefDeltas {
		ref.refs -= d
		if ref.refs < 0 {
			ref.refs = 0
		}
	}
}

// cteBodyNamesResolveAsDeclared reports whether every unqualified relation
// name the CTE's body reads resolves, in the current CTE scope, to what it
// resolved to where the CTE was declared (declScope). A pulled-up body plans
// its FROM items in the referencing scope, which also sees this CTE itself, a
// later sibling and any WITH nested around the reference; PG resolves the
// body at the WITH's own level. A name that would rebind declines the pull-up
// (planCTEReferenceAsSubquery then plans the body under declScope). Entries
// without a recorded scope (built outside preplanWithClause) decline only
// when the body reads the CTE's own name.
func cteBodyNamesResolveAsDeclared(e *plannedCTE) bool {
	ok := true
	complete := walkParserRangeVarNames(e.query, func(name string) {
		key := strings.ToLower(name)
		var declared *plannedCTE
		if e.declScope != nil {
			declared = e.declScope[key]
		} else if key == strings.ToLower(e.name) {
			ok = false
			return
		} else {
			declared = planCTEs[key]
		}
		if planCTEs[key] != declared {
			ok = false
		}
	})
	return ok && complete
}

// walkParserRangeVarNames calls fn with the name of every unqualified,
// non-subquery relation reference in s (FROM lists, set-operation branches,
// sublinks, nested WITH bodies; SelectStmt.From, the flattened copy of
// FromExprs, is skipped). Names a nested WITH declares are reported too,
// which only makes cteBodyNamesResolveAsDeclared stricter. It returns false
// when the AST was too deep to walk completely.
func walkParserRangeVarNames(s *parser.SelectStmt, fn func(string)) bool {
	complete := true
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if depth > 96 {
			complete = false
			return
		}
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Ptr:
			if !v.IsNil() {
				walk(v.Elem(), depth+1)
			}
		case reflect.Struct:
			if v.CanInterface() {
				if rv, ok := v.Interface().(parser.RangeVar); ok && rv.Subquery == nil &&
					rv.TableFunc == nil && rv.Schema == "" && rv.Name != "" {
					fn(rv.Name)
				}
			}
			skipFrom := false
			if v.CanInterface() {
				if sel, ok := v.Interface().(parser.SelectStmt); ok && len(sel.FromExprs) > 0 {
					skipFrom = true
				}
			}
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if skipFrom && t.Field(i).Name == "From" {
					continue
				}
				if f := v.Field(i); f.CanInterface() {
					walk(f, depth+1)
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(s), 0)
	return complete
}

// planCTEReferenceAsSubquery plans one reference to a multiply-referenced
// NOT MATERIALIZED CTE (inlinesEachReference) as the ordinary subquery PG's
// inline_cte makes of it: the written body, under the reference's alias, with
// the CTE's column names (the reference's own alias list overriding a
// prefix), planned afresh for this reference under the declaration's CTE
// scope. A reference the FROM-list pull-up already flattened never gets
// here. The preplanned body is dead once a reference inlines, so its own
// references to other CTEs are taken back (takeBackPulledBodyRefs).
// M0146-0007f.
func planCTEReferenceAsSubquery(rv parser.RangeVar, e *plannedCTE, alias string, cat catalog.Catalog, sourceIdx int16, lateralCtx *resolveContext, ps PlannerSettings, scope *rtableScope) (Node, rangeBinding, error) {
	sub := rv
	sub.Schema, sub.Name = "", ""
	sub.Alias = alias
	sub.Subquery = e.query
	sub.Columns = nil
	if len(e.aliasColumns) > 0 || len(rv.Columns) > 0 {
		sub.Columns = make([]string, len(e.schema))
		for i, c := range e.schema {
			sub.Columns[i] = c.Name
		}
		copy(sub.Columns, rv.Columns)
	}
	saved := planCTEs
	planCTEs = e.declScope
	node, b, err := planSubqueryRangeVar(sub, cat, sourceIdx, lateralCtx, ps, scope)
	planCTEs = saved
	if err != nil {
		return nil, rangeBinding{}, err
	}
	takeBackPulledBodyRefs(e)
	b.cteRef = true
	return node, b, nil
}

// selectTreeHasLocking is contain_dml's row-mark arm over a CTE body: any
// query level in it (sublinks and nested WITH bodies included) carrying a
// FOR UPDATE/SHARE clause. Such a CTE is never inlined (SS_process_ctes'
// `!contain_dml(cte->ctequery)`), so the body runs once and locks every row
// it returns. An AST too deep to walk reports true, which keeps the CTE.
func selectTreeHasLocking(s *parser.SelectStmt) bool {
	found := false
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if found {
			return
		}
		if depth > 512 {
			found = true
			return
		}
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Ptr:
			if v.IsNil() {
				return
			}
			if sel, ok := v.Interface().(*parser.SelectStmt); ok && len(sel.Locking) > 0 {
				found = true
				return
			}
			walk(v.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if f := v.Field(i); f.CanInterface() {
					walk(f, depth+1)
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(s), 0)
	return found
}
