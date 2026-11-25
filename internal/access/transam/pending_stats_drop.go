package transam

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

// Cumulative-statistics kinds an xl_xact_stats_item names (pgstat_kind.h).
const (
	PgStatKindDatabase int32 = 1 // PGSTAT_KIND_DATABASE
	PgStatKindRelation int32 = 2 // PGSTAT_KIND_RELATION
	PgStatKindFunction int32 = 3 // PGSTAT_KIND_FUNCTION
)

// XactStatsItem is PG's xl_xact_stats_item (xact.h): a cumulative-statistics
// entry a commit or abort record tells replay to drop
// (XACT_XINFO_HAS_DROPPED_STATS, pgstat_execute_transactional_drops).
type XactStatsItem struct {
	Kind  int32
	DBOid uint32
	ObjID uint64
}

// PendingStatsDrop is PG's PgStat_PendingDroppedStatsItem (pgstat_xact.c):
//   - !IsCreate (pgstat_drop_transactional): the transaction dropped the
//     object; its commit record names the stats entry.
//   - IsCreate (pgstat_create_transactional): the transaction created the
//     object; its abort record names the stats entry.
type PendingStatsDrop struct {
	Item XactStatsItem
	// Xid is the (sub)transaction that dropped or created the object. An
	// entry whose subtransaction, or an ancestor of it, was rolled back is
	// left out of the commit record: AtEOSubXact_PgStat_DroppedStats discards
	// a rolled-back subtransaction's drops.
	Xid      storage.TransactionID
	IsCreate bool
}

// pendingStatsDrops holds the entries per top-level XID.
type pendingStatsDrops struct {
	mu    sync.Mutex
	byTop map[storage.TransactionID][]PendingStatsDrop
}

// AddPendingStatsDrop records d for top's commit or abort record
// (create_drop_transactional_internal).
func (m *Manager) AddPendingStatsDrop(top storage.TransactionID, d PendingStatsDrop) {
	if top == storage.InvalidTransactionID {
		return
	}
	p := &m.pendingStatsDrop
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byTop == nil {
		p.byTop = make(map[storage.TransactionID][]PendingStatsDrop)
	}
	p.byTop[top] = append(p.byTop[top], d)
}

// PendingStatsDrops is pgstat_get_transactional_drops: at commit the stats
// entries of objects top dropped (outside rolled-back subtransactions), at
// abort those of objects it created, in the order they were recorded.
func (m *Manager) PendingStatsDrops(top storage.TransactionID, commit bool) []XactStatsItem {
	p := &m.pendingStatsDrop
	p.mu.Lock()
	entries := p.byTop[top]
	p.mu.Unlock()
	var items []XactStatsItem
	for _, e := range entries {
		if e.IsCreate == commit {
			continue
		}
		if commit && m.subxactTreeAborted(e.Xid) {
			continue
		}
		items = append(items, e.Item)
	}
	return items
}

// endPendingStatsDrops forgets top's entries once its end record is written
// (AtEOXact_PgStat_DroppedStats).
func (m *Manager) endPendingStatsDrops(top storage.TransactionID) {
	p := &m.pendingStatsDrop
	p.mu.Lock()
	delete(p.byTop, top)
	p.mu.Unlock()
}
