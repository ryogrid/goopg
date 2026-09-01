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
	// review/260831 XL-68: both regions used to grow from nil, so every WAL
	// record paid a handful of reallocations and then one more copy for the
	// concatenation at the end. The sizes are known up front — a block header
	// is at most 25 bytes (2 id/flags + 2 data length + 5 image header + 12
	// RelFileLocator + 4 block number) and the payload is the images, the block
	// data and the main data — so both are allocated once, and so is the result.
	const maxBlockHeaderBytes = 25
	headerCap := len(blocks)*maxBlockHeaderBytes + 5 // + main-data chunk header
	payloadCap := len(mainData)
	for i := range blocks {
		payloadCap += len(blocks[i].Data)
		payloadCap += fpiEncodedLen(blocks[i].Image)
	}
	header := make([]byte, 0, headerCap)   // block + main-data chunk headers
	payload := make([]byte, 0, payloadCap) // block images/data, then main data

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
			var imgLen, holeOffset uint16
			var bimgInfo byte
			var err error
			payload, imgLen, holeOffset, bimgInfo, err = appendFullPageImage(payload, b.Image)
			if err != nil {
				return nil, err
			}
			header = binary.LittleEndian.AppendUint16(header, imgLen)
			header = binary.LittleEndian.AppendUint16(header, holeOffset)
			header = append(header, bimgInfo)
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

	out := make([]byte, 0, len(header)+len(payload))
	out = append(out, header...)
	out = append(out, payload...)
	return out, nil
}

// appendFullPageImage appends the on-wire image bytes to dst and returns the
// XLogRecordBlockImageHeader fields (image length, hole offset, bimg_info).
// The hole is removed when the page carries a valid standard header, matching
// PostgreSQL's XLogRecordAssemble; otherwise the full page is emitted.
//
// review/260831 XL-68: it used to build the image in a freshly allocated buffer
// that the caller then copied into the payload — a page-sized allocation and
// copy per full-page image, on a path that emits one per record.
// fpiHole reports the free-space hole [lower:upper) that a full-page image
// omits, and whether the page has a usable standard header at all. It is the
// one place that decides what a "hole" is, so fpiEncodedLen and
// appendFullPageImage cannot disagree about the size of what gets written.
func fpiHole(page storage.Page) (lower, upper int, hasHole bool) {
	if len(page) != storage.BlockSize {
		return 0, 0, false
	}
	hdr := storage.MustHeader(page)
	lower, upper = int(hdr.Lower()), int(hdr.Upper())
	if lower >= storage.SizeOfPageHeaderData && upper > lower && upper <= storage.BlockSize {
		return lower, upper, true
	}
	return 0, 0, false
}

// fpiEncodedLen is how many payload bytes img will occupy (0 when there is no
// image), used to size the payload buffer up front.
func fpiEncodedLen(img *FullPageImage) int {
	if img == nil {
		return 0
	}
	if len(img.Page) != storage.BlockSize {
		return 0 // rejected by appendFullPageImage
	}
	if lower, upper, ok := fpiHole(img.Page); ok {
		return storage.BlockSize - (upper - lower)
	}
	return storage.BlockSize
}

func appendFullPageImage(dst []byte, img *FullPageImage) (out []byte, imgLen, holeOffset uint16, bimgInfo byte, err error) {
	page := img.Page
	if len(page) != storage.BlockSize {
		return dst, 0, 0, 0, fmt.Errorf("wal: full-page image is %d bytes, want %d", len(page), storage.BlockSize)
	}
	if img.Apply {
		bimgInfo |= bkpImageApply
	}

	// Valid standard header → remove the free-space hole [pd_lower:pd_upper].
	if lower, upper, ok := fpiHole(page); ok {
		holeLen := upper - lower
		bimgInfo |= bkpImageHasHole
		dst = append(dst, page[:lower]...)
		dst = append(dst, page[upper:]...)
		return dst, uint16(storage.BlockSize - holeLen), uint16(lower), bimgInfo, nil
	}

	// No detectable hole → full page, hole_offset = 0, no BKPIMAGE_HAS_HOLE.
	dst = append(dst, page...)
	return dst, uint16(storage.BlockSize), 0, bimgInfo, nil
}
