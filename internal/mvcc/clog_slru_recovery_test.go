package mvcc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEnablePGSLRUMirrorLoadsFromDisk verifies that EnablePGSLRUMirror wires a
// pool that reads committed/aborted statuses straight from existing SLRU
// segment files. The SLRU is fsynced at every commit, so it is the
// authoritative status source across a restart (M0106-0013).
func TestEnablePGSLRUMirrorLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")

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

	c := &CLog{}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	// After EnablePGSLRUMirror, GetStatus must reflect the on-disk SLRU state.
	if got := c.GetStatus(3); got != TxnStatusCommitted {
		t.Errorf("XID 3: got %v want TxnStatusCommitted", got)
	}
	if got := c.GetStatus(4); got != TxnStatusAborted {
		t.Errorf("XID 4: got %v want TxnStatusAborted", got)
	}
}

// TestHighestKnownXIDReturnsMaxTerminalXID verifies HighestKnownXID returns
// the highest XID that has a non-Unknown status in the clog, reading directly
// from the on-disk SLRU segment (M0117-0006 Part C: there is no resident
// "banks" store to populate first).
func TestHighestKnownXIDReturnsMaxTerminalXID(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	if err := os.MkdirAll(slruDir, 0700); err != nil {
		t.Fatal(err)
	}
	// XID 3: byte 0, lane 3 (shift 6) → Committed. XID 5: byte 1, lane 1
	// (shift 2) → Aborted. XID 7: byte 1, lane 3 (shift 6) → Committed.
	seg0 := make([]byte, storage.BlockSize)
	seg0[0] = pgClogStatusCommitted << 6
	seg0[1] = (pgClogStatusAborted << 2) | (pgClogStatusCommitted << 6)
	if err := os.WriteFile(filepath.Join(slruDir, "0000"), seg0, 0600); err != nil {
		t.Fatal(err)
	}

	c := &CLog{}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	got := c.HighestKnownXID()
	if got != storage.TransactionID(7) {
		t.Errorf("HighestKnownXID = %d, want 7", got)
	}
}

// TestHighestKnownXIDEmptyReturns0 verifies HighestKnownXID returns 0 when
// there are no terminal statuses (fresh or empty clog, mirror never enabled).
func TestHighestKnownXIDEmptyReturns0(t *testing.T) {
	c := &CLog{}
	if got := c.HighestKnownXID(); got != 0 {
		t.Errorf("HighestKnownXID on empty clog = %d, want 0", got)
	}
}

// TestPoolFaultsInShortSegmentFile verifies that a segment file shorter than
// one page is handled gracefully: real bytes decode correctly and the
// zero-filled tail reads back as Unknown, not an error. M0117-0006 Part C
// retired the pre-pool loadFromSLRU method this test used to pin directly;
// clogBufferPool.readPageFromDisk now owns this fault-in path.
func TestPoolFaultsInShortSegmentFile(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	if err := os.MkdirAll(slruDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Write a 2-byte segment (only covers XIDs 0-7 of page 0).
	// XID=3: byte 0 lane 3 → committed (0x40).
	seg := []byte{0x40, 0x00}
	if err := os.WriteFile(filepath.Join(slruDir, "0000"), seg, 0600); err != nil {
		t.Fatal(err)
	}

	c := &CLog{}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	if got := c.GetStatus(3); got != TxnStatusCommitted {
		t.Errorf("XID 3: got %v want TxnStatusCommitted", got)
	}
	// XID past the short file's real bytes must fault in as Unknown (the
	// zero-filled tail), not error.
	if got := c.GetStatus(100); got != TxnStatusUnknown {
		t.Errorf("XID 100 (past short file): got %v want TxnStatusUnknown", got)
	}
}
