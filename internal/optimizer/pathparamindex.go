package optimizer

// M0127-P5.4b-ii-a — parameterised base index paths: the half of PG's level-1
// population the M0127-P5.1 ledger row deferred, and the thing without which
// nothing in the search can be parameterised at all.
//
// PG oracle: `create_index_paths` (indxpath.c:235) and its join half
// `consider_index_join_clauses` / `get_join_index_paths` (:446-:544);
// `get_baserel_parampathinfo` (relnode.c:1550) for the ParamPathInfo;
// `get_parameterized_baserel_size` (costsize.c:5379) for `ppi_rows`;
// `var_eq_non_const` (selfuncs.c) for the per-clause selectivity that sizing
// reduces to. Design: leftdeep-joins 03 §5.2 (the NLI inner), 03 §9 (the
// discipline these paths are the first citizens of).
//
// Why this is a slice of its own, ahead of the NLI arm it exists to feed
// (P5.4b-ii-b): the arm iterates `inner.CheapestParameterized`, and that list
// is empty for every rel in every query until something puts a `RequiredOuter`
// path into a pathlist. The discipline that governs such a path landed first
// (P5.4b-i, `pathparam.go`); this is the first path that exercises it, and the
// arm that consumes it is the last of the three. Each step is separately
// falsifiable, which a single combined change would not be.
//
// Live since M0127-P5.9 (2026-08-06): `addParameterizedIndexPaths` is reached
// from `searchOneProblem` via `addBaseRelIndexPaths` (pathindexordered.go:49)
// and `GOOPG_PGSHAPED_DP` defaults ON, so these paths DO compete for
// production plans. Validated by `pathparamindex_test.go`, no longer in
// isolation.

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
)

// paramIndexClause is one join clause that could drive an index probe of a base
// relation: an equality whose INNER operand is a bare column of that relation
// and whose OUTER operand is computable entirely outside it.
//
// `outerRels` is the parameterisation this clause would impose — PG's
// `clause_relids` minus the rel itself (indxpath.c:519), which for goopg's
// two-sided operand split is simply the other operand's relids.
type paramIndexClause struct {
	ri       *restrictInfo
	innerCol string // the base rel's column name; matched against Index.Columns
	// innerKey is the `*ColumnRef` `innerCol` was read off. Kept beside the
	// name because `buildIndexPathkeys` (P5.4c-ii-a) needs the very expression
	// the query's clauses carry — goopg's pathkeys are syntactic, so a
	// re-synthesised ColumnRef with a different `Index`/`SourceTableIdx` would
	// read as a different column under `exprEqual`.
	innerKey  Expr
	outerKey  Expr // the value the outer side supplies for the probe
	outerRels RelSet
}

// indexableJoinClausesFor selects, from the search's whole clause list, the
// clauses that could parameterise `relids`.
//
// The test has three parts, and each rejects a shape that would otherwise
// produce an index path the executor cannot build:
//
//   - the clause must be an equijoin (`isEquijoin`), because only an equality
//     has the two-sided operand split a probe key is read off;
//   - ONE operand's relids must be exactly `relids` — a single-rel operand of
//     this rel. `a.x = b.y + c.z` at rel {b} fails here: the operand containing
//     `b` also contains `c`, so no value of `b`'s column is being equated;
//   - that operand must be a bare `*ColumnRef`, since `Index.Columns` names
//     columns and an expression index is not a thing goopg's catalog has.
//
// The outer operand needs no disjointness check of its own: `isEquijoin` is set
// only when the two operand relsets are disjoint (joinrestrict.go), so an
// operand equal to `relids` guarantees the other side is outside it.
func indexableJoinClausesFor(relids RelSet, clauses []*restrictInfo) []paramIndexClause {
	var out []paramIndexClause
	for _, ri := range clauses {
		if ri == nil || !ri.isEquijoin {
			continue
		}
		var innerKey, outerKey Expr
		var outerRels RelSet
		switch {
		case ri.leftRelids == relids:
			innerKey, outerKey, outerRels = ri.leftKey, ri.rightKey, ri.rightRelids
		case ri.rightRelids == relids:
			innerKey, outerKey, outerRels = ri.rightKey, ri.leftKey, ri.leftRelids
		default:
			continue
		}
		col, isCol := innerKey.(*ColumnRef)
		if !isCol || col.Name == "" || outerKey == nil || outerRels == 0 {
			continue
		}
		out = append(out, paramIndexClause{
			ri:        ri,
			innerCol:  col.Name,
			innerKey:  col,
			outerKey:  outerKey,
			outerRels: outerRels,
		})
	}
	return out
}

// consideredParameterizations is PG's `considered_relids` list, built by
// `consider_index_join_outer_rels` (indxpath.c:503-591): every candidate outer
// relset worth offering the index, which is each clause's own outer relids PLUS
// the union of that clause's relids with each set already considered.
//
// The unions are what make a COMPOSITE index reachable from a join. A
// two-column index whose columns are equated to two DIFFERENT outer rels — the
// composite-FK shape, `lineitem(l_partkey, l_suppkey)` probed from `part` and
// `supplier` — has no single clause that binds it, so a singleton-only list
// offers the index two half-bound key sets and
// `pickIndexCoveringLeadingPrefix` correctly declines both. Only the union
// {part, supplier} binds the whole key. This half was ledgered against
// P5.4b-ii-b, deliberately: until the NLI arm existed to consume a
// parameterised path, the extra sets would have been generated, priced, and
// never read. It lands with the arm.
//
// Three details of PG's loop are reproduced because each is load-bearing:
//
//   - The unions are generated against a SNAPSHOT of the list (PG's
//     `num_considered_relids`, :540), not against the growing list. Unions of
//     unions are already covered by pairing the newest clause with each
//     earlier singleton, and iterating the live list would compound
//     exponentially.
//   - A pair where one set contains the other is skipped (`bms_subset_compare
//     != BMS_DIFFERENT`, :552): the union is then just the larger set, already
//     present.
//   - The equivalence-class skip (:562, `eclassAlreadyUsed` below).
//   - The `10 * considered_clauses` valve (:571): PG stops COMBINING once the
//     list outgrows the number of clauses that produced it, but still offers
//     each clause's own set. goopg's ceiling of 16 relations makes the valve
//     nearly unreachable, but reproducing it keeps the path count bounded by
//     the same arithmetic PG's is.
//
// The order is PG's — for each clause, its unions with earlier sets first, then
// the clause's own set — so path generation stays deterministic.
//
// Representation note: PG's `considered_relids` entries INCLUDE the indexed rel
// itself (they are maximal `clause_relids` sets), while goopg's carry only the
// outer part. The two differ by the same constant rel in every entry, so subset
// and union comparisons agree; keeping the rel out is what lets the result be
// used directly as a `RequiredOuter`.
func consideredParameterizations(cands []paramIndexClause) []RelSet {
	var out []RelSet
	seen := make(map[RelSet]bool, len(cands))
	add := func(rels RelSet) {
		if rels == 0 || seen[rels] {
			return
		}
		seen[rels] = true
		out = append(out, rels)
	}
	for n, c := range cands {
		if seen[c.outerRels] {
			continue
		}
		snapshot := len(out)
		for pos := 0; pos < snapshot; pos++ {
			old := out[pos]
			if relsSubset(c.outerRels, old) || relsSubset(old, c.outerRels) {
				continue
			}
			if eclassAlreadyUsed(c.ri.ecID, old, cands) {
				continue
			}
			// `n+1` clauses have been considered by the time this one is
			// processed, mirroring PG's running `considered_clauses`.
			if len(out) >= 10*(n+1) {
				break
			}
			add(c.outerRels | old)
		}
		add(c.outerRels)
	}
	return out
}

// eclassAlreadyUsed is `eclass_already_used` (indxpath.c:600): would combining
// this clause with `oldRels` produce a usefully different parameterisation, or
// is some OTHER clause from the same equivalence class already usable at
// `oldRels`?
//
// The case it prevents: `a.x = c.x` and `b.x = c.x` are one equivalence class,
// so at a parameterisation of {a} the rel `c` is already probed on column `x`.
// Offering {a,b} as well would generate a second path with a strictly larger
// `RequiredOuter` and the same index key set — dominated, but only after being
// built and priced, and a needless extra parameterisation for every join level
// above to consider.
func eclassAlreadyUsed(ecID int, oldRels RelSet, cands []paramIndexClause) bool {
	if ecID == noEquivClass {
		return false
	}
	for _, c := range cands {
		if c.ri != nil && c.ri.ecID == ecID && relsSubset(c.outerRels, oldRels) {
			return true
		}
	}
	return false
}

// addParameterizedIndexPaths is `create_index_paths`' join half for every base
// relation in the search: for each candidate parameterisation, the clauses
// movable into the rel under it, the longest B-tree index they fully cover, and
// one path priced and sized for ONE execution with that parameter bound.
//
// It is a step of its own rather than part of `buildInitialRels` because of
// PG's own ordering: `set_base_rel_pathlists` (allpaths.c:191) runs after
// `deconstruct_jointree` has built the `joininfo` lists it reads, and goopg's
// clause list (P5.2) is likewise built after the initial rels. The protocol is
// therefore three steps — `buildInitialRels`, this, then `joinSearch` — and
// this one is optional in exactly the case where it has nothing to say: a
// caller with no catalog (every unit test of the enumerator) gets no index
// paths and an unchanged search.
//
// `setCheapest` is re-run on every rel that gained a path, because
// `joinSearch`'s invariant is that every initial rel already has its cheapest
// slots filled, and a new parameterised path changes `CheapestParameterized`
// even though it can never change `CheapestTotal` (03 §9 rule 1).
func (s *searchCtx) addParameterizedIndexPaths(cat catalog.Catalog) {
	if s == nil || cat == nil || s.clauses == nil || len(s.clauses.all) == 0 {
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
		// The CONSUMER's eligibility test, applied at the producer
		// (M0127-P5.5-c): a leaf `createPlan` cannot rebuild as an `*IndexScan`
		// must not have an index path costed over it either, or the DP prices a
		// plan the builder then refuses. One predicate, two callers, no drift
		// (rule #2; createplanindex.go).
		if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
			continue
		}
		// And the NLI arm's stricter half (M0127-P5.5-e-ii-b): the ONLY consumer
		// of a parameterised index path is `createNestLoopIndexJoinPlan`, whose
		// `NestedLoopIndexJoin.Inner` is typed `*IndexScan` and so cannot carry
		// the `*Filter` wrappers a leaf's local quals live in. Costing a path
		// here whose only consumer must refuse it would let the DP choose a plan
		// the builder cannot honour — so the producer applies the consumer's own
		// rule (rule #2).
		//
		// That rule used to be `scanLeafIsBare`: no wrappers at all. It is now
		// `absorbableLeafCond`, because the quals have somewhere to go —
		// `IndexScan.Cond`, PG's `Filter:` alongside `Index Cond:` on one node.
		// The bare rule declined every filtered relation, which is precisely the
		// shape PG parameterises in TPC-H (Q19's `lineitem` behind
		// `l_shipmode`/`l_shipinstruct`, Q3's behind `l_shipdate`), so goopg
		// could not reach those plans at any cost setting. A leaf whose wrappers
		// are NOT leaf-local is still declined — see `absorbableLeafCond` for
		// why that one is a coordinates question and not a shape question.
		//
		// Note the row estimate needs no adjustment for the absorbed quals:
		// `parameterizedBaserelRows` multiplies `rel.Rows`, which already
		// carries the leaf's baserestrict selectivity (see its comment), so the
		// local quals are priced exactly once whether they sit in a wrapper or
		// in `Cond`.
		if _, _, ok := absorbableLeafCond(rel.baseLeaf); !ok {
			continue
		}
		cands := indexableJoinClausesFor(rel.Relids, s.clauses.all)
		if len(cands) == 0 {
			continue
		}
		// `baserel->tuples` — the PRE-restriction count `cost_index` charges
		// over, and the count `get_variable_numdistinct` scales a fractional
		// distinct estimate by. Not `rel.Rows`, which is post-restriction.
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			// Not always populated (no catalog estimate; every enumerator unit
			// test). Falling back to the rel's own row count keeps this a real
			// relation size instead of 1, which would make
			// `get_variable_numdistinct` scale a fractional distinct estimate
			// by nothing and hand `cost_index` a one-row table.
			relTuples = rel.Rows
		}
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		added := false
		for _, req := range consideredParameterizations(cands) {
			if s.addOneParameterizedIndexPath(rel, tbl, cat, cands, req, relPages, relTuples, totalPages) {
				added = true
			}
		}
		if added {
			setCheapest(rel)
		}
	}
}

// addOneParameterizedIndexPath is `get_join_index_paths` (indxpath.c:93) for a
// single candidate parameterisation `req`: every candidate clause whose outer
// operand is computable within `req` is movable into the rel under it
// (`join_clause_is_movable_into`, relnode.c:1580), so they are collected
// together and offered to the index as one key set.
//
// Returns whether a path was added.
func (s *searchCtx) addOneParameterizedIndexPath(rel *RelOptInfo, tbl *catalog.Table, cat catalog.Catalog, cands []paramIndexClause, req RelSet, relPages int64, relTuples, totalPages float64) bool {
	innerToOuter := make(map[string]Expr, len(cands))
	// The inner-side operand of the same clauses, keyed the same way — the
	// column expressions `buildIndexPathkeys` names the index's key columns
	// with (P5.4c-ii-a).
	innerExprs := make(map[string]Expr, len(cands))
	var bound []paramIndexClause
	for _, c := range cands {
		if !relsSubset(c.outerRels, req) {
			continue
		}
		// First clause per column wins, so a rel equated to the same outer
		// column twice does not double-count its selectivity below.
		if _, dup := innerToOuter[c.innerCol]; dup {
			continue
		}
		innerToOuter[c.innerCol] = c.outerKey
		innerExprs[c.innerCol] = c.innerKey
		bound = append(bound, c)
	}
	if len(bound) == 0 {
		return false
	}
	// The SAME index-eligibility rule the NLI constructor applies
	// (`pickIndexCoveringLeadingPrefix`, nl_index_join.go): an index
	// qualifies when its LEADING column is bound, and the probe binds the
	// gapless prefix that is bound from there — PG's `amoptionalkey` rule
	// (indxpath.c:1029-1076). Sharing the function is what keeps path
	// generation from costing an index the constructor would decline — the
	// first half of 03 §5.2's binding contract; the second half (sharing
	// `tryBuildNLI`'s whole gauntlet) belongs to the arm that builds the
	// join, P5.4b-ii-b.
	//
	// Requiring TOTAL coverage here is what made goopg choose a Bitmap Heap
	// Scan for TPC-H Q8 where PG chooses `Index Scan using
	// lineitem_part_supp_fkidx`: with only `l_partkey` bound, this producer
	// declined and the bitmap producer — which never had the restriction —
	// was the only candidate left. It was never a costing difference. At
	// every parameterisation where both produced a path, the index path was
	// already cheaper and `addPath` already discarded the bitmap.
	idx, _ := pickIndexCoveringLeadingPrefix(cat, tbl, innerToOuter)
	if idx == nil {
		return false
	}
	// The probe binds a gapless leading prefix, which may be SHORTER than
	// `bound`: a clause on a column past the first unbound one is not an index
	// qual at all. Splitting the two selectivities is PG's own split — the
	// index quals drive `cost_index`'s page estimate (`indexSelectivity`),
	// while `ppi_rows` counts every movable clause because the ones that are
	// not index quals still filter (`get_parameterized_baserel_size`,
	// costsize.c:5379). Sharing one number would either over-charge the probe
	// or under-count the filter.
	clauses := indexPathClauses(idx, bound)
	probed := boundPrefixClauses(bound, clauses)
	fullyBound := len(clauses) == len(idx.Columns)
	sel := parameterizedIndexSelectivity(tbl, idx, probed, relTuples, fullyBound)
	rows := parameterizedBaserelRows(rel, idx, parameterizedIndexSelectivity(tbl, idx, bound, relTuples, fullyBound), fullyBound)
	// The index's own ordering (`build_index_pathkeys`, pathkeys.c:740) AND the
	// direction it was computed for, taken together so they cannot disagree
	// (M0127-P5.5-a, pathindexcarrier.go). PG passes the same `useful_pathkeys`
	// to the parameterised path that it passes to the plain one
	// (`build_index_paths`, indxpath.c:750-800), so this is not a special case
	// for parameterisation — it is simply the only index path goopg builds
	// today. Forward scan only: goopg's index scan has no backward mode to
	// select, and PG's own `pathkeys_possibly_useful` gate needs
	// `query_pathkeys`, which this seam does not have (03 §10). Both ledgered.
	keys, dir := indexPathOrdering(idx, innerExprs, false)
	// `cost_index(path, root, loop_count, false)` (costsize.c:520) with the
	// loop count `create_index_path` takes from `get_loop_count` — the SAME
	// scan model the unparameterised ordered path uses (pathindexordered.go),
	// not a second one.
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	cost := costIndexScan(s.cp, indexScanInputs{
		relPages:        relPages,
		relTuples:       relTuples,
		indexPages:      indexPages,
		indexTuples:     indexTuples,
		treeHeight:      treeHeight,
		selectivity:     sel,
		// take2 P2-09: a unique index bound by equality on every key column
		// yields at most one tuple. `fullyBound` is exactly PG's "equality on
		// every key column" precondition — it is what parameterizedIndexSelectivity
		// already takes for the same reason.
		uniqueEqualityOnAllKeys: idx != nil && idx.Unique && fullyBound,
		correlation:             indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages: totalPages,
		loopCount:       s.loopCountFor(req),
	})
	addPath(rel, &Path{
		Kind:     PathIndexScan,
		Rel:      rel,
		Rows:     rows,
		Cost:     cost,
		Pathkeys: keys,
		// `IndexPath.indexinfo` / `indexscandir` (pathnodes.h:1845/1849): the
		// index this path's cost and rows were computed FOR, named so P5.5's
		// createPlan can re-emit exactly this probe rather than re-deriving one.
		IndexInfo:    idx,
		IndexScanDir: dir,
		// `IndexPath.indexclauses` (pathnodes.h:1846), re-presented in
		// INDEX-COLUMN order by `indexPathClauses` (M0127-P5.5-b). `bound` is in
		// the search's candidate order and `IndexScan.Keys[i]` binds
		// `Index.Columns[i]` positionally, so the reordering is the difference
		// between the right answer and a probe that compares the wrong pair of
		// columns. Never nil here: `pickIndexCoveringLeadingPrefix` accepted
		// `idx` only because every one of its columns is bound.
		IndexClauses:  clauses,
		RequiredOuter: req,
	}, "index.parameterised")
	return true
}

// parameterizedIndexSelectivity is `indexSelectivity` for the clauses bound
// into this probe: the fraction of the relation the INDEX QUALS alone admit
// (PG computes it in `btcostestimate` via `clauselist_selectivity` over
// `indexQuals`). Index quals only — the relation's local quals are not in it,
// because `cost_index` charges over `baserel->tuples` and including them would
// count them twice.
func parameterizedIndexSelectivity(tbl *catalog.Table, idx *catalog.Index, bound []paramIndexClause, relTuples float64, fullyBound bool) float64 {
	// PG's `vardata->isunique` short circuit (`var_eq_non_const`, selfuncs.c):
	// when the equated column matched a unique index, assume exactly one match
	// regardless of what the statistics say.
	//
	// `fullyBound` is the precondition, and it is a PARAMETER because it
	// stopped being a guarantee when the producer began emitting prefix
	// probes. Uniqueness of an index on (a, b) says nothing about how many
	// rows share one value of `a` — asserting one row for a prefix probe would
	// under-count `lineitem_part_supp_fkidx` bound on `l_partkey` alone by the
	// ~30 lineitems per part, and it is the ROW estimate that decides whether
	// the nested loop above is affordable.
	if idx.Unique && fullyBound {
		if relTuples <= 1 {
			return 1
		}
		return 1 / relTuples
	}
	sel := 1.0
	for _, c := range bound {
		sel *= varEqNonConstSelectivity(columnStatsByName(tbl, c.innerCol), relTuples)
	}
	if sel > 1 {
		return 1
	}
	if sel < 0 {
		return 0
	}
	return sel
}

// parameterizedBaserelRows is `get_parameterized_baserel_size`
// (costsize.c:5379): the rows ONE execution of the parameterised scan returns.
//
// PG computes `rel->tuples * clauselist_selectivity(param_clauses u
// baserestrictinfo)` and caps at `rel->rows`. goopg's `rel.Rows` already
// carries the baserestrict selectivity (the local quals live inside the leaf
// node and their effect is inside `initialRelRows`, joinsearch.go:236), so the
// same product is `rel.Rows * selectivity(param_clauses)` — the two forms are
// equal, and this one cannot double-count the local quals.
//
// PG's floor is `clamp_row_est`: a scan that returns "0.3 rows" still costs a
// probe, and a zero would make every join above it free.
func parameterizedBaserelRows(rel *RelOptInfo, idx *catalog.Index, sel float64, fullyBound bool) float64 {
	// The uniqueness short-circuit is on the ROWS side and not merely inherited
	// from the selectivity: `parameterizedIndexSelectivity` expresses "one
	// tuple" as `1/relTuples`, which reproduces one row only when `rel.Rows`
	// and `relTuples` agree. They need not — `rel.Rows` is post-restriction —
	// and a unique index FULLY bound by equality returns at most one row by
	// definition of the constraint, whatever the restriction did. A prefix
	// probe has no such guarantee — see `parameterizedIndexSelectivity`.
	if idx != nil && idx.Unique && fullyBound {
		return 1
	}
	rows := rel.Rows * sel
	if rows > rel.Rows {
		rows = rel.Rows
	}
	if rows < 1 || math.IsNaN(rows) {
		return 1
	}
	return rows
}

// loopCountFor is `get_loop_count` (indxpath.c:3266): how many times a scan
// parameterised by `req` is expected to be re-executed.
//
// PG's rule is the SMALLEST row count among the relations supplying the
// parameter — not the product and not the sum. Worth keeping exactly, because
// `cost_index`'s repeated-scan arm DIVIDES by this number, so an inflated one
// would make a parameterised inner look free.
func (s *searchCtx) loopCountFor(req RelSet) float64 {
	if req == 0 {
		return 1
	}
	result := 0.0
	for _, rel := range s.levelRels(1) {
		if rel == nil || rel.Relids&req == 0 || rel.Rows <= 0 {
			continue
		}
		if result == 0 || rel.Rows < result {
			result = rel.Rows
		}
	}
	if result > 0 {
		return result
	}
	return 1
}

// varEqNonConstSelectivity is `var_eq_non_const` (selfuncs.c) for the case this
// slice needs: `column = <value not known at plan time>`.
//
// PG's reasoning, reproduced because it is not the obvious formula: the probe
// value is unknown, so the estimate is averaged over all possible values —
// non-null fraction divided by the number of distinct values — rather than read
// off the MCV list, which would only be right if the probe value were the most
// common one. The MCV cross-check that follows is the other half of that
// argument: the average can never exceed the most common value's own frequency.
//
// With no statistics PG falls back to `get_variable_numdistinct`'s default
// (DEFAULT_NUM_DISTINCT = 200, selfuncs.h), which is what the zero-stats branch
// returns here — through the package-level `defaultNumDistinct`
// (joinselectivity.go), so this arm and the join estimator's cannot drift apart.
func varEqNonConstSelectivity(stats *catalog.ColumnStats, relRows float64) float64 {
	if stats == nil {
		return 1.0 / defaultNumDistinct
	}
	sel := 1.0 - stats.NullFrac
	if nd := variableNumDistinct(stats, relRows); nd > 1 {
		sel /= nd
	}
	if len(stats.MCV) > 0 && stats.MCV[0].Frequency > 0 && sel > stats.MCV[0].Frequency {
		sel = stats.MCV[0].Frequency
	}
	if sel < 0 {
		return 0
	}
	if sel > 1 {
		return 1
	}
	return sel
}

// variableNumDistinct is `get_variable_numdistinct` (selfuncs.c:6341) for the
// case this file needs, and it is a THIN WRAPPER on purpose.
//
// The reduction it performs — absolute count, else fraction times relation
// size, else the small-relation/default split — had already been written and
// centralised in `getVariableNumDistinct` (joinselectivity.go). An earlier
// version of this function open-coded it again, which made it the FOURTH copy
// (`catalog.go`'s `StaDistinct` comment names the three) and, worse, a copy
// that disagreed: it returned the absolute `NDistinct` whenever that was
// positive, while `StaDistinct()` applies PG's analyze.c rule that the FRACTION
// wins once it exceeds 0.1, absolute count or not. ANALYZE sets both fields, so
// for any column with `NDistinctFrac > 0.1` the join estimator and this one
// would have returned different distinct counts for the same column in the
// same plan — the exact sibling-path divergence this repo has been bitten by
// repeatedly, and silent because both answers are plausible.
//
// So the only thing left here is the shape conversion.
func variableNumDistinct(stats *catalog.ColumnStats, relRows float64) float64 {
	nd, _ := getVariableNumDistinct(joinVarStats{stats: stats, tuples: relRows})
	return nd
}

// columnStatsByName resolves a column name to its ANALYZE statistics, or nil
// when the table has never been analysed or the name is not one of its columns.
// `Table.Stats.Columns` is positional over `Table.Columns`, so the lookup is by
// ordinal rather than by a second name index.
func columnStatsByName(tbl *catalog.Table, name string) *catalog.ColumnStats {
	if tbl == nil || tbl.Stats == nil {
		return nil
	}
	for i := range tbl.Columns {
		if tbl.Columns[i].Name != name {
			continue
		}
		if i >= len(tbl.Stats.Columns) {
			return nil
		}
		return &tbl.Stats.Columns[i]
	}
	return nil
}

// boundPrefixClauses narrows `bound` to just the clauses the probe actually
// binds, named by the index-column list `indexPathClauses` produced. The
// remainder are movable clauses that could not become index quals; they still
// filter, so they belong to the row estimate but not to the probe's page cost.
func boundPrefixClauses(bound []paramIndexClause, clauses []indexPathClause) []paramIndexClause {
	if len(clauses) >= len(bound) {
		return bound
	}
	keep := make([]paramIndexClause, 0, len(clauses))
	for _, ipc := range clauses {
		for _, c := range bound {
			if c.ri == ipc.ri {
				keep = append(keep, c)
				break
			}
		}
	}
	return keep
}
