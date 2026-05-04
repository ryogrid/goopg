package storage

import "sync"

// vmKey identifies a heap relation (without fork) for Visibility Map lookups.
type vmKey struct{ DBOid, RelOid uint32 }

func vmKeyFor(rel RelFileNode) vmKey { return vmKey{rel.DBOid, rel.RelOid} }

// VisibilityMap is the in-memory visibility map for heap relations (M0046-0004).
//
// Each heap page has an ALL_VISIBLE bit. When set, every tuple on the page is
// known to be visible to ALL active and future snapshots, so an index scan can
// return data directly from the index key without fetching the heap page.
//
// VACUUM sets ALL_VISIBLE after verifying that all remaining tuples on a page
// have committed xmin < OldestXmin and no xmax. Any insert, delete, or update
// on a page clears its bit.
//
// Thread-safe. All methods are nil-safe.
type VisibilityMap struct {
	mu    sync.RWMutex
	pages map[vmKey][]bool // indexed by BlockNumber; true = ALL_VISIBLE
}

// NewVisibilityMap allocates an empty VisibilityMap.
func NewVisibilityMap() *VisibilityMap {
	return &VisibilityMap{pages: make(map[vmKey][]bool)}
}

// AllVisible reports whether all tuples on rel/blk are visible to every
// snapshot. Returns false for any nil receiver or unknown block.
func (v *VisibilityMap) AllVisible(rel RelFileNode, blk BlockNumber) bool {
	if v == nil {
		return false
	}
	key := vmKeyFor(rel)
	v.mu.RLock()
	defer v.mu.RUnlock()
	blocks := v.pages[key]
	if int(blk) >= len(blocks) {
		return false
	}
	return blocks[blk]
}

// SetAllVisible marks rel/blk as ALL_VISIBLE.
func (v *VisibilityMap) SetAllVisible(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	blocks := v.pages[key]
	for int(blk) >= len(blocks) {
		blocks = append(blocks, false)
	}
	blocks[blk] = true
	v.pages[key] = blocks
}

// ClearBlock clears the ALL_VISIBLE bit for rel/blk. Called when any tuple
// on the page is inserted, deleted, or updated.
func (v *VisibilityMap) ClearBlock(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	blocks := v.pages[key]
	if int(blk) < len(blocks) {
		blocks[blk] = false
	}
}

// DropRelation removes all VM entries for rel. Called on DROP TABLE / TRUNCATE
// to prevent stale visibility bits from being returned for future relations
// with the same OID.
func (v *VisibilityMap) DropRelation(rel RelFileNode) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.pages, key)
}

// PageAllVisible checks whether every LP_NORMAL tuple on the page p has a
// committed xmin before horizon and no valid xmax (i.e. not deleted). Used by
// VACUUM to decide whether to set the ALL_VISIBLE bit after a page prune.
//
// Returns false on any parse error to keep the caller's logic conservative.
func PageAllVisible(p Page, horizon TransactionID) bool {
	if horizon == InvalidTransactionID {
		return false
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return false
	}
	for idx := 0; idx < count; idx++ {
		item, err := readItemID(p, idx)
		if err != nil {
			return false
		}
		if item.Flags != ItemIDNormal {
			continue // unused / redirect — skip
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) {
			return false
		}
		t, err := ParseHeapTuple(p[off : off+ln])
		if err != nil {
			return false
		}
		// Tuple must have a committed xmin before the global horizon.
		if t.Header.Xmin == InvalidTransactionID || t.Header.Xmin >= horizon {
			return false
		}
		// Tuple must not be deleted.
		if t.Header.Xmax != InvalidTransactionID && !IsHeapTupleLockOnly(t.Header.Infomask) {
			return false
		}
	}
	return true
}
