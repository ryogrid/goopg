package transam

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func newCacheTestCLog(t *testing.T) *CLog {
	t.Helper()
	dir := t.TempDir()
	c, err := OpenCLog(filepath.Join(dir, "pg_xact_flat"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	// The SLRU buffer pool is the sole live store (M0117-0006 Part C); without
	// it setStatusWithLSN has no pool to write into.
	if err := c.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact")); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	return c
}

// perf-optimize-take3/05 candidate E. The cache is only sound if it memoises
// TERMINAL statuses. Caching a status that can still change would freeze a
// transaction's visibility — the single way this optimisation could corrupt
// reads.
func TestCLogStatusCacheOnlyMemoisesTerminalStatuses(t *testing.T) {
	t.Run("committed is cached and correct", func(t *testing.T) {
		c := newCacheTestCLog(t)
		const xid = FirstNormalTransactionID + 11
		if err := c.SetCommittedWithLSN(xid, 0); err != nil {
			t.Fatal(err)
		}
		for range 3 { // first read populates, later reads hit the cache
			if got := c.GetStatus(xid); got != TxnStatusCommitted {
				t.Fatalf("got %v, want Committed", got)
			}
		}
	})

	t.Run("aborted is cached and correct", func(t *testing.T) {
		c := newCacheTestCLog(t)
		const xid = FirstNormalTransactionID + 12
		if err := c.SetAborted(xid); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if got := c.GetStatus(xid); got != TxnStatusAborted {
				t.Fatalf("got %v, want Aborted", got)
			}
		}
	})

	// The two transitions that MUST still be observed after a prior read.
	t.Run("unknown -> committed is observed", func(t *testing.T) {
		c := newCacheTestCLog(t)
		const xid = FirstNormalTransactionID + 13
		if got := c.GetStatus(xid); got != TxnStatusUnknown {
			t.Fatalf("precondition: got %v, want Unknown", got)
		}
		if err := c.SetCommittedWithLSN(xid, 0); err != nil {
			t.Fatal(err)
		}
		if got := c.GetStatus(xid); got != TxnStatusCommitted {
			t.Fatalf("in-progress status was memoised: got %v after commit, want Committed", got)
		}
	})

	t.Run("sub-committed -> committed is observed", func(t *testing.T) {
		c := newCacheTestCLog(t)
		const xid = FirstNormalTransactionID + 14
		if err := c.SetSubCommitted(xid); err != nil {
			t.Fatal(err)
		}
		if got := c.GetStatus(xid); got != TxnStatusSubCommitted {
			t.Fatalf("precondition: got %v, want SubCommitted", got)
		}
		// SubCommitted is explicitly NOT terminal (clog.go:27-31): the parent
		// resolves it. Memoising it would strand the subtransaction.
		if err := c.SetCommittedWithLSN(xid, 0); err != nil {
			t.Fatal(err)
		}
		if got := c.GetStatus(xid); got != TxnStatusCommitted {
			t.Fatalf("SubCommitted was memoised: got %v after parent commit, want Committed", got)
		}
	})
}

// The sequence that broke the first version of this cache: a read populates it
// with a swept-Aborted status, then WAL replay stamps the durable Committed.
// Mirrors initdb's TestReplayCLogFromWAL_OverridesMarkUnknownAsAborted.
func TestCLogStatusCacheHonoursTerminalOverride(t *testing.T) {
	c := newCacheTestCLog(t)
	const xid = FirstNormalTransactionID + 300
	if err := c.MarkUnknownAsAborted(xid + 1); err != nil {
		t.Fatalf("MarkUnknownAsAborted: %v", err)
	}
	if got := c.GetStatus(xid); got != TxnStatusAborted {
		t.Fatalf("precondition: got %v, want Aborted", got)
	}
	// Replay now stamps the durable commit over the swept lane.
	if err := c.SetCommittedWithLSN(xid, 0); err != nil {
		t.Fatal(err)
	}
	if got := c.GetStatus(xid); got != TxnStatusCommitted {
		t.Fatalf("stale Aborted survived a terminal override: got %v, want Committed", got)
	}
}

// Truncation makes an XID range reusable after wraparound; a stale "committed"
// for a recycled XID would make an in-progress transaction visible.
func TestCLogStatusCacheInvalidatedOnTruncate(t *testing.T) {
	c := newCacheTestCLog(t)
	const xid = FirstNormalTransactionID + 500
	if err := c.SetCommittedWithLSN(xid, 0); err != nil {
		t.Fatal(err)
	}
	if got := c.GetStatus(xid); got != TxnStatusCommitted { // populate
		t.Fatalf("precondition: %v", got)
	}
	if _, ok := c.statusCache.lookup(xid); !ok {
		t.Fatal("precondition: xid should be cached")
	}
	if err := c.TruncateCLOG(FirstNormalTransactionID + 1000); err != nil {
		t.Fatalf("TruncateCLOG: %v", err)
	}
	if _, ok := c.statusCache.lookup(xid); ok {
		t.Fatal("truncation must drop the cache: a recycled XID could otherwise read a stale terminal status")
	}
}

// The packing must round-trip and must not confuse two XIDs sharing a slot.
func TestCLogStatusCacheSlotCollision(t *testing.T) {
	var cache clogStatusCache
	a := storage.TransactionID(FirstNormalTransactionID + 7)
	b := a + clogStatusCacheSize // same slot, different xid
	cache.store(a, TxnStatusCommitted)
	if st, ok := cache.lookup(a); !ok || st != TxnStatusCommitted {
		t.Fatalf("a: got %v ok=%v", st, ok)
	}
	if _, ok := cache.lookup(b); ok {
		t.Fatal("b shares a's slot but is a different XID: must miss, not alias")
	}
	cache.store(b, TxnStatusAborted)
	if st, ok := cache.lookup(b); !ok || st != TxnStatusAborted {
		t.Fatalf("b: got %v ok=%v", st, ok)
	}
	if _, ok := cache.lookup(a); ok {
		t.Fatal("a was displaced by b: must miss, not return b's status")
	}
	// Non-terminal statuses are never stored.
	cache.store(a, TxnStatusSubCommitted)
	if _, ok := cache.lookup(a); ok {
		t.Fatal("SubCommitted must not be cached")
	}
	cache.store(a, TxnStatusUnknown)
	if _, ok := cache.lookup(a); ok {
		t.Fatal("Unknown must not be cached")
	}
}
