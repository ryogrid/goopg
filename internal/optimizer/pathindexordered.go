package optimizer

// M0127-P5.4c-ii-b — UNPARAMETERISED ordered index paths: `build_index_paths`'
// `useful_pathkeys != NIL` arm (indxpath.c:750-800), the producer without
// which the merge arm's sort-skip branch can never fire.
//
// PG oracle: `build_index_paths` (indxpath.c:975) step 2 and its step-4
// generation condition, `truncate_useless_pathkeys` / `has_useful_pathkeys` /
// `pathkeys_useful_for_merging` (pathkeys.c), `cost_index` (costsize.c:520,
// reproduced in costindex.go). Design: leftdeep-joins 03 §5.3, 04 §1.1.
//
// The finding P5.4c-ii-a stopped on, restated because it is what this file
// exists to fix: recording an index's ordering on a PARAMETERISED path does
// not make an ordered merge outer reachable. `addMergeJoinPath` refuses a
// parameterised path outright (`try_mergejoin_path`, joinpath.c:1073-1081),
// and every ordered path goopg had was parameterised, because index paths were
// built only from join clauses. An ordered path with an EMPTY `RequiredOuter`
// is a different object, and this is the arm that builds it.
//
// PG's fourth generation condition is the one being exercised:
//
//	if (index_clauses != NIL || useful_pathkeys != NIL || useful_predicate ||
//	    index_only_scan)
//
// — an index path is worth building when the index's ORDERING is useful even
// though not one qual can be pushed into it. Such a path reads the whole
// relation through the index, which is nearly always the loser; it wins only
// when the join above it can skip a sort, which is precisely the comparison
// `addPath`'s pathkey dimension exists to hold open.
//
// Live since M0127-P5.9 (2026-08-06): `addBaseRelIndexPaths` is called from
// `searchOneProblem` (relfromjoinlist.go:325) and `GOOPG_PGSHAPED_DP` defaults
// ON, so these paths DO compete for production plans. Validated by
// `pathindexordered_test.go`, no longer in isolation.

import (
	"github.com/goopg/goopg/internal/catalog"
)

// addBaseRelIndexPaths is the whole of `create_index_paths` (indxpath.c:235)
// for the search's base relations: the join half (parameterised probes,
// P5.4b-ii-a) and the plain half (ordering-only scans, this slice).
//
// It exists so the protocol step between `buildInitialRels` and `joinSearch`
// is ONE call rather than two a caller can half-remember. PG runs both halves
// inside one function for the same reason: they read the same clause list and
// they compete in the same rel's pathlist, so generating one without the other
// hands `addPath` an incomplete field.
func (s *searchCtx) addBaseRelIndexPaths(cat catalog.Catalog) {
	// B-17d: every producer below always runs. The session's scan toggles
	// are counted on the paths through s.cp (cost_seqscan / cost_index /
	// cost_bitmap_heap_scan's own flags, costsize.c:295,560,1023), exactly as
	// the join producers do since P2-05 — a disabled method is a strong
	// preference in add_path, not a missing candidate. The one generation
	// gate that stays a gate is enable_indexonlyscan (check_index_only,
	// indxpath.c), like enable_memoize in get_memoize_path; TID and
	// incremental-sort have no producer to gate. The rule-based legacy scan
	// choice (planIndexScanFromWhere, rewriteScanInputsWithSingleTable-
	// Predicates) keeps its own declines: it has no cost competition to
	// express a preference in, and retires with the legacy planner (P6).
	s.addParameterizedIndexPaths(cat)
	s.addOrderedIndexPaths(cat)
	// M0128-P2.4: bitmap scan paths compete alongside index scan paths in
	// add_path for every usable index. They are always generated — PG's
	// create_index_paths generates both indexscan and bitmap paths for every
	// index, and add_path keeps the cheaper one in each cost regime.
	s.addBaseRelBitmapPaths(cat)
	s.addParameterizedBitmapPaths(cat)
	// M0134-0187: `create_index_path(..., indexonly=true)` for every index
	// covering what the statement reads from the relation — generated here
	// because it competes in the same rel's pathlist as the rest.
	if !indexOnlyHardDisabled(cat) {
		s.addIndexOnlyPaths(cat)
	}
}

// addOrderedIndexPaths generates, for every base relation, the unparameterised
// index paths whose ordering some join clause of that relation could merge on.
//
// The gate is `has_useful_pathkeys` (pathkeys.c:2319), and since C-07/P3-06 it
// is COMPLETE: `hasUsefulPathkeys` (querypathkeys.go) answers both of
// upstream's live arms — the rel's own join clauses (merging) and
// `root->query_pathkeys` (ordering), the latter derived by the
// `standard_qp_callback` analogue from the statement's GROUP BY / window /
// DISTINCT / ORDER BY.
//
// What the gate opens onto is still the merging half alone. The useful-column
// set below comes from `mergeableColumnExprsFor`, i.e.
// `pathkeys_useful_for_merging`; `pathkeys_useful_for_ordering` has no
// counterpart, so a rel that passes the gate on the ORDERING arm and has no
// join clause still produces nothing. That is deliberate and is C-07's
// recorded scope line: goopg has no consumer that would SELECT a path for its
// ordering — `finalPath` (joinsearch.go:298) chooses on cost alone, the search
// boundary publishes a Node and drops the chosen path's `Pathkeys`
// (relfromjoinlist.go:218), and the ORDER BY `*Sort` is wrapped on
// unconditionally above it (planner.go:1720). Generating an ordering-only full
// index scan there could only lose on total cost, or win `CheapestStartup`
// under a LIMIT while the redundant Sort still runs.
//
// C-11 (upper `RelOptInfo`s incl. `ORDERED`) and C-12 (a real upper-rel
// `PathSort`) were filed as that consumer. Both landed 2026-09-06 and the
// re-adjudication on 2026-09-07 measured that neither unblocks this: the
// widening does generate the path, and no plan moves, because
// `createOrderedPaths` consumes a NODE and `newPrebuiltPath` carries no
// `Pathkeys` across the seam. The widening is still the map union at the
// `colExprs` line below; what it now waits on is a boundary that publishes
// the chosen path's ordering. See querypathkeys.go's header for the full
// measurement, and `TestCreateOrderedPathsInputArmIsUnreachableFromANode`
// for the pin.
func (s *searchCtx) addOrderedIndexPaths(cat catalog.Catalog) {
	if s == nil || cat == nil {
		return
	}
	totalPages := s.totalTablePages()
	for i, rel := range s.levelRels(1) {
		if i >= len(s.relInfos) {
			break
		}
		if !s.hasUsefulPathkeys(rel) {
			continue
		}
		tbl := s.relInfos[i].table
		if tbl == nil {
			continue
		}
		// Same consumer-side eligibility gate as `addParameterizedIndexPaths`
		// (M0127-P5.5-c): no index path over a leaf `createPlan` cannot rebuild.
		if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
			continue
		}
		colExprs := mergeableColumnExprsFor(rel.Relids, s.clausesAll())
		if len(colExprs) == 0 {
			continue
		}
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		added := false
		for _, idx := range cat.IndexesOnTable(tbl) {
			if s.addOneOrderedIndexPath(rel, tbl, idx, colExprs, relPages, relTuples, totalPages) {
				added = true
			}
		}
		if added {
			// A new unparameterised path can change `CheapestTotal` and
			// `CheapestStartup`, unlike the parameterised ones — hence the
			// same re-run `addParameterizedIndexPaths` does, for a stronger
			// reason.
			setCheapest(rel)
		}
	}
}

// addOneOrderedIndexPath builds the ordering-only path for one index, or
// declines. Returns whether a path was added.
func (s *searchCtx) addOneOrderedIndexPath(rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index, colExprs map[string]Expr, relPages int64, relTuples, totalPages float64) bool {
	// `index_is_ordered = (index->sortopfamily != NULL)` (indxpath.c:915). A
	// non-orderable AM has no ordering to offer, and for goopg that includes a
	// `USING hash` index, which rides the B-tree substrate but is not an
	// ordered AM in PG.
	if !indexIsOrderable(idx) {
		return false
	}
	// A partial index. PG only ever reaches `build_index_paths` for an index
	// whose predicate was PROVEN from the query's restriction clauses
	// (`check_index_predicates` sets `index->predOK`; an unproven partial
	// index is skipped in `create_index_paths`). goopg has no
	// predicate-implication prover, so the honest answer is to decline rather
	// than to emit a path that would silently drop rows. Ledgered.
	if idx.HasPredicate {
		return false
	}

	// Steps 2 and 2b of `build_index_paths`, fused — see the note below on why
	// the fusion is exact rather than a shortcut. The scan direction those keys
	// describe comes back with them (M0127-P5.5-a, pathindexcarrier.go); PG's
	// backward arm (indxpath.c:1010-1050) would be a second call here with
	// `true`, and is ledgered rather than written because goopg's executor
	// `IndexScan` has no direction to set.
	keys, dir := indexPathOrdering(idx, colExprs, false)
	if len(keys) == 0 {
		return false
	}

	// Step 4's condition, with `index_clauses == NIL` (the search's clause
	// list holds JOIN clauses; a clause used as an index qual is what makes a
	// path parameterised, and that path is P5.4b-ii-a's) and
	// `index_only_scan == false` (goopg's search has no visibility-map model,
	// so `check_index_only`'s answer is always no). What is left is
	// `useful_pathkeys != NIL`, which the non-empty `keys` above just
	// established.
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	in := indexScanInputs{
		relPages:    relPages,
		relTuples:   relTuples,
		indexPages:  indexPages,
		indexTuples: indexTuples,
		treeHeight:  treeHeight,
		// No index quals, so the index admits the whole heap.
		selectivity: 1.0,
		correlation: indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),

		totalTablePages: totalPages,
	}
	cost := costIndexScan(s.cp, in)
	// take2 P4-01 Slice 1: the scan Target, computed from NeededCols at
	// path-creation time. Assert-only — never applied, never costed.
	tgt, tgtKnown := scanPathTarget(rel)
	serial := &Path{
		Kind: PathIndexScan,
		Rel:  rel,
		// create_index_path (pathnode.c:1078): `rel->consider_parallel`. C-19a.
		ParallelSafe: rel.ParallelSafeForPath(),
		// B-17d: `cost_index`'s own flag (costsize.c:560), on top of nothing
		// (no input). The producer always runs.
		DisabledNodes: disabledNodesFor(!s.cp.enableIndexScan),
		// `path->rows = baserel->rows` (cost_index's unparameterised arm):
		// the rel's own post-restriction estimate, NOT the pre-restriction
		// `relTuples` the cost was charged over. The scan reads every tuple
		// and the leaf node's quals then discard most of them; the rows that
		// emerge are the rel's.
		Rows:     rel.Rows,
		Cost:     cost,
		Pathkeys: keys,
		// `IndexPath.indexinfo` / `indexscandir` (pathnodes.h:1845/1849). This
		// path is a FULL scan of `idx` — no quals were pushed into it, which is
		// exactly what PG's "an empty indexclauses list implies a full index
		// scan" (pathnodes.h:1817) describes — so naming the index is the whole
		// of what P5.5's createPlan needs to rebuild it.
		IndexInfo:    idx,
		IndexScanDir: dir,
		// `IndexPath.indexclauses` stays EMPTY, which is a statement and not an
		// omission: "an empty indexclauses list implies a full index scan"
		// (pathnodes.h:1817) is exactly the object this arm builds
		// (M0127-P5.5-b, pathindexclauses.go).
		//
		// Empty, and that is the entire point of the slice.
		RequiredOuter: 0,
		Target:        tgt,
		TargetKnown:   tgtKnown,
	}
	addPath(rel, serial, "index.ordered")

	// C-19c: the PARTIAL twin — build_index_paths' "if (index->amcanparallel
	// && rel->consider_parallel && outer_relids == NULL && scantype !=
	// ST_BITMAPSCAN)" arm (indxpath.c:1039-1062): the same path with
	// `partial_path = true`, whose cost_index sizes the workers and, when
	// there are none worth having, is freed. Into PartialPathlist, which
	// nothing consumes before C-19d: the serial arm is unchanged by
	// construction.
	s.addPartialIndexPath(rel, tbl, serial, in, "index.ordered.partial")
	return true
}

// addPartialIndexPath offers the partial twin of a plain or index-only index
// path just added to `rel.Pathlist` — C-19c, `create_index_path(...,
// partial_path = true)` + `add_partial_path`. `serial` is that path and `in`
// its costing input, so the twin is the serial path's shape (index, direction,
// pathkeys, index-only column list, target) priced on exactly the serial
// path's model with the divisor applied (costPartialIndexScan). It shares no
// pointer with the serial path: `Pathkeys` and the covered list are read-only
// after creation, and nothing about the twin is mutated later.
//
// The gate is upstream's: `amcanparallel` (btree only — a `USING hash` index
// rides goopg's btree substrate but is not parallel-capable in PG either),
// `rel->consider_parallel`, no required outer (every caller here is
// unparameterised), and the session's parallelModeOK. A twin sized at zero
// workers is not offered ("just free it").
func (s *searchCtx) addPartialIndexPath(rel *RelOptInfo, tbl *catalog.Table, serial *Path, in indexScanInputs, producer string) {
	if s == nil || !s.parallelModeOK || rel == nil || !rel.ConsiderParallel || serial == nil {
		return
	}
	idx := serial.IndexInfo
	if idx == nil || !isBTreeIndex(idx) || idx.DeclaredHash || serial.RequiredOuter != 0 {
		return
	}
	cost, rows, workers := costPartialIndexScan(s.cp, in, serial.Rows, tableParallelWorkersReloption(tbl))
	if workers <= 0 {
		return
	}
	twin := *serial
	twin.Cost = cost
	twin.Rows = rows
	// A partial path is parallel-safe by construction (`parallel_safe =
	// rel->consider_parallel`, checked above).
	twin.ParallelSafe = true
	twin.ParallelWorkers = workers
	addPartialPath(rel, &twin, producer)
}

// mergeableColumnExprsFor is `pathkeys_useful_for_merging` (pathkeys.c:2166)
// turned inside out: instead of truncating a finished pathkey list against the
// rel's join clauses, it collects the columns those clauses make useful and
// lets `buildIndexPathkeys` stop at the first index column not among them.
//
// The two formulations agree exactly, and the reason is worth pinning because
// it is not obvious. PG scans its pathkey list left to right and BREAKS at the
// first key with no mergejoinable clause (`else break`, pathkeys.c:2213) —
// additional sort-key positions past an unusable one are useless, since a
// merge join can only use a prefix. `buildIndexPathkeys` scans the index's
// columns left to right and breaks at the first column absent from the map
// (PG's own STOP-not-skip rule for an unusable index column,
// pathkeys.c:815-822). Feeding it a map whose keys are exactly the
// merge-useful columns makes the two breaks the same break.
//
// goopg cannot separate the steps even if it wanted to: its pathkeys are
// SYNTACTIC, so a pathkey must BE a `*ColumnRef` the query's clauses carry —
// there is no equivalence-class object to name a column that no clause
// mentions. That is also why the map's values are the clause's own operand
// rather than a freshly built `ColumnRef` with the same name: a re-synthesised
// one has a different `Index`/`SourceTableIdx` and `exprEqual` reads it as a
// different column (the P5.4c-ii-a finding).
//
// Two of PG's conditions collapse at level 1 and are therefore not tested for:
//
//   - `eclass_useful_for_merging`'s "members not yet joined to the rel" — at
//     level 1 nothing is joined to anything, so every join clause of the rel
//     still has an unjoined member;
//   - `right_merge_direction` (pathkeys.c:2262), which compares the pathkey's
//     direction against `query_pathkeys` and passes anything when there are
//     none. The search boundary has no query pathkeys (03 §10).
//
// Only equijoins qualify, which is PG's `mergeopfamilies != NIL`: a
// non-equality join clause (`a.x < b.y`) is a join clause but not a
// mergeclause, and no ordering helps it.
func mergeableColumnExprsFor(relids RelSet, clauses []*restrictInfo) map[string]Expr {
	out := make(map[string]Expr)
	for _, ri := range clauses {
		if ri == nil || !ri.isEquijoin {
			continue
		}
		var key Expr
		switch {
		case ri.leftRelids == relids:
			key = ri.leftKey
		case ri.rightRelids == relids:
			key = ri.rightKey
		default:
			continue
		}
		col, isCol := key.(*ColumnRef)
		if !isCol || col.Name == "" {
			continue
		}
		// First clause per column wins, matching
		// `addOneParameterizedIndexPath`'s rule: which of two clauses on the
		// same column supplies the expression cannot matter for an ordering
		// (both name the same column of the same rel), so the deterministic
		// choice is the one that keeps path generation reproducible.
		if _, dup := out[col.Name]; dup {
			continue
		}
		out[col.Name] = col
	}
	return out
}

// totalTablePages is `root->total_table_pages` (set by `set_base_rel_sizes`,
// allpaths.c): the summed page count of every base relation in the query,
// which `index_pages_fetched` pro-rates the cache budget between. Leaves with
// no `catalog.Table` (a subquery, a VALUES list) contribute nothing, exactly
// as PG's loop skips non-RTE_RELATION entries.
func (s *searchCtx) totalTablePages() float64 {
	total := 0.0
	for i := range s.relInfos {
		tbl := s.relInfos[i].table
		if tbl == nil {
			continue
		}
		rows := float64(s.relInfos[i].baseRows)
		if rows < 1 {
			rows = 1
		}
		total += float64(baseRelPages(tbl, rows))
	}
	return total
}

// baseRelPages is `baserel->pages`: the relation's physical size in blocks.
//
// ANALYZE's recorded page count is the real thing and wins when present — it
// is literally `pg_class.relpages`. Without it the count is derived from the
// row estimate and the declared tuple width, the same arithmetic
// `buildInitialRels` prices its sequential scan with, so the two rivals in one
// `addPath` comparison at least share a page model when neither has statistics.
func baseRelPages(tbl *catalog.Table, relTuples float64) int64 {
	if tbl != nil && tbl.Stats != nil && tbl.Stats.Pages > 0 {
		return int64(tbl.Stats.Pages)
	}
	return estScanPages(relTuples, tableDataWidth(tbl)+perTupleOverhead)
}

// leadingKeyStats resolves the statistics of the index's LEADING key column,
// which is the only column `btcost_correlation` (selfuncs.c:7305) looks at.
// nil when the index leads with an expression or the table was never analysed.
func leadingKeyStats(idx *catalog.Index, tbl *catalog.Table) *catalog.ColumnStats {
	if idx == nil || len(idx.Columns) == 0 || idx.Columns[0] == "" {
		return nil
	}
	return columnStatsByName(tbl, idx.Columns[0])
}
