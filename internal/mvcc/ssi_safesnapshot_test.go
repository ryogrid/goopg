package mvcc

import (
	"testing"
	"time"
)

// snapshotInBackground starts SnapshotFor(tx) in a goroutine and returns a
// channel that is closed once it returns. Used to observe whether the
// GetSafeSnapshot deferral blocks a READ ONLY DEFERRABLE transaction's first
// snapshot acquisition. M0118-0001 (read-only-anomaly-3).
func snapshotInBackground(t *testing.T, m *Manager, tx Transaction) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		if _, err := m.SnapshotFor(tx); err != nil {
			t.Errorf("SnapshotFor: %v", err)
		}
		close(done)
	}()
	return done
}

// assertBlocked fails if done closes within the window (the snapshot was NOT
// deferred when it should have been).
func assertBlocked(t *testing.T, done <-chan struct{}, window time.Duration, msg string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("%s: SnapshotFor returned before it was safe", msg)
	case <-time.After(window):
	}
}

// assertUnblocked fails if done does not close within the window (the snapshot
// never became safe / the waiter was not woken).
func assertUnblocked(t *testing.T, done <-chan struct{}, window time.Duration, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(window):
		t.Fatalf("%s: SnapshotFor did not unblock", msg)
	}
}

// TestSafeSnapshotDeferrableWaitsForWriter verifies the core GetSafeSnapshot
// behaviour: a SERIALIZABLE READ ONLY DEFERRABLE transaction defers its first
// snapshot until a concurrent declared-read-write SERIALIZABLE transaction
// commits, then proceeds (it is never aborted).
func TestSafeSnapshotDeferrableWaitsForWriter(t *testing.T) {
	m := NewManager()

	// Declared read-write SERIALIZABLE writer, active.
	w, err := m.Begin(IsolationSerializable, 1)
	if err != nil {
		t.Fatalf("Begin writer: %v", err)
	}
	m.MarkSerializableModes(w.Handle, false /*readOnly*/, false /*deferrable*/)

	// READ ONLY DEFERRABLE reader.
	r, err := m.Begin(IsolationSerializable, 2)
	if err != nil {
		t.Fatalf("Begin reader: %v", err)
	}
	m.MarkSerializableModes(r.Handle, true /*readOnly*/, true /*deferrable*/)

	done := snapshotInBackground(t, m, r)
	assertBlocked(t, done, 150*time.Millisecond, "writer still active")

	// Committing the writer must wake the reader's safe-snapshot wait.
	if err := m.Commit(w); err != nil {
		t.Fatalf("Commit writer: %v", err)
	}
	assertUnblocked(t, done, 2*time.Second, "after writer commit")
}

// TestSafeSnapshotDeferrableUnblockedByAbort verifies that a concurrent
// writer's ABORT (not just commit) also satisfies the safe-snapshot wait —
// finish() stamps FinishedAt and broadcasts for both outcomes.
func TestSafeSnapshotDeferrableUnblockedByAbort(t *testing.T) {
	m := NewManager()

	w, err := m.Begin(IsolationSerializable, 1)
	if err != nil {
		t.Fatalf("Begin writer: %v", err)
	}
	m.MarkSerializableModes(w.Handle, false, false)

	r, err := m.Begin(IsolationSerializable, 2)
	if err != nil {
		t.Fatalf("Begin reader: %v", err)
	}
	m.MarkSerializableModes(r.Handle, true, true)

	done := snapshotInBackground(t, m, r)
	assertBlocked(t, done, 150*time.Millisecond, "writer still active")

	if err := m.Rollback(w); err != nil {
		t.Fatalf("Rollback writer: %v", err)
	}
	assertUnblocked(t, done, 2*time.Second, "after writer abort")
}

// TestSafeSnapshotDeferrableNoWritersImmediate verifies that a READ ONLY
// DEFERRABLE transaction with no concurrent writers does not block — the safe
// snapshot is available immediately.
func TestSafeSnapshotDeferrableNoWritersImmediate(t *testing.T) {
	m := NewManager()

	r, err := m.Begin(IsolationSerializable, 1)
	if err != nil {
		t.Fatalf("Begin reader: %v", err)
	}
	m.MarkSerializableModes(r.Handle, true, true)

	done := snapshotInBackground(t, m, r)
	assertUnblocked(t, done, 1*time.Second, "no concurrent writers")
}

// TestSafeSnapshotNonDeferrableNoWait verifies that a SERIALIZABLE READ ONLY
// transaction that is NOT declared DEFERRABLE never defers, even with a
// concurrent active writer — only the DEFERRABLE mode requests GetSafeSnapshot.
func TestSafeSnapshotNonDeferrableNoWait(t *testing.T) {
	m := NewManager()

	w, err := m.Begin(IsolationSerializable, 1)
	if err != nil {
		t.Fatalf("Begin writer: %v", err)
	}
	m.MarkSerializableModes(w.Handle, false, false)

	r, err := m.Begin(IsolationSerializable, 2)
	if err != nil {
		t.Fatalf("Begin reader: %v", err)
	}
	m.MarkSerializableModes(r.Handle, true /*readOnly*/, false /*deferrable*/)

	done := snapshotInBackground(t, m, r)
	assertUnblocked(t, done, 1*time.Second, "read only but not deferrable")
}

// TestSafeSnapshotIgnoresReadOnlyPeers verifies that a concurrent SERIALIZABLE
// READ ONLY peer does not extend the wait — only declared read-write peers can
// place the deferrable reader in a dangerous structure, so only they are
// enrolled in the drain set.
func TestSafeSnapshotIgnoresReadOnlyPeers(t *testing.T) {
	m := NewManager()

	// A concurrent READ ONLY (non-deferrable) SERIALIZABLE peer, left active.
	peer, err := m.Begin(IsolationSerializable, 1)
	if err != nil {
		t.Fatalf("Begin peer: %v", err)
	}
	m.MarkSerializableModes(peer.Handle, true /*readOnly*/, false)

	r, err := m.Begin(IsolationSerializable, 2)
	if err != nil {
		t.Fatalf("Begin reader: %v", err)
	}
	m.MarkSerializableModes(r.Handle, true, true)

	done := snapshotInBackground(t, m, r)
	assertUnblocked(t, done, 1*time.Second, "only read-only peers active")
}
