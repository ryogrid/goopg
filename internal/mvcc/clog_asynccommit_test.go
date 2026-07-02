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

// TestCLogSetCommittedWithLSNDefersFlush pins the M0117-0007 Part B
// continuation (lazy async-commit write-back): SetCommittedWithLSN records
// xid's commit LSN on its CLOG page and makes the new status immediately
// readable in-memory, but — unlike the mechanical-wiring-only behavior this
// test used to assert — does NOT itself force a durable write-back. The
// group-commit flush (and hence the flushWAL barrier) only fires on a later
// event: here, a subsequent FlushAll (the checkpointer's drain call).
func TestCLogSetCommittedWithLSNDefersFlush(t *testing.T) {
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

	if flushCalls != 0 {
		t.Fatalf("flushWAL hook called %d times immediately after SetCommittedWithLSN, want 0 (write-back must be deferred)", flushCalls)
	}
	// The status must already be readable in-memory even though the page is
	// still dirty — GetStatus reads the resident buffer pool, not disk.
	if got := c.GetStatus(xid); got != TxnStatusCommitted {
		t.Fatalf("GetStatus(%d) = %v, want Committed", xid, got)
	}

	// FlushAll (the checkpointer's drain call) is the backstop that bounds
	// how long the page can stay dirty; it must fire the barrier with the
	// XID's recorded LSN.
	if err := c.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if flushCalls != 1 {
		t.Fatalf("flushWAL hook called %d times after FlushAll, want 1", flushCalls)
	}
	if flushedTo != lsn {
		t.Fatalf("flushWAL got LSN %d, want %d", flushedTo, lsn)
	}
	if got := c.GetStatus(xid); got != TxnStatusCommitted {
		t.Fatalf("GetStatus(%d) = %v, want Committed after flush", xid, got)
	}
}

// TestCLogFlushAllBeforePoolExistsIsNoop mirrors
// TestCLogSetFlushWALHookBeforePoolExistsIsNoop for FlushAll: calling it
// before EnablePGSLRUMirror has created the pool must not panic.
func TestCLogFlushAllBeforePoolExistsIsNoop(t *testing.T) {
	c, err := OpenCLog(filepath.Join(t.TempDir(), "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := c.FlushAll(); err != nil {
		t.Fatalf("FlushAll on a pool-less CLog: %v", err)
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
