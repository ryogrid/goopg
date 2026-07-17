package wal

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// segmentTotalBytes returns the total size of every WAL segment
// file under walDir. With Preallocate=false (the default in these
// tests) segment files only grow when state.writeAt actually
// pwrites to them — so this byte total is the operational signal
// for "did the buffered append actually hit disk?"
func segmentTotalBytes(t *testing.T, walDir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := parseSegmentName(e.Name()); !ok {
			continue
		}
		st, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += st.Size()
	}
	return total
}

// TestWALBufferDisabledByDefault pins backwards-compat: when
// WALBuffers=0 (the legacy zero value used by every existing
// test), Append calls writeAt directly so the segment file grows
// per-record. This guards against a future change that flips the
// default to nonzero and breaks fixtures that don't pass the
// option.
func TestWALBufferDisabledByDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, _, err := w.Append([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got := segmentTotalBytes(t, dir); got == 0 {
		t.Errorf("segment bytes=%d, want >0 (buffer disabled — append must hit disk)", got)
	}
}

// TestWALBufferRetainsRecordsInRAM is the headline contract: with
// wal_buffers > record_size, an Append produces 0 bytes on disk.
// Without a flush trigger or overflow drain the segment file size
// stays at its starting value (preallocated or empty).
func TestWALBufferRetainsRecordsInRAM(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const cap = 1 << 16 // 64 KiB plenty for a few small records
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 4096,
		WALBuffers:  cap,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	startBytes := segmentTotalBytes(t, dir)
	for i := 0; i < 5; i++ {
		if _, _, err := w.Append([]byte("hello world")); err != nil {
			t.Fatal(err)
		}
	}
	if got := segmentTotalBytes(t, dir); got != startBytes {
		t.Errorf("segment bytes grew %d->%d while buffer had room — append should stay in RAM",
			startBytes, got)
	}
}

// TestWALBufferOverflowDrainsToSegments pins the overflow path:
// once Appends exceed the buffer cap, a drain fires before the
// new record lands. Drained bytes appear in the segment file.
func TestWALBufferOverflowDrainsToSegments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Tiny buffer (256 bytes) so even a few small records overflow.
	const cap = 256
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 1 << 20,
		WALBuffers:  cap,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Each encodeRecord adds a fixed header + the payload.
	// 50-byte payloads fit comfortably; 8 of them push past 256.
	startBytes := segmentTotalBytes(t, dir)
	payload := bytes.Repeat([]byte{0xAA}, 50)
	for i := 0; i < 8; i++ {
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	got := segmentTotalBytes(t, dir)
	if got <= startBytes {
		t.Errorf("segment bytes did not grow despite overflow: start=%d got=%d", startBytes, got)
	}
}

// TestFlushUpToDrainsBufferThenSyncs pins the two-stage semantics:
// FlushUpTo drains the buffer through targetLSN AND fdatasyncs
// the touched segments. After FlushUpTo, ReadAll reflects every
// appended byte even with the buffer enabled.
func TestFlushUpToDrainsBufferThenSyncs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 4096,
		WALBuffers:  1 << 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var lastEnd uint64
	payloads := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("gamma"),
	}
	for _, p := range payloads {
		_, end, err := w.Append(p)
		if err != nil {
			t.Fatal(err)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}

	records, err := ReadAll(dir, 4096)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != len(payloads) {
		t.Fatalf("ReadAll returned %d records, want %d", len(records), len(payloads))
	}
	for i, r := range records {
		if !bytes.Equal(r.Payload, payloads[i]) {
			t.Errorf("record %d payload=%q, want %q", i, r.Payload, payloads[i])
		}
	}
}

// TestWALBufferRecordLargerThanBufferBypasses pins the
// "fragment-free" rule: a record larger than wal_buffers writes
// straight to disk in one shot rather than failing or splitting
// across drains. Without this, an oversized log would deadlock
// the buffered path.
func TestWALBufferRecordLargerThanBufferBypasses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 1 << 20,
		WALBuffers:  64, // smaller than the record we'll append
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	startBytes := segmentTotalBytes(t, dir)
	big := bytes.Repeat([]byte{0xBB}, 256)
	if _, _, err := w.Append(big); err != nil {
		t.Fatal(err)
	}
	if got := segmentTotalBytes(t, dir); got <= startBytes {
		t.Errorf("oversized record didn't bypass to disk: start=%d got=%d", startBytes, got)
	}
}

// TestWALBufferCountersTrackDrains pins the M0013-0003
// observability surface. Two distinct triggers (overflow drain
// from Append, Stage 1 drain from FlushUpTo) bump two distinct
// counters so an operator can tell a sizing problem (high
// overflow) from natural commit-driven cost (high flush). Also
// pins WALBuffersCapacity / WALBuffersBytesResident accessors —
// the SQL view's columns rely on them.
func TestWALBufferCountersTrackDrains(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A9: the PG frame's conservative per-append ring reservation is
	// 2*(paddedRecordLen+64) — a cap that small forces every append onto the
	// direct-write bypass and the buffer never accumulates. 1 KiB is large
	// enough that 50-byte payloads buffer, small enough that a burst of them
	// still forces overflow drains.
	const cap = 1024
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 1 << 20,
		WALBuffers:  cap,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Capacity matches GUC, no drains yet.
	if got := w.WALBuffersCapacity(); got != cap {
		t.Errorf("WALBuffersCapacity = %d, want %d", got, cap)
	}
	if got := w.WALBuffersOverflowDrainBytes(); got != 0 {
		t.Errorf("OverflowDrainBytes initially %d, want 0", got)
	}
	if got := w.WALBuffersFlushDrainBytes(); got != 0 {
		t.Errorf("FlushDrainBytes initially %d, want 0", got)
	}

	// Force overflow drains: 16 × 50-byte payload (≈80 emitted bytes each)
	// exceeds the 1 KiB cap.
	payload := bytes.Repeat([]byte{0xCC}, 50)
	for i := 0; i < 16; i++ {
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.WALBuffersOverflowDrainBytes(); got == 0 {
		t.Errorf("OverflowDrainBytes still 0 after overflow appends")
	}
	if got := w.WALBuffersFlushDrainBytes(); got != 0 {
		t.Errorf("FlushDrainBytes = %d before any FlushUpTo, want 0", got)
	}

	// Force a Stage 1 flush drain: append something that fits,
	// then FlushUpTo to push it out of the buffer.
	_, end, err := w.Append([]byte("commit"))
	if err != nil {
		t.Fatal(err)
	}
	beforeFlush := w.WALBuffersFlushDrainBytes()
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if got := w.WALBuffersFlushDrainBytes(); got <= beforeFlush {
		t.Errorf("FlushDrainBytes did not advance: before=%d after=%d", beforeFlush, got)
	}
}

// TestWALBufferReadAllRoundTrip is the end-to-end correctness
// check: a sequence of buffered appends round-trips through the
// drain path on FlushUpTo, byte-identical to what the
// buffer-disabled control would produce. No silent corruption,
// no out-of-order drains, no missing records.
func TestWALBufferReadAllRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		buf  int64
	}{
		{"buffer_disabled", 0},
		{"buffer_64KiB", 1 << 16},
		{"buffer_256B_forces_drains", 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "pg_wal")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			w, err := NewWriter(Config{
				WALDir:      dir,
				SegmentSize: 4096,
				WALBuffers:  tc.buf,
			})
			if err != nil {
				t.Fatal(err)
			}

			payloads := make([][]byte, 20)
			var lastEnd uint64
			for i := range payloads {
				payloads[i] = []byte{byte(i + 1), byte('a' + (i % 26))}
				_, end, err := w.Append(payloads[i])
				if err != nil {
					t.Fatal(err)
				}
				lastEnd = end
			}
			if err := w.FlushUpTo(lastEnd); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()

			records, err := ReadAll(dir, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != len(payloads) {
				t.Fatalf("got %d records, want %d", len(records), len(payloads))
			}
			for i, r := range records {
				// A9: a payload whose leading byte collides with a
				// natively-sized RecordKind (e.g. 11 = SmgrCreate) fails the
				// native-size guard and routes to the decoded replay path —
				// its body comes back in XLog.MainData with Payload nil.
				// Either way the bytes must round-trip unchanged.
				body := r.Payload
				if body == nil && r.XLog != nil {
					body = r.XLog.MainData
				}
				if !bytes.Equal(body, payloads[i]) {
					t.Errorf("payload[%d]=%v want %v", i, body, payloads[i])
				}
			}
		})
	}
}
