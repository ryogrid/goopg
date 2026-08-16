package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapCLog_WritesPGCanonicalSLRU pins the on-disk layout of the
// PG-canonical pg_xact/ SLRU directory created by bootstrapCLog.
//
// PG18's commit log SLRU (postgres/src/backend/access/transam/clog.c) uses
// 2 bits per XID packed 4-per-byte (lane = xid % 4, bit-shift = lane*2),
// 8192-byte pages, 32 pages per %04X-named segment file at the data-dir
// root.  TransactionLogFetch short-circuits BootstrapTransactionId(1) and
// FrozenTransactionId(2) as COMMITTED without consulting the SLRU, so we
// only require the file to exist with a zeroed BLCKSZ first page; the
// runtime SetCommitted/SetAborted path covers normal XIDs (>= 3).
//
// Without this layout PG's RangeVarGetRelidExtended on a basebackup-shipped
// standby returns 42P01 even when the heap row is on disk, because
// HeapTupleSatisfiesMVCC sees TransactionIdDidCommit(xmin)=false (the SLRU
// segment is missing). See M0106-0010 batched-43 diagnosis.
func TestBootstrapCLog_WritesPGCanonicalSLRU(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "global"), 0700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	if err := bootstrapCLog(dataDir); err != nil {
		t.Fatalf("bootstrapCLog: %v", err)
	}

	// (1) pg_xact/ directory exists with one segment file.
	pgXactDir := filepath.Join(dataDir, "pg_xact")
	fi, err := os.Stat(pgXactDir)
	if err != nil {
		t.Fatalf("stat %q: %v", pgXactDir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("expected %q to be a directory; mode=%v", pgXactDir, fi.Mode())
	}
	seg0 := filepath.Join(pgXactDir, "0000")
	segInfo, err := os.Stat(seg0)
	if err != nil {
		t.Fatalf("stat %q: %v", seg0, err)
	}
	// (2) Segment 0 covers at least one BLCKSZ page so
	// SimpleLruReadPage_ReadOnly can read the first page without extending.
	if segInfo.Size() < int64(storage.BlockSize) {
		t.Fatalf("segment %q size %d < BLCKSZ %d", seg0, segInfo.Size(), storage.BlockSize)
	}

	// (3) BootstrapTransactionId (1) and FrozenTransactionId (2) MUST NOT
	// be stamped in the SLRU — PG bypasses the SLRU for these XIDs via
	// TransactionLogFetch's short-circuit. The first byte of the segment
	// covers XIDs {0,1,2,3} so it must remain zero.
	raw, err := os.ReadFile(seg0)
	if err != nil {
		t.Fatalf("read %q: %v", seg0, err)
	}
	if raw[0] != 0x00 {
		t.Errorf("segment 0 byte 0 = 0x%02x, want 0x00 (XIDs 0..2 are SLRU-bypassed)", raw[0])
	}

	// (4) M0117-0006 Part B retires the goopg-legacy flat file: the
	// PG-canonical pg_xact/ SLRU is now the single CLOG store (PG itself has no
	// global/pg_xact file, and basebackup already excludes it). Bootstrap XIDs 1
	// and 2 are SLRU-bypassed and resolved as committed by the
	// TransactionLogFetch short-circuit, so bootstrap writes no flat file.
	flatPath := filepath.Join(dataDir, "global", "pg_xact")
	if _, err := os.Stat(flatPath); !os.IsNotExist(err) {
		t.Errorf("legacy flat file %q present (Part B should not write it): err=%v", flatPath, err)
	}
	// Reopening via the production recovery sequence must still resolve the
	// bootstrap/frozen XIDs as committed (the short-circuit, with the SLRU as the
	// live store).
	c, err := transam.OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(pgXactDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	if !c.DidCommit(transam.BootstrapTransactionID, nil) || !c.DidCommit(transam.FrozenTransactionID, nil) {
		t.Errorf("bootstrap/frozen XIDs not resolved committed after reopen")
	}
}

// TestCLog_SLRUMirror_StatusBitLayout verifies that runtime SetCommitted /
// SetAborted on normal XIDs (>= 3) writes the matching 2-bit lane of the
// PG-canonical segment file.
//
// Layout (PG18):
//
//	lane    = xid % 4
//	bshift  = lane * 2
//	byteVal = STATUS << bshift
//	STATUS in {COMMITTED=0x01, ABORTED=0x02}
//	byte 0 of segment 0 covers xids 0..3, byte 1 covers 4..7, etc.
func TestCLog_SLRUMirror_StatusBitLayout(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "global"), 0700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	clog, err := transam.OpenCLog(filepath.Join(dataDir, "global", "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	pgXactDir := filepath.Join(dataDir, "pg_xact")
	if err := clog.EnablePGSLRUMirror(pgXactDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	// Commit XID 3 (lane=3, shift=6 → 0x01<<6 = 0x40 in byte 0)
	if err := clog.SetCommitted(storage.TransactionID(3)); err != nil {
		t.Fatalf("SetCommitted(3): %v", err)
	}
	// Abort XID 7 (lane=3, shift=6 → 0x02<<6 = 0x80 in byte 1)
	if err := clog.SetAborted(storage.TransactionID(7)); err != nil {
		t.Fatalf("SetAborted(7): %v", err)
	}
	// Commit XID 9 (lane=1, shift=2 → 0x01<<2 = 0x04 in byte 2)
	if err := clog.SetCommitted(storage.TransactionID(9)); err != nil {
		t.Fatalf("SetCommitted(9): %v", err)
	}

	if err := clog.FlushAll(); err != nil { // C2-S1
		t.Fatalf("FlushAll: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(pgXactDir, "0000"))
	if err != nil {
		t.Fatalf("read seg0: %v", err)
	}
	cases := []struct {
		off  int
		want byte
		desc string
	}{
		{0, 0x40, "XID 3 commit -> byte 0 = 0x40"},
		{1, 0x80, "XID 7 abort  -> byte 1 = 0x80"},
		{2, 0x04, "XID 9 commit -> byte 2 = 0x04"},
	}
	for _, tc := range cases {
		if raw[tc.off] != tc.want {
			t.Errorf("%s: got 0x%02x, want 0x%02x", tc.desc, raw[tc.off], tc.want)
		}
	}
}

// TestCLog_SLRUMirror_ExtendsSegmentFile verifies that a SetCommitted on an
// XID that falls into a later page within segment 0 grows the segment file
// up to a BLCKSZ-aligned size.  SimpleLruReadPage_ReadOnly requires whole
// pages.
func TestCLog_SLRUMirror_ExtendsSegmentFile(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "global"), 0700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	clog, err := transam.OpenCLog(filepath.Join(dataDir, "global", "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	pgXactDir := filepath.Join(dataDir, "pg_xact")
	if err := clog.EnablePGSLRUMirror(pgXactDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	// 32768 XIDs per page (BLCKSZ*4). XID 32768 is the first XID of page 1.
	const firstXIDOnPage1 = 32768
	if err := clog.SetCommitted(storage.TransactionID(firstXIDOnPage1)); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	if err := clog.FlushAll(); err != nil { // C2-S1: extension reaches disk at flush points
		t.Fatalf("FlushAll: %v", err)
	}
	fi, err := os.Stat(filepath.Join(pgXactDir, "0000"))
	if err != nil {
		t.Fatalf("stat seg0: %v", err)
	}
	wantMin := int64(storage.BlockSize) * 2
	if fi.Size() < wantMin {
		t.Errorf("segment 0 size %d < %d (must cover pages 0 and 1)", fi.Size(), wantMin)
	}
}

// TestCLog_SLRUMirror_SegmentRollover verifies that an XID past
// 1048576 lands in segment 1 (file "0001"), not segment 0.
func TestCLog_SLRUMirror_SegmentRollover(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "global"), 0700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	clog, err := transam.OpenCLog(filepath.Join(dataDir, "global", "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	pgXactDir := filepath.Join(dataDir, "pg_xact")
	if err := clog.EnablePGSLRUMirror(pgXactDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	const firstXIDInSeg1 = 1048576 // 32768 * 32
	if err := clog.SetCommitted(storage.TransactionID(firstXIDInSeg1)); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	seg1 := filepath.Join(pgXactDir, fmt.Sprintf("%04X", 1))
	if err := clog.FlushAll(); err != nil { // C2-S1
		t.Fatalf("FlushAll: %v", err)
	}
	fi, err := os.Stat(seg1)
	if err != nil {
		t.Fatalf("stat %q: %v", seg1, err)
	}
	if fi.Size() < int64(storage.BlockSize) {
		t.Errorf("segment 1 size %d < BLCKSZ", fi.Size())
	}
	// Byte 0 of segment 1 must have lane 0 (xid % 4 = 0) set to COMMITTED.
	raw, err := os.ReadFile(seg1)
	if err != nil {
		t.Fatalf("read seg1: %v", err)
	}
	if raw[0] != 0x01 {
		t.Errorf("seg1 byte 0 = 0x%02x, want 0x01 (XID 1048576 lane 0 = COMMITTED)", raw[0])
	}
}
