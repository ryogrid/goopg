package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestMemoizeKeyNDistinctAppliesRestrictionTerm pins M0146-0199:
// cost_memoize_rescan's ndistinct is estimate_num_groups over the cache keys
// (costsize.c:2581), and estimate_num_groups scales a filtered relation's
// distinct count by the Yao/Dell'Era term (selfuncs.c:3721). TPC-DS SF1 Q32's
// SubPlan probes date_dim from catalog_sales filtered to 88 of 1441557 rows;
// cs_sold_date_sk has 1830 distinct values, so PG estimates 86 distinct keys
// over 88 calls and its Memoize wins. Without the term goopg kept 1830, the
// calls clamp turned it into 88 and the hit ratio into zero.
func TestMemoizeKeyNDistinctAppliesRestrictionTerm(t *testing.T) {
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)
	const tuples = 1441557
	s := memoTestCtx(t, tuples, 0, true)
	s.relInfos[0].table.Stats.Columns[0] = catalog.ColumnStats{NDistinct: 1830}

	inner, _ := memoInnerRel(innerRelids, outerRelids, 7.54)
	ip := inner.CheapestParameterized[1]

	s.relInfos[0].filteredRows = tuples
	if nd, def := memoizeKeyNDistinct(s, ip, outerRelids); def || nd != 1830 {
		t.Fatalf("unfiltered outer: ndistinct = %v (default %v), want the column's 1830", nd, def)
	}

	s.relInfos[0].filteredRows = 88
	nd, def := memoizeKeyNDistinct(s, ip, outerRelids)
	if def || nd != 86 {
		t.Fatalf("outer filtered to 88 rows: ndistinct = %v (default %v), want PG's 86", nd, def)
	}

	// The same key twice is one grouping variable (add_unique_group_var),
	// not 86 squared.
	dup := *ip
	dup.IndexClauses = append(append([]indexPathClause(nil), ip.IndexClauses...), ip.IndexClauses[0])
	if got, _ := memoizeKeyNDistinct(s, &dup, outerRelids); got != 86 {
		t.Fatalf("repeated key: ndistinct = %v, want 86", got)
	}

	// With 86 distinct keys over 88 calls the cache hits ~2%, so the rescan
	// prices below the bare probe — the comparison PG's add_path makes.
	cp := defaultCostParams()
	outer := scanRel(outerRelids, 88, estScanPages(88, 32))
	p := getMemoizePath(s, outer, outer.CheapestTotal, ip, cp)
	if p == nil {
		t.Fatal("no Memoize path for an eligible probe")
	}
	if pathRescanTotal(p) >= pathRescanTotal(ip) {
		t.Fatalf("Memoize rescan %.4f not below the bare probe's %.4f", pathRescanTotal(p), pathRescanTotal(ip))
	}
}

// TestClonedNLIKeepsItsCache pins M0146-0199's second half: the kept-sublink
// rebase clones a SubPlan's plan through clonePlanReplacingOuter, and the
// clone used to drop InnerMemo, so a join costed with PG's Memoize (TPC-DS
// SF1 Q32, Nested Loop 996.41) ran and printed without one. The cache must
// survive the clone and the offset remap, wrap the clone's own probe, and be
// keyed on that probe's keys.
func TestClonedNLIKeepsItsCache(t *testing.T) {
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
	orig, ok := n.(*NestedLoopIndexJoin)
	if !ok || orig.InnerMemo == nil {
		t.Fatalf("fixture: want a memoized *NestedLoopIndexJoin, got %T", n)
	}

	check := func(name string, got Node, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		nli, ok := got.(*NestedLoopIndexJoin)
		if !ok {
			t.Fatalf("%s: got %T", name, got)
		}
		if nli.InnerMemo == nil {
			t.Fatalf("%s dropped the cache the search costed", name)
		}
		if nli.InnerMemo == orig.InnerMemo || nli.InnerMemo.Child != nli.Inner {
			t.Fatalf("%s: the cache must wrap the new probe, not the original scan", name)
		}
		if len(nli.InnerMemo.KeyExprs) != 1 || nli.InnerMemo.KeyExprs[0] != nliIn(nli.Inner).Key {
			t.Fatalf("%s: cache key %v is not the probe key %v", name, nli.InnerMemo.KeyExprs, nliIn(nli.Inner).Key)
		}
		if nli.InnerMemo.EstEntries != 128 {
			t.Fatalf("%s: EstEntries %d, want 128", name, nli.InnerMemo.EstEntries)
		}
	}
	cl, err := clonePlanReplacingOuter(orig, nil)
	check("clonePlanReplacingOuter", cl, err)
	rm, err := remapSourceTableIdx(orig, 2)
	check("remapSourceTableIdx", rm, err)

	if rebindInnerMemo(orig.InnerMemo, &SeqScan{}) != nil {
		t.Fatal("a cache over an inner that is no longer a keyed probe must be dropped")
	}
}
