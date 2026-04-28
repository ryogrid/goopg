package mvcc

import "github.com/goopg/goopg/internal/storage"

// TupleVisible applies v0 MVCC visibility checks for a heap tuple
// header against a statement snapshot.
func TupleVisible(h storage.HeapTupleHeader, snap Snapshot, currentXID storage.TransactionID) bool {
	// Creator not set is always invalid.
	if h.Xmin == storage.InvalidTransactionID {
		return false
	}

	// Tuples created by our own transaction are visible to us unless we
	// already deleted them in the same transaction.
	if h.Xmin == currentXID {
		return h.Xmax != currentXID
	}
	if !snap.SeesCommittedXID(h.Xmin) {
		return false
	}

	// No deleting transaction: tuple is visible.
	if h.Xmax == storage.InvalidTransactionID {
		return true
	}
	// Deleted by our own transaction: invisible.
	if h.Xmax == currentXID {
		return false
	}
	// Deleted by a transaction visible as committed to the snapshot:
	// invisible. Otherwise (future or still in-progress): visible.
	return !snap.SeesCommittedXID(h.Xmax)
}
