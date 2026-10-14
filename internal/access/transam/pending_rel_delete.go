package transam

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// PendingRelDelete is relation storage a transaction dropped, whose removal
// waits for the transaction to commit: PG's PendingRelDelete with
// atCommit = true (catalog/storage.c, RelationDropStorage). The commit record
// lists every live entry (XACT_XINFO_HAS_RELFILELOCATORS, read through
// PendingRelDeletes), and Drop runs only after the commit record is written,
// as smgrDoPendingDeletes runs after RecordTransactionCommit. An abort
// forgets the entries, so the storage survives a rolled-back DROP.
type PendingRelDelete struct {
	Rel storage.RelFileNode
	// Xid is the (sub)transaction that dropped Rel. The entry is forgotten
	// when that subtransaction, or one of its ancestors, is rolled back
	// (ROLLBACK TO SAVEPOINT): PG keys the same decision on nestLevel.
	Xid storage.TransactionID
	// Drop removes the storage: buffers, files, in-memory maps.
	Drop func()
}

// pendingRelDeletes holds the entries per top-level XID.
type pendingRelDeletes struct {
	mu    sync.Mutex
	byTop map[storage.TransactionID][]PendingRelDelete
}

// AddPendingRelDelete schedules d for removal at the commit of the
// top-level transaction top. A relation already scheduled by the same
// transaction is not added twice.
func (m *Manager) AddPendingRelDelete(top storage.TransactionID, d PendingRelDelete) {
	p := &m.pendingRelDel
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byTop[top] {
		if e.Rel == d.Rel && !m.subxactTreeAborted(e.Xid) {
			return
		}
	}
	if p.byTop == nil {
		p.byTop = make(map[storage.TransactionID][]PendingRelDelete)
	}
	p.byTop[top] = append(p.byTop[top], d)
}

// PendingRelDeletes returns the relations top would remove if it committed
// now, in the order they were dropped (smgrGetPendingDeletes(true)). The
// commit-record hook lists them.
func (m *Manager) PendingRelDeletes(top storage.TransactionID) []storage.RelFileNode {
	p := &m.pendingRelDel
	p.mu.Lock()
	defer p.mu.Unlock()
	var rels []storage.RelFileNode
	for _, e := range p.byTop[top] {
		if !m.subxactTreeAborted(e.Xid) {
			rels = append(rels, e.Rel)
		}
	}
	return rels
}

// endPendingRelDeletes ends top's pending deletes: at commit it runs the live
// entries' Drop (smgrDoPendingDeletes(true)), at abort it forgets them all.
func (m *Manager) endPendingRelDeletes(top storage.TransactionID, commit bool) {
	p := &m.pendingRelDel
	p.mu.Lock()
	entries := p.byTop[top]
	delete(p.byTop, top)
	p.mu.Unlock()
	if !commit {
		return
	}
	for _, e := range entries {
		if e.Drop != nil && !m.subxactTreeAborted(e.Xid) {
			e.Drop()
		}
	}
}

// subxactTreeAborted reports whether xid, or any subtransaction between it
// and its top-level transaction, was rolled back.
func (m *Manager) subxactTreeAborted(xid storage.TransactionID) bool {
	for i := 0; i < 64 && xid != storage.InvalidTransactionID; i++ {
		if m.IsAborted(xid) {
			return true
		}
		parent := m.subxactParent(xid)
		if parent == storage.InvalidTransactionID || parent == xid {
			return false
		}
		xid = parent
	}
	return false
}

// subxactParent returns xid's parent, or InvalidTransactionID for a
// top-level XID.
func (m *Manager) subxactParent(xid storage.TransactionID) storage.TransactionID {
	if sm := m.attachedSubxactMap(); sm != nil {
		return sm.Parent(xid)
	}
	m.subxactMu.RLock()
	defer m.subxactMu.RUnlock()
	return m.subxactParents[xid]
}
