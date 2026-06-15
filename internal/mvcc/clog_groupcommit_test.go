package mvcc

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0117-0005: group commit + incremental flat-file flush.
//
// These tests pin two properties of the durable-write path:
//   - the group-commit Treiber stack never loses or mis-applies an update under
//     heavy concurrency (every committed/aborted XID is durably readable from
//     BOTH the flat file and the PG-compatible SLRU mirror after the writers
//     finish); and
//   - the incremental flat-file flush (only-dirty-pages WriteAt) produces a flat
//     file byte-equivalent to the legacy whole-file rewrite for every XID,
//     including across page and bank boundaries.

// TestGroupCommitConcurrent fans out many concurrent committers against one
// CLog with the SLRU mirror enabled. Each goroutine sets a distinct XID to a
// terminal status; afterwards every status must survive a flat-file reopen and
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

	// XIDs span >1 SLRU page and >1 bank so the leader batches across segments
	// and the flat writer across banks. Use a deterministic want per XID.
	const n = 2000
	want := make(map[storage.TransactionID]TxnStatus, n)
	xids := make([]storage.TransactionID, 0, n)
	for i := 0; i < n; i++ {
		// Spread XIDs: small block, across the first page boundary, and into a
		// second bank (xidsPerBank apart) to exercise multi-bank flat flush.
		xid := FirstNormalTransactionID + storage.TransactionID(i*37)
		if i%5 == 0 {
			xid = storage.TransactionID(xidsPerBank) + FirstNormalTransactionID + storage.TransactionID(i*37)
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

	// View 2: flat file reopened from disk (proves incremental pages landed).
	reopened, err := OpenCLog(flatPath)
	if err != nil {
		t.Fatalf("re-OpenCLog: %v", err)
	}
	for xid, st := range want {
		if got := reopened.GetStatus(xid); got != st {
			t.Errorf("flat-reopened GetStatus(%d) = %d, want %d (incremental flush lost an update)", xid, got, st)
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

// TestIncrementalFlushMatchesWholeFile asserts the incremental (only-dirty-page)
// flat writer yields a flat file identical to what the legacy whole-file
// rewrite would produce, for a sparse set spanning multiple pages and banks.
// We compare the reopened-from-flat view against a reference CLog flushed with
// the whole-file flush().
func TestIncrementalFlushMatchesWholeFile(t *testing.T) {
	set := []xidStatus{
		{FirstNormalTransactionID, TxnStatusCommitted},
		{4, TxnStatusAborted},
		{5, TxnStatusCommitted},
		{clogFlatPageSize - 1, TxnStatusAborted},      // last XID of flat page 0
		{clogFlatPageSize, TxnStatusCommitted},        // first XID of flat page 1
		{clogFlatPageSize + 1, TxnStatusSubCommitted}, // sub-committed across the page line
		{xidsPerBank - 1, TxnStatusCommitted},         // last XID of bank 0
		{xidsPerBank, TxnStatusAborted},               // first XID of bank 1
		{xidsPerBank + 9999, TxnStatusCommitted},
	}

	// Incremental path: the normal Set* API (setStatus → group commit →
	// incremental page flush).
	incDir := t.TempDir()
	incPath := filepath.Join(incDir, "pg_xact_flat")
	inc, err := OpenCLog(incPath)
	if err != nil {
		t.Fatalf("OpenCLog(inc): %v", err)
	}
	writeStatuses(t, inc, set)

	// Reference path: set the bytes then force a whole-file flush().
	refDir := t.TempDir()
	refPath := filepath.Join(refDir, "pg_xact_flat")
	ref, err := OpenCLog(refPath)
	if err != nil {
		t.Fatalf("OpenCLog(ref): %v", err)
	}
	writeStatuses(t, ref, set)
	if err := ref.flush(); err != nil { // whole-file rewrite over the same state
		t.Fatalf("ref.flush(): %v", err)
	}

	// Reopen both from their flat files and compare every XID in the set.
	incReopen, err := OpenCLog(incPath)
	if err != nil {
		t.Fatalf("re-OpenCLog(inc): %v", err)
	}
	refReopen, err := OpenCLog(refPath)
	if err != nil {
		t.Fatalf("re-OpenCLog(ref): %v", err)
	}
	for _, s := range set {
		gotInc := incReopen.GetStatus(s.xid)
		gotRef := refReopen.GetStatus(s.xid)
		if gotInc != s.want {
			t.Errorf("incremental flat GetStatus(%d) = %d, want %d", s.xid, gotInc, s.want)
		}
		if gotInc != gotRef {
			t.Errorf("incremental vs whole-file mismatch at xid %d: incremental=%d whole-file=%d", s.xid, gotInc, gotRef)
		}
	}
}

// TestGroupCommitIdempotentResetsDirty verifies that re-setting an XID to the
// same status is a no-op (no spurious flush) and that the dirty-page set is
// drained by each flush so it does not grow without bound.
func TestGroupCommitIdempotentResetsDirty(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	const xid = FirstNormalTransactionID + 1
	if err := c.SetCommitted(xid); err != nil {
		t.Fatalf("SetCommitted: %v", err)
	}
	// After a successful group flush the dirty set must be empty.
	c.dirtyMu.Lock()
	n := len(c.dirtyPages)
	c.dirtyMu.Unlock()
	if n != 0 {
		t.Errorf("dirtyPages not drained after flush: %d entries", n)
	}
	// Idempotent re-set: must not re-dirty anything.
	if err := c.SetCommitted(xid); err != nil {
		t.Fatalf("SetCommitted (idempotent): %v", err)
	}
	c.dirtyMu.Lock()
	n = len(c.dirtyPages)
	c.dirtyMu.Unlock()
	if n != 0 {
		t.Errorf("idempotent re-set dirtied %d pages, want 0", n)
	}
}
