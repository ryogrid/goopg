package mvcc

import "github.com/goopg/goopg/internal/storage"

// TupleVisible applies v0 MVCC visibility checks for a heap tuple
// header against a statement snapshot.
//
// Lock-only xmax (HEAP_XMAX_LOCK_ONLY) is recognised: when the
// xmax represents a row-lock holder rather than a deleter, the
// tuple is treated as if no xmax were set — readers see it as
// live regardless of whether the lock holder has committed,
// aborted, or is still in progress. Mirrors upstream's
// HeapTupleSatisfiesMVCC handling of the LOCK_ONLY bit.
func TupleVisible(h storage.HeapTupleHeader, snap Snapshot, currentXID storage.TransactionID) bool {
	// Creator not set is always invalid.
	if h.Xmin == storage.InvalidTransactionID {
		return false
	}

	// A lock-only xmax is not a delete — short-circuit the
	// xmax-based invisibility paths and treat as if no xmax
	// were set. Even our own xact's row lock doesn't hide the
	// tuple from us. This is the foundation for SELECT FOR
	// UPDATE / FOR SHARE: locking a row must not make
	// concurrent readers (or the locker itself on a re-scan)
	// blind to it.
	xmaxIsLockOnly := h.Xmax != storage.InvalidTransactionID && storage.IsHeapTupleLockOnly(h.Infomask)

	// Tuples created by our own transaction are visible to us unless we
	// already deleted them in the same transaction. A lock-only
	// self-stamp doesn't count as a delete.
	if h.Xmin == currentXID {
		if xmaxIsLockOnly {
			return true
		}
		return h.Xmax != currentXID
	}

	// M0115-0001: FrozenTransactionID fast path. Frozen tuples are universally
	// visible; skip all xmin snapshot arithmetic (CPU micro-optimisation only —
	// SeesCommittedXID would return true anyway via xid < Xmin).
	if h.Xmin != storage.FrozenTransactionID {
		// M0115-0002: hint-bit read path — skip SeesCommittedXID when the
		// result is already cached in t_infomask.
		if h.Infomask&storage.HeapXminInvalid != 0 {
			return false
		}
		if h.Infomask&storage.HeapXminCommitted == 0 {
			if !snap.SeesCommittedXID(h.Xmin) {
				return false
			}
		}
	}

	// No deleting transaction: tuple is visible.
	if h.Xmax == storage.InvalidTransactionID {
		return true
	}
	// Lock-only xmax — visible regardless of holder progress.
	if xmaxIsLockOnly {
		return true
	}
	// Deleted by our own transaction: invisible.
	if h.Xmax == currentXID {
		return false
	}
	// M0115-0002: hint-bit read for xmax.
	if h.Infomask&storage.HeapXmaxInvalid != 0 {
		return true
	}
	if h.Infomask&storage.HeapXmaxCommitted != 0 {
		return false
	}
	// Deleted by a transaction visible as committed to the snapshot:
	// invisible. Otherwise (future or still in-progress): visible.
	return !snap.SeesCommittedXID(h.Xmax)
}
