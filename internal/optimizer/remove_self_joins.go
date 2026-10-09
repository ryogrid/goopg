package optimizer

import (
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// removeUselessSelfJoins is PG 18's remove_useless_self_joins
// (postgres/src/backend/optimizer/plan/analyzejoins.c) for the inner
// self-join of one base table on a unique key, applied to the statement
// before its FROM clause is planned (M0146-0115).
//
// Two references x (earlier) and y (later) to one table, joined by `x.c = y.c`
// on every column of a unique index, pair each y row with exactly one x row —
// the same row. PG removes x, keeps y, rewrites every reference to x as one to
// y, and turns each equality whose two sides are now the same expression into
// `expr IS NOT NULL` (regress join: `select p.* from sj p, sj q where q.a = p.a
// and q.b = q.a - 1` → `Seq Scan on sj q  Filter: ((a IS NOT NULL) AND (b = (a
// - 1)))`). It repeats until no pair is left (a double self-join removes two).
//
// The slice is fail-closed: it handles the FROM shapes it can rewrite on the
// statement and declines everything else, which keeps the join — PG's answer
// whenever its own proof fails.
//
//   - x is a comma-list item with no joins of its own, or the base of a FROM
//     item whose first join is `INNER JOIN y ON …` (its ON clause moves to
//     WHERE: an inner join at the bottom of the item filters the same rows);
//     y is a comma-list item or an inner-joined item, never a nullable side.
//   - Both are plain tables (no subquery, function, LATERAL, TABLESAMPLE,
//     column aliases), with the same catalog table that is not a view,
//     partitioned, inherited from, or under row-level security; the statement
//     has no set operation and no row-locking clause.
//   - The unique proof reads top-level WHERE conjuncts (plus the moved ON
//     conjuncts): `x.c = y.c` on the same column, covering an immediate,
//     non-partial unique index.
//   - Every reference is attributable: no unqualified reference to a column of
//     the table or to either qualifier (it would be ambiguous or a whole-row
//     reference), no schema-qualified column, no NATURAL / USING join, and no
//     nested FROM that reuses x's qualifier.
//
// Not ported (ledgered): a semi join made by pulling up EXISTS / IN, self-joins
// proved through equivalence classes or constants, and joins nested deeper
// than the first join of a FROM item.
func removeUselessSelfJoins(s *parser.SelectStmt, cat catalog.Catalog) *parser.SelectStmt {
	if s == nil || cat == nil {
		return s
	}
	for i := 0; i < 16; i++ {
		next, ok := removeOneSelfJoin(s, cat)
		if !ok {
			return s
		}
		s = next
	}
	return s
}

// selfJoinItem is one FROM entry: FromExprs[ci].Base (k == -1) or
// FromExprs[ci].Joins[k].Right.
type selfJoinItem struct {
	ci, k int
	rv    parser.RangeVar
	qual  string // lower-cased, for matching
	orig  string // as written, for the rewrite
	join  *parser.JoinExpr
	tbl   *catalog.Table
}

func removeOneSelfJoin(s *parser.SelectStmt, cat catalog.Catalog) (*parser.SelectStmt, bool) {
	if s.SetOp != nil || s.SetOpOperand != nil || len(s.Locking) > 0 || len(s.FromExprs) == 0 {
		return nil, false
	}
	var items []selfJoinItem
	quals := map[string]int{}
	for ci, it := range s.FromExprs {
		items = append(items, selfJoinItem{ci: ci, k: -1, rv: it.Base})
		for k := range it.Joins {
			j := &s.FromExprs[ci].Joins[k]
			if j.Natural || len(j.Using) > 0 {
				return nil, false
			}
			items = append(items, selfJoinItem{ci: ci, k: k, rv: j.Right, join: j})
		}
	}
	if len(items) != len(s.From) {
		return nil, false
	}
	for i := range items {
		rv := items[i].rv
		q := rv.Alias
		if q == "" {
			q = rv.Name
		}
		items[i].qual = strings.ToLower(q)
		items[i].orig = q
		quals[items[i].qual]++
		if rv.Lateral || rv.TableFunc != nil {
			return nil, false
		}
	}
	for xi := 0; xi < len(items); xi++ {
		x := items[xi]
		if !selfJoinPlainTable(&items[xi], cat) || quals[x.qual] != 1 {
			continue
		}
		xItem := s.FromExprs[x.ci]
		caseB := false
		switch {
		case x.k == -1 && len(xItem.Joins) == 0:
		case x.k == -1 && xItem.Joins[0].Type == parser.JoinInner && xItem.Joins[0].On != nil:
			caseB = true
		default:
			continue
		}
		for yi := xi + 1; yi < len(items); yi++ {
			y := items[yi]
			if !selfJoinPlainTable(&items[yi], cat) || quals[y.qual] != 1 || items[yi].tbl != items[xi].tbl {
				continue
			}
			if caseB && !(y.ci == x.ci && y.k == 0) {
				continue
			}
			if !caseB && y.k >= 0 && y.join.Type != parser.JoinInner {
				continue
			}
			if out, ok := rewriteSelfJoin(s, cat, items, items[xi], items[yi], caseB); ok {
				return out, true
			}
		}
	}
	return nil, false
}

// selfJoinPlainTable resolves item's table and reports whether it is a plain
// heap table SJE may consider.
func selfJoinPlainTable(it *selfJoinItem, cat catalog.Catalog) bool {
	rv := it.rv
	if rv.Subquery != nil || rv.TableFunc != nil || rv.Lateral || rv.TableSample != nil || len(rv.Columns) > 0 {
		return false
	}
	if rv.Schema == "" && lookupPlannedCTE(rv.Name) != nil {
		return false
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok || tbl == nil || tbl.View != nil || tbl.IsMatView || len(tbl.PartitionKey) > 0 || tbl.RowSecurity {
		return false
	}
	if im := inMemoryCat(cat); im != nil {
		if len(catalog.AccessibleInheritanceChildren(im.InheritanceChildren(tbl.OID), currentTempOwner(cat))) > 0 {
			return false
		}
	}
	it.tbl = tbl
	return true
}

func rewriteSelfJoin(s *parser.SelectStmt, cat catalog.Catalog, items []selfJoinItem, x, y selfJoinItem, caseB bool) (*parser.SelectStmt, bool) {
	conjuncts := splitParserConjuncts(s.Where, nil)
	if caseB {
		conjuncts = splitParserConjuncts(s.FromExprs[x.ci].Joins[0].On, conjuncts)
	}
	// The unique proof: same-column equalities between x and y.
	var cols []string
	for _, c := range conjuncts {
		l, r, ok := columnEquality(c)
		if !ok || l.Schema != "" || r.Schema != "" || !strings.EqualFold(l.Column, r.Column) {
			continue
		}
		lq, rq := strings.ToLower(l.Table), strings.ToLower(r.Table)
		if (lq == x.qual && rq == y.qual) || (lq == y.qual && rq == x.qual) {
			if col, ok := cat.LookupColumn(x.tbl, l.Column); ok {
				cols = append(cols, col.Name)
			}
		}
	}
	if len(cols) == 0 || !selfJoinUniqueKeyCovered(cat, x.tbl, cols) {
		return nil, false
	}
	// Attribution: every reference to the table's columns must be qualified,
	// and no nested FROM may reuse x's qualifier.
	colNames := map[string]bool{}
	for _, c := range x.tbl.Columns {
		colNames[strings.ToLower(c.Name)] = true
	}
	safe := true
	fromQuals := 0
	// s.From repeats FromExprs' entries; walk without it, so x's own FROM
	// entry counts once and any nested FROM reusing its qualifier shows.
	probe := *s
	probe.From = nil
	walkParserNodes(reflect.ValueOf(&probe), 0, func(n any) {
		switch v := n.(type) {
		case *parser.ColumnRef:
			if v.Schema != "" {
				safe = false
			}
			if v.Table == "" {
				lc := strings.ToLower(v.Column)
				if colNames[lc] || lc == x.qual || lc == y.qual {
					safe = false
				}
			}
		case parser.RangeVar:
			q := v.Alias
			if q == "" {
				q = v.Name
			}
			if strings.EqualFold(q, x.qual) {
				fromQuals++
			}
		}
	}, &safe)
	if !safe || fromQuals != 1 {
		return nil, false
	}
	// A bare `*` expands to every FROM item's columns in FROM order; spell it
	// out per item first, so renaming x's star to y's keeps x's columns.
	targets := make([]parser.ResTarget, 0, len(s.Targets))
	for _, t := range s.Targets {
		if st, ok := t.Expr.(*parser.StarExpr); ok && st.Table == "" && st.Schema == "" {
			for _, it := range items {
				targets = append(targets, parser.ResTarget{Expr: &parser.StarExpr{Table: it.orig}})
			}
			continue
		}
		targets = append(targets, t)
	}
	draft := *s
	draft.Targets = targets
	renamed, ok := renameParserQualifier(&draft, x.qual, y.orig)
	if !ok {
		return nil, false
	}
	out := renamed
	// The FROM list without x.
	out.FromExprs = append([]parser.FromExpr(nil), out.FromExprs...)
	var moved []parser.Expr
	if caseB {
		item := out.FromExprs[x.ci]
		on := item.Joins[0].On
		item.Base = item.Joins[0].Right
		item.Joins = append([]parser.JoinExpr(nil), item.Joins[1:]...)
		out.FromExprs[x.ci] = item
		moved = splitParserConjuncts(on, nil)
	} else {
		out.FromExprs = append(out.FromExprs[:x.ci:x.ci], out.FromExprs[x.ci+1:]...)
	}
	out.From = nil
	for _, it := range out.FromExprs {
		out.From = append(out.From, it.Base)
		for _, j := range it.Joins {
			out.From = append(out.From, j.Right)
		}
	}
	// The WHERE, with each now-reflexive equality as `expr IS NOT NULL`, in
	// the order PG's kept relation lists them: the removed relation's own
	// restrictions first, then the derived NOT NULL tests, then the rest
	// (`(b IS NULL) AND (c IS NOT NULL)` for `x.c = y.c AND x.b IS NULL`).
	// A repeated test appears once (the second link of a double removal
	// derives `a IS NOT NULL` again).
	before := append(splitParserConjuncts(s.Where, nil), splitParserConjuncts(movedOriginal(s, x, caseB), nil)...)
	after := append(splitParserConjuncts(out.Where, nil), moved...)
	if len(before) != len(after) {
		return nil, false
	}
	var onlyX, derived, rest []parser.Expr
	for i, c := range after {
		if b, ok := c.(*parser.BinaryOp); ok && b.Op == parser.OpEq && parserExprsSame(b.Left, b.Right) {
			derived = append(derived, &parser.IsNullExpr{Operand: b.Left, Negated: true})
			continue
		}
		if conjunctReadsOnly(before[i], x.qual) {
			onlyX = append(onlyX, c)
			continue
		}
		rest = append(rest, c)
	}
	var where []parser.Expr
	for _, c := range append(append(onlyX, derived...), rest...) {
		dup := false
		for _, w := range where {
			if parserExprsSame(w, c) {
				dup = true
				break
			}
		}
		if !dup {
			where = append(where, c)
		}
	}
	out.Where = andParserConjuncts(where)
	return out, true
}

// movedOriginal is the ON clause rewriteSelfJoin moves to WHERE, as written
// before the rename (nil when nothing moves).
func movedOriginal(s *parser.SelectStmt, x selfJoinItem, caseB bool) parser.Expr {
	if !caseB {
		return nil
	}
	return s.FromExprs[x.ci].Joins[0].On
}

// conjunctReadsOnly reports whether every column c reads is qualified by qual
// (and it reads at least one): one of the removed relation's own
// restrictions.
func conjunctReadsOnly(c parser.Expr, qual string) bool {
	n, ok := 0, true
	walkParserNodes(reflect.ValueOf(c), 0, func(v any) {
		cr, isCol := v.(*parser.ColumnRef)
		if !isCol {
			if _, isSel := v.(*parser.SelectStmt); isSel {
				ok = false
			}
			return
		}
		if !strings.EqualFold(cr.Table, qual) {
			ok = false
		}
		n++
	}, &ok)
	return ok && n > 0
}

// selfJoinUniqueKeyCovered reports whether cols cover every column of an
// immediate, non-partial unique index of tbl (relation_has_unique_index_for).
func selfJoinUniqueKeyCovered(cat catalog.Catalog, tbl *catalog.Table, cols []string) bool {
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx == nil || !idx.Unique || idx.HasPredicate || idx.Deferrable || len(idx.Columns) == 0 {
			continue
		}
		covered := true
		for _, c := range idx.Columns {
			if c == "" {
				covered = false
				break
			}
			found := false
			for _, have := range cols {
				if strings.EqualFold(have, c) {
					found = true
					break
				}
			}
			if !found {
				covered = false
				break
			}
		}
		if covered {
			return true
		}
	}
	return false
}

// walkParserNodes visits every value reachable from v (depth-capped; a cap
// hit clears *ok so the caller fails closed).
func walkParserNodes(v reflect.Value, depth int, visit func(any), ok *bool) {
	if !*ok || !v.IsValid() {
		return
	}
	if depth > 200 {
		*ok = false
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		if v.CanInterface() {
			visit(v.Interface())
		}
		walkParserNodes(v.Elem(), depth+1, visit, ok)
	case reflect.Struct:
		if v.CanInterface() {
			visit(v.Interface())
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				walkParserNodes(v.Field(i), depth+1, visit, ok)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkParserNodes(v.Index(i), depth+1, visit, ok)
		}
	}
}

// renameParserQualifier returns a deep copy of s with every column or star
// qualified by from requalified by to. The copy shares nothing with s, so the
// caller's statement (a prepared or cached AST) is never touched.
func renameParserQualifier(s *parser.SelectStmt, from, to string) (*parser.SelectStmt, bool) {
	ok := true
	var copyV func(v reflect.Value, depth int) reflect.Value
	copyV = func(v reflect.Value, depth int) reflect.Value {
		if !ok || !v.IsValid() {
			return v
		}
		if depth > 200 {
			ok = false
			return v
		}
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return v
			}
			if v.Elem().Kind() != reflect.Struct {
				return v
			}
			cp := reflect.New(v.Elem().Type())
			cp.Elem().Set(copyV(v.Elem(), depth+1))
			switch n := cp.Interface().(type) {
			case *parser.ColumnRef:
				if strings.EqualFold(n.Table, from) {
					n.Table = to
				}
			case *parser.StarExpr:
				if strings.EqualFold(n.Table, from) {
					n.Table = to
				}
			}
			return cp
		case reflect.Interface:
			if v.IsNil() {
				return v
			}
			inner := copyV(v.Elem(), depth+1)
			out := reflect.New(v.Type()).Elem()
			out.Set(inner)
			return out
		case reflect.Struct:
			cp := reflect.New(v.Type()).Elem()
			cp.Set(v)
			for i := 0; i < cp.NumField(); i++ {
				if !cp.Type().Field(i).IsExported() || !cp.Field(i).CanSet() {
					continue
				}
				cp.Field(i).Set(copyV(cp.Field(i), depth+1))
			}
			return cp
		case reflect.Slice:
			if v.IsNil() {
				return v
			}
			cp := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			for i := 0; i < v.Len(); i++ {
				cp.Index(i).Set(copyV(v.Index(i), depth+1))
			}
			return cp
		case reflect.Map:
			// No AST node holds a map today; refuse rather than share one.
			if !v.IsNil() {
				ok = false
			}
			return v
		}
		return v
	}
	out := copyV(reflect.ValueOf(s), 0)
	if !ok {
		return nil, false
	}
	return out.Interface().(*parser.SelectStmt), true
}

// parserExprsSame reports whether a and b are the same expression, ignoring
// source positions (every unexported field): `y.b + 1 = y.b + 1` after the
// rename is reflexive.
func parserExprsSame(a, b parser.Expr) bool {
	return parserValuesSame(reflect.ValueOf(a), reflect.ValueOf(b), 0)
}

func parserValuesSame(a, b reflect.Value, depth int) bool {
	if depth > 200 || a.IsValid() != b.IsValid() {
		return false
	}
	if !a.IsValid() {
		return true
	}
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Interface, reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return parserValuesSame(a.Elem(), b.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !a.Type().Field(i).IsExported() {
				continue
			}
			if !parserValuesSame(a.Field(i), b.Field(i), depth+1) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !parserValuesSame(a.Index(i), b.Index(i), depth+1) {
				return false
			}
		}
		return true
	case reflect.String:
		return a.String() == b.String()
	case reflect.Map, reflect.Func, reflect.Chan:
		return false
	}
	return a.Interface() == b.Interface()
}
