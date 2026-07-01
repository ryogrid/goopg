package mvcc

import (
	"path/filepath"
	"testing"
)

// M0117-0007 Part B: CLog.SetFlushWALHook / SetCommittedWithLSN wire the
// Part A write barrier to a live WAL writer. These tests exercise the
// CLog-level API (not the pool-level API clog_bufferpool_lsn_test.go
// already covers) end to end.

// TestCLogSetFlushWALHookBeforePoolExistsIsNoop pins the documented
// out-of-order-call contract: calling SetFlushWALHook before
// EnablePGSLRUMirror has created the pool must not panic.
func TestCLogSetFlushWALHookBeforePoolExistsIsNoop(t *testing.T) {
	c, err := OpenCLog(filepath.Join(t.TempDir(), "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	c.SetFlushWALHook(func(lsn uint64) error { return nil })
}

// TestCLogSetCommittedWithLSNFiresBarrierOnFlush proves the whole Part B
// chain: SetCommittedWithLSN records xid's commit LSN on its CLOG page, and
// the hook installed via SetFlushWALHook is invoked with that LSN when the
// page is later durably written back (TruncateCLOG-independent — this is
// the same group-commit flush every SetCommitted call already goes through).
func TestCLogSetCommittedWithLSNFiresBarrierOnFlush(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact")); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}

	var flushCalls int
	var flushedTo uint64
	c.SetFlushWALHook(func(lsn uint64) error {
		flushCalls++
		flushedTo = lsn
		return nil
	})

	const xid = FirstNormalTransactionID + 41
	const lsn = uint64(123456)
	if err := c.SetCommittedWithLSN(xid, lsn); err != nil {
		t.Fatalf("SetCommittedWithLSN: %v", err)
	}

	if flushCalls != 1 {
		t.Fatalf("flushWAL hook called %d times, want 1 (SetCommittedWithLSN's own group-commit flush)", flushCalls)
	}
	if flushedTo != lsn {
		t.Fatalf("flushWAL got LSN %d, want %d", flushedTo, lsn)
	}
	if got := c.GetStatus(xid); got != TxnStatusCommitted {
		t.Fatalf("GetStatus(%d) = %v, want Committed", xid, got)
	}
}

// TestCLogSetCommittedNoLSNNeverFiresBarrier pins the sync-commit-path
// invariant this design deliberately preserves: plain SetCommitted (lsn=0,
// used by every synchronous commit and by WAL replay) must NEVER trigger the
// flushWAL hook, so a synchronous commit never pays a second, redundant
// FlushUpTo round trip on top of the explicit one the caller already made.
func TestCLogSetCommittedNoLSNNeverFiresBarrier(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact")); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	flushCalls := 0
	c.SetFlushWALHook(func(lsn uint64) error {
		flushCalls++
		return nil
	})

	const xid = FirstNormalTransactionID + 42
	if err := c.SetCommitted(xid); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	if flushCalls != 0 {
		t.Fatalf("flushWAL hook called %d times for a plain SetCommitted, want 0", flushCalls)
	}
	if got := c.GetStatus(xid); got != TxnStatusCommitted {
		t.Fatalf("GetStatus(%d) = %v, want Committed", xid, got)
	}
}
