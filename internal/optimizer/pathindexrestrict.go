package optimizer

// M0145-0029 slice 1 — UNPARAMETERISED restriction index paths: the arm of
// `build_index_paths` (postgres/src/backend/optimizer/path/indxpath.c:975)
// whose `index_clauses` come from the relation's OWN restriction clauses
// (`baserestrictinfo`, matched by `match_restriction_clauses_to_index`,
// indxpath.c:2240) rather than from join clauses.
//
// Before this file the search had no such path. Its base relations got index
// access only through bitmap paths (`pathbitmap.go`, equality only), ordering-
// only full index scans (`pathindexordered.go`) and index-only scans; a plain
// `Index Scan` driven by `WHERE col = const` existed only on the rule-based
// bypass (`planIndexScanFromWhere`, `rewriteScanInputsWithSingleTablePredicates`)
// that single-relation scopes took on the legacy pipeline. The jointree
// pipeline plans those scopes through the search, so the flip would have lost
// the path outright (the M0145-0008 flip triage, group I) — the same
// route-borne bypass loss as Q17/Q20.
//
// Slice 1 covers the EQUALITY prefix: `col = const` conjuncts bound to a
// gapless leading prefix of the index's columns (PG's btree `amoptionalkey`
// rule, the same prefix `pickIndexCoveringLeadingPrefix` binds for the
// parameterised arm). Slice 2 adds RANGE bounds (`<`, `<=`, `>`, `>=`) on the
// index's LEADING column when no equality binds it — the one range shape the
// executor's probe expresses (LowKey/HighKey carry no equality prefix).
// Slice 4 adds a ScalarArrayOp qual (`col IN (…)` / `col = ANY (…)`) on the
// LEADING column, again only when no equality binds it (IndexScan.SAOPKeys is
// exclusive of every other probe shape). An equality prefix followed by a
// range or SAOP column is not built yet — ledgered.

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// addRestrictionIndexPaths generates, for every base relation, one plain index
// path per index whose leading columns are bound by the relation's local
// equality restrictions.
func (s *searchCtx) addRestrictionIndexPaths(cat catalog.Catalog) {
	if s == nil || cat == nil {
		return
	}
	totalPages := s.totalTablePages()
	for i, rel := range s.levelRels(1) {
		if i >= len(s.relInfos) {
			break
		}
		tbl := s.relInfos[i].table
		if tbl == nil {
			continue
		}
		// Same consumer-side eligibility gate as the other base-rel index
		// producers: no path over a leaf createPlan cannot rebuild.
		if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
			continue
		}
		conjuncts := extractFilterConjuncts(rel.baseLeaf)
		if len(conjuncts) == 0 {
			continue
		}
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		var colExprs map[string]Expr
		if s.hasUsefulPathkeys(rel) {
			colExprs = mergeableColumnExprsFor(rel.Relids, s.clausesAll())
			addQueryPathkeyColumnExprs(colExprs, s.queryPathkeys, s.relInfos[i].sourceIdx)
		}
		added := false
		for _, idx := range cat.IndexesOnTable(tbl) {
			// A catalog-only index (gist/spgist/gin/brin) has nothing to scan (M0146-0069).
			if !idx.HasStorage() {
				continue
			}
			if s.addOneRestrictionIndexPath(cat, rel, tbl, idx, conjuncts, colExprs, relPages, relTuples, totalPages) {
				added = true
			}
		}
		if added {
			setCheapest(rel)
		}
	}
}

// restrictionEqualityPrefix binds `idx`'s gapless leading prefix to the local
// `col = const` conjuncts, returning the index clauses in index-column order.
// A column with no usable equality ends the prefix; nothing past it is an
// index qual (PG's btree `amoptionalkey` handling in `build_index_paths`).
func restrictionEqualityPrefix(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) []indexPathClause {
	var clauses []indexPathClause
	for pos := range idx.Columns {
		found := restrictionEqualityOn(cat, tbl, idx, pos, conjuncts)
		if found == nil {
			break
		}
		clauses = append(clauses, *found)
	}
	return clauses
}

// restrictionEqualityOn is the first local `col = const` conjunct binding
// index column pos, as an index clause, or nil.
func restrictionEqualityOn(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) *indexPathClause {
	colName := idx.Columns[pos]
	for _, conj := range conjuncts {
		bin, ok := conj.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			continue
		}
		cr, val, _, ok := normalizeColumnIndexKey(bin.Left, bin.Right)
		if !ok {
			continue
		}
		// For a rebuildable base leaf the output positions are the table's
		// column positions — the same assumption the bitmap arm's
		// matchBitmapIndexQuals relies on.
		if cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != colName {
			continue
		}
		if !restrictionKeyUsable(cat, tbl.Columns[cr.Index], val) {
			continue
		}
		return &indexPathClause{indexCol: pos, key: val, local: conj}
	}
	return nil
}

// restrictionSkipRun is the unparameterised twin of pickIndexSkipRun
// (M0146-0005dg): PG 18 binds `col = const` on a NON-leading btree column
// as an index qual and fills the unbound leading columns with skip arrays
// (nbtpreprocesskeys.c `_bt_skiparray`), so `inv_item_sk = 100` on
// `(inv_date_sk, inv_item_sk, inv_warehouse_sk)` drives an Index Scan
// instead of a Seq Scan. It returns the contiguous equality run starting at
// the first bound column `skip` (>= 1), or nil when the leading column is
// itself bound (the prefix arm's domain) or the shape is one the executor's
// enumeration does not take: an expression or DESC column in the skipped
// prefix, or an unbound key column that may hold NULLs (the byte-key btree
// keeps no NULL-keyed entry — indexSkipNullSafe).
func restrictionSkipRun(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) ([]indexPathClause, int) {
	if !isBTreeIndex(idx) || len(idx.Columns) < 2 {
		return nil, 0
	}
	if restrictionEqualityOn(cat, tbl, idx, 0, conjuncts) != nil {
		return nil, 0
	}
	skip := -1
	var clauses []indexPathClause
	for pos := 1; pos < len(idx.Columns); pos++ {
		c := restrictionEqualityOn(cat, tbl, idx, pos, conjuncts)
		if c == nil {
			if skip > 0 {
				break
			}
			continue
		}
		if skip < 0 {
			skip = pos
		}
		clauses = append(clauses, *c)
	}
	if skip < 1 {
		return nil, 0
	}
	for i := 0; i < skip; i++ {
		if idx.Columns[i] == "" || (i < len(idx.ColDescending) && idx.ColDescending[i]) {
			return nil, 0
		}
	}
	if !indexSkipNullSafe(tbl, idx, skip, len(clauses)) {
		return nil, 0
	}
	return clauses, skip
}

// restrictionLeadingRange binds the index's LEADING column to the first local
// lower bound (`col > c` / `col >= c`, either operand order) and the first
// upper bound (`col < c` / `col <= c`) on it, returning up to two clauses with
// `op` in canonical `col op key` form. Only the leading column: the probe's
// LowKey/HighKey carry no equality prefix, so a range on a later column is
// not an index qual here (PG's `build_index_paths` would bind it behind an
// equality prefix — ledgered).
//
// The consumed conjunct is recorded in `local` — so the lowering drops it
// from the reinstated Filter — only for a SINGLE-column index. On a composite
// index an exclusive lower bound's padded key can admit trailing-column
// entries that share the bound value (the guard `tryRangeIndexScan` keeps),
// so there the conjunct stays in the Filter as a recheck; the clause is still
// an index qual for costing.
func restrictionLeadingRange(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) []indexPathClause {
	return restrictionRangeOnColumn(cat, tbl, idx, 0, conjuncts)
}

// restrictionRangeOnColumn is restrictionLeadingRange for index column `pos`:
// pos 0 is the leading-column range; pos > 0 follows a leading ScalarArrayOp,
// whose own per-element bounds (indexScanOp's SAOP descent) neither stop at
// the column's NULL group nor swap for a DESC column, so those bounds record
// no `local` and the conjunct stays in the Filter as a recheck. Bounds behind
// an EQUALITY prefix are restrictionRangeBehindPrefix's.
func restrictionRangeOnColumn(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) []indexPathClause {
	return restrictionRangeOnColumnLocal(cat, tbl, idx, pos, conjuncts, pos == 0)
}

// restrictionRangeBehindPrefix is restrictionRangeOnColumn for the column
// after an equality prefix (IndexScan.RangePrefix, BitmapIndexScan Keys plus
// bounds). Those probes bound through indexRangeBounds, which swaps a DESC
// column and — with one open end — stops at the column's NULL group within
// the prefix (nullStopRangeBound, M0148-0001c3a), and a cluster without
// NULL-keyed entries stores none to run into. So the bounds are exact and
// record `local`: PG prints `Index Cond: ((a = 3) AND (b < 200))` and no
// Filter (M0148-0001c3a2). This rule used to keep them a Filter recheck,
// written before the prefix-aware NULL stop existed.
func restrictionRangeBehindPrefix(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) []indexPathClause {
	return restrictionRangeOnColumnLocal(cat, tbl, idx, pos, conjuncts, true)
}

func restrictionRangeOnColumnLocal(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr, withLocal bool) []indexPathClause {
	if pos < 0 || pos >= len(idx.Columns) {
		return nil
	}
	lead := idx.Columns[pos]
	var lo, hi *indexPathClause
	for _, conj := range conjuncts {
		bin, ok := conj.(*BinaryOp)
		if !ok {
			continue
		}
		switch bin.Op {
		case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		default:
			continue
		}
		cr, val, colOnRight, ok := normalizeColumnIndexKey(bin.Left, bin.Right)
		if !ok {
			continue
		}
		if cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != lead {
			continue
		}
		if !restrictionKeyUsable(cat, tbl.Columns[cr.Index], val) {
			continue
		}
		op := bin.Op
		if colOnRight {
			op = swapInequalityOp(op)
		}
		c := &indexPathClause{indexCol: pos, key: val, op: op}
		if withLocal {
			c.local = conj
		}
		switch op {
		case parser.OpGt, parser.OpGe:
			if lo == nil {
				lo = c
			}
		default:
			if hi == nil {
				hi = c
			}
		}
	}
	var out []indexPathClause
	if lo != nil {
		out = append(out, *lo)
	}
	if hi != nil {
		out = append(out, *hi)
	}
	// M0148-0001c3a2: one column's clauses in restriction-list order, as
	// PG's match_clauses_to_index files them (`((a < 50) AND (a > 10))`).
	if lo != nil && hi != nil && conjunctPrecedes(clauseSource(*hi, conjuncts), clauseSource(*lo, conjuncts), conjuncts) {
		out[0], out[1] = out[1], out[0]
	}
	return out
}

// clauseSource is an index clause's restriction conjunct: its `local`, or —
// for a bound that records none — the conjunct holding its key.
func clauseSource(c indexPathClause, conjuncts []Expr) Expr {
	if c.local != nil {
		return c.local
	}
	for _, conj := range conjuncts {
		if bin, ok := conj.(*BinaryOp); ok && c.key != nil && (bin.Left == c.key || bin.Right == c.key) {
			return conj
		}
	}
	return nil
}

// conjunctPrecedes reports whether restriction conjunct a comes before b in
// the relation's restriction list as written — baserestrictinfo order, which
// PG's index clauses keep. goopg's leaf Filter is cost-sorted
// (order_qual_clauses, local_filters.go) before the producers see it, so the
// written order is read off the conjuncts' source positions, which the leaf
// localization keeps; equal or unknown positions fall back to list order.
func conjunctPrecedes(a, b Expr, conjuncts []Expr) bool {
	if a == nil || b == nil {
		return false
	}
	if pa, pb := a.Pos(), b.Pos(); pa > 0 && pb > 0 && pa != pb {
		return pa < pb
	}
	ia, ib := len(conjuncts), len(conjuncts)
	for i, c := range conjuncts {
		if c == a {
			ia = i
		}
		if c == b {
			ib = i
		}
	}
	return ia < ib
}

// restrictionLeadingSAOP binds the index's LEADING column to the first local
// ScalarArrayOp conjunct usable as a btree multi-descent — the gates of
// `match_saopclause_to_indexcol` (indxpath.c:3136) that trySAOPIndexScan
// reproduces: a plain `IN (list)` or `= ANY (list)` (OR of equalities; NOT
// IN, `!= ANY`, ALL and other operators are no union of descents), a bare
// column operand, and constant elements the key encoder takes
// (restrictionKeyUsable: no boolean, no enum string). The conjunct is exactly
// the union of the descents (the executor dedupes by TID), so it is recorded
// in `local` and dropped from the Filter.
func restrictionLeadingSAOP(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) []indexPathClause {
	return restrictionSAOPOnColumn(cat, tbl, idx, 0, conjuncts)
}

// restrictionSAOPOnColumn is restrictionLeadingSAOP on index column pos: the
// array of a probe behind an equality prefix on the columns before it
// (M0148-0001c3a4 (b), PG `((thousand = 42) AND (tenthous = ANY (…)))`).
func restrictionSAOPOnColumn(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) []indexPathClause {
	if pos < 0 || pos >= len(idx.Columns) {
		return nil
	}
	lead := idx.Columns[pos]
	for _, conj := range conjuncts {
		in, ok := conj.(*InExpr)
		if !ok || in.Subquery != nil || in.Plan != nil || len(in.List) == 0 {
			continue
		}
		ineqOp, ineq := saopAnyInequality(in)
		if in.Negated || in.NotEqualAny || in.AllOp || (in.AnyOp != parser.OpUnknown && in.AnyOp != parser.OpEq && !ineq) {
			continue
		}
		cr, ok := in.Operand.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != lead {
			continue
		}
		list, listOK := saopElementsFor(tbl.Columns[cr.Index], in.List)
		if !listOK || len(list) == 0 {
			continue
		}
		usable := true
		for _, e := range list {
			// A NULL element matches nothing under `=` and the executor's
			// multi-descent probe skips it (ScalarArrayOp semantics), so it
			// does not stop the list from being an index qual — PG keeps
			// `col = ANY ('{7,NULL}')` as the Index Cond (M0146-0060).
			if _, isNull := e.(*NullConst); isNull {
				continue
			}
			if !restrictionKeyUsable(cat, tbl.Columns[cr.Index], e) {
				usable = false
				break
			}
		}
		if !usable {
			continue
		}
		c := indexPathClause{indexCol: pos, saop: list, saopOp: ineqOp, local: conj}
		if ineqOp == parser.OpUnknown {
			c.saopColQuals = saopColumnQuals(cat, tbl, lead, conj, conjuncts)
		}
		return []indexPathClause{c}
	}
	// M0148-0001c3a2: an OR of equalities on the leading column is the same
	// ScalarArrayOp probe (match_orclause_to_indexcol); the OR is its exact
	// recheck and leaves the Filter like the IN does.
	for _, conj := range conjuncts {
		if idx.DeclaredHash {
			// A hash index takes no ScalarArrayOp quals (amsearcharray).
			break
		}
		if in, ok := orEqualitySAOP(cat, tbl, lead, conj); ok {
			return []indexPathClause{{indexCol: pos, saop: in.List, local: conj}}
		}
		// M0148-0001c3a4 (a): an OR of one inequality operator.
		if in, op, ok := orInequalitySAOP(cat, tbl, lead, conj); ok {
			return []indexPathClause{{indexCol: pos, saop: in.List, saopOp: op, local: conj}}
		}
	}
	return nil
}

// rangeIndexSelectivity is the range clauses' selectivity: clauselist_
// selectivity over the index quals, which pairs a lower and an upper bound on
// the same column into one band (`hibound + lobound - 1`, clausesel.c) —
// goopg's conjunctionSelectivity. The clauses' source conjuncts are found by
// operator and key identity, since a composite index's clauses carry no
// `local`.
func rangeIndexSelectivity(leaf Node, conjuncts []Expr, clauses []indexPathClause) float64 {
	var quals []Expr
	for _, c := range clauses {
		if c.nullTest != IndexNullTestNone {
			// The NullTest conjunct itself (nulltestsel through
			// clauseSelectivity), M0148-0001c3a.
			if c.local != nil {
				quals = append(quals, c.local)
			}
			continue
		}
		for _, conj := range conjuncts {
			bin, ok := conj.(*BinaryOp)
			if ok && (bin.Left == c.key || bin.Right == c.key) {
				quals = append(quals, conj)
				break
			}
		}
	}
	if len(quals) == 0 {
		return 1
	}
	return clampSelectivity(conjunctionSelectivity(quals, leaf))
}

// restrictionLeadingNullTest is match_clause_to_indexcol's NullTest arm
// (indxpath.c) for the leading index column: a `col IS [NOT] NULL` conjunct
// on a plain key column becomes an index clause, since btree has
// amsearchnulls. `IS NULL` needs an index that stores NULL-keyed entries
// (catalog.IndexHasNullKeyedEntries) — in an older cluster the rows it asks
// for are not in the index at all, so it stays a Filter there. `IS NOT NULL`
// is always answerable: an index without NULL-keyed entries holds only
// non-NULL keys. The first matching conjunct wins (PG would take a duplicate
// too; it is redundant). M0148-0001c3a.
func restrictionLeadingNullTest(idx *catalog.Index, conjuncts []Expr) *indexPathClause {
	return restrictionNullTestOnColumn(idx, 0, conjuncts)
}

// restrictionNullTestOnColumn is restrictionLeadingNullTest for index column
// pos, which follows an equality prefix when pos > 0 (slice 3). Unlike a
// range bound there, the NullTest is exact behind the prefix — IS NULL is
// the [prefix, NULL] group and IS NOT NULL stops at it — so its conjunct
// leaves the Filter like the leading one.
func restrictionNullTestOnColumn(idx *catalog.Index, pos int, conjuncts []Expr) *indexPathClause {
	if idx == nil || pos < 0 || pos >= len(idx.Columns) || idx.Columns[pos] == "" || idx.DeclaredHash {
		return nil
	}
	for _, conj := range conjuncts {
		nt, ok := conj.(*IsNullExpr)
		if !ok {
			continue
		}
		cr, ok := nt.Operand.(*ColumnRef)
		if !ok || cr.Name != idx.Columns[pos] {
			continue
		}
		kind := IndexNullTestIsNull
		if nt.Negated {
			kind = IndexNullTestIsNotNull
		} else if !catalog.IndexHasNullKeyedEntries(idx) {
			continue
		}
		return &indexPathClause{indexCol: pos, local: conj, nullTest: kind}
	}
	return nil
}

// restrictionNullTestsOnColumn is every NullTest index clause on index column
// pos, in restriction-list order (restrictionNullTestOnColumn's first is the
// head): PG takes each as an index clause, duplicates and contradictions
// included, and _bt_preprocess_keys merges them at scan time
// (M0148-0001c3a2).
func restrictionNullTestsOnColumn(idx *catalog.Index, pos int, conjuncts []Expr) []indexPathClause {
	var out []indexPathClause
	for len(conjuncts) > 0 {
		nt := restrictionNullTestOnColumn(idx, pos, conjuncts)
		if nt == nil {
			break
		}
		out = append(out, *nt)
		for i, c := range conjuncts {
			if c == nt.local {
				conjuncts = conjuncts[i+1:]
				break
			}
		}
	}
	return out
}

// withNullTestsInConjunctOrder is withNullTestInConjunctOrder for each of
// the column's NullTest clauses in turn.
func withNullTestsInConjunctOrder(rng []indexPathClause, nts []indexPathClause, conjuncts []Expr) []indexPathClause {
	for _, nt := range nts {
		rng = withNullTestInConjunctOrder(rng, nt, conjuncts)
	}
	return rng
}

// withNullTestInConjunctOrder places the NullTest clause among the leading
// column's range clauses in restriction-list order: PG lists one index
// column's clauses in baserestrictinfo order, which is what Index Cond prints
// (`((x IS NOT NULL) AND (x < 5))`).
func withNullTestInConjunctOrder(rng []indexPathClause, nt indexPathClause, conjuncts []Expr) []indexPathClause {
	out := make([]indexPathClause, 0, len(rng)+1)
	placed := false
	for _, c := range rng {
		if src := clauseSource(c, conjuncts); !placed && src != nil && conjunctPrecedes(nt.local, src, conjuncts) {
			out = append(out, nt)
			placed = true
		}
		out = append(out, c)
	}
	if !placed {
		out = append(out, nt)
	}
	return out
}

// restrictionKeyUsable admits the probe-key kinds the executor's btree key
// encoder takes directly — the set the rule-based producer
// (planIndexScanFromWhereShape) accepted. A boolean key and a string against a
// user enum column (which needed the rule path's enum cast) decline; the
// clause stays a filter and the path is simply not built from it.
//
// Built on isConstExpr (the literal kinds normalizeColumnConst already
// admitted) rather than a second Expr type switch, so the literal-kind list
// lives in one place.
func restrictionKeyUsable(cat catalog.Catalog, col catalog.Column, val Expr) bool {
	// A client bind parameter ($n, PG's PARAM_EXTERN) is a pseudo-constant
	// for the whole statement — match_clause_to_indexcol takes any operand
	// with no Var of the indexed relation and no volatile function. The
	// executor encodes the bound value against the column type at run time,
	// as it always did for the rule-based producer's $n keys (M0146-0060).
	if _, isParam := val.(*ParamRef); isParam {
		return true
	}
	// A constant cast — `'reg_idx'` resolved as `'reg_idx'::regclass` — is a
	// Const after PG's eval_const_expressions, so it is an index qual there.
	// The executor evaluates the key expression once per scan; the cast must
	// already produce the column's own type, since the probe encodes the
	// key's Datum against the column with no further coercion (M0146-0060).
	if c, isCast := val.(*CastExpr); isCast {
		return isConstantPlanExpr(c) && castTargetIsColumnType(c, col)
	}
	if t, isOuter := outerParamKeyType(val); isOuter {
		// The probe encodes the key's runtime Datum against the column's
		// btree byte key with no cast in between, so an outer key must carry
		// the column's own type; a cross-type comparison stays a filter.
		return t.Name == col.Type.Name && t.IsArray == col.Type.IsArray
	}
	if !isConstExpr(val) {
		return false
	}
	if _, isBool := val.(*BooleanConst); isBool {
		return false
	}
	// M0146-0061: a numeric literal against an integer column is
	// `(col)::numeric op const` in PG — btree integer_ops has no numeric
	// member, so the clause is not indexable and stays a filter. Encoding
	// the literal into the int key rounded it: `a > 198.5` probed `a > 199`
	// and `a = 198.5` matched a = 199.
	if _, isNum := val.(*NumericConst); isNum && isIntegerLikeType(col.Type.Name) {
		return false
	}
	if _, isStr := val.(*StringConst); isStr {
		if im := inMemoryCat(cat); im != nil {
			if _, isEnum := im.LookupEnum(col.Type.Name); isEnum {
				return false
			}
		}
	}
	return true
}

// partialIndexUsable reports whether idx may be scanned for a relation whose
// restriction is conjuncts: always for a plain index, and for a partial index
// only when a conjunct proves its predicate.
func partialIndexUsable(tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) bool {
	if !idx.HasPredicate {
		return true
	}
	resolved, err := ResolveIndexPredicateOf(idx, tbl)
	if err != nil || resolved == nil {
		return false
	}
	return partialPredicateProvenBy(resolved, conjuncts)
}

// castTargetIsColumnType reports whether a cast produces the column's type —
// compared by pg_type OID where both names resolve, else by name — with the
// array flag matching.
func castTargetIsColumnType(c *CastExpr, col catalog.Column) bool {
	target := strings.ToLower(strings.TrimSpace(c.TargetType))
	if strings.HasSuffix(target, "[]") != col.Type.IsArray {
		return false
	}
	target = strings.TrimSuffix(target, "[]")
	colName := strings.ToLower(col.Type.Name)
	if a, b := catalog.TypeNameToOID(target), catalog.TypeNameToOID(colName); a != 0 && b != 0 {
		return a == b
	}
	return target == colName
}

// restrictionIndexSelectivity is the index quals' selectivity alone
// (btcostestimate's clauselist_selectivity over indexQuals), with PG's
// `isunique` short circuit for a unique index bound on every key column.
// Shared by the plain (addOneRestrictionIndexPath) and index-only
// (addOneIndexOnlyPath) producers so the two price the same probe alike.
func restrictionIndexSelectivity(tbl *catalog.Table, idx *catalog.Index, clauses []indexPathClause, relTuples float64) (float64, bool) {
	rawRows := 0.0
	if tbl.Stats != nil {
		rawRows = float64(tableRows(tbl))
	}
	sel := 1.0
	for _, c := range clauses {
		stats := columnStatsByName(tbl, idx.Columns[c.indexCol])
		sel *= indexKeyEqSelectivity(stats, c.key, rawRows, columnTypeByName(tbl, idx.Columns[c.indexCol]))
	}
	unique := idx.Unique && len(clauses) == len(idx.Columns)
	if unique && relTuples > 0 {
		sel = 1.0 / relTuples
	}
	return sel, unique
}

// addOneRestrictionIndexPath builds the equality-prefix path for one index, or
// declines. Returns whether a path was added.
func (s *searchCtx) addOneRestrictionIndexPath(cat catalog.Catalog, rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	conjuncts []Expr, colExprs map[string]Expr, relPages int64, relTuples, totalPages float64) bool {
	if idx == nil || len(idx.Columns) == 0 {
		return false
	}
	// A partial index is scanned only when the restriction clauses prove its
	// predicate (PG's `predOK`, check_index_predicates in indxpath.c) — the
	// bitmap producer's partialPredicateProvenBy. An unproven one would
	// silently drop the rows it does not index (M0146-0060).
	if !partialIndexUsable(tbl, idx, conjuncts) {
		return false
	}
	clauses := restrictionEqualityPrefix(cat, tbl, idx, conjuncts)
	// A `USING hash` index (goopg builds it on a btree, DeclaredHash) answers
	// equality only: PG's hash AM has no ordering, so no range or skip qual,
	// and amsearcharray is false, so no ScalarArrayOp qual (hashhandler,
	// hash.c) — only the `col = const` path below (M0146-0060).
	if idx.DeclaredHash && len(clauses) != len(idx.Columns) {
		return false
	}
	isRange := false
	// Slice 2b: an equality prefix that stops short of the last index column
	// may be followed by range bounds on the NEXT column (PG binds `b > 5`
	// behind `a = 1` on `(a, b)`). The eq clauses become IndexScan.RangePrefix.
	prefixRange := false
	if n := len(clauses); n > 0 && n < len(idx.Columns) && !prefixEqualitySAOP(cat, tbl, idx, n, conjuncts) {
		rng := restrictionRangeBehindPrefix(cat, tbl, idx, n, conjuncts)
		// M0148-0001c3a slice 3: a NullTest on the column after the prefix
		// (`a = 1 AND b IS NULL`), bounded at the prefix's NULL group.
		if nts := restrictionNullTestsOnColumn(idx, n, conjuncts); len(nts) > 0 {
			rng = withNullTestsInConjunctOrder(rng, nts, conjuncts)
		}
		if len(rng) > 0 {
			clauses = append(clauses, rng...)
			prefixRange = true
		}
	}
	isSAOP := false
	saopAt := 0
	// M0148-0001c3a4 (b): no bound behind the equality prefix, but an array
	// on the next column — build_index_paths takes the column's
	// ScalarArrayOp clause behind the prefix's equalities, and the array may
	// itself be followed by bounds on the column after it.
	if n := len(clauses); n > 0 && n < len(idx.Columns) && !prefixRange && !idx.DeclaredHash {
		if sa := restrictionSAOPOnColumn(cat, tbl, idx, n, conjuncts); len(sa) > 0 {
			clauses = append(clauses, sa...)
			isSAOP, saopAt = true, n
			// An inequality array is a range on its column; the next
			// column's quals stay in the Filter (M0148-0001c3a4 (a)).
			if n+1 < len(idx.Columns) && sa[0].saopOp == parser.OpUnknown {
				clauses = append(clauses, restrictionRangeBehindPrefix(cat, tbl, idx, n+1, conjuncts)...)
			}
		}
	}
	if len(clauses) == 0 {
		clauses = restrictionLeadingSAOP(cat, tbl, idx, conjuncts)
		isSAOP = len(clauses) > 0
		// A SAOP on the leading column may be followed by bounds on the
		// second (PG `Index Cond: ((a = ANY (...)) AND (b > 1))`).
		// M0148-0001c3a4 (c): exact index quals, as behind an equality
		// prefix — the executor bounds each element's descent with
		// indexRangeBounds (NULL stop, DESC swap), so no Filter recheck.
		if isSAOP && len(idx.Columns) > 1 && clauses[0].saopOp == parser.OpUnknown {
			clauses = append(clauses, restrictionRangeBehindPrefix(cat, tbl, idx, 1, conjuncts)...)
		}
	}
	if len(clauses) == 0 {
		clauses = restrictionLeadingRange(cat, tbl, idx, conjuncts)
		// M0148-0001c3a: a NullTest on the leading column is an index
		// clause too (match_clause_to_indexcol's NullTest arm), on its own
		// or beside the range bounds.
		if nts := restrictionNullTestsOnColumn(idx, 0, conjuncts); len(nts) > 0 {
			clauses = withNullTestsInConjunctOrder(clauses, nts, conjuncts)
		}
		isRange = true
		// M0148-0001c3a4 (i): an `= ANY` array on the second column behind
		// the range is an index qual too, checked per entry
		// (IndexScan.TrailingSAOP).
		if len(clauses) > 0 {
			clauses = append(clauses, trailingSAOPOnRange(cat, tbl, idx, conjuncts)...)
		}
	}
	// M0146-0005dg: nothing binds the leading column — an equality run on a
	// later column still drives a btree skip scan.
	skip := 0
	if len(clauses) == 0 {
		clauses, skip = restrictionSkipRun(cat, tbl, idx, conjuncts)
		isRange = false
	}
	if len(clauses) == 0 {
		return false
	}
	// build_index_paths builds ONE path per index and makes it index-only
	// when check_index_only holds (indxpath.c:1010, `index_only_scan`). When
	// addIndexOnlyPaths will build that index-only path with these same
	// clauses, a plain twin here would tie it on cost and — filed first —
	// win add_path's tie, electing the heap-fetching shape PG never builds.
	if s.restrictionPathIsIndexOnly(cat, rel, tbl, idx, conjuncts) {
		return false
	}
	// The byte-key btree stores NO entry whose key has a NULL column
	// (collectBTreeEntries: "not storable in the byte-key btree"), so a probe
	// that leaves a nullable key column unbound would silently miss every
	// matching row with a NULL there (measured: `a IN (7, 8) AND b > 90` on
	// (a, b, c) lost the (7, 92, NULL) row). Bound columns are safe — a NULL
	// there fails the qual anyway. PG stores NULL keys and has no such rule.
	if skip == 0 && !indexUnboundKeysNotNull(tbl, idx, boundIndexColumns(clauses)) {
		return false
	}
	var sel float64
	var unique bool
	numSAScans := 0.0
	boundSel := 0.0
	trailingAt, trailing := clausesTrailingSAOP(clauses)
	switch {
	case trailing:
		sel, boundSel, numSAScans = trailingSAOPSelectivity(cat, rel, tbl, idx, clauses, trailingAt, relTuples, conjuncts)
	case isSAOP:
		// scalararraysel over the IN (clauseSelectivity's InExpr arm), and
		// one descent per element (btcostestimate's num_sa_scans).
		sel = clampSelectivity(clauseSelectivity(saopPricedClause(cat, tbl, idx, clauses[saopAt]), rel.baseLeaf))
		if saopAt > 0 {
			// The equality prefix's own selectivity (clauselist_selectivity
			// multiplies the independent quals).
			eqSel, _ := restrictionIndexSelectivity(tbl, idx, clauses[:saopAt], relTuples)
			sel = clampSelectivity(sel * eqSel)
		}
		if len(clauses) > saopAt+1 {
			sel = clampSelectivity(sel * rangeIndexSelectivity(rel.baseLeaf, conjuncts, clauses[saopAt+1:]))
		}
		numSAScans = saopDescents(clauses[saopAt])
		// M0148-0001c3a4 (i): the column quals merged into the array.
		for _, q := range clauses[saopAt].saopColQuals {
			sel = clampSelectivity(sel * clauseSelectivity(q.local, rel.baseLeaf))
		}
	case isRange:
		sel = rangeIndexSelectivity(rel.baseLeaf, conjuncts, clauses)
	case prefixRange:
		var eq, rng []indexPathClause
		for _, c := range clauses {
			if c.op == parser.OpUnknown && c.nullTest == IndexNullTestNone {
				eq = append(eq, c)
			} else {
				rng = append(rng, c)
			}
		}
		sel, _ = restrictionIndexSelectivity(tbl, idx, eq, relTuples)
		sel = clampSelectivity(sel * rangeIndexSelectivity(rel.baseLeaf, conjuncts, rng))
	default:
		sel, unique = restrictionIndexSelectivity(tbl, idx, clauses, relTuples)
	}

	var keys []PathKey
	dir := ForwardScanDirection
	if len(colExprs) > 0 && indexIsOrderable(idx) {
		keys, dir = indexPathOrdering(idx, colExprs, false)
	}

	indexPages, indexTuples, treeHeight := estimateIndexGeometry(cat, idx, tbl, relTuples)
	// A skip probe descends once per distinct skipped prefix
	// (btcostestimate's num_sa_scans); a reverted estimate prices the index
	// side over the whole index (skipScanDescents, boundSelectivity 1).
	// (boundSel may already hold the trailing-array arm's value.)
	if skip > 0 {
		var boundCounted bool
		numSAScans, boundCounted = skipScanDescents(tbl, idx, skip, relTuples, indexPages)
		if !boundCounted {
			boundSel = 1
		}
	}
	qpquals := localQualOpCount(rel.baseLeaf) - indexClausesEvalOps(clauses)
	if qpquals < 0 {
		qpquals = 0
	}
	in := indexScanInputs{
		relPages:                relPages,
		relTuples:               relTuples,
		indexPages:              indexPages,
		indexTuples:             indexTuples,
		treeHeight:              treeHeight,
		selectivity:             sel,
		uniqueEqualityOnAllKeys: unique,
		correlation:             indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages:         totalPages,
		// qpquals = baserestrictinfo minus the index quals (costsize.c:806-820):
		// the consumed equalities are applied by the probe, not re-checked.
		numQualOps:       qpquals,
		numIndexQuals:    indexQualCount(clauses),
		numSAScans:       numSAScans,
		boundSelectivity: boundSel,
	}
	cost := costIndexScan(s.cp, in)
	tgt, tgtKnown := scanPathTarget(rel)
	fwd := &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		ParallelSafe:  rel.ParallelSafeForPath(),
		DisabledNodes: disabledNodesFor(!s.cp.enableIndexScan),
		// cost_index's unparameterised arm: `path->rows = baserel->rows`.
		Rows:          rel.Rows,
		Cost:          cost,
		Pathkeys:      keys,
		IndexInfo:     idx,
		IndexScanDir:  dir,
		IndexClauses:  clauses,
		RequiredOuter: 0,
		Target:        tgt,
		TargetKnown:   tgtKnown,
		// M0146-0005dg: clause i probes Columns[skip+i].
		IndexSkipPrefix: skip,
	}
	addPath(rel, fwd, "index.restrict")
	// M0148-0001c3a3: build_index_paths step 5, the backward twin, when the
	// reversed order is useful. Not for a ScalarArrayOp or skip probe: the
	// executor walks only the plain probe path backward.
	if len(keys) > 0 && !isSAOP && skip == 0 {
		if bkeys := s.usefulBackwardIndexPathkeys(rel, idx, colExprs); len(bkeys) > 0 {
			back := *fwd
			back.Pathkeys, back.IndexScanDir = bkeys, BackwardScanDirection
			addPath(rel, &back, "index.restrict.backward")
		}
	}
	return true
}

// boundIndexColumns is how many leading index columns the clauses bind: one
// past the highest bound index column (equality prefix, SAOP, or a range on
// the column after them).
func boundIndexColumns(clauses []indexPathClause) int {
	n := 0
	for _, c := range clauses {
		if c.indexCol+1 > n {
			n = c.indexCol + 1
		}
	}
	return n
}

// indexUnboundKeysNullSafe widens indexUnboundKeysNotNull with the query's
// own restriction: an unbound key column that a strict conjunct of quals
// restricts (`b = 20`, `b > 3`, `b IN (...)`, `b IS NOT NULL`) cannot lose a
// qualifying row to a missing NULL-keyed entry, because a row with a NULL
// there fails that conjunct anyway. quals may be nil (no restriction known).
func indexUnboundKeysNullSafe(tbl *catalog.Table, idx *catalog.Index, bound int, quals Expr) bool {
	if tbl == nil || idx == nil {
		return false
	}
	if catalog.IndexHasNullKeyedEntries(idx) {
		return true
	}
	for i := bound; i < len(idx.Columns); i++ {
		name := idx.Columns[i]
		if name == "" {
			return false
		}
		if qualsRejectNull(name, quals) {
			continue
		}
		notNull := false
		for _, c := range tbl.Columns {
			if c.Name == name {
				notNull = c.NotNull
				break
			}
		}
		if !notNull {
			return false
		}
	}
	return true
}

// qualsRejectNull reports whether some top-level AND conjunct of quals is a
// strict test of column name — a comparison, an IN list, or IS NOT NULL —
// that is never true when the column is NULL.
func qualsRejectNull(name string, quals Expr) bool {
	isCol := func(e Expr) bool {
		for {
			if c, ok := e.(*CastExpr); ok {
				e = c.Operand
				continue
			}
			break
		}
		cr, ok := e.(*ColumnRef)
		return ok && cr.Name == name
	}
	var walk func(e Expr) bool
	walk = func(e Expr) bool {
		switch x := e.(type) {
		case *BinaryOp:
			switch x.Op {
			case parser.OpAnd:
				return walk(x.Left) || walk(x.Right)
			case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
				return isCol(x.Left) || isCol(x.Right)
			}
		case *InExpr:
			return isCol(x.Operand)
		case *IsNullExpr:
			return x.Negated && isCol(x.Operand)
		}
		return false
	}
	return quals != nil && walk(quals)
}

// indexUnboundKeysNotNull reports whether every index key column from
// position `bound` on is declared NOT NULL — the condition under which a
// probe binding only the first `bound` columns cannot miss rows the byte-key
// btree never stored (entries with a NULL key column are absent). Expression
// key columns (no catalog column) are treated as nullable.
func indexUnboundKeysNotNull(tbl *catalog.Table, idx *catalog.Index, bound int) bool {
	if tbl == nil || idx == nil {
		return false
	}
	// An index that stores NULL-keyed entries (a tuple-format index in a
	// cluster with null_keyed_index_entries) misses no row, so no key column
	// needs to be NOT NULL. store-null-keys S3.
	if catalog.IndexHasNullKeyedEntries(idx) {
		return true
	}
	for i := bound; i < len(idx.Columns); i++ {
		name := idx.Columns[i]
		if name == "" {
			return false
		}
		found := false
		for _, c := range tbl.Columns {
			if c.Name == name {
				if !c.NotNull {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}


// normalizeColumnIndexKey recognises a clause the index producers can bind:
// a column of this relation compared with a PSEUDO-CONSTANT operand — a
// literal, or a value fixed for the whole scan though unknown at plan time (a
// correlated outer-level reference, `OuterColumnRef` / `ExecParamRef`).
// colOnRight reports that the column was the right operand, so a range
// operator must be flipped. This is the operand half of PG's
// `match_clause_to_indexcol` (indxpath.c): the other side may be anything that
// references none of the index's own relation's columns and is not volatile,
// and an outer-level Var is a PARAM_EXEC Param there (M0146-0015a). The
// shared `normalizeColumnConst` keeps its literal-only contract: the
// selectivity code reads MCVs and histograms through it.
func normalizeColumnIndexKey(l, r Expr) (*ColumnRef, Expr, bool, bool) {
	if cr, ok := l.(*ColumnRef); ok && isIndexKeyOperand(r) {
		return cr, coercedLiteralKey(r), false, true
	}
	if cr, ok := r.(*ColumnRef); ok && isIndexKeyOperand(l) {
		return cr, coercedLiteralKey(l), true, true
	}
	return nil, nil, false, false
}

// coercedLiteralKey unwraps the resolver's implicit coercion of an untyped
// literal to the column's type (`'reg_idx'` → `'reg_idx'::regclass`) to the
// literal itself: the btree key encoder converts a literal against the
// column's own type, array columns included, which is how the rule-based
// producer probed these keys. In PG the coercion folds to a Const of the
// column type (eval_const_expressions); the literal is goopg's equivalent.
// An explicit or non-literal cast is kept as is (M0146-0060).
func coercedLiteralKey(e Expr) Expr {
	c, ok := e.(*CastExpr)
	if !ok || c.Explicit {
		return e
	}
	if _, isStr := c.Operand.(*StringConst); isStr {
		return c.Operand
	}
	if _, isTyped := c.Operand.(*TypedStringLit); isTyped {
		return c.Operand
	}
	return e
}

// isIndexKeyOperand is the pseudo-constant set normalizeColumnIndexKey admits.
func isIndexKeyOperand(e Expr) bool {
	if isConstExpr(e) {
		return true
	}
	if _, isParam := e.(*ParamRef); isParam {
		return true
	}
	if c, isCast := e.(*CastExpr); isCast && isConstantPlanExpr(c) {
		return true
	}
	_, isOuter := outerParamKeyType(e)
	return isOuter
}

// outerParamKeyType reports whether e is a correlated outer-level reference —
// constant for one execution of the scan, re-evaluated when the enclosing
// SubPlan re-runs it — and returns its type.
func outerParamKeyType(e Expr) (catalog.Type, bool) {
	if o, ok := e.(*OuterColumnRef); ok {
		return o.Type, true
	}
	if p, ok := e.(*ExecParamRef); ok {
		return p.Type, true
	}
	return catalog.Type{}, false
}

// indexKeyEqSelectivity is the selectivity of `col = key` for an index
// clause. A literal reads the column's MCVs/histogram (eqSelectivityForColumn);
// a key unknown at plan time takes PG's `var_eq_non_const` average, which is
// what `eqsel` computes when the other operand is a Param.
func indexKeyEqSelectivity(stats *catalog.ColumnStats, key Expr, rawRows float64, typeName string) float64 {
	if _, isOuter := outerParamKeyType(key); isOuter {
		return varEqNonConstSelectivity(stats, rawRows)
	}
	return eqSelectivityForColumn(stats, key, rawRows, typeName)
}

// saopColQual is one further qual on a ScalarArrayOp probe's column
// (optimizer.SAOPColQual's path form).
type saopColQual struct {
	op     parser.OpCode
	key    Expr
	array  []Expr
	local  Expr
	before bool
}

// saopColumnQuals collects the restriction's other constant quals on the
// array's column — further `= ANY` lists and range bounds — which
// _bt_preprocess_keys merges into the array (_bt_merge_arrays,
// _bt_compare_array_scankey_args): PG prints them in the same Index Cond
// (`((unique1 = ANY ('{1,42,7}')) AND (unique1 >= 42))`). before marks a
// qual written ahead of the array, by source position (restriction order).
func saopColumnQuals(cat catalog.Catalog, tbl *catalog.Table, colName string, primary Expr, conjuncts []Expr) []saopColQual {
	var out []saopColQual
	for _, conj := range conjuncts {
		if conj == primary {
			continue
		}
		before := conj.Pos() < primary.Pos()
		switch x := conj.(type) {
		case *InExpr:
			if x.Subquery != nil || x.Plan != nil || len(x.List) == 0 || x.Negated || x.NotEqualAny || x.AllOp ||
				(x.AnyOp != parser.OpUnknown && x.AnyOp != parser.OpEq) {
				continue
			}
			cr, ok := x.Operand.(*ColumnRef)
			if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != colName {
				continue
			}
			list, ok := saopElementsFor(tbl.Columns[cr.Index], x.List)
			if !ok {
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
				out = append(out, saopColQual{op: parser.OpEq, array: list, local: conj, before: before})
			}
		case *BinaryOp:
			switch x.Op {
			case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
			default:
				continue
			}
			cr, val, colOnRight, ok := normalizeColumnIndexKey(x.Left, x.Right)
			if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != colName ||
				!isConstExpr(val) || !restrictionKeyUsable(cat, tbl.Columns[cr.Index], val) {
				continue
			}
			op := x.Op
			if colOnRight {
				op = swapInequalityOp(op)
			}
			out = append(out, saopColQual{op: op, key: val, local: conj, before: before})
		}
	}
	// Index Cond lists a column's quals in restriction (source) order;
	// conjuncts may arrive reordered.
	sort.SliceStable(out, func(i, j int) bool { return out[i].local.Pos() < out[j].local.Pos() })
	return out
}

// prefixEqualitySAOP reports an `= ANY` array on index column n behind an
// equality prefix (M0148-0001c3a4 (i)). PG makes every btree qual on the
// column an index qual and _bt_preprocess_keys merges the column's bounds
// into the array, so the array, not the bounds, is the column's probe: the
// bounds ride along as its saopColQuals.
func prefixEqualitySAOP(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, n int, conjuncts []Expr) bool {
	if idx.DeclaredHash || n <= 0 || n >= len(idx.Columns) {
		return false
	}
	sa := restrictionSAOPOnColumn(cat, tbl, idx, n, conjuncts)
	return len(sa) > 0 && sa[0].saopOp == parser.OpUnknown
}
