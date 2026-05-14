package mvcc

import (
	"errors"
	"testing"
)

// TestPreCommitCheck_NoOpForRC asserts that a READ COMMITTED transaction
// hitting the commit path bypasses the SSI pre-commit scan entirely —
// RC xacts are never registered in `ssiState.xacts`, so the check must
// be a silent no-op.
func TestPreCommitCheck_NoOpForRC(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin RC: %v", err)
	}
	if err := m.PreCommitCheckForSerializationFailure(tx.Handle); err != nil {
		t.Fatalf("PreCommitCheck(RC) = %v, want nil", err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("Commit(RC) = %v, want nil", err)
	}
}

// TestPreCommitCheck_NoOpForReadOnlySerializable asserts a SERIALIZABLE
// transaction that read nothing and wrote nothing — no edges in its
// rw-graph — commits cleanly. The scan must not flag false positives
// against an empty graph.
func TestPreCommitCheck_NoOpForReadOnlySerializable(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := m.PreCommitCheckForSerializationFailure(tx.Handle); err != nil {
		t.Fatalf("PreCommitCheck(empty graph) = %v, want nil", err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("Commit(read-only SERIALIZABLE) = %v, want nil", err)
	}
}

// TestPreCommitCheck_AlreadyDoomedReturns40001 asserts that a
// SERIALIZABLE transaction whose Doomed flag is set (by a peer's
// earlier dangerous-structure scan) fails at its own commit with
// SQLSTATE 40001. Pins the "Canceled on identification as a pivot"
// branch.
func TestPreCommitCheck_AlreadyDoomedReturns40001(t *testing.T) {
	m := NewManager()
	tx := beginAndAssign(t, m)
	if !m.MarkDoomedForTest(tx.Handle) {
		t.Fatal("MarkDoomedForTest failed to mark")
	}
	err := m.PreCommitCheckForSerializationFailure(tx.Handle)
	if err == nil {
		t.Fatal("PreCommitCheck(doomed) = nil, want SerializationFailureError")
	}
	var sfe *SerializationFailureError
	if !errors.As(err, &sfe) {
		t.Fatalf("err type = %T, want *SerializationFailureError", err)
	}
	if got := sfe.SQLSTATE(); got != "40001" {
		t.Fatalf("SQLSTATE = %q, want 40001", got)
	}
	if !IsSerializationFailure(err) {
		t.Fatal("IsSerializationFailure returned false on a SerializationFailureError")
	}
	// Cleanup: Commit must propagate the same error; Rollback then
	// drains the active set.
	if err := m.Commit(tx); err == nil {
		t.Fatal("Commit(doomed) = nil, want SerializationFailureError")
	}
	if err := m.Rollback(tx); err != nil {
		t.Fatalf("Rollback after failed commit: %v", err)
	}
}

// TestPreCommitCheck_WriteSkewDoomsPivot pins the canonical 2-cycle
// dangerous-structure detection: T1 reads X, T2 reads Y, T1 writes Y,
// T2 writes X. The rw-conflict graph is `T1 -rw-> T2` and
// `T2 -rw-> T1`, a 2-cycle. When T1 commits first, its pre-commit scan
// must doom T2 (walking T1.inConflicts -> [T2] -> T2.inConflicts ->
// [T1==me]). T1 commits cleanly; T2's later commit fails with 40001.
//
// The test exercises the M0104-0006 wiring end-to-end (no executor
// involvement): edges are installed via the public read-path and
// write-path hooks (M0104-0004/0005), pre-commit runs from
// Manager.finish on T1's Commit, and T2's Commit returns the typed
// SerializationFailureError.
func TestPreCommitCheck_WriteSkewDoomsPivot(t *testing.T) {
	m := NewManager()
	t1 := beginAndAssign(t, m)
	t2 := beginAndAssign(t, m)

	// Install the 2-cycle directly via the conflict-tracking helpers
	// the executor will call once write-path wiring lands. Use the
	// write-path hook so the edge orientation matches the production
	// path: T1 writes a target T2 SIREAD-holds → `T2 -rw-> T1`, then
	// T2 writes a target T1 SIREAD-holds → `T1 -rw-> T2`.
	xTag := TupleLockTag(7, 100, 0, 1)
	yTag := TupleLockTag(7, 101, 0, 1)
	m.AcquirePredicateLock(t1.Handle, xTag) // T1 reads X
	m.AcquirePredicateLock(t2.Handle, yTag) // T2 reads Y
	if !m.CheckForSerializableConflictIn(t1.Handle, yTag) {
		t.Fatal("T1 writes Y: expected new edge T2->T1")
	}
	if !m.CheckForSerializableConflictIn(t2.Handle, xTag) {
		t.Fatal("T2 writes X: expected new edge T1->T2")
	}
	if !m.HasRWConflict(t1.Handle, t2.Handle) {
		t.Fatal("expected T1 -rw-> T2 edge")
	}
	if !m.HasRWConflict(t2.Handle, t1.Handle) {
		t.Fatal("expected T2 -rw-> T1 edge")
	}

	// T1 commits first. The pre-commit scan walks T1.inConflicts=[T2],
	// then T2.inConflicts=[T1==me] → dooms T2. T1 itself is not doomed.
	if m.IsDoomedForTest(t2.Handle) {
		t.Fatal("T2 doomed before T1's pre-commit ran")
	}
	if err := m.Commit(t1); err != nil {
		t.Fatalf("Commit(T1): %v", err)
	}
	if !m.IsDoomedForTest(t2.Handle) {
		t.Fatal("T2 not doomed after T1 commit; pre-commit scan failed to detect the 2-cycle")
	}

	// T2's commit must now fail with SerializationFailureError.
	err := m.Commit(t2)
	if err == nil {
		t.Fatal("Commit(T2) = nil, want SerializationFailureError")
	}
	if !IsSerializationFailure(err) {
		t.Fatalf("Commit(T2) error type = %T (%v), want *SerializationFailureError", err, err)
	}
	// The aborting xact must still be cleanable via Rollback so the
	// active map is drained. Without this contract the executor would
	// leak the transaction handle.
	if err := m.Rollback(t2); err != nil {
		t.Fatalf("Rollback(T2) after failed commit: %v", err)
	}
	if got := m.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount = %d after both xacts finished, want 0", got)
	}
}

// TestPreCommitCheck_ThreeNodeCycleDoomsPivot pins the 3-node dangerous
// structure `T0 -rw-> Pivot -rw-> Me`: when Me commits with the scan,
// it finds Pivot still in-flight and Pivot's inConflict T0 still
// in-flight, so the scan dooms Pivot. Both T0 and Me commit cleanly;
// Pivot's later commit fails with 40001.
func TestPreCommitCheck_ThreeNodeCycleDoomsPivot(t *testing.T) {
	m := NewManager()
	t0 := beginAndAssign(t, m)
	pivot := beginAndAssign(t, m)
	me := beginAndAssign(t, m)

	// Build `T0 -rw-> Pivot` and `Pivot -rw-> Me` via the write-path
	// hook (the polarity is the same as the read-path; we just need
	// the resulting edges, not the discovery side).
	tag1 := TupleLockTag(7, 100, 0, 1)
	tag2 := TupleLockTag(7, 101, 0, 1)
	m.AcquirePredicateLock(t0.Handle, tag1)
	m.AcquirePredicateLock(pivot.Handle, tag2)
	if !m.CheckForSerializableConflictIn(pivot.Handle, tag1) {
		t.Fatal("expected new T0 -rw-> Pivot edge")
	}
	if !m.CheckForSerializableConflictIn(me.Handle, tag2) {
		t.Fatal("expected new Pivot -rw-> Me edge")
	}

	// Me commits first. The scan walks me.inConflicts=[Pivot] →
	// Pivot.inConflicts=[T0]; T0 is in-flight, so Pivot is doomed.
	if err := m.Commit(me); err != nil {
		t.Fatalf("Commit(me): %v", err)
	}
	if !m.IsDoomedForTest(pivot.Handle) {
		t.Fatal("Pivot not doomed after me commit")
	}

	// T0 commits cleanly — its inConflicts are empty.
	if err := m.Commit(t0); err != nil {
		t.Fatalf("Commit(T0): %v", err)
	}

	// Pivot's commit fails.
	if err := m.Commit(pivot); err == nil {
		t.Fatal("Commit(pivot) = nil, want SerializationFailureError")
	} else if !IsSerializationFailure(err) {
		t.Fatalf("Commit(pivot) error type = %T (%v), want *SerializationFailureError", err, err)
	}
	if err := m.Rollback(pivot); err != nil {
		t.Fatalf("Rollback(pivot): %v", err)
	}
}

// TestPreCommitCheck_LinearChainIsSafe asserts the scan does NOT doom a
// pivot when the only structure is a linear `T0 -rw-> Pivot -rw-> Me`
// where T0 has already committed. Upstream's check requires the Tin
// side to be "in-flight and not doomed" (or == me); a committed Tin is
// not dangerous because the read-write order is already fixed in time.
//
// Note: the current first-slice substrate scrubs the committed T0 from
// Pivot.inConflicts at finish time. So once T0 commits, the scan from
// Me will not even find a Tin candidate. The test pins both behaviours
// — no doom, and Me's commit succeeds.
func TestPreCommitCheck_LinearChainIsSafe(t *testing.T) {
	m := NewManager()
	t0 := beginAndAssign(t, m)
	pivot := beginAndAssign(t, m)
	me := beginAndAssign(t, m)

	tag1 := TupleLockTag(7, 100, 0, 1)
	tag2 := TupleLockTag(7, 101, 0, 1)
	m.AcquirePredicateLock(t0.Handle, tag1)
	m.AcquirePredicateLock(pivot.Handle, tag2)
	if !m.CheckForSerializableConflictIn(pivot.Handle, tag1) {
		t.Fatal("expected new T0 -rw-> Pivot edge")
	}
	if !m.CheckForSerializableConflictIn(me.Handle, tag2) {
		t.Fatal("expected new Pivot -rw-> Me edge")
	}

	// T0 commits first; its edges are scrubbed from Pivot.inConflicts.
	if err := m.Commit(t0); err != nil {
		t.Fatalf("Commit(T0): %v", err)
	}
	// Me's scan now sees Pivot.inConflicts = []; nothing to doom.
	if err := m.Commit(me); err != nil {
		t.Fatalf("Commit(me): %v", err)
	}
	if m.IsDoomedForTest(pivot.Handle) {
		t.Fatal("Pivot doomed by linear chain; expected no doom")
	}
	if err := m.Commit(pivot); err != nil {
		t.Fatalf("Commit(pivot): %v", err)
	}
}

// TestPreCommitCheck_FinishedPivotIgnored asserts the scan skips
// pivots whose FinishedAt has been stamped — they cannot produce a new
// anomaly. This is a defensive pin against a future change to retain
// finished xacts in ssiState.xacts past their FinishedAt.
func TestPreCommitCheck_FinishedPivotIgnored(t *testing.T) {
	m := NewManager()
	me := beginAndAssign(t, m)
	pivot := beginAndAssign(t, m)
	tin := beginAndAssign(t, m)

	// Build `Tin -rw-> Pivot -rw-> Me`.
	tag1 := TupleLockTag(7, 100, 0, 1)
	tag2 := TupleLockTag(7, 101, 0, 1)
	m.AcquirePredicateLock(tin.Handle, tag1)
	m.AcquirePredicateLock(pivot.Handle, tag2)
	if !m.CheckForSerializableConflictIn(pivot.Handle, tag1) {
		t.Fatal("expected new Tin -rw-> Pivot edge")
	}
	if !m.CheckForSerializableConflictIn(me.Handle, tag2) {
		t.Fatal("expected new Pivot -rw-> Me edge")
	}

	// Manually mark pivot as if its FinishedAt had been stamped.
	m.mu.Lock()
	if sx, ok := m.ssiState.xacts[pivot.Handle]; ok {
		sx.FinishedAt = 99
	}
	m.mu.Unlock()

	if err := m.PreCommitCheckForSerializationFailure(me.Handle); err != nil {
		t.Fatalf("PreCommitCheck(me) = %v, want nil", err)
	}
	if m.IsDoomedForTest(pivot.Handle) {
		t.Fatal("finished pivot was doomed by scan; expected skip")
	}

	// Reset for clean teardown.
	m.mu.Lock()
	if sx, ok := m.ssiState.xacts[pivot.Handle]; ok {
		sx.FinishedAt = InvalidCommitSeqNo
	}
	m.mu.Unlock()
	_ = m.Rollback(me)
	_ = m.Rollback(pivot)
	_ = m.Rollback(tin)
}

// TestPreCommitCheck_IdempotentDoomedPivot asserts that running the
// scan multiple times against an already-doomed pivot is a no-op — the
// pivot stays doomed, and no panics arise from re-walking the same
// edge.
func TestPreCommitCheck_IdempotentDoomedPivot(t *testing.T) {
	m := NewManager()
	t1 := beginAndAssign(t, m)
	t2 := beginAndAssign(t, m)

	xTag := TupleLockTag(7, 100, 0, 1)
	yTag := TupleLockTag(7, 101, 0, 1)
	m.AcquirePredicateLock(t1.Handle, xTag)
	m.AcquirePredicateLock(t2.Handle, yTag)
	_ = m.CheckForSerializableConflictIn(t1.Handle, yTag)
	_ = m.CheckForSerializableConflictIn(t2.Handle, xTag)

	if err := m.PreCommitCheckForSerializationFailure(t1.Handle); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if !m.IsDoomedForTest(t2.Handle) {
		t.Fatal("T2 not doomed after first scan")
	}
	// Second invocation walks the same graph; pivot already doomed,
	// scan skips the inner loop.
	if err := m.PreCommitCheckForSerializationFailure(t1.Handle); err != nil {
		t.Fatalf("second scan: %v", err)
	}

	_ = m.Commit(t1)
	if err := m.Commit(t2); err == nil {
		t.Fatal("Commit(T2) succeeded; expected 40001")
	} else if !IsSerializationFailure(err) {
		t.Fatalf("Commit(T2) err = %T (%v), want *SerializationFailureError", err, err)
	}
	_ = m.Rollback(t2)
}
