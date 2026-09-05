package optimizer

// M0127-P5.1 — the join search's level lists, its relset map, and
// buildInitialRels: the substrate `joinSearchOneLevel` (P5.3) enumerates over.
//
// PG oracle: `standard_join_search` (allpaths.c:3457) owns
// `root->join_rel_level[]`, a level-indexed array of Lists where entry `lev`
// holds every RelOptInfo whose relid set has exactly `lev` base rels
// (:3475-3496); `build_join_rel` (relnode.c:696) resolves a relset to its
// RelOptInfo through `root->join_rel_hash` / `join_rel_list`, creating the rel
// on a miss. This file reproduces both halves — the level lists and the lookup
// beside them — plus PG's level-1 population (`set_base_rel_pathlists`,
// allpaths.c:191) at goopg's fidelity level. Design: leftdeep-joins 03 §1-§2.
//
// NOTHING here was called from `planSelect` when this file landed — the
// wiring arrived at P5.9-b (joinsearchseam.go) and the search has been ON by
// default since P5.9 (`GOOPG_PGSHAPED_DP`, 08 §2), so everything here moves
// plans. The file's original validation vehicle was `joinsearch_test.go`.
//
// What it DOES settle is the leaf-whitelist gap. `tryBushyDP` abandons the
// entire search when any FROM item is not a `*SeqScan` / `*IndexScan`
// (bushy.go's leaf whitelist) — a single subquery, CTE or VALUES item
// disables join reordering for the whole statement (ledger rows M0125-0034 /
// -0036, and M0125-0037 stage (ii)). `buildInitialRels` admits every FROM item
// instead: each becomes one initial rel whose single path carries the leaf the
// pre-search pipeline already produced.

import (
	"fmt"
	"math/bits"
	"os"

	"github.com/goopg/goopg/internal/catalog"
)

// maxSearchRels is the relset width. `RelSet` is a uint16 (path.go:29), so the
// search can address 16 base relations — above the level the collapse limits
// admit (03 §7), and above the old DP's own 12-table bail-out (bushy.go:99).
//
// This constant is that bail-out's REPLACEMENT, not its executioner: it is a
// representation limit on the new search, while `bushy.go:99` is a runtime
// guard on the old subset-bitmask DP, whose enumeration is 3ⁿ over splits of
// subsets (3¹⁶ ≈ 43 M) rather than this search's clause-connected pair
// enumeration. Deleting the old guard while the old DP is still the production
// path would hand it 13-16-relation queries it cannot finish, so the deletion
// happens WITH that DP in P6.3 — which is what 03 §7 says ("deleted with the
// bushy DP"), and which the P5.8 TODO line stated more loosely. M0127-P5.8.
// take2 P3-09: 32, following RelSet's width rather than restating it. The
// ceiling was 16 because RelSet was uint16, and the comment above calls it
// "a representation limit on the new search" — so widening the
// representation IS the change. The old bushy DP's separate 3^n runtime
// guard, which that comment warned must not be raised alongside this one, no
// longer exists: bushy.go was deleted with that DP at M0127-P6.3.
const maxSearchRels = 32

// pgShapedDP gates the whole PG-shaped search (08 §2, S5). **FLIPPED ON
// 2026-08-06 by M0127-P5.9** — the acceptance event. Every P5 task landed dark
// behind this gate; run 4 of the 09 §3 bar (2026-08-06, HEAD `9e0cfe67`) is the
// first run in which nothing in the evidence is attributed to the flag —
// clauses 1-5 PASS, and clause 6 was discharged by measurement two days later
// (09 §3.13: both PG-only bushy partitions were OFFERED to `makeJoinRel` at
// phase 2, so the search can express them and lost them on cost, which the §4
// ratchet admits).
//
// The knob survives the flip as a KILL-SWITCH, not a soak switch. Until
// M0127-P6.3 the rollback story for S5 was "flips `GOOPG_PGSHAPED_DP` OFF,
// restoring the `tryBushyDP` enumerator"; P6.3 deleted that enumerator
// (08 §4), so `=0` now means "no join-order search at all" — the statement
// keeps its syntactic FROM order and the rule-driven rewrites
// (`rewriteJoinsToNLI`, the qual-placement passes) do what they have always
// done to such a tree. Anything else (unset, `1`, garbage) is ON, mirroring
// `GOOPG_JOIN_SLOT_CHAIN` (08 §2 S1: "default ON, env kill-switch OFF only").
//
// The gate is read once at process start so a plan cannot change shape
// mid-statement.
var pgShapedDP = pgShapedDPFromEnv(os.Getenv("GOOPG_PGSHAPED_DP"))

// pgShapedDPFromEnv is the kill-switch's polarity, factored out so it is
// testable without a subprocess: only the exact string "0" turns the search
// off. An unset variable reads as "" and is therefore ON.
func pgShapedDPFromEnv(v string) bool { return v != "0" }

// pgShapedDPEnabled reports whether the PG-shaped join search is active. P5.3's
// entry point is its only production caller; exposed as a function so the flag
// stays a single read site.
func pgShapedDPEnabled() bool { return pgShapedDP }

// SetPGShapedJoinSearch — the cross-package test pin for the other enumerator
// arm — went away with the old DP at M0127-P6.3 (08 §4), as its doc always
// said it would. The planner-internal `useLegacyEnumerator` (legacyarm_test.go)
// remains and now pins the kill-switch arm: no search, syntactic order, rule
// rewrites.

// searchCtx is the join search's working state — the subset of PG's
// PlannerInfo the search itself reads. One per join problem.
type searchCtx struct {
	// joinrels is PG's `root->join_rel_level`: `joinrels[lev]` holds every
	// RelOptInfo whose relset has exactly `lev` base rels. Index 0 is unused
	// (PG's array is 1-based) and `joinrels[1]` is the initial rels, in FROM
	// order. Level order is significant: phase 1 pairs `joinrels[lev-1]` with
	// `joinrels[1]`, phase 2 pairs `joinrels[k]` with `joinrels[lev-k]`, and
	// phase 2's mirror-image rule (joinrels.c:174-177) indexes INTO the level
	// list, so entries must never be reordered once appended.
	joinrels [][]*RelOptInfo

	// relMap is the relset -> rel lookup that sits beside the level lists —
	// PG's `join_rel_hash` (relnode.c). Base rels are registered here too, so
	// a singleton relset resolves through the same door as a composite one.
	// This is what makes `makeJoinRel` (P5.3) a find-or-create rather than an
	// unconditional allocation: the same joinrel is reached from every pair
	// of subsets that spans it, and every such pair must add its paths to ONE
	// rel or add_path cannot prune across them.
	relMap map[RelSet]*RelOptInfo

	// nrels is the number of base relations in this join problem; the search
	// runs levels 2..nrels and the final rel is the sole entry at nrels.
	nrels int

	// cp is the cost currency every path in this search is priced in (04 §1).
	cp costParams

	// tupleFraction is PG's `root->tuple_fraction` (pathnodes.h:341): how much
	// of the result will actually be fetched, in upstream's overloaded
	// encoding — 0 for all rows, (0,1) for a fraction, >= 1 for an absolute
	// count. `preprocessLimit` (tuplefraction.go) derives it from the `*Limit`
	// above the join and `buildInitialRels` records it here.
	//
	// It is on the CONTEXT rather than on each rel because it is a property of
	// the query, not of a relation: every rel the search creates copies it into
	// its own `ConsiderStartup`, exactly as `build_simple_rel` /
	// `build_join_rel` do (relnode.c:211/707), and `finalPath` reads it once at
	// the root. Zero is the "fetch everything" default, which is the regime
	// every search ran in before M0127-P5.7-b.
	tupleFraction float64

	// relInfos is the per-initial-rel estimate `buildInitialRels` was handed,
	// kept because the parameterised index paths of P5.4b-ii-a need each base
	// rel's `catalog.Table` and cannot be built until the clause list exists
	// (pathparamindex.go). Index i corresponds to `joinrels[1][i]`, which is
	// FROM order — the same correspondence `buildInitialRels` established when
	// it derived relid `1<<i` from position i.
	relInfos []baseRelInfo

	// clauses is the join-clause bookkeeping the enumerator gates on — PG's
	// per-rel `joininfo` lists, flattened (joinrestrict.go:91). nil is legal
	// and means "no join qual anywhere", which phase 1's clauseless branch
	// handles; every predicate on it is nil-safe. Set by `joinSearch`.
	clauses *restrictInfoList

	// builder is the sizing-and-costing collaborator `makeJoinRel` calls —
	// P5.6's calcJoinrelSize and P5.4's add_paths_to_joinrel
	// (joinsearchlevel.go:36). Set by `joinSearch`, which refuses a nil one.
	builder joinRelBuilder

	// trace is the enumeration-provenance record (M0127-P5.9-l-ii,
	// joinsearchtrace.go) — nil unless `GOOPG_PGSHAPED_DP_TRACE=1`, and read
	// only through its own nil-safe methods. It answers the one question the
	// chosen plan cannot: whether a pairing PG chose was ever OFFERED here.
	trace *searchTrace

	// tracePhase is which `join_search_one_level` pass is currently
	// enumerating, so `makeJoinRel` can record a pair's provenance without
	// being handed a phase argument it would otherwise ignore. Meaningless
	// while `trace` is nil.
	tracePhase int

	// joinInfoList is root->join_info_list: every SpecialJoinInfo built during
	// jointree deconstruction, in bottom-up order. Consumed by join_is_legal
	// (joinIsLegal), joinOrderRestricted, and hasJoinRestriction. nil means
	// "no special joins" — a simple inner-join-only FROM clause — which is
	// the fast path equivalent of an empty list. M0128-P1.2.
	joinInfoList []*SpecialJoinInfo

	// queryPathkeys is `PlannerInfo.query_pathkeys` (C-07/P3-06,
	// querypathkeys.go): the ordering the STATEMENT wants from this level,
	// derived once by `standard_qp_callback` before the first rel exists and
	// carried here for the same reason `tupleFraction` is — it is a property
	// of the query, not of a relation. Read by `hasUsefulPathkeys`. Empty
	// means "no special ordering requested", which is upstream's NIL.
	queryPathkeys []PathKey

	// neededCols / neededColsKnown are PG's `reltarget` + `attr_needed` for
	// this statement, by COLUMN NAME — see pathindexonlyneed.go for the
	// direction of the approximation. `neededColsKnown == false` means
	// "assume every column is needed", which is what the pre-index-only
	// planner assumed unconditionally. Consumed by `addIndexOnlyPaths`.
	neededCols      map[string]bool
	neededColsKnown bool

	// outputCols / outputColsKnown are the statement's ABOVE-TREE
	// needed-column set (outputColumnNames), the union needed above the
	// scan/join tree. Take2 P4-01 Slice 3: per-joinrel keep-sets derive
	// from it. outputEligible folds the positional gates (statement-top
	// problem, no pinned spine above, no outer-scope reads): only an
	// eligible problem stamps the set onto its rels, so the derivation
	// declines everywhere else by construction.
	outputCols      map[string]bool
	outputColsKnown bool
	outputEligible  bool

	// parallelModeOK is `root->glob->parallelModeOK` for this search
	// (considerparallel.go); cat is the catalog the qual-safety walk resolves
	// user routines through. Both are set by `setBaseRelConsiderParallel`,
	// the C-19a protocol step; a search that skips the step (every direct
	// test caller) keeps the zero values — no rel considers parallel, no
	// partial path exists — which is the pre-C-19 regime exactly.
	parallelModeOK bool
	cat            catalog.Catalog
}

// newSearchCtx allocates the level lists for an nrels-relation join problem.
// joinInfoList is the statement's SpecialJoinInfo list (nil if none — a simple
// inner-join FROM clause).
func newSearchCtx(nrels int, cp costParams, joinInfoList []*SpecialJoinInfo) (*searchCtx, error) {
	if nrels < 1 {
		return nil, fmt.Errorf("join search: need at least one base relation, got %d", nrels)
	}
	if nrels > maxSearchRels {
		return nil, fmt.Errorf("join search: %d base relations exceeds the %d-relation relset width", nrels, maxSearchRels)
	}
	return &searchCtx{
		joinrels:     make([][]*RelOptInfo, nrels+1),
		relMap:       make(map[RelSet]*RelOptInfo),
		nrels:        nrels,
		cp:           cp,
		joinInfoList: joinInfoList,
	}, nil
}

// levelRels returns the rels at the given level, or nil when the level is out
// of range or still empty. The slice is the live list, not a copy: phase 2
// reads it by index (the mirror-image `first_rel` rule).
func (s *searchCtx) levelRels(lev int) []*RelOptInfo {
	if lev < 1 || lev >= len(s.joinrels) {
		return nil
	}
	return s.joinrels[lev]
}

// findRel resolves a relset to its RelOptInfo, or nil. PG's `find_join_rel`
// (relnode.c), which `build_join_rel` calls before allocating.
func (s *searchCtx) findRel(relids RelSet) *RelOptInfo {
	return s.relMap[relids]
}

// relLevel is the level a relset belongs to: its member count. PG's
// `bms_num_members(relids)`.
func relLevel(relids RelSet) int { return bits.OnesCount32(uint32(relids)) }

// addRel files a rel into its level list and the relset map. The level is
// derived from the relset, never passed in, so the two indexes cannot disagree
// about where a rel lives — the failure mode that would let phase 2 pair a rel
// with itself.
//
// A duplicate relset is a caller bug, not a recoverable condition: every
// creation site must go through findRel first (`makeJoinRel`'s find-or-create,
// P5.3), because two RelOptInfos over the same relset would split the pathlist
// add_path is supposed to prune within.
func (s *searchCtx) addRel(rel *RelOptInfo) error {
	if rel == nil {
		return fmt.Errorf("join search: nil rel")
	}
	if rel.Relids == 0 {
		return fmt.Errorf("join search: rel with an empty relset")
	}
	lev := relLevel(rel.Relids)
	if lev >= len(s.joinrels) {
		return fmt.Errorf("join search: rel at level %d exceeds the %d-relation problem", lev, s.nrels)
	}
	if prev := s.relMap[rel.Relids]; prev != nil {
		return fmt.Errorf("join search: relset %#08x already registered", uint32(rel.Relids))
	}
	s.relMap[rel.Relids] = rel
	s.joinrels[lev] = append(s.joinrels[lev], rel)
	return nil
}

// finalRel returns the top-level rel — the sole entry at level nrels — or nil
// when the search has not reached it. PG's `standard_join_search` asserts the
// final level holds exactly one rel (allpaths.c:3508-3512); goopg reports the
// violation to its caller instead of asserting, because P5.3's fallback on a
// failed search is the syntactic shape rather than an error (03 §4.2).
func (s *searchCtx) finalRel() (*RelOptInfo, error) {
	top := s.levelRels(s.nrels)
	switch len(top) {
	case 1:
		return top[0], nil
	case 0:
		return nil, fmt.Errorf("join search: no rel at the final level %d", s.nrels)
	default:
		return nil, fmt.Errorf("join search: %d rels at the final level %d, expected exactly 1", len(top), s.nrels)
	}
}

// finalPath is the path the search CHOSE: `standard_planner`'s
// `best_path = get_cheapest_fractional_path(final_rel, root->tuple_fraction)`
// (planner.c:437), and the one value a caller may hand
// `createPlanAtSearchRoot`.
//
// It exists as a method rather than leaving the caller to read
// `finalRel().CheapestTotal` because those two are not the same path under a
// LIMIT, and the difference is the whole of M0127-P5.7-b: with a fraction in
// play the cheapest way to produce ALL the rows is not the cheapest way to
// produce the first ten. Reading CheapestTotal directly would silently discard
// the fraction, so the search publishes the answer instead of the ingredients.
//
// With no LIMIT (`tupleFraction == 0`) this returns `CheapestTotal` exactly,
// which is what every caller would have got before.
func (s *searchCtx) finalPath() (*Path, error) {
	rel, err := s.finalRel()
	if err != nil {
		return nil, err
	}
	p := getCheapestFractionalPath(rel, s.tupleFraction)
	if p == nil {
		// setCheapest leaves every slot nil only for an empty pathlist, which
		// joinSearch already rejects per level; reaching here means the final
		// rel was assembled outside that path.
		return nil, fmt.Errorf("join search: final rel %#08x has no cheapest path", uint32(rel.Relids))
	}
	return p, nil
}

// buildInitialRels populates level 1: one RelOptInfo per FROM item, each with
// its cardinality and one costed path. `bindings`, `scans` and `relInfos` are
// the three parallel per-FROM-item slices the existing pipeline already
// assembles for the bushy DP (bushy.go:184-196), so P5.3's entry point hands
// over exactly what `tryBushyDP` collects.
//
// Every FROM item becomes an initial rel — this is where the leaf-whitelist
// gap closes. A subquery / CTE / VALUES / function-scan leaf is not a reason to
// abandon the search; it is a relation whose one path is a `PathPrebuilt` over
// the already-planned subtree, with rows and cost read off that subtree's own
// estimate, which is 03 §2's `set_subquery_pathlist` analogue at our fidelity
// level.
//
// The path is `PathPrebuilt` for base-table leaves too, and that is deliberate
// rather than a shortcut: goopg's leaf is not a bare relation but a scan node
// the pre-search pipeline has already chosen an access method for and already
// attached this relation's local quals to. Carrying the node whole is what lets
// P5.5's createPlan re-emit an `*IndexScan` leaf as an index scan instead of
// silently demoting it to a seq scan. Its COST, however, is re-derived here in
// the search's own currency (04 §1) rather than inherited, because the two
// cost models must not be mixed inside one comparison.
//
// The parameterised index paths NLI needs (03 §2's base-table row, §5.2) are
// NOT added here, and their absence is not the deferral it once was: they
// depend on the clause list, which is built after the initial rels, so they are
// a separate step — `addParameterizedIndexPaths` (pathparamindex.go), run
// between this and `joinSearch`. That mirrors PG, where `set_base_rel_pathlists`
// (allpaths.c:191) likewise runs only once `deconstruct_jointree` has produced
// the `joininfo` lists `create_index_paths` reads. Real UNPARAMETERISED
// per-index path generation is still P5.4's and still ledgered as deferred.
// `tupleFraction` is the query's `root->tuple_fraction` (see searchCtx), and it
// is a PARAMETER rather than something set afterwards because PG fixes it in
// `subquery_planner` before any rel exists: `build_simple_rel` reads it while
// constructing the rel (relnode.c:211), and a flag that arrived after the first
// `addPath` would have let one dominance decision be taken in the wrong regime.
// Pass 0 for "fetch all rows", which is every caller that has no LIMIT.
func buildInitialRels(bindings []rangeBinding, scans []Node, relInfos []baseRelInfo, cp costParams, tupleFraction float64, joinInfoList []*SpecialJoinInfo) (*searchCtx, error) {
	if len(bindings) == 0 {
		return nil, fmt.Errorf("join search: empty FROM list")
	}
	if len(scans) != len(bindings) || len(relInfos) != len(bindings) {
		return nil, fmt.Errorf("join search: %d bindings but %d scans and %d rel infos",
			len(bindings), len(scans), len(relInfos))
	}
	s, err := newSearchCtx(len(bindings), cp, joinInfoList)
	if err != nil {
		return nil, err
	}
	s.relInfos = relInfos
	s.tupleFraction = tupleFraction
	// The relid → relation-name map has to be taken HERE, from the same
	// `bindings` slice whose position i defines relid `1<<i` below: taking it
	// anywhere else would be a second derivation of the correspondence the
	// whole trace is read through (M0127-P5.9-l-ii). nil unless the gate is on.
	s.trace = newSearchTrace(bindings)
	for i := range bindings {
		leaf := scans[i]
		if leaf == nil {
			return nil, fmt.Errorf("join search: FROM item %d has no leaf node", i)
		}
		rows := initialRelRows(leaf, relInfos[i])
		width := nodeTupleWidth(leaf)
		rel := newRelOptInfo(RelSet(1)<<uint(i), rows, width)
		// The column count the hash geometry is solved for, from the same
		// schema the executor will call len() on (M0127-P5.7-a).
		rel.NCols = len(leaf.Output())
		// AvgVarBytes: sum the per-column AvgWidth from ANALYZE stats
		// across every column of this relation. Zero when the relation
		// has never been ANALYZEd or is all-fixed-width — both are
		// correct for hash sizing (M0128-P3.1).
		ri := relInfos[i]
		if ri.table != nil && ri.table.Stats != nil && len(ri.table.Stats.Columns) > 0 {
			var sum float64
			for _, cs := range ri.table.Stats.Columns {
				sum += cs.AvgWidth
			}
			rel.AvgVarBytes = sum
			// The same widths unsummed, for the build-side narrowing
			// (see RelOptInfo.ColVarBytes).
			rel.ColVarBytes = tableColVarBytes(ri.table)
		}
		// `rel->consider_startup = (root->tuple_fraction > 0)`
		// (relnode.c:211): a fast start is worth keeping paths for exactly when
		// something will ask for a fraction (M0127-P5.7-b).
		rel.ConsiderStartup = s.tupleFraction > 0
		// The leaf is recorded on the rel as well as inside the PathPrebuilt
		// below, because two different consumers need it two different ways:
		// createPlan's PathPrebuilt arm returns the node it wrapped, while the
		// index-scan arm (M0127-P5.5-c) rebuilds a DIFFERENT node and needs the
		// original leaf's identity — alias, schema, local-qual wrappers — to
		// copy forward (createplanindex.go).
		rel.baseLeaf = leaf
		// …and WHERE it sat, which is the half of 03 §10's map the leaf node
		// itself cannot supply: a `*SeqScan` knows its own schema but not the
		// offset at which that schema was spliced into the pre-search
		// concatenation the search's clauses are written in. Recorded here
		// because this is the only place both facts are in scope at once
		// (M0127-P5.5-e-i; see RelOptInfo.baseOffset).
		rel.baseOffset = bindings[i].offset
		if err := s.addRel(rel); err != nil {
			return nil, err
		}
		p := newPrebuiltPath(rel, leaf)
		// The scan-cost currency of 04 §1 over the rel's own estimate.
		// numQualOps is 0 because the local quals are already inside the
		// leaf node and their selectivity is already inside `rows`; charging
		// for them again here would double-count (the per-tuple operator
		// term is what `estimateBaseRelInfo` has already spent).
		p.Cost = costSeqscan(cp, estScanPages(rows, width), rows, 0)
		addPath(rel, p, "joinsearch.prebuilt")
		setCheapest(rel)
	}
	return s, nil
}

// initialRelRows is the initial rel's cardinality: post-local-filter for a base
// table (today's `baseRelInfo.filteredRows`, kept per 03 §2), and the subtree's
// own estimate for every other leaf class. Floored at 1 — the bushy DP's
// "no zero-row singletons" invariant (cardinality.go:311), which matters more
// here because a 0-row initial rel would make every join above it free.
func initialRelRows(leaf Node, info baseRelInfo) float64 {
	rows := info.filteredRows
	switch leafBaseScan(leaf).(type) {
	case *SeqScan, *IndexScan, *IndexOnlyScan:
		// Base-table leaf: `filteredRows` is the authority.
	default:
		// Subquery / CTE / VALUES / function scan / set-op / an
		// already-built join subtree: `filteredRows` was derived from a
		// synthetic catalog.Table and means nothing, so read the subtree.
		rows = EstimateRows(leaf)
		// M0129-S1: CTE scans have no per-column statistics, so
		// filterSelectivity defaults to defaultEqSelectivity (0.005)
		// per conjunct. For a CTE like year_total with 4 conjuncts
		// over columns that actually have 2 distinct values each,
		// 0.005⁴×17977≈0.000011 collapses to 1 row — a severe
		// under-estimate that makes nested loops look free. Fall
		// back to the CTE body's unfiltered row count to avoid
		// the default-selectivity cliff.
		if rows <= 1 {
			if cte, ok := leafBaseScan(leaf).(*CTEScan); ok && cte.Child != nil {
				if bodyRows := EstimateRows(cte.Child); bodyRows > 1 {
					rows = bodyRows
				}
			}
		}
	}
	if rows < 1 {
		return 1
	}
	return float64(rows)
}

// leafBaseScan peels the `*Filter` wrappers off a search leaf and returns what
// is underneath — the same peel `scanLeafFor` (createplanindex.go) does, and
// for the same reason: a base-relation leaf whose local quals have been pushed
// into it is a `*Filter` over a scan, not a scan (M0127-P5.9-b attaches them
// before the search rather than after it, joinsearchseam.go).
//
// It matters HERE because the wrapper decides which cardinality is believed. A
// filter-wrapped base table reaching `initialRelRows`' default arm would be
// re-estimated by `EstimateRows` — a second selectivity computation over the
// same predicate `estimateBaseRelInfo` already applied to produce
// `filteredRows`, and a different one, which is the sibling-divergence shape
// this planner keeps paying for.
func leafBaseScan(n Node) Node {
	for {
		f, ok := n.(*Filter)
		if !ok || f.Child == nil {
			return n
		}
		n = f.Child
	}
}

// stampNeededColsOnRels copies the statement's needed-column set onto every rel
// the search has built so far. take2 P4-01 rev 10 step 1.
//
// The set is a statement property, so every rel gets the same map by reference
// rather than a copy — it is read-only from here on, and `createPlanNode`
// reaches it through `Path.Rel`.
func (s *searchCtx) stampNeededColsOnRels() {
	if s == nil {
		return
	}
	for _, level := range s.joinrels {
		for _, r := range level {
			if r == nil {
				continue
			}
			r.NeededCols, r.NeededColsKnown = s.neededCols, s.neededColsKnown
		}
	}
}

// stampOutputColsOnRels copies the statement's above-tree needed-column set
// onto every rel the search has built so far. Take2 P4-01 Slice 3: the union
// needed above the scan/join tree, from which per-joinrel keep-sets derive.
//
// Called only when s.outputEligible (the statement-top problem with no pinned
// spine above it and no outer-scope reads); every other problem leaves the
// zero value on its rels and the derivation declines there. Same by-reference
// sharing as the needed set.
func (s *searchCtx) stampOutputColsOnRels() {
	if s == nil || !s.outputEligible {
		return
	}
	for _, level := range s.joinrels {
		for _, r := range level {
			if r == nil {
				continue
			}
			r.OutputCols, r.OutputColsKnown = s.outputCols, s.outputColsKnown
		}
	}
}
