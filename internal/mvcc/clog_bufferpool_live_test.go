package mvcc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0117-0006 Part B: the bounded SLRU buffer pool is the live in-memory CLOG
// store once EnablePGSLRUMirror runs. These tests pin that every public path
// routes through the pool (not the now-vestigial resident banks) and that the
// status set survives the production recovery reopen sequence.

// TestCLOGPoolIsLiveStore exercises the read/write hot path plus HighestKnownXID
// through the pool, spanning byte lanes, the first page boundary, and a second
// segment file so the pool faults and evicts across segments.
func TestCLOGPoolIsLiveStore(t *testing.T) {
	dir := t.TempDir()
	flatPath := filepath.Join(dir, "pg_xact_flat")
	slruDir := filepath.Join(dir, "pg_xact")

	c, err := OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	if c.pool.Load() == nil {
		t.Fatal("pool not promoted to live store after EnablePGSLRUMirror")
	}

	set := []xidStatus{
		{FirstNormalTransactionID, TxnStatusCommitted},
		{4, TxnStatusAborted},
		{5, TxnStatusSubCommitted},
		{storage.TransactionID(clogXactsPerPage) - 1, TxnStatusCommitted},
		{storage.TransactionID(clogXactsPerPage), TxnStatusAborted},
		{storage.TransactionID(clogXactsPerSegment) + 7, TxnStatusCommitted}, // segment 1
	}
	writeStatuses(t, c, set)

	for _, s := range set {
		if got := c.GetStatus(s.xid); got != s.want {
			t.Errorf("pool GetStatus(%d) = %d, want %d", s.xid, got, s.want)
		}
	}

	// HighestKnownXID scans the SLRU for the maximum terminal lane.
	wantHigh := storage.TransactionID(clogXactsPerSegment) + 7
	if got := c.HighestKnownXID(); got != wantHigh {
		t.Errorf("HighestKnownXID = %d, want %d", got, wantHigh)
	}

	// Idempotent re-set is a no-op (and must not error).
	if err := c.SetCommitted(FirstNormalTransactionID); err != nil {
		t.Fatalf("idempotent SetCommitted: %v", err)
	}

	// Full production recovery reopen reconstructs everything from the SLRU.
	reopened, err := OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("re-OpenCLog: %v", err)
	}
	if err := reopened.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("re-EnablePGSLRUMirror: %v", err)
	}
	for _, s := range set {
		if got := reopened.GetStatus(s.xid); got != s.want {
			t.Errorf("recovery GetStatus(%d) = %d, want %d", s.xid, got, s.want)
		}
	}
}

// TestCLOGPoolMarkUnknownAsAbortedThroughPool verifies the crash-recovery sweep
// (Unknown→Aborted) routes through the pool and preserves an already-committed
// lane (the M0117-0004 guard, now read via the pool).
func TestCLOGPoolMarkUnknownAsAbortedThroughPool(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	if err := c.SetCommitted(10); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	if err := c.MarkUnknownAsAborted(21); err != nil {
		t.Fatalf("MarkUnknownAsAborted: %v", err)
	}
	if got := c.GetStatus(10); got != TxnStatusCommitted {
		t.Errorf("XID 10 committed lane clobbered by sweep: got %d", got)
	}
	for _, xid := range []storage.TransactionID{3, 9, 11, 20} {
		if got := c.GetStatus(xid); got != TxnStatusAborted {
			t.Errorf("XID %d: got %d, want Aborted", xid, got)
		}
	}
}

// TestCLOGPoolTruncateInvalidates verifies TruncateCLOG removes the SLRU
// segments below the cutoff and invalidates the matching resident pool pages,
// while the retained high XID still reads its committed status.
func TestCLOGPoolTruncateInvalidates(t *testing.T) {
	dir := t.TempDir()
	slruDir := filepath.Join(dir, "pg_xact")
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	lowXID := storage.TransactionID(FirstNormalTransactionID + 1)
	highXID := storage.TransactionID(clogXactsPerSegment) + 100
	if err := c.SetCommitted(lowXID); err != nil {
		t.Fatalf("SetCommitted low: %v", err)
	}
	if err := c.SetCommitted(highXID); err != nil {
		t.Fatalf("SetCommitted high: %v", err)
	}
	// Truncate everything below segment 1's first page.
	if err := c.TruncateCLOG(storage.TransactionID(clogXactsPerSegment)); err != nil {
		t.Fatalf("TruncateCLOG: %v", err)
	}
	if _, err := os.Stat(filepath.Join(slruDir, "0000")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SLRU segment 0000 not removed after truncate: err=%v", err)
	}
	if got := c.GetStatus(highXID); got != TxnStatusCommitted {
		t.Errorf("retained high XID: got %d, want Committed", got)
	}
}
