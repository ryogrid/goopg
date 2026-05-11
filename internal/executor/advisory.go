package executor

// advisory.go — session-scoped advisory lock manager (M0096-0003).
//
// Implements the blocking/non-blocking variants of PostgreSQL's advisory
// lock functions so isolation specs that use pg_advisory_lock as a step
// synchroniser can detect blocking correctly (IsolationRunner marks a step
// as "<waiting ...>" when its query has not returned within 300 ms).
//
// Design: a single process-global Manager with a map of lock-key → holder
// session ID plus per-key waiter queues implemented with channels.
// Blocking in Go happens naturally because each isolation-spec step runs in
// its own goroutine; when that goroutine blocks inside acquire(), the
// IsolationRunner's 300-ms timer fires and records the step as waiting.

import (
	"context"
	"reflect"
	"sync"
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

// advisoryManager is the process-global advisory lock state.
type advisoryManager struct {
	mu        sync.Mutex
	held      map[advisoryKey]uintptr            // key → session that holds it
	waiters   map[advisoryKey][]*advisoryWaiter  // key → pending waiters
	bySession map[uintptr][]advisoryKey          // session → keys it holds
}

// globalAdvisoryMgr is the single advisory lock manager for this process.
var globalAdvisoryMgr = &advisoryManager{
	held:      make(map[advisoryKey]uintptr),
	waiters:   make(map[advisoryKey][]*advisoryWaiter),
	bySession: make(map[uintptr][]advisoryKey),
}

// acquire blocks until the lock is available for sess or ctx is cancelled.
// Returns ctx.Err() if cancelled, nil on success.
func (m *advisoryManager) acquire(ctx context.Context, key advisoryKey, sess uintptr) error {
	m.mu.Lock()

	holder, held := m.held[key]
	if !held {
		// Lock is free — acquire immediately.
		m.held[key] = sess
		m.bySession[sess] = append(m.bySession[sess], key)
		m.mu.Unlock()
		return nil
	}
	if holder == sess {
		// Same session already holds it — re-entrant, no-op.
		m.mu.Unlock()
		return nil
	}

	// Lock is held by another session — register a waiter and block.
	w := &advisoryWaiter{sess: sess, ready: make(chan struct{})}
	m.waiters[key] = append(m.waiters[key], w)
	m.mu.Unlock()

	select {
	case <-w.ready:
		// Lock was released; retry acquiring (there may be multiple waiters).
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return m.acquire(ctx, key, sess)

	case <-ctx.Done():
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
func (m *advisoryManager) tryAcquire(key advisoryKey, sess uintptr) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	holder, held := m.held[key]
	if held && holder != sess {
		return false
	}
	m.held[key] = sess
	if !held {
		m.bySession[sess] = append(m.bySession[sess], key)
	}
	return true
}

// release releases the lock held by sess for key.
// Returns true if the lock was held by sess, false otherwise.
func (m *advisoryManager) release(key advisoryKey, sess uintptr) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	holder, held := m.held[key]
	if !held || holder != sess {
		return false
	}

	delete(m.held, key)

	// Remove from session's held-key list.
	list := m.bySession[sess]
	for i, k := range list {
		if k == key {
			m.bySession[sess] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(m.bySession[sess]) == 0 {
		delete(m.bySession, sess)
	}

	// Wake all waiters for this key so they retry.
	for _, w := range m.waiters[key] {
		close(w.ready)
	}
	delete(m.waiters, key)

	return true
}

// releaseAll releases every lock held by sess and wakes associated waiters.
// Called by pg_advisory_unlock_all() and implicitly at session teardown.
func (m *advisoryManager) releaseAll(sess uintptr) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, key := range m.bySession[sess] {
		if holder := m.held[key]; holder == sess {
			delete(m.held, key)
			for _, w := range m.waiters[key] {
				close(w.ready)
			}
			delete(m.waiters, key)
		}
	}
	delete(m.bySession, sess)
}

// advisorySessionID returns a stable uintptr identity for a Session, used
// to track lock ownership.  For *BasicSession (the only implementation), this
// is the pointer value; nil returns 0.
func advisorySessionID(sess Session) uintptr {
	if sess == nil {
		return 0
	}
	v := reflect.ValueOf(sess)
	if v.Kind() == reflect.Ptr && !v.IsNil() {
		return uintptr(v.Pointer())
	}
	return 0
}
