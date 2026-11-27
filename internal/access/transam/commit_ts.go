package transam

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// Commit timestamps (commit_ts.c): with track_commit_timestamp on, every
// committed transaction records its commit time and its replication origin
// (the session's replorigin_session_origin, InvalidRepOriginId = 0 for a
// local transaction). The apply worker's update_origin_differs /
// delete_origin_differs conflict detection reads them back for a local
// row's xmin. M0149-0061j.
//
// goopg keeps the entries in memory only: PG's pg_commit_ts SLRU (and its
// WAL records) is not modelled, so timestamps do not survive a restart.

// CommitTsInfo is one transaction's commit timestamp and origin.
type CommitTsInfo struct {
	Time   time.Time
	Origin uint16
}

type commitTsStore struct {
	track   atomic.Bool
	mu      sync.RWMutex
	entries map[storage.TransactionID]CommitTsInfo
	origins map[TxnHandle]TxnOrigin
}

// SetTrackCommitTimestamp sets track_commit_timestamp (a postmaster-context
// GUC, read at startup).
func (m *Manager) SetTrackCommitTimestamp(on bool) { m.commitTs.track.Store(on) }

// TrackCommitTimestamp reports track_commit_timestamp.
func (m *Manager) TrackCommitTimestamp() bool { return m.commitTs.track.Load() }

// SetTxnOrigin marks tx as applying changes from replication origin origin
// (replorigin_session_origin), recorded with its commit timestamp.
func (m *Manager) SetTxnOrigin(tx Transaction, origin uint16) {
	s := &m.commitTs
	s.mu.Lock()
	if s.origins == nil {
		s.origins = map[TxnHandle]TxnOrigin{}
	}
	o := s.origins[tx.Handle]
	o.ID = origin
	s.origins[tx.Handle] = o
	s.mu.Unlock()
}

// TxnOrigin is a transaction's replication origin session state
// (replorigin_session_origin, _origin_lsn, _origin_timestamp): the origin
// it applies changes from and the remote commit being applied, which the
// commit record carries in xl_xact_origin. M0149-0062a.
type TxnOrigin struct {
	ID   uint16
	LSN  uint64 // remote commit end LSN
	Time int64  // remote commit time, PG microseconds since 2000-01-01
}

// SetTxnOriginProgress records the remote commit tx is applying
// (apply_handle_commit_internal sets replorigin_session_origin_lsn and
// _timestamp before committing).
func (m *Manager) SetTxnOriginProgress(tx Transaction, lsn uint64, ts int64) {
	s := &m.commitTs
	s.mu.Lock()
	if o, ok := s.origins[tx.Handle]; ok {
		o.LSN, o.Time = lsn, ts
		s.origins[tx.Handle] = o
	}
	s.mu.Unlock()
}

// TxnOriginOfXID is the origin state of the in-progress transaction whose
// top-level xid is xid, for the commit record (XactLogCommitRecord).
func (m *Manager) TxnOriginOfXID(xid storage.TransactionID) (TxnOrigin, bool) {
	s := &m.commitTs
	s.mu.RLock()
	defer s.mu.RUnlock()
	for h, o := range s.origins {
		procNum := int(h) - 1
		if procNum >= 0 && procNum < len(m.procArray.slots) && storage.TransactionID(m.procArray.slots[procNum].xid.Load()) == xid {
			return o, o.ID != 0
		}
	}
	return TxnOrigin{}, false
}

// CommitTsData is TransactionIdGetCommitTsData: xid's commit timestamp and
// origin, ok = false when none was recorded.
func (m *Manager) CommitTsData(xid storage.TransactionID) (CommitTsInfo, bool) {
	s := &m.commitTs
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.entries[xid]
	return info, ok
}

// recordCommitTs is TransactionTreeSetCommitTsData at commit.
func (m *Manager) recordCommitTs(tx Transaction, xid storage.TransactionID, kind XactMarker) {
	s := &m.commitTs
	s.mu.Lock()
	defer s.mu.Unlock()
	origin := s.origins[tx.Handle].ID
	delete(s.origins, tx.Handle)
	if kind != XactCommit || xid == storage.InvalidTransactionID || !s.track.Load() {
		return
	}
	if s.entries == nil {
		s.entries = map[storage.TransactionID]CommitTsInfo{}
	}
	s.entries[xid] = CommitTsInfo{Time: time.Now(), Origin: origin}
}
