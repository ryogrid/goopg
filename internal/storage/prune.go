package storage

// PruneResult carries the actions taken by PagePruneOpt so the caller can
// emit a WAL record with sufficient information for crash-safe replay.
type PruneResult struct {
	// Redirects is a list of (oldSlot, newSlot) pairs: line pointers that
	// were converted from ItemIDNormal to ItemIDRedirect. The index still
	// points to oldSlot; the redirect leads to the live chain tip newSlot.
	Redirects [][2]uint16
	// Unused is the list of 1-based slot numbers marked ItemIDUnused.
	// These are HOT-only dead tuples and standalone deleted tuples.
	Unused []uint16
}

// PagePruneOpt is the opportunistic page-pruning entry point (M0046-0002).
// It reclaims dead heap tuples inline when the HOT-update path encounters a
// full page, avoiding an unnecessary relation extension.
//
// A tuple is considered universally dead when:
//   - xmax != InvalidTransactionID (a deleting transaction exists)
//   - xmax is not lock-only (it's a genuine delete, not a row lock)
//   - xmax < oldestXmin (the deleting transaction is committed and older
//     than all active snapshots — no future reader can observe this tuple)
//
// For HOT chain roots (NOT HEAP_ONLY_TUPLE): the line pointer is converted to
// ItemIDRedirect pointing to the live chain tip so the index entry remains
// valid. For HOT-only and standalone dead tuples: the line pointer is marked
// ItemIDUnused so future inserts can reuse the slot.
//
// After building the PruneResult, callers must:
//  1. Apply redirects via PageSetItemIDRedirect (already done internally).
//  2. Call VacuumHeapPageBySlots(p, result.Unused) to compact the page.
//  3. Emit a WAL record carrying the PruneResult.
//
// The caller must hold the page's exclusive content lock for the entire call.
func PagePruneOpt(p Page, oldestXmin TransactionID) (PruneResult, error) {
	var result PruneResult

	if oldestXmin == InvalidTransactionID {
		return result, nil
	}

	h := MustHeader(p)
	pruneXID := TransactionID(h.PruneXID())
	if pruneXID == InvalidTransactionID || pruneXID >= oldestXmin {
		// Fast path: pd_prune_xid not set or not old enough.
		return result, nil
	}

	isDead := func(hdr HeapTupleHeader) bool {
		if hdr.Xmax == InvalidTransactionID {
			return false
		}
		if IsHeapTupleLockOnly(hdr.Infomask) {
			return false
		}
		// For an updater-bearing multixact xmax, hdr.Xmax is a MultiXactId, not
		// a transaction id; comparing it to the oldestXmin horizon would be a
		// category error (it could spuriously mark a live, only-locked row dead
		// and prune it). Resolve the updater member and test that xid instead. A
		// multi with no updater (only lockers) is not a delete, and an
		// unresolvable multi is conservatively NOT dead — never prune a tuple we
		// cannot prove dead.
		effXmax := hdr.Xmax
		if IsHeapTupleXmaxMulti(hdr.Infomask) {
			if ResolveMultiUpdater == nil {
				return false
			}
			upd, hasUpdater, resolved := ResolveMultiUpdater(hdr.Xmax)
			if !resolved || !hasUpdater {
				return false
			}
			effXmax = upd
		}
		return effXmax < oldestXmin
	}

	count, err := PageLinePointerCount(p)
	if err != nil {
		return result, err
	}

	for idx := 0; idx < count; idx++ {
		slot := uint16(idx + 1)
		item, err := readItemID(p, idx)
		if err != nil {
			return result, err
		}
		if item.Flags != ItemIDNormal {
			continue
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) {
			continue // skip corrupt items silently
		}
		t, err := ParseHeapTuple(p[off : off+ln])
		if err != nil {
			continue
		}
		if !isDead(t.Header) {
			continue
		}

		if t.Header.Infomask&HeapOnlyTuple != 0 {
			// HOT-only: not pointed to by any index → mark unused.
			result.Unused = append(result.Unused, slot)
		} else if t.Header.Infomask&HeapHotUpdated != 0 {
			// HOT chain root: the B-tree index points here. Follow the
			// chain to the live tip and create a redirect.
			liveTip := pruneChainTip(p, t.Header.CTID.Offset, isDead)
			if liveTip != 0 && liveTip != slot {
				if err := PageSetItemIDRedirect(p, slot, liveTip); err != nil {
					return result, err
				}
				result.Redirects = append(result.Redirects, [2]uint16{slot, liveTip})
			} else {
				// Entire chain is dead or self-referencing → mark unused.
				result.Unused = append(result.Unused, slot)
			}
		} else {
			// Standalone dead tuple (non-HOT delete): mark unused.
			result.Unused = append(result.Unused, slot)
		}
	}

	if len(result.Redirects) == 0 && len(result.Unused) == 0 {
		return result, nil
	}

	// Compact the page: mark unused slots and repack live tuple data.
	if len(result.Unused) > 0 {
		if _, err := VacuumHeapPageBySlots(p, result.Unused); err != nil {
			return result, err
		}
	} else {
		// No unused slots but we have redirects: the tuple data for the
		// redirected slots needs to be freed. Run a compaction pass with
		// an empty dead set — VacuumHeapPageBySlots will zero the region
		// and repack surviving ItemIDNormal tuples, which is sufficient.
		if _, err := VacuumHeapPageBySlots(p, nil); err != nil {
			return result, err
		}
	}

	// Clear pd_prune_xid: page is pruned; next check is a no-op until
	// new dead-tuple xmax values accumulate.
	h.SetPruneXID(0)
	return result, nil
}

// pruneChainTip follows the CTID chain starting at startSlot and returns
// the slot of the first tuple that is NOT dead (the live chain tip).
// Returns 0 when the entire chain is dead or the chain exceeds the depth limit.
func pruneChainTip(p Page, startSlot uint16, isDead func(HeapTupleHeader) bool) uint16 {
	const maxChain = 64
	cur := startSlot
	for i := 0; i < maxChain; i++ {
		item, err := readItemID(p, int(cur)-1)
		if err != nil || item.Flags != ItemIDNormal {
			return 0
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) {
			return 0
		}
		t, err := ParseHeapTuple(p[off : off+ln])
		if err != nil {
			return 0
		}
		if !isDead(t.Header) {
			return cur // live slot found
		}
		if t.Header.CTID.Offset == cur {
			return 0 // self-reference guard
		}
		cur = t.Header.CTID.Offset
	}
	return 0
}
