package wal

import (
	"encoding/binary"
	"fmt"
)

// xlogInfoNoop is the xl_info byte for RM_XLOG_ID's NOOP record
// (PG: postgres/src/include/catalog/pg_control.h:70 `#define XLOG_NOOP 0x20`).
// PG's xlog_redo treats XLOG_NOOP as a true no-op
// (postgres/src/backend/access/transam/xlog.c:8508 `else if (info == XLOG_NOOP)
// { /* nothing to do here */ }`), so a NOOP record emitted by goopg is
// recovery-safe for both goopg's own replay and a PG18 standby replaying
// goopg-produced WAL. 0x20 sits in the rmgr-specific info bits (high
// 4 bits) so it passes EncodeXLogRecordHeader's framework-bits validation
// (framework bits = info & 0x0F = 0).
const xlogInfoNoop uint8 = 0x20

// buildSegmentPadRecord returns the on-disk bytes for a single
// `RmgrXLog`/`XLOG_NOOP` record of exactly `padLen` bytes. The record's
// xl_prev points at `prev` so the segment tail's prev-chain stays
// continuous; the record body (everything after the 24-byte header) is
// wrapped in a zero-filled main-data chunk so the WAL parser
// (parseXLogRecordData in pg_xlog_decode.go) accepts it.
//
// Foundation for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert
// locks per docs/design/perf-optimize/07-wal-fsm-insert.md §2). The
// lsnAllocator ([[0107-0007h]]) onCrossSegment hook calls this builder
// to fill the `[start, boundary)` gap left when a cross-segment
// reservation hops to the start of the new segment. Dead code until
// the slice B call-site rewrite wires it in; foundation-first pattern
// matches slice C ([[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]]
// landed before [[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]]
// consumed them).
//
// Constraints:
//   - padLen must be >= SizeOfXLogRecord (24). Pad lengths below that
//     cannot carry an XLogRecord header at all; the caller is expected
//     to engineer reservations so the leftover gap is at least one
//     record header. Returns ErrPadLenTooSmall.
//   - padLen of exactly 25 is not encodable: an XLogRecord with a
//     1-byte body cannot carry the 2-byte short main-data chunk
//     header (let alone a long chunk header). Returns ErrPadLen1ByteBody.
//     In practice the LSN allocator's reservations are 8-byte aligned
//     (maxAlignXLog) so padLen is always 24 or a multiple of 8 — every
//     such value is encodable.
//
// The byte layout for padLen p:
//   - p == 24: header only (TotLen=24, no body).
//   - 26 <= p <= 281: header + xlrBlockIDDataShort tag + 1-byte length
//     + (p-26) zero data bytes.
//   - p >= 282: header + xlrBlockIDDataLong tag + 4-byte length +
//     (p-29) zero data bytes.
func buildSegmentPadRecord(padLen int, prev uint64) ([]byte, error) {
	if padLen < SizeOfXLogRecord {
		return nil, fmt.Errorf("wal: pad record padLen=%d below minimum %d", padLen, SizeOfXLogRecord)
	}
	bodyLen := padLen - SizeOfXLogRecord
	body := make([]byte, bodyLen)
	switch {
	case bodyLen == 0:
		// No chunk header; the parser's headerLoop exits immediately
		// when len(wrapped)-off (=0) <= datatotal (=0).
	case bodyLen == 1:
		return nil, fmt.Errorf("wal: pad record padLen=%d not encodable (1-byte body cannot carry a chunk header)", padLen)
	case bodyLen <= 2+255:
		// xlrBlockIDDataShort: 1-byte tag + 1-byte length.
		body[0] = xlrBlockIDDataShort
		body[1] = byte(bodyLen - 2)
		// Bytes [2:] stay zero from make.
	default:
		// xlrBlockIDDataLong: 1-byte tag + 4-byte length.
		body[0] = xlrBlockIDDataLong
		binary.LittleEndian.PutUint32(body[1:5], uint32(bodyLen-5))
		// Bytes [5:] stay zero from make.
	}
	out := make([]byte, padLen)
	h := XLogRecord{
		TotLen: uint32(padLen),
		XID:    0,
		Prev:   prev,
		Info:   xlogInfoNoop,
		Rmid:   RmgrXLog,
	}
	if err := EncodeXLogRecordHeader(out[:SizeOfXLogRecord], h, body); err != nil {
		return nil, err
	}
	copy(out[SizeOfXLogRecord:], body)
	return out, nil
}
