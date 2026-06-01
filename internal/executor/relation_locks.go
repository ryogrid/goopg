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

func init() {
	catalog.RelationLockRowsFunc = func() [][]string {
		return globalRelLockMgr.PgLockRows()
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
