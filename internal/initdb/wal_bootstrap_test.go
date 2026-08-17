package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// TestWriteBootstrapWAL verifies that WriteBootstrapWAL produces a
// wal_segment_size (16 MiB) file with a PG-compatible long page
// header and a valid XLOG_CHECKPOINT_SHUTDOWN record.
//
// Pinned fields (must match PostgreSQL exactly, mod sysid/time):
//   - xlp_magic = 0xD118 (XLOGPageMagic)
//   - xlp_info  has XLPLongHeader (0x0002) bit set
//   - xlp_tli   = 1 (BootstrapTimeLineID)
//   - xlp_pageaddr = 0x01000000 (wal_segment_size)
//   - xlp_sysid = sysID passed in
//   - xlp_seg_size  = 16 MiB
//   - xlp_xlog_blcksz = 8192
//   - xl_tot_len = 114 at offset 40
//   - xl_xid    = 0 at offset 44
//   - xl_prev   = 0 at offset 48
//   - xl_info   = 0x00 (XLOG_CHECKPOINT_SHUTDOWN) at offset 56
//   - xl_rmid   = 0 (RM_XLOG_ID) at offset 57
//   - redo LSN in CheckPoint body = pgInitCheckpointLSN (0x01000028)
//   - ThisTimeLineID in CheckPoint body = 1
func TestWriteBootstrapWAL(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	if err := os.Mkdir(walDir, 0o700); err != nil {
		t.Fatal(err)
	}

	const sysID = uint64(0xDEADBEEFCAFEBABE)
	ts := time.Unix(1700000000, 0) // fixed time for reproducibility

	if err := WriteBootstrapWAL(dir, sysID, ts); err != nil {
		t.Fatalf("WriteBootstrapWAL: %v", err)
	}

	segFile := filepath.Join(walDir, "000000010000000000000001")
	data, err := os.ReadFile(segFile)
	if err != nil {
		t.Fatalf("segment file missing: %v", err)
	}

	// File must be exactly wal_segment_size.
	want := int(xlog.DefaultSegmentSize)
	if len(data) != want {
		t.Fatalf("segment size = %d, want %d", len(data), want)
	}

	le := binary.LittleEndian

	// Long page header (bytes 0..39).
	magic := le.Uint16(data[0:2])
	if magic != xlog.XLOGPageMagic {
		t.Errorf("xlp_magic = 0x%04X, want 0x%04X", magic, xlog.XLOGPageMagic)
	}
	info := le.Uint16(data[2:4])
	if info&xlog.XLPLongHeader == 0 {
		t.Errorf("xlp_info = 0x%04X: XLPLongHeader bit not set", info)
	}
	tli := le.Uint32(data[4:8])
	if tli != 1 {
		t.Errorf("xlp_tli = %d, want 1", tli)
	}
	pageAddr := le.Uint64(data[8:16])
	wantPageAddr := uint64(xlog.DefaultSegmentSize)
	if pageAddr != wantPageAddr {
		t.Errorf("xlp_pageaddr = 0x%016X, want 0x%016X", pageAddr, wantPageAddr)
	}
	gotSysID := le.Uint64(data[24:32])
	if gotSysID != sysID {
		t.Errorf("xlp_sysid = 0x%016X, want 0x%016X", gotSysID, sysID)
	}
	segSize := le.Uint32(data[32:36])
	if segSize != uint32(xlog.DefaultSegmentSize) {
		t.Errorf("xlp_seg_size = %d, want %d", segSize, xlog.DefaultSegmentSize)
	}
	xlogBlcksz := le.Uint32(data[36:40])
	if xlogBlcksz != uint32(xlog.XLOGBlockSize) {
		t.Errorf("xlp_xlog_blcksz = %d, want %d", xlogBlcksz, xlog.XLOGBlockSize)
	}

	// XLogRecord header starts at byte 40 (SizeOfXLogLongPHD).
	recOff := xlog.SizeOfXLogLongPHD // 40
	totLen := le.Uint32(data[recOff+0 : recOff+4])
	if totLen != 114 {
		t.Errorf("xl_tot_len = %d, want 114", totLen)
	}
	xid := le.Uint32(data[recOff+4 : recOff+8])
	if xid != 0 {
		t.Errorf("xl_xid = %d, want 0", xid)
	}
	prev := le.Uint64(data[recOff+8 : recOff+16])
	if prev != 0 {
		t.Errorf("xl_prev = 0x%016X, want 0", prev)
	}
	xlInfo := data[recOff+16]
	if xlInfo != 0x00 {
		t.Errorf("xl_info = 0x%02X, want 0x00 (XLOG_CHECKPOINT_SHUTDOWN)", xlInfo)
	}
	xlRmid := data[recOff+17]
	if xlRmid != 0 {
		t.Errorf("xl_rmid = %d, want 0 (RM_XLOG_ID)", xlRmid)
	}
	// Padding bytes 18..19 must be zero.
	if data[recOff+18] != 0 || data[recOff+19] != 0 {
		t.Errorf("xl_record padding [18..19] non-zero: %02X %02X", data[recOff+18], data[recOff+19])
	}
	// xl_crc at offset 20 must be non-zero (actual CRC32C).
	crc := le.Uint32(data[recOff+20 : recOff+24])
	if crc == 0 {
		t.Errorf("xl_crc = 0, expected non-zero CRC32C")
	}

	// Payload starts at byte 40+24 = 64.
	// Byte 64: XLogRecordDataHeaderShort id = 255.
	// Byte 65: data_length = 88.
	payOff := recOff + xlog.SizeOfXLogRecord // 64
	if data[payOff] != 255 {
		t.Errorf("data header id = %d, want 255 (XLR_BLOCK_ID_DATA_SHORT)", data[payOff])
	}
	if data[payOff+1] != 88 {
		t.Errorf("data header data_length = %d, want 88 (sizeof CheckPoint)", data[payOff+1])
	}

	// CheckPoint body starts at byte 66.
	cpOff := payOff + 2
	redoLSN := le.Uint64(data[cpOff+0 : cpOff+8])
	if redoLSN != pgInitCheckpointLSN {
		t.Errorf("CheckPoint.redo = 0x%016X, want 0x%016X", redoLSN, pgInitCheckpointLSN)
	}
	thisTLI := le.Uint32(data[cpOff+8 : cpOff+12])
	if thisTLI != 1 {
		t.Errorf("CheckPoint.ThisTimeLineID = %d, want 1", thisTLI)
	}
	prevTLI := le.Uint32(data[cpOff+12 : cpOff+16])
	if prevTLI != 1 {
		t.Errorf("CheckPoint.PrevTimeLineID = %d, want 1", prevTLI)
	}
	fpw := data[cpOff+16]
	if fpw != 1 {
		t.Errorf("CheckPoint.fullPageWrites = %d, want 1", fpw)
	}
	cpTime := le.Uint64(data[cpOff+64 : cpOff+72])
	if cpTime != uint64(ts.Unix()) {
		t.Errorf("CheckPoint.time = %d, want %d", cpTime, ts.Unix())
	}

	// Bytes beyond the first 8 KiB page must be zero.
	for i := xlog.XLOGBlockSize; i < want; i++ {
		if data[i] != 0 {
			t.Fatalf("byte %d beyond first page is non-zero: 0x%02X", i, data[i])
		}
	}
}

// TestWriteBootstrapWAL_Idempotent verifies that calling WriteBootstrapWAL
// twice on the same directory overwrites the previous segment cleanly.
func TestWriteBootstrapWAL_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pg_wal"), 0o700); err != nil {
		t.Fatal(err)
	}
	ts := time.Now()
	if err := WriteBootstrapWAL(dir, 0x1111, ts); err != nil {
		t.Fatal(err)
	}
	// Second call with different sysID should overwrite.
	if err := WriteBootstrapWAL(dir, 0x2222, ts); err != nil {
		t.Fatalf("second WriteBootstrapWAL: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pg_wal", "000000010000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	gotSysID := binary.LittleEndian.Uint64(data[24:32])
	if gotSysID != 0x2222 {
		t.Errorf("after overwrite xlp_sysid = 0x%016X, want 0x2222", gotSysID)
	}
}
