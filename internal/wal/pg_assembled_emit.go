package wal

import (
	"encoding/binary"
	"fmt"
)

// pg_assembled_emit.go carries PG-format records (built by assembleXLogRecord)
// through the existing payload-based Append machinery WITHOUT the native
// wrapXLogMainData single-chunk wrapping.
//
// The whole emit path (Writer.Append → tryAppend / append / appendPGCompat →
// encodeRecordXLog + predictXLogRecordLen) is keyed on a []byte payload. A
// native record is a goopg RecordKind body that encodeRecordXLog wraps as one
// main-data chunk and classifies via payload[0] (classifyXLogRecord). A
// PG-format record's body is already fully assembled (block refs + FPI +
// main-data chunk, via assembleXLogRecord) and must be emitted verbatim under an
// explicit (xl_rmid, xl_info, xl_xid) header — never re-wrapped or re-classified.
//
// To reuse the concurrency-sensitive append/reservation machinery unchanged, a
// PG-format record is passed as a thin ENVELOPE: a reserved marker byte, the
// header fields (rmid, info, xid), then the assembled body. encodeRecordXLog and
// predictXLogRecordLen recognise the marker and emit the body directly. The
// envelope is a PRE-ENCODE TRANSPORT only — it is stripped before the record is
// framed, so it never appears in the on-disk WAL stream (unlike the removed
// canonical / native-skip tags). This is the "emit PG records at the source,
// never convert" path (docs/design/wal-pg-identical-stream/01-record-content-parity.md):
// the body is built once at the mutation site, not translated from a native record.

// pgAssembledMarker tags an Append payload as a pre-assembled PG record.
// 0xFF is reserved: goopg RecordKind values are dense only up to ~132, leaving
// 133..255 free. TestPGAssembledMarkerReserved guards the reservation against a
// future RecordKind colliding with it.
const pgAssembledMarker byte = 0xFF

// pgAssembledEnvelopeHeader = marker(1) + rmid(1) + info(1) + xid(4).
const pgAssembledEnvelopeHeader = 7

// framePGAssembled wraps an assembled PG record body (from assembleXLogRecord)
// in the transport envelope consumed by encodeRecordXLog / predictXLogRecordLen.
func framePGAssembled(rmid Rmgr, info uint8, xid uint32, body []byte) []byte {
	out := make([]byte, pgAssembledEnvelopeHeader+len(body))
	out[0] = pgAssembledMarker
	out[1] = byte(rmid)
	out[2] = info
	binary.LittleEndian.PutUint32(out[3:7], xid)
	copy(out[pgAssembledEnvelopeHeader:], body)
	return out
}

// unframePGAssembled reports whether payload is a pre-assembled PG envelope and,
// if so, returns its header fields and the assembled body (aliasing payload).
func unframePGAssembled(payload []byte) (rmid Rmgr, info uint8, xid uint32, body []byte, ok bool) {
	if len(payload) < pgAssembledEnvelopeHeader || payload[0] != pgAssembledMarker {
		return 0, 0, 0, nil, false
	}
	rmid = Rmgr(payload[1])
	info = payload[2]
	xid = binary.LittleEndian.Uint32(payload[3:7])
	body = payload[pgAssembledEnvelopeHeader:]
	return rmid, info, xid, body, true
}

// encodeAssembledXLog frames an already-assembled PG record body into the
// on-disk XLogRecord stream: the 24-byte header carrying (rmid, info, xid, prev)
// + the body + trailing MAXALIGN pad. It is the non-wrapping counterpart of
// encodeRecordXLog and shares its prev / CRC / pad conventions. Returns the
// padded stream and the un-padded record length (== xl_tot_len).
func encodeAssembledXLog(body []byte, rmid Rmgr, info uint8, xid uint32, prev uint64) ([]byte, int, error) {
	realLen := xlogRecordHeaderSize + len(body)
	header := XLogRecord{
		TotLen: uint32(realLen),
		XID:    xid,
		Prev:   prev,
		Info:   info,
		Rmid:   rmid,
	}
	out := make([]byte, maxAlignXLog(realLen))
	if err := EncodeXLogRecordHeader(out[:xlogRecordHeaderSize], header, body); err != nil {
		return nil, 0, fmt.Errorf("wal: encode assembled xlog header: %w", err)
	}
	copy(out[xlogRecordHeaderSize:realLen], body)
	return out, realLen, nil
}
