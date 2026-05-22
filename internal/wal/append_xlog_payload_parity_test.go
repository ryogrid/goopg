package wal

import (
	"bytes"
	"fmt"
	"testing"
)

// Foundation 23 for M0107-0007 slice B — parity gate locking byte-
// identical equivalence between two emission paths for a sequence of
// PG-compat WAL records:
//
//   (1) "legacy" — encodeRecordXLog + emitWithPageHeaders + walBuf.append
//       with the caller manually threading the prev RecPtr (0-based
//       record-CONTENT start of the prior record, == reservation start +
//       leading PHD bytes) into each call. This is exactly the sequence
//       state.append's PG-compat Path B runs under appendMu today
//       (writer.go state.append lines 1132 / 1222-1238 / 1244-1249).
//
//   (2) "core" — stripeWriterCore.AppendXLogPayload via foundation 22
//       ([[0107-0007ae]]), which packages
//       predictXLogRecordLen → AppendBuiltEmitted → encodeRecordXLog
//       (with the post-reservation prev) → emitWithPageHeaders into a
//       single call.
//
// Foundations 1–22 landed the individual primitives and tested them in
// isolation; foundation 22 pinned (single-record) byte equivalence of
// the composer output against direct encodeRecordXLog +
// emitWithPageHeaders for a fixed prev. This foundation extends the
// equivalence to a multi-record chain so the xl_prev linkage is also
// pinned, and pins that the SAME byte sequence lands at the SAME LSN
// positions in the walBuf ring under both paths.
//
// Why this matters for the call-site rewrite. The slice B call-site
// rewrite (which is multi-loop scope and NOT in this loop) replaces
// state.append's PG-compat path body with s.core.AppendXLogPayload.
// For that swap to be semantics-preserving the byte stream produced
// by the core path MUST be identical to the byte stream produced by
// the legacy path for every payload sequence the rewrite will face.
// This parity gate locks that invariant in advance — any future code
// change that perturbs encoding, page-header emission, or the prev
// linkage in either path will fail this test before reaching the
// call-site rewrite.
//
// Scope. Records that do not cross a SEGMENT boundary. Cross-segment
// behaviour differs by design between paths (legacy continues the
// record via XLP_FIRST_IS_CONTRECORD across segments, core's
// insertPosTracker emits an XLOG_NOOP pad in the gap and starts the
// record fresh on the next segment — both are PG-compatible since PG
// honours both contrecord-across-segment and XLOG_NOOP-at-boundary).
// Cross-PAGE (within a segment) IS in scope and is exercised by the
// large-payload cases below.
//
// ============================================================
// PARITY GATE (resolved in foundation 23 follow-up)
// ============================================================
//
// The prev-RecPtr convention divergence discovered during foundation 23
// has been resolved by changing reserveEmittedAndPublish to store
// `t.prev = start + uint64(leading)` (record-CONTENT start) instead of
// `t.prev = start` (reservation start). Both paths now agree on the
// xl_prev convention:
//
//   - legacy state.append: s.prevRecPtr = writePos + leading (content start)
//   - core reserveEmittedAndPublish: t.prev = start + leading (content start)
//
// The three multi-record parity tests are now active (t.Skip removed).
// The first-record test (prev=0 on both sides) remains the always-on
// single-record regression guard.
//
// PG-compat — none (test only). Design:
// docs/design/0107-0007af-wal-append-parity-gate.md.



// emitLegacyPGCompatRecord replays the exact sequence state.append's
// PG-compat Path B runs after acquiring appendMu (writer.go lines
// 1219-1238 modulo the appendMu / writePos bookkeeping):
//
//	stream, leading := emitWithPageHeaders(encodeRecordXLog(payload, prev),
//	                                       realRecLen, writePos, segSize,
//	                                       sysID, tli)
//	walBuf.append(stream)
//	start := writePos + leading + 1   // 1-based goopg LSN
//	prevRecPtr = start - 1            // 0-based PG RecPtr of THIS record
//
// Returns the resulting 0-based start (== record-content LSN ==
// writePos + leading) so the caller can feed it into the next record's
// prev parameter.
func emitLegacyPGCompatRecord(walBuf *walBuffer, payload []byte, prev uint64, writePos, segSize int64, sysID uint64, tli uint32) (start0Based uint64, advance int64, err error) {
	record, realRecLen, err := encodeRecordXLog(payload, prev)
	if err != nil {
		return 0, 0, err
	}
	stream, leading := emitWithPageHeaders(record, realRecLen, writePos, segSize, sysID, tli)
	walBuf.append(stream)
	return uint64(writePos) + uint64(leading), int64(len(stream)), nil
}

func TestAppendXLogPayloadParityWithLegacyEncodeEmit(t *testing.T) {
	t.Parallel()
	// 1 MiB segment so no payload below crosses a segment boundary;
	// the 4-page boundary-crossing payload exercises the in-segment
	// contrecord case under both paths.
	const segSize = int64(1 << 20)
	const sysID = uint64(0xDEAD_BEEF_CAFE_BABE)
	const tli = uint32(5)

	payloads := [][]byte{
		[]byte("alpha"),
		[]byte("beta-record-body"),
		// Page-boundary crossing within a segment. 7900 bytes plus
		// the 24-byte record header lands well inside the second
		// page, exercising the contrecord page-header insertion the
		// core path threads through emitWithPageHeaders verbatim.
		makePagePayload(7900),
		[]byte{0x00, 0x01, 0x02, 0x03},
		makePagePayload(50),
		[]byte("gamma-after-cross-page"),
		// A second page crossing later in the chain to pin that the
		// composer's start/prev arithmetic survives multiple
		// boundary events.
		makePagePayload(9000),
		[]byte("epsilon"),
	}

	// Reference path: legacy encodeRecordXLog + emitWithPageHeaders +
	// walBuf.append, manually advancing writePos and prev.
	refBuf := newWALBuffer(1 << 20)
	refBuf.reset(0)
	var refPrev uint64
	var refPos int64
	refStarts := make([]uint64, 0, len(payloads))
	for i, p := range payloads {
		start, adv, err := emitLegacyPGCompatRecord(refBuf, p, refPrev, refPos, segSize, sysID, tli)
		if err != nil {
			t.Fatalf("legacy emit #%d (payload len %d): %v", i, len(p), err)
		}
		refStarts = append(refStarts, start)
		refPos += adv
		refPrev = start // next record's xl_prev = this record's 0-based content start
	}

	// Under-test path: core.AppendXLogPayload through foundation 22.
	c := makeAppendXLogPayloadFixture(t, uint64(segSize))
	coreStarts := make([]uint64, 0, len(payloads))
	corePrevs := make([]uint64, 0, len(payloads))
	for i, p := range payloads {
		start, prev, _, leading, err := c.AppendXLogPayload(int32(i%appendLockStripes), p, segSize, sysID, tli)
		if err != nil {
			t.Fatalf("core AppendXLogPayload #%d (payload len %d): %v", i, len(p), err)
		}
		// Store content start (start + leading) to match legacy emitLegacyPGCompatRecord's
		// return value (writePos + leading). Both paths yield the XLogRecord header address.
		coreStarts = append(coreStarts, start+uint64(leading))
		corePrevs = append(corePrevs, prev)
	}
	// Publish so walBuf.readAt is permitted to surface every reserved
	// byte (foundation 7 tailPublisher contract).
	c.PublishUpTo(refPos)

	// LSN-start parity: the 0-based content start of every record
	// must match across paths.
	if len(refStarts) != len(coreStarts) {
		t.Fatalf("start-count mismatch: legacy=%d core=%d", len(refStarts), len(coreStarts))
	}
	for i := range refStarts {
		if refStarts[i] != coreStarts[i] {
			t.Errorf("start[%d]: legacy=%d core=%d", i, refStarts[i], coreStarts[i])
		}
	}

	// prev chain parity: the core path returns the joint-atomic prev
	// that was stamped into the record's xl_prev. It must equal the
	// 0-based content start of the immediately-preceding record,
	// mirroring what state.append's manual s.prevRecPtr threading
	// produces.
	var wantPrev uint64
	for i, got := range corePrevs {
		if got != wantPrev {
			t.Errorf("prev[%d]: want=%d got=%d (xl_prev chain divergence)",
				i, wantPrev, got)
		}
		wantPrev = coreStarts[i]
	}

	// Byte-identical walBuf contents — the on-the-wire substitutability
	// invariant the call-site rewrite depends on. We compare exactly
	// `refPos` bytes (the total emitted across all records); both
	// rings are sized identically and reset to base=0, so absolute
	// position lookup via readAt is well-defined.
	refBytes := make([]byte, refPos)
	if n := refBuf.readAt(0, refBytes); n != int(refPos) {
		t.Fatalf("legacy refBuf.readAt n=%d, want %d (was every byte appended?)", n, refPos)
	}
	coreBytes := make([]byte, refPos)
	if n := c.walBuf.readAt(0, coreBytes); n != int(refPos) {
		t.Fatalf("core walBuf.readAt n=%d, want %d (was PublishUpTo high enough?)", n, refPos)
	}
	if !bytes.Equal(refBytes, coreBytes) {
		// Find the first divergence for a useful failure message.
		first := -1
		for i := range refBytes {
			if refBytes[i] != coreBytes[i] {
				first = i
				break
			}
		}
		t.Fatalf("walBuf byte stream divergence at offset %d: legacy=0x%02x core=0x%02x (total bytes: %d)",
			first, refBytes[first], coreBytes[first], refPos)
	}
}

// TestAppendXLogPayloadParityShortRecordsSingleStripe pins parity
// under the single-stripe case (every record on stripe 0) — i.e.,
// the byte stream when stripe selection collapses to one lock. This
// is the closest analogue to today's appendMu-serialised legacy path
// and isolates parity failures caused by anything other than stripe
// distribution.
func TestAppendXLogPayloadParityShortRecordsSingleStripe(t *testing.T) {
	t.Parallel()
	const segSize = int64(1 << 20)
	const sysID = uint64(0x12345)
	const tli = uint32(2)

	payloads := make([][]byte, 0, 64)
	for i := 0; i < 64; i++ {
		payloads = append(payloads, []byte(fmt.Sprintf("rec-%03d-body", i)))
	}

	refBuf := newWALBuffer(1 << 20)
	refBuf.reset(0)
	var refPrev uint64
	var refPos int64
	for i, p := range payloads {
		start, adv, err := emitLegacyPGCompatRecord(refBuf, p, refPrev, refPos, segSize, sysID, tli)
		if err != nil {
			t.Fatalf("legacy emit #%d: %v", i, err)
		}
		refPos += adv
		refPrev = start
	}

	c := makeAppendXLogPayloadFixture(t, uint64(segSize))
	for i, p := range payloads {
		if _, _, _, _, err := c.AppendXLogPayload(0, p, segSize, sysID, tli); err != nil {
			t.Fatalf("core AppendXLogPayload #%d: %v", i, err)
		}
	}
	c.PublishUpTo(refPos)

	refBytes := make([]byte, refPos)
	refBuf.readAt(0, refBytes)
	coreBytes := make([]byte, refPos)
	c.walBuf.readAt(0, coreBytes)

	if !bytes.Equal(refBytes, coreBytes) {
		t.Fatalf("single-stripe walBuf byte stream divergence (total %d bytes)", refPos)
	}
}

// TestAppendXLogPayloadParityEmptyBodyRecords pins parity for the
// edge case of body-less records ([]byte{}) — paddedLen = 32, the
// smallest legitimate PG-compat record. Both paths must emit the
// same 32-byte body-less record sequence.
func TestAppendXLogPayloadParityEmptyBodyRecords(t *testing.T) {
	t.Parallel()
	const segSize = int64(1 << 20)
	const sysID = uint64(7)
	const tli = uint32(1)

	payloads := [][]byte{
		{},
		{},
		[]byte("witness-after-two-empties"),
		{},
		{},
	}

	refBuf := newWALBuffer(1 << 20)
	refBuf.reset(0)
	var refPrev uint64
	var refPos int64
	for i, p := range payloads {
		start, adv, err := emitLegacyPGCompatRecord(refBuf, p, refPrev, refPos, segSize, sysID, tli)
		if err != nil {
			t.Fatalf("legacy emit #%d: %v", i, err)
		}
		refPos += adv
		refPrev = start
	}

	c := makeAppendXLogPayloadFixture(t, uint64(segSize))
	for i, p := range payloads {
		if _, _, _, _, err := c.AppendXLogPayload(int32(i), p, segSize, sysID, tli); err != nil {
			t.Fatalf("core AppendXLogPayload #%d: %v", i, err)
		}
	}
	c.PublishUpTo(refPos)

	refBytes := make([]byte, refPos)
	refBuf.readAt(0, refBytes)
	coreBytes := make([]byte, refPos)
	c.walBuf.readAt(0, coreBytes)
	if !bytes.Equal(refBytes, coreBytes) {
		t.Fatalf("empty-body parity walBuf byte stream divergence (total %d bytes)", refPos)
	}
}

// TestAppendXLogPayloadParityFirstRecordAlwaysAgrees pins the trivial
// single-record case (no prior record, prev = 0 on both sides) as an
// always-on regression — this branch of parity IS satisfied by the
// current implementation because the prev=0 path is the only one
// where legacy and core agree on the prev semantic (both store /
// stamp 0). Catches future regressions that would break even the
// single-record case while the multi-record gate is deferred.
func TestAppendXLogPayloadParityFirstRecordAlwaysAgrees(t *testing.T) {
	t.Parallel()
	const segSize = int64(1 << 20)
	const sysID = uint64(0xABCD)
	const tli = uint32(9)
	payload := []byte("solo-first-record")

	// Legacy.
	refBuf := newWALBuffer(1 << 20)
	refBuf.reset(0)
	_, adv, err := emitLegacyPGCompatRecord(refBuf, payload, /*prev*/ 0, /*writePos*/ 0, segSize, sysID, tli)
	if err != nil {
		t.Fatalf("legacy emit: %v", err)
	}

	// Core.
	c := makeAppendXLogPayloadFixture(t, uint64(segSize))
	_, _, total, _, err := c.AppendXLogPayload(0, payload, segSize, sysID, tli)
	if err != nil {
		t.Fatalf("core AppendXLogPayload: %v", err)
	}
	c.PublishUpTo(int64(total))

	if int64(total) != adv {
		t.Fatalf("emitted size mismatch: legacy=%d core=%d", adv, total)
	}

	refBytes := make([]byte, adv)
	refBuf.readAt(0, refBytes)
	coreBytes := make([]byte, total)
	c.walBuf.readAt(0, coreBytes)
	if !bytes.Equal(refBytes, coreBytes) {
		t.Fatalf("single-record walBuf byte stream divergence (%d bytes)", adv)
	}
}
