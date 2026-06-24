package executor

// relation_locks_bridge_test.go — unit tests for the live tableLockMgr →
// pg_locks bridge (design 0118-0070). The bridge surfaces transaction-scoped
// relation locks held on the executor's dedicated tableLockMgr in pg_locks,
// stamped with the holder's backend PID so they join pg_stat_activity.

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/lockmgr"
)

// findBridgeRow scans the bridge output for a row matching (relOID, mode,
// granted) and returns its pid (column 11) and whether it was found.
func findBridgeRow(rows [][]string, relOID, mode, granted string) (pid string, found bool) {
	for _, r := range rows {
		// columns: 0 locktype, 2 relation, 11 pid, 12 mode, 13 granted
		if r[0] == "relation" && r[2] == relOID && r[12] == mode && r[13] == granted {
			return r[11], true
		}
	}
	return "", false
}

// TestTableLockMgrBridgeSurfacesGrantedRelationLock: a granted relation lock on
// tableLockMgr appears as a pg_locks row with the registered backend PID.
func TestTableLockMgrBridgeSurfacesGrantedRelationLock(t *testing.T) {
	const backend lockmgr.BackendID = 90001
	const relOID = uint32(778001)
	tag := lockmgr.LockTag{DB: 5, Rel: relOID}

	RegisterLockBackendPID(backend, 4242)
	defer UnregisterLockBackendPID(backend)
	if err := tableLockMgr.TryAcquire(backend, tag, lockmgr.AccessExclusiveLock); err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer tableLockMgr.ReleaseAll(backend)

	rows := tableLockMgrPgLockRows()
	pid, found := findBridgeRow(rows, "778001", "AccessExclusiveLock", "t")
	if !found {
		t.Fatalf("granted AccessExclusiveLock on rel 778001 not in bridge rows: %+v", rows)
	}
	if pid != "4242" {
		t.Errorf("pid = %q, want 4242", pid)
	}
}

// TestTableLockMgrBridgeWaiterNotGranted: a backend blocked behind a conflicting
// holder surfaces with granted="f" and its own PID.
func TestTableLockMgrBridgeWaiterNotGranted(t *testing.T) {
	const holder lockmgr.BackendID = 90010
	const waiter lockmgr.BackendID = 90011
	const relOID = uint32(778002)
	tag := lockmgr.LockTag{DB: 5, Rel: relOID}

	RegisterLockBackendPID(holder, 5000)
	RegisterLockBackendPID(waiter, 5001)
	defer UnregisterLockBackendPID(holder)
	defer UnregisterLockBackendPID(waiter)

	// holder takes ACCESS SHARE; an AccessExclusive acquirer must wait.
	if err := tableLockMgr.TryAcquire(holder, tag, lockmgr.AccessShareLock); err != nil {
		t.Fatalf("holder TryAcquire: %v", err)
	}
	defer tableLockMgr.ReleaseAll(holder)

	// Park the waiter in a goroutine; it blocks until the holder releases.
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_ = tableLockMgr.Acquire(context.Background(), waiter, tag, lockmgr.AccessExclusiveLock)
		close(done)
	}()
	<-started
	// Poll until the waiter has enqueued and the bridge reports it as granted=f.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, granted := findBridgeRow(tableLockMgrPgLockRows(), "778002", "AccessExclusiveLock", "f"); granted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiter never appeared as granted=f: %+v", tableLockMgrPgLockRows())
		}
		time.Sleep(2 * time.Millisecond)
	}

	pid, found := findBridgeRow(tableLockMgrPgLockRows(), "778002", "AccessExclusiveLock", "f")
	if !found {
		t.Fatalf("waiting AccessExclusiveLock not surfaced as granted=f: %+v", tableLockMgrPgLockRows())
	}
	if pid != "5001" {
		t.Errorf("waiter pid = %q, want 5001", pid)
	}

	tableLockMgr.ReleaseAll(holder)
	<-done
	tableLockMgr.ReleaseAll(waiter)
}

// TestTableLockMgrBridgeUnknownBackendPidZero: a lock held under a backend that
// was never registered surfaces with pid "0" (filtered by the pg_stat_activity
// join, but row-count-honest for un-joined readers).
func TestTableLockMgrBridgeUnknownBackendPidZero(t *testing.T) {
	const backend lockmgr.BackendID = 90020
	const relOID = uint32(778003)
	tag := lockmgr.LockTag{DB: 5, Rel: relOID}

	if err := tableLockMgr.TryAcquire(backend, tag, lockmgr.ShareLock); err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer tableLockMgr.ReleaseAll(backend)

	pid, found := findBridgeRow(tableLockMgrPgLockRows(), "778003", "ShareLock", "t")
	if !found {
		t.Fatalf("ShareLock on rel 778003 not surfaced: %+v", tableLockMgrPgLockRows())
	}
	if pid != "0" {
		t.Errorf("unregistered-backend pid = %q, want 0", pid)
	}
}

// TestTableLockMgrBridgeSkipsTupleTags: a tuple-level lock (Block/Offset set)
// is NOT surfaced as a relation row.
func TestTableLockMgrBridgeSkipsTupleTags(t *testing.T) {
	const backend lockmgr.BackendID = 90030
	const relOID = uint32(778004)
	tupleTag := lockmgr.LockTag{DB: 5, Rel: relOID, Block: 1, Offset: 2}

	RegisterLockBackendPID(backend, 6000)
	defer UnregisterLockBackendPID(backend)
	if err := tableLockMgr.TryAcquire(backend, tupleTag, lockmgr.ExclusiveLock); err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer tableLockMgr.ReleaseAll(backend)

	if _, found := findBridgeRow(tableLockMgrPgLockRows(), "778004", "ExclusiveLock", "t"); found {
		t.Errorf("tuple-level lock leaked into relation pg_locks rows: %+v", tableLockMgrPgLockRows())
	}
}
