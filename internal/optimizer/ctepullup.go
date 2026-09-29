package optimizer

import (
	"reflect"
	"strings"

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
// planning-time one) and its body could be pulled up at all. The returned
// entry is the CTE whose preplanned body the pull-up replaces.
func cteAsDerivedItem(it parser.FromExpr) (parser.FromExpr, *plannedCTE, bool) {
	rv := it.Base
	if rv.Subquery != nil || rv.Schema != "" || rv.Name == "" || rv.Lateral ||
		rv.TableFunc != nil || rv.TableSample != nil || len(rv.Columns) > 0 || len(it.Joins) > 0 {
		return it, nil, false
	}
	e := planCTEs[strings.ToLower(rv.Name)]
	if e == nil || e.astRefs != 1 || !e.inlineEligible || !e.selectOwned ||
		e.materialized == "materialized" || e.volatile || e.isDML || e.query == nil ||
		len(e.aliasColumns) > 0 {
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
