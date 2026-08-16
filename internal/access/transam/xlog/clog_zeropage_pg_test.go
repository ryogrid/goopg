package xlog

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2 part 5: CLOG_ZEROPAGE redo --------------------------------
//
// goopg never emits this opcode natively — its own CLOG store is a lazily
// faulted, always-zero-on-miss buffer pool (clogBufferPool.readPageFromDisk),
// so it has never needed an explicit zero-page record. Upstream WriteZeroPageXlogRec
// (clog.c:1073-1078) fires once per 32768 XIDs, right before the first
// commit/abort into a fresh CLOG page, and clog_redo (clog.c:1114-1130) writes
// a zeroed BLCKSZ page to the pg_xact/ segment. A crashed real PG's WAL tail
// that allocated a fresh CLOG page during the crashed run carries one of
// these; without an arm goopg's physical replay silently dropped it and the
// segment for that XID range was never created on disk.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2 part 5.

// buildClogZeroPagePG assembles a real xl_clog_zeropage record — a bare int64
// pageno as main data, no block references, on RM_CLOG with opcode 0x00.
func buildClogZeroPagePG(t *testing.T, pageno int64) []byte {
	t.Helper()
	mainData := make([]byte, 8)
	binary.LittleEndian.PutUint64(mainData[0:8], uint64(pageno))
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrCLOG, xlogClogZeroPage, 0, body)
}

// TestApplyRecordReplaysPGClogZeroPage asserts the segment file for a
// previously-nonexistent CLOG page is created, zero-filled, and sized to at
// least the requested page's offset + BLCKSZ.
func TestApplyRecordReplaysPGClogZeroPage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	// pageno=3 within segment 0 (segment holds pages 0-31).
	const pageno = 3
	framed := buildClogZeroPagePG(t, pageno)
	applyPGRecord(t, mgr, framed, 100)

	segPath := filepath.Join(dataDir, "pg_xact", "0000")
	data, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatalf("segment file not created: %v", err)
	}
	off := int64(pageno) * int64(storage.BlockSize)
	want := off + int64(storage.BlockSize)
	if int64(len(data)) < want {
		t.Fatalf("segment file len = %d, want >= %d", len(data), want)
	}
	page := data[off : off+int64(storage.BlockSize)]
	for i, b := range page {
		if b != 0 {
			t.Fatalf("page byte %d = %#x, want 0 (zeroed page)", i, b)
		}
	}
}

// TestApplyRecordReplaysPGClogZeroPageHighSegment asserts a pageno that lands
// in a later segment file names it correctly ("%04X" of pageno/32) and does
// not disturb segment 0.
func TestApplyRecordReplaysPGClogZeroPageHighSegment(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	// pageno=65 -> segNo=2 (65/32), pageInSeg=1.
	const pageno = 65
	framed := buildClogZeroPagePG(t, pageno)
	applyPGRecord(t, mgr, framed, 100)

	segPath := filepath.Join(dataDir, "pg_xact", "0002")
	if _, err := os.Stat(segPath); err != nil {
		t.Fatalf("segment 0002 not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "pg_xact", "0000")); err == nil {
		t.Fatalf("segment 0000 should not have been created for pageno=65")
	}
}
