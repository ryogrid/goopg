package xlog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// M0131-S30.6: goopg's WAL byte layout must not depend on WHICH append path
// wrote it.
//
// state.appendPGCompat has two paths. Path B (stripe, Config.WALBuffers > 0 —
// the production path) reserves through insertPosTracker.reserveEmittedAndPublish,
// which re-lands a record that would straddle a segment boundary AT the boundary
// and fills the gap with an XLOG_NOOP pad. Path A (Config.WALBuffers == 0,
// oversized record, ring drain) used to encode at the tracker cursor and emit
// straight through emitWithPageHeaders with no crossing check, so it wrote
// records that straddle the boundary. Every consumer therefore had to tolerate
// two layouts — which is exactly why the S30.1 reader fix had to make its
// segment-tail skip CRC-conditional rather than positional.
//
// Upstream has one rule for all inserters: ReserveXLogInsertLocation reserves in
// usable-byte space (postgres/src/backend/access/transam/xlog.c), and
// XLogInsertRecord's boundary handling is the same for a backend-buffered insert
// and an oversized one.
//
// This gate appends the SAME payload sequence — one that crosses a segment
// boundary — through both paths and requires the resulting segment files to be
// byte-identical.

const crossParitySegSize = int64(4 * XLOGBlockSize)

// payloadOfEmittedSize returns a payload whose emitted size at `pos` is exactly
// `want` bytes, mirroring the sizing trick in reader_segment_tail_gap_test.go
// (the writer's own prediction functions decide, so the cursor is known exactly
// before each Append).
func payloadOfEmittedSize(pos uint64, want int, segSize int64) (string, bool) {
	for n := 1; n <= want; n++ {
		p := fmt.Sprintf("%0*d", n, n%10)
		_, padded := predictXLogRecordLen([]byte(p))
		total, _ := predictEmittedSize(padded, int64(pos), segSize)
		if total == want {
			return p, true
		}
	}
	return "", false
}

// crossParityPayloads drives the cursor to `segSize - lead` and then appends a
// record that cannot fit in the remaining `lead` bytes, plus a few more behind
// it. It returns the payload list so both paths replay the identical sequence.
// The crossing record is sized from `lead` so it genuinely cannot fit in the
// remaining bytes: a payload of lead+512 bytes always overruns the boundary.
func crossParityPayloads(t *testing.T, lead int64) []string {
	t.Helper()
	var out []string
	target := uint64(crossParitySegSize) - uint64(lead)
	cur := uint64(0)
	for cur < target {
		step := int(target - cur)
		if step > 512 {
			step = 512
		}
		p, ok := payloadOfEmittedSize(cur, step, crossParitySegSize)
		if !ok {
			t.Fatalf("no payload produces an emitted size of %d bytes at pos=%d", step, cur)
		}
		total, _ := predictEmittedSize(func() int { _, pad := predictXLogRecordLen([]byte(p)); return pad }(), int64(cur), crossParitySegSize)
		cur += uint64(total)
		out = append(out, p)
	}
	cross := fmt.Sprintf("%0*d", lead+512, 7)
	out = append(out, cross)
	for i := 0; i < 3; i++ {
		out = append(out, fmt.Sprintf("after-boundary-%d", i))
	}
	return out
}

// writeCrossParityWAL appends `payloads` through the path selected by
// walBuffers (0 = Path A direct write, > 0 = Path B stripe) and returns the WAL
// directory.
func writeCrossParityWAL(t *testing.T, payloads []string, walBuffers int64) string {
	t.Helper()
	walDir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: crossParitySegSize,
		Preallocate: true,
		WALBuffers:  walBuffers,
	})
	if err != nil {
		t.Fatal(err)
	}
	var lastEnd uint64
	for _, p := range payloads {
		_, end, aerr := w.Append([]byte(p))
		if aerr != nil {
			t.Fatalf("append %q: %v", p, aerr)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	w.stateRef.eagerWG.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return walDir
}

func readSegmentBytes(t *testing.T, walDir string, idx uint64) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(walDir, formatSegmentName(idx)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAppendPathsAgreeOnSegmentBoundaryLayout is the S30.6 gate.
func TestAppendPathsAgreeOnSegmentBoundaryLayout(t *testing.T) {
	// lead 8/16 are sub-header gaps (no pad fits — both paths must leave them
	// unwritten and land the record at the boundary); 64 and 12288 are real
	// pads, the latter spanning a page boundary (the S30.1b shape: the gap
	// [20480, 32768) contains the page boundary at 24576).
	for _, lead := range []int64{8, 16, 64, 12288} {
		t.Run(fmt.Sprintf("lead%d", lead), func(t *testing.T) {
			payloads := crossParityPayloads(t, lead)
			dirA := writeCrossParityWAL(t, payloads, 0)
			dirB := writeCrossParityWAL(t, payloads, 1<<20)

			for idx := uint64(0); idx < 2; idx++ {
				a := readSegmentBytes(t, dirA, idx)
				b := readSegmentBytes(t, dirB, idx)
				if len(a) != len(b) {
					t.Fatalf("segment %d: Path A is %d bytes, Path B is %d", idx, len(a), len(b))
				}
				if !bytes.Equal(a, b) {
					off := 0
					for off < len(a) && a[off] == b[off] {
						off++
					}
					t.Fatalf("segment %d: Path A and Path B disagree at byte %d "+
						"(A=%#x B=%#x) — the two append paths produce different "+
						"segment-boundary layouts (M0131-S30.6)",
						idx, off, a[off], b[off])
				}
			}

			// Both layouts must also replay completely.
			for _, dir := range []string{dirA, dirB} {
				recs, err := readAllUncached(dir, crossParitySegSize)
				if err != nil {
					t.Fatalf("readAllUncached(%s): %v", dir, err)
				}
				got := make(map[string]bool, len(recs))
				for _, r := range recs {
					got[string(r.Payload)] = true
				}
				for i, p := range payloads {
					if !got[p] {
						t.Fatalf("%s: payload %d/%d %q missing from replay (%d records read)",
							dir, i+1, len(payloads), p, len(recs))
					}
				}
			}
		})
	}
}
