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
//   - A leaf's local quals are either consumed as index quals (M0145-0029
//     slice 3: `restrictionEqualityPrefix`, PG's `build_index_paths` with
//     `index_clauses` and `indexonly = true`) or kept as the Index Only
//     Scan's Filter (M0146-0019a), re-based by createPlan from the full leaf
//     schema onto the covered columns. A residual that reads an uncovered
//     column or holds a correlated sublink is refused
//     (indexOnlyResidualAdmissible).
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
		// A leaf's local quals are index quals or the scan's Filter — see the
		// file header.
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
		needed := s.neededColumnsOfRel(rel, tbl)
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
				var ok bool
				clauses, ok = s.indexOnlyLeafClauses(cat, rel, tbl, idx, needed, conjuncts)
				if !ok {
					continue
				}
			} else if !indexUnboundKeysNotNull(tbl, idx, 0) {
				// A full scan leaves every key column unbound, so it needs the
				// same NULL-key rule as indexOnlyLeafClauses: an index without
				// NULL-keyed entries would drop those rows.
				continue
			}
			if s.addOneIndexOnlyPath(cat, rel, tbl, idx, needed, clauses, relPages, relTuples, totalPages) {
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
// neededColumnsOfRel is neededColumnsOf for the relation `rel` reads
// `tbl` as: a column only another alias of the same table reads by
// qualified name is not needed here (M0146-0005bq-b, pathindexonlyneed.go).
func (s *searchCtx) neededColumnsOfRel(rel *RelOptInfo, tbl *catalog.Table) []catalog.Column {
	qual := ""
	if rel != nil {
		if id, _, ok := scanLeafFor(rel.baseLeaf); ok && id != nil {
			qual = id.alias
			if qual == "" {
				qual = tbl.Name
			}
		}
	}
	out := make([]catalog.Column, 0, len(tbl.Columns))
	for _, c := range tbl.Columns {
		if neededColumnNamedFor(s.neededCols, qual, c.Name) {
			out = append(out, c)
		}
	}
	// M0146-0046: check_index_only's attrs_used includes system columns,
	// which no index stores — a statement reading this relation's `ctid`
	// (or another heap system column) can never be answered from the index.
	// The synthetic entry is never covered, so every index-only producer
	// declines; a column of the table cannot carry a system column's name.
	for _, sys := range heapSystemColumnNames {
		if neededColumnNamedFor(s.neededCols, qual, sys) {
			out = append(out, catalog.Column{Name: sys})
		}
	}
	return out
}

// heapSystemColumnNames are the system columns a heap scan supplies and an
// index-only scan cannot (PG's SelfItemPointer through TableOid).
var heapSystemColumnNames = []string{"ctid", "xmin", "xmax", "cmin", "cmax", "tableoid"}

func (s *searchCtx) neededColumnsOf(tbl *catalog.Table) []catalog.Column {
	out := make([]catalog.Column, 0, len(tbl.Columns))
	for _, c := range tbl.Columns {
		if s.neededCols[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// restrictionPathIsIndexOnly reports whether addIndexOnlyPaths builds the
// index-only path over `idx` with the equality-prefix clauses of the leaf's
// `conjuncts` — the same three conditions it applies (enable_indexonlyscan,
// a covering index, every local qual consumed). The plain restriction
// producer asks it so that exactly one of the two builds the path, as
// build_index_paths' single `index_only_scan` flag does.
func (s *searchCtx) restrictionPathIsIndexOnly(cat catalog.Catalog, rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) bool {
	if !s.neededColsKnown || indexOnlyHardDisabled(cat) {
		return false
	}
	needed := s.neededColumnsOfRel(rel, tbl)
	if len(needed) == 0 {
		return false
	}
	_, ok := s.indexOnlyLeafClauses(cat, rel, tbl, idx, needed, conjuncts)
	return ok
}

// indexOnlyLeafClauses is build_index_paths' index-only arm for a leaf with
// local quals (M0146-0019a): the equality-prefix index clauses `idx` can bind
// (possibly none — a full index-only scan), with every other local qual kept
// as the Index Only Scan's Filter, as PG keeps them in the scan's qpqual. It
// declines when the index does not cover what the statement reads, when an
// unbound key column may hold NULLs (those entries are absent), or when a
// residual qual cannot be evaluated over the narrowed row
// (indexOnlyResidualAdmissible).
func (s *searchCtx) indexOnlyLeafClauses(cat catalog.Catalog, rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	needed []catalog.Column, conjuncts []Expr) ([]indexPathClause, bool) {
	if idx == nil || idx.HasPredicate {
		return nil, false
	}
	covered, ok := indexCoversColumns(idx, needed)
	if !ok {
		return nil, false
	}
	clauses := restrictionEqualityPrefix(cat, tbl, idx, conjuncts)
	if !plainRestrictionBindsOnlyPrefix(cat, tbl, idx, conjuncts, len(clauses)) {
		return nil, false
	}
	if !indexUnboundKeysNotNull(tbl, idx, len(clauses)) {
		return nil, false
	}
	used := make(map[Expr]bool, len(clauses))
	for _, c := range clauses {
		used[c.local] = true
	}
	var residual []Expr
	for _, conj := range conjuncts {
		if !used[conj] {
			residual = append(residual, conj)
		}
	}
	if len(residual) > 0 {
		id, _, ok := scanLeafFor(rel.baseLeaf)
		if !ok || !indexOnlyResidualAdmissible(residual, id.schema, covered) {
			return nil, false
		}
	}
	return clauses, true
}

// plainRestrictionBindsOnlyPrefix reports whether the plain restriction
// producer (addOneRestrictionIndexPath) would bind exactly the leaf's
// equality prefix of length n on idx — none when n is 0. PG builds ONE path
// per index with one clause set and makes it index-only when it can
// (build_index_paths), so the index-only path may stand in for the plain one
// only when their clauses agree. A prefix followed by a range on the next
// column, a leading SAOP or range, or a skip run are clauses the index-only
// lowering does not carry yet (ledgered); the plain path keeps those.
func plainRestrictionBindsOnlyPrefix(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr, n int) bool {
	if n > 0 {
		return n >= len(idx.Columns) || len(restrictionRangeOnColumn(cat, tbl, idx, n, conjuncts)) == 0
	}
	if len(restrictionLeadingSAOP(cat, tbl, idx, conjuncts)) > 0 || len(restrictionLeadingRange(cat, tbl, idx, conjuncts)) > 0 {
		return false
	}
	skipClauses, _ := restrictionSkipRun(cat, tbl, idx, conjuncts)
	return len(skipClauses) == 0
}

// indexOnlyResidualAdmissible reports whether every residual qual can stay as
// an Index Only Scan Filter over the covered columns: each column it reads
// (in the leaf's own schema) is covered, and no sublink in it is correlated —
// a correlated sublink's PARAM_EXEC args address the leaf row, which the
// narrowed scan no longer emits. Uncorrelated sublinks (TPC-H Q16's hashed
// `NOT (ps_suppkey = ANY (SubPlan))`) read only their operand.
func indexOnlyResidualAdmissible(residual []Expr, leafSchema Schema, covered []catalog.Column) bool {
	in := make(map[string]bool, len(covered))
	for _, c := range covered {
		in[c.Name] = true
	}
	good := true
	for _, e := range residual {
		walked := walkExprRefs(e, scopeIgnore, exprVisitor{
			Visit: func(n Expr) bool {
				if cr, isCol := n.(*ColumnRef); isCol {
					if cr.Index < 0 || cr.Index >= len(leafSchema) || !in[leafSchema[cr.Index].Name] {
						good = false
					}
				}
				if plans := ExprSubplans(n); len(plans) > 0 {
					h := handleFor(n)
					if h == nil || len(h.params()) > 0 {
						good = false
					}
					for _, p := range plans {
						if planHasOuterRef(p) {
							good = false
						}
					}
				}
				return good
			},
			OnUnknown: func(Expr) { good = false },
		})
		if !walked || !good {
			return false
		}
	}
	return true
}

// addOneIndexOnlyPath builds the index-only path for one index, or declines.
// `clauses` is empty for the full-index-scan shape over a bare leaf, or the
// index quals that consume all of the leaf's local quals.
func (s *searchCtx) addOneIndexOnlyPath(cat catalog.Catalog, rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
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
	qpquals := localQualOpCount(rel.baseLeaf) - indexClausesEvalOps(clauses)
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
		allVisFrac:              relAllVisibleFraction(cat, tbl, relPages),
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
// index scan. goopg's VM is readable through the catalog (wired by initdb),
// so this is the real figure: a never-vacuumed table returns 0 and the path
// loses on cost. The block count is resolved as the pg_class view resolves it
// (`InMemory.RelAllVisibleBlocks`) — the package-level `catalog.RelAllVisible`
// is only the fallback for a catalog that is not an InMemory, since it keys a
// DBOid-less table under the wrong database.
func relAllVisibleFraction(cat catalog.Catalog, tbl *catalog.Table, relPages int64) float64 {
	if tbl == nil || relPages <= 0 {
		return 0
	}
	var visible int32
	if im := inMemoryCat(cat); im != nil {
		visible = im.RelAllVisibleBlocks(tbl)
	} else {
		visible = catalog.RelAllVisible(tbl)
	}
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
