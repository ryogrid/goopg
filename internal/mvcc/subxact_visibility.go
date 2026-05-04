package mvcc

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// SubxactMap is an in-memory map from subxact XID to its parent XID
// and status. It mirrors upstream's pg_subtrans SLRU but lives
// entirely in memory for the lifetime of each top-level transaction.
//
// All operations are safe for concurrent use.
type SubxactMap struct {
	mu      sync.RWMutex
	parents map[storage.TransactionID]storage.TransactionID // subxid → parent xid
	aborted map[storage.TransactionID]bool                  // aborted subxids
}

// NewSubxactMap constructs an empty map.
func NewSubxactMap() *SubxactMap {
	return &SubxactMap{
		parents: make(map[storage.TransactionID]storage.TransactionID),
		aborted: make(map[storage.TransactionID]bool),
	}
}

// Register records that subxid is a child of parentXid. Must be called
// when a subtransaction first writes (lazy XID allocation).
func (m *SubxactMap) Register(subxid, parentXid storage.TransactionID) {
	m.mu.Lock()
	m.parents[subxid] = parentXid
	m.mu.Unlock()
}

// MarkAborted records that subxid was rolled back (ROLLBACK TO
// SAVEPOINT). Rows inserted under subxid remain invisible even after
// the top-level parent commits.
func (m *SubxactMap) MarkAborted(subxid storage.TransactionID) {
	m.mu.Lock()
	m.aborted[subxid] = true
	m.mu.Unlock()
}

// IsAborted reports whether subxid was individually rolled back.
func (m *SubxactMap) IsAborted(subxid storage.TransactionID) bool {
	m.mu.RLock()
	v := m.aborted[subxid]
	m.mu.RUnlock()
	return v
}

// Parent returns the parent XID of subxid, or 0 when subxid is not
// a registered subxact (i.e., it is already a top-level XID).
func (m *SubxactMap) Parent(subxid storage.TransactionID) storage.TransactionID {
	m.mu.RLock()
	p := m.parents[subxid]
	m.mu.RUnlock()
	return p
}

// TopLevelXid resolves xid to its top-level ancestor by walking the
// parent chain. Returns xid itself when xid is not in the subxact map.
// Detects cycles defensively (max 64 hops).
func (m *SubxactMap) TopLevelXid(xid storage.TransactionID) storage.TransactionID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := 0; i < 64; i++ {
		parent, ok := m.parents[xid]
		if !ok {
			return xid
		}
		xid = parent
	}
	return xid // cycle guard
}

// IsSubxact reports whether xid is a registered subxact XID.
func (m *SubxactMap) IsSubxact(xid storage.TransactionID) bool {
	m.mu.RLock()
	_, ok := m.parents[xid]
	m.mu.RUnlock()
	return ok
}

// SubxactResolver is the interface TupleVisibleSubxact accepts for
// the subxact-to-parent resolution. The Manager satisfies it via its
// embedded *SubxactMap.
type SubxactResolver interface {
	// TopLevelXid walks the parent chain from xid to its top-level ancestor.
	TopLevelXid(xid storage.TransactionID) storage.TransactionID
	// IsAborted reports whether xid was individually rolled back.
	IsAborted(xid storage.TransactionID) bool
	// IsSubxact reports whether xid is a subtransaction XID.
	IsSubxact(xid storage.TransactionID) bool
}

// SeesCommittedXIDWithSubxacts is an extension of Snapshot.SeesCommittedXID
// that resolves subxact XIDs through the parent chain. It is called by
// TupleVisibleSubxact instead of SeesCommittedXID when a SubxactResolver
// is available.
//
// Visibility rules for subxacts:
//  1. If xid is individually aborted (ROLLBACK TO SAVEPOINT): invisible.
//  2. Resolve xid to its top-level ancestor via TopLevelXid.
//  3. Apply the normal snapshot check against the top-level XID.
//
// This matches upstream's HeapTupleSatisfiesMVCC subxact branch:
// the tuple is visible iff its creating xid's top-level transaction
// was committed AND the subxact itself was not rolled back.
func SeesCommittedXIDWithSubxacts(snap Snapshot, xid storage.TransactionID, r SubxactResolver) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if r != nil && r.IsSubxact(xid) {
		// Rule 1: individually rolled-back subxact rows are always invisible.
		if r.IsAborted(xid) {
			return false
		}
		// Rule 2: resolve to top-level and apply normal snapshot check.
		topXid := r.TopLevelXid(xid)
		return snap.SeesCommittedXID(topXid)
	}
	// No subxact resolution needed.
	return snap.SeesCommittedXID(xid)
}

// TupleVisibleSubxact is TupleVisible extended with subxact-aware
// visibility resolution. When resolver is nil it degrades to the
// standard TupleVisible behaviour, so all existing callers continue
// to work.
func TupleVisibleSubxact(h storage.HeapTupleHeader, snap Snapshot, currentXID storage.TransactionID, r SubxactResolver) bool {
	if h.Xmin == storage.InvalidTransactionID {
		return false
	}
	xmaxIsLockOnly := h.Xmax != storage.InvalidTransactionID && storage.IsHeapTupleLockOnly(h.Infomask)

	// A tuple is self-visible when its xmin was written by the current
	// transaction. Inside a subtransaction the top-level XID (and any
	// intermediate ancestor XID) also qualifies as "self": tuples
	// inserted before the savepoint remain visible inside it.
	if isCurrentTxXID(h.Xmin, currentXID, r) {
		if xmaxIsLockOnly {
			return true
		}
		return !isCurrentTxXID(h.Xmax, currentXID, r)
	}
	if !SeesCommittedXIDWithSubxacts(snap, h.Xmin, r) {
		return false
	}
	if h.Xmax == storage.InvalidTransactionID {
		return true
	}
	if xmaxIsLockOnly {
		return true
	}
	if isCurrentTxXID(h.Xmax, currentXID, r) {
		return false
	}
	return !SeesCommittedXIDWithSubxacts(snap, h.Xmax, r)
}

// isCurrentTxXID reports whether xid was written by the current transaction.
// It returns true when xid equals currentXID exactly, OR when currentXID is
// a subtransaction and xid is its top-level ancestor (a parent XID inserted
// before the savepoint). The v0 subxact model records all sub-XIDs as
// children of the top-level XID, so TopLevelXid resolves in one hop.
func isCurrentTxXID(xid, currentXID storage.TransactionID, r SubxactResolver) bool {
	if xid == storage.InvalidTransactionID {
		return false
	}
	if xid == currentXID {
		return true
	}
	if r == nil || !r.IsSubxact(currentXID) {
		return false
	}
	return xid == r.TopLevelXid(currentXID)
}

// SubxactMapForManager attaches a SubxactMap to the Manager's subxact
// tracking and exposes the SubxactResolver interface. The Manager calls
// this at transaction begin to initialise subxact tracking.
//
// For simplicity in v0, the SubxactMap is stored on the Manager itself
// via the AddSubxactMap / SubxactMapFor helpers below. A production
// implementation would store it in the per-txn state.
func (m *Manager) addSubxactEntry(subxid, parentXid storage.TransactionID) {
	m.subxactMu.Lock()
	if m.subxactParents == nil {
		m.subxactParents = make(map[storage.TransactionID]storage.TransactionID)
		m.subxactAborted = make(map[storage.TransactionID]bool)
	}
	m.subxactParents[subxid] = parentXid
	m.subxactMu.Unlock()
}

func (m *Manager) markSubxactAborted(subxid storage.TransactionID) {
	m.subxactMu.Lock()
	if m.subxactAborted == nil {
		m.subxactAborted = make(map[storage.TransactionID]bool)
	}
	m.subxactAborted[subxid] = true
	m.subxactMu.Unlock()
}

// RegisterSubXid records that subxid is a child of parentXid.
// Called when a subtransaction first writes (lazy XID allocation).
func (m *Manager) RegisterSubXid(subxid, parentXid storage.TransactionID) {
	m.addSubxactEntry(subxid, parentXid)
}

// MarkSubxactAborted records that subxid was individually rolled back.
func (m *Manager) MarkSubxactAborted(subxid storage.TransactionID) {
	m.markSubxactAborted(subxid)
}

// TopLevelXid resolves xid to its top-level ancestor.
func (m *Manager) TopLevelXid(xid storage.TransactionID) storage.TransactionID {
	m.subxactMu.RLock()
	defer m.subxactMu.RUnlock()
	if m.subxactParents == nil {
		return xid
	}
	for i := 0; i < 64; i++ {
		parent, ok := m.subxactParents[xid]
		if !ok {
			return xid
		}
		xid = parent
	}
	return xid
}

// IsAborted reports whether xid was individually rolled back.
func (m *Manager) IsAborted(xid storage.TransactionID) bool {
	m.subxactMu.RLock()
	v := m.subxactAborted[xid]
	m.subxactMu.RUnlock()
	return v
}

// IsSubxact reports whether xid is a registered subxact XID.
func (m *Manager) IsSubxact(xid storage.TransactionID) bool {
	m.subxactMu.RLock()
	_, ok := m.subxactParents[xid]
	m.subxactMu.RUnlock()
	return ok
}

// subxactFields hold the per-manager subxact state. They are embedded
// directly rather than via a pointer so the zero Manager value stays
// valid (maps are lazily initialised).
type subxactFields struct {
	subxactMu      sync.RWMutex
	subxactParents map[storage.TransactionID]storage.TransactionID
	subxactAborted map[storage.TransactionID]bool
}
