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
	for pos, colName := range idx.Columns {
		var found *indexPathClause
		for _, conj := range conjuncts {
			bin, ok := conj.(*BinaryOp)
			if !ok || bin.Op != parser.OpEq {
				continue
			}
			cr, val, ok := normalizeColumnConst(bin.Left, bin.Right)
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
			found = &indexPathClause{indexCol: pos, key: val, local: conj}
			break
		}
		if found == nil {
			break
		}
		clauses = append(clauses, *found)
	}
	return clauses
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
// pos 0 is the leading-column range; pos > 0 follows an equality prefix
// (slice 2b). Behind a prefix the bounds NEVER record `local`: the conjunct
// stays in the Filter as a recheck, because an open-ended bound runs to the
// prefix's padded upper bound, past entries whose bounded column is NULL.
func restrictionRangeOnColumn(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) []indexPathClause {
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
		cr, val, colOnRight, ok := normalizeColumnConstRange(bin.Left, bin.Right)
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
		if pos == 0 && len(idx.Columns) == 1 {
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
	return out
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
	if len(idx.Columns) == 0 {
		return nil
	}
	lead := idx.Columns[0]
	for _, conj := range conjuncts {
		in, ok := conj.(*InExpr)
		if !ok || in.Subquery != nil || in.Plan != nil || len(in.List) == 0 {
			continue
		}
		if in.Negated || in.NotEqualAny || in.AllOp || (in.AnyOp != parser.OpUnknown && in.AnyOp != parser.OpEq) {
			continue
		}
		cr, ok := in.Operand.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != lead {
			continue
		}
		usable := true
		for _, e := range in.List {
			if !restrictionKeyUsable(cat, tbl.Columns[cr.Index], e) {
				usable = false
				break
			}
		}
		if !usable {
			continue
		}
		return []indexPathClause{{indexCol: 0, saop: in.List, local: conj}}
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
	if !isConstExpr(val) {
		return false
	}
	if _, isBool := val.(*BooleanConst); isBool {
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

// restrictionIndexSelectivity is the index quals' selectivity alone
// (btcostestimate's clauselist_selectivity over indexQuals), with PG's
// `isunique` short circuit for a unique index bound on every key column.
// Shared by the plain (addOneRestrictionIndexPath) and index-only
// (addOneIndexOnlyPath) producers so the two price the same probe alike.
func restrictionIndexSelectivity(tbl *catalog.Table, idx *catalog.Index, clauses []indexPathClause, relTuples float64) (float64, bool) {
	rawRows := 0.0
	if tbl.Stats != nil {
		rawRows = float64(tbl.Stats.RowCount)
	}
	sel := 1.0
	for _, c := range clauses {
		stats := columnStatsByName(tbl, idx.Columns[c.indexCol])
		sel *= eqSelectivityForColumn(stats, c.key, rawRows)
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
	// An unproven partial index would silently drop rows (no predicate-
	// implication prover — same decline as the ordered arm, ledgered there).
	if idx.HasPredicate {
		return false
	}
	clauses := restrictionEqualityPrefix(cat, tbl, idx, conjuncts)
	isRange := false
	// Slice 2b: an equality prefix that stops short of the last index column
	// may be followed by range bounds on the NEXT column (PG binds `b > 5`
	// behind `a = 1` on `(a, b)`). The eq clauses become IndexScan.RangePrefix.
	prefixRange := false
	if n := len(clauses); n > 0 && n < len(idx.Columns) {
		if rng := restrictionRangeOnColumn(cat, tbl, idx, n, conjuncts); len(rng) > 0 {
			clauses = append(clauses, rng...)
			prefixRange = true
		}
	}
	isSAOP := false
	if len(clauses) == 0 {
		clauses = restrictionLeadingSAOP(cat, tbl, idx, conjuncts)
		isSAOP = len(clauses) > 0
		// A SAOP on the leading column may be followed by bounds on the
		// second (PG `Index Cond: ((a = ANY (...)) AND (b > 1))`). The bounds
		// stay in the Filter as a recheck (restrictionRangeOnColumn records no
		// `local` behind a leading column).
		if isSAOP && len(idx.Columns) > 1 {
			clauses = append(clauses, restrictionRangeOnColumn(cat, tbl, idx, 1, conjuncts)...)
		}
	}
	if len(clauses) == 0 {
		clauses = restrictionLeadingRange(cat, tbl, idx, conjuncts)
		isRange = true
	}
	if len(clauses) == 0 {
		return false
	}
	// build_index_paths builds ONE path per index and makes it index-only
	// when check_index_only holds (indxpath.c:1010, `index_only_scan`). When
	// addIndexOnlyPaths will build that index-only path with these same
	// clauses, a plain twin here would tie it on cost and — filed first —
	// win add_path's tie, electing the heap-fetching shape PG never builds.
	if s.restrictionPathIsIndexOnly(cat, tbl, idx, conjuncts) {
		return false
	}
	// The byte-key btree stores NO entry whose key has a NULL column
	// (collectBTreeEntries: "not storable in the byte-key btree"), so a probe
	// that leaves a nullable key column unbound would silently miss every
	// matching row with a NULL there (measured: `a IN (7, 8) AND b > 90` on
	// (a, b, c) lost the (7, 92, NULL) row). Bound columns are safe — a NULL
	// there fails the qual anyway. PG stores NULL keys and has no such rule.
	if !indexUnboundKeysNotNull(tbl, idx, boundIndexColumns(clauses)) {
		return false
	}
	var sel float64
	var unique bool
	numSAScans := 0.0
	switch {
	case isSAOP:
		// scalararraysel over the IN (clauseSelectivity's InExpr arm), and
		// one descent per element (btcostestimate's num_sa_scans).
		sel = clampSelectivity(clauseSelectivity(clauses[0].local, rel.baseLeaf))
		if len(clauses) > 1 {
			sel = clampSelectivity(sel * rangeIndexSelectivity(rel.baseLeaf, conjuncts, clauses[1:]))
		}
		numSAScans = float64(len(clauses[0].saop))
	case isRange:
		sel = rangeIndexSelectivity(rel.baseLeaf, conjuncts, clauses)
	case prefixRange:
		var eq, rng []indexPathClause
		for _, c := range clauses {
			if c.op == parser.OpUnknown {
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

	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	qpquals := localQualOpCount(rel.baseLeaf) - float64(len(clauses))
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
		numQualOps: qpquals,
		numSAScans: numSAScans,
	}
	cost := costIndexScan(s.cp, in)
	tgt, tgtKnown := scanPathTarget(rel)
	addPath(rel, &Path{
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
	}, "index.restrict")
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

// indexUnboundKeysNotNull reports whether every index key column from
// position `bound` on is declared NOT NULL — the condition under which a
// probe binding only the first `bound` columns cannot miss rows the byte-key
// btree never stored (entries with a NULL key column are absent). Expression
// key columns (no catalog column) are treated as nullable.
func indexUnboundKeysNotNull(tbl *catalog.Table, idx *catalog.Index, bound int) bool {
	if tbl == nil || idx == nil {
		return false
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

