package xlog

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// xlog_assemble.go builds the body of a PostgreSQL-compatible XLogRecord —
// the bytes that follow the 24-byte XLogRecord header. This is the encode-side
// counterpart of the faithful decoder in pg_xlog_decode.go, and the keystone
// for record-content parity (docs/design/wal-pg-identical-stream/01-record-content-parity.md §2):
// every Section-A record is rewritten to construct real block references + full-page
// images here instead of wrapping a goopg-native payload as one main-data chunk.
//
// The on-wire layout mirrors PostgreSQL's XLogRecordAssemble
// (postgres/src/backend/access/transam/xloginsert.c) and doc 03 §1.3:
//
//	header region:  per block (ascending id) —
//	                  XLogRecordBlockHeader{id, fork_flags, data_length}
//	                  (if HAS_IMAGE)  XLogRecordBlockImageHeader{length, hole_offset, bimg_info}
//	                  (if !SAME_REL)  RelFileLocator{spc,db,rel} + BlockNumber
//	                then (if main data) the main-data chunk header
//	                  (XLR_BLOCK_ID_DATA_SHORT/LONG + length)
//	payload region: per block (ascending id) — image bytes (hole removed), then block data;
//	                then the main-data bytes.
//
// This is validated round-trip against parseXLogRecordData/decodeXLogBlockRefHeader
// (xlog_assemble_test.go) and, once records are flipped, against pg_waldump.

// BlockRef is one block reference to embed in an assembled XLogRecord.
//
// Fork flags (BKPBLOCK_*) are derived from the contents, not set by the caller:
// HAS_IMAGE iff Image != nil, HAS_DATA iff len(Data) > 0, plus WillInit / SameRel.
type BlockRef struct {
	// ID is the block reference id (0..xlrMaxBlockID). PostgreSQL emits
	// references in ascending id order; SameRel back-references the rel of
	// the immediately preceding reference.
	ID uint8
	// Rel identifies the relation file. A shared catalog (Rel.DBOid == 0)
	// encodes as the global tablespace OID (1664); otherwise Rel.TblOid == 0
	// encodes as the pg_default tablespace OID (1663), matching PostgreSQL's
	// RelFileLocator. B4.1a.
	Rel storage.RelFileNode
	// Block is the block number within Rel's fork.
	Block storage.BlockNumber
	// Data is the rmgr-specific block data (BKPBLOCK_HAS_DATA). Must fit uint16.
	Data []byte
	// Image, when non-nil, attaches a full-page image (BKPBLOCK_HAS_IMAGE).
	Image *FullPageImage
	// WillInit marks the block as re-initialized from the record
	// (BKPBLOCK_WILL_INIT) so redo need not read the prior page.
	WillInit bool
	// SameRel omits the RelFileLocator and reuses the preceding reference's
	// rel (BKPBLOCK_SAME_REL). Invalid on the first reference.
	SameRel bool
}

// FullPageImage is a full-page image to embed in a block reference. The
// free-space "hole" (page[pd_lower:pd_upper]) is detected from the standard
// page header and removed on the wire, matching PostgreSQL with the default
// wal_compression=off (byte-identical, and smaller than a full 8 KiB image).
type FullPageImage struct {
	// Page is the raw block image; must be exactly storage.BlockSize bytes.
	Page storage.Page
	// Apply requests BKPIMAGE_APPLY (redo restores the image unconditionally).
	Apply bool
}

// assembleXLogRecord builds the body (post-header bytes) of a PG-compatible
// XLogRecord from optional main data and zero or more block references. The
// owning transaction id is a header field (XLogRecord.XID) and is stamped by
// the caller, not carried in the body.
func assembleXLogRecord(mainData []byte, blocks []BlockRef) ([]byte, error) {
	var header []byte // block + main-data chunk headers
	var payload []byte // block images/data, then main data

	for i := range blocks {
		b := &blocks[i]
		if b.ID > xlrMaxBlockID {
			return nil, fmt.Errorf("wal: block ref id %d exceeds max %d", b.ID, xlrMaxBlockID)
		}
		if len(b.Data) > 0xFFFF {
			return nil, fmt.Errorf("wal: block data %d exceeds uint16", len(b.Data))
		}
		if b.SameRel && i == 0 {
			return nil, fmt.Errorf("wal: first block ref cannot be SAME_REL")
		}

		forkFlags := byte(b.Rel.Fork) & bkpBlockForkMask
		if len(b.Data) > 0 {
			forkFlags |= bkpBlockHasData
		}
		if b.Image != nil {
			forkFlags |= bkpBlockHasImage
		}
		if b.WillInit {
			forkFlags |= bkpBlockWillInit
		}
		if b.SameRel {
			forkFlags |= bkpBlockSameRel
		}

		// XLogRecordBlockHeader: id, fork_flags, data_length.
		header = append(header, b.ID, forkFlags)
		header = binary.LittleEndian.AppendUint16(header, uint16(len(b.Data)))

		// XLogRecordBlockImageHeader + the image bytes (into payload).
		if b.Image != nil {
			imgBytes, imgLen, holeOffset, bimgInfo, err := encodeFullPageImage(b.Image)
			if err != nil {
				return nil, err
			}
			header = binary.LittleEndian.AppendUint16(header, imgLen)
			header = binary.LittleEndian.AppendUint16(header, holeOffset)
			header = append(header, bimgInfo)
			payload = append(payload, imgBytes...)
		}

		// RelFileLocator (unless SAME_REL) + BlockNumber.
		if !b.SameRel {
			spc := b.Rel.TblOid
			switch {
			case b.Rel.DBOid == 0:
				// Shared catalog in global/ — PG's RelFileLocator carries
				// spcOid=1664/dbOid=0 (B4.1a).
				spc = pgGlobalTableSpaceOID
			case spc == 0:
				spc = pgDefaultTableSpaceOID
			}
			header = binary.LittleEndian.AppendUint32(header, spc)
			header = binary.LittleEndian.AppendUint32(header, b.Rel.DBOid)
			header = binary.LittleEndian.AppendUint32(header, b.Rel.RelOid)
		}
		header = binary.LittleEndian.AppendUint32(header, uint32(b.Block))

		// Block data follows in the payload region, after this block's image.
		if len(b.Data) > 0 {
			payload = append(payload, b.Data...)
		}
	}

	// Main-data chunk header goes last in the header region (the decoder
	// breaks its scan loop on it); the main-data bytes go last in the payload.
	if len(mainData) > 0 {
		if len(mainData) <= 0xFF {
			header = append(header, xlrBlockIDDataShort, byte(len(mainData)))
		} else {
			header = append(header, xlrBlockIDDataLong)
			header = binary.LittleEndian.AppendUint32(header, uint32(len(mainData)))
		}
		payload = append(payload, mainData...)
	}

	return append(header, payload...), nil
}

// encodeFullPageImage returns the on-wire image bytes plus the
// XLogRecordBlockImageHeader fields (image length, hole offset, bimg_info).
// The hole is removed when the page carries a valid standard header, matching
// PostgreSQL's XLogRecordAssemble; otherwise the full page is emitted.
func encodeFullPageImage(img *FullPageImage) (imgBytes []byte, imgLen, holeOffset uint16, bimgInfo byte, err error) {
	page := img.Page
	if len(page) != storage.BlockSize {
		return nil, 0, 0, 0, fmt.Errorf("wal: full-page image is %d bytes, want %d", len(page), storage.BlockSize)
	}
	if img.Apply {
		bimgInfo |= bkpImageApply
	}

	hdr := storage.MustHeader(page)
	lower := int(hdr.Lower())
	upper := int(hdr.Upper())
	// Valid standard header → remove the free-space hole [pd_lower:pd_upper].
	if lower >= storage.SizeOfPageHeaderData && upper > lower && upper <= storage.BlockSize {
		holeLen := upper - lower
		bimgInfo |= bkpImageHasHole
		out := make([]byte, 0, storage.BlockSize-holeLen)
		out = append(out, page[:lower]...)
		out = append(out, page[upper:]...)
		return out, uint16(storage.BlockSize - holeLen), uint16(lower), bimgInfo, nil
	}

	// No detectable hole → full page, hole_offset = 0, no BKPIMAGE_HAS_HOLE.
	out := make([]byte, storage.BlockSize)
	copy(out, page)
	return out, uint16(storage.BlockSize), 0, bimgInfo, nil
}
