package optimizer

// M0134-0187 — the index-only PATH: `check_index_only` (indxpath.c:1010) plus
// `create_index_path(..., indexonly=true)`. DESIGN §15/§21.
//
// Until this file, goopg reached an index-only scan ONLY through
// `tryPromoteIndexOnlyScan`, a post-planning peephole over a `*Project`.
// Inside a join tree the join reads the scan directly, so the promotion fired
// zero times across all 22 TPC-H queries and the index-only scans PG chooses
// for Q13/Q16 (both HASH-JOIN INPUTS) were unreachable. A path fixes that:
// `addPath` decides on cost, alongside the seq scan and every other access
// method. Nothing here prefers the shape — the saving comes entirely from
// `cost_index`'s `allvisfrac` term (`heapPagesAfterVM`, costindex.go), so on
// a cold visibility map the path is generated, priced exactly as a plain
// index scan, and loses. That is PG's behaviour.
//
// Two narrowings, stated as refusals:
//
//   - Only a BARE leaf, or a leaf whose local quals are ALL consumed as index
//     quals (M0145-0029 slice 3: `restrictionEqualityPrefix`, PG's
//     `build_index_paths` with `index_clauses` and `indexonly = true`). A
//     residual local qual has a predicate whose `ColumnRef.Index` values are
//     written against the FULL leaf schema; narrowing the scan under it would
//     re-point them, so a leaf that would keep one is still refused (ledgered:
//     PG keeps such quals as the Index Only Scan's Filter).
//   - Only when the needed set is KNOWN (`neededColumnNames`): an index-only
//     scan that drops a column the query reads returns wrong rows.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// addIndexOnlyPaths generates, for every base relation, the index-only paths
// whose index covers everything the statement reads from that relation.
func (s *searchCtx) addIndexOnlyPaths(cat catalog.Catalog) {
	if s == nil || cat == nil || !s.neededColsKnown {
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
		// A leaf with local quals is refused unless an index consumes all of
		// them — see the file header.
		bare := scanLeafIsBare(rel.baseLeaf)
		var conjuncts []Expr
		if !bare {
			if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
				continue
			}
			conjuncts = extractFilterConjuncts(rel.baseLeaf)
			if len(conjuncts) == 0 {
				continue
			}
		}
		needed := s.neededColumnsOf(tbl)
		if len(needed) == 0 {
			// Nothing read from this relation at all — the walker and the
			// plan disagree about what this rel is for; a scan emitting no
			// columns is not a shape to invent from an approximation.
			continue
		}
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		added := false
		for _, idx := range cat.IndexesOnTable(tbl) {
			var clauses []indexPathClause
			if !bare {
				clauses = consumingIndexClauses(cat, tbl, idx, conjuncts)
				if clauses == nil {
					continue
				}
			}
			if s.addOneIndexOnlyPath(rel, tbl, idx, needed, clauses, relPages, relTuples, totalPages) {
				added = true
			}
		}
		if added {
			// An unparameterised path can change CheapestTotal/CheapestStartup
			// — the same re-run `addOrderedIndexPaths` does.
			setCheapest(rel)
		}
	}
}

// neededColumnsOf is the per-relation half of `check_index_only`'s question:
// which of THIS table's columns does the statement read? Name-matched against
// the statement-wide set, which over-states rather than under-states — see
// pathindexonlyneed.go.
func (s *searchCtx) neededColumnsOf(tbl *catalog.Table) []catalog.Column {
	out := make([]catalog.Column, 0, len(tbl.Columns))
	for _, c := range tbl.Columns {
		if s.neededCols[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// consumingIndexClauses returns the equality-prefix index clauses of `idx`
// when they consume EVERY local conjunct of the leaf, else nil. A partial
// index declines, as in the plain restriction producer.
func consumingIndexClauses(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) []indexPathClause {
	if idx == nil || idx.HasPredicate {
		return nil
	}
	clauses := restrictionEqualityPrefix(cat, tbl, idx, conjuncts)
	if len(clauses) == 0 || len(clauses) != len(conjuncts) {
		return nil
	}
	// Each clause names a distinct conjunct (one per index column), so equal
	// counts mean every conjunct is consumed — unless one conjunct bound two
	// columns, which restrictionEqualityPrefix cannot do (one column per
	// `col = const`); checked anyway, since a missed residual is wrong rows.
	used := make(map[Expr]bool, len(clauses))
	for _, c := range clauses {
		used[c.local] = true
	}
	for _, conj := range conjuncts {
		if !used[conj] {
			return nil
		}
	}
	return clauses
}

// restrictionPathIsIndexOnly reports whether addIndexOnlyPaths builds the
// index-only path over `idx` with the equality-prefix clauses of the leaf's
// `conjuncts` — the same three conditions it applies (enable_indexonlyscan,
// a covering index, every local qual consumed). The plain restriction
// producer asks it so that exactly one of the two builds the path, as
// build_index_paths' single `index_only_scan` flag does.
func (s *searchCtx) restrictionPathIsIndexOnly(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) bool {
	if !s.neededColsKnown || indexOnlyHardDisabled(cat) {
		return false
	}
	needed := s.neededColumnsOf(tbl)
	if len(needed) == 0 {
		return false
	}
	if _, ok := indexCoversColumns(idx, needed); !ok {
		return false
	}
	return consumingIndexClauses(cat, tbl, idx, conjuncts) != nil
}

// addOneIndexOnlyPath builds the index-only path for one index, or declines.
// `clauses` is empty for the full-index-scan shape over a bare leaf, or the
// index quals that consume all of the leaf's local quals.
func (s *searchCtx) addOneIndexOnlyPath(rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	needed []catalog.Column, clauses []indexPathClause, relPages int64, relTuples, totalPages float64) bool {
	covered, ok := indexCoversColumns(idx, needed)
	if !ok {
		return false
	}
	// A full index scan: no bound quals, so every entry is read. PG's
	// selectivity for an index path with no indexclauses is 1.0 ("An empty
	// indexclauses list implies a full index scan", pathnodes.h:1817).
	sel, unique := 1.0, false
	if len(clauses) > 0 {
		sel, unique = restrictionIndexSelectivity(tbl, idx, clauses, relTuples)
	}
	qpquals := localQualOpCount(rel.baseLeaf) - float64(len(clauses))
	if qpquals < 0 {
		qpquals = 0
	}
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
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
		loopCount:               1,
		indexOnly:               true,
		allVisFrac:              relAllVisibleFraction(tbl, relPages),
		// R1 (plan-parity-fix-take2): every local conjunct not consumed as an
		// index qual is a qpqual, as the seq rival counts them
		// (costsize.c:806-820).
		numQualOps: qpquals,
	}
	cost := costIndexScan(s.cp, in)
	// take2 P4-01 Slice 1: the scan Target, computed from NeededCols at
	// path-creation time (see the Target comment on the literal below).
	tgt, tgtKnown := scanPathTarget(rel)
	serial := &Path{
		Kind:             PathIndexScan,
		Rel:              rel,
		Rows:             rel.Rows,
		Cost:             cost,
		// create_index_path (pathnode.c:1078): `rel->consider_parallel`. C-19a.
		ParallelSafe:     rel.ParallelSafeForPath(),
		// B-17d: an index-only path is costed by cost_index too, so
		// enable_indexscan = off counts here as well (planner.go's
		// indexOnlyScanRejected documents the OR). enable_indexonlyscan =
		// off stays a generation gate at the caller (check_index_only).
		DisabledNodes:    disabledNodesFor(!s.cp.enableIndexScan),
		IndexInfo:        idx,
		IndexScanDir:     ForwardScanDirection,
		IndexOnly:        true,
		IndexOnlyCovered: covered,
		// take2 P4-01: this path emits only the covered columns, so it must
		// carry its own width. Without it the hash geometry was solved for the
		// relation's FULL column count while the executor measured this
		// narrowed node's schema — the planner/executor divergence the shared
		// hashsize.Choose exists to prevent.
		NCols:       len(covered),
		AvgVarBytes: coveredAvgVarBytes(tbl, covered),
		// R91: virtual-bucket geometry consumes the emitted packed-tuple
		// width, never the Goopg map-footprint fields. TupleWidth is the
		// existing PG-style source over exactly this covered schema.
		OutputWidth: indexOnlyOutputWidth(covered),
		// take2 P4-01 Slice 1: the scan Target, computed from NeededCols at
		// path-creation time. Assert-only — never applied, never costed; the
		// NCols/AvgVarBytes pair above is unchanged.
		Target:      tgt,
		TargetKnown: tgtKnown,
		// Empty: the full-index-scan shape, and `createPlan` reads the empty
		// list as exactly that. Non-empty: the probe, whose conjuncts
		// createPlan drops from the leaf (they were all of them).
		IndexClauses: clauses,
	}
	addPath(rel, serial, "indexonly")
	// C-19c: the partial twin, `create_index_path(..., index_only_scan,
	// ..., partial_path = true)` — the same build_index_paths arm as the
	// plain scan's; cost_index sizes it on index pages alone
	// (`rand_heap_pages = -1` when indexonly). Executor counterpart exists
	// since M0134-0189 (Parallel Index Only Scan).
	s.addPartialIndexPath(rel, tbl, serial, in, "indexonly.partial")
	return true
}

// indexOnlyOutputWidth constructs the same SchemaColumn view a plan node emits
// before asking the one PG-style byte-width authority, TupleWidth. It is not a
// map-footprint proxy: catalog columns carry their real planner type widths.
func indexOnlyOutputWidth(covered []catalog.Column) int {
	schema := make([]SchemaColumn, len(covered))
	for i, col := range covered {
		schema[i] = SchemaColumn{Name: col.Name, Type: col.Type}
	}
	return TupleWidth(schema)
}

// indexCoversColumns is `check_index_only`'s coverage test: every needed column
// must be an index KEY column. Returns the covered list in INDEX-COLUMN order —
// the order the scan emits them and therefore the order `baseRelLayout`
// re-bases. Index order rather than table order is not cosmetic: the executor's
// `IndexOnlyScan.Covered` names what the index tuple supplies position by
// position.
func indexCoversColumns(idx *catalog.Index, needed []catalog.Column) ([]catalog.Column, bool) {
	if idx == nil || len(idx.Columns) == 0 || !isBTreeIndex(idx) {
		return nil, false
	}
	// A partial index answers only for the rows its predicate admits — the
	// same refusal `pickIndexCoveringLeadingPrefix` makes.
	if idx.HasPredicate {
		return nil, false
	}
	inIndex := make(map[string]bool, len(idx.Columns))
	for _, c := range idx.Columns {
		inIndex[c] = true
	}
	for _, c := range needed {
		if !inIndex[c.Name] {
			return nil, false
		}
	}
	wanted := make(map[string]catalog.Column, len(needed))
	for _, c := range needed {
		wanted[c.Name] = c
	}
	covered := make([]catalog.Column, 0, len(needed))
	for _, name := range idx.Columns {
		if c, want := wanted[name]; want {
			covered = append(covered, c)
		}
	}
	return covered, len(covered) == len(needed)
}

// relAllVisibleFraction is PG's `baserel->allvisfrac` (`estimate_rel_size`,
// plancat.c:1050): the fraction of the heap the visibility map marks
// all-visible — the ONLY thing that makes an index-only scan cheaper than an
// index scan. goopg's VM is readable through `catalog.RelAllVisible` (wired by
// initdb), so this is the real figure: a never-vacuumed table returns 0 and
// the path loses on cost.
func relAllVisibleFraction(tbl *catalog.Table, relPages int64) float64 {
	if tbl == nil || relPages <= 0 {
		return 0
	}
	visible := catalog.RelAllVisible(tbl)
	if visible <= 0 {
		return 0
	}
	frac := float64(visible) / float64(relPages)
	if frac > 1 {
		return 1
	}
	return frac
}

// coveredAvgVarBytes is the variable-width payload of just the columns an
// index-only path emits — the AvgVarBytes twin of its NCols.
//
// `RelOptInfo.AvgVarBytes` sums every column of the relation (joinsearch.go),
// which is the right figure for a scan that returns every column and an
// over-count for one that does not.
func coveredAvgVarBytes(tbl *catalog.Table, covered []catalog.Column) float64 {
	if tbl == nil || tbl.Stats == nil {
		return 0
	}
	var sum float64
	for _, c := range covered {
		for i := range tbl.Columns {
			if !strings.EqualFold(tbl.Columns[i].Name, c.Name) {
				continue
			}
			if i < len(tbl.Stats.Columns) {
				sum += tbl.Stats.Columns[i].AvgWidth
			}
			break
		}
	}
	return sum
}
