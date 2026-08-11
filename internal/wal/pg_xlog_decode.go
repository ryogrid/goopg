package wal

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

const (
	xlrMaxBlockID       byte = 32
	xlrBlockIDOrigin    byte = 253
	xlrBlockIDTopLevelX byte = 252

	xlogHeapInsert          uint8 = 0x00 // XLOG_HEAP_INSERT
	xlogHeapDelete          uint8 = 0x10 // XLOG_HEAP_DELETE
	xlogHeapUpdate          uint8 = 0x20 // XLOG_HEAP_UPDATE
	xlogHeapHotUpdate       uint8 = 0x40 // XLOG_HEAP_HOT_UPDATE
	xlogHeapInplace         uint8 = 0x70 // XLOG_HEAP_INPLACE
	xlogHeapOpMask          uint8 = 0x70
	xlogHeapInit            uint8 = 0x80
	xlogXactCommit          uint8 = 0x00
	xlogXactAbort           uint8 = 0x20
	xlogXactOpMask          uint8 = 0x70

	// M0131-S21a: the RM_HEAP_ID opcode goopg never emits but a real PG
	// crash tail carries (heapam_xlog.h:36). Recognised — not implemented:
	// heap_redo's TRUNCATE arm is an explicit no-op upstream ("TRUNCATE is a
	// no-op because the actions are already logged as SMGR WAL records",
	// heapam_xlog.c:1201-1208).
	xlogHeapTruncate uint8 = 0x30 // XLOG_HEAP_TRUNCATE

	// M0131-S21a: RM_XACT_ID's remaining opcodes (xact.h:169-175). ASSIGNMENT
	// and INVALIDATIONS are recovery-bookkeeping records goopg has no consumer
	// for; the three two-phase-commit opcodes are refused loudly rather than
	// dropped (see the RmgrXact arm in recovery.go).
	xlogXactPrepare        uint8 = 0x10 // XLOG_XACT_PREPARE
	xlogXactCommitPrepared uint8 = 0x30 // XLOG_XACT_COMMIT_PREPARED
	xlogXactAbortPrepared  uint8 = 0x40 // XLOG_XACT_ABORT_PREPARED
	xlogXactAssignment     uint8 = 0x50 // XLOG_XACT_ASSIGNMENT
	xlogXactInvalidations  uint8 = 0x60 // XLOG_XACT_INVALIDATIONS

	// XlogXactCommit and XlogXactAbort are exported for the crash-recovery
	// xact-stamp pass in internal/initdb. (M0106-0013)
	XlogXactCommit = xlogXactCommit
	XlogXactAbort  = xlogXactAbort
	// XlogXactOpMask masks the opcode bits from XLogRecord.Info for RmgrXact.
	XlogXactOpMask = xlogXactOpMask
	// XlogClogTruncate is exported for the initdb clog-recovery scan (A9).
	XlogClogTruncate = xlogClogTruncate
	xlogStandbyRunningXacts uint8 = 0x10
	// M0131-S21a: RM_STANDBY_ID's other two opcodes (standbydefs.h:34-36).
	// Both are hot-standby-only upstream — standby_redo returns immediately
	// when standbyState == STANDBY_DISABLED, which is always true during a
	// crash-recovery start (standby.c:1170-1172).
	xlogStandbyLock          uint8 = 0x00 // XLOG_STANDBY_LOCK
	xlogStandbyInvalidations uint8 = 0x20 // XLOG_INVALIDATIONS
	// xlogXLogParameterChange is the xl_info opcode for XLOG_PARAMETER_CHANGE
	// (pg_control.h:74). Emitted by the primary when GUC echo fields change;
	// replayed on the standby to update pg_control's GUC echo section.
	xlogXLogParameterChange uint8 = 0x60

	// RM_HEAP2_ID (rmid 9) opcodes (heapam_xlog.h:59-66). Used by
	// recordKindToRmgrInfo (doc 04 §3) to map HeapVacuum/HeapPruneOpt/
	// HeapFreeze onto distinct real-PG HEAP2 prune-record subtypes.
	xlogHeap2PruneOnAccess    uint8 = 0x10 // XLOG_HEAP2_PRUNE_ON_ACCESS
	xlogHeap2PruneVacuumScan  uint8 = 0x20 // XLOG_HEAP2_PRUNE_VACUUM_SCAN
	xlogHeap2PruneVacuumClean uint8 = 0x30 // XLOG_HEAP2_PRUNE_VACUUM_CLEANUP

	// M0131-S21a: the RM_HEAP2_ID opcodes goopg never emits
	// (heapam_xlog.h:59-66). NEW_CID is logical-decoding-only and a physical
	// no-op; the rest are real page mutations still awaiting redo (S21a-2).
	xlogHeap2NewCid      uint8 = 0x70 // XLOG_HEAP2_NEW_CID
	xlogHeap2MultiInsert uint8 = 0x50 // XLOG_HEAP2_MULTI_INSERT

	// XLOG_HEAP2_VISIBLE (M0131-S21a-2 part 3): every VACUUM that marks a page
	// all-visible emits one, and so does an INSERT that freezes a page it
	// filled itself. It is the FIRST record whose redo writes the
	// visibility-map fork rather than the main fork.
	xlogHeap2Visible uint8 = 0x40

	// XLOG_HEAP2_LOCK_UPDATED (M0131-S21a-2 part 4, heapam_xlog.h:65): the
	// near-sibling of XLOG_HEAP_LOCK, emitted when a tuple-lock request
	// (SELECT ... FOR UPDATE/SHARE, a FK RI check, an UPDATE about to
	// rewrite a row) discovers the row was already updated by a concurrent
	// still-live transaction — the locker re-locks the *newest visible
	// version* of the row via heap_lock_updated_tuple_rec, which is on RM_HEAP2
	// because it can chain across multiple row versions.
	xlogHeap2LockUpdated uint8 = 0x60

	// xlogVisibilitymapXLogCatalogRel is VISIBILITYMAP_XLOG_CATALOG_REL
	// (visibilitymapdefs.h:31): an xl_heap_visible flags bit that exists only
	// on the wire, telling a hot standby that the relation is catalog-ish when
	// resolving snapshot conflicts. It must be masked off before the flags
	// reach the map page (visibilitymapdefs.h:29).
	xlogVisibilitymapXLogCatalogRel uint8 = 0x04

	// XLH_INSERT_* flag bits carried in xl_heap_insert/xl_heap_multi_insert's
	// `flags` byte (heapam_xlog.h:72-79). Redo consults only the two
	// visibility-map bits: ALL_VISIBLE_CLEARED clears PD_ALL_VISIBLE on the
	// heap page, ALL_FROZEN_SET sets it (heapam_xlog.c, heap_xlog_multi_insert).
	// The remaining bits (LAST_IN_MULTI, IS_SPECULATIVE, CONTAINS_NEW_TUPLE,
	// ON_TOAST_RELATION) exist for logical decoding and speculative-insert
	// bookkeeping and have no physical effect.
	xlogHeapInsertAllVisibleCleared uint8 = 1 << 0
	xlogHeapInsertAllFrozenSet      uint8 = 1 << 5

	// XLOG_HEAP_LOCK (heapam_xlog.h:39), shares RM_HEAP_ID's opmask
	// with xlogHeap{Insert,Delete,Update,HotUpdate,Inplace}. Used by
	// recordKindToRmgrInfo (doc 04 §3.1) to map HeapLock.
	xlogHeapLock uint8 = 0x60

	// XLOG_HEAP_CONFIRM (heapam_xlog.h:38) completes a speculative insert
	// (INSERT .. ON CONFLICT): the inserter first writes the tuple with a
	// speculative token in t_ctid, then confirms it. goopg never emits one —
	// its upsert path takes the row lock before inserting — but every PG
	// ON CONFLICT statement produces one, so a PG tail needs the redo.
	xlogHeapConfirm uint8 = 0x50

	// XLHL_* are the bits of xl_heap_lock.infobits_set (heapam_xlog.h:386-390),
	// the wire encoding of the infomask/infomask2 bits redo must restore. The
	// translation to page bits is upstream's fix_infomask_from_infobits.
	xlhlXmaxIsMulti    uint8 = 0x01
	xlhlXmaxLockOnly   uint8 = 0x02
	xlhlXmaxExclLock   uint8 = 0x04
	xlhlXmaxKeyShrLock uint8 = 0x08
	xlhlKeysUpdated    uint8 = 0x10

	// XLH_LOCK_ALL_FROZEN_CLEARED (heapam_xlog.h:393): the locker had to clear
	// the block's visibility-map ALL_FROZEN bit.
	xlhLockAllFrozenCleared uint8 = 0x01

	// RM_BTREE_ID (rmid 11) opcodes (nbtxlog.h:27-39). Used by
	// recordKindToRmgrInfo (doc 04 §3.1) to map
	// BtreeInsert/BtreeSplit/BtreeVacuum/BtreeUnlinkPage/BtreeNewRoot/
	// BtreeMarkPageHalfDead onto their real-PG opcodes.
	xlogBtreeInsertLeaf       uint8 = 0x00 // XLOG_BTREE_INSERT_LEAF
	xlogBtreeSplitL           uint8 = 0x30 // XLOG_BTREE_SPLIT_L
	xlogBtreeSplitR           uint8 = 0x40 // XLOG_BTREE_SPLIT_R
	xlogBtreeUnlinkPage       uint8 = 0x80 // XLOG_BTREE_UNLINK_PAGE
	xlogBtreeUnlinkPageMeta   uint8 = 0x90 // XLOG_BTREE_UNLINK_PAGE_META
	xlogBtreeNewRoot          uint8 = 0xA0 // XLOG_BTREE_NEWROOT
	xlogBtreeMarkPageHalfDead uint8 = 0xB0 // XLOG_BTREE_MARK_PAGE_HALFDEAD
	xlogBtreeVacuum           uint8 = 0xC0 // XLOG_BTREE_VACUUM

	// XLOG_SMGR_CREATE (storage_xlog.h:30). Used by recordKindToRmgrInfo
	// (doc 04 §3.1) to map SmgrCreate onto RM_SMGR_ID.
	xlogSmgrCreate uint8 = 0x10

	// RM_TBLSPC_ID info codes (commands/tablespace.h). B4.1d.
	xlogTblspcCreate uint8 = 0x00 // XLOG_TBLSPC_CREATE
	xlogTblspcDrop   uint8 = 0x10 // XLOG_TBLSPC_DROP

	// RM_DBASE_ID info codes (commands/dbcommands_xlog.h). B4.6 Stage 3.
	xlogDbaseCreateFileCopy uint8 = 0x00 // XLOG_DBASE_CREATE_FILE_COPY
	xlogDbaseCreateWalLog   uint8 = 0x10 // XLOG_DBASE_CREATE_WAL_LOG
	xlogDbaseDrop           uint8 = 0x20 // XLOG_DBASE_DROP

	// XLOG_FPI (pg_control.h:79), RM_XLOG_ID's full-page-image opcode.
	// Used by recordKindToRmgrInfo (doc 04 §3.1) to map PageImage.
	xlogXLogFPI uint8 = 0xB0

	// M0131-S16.4: the REST of RM_XLOG_ID's opcode space
	// (pg_control.h:68-82). Named so replayDecodedXLogRecord can enumerate
	// the benign no-ops explicitly and refuse anything outside the list,
	// instead of collapsing the whole space into one silent `default:`.
	// XLOG_NEXTOID is deliberately absent from the benign set — it carries
	// real state and is applied by the initdb OID-recovery scan (S21a).
	xlogXLogCheckpointShutdown  uint8 = 0x00 // XLOG_CHECKPOINT_SHUTDOWN
	xlogXLogCheckpointOnline    uint8 = 0x10 // XLOG_CHECKPOINT_ONLINE
	xlogXLogNoop                uint8 = 0x20 // XLOG_NOOP
	xlogXLogNextOid             uint8 = 0x30 // XLOG_NEXTOID (NOT benign)
	xlogXLogSwitch              uint8 = 0x40 // XLOG_SWITCH
	xlogXLogBackupEnd           uint8 = 0x50 // XLOG_BACKUP_END
	xlogXLogRestorePoint        uint8 = 0x70 // XLOG_RESTORE_POINT
	xlogXLogFPWChange           uint8 = 0x80 // XLOG_FPW_CHANGE
	xlogXLogEndOfRecovery       uint8 = 0x90 // XLOG_END_OF_RECOVERY
	xlogXLogFPIForHint          uint8 = 0xA0 // XLOG_FPI_FOR_HINT
	xlogXLogOverwriteContrecord uint8 = 0xD0 // XLOG_OVERWRITE_CONTRECORD
	xlogXLogCheckpointRedo      uint8 = 0xE0 // XLOG_CHECKPOINT_REDO

	// XlogXLogNextOid is exported for the initdb OID-recovery scan (S21a):
	// the physical replay pass has no catalog handle, so — exactly like
	// CLOG_TRUNCATE — the record is recognised in replayDecodedXLogRecord and
	// re-applied by a second pass in internal/initdb.
	XlogXLogNextOid = xlogXLogNextOid

	// CLOG_TRUNCATE (clog.h:56), RM_CLOG_ID's (only non-zeropage)
	// opcode. Used by recordKindToRmgrInfo (doc 04 §3.1) to map
	// ClogTruncate.
	xlogClogTruncate uint8 = 0x10

	// CLOG_ZEROPAGE (clog.h:55), RM_CLOG_ID's other opcode: WriteZeroPageXlogRec
	// (clog.c:1073-1078) fires once per 32768 XIDs, right before ExtendCLOG
	// hands out the first XID of a fresh page. M0131-S21a-2 part 5.
	xlogClogZeroPage uint8 = 0x00

	bkpBlockForkMask byte = 0x0F
	bkpBlockHasImage byte = 0x10
	bkpBlockHasData  byte = 0x20
	bkpBlockWillInit byte = 0x40
	bkpBlockSameRel  byte = 0x80

	bkpImageHasHole    byte = 0x01
	bkpImageApply      byte = 0x02
	bkpImageCompressMS byte = 0x1C

	sizeOfXLogRecordBlockHeader       = 4
	sizeOfXLogRecordBlockImageHeader  = 5
	sizeOfXLogRecordBlockCompressHead = 2
	sizeOfRelFileLocator              = 12
	sizeOfXLogHeapInsertData          = 3
	sizeOfXLogHeapHeaderData          = 5
	// sizeOfXLogHeapMultiInsertData is SizeOfHeapMultiInsert —
	// offsetof(xl_heap_multi_insert, offsets) (heapam_xlog.h:188). The struct is
	// {uint8 flags; uint16 ntuples; OffsetNumber offsets[]}, so C's alignment
	// puts ntuples at byte 2 and the offsets array at byte 4.
	sizeOfXLogHeapMultiInsertData = 4
	// sizeOfXLogMultiInsertTuple is SizeOfMultiInsertTuple —
	// offsetof(xl_multi_insert_tuple, t_hoff) + sizeof(uint8)
	// (heapam_xlog.h:199): {uint16 datalen; uint16 t_infomask2;
	// uint16 t_infomask; uint8 t_hoff} = 7 bytes, tuple data following.
	sizeOfXLogMultiInsertTuple = 7

	pgDefaultTableSpaceOID uint32 = 1663
	// pgGlobalTableSpaceOID is GLOBALTABLESPACE_OID: the tablespace of the
	// cluster-wide SHARED catalogs (pg_database, pg_authid, pg_tablespace,
	// pg_shdepend, …), whose files live under global/. goopg encodes these
	// with the DBOid==0 sentinel (sharedOrPerDBRelDir → "global"); on the WAL
	// wire a shared relation's RelFileLocator carries spcOid=1664/dbOid=0 so a
	// real PostgreSQL standby routes the replayed block to its own global/.
	// B4.1a.
	pgGlobalTableSpaceOID uint32 = 1664
)

// XLogBlockRef is one block reference carried inside a decoded
// PG-compatible XLogRecord.
type XLogBlockRef struct {
	ID         byte
	Rel        storage.RelFileNode
	Block      storage.BlockNumber
	HasImage   bool
	ImageApply bool
	WillInit   bool
	Image      storage.Page
	Data       []byte
}

// XLogDecodedRecord is the structured form of a PG-compatible
// XLogRecord after its block references and main-data chunks have
// been decoded.
type XLogDecodedRecord struct {
	Header       XLogRecord
	RecordOrigin uint16
	TopLevelXID  uint32
	MainData     []byte
	Blocks       []XLogBlockRef
}

type decodedXLogRecord struct {
	Header   XLogRecord
	Payload  []byte
	XLog     *XLogDecodedRecord
	Consumed int
}

type xlogBlockMeta struct {
	ref        XLogBlockRef
	dataLen    int
	imgLen     int
	holeOffset int
	bimgInfo   byte
}

func decodeRecordXLogDetailed(stream []byte) (decodedXLogRecord, error) {
	if len(stream) < xlogRecordHeaderSize {
		return decodedXLogRecord{}, fmt.Errorf("%w: truncated xlog record header", ErrCorruptRecord)
	}
	header, err := DecodeXLogRecordHeader(stream[:xlogRecordHeaderSize])
	if err != nil {
		return decodedXLogRecord{}, err
	}
	total := int(header.TotLen)
	if total < xlogRecordHeaderSize || total > len(stream) {
		return decodedXLogRecord{}, fmt.Errorf("%w: bad xlog total length %d", ErrCorruptRecord, total)
	}
	wrapped := stream[xlogRecordHeaderSize:total]
	if err := VerifyXLogRecordCRC(stream[:xlogRecordHeaderSize], wrapped, header.CRC); err != nil {
		return decodedXLogRecord{}, err
	}
	decoded, err := parseXLogRecordData(header, wrapped)
	if err != nil {
		return decodedXLogRecord{}, err
	}
	decoded.Consumed = maxAlignXLog(total)
	if decoded.Consumed > len(stream) {
		decoded.Consumed = len(stream)
	}
	return decoded, nil
}

func parseXLogRecordData(header XLogRecord, wrapped []byte) (decodedXLogRecord, error) {
	decoded := decodedXLogRecord{Header: header}
	xlogRecord := &XLogDecodedRecord{Header: header}
	var (
		off         int
		lastRel     storage.RelFileNode
		haveRel     bool
		mainDataLen int
		blocks      []xlogBlockMeta
	)
	datatotal := 0
headerLoop:
	for len(wrapped)-off > datatotal {
		switch id := wrapped[off]; {
		case id <= xlrMaxBlockID:
			blk, n, rel, ok, err := decodeXLogBlockRefHeader(wrapped[off:], lastRel, haveRel)
			if err != nil {
				return decodedXLogRecord{}, err
			}
			blocks = append(blocks, blk)
			off += n
			lastRel = rel
			haveRel = ok
			datatotal += blk.dataLen + blk.imgLen
		case id == xlrBlockIDDataShort:
			if mainDataLen != 0 {
				return decodedXLogRecord{}, fmt.Errorf("%w: duplicate main-data chunk", ErrCorruptRecord)
			}
			if off+2 > len(wrapped) {
				return decodedXLogRecord{}, fmt.Errorf("%w: truncated short xlog data header", ErrCorruptRecord)
			}
			n := int(wrapped[off+1])
			mainDataLen = n
			datatotal += n
			off += 2
			break headerLoop
		case id == xlrBlockIDDataLong:
			if mainDataLen != 0 {
				return decodedXLogRecord{}, fmt.Errorf("%w: duplicate main-data chunk", ErrCorruptRecord)
			}
			if off+5 > len(wrapped) {
				return decodedXLogRecord{}, fmt.Errorf("%w: truncated long xlog data header", ErrCorruptRecord)
			}
			n := int(binary.LittleEndian.Uint32(wrapped[off+1 : off+5]))
			mainDataLen = n
			datatotal += n
			off += 5
			break headerLoop
		case id == xlrBlockIDOrigin:
			if off+3 > len(wrapped) {
				return decodedXLogRecord{}, fmt.Errorf("%w: truncated origin chunk", ErrCorruptRecord)
			}
			xlogRecord.RecordOrigin = binary.LittleEndian.Uint16(wrapped[off+1 : off+3])
			off += 3
		case id == xlrBlockIDTopLevelX:
			if off+5 > len(wrapped) {
				return decodedXLogRecord{}, fmt.Errorf("%w: truncated top-level xid chunk", ErrCorruptRecord)
			}
			xlogRecord.TopLevelXID = binary.LittleEndian.Uint32(wrapped[off+1 : off+5])
			off += 5
		default:
			return decodedXLogRecord{}, fmt.Errorf("%w: unsupported xlog chunk tag 0x%02x", ErrCorruptRecord, id)
		}
	}
	if len(wrapped)-off != datatotal {
		return decodedXLogRecord{}, fmt.Errorf("%w: xlog data section size mismatch headers=%d payload=%d expected=%d", ErrCorruptRecord, off, len(wrapped)-off, datatotal)
	}
	payloadOff := off
	xlogRecord.Blocks = make([]XLogBlockRef, 0, len(blocks))
	for _, blk := range blocks {
		if blk.imgLen > 0 {
			if payloadOff+blk.imgLen > len(wrapped) {
				return decodedXLogRecord{}, fmt.Errorf("%w: truncated block image payload", ErrCorruptRecord)
			}
			img, err := decodeXLogBlockImage(wrapped[payloadOff:payloadOff+blk.imgLen], blk.holeOffset, blk.imgLen, blk.bimgInfo)
			if err != nil {
				return decodedXLogRecord{}, err
			}
			blk.ref.HasImage = true
			blk.ref.ImageApply = blk.bimgInfo&bkpImageApply != 0
			blk.ref.Image = img
			payloadOff += blk.imgLen
		}
		if blk.dataLen > 0 {
			if payloadOff+blk.dataLen > len(wrapped) {
				return decodedXLogRecord{}, fmt.Errorf("%w: truncated block data", ErrCorruptRecord)
			}
			blk.ref.Data = cloneXLogBytes(wrapped[payloadOff : payloadOff+blk.dataLen])
			payloadOff += blk.dataLen
		}
		xlogRecord.Blocks = append(xlogRecord.Blocks, blk.ref)
	}
	if mainDataLen > 0 {
		if payloadOff+mainDataLen > len(wrapped) {
			return decodedXLogRecord{}, fmt.Errorf("%w: truncated main-data payload", ErrCorruptRecord)
		}
		xlogRecord.MainData = cloneXLogBytes(wrapped[payloadOff : payloadOff+mainDataLen])
		payloadOff += mainDataLen
	}
	if payloadOff != len(wrapped) {
		return decodedXLogRecord{}, fmt.Errorf("%w: trailing xlog payload bytes %d", ErrCorruptRecord, len(wrapped)-payloadOff)
	}
	decoded.XLog = xlogRecord
	if len(xlogRecord.Blocks) == 0 && xlogRecord.RecordOrigin == 0 && xlogRecord.TopLevelXID == 0 && nativeHeaderMatchesMainData(header, xlogRecord.MainData) {
		decoded.Payload = xlogRecord.MainData
		return decoded, nil
	}
	return decoded, nil
}

func nativeHeaderMatchesMainData(header XLogRecord, mainData []byte) bool {
	if len(mainData) == 0 {
		return false
	}
	rmid, info, xid := classifyXLogRecord(mainData)
	if header.Rmid != rmid || header.Info != info || header.XID != xid {
		return false
	}
	// A9: a genuine native record's main-data has the fixed on-wire size
	// registered for its RecordKind. A PG-format record built via
	// framePGAssembled whose body happens to classify to the same
	// (rmid, info, xid=0) — e.g. an xl_clog_truncate / xl_smgr_create whose
	// leading byte collides with a native RecordKind — has a different length,
	// so reject it here and let it route to the decoded replay path instead of
	// the native payload[0] switch. (smgr-create additionally carries a real
	// xid so it already fails the check above; this guard covers the xid=0
	// clog-truncate case and is belt-and-suspenders for a bootstrap smgr-create
	// in a colliding tablespace.)
	if size, ok := nativeFixedRecordSize(mainData[0]); ok && len(mainData) != size {
		return false
	}
	return true
}

// nativeFixedRecordSize returns the fixed on-wire body size of a native record
// for the RecordKinds whose PG-format twin is a same-classified main-data-only
// record (A9 collision disambiguation). Only these kinds need the guard; every
// other RecordKind keeps the length-agnostic classify match.
func nativeFixedRecordSize(kind byte) (int, bool) {
	switch kind {
	case RecordKindSmgrCreate:
		return smgrRecordSize, true
	case RecordKindClogTruncate:
		return xactRecordSize, true
	}
	return 0, false
}

func decodeXLogBlockRefHeader(src []byte, lastRel storage.RelFileNode, haveRel bool) (xlogBlockMeta, int, storage.RelFileNode, bool, error) {
	if len(src) < sizeOfXLogRecordBlockHeader {
		return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("%w: truncated block header", ErrCorruptRecord)
	}
	meta := xlogBlockMeta{ref: XLogBlockRef{ID: src[0]}}
	forkFlags := src[1]
	meta.dataLen = int(binary.LittleEndian.Uint16(src[2:4]))
	off := sizeOfXLogRecordBlockHeader
	if forkFlags&bkpBlockHasImage != 0 {
		if off+sizeOfXLogRecordBlockImageHeader > len(src) {
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("%w: truncated block image header", ErrCorruptRecord)
		}
		meta.imgLen = int(binary.LittleEndian.Uint16(src[off : off+2]))
		meta.holeOffset = int(binary.LittleEndian.Uint16(src[off+2 : off+4]))
		meta.bimgInfo = src[off+4]
		off += sizeOfXLogRecordBlockImageHeader
		if meta.bimgInfo&bkpImageCompressMS != 0 {
			if off+sizeOfXLogRecordBlockCompressHead > len(src) {
				return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("%w: truncated block image compression header", ErrCorruptRecord)
			}
			// M0131-S16.5: wrapped in ErrUnsupportedRecord so the reader
			// surfaces it to the caller instead of absorbing it as a
			// clean end-of-WAL — these bytes are a real, durable record.
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf(
				"%w: compressed PostgreSQL backup block images are not supported yet", ErrUnsupportedRecord)
		}
	}
	if forkFlags&bkpBlockSameRel != 0 {
		if !haveRel {
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("%w: SAME_REL without previous locator", ErrCorruptRecord)
		}
		meta.ref.Rel = lastRel
	} else {
		if off+sizeOfRelFileLocator > len(src) {
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("%w: truncated relfilelocator", ErrCorruptRecord)
		}
		spcOID := binary.LittleEndian.Uint32(src[off : off+4])
		// For the two built-in tablespaces spcOID is dropped: the
		// shared-vs-per-DB routing is carried by dbOid (0 → global/, via
		// sharedOrPerDBRelDir). A shared catalog's locator is
		// spcOid=1664/dbOid=0; TblOid stays 0 so relDir resolves to global/.
		// B4.1a.
		//
		// M0131-S16.2: a USER tablespace OID used to be rejected here, and the
		// reader then reported that rejection as a clean end-of-WAL — so every
		// record behind the first pg_tblspc-resident relation was dropped on
		// goopg's OWN restart path (exposed by
		// TestIndexTablespaceSurvivesRestartViaCatalogHeap the moment the
		// reader stopped swallowing the error). Carry the OID into TblOid
		// instead: storage.relDir already routes a non-zero TblOid through
		// pg_tblspc/<TblOid>/<version dir>/<dbOid> (smgr.go:624-636,
		// M0122-0007), which is exactly where the emitter put the file.
		tblOID := spcOID
		if spcOID == pgDefaultTableSpaceOID || spcOID == pgGlobalTableSpaceOID {
			tblOID = 0
		}
		meta.ref.Rel = storage.RelFileNode{
			TblOid: tblOID,
			DBOid:  binary.LittleEndian.Uint32(src[off+4 : off+8]),
			RelOid: binary.LittleEndian.Uint32(src[off+8 : off+12]),
		}
		off += sizeOfRelFileLocator
	}
	meta.ref.Rel.Fork = storage.ForkNumber(forkFlags & bkpBlockForkMask)
	if off+4 > len(src) {
		return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("%w: truncated block number", ErrCorruptRecord)
	}
	meta.ref.Block = storage.BlockNumber(binary.LittleEndian.Uint32(src[off : off+4]))
	off += 4
	meta.ref.WillInit = forkFlags&bkpBlockWillInit != 0
	return meta, off, meta.ref.Rel, true, nil
}

func decodeXLogBlockImage(src []byte, holeOffset, imgLen int, bimgInfo byte) (storage.Page, error) {
	page := make(storage.Page, storage.BlockSize)
	if bimgInfo&bkpImageHasHole != 0 {
		holeLen := storage.BlockSize - imgLen
		if holeOffset < 0 || holeOffset > storage.BlockSize || holeLen < 0 || holeOffset+holeLen > storage.BlockSize {
			return nil, fmt.Errorf("%w: invalid backup-block hole off=%d len=%d", ErrCorruptRecord, holeOffset, holeLen)
		}
		prefix := holeOffset
		suffix := storage.BlockSize - holeOffset - holeLen
		if prefix+suffix != len(src) {
			return nil, fmt.Errorf("%w: backup-block image len=%d does not match hole layout", ErrCorruptRecord, len(src))
		}
		copy(page[:prefix], src[:prefix])
		copy(page[holeOffset+holeLen:], src[prefix:])
		return page, nil
	}
	if len(src) != storage.BlockSize {
		return nil, fmt.Errorf("%w: backup-block image len=%d, want %d", ErrCorruptRecord, len(src), storage.BlockSize)
	}
	copy(page, src)
	return page, nil
}

func cloneXLogBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	out := make([]byte, len(src))
	copy(out, src)
	return out
}
