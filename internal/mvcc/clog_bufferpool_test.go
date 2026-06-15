package mvcc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEffectiveCLOGBuffers pins the transaction_buffers → resident-page-budget
// resolution against PG's clog.c:CLOGShmemBuffers semantics.
func TestEffectiveCLOGBuffers(t *testing.T) {
	cases := []struct {
		name       string
		txnBuffers int
		shared     int
		want       int
	}{
		{"auto-no-shared-floor", 0, 0, 16},
		{"auto-small-shared-floor", 0, 1000, 16},   // 1000/512=1 → floor 16
		{"auto-mid-shared", 0, 16384, 32},          // 16384/512=32 → 32
		{"auto-rounds-to-bank", 0, 16384 + 511, 32}, // 32.99 → 32, bank-aligned
		{"auto-caps-at-1024", 0, 1 << 30, 1024},    // huge → 1024 cap
		{"explicit-floors-at-16", 5, 0, 16},
		{"explicit-passthrough", 64, 0, 64},
		{"explicit-caps", clogMaxAllowedBuffers + 1000, 0, clogMaxAllowedBuffers},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveCLOGBuffers(tc.txnBuffers, tc.shared); got != tc.want {
				t.Fatalf("EffectiveCLOGBuffers(%d,%d) = %d, want %d",
					tc.txnBuffers, tc.shared, got, tc.want)
			}
		})
	}
}

// TestCLOGBufferPoolRoundTripAllLanes verifies every terminal status round-trips
// through the pool and survives a flush + fresh-pool reopen (durability), and
// that two XIDs packed into the same byte do not clobber each other.
func TestCLOGBufferPoolRoundTripAllLanes(t *testing.T) {
	dir := t.TempDir()
	pool := newCLOGBufferPool(dir, 8)

	// Four XIDs sharing one byte (lanes 0..3 of the first byte of page 0):
	// XIDs FirstNormalTransactionID-aligned would split bytes; pick a byte
	// boundary explicitly. clogXactsPerByte == 4 so XIDs 100..103 share byte 25.
	want := map[storage.TransactionID]TxnStatus{
		100: TxnStatusCommitted,
		101: TxnStatusAborted,
		102: TxnStatusSubCommitted,
		103: TxnStatusCommitted,
		// a high XID on a different page to exercise multi-page residency
		clogXactsPerPage + 7: TxnStatusAborted,
	}
	for xid, st := range want {
		changed, err := pool.setStatus(xid, st)
		if err != nil {
			t.Fatalf("setStatus(%d,%v): %v", xid, st, err)
		}
		if !changed {
			t.Fatalf("setStatus(%d,%v) reported no change on first write", xid, st)
		}
	}
	for xid, st := range want {
		got, err := pool.getStatus(xid)
		if err != nil {
			t.Fatalf("getStatus(%d): %v", xid, err)
		}
		if got != st {
			t.Fatalf("getStatus(%d) = %v, want %v (packing clobber?)", xid, got, st)
		}
	}

	// Flush and reopen with a brand-new pool over the same dir: the durable
	// segment files must reproduce every lane.
	if err := pool.flushDirty(); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	reopened := newCLOGBufferPool(dir, 8)
	for xid, st := range want {
		got, err := reopened.getStatus(xid)
		if err != nil {
			t.Fatalf("reopened getStatus(%d): %v", xid, err)
		}
		if got != st {
			t.Fatalf("after reopen getStatus(%d) = %v, want %v", xid, got, st)
		}
	}
}

// TestCLOGBufferPoolIdempotentSet verifies a repeat set of the same lane is a
// no-op (changed == false), matching setStatus's idempotency contract.
func TestCLOGBufferPoolIdempotentSet(t *testing.T) {
	pool := newCLOGBufferPool(t.TempDir(), 4)
	if changed, err := pool.setStatus(500, TxnStatusCommitted); err != nil || !changed {
		t.Fatalf("first set: changed=%v err=%v, want true/nil", changed, err)
	}
	if changed, err := pool.setStatus(500, TxnStatusCommitted); err != nil || changed {
		t.Fatalf("repeat set: changed=%v err=%v, want false/nil", changed, err)
	}
	// A different terminal status DOES change the lane (clear-then-set).
	if changed, err := pool.setStatus(500, TxnStatusAborted); err != nil || !changed {
		t.Fatalf("overwrite set: changed=%v err=%v, want true/nil", changed, err)
	}
	if got, _ := pool.getStatus(500); got != TxnStatusAborted {
		t.Fatalf("after overwrite getStatus(500) = %v, want Aborted", got)
	}
}

// TestCLOGBufferPoolLRUEviction drives more distinct pages than the pool has
// slots and verifies (a) the resident set never exceeds the budget, (b) an
// evicted dirty page is written back to disk and reads correctly afterwards.
func TestCLOGBufferPoolLRUEviction(t *testing.T) {
	dir := t.TempDir()
	const nslots = 2
	pool := newCLOGBufferPool(dir, nslots)

	// Touch three different pages; with 2 slots one must be evicted.
	xids := []storage.TransactionID{
		1*clogXactsPerPage + 1, // page 1
		2*clogXactsPerPage + 2, // page 2
		3*clogXactsPerPage + 3, // page 3
	}
	for _, xid := range xids {
		if _, err := pool.setStatus(xid, TxnStatusCommitted); err != nil {
			t.Fatalf("setStatus(%d): %v", xid, err)
		}
		if rp := pool.residentPages(); rp > nslots {
			t.Fatalf("resident pages %d exceeds budget %d", rp, nslots)
		}
	}
	if rp := pool.residentPages(); rp != nslots {
		t.Fatalf("final resident pages = %d, want %d", rp, nslots)
	}

	// Page 1 was the first touched and should have been evicted (and, being
	// dirty, written back). Reading it back must reconstruct from disk.
	if got, err := pool.getStatus(xids[0]); err != nil || got != TxnStatusCommitted {
		t.Fatalf("evicted-then-reloaded getStatus(%d) = %v err=%v, want Committed/nil",
			xids[0], got, err)
	}
	// The evicted page's segment file must exist on disk (proof of writeback).
	seg1 := filepath.Join(dir, "0000") // pages 1..31 live in segment 0
	if _, err := os.Stat(seg1); err != nil {
		t.Fatalf("expected segment 0000 written back on eviction: %v", err)
	}
}

// TestCLOGBufferPoolEncodingMatchesSLRUMirror is the sibling-path equivalence
// check: the byte layout the pool writes to a segment file must be identical to
// what the existing CLog.mirrorToSLRUUnlocked writer produces for the same set
// of (xid, status) pairs. encode↔encode parity guards against the pool drifting
// from the canonical on-disk format the rest of CLog (and PG standbys) read.
func TestCLOGBufferPoolEncodingMatchesSLRUMirror(t *testing.T) {
	pairs := []struct {
		xid    storage.TransactionID
		status TxnStatus
	}{
		{FirstNormalTransactionID, TxnStatusCommitted},
		{FirstNormalTransactionID + 1, TxnStatusAborted},
		{FirstNormalTransactionID + 2, TxnStatusSubCommitted},
		{1234, TxnStatusCommitted},
		{1235, TxnStatusCommitted},
		{clogXactsPerPage + 9, TxnStatusAborted},
		{clogXactsPerPage*2 + 4, TxnStatusSubCommitted},
	}

	// Path A: the existing per-XID SLRU mirror writer.
	dirA := t.TempDir()
	clog := &CLog{slruDir: dirA}
	for _, p := range pairs {
		if err := clog.mirrorToSLRUUnlocked(p.xid, p.status); err != nil {
			t.Fatalf("mirrorToSLRUUnlocked(%d,%v): %v", p.xid, p.status, err)
		}
	}

	// Path B: the buffer pool.
	dirB := t.TempDir()
	pool := newCLOGBufferPool(dirB, 8)
	for _, p := range pairs {
		if _, err := pool.setStatus(p.xid, p.status); err != nil {
			t.Fatalf("pool.setStatus(%d,%v): %v", p.xid, p.status, err)
		}
	}
	if err := pool.flushDirty(); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}

	// Compare every segment file the mirror produced against the pool's. The
	// mirror extends only up to the touched page; the pool writes whole BLCKSZ
	// pages, so compare on the mirror's (shorter-or-equal) length prefix — the
	// pool's tail is zero-padding for untouched lanes, which the mirror also
	// leaves zero.
	entries, err := os.ReadDir(dirA)
	if err != nil {
		t.Fatalf("readdir A: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("mirror wrote no segment files")
	}
	for _, e := range entries {
		a, err := os.ReadFile(filepath.Join(dirA, e.Name()))
		if err != nil {
			t.Fatalf("read A/%s: %v", e.Name(), err)
		}
		b, err := os.ReadFile(filepath.Join(dirB, e.Name()))
		if err != nil {
			t.Fatalf("read B/%s: %v", e.Name(), err)
		}
		for i := range a {
			var bb byte
			if i < len(b) {
				bb = b[i]
			}
			if a[i] != bb {
				t.Fatalf("segment %s byte %d: mirror=0x%02x pool=0x%02x (encoding drift)",
					e.Name(), i, a[i], bb)
			}
		}
	}
}
