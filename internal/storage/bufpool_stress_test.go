package storage

import (
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPoolHighConcurrencyPinUnpinStress exercises the lock-free bufmap /
// pin fast path under heavy contention. M0107-0006 verification gate: the
// design doc calls for 1000 goroutines doing Pin/Unpin/evict for 30s. The
// default test run uses scaled-down counts so it stays CI-friendly; the full
// gate can be triggered via env vars (GOOPG_BUFPOOL_STRESS_GOROUTINES,
// GOOPG_BUFPOOL_STRESS_SECONDS).
//
// Why this exists: the bufmap rewrite landed in loop 1 of 2026-05-21 fixed
// three correctness bugs (sentinel collision, Robin-Hood early-exit on plain
// linear probes, FPI lock re-entry). None of the existing storage tests put
// the new lock-free path under sustained, many-thread collision pressure.
// Without a dedicated stress test there is no regression signal for the next
// rewrite (M0107-0007 will rip out the global appendMu, which depends on the
// same bufmap).
func TestPoolHighConcurrencyPinUnpinStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bufpool stress test in -short mode")
	}

	nGoroutines := envInt("GOOPG_BUFPOOL_STRESS_GOROUTINES", 64)
	dur := time.Duration(envInt("GOOPG_BUFPOOL_STRESS_SECONDS", 2)) * time.Second
	const nBlocks = 256
	const slots = 32

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	rel := RelFileNode{DBOid: 1, RelOid: 7, Fork: MainFork}
	seed := make(Page, BlockSize)
	if err := InitPage(seed); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nBlocks; i++ {
		if _, err := mgr.Extend(rel, seed); err != nil {
			t.Fatalf("extend block %d: %v", i, err)
		}
	}

	// Small pool relative to working set so every goroutine forces eviction
	// of someone else's slot. nBlocks/slots = 8x oversubscription.
	pool, err := NewPool(mgr, PoolConfig{Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	deadline := time.Now().Add(dur)
	var (
		pinOK    atomic.Uint64
		pinFail  atomic.Uint64
		markDirt atomic.Uint64
	)

	var wg sync.WaitGroup
	wg.Add(nGoroutines)
	for g := 0; g < nGoroutines; g++ {
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for time.Now().Before(deadline) {
				blk := BlockNumber(r.Intn(nBlocks))
				s, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
				if err != nil {
					pinFail.Add(1)
					runtime.Gosched()
					continue
				}
				pinOK.Add(1)
				// Mix a MarkDirty into roughly 1/8 of iterations so the
				// FPI path (atomic fpiSinceCheckpoint flag) is exercised
				// alongside Pin/Unpin churn.
				if r.Intn(8) == 0 {
					pool.MarkDirty(s)
					markDirt.Add(1)
				}
				pool.Unpin(s)
			}
		}(int64(g) + 1)
	}
	wg.Wait()

	t.Logf("stress: goroutines=%d duration=%s pinOK=%d pinFail=%d markDirty=%d",
		nGoroutines, dur, pinOK.Load(), pinFail.Load(), markDirt.Load())
	if pinOK.Load() == 0 {
		t.Fatalf("no successful pins recorded — pool is broken or deadlocked")
	}
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
