package wal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendFlushAndReadAll(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}

	start1, end1, err := w.Append([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	start2, end2, err := w.Append([]byte("world"))
	if err != nil {
		t.Fatal(err)
	}
	if start1 != 1 {
		t.Fatalf("first record start LSN = %d, want 1", start1)
	}
	if start2 != end1+1 {
		t.Fatalf("second record start LSN = %d, want %d", start2, end1+1)
	}
	if err := w.FlushUpTo(end2); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("record count = %d, want 2", len(recs))
	}
	if got := string(recs[0].Payload); got != "hello" {
		t.Fatalf("record[0] = %q, want hello", got)
	}
	if got := string(recs[1].Payload); got != "world" {
		t.Fatalf("record[1] = %q, want world", got)
	}
	if recs[0].StartLSN != start1 || recs[0].EndLSN != end1 {
		t.Fatalf("record[0] LSN range = [%d,%d], want [%d,%d]", recs[0].StartLSN, recs[0].EndLSN, start1, end1)
	}
	if recs[1].StartLSN != start2 || recs[1].EndLSN != end2 {
		t.Fatalf("record[1] LSN range = [%d,%d], want [%d,%d]", recs[1].StartLSN, recs[1].EndLSN, start2, end2)
	}
}

// TestWriterFsyncCountRealSignal pins the M0122-0003 pg_stat_io follow-up:
// Writer.FsyncCount() must reflect real fdatasync(2) calls made by
// FlushUpTo, one per segment actually synced — not a fabricated value.
func TestWriterFsyncCountRealSignal(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if got := w.FsyncCount(); got != 0 {
		t.Fatalf("FsyncCount before any flush = %d, want 0", got)
	}

	_, end1, err := w.Append([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end1); err != nil {
		t.Fatal(err)
	}
	if got := w.FsyncCount(); got != 1 {
		t.Fatalf("FsyncCount after first flush = %d, want 1", got)
	}

	_, end2, err := w.Append([]byte("world"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end2); err != nil {
		t.Fatal(err)
	}
	if got := w.FsyncCount(); got != 2 {
		t.Fatalf("FsyncCount after second flush = %d, want 2", got)
	}

	// A no-op flush (nothing new written) must not fdatasync again.
	if err := w.FlushUpTo(end2); err != nil {
		t.Fatal(err)
	}
	if got := w.FsyncCount(); got != 2 {
		t.Fatalf("FsyncCount after redundant flush = %d, want unchanged 2", got)
	}
}

// TestWriterFsyncTimeNanosAccumulates is AddFsyncTimeNanos/FsyncTimeNanos's
// pg_stat_io.fsync_time analogue of storage.Pool's
// TestPoolReadTimeNanosAccumulates (M0122-0003 track_io_timing follow-up).
// Writer itself never calls AddFsyncTimeNanos — initdb.Open's
// OnWALSyncDone hook does, gated on the calling backend's track_io_timing —
// so this only exercises the accumulator in isolation.
func TestWriterFsyncTimeNanosAccumulates(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if got := w.FsyncTimeNanos(); got != 0 {
		t.Fatalf("FsyncTimeNanos before any accumulation = %d, want 0", got)
	}
	w.AddFsyncTimeNanos(1_000_000)
	w.AddFsyncTimeNanos(2_000_000)
	if got, want := w.FsyncTimeNanos(), int64(3_000_000); got != want {
		t.Errorf("FsyncTimeNanos after two adds = %d, want %d", got, want)
	}
	w.AddFsyncTimeNanos(0)
	w.AddFsyncTimeNanos(-5)
	if got, want := w.FsyncTimeNanos(), int64(3_000_000); got != want {
		t.Errorf("FsyncTimeNanos after non-positive adds = %d, want unchanged %d", got, want)
	}
}

func TestFlushUpToRejectsUnwrittenLSN(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := w.Append([]byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end + 1); !errors.Is(err, ErrLSNNotWritten) {
		t.Fatalf("FlushUpTo returned %v, want ErrLSNNotWritten", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReopenContinuesLSN(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w1, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, end1, err := w1.Append([]byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w1.FlushUpTo(end1); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	start2, _, err := w2.Append([]byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if start2 != end1+1 {
		t.Fatalf("reopened writer start LSN = %d, want %d", start2, end1+1)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordCanSpanSegments(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 80)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("record count = %d, want 1", len(recs))
	}
	if len(recs[0].Payload) != len(payload) {
		t.Fatalf("payload len = %d, want %d", len(recs[0].Payload), len(payload))
	}
	for i := range payload {
		if recs[0].Payload[i] != payload[i] {
			t.Fatalf("payload[%d] = %d, want %d", i, recs[0].Payload[i], payload[i])
		}
	}
}

// TestPreallocatedSegmentIsFullSize pins the M0007 / 0007-0001
// contract: with Preallocate=true, every newly created segment
// file is exactly SegmentSize bytes from the moment it appears,
// even after a single short append.
func TestPreallocatedSegmentIsFullSize(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	const segSize = int64(1024)
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: segSize, Preallocate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append([]byte("short")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(walDir, formatSegmentName(0)))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != segSize {
		t.Errorf("segment 0 size=%d want %d (preallocated)", st.Size(), segSize)
	}
}

// TestPreallocationCounters pins the M0007 "Counters / observability"
// follow-up: SegmentsPreallocated/PreallocatedBytes stay at 0 when
// Preallocate is off, and count each newly zero-filled segment
// (not re-opens of an already-preallocated one) when it's on.
func TestPreallocationCounters(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		walDir := filepath.Join(t.TempDir(), "pg_wal")
		w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1024})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if _, _, err := w.Append([]byte("short")); err != nil {
			t.Fatal(err)
		}
		if got := w.SegmentsPreallocated(); got != 0 {
			t.Errorf("SegmentsPreallocated=%d want 0 (Preallocate off)", got)
		}
		if got := w.PreallocatedBytes(); got != 0 {
			t.Errorf("PreallocatedBytes=%d want 0 (Preallocate off)", got)
		}
	})
	t.Run("enabled across rollover", func(t *testing.T) {
		walDir := filepath.Join(t.TempDir(), "pg_wal")
		const segSize = int64(1024)
		w, err := NewWriter(Config{WALDir: walDir, SegmentSize: segSize, Preallocate: true})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		// eagerWG.Wait pins down the eager next-segment lookahead
		// follow-up (M0007): openSegment now also kicks off a
		// background preallocation of segNo+1 as soon as segNo opens
		// for real use, so counts settle one segment ahead of what a
		// pre-eager-lookahead reader would expect. Waiting here makes
		// the assertions deterministic instead of racing the
		// background goroutine (it happened to always lose that race
		// in practice, but nothing guarantees that).
		if _, _, err := w.Append([]byte("short")); err != nil {
			t.Fatal(err)
		}
		w.stateRef.eagerWG.Wait()
		if got := w.SegmentsPreallocated(); got != 2 {
			t.Errorf("SegmentsPreallocated=%d want 2 after first segment (segment 0 itself + eager segment 1)", got)
		}
		if got := w.PreallocatedBytes(); got != 2*segSize {
			t.Errorf("PreallocatedBytes=%d want %d after first segment", got, 2*segSize)
		}
		// Re-opening the same (already preallocated) segment must not
		// double-count: openSegment's wasNew branch only fires on
		// first-time creation, and the cached-file fast path skips
		// the eager-lookahead trigger too (no re-trigger for segment 1).
		if _, _, err := w.Append([]byte("more")); err != nil {
			t.Fatal(err)
		}
		w.stateRef.eagerWG.Wait()
		if got := w.SegmentsPreallocated(); got != 2 {
			t.Errorf("SegmentsPreallocated=%d want 2 still (no re-preallocation)", got)
		}
		// Force a rollover into segment 1 (already eagerly preallocated
		// above, so this doesn't bump the counter itself) with a big
		// payload; opening it for real use eagerly kicks off segment 2.
		big := make([]byte, segSize)
		if _, _, err := w.Append(big); err != nil {
			t.Fatal(err)
		}
		w.stateRef.eagerWG.Wait()
		if got := w.SegmentsPreallocated(); got != 3 {
			t.Errorf("SegmentsPreallocated=%d want 3 after rollover (segment 1 was already eager; segment 2 now eagerly preallocated too)", got)
		}
		if got := w.PreallocatedBytes(); got != 3*segSize {
			t.Errorf("PreallocatedBytes=%d want %d after rollover", got, 3*segSize)
		}
	})
}

// TestPreallocatedSegmentRecoversCleanly: write three records
// into a preallocated segment, close, reopen with the same
// config. Recovery's WrittenLSN matches the third record's end
// and ReadAll returns exactly those three records — no spurious
// empty records emitted from the zero-fill tail.
func TestPreallocatedSegmentRecoversCleanly(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	const segSize = int64(1024)
	cfg := Config{WALDir: walDir, SegmentSize: segSize, Preallocate: true}

	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var lastEnd uint64
	for _, p := range []string{"alpha", "beta", "gamma"} {
		_, end, err := w.Append([]byte(p))
		if err != nil {
			t.Fatal(err)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := NewWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if got := w2.WrittenLSN(); got != lastEnd {
		t.Errorf("reopened WrittenLSN=%d want %d", got, lastEnd)
	}

	recs, err := ReadAll(walDir, segSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("ReadAll returned %d records want 3 (zero-fill tail leaked?)", len(recs))
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if string(recs[i].Payload) != want {
			t.Errorf("recs[%d]=%q want %q", i, recs[i].Payload, want)
		}
	}
}

// TestAppendRejectsEmptyPayload pins the new invariant: empty
// records collide with the EOS sentinel, so the writer rejects
// them outright. See
// docs/design/0007-0001-wal-segment-preallocation.md.
func TestAppendRejectsEmptyPayload(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, _, err := w.Append(nil); !errors.Is(err, ErrEmptyPayload) {
		t.Errorf("Append(nil) err=%v want ErrEmptyPayload", err)
	}
	if _, _, err := w.Append([]byte{}); !errors.Is(err, ErrEmptyPayload) {
		t.Errorf("Append([]byte{}) err=%v want ErrEmptyPayload", err)
	}
}
