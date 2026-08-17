package transam

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestWaitForOlderSlotsToCommit_NoActive verifies that WaitForOlderSlotsToCommit
// returns immediately when no other transactions are active. M0100-0009.
func TestWaitForOlderSlotsToCommit_NoActive(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	// No other transactions active — should return immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.WaitForOlderSlotsToCommit(ctx, tx.Handle); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = m.Commit(tx)
}

// TestWaitForOlderSlotsToCommit_BlocksUntilOtherCommits verifies that
// WaitForOlderSlotsToCommit blocks until a concurrently-active transaction
// commits. M0100-0009.
func TestWaitForOlderSlotsToCommit_BlocksUntilOtherCommits(t *testing.T) {
	m := NewManager()

	// Start the "other" transaction — this simulates s1's BEGIN.
	other, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}

	// Start the DROP CONCURRENTLY transaction.
	dropper, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// WaitForOlderSlotsToCommit should block until `other` commits.
	done := make(chan error, 1)
	go func() {
		done <- m.WaitForOlderSlotsToCommit(ctx, dropper.Handle)
	}()

	// Give the waiter goroutine time to start and block.
	time.Sleep(50 * time.Millisecond)

	// Ensure it has NOT returned yet.
	select {
	case err := <-done:
		t.Fatalf("WaitForOlderSlotsToCommit returned early: %v", err)
	default:
	}

	// Commit the "other" transaction — the waiter should now unblock.
	if err := m.Commit(other); err != nil {
		t.Fatalf("Commit other: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForOlderSlotsToCommit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForOlderSlotsToCommit did not unblock after other committed")
	}

	_ = m.Commit(dropper)
}

// TestWaitForOlderSlotsToCommit_CancelContext verifies that context
// cancellation unblocks the waiter. M0100-0009.
func TestWaitForOlderSlotsToCommit_CancelContext(t *testing.T) {
	m := NewManager()

	other, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	dropper, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- m.WaitForOlderSlotsToCommit(ctx, dropper.Handle)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected context cancellation error, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForOlderSlotsToCommit did not unblock on cancel")
	}

	// Cleanup.
	_ = m.Rollback(other)
	_ = m.Commit(dropper)
}

// TestWaitForOlderSlotsToCommit_DoesNotWaitForSelf verifies that the caller's
// own slot is excluded from the wait set — prevents self-deadlock.
// M0100-0009.
func TestWaitForOlderSlotsToCommit_DoesNotWaitForSelf(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Only the caller itself is in a transaction; should return immediately.
	if err := m.WaitForOlderSlotsToCommit(ctx, tx.Handle); err != nil {
		t.Fatalf("should not block on self: %v", err)
	}
	_ = m.Commit(tx)
}

// TestWaitForOlderSlotsToCommit_MultipleOthers verifies that the waiter
// blocks until ALL other active transactions complete. M0100-0009.
func TestWaitForOlderSlotsToCommit_MultipleOthers(t *testing.T) {
	m := NewManager()

	t1, _ := m.Begin(IsolationReadCommitted)
	t2, _ := m.Begin(IsolationReadCommitted)
	dropper, _ := m.Begin(IsolationReadCommitted)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- m.WaitForOlderSlotsToCommit(ctx, dropper.Handle)
	}()

	time.Sleep(50 * time.Millisecond)

	// Commit t1 first — waiter should still block (t2 active).
	if err := m.Commit(t1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("returned early after t1 commit (t2 still active): %v", err)
	default:
	}

	// Now commit t2 — waiter should unblock.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		_ = m.Commit(t2)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForOlderSlotsToCommit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not unblock after t2 committed")
	}
	wg.Wait()
	_ = m.Commit(dropper)
}
