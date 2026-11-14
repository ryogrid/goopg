package optimizer

// M0148-0001c3a2 — BitmapOr paths over OR restriction clauses: PG's
// generate_bitmap_or_paths / build_paths_for_OR (indxpath.c) and the
// BitmapOr arm of create_bitmap_subplan (createplan.c).
//
// For a restriction conjunct `A1 OR A2 OR …`, every arm must be matched by at
// least one bitmap index path built from the arm's own clauses (plus the
// relation's other restriction clauses, which may complete the index-column
// prefix); the arm's paths are combined by choose_bitmap_and and the arms are
// ORed into one BitmapOr. Design:
// docs/design/0100-0149/0148-0001c3a2-bitmap-or-paths.md.

import (
	"math"
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// flattenExprOr flattens a planner Expr's top-level OR tree into its arms, as
// PG's canonicalize_qual leaves an OR clause flat. Non-OR expressions are
// returned in a singleton slice.
func flattenExprOr(expr Expr) []Expr {
	if expr == nil {
		return nil
	}
	bin, ok := expr.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []Expr{expr}
	}
	return append(flattenExprOr(bin.Left), flattenExprOr(bin.Right)...)
}

// combineOr builds a left-associated OR tree over the given arms (combineAnd's
// OR twin). nil/empty input returns nil.
func combineOr(arms []Expr) Expr {
	if len(arms) == 0 {
		return nil
	}
	out := arms[0]
	for _, a := range arms[1:] {
		out = &BinaryOp{pos: out.Pos(), Op: parser.OpOr, Left: out, Right: a}
	}
	return out
}

// groupSimilarOrArgs is PG 18's group_similar_or_args (indxpath.c): arms of
// the form `indexkey op constant` on the same index key column with the same
// operator fold into one sub-OR, placed where the group's first arm stood;
// the others keep their order. The sub-OR is marked Nested — it stays one
// argument of the OR (EXPLAIN prints `((a = 1) OR ((b = 3) OR (b = 7)))`) and
// one item to the implication test. A column counts only when an index that
// takes ScalarArrayOp quals (amsearcharray: btree, not hash) has it as a key.
//
// An inequality group is matched as `col < ANY (…)` (orInequalitySAOP,
// M0148-0001c3a4 (a)); ok is kept for the callers' contract and is always
// true. The operand type part of PG's key (opno, inputcollid) is
// approximated by the key's literal kind (keyTypeTag).
func groupSimilarOrArgs(tbl *catalog.Table, indexes []*catalog.Index, arms []Expr) ([]Expr, bool) {
	type armKey struct {
		col, typ string
		op       parser.OpCode
	}
	keyCols := map[string]bool{}
	for _, idx := range indexes {
		if !idx.HasStorage() || idx.DeclaredHash {
			continue
		}
		for _, c := range idx.Columns {
			keyCols[c] = true
		}
	}
	keys := make([]*armKey, len(arms))
	for i, arm := range arms {
		bin, ok := arm.(*BinaryOp)
		if !ok {
			continue
		}
		cr, val, colOnRight, ok := normalizeColumnIndexKey(bin.Left, bin.Right)
		if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) {
			continue
		}
		name := tbl.Columns[cr.Index].Name
		if !keyCols[name] {
			continue
		}
		op := bin.Op
		if colOnRight {
			op = swapInequalityOp(op)
		}
		keys[i] = &armKey{col: name, typ: keyTypeTag(val), op: op}
	}
	used := make([]bool, len(arms))
	out := make([]Expr, 0, len(arms))
	for i, arm := range arms {
		if used[i] {
			continue
		}
		used[i] = true
		if keys[i] == nil {
			out = append(out, arm)
			continue
		}
		group := []Expr{arm}
		for j := i + 1; j < len(arms); j++ {
			if !used[j] && keys[j] != nil && *keys[j] == *keys[i] {
				used[j] = true
				group = append(group, arms[j])
			}
		}
		if len(group) == 1 {
			out = append(out, arm)
			continue
		}
		// M0148-0001c3a4 (a): an inequality group is matched as
		// `col < ANY (…)` (orInequalitySAOP) like an equality group as
		// `col = ANY (…)`.
		sub := combineOr(group).(*BinaryOp)
		sub.Nested = true
		out = append(out, sub)
	}
	return out, true
}

// keyTypeTag is the operand-type part of groupSimilarOrArgs' key: an
// explicit cast's target type, else the literal's node kind. Two arms whose
// keys differ here (`tenthous = 1::int2` and `tenthous = 3::int8`) use
// different operators in PG and do not group.
func keyTypeTag(e Expr) string {
	if c, ok := e.(*CastExpr); ok {
		return "cast:" + c.TargetType
	}
	return fmt.Sprintf("%T", e)
}

// orEqualitySAOP is match_orclause_to_indexcol's conversion (indxpath.c): an
// OR whose every arm is `col = const` on the named column becomes the
// ScalarArrayOp `col = ANY ('{…}')`, returned as the IN it is priced as
// (scalararraysel, which btcostestimate applies to the converted qual). The
// constants must be keys the btree encoder takes exactly
// (restrictionKeyUsable), so the OR itself is the probe's exact recheck.
// M0148-0001c3a2.
func orEqualitySAOP(cat catalog.Catalog, tbl *catalog.Table, colName string, conj Expr) (*InExpr, bool) {
	bin, ok := conj.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return nil, false
	}
	var cr *ColumnRef
	var elems []Expr
	for _, arm := range flattenExprOr(conj) {
		ab, ok := arm.(*BinaryOp)
		if !ok || ab.Op != parser.OpEq {
			return nil, false
		}
		c, val, _, ok := normalizeColumnIndexKey(ab.Left, ab.Right)
		// M0148-0001c3a6 (i): a cross-type integer Const (`3::int8`) is a
		// key too; group_similar_or_args has already kept one type per OR.
		if !ok || c.Index < 0 || c.Index >= len(tbl.Columns) || tbl.Columns[c.Index].Name != colName ||
			!bitmapConstKey(val) || !restrictionKeyUsable(cat, tbl.Columns[c.Index], val) {
			return nil, false
		}
		// match_orclause_to_indexcol: every constant of one type (one
		// operator) — `1::int2 OR 3::int8` is not one array.
		if len(elems) > 0 && keyTypeTag(val) != keyTypeTag(elems[0]) {
			return nil, false
		}
		cr = c
		elems = append(elems, val)
	}
	if len(elems) < 2 {
		return nil, false
	}
	return &InExpr{pos: conj.Pos(), Operand: cr, List: elems}, true
}

// orInequalitySAOP is match_orclause_to_indexcol's conversion for an OR whose
// every arm is `col op const` with ONE inequality operator on the named
// column (commuted to column-first: `43 > thousand` is `thousand < 43`) —
// `thousand < ANY ('{42,99,43}')`, which nbtree runs as the range of the
// extreme element (M0148-0001c3a4 (a)). The OR is the exact recheck.
func orInequalitySAOP(cat catalog.Catalog, tbl *catalog.Table, colName string, conj Expr) (*InExpr, parser.OpCode, bool) {
	bin, ok := conj.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return nil, parser.OpUnknown, false
	}
	var cr *ColumnRef
	var elems []Expr
	op := parser.OpUnknown
	for _, arm := range flattenExprOr(conj) {
		ab, ok := arm.(*BinaryOp)
		if !ok {
			return nil, parser.OpUnknown, false
		}
		switch ab.Op {
		case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		default:
			return nil, parser.OpUnknown, false
		}
		c, val, colOnRight, ok := normalizeColumnIndexKey(ab.Left, ab.Right)
		if !ok || c.Index < 0 || c.Index >= len(tbl.Columns) || tbl.Columns[c.Index].Name != colName ||
			!isConstExpr(val) || !restrictionKeyUsable(cat, tbl.Columns[c.Index], val) {
			return nil, parser.OpUnknown, false
		}
		armOp := ab.Op
		if colOnRight {
			armOp = swapInequalityOp(armOp)
		}
		if op != parser.OpUnknown && armOp != op {
			return nil, parser.OpUnknown, false
		}
		op, cr = armOp, c
		elems = append(elems, val)
	}
	if len(elems) < 2 {
		return nil, parser.OpUnknown, false
	}
	return &InExpr{pos: conj.Pos(), Operand: cr, List: elems, AnyOp: op}, op, true
}

// saopElementsFor is the element list a ScalarArrayOp index qual probes:
// list itself, or — mirroring collectInValues' single-element expansion —
// the elements of a lone array literal (`= ANY ('{7, 8, 9}')`, or the literal
// cast to the column's own array type), as PG's constant array is
// (_bt_preprocess_array_keys deconstructs it). ok is false when the literal
// cannot be taken apart here exactly (quotes, nested braces, a cast to
// another element type): the qual then stays a Filter, which evaluates it
// correctly. Without this the probe encoded the whole `{7, 8, 9}` string as
// one integer key and failed with 22P02 (M0148-0001c3a4 (i)).
func saopElementsFor(col catalog.Column, list []Expr) ([]Expr, bool) {
	return arrayLiteralElements(col.Type, list)
}

// arrayLiteralElements is saopElementsFor over the operand's type; the
// selectivity estimator calls it with a ColumnRef's type (scalararraysel
// deconstructs the same constant array the probe does).
func arrayLiteralElements(typ catalog.Type, list []Expr) ([]Expr, bool) {
	if len(list) != 1 {
		return list, true
	}
	e := list[0]
	if c, isCast := e.(*CastExpr); isCast {
		sc, isStr := c.Operand.(*StringConst)
		if !isStr || !isArrayLiteralString(sc.Value) {
			return list, true
		}
		if !castIsColumnArrayType(c.TargetType, typ) {
			return nil, false
		}
		e = sc
	}
	sc, isStr := e.(*StringConst)
	if !isStr || !isArrayLiteralString(sc.Value) {
		return list, true
	}
	body := strings.TrimSpace(sc.Value[1 : len(sc.Value)-1])
	if strings.ContainsAny(body, "{}\"\\") {
		return nil, false
	}
	if body == "" {
		// An empty array matches nothing. The primary-array producers keep
		// it a Filter (a zero-element probe has no shape here); a column
		// qual or the selectivity estimator takes it as is.
		return []Expr{}, true
	}
	intCol := false
	switch typ.Name {
	case "int2", "int4", "int8", "smallint", "integer", "bigint", "int":
		intCol = !typ.IsArray
	}
	var out []Expr
	for _, tok := range strings.Split(body, ",") {
		tok = strings.TrimSpace(tok)
		switch {
		case strings.EqualFold(tok, "NULL"):
			out = append(out, &NullConst{pos: sc.pos})
		case tok == "":
			return nil, false
		case intCol:
			v, err := strconv.ParseInt(tok, 10, 64)
			if err != nil {
				return nil, false
			}
			out = append(out, &IntegerConst{pos: sc.pos, Value: v})
		default:
			out = append(out, &StringConst{pos: sc.pos, Value: tok})
		}
	}
	return out, true
}

// isArrayLiteralString is the `{…}` shape collectInValues expands.
func isArrayLiteralString(v string) bool {
	return len(v) >= 2 && v[0] == '{' && v[len(v)-1] == '}'
}

// castIsColumnArrayType reports whether an array cast's target is the
// column's own type as an array (`'{…}'::int[]` on an int4 column).
// crossIntArrayLiteral is `= ANY ('{…}'::T[])` with T another integer type
// than the integer column's: PG's integer_ops btree family holds the
// cross-type `=` (int24eq, int48eq, …), so the array is an index qual. It
// returns the elements the scan can probe — those inside the column
// type's range; one outside matches no key, as PG's exact cross-type
// comparison finds — and the literal's display text `'{33,4400000000}'::
// bigint[]`. false when an element is NULL or not an integer.
func crossIntArrayLiteral(col catalog.Type, list []Expr) ([]Expr, string, bool) {
	if len(list) != 1 || col.IsArray {
		return nil, "", false
	}
	c, isCast := list[0].(*CastExpr)
	if !isCast {
		return nil, "", false
	}
	sc, isStr := c.Operand.(*StringConst)
	if !isStr || !isArrayLiteralString(sc.Value) {
		return nil, "", false
	}
	t := strings.ToLower(strings.TrimSpace(c.TargetType))
	if !strings.HasSuffix(t, "[]") {
		return nil, "", false
	}
	canon := map[string]string{"int": "int4", "integer": "int4", "int4": "int4", "smallint": "int2", "int2": "int2",
		"bigint": "int8", "int8": "int8"}
	display := map[string]string{"int2": "smallint", "int4": "integer", "int8": "bigint"}
	elem, ok1 := canon[strings.TrimSpace(strings.TrimSuffix(t, "[]"))]
	colT, ok2 := canon[strings.ToLower(col.Name)]
	if !ok1 || !ok2 || elem == colT {
		return nil, "", false
	}
	elems, ok := arrayLiteralElements(catalog.Type{Name: "int8"}, []Expr{sc})
	if !ok || len(elems) == 0 {
		return nil, "", false
	}
	lo, hi := int64(math.MinInt64), int64(math.MaxInt64)
	switch colT {
	case "int2":
		lo, hi = math.MinInt16, math.MaxInt16
	case "int4":
		lo, hi = math.MinInt32, math.MaxInt32
	}
	texts := make([]string, 0, len(elems))
	probe := make([]Expr, 0, len(elems))
	for _, e := range elems {
		ic, isInt := e.(*IntegerConst)
		if !isInt {
			return nil, "", false
		}
		texts = append(texts, strconv.FormatInt(ic.Value, 10))
		if ic.Value >= lo && ic.Value <= hi {
			probe = append(probe, ic)
		}
	}
	return probe, "'{" + strings.Join(texts, ",") + "}'::" + display[elem] + "[]", true
}

// crossIntCastKey is the value of an explicit cast of an integer literal to
// an integer type other than the integer column's (`3::int8` against int4),
// when the value fits the column's type; false otherwise.
func crossIntCastKey(c *CastExpr, col catalog.Type) (int64, bool) {
	if c == nil || !c.Explicit || c.Typmod != 0 || col.IsArray {
		return 0, false
	}
	ic, isInt := c.Operand.(*IntegerConst)
	if !isInt {
		return 0, false
	}
	canon := map[string]string{"int": "int4", "integer": "int4", "int4": "int4", "smallint": "int2", "int2": "int2",
		"bigint": "int8", "int8": "int8"}
	to, ok1 := canon[strings.ToLower(c.TargetType)]
	colT, ok2 := canon[strings.ToLower(col.Name)]
	if !ok1 || !ok2 || to == colT {
		return 0, false
	}
	lo, hi := int64(math.MinInt64), int64(math.MaxInt64)
	switch colT {
	case "int2":
		lo, hi = math.MinInt16, math.MaxInt16
	case "int4":
		lo, hi = math.MinInt32, math.MaxInt32
	}
	if ic.Value < lo || ic.Value > hi {
		return 0, false
	}
	return ic.Value, true
}

func castIsColumnArrayType(target string, typ catalog.Type) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	if !strings.HasSuffix(t, "[]") {
		return false
	}
	elem := strings.TrimSpace(strings.TrimSuffix(t, "[]"))
	canon := map[string]string{"int": "int4", "integer": "int4", "int4": "int4", "smallint": "int2", "int2": "int2",
		"bigint": "int8", "int8": "int8", "text": "text"}
	ce, ok1 := canon[elem]
	cc, ok2 := canon[strings.ToLower(typ.Name)]
	return ok1 && ok2 && ce == cc
}

// saopAnyInequality reports an explicit `col op ANY (list)` with an
// inequality operator (`id1 >= ANY (array[1,5])`).
func saopAnyInequality(in *InExpr) (parser.OpCode, bool) {
	if in == nil || in.Negated || in.NotEqualAny || in.AllOp {
		return parser.OpUnknown, false
	}
	switch in.AnyOp {
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		return in.AnyOp, true
	}
	return parser.OpUnknown, false
}

// saopPricedClause is the clause a ScalarArrayOp index clause is priced by:
// the IN conjunct itself, or for an OR of equalities the ScalarArrayOp it
// was converted to (orEqualitySAOP).
func saopPricedClause(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, c indexPathClause) Expr {
	if c.indexCol >= 0 && c.indexCol < len(idx.Columns) {
		if in, ok := orEqualitySAOP(cat, tbl, idx.Columns[c.indexCol], c.local); ok {
			return in
		}
	}
	return c.local
}

// bitmapSAOPOnColumn matches a ScalarArrayOp index qual on index column pos
// for a bitmap path: a `col IN (consts)` / `col = ANY (…)` conjunct
// (match_saopclause_to_indexcol), or an OR whose every arm is `col = const`
// on that column (match_orclause_to_indexcol, which turns it into
// `col = ANY ('{…}')`). The elements must be constants the key encoder takes
// exactly (restrictionKeyUsable), so the conjunct is the recheck (`local`).
// selExpr is the clause to price: the IN itself, or the ScalarArrayOp an OR
// becomes (scalararraysel, as btcostestimate prices the index qual).
// M0148-0001c3a2.
func bitmapSAOPOnColumn(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) (*indexPathClause, Expr) {
	if idx.DeclaredHash || pos < 0 || pos >= len(idx.Columns) {
		return nil, nil
	}
	colName := idx.Columns[pos]
	for _, conj := range conjuncts {
		if in, ok := conj.(*InExpr); ok {
			ineqOp, ineq := saopAnyInequality(in)
			if in.Subquery != nil || in.Plan != nil || len(in.List) == 0 || in.Negated || in.NotEqualAny || in.AllOp ||
				(in.AnyOp != parser.OpUnknown && in.AnyOp != parser.OpEq && !ineq) {
				continue
			}
			cr, ok := in.Operand.(*ColumnRef)
			if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != colName {
				continue
			}
			list, listOK := saopElementsFor(tbl.Columns[cr.Index], in.List)
			if !listOK || len(list) == 0 {
				continue
			}
			usable := true
			for _, e := range list {
				if _, isNull := e.(*NullConst); isNull {
					continue
				}
				if !isConstExpr(e) || !restrictionKeyUsable(cat, tbl.Columns[cr.Index], e) {
					usable = false
					break
				}
			}
			if usable {
				c := &indexPathClause{indexCol: pos, saop: list, saopOp: ineqOp, local: conj}
				if ineqOp == parser.OpUnknown {
					// M0148-0001c3a4 (i): the column's other arrays and
					// bounds merge into the probe, as on the plain path.
					c.saopColQuals = saopColumnQuals(cat, tbl, colName, conj, conjuncts)
				}
				return c, conj
			}
			continue
		}
		if in, ok := orEqualitySAOP(cat, tbl, colName, conj); ok {
			return &indexPathClause{indexCol: pos, saop: in.List, local: conj}, in
		}
		// M0148-0001c3a4 (a): an OR of one inequality on the column, priced
		// as the OR itself (scalararraysel's useOr combination is the OR's).
		if in, op, ok := orInequalitySAOP(cat, tbl, colName, conj); ok {
			return &indexPathClause{indexCol: pos, saop: in.List, saopOp: op, local: conj}, conj
		}
	}
	return nil, nil
}

// bitmapOrHeapPaths is the restriction half of generate_bitmap_or_paths
// (create_index_paths passes rel->baserestrictinfo): one BitmapHeapScan over a
// BitmapOr per OR conjunct of the leaf whose every arm an index can serve.
func (s *searchCtx) bitmapOrHeapPaths(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node,
) []*Path {
	var out []*Path
	for _, orPath := range s.bitmapOrPathsFor(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, extractFilterConjuncts(leaf), nil) {
		out = append(out, s.bitmapHeapPathOver(rel, tbl, orPath, relTuples, T, totalPages, maxEntries, leaf))
	}
	return out
}

// bitmapOrPathsFor is generate_bitmap_or_paths(clauses, other_clauses): one
// BitmapOr path per OR clause of clauses whose every arm an index can serve,
// with all_clauses (clauses plus other_clauses) as each arm's context. The
// restriction call passes the leaf's conjuncts; an AND arm recurses with its
// own items (M0148-0001c3a4 (d), "Recurse in case there are sub-ORs").
func (s *searchCtx) bitmapOrPathsFor(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node, clauses, otherClauses []Expr,
) []*Path {
	all := make([]Expr, 0, len(clauses)+len(otherClauses))
	all = append(all, clauses...)
	all = append(all, otherClauses...)
	var out []*Path
	for _, conj := range clauses {
		if bin, ok := conj.(*BinaryOp); !ok || bin.Op != parser.OpOr || bin.Nested {
			continue
		}
		arms, ok := groupSimilarOrArgs(tbl, indexes, flattenExprOr(conj))
		if !ok {
			continue
		}
		// The other clauses, without this OR: as an index clause of an arm
		// it would only repeat the arms (PG removes the rinfo from
		// inner_other_clauses once it is grouped; ungrouped, an OR over
		// several columns matches no index column anyway).
		others := make([]Expr, 0, len(all)-1)
		for _, c := range all {
			if c != conj {
				others = append(others, c)
			}
		}
		var bitmapquals []*Path
		for _, arm := range arms {
			if b, ok := arm.(*BinaryOp); ok && b.Nested {
				qs := s.bitmapPathsForOrGroup(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, b, others)
				if qs == nil {
					bitmapquals = nil
					break
				}
				bitmapquals = append(bitmapquals, qs...)
				continue
			}
			q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, arm, others)
			if q == nil {
				// "If nothing matched this arm, we can't do anything with
				// this OR clause."
				bitmapquals = nil
				break
			}
			bitmapquals = append(bitmapquals, q)
		}
		if len(bitmapquals) < 2 {
			continue
		}
		orCost, orSel := costBitmapOrCost(s.cp, bitmapquals)
		orPath := &Path{
			Kind:              PathBitmapOr,
			Rel:               rel,
			Rows:              clampRowEst(orSel * relTuples),
			Cost:              orCost,
			BitmapSelectivity: orSel,
			Children:          bitmapquals,
			DisabledNodes:     disabledNodesFor(false, bitmapquals...),
			// create_bitmap_or_path (pathnode.c): `rel->consider_parallel`.
			ParallelSafe: rel.ParallelSafeForPath(),
		}
		out = append(out, orPath)
	}
	return out
}

// bitmapPathsForOrGroup is make_bitmap_paths_for_or_group (indxpath.c): the
// group matched as a whole (one ScalarArrayOp arm), or its arms one by one,
// whichever is cheaper. With no other restriction clause a whole-group match
// is taken outright, as PG takes it.
func (s *searchCtx) bitmapPathsForOrGroup(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node, group *BinaryOp, others []Expr,
) []*Path {
	var joint []*Path
	jointCost := 0.0
	if q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, group, others); q != nil {
		joint, jointCost = []*Path{q}, bitmapqualTotalCost(q)
	}
	if joint != nil && len(others) == 0 {
		return joint
	}
	var split []*Path
	splitCost := 0.0
	for _, arm := range flattenExprOr(group) {
		q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, arm, others)
		if q == nil {
			split = nil
			break
		}
		split = append(split, q)
		splitCost += bitmapqualTotalCost(q)
	}
	switch {
	case split == nil:
		return joint
	case joint == nil:
		return split
	case jointCost < splitCost:
		return joint
	}
	return split
}

// bitmapqualTotalCost is `bitmapqual->total_cost` as
// make_bitmap_paths_for_or_group reads it: an index leaf's IndexPath
// total_cost (cost_index's whole-scan cost), a BitmapAnd's
// cost_bitmap_and_node total.
func bitmapqualTotalCost(p *Path) float64 {
	if p.Kind == PathBitmapIndexScan {
		return p.IndexPathTotalCost
	}
	return p.Cost.Total
}

// bitmapOrArmPath is one arm of generate_bitmap_or_paths: build_paths_for_OR
// over the arm's clauses (its AND items, or the arm itself) with the
// relation's other restriction clauses as context, then choose_bitmap_and over
// the result. Returns nil when no index serves the arm.
//
// An index qualifies only when its path takes at least one index clause from
// the arm itself — build_paths_for_OR's `clauseset.nonempty` test; an index
// matched by the other clauses alone would scan the same rows for every arm.
// Every index clause must also be exact (recorded in `local`), since the
// arm's recheck qual is built from them (bitmapArmSubqual). PG's other
// build_paths_for_OR routes are deferred (design doc §Not covered): a sub-OR
// inside an AND arm (the recursive generate_bitmap_or_paths call) and a
// partial index whose predicate the arm proves with no index clause
// (useful_predicate).
func (s *searchCtx) bitmapOrArmPath(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node, arm Expr, conjuncts []Expr,
) *Path {
	armClauses := flattenExprAnd(arm)
	inArm := make(map[Expr]bool, len(armClauses))
	for _, c := range armClauses {
		// M0148-0001c3a4 (d): a sub-OR inside an AND arm stays one of the
		// arm's clauses — match_orclause_to_indexcol may take it as a
		// ScalarArrayOp (bitmapSAOPOnColumn) — and the recursive
		// generate_bitmap_or_paths call below offers its own BitmapOr.
		inArm[c] = true
	}
	// all_clauses: the arm's clauses first, so an arm clause wins its index
	// column over a restriction clause on the same column. conjuncts holds
	// the relation's other restriction clauses (not the OR itself).
	ctx := make([]Expr, 0, len(armClauses)+len(conjuncts))
	ctx = append(ctx, armClauses...)
	ctx = append(ctx, conjuncts...)
	var indlist []*Path
	for _, idx := range indexes {
		if !idx.HasStorage() {
			continue
		}
		child := s.buildBitmapIndexChild(rel, tbl, idx, relPages, relTuples, totalPages, leaf, ctx)
		if child == nil {
			continue
		}
		usesArm, exact := false, true
		for _, c := range child.IndexClauses {
			if c.ri != nil || c.local == nil {
				exact = false
				break
			}
			if inArm[c.local] {
				usesArm = true
			}
		}
		if !usesArm || !exact {
			continue
		}
		_, child.Rows = computeBitmapPages(child.Rows, relTuples, T,
			indexPagesForPath(s.cat, child, tbl, relTuples), totalPages, s.cp.effectiveCacheSize, maxEntries)
		indlist = append(indlist, child)
	}
	// generate_bitmap_or_paths' AND-arm recursion: the sub-ORs among the
	// arm's items, each a BitmapOr candidate for choose_bitmap_and.
	if len(armClauses) > 1 {
		indlist = append(indlist, s.bitmapOrPathsFor(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, armClauses, conjuncts)...)
	}
	if len(indlist) == 0 {
		return nil
	}
	// choose_bitmap_and over the arm's paths: an AND of several when that is
	// cheaper, else the single cheapest.
	heaps := make([]*Path, len(indlist))
	for i, c := range indlist {
		heaps[i] = &Path{Kind: PathBitmapHeapScan, Rel: rel, Children: []*Path{c}}
	}
	if and := s.chooseBitmapAnd(heaps, rel, tbl, relTuples, T, totalPages, maxEntries); and != nil {
		return and.Children[0]
	}
	var best *Path
	bestCost := 0.0
	for _, h := range heaps {
		c := bitmapScanCostEst(s.cp, h, relTuples, T, indexPagesForPath(s.cat, h, tbl, relTuples), totalPages, maxEntries, relQualOpCount(rel)).Total
		if best == nil || c < bestCost {
			best, bestCost = h.Children[0], c
		}
	}
	return best
}

// bitmapArmSubqual is create_bitmap_subplan's `*qual` for one bitmap tree
// node: a Bitmap Index Scan's index clauses plus its partial-index predicate
// when those clauses do not already imply it; a BitmapAnd's children's quals
// concatenated without duplicates; a BitmapOr's arms ORed into one clause.
// ok is false when a clause is not exact (no `local` conjunct), which the
// BitmapOr producer never admits.
func bitmapArmSubqual(p *Path) ([]Expr, bool) {
	switch p.Kind {
	case PathBitmapIndexScan:
		var qual []Expr
		for _, c := range p.IndexClauses {
			if c.ri != nil || c.local == nil {
				return nil, false
			}
			qual = append(qual, c.local)
		}
		if p.PartialPredicate != nil {
			implied := false
			for _, q := range qual {
				if predicateImpliedBy(q, p.PartialPredicate) {
					implied = true
					break
				}
			}
			if !implied {
				qual = append(qual, p.PartialPredicate)
			}
		}
		return qual, true
	case PathBitmapAnd:
		var qual []Expr
		for _, ch := range p.Children {
			sub, ok := bitmapArmSubqual(ch)
			if !ok {
				return nil, false
			}
			for _, e := range sub {
				dup := false
				for _, have := range qual {
					if exprEqual(have, e) {
						dup = true
						break
					}
				}
				if !dup {
					qual = append(qual, e)
				}
			}
		}
		return qual, true
	case PathBitmapOr:
		arms := make([]Expr, 0, len(p.Children))
		for _, ch := range p.Children {
			sub, ok := bitmapArmSubqual(ch)
			if !ok || len(sub) == 0 {
				// A qual-free arm makes the OR constant TRUE; the producer
				// never builds one.
				return nil, false
			}
			arms = append(arms, combineAnd(sub))
		}
		return []Expr{combineOr(arms)}, true
	}
	return nil, false
}

// bitmapOrRecheck is create_bitmap_scan_plan for a BitmapOr qual: the
// Recheck Cond (bitmapqualorig) is the ORed arm quals, and a leaf conjunct the
// recheck provably implies leaves the Filter (qpqual) — `thousand = 42`
// repeated in every arm, or the OR clause itself when each arm's qual covers
// one of its arms.
//
// It serves a BitmapAnd tree too, whose recheck list is its children's quals
// (one item each, as PG's bitmapqualorig list): the implication test reads
// the list as their AND, as predicate_implied_by reads indexquals.
func bitmapOrRecheck(treePath *Path, leaf Node) ([]Expr, map[Expr]bool) {
	qual, ok := bitmapArmSubqual(treePath)
	if !ok || len(qual) == 0 || (treePath.Kind == PathBitmapOr && len(qual) != 1) {
		panic("createPlan: bitmap tree path with an inexact qual")
	}
	all := combineAnd(qual)
	drop := map[Expr]bool{}
	for _, c := range extractFilterConjuncts(leaf) {
		if predicateImpliedBy(all, c) {
			drop[c] = true
		}
	}
	return qual, drop
}

// bitmapTreeExact reports whether every index clause in a bitmap tree is
// exact (bitmapArmSubqual can build its recheck qual).
func bitmapTreeExact(p *Path) bool {
	_, ok := bitmapArmSubqual(p)
	return ok
}

// predClass is predtest.c's PredClass.
type predClass int

const (
	predAtom predClass = iota
	predAnd
	predOr
)

// predicateClassify is predicate_classify (predtest.c) over goopg's binary
// AND / OR trees: a same-kind child is flattened into its parent's items,
// except a Nested clause, which is one item (a group_similar_or_args sub-OR
// is one argument of its OR in PG, and its ScalarArrayOp index qual an OR of
// its elements).
func predicateClassify(e Expr) (predClass, []Expr) {
	b, ok := e.(*BinaryOp)
	if !ok || (b.Op != parser.OpAnd && b.Op != parser.OpOr) {
		return predAtom, nil
	}
	var items []Expr
	var walk func(x Expr)
	walk = func(x Expr) {
		if xb, ok := x.(*BinaryOp); ok && xb.Op == b.Op && !xb.Nested {
			walk(xb.Left)
			walk(xb.Right)
			return
		}
		items = append(items, x)
	}
	walk(b.Left)
	walk(b.Right)
	if b.Op == parser.OpAnd {
		return predAnd, items
	}
	return predOr, items
}

// predicateImpliedBy is predicate_implied_by_recurse (predtest.c, strong
// form): does clause imply predicate? The AND / OR rules are PG's verbatim —
// notably OR => OR holds only when each clause arm implies ONE predicate
// arm, which is why PG keeps `(u1 = 1) OR (u2 = 3) OR (u2 = 7)` in the Filter
// under a recheck whose second arm is the group `(u2 = 3) OR (u2 = 7)`. The
// atom-vs-atom test is equal() plus goopg's narrow operator prover
// (provePartialIndexPredicate) for predicate_implied_by_simple_clause.
func predicateImpliedBy(clause, predicate Expr) bool {
	pclass, pitems := predicateClassify(predicate)
	cclass, citems := predicateClassify(clause)
	switch cclass {
	case predAnd:
		switch pclass {
		case predAnd:
			for _, p := range pitems {
				if !predicateImpliedBy(clause, p) {
					return false
				}
			}
			return true
		case predOr:
			for _, p := range pitems {
				if predicateImpliedBy(clause, p) {
					return true
				}
			}
			for _, c := range citems {
				if predicateImpliedBy(c, predicate) {
					return true
				}
			}
			return false
		default:
			for _, c := range citems {
				if predicateImpliedBy(c, predicate) {
					return true
				}
			}
			return false
		}
	case predOr:
		if pclass == predOr {
			for _, c := range citems {
				implied := false
				for _, p := range pitems {
					if predicateImpliedBy(c, p) {
						implied = true
						break
					}
				}
				if !implied {
					return false
				}
			}
			return true
		}
		for _, c := range citems {
			if !predicateImpliedBy(c, predicate) {
				return false
			}
		}
		return true
	default:
		switch pclass {
		case predAnd:
			for _, p := range pitems {
				if !predicateImpliedBy(clause, p) {
					return false
				}
			}
			return true
		case predOr:
			for _, p := range pitems {
				if predicateImpliedBy(clause, p) {
					return true
				}
			}
			return false
		default:
			return exprEqual(clause, predicate) || provePartialIndexPredicate(predicate, clause) ||
				operatorPredicateProof(clause, predicate)
		}
	}
}

// operatorPredicateProof is the btree-inference core of predtest.c's
// operator_predicate_proof for implication: clause `x op1 c1` implies
// predicate `x op2 c2` on the same column with integer constants when the
// strategy pair and the constants' comparison say so (BT_implic_table) —
// `x < 1` implies `x < 3`, `x >= 5` implies `x > 4`. It is what lets PG drop
// a restriction OR that a ScalarArrayOp recheck covers arm by arm
// (`(u < 1) OR (u < 3)` ⇒ `u < 3`, M0148-0001c3a4 (a)). The rules hold
// for any total order (no integer discreteness is assumed), and only
// integer literal constants are compared, so the proof is sound by
// construction.
func operatorPredicateProof(clause, predicate Expr) bool {
	var op1, op2 parser.OpCode
	var cmp int
	if cc, o1, c1, ok := intComparison(clause); ok {
		pc, o2, c2, ok := intComparison(predicate)
		if !ok || cc.Index != pc.Index || cc.Name != pc.Name {
			return false
		}
		op1, op2 = o1, o2
		switch {
		case c1 < c2:
			cmp = -1
		case c1 > c2:
			cmp = 1
		}
	} else if cc, o1, s1, ok := nameComparison(clause); ok {
		// M0148-0001c3a4 (e): a `name` column always compares under the "C"
		// collation (pg_type typcollation C_COLLATION_OID), so its constants
		// order bytewise — `stringu1 = 'ATAAAA'` implies `stringu1 < 'B'`.
		pc, o2, s2, ok := nameComparison(predicate)
		if !ok || cc.Index != pc.Index || cc.Name != pc.Name {
			return false
		}
		op1, op2 = o1, o2
		cmp = strings.Compare(s1, s2)
	} else {
		return false
	}
	return btreeImplication(op1, op2, cmp)
}

// btreeImplication is BT_implic_table for `x op1 c1` implies `x op2 c2`,
// given cmp = sign(c1 - c2) under the column's ordering.
func btreeImplication(op1, op2 parser.OpCode, cmp int) bool {
	switch op2 {
	case parser.OpLt:
		return (op1 == parser.OpLt && cmp <= 0) || ((op1 == parser.OpLe || op1 == parser.OpEq) && cmp < 0)
	case parser.OpLe:
		return (op1 == parser.OpLt || op1 == parser.OpLe || op1 == parser.OpEq) && cmp <= 0
	case parser.OpGt:
		return (op1 == parser.OpGt && cmp >= 0) || ((op1 == parser.OpGe || op1 == parser.OpEq) && cmp > 0)
	case parser.OpGe:
		return (op1 == parser.OpGt || op1 == parser.OpGe || op1 == parser.OpEq) && cmp >= 0
	case parser.OpEq:
		return op1 == parser.OpEq && cmp == 0
	}
	return false
}

// nameComparison reads `col op 'literal'` (either side, the literal bare or
// cast to name), normalised to column-first, for a `name` column.
func nameComparison(e Expr) (*ColumnRef, parser.OpCode, string, bool) {
	b, ok := e.(*BinaryOp)
	if !ok {
		return nil, parser.OpUnknown, "", false
	}
	switch b.Op {
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe, parser.OpEq:
	default:
		return nil, parser.OpUnknown, "", false
	}
	lit := func(x Expr) (string, bool) {
		if c, isCast := x.(*CastExpr); isCast {
			if !strings.EqualFold(strings.TrimSpace(c.TargetType), "name") {
				return "", false
			}
			x = c.Operand
		}
		sc, isStr := x.(*StringConst)
		if !isStr {
			return "", false
		}
		return sc.Value, true
	}
	nameCol := func(cr *ColumnRef) bool { return cr.Type.Name == "name" && !cr.Type.IsArray }
	if cr, isCol := b.Left.(*ColumnRef); isCol && nameCol(cr) {
		if v, ok := lit(b.Right); ok {
			return cr, b.Op, v, true
		}
	}
	if cr, isCol := b.Right.(*ColumnRef); isCol && nameCol(cr) {
		if v, ok := lit(b.Left); ok {
			return cr, swapInequalityOp(b.Op), v, true
		}
	}
	return nil, parser.OpUnknown, "", false
}

// intComparison reads `col op intconst` (either side), normalised to
// column-first, for an integer-typed column — whose order is the
// constants' numeric order (a text column compares `10` before `9`).
func intComparison(e Expr) (*ColumnRef, parser.OpCode, int64, bool) {
	b, ok := e.(*BinaryOp)
	if !ok {
		return nil, parser.OpUnknown, 0, false
	}
	switch b.Op {
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe, parser.OpEq:
	default:
		return nil, parser.OpUnknown, 0, false
	}
	intCol := func(cr *ColumnRef) bool {
		switch cr.Type.Name {
		case "int2", "int4", "int8", "smallint", "integer", "bigint", "int":
			return !cr.Type.IsArray
		}
		return false
	}
	if cr, isCol := b.Left.(*ColumnRef); isCol && intCol(cr) {
		if ic, isInt := b.Right.(*IntegerConst); isInt {
			return cr, b.Op, ic.Value, true
		}
	}
	if cr, isCol := b.Right.(*ColumnRef); isCol && intCol(cr) {
		if ic, isInt := b.Left.(*IntegerConst); isInt {
			return cr, swapInequalityOp(b.Op), ic.Value, true
		}
	}
	return nil, parser.OpUnknown, 0, false
}
