package postmaster

// B-18 commit 1 (take2 P2-04 cache-key half) gate test: the same SQL under two
// SET random_page_cost values must cache DIFFERENT plans, and a third
// execution under an already-seen SET must hit.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/misc"
)

func mustSet(t *testing.T, sess *misc.SessionRegistry, name, val string) {
	t.Helper()
	if err := sess.Set(name, val, false); err != nil {
		t.Fatalf("SET %s = %s: %v", name, val, err)
	}
}

// TestPlanCacheSeparatesRandomPageCost is the gate: one SQL text, two planner
// contexts, two cache entries — and the third same-SET execution hits.
func TestPlanCacheSeparatesRandomPageCost(t *testing.T) {
	const sql = "SELECT * FROM t WHERE a = 1"

	sessA := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	mustSet(t, sessA, "random_page_cost", "1.1")
	sessB := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	mustSet(t, sessB, "random_page_cost", "4.0")

	// Sanity: the SETs really reach the planner context.
	if got := sessionPlannerSettings(sessA).RandomPageCost; got != 1.1 {
		t.Fatalf("sessA RandomPageCost = %v, want 1.1", got)
	}
	if got := sessionPlannerSettings(sessB).RandomPageCost; got != 4.0 {
		t.Fatalf("sessB RandomPageCost = %v, want 4.0", got)
	}

	keyA := planCacheKey(sql, 7, sessionPlannerFingerprint(sessA))
	keyB := planCacheKey(sql, 7, sessionPlannerFingerprint(sessB))
	if keyA == keyB {
		t.Fatal("same SQL under different random_page_cost must key apart")
	}

	pc := newPlanCache()
	planA := &optimizer.DDL{}
	planB := &optimizer.DDL{}

	// First execution under A: miss, then mark (doorkeeper admits on second
	// sighting, so the first Put stores nothing).
	if _, ok := pc.Get(keyA); ok {
		t.Fatal("first execution must miss")
	}
	pc.Put(keyA, planA)
	if _, ok := pc.Get(keyA); ok {
		t.Fatal("first Put is only a doorkeeper mark: second execution must still miss")
	}

	// Same SQL under B plans and caches independently — B must neither see
	// A's mark nor publish over it.
	if _, ok := pc.Get(keyB); ok {
		t.Fatal("B must miss: A's entry is under a different key")
	}
	pc.Put(keyB, planB)
	pc.Put(keyB, planB)
	gotB, ok := pc.Get(keyB)
	if !ok || gotB != optimizer.Node(planB) {
		t.Fatal("B's plan must be cached under B's key")
	}

	// Third execution under A (same SET): hit, and it is A's plan, not B's.
	pc.Put(keyA, planA)
	gotA, ok := pc.Get(keyA)
	if !ok {
		t.Fatal("third same-SET execution must hit")
	}
	if gotA != optimizer.Node(planA) {
		t.Error("A's entry must hold A's plan: contexts contaminated each other")
	}
}

// TestPlannerCacheFingerprintFloatsAreExact pins the no-rounding rule: two
// distinct float64 costs must fingerprint apart. A fixed-precision verb would
// fold neighbours (e.g. 1.1 vs nextafter(1.1)) onto one key and serve a plan
// costed under the wrong value.
func TestPlannerCacheFingerprintFloatsAreExact(t *testing.T) {
	base := optimizer.DefaultPlannerSettings()
	neighbour := base
	neighbour.RandomPageCost = math.Nextafter(base.RandomPageCost, math.MaxFloat64)
	if neighbour.RandomPageCost == base.RandomPageCost {
		t.Skip("no representable neighbour (unexpected)")
	}
	a := plannerCacheFingerprint(base, false, false, false, false)
	b := plannerCacheFingerprint(neighbour, false, false, false, false)
	if a == b {
		t.Error("adjacent float64 costs must fingerprint apart: float formatting rounds")
	}
	// Identical inputs fingerprint identically (key stability, no map-order or
	// pointer noise).
	if c := plannerCacheFingerprint(base, false, false, false, false); c != a {
		t.Error("fingerprint is not deterministic")
	}
}

// TestPlannerCacheFingerprintCoversEveryField guards the positional list: flip
// each PlannerSettings field one at a time and require the fingerprint to
// move. A field added to the struct but forgotten in plannerCacheFingerprint
// would otherwise leak across contexts silently.
func TestPlannerCacheFingerprintCoversEveryField(t *testing.T) {
	base := optimizer.DefaultPlannerSettings()
	fp := func(ps optimizer.PlannerSettings) string {
		return plannerCacheFingerprint(ps, false, false, false, false)
	}
	want := fp(base)
	mutations := []func(*optimizer.PlannerSettings){
		func(ps *optimizer.PlannerSettings) { ps.SeqPageCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.RandomPageCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.CPUTupleCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.CPUIndexTupleCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.CPUOperatorCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.ParallelSetupCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.ParallelTupleCost += 0.5 },
		func(ps *optimizer.PlannerSettings) { ps.EffectiveCacheSize += 1 },
		func(ps *optimizer.PlannerSettings) { ps.WorkMem += 1024 },
		func(ps *optimizer.PlannerSettings) { ps.EnableHashJoin = !ps.EnableHashJoin },
		func(ps *optimizer.PlannerSettings) { ps.EnableMergeJoin = !ps.EnableMergeJoin },
		func(ps *optimizer.PlannerSettings) { ps.EnableNestLoop = !ps.EnableNestLoop },
		func(ps *optimizer.PlannerSettings) { ps.EnableSort = !ps.EnableSort },
		func(ps *optimizer.PlannerSettings) { ps.EnableSeqScan = !ps.EnableSeqScan },
		func(ps *optimizer.PlannerSettings) { ps.EnableIndexScan = !ps.EnableIndexScan },
		func(ps *optimizer.PlannerSettings) { ps.EnableBitmapScan = !ps.EnableBitmapScan },
		func(ps *optimizer.PlannerSettings) { ps.EnableHashAgg = !ps.EnableHashAgg },
		func(ps *optimizer.PlannerSettings) {
			ps.EnablePresortedAggregate = !ps.EnablePresortedAggregate
		},
		func(ps *optimizer.PlannerSettings) { ps.Geqo = !ps.Geqo },
		func(ps *optimizer.PlannerSettings) { ps.GeqoThreshold++ },
		func(ps *optimizer.PlannerSettings) { ps.EnableNestLoopIndex = !ps.EnableNestLoopIndex },
		func(ps *optimizer.PlannerSettings) { ps.GeqoEffort++ },
		func(ps *optimizer.PlannerSettings) { ps.GeqoPoolSize++ },
		func(ps *optimizer.PlannerSettings) { ps.GeqoGenerations++ },
		func(ps *optimizer.PlannerSettings) { ps.GeqoSelectionBias += 0.25 },
		func(ps *optimizer.PlannerSettings) { ps.GeqoSeed += 0.25 },
		func(ps *optimizer.PlannerSettings) { ps.EnableMemoize = !ps.EnableMemoize },
		func(ps *optimizer.PlannerSettings) { ps.HashMemMultiplier += 0.5 },
	}
	for i, mutate := range mutations {
		ps := base
		mutate(&ps)
		if got := fp(ps); got == want {
			t.Errorf("mutation %d leaves the fingerprint unchanged: field not covered", i)
		}
	}
	// Each scan-toggle position moves the key on its own.
	toggles := [][4]bool{
		{true, false, false, false},
		{false, true, false, false},
		{false, false, true, false},
		{false, false, false, true},
	}
	for i, tg := range toggles {
		if got := plannerCacheFingerprint(base, tg[0], tg[1], tg[2], tg[3]); got == want {
			t.Errorf("scan toggle %d leaves the fingerprint unchanged", i)
		}
	}
}
