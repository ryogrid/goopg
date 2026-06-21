package mvcc

import (
	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/storage"
)

// TupleVisible applies v0 MVCC visibility checks for a heap tuple
// header against a statement snapshot.
//
// Lock-only xmax (HEAP_XMAX_LOCK_ONLY) is recognised: when the
// xmax represents a row-lock holder rather than a deleter, the
// tuple is treated as if no xmax were set — readers see it as
// live regardless of whether the lock holder has committed,
// aborted, or is still in progress. Mirrors upstream's
// HeapTupleSatisfiesMVCC handling of the LOCK_ONLY bit.
//
// mxs is the process-shared MultiXact member store, used to resolve
// an updater-bearing multixact xmax (HEAP_XMAX_IS_MULTI set,
// LOCK_ONLY clear) back to the real updater transaction id before the
// xmax-based invisibility checks. It may be nil — see the multixact
// branch below — but callers on the live read path should pass the
// real store so a row genuinely updated under a shared lock is judged
// against its updater, not its (meaningless as an xid) MultiXactId.
func TupleVisible(h storage.HeapTupleHeader, snap Snapshot, currentXID storage.TransactionID, mxs *multixact.Store) bool {
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
		} else if !snap.SeesCommittedXID(h.Xmin) {
			// HeapXminCommitted cached that xmin committed, but this snapshot
			// may have taken before xmin committed (xmin was in-progress).
			return false
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

	// effXmax is the transaction id that actually updated/deleted the
	// tuple. We reach here only with xmax set and NOT lock-only. If the
	// IS_MULTI bit is set, h.Xmax is a MultiXactId (a row updated under a
	// shared lock: an updater plus one or more lockers), not a transaction
	// id — resolve the updater member so the self/snapshot checks below
	// reason about the real xid. Mirrors executor.isConcurrentlyUpdated and
	// upstream HeapTupleSatisfiesMVCC's HEAP_XMAX_IS_MULTI handling.
	effXmax := h.Xmax
	if storage.IsHeapTupleXmaxMulti(h.Infomask) {
		if mxs == nil {
			// The bits say an updater exists but we cannot resolve it. Treat
			// the row as validly updated/deleted (invisible) rather than
			// mis-reading the MultiXactId as a deleter xid — never expose a
			// version whose successor may already be committed. (No producer
			// emits non-lock-only multis yet, so this is unreachable today.)
			return false
		}
		members, ok := mxs.Members(multixact.MultiXactId(h.Xmax))
		if !ok {
			return false
		}
		upd, has := multixact.GetUpdateXid(members)
		if !has {
			// Only lockers in the multi (no updater): not a delete. An
			// all-locker multi should carry LOCK_ONLY and return above; treat
			// this inconsistent case as a pure row lock — tuple visible.
			return true
		}
		effXmax = upd
	}

	// Deleted by our own transaction: invisible.
	if effXmax == currentXID {
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
	return !snap.SeesCommittedXID(effXmax)
}
