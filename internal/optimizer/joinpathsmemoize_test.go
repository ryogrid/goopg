package optimizer

// M0127-P5.4b-ii-b-2 — Memoize as a PATH (joinpathsmemoize.go) and its
// consumer, the NLI arm of `createPlan` (createplannl.go).
//
// The searched join search is ON by default since P5.9, so unlike the earlier
// P5.4b slices these tests guard live behaviour. What they pin, in order:
//
//   - the SAFETY property the slice's blast radius rests on — a server with no
//     ANALYZE statistics prices the cache strictly ABOVE the probe it wraps, so
//     it can never win `addPath` and no plan on a fresh server can move;
//   - that with statistics the cache is priced BELOW the probe, i.e. the
//     arithmetic is actually the hit-ratio one and not a constant;
//   - that `addNLIPaths` offers both candidates and `addPath` picks, rather
//     than the arm deciding;
//   - that the built `*NestedLoopIndexJoin` keys its cache on the SAME
//     expression objects it probes with — the one way a Memoize can return
//     wrong rows instead of merely being useless;
//   - and that a `PathMemoize` reaching `createPlanNode` on its own is a loud
//     failure, because goopg's cache has no free-standing node.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// memoTestCtx is a two-relation search whose rel 0 (`dim`) is the outer and rel
// 1 (`fact`) the inner. `outerNDistinctFrac` is the statistic the whole slice
// turns on: the fraction of `dim.k`'s rows that are distinct, hence how often a
// probe repeats a parameter set already cached.
//
// A zero `outerNDistinctFrac` means "analysed, but no ndistinct recorded",
// which is the shape a fresh goopg server presents for every column.
func memoTestCtx(t *testing.T, outerRows int64, outerNDistinctFrac float64, analysed bool) *searchCtx {
	t.Helper()
	c := catalog.NewInMemory()
	dim, err := c.CreateTable(parser.ObjectName{Name: "dim"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if analysed {
		dim.Stats = &catalog.TableStats{
			RowCount: outerRows,
			Columns:  []catalog.ColumnStats{{NDistinctFrac: outerNDistinctFrac}},
			Analyzed: true,
		}
	}
	fact, err := c.CreateTable(parser.ObjectName{Name: "fact"}, []catalog.Column{
		{Name: "fk", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := newSearchCtx(2, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: dim, baseRows: outerRows},
		{table: fact, baseRows: 1_000_000},
	}
	return s
}

// memoInnerRel is the parameterised inner the arm probes: one index path bound
// by `param`, whose single index clause names the OUTER column `dim.k` as the
// value it binds — which is what makes it a cache key.
func memoInnerRel(relids RelSet, param RelSet, probeCost float64) (*RelOptInfo, *ColumnRef) {
	rel := newRelOptInfo(relids, 1_000_000, 32)
	generateScanPaths(rel, defaultCostParams(), estScanPages(1_000_000, 32), 0, 0, true)
	key := &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}
	ri := &restrictInfo{
		relids:      param | relids,
		leftRelids:  param,
		rightRelids: relids,
		ecID:        noEquivClass,
	}
	addPath(rel, &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		Rows:          1,
		Cost:          Cost{Total: probeCost},
		IndexClauses:  []indexPathClause{{ri: ri, indexCol: 0, key: key}},
		RequiredOuter: param,
	}, "test")
	setCheapest(rel)
	return rel, key
}

// TestMemoizeWithoutStatisticsIsStrictlyMoreExpensive is the property that
// bounds this slice on a stats-free server — the TPC-H spot-check's fresh
// capped cluster, which never ANALYZEs and whose in-session statistics do not
// survive a restart, so every cache key there takes `getVariableNumDistinct`'s
// default arm. (It does NOT cover the TPC-DS SF0.5 cluster, which persists
// per-column statistics since M0125-0028/-0029 and can genuinely choose a
// cache.)
//
// PG makes the same choice explicitly (costsize.c:2592, "a bit too risky"):
// a defaulted ndistinct is replaced by `calls`, one distinct parameter set per
// call, so the hit ratio is exactly zero and the wrapper costs the wrapped path
// plus every caching charge.
func TestMemoizeWithoutStatisticsIsStrictlyMoreExpensive(t *testing.T) {
	cp := defaultCostParams()
	inner := Cost{Startup: 1, Total: 100}
	rescan, est := costMemoizeRescan(cp, inner, 1, 10000, 200, true, 4, 1)
	if rescan.Total <= inner.Total {
		t.Fatalf("rescan total %.4f <= probe total %.4f; a defaulted ndistinct must never buy a discount",
			rescan.Total, inner.Total)
	}
	if est < 1 {
		t.Fatalf("est_entries %d; the cache must still be sized even when it will not be chosen", est)
	}
}

// TestMemoizeWithStatisticsPricesTheHitRatio: the mirror of the test above. The
// same inner, the same call count, but a key with 200 distinct values across
// 10,000 calls — so 98% of probes repeat a parameter set — must price BELOW the
// probe. Without this the previous test would also pass on an implementation
// that always returned a penalty.
func TestMemoizeWithStatisticsPricesTheHitRatio(t *testing.T) {
	cp := defaultCostParams()
	inner := Cost{Startup: 1, Total: 100}
	rescan, est := costMemoizeRescan(cp, inner, 1, 10000, 200, false, 4, 1)
	if rescan.Total >= inner.Total {
		t.Fatalf("rescan total %.4f >= probe total %.4f; a 98%% hit ratio must be cheaper than probing",
			rescan.Total, inner.Total)
	}
	if est != 200 {
		t.Fatalf("est_entries %d, want 200 (min(ndistinct, capacity))", est)
	}
}

// TestMemoizeNDistinctClampedToCalls: PG's `estimate_num_groups` clamps its
// answer to the input row count (selfuncs.c), and `cost_memoize_rescan` relies
// on that clamp — without it a key with more distinct values than the outer has
// rows drives `hit_ratio` negative, which is the condition PG asserts against
// at costsize.c:2624.
func TestMemoizeNDistinctClampedToCalls(t *testing.T) {
	cp := defaultCostParams()
	inner := Cost{Startup: 1, Total: 100}
	rescan, _ := costMemoizeRescan(cp, inner, 1, 100, 1_000_000, false, 4, 1)
	if rescan.Total < inner.Total {
		t.Fatalf("rescan total %.4f < probe total %.4f; an unclamped ndistinct produced a negative hit ratio",
			rescan.Total, inner.Total)
	}
	if rescan.Startup < 0 || rescan.Total < 0 {
		t.Fatalf("negative rescan cost %+v", rescan)
	}
}

// TestGetMemoizePathGates walks PG's eligibility gauntlet, one refusal per
// case. Each is a case where a cache would be built that the executor could not
// key on, or could key on but would never hit.
func TestGetMemoizePathGates(t *testing.T) {
	cp := defaultCostParams()
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)
	s := memoTestCtx(t, 10000, 0.02, true)

	newOuter := func(rows float64) *RelOptInfo {
		return scanRel(outerRelids, rows, estScanPages(rows, 32))
	}

	t.Run("switch off", func(t *testing.T) {
		prev := MemoizeEnabled()
		SetMemoizeEnabled(false)
		defer SetMemoizeEnabled(prev)
		outer := newOuter(10000)
		inner, _ := memoInnerRel(innerRelids, outerRelids, 1)
		if p := getMemoizePath(s, outer, outer.CheapestTotal, inner.CheapestParameterized[1], cp); p != nil {
			t.Fatal("enable_memoize = off must suppress the path")
		}
	})

	t.Run("single outer row", func(t *testing.T) {
		// joinpath.c:696 — every probe is a first probe, so the cache is pure
		// overhead.
		outer := newOuter(1)
		inner, _ := memoInnerRel(innerRelids, outerRelids, 1)
		if p := getMemoizePath(s, outer, outer.CheapestTotal, inner.CheapestParameterized[1], cp); p != nil {
			t.Fatal("an outer of one row must not get a cache")
		}
	})

	t.Run("no search context", func(t *testing.T) {
		// The degradation `addPathsToJoinrel`'s doc comment promises: with no
		// `relInfos` there is no statistic behind any key, so no cache is
		// offered rather than one being offered on a guess.
		outer := newOuter(10000)
		inner, _ := memoInnerRel(innerRelids, outerRelids, 1)
		if p := getMemoizePath(nil, outer, outer.CheapestTotal, inner.CheapestParameterized[1], cp); p != nil {
			t.Fatal("a nil search context must not produce a cache path")
		}
	})

	t.Run("non-column cache key", func(t *testing.T) {
		// `paraminfo_get_equal_hashops` (joinpath.c:438) refuses a key it
		// cannot hash; goopg's cache keys on bare outer columns, so anything
		// else is refused at the same place.
		outer := newOuter(10000)
		inner, _ := memoInnerRel(innerRelids, outerRelids, 1)
		ip := inner.CheapestParameterized[1]
		ip.IndexClauses[0].key = &IntegerConst{}
		if p := getMemoizePath(s, outer, outer.CheapestTotal, ip, cp); p != nil {
			t.Fatal("a non-column cache key must be refused")
		}
	})

	t.Run("eligible", func(t *testing.T) {
		outer := newOuter(10000)
		inner, _ := memoInnerRel(innerRelids, outerRelids, 1)
		ip := inner.CheapestParameterized[1]
		p := getMemoizePath(s, outer, outer.CheapestTotal, ip, cp)
		if p == nil {
			t.Fatal("the eligible case produced no path; the gauntlet is refusing everything")
		}
		if p.Kind != PathMemoize || p.MemoizeInfo == nil {
			t.Fatalf("got kind %v, MemoizeInfo %v", p.Kind, p.MemoizeInfo)
		}
		// `create_memoize_path` copies the subpath's own costs (pathnode.c:1690)
		// — the FIRST execution is a guaranteed miss and costs a full probe.
		// Only the rescan differs.
		if p.Cost != ip.Cost || p.Rows != ip.Rows || p.RequiredOuter != ip.RequiredOuter {
			t.Fatalf("wrapper must carry the subpath's cost/rows/parameterisation, got %+v", p)
		}
		if pathRescanTotal(p) >= pathRescanTotal(ip) {
			t.Fatalf("rescan %.4f not below the bare probe's %.4f", pathRescanTotal(p), pathRescanTotal(ip))
		}
	})
}

// TestNLIArmOffersBothCandidates: `match_unsorted_outer` hands the bare inner
// AND the cached one to `try_nestloop_path` (joinpath.c:1965-1986) and lets
// `add_path` decide. The arm must not decide for it — that is the whole
// difference between this and `maybeAttachMemoize`, which attaches a cache to a
// method that was already chosen.
func TestNLIArmOffersBothCandidates(t *testing.T) {
	cp := defaultCostParams()
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)
	s := memoTestCtx(t, 10000, 0.02, true)

	outer := scanRel(outerRelids, 10000, estScanPages(10000, 32))
	inner, _ := memoInnerRel(innerRelids, outerRelids, indexProbeCost(cp))
	joinrel := newRelOptInfo(outerRelids|innerRelids, 10000, 64)
	addNLIPaths(s, joinrel, outer, inner, cp, nil)

	if len(joinrel.Pathlist) != 1 {
		t.Fatalf("got %d paths, want 1 survivor of the two candidates", len(joinrel.Pathlist))
	}
	won := joinrel.Pathlist[0]
	if won.Children[1].Kind != PathMemoize {
		t.Fatal("the cached candidate must win when 98% of probes repeat a key")
	}

	// The negative control, and the one that gives the assertion above teeth:
	// the same fixture with no statistics must leave the BARE probe standing.
	sBlind := memoTestCtx(t, 10000, 0, false)
	blindRel := newRelOptInfo(outerRelids|innerRelids, 10000, 64)
	blindInner, _ := memoInnerRel(innerRelids, outerRelids, indexProbeCost(cp))
	addNLIPaths(sBlind, blindRel, outer, blindInner, cp, nil)
	if len(blindRel.Pathlist) != 1 {
		t.Fatalf("got %d paths, want 1", len(blindRel.Pathlist))
	}
	if k := blindRel.Pathlist[0].Children[1].Kind; k != PathIndexScan {
		t.Fatalf("without statistics the bare probe must win, got inner kind %v", k)
	}
}

// TestCreatePlanMemoizeKeysAreTheProbeKeys is the correctness assertion of the
// slice. `memoizeOp` evaluates `KeyExprs` against the same bound outer slot
// `indexScanOp.Rescan` evaluates `IndexScan.Key` against, so if the two lists
// were derived separately the cache could be keyed on one column and probed on
// another — a cache that returns the WRONG rows, not a slow one.
func TestCreatePlanMemoizeKeysAreTheProbeKeys(t *testing.T) {
	cp := defaultCostParams()
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)
	_ = cp
	_, _ = outerRelids, innerRelids

	// The consumer is exercised in isolation, over the same leaf fixtures the
	// rest of the NLI arm's `createPlan` tests use: relid 1 (`b`) is the OUTER
	// and occupies binding columns 2-4, relid 0 (`a`) the parameterised inner.
	// The probe binds `b.b1` — binding column 3, OUTER position 1 — so a cache
	// key that was re-derived in merged coordinates instead of copied would be
	// a different expression object and the assertion below would catch it.
	a, b := cpjTwoRel()
	probe := cpnParamIndexPath(a, cpiIndex("a0"), b.Relids, 3)
	memo := &Path{
		Kind:          PathMemoize,
		Rel:           probe.Rel,
		Rows:          probe.Rows,
		Cost:          probe.Cost,
		RequiredOuter: probe.RequiredOuter,
		Children:      []*Path{probe},
		MemoizeInfo:   &memoizePathInfo{estEntries: 128, rescan: Cost{Total: 1}},
	}

	n, _ := createPlanNode(cpnNestLoopPath(cpjLeafPath(b), memo, nil))
	nli, ok := n.(*NestedLoopIndexJoin)
	if !ok {
		t.Fatalf("createPlan emitted %T, want *NestedLoopIndexJoin", n)
	}
	if nli.InnerMemo == nil {
		t.Fatal("the chosen PathMemoize produced no InnerMemo; the cache the search costed was dropped")
	}
	if nli.InnerMemo.Child != nli.Inner {
		t.Fatal("the cache must wrap the very probe the join drives")
	}
	if len(nli.InnerMemo.KeyExprs) != 1 || nli.InnerMemo.KeyExprs[0] != nliIn(nli.Inner).Key {
		t.Fatalf("cache key %v is not the probe key %v — the two were derived separately",
			nli.InnerMemo.KeyExprs, nliIn(nli.Inner).Key)
	}
	if nli.InnerMemo.EstEntries != 128 {
		t.Fatalf("EstEntries %d, want 128: the executor must size the table from the estimate the search costed",
			nli.InnerMemo.EstEntries)
	}
	// OUTER coordinates, not merged: `b.b1` is outer position 1. The cache and
	// the probe are the same object, so this also pins the cache's space.
	if cr, ok := nliIn(nli.Inner).Key.(*ColumnRef); !ok || cr.Index != 1 {
		t.Fatalf("probe key = %v, want col(1) in OUTER coordinates", nliIn(nli.Inner).Key)
	}
}

// TestCreatePlanRefusesFreeStandingMemoize: goopg's cache is a FIELD on the
// join, so a `PathMemoize` anywhere else has no way to be emitted. Building the
// bare child instead would silently drop a cache the search paid for in the
// comparison that chose the plan.
func TestCreatePlanRefusesFreeStandingMemoize(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("createPlanNode accepted a free-standing PathMemoize")
		}
	}()
	createPlanNode(&Path{Kind: PathMemoize, MemoizeInfo: &memoizePathInfo{}})
}
