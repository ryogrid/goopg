package executor

import (
	"errors"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/storage"
)

// rel is a synthetic RelFileNode for the lock-integration tests.
// We only need (DBOid, RelOid) to identify the lock target — no
// actual storage backs these; the tests exercise the
// Context.acquireRelLock helper directly to prove the
// SQLSTATE 40P01 mapping is wired correctly.
func rel(relOid uint32) storage.RelFileNode {
	return storage.RelFileNode{DBOid: 1, RelOid: relOid, Fork: storage.MainFork}
}

// makeCtx builds an executor.Context wired to the given lock
// manager and backend ID. Other Context fields stay zero/nil —
// the tests don't touch the storage / planner paths.
func makeCtx(lm *lockmgr.LockManager, b lockmgr.BackendID) *Context {
	c := NewContext()
	c.LockMgr = lm
	c.BackendID = b
	return c
}

// TestExecutorAcquireHelperNilLockMgr is the regression guard:
// every existing test path constructs a Context without a
// LockMgr, and acquireRelLock must remain a no-op for them.
// Without this guard a future change that forgets the nil-check
// would break every storage_test.go fixture that doesn't bother
// to wire a lock manager.
func TestExecutorAcquireHelperNilLockMgr(t *testing.T) {
	c := NewContext()
	if err := c.acquireRelLock(rel(100), lockmgr.AccessShareLock); err != nil {
		t.Errorf("nil-LockMgr should be a no-op, got %v", err)
	}
}

// TestExecutorAcquireHelperGrantsLock pins the happy path:
// uncontended Acquire returns nil and the lock manager records
// the holder.
func TestExecutorAcquireHelperGrantsLock(t *testing.T) {
	lm := lockmgr.New()
	c := makeCtx(lm, 1)
	if err := c.acquireRelLock(rel(100), lockmgr.RowExclusiveLock); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	tag := lockmgr.LockTag{DB: 1, Rel: 100}
	if got := lm.Holders(tag); len(got) != 1 {
		t.Errorf("Holders=%v, want 1 holder", got)
	}
}

// TestExecutorDeadlockTwoSession is the headline integration
// path: two backends form an A↔B cycle through the executor's
// acquireRelLock helper. Exactly one gets back
// *ExecError{Code: "40P01", Message: "deadlock detected"}; the
// survivor's pending acquire returns nil. This test is the
// regression guard for the "ErrDeadlockDetected → 40P01"
// mapping in context.go — flip the mapping or break the
// translation and this test fails.
func TestExecutorDeadlockTwoSession(t *testing.T) {
	lm := lockmgr.New()
	lm.SetDeadlockTimeout(0) // synchronous trigger via CheckDeadlocksNow
	c1 := makeCtx(lm, 1)
	c2 := makeCtx(lm, 2)
	relA := rel(10)
	relB := rel(20)
	if err := c1.acquireRelLock(relA, lockmgr.RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := c2.acquireRelLock(relB, lockmgr.RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	res1 := make(chan error, 1)
	res2 := make(chan error, 1)
	go func() { res1 <- c1.acquireRelLock(relB, lockmgr.AccessExclusiveLock) }()
	go func() { res2 <- c2.acquireRelLock(relA, lockmgr.AccessExclusiveLock) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(lm.Waiters(lockmgr.LockTag{DB: 1, Rel: 10})) == 1 &&
			len(lm.Waiters(lockmgr.LockTag{DB: 1, Rel: 20})) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !lm.CheckDeadlocksNow() {
		t.Fatal("deadlock check did not detect cycle")
	}

	// Exactly one of the two backends gets a 40P01 ExecError;
	// the other returns nil after the victim's holdings are
	// released by the helper-driven ReleaseAll.
	dl := 0
	ok := 0
	for _, ch := range []chan error{res1, res2} {
		select {
		case err := <-ch:
			if err == nil {
				ok++
				continue
			}
			ee, isExecErr := err.(*ExecError)
			if !isExecErr {
				t.Errorf("got %T, want *ExecError", err)
				continue
			}
			if ee.Code != "40P01" {
				t.Errorf("code=%s, want 40P01", ee.Code)
			}
			if ee.Message != "deadlock detected" {
				t.Errorf("message=%q, want %q", ee.Message, "deadlock detected")
			}
			dl++
		case <-time.After(time.Second):
			t.Fatal("acquire never returned")
		}
	}
	if dl != 1 || ok != 1 {
		t.Errorf("deadlocks=%d ok=%d, want 1 each", dl, ok)
	}
}

// TestExecutorDeadlockThreeSession exercises a 3-edge cycle
// (A→B→C→A). Pins that the DFS-based detector finds cycles
// longer than 2, and that exactly one backend (the youngest)
// gets the 40P01 mapping.
func TestExecutorDeadlockThreeSession(t *testing.T) {
	lm := lockmgr.New()
	lm.SetDeadlockTimeout(0)
	c := []*Context{nil, makeCtx(lm, 1), makeCtx(lm, 2), makeCtx(lm, 3)}
	relA := rel(10)
	relB := rel(20)
	relC := rel(30)

	if err := c[1].acquireRelLock(relA, lockmgr.RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := c[2].acquireRelLock(relB, lockmgr.RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := c[3].acquireRelLock(relC, lockmgr.RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	res := []chan error{nil, make(chan error, 1), make(chan error, 1), make(chan error, 1)}
	go func() { res[1] <- c[1].acquireRelLock(relB, lockmgr.AccessExclusiveLock) }()
	go func() { res[2] <- c[2].acquireRelLock(relC, lockmgr.AccessExclusiveLock) }()
	go func() { res[3] <- c[3].acquireRelLock(relA, lockmgr.AccessExclusiveLock) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w := len(lm.Waiters(lockmgr.LockTag{DB: 1, Rel: 10})) +
			len(lm.Waiters(lockmgr.LockTag{DB: 1, Rel: 20})) +
			len(lm.Waiters(lockmgr.LockTag{DB: 1, Rel: 30}))
		if w == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !lm.CheckDeadlocksNow() {
		t.Fatal("3-cycle not detected")
	}

	dl := 0
	for i := 1; i <= 3; i++ {
		select {
		case err := <-res[i]:
			if err == nil {
				continue
			}
			ee, isExecErr := err.(*ExecError)
			if !isExecErr || ee.Code != "40P01" {
				t.Errorf("backend %d: got %v, want 40P01 ExecError", i, err)
				continue
			}
			dl++
			if i != 3 {
				// Youngest-backend victim policy: BackendID 3
				// is the highest, so it should be cancelled.
				t.Errorf("expected backend 3 (youngest) cancelled, got %d", i)
			}
		case <-time.After(500 * time.Millisecond):
			// survivor still parked is fine
		}
	}
	if dl != 1 {
		t.Errorf("deadlocks=%d, want exactly 1", dl)
	}
}

// TestExecutorNonDeadlockContention pins the false-positive
// guard: a linear waiter chain (1 holds, 2 waits, 3 waits
// behind 2) must NOT produce a 40P01 — only true cycles do.
// This is DoD #5 from the milestone: "Non-deadlock contention
// tests confirm no false-positive 40P01 under normal
// wait/unblock flows."
func TestExecutorNonDeadlockContention(t *testing.T) {
	lm := lockmgr.New()
	lm.SetDeadlockTimeout(0)
	c1 := makeCtx(lm, 1)
	c2 := makeCtx(lm, 2)
	c3 := makeCtx(lm, 3)
	r := rel(10)
	tag := lockmgr.LockTag{DB: 1, Rel: 10}

	if err := c1.acquireRelLock(r, lockmgr.AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	res2 := make(chan error, 1)
	go func() { res2 <- c2.acquireRelLock(r, lockmgr.AccessShareLock) }()
	for time.Now().Before(time.Now().Add(time.Second)) && len(lm.Waiters(tag)) != 1 {
		time.Sleep(time.Millisecond)
	}
	res3 := make(chan error, 1)
	go func() { res3 <- c3.acquireRelLock(r, lockmgr.AccessShareLock) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(lm.Waiters(tag)) != 2 {
		time.Sleep(time.Millisecond)
	}

	// Detector must NOT cancel anyone — the chain has no cycle.
	if lm.CheckDeadlocksNow() {
		t.Fatal("false-positive 40P01 on linear waiter chain")
	}
	// Release backend 1; backends 2 and 3 should both wake
	// (AccessShare is compatible-with-itself) and return nil.
	c1.LockMgr.ReleaseAll(c1.BackendID)
	for _, ch := range []chan error{res2, res3} {
		select {
		case err := <-ch:
			if err != nil {
				if errors.Is(err, lockmgr.ErrDeadlockDetected) {
					t.Errorf("false-positive ErrDeadlockDetected: %v", err)
				} else {
					t.Errorf("waiter: %v", err)
				}
			}
		case <-time.After(time.Second):
			t.Fatal("waiter did not progress after Release")
		}
	}
}
