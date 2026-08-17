package lmgr

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// waitForWaiters polls the manager until len(Waiters(tag)) == n or
// the deadline expires. Returns true if it reached the target count.
func waitForWaiters(lm *LockManager, t LockTag, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(lm.Waiters(t)) == n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return len(lm.Waiters(t)) == n
}

// TestDeadlockDetectsTwoSessionCycle: classic A↔B cycle.
// Backend 1 holds tagA, then waits on tagB. Backend 2 holds tagB,
// then waits on tagA. CheckDeadlocksNow detects the cycle and
// cancels one victim with ErrDeadlockDetected; the survivor's
// Acquire then completes.
func TestDeadlockDetectsTwoSessionCycle(t *testing.T) {
	lm := New()
	lm.SetDeadlockTimeout(0) // disable auto-fire so the test drives explicitly
	tagA := LockTag{DB: 1, Rel: 100}
	tagB := LockTag{DB: 1, Rel: 200}

	if err := lm.Acquire(context.Background(), 1, tagA, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(context.Background(), 2, tagB, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}

	res1 := make(chan error, 1)
	res2 := make(chan error, 1)
	go func() { res1 <- lm.Acquire(context.Background(), 1, tagB, AccessExclusiveLock) }()
	go func() { res2 <- lm.Acquire(context.Background(), 2, tagA, AccessExclusiveLock) }()

	// Wait until both are parked.
	if !waitForWaiters(lm, tagA, 1, time.Second) || !waitForWaiters(lm, tagB, 1, time.Second) {
		t.Fatal("backends did not both park as expected")
	}

	if !lm.CheckDeadlocksNow() {
		t.Fatal("deadlock check did not detect cycle")
	}

	// After the cycle is broken, exactly one backend must be the victim
	// (ErrDeadlockDetected) and the other the survivor (nil, once the
	// victim's holdings are released). Both Acquire calls become ready at
	// nearly the same instant — cancelling the victim immediately frees the
	// lock the survivor was waiting on — so we must NOT assume which channel
	// is ready first. Collect BOTH results and check the pair
	// order-independently.
	//
	// (Previously this used a `select` and labelled the first-ready channel
	// the victim; Go's randomised select made it flake under -race whenever
	// the survivor's success landed first — "victim got <nil>".)
	results := make([]error, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case err := <-res1:
			results = append(results, err)
		case err := <-res2:
			results = append(results, err)
		case <-time.After(2 * time.Second):
			t.Fatal("a backend neither was cancelled nor made progress")
		}
	}
	var deadlocks, survivors int
	for _, err := range results {
		switch {
		case errors.Is(err, ErrDeadlockDetected):
			deadlocks++
		case err == nil:
			survivors++
		default:
			t.Errorf("unexpected Acquire error: %v", err)
		}
	}
	if deadlocks != 1 || survivors != 1 {
		t.Errorf("got %d deadlock victim(s) and %d survivor(s); want exactly 1 each", deadlocks, survivors)
	}
}

// TestDeadlockYoungestBackendIsVictim pins the victim-selection
// policy: highest BackendID in the cycle. Without this, the
// detector might cancel an older session that has more work
// invested.
func TestDeadlockYoungestBackendIsVictim(t *testing.T) {
	lm := New()
	lm.SetDeadlockTimeout(0)
	tagA := LockTag{DB: 1, Rel: 1}
	tagB := LockTag{DB: 1, Rel: 2}

	if err := lm.Acquire(context.Background(), 5, tagA, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(context.Background(), 9, tagB, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	res5 := make(chan error, 1)
	res9 := make(chan error, 1)
	go func() { res5 <- lm.Acquire(context.Background(), 5, tagB, AccessExclusiveLock) }()
	go func() { res9 <- lm.Acquire(context.Background(), 9, tagA, AccessExclusiveLock) }()
	if !waitForWaiters(lm, tagA, 1, time.Second) || !waitForWaiters(lm, tagB, 1, time.Second) {
		t.Fatal("not parked")
	}
	lm.CheckDeadlocksNow()

	select {
	case err := <-res9:
		if !errors.Is(err, ErrDeadlockDetected) {
			t.Errorf("backend 9 (younger) got %v, want ErrDeadlockDetected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("victim not cancelled")
	}
	// Backend 5 should not have been cancelled.
	select {
	case err := <-res5:
		if errors.Is(err, ErrDeadlockDetected) {
			t.Error("older backend 5 was cancelled instead of younger backend 9")
		}
		// nil is fine — survivor finished.
	case <-time.After(500 * time.Millisecond):
		// Survivor may still be parked; that's OK too.
	}
}

// TestDeadlockDetectsThreeSessionCycle: A→B→C→A multi-edge cycle.
// Pins that DFS finds cycles longer than 2.
func TestDeadlockDetectsThreeSessionCycle(t *testing.T) {
	lm := New()
	lm.SetDeadlockTimeout(0)
	tagA := LockTag{DB: 1, Rel: 10}
	tagB := LockTag{DB: 1, Rel: 20}
	tagC := LockTag{DB: 1, Rel: 30}

	for _, p := range []struct {
		b BackendID
		t LockTag
	}{{1, tagA}, {2, tagB}, {3, tagC}} {
		if err := lm.Acquire(context.Background(), p.b, p.t, AccessExclusiveLock); err != nil {
			t.Fatal(err)
		}
	}
	results := map[BackendID]chan error{
		1: make(chan error, 1),
		2: make(chan error, 1),
		3: make(chan error, 1),
	}
	// 1 wants B, 2 wants C, 3 wants A — closes the cycle.
	go func() { results[1] <- lm.Acquire(context.Background(), 1, tagB, AccessExclusiveLock) }()
	go func() { results[2] <- lm.Acquire(context.Background(), 2, tagC, AccessExclusiveLock) }()
	go func() { results[3] <- lm.Acquire(context.Background(), 3, tagA, AccessExclusiveLock) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(lm.Waiters(tagA))+len(lm.Waiters(tagB))+len(lm.Waiters(tagC)) == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if !lm.CheckDeadlocksNow() {
		t.Fatal("3-cycle not detected")
	}

	// Exactly one backend should get ErrDeadlockDetected.
	cancelled := 0
	for b, ch := range results {
		select {
		case err := <-ch:
			if errors.Is(err, ErrDeadlockDetected) {
				cancelled++
				if b != 3 {
					t.Errorf("expected youngest (3) cancelled, got %d", b)
				}
			}
		case <-time.After(500 * time.Millisecond):
			// Survivor still parked is fine.
		}
	}
	if cancelled != 1 {
		t.Errorf("expected exactly 1 cancellation, got %d", cancelled)
	}
}

// TestDeadlockNoCycleNoCancel: a long but cycle-free wait chain
// (1 holds A; 2 waits on A; 3 waits on A behind 2). The detector
// must NOT cancel anyone.
func TestDeadlockNoCycleNoCancel(t *testing.T) {
	lm := New()
	lm.SetDeadlockTimeout(0)
	tagA := LockTag{DB: 1, Rel: 1}
	if err := lm.Acquire(context.Background(), 1, tagA, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	res2 := make(chan error, 1)
	res3 := make(chan error, 1)
	go func() { res2 <- lm.Acquire(context.Background(), 2, tagA, AccessShareLock) }()
	if !waitForWaiters(lm, tagA, 1, time.Second) {
		t.Fatal("backend 2 didn't park")
	}
	go func() { res3 <- lm.Acquire(context.Background(), 3, tagA, AccessShareLock) }()
	if !waitForWaiters(lm, tagA, 2, time.Second) {
		t.Fatal("backend 3 didn't park")
	}
	if lm.CheckDeadlocksNow() {
		t.Fatal("false-positive deadlock on linear wait chain")
	}
	// No one should have been cancelled.
	select {
	case <-res2:
		t.Error("backend 2 cancelled spuriously")
	case <-res3:
		t.Error("backend 3 cancelled spuriously")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestDeadlockTimerSchedulesCheck: with a non-zero deadlockTimeout
// the AfterFunc fires the detector automatically. Pins that the
// timer wiring is live, not just the synchronous CheckDeadlocksNow
// path.
func TestDeadlockTimerSchedulesCheck(t *testing.T) {
	lm := New()
	lm.SetDeadlockTimeout(20 * time.Millisecond)
	tagA := LockTag{DB: 1, Rel: 1}
	tagB := LockTag{DB: 1, Rel: 2}
	if err := lm.Acquire(context.Background(), 1, tagA, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(context.Background(), 2, tagB, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	res1 := make(chan error, 1)
	res2 := make(chan error, 1)
	go func() { res1 <- lm.Acquire(context.Background(), 1, tagB, AccessExclusiveLock) }()
	go func() { res2 <- lm.Acquire(context.Background(), 2, tagA, AccessExclusiveLock) }()

	// Wait long enough for the timer to fire.
	cancelled := false
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case err := <-res1:
			if errors.Is(err, ErrDeadlockDetected) {
				cancelled = true
				break loop
			}
		case err := <-res2:
			if errors.Is(err, ErrDeadlockDetected) {
				cancelled = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !cancelled {
		t.Fatal("timer did not trigger deadlock detection")
	}
}

// TestDeadlockSurvivorMakesProgress: the surviving backend must
// successfully acquire its blocked lock after the victim's
// ReleaseAll runs.
func TestDeadlockSurvivorMakesProgress(t *testing.T) {
	lm := New()
	lm.SetDeadlockTimeout(0)
	tagA := LockTag{DB: 1, Rel: 1}
	tagB := LockTag{DB: 1, Rel: 2}
	if err := lm.Acquire(context.Background(), 1, tagA, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(context.Background(), 2, tagB, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	res1 := make(chan error, 1)
	res2 := make(chan error, 1)
	go func() { defer wg.Done(); res1 <- lm.Acquire(context.Background(), 1, tagB, AccessExclusiveLock) }()
	go func() { defer wg.Done(); res2 <- lm.Acquire(context.Background(), 2, tagA, AccessExclusiveLock) }()
	if !waitForWaiters(lm, tagA, 1, time.Second) || !waitForWaiters(lm, tagB, 1, time.Second) {
		t.Fatal("not parked")
	}
	lm.CheckDeadlocksNow()
	wg.Wait()
	close(res1)
	close(res2)

	// Exactly one ErrDeadlockDetected, exactly one nil.
	errs := []error{<-res1, <-res2}
	dl := 0
	ok := 0
	for _, e := range errs {
		switch {
		case errors.Is(e, ErrDeadlockDetected):
			dl++
		case e == nil:
			ok++
		}
	}
	if dl != 1 || ok != 1 {
		t.Errorf("got dl=%d ok=%d, want 1 each (errs=%v)", dl, ok, errs)
	}
}
