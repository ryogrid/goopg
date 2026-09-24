package storage

import "testing"

// TestCleanupLockRequiresTheOnlyPin pins the bufmgr.c cleanup-lock contract
// (M0145-0008q): the exclusive content lock counts as a cleanup lock only
// while the caller's pin is the page's only one, and the conditional variant
// leaves no lock behind when it fails.
func TestCleanupLockRequiresTheOnlyPin(t *testing.T) {
	mgr := NewManager(ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 7100, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s)

	if !pool.ConditionalLockForCleanup(s) {
		t.Fatal("sole pin: conditional cleanup lock refused")
	}
	if !pool.IsCleanupOK(s) {
		t.Fatal("sole pin: IsCleanupOK false under the cleanup lock")
	}
	s.Unlock()

	second, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	if pool.ConditionalLockForCleanup(s) {
		t.Fatal("two pins: conditional cleanup lock granted")
	}
	if !s.TryRLock() {
		t.Fatal("a failed conditional cleanup lock left the content lock held")
	}
	s.RUnlock()
	s.Lock()
	if pool.IsCleanupOK(s) {
		t.Fatal("two pins: IsCleanupOK true")
	}
	s.Unlock()
	pool.Unpin(second)

	pool.LockForCleanup(s)
	s.Unlock()
}
