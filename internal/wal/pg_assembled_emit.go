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

// heapHeaderPlusData splits a marshaled HeapTuple into the block-0 payload PG
// carries for insert/update: xl_heap_header (t_infomask2, t_infomask, t_hoff)
// followed by the tuple bytes past the fixed 23-byte header (null bitmap +
// padding + column data), verbatim.
func heapHeaderPlusData(tuple []byte) []byte {
	out := make([]byte, 0, sizeOfXLogHeapHeaderData+len(tuple)-storage.SizeOfHeapTupleHeaderData)
	out = append(out, tuple[18:22]...) // t_infomask2 (2) + t_infomask (2)
	out = append(out, tuple[22])       // t_hoff
	out = append(out, tuple[storage.SizeOfHeapTupleHeaderData:]...)
	return out
}

// sizeOfXLogHeapUpdateData is PG's SizeOfHeapUpdate: old_xmax(4) + old_offnum(2)
// + old_infobits_set(1) + flags(1) + new_xmax(4) + new_offnum(2).
const sizeOfXLogHeapUpdateData = 14

// xlhUpdateContainsNewTuple is PG's XLH_UPDATE_CONTAINS_NEW_TUPLE (heapam_xlog.h):
// block 0 carries the new tuple's data.
const xlhUpdateContainsNewTuple uint8 = 0x10

// EncodeHeapHotUpdatePG builds a PostgreSQL xl_heap_update record for one HOT
// (same-page) update, framed for the assembled-record Append path. Main data is
// xl_heap_update{old_xmax, old_offnum, old_infobits_set=0, flags=CONTAINS_NEW_TUPLE,
// new_xmax=0, new_offnum}; block 0 references the (shared) page and carries the
// new tuple as xl_heap_header + tuple bytes past the fixed header. The new tuple
// already carries HEAP_ONLY_TUPLE in its infomask. Prefix/suffix compression is
// skipped (PREFIX/SUFFIX_FROM_OLD unset). opcode = XLOG_HEAP_HOT_UPDATE (0x40);
// xl_xid = xmax (the updating xact).
//
// A HOT update touches only one page, so there is no block 1. Replay places the
// new tuple at new_offnum on block 0 and stamps the old tuple (old_offnum) with
// old_xmax + t_ctid->new + HEAP_HOT_UPDATED (replayDecodedXLogHeapUpdate).
func EncodeHeapHotUpdatePG(rel storage.RelFileNode, blk storage.BlockNumber, oldSlot, newSlot uint16, xmax storage.TransactionID, newTuple []byte) ([]byte, error) {
	if len(newTuple) < storage.SizeOfHeapTupleHeaderData {
		return nil, fmt.Errorf("wal: heap-hot-update new tuple %d bytes < fixed header %d", len(newTuple), storage.SizeOfHeapTupleHeaderData)
	}
	mainData := make([]byte, sizeOfXLogHeapUpdateData)
	binary.LittleEndian.PutUint32(mainData[0:4], uint32(xmax)) // old_xmax
	binary.LittleEndian.PutUint16(mainData[4:6], oldSlot)      // old_offnum
	mainData[6] = 0                                            // old_infobits_set
	mainData[7] = xlhUpdateContainsNewTuple                    // flags
	// new_xmax [8:12] stays 0 (the new tuple is not deleted).
	binary.LittleEndian.PutUint16(mainData[12:14], newSlot) // new_offnum

	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: blk, Data: heapHeaderPlusData(newTuple),
	}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrHeap, xlogHeapHotUpdate, uint32(xmax), body), nil
}

// sizeOfXLogBtreeInsertData is PG's SizeOfBtreeInsert: offnum(2).
const sizeOfXLogBtreeInsertData = 2

// EncodeBtreeInsertPG builds a PostgreSQL xl_btree_insert record for one leaf
// index-tuple insertion (XLOG_BTREE_INSERT_LEAF), framed for the assembled path.
// Main data is xl_btree_insert{offnum}; block 0 carries the new IndexTuple as
// block data. goopg replay re-inserts the tuple by key (btree.ApplyInsertRecord)
// and does not need offnum, so the emit path passes 0 — a documented parity gap
// (a real-PG standby would need the true leaf offset). xl_xid = 0 (btree index
// changes are not logical user-data events).
func EncodeBtreeInsertPG(rel storage.RelFileNode, blk storage.BlockNumber, offnum uint16, item []byte) ([]byte, error) {
	if len(item) == 0 {
		return nil, fmt.Errorf("wal: btree-insert item is empty")
	}
	mainData := make([]byte, sizeOfXLogBtreeInsertData)
	binary.LittleEndian.PutUint16(mainData[0:2], offnum)
	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: item}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeInsertLeaf, 0, body), nil
}

const (
	// xlogXactHasInfo is PG's XLOG_XACT_HAS_INFO — an xl_info bit above the
	// op-mask signalling that the xl_xact_commit/abort body carries an xinfo
	// word + xinfo-gated chunks after the fixed xact_time.
	xlogXactHasInfo uint8 = 0x80
	// xactXinfoHasInvals is PG's XACT_XINFO_HAS_INVALS (an xl_xact_xinfo bit):
	// the commit carries shared-invalidation messages (goopg uses it as the
	// relcache-init-file invalidation signal; the message array is empty).
	xactXinfoHasInvals uint32 = 1 << 3
	// minSizeOfXactCommit is PG's fixed xl_xact_commit prefix: xact_time (s8).
	minSizeOfXactCommit = 8
)

// EncodeXactCommitPG builds a PostgreSQL xl_xact_commit record, framed for the
// assembled path. The committing xid is carried in the record header (xl_xid),
// not the body. The body is xact_time (s8, always 0 — goopg has no commit
// timestamp). When hasInvals is set (the transaction changed a nailed catalog),
// XLOG_XACT_HAS_INFO + xinfo{HAS_INVALS} + an empty invals array (nmsgs=0) are
// appended, so standby replay unlinks the relcache init files (the old
// RecordKindXactCommitInval signal). No block references; opcode = XLOG_XACT_COMMIT.
func EncodeXactCommitPG(xid storage.TransactionID, hasInvals bool) ([]byte, error) {
	info := xlogXactCommit
	var mainData []byte
	if hasInvals {
		info |= xlogXactHasInfo
		mainData = make([]byte, minSizeOfXactCommit+8) // xact_time(8) + xinfo(4) + nmsgs(4)
		binary.LittleEndian.PutUint32(mainData[8:12], xactXinfoHasInvals)
		// mainData[12:16] = nmsgs = 0 (goopg carries no message array).
	} else {
		mainData = make([]byte, minSizeOfXactCommit) // xact_time = 0
	}
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrXact, info, uint32(xid), body), nil
}

// EncodeXactAbortPG builds a PostgreSQL xl_xact_abort record (xact_time s8 = 0,
// no chunks). The aborting xid is carried in the header (xl_xid); opcode =
// XLOG_XACT_ABORT.
func EncodeXactAbortPG(xid storage.TransactionID) ([]byte, error) {
	mainData := make([]byte, minSizeOfXactCommit) // xact_time = 0
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrXact, xlogXactAbort, uint32(xid), body), nil
}

// xactCommitCarriesInvals reports whether a decoded xl_xact_commit body signals
// relcache invalidations (XLOG_XACT_HAS_INFO + xinfo HAS_INVALS).
func xactCommitCarriesInvals(info uint8, mainData []byte) bool {
	if info&xlogXactHasInfo == 0 || len(mainData) < minSizeOfXactCommit+4 {
		return false
	}
	xinfo := binary.LittleEndian.Uint32(mainData[minSizeOfXactCommit : minSizeOfXactCommit+4])
	return xinfo&xactXinfoHasInvals != 0
}
