package transam

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
)

// procarray.go — per-backend slot array for lock-free MVCC snapshot construction.
// Each procSlot occupies exactly one CPU cache line (64 B) to prevent false sharing.

// DefaultProcArraySize is the number of per-backend slots in the ProcArray.
// It caps the number of concurrent connections + background workers.
const DefaultProcArraySize = 1024

const defaultProcArraySize = DefaultProcArraySize

// ReservedPreparedSlots is the number of high proc-array slots reserved for
// detached prepared transactions (DetachToDedicatedSlot). Connections and
// internal auto-assigned transactions only use the low region [0, ConnSlotCount),
// so a parked prepared transaction's slot is never clobbered by a backend that
// reuses a procNum. M0118-0009 (stats — cross-backend two-phase commit).
const ReservedPreparedSlots = 64

// ConnSlotCount is the number of proc-array slots available to connections and
// internal auto-assigned transactions (the low region). The reserved prepared-
// transaction region is [ConnSlotCount, DefaultProcArraySize). M0118-0009.
const ConnSlotCount = DefaultProcArraySize - ReservedPreparedSlots

// procSlot holds per-backend transaction state. Layout is fixed at 64 bytes.
//
//	offset  0: xid       (8 B) — write-XID; 0 = no active write-xact
//	offset  8: xmin      (8 B) — lowest Xmin seen; ^uint64(0) = none
//	offset 16: firstSnap (8 B) — RR/SSI pinned snapshot; nil = RC
//	offset 24: snapshotXmin (4 B)
//	offset 28: isolation (4 B)
//	offset 32: inTxn     (4 B) — 1 = active txn; 0 = idle
//	offset 36: _pad     (28 B)
type procSlot struct {
	xid          atomic.Uint64 // write-XID; 0 = no active write-xact
	xmin         atomic.Uint64 // lowest Xmin seen; ^uint64(0) = none
	firstSnap    *Snapshot     // RR/SSI pinned snapshot; nil = RC
	snapshotXmin uint32        // running min Xmin for OldestXmin
	isolation    int32         // IsolationLevel stored as int32
	inTxn        atomic.Uint32 // 1 = active txn; 0 = idle
	pinnedSnap   atomic.Bool   // true once an RR/SSI snapshot is pinned for the txn
	connHeld     atomic.Uint32 // 1 = a live CONNECTION owns this slot (spans txns)
	_pad         [20]byte      // explicit pad to 64 B
}

// ProcArray is the shared array of per-backend transaction slots.
type ProcArray struct {
	slots []procSlot
}

// newProcArray allocates a fresh slot array of size n.
func newProcArray(n int) ProcArray {
	return ProcArray{slots: make([]procSlot, n)}
}

// Ensure the compiler keeps storage import live (TransactionID used by callers).
var _ storage.TransactionID
