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

// TestDirectIORoundTripWithPreallocation pins the Phase 2
// behaviour: with DirectIO=on AND wal_init_zero=on (the supported
// production combination — preallocation guarantees the segment
// body is fully laid down so RMW reads always succeed), records
// round-trip through the kernel via the per-write RMW path.
// Skips when the probe rejected O_DIRECT (the test host's tmpdir
// isn't on a DIO-capable FS) — same idiom as the M0009 io_uring
// tests. SegmentSize is chosen as 4 KiB (one block) to exercise
// the regular writeAtDirectIO loop without crossing segments.
func TestDirectIORoundTripWithPreallocation(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: 4096,
		DirectIO:    true,
		Preallocate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reason := w.DirectIOFallbackReason(); reason != "" {
		t.Skipf("O_DIRECT unsupported on this host: %s", reason)
	}

	payloads := [][]byte{
		[]byte("alpha"),
		[]byte("bravo charlie"),
		[]byte("delta echo foxtrot golf"),
	}
	var lastEnd uint64
	for _, p := range payloads {
		_, end, err := w.Append(p)
		if err != nil {
			t.Fatalf("Append %q: %v", p, err)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(payloads) {
		t.Fatalf("record count = %d, want %d", len(recs), len(payloads))
	}
	for i, p := range payloads {
		if got := string(recs[i].Payload); got != string(p) {
			t.Errorf("record[%d] = %q, want %q", i, got, p)
		}
	}
}

// TestDirectIORecordSpanningBlocks: a single payload large enough
// to straddle multiple 4 KiB blocks exercises writeAtDirectIO's
// region-len math when the user bytes don't align to block
// boundaries. SegmentSize is one MiB so the record fits in a
// single segment but crosses ~3 block boundaries. Skips on probe
// fallback like the round-trip test.
func TestDirectIORecordSpanningBlocks(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: 1 << 20,
		DirectIO:    true,
		Preallocate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reason := w.DirectIOFallbackReason(); reason != "" {
		t.Skipf("O_DIRECT unsupported on this host: %s", reason)
	}

	// Build a payload that lands at offset ~1.5 KiB (after the
	// 12-byte record header) and stretches past the third block
	// boundary so the RMW touches blocks 0..3.
	payload := make([]byte, 12000) // ~3 blocks
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("record count = %d, want 1", len(recs))
	}
	if len(recs[0].Payload) != len(payload) {
		t.Fatalf("payload length = %d, want %d", len(recs[0].Payload), len(payload))
	}
	for i := range payload {
		if recs[0].Payload[i] != payload[i] {
			t.Fatalf("payload[%d] = %d, want %d", i, recs[0].Payload[i], payload[i])
		}
	}
}
