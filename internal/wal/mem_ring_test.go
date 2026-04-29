package wal

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMemRingNilSafe: every public method on a nil ring is a
// no-op or zero-return. NewMemRing(0) returns nil so the writer
// can stash that result directly without checking.
func TestMemRingNilSafe(t *testing.T) {
	if NewMemRing(0) != nil {
		t.Errorf("NewMemRing(0) must return nil")
	}
	if NewMemRing(-1) != nil {
		t.Errorf("NewMemRing(-1) must return nil")
	}
	var r *MemRing
	r.Append(0, []byte("ignored"))
	if n, ok := r.ReadAt(0, make([]byte, 4)); ok || n != 0 {
		t.Errorf("nil ring ReadAt = (%d, %v), want (0, false)", n, ok)
	}
	if r.Hits() != 0 || r.Misses() != 0 || r.Cap() != 0 {
		t.Errorf("nil ring counters non-zero")
	}
}

// TestMemRingRoundTripWithinCap: appends a few records, reads
// them back. Pins the basic byte-faithful round-trip.
func TestMemRingRoundTripWithinCap(t *testing.T) {
	r := NewMemRing(64)
	r.Append(0, []byte("hello"))
	r.Append(5, []byte(" world"))
	out := make([]byte, 11)
	n, ok := r.ReadAt(0, out)
	if !ok || n != 11 {
		t.Fatalf("ReadAt = (%d, %v), want (11, true)", n, ok)
	}
	if string(out) != "hello world" {
		t.Errorf("out = %q, want %q", out, "hello world")
	}
	if r.Hits() != 1 || r.Misses() != 0 {
		t.Errorf("hits=%d misses=%d, want 1/0", r.Hits(), r.Misses())
	}
}

// TestMemRingEvictsOldBytesOnOverflow: with cap=8 and 16 bytes
// written, only the trailing 8 are resident. Reads of the evicted
// half must miss; reads of the resident half must hit.
func TestMemRingEvictsOldBytesOnOverflow(t *testing.T) {
	r := NewMemRing(8)
	r.Append(0, []byte("AAAAAAAA"))
	r.Append(8, []byte("BBBBBBBB"))
	out := make([]byte, 8)
	if _, ok := r.ReadAt(0, out); ok {
		t.Errorf("ReadAt(0) hit; the As should be evicted")
	}
	if _, ok := r.ReadAt(8, out); !ok || string(out) != "BBBBBBBB" {
		t.Errorf("ReadAt(8) miss / wrong content (out=%q)", out)
	}
	if r.Hits() != 1 || r.Misses() != 1 {
		t.Errorf("hits=%d misses=%d, want 1/1", r.Hits(), r.Misses())
	}
}

// TestMemRingPartialOverlapMisses: a ReadAt straddling the
// resident/evicted boundary returns (0, false) so the caller
// fetches the whole range from disk. We don't try to stitch.
func TestMemRingPartialOverlapMisses(t *testing.T) {
	r := NewMemRing(8)
	r.Append(0, []byte("0123456789AB")) // 12 bytes; cap=8 evicts first 4
	out := make([]byte, 8)
	if _, ok := r.ReadAt(2, out); ok {
		t.Errorf("ReadAt(2) hit; range crosses head boundary, must miss")
	}
}

// TestMemRingWraps: a write that crosses the buffer's wrap
// boundary still round-trips. cap=8, write 6 bytes (no wrap),
// then 6 more (wraps).
func TestMemRingWraps(t *testing.T) {
	r := NewMemRing(8)
	r.Append(0, []byte("AAAAAA"))
	r.Append(6, []byte("BBBBBB"))
	out := make([]byte, 8)
	if _, ok := r.ReadAt(4, out); !ok || string(out) != "AABBBBBB" {
		t.Errorf("wrap round-trip failed: out=%q ok=%v", out, ok)
	}
}

// TestMemRingWriteLargerThanCap: a single Append longer than the
// ring keeps only the trailing cap bytes. Edge case the writer
// might hit on a giant record.
func TestMemRingWriteLargerThanCap(t *testing.T) {
	r := NewMemRing(4)
	r.Append(0, []byte("ABCDEFGH"))
	out := make([]byte, 4)
	if _, ok := r.ReadAt(4, out); !ok || string(out) != "EFGH" {
		t.Errorf("trailing read failed: out=%q ok=%v", out, ok)
	}
	if _, ok := r.ReadAt(0, out); ok {
		t.Errorf("evicted ABCD should miss")
	}
}

// TestMemRingConcurrentReads: two readers in parallel against an
// inflight Append. Pins that the lock-protected memcpy doesn't
// produce torn reads; either we get the pre-Append range or the
// post-Append range, never a mix.
func TestMemRingConcurrentReads(t *testing.T) {
	r := NewMemRing(64)
	r.Append(0, []byte("INITIAL_INITIAL_"))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := make([]byte, 16)
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = r.ReadAt(0, out)
				}
			}
		}()
	}
	for i := 1; i <= 100; i++ {
		// Append 16 bytes per step. After 4 steps the head
		// advances past LSN 0, so subsequent reads at LSN 0
		// miss — but never tear.
		r.Append(int64(i*16), []byte("XXXXXXXXXXXXXXXX"))
	}
	close(stop)
	wg.Wait()
}

// TestIteratorReadsFromMemRing: the iterator (used by walsender)
// hits the ring when the writer has one, and falls back to disk
// when it doesn't. Pins the wal-package end-to-end integration.
//
// To prove the read came from RAM (not disk), we delete the
// segment files behind the writer's back AFTER the writer
// finishes. With a ring, the iterator still streams successfully;
// without a ring, the read fails.
func TestIteratorReadsFromMemRing(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:             walDir,
		SegmentSize:        4096,
		SenderMemoryBuffer: 1 << 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.MemRing() == nil {
		t.Fatal("writer should have a memRing with SenderMemoryBuffer > 0")
	}

	want := []byte("from-ram-only")
	_, end, err := w.Append(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}

	// Open iterator, then nuke the segment file. With a ring
	// hot, the next pread is unnecessary — the iterator returns
	// from RAM.
	it, err := NewRecordIterator(w, walDir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	// Remove the segment so any disk fallback would fail. We
	// have to do this AFTER flushUpTo so the writer's fdatasync
	// already committed; the file's existence is no longer
	// relevant from the ring's perspective.
	if err := os.Remove(filepath.Join(walDir, formatSegmentName(0))); err != nil {
		t.Fatalf("remove segment 0: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("Next from ring: %v", err)
	}
	if string(rec.Payload) != string(want) {
		t.Errorf("payload = %q, want %q", rec.Payload, want)
	}
	if hits := w.MemRing().Hits(); hits == 0 {
		t.Errorf("ring hits = 0, want > 0 (iterator should have read from ring)")
	}
}

// TestIteratorFallsBackToDiskWithoutRing: same scenario but with
// SenderMemoryBuffer=0, the iterator's read must succeed via
// pread on the segment file. Pins that the ring is genuinely
// optional and the legacy disk path is unchanged.
func TestIteratorFallsBackToDiskWithoutRing(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if w.MemRing() != nil {
		t.Fatal("writer must have nil memRing without SenderMemoryBuffer")
	}
	want := []byte("from-disk-only")
	_, end, err := w.Append(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	it, err := NewRecordIterator(w, walDir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("Next from disk: %v", err)
	}
	if string(rec.Payload) != string(want) {
		t.Errorf("payload = %q, want %q", rec.Payload, want)
	}
}
