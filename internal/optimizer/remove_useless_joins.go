package optimizer

import (
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// removeUselessLeftJoins is PG's remove_useless_joins / join_is_removable
// (postgres/src/backend/optimizer/plan/analyzejoins.c) for the FROM-clause
// LEFT JOIN, applied to the statement before the FROM clause is planned
// (M0146-0005dl).
//
// A LEFT JOIN whose nullable side B is one base table can be dropped when
//
//   - no column of B is read anywhere but B's own ON clause (PG:
//     attr_needed of every inner attribute is within the join's relids),
//     and
//   - B is provably unique for the ON clause's equalities — they equate a
//     set of B's columns covering a non-partial unique index to columns of
//     the other tables (rel_is_distinct_for → relation_has_unique_index_for).
//
// Then every preserved row matches at most one B row and keeps exactly one
// output row whether it matches or not, and nothing reads B's columns, so
// the join contributes nothing. TPC-DS Q72 left-joins catalog_returns on its
// primary key and never reads it; PG plans no join at all.
//
// PG repeats the scan after each removal, since one removal can make another
// join removable; so does this. Everything it cannot prove declines, which
// keeps the join — PG's own answer whenever the proof fails:
//
//   - NATURAL / USING joins, a non-table B (subquery, function, LATERAL,
//     TABLESAMPLE, column aliases), and any statement shape the column-name
//     collector does not model (SELECT *, WITH, window clauses, ...);
//   - an ON clause holding a sublink;
//   - a B column name read unqualified anywhere else, even if it belongs to
//     another table (name-level attribution over-counts, never under);
//   - an equality whose other side is not a same-typed column of another
//     FROM table (PG also accepts constants and cross-type operators of the
//     index opfamily — not ported).
func removeUselessLeftJoins(s *parser.SelectStmt, cat catalog.Catalog) *parser.SelectStmt {
	if s == nil || cat == nil || len(s.FromExprs) == 0 {
		return s
	}
	for {
		next, ok := removeOneUselessLeftJoin(s, cat)
		if !ok {
			return s
		}
		s = next
	}
}

// removeOneUselessLeftJoin returns a copy of s without its first removable
// LEFT JOIN, or false when none is removable.
func removeOneUselessLeftJoin(s *parser.SelectStmt, cat catalog.Catalog) (*parser.SelectStmt, bool) {
	flat := 0
	for _, it := range s.FromExprs {
		if it.Base.Lateral || it.Base.TableFunc != nil {
			return nil, false
		}
		for _, j := range it.Joins {
			if j.Right.Lateral || j.Right.TableFunc != nil {
				// A later LATERAL item may read B; the name collector does
				// not attribute FROM-clause function arguments.
				return nil, false
			}
		}
		flat += 1 + len(it.Joins)
	}
	if len(s.From) != flat {
		return nil, false
	}
	tableMap := buildTableMap(s.FromExprs, cat)
	pos := 0
	for ci, it := range s.FromExprs {
		pos++ // the Base
		for k, j := range it.Joins {
			flatIdx := pos
			pos++
			if !leftJoinIsRemovable(s, ci, k, j, tableMap, cat) {
				continue
			}
			out := *s
			out.FromExprs = append([]parser.FromExpr(nil), s.FromExprs...)
			item := out.FromExprs[ci]
			item.Joins = append(append([]parser.JoinExpr(nil), it.Joins[:k]...), it.Joins[k+1:]...)
			out.FromExprs[ci] = item
			out.From = append(append([]parser.RangeVar(nil), s.From[:flatIdx]...), s.From[flatIdx+1:]...)
			return &out, true
		}
	}
	return nil, false
}

// leftJoinIsRemovable is join_is_removable for FROM item ci's k-th join.
func leftJoinIsRemovable(s *parser.SelectStmt, ci, k int, j parser.JoinExpr, tableMap map[string]*catalog.Table, cat catalog.Catalog) bool {
	b := j.Right
	if j.Type != parser.JoinLeft || j.Natural || len(j.Using) > 0 || j.On == nil {
		return false
	}
	if b.Subquery != nil || b.TableFunc != nil || b.Lateral || b.TableSample != nil || len(b.Columns) > 0 {
		return false
	}
	qual := b.Alias
	if qual == "" {
		qual = b.Name
	}
	tbl := tableMap[qual]
	if tbl == nil {
		return false
	}
	if live, ok := cat.LookupTable(parser.ObjectName{Schema: b.Schema, Name: b.Name}); !ok || live != tbl {
		// Another FROM entry claims the same qualifier; attribution by name
		// would be unsound.
		return false
	}
	if parserExprHasNode(reflect.ValueOf(j.On), 0, func(x any) bool {
		_, isSel := x.(*parser.SelectStmt)
		return isSel
	}) {
		return false
	}

	// Which side of a column reference is B's: qualified by B's qualifier,
	// or unqualified and owned by B alone among the FROM tables.
	ownerOf := func(c *parser.ColumnRef) (string, bool) {
		if c.Table != "" {
			if _, known := tableMap[c.Table]; !known {
				return "", false
			}
			return c.Table, true
		}
		return resolveUnqualifiedOwner(c.Column, tableMap, cat)
	}
	var bCols []string
	for _, c := range splitParserConjuncts(j.On, nil) {
		l, r, ok := columnEquality(c)
		if !ok {
			continue
		}
		lo, lok := ownerOf(l)
		ro, rok := ownerOf(r)
		if !lok || !rok {
			continue
		}
		var inner, other *parser.ColumnRef
		var otherOwner string
		switch {
		case strings.EqualFold(lo, qual) && !strings.EqualFold(ro, qual):
			inner, other, otherOwner = l, r, ro
		case strings.EqualFold(ro, qual) && !strings.EqualFold(lo, qual):
			inner, other, otherOwner = r, l, lo
		default:
			continue
		}
		ic, iok := cat.LookupColumn(tbl, inner.Column)
		oc, ook := cat.LookupColumn(tableMap[otherOwner], other.Column)
		if !iok || !ook || ic.Type.Name != oc.Type.Name {
			// Same-typed pairs only: their `=` is the operator the unique
			// index itself uses.
			continue
		}
		bCols = append(bCols, ic.Name)
	}
	if len(bCols) == 0 {
		return false
	}
	unique := false
	for _, key := range uniqueKeyColumnSets(cat, tbl) {
		if columnsCover(bCols, key) {
			unique = true
			break
		}
	}
	if !unique {
		return false
	}

	// attr_needed: read the statement with B's ON clause blanked; no column
	// of B (nor B as a whole-row reference) may remain.
	probe := *s
	probe.FromExprs = append([]parser.FromExpr(nil), s.FromExprs...)
	item := probe.FromExprs[ci]
	item.Joins = append([]parser.JoinExpr(nil), item.Joins...)
	item.Joins[k].On = nil
	probe.FromExprs[ci] = item
	needed, ok := neededColumnNames(&probe)
	if !ok {
		return false
	}
	if needed[neededUnqualKey(qual)] || needed[strings.ToLower(qual)] {
		return false
	}
	for _, col := range tbl.Columns {
		if neededColumnNamedFor(needed, qual, strings.ToLower(col.Name)) || neededColumnNamedFor(needed, qual, col.Name) {
			return false
		}
	}
	return true
}
