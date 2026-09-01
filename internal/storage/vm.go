package storage

import "sync"

// vmKey identifies a heap relation (without fork) for Visibility Map lookups.
type vmKey struct{ DBOid, RelOid uint32 }

func vmKeyFor(rel RelFileNode) vmKey { return vmKey{rel.DBOid, rel.RelOid} }

// VM bit flags per heap block are declared in vm_fork.go (VMAllVisible=0x01,
// VMAllFrozen=0x02 — upstream visibilitymap.c two-bit layout, which the
// on-disk fork already implemented). Semantics:
//
//   - VMAllVisible: every tuple visible to all snapshots — index-only scans
//     skip the heap fetch; VACUUM may skip the page unless aggressive.
//   - VMAllFrozen: additionally, every tuple is at-or-below the freeze cutoff.
//     Skips of all-frozen pages never stall relfrozenxid advancement
//     (vacuumlazy.c skippedallvis counts only visible-not-frozen skips).

// VisibilityMap is the in-memory visibility map for heap relations (M0046-0004).
//
// Each heap page carries two bits (VMAllVisible / VMAllFrozen). VACUUM sets
// ALL_VISIBLE after verifying that all remaining tuples on a page have
// committed xmin < OldestXmin and no un-resolved xmax, and ALL_FROZEN when in
// addition every tuple is at-or-below the pass's freeze cutoff. Any insert,
// delete, or update on a page clears both bits.
//
// Thread-safe. All methods are nil-safe.
type VisibilityMap struct {
	mu    sync.RWMutex
	pages map[vmKey][]uint8 // indexed by BlockNumber; bitmask of VM* flags
}

// NewVisibilityMap allocates an empty VisibilityMap.
func NewVisibilityMap() *VisibilityMap {
	return &VisibilityMap{pages: make(map[vmKey][]uint8)}
}

func (v *VisibilityMap) bitsFor(key vmKey, blk BlockNumber) uint8 {
	blocks := v.pages[key]
	if int(blk) >= len(blocks) {
		return 0
	}
	return blocks[blk]
}

func (v *VisibilityMap) setBits(key vmKey, blk BlockNumber, mask uint8) {
	blocks := v.pages[key]
	for int(blk) >= len(blocks) {
		blocks = append(blocks, 0)
	}
	blocks[blk] |= mask
	v.pages[key] = blocks
}

func (v *VisibilityMap) clearBits(key vmKey, blk BlockNumber, mask uint8) {
	blocks := v.pages[key]
	if int(blk) < len(blocks) {
		blocks[blk] &^= mask
	}
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
	return v.bitsFor(key, blk)&VMAllVisible != 0
}

// AllFrozen reports whether every tuple on rel/blk is frozen (or older than
// any conceivable freeze cutoff). Skips of such pages never stall
// relfrozenxid advancement.
func (v *VisibilityMap) AllFrozen(rel RelFileNode, blk BlockNumber) bool {
	if v == nil {
		return false
	}
	key := vmKeyFor(rel)
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.bitsFor(key, blk)&VMAllFrozen != 0
}

// SetAllVisible marks rel/blk as ALL_VISIBLE.
func (v *VisibilityMap) SetAllVisible(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.setBits(key, blk, VMAllVisible)
}

// SetAllFrozen marks rel/blk as ALL_FROZEN (implies ALL_VISIBLE).
func (v *VisibilityMap) SetAllFrozen(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.setBits(key, blk, VMAllVisible|VMAllFrozen)
}

// ClearBlock clears BOTH bits for rel/blk. Called when any tuple on the page
// is inserted, deleted, or updated.
func (v *VisibilityMap) ClearBlock(rel RelFileNode, blk BlockNumber) {
	if v == nil {
		return
	}
	key := vmKeyFor(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.clearBits(key, blk, VMAllVisible|VMAllFrozen)
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
		// review/260831-2 ST-7: the comparison is circular (PG's
		// TransactionIdPrecedes in heap_page_is_all_visible); a plain `>=`
		// reads every pre-wraparound xmin as newer than a post-wraparound
		// horizon and permanently refuses the ALL_VISIBLE bit.
		if t.Header.Xmin == InvalidTransactionID || !XIDPrecedes(t.Header.Xmin, horizon) {
			return false
		}
		// Tuple must not be deleted. An updater-bearing multixact xmax
		// (IS_MULTI && !LOCK_ONLY) lands here as "deleted" and correctly fails
		// all-visible — the conservative direction needs no multixact resolution
		// (resolving could only ever mark MORE pages all-visible, and an
		// all-locker multi already carries LOCK_ONLY, so it is handled below).
		if t.Header.Xmax != InvalidTransactionID && !IsHeapTupleLockOnly(t.Header.Infomask) {
			return false
		}
	}
	return true
}

// PageAllFrozen reports whether every LP_NORMAL tuple on page p carries an
// xmin strictly below freezeBelow (already frozen or older than this pass's
// cutoff) and no unresolved xmax — i.e., after this pass the page can carry
// the ALL_FROZEN bit. A page with zero live tuples qualifies vacuously.
func PageAllFrozen(p Page, freezeBelow TransactionID) bool {
	if freezeBelow == InvalidTransactionID || freezeBelow == 0 {
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
			continue
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
		// review/260831-2 ST-7: circular comparison, as in PageAllVisible.
		if t.Header.Xmin == InvalidTransactionID || !XIDPrecedes(t.Header.Xmin, freezeBelow) {
			return false
		}
		if t.Header.Xmax != InvalidTransactionID && !IsHeapTupleLockOnly(t.Header.Infomask) {
			return false
		}
	}
	return true
}

// CountAllVisible returns the number of blocks with the ALL_VISIBLE bit set
// for rel (pg_class.relallvisible). 0 for nil receiver.
func (v *VisibilityMap) CountAllVisible(rel RelFileNode) int32 {
	if v == nil {
		return 0
	}
	key := vmKeyFor(rel)
	v.mu.RLock()
	defer v.mu.RUnlock()
	n := int32(0)
	for _, mask := range v.pages[key] {
		if mask&VMAllVisible != 0 {
			n++
		}
	}
	return n
}
