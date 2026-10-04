package optimizer

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005bn — subquery_push_qual BEFORE the subquery is planned.
//
// PG's set_subquery_pathlist (allpaths.c) moves a restriction on a subquery
// rel's output into the subquery's Query — into havingQual when it groups —
// and only then plans the subquery (subquery_planner), whose HAVING
// preprocessing moves an aggregate-free clause on to WHERE. The pushed qual
// therefore filters at the scans and prices every join and aggregate above
// them. goopg plans a derived table (planSubqueryRangeVar) and a CTE body
// (preplanWithClause) eagerly, before the outer WHERE is looked at; its
// later pushes (pushQualsIntoSubqueryLeaf, pushQualsThroughSingleRefCTEs)
// splice the qual into an already-planned body whose estimates stay those
// of the unfiltered input. TPC-DS Q78: `ss_sold_year = 1998` reached the
// date_dim scan, but store_sales ⋈ date_dim kept its all-years estimate
// (281532 rows where PG's 3441, ea-ratchet qerr 87 on the top join).
//
// This pass does PG's move on the AST: a WHERE conjunct `col op const` whose
// column is a grouping column of a grouped derived table (or of a
// single-reference inlinable CTE, presented as the derived table inline_cte
// makes of it) is rewritten into the body's HAVING and removed from the
// WHERE, before the FROM list is planned. For `=`, the constant also
// follows the equalities the column takes part in — WHERE equalities, INNER
// join ON clauses, and the ON clause of a LEFT join whose nullable side the
// partner is (reconsider_outer_join_clauses) — and every partner must itself
// take the push, or the conjunct stays where it is: goopg's own constant
// propagation reads the WHERE conjunct, and moving it without covering every
// partner would lose a derived restriction.
//
// Declined (the conjunct stays, later pushes still apply): a column the
// body does not group on, a body with set operations, grouping sets,
// DISTINCT, LIMIT/OFFSET, locking, window functions or its own WITH; any
// item under a RIGHT/FULL join or on a LEFT join's nullable side (a WHERE
// qual there is not a restriction of that rel alone); LATERAL items; an
// unqualified column that is not exposed by exactly one FROM item; and any
// FROM item whose exposed names cannot be enumerated, for unqualified
// columns.

// pushQualItem is one FROM position of the statement.
type pushQualItem struct {
	rv       parser.RangeVar
	alias    string
	names    map[string]int // lower-cased exposed name → count
	namesOK  bool
	nullable bool
	body     *parser.SelectStmt // grouped body a qual may be pushed into
	cte      *plannedCTE
	chain    int // FROM item index
	chainPos int // 0 = Base, k = Joins[k-1].Right
	pushed   []parser.Expr
}

// pushQualEq is one `a = b` column equality with the join it came from.
type pushQualEq struct {
	a, b *parser.ColumnRef
	// leftNullable is the flat position of the nullable side when the
	// equality is a LEFT join's ON clause, else -1.
	leftNullable int
	// Resolved endpoints (flat position + lower-cased column); ok is false
	// when either side does not resolve to exactly one FROM position.
	apos, bpos int
	acol, bcol string
	ok         bool
	// expr is the ON conjunct itself, and chain/join locate its JoinExpr
	// (FromExprs[chain].Joins[join]); set for a LEFT join's ON equality.
	expr        parser.Expr
	chain, join int
}

func pushWhereQualsIntoGroupedItems(s *parser.SelectStmt, cat catalog.Catalog) *parser.SelectStmt {
	if s == nil || s.Where == nil || len(s.FromExprs) == 0 {
		return s
	}
	var items []*pushQualItem
	var eqs []pushQualEq
	flat := 0
	for ci, it := range s.FromExprs {
		chainOuter := false
		for _, j := range it.Joins {
			if j.Type == parser.JoinRight || j.Type == parser.JoinFull {
				chainOuter = true
			}
		}
		add := func(rv parser.RangeVar, pos int, nullable bool) {
			items = append(items, newPushQualItem(rv, ci, pos, nullable || chainOuter, cat))
		}
		add(it.Base, 0, false)
		for k, j := range it.Joins {
			switch j.Type {
			case parser.JoinInner, parser.JoinCross, parser.JoinLeft, parser.JoinRight, parser.JoinFull:
			default:
				return s
			}
			if j.Natural || len(j.Using) > 0 {
				chainOuter = true
				for _, x := range items {
					if x.chain == ci {
						x.nullable = true
					}
				}
			}
			add(j.Right, k+1, j.Type == parser.JoinLeft)
			nullablePos := -1
			if j.Type == parser.JoinLeft {
				nullablePos = len(items) - 1
			}
			for _, c := range splitParserConjuncts(j.On, nil) {
				if a, b, ok := columnEquality(c); ok {
					eqs = append(eqs, pushQualEq{a: a, b: b, leftNullable: nullablePos, expr: c, chain: ci, join: k})
				}
			}
		}
		flat += 1 + len(it.Joins)
	}
	if len(s.From) != flat {
		return s
	}
	for _, x := range items {
		if x.rv.Lateral {
			return s
		}
	}
	conjuncts := splitParserConjuncts(s.Where, nil)
	for _, c := range conjuncts {
		if a, b, ok := columnEquality(c); ok {
			eqs = append(eqs, pushQualEq{a: a, b: b, leftNullable: -1})
		}
	}
	for i := range eqs {
		e := &eqs[i]
		ap, aok := resolvePushQualColumn(e.a, items)
		bp, bok := resolvePushQualColumn(e.b, items)
		e.apos, e.bpos, e.ok = ap, bp, aok && bok
		e.acol, e.bcol = strings.ToLower(e.a.Column), strings.ToLower(e.b.Column)
	}

	var keep []parser.Expr
	moved := false
	redundant := map[parser.Expr]bool{}
	for _, c := range conjuncts {
		if used, ok := pushGroupedItemConjunct(c, items, eqs); ok {
			moved = true
			for _, ei := range used {
				redundant[eqs[ei].expr] = true
			}
			continue
		}
		keep = append(keep, c)
	}
	if !moved {
		return s
	}

	out := *s
	out.FromExprs = make([]parser.FromExpr, len(s.FromExprs))
	out.From = make([]parser.RangeVar, 0, len(s.From))
	for ci, it := range s.FromExprs {
		ni := it
		ni.Joins = append([]parser.JoinExpr(nil), it.Joins...)
		for k := range ni.Joins {
			ni.Joins[k].On = dropRedundantOnConjuncts(ni.Joins[k].On, redundant)
		}
		for _, x := range items {
			if x.chain != ci || len(x.pushed) == 0 {
				continue
			}
			rv := pushedItemRangeVar(x)
			if x.chainPos == 0 {
				ni.Base = rv
			} else {
				ni.Joins[x.chainPos-1].Right = rv
			}
		}
		out.FromExprs[ci] = ni
		out.From = append(out.From, ni.Base)
		for _, j := range ni.Joins {
			out.From = append(out.From, j.Right)
		}
	}
	out.Where = andParserConjuncts(keep)
	for _, x := range items {
		if len(x.pushed) > 0 && x.cte != nil {
			// The preplanned body is dead: the reference now plans as the
			// derived table carrying the pushed qual (inline_cte).
			takeBackPulledBodyRefs(x.cte)
		}
	}
	return &out
}

// dropRedundantOnConjuncts removes from a LEFT join's ON clause the
// equalities a pushed constant has made redundant (M0146-0005dz). Once
// `pres.y = const` is in the preserved item's body and `null.y = const` in
// the nullable item's (the push followed this very ON equality), every pair
// the join can form satisfies `null.y = pres.y`, so the clause tests
// nothing: PG's reconsider_outer_join_clauses (equivclass.c) removes it and
// throws back a constant-TRUE clause only so the join keeps a clause. Here
// the last remaining conjunct is never dropped, which serves the same
// purpose. TPC-DS Q78's `ws_sold_year = ss_sold_year` is the case: PG merges
// on item and customer alone, and the year no longer multiplies into the
// join's selectivity.
func dropRedundantOnConjuncts(on parser.Expr, redundant map[parser.Expr]bool) parser.Expr {
	if on == nil || len(redundant) == 0 {
		return on
	}
	cs := splitParserConjuncts(on, nil)
	var keep []parser.Expr
	for _, c := range cs {
		if !redundant[c] {
			keep = append(keep, c)
		}
	}
	if len(keep) == len(cs) || len(keep) == 0 {
		return on
	}
	return andParserConjuncts(keep)
}

// pushGroupedItemConjunct pushes c into the grouped items it restricts and
// reports whether it moved. Nothing is recorded unless every target accepts.
// used lists the eqs entries (LEFT join ON equalities) through which the
// constant reached a nullable partner; once the push is recorded each of
// them compares two columns pinned to the same constant.
func pushGroupedItemConjunct(c parser.Expr, items []*pushQualItem, eqs []pushQualEq) (used []int, moved bool) {
	b, ok := c.(*parser.BinaryOp)
	if !ok {
		return nil, false
	}
	switch b.Op {
	case parser.OpEq, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe, parser.OpNe:
	default:
		return nil, false
	}
	col, colLeft := b.Left.(*parser.ColumnRef)
	cst := b.Right
	if !colLeft {
		col, ok = b.Right.(*parser.ColumnRef)
		if !ok {
			return nil, false
		}
		cst = b.Left
	}
	if !pushQualConst(cst) {
		return nil, false
	}
	start, ok := resolvePushQualColumn(col, items)
	if !ok || items[start].nullable {
		return nil, false
	}
	// The equality class of the column, reached through eqs.
	type member struct {
		pos int
		col *parser.ColumnRef
	}
	members := []member{{start, col}}
	seen := map[string]bool{pushQualKey(start, col.Column): true}
	for i := 0; i < len(members); i++ {
		m := members[i]
		mcol := strings.ToLower(m.col.Column)
		for ei, e := range eqs {
			var other *parser.ColumnRef
			var opos int
			switch {
			case !e.ok:
				// An unresolved equality naming this column may be one of
				// its partners: decline rather than lose it.
				if e.acol == mcol || e.bcol == mcol {
					return nil, false
				}
				continue
			case e.apos == m.pos && e.acol == mcol:
				other, opos = e.b, e.bpos
			case e.bpos == m.pos && e.bcol == mcol:
				other, opos = e.a, e.apos
			default:
				continue
			}
			k := pushQualKey(opos, other.Column)
			if seen[k] {
				continue
			}
			if b.Op != parser.OpEq {
				// PG derives nothing from an inequality through an
				// equivalence class; goopg must not lose what it derives.
				return nil, false
			}
			// A partner on a LEFT join's nullable side takes the constant
			// only through that join's own ON clause, from its preserved
			// side (reconsider_outer_join_clauses); a nullable member
			// passes it no further.
			if items[m.pos].nullable || (items[opos].nullable && e.leftNullable != opos) {
				return nil, false
			}
			seen[k] = true
			members = append(members, member{opos, other})
			if items[opos].nullable && e.leftNullable == opos {
				used = append(used, ei)
			}
		}
	}
	type target struct {
		x    *pushQualItem
		expr parser.Expr
	}
	var targets []target
	for _, m := range members {
		x := items[m.pos]
		inner, ok := groupedBodyColumn(x, m.col)
		if !ok {
			return nil, false
		}
		var e parser.Expr
		if colLeft {
			e = parser.NewBinaryOp(b.Pos(), b.Op, inner, cst)
		} else {
			e = parser.NewBinaryOp(b.Pos(), b.Op, cst, inner)
		}
		targets = append(targets, target{x, e})
	}
	for _, t := range targets {
		t.x.pushed = append(t.x.pushed, t.expr)
	}
	return used, true
}

func pushQualKey(pos int, col string) string {
	return strconv.Itoa(pos) + "\x00" + strings.ToLower(col)
}

func newPushQualItem(rv parser.RangeVar, chain, pos int, nullable bool, cat catalog.Catalog) *pushQualItem {
	x := &pushQualItem{rv: rv, chain: chain, chainPos: pos, nullable: nullable}
	x.alias = rv.Alias
	if x.alias == "" {
		x.alias = rv.Name
	}
	switch {
	case rv.Subquery != nil:
		if len(rv.Columns) == 0 {
			x.body = rv.Subquery
			x.names, x.namesOK = targetOutputNames(rv.Subquery)
		}
	case rv.TableFunc != nil || rv.Name == "":
	default:
		if rv.Schema == "" {
			if e := planCTEs[strings.ToLower(rv.Name)]; e != nil {
				x.names = map[string]int{}
				for _, c := range e.schema {
					x.names[strings.ToLower(c.Name)]++
				}
				x.namesOK = true
				if e.astRefs == 1 && e.inlineEligible && e.selectOwned &&
					e.materialized != "materialized" && !e.volatile && !e.isDML &&
					e.query != nil && len(e.aliasColumns) == 0 && len(rv.Columns) == 0 {
					x.body, x.cte = e.query, e
				}
				break
			}
		}
		if cat != nil && len(rv.Columns) == 0 {
			if tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name}); ok && tbl != nil {
				x.names = map[string]int{}
				for _, c := range tbl.Columns {
					x.names[strings.ToLower(c.Name)]++
				}
				x.namesOK = true
			}
		}
	}
	if x.body != nil && !pushableGroupedBody(x.body) {
		x.body, x.cte = nil, nil
	}
	return x
}

// pushableGroupedBody is the body half of subquery_is_pushdown_safe for the
// HAVING arm: a plain grouped SELECT.
func pushableGroupedBody(b *parser.SelectStmt) bool {
	if b.With != nil || b.SetOp != nil || b.SetOpOperand != nil || b.GroupingSets != nil ||
		len(b.GroupBy) == 0 || b.Distinct || len(b.DistinctOn) > 0 || b.Limit != nil ||
		b.Offset != nil || b.WithTies || len(b.Locking) > 0 || len(b.WindowClause) > 0 ||
		len(b.ValuesRows) > 0 || len(b.Targets) == 0 {
		return false
	}
	for _, t := range b.Targets {
		if t.Expr == nil || parserExprHasNode(reflect.ValueOf(t.Expr), 0, func(x any) bool {
			fc, ok := x.(*parser.FuncCall)
			return ok && fc.Over != nil
		}) {
			return false
		}
	}
	return true
}

// targetOutputNames is the exposed column-name multiset of a SELECT's target
// list; false when a star (or an unnameable target) makes it unknown.
func targetOutputNames(b *parser.SelectStmt) (map[string]int, bool) {
	names := map[string]int{}
	for _, t := range b.Targets {
		n, ok := targetOutputName(t)
		if !ok {
			return nil, false
		}
		names[n]++
	}
	return names, true
}

func targetOutputName(t parser.ResTarget) (string, bool) {
	if t.Alias != "" {
		return strings.ToLower(t.Alias), true
	}
	switch e := t.Expr.(type) {
	case *parser.ColumnRef:
		if e.Column == "" || e.Column == "*" {
			return "", false
		}
		return strings.ToLower(e.Column), true
	case *parser.StarExpr:
		return "", false
	case *parser.FuncCall:
		return strings.ToLower(e.Name.Name), true
	}
	return "?column?", true
}

// resolvePushQualColumn finds the one FROM position a column reference names.
func resolvePushQualColumn(cr *parser.ColumnRef, items []*pushQualItem) (int, bool) {
	if cr == nil || cr.Schema != "" || cr.Column == "" || cr.Column == "*" {
		return 0, false
	}
	name := strings.ToLower(cr.Column)
	found := -1
	for i, x := range items {
		if cr.Table != "" {
			if !strings.EqualFold(x.alias, cr.Table) {
				continue
			}
			if found >= 0 || !x.namesOK || x.names[name] != 1 {
				return 0, false
			}
			found = i
			continue
		}
		if !x.namesOK {
			return 0, false
		}
		switch x.names[name] {
		case 0:
		case 1:
			if found >= 0 {
				return 0, false
			}
			found = i
		default:
			return 0, false
		}
	}
	return found, found >= 0
}

// groupedBodyColumn maps an outer reference to item x's output column onto
// the body expression it stands for, when that expression is a plain column
// the body groups on — the only substitution the HAVING arm needs
// (ReplaceVarsFromTargetList over a grouping Var).
func groupedBodyColumn(x *pushQualItem, cr *parser.ColumnRef) (parser.Expr, bool) {
	if x.body == nil {
		return nil, false
	}
	name := strings.ToLower(cr.Column)
	idx := -1
	for i, t := range x.body.Targets {
		n, ok := targetOutputName(t)
		if !ok {
			return nil, false
		}
		if n == name {
			if idx >= 0 {
				return nil, false
			}
			idx = i
		}
	}
	if idx < 0 {
		return nil, false
	}
	inner, ok := x.body.Targets[idx].Expr.(*parser.ColumnRef)
	if !ok || inner.Column == "" || inner.Column == "*" {
		return nil, false
	}
	for _, g := range x.body.GroupBy {
		switch ge := g.(type) {
		case *parser.ColumnRef:
			if sameColumnRef(ge, inner) {
				return inner, true
			}
		case *parser.IntegerConst:
			if int(ge.Value) == idx+1 {
				return inner, true
			}
		}
	}
	return nil, false
}

func sameColumnRef(a, b *parser.ColumnRef) bool {
	return a != nil && b != nil && strings.EqualFold(a.Schema, b.Schema) &&
		strings.EqualFold(a.Table, b.Table) && strings.EqualFold(a.Column, b.Column)
}

// columnEquality reports `a = b` over two column references.
func columnEquality(e parser.Expr) (*parser.ColumnRef, *parser.ColumnRef, bool) {
	b, ok := e.(*parser.BinaryOp)
	if !ok || b.Op != parser.OpEq {
		return nil, nil, false
	}
	l, lok := b.Left.(*parser.ColumnRef)
	r, rok := b.Right.(*parser.ColumnRef)
	return l, r, lok && rok
}

// pushQualConst admits a literal, optionally under a cast.
func pushQualConst(e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.IntegerConst, *parser.NumericConst, *parser.StringConst,
		*parser.TypedStringLit, *parser.BooleanConst:
		return true
	case *parser.CastExpr:
		return pushQualConst(x.Operand)
	}
	return false
}

func splitParserConjuncts(e parser.Expr, out []parser.Expr) []parser.Expr {
	if e == nil {
		return out
	}
	if b, ok := e.(*parser.BinaryOp); ok && b.Op == parser.OpAnd {
		out = splitParserConjuncts(b.Left, out)
		return splitParserConjuncts(b.Right, out)
	}
	return append(out, e)
}

func andParserConjuncts(cs []parser.Expr) parser.Expr {
	var out parser.Expr
	for _, c := range cs {
		if out == nil {
			out = c
			continue
		}
		out = parser.NewBinaryOp(c.Pos(), parser.OpAnd, out, c)
	}
	return out
}

// pushedItemRangeVar is x with its pushed quals ANDed into a copy of its
// body's HAVING; a CTE reference becomes the derived table inline_cte makes.
func pushedItemRangeVar(x *pushQualItem) parser.RangeVar {
	body := *x.body
	having := splitParserConjuncts(body.Having, nil)
	body.Having = andParserConjuncts(append(having, x.pushed...))
	if x.cte != nil {
		return parser.RangeVar{Alias: x.alias, Subquery: &body}
	}
	rv := x.rv
	rv.Subquery = &body
	return rv
}

// cloneFromJoins copies a FROM list and each item's join chain, so join
// types can be rewritten without touching the statement's AST.
func cloneFromJoins(items []parser.FromExpr) []parser.FromExpr {
	out := make([]parser.FromExpr, len(items))
	for i, it := range items {
		out[i] = it
		out[i].Joins = append([]parser.JoinExpr(nil), it.Joins...)
	}
	return out
}

// reducedFromOr is the reduced FROM list when the FROM walk recorded one,
// else from.
func (c *resolveContext) reducedFromOr(from []parser.FromExpr) []parser.FromExpr {
	if c != nil && c.reducedFrom != nil {
		return c.reducedFrom
	}
	return from
}
