package wal

// PostgreSQL-compatible WAL page header types and helpers (M0014-0001).
// Mirrors postgres/src/include/access/xlog_internal.h
// (XLogPageHeaderData / XLogLongPageHeaderData) and
// postgres/src/include/access/xlogdefs.h (XLogFileName).
//
// This slice is *purely additive*: the writer's append path doesn't
// consume these helpers yet. Establishing the symbol names, constants,
// and on-disk encodings in one well-tested commit lets the
// writer/reader switchover land incrementally in subsequent slices
// (M0014-0001b for writer, M0014-0002 for record headers, etc.).
//
// See docs/design/0014-0001-xlog-page-and-segment-layout-compat.md.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

// XLOGBlockSize is the WAL page size — fixed at 8 KiB to match
// upstream PostgreSQL's compile-time XLOG_BLCKSZ default.
const XLOGBlockSize = 8192

// XLOGPageMagic is the magic number written into every page header's
// `xlp_magic` field. Bumped per major version upstream; targeting
// PG18 here. A later major upgrade is one constant change.
//
// Cross-reference: `XLOG_PAGE_MAGIC` in
// postgres/src/include/access/xlog_internal.h for the relevant
// release.
const XLOGPageMagic uint16 = 0xD119

// xlp_info flag bits (mirror postgres/src/include/access/xlog_internal.h).
const (
	// XLPFirstIsContRecord: this page begins with the continuation
	// of a record that started on a previous page.
	XLPFirstIsContRecord uint16 = 0x0001
	// XLPLongHeader: page header is the 40-byte
	// XLogLongPageHeader form (set on the first page of every
	// segment).
	XLPLongHeader uint16 = 0x0002
	// XLPBkpRemovable: backup blocks starting in this page are
	// removable (used by pg_basebackup).
	XLPBkpRemovable uint16 = 0x0004
	// XLPFirstIsOverwriteContRecord: replaces a missing
	// contrecord. See upstream's CreateOverwriteContrecordRecord.
	XLPFirstIsOverwriteContRecord uint16 = 0x0008
	// XLPAllFlags: union of all defined bits — used for validity
	// checking.
	XLPAllFlags uint16 = XLPFirstIsContRecord | XLPLongHeader |
		XLPBkpRemovable | XLPFirstIsOverwriteContRecord
)

// SizeOfXLogShortPHD is the on-disk size of the short page header
// (after MAXALIGN of the 20 logical bytes).
const SizeOfXLogShortPHD = 24

// SizeOfXLogLongPHD is the on-disk size of the long page header
// (short header + sysid/seg_size/xlog_blcksz cross-check).
const SizeOfXLogLongPHD = 40

// ErrInvalidPageHeader is the typed sentinel a decoder returns when
// the magic number doesn't match XLOGPageMagic. The M0014-0004
// legacy-format detector branches on it to surface an actionable
// "this WAL was written by a pre-M0014 goopg or a different PG
// major" diagnostic.
var ErrInvalidPageHeader = errors.New("wal: invalid page header")

// XLogPageHeader is the typed view of an upstream-compatible WAL
// page header. Field order and on-disk byte layout match
// XLogPageHeaderData verbatim:
//
//	offset 0   Magic     (2 bytes)
//	offset 2   Info      (2 bytes)
//	offset 4   TLI       (4 bytes)
//	offset 8   PageAddr  (8 bytes)
//	offset 16  RemLen    (4 bytes)
//	offset 20  _padding  (4 bytes; MAXALIGN to 24)
//
// Endianness: little-endian by construction. Upstream writes WAL in
// host byte order and pg_waldump expects host byte order; goopg
// targets x86_64/aarch64 Linux which are both LE. Cross-arch transfer
// is out of scope (matches upstream's de-facto limitation).
type XLogPageHeader struct {
	Magic    uint16 // xlp_magic
	Info     uint16 // xlp_info — flag bits (XLP_*)
	TLI      uint32 // xlp_tli — timeline of the first record on this page
	PageAddr uint64 // xlp_pageaddr — absolute byte LSN of this page
	RemLen   uint32 // xlp_rem_len — bytes left of a continued record (0 if not a contrecord page)
}

// XLogLongPageHeader is the long form: standard header followed by
// sysid/seg_size/xlog_blcksz cross-check. Set on the first page of
// every segment so a single-page-recovery scenario can self-identify.
type XLogLongPageHeader struct {
	Std        XLogPageHeader
	SysID      uint64 // xlp_sysid — matches pg_control's system_identifier
	SegSize    uint32 // xlp_seg_size — cross-check (must equal wal_segment_size)
	XLogBlcksz uint32 // xlp_xlog_blcksz — cross-check (must equal XLOGBlockSize)
}

// EncodeXLogPageHeader writes the 24-byte short page header to
// dst[:SizeOfXLogShortPHD]. Panics if dst is too small (caller bug).
// Returns an error if Info contains undefined flag bits — pins the
// XLPAllFlags contract so a future flag addition doesn't silently
// produce pages pg_waldump rejects.
func EncodeXLogPageHeader(dst []byte, h XLogPageHeader) error {
	if len(dst) < SizeOfXLogShortPHD {
		return fmt.Errorf("wal: short page header buffer = %d bytes, need %d", len(dst), SizeOfXLogShortPHD)
	}
	if h.Info & ^XLPAllFlags != 0 {
		return fmt.Errorf("wal: undefined xlp_info flag bits 0x%x", h.Info)
	}
	binary.LittleEndian.PutUint16(dst[0:2], h.Magic)
	binary.LittleEndian.PutUint16(dst[2:4], h.Info)
	binary.LittleEndian.PutUint32(dst[4:8], h.TLI)
	binary.LittleEndian.PutUint64(dst[8:16], h.PageAddr)
	binary.LittleEndian.PutUint32(dst[16:20], h.RemLen)
	// Bytes 20..23 are MAXALIGN padding; zero-fill for a stable
	// on-disk image.
	dst[20] = 0
	dst[21] = 0
	dst[22] = 0
	dst[23] = 0
	return nil
}

// DecodeXLogPageHeader parses the first 24 bytes of src. Returns
// ErrInvalidPageHeader if the magic value doesn't match
// XLOGPageMagic — surfaces a clear distinction between a corrupt /
// truncated page and a page written by a different format
// (legacy goopg / different PG major).
func DecodeXLogPageHeader(src []byte) (XLogPageHeader, error) {
	if len(src) < SizeOfXLogShortPHD {
		return XLogPageHeader{}, fmt.Errorf("wal: short page header source = %d bytes, need %d", len(src), SizeOfXLogShortPHD)
	}
	h := XLogPageHeader{
		Magic:    binary.LittleEndian.Uint16(src[0:2]),
		Info:     binary.LittleEndian.Uint16(src[2:4]),
		TLI:      binary.LittleEndian.Uint32(src[4:8]),
		PageAddr: binary.LittleEndian.Uint64(src[8:16]),
		RemLen:   binary.LittleEndian.Uint32(src[16:20]),
	}
	if h.Magic != XLOGPageMagic {
		return h, fmt.Errorf("%w: magic=0x%04x want 0x%04x", ErrInvalidPageHeader, h.Magic, XLOGPageMagic)
	}
	if h.Info & ^XLPAllFlags != 0 {
		return h, fmt.Errorf("%w: undefined xlp_info flag bits 0x%x", ErrInvalidPageHeader, h.Info)
	}
	return h, nil
}

// EncodeXLogLongPageHeader writes the 40-byte long form. Bytes
// 0..23 are the standard header (with XLPLongHeader forced into
// Info — callers don't have to set it); 24..39 carry the
// sysid/seg_size/xlog_blcksz triple.
func EncodeXLogLongPageHeader(dst []byte, h XLogLongPageHeader) error {
	if len(dst) < SizeOfXLogLongPHD {
		return fmt.Errorf("wal: long page header buffer = %d bytes, need %d", len(dst), SizeOfXLogLongPHD)
	}
	std := h.Std
	std.Info |= XLPLongHeader
	if err := EncodeXLogPageHeader(dst[:SizeOfXLogShortPHD], std); err != nil {
		return err
	}
	binary.LittleEndian.PutUint64(dst[24:32], h.SysID)
	binary.LittleEndian.PutUint32(dst[32:36], h.SegSize)
	binary.LittleEndian.PutUint32(dst[36:40], h.XLogBlcksz)
	return nil
}

// DecodeXLogLongPageHeader parses the first 40 bytes of src.
// Validates that XLPLongHeader is set in the embedded short
// header's Info — a long-form decode against a short-form page is a
// caller bug worth surfacing as an error rather than silently
// reading garbage from the cross-check fields.
func DecodeXLogLongPageHeader(src []byte) (XLogLongPageHeader, error) {
	if len(src) < SizeOfXLogLongPHD {
		return XLogLongPageHeader{}, fmt.Errorf("wal: long page header source = %d bytes, need %d", len(src), SizeOfXLogLongPHD)
	}
	std, err := DecodeXLogPageHeader(src[:SizeOfXLogShortPHD])
	if err != nil {
		return XLogLongPageHeader{}, err
	}
	if std.Info&XLPLongHeader == 0 {
		return XLogLongPageHeader{}, fmt.Errorf("%w: XLP_LONG_HEADER bit not set", ErrInvalidPageHeader)
	}
	return XLogLongPageHeader{
		Std:        std,
		SysID:      binary.LittleEndian.Uint64(src[24:32]),
		SegSize:    binary.LittleEndian.Uint32(src[32:36]),
		XLogBlcksz: binary.LittleEndian.Uint32(src[36:40]),
	}, nil
}

// XLogFileName returns the upstream-compatible WAL segment filename
// for (tli, segno, segSize). Format: `<TLI:%08X><Log:%08X><Seg:%08X>`,
// where Log = (segno * segSize) / 4 GiB and Seg = segno mod
// (4 GiB / segSize).
//
// Mirrors `XLogFileName` in postgres/src/include/access/xlog_internal.h.
// At the upstream default 16 MiB segment size, `4 GiB / 16 MiB = 256`
// so each "log" holds 256 segments — exactly upstream's behaviour.
func XLogFileName(tli uint32, segno uint64, segSize int64) string {
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	// Total byte position = segno * segSize. Split into 32-bit
	// log_no (high) and seg_in_log (low).
	totalBytes := uint64(segno) * uint64(segSize)
	logNo := totalBytes >> 32
	segInLog := segno % (uint64(1<<32) / uint64(segSize))
	return fmt.Sprintf("%08X%08X%08X", tli, logNo, segInLog)
}

// ParseXLogFileName inverts XLogFileName. Returns ok=false on any
// length / parse error so callers can branch cleanly on
// "unrecognised name in pg_wal" without distinguishing every
// failure mode (legacy goopg counter format, garbage, etc.). Caller
// supplies segSize so the inverse math matches the writer's
// configured size.
func ParseXLogFileName(name string, segSize int64) (tli uint32, segno uint64, ok bool) {
	if len(name) != 24 {
		return 0, 0, false
	}
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	// strconv.ParseUint with base=16 is strict — it rejects any
	// non-hex character and partial parses, unlike fmt.Sscanf
	// whose `%08X` verb is permissive.
	t, err := strconv.ParseUint(name[0:8], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	logNo, err := strconv.ParseUint(name[8:16], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	segInLog, err := strconv.ParseUint(name[16:24], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	totalBytes := logNo<<32 | segInLog*uint64(segSize)
	return uint32(t), totalBytes / uint64(segSize), true
}
