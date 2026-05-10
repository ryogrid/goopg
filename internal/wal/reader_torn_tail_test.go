package wal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Tests for the M0088-0001 torn-tail tolerance heuristic in
// internal/wal/reader.go (isPreallocatedTail). They simulate a
// non-clean shutdown of the WAL writer mid-record: bytes up to the
// kill point are real, bytes after are the preallocated zero-fill
// tail. Recovery must treat the corrupt record + zero tail as
// end-of-WAL, NOT as fatal on-disk corruption.

const tornTailSegmentSize int64 = 32 * 1024 // 32 KiB — small for fast tests.

// writeSegment writes `payload` followed by enough zero bytes to fill
// the segment to exactly `segSize`. Mimics the WAL writer's
// preallocation behaviour (writer.go::preallocateSegment).
func writeSegment(t *testing.T, dir string, segNo uint64, payload []byte, segSize int64) {
	t.Helper()
	if int64(len(payload)) > segSize {
		t.Fatalf("payload %d > segSize %d", len(payload), segSize)
	}
	buf := make([]byte, segSize)
	copy(buf, payload)
	path := filepath.Join(dir, formatSegmentName(segNo))
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReadAllStopsAtTornTailWithinLastSegment is the
// pre-M0088 happy path: a corrupt record near the end of the
// final segment, with zero-fill tail to EOF. Already passed
// pre-fix via the segmentSize heuristic; pinned here so the
// new look-ahead-zero check doesn't regress it.
func TestReadAllStopsAtTornTailWithinLastSegment(t *testing.T) {
	dir := t.TempDir()

	// Two valid records, then a torn 3rd (CRC flipped), then zeros.
	r1 := encodeRecord([]byte("first"))
	r2 := encodeRecord([]byte("second"))
	r3 := encodeRecord([]byte("third"))
	// Flip the CRC byte of the 3rd record.
	r3[4] ^= 0xFF

	stream := append(append(append([]byte{}, r1...), r2...), r3...)
	writeSegment(t, dir, 0, stream, tornTailSegmentSize)

	got, err := ReadAll(dir, tornTailSegmentSize)
	if err != nil {
		t.Fatalf("ReadAll: %v (want nil)", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if string(got[0].Payload) != "first" || string(got[1].Payload) != "second" {
		t.Fatalf("got payloads %q + %q, want first + second", got[0].Payload, got[1].Payload)
	}
}

// TestReadAllStopsAtTornTailEarlySegment is the
// M0088-0001 target case: a corrupt record more than 1
// segment-size from EOF, followed by zero-tail across
// multiple preallocated segments. Pre-fix this errors
// fatally; post-fix it succeeds.
func TestReadAllStopsAtTornTailEarlySegment(t *testing.T) {
	dir := t.TempDir()

	r1 := encodeRecord([]byte("first"))
	r2 := encodeRecord([]byte("second"))
	r2[4] ^= 0xFF // flip CRC of record 2 — torn write here

	// Segment 0: r1 + torn r2 + zero-tail.
	stream0 := append(append([]byte{}, r1...), r2...)
	writeSegment(t, dir, 0, stream0, tornTailSegmentSize)

	// Segments 1, 2, 3: fully zero (preallocated but never
	// written-into by the writer because we were killed mid-record
	// in segment 0).
	writeSegment(t, dir, 1, nil, tornTailSegmentSize)
	writeSegment(t, dir, 2, nil, tornTailSegmentSize)
	writeSegment(t, dir, 3, nil, tornTailSegmentSize)

	got, err := ReadAll(dir, tornTailSegmentSize)
	if err != nil {
		t.Fatalf("ReadAll: %v (want nil — torn-tail should be treated as EOS)", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1 (only the valid r1)", len(got))
	}
	if string(got[0].Payload) != "first" {
		t.Fatalf("got payload %q, want %q", got[0].Payload, "first")
	}
}

// TestReadAllPropagatesRealMidStreamCorruption is the
// safety check: a corrupt record followed by non-zero bytes
// (simulating real bit-flip in the middle of an otherwise-
// valid WAL) must STILL fail. This pins the conservative
// behaviour — we only tolerate corrupt records whose tail
// is solid zeros (the preallocated signal).
func TestReadAllPropagatesRealMidStreamCorruption(t *testing.T) {
	dir := t.TempDir()

	r1 := encodeRecord([]byte("first"))
	r2 := encodeRecord([]byte("second"))
	r3 := encodeRecord([]byte("third"))
	r2[4] ^= 0xFF // flip CRC of record 2

	// r1 + corrupt r2 + valid r3 + zero-tail.
	stream := append(append(append([]byte{}, r1...), r2...), r3...)
	writeSegment(t, dir, 0, stream, tornTailSegmentSize)

	_, err := ReadAll(dir, tornTailSegmentSize)
	if err == nil {
		t.Fatalf("ReadAll succeeded; want an error because non-zero bytes follow the corrupt record")
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("ReadAll err = %v; want ErrCorruptRecord", err)
	}
}

// TestIsPreallocatedTail unit-tests the helper directly so the
// chunking + zero-comparison logic is pinned independent of the
// caller paths.
func TestIsPreallocatedTail(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"empty", []byte{}, true},
		{"all zero short", make([]byte, 100), true},
		{"all zero one chunk", make([]byte, 65536), true},
		{"all zero multi chunk", make([]byte, 200_000), true},
		{"trailing nonzero", append(make([]byte, 99), 0x01), false},
		{"leading nonzero", append([]byte{0x42}, make([]byte, 99)...), false},
		{"middle nonzero", func() []byte {
			b := make([]byte, 200)
			b[100] = 0xFF
			return b
		}(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPreallocatedTail(c.b); got != c.want {
				t.Fatalf("isPreallocatedTail = %v, want %v", got, c.want)
			}
		})
	}
}
