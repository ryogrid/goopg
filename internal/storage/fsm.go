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
	// chunkMax[key][c] is the exact maximum of
	// pages[key][c*fsmChunkBlocks : (c+1)*fsmChunkBlocks], maintained by
	// RecordFreeSpace. It is kept only for relations larger than one chunk:
	// below that the plain scan is already cheaper than any bookkeeping, so
	// single-chunk relations keep exactly their previous behaviour. review/260831 ST-5/ST-6: the lookups used to scan every
	// registered page of the relation, so choosing an insert target cost
	// O(relation size) — 22 µs per insert on a 100k-page (800 MB) table, and
	// growing. A chunk whose maximum is below what the caller needs cannot
	// contain an answer, so the scan skips it wholesale; on an append-heavy
	// relation (every page but the tail full) that leaves one chunk to look at.
	// This is the same idea as PG's FSM tree, flattened to one level because
	// the reader only ever asks "is there anything here at all".
	chunkMax map[fsmKey][]uint16
}

// fsmChunkBlocks is how many heap blocks one chunkMax entry summarises. 512
// blocks (4 MB of heap) keeps the summary at 1/512th the size of the page
// array while bounding the work RecordFreeSpace does when it has to recompute
// a chunk maximum.
const fsmChunkBlocks = 512

// NewFSM allocates an empty FSM.
func NewFSM() *FSM {
	return &FSM{pages: make(map[fsmKey][]uint16), chunkMax: make(map[fsmKey][]uint16)}
}

// chunkMaxLocked returns the chunk summary for key, building it if this is the
// first write since the relation outgrew one chunk. The WRITE lock must be
// held: readers never build a summary, they fall back to a plain scan.
func (f *FSM) chunkMaxLocked(key fsmKey) []uint16 {
	if cm, ok := f.chunkMax[key]; ok {
		return cm
	}
	cm := buildChunkMax(f.pages[key])
	if f.chunkMax == nil {
		f.chunkMax = make(map[fsmKey][]uint16)
	}
	f.chunkMax[key] = cm
	return cm
}

// buildChunkMax computes the whole summary for a page array from scratch.
func buildChunkMax(pages []uint16) []uint16 {
	n := (len(pages) + fsmChunkBlocks - 1) / fsmChunkBlocks
	cm := make([]uint16, n)
	for blk, free := range pages {
		if c := blk / fsmChunkBlocks; free > cm[c] {
			cm[c] = free
		}
	}
	return cm
}

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
	pages := f.pages[key]
	cm := f.chunkMax[key]
	if len(cm) == 0 {
		// Single-chunk relation (or one whose summary has not been built):
		// the plain scan is the fallback, and is what it always did.
		for blk, free := range pages {
			if free >= minFreeBytes {
				return BlockNumber(blk), true
			}
		}
		return 0, false
	}
	for c, cmax := range cm {
		if cmax < minFreeBytes {
			continue // no page in this chunk can qualify
		}
		lo := c * fsmChunkBlocks
		hi := min(lo+fsmChunkBlocks, len(pages))
		for blk := lo; blk < hi; blk++ {
			if pages[blk] >= minFreeBytes {
				return BlockNumber(blk), true
			}
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
	// No summary (single-chunk relation, or one not yet summarised): one
	// all-permitting entry makes the loop below the plain full scan.
	cm := f.chunkMax[key]
	if len(cm) == 0 {
		cm = []uint16{^uint16(0)}
	}
	type entry struct {
		free uint16
		blk  BlockNumber
	}
	// Insertion sort over a small fixed-size buffer (n is typically 4).
	// Each scan slot: (free uint16, blk BlockNumber). When a new page
	// beats the smallest kept candidate, slide it in and drop the tail.
	kept := make([]entry, 0, n)
	for c, cmax := range cm {
		if cmax < minFreeBytes {
			continue // no page in this chunk can qualify
		}
		if len(kept) == n && cmax <= kept[n-1].free {
			// Nothing here can displace the weakest kept candidate: the
			// per-page test below is `>`, so a tie loses to the lower block
			// number that is already kept.
			continue
		}
		lo := c * fsmChunkBlocks
		hi := min(lo+fsmChunkBlocks, len(pages))
		for blk := lo; blk < hi; blk++ {
			free := pages[blk]
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
	if need := int(blk) + 1; need > len(pages) {
		// review/260831 ST-5: one growth step, not one append per block. A
		// VACUUM or an extension that jumps to a high block number used to
		// walk every intervening index.
		grown := make([]uint16, need)
		copy(grown, pages)
		pages = grown
		f.pages[key] = pages
	}
	old := pages[blk]
	pages[blk] = freeBytes

	if len(pages) <= fsmChunkBlocks {
		// Single-chunk relation: no summary is kept, and any summary left by
		// an earlier, larger incarnation of this key would be stale.
		delete(f.chunkMax, key)
		return
	}
	// Writers own the summary outright — readers only read it — so a relation
	// that has just outgrown one chunk gets its summary built here, once.
	cm := f.chunkMaxLocked(key)
	c := int(blk) / fsmChunkBlocks
	for len(cm) <= c {
		cm = append(cm, 0)
	}
	switch {
	case freeBytes >= cm[c]:
		cm[c] = freeBytes
	case old == cm[c]:
		// The page that HELD this chunk's maximum just lost space, so the
		// maximum has to be recomputed — bounded to one chunk, and only on
		// the path that actually lowers it.
		lo := c * fsmChunkBlocks
		hi := min(lo+fsmChunkBlocks, len(pages))
		m := uint16(0)
		for i := lo; i < hi; i++ {
			if pages[i] > m {
				m = pages[i]
			}
		}
		cm[c] = m
	}
	f.chunkMax[key] = cm
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
	delete(f.chunkMax, key)
}
