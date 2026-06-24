package storage

import "encoding/binary"

// PageFreezeStats summarises one tuple-freeze pass over a heap page.
type PageFreezeStats struct {
	Frozen         int           // number of xmin values rewritten
	MinUnfrozenXID TransactionID // lowest xmin NOT frozen (0 = all frozen / none present)
	// FrozenSlots is the 1-based slot list of LP_NORMAL line pointers
	// whose tuple xmin was rewritten to FrozenTransactionID. Captured
	// for the M0080-0001 `RecordKindHeapFreeze` WAL record so replay
	// can reapply the same transformation deterministically. Always
	// in ascending order. (M0080-0001.)
	FrozenSlots []uint16
}

// PageFreezeOldTuples walks every LP_NORMAL tuple on p and rewrites the xmin
// of any "old enough" tuple to FrozenTransactionID, making it permanently
// visible to all snapshots. This prevents XID wraparound from rendering old
// tuples invisible.
//
// A tuple is eligible for freezing when:
//   - xmin is a real, committed XID (≠ Invalid, ≠ FrozenTransactionID)
//   - xmin < freezeBelow (the vacuum freeze horizon)
//   - the tuple is live: xmax == InvalidTransactionID or xmax is lock-only
//
// freezeBelow is typically (currentXID − vacuum_freeze_min_age). Passing
// freezeBelow = 0 is a no-op.
//
// The caller must hold the page's exclusive content lock.
// Returns PageFreezeStats with the count of frozen tuples and the lowest
// xmin NOT frozen (used to advance relfrozenxid).
func PageFreezeOldTuples(p Page, freezeBelow TransactionID) (PageFreezeStats, error) {
	var stats PageFreezeStats
	if freezeBelow == 0 {
		return stats, nil
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return stats, err
	}
	for idx := 0; idx < count; idx++ {
		item, err := readItemID(p, idx)
		if err != nil {
			return stats, err
		}
		if item.Flags != ItemIDNormal {
			continue
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) || off+SizeOfHeapTupleHeaderData > len(p) {
			continue // corrupt item, skip silently
		}

		// Read xmin from the raw page bytes (offset 0 in header).
		xmin := TransactionID(binary.LittleEndian.Uint32(p[off : off+4]))
		if xmin == InvalidTransactionID || xmin == FrozenTransactionID {
			continue
		}

		if xmin >= freezeBelow {
			// Not old enough — track for relfrozenxid computation.
			if stats.MinUnfrozenXID == 0 || xmin < stats.MinUnfrozenXID {
				stats.MinUnfrozenXID = xmin
			}
			continue
		}

		// Read xmax (bytes 4-8) to check whether the tuple is still live.
		xmax := TransactionID(binary.LittleEndian.Uint32(p[off+4 : off+8]))
		if xmax != InvalidTransactionID {
			// Read infomask (bytes 20-22) to check lock-only.
			infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
			if !IsHeapTupleLockOnly(infomask) {
				// A non-lock-only xmax marks the tuple deleted/updated, so its
				// xmin is not worth freezing — EXCEPT when xmax is an updater-
				// bearing multixact that, once resolved, holds only lockers (no
				// updater): then the row is still live and freezable. Here xmax
				// is a MultiXactId, not an xid, so resolve it through the member
				// store rather than trusting the raw value.
				deleted := true
				if IsHeapTupleXmaxMulti(infomask) && ResolveMultiUpdater != nil {
					if _, hasUpdater, resolved := ResolveMultiUpdater(xmax); resolved && !hasUpdater {
						deleted = false // all lockers: row still live
					}
				}
				if deleted {
					// Deleted (or an unresolvable multi we won't risk freezing) —
					// skip; vacuum will reclaim it.
					continue
				}
			}
		}

		// Freeze: overwrite xmin with FrozenTransactionID in the page bytes.
		binary.LittleEndian.PutUint32(p[off:off+4], uint32(FrozenTransactionID))
		stats.Frozen++
		stats.FrozenSlots = append(stats.FrozenSlots, uint16(idx+1))
	}
	return stats, nil
}

// PageFreezeBySlots is the deterministic replay kernel for
// `RecordKindHeapFreeze`. Given a 1-based ascending list of
// LP_NORMAL slots whose xmin should be rewritten to
// FrozenTransactionID, applies the rewrite verbatim. Returns
// an error if any slot is out of range or refers to a non-
// LP_NORMAL line pointer.
//
// Idempotent in practice: if the page has already been frozen
// (xmin == FrozenTransactionID at the listed slot), the rewrite
// is a no-op. The caller is responsible for the pd_lsn
// idempotency check before invoking this kernel.
//
// (M0080-0001.)
func PageFreezeBySlots(p Page, frozenSlots []uint16) error {
	if len(frozenSlots) == 0 {
		return nil
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		return err
	}
	for _, slot := range frozenSlots {
		if slot == 0 || int(slot) > count {
			return ErrInvalidSlot
		}
		idx := int(slot) - 1
		item, err := readItemID(p, idx)
		if err != nil {
			return err
		}
		if item.Flags != ItemIDNormal {
			// Slot was vacuumed away after the WAL record was
			// emitted but before this replay — nothing to freeze.
			continue
		}
		off := int(item.Offset)
		ln := int(item.Length)
		if off < 0 || ln < 0 || off+ln > len(p) || off+SizeOfHeapTupleHeaderData > len(p) {
			continue // corrupt; defensive skip
		}
		binary.LittleEndian.PutUint32(p[off:off+4], uint32(FrozenTransactionID))
	}
	return nil
}
