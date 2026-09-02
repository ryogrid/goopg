package optimizer

// Path / RelOptInfo / add_path / set_cheapest — the substrate of the PostgreSQL
// cost model, reproduced for goopg. See docs/design/cost-model/, chapters 02 (the
// PG oracle) and 03 (the goopg substrate).
//
// This file is phase C0.1: the pure data types plus add_path/set_cheapest, with
// NO integration into the live planning pipeline. Nothing calls it from
// planSelect yet, so it cannot change a plan; it is validated in isolation by
// path_test.go. Cost functions arrive in C3, path generation in C3/C4,
// create_plan in C0.2.
//
// The one non-obvious fidelity point reproduced here is PG 18's dominance order:
// compare_path_costs_fuzzily (postgres/src/backend/optimizer/util/pathnode.c:185)
// compares disabled_nodes FIRST ("trumps all else", :191), then total cost, then
// startup cost, each within a multiplicative STD_FUZZ_FACTOR (:50) tolerance.
// add_path (:464) folds in pathkeys, parallel_safe, and required-outer relids.

import "github.com/goopg/goopg/internal/catalog"

// stdFuzzFactor is PG's STD_FUZZ_FACTOR (pathnode.c:50): two costs within 1% are
// treated as equal, and the tie is broken on the non-cost dimensions. This is the
// determinism mechanism the integer->float migration needs (design ch. 07 §4).
const stdFuzzFactor = 1.01

// RelSet is a set of base-relation ids, one bit per relation. (It reused the
// uint16 bitmask the bushy DP keyed joinrels on; that DP is deleted as of
// M0127-P6.3 and the width is now the search's own — `maxSearchRels`,
// joinsearch.go.) This bounds a single query's join at 16 base relations.
type RelSet uint16

// Cost is PG's two-number cost, in PG's units (seq_page_cost = 1.0, design ch. 02
// §3). Startup is the cost expended before the first row emerges; Total is the
// cost to return all rows. A cheaper-total path can lose to a cheaper-startup one
// under a LIMIT, which is why both axes are carried (design ch. 07 §1.2).
type Cost struct {
	Startup float64
	Total   float64
}

// PathKind identifies which physical operator a Path represents. createPlan
// (C0.2) switches on it to emit the corresponding executor Node.
type PathKind int

const (
	// PathPrebuilt wraps an already-constructed executor Node. It is the C0
	// bridge: while path generation does not yet exist, the join subtree the
	// integer DP builds is wrapped in a PathPrebuilt so createPlan has something
	// to translate and the Path<->Node round-trip is exercised end-to-end
	// (design ch. 03 §3.1 staging note). Later phases add the real kinds below
	// and PathPrebuilt is retired for join/scan rels.
	PathPrebuilt PathKind = iota
	PathSeqScan
	PathIndexScan
	PathHashJoin
	PathMergeJoin
	PathNestLoop
	PathAgg
	PathSort
	PathGather
	PathGatherMerge
	// PathMemoize is PG's `MemoizePath` (pathnodes.h:2079): a caching wrapper
	// over a PARAMETERISED inner path, so a rescan with a parameter set already
	// seen is served from the cache instead of re-probed. It is produced only by
	// `getMemoizePath` (joinpathsmemoize.go) and consumed only by the NLI arm of
	// `createNestLoopPlan`, because goopg's executor expresses the cache as
	// `NestedLoopIndexJoin.InnerMemo` rather than as a free-standing node —
	// which is why `createPlanNode` has no arm for it (M0127-P5.4b-ii-b-2).
	PathMemoize

	// M0128-P2.4: bitmap scan path kinds. PathBitmapIndexScan is the index-
	// access leaf (MultiExec-style, feeds a TIDBitmap); PathBitmapHeapScan
	// consumes the bitmap with page-at-a-time heap access; PathBitmapAnd /
	// PathBitmapOr combine multiple bitmap sub-trees. Design:
	// docs/design/0128-0001-bitmap-heap-scan.md §3.4.
	PathBitmapIndexScan
	PathBitmapHeapScan
	PathBitmapAnd
	PathBitmapOr
)

// Path is one way to produce a relation, with a cost and an ordering. It is kept
// deliberately small — thousands are allocated per join search — with kind-
// specific data in a narrow payload rather than a fat struct (design ch. 03 §1).
type Path struct {
	Kind PathKind
	Rel  *RelOptInfo

	Cost Cost

	// Rows is THIS path's row count, which is the rel's row count for an
	// ordinary path and the per-outer-row count for a parameterised one. It is
	// PG's `path->rows`, and for a parameterised path PG sets it from
	// `param_info->ppi_rows` (`create_index_path`, pathnode.c) — so Rows is
	// goopg's `ppiRows` carrier, not a second field (leftdeep-joins 03 §9
	// rule 3). This is the one structured exception to 04 §2's "rows once per
	// RelOptInfo": `Rel.Rows` stays canonical for the relation as a whole, and
	// each parameterisation carries its own count beside it.
	//
	// Consequence for costing, and the reason §9 says NLI costing is otherwise
	// garbage: a join's cost primitives must read the CHILD PATH's Rows, never
	// `child.Rel.Rows`. An index path parameterised by the outer produces a
	// handful of rows per probe; charging the rel's full post-filter count for
	// it would price an NLI as if it re-scanned the whole inner every time.
	Rows float64

	// Pathkeys is the ordering this path guarantees (design ch. 04). Empty in
	// C0/C1 for every path until sort / ordered-scan / merge paths exist.
	Pathkeys []PathKey

	// ParallelSafe / ParallelWorkers describe parallel eligibility. Workers > 0
	// only for partial paths (design ch. 08 §2). Unused until C5.
	ParallelSafe    bool
	ParallelWorkers int

	// NCols / AvgVarBytes describe what THIS PATH emits, when that is narrower
	// than its relation — PG's `pathtarget` at the granularity goopg needs for
	// hash sizing. Zero NCols means "not narrowed"; read them through
	// pathNCols / pathAvgVarBytes, never directly, so the fallback to the
	// rel's figures stays in one place.
	NCols       int
	AvgVarBytes float64

	// DisabledNodes reproduces PG 18's path->disabled_nodes (the count of
	// enable_*-disabled nodes below this path). goopg has no enable_* GUCs, so it
	// is always 0 today; carried so the dominance order matches PG and adding
	// enable_* later is a data change, not a code change (design ch. 02 §2.2).
	DisabledNodes int

	// RequiredOuter is the set of outer relations a parameterized path depends on
	// (the minimal analogue of PG's ParamPathInfo, design ch. 03 §3.1). Empty for
	// every ordinary path; non-empty only for an NLI inner index path (C3).
	RequiredOuter RelSet

	// HashKeys / Residual are this join path's qual placement (leftdeep-joins
	// 03 §5.4): the equality clauses the operator KEYS on, and the clauses it
	// must evaluate per tuple. Both are nil for a non-join path. They are
	// decided once, here in path generation, rather than by a post-hoc pass —
	// which is what lets the placement be COSTED (a qual deferred to a higher
	// join is a qual evaluated on more tuples) instead of being invisible to
	// the search.
	//
	// Only a keyed operator (hash, and merge since P5.4c-i) fills HashKeys; a
	// plain nested loop keys on nothing and carries every clause in Residual.
	// For a merge join the list is ORDERED: clauses are grouped by the sort key
	// they serve, in the path's own pathkey order (`joinpathsmerge.go`).
	// P5.5's createPlan reads the pair to emit the executor Join's key
	// expressions and its residual predicate.
	HashKeys []*restrictInfo
	Residual []*restrictInfo

	// IndexInfo / IndexScanDir are `IndexPath.indexinfo` and
	// `IndexPath.indexscandir` (pathnodes.h:1845/1849), the two facts
	// `create_indexscan_plan` needs to re-emit the scan the search chose. They
	// are set on a `PathIndexScan` and on nothing else: `IndexInfo` is nil and
	// `IndexScanDir` is `NoMovementScanDirection` — the zero value — for every
	// other kind, which is why the flat struct can carry them without a second
	// discriminator. Both are filled through `indexPathOrdering`
	// (pathindexcarrier.go, M0127-P5.5-a) so the direction can never disagree
	// with `Pathkeys`. P5.5's createPlan arm reads them.
	//
	// IndexClauses is `IndexPath.indexclauses` (pathnodes.h:1846): the quals
	// pushed INTO the probe, in INDEX-COLUMN order — the order PG's own list is
	// in (indxpath.c:1042) and the order goopg's executor needs, since
	// `IndexScan.Keys[i]` binds `Index.Columns[i]` positionally. Empty on the
	// unparameterised ordered path, which is pathnodes.h:1817's "an empty list
	// implies a full index scan" rather than an omission. Built only by
	// `indexPathClauses` (pathindexclauses.go, M0127-P5.5-b), which is what
	// makes the ordering structural. P5.5's createPlan arm reads it to build
	// `Keys` and to drop the pushed clauses from the node's filter quals.
	IndexInfo    *catalog.Index
	IndexScanDir ScanDirection
	IndexClauses []indexPathClause

	// IndexOnly marks this path as PG's T_IndexOnlyScan rather than
	// T_IndexScan (`create_index_path`'s `indexonly` argument). PG carries the
	// distinction on the pathtype of the SAME IndexPath struct rather than in
	// a separate node, and so does this. IndexOnlyCovered is the column list
	// the scan emits, in INDEX-COLUMN order — a subset of the table's columns,
	// which is what makes the node narrower than the leaf it replaces;
	// `baseRelLayout` re-bases it by name (M0134-0187).
	IndexOnly        bool
	IndexOnlyCovered []catalog.Column

	// MemoizeInfo is the `MemoizePath`-only payload (M0127-P5.4b-ii-b-2): the
	// entry-count estimate `cost_memoize_rescan` computed on the way to the
	// rescan cost, and that rescan cost itself. It is nil for every other kind,
	// and a non-nil one is what makes `PathMemoize` self-describing — the two
	// numbers are produced together from one set of statistics, so carrying them
	// as one pointer is what stops an entry estimate from being paired with a
	// cost that was computed from a different ndistinct.
	MemoizeInfo *memoizePathInfo

	// BitmapSelectivity is the estimated fraction of the relation's rows that
	// the bitmap tree (index/and/or) admits from the index side, before heap
	// access and recheck. It is PG's `indexselectivity` on an IndexPath and
	// `bitmapselectivity` on BitmapAnd/BitmapOr. Zero for non-bitmap paths.
	// Set by costBitmapIndexScan for leaves and costBitmapAnd/costBitmapOr
	// for combinators.
	BitmapSelectivity float64

	// PartialPredicate is the resolved planner expression for a partial
	// index's WHERE predicate (PG's indpred). It is nil when the index is not
	// partial and nil when the prover has proven the predicate is implied by
	// the query quals (future — the prover does not yet exist). When non-nil,
	// createBitmapHeapScanPlan appends it to BitmapQual so the executor
	// rechecks it against every heap tuple. Only meaningful on
	// PathBitmapIndexScan leaves (M0129-S5.4).
	PartialPredicate Expr

	Children []*Path

	// node is the executor Node a PathPrebuilt wraps. nil for every other kind.
	node Node
}

// RelOptInfo is a relation — base or join — and its candidate paths. Rows is the
// single source of truth for this relation's cardinality: it is computed once
// (design ch. 05 §1) and every path over the rel reads it; costing never
// re-estimates (design ch. 03 §1.1, invariant #2).
type RelOptInfo struct {
	Relids RelSet
	Rows   float64
	Width  int

	// NCols is how many COLUMNS a row of this relation carries. It sits beside
	// Width (which is bytes) because goopg has two different width models and
	// they answer two different questions: Width feeds the page math PG feeds
	// with `pathtarget->width`, while NCols feeds `hashsize.Choose`, whose
	// per-row footprint is `48·columns` because a goopg hash entry is a []Datum
	// and not a packed MinimalTuple. Deriving one from the other is not
	// possible and guessing is not safe — see hashJoinInputs.innerCols.
	//
	// Set by the two production constructors (`buildInitialRels` from the
	// leaf's schema, `makeJoinRel` as the sum over the two inputs, which is
	// what the executor's Join concatenates). Zero on a rel built by a test
	// that does not care, which `relNCols` reads as "unknown".
	NCols int

	// AvgVarBytes is the average total variable-width payload per row, in
	// bytes — the sum of the per-column average widths (ColumnStats.AvgWidth)
	// across every column of this relation. It feeds `hashsize.EntryBytes` as
	// the `avgVarBytes` parameter, replacing the hardcoded zero that under-
	// counted text-heavy builds. Set alongside NCols by the same two
	// constructors; absent (zero) on a rel whose columns have never been
	// ANALYZEd, and zero for every fixed-width relation — both are correct.
	// M0128-P3.1.
	AvgVarBytes float64

	Pathlist        []*Path
	PartialPathlist []*Path

	CheapestTotal   *Path
	CheapestStartup *Path

	// ConsiderStartup / ConsiderParamStartup are PG's per-rel
	// `consider_startup` / `consider_param_startup` (pathnodes.h:889-890), and
	// they are the SAME fact `tuple_fraction` is: `build_simple_rel`
	// (relnode.c:211) and `build_join_rel` (:707) set both from
	// `root->tuple_fraction > 0`, so "is a fast-start plan interesting here"
	// is decided once, for the whole query, by whether a LIMIT was asked for.
	//
	// They exist because a startup-cheap path is only worth KEEPING when
	// something will later select on startup. `compare_path_costs_fuzzily`
	// enforces exactly that (pathnode.c:178-183): a path that loses on total
	// cost may not survive on good startup cost alone unless the relevant flag
	// is set. Without the flag goopg kept every such path — harmless for the
	// chosen plan, since selection was by total cost, but it is not PG's
	// pathlist, and P5.7-b's fractional selection is precisely the consumer
	// that makes the difference visible.
	//
	// ConsiderParamStartup stays false: PG sets it per base rel in
	// `set_base_rel_consider_startup` (allpaths.c:247) from the outer-join
	// `SpecialJoinInfo` list, and 03 §4.4's pin keeps special joins out of the
	// search entirely, so nothing can set it truthfully yet (ledger row
	// 2026-08-05).
	ConsiderStartup      bool
	ConsiderParamStartup bool

	// CheapestParameterized is PG's `cheapest_parameterized_paths`
	// (pathnode.c:255): every parameterised path that survived the addPath
	// tournament, with the cheapest UNparameterised path (if any) prepended —
	// PG's callers find that inclusion more convenient, and the NLI arm of
	// P5.4b-ii reads exactly this list. Empty until parameterised paths exist.
	CheapestParameterized []*Path

	// baseLeaf is the executor Node the pre-search pipeline handed
	// `buildInitialRels` for this FROM item — the search boundary's half of
	// 03 §10's coordinate map, recording what a base relid MEANS: the relation,
	// the alias, the output schema and the local quals, all already resolved
	// (M0127-P5.5-c). Set only on level-1 rels; nil on every join rel. It is
	// what PG reaches through `RelOptInfo`'s range-table entry and goopg's
	// search-only rel cannot: `createPlan`'s scan arms re-emit from it
	// (createplanindex.go), and the index-path producers gate on it through the
	// same `scanLeafFor` predicate, so a path is never COSTED over a leaf
	// the builder cannot rebuild.
	baseLeaf Node

	// baseOffset is WHERE this base relation's columns sat before the search
	// ran: the index, in the pre-search "binding" coordinate space, of the
	// leaf's first output column (`rangeBinding.offset`, planner.go:354). It is
	// `baseLeaf`'s companion and the other half of 03 §10's coordinate map —
	// `baseLeaf` records what a relid MEANS, `baseOffset` records where it USED
	// TO BE. Set only on level-1 rels, beside `baseLeaf`, and meaningful only
	// when that field is non-nil (0 is a legitimate offset for the first FROM
	// item, so the nil check is the discriminator, not the value).
	//
	// Why the search needs it at all: every `restrictInfo.clause` the search
	// reasons about carries ColumnRefs in exactly this space — `relidsOfExpr`
	// (joinrestrict.go:357) decides a clause's relset by bucketing each
	// ColumnRef.Index against the same `cumOffsets` — while the tree
	// `createPlan` emits is a cost-chosen reordering whose columns sit
	// somewhere else entirely. A join arm that copied a clause across
	// unchanged would key on whatever column happened to land at that index.
	// `outputLayout` (createplanjoin.go) is the per-node translation built
	// from this field.
	baseOffset int
}

// newRelOptInfo creates a rel with the given relids and (once-computed) size.
func newRelOptInfo(relids RelSet, rows float64, width int) *RelOptInfo {
	return &RelOptInfo{Relids: relids, Rows: rows, Width: width}
}

// relNCols is the column count the hash-join cost model must feed
// `hashsize.Choose` for this relation (M0127-P5.7-a).
//
// It prefers the field the search set, and falls back to the base leaf's own
// schema — the rel and its leaf cannot disagree about how many columns a scan
// of it produces, and the fallback keeps a rel constructed without the field
// (every test that calls newRelOptInfo directly) priced correctly whenever it
// is a base rel. Zero is returned only when neither is available, and
// hashJoinCost reads that as "assume no spill", which is what it did before
// this function existed.
// pathNCols is `relNCols` at PATH granularity — take2 P1-20's sibling in
// P4-01.
//
// A path can produce FEWER COLUMNS than its relation. `pathgen.go` used to read
// the column count from the rel, justified by "a parameterised path returns
// fewer ROWS than its rel but the same columns". That is true of
// parameterisation and false of PROJECTION: an index-only path emits only the
// columns its index covers, so the hash geometry was solved for the relation's
// full width while the executor measured the narrowed node's schema at runtime
// (`len(o.left.Schema())`). Planner and executor disagreed about the size of
// the same hash table.
//
// Zero means "this path does not narrow", and the rel's count is used.
func pathNCols(p *Path) int {
	if p != nil && p.NCols > 0 {
		return p.NCols
	}
	if p == nil {
		return 0
	}
	return relNCols(p.Rel)
}

// pathAvgVarBytes is pathNCols' variable-payload twin, and narrows for the same
// reason: a projected path carries only the payload of the columns it emits.
func pathAvgVarBytes(p *Path) float64 {
	if p == nil {
		return 0
	}
	if p.NCols > 0 {
		return p.AvgVarBytes
	}
	if p.Rel != nil {
		return p.Rel.AvgVarBytes
	}
	return 0
}

func relNCols(r *RelOptInfo) int {
	if r == nil {
		return 0
	}
	if r.NCols > 0 {
		return r.NCols
	}
	if r.baseLeaf != nil {
		return len(r.baseLeaf.Output())
	}
	return 0
}

// newPrebuiltPath wraps an already-built executor Node as a Path over rel. Cost
// is left zero in C0 (selection is still the integer DP, so the cost is not
// consulted); Rows is taken from the rel. This is the C0 bridge (design ch. 03
// §3.1).
func newPrebuiltPath(rel *RelOptInfo, n Node) *Path {
	return &Path{Kind: PathPrebuilt, Rel: rel, Rows: rel.Rows, node: n}
}

// pathCostComparison is the result of compare_path_costs_fuzzily.
type pathCostComparison int

const (
	costsEqual     pathCostComparison = iota // within fuzz on both axes
	costsBetter1                             // path1 is fuzzily cheaper
	costsBetter2                             // path2 is fuzzily cheaper
	costsDifferent                           // each is better on one axis
)

// considerPathStartupCost is PG's CONSIDER_PATH_STARTUP_COST macro
// (pathnode.c:187): whether THIS path's startup cost is interesting, read off
// its own parent rel and keyed on whether the path is parameterised. See
// RelOptInfo.ConsiderStartup for what sets it.
//
// A path with no parent rel is not a production shape — every constructor in
// this package sets `Rel` — but the comparator is also driven directly by unit
// tests that build bare paths to exercise one axis. Those get the
// fast-start-interesting answer, so the comparator's trade-off arm stays
// reachable without a rel in hand.
func considerPathStartupCost(p *Path) bool {
	if p.Rel == nil {
		return true
	}
	if p.RequiredOuter != 0 {
		return p.Rel.ConsiderParamStartup
	}
	return p.Rel.ConsiderStartup
}

// comparePathCostsFuzzily reproduces compare_path_costs_fuzzily
// (pathnode.c:185). disabled_nodes trumps all else (:191); then total cost is
// checked before startup (many paths have zero startup); each within `fuzz`.
//
// The two "different" arms carry PG's policy rule (:178-183, M0127-P5.7-b): the
// total-cost LOSER is allowed to be called merely different — rather than
// dominated — only when its own rel says a fast start is interesting. The
// asymmetry is upstream's and it is deliberate: the rule is about which paths
// are worth keeping, so it is the loser's parent that decides, and it does not
// apply when the totals are fuzzily equal (there PG compares startup anyway, in
// the hope of eliminating one path or the other).
func comparePathCostsFuzzily(p1, p2 *Path, fuzz float64) pathCostComparison {
	// Number of disabled nodes, if different, trumps all else (pathnode.c:191).
	if p1.DisabledNodes != p2.DisabledNodes {
		if p1.DisabledNodes < p2.DisabledNodes {
			return costsBetter1
		}
		return costsBetter2
	}

	// Total cost first — more likely to differ (pathnode.c:203).
	if p1.Cost.Total > p2.Cost.Total*fuzz {
		// p1 fuzzily worse on total; if p2 is fuzzily worse on startup they are
		// genuinely different, else p2 dominates.
		if considerPathStartupCost(p1) && p2.Cost.Startup > p1.Cost.Startup*fuzz {
			return costsDifferent
		}
		return costsBetter2
	}
	if p2.Cost.Total > p1.Cost.Total*fuzz {
		if considerPathStartupCost(p2) && p1.Cost.Startup > p2.Cost.Startup*fuzz {
			return costsDifferent
		}
		return costsBetter1
	}
	// Fuzzily equal on total; decide on startup.
	if p1.Cost.Startup > p2.Cost.Startup*fuzz {
		return costsBetter2
	}
	if p2.Cost.Startup > p1.Cost.Startup*fuzz {
		return costsBetter1
	}
	// Both total and startup are fuzzily equal. Use the actual (non-fuzzy)
	// cost to pick a direction rather than returning costsEqual. Returning
	// costsEqual would let a non-cost dimension (typically pathkeys) decide
	// dominance alone — a hash path with no pathkeys then loses to a
	// near-identically-costed merge path with pathkeys. For CTE self-joins
	// with low row-count estimates, both paths land inside the 1% fuzz band
	// and the hash path is silently rejected, leaving only nested-loop
	// plans. A weak actual-cost directional signal preserves the tie-breaking
	// cost comparison that makes the paths incomparable (hash cheaper, merge
	// has pathkeys), so both survive and setCheapest can pick the cheaper
	// one. M0129-S1.
	if p1.Cost.Total < p2.Cost.Total {
		return costsBetter1
	}
	if p2.Cost.Total < p1.Cost.Total {
		return costsBetter2
	}
	// Truly equal on total; use startup as the final tie-break.
	if p1.Cost.Startup < p2.Cost.Startup {
		return costsBetter1
	}
	if p2.Cost.Startup < p1.Cost.Startup {
		return costsBetter2
	}
	return costsEqual
}

// dimensionCmp is a per-dimension comparison: dimBetter1 means path1 is at least
// as good and better on this axis, dimEqual means indistinguishable, dimBetter2
// the reverse, dimIncomparable means neither dominates on this axis.
type dimensionCmp int

const (
	dimEqual dimensionCmp = iota
	dimBetter1
	dimBetter2
	dimIncomparable
)

func costDim(c pathCostComparison) dimensionCmp {
	switch c {
	case costsBetter1:
		return dimBetter1
	case costsBetter2:
		return dimBetter2
	case costsDifferent:
		return dimIncomparable
	default:
		return dimEqual
	}
}

// boolDim compares two eligibility booleans where true is "better" (e.g.
// parallel_safe): true dominates false.
func boolDim(a, b bool) dimensionCmp {
	switch {
	case a == b:
		return dimEqual
	case a && !b:
		return dimBetter1
	default:
		return dimBetter2
	}
}

// outerDim compares required-outer relid sets: a path requiring FEWER outer
// relations (a subset) is less constrained and therefore better; unrelated sets
// are incomparable. Reproduces the bms_subset logic add_path uses for
// parameterized paths (pathnode.c).
func outerDim(a, b RelSet) dimensionCmp {
	if a == b {
		return dimEqual
	}
	aSubB := a&b == a // a ⊆ b
	bSubA := a&b == b // b ⊆ a
	switch {
	case aSubB:
		return dimBetter1 // a is less constrained
	case bSubA:
		return dimBetter2
	default:
		return dimIncomparable
	}
}

// pathRelation is the overall dominance relationship between two paths across all
// four dimensions (cost, pathkeys, parallel_safe, required-outer).
type pathRel int

const (
	relIncomparable pathRel = iota // each better on some axis -> keep both
	relEqual                       // indistinguishable on every axis
	relADominates                  // a is no worse everywhere and better somewhere
	relBDominates
)

// comparePaths reduces the per-dimension comparisons to a single relationship. A
// dominates B iff A is no worse than B on every dimension and strictly better on
// at least one; if A is better on one axis and B on another they are
// incomparable and both survive.
func comparePaths(a, b *Path) pathRel {
	dims := []dimensionCmp{
		costDim(comparePathCostsFuzzily(a, b, stdFuzzFactor)),
		comparePathkeysDim(a.Pathkeys, b.Pathkeys),
		boolDim(a.ParallelSafe, b.ParallelSafe),
		outerDim(a.RequiredOuter, b.RequiredOuter),
	}
	hasA, hasB := false, false
	for _, d := range dims {
		switch d {
		case dimIncomparable:
			return relIncomparable
		case dimBetter1:
			hasA = true
		case dimBetter2:
			hasB = true
		}
	}
	switch {
	case hasA && hasB:
		return relIncomparable // a trade-off across axes
	case hasA:
		return relADominates
	case hasB:
		return relBDominates
	default:
		return relEqual
	}
}

// addPath reproduces add_path (pathnode.c:464): keep newPath unless an existing
// path dominates it (or exactly ties it), and drop any existing path newPath
// dominates. On an exact tie the incumbent is kept and newPath rejected, so
// duplicates do not accumulate — matching PG's practical behaviour of keeping the
// first of two indistinguishable paths.
func addPath(rel *RelOptInfo, newPath *Path, producer string) {
	before := len(rel.Pathlist)
	rel.Pathlist = addToPathlist(rel.Pathlist, newPath)
	// A candidate is accepted when it is present in the resulting list. Length
	// alone is not sufficient — an accepted path can evict several incumbents
	// and SHRINK the list — so the tail is checked instead (addToPathlist
	// appends the survivor last).
	tracePath(rel, newPath, producer, false, pathlistVerdict(rel.Pathlist, newPath, before))
}

// pathlistVerdict reports whether newPath survived addToPathlist.
func pathlistVerdict(list []*Path, newPath *Path, _ int) pathVerdict {
	if len(list) > 0 && list[len(list)-1] == newPath {
		return verdictAccepted
	}
	return verdictDominated
}

// addPartialPath is add_partial_path (pathnode.c:798): the same dominance pruning
// over the partial pathlist, used for parallel candidates (design ch. 08 §2).
// Present now; exercised from C5.
func addPartialPath(rel *RelOptInfo, newPath *Path, producer string) {
	before := len(rel.PartialPathlist)
	rel.PartialPathlist = addToPathlist(rel.PartialPathlist, newPath)
	// The partial list is traced too: `parallelism` is one of the nine
	// divergence classes the parity work tracks, so a provenance channel that
	// covered only addPath could not answer whether a partial path was ever
	// offered.
	tracePath(rel, newPath, producer, true, pathlistVerdict(rel.PartialPathlist, newPath, before))
}

func addToPathlist(list []*Path, newPath *Path) []*Path {
	// If any existing path dominates or exactly ties the newcomer, reject it and
	// leave the list untouched (a rejected path cannot itself dominate anything,
	// by transitivity of the dominance order).
	for _, old := range list {
		switch comparePaths(newPath, old) {
		case relBDominates, relEqual:
			return list
		}
	}
	// The newcomer survives; drop every incumbent it strictly dominates.
	survivors := list[:0]
	for _, old := range list {
		if comparePaths(newPath, old) != relADominates {
			survivors = append(survivors, old)
		}
	}
	return append(survivors, newPath)
}

// costSelector is PG's CostSelector (pathnodes.h): which axis compare_path_costs
// orders on first.
type costSelector int

const (
	totalCost costSelector = iota
	startupCost
)

// comparePathCosts is compare_path_costs (pathnode.c:69): an EXACT (unfuzzed)
// three-way order, unlike comparePathCostsFuzzily above. disabled_nodes trumps
// all else; then the selected axis; then the other axis as the tie-break.
//
// Both comparators exist in PG and they are not interchangeable: add_path uses
// the fuzzy one so that near-ties are decided on the non-cost dimensions, while
// set_cheapest uses this exact one so that a genuinely equal cost falls through
// to the pathkey tie-break rather than being swallowed by the fuzz band.
func comparePathCosts(p1, p2 *Path, criterion costSelector) int {
	if p1.DisabledNodes != p2.DisabledNodes {
		if p1.DisabledNodes < p2.DisabledNodes {
			return -1
		}
		return +1
	}
	first, second := p1.Cost.Total, p2.Cost.Total
	tieFirst, tieSecond := p1.Cost.Startup, p2.Cost.Startup
	if criterion == startupCost {
		first, second = p1.Cost.Startup, p2.Cost.Startup
		tieFirst, tieSecond = p1.Cost.Total, p2.Cost.Total
	}
	switch {
	case first < second:
		return -1
	case first > second:
		return +1
	case tieFirst < tieSecond:
		return -1
	case tieFirst > tieSecond:
		return +1
	}
	return 0
}

// setCheapest reproduces set_cheapest (pathnode.c:272) INCLUDING its
// parameterisation discipline (leftdeep-joins 03 §9 rule 1), which the previous
// test-only version did not have:
//
//   - CheapestStartup and CheapestTotal are chosen among UNPARAMETERISED paths
//     only. A parameterised path cannot stand in for the rel in general — it
//     only produces rows once some particular outer relation supplies its
//     parameter — so handing one to a join that cannot supply that parameter
//     produces a plan that cannot be built.
//   - CheapestTotal falls back to the best (cheapest of the LEAST
//     parameterised) parameterised path when there is no unparameterised path
//     at all, since the rel must still have a representative. CheapestStartup
//     does NOT: PG leaves it nil in that case.
//   - CheapestParameterized collects every parameterised survivor, with the
//     cheapest unparameterised path prepended (PG's `lcons`, :375).
//
// This ordering is not optional and it is why the discipline lands BEFORE the
// paths: the moment a RequiredOuter path enters a pathlist, a parameterisation-
// blind minimum would let it win CheapestTotal and be handed to a join that
// cannot bind it. The dominance side was already parameterisation-aware
// (addPath's outerDim) — selection was the only gap.
//
// Ties are broken PG's way: on an exactly equal cost the better-sorted path
// wins (:358-369), and failing that the earliest-added one, so plan choice
// stays stable across runs.
//
// An empty pathlist is where PG elogs "could not devise a query plan"
// (:282). goopg's callers check that themselves before calling — level 1 in
// buildInitialRels always adds a path first, and joinSearch rejects an empty
// joinrel pathlist explicitly (joinsearchlevel.go:110-112) — so this leaves
// every field nil and lets addPathsToJoinrel's loud nil-cheapest error be the
// single place the failure is reported.
func setCheapest(rel *RelOptInfo) {
	var cheapestStartup, cheapestTotal, bestParam *Path
	var parameterized []*Path

	for _, path := range rel.Pathlist {
		if path.RequiredOuter != 0 {
			parameterized = append(parameterized, path)
			// Once an unparameterised cheapest-total exists the best
			// parameterised path is no longer needed (pathnode.c:301-305).
			if cheapestTotal != nil {
				continue
			}
			if bestParam == nil {
				bestParam = path
				continue
			}
			// Least parameterised wins; among equal parameterisations, the
			// cheaper total (pathnode.c:312-334, bms_subset_compare).
			switch outerDim(path.RequiredOuter, bestParam.RequiredOuter) {
			case dimEqual:
				if comparePathCosts(path, bestParam, totalCost) < 0 {
					bestParam = path
				}
			case dimBetter1:
				bestParam = path
			default:
				// dimBetter2: the incumbent is less parameterised, keep it.
				// dimIncomparable: neither has the least possible
				// parameterisation for this rel, so PG sits on the old path
				// until something better comes along (:335-343).
			}
			continue
		}

		if cheapestTotal == nil {
			cheapestStartup, cheapestTotal = path, path
			continue
		}
		if cmp := comparePathCosts(cheapestStartup, path, startupCost); cmp > 0 ||
			(cmp == 0 && comparePathkeysDim(cheapestStartup.Pathkeys, path.Pathkeys) == dimBetter2) {
			cheapestStartup = path
		}
		if cmp := comparePathCosts(cheapestTotal, path, totalCost); cmp > 0 ||
			(cmp == 0 && comparePathkeysDim(cheapestTotal.Pathkeys, path.Pathkeys) == dimBetter2) {
			cheapestTotal = path
		}
	}

	if cheapestTotal != nil {
		// PG prepends, so the list stays "cheapest unparameterised first".
		parameterized = append([]*Path{cheapestTotal}, parameterized...)
	} else {
		cheapestTotal = bestParam
	}

	rel.CheapestStartup = cheapestStartup
	rel.CheapestTotal = cheapestTotal
	rel.CheapestParameterized = parameterized
}

// disabledNodesFor is PG 18's `disabled_nodes` accumulation
// (pathnode.c: a path's count is the sum of its children's plus one if this
// node's method is disabled by an enable_* GUC).
//
// PG deliberately does NOT skip the producer: a disabled method still yields a
// path, so a query whose only legal plan needs that method still plans. The
// dominance order in comparePathCosts prefers fewer disabled nodes before it
// looks at cost, which is what makes the GUC a strong preference rather than a
// prohibition. take2 P2-05.
func disabledNodesFor(disabled bool, children ...*Path) int {
	n := 0
	for _, c := range children {
		if c != nil {
			n += c.DisabledNodes
		}
	}
	if disabled {
		n++
	}
	return n
}
