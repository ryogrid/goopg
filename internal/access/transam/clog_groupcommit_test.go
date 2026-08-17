package transam

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// Historical name: this file exercised the M0117-0005 group-commit Treiber
// stack, deleted in C2-S4 (the commit path performs no eager write-back any
// more, so there is nothing to batch). The test remains valuable as a pure
// CONCURRENCY oracle: many concurrent SetCommitted/SetAborted stampers
// against one CLog must never lose or mis-apply a lane, observed through
// three views — live GetStatus, a full recovery reopen, and an independent
// SLRU byte decode (after an explicit FlushAll — C2-S1).

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

	// XIDs span >1 SLRU page so stampers cross segment boundaries concurrently. Use a
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
			t.Errorf("recovery-reopened GetStatus(%d) = %d, want %d (flushed concurrent write lost)", xid, got, st)
		}
	}

	// View 3: SLRU-only reconstruction (durable PG-compatible mirror).
	fresh := freshFromSLRU(t, slruDir)
	for xid, st := range want {
		if got := fresh.GetStatus(xid); got != st {
			t.Errorf("SLRU-derived GetStatus(%d) = %d, want %d (concurrent SLRU stamping dropped a lane)", xid, got, st)
		}
	}
}
