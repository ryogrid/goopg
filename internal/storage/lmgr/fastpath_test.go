package lmgr

import (
	"context"
	"testing"
)

// perf-optimize-take3 candidate A. The fast path is only sound if the strong
// counter is exact: it must be raised while a conflicting lock is held and
// return to zero afterwards, on EVERY release route (Release, ReleaseAll, and
// the waiter-promotion path). A leaked increment silently disables the fast
// path forever; a missed increment silently skips a real conflict.
func TestFastPathStrongCounterIsExact(t *testing.T) {
	tag := LockTag{DB: 1, Rel: 42}
	weak := []Mode{AccessShareLock, RowShareLock, RowExclusiveLock}
	strong := []Mode{ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}

	for _, m := range weak {
		if !EligibleForFastPath(m) {
			t.Fatalf("%v should be fast-path eligible (upstream: mode < ShareUpdateExclusiveLock)", m)
		}
	}
	for _, m := range strong {
		if EligibleForFastPath(m) {
			t.Fatalf("%v must not be fast-path eligible", m)
		}
	}

	for _, sm := range strong {
		t.Run("Release/"+sm.String(), func(t *testing.T) {
			lm := New()
			if !lm.NoConflictFastPath(tag, AccessShareLock) {
				t.Fatal("empty manager must offer the fast path")
			}
			if err := lm.Acquire(context.Background(), 1, tag, sm); err != nil {
				t.Fatalf("acquire %v: %v", sm, err)
			}
			if lm.NoConflictFastPath(tag, AccessShareLock) {
				t.Fatalf("%v held: fast path must be refused", sm)
			}
			lm.Release(1, tag, sm)
			if !lm.NoConflictFastPath(tag, AccessShareLock) {
				t.Fatalf("%v released: counter leaked, fast path stays off", sm)
			}
		})
		t.Run("ReleaseAll/"+sm.String(), func(t *testing.T) {
			lm := New()
			if err := lm.Acquire(context.Background(), 1, tag, sm); err != nil {
				t.Fatalf("acquire %v: %v", sm, err)
			}
			if lm.NoConflictFastPath(tag, AccessShareLock) {
				t.Fatalf("%v held: fast path must be refused", sm)
			}
			lm.ReleaseAll(1)
			if !lm.NoConflictFastPath(tag, AccessShareLock) {
				t.Fatalf("%v ReleaseAll'd: counter leaked", sm)
			}
		})
	}

	// Weak locks alone never disable the fast path (they cannot conflict with
	// each other), and repeated acquire/release must not drift the counter.
	lm := New()
	for range 50 {
		for _, m := range weak {
			if err := lm.Acquire(context.Background(), 7, tag, m); err != nil {
				t.Fatalf("weak acquire %v: %v", m, err)
			}
		}
		if !lm.NoConflictFastPath(tag, AccessShareLock) {
			t.Fatal("weak holders must not disable the fast path")
		}
		lm.ReleaseAll(7)
	}
	if !lm.NoConflictFastPath(tag, AccessShareLock) {
		t.Fatal("counter drifted after repeated weak acquire/release")
	}

	// A strong lock on a DIFFERENT tag must not disable this tag unless the
	// two happen to share a bucket; assert on a tag we know hashes elsewhere.
	other := LockTag{DB: 1, Rel: 43}
	if fastPathBucket(other) != fastPathBucket(tag) {
		lm2 := New()
		if err := lm2.Acquire(context.Background(), 2, other, AccessExclusiveLock); err != nil {
			t.Fatal(err)
		}
		if !lm2.NoConflictFastPath(tag, AccessShareLock) {
			t.Fatal("a strong lock on an unrelated bucket must not block the fast path")
		}
	}
}

// A strong lock must still be seen by the fast path when it is granted after a
// wait (the waiter-promotion route through wakePassLocked), not just when it is
// granted immediately.
func TestFastPathStrongCounterAfterWaiterPromotion(t *testing.T) {
	lm := New()
	tag := LockTag{DB: 1, Rel: 99}
	ctx := context.Background()

	if err := lm.Acquire(ctx, 1, tag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- lm.Acquire(ctx, 2, tag, AccessExclusiveLock) }()

	// Let backend 2 queue, then hand it the lock.
	for range 1000 {
		if len(lm.Waiters(tag)) > 0 {
			break
		}
	}
	lm.Release(1, tag, AccessShareLock)
	if err := <-done; err != nil {
		t.Fatalf("promoted acquire: %v", err)
	}
	if lm.NoConflictFastPath(tag, AccessShareLock) {
		t.Fatal("AccessExclusiveLock granted via promotion: fast path must be refused")
	}
	lm.Release(2, tag, AccessExclusiveLock)
	if !lm.NoConflictFastPath(tag, AccessShareLock) {
		t.Fatal("counter leaked on the promotion route")
	}
}
