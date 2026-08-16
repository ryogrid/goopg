package xlog

import (
	"path/filepath"
	"testing"
)

// TestStripeWriterCoreMountedAfterNewWriter pins the slice B call-site
// rewrite part 1 invariant: every freshly-constructed *Writer has a
// non-nil *stripeWriterCore that borrows the same walBuf/memRing as
// the legacy state.append path. Foundations 1–15 packaged the core in
// isolation; this test pins the mount-point so the call-site rewrite
// (parts 2/3 — switching state.append's body to s.core.Append and the
// drain prelude to s.core.PublishUpTo) finds the field where it
// expects.
func TestStripeWriterCoreMountedAfterNewWriter(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:             filepath.Clean(dir),
		SegmentSize:        DefaultSegmentSize,
		SenderMemoryBuffer: 64 * 1024,
		WALBuffers:         64 * 1024,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if w.core == nil {
		t.Fatal("Writer.core is nil after NewWriter; expected the slice B mount-point to be populated")
	}
	// Ring borrowing — the core must reference the same rings as the
	// legacy state.append path so on-disk visibility stays unified.
	if w.core.memRing != w.memRing {
		t.Errorf("core.memRing pointer = %p, want %p (Writer.memRing)", w.core.memRing, w.memRing)
	}
	if w.core.walBuf != w.stateRef.walBuf {
		t.Errorf("core.walBuf pointer = %p, want %p (state.walBuf)", w.core.walBuf, w.stateRef.walBuf)
	}
	// Position tracker must start at the loaded writePos / prevRecPtr
	// so the recovery-resume contract holds on the very first append.
	gotCurr, gotPrev := w.core.Load()
	wantCurr := uint64(w.stateRef.writePos)
	wantPrev := w.stateRef.prevRecPtr
	if gotCurr != wantCurr {
		t.Errorf("core.Load curr = %d, want %d (state.writePos)", gotCurr, wantCurr)
	}
	if gotPrev != wantPrev {
		t.Errorf("core.Load prev = %d, want %d (state.prevRecPtr)", gotPrev, wantPrev)
	}
	// Owned primitives wired non-nil so transitional callers (the
	// rewrite's parts 2/3) do not need defensive nil-checks beyond
	// the core itself.
	if w.core.locks == nil {
		t.Error("core.locks is nil; appendLockSet must be owned by the core")
	}
	if w.core.posTracker == nil {
		t.Error("core.posTracker is nil; insertPosTracker must be owned by the core")
	}
	if w.core.inserting == nil {
		t.Error("core.inserting is nil; insertionTracker must be owned by the core")
	}
	if w.core.publisher == nil {
		t.Error("core.publisher is nil; tailPublisher must be owned by the core")
	}
}

// TestStripeWriterCoreMountedAcceptsBareConfig pins the mount-point's
// nil-ring resilience: NewWriter with the smallest possible Config
// (WALBuffers=0, SenderMemoryBuffer=0) still constructs a core. The
// ring pointers propagate nil verbatim — each composing foundation is
// independently nil-safe per slice B's design — so the call-site
// rewrite's `s.core.Append(...)` call survives the legacy "no
// wal_buffers, no sender memory" deployment.
func TestStripeWriterCoreMountedAcceptsBareConfig(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      filepath.Clean(dir),
		SegmentSize: DefaultSegmentSize,
		// WALBuffers and SenderMemoryBuffer intentionally zero.
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if w.core == nil {
		t.Fatal("Writer.core is nil under bare Config; nil rings must not gate mounting")
	}
	if w.core.walBuf != nil {
		t.Errorf("core.walBuf = %p, want nil (WALBuffers=0)", w.core.walBuf)
	}
	if w.core.memRing != nil {
		t.Errorf("core.memRing = %p, want nil (SenderMemoryBuffer=0)", w.core.memRing)
	}
	// Tracker must still report (0, 0) for a fresh empty WAL dir.
	curr, prev := w.core.Load()
	if curr != 0 || prev != 0 {
		t.Errorf("core.Load = (%d, %d), want (0, 0) for fresh WAL dir", curr, prev)
	}
	// PublishedTail must read 0 — no append has happened, so no
	// watermark advance.
	if got := w.core.PublishedTail(); got != 0 {
		t.Errorf("core.PublishedTail = %d, want 0", got)
	}
}
