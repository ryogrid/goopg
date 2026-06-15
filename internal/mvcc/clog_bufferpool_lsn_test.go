package mvcc

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestLSNIndexInPage pins lsnIndexInPage against PG's GetLSNIndex intra-page
// term (clog.c:95-96): (xid % CLOG_XACTS_PER_PAGE) / CLOG_XACTS_PER_LSN_GROUP.
// Covers group boundaries, the last group of a page, and the wrap into the next
// page (where the index resets to 0).
func TestLSNIndexInPage(t *testing.T) {
	if clogLSNsPerPage != 1024 {
		t.Fatalf("clogLSNsPerPage = %d, want 1024 (CLOG_LSNS_PER_PAGE for BLCKSZ=8192)", clogLSNsPerPage)
	}
	cases := []struct {
		xid  storage.TransactionID
		want int
	}{
		{0, 0},
		{31, 0}, // last XID of group 0
		{32, 1}, // first XID of group 1
		{33, 1},
		{63, 1}, // last XID of group 1
		{64, 2},
		{clogXactsPerPage - 1, clogLSNsPerPage - 1}, // last group of the page (1023)
		{clogXactsPerPage, 0},                       // first XID of next page → resets
		{clogXactsPerPage + 32, 1},                  // group 1 of next page
		{clogXactsPerPage*2 + 64, 2},                // group 2 two pages on
	}
	for _, tc := range cases {
		if got := lsnIndexInPage(tc.xid); got != tc.want {
			t.Errorf("lsnIndexInPage(%d) = %d, want %d", tc.xid, got, tc.want)
		}
	}
}

// TestGroupLSNMaxSemantics verifies the group-LSN array is raised monotonically
// (≙ PG's `if (group_lsn[i] < lsn) group_lsn[i] = lsn`): set, raise, never
// lower; a second XID in the SAME 32-XID group shares (and can raise) the
// barrier; a zero LSN is a no-op.
func TestGroupLSNMaxSemantics(t *testing.T) {
	pool := newCLOGBufferPool(t.TempDir(), 4)
	const xid = storage.TransactionID(1000) // group 31 of page 0

	mustSet := func(x storage.TransactionID, lsn uint64) {
		t.Helper()
		if _, err := pool.setStatusWithLSN(x, TxnStatusCommitted, lsn); err != nil {
			t.Fatalf("setStatusWithLSN(%d,%d): %v", x, lsn, err)
		}
	}
	mustLSN := func(x storage.TransactionID, want uint64) {
		t.Helper()
		got, err := pool.groupLSNFor(x)
		if err != nil {
			t.Fatalf("groupLSNFor(%d): %v", x, err)
		}
		if got != want {
			t.Fatalf("groupLSNFor(%d) = %d, want %d", x, got, want)
		}
	}

	mustSet(xid, 500)
	mustLSN(xid, 500)

	mustSet(xid, 900) // raise
	mustLSN(xid, 900)

	mustSet(xid, 400) // attempt lower — must NOT lower
	mustLSN(xid, 900)

	mustSet(xid, 0) // zero LSN — no-op
	mustLSN(xid, 900)

	// A different XID in the same 32-XID group shares the barrier and can raise
	// it. xid=1000 → group 1000/32 = 31; pick another XID in [992,1023].
	sibling := storage.TransactionID(1005)
	if lsnIndexInPage(sibling) != lsnIndexInPage(xid) {
		t.Fatalf("test setup: %d and %d not in the same LSN group", xid, sibling)
	}
	mustLSN(sibling, 900) // sees the shared barrier
	mustSet(sibling, 1500)
	mustLSN(xid, 1500) // raised for the original XID too

	// A neighbouring group is independent.
	other := storage.TransactionID(2000) // group 62
	if lsnIndexInPage(other) == lsnIndexInPage(xid) {
		t.Fatalf("test setup: %d unexpectedly shares a group with %d", other, xid)
	}
	mustLSN(other, 0)
}

// TestGroupLSNZeroedOnReopen guards the "WAL already flushed for on-disk pages"
// invariant: the group LSN is in-memory only (zeroed when a page faults in),
// while the status BITS survive a flush + fresh-pool reopen because the data
// page is durable.
func TestGroupLSNZeroedOnReopen(t *testing.T) {
	dir := t.TempDir()
	const xid = storage.TransactionID(777)

	pool := newCLOGBufferPool(dir, 4)
	if _, err := pool.setStatusWithLSN(xid, TxnStatusCommitted, 4242); err != nil {
		t.Fatalf("setStatusWithLSN: %v", err)
	}
	if got, _ := pool.groupLSNFor(xid); got != 4242 {
		t.Fatalf("pre-flush groupLSNFor = %d, want 4242", got)
	}
	if err := pool.flushDirty(); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}

	// Reopen: a fresh pool faults the page in from disk. The status bit is
	// durable; the group LSN is not persisted ⇒ reads back 0.
	reopened := newCLOGBufferPool(dir, 4)
	st, err := reopened.getStatus(xid)
	if err != nil {
		t.Fatalf("getStatus after reopen: %v", err)
	}
	if st != TxnStatusCommitted {
		t.Fatalf("status after reopen = %v, want Committed (data page must be durable)", st)
	}
	lsn, err := reopened.groupLSNFor(xid)
	if err != nil {
		t.Fatalf("groupLSNFor after reopen: %v", err)
	}
	if lsn != 0 {
		t.Fatalf("groupLSN after reopen = %d, want 0 (must not be persisted)", lsn)
	}
}

// TestFlushWALBarrierFiresBeforeWrite installs a flushWAL spy and asserts it is
// called with a page's max group LSN BEFORE the page bytes reach disk, for both
// flushDirty and eviction-driven writeback. A nil hook (the default) is never
// called.
func TestFlushWALBarrierFiresBeforeWrite(t *testing.T) {
	t.Run("default-off", func(t *testing.T) {
		pool := newCLOGBufferPool(t.TempDir(), 2)
		// flushWAL is nil; setting + flushing must not panic and must not error.
		if _, err := pool.setStatusWithLSN(10, TxnStatusCommitted, 999); err != nil {
			t.Fatalf("setStatusWithLSN: %v", err)
		}
		if err := pool.flushDirty(); err != nil {
			t.Fatalf("flushDirty with nil hook: %v", err)
		}
	})

	t.Run("flushDirty-barrier", func(t *testing.T) {
		pool := newCLOGBufferPool(t.TempDir(), 4)
		var flushedTo uint64
		var flushCalls int
		// The hook records the LSN; the pool's WriteAt has not yet run when the
		// hook fires (flushWALBeforeWriteLocked precedes the writeback loop), so
		// observing the call at all proves the ordering for flushDirty.
		pool.flushWAL = func(lsn uint64) error {
			flushCalls++
			flushedTo = lsn
			return nil
		}
		// Two XIDs in different groups on the same page → page max = 8000.
		if _, err := pool.setStatusWithLSN(100, TxnStatusCommitted, 3000); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.setStatusWithLSN(2000, TxnStatusCommitted, 8000); err != nil {
			t.Fatal(err)
		}
		if err := pool.flushDirty(); err != nil {
			t.Fatalf("flushDirty: %v", err)
		}
		if flushCalls != 1 {
			t.Fatalf("flushWAL called %d times, want 1", flushCalls)
		}
		if flushedTo != 8000 {
			t.Fatalf("flushWAL got LSN %d, want page max 8000", flushedTo)
		}
	})

	t.Run("eviction-barrier-ordering", func(t *testing.T) {
		dir := t.TempDir()
		pool := newCLOGBufferPool(dir, 1) // single slot → next pin evicts
		var flushed []uint64
		pool.flushWAL = func(lsn uint64) error {
			flushed = append(flushed, lsn)
			return nil
		}

		// Page 0 dirty with LSN 5000.
		if _, err := pool.setStatusWithLSN(50, TxnStatusCommitted, 5000); err != nil {
			t.Fatal(err)
		}
		// Pin a page in a different segment → evicts page 0 (dirty), which must
		// flush WAL(5000) BEFORE writing page 0 back.
		far := storage.TransactionID(clogXactsPerPage * slruPagesPerSegment * 3)
		if _, err := pool.getStatus(far); err != nil {
			t.Fatalf("getStatus(far) triggering eviction: %v", err)
		}
		if len(flushed) != 1 || flushed[0] != 5000 {
			t.Fatalf("eviction flush sequence = %v, want [5000]", flushed)
		}

		// The evicted page's status bit must be durable on disk.
		reopened := newCLOGBufferPool(dir, 4)
		st, err := reopened.getStatus(50)
		if err != nil {
			t.Fatalf("getStatus(50) after eviction+reopen: %v", err)
		}
		if st != TxnStatusCommitted {
			t.Fatalf("evicted status = %v, want Committed", st)
		}
	})
}
