package postmaster

import (
	"fmt"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// perf-optimize-take3/06 candidate H. The admission filter must keep the cache
// USEFUL for repeated SQL while keeping one-shot SQL out of the write lock.
func TestPlanCacheAdmitsOnSecondSighting(t *testing.T) {
	pc := newPlanCache()
	var node optimizer.Node

	// First Put is only a mark: nothing is cached yet.
	pc.Put("k1", node)
	if _, ok := pc.Get("k1"); ok {
		t.Fatal("first Put must not populate the cache (doorkeeper)")
	}
	// Second Put admits.
	pc.Put("k1", node)
	if _, ok := pc.Get("k1"); !ok {
		t.Fatal("second Put must populate the cache")
	}
}

// The pgbench shape: every key unique. Nothing may be admitted, so the shard
// write lock is never taken and no live entry is evicted.
func TestPlanCacheOneShotKeysNeverAdmitted(t *testing.T) {
	pc := newPlanCache()
	var node optimizer.Node

	// A genuinely hot statement earns its slot.
	pc.Put("hot", node)
	pc.Put("hot", node)
	if _, ok := pc.Get("hot"); !ok {
		t.Fatal("hot key should be cached")
	}

	// Now flood with unique keys, far more than the 512-entry capacity.
	for i := range 20000 {
		pc.Put(fmt.Sprintf("select abalance from pgbench_accounts where aid = %d", i), node)
	}

	// The hot entry must have survived: one-shot keys never entered, so they
	// could not have evicted it. This is the regression that mattered — before
	// the filter, 512 entries were churned continuously by literal noise.
	if _, ok := pc.Get("hot"); !ok {
		t.Fatal("one-shot keys evicted the hot entry: admission filter is not working")
	}

	// And essentially nothing from the flood is resident.
	resident := 0
	for i := range 20000 {
		if _, ok := pc.Get(fmt.Sprintf("select abalance from pgbench_accounts where aid = %d", i)); ok {
			resident++
		}
	}
	if resident != 0 {
		t.Fatalf("one-shot keys were admitted: %d resident", resident)
	}
}

// Marks survive Invalidate so a hot statement is re-admitted immediately after
// DDL rather than re-learning.
func TestPlanCacheDoorkeeperSurvivesInvalidate(t *testing.T) {
	pc := newPlanCache()
	var node optimizer.Node
	pc.Put("k", node)
	pc.Put("k", node)
	if _, ok := pc.Get("k"); !ok {
		t.Fatal("precondition: k cached")
	}
	pc.Invalidate()
	if _, ok := pc.Get("k"); ok {
		t.Fatal("Invalidate must drop entries")
	}
	pc.Put("k", node) // single Put: the mark survived, so this re-admits
	if _, ok := pc.Get("k"); !ok {
		t.Fatal("mark should survive Invalidate so a hot key is re-admitted at once")
	}
}

func TestPlanCacheAdmissionIsRaceFree(t *testing.T) {
	pc := newPlanCache()
	var node optimizer.Node
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 2000 {
				k := fmt.Sprintf("k%d", i%64)
				pc.Put(k, node)
				pc.Get(k)
			}
			_ = g
		}(g)
	}
	wg.Wait()
}
