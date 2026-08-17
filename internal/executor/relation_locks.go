package executor

// relation_locks.go — global registry of LOCK TABLE relation locks for pg_locks. M0097.
//
// LOCK TABLE acquires a relation lock that is visible in pg_locks.  Because
// pg_locks.VirtualRows is a global function with no connection context, we
// keep a process-wide slice of currently-held relation locks (one entry per
// locked relation per session).  Each entry records the session pointer as
// its owner so that the locks can be released on COMMIT/ROLLBACK.
//
// This mirrors the advisory-lock pattern (advisory.go / catalog.AdvisoryLockRowsFunc).

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage/lmgr"
)

type relLockEntry struct {
	sessID uint64
	dbOID  uint32
	relOID uint32
	mode   string
}

type relLockMgr struct {
	mu    sync.Mutex
	locks []relLockEntry
}

var globalRelLockMgr = &relLockMgr{}

// sessPtr converts a Session interface to a stable uint64 identifier.
func sessPtr(sess Session) uint64 {
	return uint64(reflect.ValueOf(sess).Pointer())
}

// AddRelationLock records that sess holds a lock on relOID with the given mode.
// If the same (sess, relOID) pair already exists, the mode is upgraded.
func (m *relLockMgr) AddRelationLock(sess Session, dbOID, relOID uint32, mode string) {
	id := sessPtr(sess)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, l := range m.locks {
		if l.sessID == id && l.relOID == relOID {
			m.locks[i].mode = mode // upgrade
			return
		}
	}
	m.locks = append(m.locks, relLockEntry{sessID: id, dbOID: dbOID, relOID: relOID, mode: mode})
}

// ReleaseSession removes all relation locks held by sess (called on COMMIT/ROLLBACK).
func (m *relLockMgr) ReleaseSession(sess Session) {
	id := sessPtr(sess)
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, l := range m.locks {
		if l.sessID != id {
			m.locks[n] = l
			n++
		}
	}
	m.locks = m.locks[:n]
}

// PgLockRows returns pg_locks rows for all currently held relation locks.
func (m *relLockMgr) PgLockRows() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.locks) == 0 {
		return nil
	}
	rows := make([][]string, 0, len(m.locks))
	for _, l := range m.locks {
		rows = append(rows, []string{
			"relation",                    // locktype
			fmt.Sprintf("%d", l.dbOID),   // database
			fmt.Sprintf("%d", l.relOID),  // relation
			"", "", "", "",               // page, tuple, virtualxid, transactionid
			"", "", "",                   // classid, objid, objsubid
			"",                           // virtualtransaction
			"0",                          // pid
			l.mode,                       // mode
			"t",                          // granted
			"f",                          // fastpath
			"",                           // waitstart
		})
	}
	return rows
}

// ——— Live tableLockMgr → pg_locks bridge (design 0118-0070) ————————————————
//
// globalRelLockMgr above is the display-only registry for relation locks that
// take NO real heavyweight lock (autocommit LOCK TABLE, exotic modes). The
// transaction-scoped relation locks that DO take a real lock — LOCK TABLE in an
// explicit transaction, DDL (DROP INDEX / ALTER TABLE / CREATE TRIGGER), and
// per-scan ACCESS SHARE — live on the executor's dedicated tableLockMgr
// (context.go). The bridge below enumerates that manager so those locks appear
// in pg_locks with a real backend PID, matching upstream's GetLockStatusData()
// feeding the pg_locks SRF.

// lockBackendPID maps a transaction-scoped lock-manager BackendID (a
// connection's stable LockBackendID, minted in runPostStartupLoop) to its
// wire-protocol backend PID, so each tableLockMgr relation lock can be stamped
// with the PID that pg_stat_activity joins on (pg_locks.pid = pg_stat_activity.pid).
// Registered once per connection at startup, removed at teardown. The
// per-statement BackendID used for autocommit-transient locks is deliberately
// NOT registered: such locks are held only for the duration of one statement,
// so a concurrent pg_locks reader almost never observes them, and when it does
// they surface with PID 0 (filtered out by the pg_stat_activity join).
var (
	lockBackendPIDMu sync.RWMutex
	lockBackendPID   = map[lmgr.BackendID]string{}
)

// RegisterLockBackendPID records the PID that backs the given transaction-scoped
// lock-manager identity. Called once per connection from runPostStartupLoop.
func RegisterLockBackendPID(b lmgr.BackendID, pid uint32) {
	if b == 0 {
		return
	}
	lockBackendPIDMu.Lock()
	lockBackendPID[b] = fmt.Sprintf("%d", pid)
	lockBackendPIDMu.Unlock()
}

// UnregisterLockBackendPID drops the mapping at connection teardown.
func UnregisterLockBackendPID(b lmgr.BackendID) {
	if b == 0 {
		return
	}
	lockBackendPIDMu.Lock()
	delete(lockBackendPID, b)
	lockBackendPIDMu.Unlock()
}

func lookupLockBackendPID(b lmgr.BackendID) string {
	lockBackendPIDMu.RLock()
	pid := lockBackendPID[b]
	lockBackendPIDMu.RUnlock()
	return pid
}

// tableLockMgrPgLockRows enumerates the executor's dedicated tableLockMgr as
// pg_locks rows. Every granted holder and queued waiter on a RELATION-level tag
// (tuple-level tags — Block/Offset non-zero — are filtered out, since the
// pg_locks relation column is meaningful only for relation locks) becomes one
// row, stamped with the holder's backend PID resolved via lockBackendPID. This
// is the live half of the pg_locks relation-lock view (design 0118-0070).
func tableLockMgrPgLockRows() [][]string {
	all := tableLockMgr.AllLocks()
	if len(all) == 0 {
		return nil
	}
	rows := make([][]string, 0, len(all))
	for _, h := range all {
		if h.Tag.Block != 0 || h.Tag.Offset != 0 {
			continue // tuple-level lock; not a relation lock
		}
		pid := lookupLockBackendPID(h.Backend)
		if pid == "" {
			// Per-statement (autocommit-transient) lock with no stable PID
			// mapping. Emit "0" like the display-only registry: pg_stat_activity
			// has no pid-0 row, so the pg_locks view's join drops it.
			pid = "0"
		}
		granted := "f"
		if h.Granted {
			granted = "t"
		}
		rows = append(rows, []string{
			"relation",                   // locktype
			fmt.Sprintf("%d", h.Tag.DB),  // database
			fmt.Sprintf("%d", h.Tag.Rel), // relation
			"", "", "", "",               // page, tuple, virtualxid, transactionid
			"", "", "",                   // classid, objid, objsubid
			"",                           // virtualtransaction
			pid,                          // pid
			h.Mode.String(),              // mode
			granted,                      // granted
			"f",                          // fastpath
			"",                           // waitstart
		})
	}
	return rows
}

func init() {
	catalog.RelationLockRowsFunc = func() [][]string {
		rows := globalRelLockMgr.PgLockRows()
		return append(rows, tableLockMgrPgLockRows()...)
	}
}

// ReleaseRelationLocks releases all relation locks held by the given session
// identity. Called from connTxState.End() on COMMIT/ROLLBACK. M0097.
// identity should be the *BasicSession pointer (same value used by AddRelationLock).
func ReleaseRelationLocks(identity any) {
	switch v := identity.(type) {
	case Session:
		globalRelLockMgr.ReleaseSession(v)
	case *BasicSession:
		globalRelLockMgr.ReleaseSession(Session(v))
	}
}
