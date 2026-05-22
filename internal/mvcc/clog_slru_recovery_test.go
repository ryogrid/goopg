package mvcc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEnablePGSLRUMirrorLoadsFromDisk verifies that EnablePGSLRUMirror reads
// committed/aborted statuses from existing SLRU segment files and merges them
// into c.data. This is the M0106-0013 crash-recovery fix: the SLRU is fsynced
// at every commit, so after a crash it is the authoritative status source even
// when the flat-file is stale or truncated.
func TestEnablePGSLRUMirrorLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	flatPath := filepath.Join(dir, "pg_xact_flat")

	// Prepare an SLRU segment with XID=3 committed, XID=4 aborted.
	// (Segment 0, page 0, byte 0: lanes 0=unknown, 1=unknown, 2=unknown,
	// 3=committed → bits 6-7 = 01 → byte = 0x40.
	// XID 4 is lane 0 of byte 1: bits 0-1 = 10 → byte = 0x02.)
	seg0 := make([]byte, storage.BlockSize)
	// XID=3: lane 3 of byte 0 → shift = 3*2 = 6 → bits = pgClogStatusCommitted<<6 = 0x40
	seg0[0] = pgClogStatusCommitted << 6
	// XID=4: lane 0 of byte 1 → shift = 0 → bits = pgClogStatusAborted = 0x02
	seg0[1] = pgClogStatusAborted
	if err := os.MkdirAll(slruDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slruDir, "0000"), seg0, 0600); err != nil {
		t.Fatal(err)
	}

	// Open clog with an empty flat-file (simulates truncated write after crash).
	c := &CLog{path: flatPath}

	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	// After EnablePGSLRUMirror, c.data must reflect the SLRU state.
	if got := c.GetStatus(3); got != TxnStatusCommitted {
		t.Errorf("XID 3: got %v want TxnStatusCommitted", got)
	}
	if got := c.GetStatus(4); got != TxnStatusAborted {
		t.Errorf("XID 4: got %v want TxnStatusAborted", got)
	}
}

// TestEnablePGSLRUMirrorFlatFileWinsIfNewer verifies that a committed status
// already in c.data (flat file) is preserved even if the SLRU byte happens to
// be zero for that XID (SLRU write lost in crash). The flat-file value wins
// via the "SLRU overrides Unknown" semantics: SLRU only overwrites Unknown
// entries, so a flat-file Committed slot survives.
func TestEnablePGSLRUMirrorFlatFileWinsIfNewer(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	flatPath := filepath.Join(dir, "pg_xact_flat")

	// Flat file has XID=3 committed but SLRU has it as zero (unknown).
	seg0 := make([]byte, storage.BlockSize) // all zero
	if err := os.MkdirAll(slruDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slruDir, "0000"), seg0, 0600); err != nil {
		t.Fatal(err)
	}

	// Pre-populate banks from "flat file" with XID=3 committed.
	c := &CLog{path: flatPath}
	c.distributeToBanks([]byte{0, byte(TxnStatusUnknown), byte(TxnStatusUnknown), byte(TxnStatusCommitted)})

	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	// XID=3 should still be committed (flat-file value, SLRU had it as unknown).
	if got := c.GetStatus(3); got != TxnStatusCommitted {
		t.Errorf("XID 3: got %v want TxnStatusCommitted (flat-file should win over SLRU unknown)", got)
	}
}

// TestHighestKnownXIDReturnsMaxTerminalXID verifies HighestKnownXID returns
// the highest XID that has a non-Unknown status in the clog.
func TestHighestKnownXIDReturnsMaxTerminalXID(t *testing.T) {
	c := &CLog{}
	flat := make([]byte, 10)
	flat[3] = byte(TxnStatusCommitted)
	flat[5] = byte(TxnStatusAborted)
	flat[7] = byte(TxnStatusCommitted)
	// flat[8], [9] remain Unknown.
	c.distributeToBanks(flat)

	got := c.HighestKnownXID()
	if got != storage.TransactionID(7) {
		t.Errorf("HighestKnownXID = %d, want 7", got)
	}
}

// TestHighestKnownXIDEmptyReturns0 verifies HighestKnownXID returns 0 when
// there are no terminal statuses (fresh or empty clog).
func TestHighestKnownXIDEmptyReturns0(t *testing.T) {
	c := &CLog{}
	if got := c.HighestKnownXID(); got != 0 {
		t.Errorf("HighestKnownXID on empty clog = %d, want 0", got)
	}
}

// TestLoadFromSLRU_SegFileNotMultipleOfBlockSize verifies that a segment file
// shorter than one page is handled gracefully (partial byte coverage).
func TestLoadFromSLRU_SegFileNotMultipleOfBlockSize(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	if err := os.MkdirAll(slruDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Write a 2-byte segment (only covers XIDs 0-7).
	// XID=3: byte 0 lane 3 → committed (0x40).
	seg := []byte{0x40, 0x00}
	if err := os.WriteFile(filepath.Join(slruDir, "0000"), seg, 0600); err != nil {
		t.Fatal(err)
	}

	c := &CLog{}
	if err := c.loadFromSLRU(slruDir); err != nil {
		t.Fatalf("loadFromSLRU: %v", err)
	}
	if got := c.GetStatus(3); got != TxnStatusCommitted {
		t.Errorf("XID 3: got %v want TxnStatusCommitted", got)
	}
}
