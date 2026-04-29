package wal

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestDirectIODisabledByDefault: with Config.DirectIO==false the
// writer reports "not requested" via DirectIORequested + empty
// fallback reason — confirms the probe never runs when the
// operator hasn't asked for it (the probe is a real syscall, so
// we don't want to pay for it on every server start).
func TestDirectIODisabledByDefault(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if w.DirectIORequested() {
		t.Errorf("DirectIORequested() = true, want false (no Config.DirectIO)")
	}
	if reason := w.DirectIOFallbackReason(); reason != "" {
		t.Errorf("DirectIOFallbackReason() = %q, want empty (probe should not have run)", reason)
	}
}

// TestDirectIOEnabledProbesFilesystem: with Config.DirectIO=true
// the writer runs the O_DIRECT probe and reports the outcome via
// DirectIOFallbackReason. On Linux ext4 / XFS the probe succeeds
// and the reason is empty; on Linux tmpfs / overlayfs / non-Linux
// the probe falls back and the reason is non-empty. Either way
// NewWriter returns a usable writer — the fallback path is the
// same buffered-write code that runs when DirectIO is off.
//
// The test pins the contract: DirectIORequested mirrors the
// Config flag, and exactly one of {direct-I/O active, fallback
// reason set} holds.
func TestDirectIOEnabledProbesFilesystem(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128, DirectIO: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !w.DirectIORequested() {
		t.Error("DirectIORequested() = false, want true (Config.DirectIO=true)")
	}
	reason := w.DirectIOFallbackReason()
	if runtime.GOOS != "linux" {
		// Non-Linux: probe always falls back. Reason must be
		// non-empty so the startup logger has something to
		// surface.
		if reason == "" {
			t.Errorf("non-Linux DirectIOFallbackReason()=empty, want non-empty (O_DIRECT is Linux-only)")
		}
		return
	}
	// Linux: probe outcome is filesystem-dependent. Either is
	// acceptable; the writer must be usable in either case.
	t.Logf("Linux probe outcome: reason=%q", reason)

	// Sanity-check: writer must round-trip a record regardless
	// of probe outcome (Phase 1 doesn't yet flip O_DIRECT on
	// segment opens, so writes always go through the buffered
	// path).
	if _, _, err := w.Append([]byte("direct-io probe")); err != nil {
		t.Fatalf("Append after probe: %v", err)
	}
}

// TestDirectIOFallbackReasonStable: the probe runs once at
// construction, and DirectIOFallbackReason returns the same value
// across repeated calls. Pins the "stable for the lifetime of the
// Writer" contract from the godoc.
func TestDirectIOFallbackReasonStable(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128, DirectIO: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	first := w.DirectIOFallbackReason()
	for i := 0; i < 3; i++ {
		if got := w.DirectIOFallbackReason(); got != first {
			t.Errorf("call %d: %q, want stable %q", i, got, first)
		}
	}
}
