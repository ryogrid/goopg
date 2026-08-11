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

	// XlogXactCommit and XlogXactAbort are exported for the crash-recovery
	// xact-stamp pass in internal/initdb. (M0106-0013)
	XlogXactCommit = xlogXactCommit
	XlogXactAbort  = xlogXactAbort
	// XlogXactOpMask masks the opcode bits from XLogRecord.Info for RmgrXact.
	XlogXactOpMask = xlogXactOpMask
	// XlogClogTruncate is exported for the initdb clog-recovery scan (A9).
	XlogClogTruncate = xlogClogTruncate
	xlogStandbyRunningXacts uint8 = 0x10
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

	// XLOG_HEAP_LOCK (heapam_xlog.h:39), shares RM_HEAP_ID's opmask
	// with xlogHeap{Insert,Delete,Update,HotUpdate,Inplace}. Used by
	// recordKindToRmgrInfo (doc 04 §3.1) to map HeapLock.
	xlogHeapLock uint8 = 0x60

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

	// CLOG_TRUNCATE (clog.h:56), RM_CLOG_ID's (only non-zeropage)
	// opcode. Used by recordKindToRmgrInfo (doc 04 §3.1) to map
	// ClogTruncate.
	xlogClogTruncate uint8 = 0x10

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
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf("wal: compressed PostgreSQL backup block images are not supported yet")
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
		if spcOID != 0 && spcOID != pgDefaultTableSpaceOID && spcOID != pgGlobalTableSpaceOID {
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf(
				"wal: unsupported PostgreSQL tablespace OID %d locator=%x fork_flags=0x%02x data_len=%d",
				spcOID, src[off:off+sizeOfRelFileLocator], forkFlags, meta.dataLen)
		}
		// spcOID is dropped: the shared-vs-per-DB routing is carried by dbOid
		// (0 → global/, via sharedOrPerDBRelDir). A shared catalog's locator is
		// spcOid=1664/dbOid=0; TblOid stays 0 so relDir resolves to global/.
		// B4.1a.
		meta.ref.Rel = storage.RelFileNode{
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
