package transam

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// PendingRelDelete is relation storage whose removal waits for the end of the
// transaction: PG's PendingRelDelete (catalog/storage.c).
//   - AtCommit (RelationDropStorage): storage the transaction dropped. The
//     commit record lists every live entry (XACT_XINFO_HAS_RELFILELOCATORS,
//     read through PendingRelDeletes), and Drop runs only after the commit
//     record is written, as smgrDoPendingDeletes runs after
//     RecordTransactionCommit. An abort forgets the entry, so the storage
//     survives a rolled-back DROP.
//   - !AtCommit (RelationCreateStorage): storage the transaction created. The
//     abort record lists it (PendingAbortRelDeletes) and Drop runs after it;
//     a commit forgets the entry.
type PendingRelDelete struct {
	Rel storage.RelFileNode
	// Xid is the (sub)transaction that dropped or created Rel. A drop entry
	// is forgotten when that subtransaction, or one of its ancestors, is
	// rolled back (ROLLBACK TO SAVEPOINT): PG keys the same decision on
	// nestLevel.
	Xid storage.TransactionID
	// AtCommit selects a drop (true) or a create (false).
	AtCommit bool
	// Drop removes the storage: buffers, files, in-memory maps.
	Drop func()
}

// pendingRelDeletes holds the entries per top-level XID.
type pendingRelDeletes struct {
	mu    sync.Mutex
	byTop map[storage.TransactionID][]PendingRelDelete
}

// AddPendingRelDelete schedules d for removal when the top-level transaction
// top commits (a drop) or aborts (a create). A relation already scheduled the
// same way by the same transaction is not added twice.
func (m *Manager) AddPendingRelDelete(top storage.TransactionID, d PendingRelDelete) {
	p := &m.pendingRelDel
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byTop[top] {
		if e.Rel == d.Rel && e.AtCommit == d.AtCommit && !m.subxactTreeAborted(e.Xid) {
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
		if e.AtCommit && !m.subxactTreeAborted(e.Xid) {
			rels = append(rels, e.Rel)
		}
	}
	return rels
}

// PendingAbortRelDeletes returns the relations top would remove if it
// aborted now, in creation order (smgrGetPendingDeletes(false)). The
// abort-record hook lists them.
func (m *Manager) PendingAbortRelDeletes(top storage.TransactionID) []storage.RelFileNode {
	p := &m.pendingRelDel
	p.mu.Lock()
	defer p.mu.Unlock()
	var rels []storage.RelFileNode
	for _, e := range p.byTop[top] {
		if !e.AtCommit {
			rels = append(rels, e.Rel)
		}
	}
	return rels
}

// endPendingRelDeletes ends top's pending deletes (smgrDoPendingDeletes): at
// commit it runs the live drop entries' Drop, at abort every create entry's.
// The other kind is forgotten.
//
// A create inside a savepoint that was rolled back is forgotten at commit,
// not removed: PG removes it at the subtransaction's abort, but goopg keeps
// such a table in the catalog (filed), so its storage must stay too.
func (m *Manager) endPendingRelDeletes(top storage.TransactionID, commit bool) {
	p := &m.pendingRelDel
	p.mu.Lock()
	entries := p.byTop[top]
	delete(p.byTop, top)
	p.mu.Unlock()
	for _, e := range entries {
		if e.Drop == nil || e.AtCommit != commit {
			continue
		}
		if commit && m.subxactTreeAborted(e.Xid) {
			continue
		}
		e.Drop()
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
