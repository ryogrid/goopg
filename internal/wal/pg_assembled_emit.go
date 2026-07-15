package wal

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
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

// xlhInsertContainsNewTuple is PG's XLH_INSERT_CONTAINS_NEW_TUPLE
// (heapam_xlog.h): block 0 carries the inserted tuple's data.
const xlhInsertContainsNewTuple uint8 = 0x08

// EncodeHeapInsertPG builds a PostgreSQL xl_heap_insert record for one heap
// tuple insertion, framed for the assembled-record Append path
// (framePGAssembled). It is the PG-format replacement for the goopg-native
// EncodeHeapInsert. `tuple` is the fully marshaled HeapTuple
// (storage.HeapTuple.MarshalBinary): a fixed 23-byte header, then the null
// bitmap + alignment + column data. The record carries:
//   - main data: xl_heap_insert{offnum uint16 = lineSlot, flags uint8}
//   - block 0 (HAS_DATA): xl_heap_header{t_infomask2, t_infomask, t_hoff} + the
//     tuple bytes past the fixed header (tuple[SizeOfHeapTupleHeaderData:]),
//     verbatim (bitmap + padding + data).
//
// The owning xid is the tuple's xmin (tuple[0:4]) and is stamped into the
// XLogRecord header (xl_xid); replay reconstructs the fixed header from it and
// self-points t_ctid at (blk, lineSlot) — which A2-pre made the primary store
// too, so stored page == replay. No FPI is attached here; the first-touch
// full-page image is still emitted as a separate record by
// MarkDirtyLogicalChange (FPI/logical unification is a later step).
func EncodeHeapInsertPG(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte) ([]byte, error) {
	if len(tuple) < storage.SizeOfHeapTupleHeaderData {
		return nil, fmt.Errorf("wal: heap-insert tuple %d bytes < fixed header %d", len(tuple), storage.SizeOfHeapTupleHeaderData)
	}
	xid := binary.LittleEndian.Uint32(tuple[0:4]) // t_xmin

	// xl_heap_header (sizeOfXLogHeapHeaderData=5): t_infomask2, t_infomask,
	// t_hoff — the tuple's fixed-header offsets [18:20]/[20:22]/[22] — then the
	// tuple bytes past the fixed header, verbatim.
	blockData := make([]byte, 0, sizeOfXLogHeapHeaderData+len(tuple)-storage.SizeOfHeapTupleHeaderData)
	blockData = append(blockData, tuple[18:22]...)  // t_infomask2 (2) + t_infomask (2)
	blockData = append(blockData, tuple[22])        // t_hoff
	blockData = append(blockData, tuple[storage.SizeOfHeapTupleHeaderData:]...)

	mainData := make([]byte, sizeOfXLogHeapInsertData)
	binary.LittleEndian.PutUint16(mainData[0:2], lineSlot) // offnum
	mainData[2] = xlhInsertContainsNewTuple                // flags

	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: blk, Data: blockData,
	}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrHeap, xlogHeapInsert, xid, body), nil
}

// sizeOfXLogHeapDeleteData is PG's SizeOfHeapDelete: xmax(4) + offnum(2) +
// infobits_set(1) + flags(1).
const sizeOfXLogHeapDeleteData = 8

// xlhDeleteContainsOldTuple is PG's XLH_DELETE_CONTAINS_OLD_TUPLE
// (heapam_xlog.h): main data carries the pre-delete tuple after xl_heap_delete.
const xlhDeleteContainsOldTuple uint8 = 0x02

// EncodeHeapDeletePG builds a PostgreSQL xl_heap_delete record for one heap
// deletion (an xmax stamp on the deleted tuple), framed for the assembled-record
// Append path. Main data is xl_heap_delete{xmax, offnum, infobits_set, flags};
// block 0 references the tuple's page (no block data). When oldTuple is non-nil
// (logical replication needs the pre-delete row), the old tuple is appended to
// main data as xl_heap_header{t_infomask2, t_infomask, t_hoff} + the tuple bytes
// past the fixed header, and XLH_DELETE_CONTAINS_OLD_TUPLE is set — mirroring
// PG's log_heap_delete.
//
// infobits_set is 0: goopg stamps a plain deleter xmax (no HEAP_KEYS_UPDATED, no
// lock bits), and replay reproduces it with PageSetHeapTupleXmax, matching the
// native replay. xl_xid = xmax (the deleting xact).
func EncodeHeapDeletePG(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, oldTuple []byte) ([]byte, error) {
	mainData := make([]byte, sizeOfXLogHeapDeleteData)
	binary.LittleEndian.PutUint32(mainData[0:4], uint32(xmax))
	binary.LittleEndian.PutUint16(mainData[4:6], lineSlot)
	mainData[6] = 0 // infobits_set

	var flags uint8
	if len(oldTuple) >= storage.SizeOfHeapTupleHeaderData {
		flags |= xlhDeleteContainsOldTuple
		mainData = append(mainData, oldTuple[18:22]...) // t_infomask2 + t_infomask
		mainData = append(mainData, oldTuple[22])       // t_hoff
		mainData = append(mainData, oldTuple[storage.SizeOfHeapTupleHeaderData:]...)
	}
	mainData[7] = flags

	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrHeap, xlogHeapDelete, uint32(xmax), body), nil
}
