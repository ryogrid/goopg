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
	xlogStandbyRunningXacts uint8 = 0x10
	// xlogXLogParameterChange is the xl_info opcode for XLOG_PARAMETER_CHANGE
	// (pg_control.h:74). Emitted by the primary when GUC echo fields change;
	// replayed on the standby to update pg_control's GUC echo section.
	xlogXLogParameterChange uint8 = 0x60

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
	return header.Rmid == rmid && header.Info == info && header.XID == xid
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
		if spcOID != 0 && spcOID != pgDefaultTableSpaceOID {
			return xlogBlockMeta{}, 0, storage.RelFileNode{}, false, fmt.Errorf(
				"wal: unsupported PostgreSQL tablespace OID %d locator=%x fork_flags=0x%02x data_len=%d",
				spcOID, src[off:off+sizeOfRelFileLocator], forkFlags, meta.dataLen)
		}
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
