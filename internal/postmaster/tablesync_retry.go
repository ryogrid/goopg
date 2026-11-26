package postmaster

import (
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// defaultWalRetrieveRetryInterval is wal_retrieve_retry_interval's boot
// value (5s).
const defaultWalRetrieveRetryInterval = 5 * time.Second

// walRetrieveRetryInterval is wal_retrieve_retry_interval: how long the
// apply worker waits before relaunching a failed tablesync worker
// (logicalrep_worker_launch from process_syncing_tables_for_apply).
func (s *Server) walRetrieveRetryInterval() time.Duration {
	if s.cfg.Registry != nil {
		if v, ok := s.cfg.Registry.Get("wal_retrieve_retry_interval"); ok {
			if ms, err := strconv.Atoi(v.Value); err == nil && ms > 0 {
				return time.Duration(ms) * time.Millisecond
			}
		}
	}
	return defaultWalRetrieveRetryInterval
}

// scheduleTableSyncRetry re-runs a subscription's failed table syncs after
// wal_retrieve_retry_interval, as PG's apply worker relaunches a tablesync
// worker that exited on an ERROR. Each attempt logs its ERROR again (a
// subscriber log watched for a sync failure therefore sees it repeat, as
// on PG). The retry stops once the subscription is gone or disabled, or
// once none of rels is left in a non-ready state; a newer refresh of the
// subscription replaces a pending retry. M0149-0048f.
func (s *Server) scheduleTableSyncRetry(subName, conninfo string, dbOid uint32, pubs []string, rels map[uint32]*refreshRel) {
	s.tableSyncRetryMu.Lock()
	defer s.tableSyncRetryMu.Unlock()
	if s.tableSyncRetry == nil {
		s.tableSyncRetry = map[string]*time.Timer{}
	}
	if t := s.tableSyncRetry[subName]; t != nil {
		t.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(s.walRetrieveRetryInterval(), func() {
		s.tableSyncRetryMu.Lock()
		current := s.tableSyncRetry[subName] == timer
		if current {
			delete(s.tableSyncRetry, subName)
		}
		s.tableSyncRetryMu.Unlock()
		if !current {
			return // replaced by a newer refresh
		}
		pending := s.pendingTableSyncRels(subName, dbOid, rels)
		if len(pending) == 0 {
			return
		}
		if _, failed := s.runSubscriptionTableSyncs(subName, conninfo, pubs, pending); failed {
			s.scheduleTableSyncRetry(subName, conninfo, dbOid, pubs, pending)
		}
	})
	s.tableSyncRetry[subName] = timer
}

// pendingTableSyncRels is the subset of rels whose pg_subscription_rel row
// still waits for its initial copy, or nil when the subscription was
// dropped or disabled since.
func (s *Server) pendingTableSyncRels(subName string, dbOid uint32, rels map[uint32]*refreshRel) map[uint32]*refreshRel {
	if s.cfg.PubSub == nil {
		return nil
	}
	sub, ok := s.cfg.PubSub.LookupSubscription(subName, dbOid)
	if !ok || !sub.Enabled {
		return nil
	}
	pending := map[uint32]*refreshRel{}
	for oid, rr := range rels {
		sr, tracked := s.cfg.PubSub.LookupSubscriptionRel(subName, oid)
		if tracked && sr.State != catalog.SubRelStateReady && sr.State != catalog.SubRelStateSyncDone {
			pending[oid] = rr
		}
	}
	return pending
}
