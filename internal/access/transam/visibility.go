package transam

import (
	"github.com/goopg/goopg/internal/access/transam/multixact"
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
// curcid is the current command id within this transaction, used for the
// cmin/cmax comparison on self-inserted tuples (PG's
// HeapTupleSatisfiesMVCC HEAPTUPLE_INSERT_IN_PROGRESS arm). Callers
// without a command counter (VACUUM, tests) pass InvalidCommandId
// (~uint32(0)) so that all self-inserted tuples are visible regardless
// of cmin.
//
// combo is the per-connection ComboCIDStore, used to resolve combo CIDs
// back to real cmin/cmax values. It may be nil when no per-connection
// store exists (VACUUM, tests).
//
// mxs is the process-shared MultiXact member store, used to resolve
// an updater-bearing multixact xmax (HEAP_XMAX_IS_MULTI set,
// LOCK_ONLY clear) back to the real updater transaction id before the
// xmax-based invisibility checks. It may be nil — see the multixact
// branch below — but callers on the live read path should pass the
// real store so a row genuinely updated under a shared lock is judged
// against its updater, not its (meaningless as an xid) MultiXactId.
func TupleVisible(h storage.HeapTupleHeader, snap Snapshot, currentXID storage.TransactionID, curcid storage.CommandId, combo *ComboCIDStore, mxs *multixact.Store) bool {
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
	//
	// M0129-S8.3e: add cmin/cmax comparison mirroring PG's
	// HeapTupleSatisfiesMVCC HEAPTUPLE_INSERT_IN_PROGRESS arm. When
	// curcid is InvalidCommandId (0), cmin >= 0 is true for tuples
	// with cmin=0 — so self-inserted tuples at command 0 are hidden
	// from command 0's own scans (the inserting statement never
	// re-scans its own writes through TupleVisible). A subsequent
	// statement's incremented curcid passes the check.
	if h.Xmin == currentXID {
		if xmaxIsLockOnly {
			return true
		}
		// cmin check: a tuple inserted by a later command (cmin >= curcid)
		// is not yet visible, mirroring PG's HeapTupleSatisfiesMVCC
		// HEAPTUPLE_INSERT_IN_PROGRESS arm. A caller that bypasses the
		// dispatch-layer per-statement CommandCounterIncrement (raw
		// Build+Open+Next in tests) must advance ctx.CmdID itself, otherwise
		// self-inserted tuples remain hidden even from a later statement.
		cmin := GetCmin(h, combo)
		if cmin >= curcid {
			return false
		}
		// Self-deleted tuple: if deleted by a later command (cmax >= curcid),
		// show the pre-image (still visible). If deleted by an earlier command,
		// the tuple is gone.
		if h.Xmax == currentXID {
			cmax := GetCmax(h, combo)
			if cmax >= curcid {
				return true // deleted by later command — pre-image visible
			}
			return false // deleted by earlier command — gone
		}
		return true
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
			// version whose successor may already be committed.
			//
			// M0131-S24: this is NOT unreachable (an earlier revision of this
			// comment claimed it was). executor.stampUpdaterXmaxNonHOT
			// (M0118-0004) emits updater-bearing multis, and a hosted PG's
			// stream carries them via XLHL_XMAX_IS_MULTI. The failure mode is
			// silent: an unresolvable multi xmax HIDES the row rather than
			// erroring.
			return false
		}
		members, ok := mxs.Members(multixact.MultiXactId(h.Xmax))
		if !ok {
			// Same silent-hiding caveat as the nil-store arm above, and it is
			// live after a restart: M0131-S20.4 seeds the MultiXactId counter
			// from pg_control but the member SETS are still process-local and
			// transient, so every pre-restart multi xmax is unresolvable here.
			// The durable pg_multixact SLRU that fixes it is M0131-S24.
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

	// Deleted by our own transaction: visibility depends on
	// whether the delete happened before or after the scan's
	// command counter (mirrors PG's HeapTupleSatisfiesMVCC).
	// cmax >= curcid → pre-image visible.
	// cmax <  curcid → invisible (deleted by earlier command).
	if effXmax == currentXID {
		cmax := GetCmax(h, combo)
		if cmax >= curcid {
			return true
		}
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
