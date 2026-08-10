package wal

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
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
// too, so stored page == replay.
//
// initPage marks this as the FIRST tuple on a freshly-initialised page: the info
// byte gets XLOG_HEAP_INIT_PAGE (0x80) and block 0 is REGBUF_WILL_INIT. This is
// mandatory for heterogeneous PG-standby replay — PG's heap redo then PageInit's
// the (possibly not-yet-extended) page before applying the tuple, instead of
// treating a missing page as a "reference to an invalid page" and PANICking. It
// matches PG's own first-insert-on-a-new-page behaviour. goopg's own replay
// masks the opcode with xlogHeapOpMask (0x70), so the 0x80 flag is ignored there
// and the tuple still applies to the smgr-create-extended page. The redundant
// trailing first-touch XLOG_FPI (still emitted by MarkDirtyLogicalChange) then
// harmlessly restores the same post-insert page image.
func EncodeHeapInsertPG(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte, initPage bool) ([]byte, error) {
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
		ID: 0, Rel: rel, Block: blk, Data: blockData, WillInit: initPage,
	}})
	if err != nil {
		return nil, err
	}
	info := xlogHeapInsert
	if initPage {
		info |= xlogHeapInit
	}
	return framePGAssembled(RmgrHeap, info, xid, body), nil
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

// EncodeHeapUpdatePG builds a PostgreSQL xl_heap_update record for one NON-HOT
// heap update (XLOG_HEAP_UPDATE, 0x20) — B0.2's catalog-ALTER record (doc 02a
// §3), shared with any future non-HOT user-table flip. Layout mirrors upstream
// log_heap_update (heapam.c): main data is the same 14-byte xl_heap_update as
// the HOT form; block 0 is the NEW tuple's page carrying xl_heap_header +
// tuple bytes; when the old version lives on a DIFFERENT page, block 1
// references it (no data). Same-page updates carry a single block 0, exactly
// like PG. Prefix/suffix compression is not used. xl_xid = xmax (the updating
// xact); replay stamps the old tuple WITHOUT HOT bits (the successor is
// reached via indexes) and places the new version at new_offnum.
func EncodeHeapUpdatePG(rel storage.RelFileNode, oldBlk storage.BlockNumber, oldSlot uint16,
	newBlk storage.BlockNumber, newSlot uint16, xmax storage.TransactionID, newTuple []byte) ([]byte, error) {
	if len(newTuple) < storage.SizeOfHeapTupleHeaderData {
		return nil, fmt.Errorf("wal: heap-update new tuple %d bytes < fixed header %d", len(newTuple), storage.SizeOfHeapTupleHeaderData)
	}
	mainData := make([]byte, sizeOfXLogHeapUpdateData)
	binary.LittleEndian.PutUint32(mainData[0:4], uint32(xmax)) // old_xmax
	binary.LittleEndian.PutUint16(mainData[4:6], oldSlot)      // old_offnum
	mainData[6] = 0                                            // old_infobits_set
	mainData[7] = xlhUpdateContainsNewTuple                    // flags
	// new_xmax [8:12] stays 0 (the new tuple is not deleted).
	binary.LittleEndian.PutUint16(mainData[12:14], newSlot) // new_offnum

	blocks := []BlockRef{{
		ID: 0, Rel: rel, Block: newBlk, Data: heapHeaderPlusData(newTuple),
	}}
	if oldBlk != newBlk {
		blocks = append(blocks, BlockRef{ID: 1, Rel: rel, Block: oldBlk})
	}
	body, err := assembleXLogRecord(mainData, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrHeap, xlogHeapUpdate, uint32(xmax), body), nil
}

// sizeOfXLogBtreeInsertData is PG's SizeOfBtreeInsert: offnum(2).
const sizeOfXLogBtreeInsertData = 2

// EncodeBtreeInsertPG builds a PostgreSQL xl_btree_insert record for one leaf
// index-tuple insertion (XLOG_BTREE_INSERT_LEAF), framed for the assembled path.
// Main data is xl_btree_insert{offnum}; block 0 carries the new IndexTuple as
// block data. `offnum` is the physical 1-based offset number the writer placed
// the tuple at, which both a real-PG standby and goopg replay
// (btree.ApplyInsertRecordAt) apply at. It used to be a hard-coded 0 — a
// documented parity gap — because goopg replay re-inserted the tuple by key;
// M0130-S11.4 slice 3b-2c-ii-B2-b-ii made the offset real, since re-deriving
// the slot needs the index's comparison semantics and recovery has no catalog
// to resolve them from. xl_xid = 0 (btree index changes are not logical
// user-data events).
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

// xl_heap_prune flags (heapam_xlog.h XLHP_*), set in the record's second
// main-data byte and gating the block-0 sub-records in flag order.
const (
	xlhpHasConflictHorizon uint8 = 1 << 3 // XLHP_HAS_CONFLICT_HORIZON (snapshot horizon in main data)
	xlhpHasFreezePlans     uint8 = 1 << 4 // XLHP_HAS_FREEZE_PLANS
	xlhpHasRedirections    uint8 = 1 << 5 // XLHP_HAS_REDIRECTIONS
	xlhpHasDeadItems       uint8 = 1 << 6 // XLHP_HAS_DEAD_ITEMS
	xlhpHasNowUnusedItems  uint8 = 1 << 7 // XLHP_HAS_NOW_UNUSED_ITEMS
)

// sizeOfXLogHeapPruneData is PG's SizeOfHeapPrune: reason(1) + flags(1). A
// snapshot_conflict_horizon (u4) follows when XLHP_HAS_CONFLICT_HORIZON is set;
// goopg does not persist a horizon, so it is omitted.
const sizeOfXLogHeapPruneData = 2

// EncodeHeapPruneOptPG builds a PostgreSQL xl_heap_prune record for one page
// prune (opportunistic or VACUUM-scan). Main data is {reason=0, flags}; block 0
// carries the redirection pairs (XLHP_HAS_REDIRECTIONS: ntargets + u2[2*n], each
// pair = old-slot, target-slot, matching goopg's [2]uint16 redirects exactly)
// followed by the now-unused slots (XLHP_HAS_NOW_UNUSED_ITEMS: ntargets + u2[n]).
// goopg reclaims LP_DEAD items directly, so there is no XLHP_HAS_DEAD_ITEMS.
// opcode = XLOG_HEAP2_PRUNE_ON_ACCESS; xl_xid = 0 (pruning is not transactional
// user-data).
func EncodeHeapPruneOptPG(rel storage.RelFileNode, blk storage.BlockNumber, redirects [][2]uint16, unused []uint16) ([]byte, error) {
	var flags uint8
	var blockData []byte
	if len(redirects) > 0 {
		flags |= xlhpHasRedirections
		blockData = binary.LittleEndian.AppendUint16(blockData, uint16(len(redirects)))
		for _, r := range redirects {
			blockData = binary.LittleEndian.AppendUint16(blockData, r[0])
			blockData = binary.LittleEndian.AppendUint16(blockData, r[1])
		}
	}
	if len(unused) > 0 {
		flags |= xlhpHasNowUnusedItems
		blockData = binary.LittleEndian.AppendUint16(blockData, uint16(len(unused)))
		for _, u := range unused {
			blockData = binary.LittleEndian.AppendUint16(blockData, u)
		}
	}
	mainData := []byte{0, flags} // reason = 0, flags
	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: blockData}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrHeap2, xlogHeap2PruneOnAccess, 0, body), nil
}

// EncodeHeapFreezePG builds a PostgreSQL xl_heap_prune record for one page's
// tuple freeze. goopg freezes uniformly (rewrites each frozen tuple's xmin to
// FrozenTransactionId — no per-tuple xmax/infomask variation), so a single
// xlhp_freeze_plan covers all frozen slots: {xmax=0, t_infomask2=0, t_infomask=0,
// frzflags=0, ntuples=len(frozenSlots)} followed by the trailing offset array =
// frozenSlots. XLHP_HAS_FREEZE_PLANS; opcode XLOG_HEAP2_PRUNE_VACUUM_CLEANUP.
// Replay applies it via PageFreezeBySlots (goopg's xmin-rewrite freeze; a real-PG
// standby's infomask-bit freeze is a separate representation — a documented gap).
func EncodeHeapFreezePG(rel storage.RelFileNode, blk storage.BlockNumber, frozenSlots []uint16) ([]byte, error) {
	if len(frozenSlots) == 0 {
		return nil, fmt.Errorf("wal: heap-freeze with no frozen slots")
	}
	var blockData []byte
	blockData = binary.LittleEndian.AppendUint16(blockData, 1) // nplans
	blockData = binary.LittleEndian.AppendUint16(blockData, 0) // pad2
	// one xlhp_freeze_plan (sizeOfXLHPFreezePlan = 11): xmax, infomask2, infomask, frzflags, ntuples.
	blockData = binary.LittleEndian.AppendUint32(blockData, 0)                        // xmax
	blockData = binary.LittleEndian.AppendUint16(blockData, 0)                        // t_infomask2
	blockData = binary.LittleEndian.AppendUint16(blockData, 0)                        // t_infomask
	blockData = append(blockData, 0)                                                 // frzflags
	blockData = binary.LittleEndian.AppendUint16(blockData, uint16(len(frozenSlots))) // ntuples
	// trailing offset array = the frozen slots.
	for _, s := range frozenSlots {
		blockData = binary.LittleEndian.AppendUint16(blockData, s)
	}

	mainData := []byte{0, xlhpHasFreezePlans} // reason = 0, flags
	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: blockData}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrHeap2, xlogHeap2PruneVacuumClean, 0, body), nil
}

// sizeOfXLogBtreeSplitData is PG's SizeOfBtreeSplit: level(4) + firstrightoff(2)
// + newitemoff(2) + postingoff(2). No tail padding — the uint32 leads.
const sizeOfXLogBtreeSplitData = 10

// EncodeBtreeSplitPG builds a PostgreSQL RM_BTREE split record
// (XLOG_BTREE_SPLIT_R opcode), shaped for upstream `btree_xlog_split`
// (nbtxlog.c:180-352). M0130-S11.5b replaced the previous image-only form,
// which carried NO main data at all: upstream's redo opens with an
// unconditional `XLogRecGetData` cast to `xl_btree_split` and reads
// `xlrec->level` before it looks at any block, so an FPI-only record is
// unreplayable by the engine it is shaped for — the images made goopg↔goopg
// replay work and nothing else.
//
//	main data: xl_btree_split{level, firstrightoff, newitemoff, postingoff}
//	block 0:   the left half — either the incremental form (the new item when it
//	           landed on this half, then the page's new high key) or a full-page
//	           image (see below)
//	block 1:   the new right sibling, WILL_INIT, block data = its item area in
//	           `_bt_restore_page` order — NO image
//	block 2:   the page that was the left page's right sibling, no data
//	           (non-rightmost split only; redo only patches its back-link)
//	block 3:   the child one level down whose incomplete-split flag this
//	           insertion finishes, no data (INTERNAL split only)
//
// Block 1 must be content, not an image: upstream's redo rebuilds the right
// page from scratch on every replay (`XLogInitBufferForRedo` + `_bt_pageinit` +
// `_bt_restore_page`) and would overwrite a restored image with whatever the
// block data says — an empty page if there were none. The right page's opaque
// header is not carried at all; redo derives it from the record's level and
// block tags, so this encoder REFUSES a right page whose header does not match
// `btree.SplitRightPageOpaque`, rather than logging a record whose redo builds
// a different page than the primary wrote.
//
// Block 0 as an image is upstream-legal, not a shortcut around it: PG's own
// `_bt_split` logs the left half incrementally (the new item plus the page's
// new high key) but its redo reaches that path only under `BLK_NEEDS_REDO` —
// with a backup image the left half takes `BLK_RESTORED` and the incremental
// rebuild, along with firstrightoff/newitemoff/postingoff, is skipped entirely.
// Upstream says as much in the comment above its own `XLogRegisterBufData`
// ("If XLogInsert decides that it can omit orignewitem due to logging a
// full-page image of the left page, everything still works out").
//
// M0130-S11.5b-2 makes the incremental form the DEFAULT, so a split record is
// two tuples rather than a page. It is not unconditional, because goopg's split
// is not upstream's: `splitPage` reads the whole page out, appends the new item,
// runs a DEDUP CONSOLIDATION pass over the merged list and refills both halves,
// so the left half can hold posting tuples that were never on the original page,
// can have lost the pre-split page's LP_DEAD-marked items, and (on a root split)
// still carries BTP_ROOT where upstream's `_bt_split` clears it. None of that is
// describable by three offsets.
//
// Rather than enumerate those cases here and hope the list stays complete, the
// encoder asks the pages: `btree.DescribeSplitLeft` derives the description from
// the pre-split page and the two halves, `btree.CheckSplitLeft` replays it and
// compares the result with the left half the primary actually wrote, and only a
// clean reproduction is logged incrementally. Anything else falls back to the
// image — the same CheckVacuumDelete discipline S11.5c introduced. A caller with
// no pre-split page or no new item (the pre-runtime/bulk paths) gets the image
// too.
//
// Under the image the three offsets are never read by any redo — upstream's or
// goopg's — but they are still filled in consistently rather than zeroed: the
// record is logged as SPLIT_R (new item on the right), firstrightoff is where
// the right half begins in the split page's offset numbering (P_FIRSTDATAKEY is
// 2 — the post-split left page is never rightmost, it links to the new right
// page), newitemoff equals it because the new item is the first right item under
// that description, and postingoff is 0 (no posting split). A reader that
// ignores the image and believes the main data therefore still gets a coherent
// story, just not the one the primary actually executed.
//
// postingoff is 0 in BOTH forms: goopg has no posting-list split at insert time
// (its dedup pass runs over the whole page instead), and a non-zero value would
// make redo run `_bt_swap_posting` over an item the primary never rewrote.
//
// Block 3 exists on an INTERNAL split only, and is MANDATORY there
// (M0130-S11.5b-3): an internal page is only ever inserted into to complete a
// split one level down, and upstream's redo clears that child's
// BTP_INCOMPLETE_SPLIT under `if (!isleaf) _bt_clear_incomplete_split(record,
// 3)` before it touches any other block. `XLogReadBufferForRedo` PANICs on an
// unregistered block id rather than reporting it, so a level > 0 record without
// block 3 does not merely lose the flag clear — it takes a real PG standby
// down. The encoder therefore refuses that combination, the same way
// EncodeBtreeNewRootPG refuses a level > 0 newroot with no block 1. A leaf
// split must NOT carry one: upstream has no cbuf there, and a block 3 its redo
// never reads would be a record PG cannot round-trip.
//
// The block carries no data because there is nothing to describe: the primary
// clears the flag itself (`splitPage`, mirroring _bt_split's own
// `cpageop->btpo_flags &= ~BTP_INCOMPLETE_SPLIT`) and redo re-derives the same
// mutation from the page it finds.
//
// xl_xid = 0 (index maintenance is not a logical user-data event).
func EncodeBtreeSplitPG(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, prePage, leftPage, rightPage storage.Page, newItem []byte, sibBlk storage.BlockNumber, sibPage storage.Page, childBlk storage.BlockNumber) ([]byte, error) {
	if len(leftPage) != storage.BlockSize || len(rightPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split left/right page must be %d bytes", storage.BlockSize)
	}
	level, err := btree.CheckSplitRightPageOpaque(rightPage, leftBlk, sibBlk)
	if err != nil {
		return nil, fmt.Errorf("wal: btree-split: %w", err)
	}
	rightData, err := btree.PGRestorePageData(rightPage)
	if err != nil {
		return nil, fmt.Errorf("wal: btree-split right items: %w", err)
	}

	leftData, err := btree.PGDataItemCount(leftPage)
	if err != nil {
		return nil, fmt.Errorf("wal: btree-split left item count: %w", err)
	}
	// P_FIRSTDATAKEY of the post-split left page: it links to the new right
	// page, so it is never rightmost and its first data item sits past P_HIKEY.
	firstRightOff := uint16(2 + leftData)
	newItemOff := firstRightOff
	info := xlogBtreeSplitR
	leftBlock := BlockRef{ID: 0, Rel: rel, Block: leftBlk, Image: &FullPageImage{Page: leftPage, Apply: true}}

	// The incremental left half, when the split the primary performed is one
	// upstream's record can describe AND replaying that description reproduces
	// the page it wrote.
	if desc, derr := btree.DescribeSplitLeft(prePage, leftPage, rightPage, newItem); derr == nil &&
		btree.CheckSplitLeft(prePage, leftPage, level, rightBlk, desc) == nil {
		firstRightOff = desc.FirstRightOff
		newItemOff = desc.NewItemOff
		if desc.NewItemOnLeft {
			info = xlogBtreeSplitL
		}
		leftBlock = BlockRef{ID: 0, Rel: rel, Block: leftBlk, Data: btree.SplitLeftBlockData(desc)}
	}

	mainData := make([]byte, sizeOfXLogBtreeSplitData)
	binary.LittleEndian.PutUint32(mainData[0:4], level)
	binary.LittleEndian.PutUint16(mainData[4:6], firstRightOff)
	binary.LittleEndian.PutUint16(mainData[6:8], newItemOff)
	binary.LittleEndian.PutUint16(mainData[8:10], 0) // postingoff

	blocks := []BlockRef{
		leftBlock,
		{ID: 1, Rel: rel, Block: rightBlk, SameRel: true, WillInit: true, Data: rightData},
	}
	if sibBlk != storage.InvalidBlockNumber {
		if len(sibPage) != storage.BlockSize {
			return nil, fmt.Errorf("wal: btree-split sibling page must be %d bytes", storage.BlockSize)
		}
		blocks = append(blocks, BlockRef{ID: 2, Rel: rel, Block: sibBlk, SameRel: true})
	}
	if level > 0 {
		if childBlk == storage.InvalidBlockNumber {
			return nil, fmt.Errorf("wal: btree-split at level %d has no child block", level)
		}
		blocks = append(blocks, BlockRef{ID: 3, Rel: rel, Block: childBlk, SameRel: true})
	} else if childBlk != storage.InvalidBlockNumber {
		return nil, fmt.Errorf("wal: btree-split at level 0 must not carry child block %d", childBlk)
	}
	body, err := assembleXLogRecord(mainData, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, info, 0, body), nil
}

// sizeOfXLogBtreeVacuumData is PG's SizeOfBtreeVacuum: ndeleted(2) +
// nupdated(2).
const sizeOfXLogBtreeVacuumData = 4

// EncodeBtreeVacuumPG builds a PostgreSQL RM_BTREE vacuum record
// (XLOG_BTREE_VACUUM opcode), shaped for upstream `btree_xlog_vacuum`
// (nbtxlog.c:479-528):
//
//	main data: xl_btree_vacuum{ndeleted, nupdated}
//	block 0:   the leaf page. Either
//	           (a) block data = the ndeleted deleted offset numbers (uint16
//	               each, ascending), no image — the incremental form; or
//	           (b) a full-page image with ndeleted = nupdated = 0.
//
// M0130-S11.5c. The previous form carried NO main data at all, only the image.
// Unlike newroot/split that is not outright unreplayable — upstream dereferences
// `xlrec` only under BLK_NEEDS_REDO, which an applied image skips — but the
// record still lies to everything that reads it without replaying it, starting
// with `pg_waldump`'s `btree_desc`, which prints ndeleted/nupdated off the end
// of a zero-length main-data area. Both forms below now carry the struct.
//
// Which form is chosen is decided by asking whether the deletion DESCRIBES what
// the primary did, not by trusting the caller: `btree.CheckVacuumDelete` replays
// the offsets against the pre-vacuum page and compares the result with the page
// VACUUM actually wrote. It says no in two cases goopg's vacuum reaches and PG's
// does not:
//
//   - the page carried POSTING LISTS. goopg's vacuum filters an EXPANDED item
//     list (one entry per TID) and re-marshals the survivors individually, so a
//     posting tuple that lost one TID — or none — comes back as several ordinary
//     tuples. Upstream keeps it a posting tuple and describes the change with
//     `xl_btree_update` (nupdated), which is a rewrite of one item, not a change
//     of the page's item count. goopg never emits nupdated > 0.
//   - the page went EMPTY. VACUUM then also stamps BTDeleted|BTHalfDead (phase 1
//     of page deletion), and no `btree_xlog_vacuum` sets those flags.
//
// The caller may also pass deleted = nil outright (the dedup-recovery rewrite in
// _bt_insertonpg's fallback path reuses this record for a page rewrite that is a
// CONSOLIDATION, not a deletion — there are no deleted offsets to name).
//
// The image form is upstream-legal for the same reason block 0 of the split
// record is: redo takes BLK_RESTORED and skips the incremental arm entirely.
// xl_xid = 0 (index maintenance is not a logical user-data event).
func EncodeBtreeVacuumPG(rel storage.RelFileNode, blk storage.BlockNumber, prePage, page storage.Page, deleted []uint16) ([]byte, error) {
	if len(page) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-vacuum page must be %d bytes", storage.BlockSize)
	}
	incremental := len(deleted) > 0 && len(prePage) == storage.BlockSize &&
		btree.CheckVacuumDelete(prePage, page, deleted) == nil

	mainData := make([]byte, sizeOfXLogBtreeVacuumData)
	var block BlockRef
	if incremental {
		binary.LittleEndian.PutUint16(mainData[0:2], uint16(len(deleted)))
		blockData := make([]byte, 0, 2*len(deleted))
		for _, off := range deleted {
			blockData = binary.LittleEndian.AppendUint16(blockData, off)
		}
		block = BlockRef{ID: 0, Rel: rel, Block: blk, Data: blockData}
	} else {
		block = BlockRef{ID: 0, Rel: rel, Block: blk, Image: &FullPageImage{Page: page, Apply: true}}
	}
	body, err := assembleXLogRecord(mainData, []BlockRef{block})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeVacuum, 0, body), nil
}

// sizeOfXLogBtreeNewRootData is PG's SizeOfBtreeNewroot: rootblk(4) + level(4).
const sizeOfXLogBtreeNewRootData = 8

// sizeOfXLogBtreeMetadata is sizeof(xl_btree_metadata): six uint32 fields, a
// bool, and 3 bytes of tail padding to the struct's 4-byte alignment. PG's
// _bt_restore_meta asserts the block-data length is EXACTLY this, so the
// padding is part of the wire format, not an artefact.
const sizeOfXLogBtreeMetadata = 28

// encodeXLogBtreeMetadata serialises a metapage as PG's xl_btree_metadata
// (nbtxlog.h). btm_magic and btm_last_cleanup_num_heap_tuples are NOT carried:
// upstream's redo re-asserts the magic and resets num_heap_tuples to -1.0.
func encodeXLogBtreeMetadata(m btree.PGBTMetaPage) []byte {
	out := make([]byte, sizeOfXLogBtreeMetadata)
	binary.LittleEndian.PutUint32(out[0:4], m.Version)
	binary.LittleEndian.PutUint32(out[4:8], uint32(m.Root))
	binary.LittleEndian.PutUint32(out[8:12], m.Level)
	binary.LittleEndian.PutUint32(out[12:16], uint32(m.FastRoot))
	binary.LittleEndian.PutUint32(out[16:20], m.FastLevel)
	binary.LittleEndian.PutUint32(out[20:24], m.LastCleanupNumDelpages)
	if m.AllEqualImage {
		out[24] = 1
	}
	return out
}

// decodeXLogBtreeMetadata is encodeXLogBtreeMetadata's inverse.
func decodeXLogBtreeMetadata(b []byte) (btree.PGBTMetaPage, error) {
	if len(b) != sizeOfXLogBtreeMetadata {
		return btree.PGBTMetaPage{}, fmt.Errorf("wal: xl_btree_metadata is %d bytes, want %d", len(b), sizeOfXLogBtreeMetadata)
	}
	le := binary.LittleEndian
	return btree.PGBTMetaPage{
		Version:                le.Uint32(b[0:4]),
		Root:                   storage.BlockNumber(le.Uint32(b[4:8])),
		Level:                  le.Uint32(b[8:12]),
		FastRoot:               storage.BlockNumber(le.Uint32(b[12:16])),
		FastLevel:              le.Uint32(b[16:20]),
		LastCleanupNumDelpages: le.Uint32(b[20:24]),
		AllEqualImage:          b[24] != 0,
	}, nil
}

// EncodeBtreeNewRootPG builds a PostgreSQL RM_BTREE new-root record
// (XLOG_BTREE_NEWROOT opcode), byte-faithful to upstream `_bt_newroot`
// (nbtinsert.c:2556-2597). M0130-S11.5a replaced the previous full-page-image
// form: an FPI-only record has a PG-shaped HEADER but no `xl_btree_newroot` main
// data, and a real PG standby's `btree_xlog_newroot` reads that struct
// unconditionally — the images made goopg↔goopg replay work while leaving the
// record unreplayable by the engine it is shaped for.
//
//	main data: xl_btree_newroot{rootblk, level}
//	block 0:   the new root, WILL_INIT, block data = its item area in
//	           `_bt_restore_page` order (level > 0 only; a level-0 root is empty)
//	block 1:   the left child, so redo can clear its incomplete-split flag
//	           (level > 0 only — upstream's redo touches block 1 under exactly
//	           the same condition)
//	block 2:   the metapage, WILL_INIT, block data = xl_btree_metadata
//
// `level` and the item area come from rootPage, and the metadata from metaPage,
// so the caller cannot describe the record inconsistently with the pages it is
// logging. xl_xid = 0 (index changes are not logical user-data events).
//
// The level-0 case has no upstream counterpart in `_bt_newroot` (upstream never
// builds a leaf root through it); goopg reaches it from VACUUM's root reset
// (`resetToEmptyRoot`). It is nonetheless exactly what upstream's redo handles:
// `btree_xlog_newroot` initialises the page, sets BTP_ROOT|BTP_LEAF for level 0
// and skips both the item restore and block 1.
func EncodeBtreeNewRootPG(rel storage.RelFileNode, rootBlk storage.BlockNumber, rootPage storage.Page, leftChildBlk storage.BlockNumber, metaBlk storage.BlockNumber, metaPage storage.Page) ([]byte, error) {
	if len(rootPage) != storage.BlockSize || len(metaPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-newroot root/meta page must be %d bytes", storage.BlockSize)
	}
	level := btree.ReadPGOpaque(rootPage).Level
	mainData := make([]byte, sizeOfXLogBtreeNewRootData)
	binary.LittleEndian.PutUint32(mainData[0:4], uint32(rootBlk))
	binary.LittleEndian.PutUint32(mainData[4:8], level)

	blocks := []BlockRef{{ID: 0, Rel: rel, Block: rootBlk, WillInit: true}}
	if level > 0 {
		if leftChildBlk == storage.InvalidBlockNumber {
			return nil, fmt.Errorf("wal: btree-newroot at level %d has no left child block", level)
		}
		data, err := btree.PGRestorePageData(rootPage)
		if err != nil {
			return nil, fmt.Errorf("wal: btree-newroot root items: %w", err)
		}
		blocks[0].Data = data
		blocks = append(blocks, BlockRef{ID: 1, Rel: rel, Block: leftChildBlk, SameRel: true})
	}
	blocks = append(blocks, BlockRef{
		ID: 2, Rel: rel, Block: metaBlk, SameRel: true, WillInit: true,
		Data: encodeXLogBtreeMetadata(btree.ReadPGMetaPage(metaPage)),
	})
	body, err := assembleXLogRecord(mainData, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeNewRoot, 0, body), nil
}

// sizeOfXLogBtreeMarkPageHalfDeadData is PG's SizeOfBtreeMarkPageHalfDead:
// poffset(2) + 2 bytes of C alignment padding + leafblk(4) + leftblk(4) +
// rightblk(4) + topparent(4) = 20. The padding is part of the wire format —
// upstream casts the main-data area straight to the struct.
const sizeOfXLogBtreeMarkPageHalfDeadData = 20

// EncodeBtreeMarkPageHalfDeadPG builds a PostgreSQL RM_BTREE phase-1
// page-deletion record (XLOG_BTREE_MARK_PAGE_HALFDEAD opcode), shaped for
// upstream `btree_xlog_mark_page_halfdead` (nbtxlog.c:762-848) and emitted by
// `_bt_mark_page_halfdead` (nbtpage.c):
//
//	main data: xl_btree_mark_page_halfdead{poffset, leafblk, leftblk, rightblk,
//	                                       topparent}
//	block 0:   the leaf, WILL_INIT, no block data — redo recreates the half-dead
//	           page from the main data alone
//	block 1:   the to-be-deleted subtree's parent, so redo can remove the
//	           downlink
//
// M0130-S11.5d-1. The record goopg emitted before this
// (`EncodeBtreeMarkPageHalfDead`, RecordKind 25) carried leafblk plus a flags
// word and NO registered blocks at all, under a header that nevertheless
// announced RM_BTREE/XLOG_BTREE_MARK_PAGE_HALFDEAD. That is the same trap
// S11.5a found under newroot, one degree worse: upstream's redo does not merely
// misread the main data, it calls `XLogInitBufferForRedo(record, 0)`
// unconditionally, and a block id that was never registered is a PANIC in
// `XLogReadBufferForRedoExtended`, not a bad page. A standby therefore dies on
// the first goopg page deletion rather than diverging quietly.
//
// `leftblk`/`rightblk` are read off leafPage rather than taken from the caller,
// for the same reason EncodeBtreeNewRootPG derives level from the page it logs:
// the record must not be able to describe a page differently from the page.
//
// `topparent` is InvalidBlockNumber when the leaf is itself the top of the
// subtree being deleted — upstream writes exactly that when `topparent ==
// leafblkno`. `poffset` is a PHYSICAL OffsetNumber in the parent.
// xl_xid = 0 (index maintenance is not a logical user-data event).
func EncodeBtreeMarkPageHalfDeadPG(rel storage.RelFileNode, leafBlk storage.BlockNumber, leafPage storage.Page, parentBlk storage.BlockNumber, poffset uint16, topparent storage.BlockNumber) ([]byte, error) {
	if len(leafPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-mark-halfdead leaf page must be %d bytes", storage.BlockSize)
	}
	if parentBlk == storage.InvalidBlockNumber {
		// Upstream registers block 1 unconditionally and its redo reads it
		// unconditionally; a root-only tree never reaches _bt_mark_page_halfdead
		// because a root is not deletable.
		return nil, fmt.Errorf("wal: btree-mark-halfdead has no parent block")
	}
	if poffset == 0 {
		return nil, fmt.Errorf("wal: btree-mark-halfdead poffset 0 is not a valid OffsetNumber")
	}
	if topparent == leafBlk {
		return nil, fmt.Errorf("wal: btree-mark-halfdead topparent %d must be InvalidBlockNumber when it is the leaf itself", topparent)
	}
	op := btree.ReadPGOpaque(leafPage)
	mainData := make([]byte, sizeOfXLogBtreeMarkPageHalfDeadData)
	binary.LittleEndian.PutUint16(mainData[0:2], poffset)
	binary.LittleEndian.PutUint32(mainData[4:8], uint32(leafBlk))
	binary.LittleEndian.PutUint32(mainData[8:12], uint32(op.Prev))
	binary.LittleEndian.PutUint32(mainData[12:16], uint32(op.Next))
	binary.LittleEndian.PutUint32(mainData[16:20], uint32(topparent))

	body, err := assembleXLogRecord(mainData, []BlockRef{
		{ID: 0, Rel: rel, Block: leafBlk, WillInit: true},
		{ID: 1, Rel: rel, Block: parentBlk, SameRel: true},
	})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeMarkPageHalfDead, 0, body), nil
}

// sizeOfXLogBtreeUnlinkPageData is PG's SizeOfBtreeUnlinkPage:
// leftsib(4) + rightsib(4) + level(4) + 4 bytes of C alignment padding +
// safexid(8) + leafleftsib(4) + leafrightsib(4) + leaftopparent(4) = 36. The
// padding exists because FullTransactionId is a uint64 and therefore 8-byte
// aligned; like every other main-data struct it is cast, not parsed, so the
// hole is wire format. 36 is `offsetof(leaftopparent) + sizeof(BlockNumber)` —
// upstream registers the offsetof size, not sizeof(struct), so the record does
// NOT carry the struct's own 4 bytes of trailing padding.
const sizeOfXLogBtreeUnlinkPageData = 36

// BtreeUnlinkPagePGRequest is the input to EncodeBtreeUnlinkPagePG. The record's
// link/level fields are deliberately NOT in it: they are read off the pages
// being logged (see EncodeBtreeUnlinkPagePG).
type BtreeUnlinkPagePGRequest struct {
	Rel storage.RelFileNode
	// TargetBlk / TargetPage are the page being unlinked. TargetPage may be
	// either its pre- or post-mutation image: the unlink preserves btpo_prev,
	// btpo_next and btpo_level, which are the only fields read from it.
	TargetBlk  storage.BlockNumber
	TargetPage storage.Page
	// SafeXid is the FullTransactionId stamped by BTPageSetDeleted — upstream's
	// `ReadNextFullTransactionId()` at deletion time, the XID from which the
	// block becomes recyclable.
	SafeXid uint64
	// LeafBlk is the half-dead leaf at the bottom of the subtree being deleted.
	// It equals TargetBlk on the last (or only) page of the subtree, in which
	// case LeafPage must be nil and no block 3 is registered.
	LeafBlk  storage.BlockNumber
	LeafPage storage.Page
	// MetaBlk / MetaPage make this the XLOG_BTREE_UNLINK_PAGE_META variant.
	// MetaBlk is storage.InvalidBlockNumber when the metapage is untouched.
	MetaBlk  storage.BlockNumber
	MetaPage storage.Page
}

// EncodeBtreeUnlinkPagePG builds a PostgreSQL RM_BTREE phase-2 page-deletion
// record (XLOG_BTREE_UNLINK_PAGE, or …_META when a metapage is supplied),
// shaped for upstream `btree_xlog_unlink_page` (nbtxlog.c:850-1005) and emitted
// by `_bt_unlink_halfdead_page` (nbtpage.c:2680-2740):
//
//	main data: xl_btree_unlink_page{leftsib, rightsib, level, safexid,
//	                                leafleftsib, leafrightsib, leaftopparent}
//	block 0:   the target, WILL_INIT, no data — redo rewrites it as an empty
//	           deleted page from the main data alone
//	block 1:   the left sibling, ONLY when the target has one
//	block 2:   the right sibling, unconditionally
//	block 3:   the half-dead leaf, ONLY when the target is an internal page —
//	           WILL_INIT, no data, redo recreates it with the next child down
//	block 4:   the metapage on the _META variant, WILL_INIT + xl_btree_metadata
//
// M0130-S11.5d-2, the second half of the pair S11.5d-1 started. goopg's native
// `RecordKindBtreeUnlinkPage` covers BOTH phases in one record and is emitted
// under this same RM_BTREE/0x80 header, so a standby reading it hits the same
// class of failure S11.5d-1 documented for phase 1: the main data is read as a
// struct it is not, and `XLogInitBufferForRedo(record, 0)` PANICs on a block id
// that was never registered.
//
// Every structural field is read off a page rather than accepted from the
// caller, the discipline S11.5a/S11.5d-1 established — the record must not be
// able to describe a page differently from the page it logs. `leftsib`,
// `rightsib` and `level` come from the target's opaque; `leafleftsib` and
// `leafrightsib` from the leaf's; `leaftopparent` from the leaf's dummy high
// key, which is where `_bt_unlink_halfdead_page` has just written it (nbtpage.c
// BTreeTupleSetTopParent(leafhikey, leaftopparent)) and where the NEXT
// invocation will read it back from.
//
// A rightmost target is refused rather than encoded: upstream's redo reads block
// 2 unconditionally, so "no right sibling" has no representation in this record
// at all — which is consistent, because `_bt_pagedel` never deletes a rightmost
// page (it would have to update the parent's high key).
//
// xl_xid = 0 (index maintenance is not a logical user-data event).
func EncodeBtreeUnlinkPagePG(req BtreeUnlinkPagePGRequest) ([]byte, error) {
	if len(req.TargetPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-unlink-page target page must be %d bytes", storage.BlockSize)
	}
	op := btree.ReadPGOpaque(req.TargetPage)
	leftsib, rightsib, level := op.Prev, op.Next, op.Level
	if rightsib == btree.PNone {
		return nil, fmt.Errorf("wal: btree-unlink-page target %d is rightmost; upstream redo reads block 2 unconditionally", req.TargetBlk)
	}
	if leftsib == req.TargetBlk || rightsib == req.TargetBlk {
		return nil, fmt.Errorf("wal: btree-unlink-page target %d is its own sibling", req.TargetBlk)
	}

	// Upstream's own values for the target-is-leaf case: the leaf's links ARE
	// the target's, and there is no next child down because this is the last
	// page of the subtree being deleted.
	leafleftsib, leafrightsib := leftsib, rightsib
	leaftopparent := storage.InvalidBlockNumber
	blocks := []BlockRef{{ID: 0, Rel: req.Rel, Block: req.TargetBlk, WillInit: true}}
	if leftsib != btree.PNone {
		blocks = append(blocks, BlockRef{ID: 1, Rel: req.Rel, Block: leftsib, SameRel: true})
	}
	blocks = append(blocks, BlockRef{ID: 2, Rel: req.Rel, Block: rightsib, SameRel: true})

	if req.LeafBlk != req.TargetBlk {
		if level == 0 {
			return nil, fmt.Errorf("wal: btree-unlink-page leaf %d differs from level-0 target %d", req.LeafBlk, req.TargetBlk)
		}
		if len(req.LeafPage) != storage.BlockSize {
			return nil, fmt.Errorf("wal: btree-unlink-page internal target needs the half-dead leaf page")
		}
		leafOp := btree.ReadPGOpaque(req.LeafPage)
		if leafOp.Flags&btree.BTPHalfDead == 0 {
			return nil, fmt.Errorf("wal: btree-unlink-page leaf %d is not half-dead", req.LeafBlk)
		}
		hikey, ok, err := btree.PGHighKeyRaw(req.LeafPage)
		if err != nil {
			return nil, fmt.Errorf("wal: btree-unlink-page leaf high key: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("wal: btree-unlink-page half-dead leaf %d has no top-parent high key", req.LeafBlk)
		}
		leafleftsib, leafrightsib = leafOp.Prev, leafOp.Next
		leaftopparent = btree.BTreeTupleGetDownLink(hikey)
		// Upstream's Assert: a valid leaftopparent means there is still a level
		// between the leaf and the target, so the target sits at level > 1.
		if leaftopparent != storage.InvalidBlockNumber && level <= 1 {
			return nil, fmt.Errorf("wal: btree-unlink-page leaftopparent %d at target level %d (want > 1)", leaftopparent, level)
		}
		blocks = append(blocks, BlockRef{ID: 3, Rel: req.Rel, Block: req.LeafBlk, SameRel: true, WillInit: true})
	} else if req.LeafPage != nil {
		return nil, fmt.Errorf("wal: btree-unlink-page target %d is the leaf; no separate leaf page may be logged", req.TargetBlk)
	}

	info := xlogBtreeUnlinkPage
	if req.MetaBlk != storage.InvalidBlockNumber {
		if len(req.MetaPage) != storage.BlockSize {
			return nil, fmt.Errorf("wal: btree-unlink-page meta page must be %d bytes", storage.BlockSize)
		}
		blocks = append(blocks, BlockRef{
			ID: 4, Rel: req.Rel, Block: req.MetaBlk, SameRel: true, WillInit: true,
			Data: encodeXLogBtreeMetadata(btree.ReadPGMetaPage(req.MetaPage)),
		})
		info = xlogBtreeUnlinkPageMeta
	}

	mainData := make([]byte, sizeOfXLogBtreeUnlinkPageData)
	le := binary.LittleEndian
	le.PutUint32(mainData[0:4], uint32(leftsib))
	le.PutUint32(mainData[4:8], uint32(rightsib))
	le.PutUint32(mainData[8:12], level)
	le.PutUint64(mainData[16:24], req.SafeXid)
	le.PutUint32(mainData[24:28], uint32(leafleftsib))
	le.PutUint32(mainData[28:32], uint32(leafrightsib))
	le.PutUint32(mainData[32:36], uint32(leaftopparent))

	body, err := assembleXLogRecord(mainData, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, info, 0, body), nil
}

// EncodePageImagePG builds a PostgreSQL RM_XLOG standalone full-page-image record
// (XLOG_FPI opcode) carrying the page as a block-0 apply-FPI. goopg's first-touch
// FPI anchor (storage.Pool.maybeEmitFPI) captures the exact page bytes, so this
// reuses the A0 FPI encoder: the free-space hole [pd_lower:pd_upper] is removed on
// the wire (matching PG with wal_compression=off), so the record is smaller than
// the native EncodePageImage's full 8 KiB body. BKPIMAGE_APPLY makes replay
// (replayDecodedXLogHeapFPIBlocks via the RmgrXLog/XLOG_FPI decoded arm) restore
// the image and stamp pd_lsn to the record LSN — identical to the native
// replayPageImage and to PG's XLOG_FPI redo. No main-data is carried; xl_xid = 0.
func EncodePageImagePG(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) ([]byte, error) {
	if len(page) != storage.BlockSize {
		return nil, fmt.Errorf("wal: page image must be %d bytes", storage.BlockSize)
	}
	blocks := []BlockRef{{ID: 0, Rel: rel, Block: blk, Image: &FullPageImage{Page: page, Apply: true}}}
	body, err := assembleXLogRecord(nil, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrXLog, xlogXLogFPI, 0, body), nil
}

// EncodeSmgrCreatePG builds a PostgreSQL RM_SMGR relation-file-creation record
// (XLOG_SMGR_CREATE opcode) with an `xl_smgr_create` main-data body
// (RelFileLocator{spcOid,dbOid,relNumber} + ForkNumber, 16 bytes) and no block
// ref — mirroring PG's log_smgrcreate. The default tablespace (goopg's TblOid=0)
// encodes as pgDefaultTableSpaceOID (1663), matching the A0 assembler's
// RelFileLocator convention. The record carries the creating transaction's xid
// in the header (PG stamps it via XLogInsert): this is both PG-faithful and what
// makes the record route to the decoded replay path — a main-data-only record
// reaches replayDecodedXLogRecord only when nativeHeaderMatchesMainData is false,
// and a non-zero header xid mismatches classifyXLogRecord's always-zero xid.
// (Bootstrap creates legitimately pass xid=0; those relations live in
// pg_default/pg_global whose spcOid low byte is not a native RecordKind, so they
// still route correctly.) Replay: the RmgrStorage/XLOG_SMGR_CREATE decoded arm
// decodes the body and calls applySmgrCreate — identical to native replaySmgrCreate.
func EncodeSmgrCreatePG(rel storage.RelFileNode, xid storage.TransactionID) ([]byte, error) {
	spc := rel.TblOid
	switch {
	case rel.DBOid == 0:
		// Shared catalog in global/ — spcOid=1664/dbOid=0 (B4.1a). Not
		// exercised today (every shared catalog's files pre-exist from
		// initdb), but keeps DBOid==0 ⟺ spcOid=1664 coherent with the
		// block-ref encoder above.
		spc = pgGlobalTableSpaceOID
	case spc == 0:
		spc = pgDefaultTableSpaceOID
	}
	mainData := make([]byte, 0, 16)
	mainData = binary.LittleEndian.AppendUint32(mainData, spc)
	mainData = binary.LittleEndian.AppendUint32(mainData, rel.DBOid)
	mainData = binary.LittleEndian.AppendUint32(mainData, rel.RelOid)
	mainData = binary.LittleEndian.AppendUint32(mainData, uint32(rel.Fork))
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrStorage, xlogSmgrCreate, uint32(xid), body), nil
}

// EncodeTblspcCreatePG builds a PostgreSQL RM_TBLSPC tablespace-create record
// (XLOG_TBLSPC_CREATE) with an `xl_tblspc_create_rec` main-data body
// (ts_id Oid + null-terminated ts_path) and no block ref, mirroring PG's
// CreateTableSpace XLogInsert (commands/tablespace.c). location is the LOCATION
// string (empty for an in-place tablespace); PG's tblspc_redo recreates the
// pg_tblspc/<oid> directory/symlink from it. The record carries the creating
// xid in the header so it routes to the decoded replay path (same contract as
// EncodeSmgrCreatePG). B4.1d.
func EncodeTblspcCreatePG(tsOID uint32, location string, xid storage.TransactionID) ([]byte, error) {
	mainData := make([]byte, 0, 4+len(location)+1)
	mainData = binary.LittleEndian.AppendUint32(mainData, tsOID)
	mainData = append(mainData, []byte(location)...)
	mainData = append(mainData, 0) // ts_path null terminator
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrTblspc, xlogTblspcCreate, uint32(xid), body), nil
}

// EncodeTblspcDropPG builds a PostgreSQL RM_TBLSPC tablespace-drop record
// (XLOG_TBLSPC_DROP) with an `xl_tblspc_drop_rec` main-data body (ts_id Oid)
// and no block ref, mirroring PG's DropTableSpace XLogInsert. B4.1d.
func EncodeTblspcDropPG(tsOID uint32, xid storage.TransactionID) ([]byte, error) {
	mainData := binary.LittleEndian.AppendUint32(make([]byte, 0, 4), tsOID)
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrTblspc, xlogTblspcDrop, uint32(xid), body), nil
}

// EncodeDbaseCreateWalLogPG builds a PostgreSQL RM_DBASE create record using the
// WAL_LOG strategy (XLOG_DBASE_CREATE_WAL_LOG) with an
// `xl_dbase_create_wal_log_rec` main-data body {db_id Oid, tablespace_id Oid},
// mirroring PG's CreateDatabaseUsingWalLog (commands/dbcommands.c). Its redo
// creates base/<db_id>/ (+ PG_VERSION); the copied relation blocks follow as
// separate full-page-image records (EncodePageImagePG) so a standby reconstructs
// goopg's exact new-database files. B4.6 Stage 3.
func EncodeDbaseCreateWalLogPG(dbOID, tsOID uint32, xid storage.TransactionID) ([]byte, error) {
	mainData := make([]byte, 0, 8)
	mainData = binary.LittleEndian.AppendUint32(mainData, dbOID)
	mainData = binary.LittleEndian.AppendUint32(mainData, tsOID)
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrDbase, xlogDbaseCreateWalLog, uint32(xid), body), nil
}

// EncodeDbaseDropPG builds a PostgreSQL RM_DBASE drop record (XLOG_DBASE_DROP)
// with an `xl_dbase_drop_rec` main-data body {db_id Oid, ntablespaces int32,
// tablespace_ids[] Oid}, mirroring PG's dropdb XLogInsert. goopg databases all
// live in the default tablespace, so tsOIDs is normally [1663]. Its redo removes
// base/<db_id>/ (per tablespace). B4.6 Stage 3.
func EncodeDbaseDropPG(dbOID uint32, tsOIDs []uint32, xid storage.TransactionID) ([]byte, error) {
	mainData := make([]byte, 0, 8+4*len(tsOIDs))
	mainData = binary.LittleEndian.AppendUint32(mainData, dbOID)
	mainData = binary.LittleEndian.AppendUint32(mainData, uint32(len(tsOIDs)))
	for _, ts := range tsOIDs {
		mainData = binary.LittleEndian.AppendUint32(mainData, ts)
	}
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrDbase, xlogDbaseDrop, uint32(xid), body), nil
}

// clogXactsPerPage is PostgreSQL's CLOG_XACTS_PER_PAGE: 2 status bits per xact →
// 4 xacts per byte → BLCKSZ*4 per CLOG page. Used to derive xl_clog_truncate's
// pageno from the oldest surviving xid (matches mvcc.clogXactsPerPage).
const clogXactsPerPage = storage.BlockSize * 4

// EncodeClogTruncatePG builds a PostgreSQL RM_CLOG truncation record
// (CLOG_TRUNCATE opcode) with an `xl_clog_truncate` main-data body
// (pageno int64, oldestXact TransactionId, oldestXactDb Oid — 16 bytes) and no
// block ref, mirroring PG's WriteTruncateXlogRec. The header xl_xid is 0 (PG's
// clog truncation is a maintenance op with no current transaction), so routing
// to the decoded replay path relies on the native-size guard in
// nativeHeaderMatchesMainData (a native RecordKindClogTruncate body is 5 bytes,
// this is 16) rather than an xid mismatch. Physical replay is a no-op; the
// truncation is re-applied by the initdb clog-recovery scan (replayCLogFromWAL).
func EncodeClogTruncatePG(oldestXid storage.TransactionID, oldestXactDb uint32) ([]byte, error) {
	pageno := int64(uint64(oldestXid) / uint64(clogXactsPerPage))
	mainData := make([]byte, 0, 16)
	mainData = binary.LittleEndian.AppendUint64(mainData, uint64(pageno))
	mainData = binary.LittleEndian.AppendUint32(mainData, uint32(oldestXid))
	mainData = binary.LittleEndian.AppendUint32(mainData, oldestXactDb)
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrCLOG, xlogClogTruncate, 0, body), nil
}

// EncodeCheckpointPG builds a PostgreSQL RM_XLOG checkpoint record carrying the
// 88-byte CheckPoint struct as main data, tagged with the EXPLICIT opcode —
// XLOG_CHECKPOINT_SHUTDOWN (0x00) or XLOG_CHECKPOINT_ONLINE (0x10) — via the
// assembled-record path (A9-checkpoint-opcode). This replaces classifyXLogRecord's
// retired classify-by-len==88 hack, which could only ever stamp SHUTDOWN.
//
// oldestActiveXid follows upstream CreateCheckPoint (xlog.c): the caller passes
// InvalidTransactionId (0) for a shutdown checkpoint — recovery derives the value
// from PrescanPreparedTransactions on that path — and the oldest running xid
// (nextXid when none) for an online one. An ONLINE checkpoint MUST be preceded by
// an XLOG_RUNNING_XACTS record (EncodeRunningXactsPG) or a hot-standby PG never
// reaches STANDBY_SNAPSHOT_READY; the checkpointer owns that pairing.
//
// Routing: the header xid is 0 and the main data is 88 bytes, but no RecordKind
// maps to (RmgrXLog, 0x00|0x10) in recordKindToRmgrInfo, so
// nativeHeaderMatchesMainData can never match — the record always reaches the
// decoded replay path (a recognised no-op; checkpoint consumers are the
// header-driven isCheckpointRecord/replayStart, not replay).
func EncodeCheckpointPG(shutdown bool, redoLSN0 uint64, tli uint32, nextXid uint64, nextOid uint32, oldestActiveXid uint32) ([]byte, error) {
	if nextXid < 3 {
		nextXid = 3
	}
	mainData := encodeCheckPointStruct(redoLSN0, tli, nextXid, nextOid, oldestActiveXid)
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	info := xlogCheckpointOnline
	if shutdown {
		info = xlogCheckpointShutdown
	}
	return framePGAssembled(RmgrXLog, info, 0, body), nil
}

// EncodeCheckpointRedoPG builds the PG17+ RM_XLOG XLOG_CHECKPOINT_REDO record
// that marks the redo point of an ONLINE checkpoint (upstream CreateCheckPoint,
// xlog.c:7099: "the LSN at which it starts becomes the new redo pointer").
// Recovery reads the record at CheckPoint.redo and FATALs with "unexpected
// record type found at redo point" unless it is exactly this. Main data is the
// 4-byte wal_level (replica=1), included upstream for the WAL summarizer.
// Shutdown checkpoints don't emit it — the checkpoint record itself marks redo.
func EncodeCheckpointRedoPG() ([]byte, error) {
	mainData := binary.LittleEndian.AppendUint32(make([]byte, 0, 4), 1) // wal_level=replica
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrXLog, xlogCheckpointRedo, 0, body), nil
}

// minSizeOfXlRunningXacts is offsetof(xl_running_xacts, xids) in PG18
// (storage/standbydefs.h): xcnt(4) + subxcnt(4) + subxid_overflow(1+3 pad) +
// nextXid(4) + oldestRunningXid(4) + latestCompletedXid(4).
const minSizeOfXlRunningXacts = 24

// EncodeRunningXactsPG builds a PostgreSQL RM_STANDBY XLOG_RUNNING_XACTS record
// (upstream LogCurrentRunningXacts, storage/standby.c) — the running-transaction
// snapshot a hot-standby PG needs to reach STANDBY_SNAPSHOT_READY after an
// ONLINE checkpoint. xids are goopg's running TOP-LEVEL xids (mvcc snapshot
// InProgress — one xid per proc slot; sub-xids live in pg_subtrans, never in
// slots), so subxcnt is always 0. subxid_overflow is stamped true whenever any
// xact is running: goopg cannot cheaply prove those xacts hold no live sub-xids,
// and overflow=true is the PG-legal conservative encoding (the standby falls
// back to pg_subtrans-era tracking until the snapshot xacts drain). An idle
// snapshot (no xids) is exact: overflow=false, oldestRunning = nextXid,
// latestCompleted = nextXid-1 — the standby becomes snapshot-ready immediately,
// which is the pg_basebackup/failover fast path.
func EncodeRunningXactsPG(nextXid, oldestRunning, latestCompleted uint32, xids []uint32) ([]byte, error) {
	mainData := make([]byte, minSizeOfXlRunningXacts+4*len(xids))
	le := binary.LittleEndian
	le.PutUint32(mainData[0:4], uint32(len(xids))) // xcnt
	le.PutUint32(mainData[4:8], 0)                 // subxcnt
	if len(xids) > 0 {
		mainData[8] = 1 // subxid_overflow (conservative; see doc comment)
	}
	le.PutUint32(mainData[12:16], nextXid)
	le.PutUint32(mainData[16:20], oldestRunning)
	le.PutUint32(mainData[20:24], latestCompleted)
	for i, xid := range xids {
		le.PutUint32(mainData[minSizeOfXlRunningXacts+4*i:], xid)
	}
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrStandby, xlogStandbyRunningXacts, 0, body), nil
}

// DecodeXLogClogTruncate parses a PG xl_clog_truncate main-data body into its
// three fields. Used by the initdb clog-recovery scan to re-apply the truncation.
func DecodeXLogClogTruncate(mainData []byte) (pageno int64, oldestXact storage.TransactionID, oldestXactDb uint32, err error) {
	if len(mainData) < 16 {
		err = fmt.Errorf("wal: xl_clog_truncate main-data len %d (want 16)", len(mainData))
		return
	}
	pageno = int64(binary.LittleEndian.Uint64(mainData[0:8]))
	oldestXact = storage.TransactionID(binary.LittleEndian.Uint32(mainData[8:12]))
	oldestXactDb = binary.LittleEndian.Uint32(mainData[12:16])
	return
}
