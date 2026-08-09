package storage

import "sync"

// fsmKey identifies a heap relation (without fork) for FSM lookups.
type fsmKey struct{ DBOid, RelOid uint32 }

func fsmKeyFor(rel RelFileNode) fsmKey { return fsmKey{rel.DBOid, rel.RelOid} }

// FSM is the in-memory free-space map for heap relations (M0046-0003).
//
// For each heap page it records an approximate count of free bytes,
// allowing writeHeapRow to find an existing page with enough room before
// extending the relation. Entries are populated by VACUUM after reclaiming
// dead tuples, and updated (decremented) after each successful tuple insert.
//
// Thread-safe: all methods serialise through a RWMutex.
type FSM struct {
	mu    sync.RWMutex
	pages map[fsmKey][]uint16 // indexed by BlockNumber
}

// NewFSM allocates an empty FSM.
func NewFSM() *FSM { return &FSM{pages: make(map[fsmKey][]uint16)} }

// GetPageWithFreeSpace returns the block number of a heap page with at
// least minFreeBytes available, and true. Returns (0, false) when no
// such page is registered in the FSM (e.g. VACUUM has not yet run).
//
// The returned block may be stale (another writer could have consumed
// the free space since the FSM entry was recorded). Callers must handle
// a failed insert gracefully (invalidate the FSM entry and retry).
func (f *FSM) GetPageWithFreeSpace(rel RelFileNode, minFreeBytes uint16) (BlockNumber, bool) {
	if f == nil || minFreeBytes == 0 {
		return 0, false
	}
	key := fsmKeyFor(rel)
	f.mu.RLock()
	defer f.mu.RUnlock()
	for blk, free := range f.pages[key] {
		if free >= minFreeBytes {
			return BlockNumber(blk), true
		}
	}
	return 0, false
}


// GetCandidates returns up to n block numbers whose registered free-space
// estimate is ≥ minFreeBytes, ordered by free-space descending (most-free
// first). Used by the insert path to choose among multiple candidate
// pages — see docs/design/perf-optimize/07-wal-fsm-insert.md §3 and
// docs/design/0107-0007b-fsm-get-candidates.md. (M0107-0007 slice C.)
//
// Like GetPageWithFreeSpace, the returned blocks may be stale (another
// writer could have consumed the free space since the FSM entry was
// recorded). Callers must handle a failed insert gracefully.
//
// Returns nil when n <= 0, minFreeBytes == 0, no page qualifies, or
// f is nil. Among ties (equal free-space estimates), the lowest block
// number is returned first — deterministic for reproducible plans.
func (f *FSM) GetCandidates(rel RelFileNode, minFreeBytes uint16, n int) []BlockNumber {
	if f == nil || n <= 0 || minFreeBytes == 0 {
		return nil
	}
	key := fsmKeyFor(rel)
	f.mu.RLock()
	defer f.mu.RUnlock()
	pages := f.pages[key]
	if len(pages) == 0 {
		return nil
	}
	type entry struct {
		free uint16
		blk  BlockNumber
	}
	// Insertion sort over a small fixed-size buffer (n is typically 4).
	// Each scan slot: (free uint16, blk BlockNumber). When a new page
	// beats the smallest kept candidate, slide it in and drop the tail.
	kept := make([]entry, 0, n)
	for blk, free := range pages {
		if free < minFreeBytes {
			continue
		}
		e := entry{free: free, blk: BlockNumber(blk)}
		if len(kept) < n {
			kept = append(kept, e)
			for i := len(kept) - 1; i > 0 && kept[i].free > kept[i-1].free; i-- {
				kept[i], kept[i-1] = kept[i-1], kept[i]
			}
			continue
		}
		if e.free <= kept[n-1].free {
			continue
		}
		kept[n-1] = e
		for i := n - 1; i > 0 && kept[i].free > kept[i-1].free; i-- {
			kept[i], kept[i-1] = kept[i-1], kept[i]
		}
	}
	if len(kept) == 0 {
		return nil
	}
	out := make([]BlockNumber, len(kept))
	for i, e := range kept {
		out[i] = e.blk
	}
	return out
}

// RecordFreeSpace stores the approximate free bytes for one heap page.
// A value of 0 marks the page as full; subsequent GetPageWithFreeSpace
// calls won't return it until a positive value is recorded again.
func (f *FSM) RecordFreeSpace(rel RelFileNode, blk BlockNumber, freeBytes uint16) {
	if f == nil {
		return
	}
	key := fsmKeyFor(rel)
	f.mu.Lock()
	defer f.mu.Unlock()
	pages := f.pages[key]
	for int(blk) >= len(pages) {
		pages = append(pages, 0)
	}
	pages[blk] = freeBytes
	f.pages[key] = pages
}

// RecordFreeSpaceForPage reads the page header's FreeSpace() and records
// it in the FSM for rel/blk. Convenience wrapper for VACUUM and insert
// paths that already have the page pinned.
func (f *FSM) RecordFreeSpaceForPage(rel RelFileNode, blk BlockNumber, p Page) {
	if f == nil {
		return
	}
	free := MustHeader(p).FreeSpace()
	if free < 0 {
		free = 0
	}
	f.RecordFreeSpace(rel, blk, uint16(free))
}

// DropRelation removes all FSM entries for rel. Called on DROP TABLE /
// TRUNCATE to prevent stale entries from directing inserts to non-existent
// pages.
func (f *FSM) DropRelation(rel RelFileNode) {
	if f == nil {
		return
	}
	key := fsmKeyFor(rel)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pages, key)
}
