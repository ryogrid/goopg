package optimizer

// M0127-P5.4b-ii-b-2 — `get_memoize_path` (joinpath.c:674): the cache that turns
// a repeated index probe into a lookup, decided where PG decides it — in path
// generation, against a cost — rather than as a post-hoc opinion about a tree
// that was already chosen.
//
// PG oracle: `get_memoize_path` (joinpath.c:674), `create_memoize_path`
// (pathnode.c:1671), `cost_memoize_rescan` (costsize.c:2541) and the
// `ExecEstimateCacheEntryOverheadBytes` it calls (nodeMemoize.c:1172). Design:
// leftdeep-joins 03 §5.2.
//
// # Why this is a PATH and not an attachment
//
// goopg already had a Memoize insertion rule — `maybeAttachMemoize`
// (memoize.go) — and it runs on a BUILT `*NestedLoopIndexJoin`, after
// `rewriteJoinsToNLI` has chosen the method. That is exactly what it can be for
// the legacy enumerator, whose join method is decided by rules rather than by a
// cost comparison. It is exactly what it must NOT be for the searched arm:
// `walkRewriteNLI` skips a searched subtree (nl_index_join.go:110), so a
// searched NLI never reached it at all, and bolting it on at `createPlan` time
// would make the executed plan cheaper than the plan the search costed — the
// costed-≠-executed class this architecture exists to kill (06 §2.1). A cache
// that only ever makes an NLI faster still changes which METHOD wins: the whole
// point of memoizing is that the NLI beats a hash join it would otherwise lose
// to, and that comparison happens in `addPath` or not at all.
//
// So the cache is a path. `getMemoizePath` wraps the parameterised inner, prices
// the rescan, and `addNLIPaths` offers BOTH the wrapped and the unwrapped
// candidate to `addPath` — which is `match_unsorted_outer`'s own shape
// (joinpath.c:1960-1975 tries `memoize_path` first and falls back to the plain
// inner).
//
// # What a stats-free server does, and why that is the safety property
//
// `cost_memoize_rescan`'s ndistinct comes from `estimate_num_groups`, and when
// THAT fell back on a default PG replaces it with `calls` — one distinct
// parameter set per call — precisely so "we do not know" cannot buy a cache
// (costsize.c:2592, "it's a bit too risky"). goopg reaches the same place
// through `getVariableNumDistinct`'s `isdefault` return (P5.6-a), and the
// arithmetic then gives `hitRatio == 0`, so the wrapped path costs the
// unwrapped path plus the caching charges and can never win `addPath`.
//
// That is what bounds this slice's blast radius, but only where the statistics
// are genuinely absent, and the two gates differ:
//
//   - the TPC-H spot-check runs a fresh capped server and never ANALYZEs, and
//     goopg's in-session ANALYZE statistics do not survive a restart, so every
//     candidate there takes the isdefault arm and no plan can move;
//   - the TPC-DS SF0.5 cluster is a different case since M0125-0028/-0029: its
//     per-column statistics and `reltuples` DO survive a restart through the
//     goopg-private sidecar, so the sweep plans WITH statistics and a Memoize
//     path can genuinely win there. That gate is load-bearing for this slice,
//     not a formality.

import (
	"math"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// memoizePathInfo is the `MemoizePath`-only payload carried on `Path`. Both
// fields come out of one `costMemoizeRescan` call because PG computes them
// together (costsize.c:2604 sets `est_entries` inside the cost function, with
// its own apology for doing so) — and they must stay together, since an entry
// count computed from one ndistinct beside a rescan cost computed from another
// would size a cache the cost model never priced.
type memoizePathInfo struct {
	// estEntries is `MemoizePath.est_entries`: the hash-table size the executor
	// should start at, `min(ndistinct, estCacheEntries)`. It becomes
	// `Memoize.EstEntries` at `createPlan` time.
	estEntries int64
	// rescan is `cost_rescan`'s answer for this node: what ONE re-execution of
	// the cached inner costs, which is the number `nestloopCost` charges per
	// outer row. It is NOT the path's own `Cost` — that stays the subpath's,
	// exactly as `create_memoize_path` sets it (pathnode.c:1697-1698), because
	// the FIRST execution is a guaranteed miss and costs the full probe.
	rescan Cost
}

// memoizeEntryOverheadBytes is `ExecEstimateCacheEntryOverheadBytes`
// (nodeMemoize.c:1172) at goopg's shapes: PG charges one `MemoizeEntry` plus one
// `MemoizeKey` per cache entry and one `MemoizeTuple` per tuple inside it.
//
// goopg's cache entry (`memoizeOp`, operators_memoize.go) is a `kvcache` entry
// holding a key slice and a `[]Row`, so the same three costs exist with Go's
// sizes rather than C's. The per-TUPLE part is the one that matters for the
// estimate — it is what makes a wide, many-row inner expensive to cache — and it
// is a slice header plus the `[]Datum` backing array's own header, since the
// Datum bytes themselves are counted separately by `hashsize.EntryBytes`.
const (
	memoizeEntryFixedBytes  = 96.0
	memoizeTupleFixedBytes  = 48.0
	memoizeMinEntryBytes    = 1.0
	memoizeMaxEstEntriesPG  = float64(1<<32 - 1) // PG's PG_UINT32_MAX clamp
	memoizeMinCacheEntries  = 1.0
	memoizeMinCallsForCache = 2.0 // `outer_path->parent->rows < 2` (joinpath.c:696)
)

func memoizeEntryOverheadBytes(tuples float64) float64 {
	return memoizeEntryFixedBytes + memoizeTupleFixedBytes*tuples
}

// costMemoizeRescan is `cost_memoize_rescan` (costsize.c:2541), transcribed.
//
// Three substitutions, each because goopg does not have the input PG reads:
//
//   - `relation_byte_size(tuples, width)` needs a per-column average width,
//     which goopg has no statistic for (ledger 2026-08-03 M0127-P3.1). The
//     substitute is `hashsize.EntryBytes(ncols, 0)` — the SAME function the hash
//     join's sizing goes through, so a cached row and a hashed row are measured
//     by one ruler and not two.
//   - `get_expr_width` per cache key, likewise: the keys are counted as columns
//     through the same function.
//   - `estimate_num_groups` over the param exprs is replaced by the caller's
//     ndistinct, clamped to `calls`. The clamp is not a simplification: PG's
//     `estimate_num_groups` clamps its answer to `input_rows` too
//     (selfuncs.c:3915), and without it a column with more distinct values than
//     the outer has rows would give a NEGATIVE hit ratio, which is the condition
//     PG asserts against at :2624.
//
// `isDefaultND` is PG's `SELFLAG_USED_DEFAULT` (:2592): a guessed ndistinct is
// replaced by `calls`, which drives the hit ratio to zero and makes the wrapped
// path strictly more expensive than the one it wraps.
func costMemoizeRescan(cp costParams, inner Cost, tuples, calls, ndistinct float64, isDefaultND bool, ncols, nkeys int) (Cost, int64) {
	if calls < 1 {
		calls = 1
	}
	if tuples < 0 {
		tuples = 0
	}

	estEntryBytes := hashsize.EntryBytes(ncols, 0)*tuples +
		memoizeEntryOverheadBytes(tuples) +
		hashsize.EntryBytes(nkeys, 0)
	if estEntryBytes < memoizeMinEntryBytes {
		estEntryBytes = memoizeMinEntryBytes
	}
	estCacheEntries := math.Floor(float64(hashsize.EffectiveMemLimit(cp.workMem)) / estEntryBytes)
	if estCacheEntries < memoizeMinCacheEntries {
		estCacheEntries = memoizeMinCacheEntries
	}

	if isDefaultND {
		ndistinct = calls
	}
	if ndistinct > calls {
		ndistinct = calls
	}
	if ndistinct < 1 {
		ndistinct = 1
	}

	evictRatio := 1.0 - math.Min(estCacheEntries, ndistinct)/ndistinct
	hitRatio := ((calls - ndistinct) / calls) *
		(estCacheEntries / math.Max(ndistinct, estCacheEntries))
	// PG asserts this range rather than clamping; goopg clamps, because the
	// inputs above are estimates from a statistics store that a user can make
	// arbitrary with a hand-written `pg_statistic` row, and a planner must not
	// panic on one.
	hitRatio = math.Max(0, math.Min(1, hitRatio))

	total := inner.Total*(1.0-hitRatio) + cp.cpuOperatorCost
	total += cp.cpuTupleCost * evictRatio
	total += cp.cpuOperatorCost / 10.0 * evictRatio * tuples
	total += cp.cpuTupleCost + cp.cpuOperatorCost*tuples

	startup := inner.Startup*(1.0-hitRatio) + cp.cpuTupleCost

	est := math.Min(math.Min(ndistinct, estCacheEntries), memoizeMaxEstEntriesPG)
	return Cost{Startup: startup, Total: total}, int64(est)
}

// getMemoizePath is `get_memoize_path` (joinpath.c:674): the eligibility
// gauntlet, then the cost. It returns nil — PG's NULL — when the cache is not
// usable, and the caller simply proceeds with the unwrapped inner.
//
// PG's gates, in PG's order, with the ones that are vacuous in goopg's searched
// shape named rather than dropped (a vacuous gate that is not written down is
// indistinguishable from a forgotten one the day 03 §4.4's pin is relaxed):
//
//  1. `enable_memoize` — goopg's `memoizeOn`, the SAME switch the legacy
//     insertion rule reads, so `SET enable_memoize = off` kills both arms.
//  2. `outer_path->parent->rows < 2` (:696): with one outer row every probe is
//     a first probe and a cache is pure overhead.
//  3. LATERAL cache keys (`extract_lateral_vars_from_PHVs`, :704): goopg's
//     searched shape has no LATERAL — 03 §4.4 pins every LATERAL construct
//     outside the search as an opaque initial rel — so the lateral half of the
//     key set is empty and the parameterised clauses are the whole of it.
//  4. no cache key at all (:711): for goopg that is an inner with no
//     `IndexClauses`, i.e. an unparameterised path, which `addNLIPaths` has
//     already excluded.
//  5. SEMI/ANTI without `inner_unique` (:726): vacuous while §4.4 admits only
//     INNER joins into the search. Written as a real test anyway, against the
//     joinrel's own type, so relaxing the pin does not silently admit the shape
//     PG refuses here.
//  6. the `inner_unique` ppi-serial coverage test (:752): goopg's search never
//     marks a join inner-unique, so PG's arm is unreachable from the false
//     branch. Ledgered.
//  7. volatile functions in the inner's target list, base restrictions, or
//     parameterised clauses (:773-799): subsumed by the key-shape test below —
//     every cache key must be a bare `*ColumnRef`, and a column reference is
//     never volatile — together with the fact that goopg's parameterised index
//     leaf is a bare `*IndexScan` with no filter of its own
//     (`addParameterizedIndexPaths` declines a wrapped leaf, P5.5-e-ii-b).
//  8. `paraminfo_get_equal_hashops` (:802): every key must be hashable. goopg's
//     memoize cache keys on `Datum` values through `kvcache`, and the shape it
//     can key on is the bare outer column — which is the same test
//     `maybeAttachMemoize` applies (memoize.go:106-110), deliberately, so the
//     legacy and searched arms cannot disagree about what is cacheable.
//
// PG's `binary_mode` has no goopg counterpart: `kvcache` compares Datums by
// their encoded form, which is the binary comparison PG switches into, so goopg
// is unconditionally in the strict mode and never in the "logical" one. That is
// a superset of what PG guarantees here, never a subset. Ledgered.
func getMemoizePath(s *searchCtx, outer *RelOptInfo, outerPath, innerPath *Path, cp costParams) *Path {
	if !memoizeOn.Load() {
		return nil
	}
	if outerPath == nil || innerPath == nil || outer == nil {
		return nil
	}
	// Gate 2. PG reads the outer REL's rows (`outer_path->parent->rows`), not
	// the path's, and the difference is real for a parameterised outer — which
	// `addNLIPaths` has already refused, so the two agree here. The rel is used
	// because that is what PG uses.
	if outer.Rows < memoizeMinCallsForCache {
		return nil
	}
	// Gate 5 CANNOT BE WRITTEN, and that is a finding rather than an omission.
	// `RelOptInfo` carries a relset, a cardinality and pathlists — there is no
	// join-type field on it, because 03 §4.4 pins every non-INNER construct
	// outside the search and a searched joinrel has therefore never needed to
	// say what kind of join produced it. This is the same shape P5.9-s recorded
	// on the joinlist. A SEMI/ANTI test here would be a test against a constant.
	// The ledger row names `RelOptInfo` as the resume point: the type must be
	// recorded on the joinrel before this gate is expressible, and until then
	// the gate is discharged by construction, not by code.
	//
	// Gates 4, 7 and 8: the cache keys are the probe's own bound expressions,
	// and each must be a bare column of the outer.
	keys, ok := memoizeCacheKeys(s, innerPath, outer.Relids)
	if !ok {
		return nil
	}

	ndistinct, isDefault := memoizeKeyNDistinct(s, innerPath, outer.Relids)
	rescan, est := costMemoizeRescan(cp, innerPath.Cost, innerPath.Rows, outer.Rows,
		ndistinct, isDefault, relNCols(innerPath.Rel), len(keys))

	return &Path{
		Kind: PathMemoize,
		// The wrapper stands for the same relation, the same rows and the same
		// parameterisation as what it wraps — `create_memoize_path`
		// (pathnode.c:1690-1698) copies `parent`, `param_info`, `rows` and both
		// costs from the subpath. Only the RESCAN cost differs, and that is the
		// number this path exists to carry.
		Rel:           innerPath.Rel,
		Rows:          innerPath.Rows,
		Cost:          innerPath.Cost,
		Pathkeys:      innerPath.Pathkeys,
		RequiredOuter: innerPath.RequiredOuter,
		Children:      []*Path{innerPath},
		MemoizeInfo:   &memoizePathInfo{estEntries: est, rescan: rescan},
	}
}

// memoizeCacheKeys is `paraminfo_get_equal_hashops` (joinpath.c:438) reduced to
// the one key shape goopg's cache can hash: a bare column of the outer.
//
// It returns the key expressions in INDEX-COLUMN order — the order
// `Path.IndexClauses` is already in, and the order `createPlan` builds
// `IndexScan.Keys` in — so the cache key tuple and the probe key tuple are the
// same list read twice rather than two lists that could disagree about which
// value belongs to which column.
//
// Duplicate expressions are NOT collapsed. PG's `list_member` check (:503) does
// collapse them, because a repeated cache key adds nothing to the key's
// identity; goopg keeps them, because the list doubles as the probe's key list
// where position IS the index column, and a collapsed list would bind the wrong
// column. The cost of the difference is a marginally larger key, never a wrong
// answer — and `costMemoizeRescan` is handed `len(keys)` so it prices the list
// it will actually build.
func memoizeCacheKeys(s *searchCtx, innerPath *Path, outerRelids RelSet) ([]Expr, bool) {
	if innerPath == nil || len(innerPath.IndexClauses) == 0 {
		return nil, false
	}
	keys := make([]Expr, 0, len(innerPath.IndexClauses))
	for _, c := range innerPath.IndexClauses {
		cr, ok := c.key.(*ColumnRef)
		if !ok {
			return nil, false
		}
		// The key must be supplied by the OUTER. `addNLIPaths` has already
		// established that the inner's whole parameterisation is contained in
		// the outer's relset, so this is a restatement — but it is the
		// restatement that makes "the cache key is an outer column" a checked
		// fact rather than an inherited assumption, and `memoizeKeyNDistinct`
		// below reads statistics on the strength of it.
		if _, _, resolved := s.resolveJoinVarColumn(cr, memoizeKeyRelids(c, outerRelids)); !resolved {
			return nil, false
		}
		keys = append(keys, cr)
	}
	return keys, true
}

// memoizeKeyRelids answers which relation a probe key belongs to, by taking the
// side of the clause that the outer supplies.
//
// It is a lookup rather than a `pull_varnos`: `restrictInfo` already carries the
// per-side relsets the whole search reasons with, so re-deriving them from the
// expression would create a second answer that could disagree with the one
// `addNLIPaths` admitted the pair on.
func memoizeKeyRelids(c indexPathClause, outerRelids RelSet) RelSet {
	if c.ri == nil {
		return 0
	}
	if relsSubset(c.ri.leftRelids, outerRelids) && c.ri.leftRelids != 0 {
		return c.ri.leftRelids
	}
	if relsSubset(c.ri.rightRelids, outerRelids) && c.ri.rightRelids != 0 {
		return c.ri.rightRelids
	}
	return 0
}

// memoizeKeyNDistinct is `estimate_num_groups` over the cache keys, at the
// fidelity `getVariableNumDistinct` (P5.6-a) provides.
//
// The combination rule for a multi-column key is PG's own and is worth naming
// because it reads backwards: `estimate_num_groups` MULTIPLIES the per-column
// counts (selfuncs.c:3861), on the assumption that the columns are independent,
// and then clamps to the input row count. Multiplying is the CONSERVATIVE
// direction here — more distinct parameter sets means a lower hit ratio means a
// less attractive cache — which is the opposite of its effect on a GROUP BY
// estimate, and is why the same formula is right in both places.
//
// The second return is PG's `SELFLAG_USED_DEFAULT`, and it is sticky: if ANY
// key column fell back on a default, the whole estimate is a guess and
// `costMemoizeRescan` replaces it with `calls`.
func memoizeKeyNDistinct(s *searchCtx, innerPath *Path, outerRelids RelSet) (float64, bool) {
	nd := 1.0
	anyDefault := false
	for _, c := range innerPath.IndexClauses {
		v := s.examineJoinVar(c.key, memoizeKeyRelids(c, outerRelids))
		colND, isDefault := getVariableNumDistinct(v)
		if isDefault {
			anyDefault = true
		}
		nd *= colND
	}
	if nd < 1 {
		nd = 1
	}
	return nd, anyDefault
}

// pathRescanTotal is `cost_rescan` (costsize.c:4700) reduced to the two cases
// goopg's searched inner paths can be: a memoize wrapper, whose rescan is the
// cached one, and everything else, which caches nothing between rescans and
// therefore costs its own total every time (costsize.c:4577, the default arm PG
// takes for an index scan).
//
// It exists so `addNLIPaths` has ONE expression for "what does re-running this
// inner cost", rather than a conditional at each call site that could be
// updated in one place and not the other.
func pathRescanTotal(p *Path) float64 {
	if p == nil {
		return 0
	}
	if p.Kind == PathMemoize && p.MemoizeInfo != nil {
		return p.MemoizeInfo.rescan.Total
	}
	return p.Cost.Total
}
