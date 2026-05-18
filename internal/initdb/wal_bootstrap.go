package initdb

// WriteBootstrapWAL writes the first WAL segment file
// (pg_wal/000000010000000000000001) with:
//   - a 40-byte XLogLongPageHeaderData at offset 0 (sysid-stamped)
//   - a 114-byte XLOG_CHECKPOINT_SHUTDOWN record immediately after
//   - zero padding to wal_segment_size (16 MiB)
//
// Must be called BEFORE writePgControl so the ordering invariant
// matches BootStrapXLOG (postgres/src/backend/access/transam/xlog.c:5175-5219):
// WAL segment flushed → pg_control written.
//
// Design: docs/design/bootstrap-procedure/03-wal-bootstrap-segment.md
// Milestone: M0106-0010 batched-04.

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/goopg/goopg/internal/wal"
)

const (
	// walBootstrapCheckpointSize is sizeof(CheckPoint) on x86_64 / PG18.
	// Mirrors the pg_control.h:35-65 layout, verified against
	// buildPgControl (pgcontrol.go) checkPointCopy at offsets 40..128.
	walBootstrapCheckpointSize = 88

	// walXlogCheckpointShutdown is XLOG_CHECKPOINT_SHUTDOWN (pg_control.h:68).
	walXlogCheckpointShutdown = uint8(0x00)

	// walXlrBlockIDDataShort is XLR_BLOCK_ID_DATA_SHORT (xlogrecord.h:243).
	walXlrBlockIDDataShort = byte(255)

	// walBootstrapTLI is BootstrapTimeLineID (xlog.c:112).
	walBootstrapTLI = uint32(1)

	// walBootstrapSegNo is the first usable segment number.
	// Segment 0 is intentionally skipped so LSN 0/0 remains the
	// "invalid pointer"; the first usable LSN is wal_segment_size.
	walBootstrapSegNo = uint64(1)
)

// WriteBootstrapWAL writes pg_wal/000000010000000000000001 with the
// long page header and the XLOG_CHECKPOINT_SHUTDOWN record.
// Must be called before writePgControl.
func WriteBootstrapWAL(dataDir string, sysID uint64, now time.Time) error {
	name := wal.XLogFileName(walBootstrapTLI, walBootstrapSegNo, wal.DefaultSegmentSize)
	segPath := filepath.Join(dataDir, "pg_wal", name)

	// Allocate one 8 KiB page; only this page carries data.
	// The segment's remaining bytes (wal_segment_size - XLOG_BLCKSZ) are
	// zero-filled via Truncate, matching XLogFileInit's pre-zero allocation.
	page := make([]byte, wal.XLOGBlockSize)

	// -- Long page header (bytes 0..39) --
	//
	// xlp_pageaddr = wal_segment_size (0x01000000): absolute byte position
	// of this page in the WAL stream.  Segment 1 page 0 starts at byte
	// 1 * wal_segment_size.
	pageAddr := uint64(wal.DefaultSegmentSize) // 0x01000000
	lph := wal.XLogLongPageHeader{
		Std: wal.XLogPageHeader{
			Magic:    wal.XLOGPageMagic, // 0xD118
			Info:     0,                 // EncodeXLogLongPageHeader adds XLPLongHeader
			TLI:      walBootstrapTLI,   // 1
			PageAddr: pageAddr,
			RemLen:   0, // no continuation record
		},
		SysID:      sysID,
		SegSize:    uint32(wal.DefaultSegmentSize), // 16 MiB
		XLogBlcksz: uint32(wal.XLOGBlockSize),      // 8192
	}
	if err := wal.EncodeXLogLongPageHeader(page[:wal.SizeOfXLogLongPHD], lph); err != nil {
		return fmt.Errorf("wal bootstrap: long page header: %w", err)
	}

	// -- XLOG_CHECKPOINT_SHUTDOWN record (bytes 40..153, total 114 bytes) --
	//
	// Layout: XLogRecord (24 B) | XLogRecordDataHeaderShort (2 B) | CheckPoint (88 B)
	// xl_tot_len = 24 + 2 + 88 = 114.

	// payload = XLogRecordDataHeaderShort (2 bytes) + CheckPoint body (88 bytes)
	payload := make([]byte, 2+walBootstrapCheckpointSize) // 90 bytes
	payload[0] = walXlrBlockIDDataShort                   // id = 255
	payload[1] = byte(walBootstrapCheckpointSize)         // data_length = 88

	encodeCheckPointBody(payload[2:], now)

	// XLogRecord header (24 bytes) at page[40..63], CRC computed over
	// payload then header[0..19] — see EncodeXLogRecordHeader.
	recDst := page[wal.SizeOfXLogLongPHD:] // starts at byte 40
	hdr := wal.XLogRecord{
		TotLen: uint32(wal.SizeOfXLogRecord) + uint32(len(payload)), // 114
		XID:    0,                                                    // InvalidTransactionId
		Prev:   0,                                                    // first record; no predecessor
		Info:   walXlogCheckpointShutdown,
		Rmid:   wal.RmgrXLog, // RM_XLOG_ID = 0
	}
	if err := wal.EncodeXLogRecordHeader(recDst[:wal.SizeOfXLogRecord], hdr, payload); err != nil {
		return fmt.Errorf("wal bootstrap: xlog record header: %w", err)
	}
	copy(recDst[wal.SizeOfXLogRecord:], payload)

	// Write the segment file: first page filled, then zero-extend to
	// wal_segment_size via Truncate (sparse on supporting filesystems).
	f, err := os.OpenFile(segPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("wal bootstrap: create %q: %w", segPath, err)
	}
	if _, err := f.Write(page); err != nil {
		_ = f.Close()
		return fmt.Errorf("wal bootstrap: write page: %w", err)
	}
	if err := f.Truncate(wal.DefaultSegmentSize); err != nil {
		_ = f.Close()
		return fmt.Errorf("wal bootstrap: truncate to segment size: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("wal bootstrap: fsync: %w", err)
	}
	return f.Close()
}

// encodeCheckPointBody fills the 88-byte CheckPoint struct in dst,
// mirroring BootStrapXLOG (xlog.c:5115-5132) and buildPgControl
// checkPointCopy (pgcontrol.go offsets 40..128).
//
// All fields are little-endian (x86_64 / PG18 wire layout).
func encodeCheckPointBody(dst []byte, now time.Time) {
	le := binary.LittleEndian

	// offset 0: redo (XLogRecPtr=uint64) — first usable WAL record LSN
	le.PutUint64(dst[0:], pgInitCheckpointLSN) // 0x01000028
	// offset 8: ThisTimeLineID (uint32)
	le.PutUint32(dst[8:], 1)
	// offset 12: PrevTimeLineID (uint32)
	le.PutUint32(dst[12:], 1)
	// offset 16: fullPageWrites (bool) — GUC default true
	dst[16] = 1
	// offset 17..19: 3-byte alignment padding (zero — dst pre-zeroed)
	// offset 20: wal_level (int/uint32) — WAL_LEVEL_REPLICA = 1
	le.PutUint32(dst[20:], 1)
	// offset 24: nextXid (FullTransactionId=uint64) — FirstNormalTransactionId=3
	le.PutUint64(dst[24:], pgFirstNormalXID)
	// offset 32: nextOid (Oid=uint32) — FirstGenbkiObjectId=10000
	le.PutUint32(dst[32:], pgFirstGenbkiOID)
	// offset 36: nextMulti (MultiXactId=uint32) — FirstMultiXactId=1
	le.PutUint32(dst[36:], pgFirstMultiXact)
	// offset 40: nextMultiOffset (MultiXactOffset=uint32) — 0
	le.PutUint32(dst[40:], 0)
	// offset 44: oldestXid (TransactionId=uint32) — FirstNormalTransactionId=3
	le.PutUint32(dst[44:], uint32(pgFirstNormalXID))
	// offset 48: oldestXidDB (Oid=uint32) — Template1DbOid=1
	le.PutUint32(dst[48:], pgTemplate1DbOID)
	// offset 52: oldestMulti (MultiXactId=uint32) — FirstMultiXactId=1
	le.PutUint32(dst[52:], pgFirstMultiXact)
	// offset 56: oldestMultiDB (Oid=uint32) — Template1DbOid=1
	le.PutUint32(dst[56:], pgTemplate1DbOID)
	// offset 60..63: 4-byte alignment padding (zero — dst pre-zeroed)
	// offset 64: time (pg_time_t=int64)
	le.PutUint64(dst[64:], uint64(now.Unix()))
	// offset 72: oldestCommitTsXid (TransactionId=uint32) — InvalidTransactionId=0
	le.PutUint32(dst[72:], 0)
	// offset 76: newestCommitTsXid (TransactionId=uint32) — InvalidTransactionId=0
	le.PutUint32(dst[76:], 0)
	// offset 80: oldestActiveXid (TransactionId=uint32) — InvalidTransactionId=0
	le.PutUint32(dst[80:], 0)
	// offset 84..87: 4-byte tail padding (zero — dst pre-zeroed)
}
