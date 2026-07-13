package mvcc

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0117-0005: group commit. M0117-0006 Part C retired the incremental
// flat-file flush this file used to also pin (TestIncrementalFlushMatchesWholeFile,
// TestGroupCommitIdempotentResetsDirty) — the flat file and its dirty-page
// tracking no longer exist; the SLRU buffer pool is the sole store, and its own
// idempotent-set behavior is pinned directly in clog_bufferpool_test.go
// (TestCLOGBufferPoolIdempotentSet). What remains here pins that the
// group-commit Treiber stack never loses or mis-applies an update under heavy
// concurrency: every committed/aborted XID is durably readable from BOTH a
// fresh recovery reopen AND an independent SLRU-only reconstruction after the
// writers finish.

// TestGroupCommitConcurrent fans out many concurrent committers against one
// CLog with the SLRU mirror enabled. Each goroutine sets a distinct XID to a
// terminal status; afterwards every status must survive a recovery reopen and
// an SLRU-only reconstruction (the crash-recovery / standby path). A lost
// update from a Treiber-stack race, or an SLRU lane dropped by the per-segment
// batching, shows up as a mismatch here.
func TestGroupCommitConcurrent(t *testing.T) {
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

	// XIDs span >1 SLRU page so the leader batches across segments. Use a
	// deterministic want per XID.
	const n = 2000
	want := make(map[storage.TransactionID]TxnStatus, n)
	xids := make([]storage.TransactionID, 0, n)
	for i := 0; i < n; i++ {
		// Spread XIDs: small block, across the first page boundary, and into a
		// second page (clogXactsPerPage apart) to exercise multi-page batching.
		xid := FirstNormalTransactionID + storage.TransactionID(i*37)
		if i%5 == 0 {
			xid = storage.TransactionID(clogXactsPerPage) + FirstNormalTransactionID + storage.TransactionID(i*37)
		}
		st := TxnStatusCommitted
		if i%3 == 0 {
			st = TxnStatusAborted
		}
		want[xid] = st
		xids = append(xids, xid)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(xids))
	for _, xid := range xids {
		wg.Add(1)
		go func(xid storage.TransactionID, st TxnStatus) {
			defer wg.Done()
			var e error
			if st == TxnStatusCommitted {
				e = c.SetCommitted(xid)
			} else {
				e = c.SetAborted(xid)
			}
			if e != nil {
				errs <- e
			}
		}(xid, want[xid])
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent Set: %v", e)
	}

	// View 1: live in-memory.
	for xid, st := range want {
		if got := c.GetStatus(xid); got != st {
			t.Errorf("in-memory GetStatus(%d) = %d, want %d", xid, got, st)
		}
	}

	// C2-S1: group commits no longer write eagerly at commit; flush before
	// the on-disk views (View 2 recovery reopen + View 3 SLRU decode).
	if err := c.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	// View 2: full production recovery reopen (OpenCLog + EnablePGSLRUMirror).
	// The SLRU is the sole durable store, so the recovery path reconstructs
	// every status from the flushed SLRU segments (C2-S1: bytes reach disk
	// via the explicit FlushAll above, not per commit) — proving the group
	// group-commit write landed durably across a restart.
	reopened, err := OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("re-OpenCLog: %v", err)
	}
	if err := reopened.EnablePGSLRUMirror(slruDir); err != nil {
		t.Fatalf("re-EnablePGSLRUMirror: %v", err)
	}
	for xid, st := range want {
		if got := reopened.GetStatus(xid); got != st {
			t.Errorf("recovery-reopened GetStatus(%d) = %d, want %d (flushed group write lost)", xid, got, st)
		}
	}

	// View 3: SLRU-only reconstruction (durable PG-compatible mirror).
	fresh := freshFromSLRU(t, slruDir)
	for xid, st := range want {
		if got := fresh.GetStatus(xid); got != st {
			t.Errorf("SLRU-derived GetStatus(%d) = %d, want %d (group SLRU batch dropped a lane)", xid, got, st)
		}
	}
}
