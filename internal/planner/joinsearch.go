package planner

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
// NOTHING here is called from `planSelect`. The enumerator that consumes it is
// P5.3, and the whole search is gated behind `GOOPG_PGSHAPED_DP` (08 §2, OFF by
// default), so this file cannot change a plan; it is validated in isolation by
// `joinsearch_test.go`.
//
// What it DOES settle is the leaf-whitelist gap. `tryBushyDP` abandons the
// entire search when any FROM item is not a `*SeqScan` / `*IndexScan` /
// `*MultiHashJoin` (bushy.go:116-123) — a single subquery, CTE or VALUES item
// disables join reordering for the whole statement (ledger rows M0125-0034 /
// -0036, and M0125-0037 stage (ii)). `buildInitialRels` admits every FROM item
// instead: each becomes one initial rel whose single path carries the leaf the
// pre-search pipeline already produced.

import (
	"fmt"
	"math/bits"
	"os"
)

// maxSearchRels is the relset width. `RelSet` is a uint16 (path.go:29), so the
// search can address 16 base relations — above the level the collapse limits
// admit (03 §7), and above the old DP's own 12-table bail-out (bushy.go:99),
// which P5.8 deletes.
const maxSearchRels = 16

// pgShapedDP gates the whole PG-shaped search (08 §2, S5). It is OFF by default
// and stays OFF through every P5 task: each lands dark, and P5.9's acceptance
// run is what flips it. The gate is read once at process start so a plan cannot
// change shape mid-statement.
var pgShapedDP = os.Getenv("GOOPG_PGSHAPED_DP") == "1"

// pgShapedDPEnabled reports whether the PG-shaped join search is active. P5.3's
// entry point is its only production caller; exposed as a function so the flag
// stays a single read site.
func pgShapedDPEnabled() bool { return pgShapedDP }

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
}

// newSearchCtx allocates the level lists for an nrels-relation join problem.
func newSearchCtx(nrels int, cp costParams) (*searchCtx, error) {
	if nrels < 1 {
		return nil, fmt.Errorf("join search: need at least one base relation, got %d", nrels)
	}
	if nrels > maxSearchRels {
		return nil, fmt.Errorf("join search: %d base relations exceeds the %d-relation relset width", nrels, maxSearchRels)
	}
	return &searchCtx{
		joinrels: make([][]*RelOptInfo, nrels+1),
		relMap:   make(map[RelSet]*RelOptInfo),
		nrels:    nrels,
		cp:       cp,
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
func relLevel(relids RelSet) int { return bits.OnesCount16(uint16(relids)) }

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
		return fmt.Errorf("join search: relset %#04x already registered", uint16(rel.Relids))
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
func buildInitialRels(bindings []rangeBinding, scans []Node, relInfos []baseRelInfo, cp costParams) (*searchCtx, error) {
	if len(bindings) == 0 {
		return nil, fmt.Errorf("join search: empty FROM list")
	}
	if len(scans) != len(bindings) || len(relInfos) != len(bindings) {
		return nil, fmt.Errorf("join search: %d bindings but %d scans and %d rel infos",
			len(bindings), len(scans), len(relInfos))
	}
	s, err := newSearchCtx(len(bindings), cp)
	if err != nil {
		return nil, err
	}
	s.relInfos = relInfos
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
		addPath(rel, p)
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
	switch leaf.(type) {
	case *SeqScan, *IndexScan, *IndexOnlyScan:
		// Base-table leaf: `filteredRows` is the authority.
	default:
		// Subquery / CTE / VALUES / function scan / set-op / an
		// already-built join subtree: `filteredRows` was derived from a
		// synthetic catalog.Table and means nothing, so read the subtree.
		rows = EstimateRows(leaf)
	}
	if rows < 1 {
		return 1
	}
	return float64(rows)
}
