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

// EncodeBtreeSplitPG builds a PostgreSQL RM_BTREE split record (XLOG_BTREE_SPLIT_L
// opcode) carrying the post-split left, right, and (non-rightmost) sibling pages
// as full-page images. goopg's split record already holds the exact final page
// bytes, so this reuses the A0 FPI encoder rather than reconstructing PG's
// incremental xl_btree_split main-data: block 0 = left (mutated in place), block
// 1 = right (WILL_INIT — a new block), block 2 = the left sibling's right-link
// update (non-rightmost only). Every block carries BKPIMAGE_APPLY, so replay
// (replayDecodedXLogHeapFPIBlocks via the RmgrBtree default arm) restores the
// images — identical to the native replayBtreeSplit and PG's BLK_RESTORED redo.
// The right block extends when it does not yet exist. No main-data is carried
// (the images are authoritative — a documented deviation from PG's incremental
// xl_btree_split); xl_xid = 0.
func EncodeBtreeSplitPG(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, sibBlk storage.BlockNumber, sibPage storage.Page) ([]byte, error) {
	if len(leftPage) != storage.BlockSize || len(rightPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split left/right page must be %d bytes", storage.BlockSize)
	}
	blocks := []BlockRef{
		{ID: 0, Rel: rel, Block: leftBlk, Image: &FullPageImage{Page: leftPage, Apply: true}},
		{ID: 1, Rel: rel, Block: rightBlk, SameRel: true, WillInit: true, Image: &FullPageImage{Page: rightPage, Apply: true}},
	}
	if sibBlk != storage.InvalidBlockNumber {
		if len(sibPage) != storage.BlockSize {
			return nil, fmt.Errorf("wal: btree-split sibling page must be %d bytes", storage.BlockSize)
		}
		blocks = append(blocks, BlockRef{ID: 2, Rel: rel, Block: sibBlk, SameRel: true, Image: &FullPageImage{Page: sibPage, Apply: true}})
	}
	body, err := assembleXLogRecord(nil, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeSplitL, 0, body), nil
}

// EncodeBtreeVacuumPG builds a PostgreSQL RM_BTREE vacuum record
// (XLOG_BTREE_VACUUM opcode) carrying the post-vacuum leaf page as a full-page
// image (backup block 0). goopg's vacuum pass already holds the exact final page
// bytes (dead items removed, opaque flags updated), so this reuses the A0 FPI
// encoder rather than reconstructing PG's incremental xl_btree_vacuum main-data
// (ndeleted/nupdated + offset arrays). BKPIMAGE_APPLY makes replay
// (replayDecodedXLogHeapFPIBlocks via the RmgrBtree default arm) restore the
// image, identical to the native replayBtreeVacuum and PG's BLK_RESTORED redo.
// No main-data is carried (the image is authoritative — a documented deviation
// from PG's incremental xl_btree_vacuum); xl_xid = 0.
func EncodeBtreeVacuumPG(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) ([]byte, error) {
	if len(page) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-vacuum page must be %d bytes", storage.BlockSize)
	}
	blocks := []BlockRef{{ID: 0, Rel: rel, Block: blk, Image: &FullPageImage{Page: page, Apply: true}}}
	body, err := assembleXLogRecord(nil, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeVacuum, 0, body), nil
}

// EncodeBtreeNewRootPG builds a PostgreSQL RM_BTREE new-root record
// (XLOG_BTREE_NEWROOT opcode) carrying the freshly-built root page (backup block
// 0, WILL_INIT — a new block) and the updated metapage (backup block 2, matching
// PG's block numbering; block 1 = left child is unused because goopg does not
// mutate the old root during root replacement). Both ride as full-page images:
// goopg's createNewRoot holds the exact final bytes for both pages, so this
// reuses the A0 FPI encoder rather than reconstructing PG's xl_btree_newroot
// main-data (rootblk + level, which PG's redo uses to rebuild the metapage).
// BKPIMAGE_APPLY makes replay restore both images via the RmgrBtree default arm
// — matching native replayBtreeNewRoot (which reconstructs the metapage from
// rootblk/level) and PG's BLK_RESTORED redo. No main-data is carried (the images
// are authoritative — a documented deviation from PG's incremental
// xl_btree_newroot); xl_xid = 0.
func EncodeBtreeNewRootPG(rel storage.RelFileNode, rootBlk storage.BlockNumber, rootPage storage.Page, metaBlk storage.BlockNumber, metaPage storage.Page) ([]byte, error) {
	if len(rootPage) != storage.BlockSize || len(metaPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-newroot root/meta page must be %d bytes", storage.BlockSize)
	}
	blocks := []BlockRef{
		{ID: 0, Rel: rel, Block: rootBlk, WillInit: true, Image: &FullPageImage{Page: rootPage, Apply: true}},
		{ID: 2, Rel: rel, Block: metaBlk, SameRel: true, Image: &FullPageImage{Page: metaPage, Apply: true}},
	}
	body, err := assembleXLogRecord(nil, blocks)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrBtree, xlogBtreeNewRoot, 0, body), nil
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
	if spc == 0 {
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
