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
