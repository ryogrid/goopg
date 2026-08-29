package transam

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// perf-optimize-take3/05 candidate E, slice 1: a lookup cache in front of the
// CLOG buffer pool.
//
// clogBufferPool guards its whole page set with ONE sync.Mutex
// (clog_bufferpool.go:133), and getStatus takes it on EVERY tuple visibility
// test: visibility.go:98-106 consults SeesCommittedXID on both branches of the
// HeapXminCommitted hint-bit test, and snapshot.go:199 puts the CLOG lookup
// before the `xid < s.Xmin` fast exit. That made CLog.GetStatus 13.9% of -N
// mutex delay.
//
// Upstream fronts the same lookup with a single-entry cache —
// cachedFetchXid/cachedFetchXidStatus, postgres/src/backend/access/transam/
// transam.c:33-62 — which is per-BACKEND because PG backends are processes.
// goopg's backends are goroutines sharing one CLog, so this is a small sharded
// array instead: same idea, but a hit costs one atomic load rather than a
// mutex acquisition, and different XIDs land in different slots instead of
// evicting each other.
//
// ONLY TERMINAL STATUSES ARE CACHED. Committed and Aborted are final and can
// never change, so a hit is always still true. In-progress (Unknown) is NOT
// cached: it is exactly the status that transitions, and memoising it would
// freeze a transaction as invisible forever. This is the same restriction
// upstream applies (it caches only on the committed path).

const (
	clogStatusCacheSize = 4096 // power of two
	clogStatusCacheMask = clogStatusCacheSize - 1

	// clogCacheValid marks a populated slot; a zero word is "empty", which is
	// also the zero value of the array, so no initialisation pass is needed.
	//
	// It lives at bit 8, BELOW the XID field, not at bit 63: an XID is a full
	// uint32, so bits 32..63 are entirely spoken for and a flag at bit 63 would
	// alias XID bit 31 — making every lookup for an XID >= 2^31 miss, and worse,
	// making `w>>32` disagree with the stored XID for every XID once the flag is
	// OR-ed in.
	clogCacheValid = uint64(1) << 8
)

// clogStatusCache maps XID -> terminal status. Each slot packs the XID and its
// status into ONE uint64 so a reader gets a self-consistent pair from a single
// atomic load — a separate xid array and status array could be read torn.
//
//	bits 32..63 : xid (full uint32)
//	bit 8       : valid
//	bits 0..7   : status
type clogStatusCache struct {
	slots [clogStatusCacheSize]atomic.Uint64
}

func clogCachePack(xid storage.TransactionID, st TxnStatus) uint64 {
	return clogCacheValid | uint64(xid)<<32 | uint64(uint8(st))
}

// lookup returns the cached terminal status for xid, if present.
func (c *clogStatusCache) lookup(xid storage.TransactionID) (TxnStatus, bool) {
	w := c.slots[uint32(xid)&clogStatusCacheMask].Load()
	if w&clogCacheValid == 0 || storage.TransactionID(w>>32) != xid {
		return TxnStatusUnknown, false
	}
	return TxnStatus(uint8(w)), true
}

// update reconciles the cache with a status WRITE: it memoises a terminal
// value and drops any stale entry otherwise.
//
// This is required, not optional. "Committed and Aborted are final" holds in
// steady state but NOT during recovery: MarkUnknownAsAborted sweeps
// in-progress lanes to Aborted and WAL replay then stamps the durable
// Committed over them (TestReplayCLogFromWAL_OverridesMarkUnknownAsAborted).
// A cache filled by a read between those two steps would otherwise pin the
// swept Aborted forever and lose a committed transaction.
func (c *clogStatusCache) update(xid storage.TransactionID, st TxnStatus) {
	if st == TxnStatusCommitted || st == TxnStatusAborted {
		c.slots[uint32(xid)&clogStatusCacheMask].Store(clogCachePack(xid, st))
		return
	}
	c.forget(xid)
}

// forget drops xid's entry if present, leaving other XIDs in the slot alone.
func (c *clogStatusCache) forget(xid storage.TransactionID) {
	slot := &c.slots[uint32(xid)&clogStatusCacheMask]
	if w := slot.Load(); w&clogCacheValid != 0 && storage.TransactionID(w>>32) == xid {
		slot.Store(0)
	}
}

// store memoises a TERMINAL status. Non-terminal statuses are ignored.
func (c *clogStatusCache) store(xid storage.TransactionID, st TxnStatus) {
	if st != TxnStatusCommitted && st != TxnStatusAborted {
		return
	}
	c.slots[uint32(xid)&clogStatusCacheMask].Store(clogCachePack(xid, st))
}

// invalidate drops every entry. Called on CLOG truncation: once status bytes
// for an XID range are gone, that XID range can be reused after wraparound, and
// a stale "committed" for a recycled XID would make an in-progress transaction
// visible. Truncation is a VACUUM-frequency event, so clearing 4096 words is
// irrelevant next to the correctness it buys.
func (c *clogStatusCache) invalidate() {
	for i := range c.slots {
		c.slots[i].Store(0)
	}
}
