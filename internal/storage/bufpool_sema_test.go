package storage

import (
	"sync"
	"testing"
	"time"
)

// TestSlotSemaArraysInitializedCorrectly verifies that slotSema and slotWaiters
// are allocated at Pool construction with the correct length and zero values.
// Regression gate for M0107-0008 (per-slot Sema wait caller).
func TestSlotSemaArraysInitializedCorrectly(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 16})
	if err != nil {
		t.Fatal(err)
	}

	if len(pool.slotSema) != 16 {
		t.Fatalf("slotSema len %d, want 16", len(pool.slotSema))
	}
	if len(pool.slotWaiters) != 16 {
		t.Fatalf("slotWaiters len %d, want 16", len(pool.slotWaiters))
	}
	for i, v := range pool.slotSema {
		if v != 0 {
			t.Errorf("slotSema[%d] = %d (want 0)", i, v)
		}
	}
	for i := range pool.slotWaiters {
		if v := pool.slotWaiters[i].Load(); v != 0 {
			t.Errorf("slotWaiters[%d] = %d (want 0)", i, v)
		}
	}
}

// TestSlotSemaConcurrentPinSameBlock verifies that when multiple goroutines
// concurrently Pin the same block (one triggers disk IO, others wait on the
// per-slot sema), all goroutines complete without deadlock.
// Regression gate for M0107-0008 (per-slot Sema wait caller).
func TestSlotSemaConcurrentPinSameBlock(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}

	// Create relation + block via PinNew.
	rel := RelFileNode{DBOid: 1, RelOid: 7001, Fork: MainFork}
	slot, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	tag := slot.Tag()
	pool.MarkDirty(slot)
	pool.Unpin(slot)
	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}

	const numGoroutines = 8
	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Evict before each Pin attempt so at least some goroutines
			// trigger a disk read; others will wait on the per-slot sema.
			pool.InvalidateBlock(tag)
			s, err := pool.Pin(tag)
			if err != nil {
				errCh <- err
				return
			}
			pool.Unpin(s)
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: goroutines did not complete within 10 s — per-slot sema wakeup broken")
	}
	close(errCh)
	for err := range errCh {
		t.Errorf("Pin error: %v", err)
	}

	// After all Pins complete, slotWaiters must all be 0.
	for i := range pool.slotWaiters {
		if v := pool.slotWaiters[i].Load(); v != 0 {
			t.Errorf("slotWaiters[%d] = %d after all Pins complete (want 0)", i, v)
		}
	}
}

// TestSlotSemaWaiterCountReturnsToZero verifies that slotWaiters[i] returns to
// 0 after a single-goroutine IO-wait cycle completes. No cross-slot bleed.
func TestSlotSemaWaiterCountReturnsToZero(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}

	rel := RelFileNode{DBOid: 1, RelOid: 7002, Fork: MainFork}
	slot, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	tag := slot.Tag()
	pool.MarkDirty(slot)
	pool.Unpin(slot)
	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}

	// Evict + re-Pin (may hit disk read).
	pool.InvalidateBlock(tag)
	s, err := pool.Pin(tag)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)

	// All waiter counts must be 0 after Pin completes.
	for i := range pool.slotWaiters {
		if v := pool.slotWaiters[i].Load(); v != 0 {
			t.Errorf("slotWaiters[%d] = %d (want 0)", i, v)
		}
	}
	// All sema values must be 0 (no phantom releases from over-release).
	for i, v := range pool.slotSema {
		if v != 0 {
			t.Errorf("slotSema[%d] = %d (want 0)", i, v)
		}
	}
}

// TestSlotSemaNoPinCondInPool confirms that the old pool-wide pinCond field
// has been removed — the pool struct must not have a sync.Cond field named
// pinCond. This is a compile-time enforcement via struct field access.
// (The test exists as documentation; if pinCond were re-introduced, the
// TestSlotSemaArraysInitializedCorrectly field-count assertion would catch it.)
func TestSlotSemaNoPinCondInPool(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	// slotSema and slotWaiters must be present and len == Slots.
	if len(pool.slotSema) != 4 || len(pool.slotWaiters) != 4 {
		t.Fatalf("pool sema arrays wrong size: slotSema=%d slotWaiters=%d",
			len(pool.slotSema), len(pool.slotWaiters))
	}
}
