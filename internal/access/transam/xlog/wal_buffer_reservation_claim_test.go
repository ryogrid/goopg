package xlog

import (
	"math/rand"
	"path/filepath"
	"testing"
)

// TestWALBufferReservationClaimCoversCrossSegmentFootprint sweeps record
// lengths and LSN start positions through segment-boundary crossings and
// asserts the tryReserve claim used by tryAppend/appendPGCompat always
// covers the reservation's actual LSN footprint — the contiguous
// [curr, new-curr) span consumed by the append, which for a crossing is
// pad gap + re-landed emitted record.
//
// The pre-fix claim 2*(recordLen+64) assumed the emitted size never
// exceeds recordLen+64 — true only for records spanning ≤2 pages (one
// page header's worth of overhead). For a record spanning ≥3 page
// boundaries predictEmittedSize returns total > recordLen+64, and a
// crossing could then reserve LSN space the claim never covered:
// curr ran ahead of head+cap, writeReserved returned
// errWALBufferReservedOutOfRange, the orphaned-but-published range let
// PublishUpTo push tail past head+cap, and the next drain's readForDrain
// panicked on resident > cap — wedging the server so every subsequent
// WAL append panicked (TestPort_IsolationSuite backend-panic storm,
// 2026-09-19).
func TestWALBufferReservationClaimCoversCrossSegmentFootprint(t *testing.T) {
	segSize := int64(256 * 1024) // small so the sweep covers every position
	recordLens := []int{1, 64, 4096, 8152, 8192, 16384, 20480, 33000, 65536, 100000}
	for _, recordLen := range recordLens {
		claim := walBufferReservationClaim(recordLen, segSize)
		for start := int64(0); start < segSize; start++ {
			totalC, _ := predictEmittedSize(recordLen, start, segSize)
			boundary := (start/segSize + 1) * segSize
			var footprint int64
			if start+int64(totalC) > boundary {
				// Crossing: the pad covers [start, boundary) and the
				// record is re-landed at boundary with a boundary-correct
				// header schedule — mirror reserveEmittedAndPublish.
				totalB, _ := predictEmittedSize(recordLen, boundary, segSize)
				footprint = (boundary - start) + int64(totalB)
			} else {
				footprint = int64(totalC)
			}
			if int64(claim) < footprint {
				t.Fatalf("recordLen=%d start=%d: footprint %d exceeds claim %d",
					recordLen, start, footprint, claim)
			}
		}
	}
}

// TestWALBufferReservationClaimMaxEmittedIsMaximal verifies the second
// half of the bound: predictEmittedSize evaluated at a segment-aligned
// start (the 40-byte long PHD position) is at least as large as the
// emitted size at ANY start position — the claim's `total` term. A
// counter-example would let a mid-page start emit more header bytes
// than the claim budgets.
func TestWALBufferReservationClaimMaxEmittedIsMaximal(t *testing.T) {
	segSize := int64(256 * 1024)
	recordLens := []int{1, 64, 8151, 8152, 8153, 8192, 16384, 33000, 65536}
	rng := rand.New(rand.NewSource(20260919))
	for _, recordLen := range recordLens {
		maxEmitted, _ := predictEmittedSize(recordLen, 0, segSize)
		// Exhaustive positions for smaller records, sampled for larger.
		var positions []int64
		if recordLen <= 8192 {
			for p := int64(0); p < segSize; p++ {
				positions = append(positions, p)
			}
		} else {
			for i := 0; i < 200000; i++ {
				positions = append(positions, rng.Int63n(segSize))
			}
			// Always probe boundary-adjacent positions where the header
			// schedule changes shape.
			for b := int64(0); b <= segSize; b += XLOGBlockSize {
				for _, d := range []int64{-1, 0, 1} {
					if p := b + d; p >= 0 && p < segSize {
						positions = append(positions, p)
					}
				}
			}
		}
		for _, pos := range positions {
			total, _ := predictEmittedSize(recordLen, pos, segSize)
			if total > maxEmitted {
				t.Fatalf("recordLen=%d pos=%d: emitted %d exceeds segment-aligned max %d",
					recordLen, pos, total, maxEmitted)
			}
		}
	}
}

// TestAppendCrossSegmentMultiPageRecordNearFullBuffer drives a multi-page
// record into a segment-boundary crossing while the WAL buffer holds a
// near-capacity resident set — the geometry that made writeReserved's
// range check fail (and the next drain's readForDrain panic on
// resident > cap) before the claim covered the pad+record footprint.
// The append must succeed and the written LSN must flush.
func TestAppendCrossSegmentMultiPageRecordNearFullBuffer(t *testing.T) {
	segSize := int64(256 * 1024)
	capBytes := int64(256 * 1024)
	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      filepath.Clean(dir),
		SegmentSize: segSize,
		WALBuffers:  capBytes,
		PageHeaders: true,
		SystemID:    0x5157_0013,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	st := w.stateRef

	// Multi-page record: ~100 KiB spans ≥13 page boundaries, so its
	// emitted size exceeds paddedLen+64 by several hundred bytes — the
	// exact case the old 2*(paddedLen+64) claim under-covered.
	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	_, paddedLen := predictXLogRecordLen(payload)
	oldClaim := 2 * (paddedLen + 64)

	// Fill the ring until the NEXT append of this record would cross the
	// segment boundary with as large a pad gap as possible — i.e. break
	// the first time the predicted emission reaches past the boundary.
	for i := 0; ; i++ {
		curr, _ := st.core.Load()
		totalC, _ := predictEmittedSize(paddedLen, int64(curr), segSize)
		boundary := (int64(curr)/segSize + 1) * segSize
		if int64(curr)+int64(totalC) > boundary {
			break
		}
		if i > 20000 {
			t.Fatalf("curr never entered the crossing window (curr=%d)", curr)
		}
		if _, _, err := w.Append(make([]byte, 256)); err != nil {
			t.Fatalf("filler Append: %v", err)
		}
	}

	// Leave the buffer nearly full: resident = cap - oldClaim, the
	// tightest occupancy at which the OLD claim still passed tryReserve.
	// With the old bound the crossing footprint (~2·total, larger than
	// oldClaim) then overflowed the ring window and writeReserved failed;
	// with the fix the claim is larger, tryReserve legitimately fails the
	// fast path, and the slow path drains + appends successfully.
	resident := st.walBuf.resident()
	target := capBytes - int64(oldClaim)
	if target < 0 {
		target = 0
	}
	if resident > target {
		if err := st.drainBufferBytes(resident-target, drainReasonOverflow); err != nil {
			t.Fatalf("drainBufferBytes: %v", err)
		}
	}

	start, end, err := w.Append(payload)
	if err != nil {
		t.Fatalf("Append across segment boundary with near-full buffer: %v", err)
	}
	if end <= start {
		t.Fatalf("Append returned non-forward LSNs: start=%d end=%d", start, end)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatalf("FlushUpTo(%d): %v", end, err)
	}
}
