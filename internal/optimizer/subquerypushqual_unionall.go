package optimizer

import (
	"math/big"
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0094 — restriction quals pushed into the members of a UNION ALL
// appendrel, as set_append_rel_size does.
//
// A FROM-clause `(A UNION ALL B …) u` that is_simple_union_all admits is an
// appendrel in PG (pull_up_simple_union_all). A restriction on u — a WHERE
// conjunct that reads u's columns and nothing else — is a baserestrictinfo
// of the appendrel parent, and set_append_rel_size gives every child its own
// copy with the parent's columns replaced by the member's expressions
// (adjust_appendrel_attrs), constant-folded: a copy that folds to TRUE is
// dropped, one that folds to FALSE or NULL makes the child a dummy rel that
// leaves the Append (set_dummy_rel_pathlist), and an Append left with one
// child is no Append at all. The quals therefore filter at the members'
// scans and price everything above them. goopg kept such a conjunct as a
// Filter on the Append (`Filter: (src > 0)` over both members).
//
// This pass makes PG's move on the AST, before the FROM list is planned, the
// way pushWhereQualsIntoGroupedItems does for a grouped body: the conjunct
// leaves the WHERE and is ANDed into each member's WHERE with the member's
// target expressions substituted; a member whose copy folds to FALSE or
// NULL is removed from the union, a copy that folds to TRUE adds nothing.
// is_safe_append_member is decided before PG pushes anything, so each
// member's original WHERE is kept on the statement scope for
// isSafeAppendMember (M0146-0093) to read.
//
// Declined — the conjunct stays a Filter, as before: a conjunct that also
// reads another FROM item, holds a sublink, a window or aggregate call, or a
// function that is not a known non-volatile built-in; an item on the
// nullable side of an outer join or under RIGHT/FULL/NATURAL/USING; a
// LATERAL item; a union whose members are not flat (a parenthesised
// compound) or use `*`; and a push that would drop every member.

// unionAllPushItem is one appendrel FROM item that may take pushed quals.
type unionAllPushItem struct {
	names   []string             // the union's output column names, lower-cased
	members []*parser.SelectStmt // the flat member list, in order
	// targets is each member's target list with `*` expanded over its
	// FROM tables, one entry per union column.
	targets [][]parser.ResTarget
	pushed  [][]parser.Expr // per member, the substituted conjuncts
	dropped []bool          // per member, folded to FALSE/NULL
	moved   []parser.Expr   // the WHERE conjuncts this item took
}

func pushWhereQualsIntoUnionAllItems(s *parser.SelectStmt, cat catalog.Catalog, scope *rtableScope) *parser.SelectStmt {
	if s == nil || s.Where == nil || len(s.FromExprs) == 0 || scope == nil {
		return s
	}
	var items []*pushQualItem
	flat := 0
	for ci, it := range s.FromExprs {
		chainOuter := false
		for _, j := range it.Joins {
			if j.Type == parser.JoinRight || j.Type == parser.JoinFull || j.Natural || len(j.Using) > 0 {
				chainOuter = true
			}
		}
		items = append(items, newPushQualItem(it.Base, ci, 0, chainOuter, cat))
		for k, j := range it.Joins {
			switch j.Type {
			case parser.JoinInner, parser.JoinCross, parser.JoinLeft, parser.JoinRight, parser.JoinFull:
			default:
				return s
			}
			items = append(items, newPushQualItem(j.Right, ci, k+1, chainOuter || j.Type == parser.JoinLeft, cat))
		}
		flat += 1 + len(it.Joins)
	}
	if len(s.From) != flat {
		return s
	}
	unions := map[int]*unionAllPushItem{}
	for i, x := range items {
		if x.rv.Lateral {
			return s
		}
		if x.nullable || !pullupUnionAllLeaf(x.rv) {
			continue
		}
		if u := newUnionAllPushItem(x.rv, cat); u != nil {
			unions[i] = u
			x.names = map[string]int{}
			for _, n := range u.names {
				x.names[n]++
			}
			x.namesOK = true
		}
	}
	if len(unions) == 0 {
		return s
	}

	var keep []parser.Expr
	for _, c := range splitParserConjuncts(s.Where, nil) {
		if !pushUnionAllConjunct(c, items, unions, cat) {
			keep = append(keep, c)
		}
	}
	// A push that would leave a union with no member is not made: the
	// conjunct goes back to the WHERE (PG's all-dummy appendrel is a
	// dummy rel, a shape this pass does not build).
	moved := false
	for i, u := range unions {
		live := 0
		for _, d := range u.dropped {
			if !d {
				live++
			}
		}
		if live == 0 {
			keep = append(keep, u.moved...)
			delete(unions, i)
			continue
		}
		if len(u.moved) > 0 {
			moved = true
		}
	}
	if !moved {
		return s
	}

	out := *s
	out.FromExprs = cloneFromJoins(s.FromExprs)
	out.From = make([]parser.RangeVar, 0, len(s.From))
	for i, u := range unions {
		if len(u.moved) == 0 {
			continue
		}
		x := items[i]
		rv := x.rv
		rv.Subquery = u.rebuild(scope)
		if x.chainPos == 0 {
			out.FromExprs[x.chain].Base = rv
		} else {
			out.FromExprs[x.chain].Joins[x.chainPos-1].Right = rv
		}
	}
	for _, it := range out.FromExprs {
		out.From = append(out.From, it.Base)
		for _, j := range it.Joins {
			out.From = append(out.From, j.Right)
		}
	}
	out.Where = andParserConjuncts(keep)
	return &out
}

// newUnionAllPushItem lists a simple UNION ALL subquery's members and its
// output names, or returns nil when either is unknowable.
func newUnionAllPushItem(rv parser.RangeVar, cat catalog.Catalog) *unionAllPushItem {
	head := rv.Subquery
	var members []*parser.SelectStmt
	for cur := head; cur != nil; {
		if cur.SetOpOperand != nil {
			return nil
		}
		members = append(members, cur)
		if cur.SetOp == nil {
			break
		}
		next := cur.SetOp.Right
		if next == nil || next.Parenthesized && next.SetOp != nil {
			return nil
		}
		cur = next
	}
	if len(members) < 2 {
		return nil
	}
	targets := make([][]parser.ResTarget, len(members))
	for k, m := range members {
		if len(m.ValuesRows) > 0 || !unionMemberPushSafe(m, cat) {
			return nil
		}
		ts, ok := expandMemberStarTargets(m, cat)
		if !ok {
			return nil
		}
		targets[k] = ts
	}
	width := len(targets[0])
	for _, ts := range targets {
		if len(ts) != width {
			return nil
		}
	}
	names := make([]string, width)
	for i, t := range targets[0] {
		if i < len(rv.Columns) && rv.Columns[i] != "" {
			names[i] = strings.ToLower(rv.Columns[i])
			continue
		}
		n, ok := targetOutputName(t)
		if !ok {
			return nil
		}
		names[i] = n
	}
	return &unionAllPushItem{names: names, members: members, targets: targets,
		pushed: make([][]parser.Expr, len(members)), dropped: make([]bool, len(members))}
}

// unionMemberPushSafe reports whether a member's WHERE may take a pushed
// copy without changing what the member returns: a plain SELECT, the members
// PG pulls into the appendrel. A grouped, DISTINCT, LIMITed or windowed
// member is a subquery RTE in PG, where subquery_push_qual applies its own
// rules (a qual on an aggregate output cannot become a WHERE qual at all,
// and one pushed below a LIMIT selects different rows); this pass leaves
// such a union's quals where they are. A volatile, aggregate, window or
// set-returning call in the targets is refused too (pullupSafeTargetCall).
func unionMemberPushSafe(m *parser.SelectStmt, cat catalog.Catalog) bool {
	if len(m.GroupBy) > 0 || m.GroupingSets != nil || m.Having != nil || m.Distinct ||
		len(m.DistinctOn) > 0 || m.Limit != nil || m.Offset != nil || m.WithTies ||
		m.With != nil || len(m.Locking) > 0 || len(m.WindowClause) > 0 {
		return false
	}
	for _, t := range m.Targets {
		if t.Expr == nil {
			return false
		}
		var refs []*parser.ColumnRef
		ok := true
		collectPushableRefs(reflect.ValueOf(t.Expr), 0, &refs, &ok, cat)
		if !ok {
			return false
		}
	}
	return true
}

// expandMemberStarTargets is a member's target list with every `*` and
// `alias.*` replaced by the columns it stands for, read from the catalog:
// only when each FROM item is a plain table and no join merges columns
// (NATURAL / USING change what `*` lists). false when that cannot be told.
func expandMemberStarTargets(m *parser.SelectStmt, cat catalog.Catalog) ([]parser.ResTarget, bool) {
	hasStar := false
	for _, t := range m.Targets {
		if t.Expr == nil {
			return nil, false
		}
		switch x := t.Expr.(type) {
		case *parser.StarExpr:
			hasStar = true
		case *parser.ColumnRef:
			if x.Column == "*" {
				return nil, false
			}
		}
	}
	if !hasStar {
		return m.Targets, true
	}
	if cat == nil {
		return nil, false
	}
	for _, f := range m.FromExprs {
		for _, j := range f.Joins {
			if j.Natural || len(j.Using) > 0 {
				return nil, false
			}
		}
	}
	type fromTable struct {
		alias string
		cols  []string
	}
	var tables []fromTable
	for _, rv := range m.From {
		if rv.Subquery != nil || rv.TableFunc != nil || rv.Name == "" || len(rv.Columns) > 0 {
			return nil, false
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok || tbl == nil {
			return nil, false
		}
		alias := rv.Alias
		if alias == "" {
			alias = rv.Name
		}
		ft := fromTable{alias: alias}
		for _, c := range tbl.Columns {
			if !c.Dropped {
				ft.cols = append(ft.cols, c.Name)
			}
		}
		tables = append(tables, ft)
	}
	var out []parser.ResTarget
	for _, t := range m.Targets {
		st, isStar := t.Expr.(*parser.StarExpr)
		if !isStar {
			out = append(out, t)
			continue
		}
		if st.Schema != "" {
			return nil, false
		}
		matched := false
		for _, ft := range tables {
			if st.Table != "" && !strings.EqualFold(st.Table, ft.alias) {
				continue
			}
			matched = true
			for _, c := range ft.cols {
				out = append(out, parser.ResTarget{Expr: &parser.ColumnRef{Table: ft.alias, Column: c}})
			}
		}
		if !matched {
			return nil, false
		}
	}
	return out, true
}

// pushUnionAllConjunct moves conjunct c into the union item every one of
// its column references resolves to, reporting whether it moved.
func pushUnionAllConjunct(c parser.Expr, items []*pushQualItem, unions map[int]*unionAllPushItem, cat catalog.Catalog) bool {
	var refs []*parser.ColumnRef
	ok := true
	collectPushableRefs(reflect.ValueOf(c), 0, &refs, &ok, cat)
	if !ok || len(refs) == 0 {
		return false
	}
	target := -1
	for _, cr := range refs {
		p, found := resolvePushQualColumn(cr, items)
		if !found || (target >= 0 && p != target) {
			return false
		}
		target = p
	}
	u := unions[target]
	if u == nil {
		return false
	}
	colOf := func(cr *parser.ColumnRef) (int, bool) {
		name, at := strings.ToLower(cr.Column), -1
		for i, n := range u.names {
			if n == name {
				if at >= 0 {
					return 0, false
				}
				at = i
			}
		}
		return at, at >= 0
	}
	// tlist_same_datatypes: the member's expression stands for the union's
	// column only when every member yields the same type there — otherwise
	// the union coerces (char(5) UNION ALL text is bpchar, and 'x  ' = 'x'
	// holds), PG makes no appendrel, and a member-local copy would compare
	// under the wrong type.
	for _, cr := range refs {
		i, ok := colOf(cr)
		if !ok || !unionColumnTypesAgree(u.members, u.targets, i, cat) {
			return false
		}
	}
	subst := make([]parser.Expr, len(u.members))
	for mi := range u.members {
		e, sok := substituteParserColumns(c, func(cr *parser.ColumnRef) (parser.Expr, bool) {
			i, ok := colOf(cr)
			if !ok {
				return nil, false
			}
			return u.targets[mi][i].Expr, true
		})
		if !sok {
			return false
		}
		subst[mi] = e
	}
	for mi, e := range subst {
		if u.dropped[mi] {
			continue
		}
		if v, isNull, folded := foldParserConstBool(e); folded {
			if isNull || !v {
				u.dropped[mi] = true
			}
			continue
		}
		u.pushed[mi] = append(u.pushed[mi], e)
	}
	u.moved = append(u.moved, c)
	return true
}

// unionColumnTypesAgree reports whether every member's target i has one
// known, identical type signature (memberTargetTypeSig).
func unionColumnTypesAgree(members []*parser.SelectStmt, targets [][]parser.ResTarget, i int, cat catalog.Catalog) bool {
	// A string literal is "unknown" until the union resolves it; beside
	// text members it becomes text in its own leaf, so the two agree.
	sig := ""
	for k, m := range members {
		s := memberTargetTypeSig(m, targets[k][i].Expr, cat)
		switch {
		case s == "":
			return false
		case sig == "" || sig == "unknown" && s == "text":
			sig = s
		case s == sig || s == "unknown" && sig == "text":
		default:
			return false
		}
	}
	return true
}

// memberTargetTypeSig is a target expression's type as far as the AST tells
// it: a literal's own type ("unknown" for a string literal, which every
// member then resolves alike), a cast's normalised base type, or a column's
// catalog type found through the member's own FROM tables. "" when unknown.
func memberTargetTypeSig(m *parser.SelectStmt, e parser.Expr, cat catalog.Catalog) string {
	switch x := e.(type) {
	case *parser.IntegerConst:
		if x.Value >= -2147483648 && x.Value <= 2147483647 {
			return "int4"
		}
		return "int8"
	case *parser.NumericConst:
		return "numeric"
	case *parser.StringConst:
		return "unknown"
	case *parser.BooleanConst:
		return "bool"
	case *parser.CastExpr:
		if x.Type.Schema != "" && !strings.EqualFold(x.Type.Schema, "pg_catalog") {
			return ""
		}
		return normalizePushTypeName(x.Type.Name)
	case *parser.ColumnRef:
		if cat == nil || x.Schema != "" || x.Column == "" || x.Column == "*" {
			return ""
		}
		found := ""
		for _, rv := range m.From {
			if rv.Subquery != nil || rv.TableFunc != nil || rv.Name == "" {
				if x.Table == "" || strings.EqualFold(x.Table, rv.Alias) {
					return ""
				}
				continue
			}
			alias := rv.Alias
			if alias == "" {
				alias = rv.Name
			}
			if x.Table != "" && !strings.EqualFold(x.Table, alias) {
				continue
			}
			tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
			if !ok || tbl == nil {
				return ""
			}
			for _, c := range tbl.Columns {
				if !strings.EqualFold(c.Name, x.Column) {
					continue
				}
				if found != "" || c.Type.IsArray {
					return ""
				}
				found = normalizePushTypeName(c.Type.Name)
			}
		}
		return found
	}
	return ""
}

// normalizePushTypeName folds a type name's SQL spellings onto one name.
func normalizePushTypeName(n string) string {
	switch strings.ToLower(n) {
	case "int2", "smallint":
		return "int2"
	case "int4", "int", "integer":
		return "int4"
	case "int8", "bigint":
		return "int8"
	case "numeric", "decimal":
		return "numeric"
	case "float4", "real":
		return "float4"
	case "float8", "double precision":
		return "float8"
	case "varchar", "character varying":
		return "varchar"
	case "char", "character", "bpchar":
		return "bpchar"
	case "bool", "boolean":
		return "bool"
	case "":
		return ""
	}
	return strings.ToLower(n)
}

// rebuild returns a copy of the union with the pushed conjuncts in each live
// member's WHERE and the dropped members removed. The originals are never
// mutated (a cached plan reuses the AST). Each member that took a conjunct
// has its original WHERE recorded on the scope, so isSafeAppendMember judges
// the member PG's pull-up saw.
func (u *unionAllPushItem) rebuild(scope *rtableScope) *parser.SelectStmt {
	var live []*parser.SelectStmt
	var link *parser.SetOpClause
	for _, m := range u.members {
		if m.SetOp != nil {
			link = m.SetOp
			break
		}
	}
	for mi, m := range u.members {
		if u.dropped[mi] {
			continue
		}
		c := *m
		c.SetOp = nil
		if len(u.pushed[mi]) > 0 {
			c.Where = andParserConjuncts(append(splitParserConjuncts(m.Where, nil), u.pushed[mi]...))
			scope.recordAppendMemberOrigWhere(&c, m.Where)
		}
		live = append(live, &c)
	}
	head := live[0]
	if u.dropped[0] {
		// The union's column names are its first member's: a new head
		// takes the old names as target aliases, over its `*`-expanded
		// target list so each union column has its own entry.
		for k := range u.members {
			if !u.dropped[k] {
				head.Targets = append([]parser.ResTarget(nil), u.targets[k]...)
				break
			}
		}
		for i := range head.Targets {
			head.Targets[i].Alias = u.names[i]
		}
	}
	for i := 0; i+1 < len(live); i++ {
		l := *link
		l.Right = live[i+1]
		live[i].SetOp = &l
	}
	return head
}

// collectPushableRefs walks a conjunct, collecting its column references and
// clearing ok when it holds something a member copy must not: a sublink, a
// window or aggregate call, or a call that is not a known non-volatile
// built-in (pullupSafeTargetCall's test).
func collectPushableRefs(v reflect.Value, depth int, refs *[]*parser.ColumnRef, ok *bool, cat catalog.Catalog) {
	if !*ok || depth > 64 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		x := v.Interface()
		switch n := x.(type) {
		case *parser.SelectStmt:
			*ok = false
			return
		case *parser.ColumnRef:
			if n.Column == "*" {
				*ok = false
				return
			}
			*refs = append(*refs, n)
			return
		case *parser.FuncCall:
			if !pullupSafeTargetCall(n, cat) {
				*ok = false
				return
			}
		}
		collectPushableRefs(v.Elem(), depth+1, refs, ok, cat)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				collectPushableRefs(v.Field(i), depth+1, refs, ok, cat)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			collectPushableRefs(v.Index(i), depth+1, refs, ok, cat)
		}
	}
}

// substituteParserColumns returns a copy of e with every column reference
// replaced by repl's expression (ReplaceVarsFromTargetList on the AST). The
// copy shares nothing it rewrites with e; untouched subtrees are shared,
// which is safe because nothing downstream mutates the AST in place. It
// reports false when repl refuses a reference.
func substituteParserColumns(e parser.Expr, repl func(*parser.ColumnRef) (parser.Expr, bool)) (parser.Expr, bool) {
	ok := true
	exprType := reflect.TypeOf((*parser.Expr)(nil)).Elem()
	var rewrite func(v reflect.Value, depth int) reflect.Value
	rewrite = func(v reflect.Value, depth int) reflect.Value {
		if !ok || depth > 64 || !v.IsValid() {
			return v
		}
		switch v.Kind() {
		case reflect.Interface:
			if v.IsNil() {
				return v
			}
			if cr, isCol := v.Interface().(*parser.ColumnRef); isCol && v.Type() == exprType {
				r, rok := repl(cr)
				if !rok {
					ok = false
					return v
				}
				return reflect.ValueOf(&r).Elem()
			}
			inner := rewrite(v.Elem(), depth+1)
			out := reflect.New(v.Type()).Elem()
			out.Set(inner)
			return out
		case reflect.Pointer:
			if v.IsNil() || v.Elem().Kind() != reflect.Struct {
				return v
			}
			cp := reflect.New(v.Elem().Type())
			cp.Elem().Set(v.Elem())
			st := cp.Elem()
			for i := 0; i < st.NumField(); i++ {
				f := st.Field(i)
				if !st.Type().Field(i).IsExported() || !f.CanSet() {
					continue
				}
				f.Set(rewrite(f, depth+1))
			}
			return cp
		case reflect.Struct:
			cp := reflect.New(v.Type()).Elem()
			cp.Set(v)
			for i := 0; i < cp.NumField(); i++ {
				f := cp.Field(i)
				if !cp.Type().Field(i).IsExported() || !f.CanSet() {
					continue
				}
				f.Set(rewrite(f, depth+1))
			}
			return cp
		case reflect.Slice:
			if v.IsNil() {
				return v
			}
			cp := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			for i := 0; i < v.Len(); i++ {
				cp.Index(i).Set(rewrite(v.Index(i), depth+1))
			}
			return cp
		}
		return v
	}
	if cr, isCol := e.(*parser.ColumnRef); isCol {
		return repl(cr)
	}
	out := rewrite(reflect.ValueOf(&e).Elem(), 0)
	if !ok {
		return nil, false
	}
	return out.Interface().(parser.Expr), true
}

// foldParserConstBool evaluates a predicate over literals the way
// eval_const_expressions would settle it: comparisons of two numeric
// literals, `=` / `<>` of two string literals (ordering would need the
// collation), boolean literals, NULL, IS [NOT] NULL of a literal, and
// AND/OR/NOT over those with three-valued logic. folded is false for
// anything else; the copy is then pushed as it stands.
func foldParserConstBool(e parser.Expr) (val, isNull, folded bool) {
	switch x := e.(type) {
	case *parser.BooleanConst:
		return x.Value, false, true
	case *parser.NullConst:
		return false, true, true
	case *parser.IsNullExpr:
		switch x.Operand.(type) {
		case *parser.NullConst:
			return !x.Negated, false, true
		case *parser.IntegerConst, *parser.NumericConst, *parser.StringConst, *parser.BooleanConst:
			return x.Negated, false, true
		}
		return false, false, false
	case *parser.UnaryOp:
		if x.Op != parser.OpNot {
			return false, false, false
		}
		v, n, ok := foldParserConstBool(x.Operand)
		if !ok || n {
			return false, n, ok
		}
		return !v, false, true
	case *parser.BinaryOp:
		switch x.Op {
		case parser.OpAnd, parser.OpOr:
			lv, ln, lok := foldParserConstBool(x.Left)
			rv, rn, rok := foldParserConstBool(x.Right)
			if !lok || !rok {
				return false, false, false
			}
			if x.Op == parser.OpAnd {
				if (!ln && !lv) || (!rn && !rv) {
					return false, false, true
				}
				if ln || rn {
					return false, true, true
				}
				return true, false, true
			}
			if (!ln && lv) || (!rn && rv) {
				return true, false, true
			}
			if ln || rn {
				return false, true, true
			}
			return false, false, true
		case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe:
			return foldParserConstCompare(x.Op, x.Left, x.Right)
		}
	}
	return false, false, false
}

func foldParserConstCompare(op parser.OpCode, l, r parser.Expr) (val, isNull, folded bool) {
	l, r = unwrapValuePreservingCast(l), unwrapValuePreservingCast(r)
	_, lnull := l.(*parser.NullConst)
	_, rnull := r.(*parser.NullConst)
	if lnull || rnull {
		if parserIsLiteral(l) && parserIsLiteral(r) {
			return false, true, true
		}
		return false, false, false
	}
	var cmp int
	switch {
	case parserNumericLiteral(l) != nil && parserNumericLiteral(r) != nil:
		cmp = parserNumericLiteral(l).Cmp(parserNumericLiteral(r))
	case isStringConst(l) && isStringConst(r):
		if op != parser.OpEq && op != parser.OpNe {
			return false, false, false
		}
		if l.(*parser.StringConst).Value == r.(*parser.StringConst).Value {
			cmp = 0
		} else {
			cmp = 1
		}
	default:
		return false, false, false
	}
	switch op {
	case parser.OpEq:
		return cmp == 0, false, true
	case parser.OpNe:
		return cmp != 0, false, true
	case parser.OpLt:
		return cmp < 0, false, true
	case parser.OpGt:
		return cmp > 0, false, true
	case parser.OpLe:
		return cmp <= 0, false, true
	case parser.OpGe:
		return cmp >= 0, false, true
	}
	return false, false, false
}

// unwrapValuePreservingCast strips a cast of a literal that cannot change
// the literal's value or how it compares: a string literal to text or
// varchar, a numeric literal to an integer or numeric type, both without a
// typmod. Anything else (bpchar's padding, a float, a typmod's rounding) is
// left as it is and so is not folded.
func unwrapValuePreservingCast(e parser.Expr) parser.Expr {
	c, ok := e.(*parser.CastExpr)
	if !ok || len(c.Typmods) > 0 || c.Type.Schema != "" && !strings.EqualFold(c.Type.Schema, "pg_catalog") {
		return e
	}
	switch strings.ToLower(c.Type.Name) {
	case "text", "varchar", "character varying":
		if isStringConst(c.Operand) {
			return c.Operand
		}
	case "int2", "int4", "int8", "smallint", "integer", "int", "bigint", "numeric", "decimal":
		if _, isInt := c.Operand.(*parser.IntegerConst); isInt {
			return c.Operand
		}
		if n, isNum := c.Operand.(*parser.NumericConst); isNum && (strings.ToLower(c.Type.Name) == "numeric" || strings.ToLower(c.Type.Name) == "decimal") {
			return n
		}
	}
	return e
}

func parserIsLiteral(e parser.Expr) bool {
	switch e.(type) {
	case *parser.IntegerConst, *parser.NumericConst, *parser.StringConst, *parser.BooleanConst, *parser.NullConst:
		return true
	}
	return false
}

func isStringConst(e parser.Expr) bool {
	_, ok := e.(*parser.StringConst)
	return ok
}

// parserNumericLiteral is an integer or numeric literal's exact value.
func parserNumericLiteral(e parser.Expr) *big.Rat {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return new(big.Rat).SetInt64(x.Value)
	case *parser.NumericConst:
		if r, ok := new(big.Rat).SetString(x.Value); ok {
			return r
		}
	}
	return nil
}
