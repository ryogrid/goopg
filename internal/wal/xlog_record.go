package wal

// PostgreSQL-compatible XLogRecord header types and helpers
// (M0014-0002 step 1). Mirrors postgres/src/include/access/xlogrecord.h.
//
// This slice is purely additive: the writer's Append path doesn't
// consume these helpers yet. The actual writer/reader switchover for
// M0014-0001 (per-page headers) and M0014-0002 (XLogRecord frames)
// lands together in a later loop — a half-migrated segment file
// (new pages, legacy records) would be neither upstream-compatible
// nor decodable by goopg's legacy reader.
//
// See docs/design/0014-0002-xlogrecord-header-and-rmgr-mapping.md.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// SizeOfXLogRecord is the on-disk size of the XLogRecord header
// (4 + 4 + 8 + 1 + 1 + 2 + 4 = 24 bytes). Matches upstream's
// `offsetof(XLogRecord, xl_crc) + sizeof(pg_crc32c)`.
const SizeOfXLogRecord = 24

// xl_info flag bit conventions (postgres/src/include/access/xlogrecord.h).
const (
	// XLRInfoMask covers the low 4 bits — set by the WAL framework
	// (XLogInsert), not the rmgr.
	XLRInfoMask uint8 = 0x0F
	// XLRRmgrInfoMask covers the high 4 bits — free for rmgr use
	// (e.g. XLOG_HEAP_INSERT vs XLOG_HEAP_DELETE).
	XLRRmgrInfoMask uint8 = 0xF0

	// XLRSpecialRelUpdate marks records that touch relfilenode
	// state outside the normal block-reference path. Currently
	// unused by external tooling but reserved.
	XLRSpecialRelUpdate uint8 = 0x01
	// XLRCheckConsistency triggers wal_consistency_checking-style
	// FPI emission for replay verification.
	XLRCheckConsistency uint8 = 0x02
)

// Rmgr identifies the resource manager owning a record (heap,
// btree, xlog, etc.). Values match upstream's RM_*_ID so
// `pg_waldump --rmgr=Heap` filters work against goopg-emitted WAL
// out of the box. Only IDs goopg's current record kinds map to are
// defined here; the rest land alongside their producers.
type Rmgr uint8

const (
	RmgrXLog    Rmgr = 0 // RM_XLOG_ID — checkpoints, EOL markers, switch
	RmgrXact    Rmgr = 1 // RM_XACT_ID — commit / abort
	RmgrStorage Rmgr = 2 // RM_SMGR_ID — relation create / truncate
	RmgrCLOG    Rmgr = 3 // RM_CLOG_ID — clog (pg_xact) truncation
	// 4..7 reserved (Database, Tablespace, MultiXact, RelMap).
	RmgrStandby Rmgr = 8  // RM_STANDBY_ID — RUNNING_XACTS snapshot markers
	RmgrHeap2   Rmgr = 9  // RM_HEAP2_ID   — heap multi-insert / vacuum
	RmgrHeap    Rmgr = 10 // RM_HEAP_ID    — heap insert / delete / update
	RmgrBtree   Rmgr = 11 // RM_BTREE_ID   — btree insert / split

	// MaxKnownRmgr bounds the real-PG IDs goopg defines symbolic
	// names for in this slice. Values in (MaxKnownRmgr, RmgrGoopgCatalog)
	// are rejected by the decoder as a typed "emitted by a newer
	// version" branch.
	MaxKnownRmgr Rmgr = RmgrBtree

	// RmgrGoopgCustomBase is the first ID in PostgreSQL's reserved
	// custom-rmgr range (RM_MIN_CUSTOM_ID, upstream
	// rmgr.h). goopg-private record kinds with no PG analog (catalog
	// DDL, roles, etc. — docs/design/wal-native-pg-format/04-*.md
	// §3.2) classify under this range; the per-record RecordKind
	// byte in the payload remains the authoritative discriminator.
	RmgrGoopgCustomBase Rmgr = 128
	// RmgrGoopgCatalog is goopg's single custom resource manager for
	// all private catalog/DDL record kinds (§3.2 of the doc above).
	RmgrGoopgCatalog Rmgr = RmgrGoopgCustomBase
)

// ErrInvalidRecordHeader is the typed sentinel a decoder returns
// when an XLogRecord header is malformed (unrecognised Rmgr,
// non-zero padding bytes, undefined info bits).
var ErrInvalidRecordHeader = errors.New("wal: invalid record header")

// XLogRecord is the typed view of an upstream-compatible WAL
// record header. On-disk byte layout, little-endian:
//
//	offset 0   TotLen  (4 bytes) xl_tot_len
//	offset 4   XID     (4 bytes) xl_xid
//	offset 8   Prev    (8 bytes) xl_prev
//	offset 16  Info    (1 byte)  xl_info
//	offset 17  Rmid    (1 byte)  xl_rmid
//	offset 18  _pad    (2 bytes) MUST be zero
//	offset 20  CRC     (4 bytes) xl_crc — CRC32C over (header bytes
//	                              before xl_crc || payload).
type XLogRecord struct {
	TotLen uint32 // xl_tot_len — total bytes including the 24-byte header
	XID    uint32 // xl_xid     — TransactionId is uint32 in upstream
	Prev   uint64 // xl_prev    — start LSN of the previous record
	Info   uint8  // xl_info    — flag bits (XLR_*)
	Rmid   Rmgr   // xl_rmid    — resource manager id
	CRC    uint32 // xl_crc     — set by EncodeXLogRecordHeader, validated by reader
}

// xlogCRC32CTable is the Castagnoli polynomial table — upstream's
// CRC32C. Go's crc32.MakeTable returns a singleton for the
// canonical polynomials, so this allocation happens once.
var xlogCRC32CTable = crc32.MakeTable(crc32.Castagnoli)

// XLogCRC32C returns the upstream-compatible CRC32C checksum over
// `data` using the Castagnoli polynomial 0x1EDC6F41.
func XLogCRC32C(data []byte) uint32 {
	return crc32.Checksum(data, xlogCRC32CTable)
}

// EncodeXLogRecordHeader writes a 24-byte XLogRecord header to
// dst[:SizeOfXLogRecord], computing xl_crc over (header bytes
// before xl_crc || payload) — matches upstream's CRC convention.
//
// Caller invariants:
//   - dst is large enough (SizeOfXLogRecord bytes minimum).
//   - payload is the bytes that follow the header on disk (rmgr-
//     specific encoding). It is NOT mutated.
//   - h.TotLen MUST equal SizeOfXLogRecord + len(payload). The
//     encoder validates this so producers can't accidentally write
//     a header that disagrees with the payload it ships with.
//   - h.Info high 4 bits are free for rmgr use; low 4 bits must be
//     a subset of (XLRSpecialRelUpdate|XLRCheckConsistency) — the
//     only framework-area flags currently defined.
func EncodeXLogRecordHeader(dst []byte, h XLogRecord, payload []byte) error {
	if len(dst) < SizeOfXLogRecord {
		return fmt.Errorf("wal: XLogRecord buffer = %d bytes, need %d", len(dst), SizeOfXLogRecord)
	}
	want := uint32(SizeOfXLogRecord) + uint32(len(payload))
	if h.TotLen != want {
		return fmt.Errorf("wal: XLogRecord TotLen=%d disagrees with header+payload=%d", h.TotLen, want)
	}
	frameworkBits := h.Info & XLRInfoMask
	allowed := XLRSpecialRelUpdate | XLRCheckConsistency
	if frameworkBits & ^allowed != 0 {
		return fmt.Errorf("wal: undefined xl_info framework bits 0x%x", frameworkBits)
	}
	binary.LittleEndian.PutUint32(dst[0:4], h.TotLen)
	binary.LittleEndian.PutUint32(dst[4:8], h.XID)
	binary.LittleEndian.PutUint64(dst[8:16], h.Prev)
	dst[16] = h.Info
	dst[17] = byte(h.Rmid)
	// Padding bytes — upstream initialises to zero; an external
	// reader treats non-zero padding as corruption, so we MUST
	// zero them. Tests pin this.
	dst[18] = 0
	dst[19] = 0
	// xl_crc is computed over (payload || header bytes 0..19) — the
	// upstream order matches XLogInsertRecord/XLogRecordAssemble in
	// postgres/src/backend/access/transam/xlog.c (line ~5170) and
	// xloginsert.c (line ~903): "rdata, then backup blocks, then
	// record header". We zero xl_crc bytes before stamping the
	// finished value back.
	dst[20] = 0
	dst[21] = 0
	dst[22] = 0
	dst[23] = 0
	crc := crc32.Update(0, xlogCRC32CTable, payload)
	crc = crc32.Update(crc, xlogCRC32CTable, dst[:20])
	binary.LittleEndian.PutUint32(dst[20:24], crc)
	return nil
}

// DecodeXLogRecordHeader parses the first 24 bytes of src. Does
// NOT validate xl_crc — recovery / replay paths must reassemble
// the full record bytes (header || payload) with xl_crc zeroed,
// invoke XLogCRC32C, and compare against the returned CRC. The
// CRC validation is intentionally separate so callers that only
// need the header (e.g. iterator framing) don't pay for the
// payload read.
//
// Returns ErrInvalidRecordHeader on:
//   - non-zero padding bytes (offsets 18..19) — upstream invariant
//     and a useful corruption signal,
//   - Rmid > MaxKnownRmgr and below RmgrGoopgCustomBase (128) — typed
//     branch for "this WAL was emitted by a producer goopg doesn't
//     know yet"; the 128..255 custom-rmgr range is always accepted,
//   - undefined framework bits in xl_info.
func DecodeXLogRecordHeader(src []byte) (XLogRecord, error) {
	if len(src) < SizeOfXLogRecord {
		return XLogRecord{}, fmt.Errorf("wal: XLogRecord source = %d bytes, need %d", len(src), SizeOfXLogRecord)
	}
	h := XLogRecord{
		TotLen: binary.LittleEndian.Uint32(src[0:4]),
		XID:    binary.LittleEndian.Uint32(src[4:8]),
		Prev:   binary.LittleEndian.Uint64(src[8:16]),
		Info:   src[16],
		Rmid:   Rmgr(src[17]),
		CRC:    binary.LittleEndian.Uint32(src[20:24]),
	}
	if src[18] != 0 || src[19] != 0 {
		return h, fmt.Errorf("%w: padding bytes nonzero (0x%02x 0x%02x)", ErrInvalidRecordHeader, src[18], src[19])
	}
	if h.Rmid > MaxKnownRmgr && h.Rmid < RmgrGoopgCustomBase {
		return h, fmt.Errorf("%w: unknown rmid=%d", ErrInvalidRecordHeader, h.Rmid)
	}
	frameworkBits := h.Info & XLRInfoMask
	allowed := XLRSpecialRelUpdate | XLRCheckConsistency
	if frameworkBits & ^allowed != 0 {
		return h, fmt.Errorf("%w: undefined xl_info framework bits 0x%x", ErrInvalidRecordHeader, frameworkBits)
	}
	return h, nil
}

// VerifyXLogRecordCRC reconstructs the running CRC over (header
// bytes before xl_crc || payload) and compares it against
// h.CRC. Used by the recovery / iterator path after a successful
// header decode + payload read. Returns nil on match,
// ErrCorruptRecord on mismatch.
//
// `headerBytes` must be the on-disk 24-byte header *as it appears
// in the segment file* — the function zeros offsets 20..23 in a
// scratch copy before checksumming so the caller doesn't have to.
func VerifyXLogRecordCRC(headerBytes, payload []byte, want uint32) error {
	if len(headerBytes) < SizeOfXLogRecord {
		return fmt.Errorf("wal: VerifyXLogRecordCRC: header = %d bytes, need %d", len(headerBytes), SizeOfXLogRecord)
	}
	var scratch [SizeOfXLogRecord]byte
	copy(scratch[:], headerBytes[:SizeOfXLogRecord])
	scratch[20] = 0
	scratch[21] = 0
	scratch[22] = 0
	scratch[23] = 0
	// CRC order matches upstream: payload first, then header[:20].
	crc := crc32.Update(0, xlogCRC32CTable, payload)
	crc = crc32.Update(crc, xlogCRC32CTable, scratch[:20])
	if crc != want {
		return fmt.Errorf("%w: xl_crc=0x%08x computed=0x%08x", ErrCorruptRecord, want, crc)
	}
	return nil
}
