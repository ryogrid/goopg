package lockmgr

import (
	"context"
	"sync"
	"testing"
	"time"
)

var testTag = LockTag{DB: 1, Rel: 100}

// TestLockConflictMatrixMatchesUpstream pins the conflict matrix
// against the upstream PG matrix exhaustively. If anyone edits
// conflictTab without updating the upstream reference, this test
// catches it. The expected set is derived from
// postgres/src/backend/storage/lmgr/lock.c LockConflicts[].
func TestLockConflictMatrixMatchesUpstream(t *testing.T) {
	// upstream[i] holds the modes that conflict with mode i.
	type pair struct {
		mode      Mode
		conflicts []Mode
	}
	cases := []pair{
		{AccessShareLock, []Mode{AccessExclusiveLock}},
		{RowShareLock, []Mode{ExclusiveLock, AccessExclusiveLock}},
		{RowExclusiveLock, []Mode{ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}},
		{ShareUpdateExclusiveLock, []Mode{ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}},
		{ShareLock, []Mode{RowExclusiveLock, ShareUpdateExclusiveLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}},
		{ShareRowExclusiveLock, []Mode{RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}},
		{ExclusiveLock, []Mode{RowShareLock, RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}},
		{AccessExclusiveLock, []Mode{AccessShareLock, RowShareLock, RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock}},
	}
	for _, c := range cases {
		var want Mask
		for _, m := range c.conflicts {
			want |= bit(m)
		}
		if conflictTab[c.mode] != want {
			t.Errorf("conflictTab[%s] = %b, want %b", c.mode, conflictTab[c.mode], want)
		}
		// Also sanity-check ConflictsWith against each held bit.
		for _, m := range []Mode{AccessShareLock, RowShareLock, RowExclusiveLock, ShareUpdateExclusiveLock, ShareLock, ShareRowExclusiveLock, ExclusiveLock, AccessExclusiveLock} {
			expected := false
			for _, conf := range c.conflicts {
				if conf == m {
					expected = true
					break
				}
			}
			if got := ConflictsWith(c.mode, bit(m)); got != expected {
				t.Errorf("ConflictsWith(%s, held=%s) = %v, want %v", c.mode, m, got, expected)
			}
		}
	}
}

// TestLockManagerNoConflictGrantsImmediately: the simplest path —
// taking AccessShare on an unowned tag returns instantly.
func TestLockManagerNoConflictGrantsImmediately(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessShareLock); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	holders := lm.Holders(testTag)
	if got := holders[1]; got != bit(AccessShareLock) {
		t.Errorf("holders[1] = %b, want %b", got, bit(AccessShareLock))
	}
}

// TestLockManagerCompatibleModesCoexist: two backends each holding
// AccessShare don't conflict. Pins the multi-holder path.
func TestLockManagerCompatibleModesCoexist(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(context.Background(), 2, testTag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	holders := lm.Holders(testTag)
	if len(holders) != 2 {
		t.Errorf("holders=%v, want 2 backends", holders)
	}
}

// TestLockManagerConflictBlocksAndWakesOnRelease: the headline path.
// Backend 1 holds AccessExclusive; backend 2 wants AccessShare and
// blocks. Releasing backend 1 wakes backend 2, which becomes a
// holder. Pins the wake-pass + signal channel mechanics.
func TestLockManagerConflictBlocksAndWakesOnRelease(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		got <- lm.Acquire(context.Background(), 2, testTag, AccessShareLock)
	}()
	// Wait until backend 2 is parked in the queue.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(lm.Waiters(testTag)) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(lm.Waiters(testTag)) != 1 {
		t.Fatal("backend 2 never queued as waiter")
	}
	// Release the conflicting holder.
	lm.Release(1, testTag, AccessExclusiveLock)
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("waiter Acquire: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not woken after Release")
	}
	holders := lm.Holders(testTag)
	if got := holders[2]; got != bit(AccessShareLock) {
		t.Errorf("holders[2] = %b, want %b", got, bit(AccessShareLock))
	}
}

// TestLockManagerSelfDoesNotConflict: a backend already holding
// RowExclusive can also take AccessShare on the same tag without
// blocking. Without grantedExcept(self), this would deadlock the
// executor's nested-statement case (e.g. INSERT … SELECT).
func TestLockManagerSelfDoesNotConflict(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	// AccessExclusive conflicts with RowExclusive in upstream's
	// matrix, but a backend can never block itself — same backend
	// re-acquiring is allowed.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := lm.Acquire(ctx, 1, testTag, AccessExclusiveLock); err != nil {
		t.Errorf("self-acquire AccessExclusive on top of RowExclusive: %v", err)
	}
	if got := lm.Holders(testTag)[1]; got != bit(RowExclusiveLock)|bit(AccessExclusiveLock) {
		t.Errorf("holders[1]=%b, want both bits", got)
	}
}

// TestParseModeRoundTrip pins ParseMode as the inverse of Mode.String()
// across every real mode, and confirms it rejects unknown / sentinel
// names. The SQL parser emits exactly these canonical names for
// LOCK TABLE, so a drift here would silently mis-map a requested mode.
func TestParseModeRoundTrip(t *testing.T) {
	for m := AccessShareLock; m <= maxMode; m++ {
		got, ok := ParseMode(m.String())
		if !ok || got != m {
			t.Errorf("ParseMode(%q) = (%v, %v), want (%v, true)", m.String(), got, ok, m)
		}
	}
	for _, bad := range []string{"", "INVALID", "NoLock", "RowExclusive", "accessexclusivelock"} {
		if got, ok := ParseMode(bad); ok {
			t.Errorf("ParseMode(%q) = (%v, true), want (NoLock, false)", bad, got)
		}
	}
}

// TestLockManagerEarlyGrantAheadOfWaiter mirrors the upstream lock-nowait
// spec: a backend that already holds a strong lock may take a weaker
// self-compatible mode immediately even while a conflicting waiter is
// parked ahead of it, because it would be inserted in front of that
// waiter (JoinWaitQueue's "special case" early grant, proc.c). Backend 1
// holds AccessExclusive; backend 2 blocks on Exclusive (queued); backend 1
// then requests ShareRowExclusive NOWAIT, which must succeed.
func TestLockManagerEarlyGrantAheadOfWaiter(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	// Backend 2 wants Exclusive; conflicts with backend 1's
	// AccessExclusive, so it parks as a waiter.
	w2 := make(chan error, 1)
	go func() { w2 <- lm.Acquire(context.Background(), 2, testTag, ExclusiveLock) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(lm.Waiters(testTag)) != 1 {
		time.Sleep(time.Millisecond)
	}
	if len(lm.Waiters(testTag)) != 1 {
		t.Fatal("backend 2 never queued as waiter")
	}
	// Backend 1 already holds a lock here, so its ShareRowExclusive
	// request jumps ahead of waiter 2 and must be granted immediately
	// even via the NOWAIT path.
	if err := lm.TryAcquire(1, testTag, ShareRowExclusiveLock); err != nil {
		t.Fatalf("early-grant NOWAIT ahead of waiter: %v", err)
	}
	if got := lm.Holders(testTag)[1]; got&bit(ShareRowExclusiveLock) == 0 {
		t.Errorf("holders[1]=%b, want ShareRowExclusive bit set", got)
	}
	// A third backend that holds nothing here must NOT jump the queue:
	// its conflicting request fails fast under NOWAIT.
	if err := lm.TryAcquire(3, testTag, ExclusiveLock); err != ErrLockNotAvailable {
		t.Errorf("backend 3 (no held lock) TryAcquire = %v, want ErrLockNotAvailable", err)
	}
	// Releasing backend 1 lets the parked Exclusive waiter through.
	lm.ReleaseAll(1)
	select {
	case err := <-w2:
		if err != nil {
			t.Errorf("backend 2: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend 2 never woke after backend 1 released")
	}
}

// TestLockManagerIdempotentAcquire: a second Acquire of an already-
// held mode is a no-op. The wire-protocol dispatcher will call this
// path repeatedly for re-prepared statements.
func TestLockManagerIdempotentAcquire(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(context.Background(), 1, testTag, AccessShareLock); err != nil {
		t.Errorf("idempotent acquire: %v", err)
	}
	if got := lm.Holders(testTag)[1]; got != bit(AccessShareLock) {
		t.Errorf("holders[1]=%b, want single AccessShare bit", got)
	}
}

// TestLockManagerWaiterCancellationCleansUp: a cancelled wait must
// leave no residual queue entry — otherwise a stale waiter would
// keep the lockState alive forever and confuse later releases.
func TestLockManagerWaiterCancellationCleansUp(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		got <- lm.Acquire(ctx, 2, testTag, AccessShareLock)
	}()
	// Wait until parked.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(lm.Waiters(testTag)) != 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-got:
		if err == nil {
			t.Error("cancelled Acquire returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Acquire never returned")
	}
	if len(lm.Waiters(testTag)) != 0 {
		t.Errorf("waiters after cancel = %d, want 0", len(lm.Waiters(testTag)))
	}
	// The cancelled backend must not appear as a holder.
	if _, ok := lm.Holders(testTag)[2]; ok {
		t.Errorf("cancelled backend 2 leaked as holder")
	}
}

// TestLockManagerReleaseAllWakesEveryone: ReleaseAll is the txn-end
// hook. Two waiters parked behind a single AccessExclusive holder
// must both wake once the holder calls ReleaseAll.
func TestLockManagerReleaseAllWakesEveryone(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, b := range []BackendID{2, 3} {
		b := b
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- lm.Acquire(context.Background(), b, testTag, AccessShareLock)
		}()
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(lm.Waiters(testTag)) != 2 {
		time.Sleep(time.Millisecond)
	}
	if len(lm.Waiters(testTag)) != 2 {
		t.Fatalf("waiters = %d, want 2", len(lm.Waiters(testTag)))
	}
	lm.ReleaseAll(1)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("waiter Acquire: %v", err)
		}
	}
	if _, ok := lm.Holders(testTag)[1]; ok {
		t.Error("ReleaseAll did not drop backend 1")
	}
}

// TestLockManagerFIFOFairness: head-of-line waiter wakes before
// later waiters even when the later waiter would have been
// compatible with the current holders.
func TestLockManagerFIFOFairness(t *testing.T) {
	lm := New()
	// Backend 1 holds AccessShare. Backend 2 wants AccessExclusive
	// and blocks. Backend 3 then wants AccessShare — could grant
	// against backend 1 alone, but FIFO says queue behind 2.
	if err := lm.Acquire(context.Background(), 1, testTag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	w2 := make(chan error, 1)
	go func() { w2 <- lm.Acquire(context.Background(), 2, testTag, AccessExclusiveLock) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(lm.Waiters(testTag)) != 1 {
		time.Sleep(time.Millisecond)
	}
	w3 := make(chan error, 1)
	go func() { w3 <- lm.Acquire(context.Background(), 3, testTag, AccessShareLock) }()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(lm.Waiters(testTag)) != 2 {
		time.Sleep(time.Millisecond)
	}
	// Verify queue order is [2, 3].
	q := lm.Waiters(testTag)
	if len(q) != 2 || q[0].Backend != 2 || q[1].Backend != 3 {
		t.Fatalf("queue = %+v, want [2, 3]", q)
	}
	// Release backend 1; backend 2 should wake (head of line),
	// backend 3 must remain queued because AccessExclusive blocks
	// AccessShare.
	lm.Release(1, testTag, AccessShareLock)
	select {
	case err := <-w2:
		if err != nil {
			t.Errorf("backend 2: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend 2 never woke")
	}
	select {
	case <-w3:
		t.Error("backend 3 woke out of FIFO order")
	case <-time.After(50 * time.Millisecond):
		// Expected: still queued behind backend 2's
		// AccessExclusive grant.
	}
	// Release backend 2; backend 3 should now wake.
	lm.Release(2, testTag, AccessExclusiveLock)
	select {
	case err := <-w3:
		if err != nil {
			t.Errorf("backend 3: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend 3 never woke after backend 2 released")
	}
}

// TestLockManagerGCEmptiesState: after every holder and waiter is
// gone, the lockState entry must be deleted so the table doesn't
// grow unbounded over a long-running cluster.
func TestLockManagerGCEmptiesState(t *testing.T) {
	lm := New()
	if err := lm.Acquire(context.Background(), 1, testTag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	lm.Release(1, testTag, AccessShareLock)
	if got := lm.Holders(testTag); got != nil {
		t.Errorf("Holders(testTag) = %v after final release, want nil (entry GC'd)", got)
	}
}
