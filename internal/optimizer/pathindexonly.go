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
//   - Only a BARE leaf. A leaf carrying local quals has a predicate whose
//     `ColumnRef.Index` values are written against the FULL leaf schema;
//     narrowing the scan under it would re-point them.
//   - Only when the needed set is KNOWN (`neededColumnNames`): an index-only
//     scan that drops a column the query reads returns wrong rows.

import "github.com/goopg/goopg/internal/catalog"

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
		// A leaf with local quals is refused — see the file header.
		if !scanLeafIsBare(rel.baseLeaf) {
			continue
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
			if s.addOneIndexOnlyPath(rel, tbl, idx, needed, relPages, relTuples, totalPages) {
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

// addOneIndexOnlyPath builds the index-only path for one index, or declines.
func (s *searchCtx) addOneIndexOnlyPath(rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	needed []catalog.Column, relPages int64, relTuples, totalPages float64) bool {
	covered, ok := indexCoversColumns(idx, needed)
	if !ok {
		return false
	}
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	cost := costIndexScan(s.cp, indexScanInputs{
		relPages:    relPages,
		relTuples:   relTuples,
		indexPages:  indexPages,
		indexTuples: indexTuples,
		treeHeight:  treeHeight,
		// A full index scan: no bound quals, so every entry is read. PG's
		// selectivity for an index path with no indexclauses is 1.0 ("An
		// empty indexclauses list implies a full index scan",
		// pathnodes.h:1817).
		selectivity:     1,
		correlation:     indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages: totalPages,
		loopCount:       1,
		indexOnly:       true,
		allVisFrac:      relAllVisibleFraction(tbl, relPages),
	})
	addPath(rel, &Path{
		Kind:             PathIndexScan,
		Rel:              rel,
		Rows:             rel.Rows,
		Cost:             cost,
		IndexInfo:        idx,
		IndexScanDir:     ForwardScanDirection,
		IndexOnly:        true,
		IndexOnlyCovered: covered,
		// No index clauses: this is the full-index-scan shape, and
		// `createPlan` reads the empty list as exactly that.
	})
	return true
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
