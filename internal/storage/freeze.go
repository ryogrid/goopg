package storage

import "encoding/binary"

// PageFreezeStats summarises one tuple-freeze pass over a heap page.
type PageFreezeStats struct {
	Frozen         int           // number of xmin values rewritten
	MinUnfrozenXID TransactionID // lowest xmin NOT frozen (0 = all frozen / none present)
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
				// Tuple is deleted — skip freezing (it will be vacuumed away).
				continue
			}
		}

		// Freeze: overwrite xmin with FrozenTransactionID in the page bytes.
		binary.LittleEndian.PutUint32(p[off:off+4], uint32(FrozenTransactionID))
		stats.Frozen++
	}
	return stats, nil
}
