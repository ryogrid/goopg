package executor

// advisory.go — advisory lock manager (M0096-0003).
//
// Implements the blocking/non-blocking variants of PostgreSQL's advisory
// lock functions so isolation specs that use pg_advisory_lock as a step
// synchroniser can detect blocking correctly (IsolationRunner marks a step
// as "<waiting ...>" when its query has not returned within 300 ms).
//
// Design: a single process-global Manager with a map of lock-key → holder
// session ID plus separate session-level and transaction-level hold counts,
// and per-key waiter queues implemented with channels.
// Blocking in Go happens naturally because each isolation-spec step runs in
// its own goroutine; when that goroutine blocks inside acquire(), the
// IsolationRunner's 300-ms timer fires and records the step as waiting.

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/activity"
)

// advisoryKey is the unified key for advisory locks.
// For pg_advisory_lock(bigint):    hi = uint32(key>>32), lo = uint32(key).
// For pg_advisory_lock(int4,int4): hi = uint32(classid), lo = uint32(objid).
// The two forms use the same keyspace in our simplified implementation.
type advisoryKey struct {
	hi, lo uint32
}

// bigintToKey splits a 64-bit advisory lock key into hi/lo halves.
func bigintToKey(key int64) advisoryKey {
	return advisoryKey{hi: uint32(uint64(key) >> 32), lo: uint32(uint64(key))}
}

// int4ToKey builds an advisory key from two int32 values.
func int4ToKey(classid, objid int32) advisoryKey {
	return advisoryKey{hi: uint32(classid), lo: uint32(objid)}
}

// advisoryWaiter is one goroutine blocked waiting for a specific key.
type advisoryWaiter struct {
	sess  uintptr
	ready chan struct{} // closed by release() to wake this waiter
}

type advisoryHold struct {
	owner        uintptr
	sessionCount int
	xactCount    int
	shared       bool // true = ShareLock, false = ExclusiveLock (for pg_locks reporting)
	twoArg       bool // true = int4+int4 form (objsubid=2), false = bigint form (objsubid=1)
}

type advisorySessionHolds struct {
	session map[advisoryKey]int
	xact    map[advisoryKey]int
}

// advisoryManager is the process-global advisory lock state.
type advisoryManager struct {
	mu        sync.Mutex
	held      map[advisoryKey]advisoryHold       // key → hold counts for one owner
	waiters   map[advisoryKey][]*advisoryWaiter  // key → pending waiters
	bySession map[uintptr]*advisorySessionHolds  // session → held keys by scope
}

// globalAdvisoryMgr is the single advisory lock manager for this process.
var globalAdvisoryMgr = &advisoryManager{
	held:      make(map[advisoryKey]advisoryHold),
	waiters:   make(map[advisoryKey][]*advisoryWaiter),
	bySession: make(map[uintptr]*advisorySessionHolds),
}

func (m *advisoryManager) sessionStateLocked(sess uintptr) *advisorySessionHolds {
	state := m.bySession[sess]
	if state == nil {
		state = &advisorySessionHolds{
			session: make(map[advisoryKey]int),
			xact:    make(map[advisoryKey]int),
		}
		m.bySession[sess] = state
	}
	return state
}

func (m *advisoryManager) cleanupSessionStateLocked(sess uintptr) {
	state := m.bySession[sess]
	if state == nil {
		return
	}
	if len(state.session) == 0 && len(state.xact) == 0 {
		delete(m.bySession, sess)
	}
}

func (m *advisoryManager) addHoldLocked(key advisoryKey, sess uintptr, xactScoped, shared, twoArg bool) {
	hold := m.held[key]
	if hold.owner == 0 {
		hold.owner = sess
		hold.shared = shared
		hold.twoArg = twoArg
	}
	state := m.sessionStateLocked(sess)
	if xactScoped {
		hold.xactCount++
		state.xact[key]++
	} else {
		hold.sessionCount++
		state.session[key]++
	}
	m.held[key] = hold
}

func (m *advisoryManager) wakeWaitersLocked(key advisoryKey) {
	for _, w := range m.waiters[key] {
		close(w.ready)
	}
	delete(m.waiters, key)
}

func (m *advisoryManager) releaseSessionLocked(key advisoryKey, sess uintptr) bool {
	hold, held := m.held[key]
	if !held || hold.owner != sess || hold.sessionCount == 0 {
		return false
	}

	hold.sessionCount--
	state := m.bySession[sess]
	if state != nil {
		if state.session[key] <= 1 {
			delete(state.session, key)
		} else {
			state.session[key]--
		}
	}
	if hold.sessionCount == 0 && hold.xactCount == 0 {
		delete(m.held, key)
		m.wakeWaitersLocked(key)
	} else {
		m.held[key] = hold
	}
	m.cleanupSessionStateLocked(sess)
	return true
}

func (m *advisoryManager) releaseAllSessionLocked(sess uintptr) {
	state := m.bySession[sess]
	if state == nil {
		return
	}
	for key, count := range state.session {
		hold, held := m.held[key]
		if !held || hold.owner != sess {
			continue
		}
		hold.sessionCount -= count
		if hold.sessionCount < 0 {
			hold.sessionCount = 0
		}
		if hold.sessionCount == 0 && hold.xactCount == 0 {
			delete(m.held, key)
			m.wakeWaitersLocked(key)
		} else {
			m.held[key] = hold
		}
	}
	state.session = make(map[advisoryKey]int)
	m.cleanupSessionStateLocked(sess)
}

func (m *advisoryManager) releaseAllXactLocked(sess uintptr) {
	state := m.bySession[sess]
	if state == nil {
		return
	}
	for key, count := range state.xact {
		hold, held := m.held[key]
		if !held || hold.owner != sess {
			continue
		}
		hold.xactCount -= count
		if hold.xactCount < 0 {
			hold.xactCount = 0
		}
		if hold.sessionCount == 0 && hold.xactCount == 0 {
			delete(m.held, key)
			m.wakeWaitersLocked(key)
		} else {
			m.held[key] = hold
		}
	}
	state.xact = make(map[advisoryKey]int)
	m.cleanupSessionStateLocked(sess)
}

// acquire blocks until the lock is available for sess or ctx is cancelled.
// Returns ctx.Err() if cancelled, nil on success.
func (m *advisoryManager) acquire(ctx context.Context, key advisoryKey, sess uintptr, xactScoped, shared, twoArg bool) error {
	m.mu.Lock()

	holder, held := m.held[key]
	if !held {
		// Lock is free — acquire immediately.
		m.addHoldLocked(key, sess, xactScoped, shared, twoArg)
		m.mu.Unlock()
		return nil
	}
	if holder.owner == sess {
		// Same session already holds it — re-entrant, no-op.
		m.addHoldLocked(key, sess, xactScoped, shared, twoArg)
		m.mu.Unlock()
		return nil
	}

	// Lock is held by another session — register a waiter and block.
	w := &advisoryWaiter{sess: sess, ready: make(chan struct{})}
	m.waiters[key] = append(m.waiters[key], w)
	m.mu.Unlock()

	// Parked on the ready channel = blocked on an advisory hold; upstream
	// reports Lock:advisory (ProcSleep). Probe only this park — the retry
	// recursion opens its own window. Identity comes from goroutine-local
	// state; unregistered goroutines skip probing.
	reg, procNum, hasActivity := activity.LookupCurrentGoroutine()
	if hasActivity {
		reg.WaitEventStart(procNum, activity.WaitTypeLock, activity.WaitAdvisoryLock)
	}

	select {
	case <-w.ready:
		// Lock was released; retry acquiring (there may be multiple waiters).
		if hasActivity {
			reg.WaitEventEnd(procNum)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return m.acquire(ctx, key, sess, xactScoped, shared, twoArg)

	case <-ctx.Done():
		if hasActivity {
			reg.WaitEventEnd(procNum)
		}
		// Cancelled before we got the lock — remove our waiter entry.
		m.mu.Lock()
		list := m.waiters[key]
		for i, ww := range list {
			if ww == w {
				m.waiters[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		return ctx.Err()
	}
}

// tryAcquire is a non-blocking attempt to acquire the lock.
// Returns true if acquired, false if already held by a different session.
func (m *advisoryManager) tryAcquire(key advisoryKey, sess uintptr, xactScoped, shared, twoArg bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	holder, held := m.held[key]
	if held && holder.owner != sess {
		return false
	}
	m.addHoldLocked(key, sess, xactScoped, shared, twoArg)
	return true
}

// release releases the lock held by sess for key.
// Returns true if the lock was held by sess, false otherwise.
func (m *advisoryManager) release(key advisoryKey, sess uintptr) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.releaseSessionLocked(key, sess)
}

// releaseAllSession releases every session-scoped advisory lock held by sess.
// Transaction-scoped locks are unaffected.
func (m *advisoryManager) releaseAllSession(sess uintptr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseAllSessionLocked(sess)
}

// releaseAllXact releases every transaction-scoped advisory lock held by sess.
func (m *advisoryManager) releaseAllXact(sess uintptr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseAllXactLocked(sess)
}

// releaseAll releases every advisory lock held by sess, regardless of scope.
func (m *advisoryManager) releaseAll(sess uintptr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseAllSessionLocked(sess)
	m.releaseAllXactLocked(sess)
}

// advisorySessionID returns a stable uintptr identity for a Session, used
// to track lock ownership.  For *BasicSession (the only implementation), this
// is the pointer value; nil returns 0.
func advisorySessionID(sess Session) uintptr {
	if sess == nil {
		return 0
	}
	return advisoryPointerID(sess)
}

func advisoryPointerID(v any) uintptr {
	if v == nil {
		return 0
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr && !rv.IsNil() {
		return uintptr(rv.Pointer())
	}
	return 0
}

// advisorySessionIDFromContext returns a stable session identity from an
// executor Context. Advisory locks must survive across statement boundaries
// within the same connection, so we use the Session pointer (which is
// per-connection, stable across statements) rather than BackendID (which
// goopg re-generates for every statement to assign fresh lock-manager slots).
// Using BackendID would cause the lock to appear "foreign" on the next
// statement from the same connection, causing self-deadlocks. M0097-0010.
func advisorySessionIDFromContext(ctx *Context) uintptr {
	if ctx == nil {
		return 0
	}
	// Prefer the per-connection AdvisorySessionIdentity (the SessionRegistry):
	// it is created once in serveConn and stays stable for the ENTIRE connection
	// lifetime, independent of explicit-transaction state. ctx.Session (the
	// BasicSession) is nil before the first BEGIN and non-nil afterward, so
	// keying advisory locks on it FLIPS the owner identity at the first BEGIN —
	// a pg_advisory_lock() taken in autocommit setup (SessionRegistry identity)
	// can then never be matched by a pg_advisory_unlock() issued after BEGIN
	// (BasicSession identity), so the lock leaks ("you don't own a lock" on
	// unlock) and every later acquirer of the same key blocks forever. Both
	// identities are per-connection stable (which is what the M0097-0010
	// self-deadlock fix needs); AdvisorySessionIdentity is simply the one that
	// also covers the autocommit-before-first-BEGIN window. M0118-0003.
	if ctx.AdvisorySessionIdentity != nil {
		if id := advisoryPointerID(ctx.AdvisorySessionIdentity); id != 0 {
			return id
		}
	}
	// Fallback to the BasicSession pointer (e.g. unit tests that wire only a
	// Session), then to the per-statement BackendID as a last resort.
	if ctx.Session != nil {
		return advisorySessionID(ctx.Session)
	}
	return uintptr(ctx.BackendID)
}

// ReleaseAdvisoryTransactionLocks releases all xact-scoped advisory locks for
// the given stable session identity. Used at transaction end.
func ReleaseAdvisoryTransactionLocks(identity any) {
	if id := advisoryPointerID(identity); id != 0 {
		globalAdvisoryMgr.releaseAllXact(id)
	}
}

// ReleaseAllAdvisoryLocks releases EVERY advisory lock held by the given stable
// session identity, both session-scoped and transaction-scoped. PostgreSQL
// releases all advisory locks (regardless of scope) when a backend exits, so
// this must run on connection teardown — a session-scoped pg_advisory_lock()
// that the client never unlocked (or abandoned the connection while holding)
// would otherwise persist for the lifetime of the server process and block
// every future acquirer of the same key. M0118-0003.
func ReleaseAllAdvisoryLocks(identity any) {
	if id := advisoryPointerID(identity); id != 0 {
		globalAdvisoryMgr.releaseAll(id)
	}
}

// PgLockRows returns pg_locks-compatible rows for all currently-held advisory
// locks. Column order matches pg_locks virtual table:
// locktype, database, relation, page, tuple, virtualxid, transactionid,
// classid, objid, objsubid, virtualtransaction, pid, mode, granted, fastpath, waitstart.
// M0097-0021.
func (m *advisoryManager) PgLockRows() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.held) == 0 {
		return nil
	}
	rows := make([][]string, 0, len(m.held))
	for key, hold := range m.held {
		classid := fmt.Sprintf("%d", key.hi)
		objid := fmt.Sprintf("%d", key.lo)
		subid := "1"
		if hold.twoArg {
			subid = "2"
		}
		mode := "ExclusiveLock"
		if hold.shared {
			mode = "ShareLock"
		}
		rows = append(rows, []string{
			"advisory", // locktype
			"16384",    // database
			"",         // relation
			"",         // page
			"",         // tuple
			"",         // virtualxid
			"",         // transactionid
			classid,    // classid
			objid,      // objid
			subid,      // objsubid
			"",         // virtualtransaction
			"0",        // pid
			mode,       // mode
			"t",        // granted
			"f",        // fastpath
			"",         // waitstart
		})
	}
	return rows
}

// init registers the advisory lock row provider with the catalog package so
// pg_locks can include advisory lock state without creating an import cycle.
// M0097-0021.
func init() {
	catalog.AdvisoryLockRowsFunc = func() [][]string {
		return globalAdvisoryMgr.PgLockRows()
	}
}
