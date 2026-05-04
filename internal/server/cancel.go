package server

import (
	"context"
	"sync"
)

// cancelEntry holds per-connection cancel state: a fixed secretKey
// (constant for the connection lifetime) and the cancel function for
// the currently-executing query (nil when idle).
type cancelEntry struct {
	mu          sync.Mutex
	secretKey   uint32
	queryCancel context.CancelFunc
}

// setQueryCancel installs fn as the cancel function for the
// currently-executing query. fn must not be nil; it replaces any
// previous cancel function without calling it.
func (e *cancelEntry) setQueryCancel(fn context.CancelFunc) {
	e.mu.Lock()
	e.queryCancel = fn
	e.mu.Unlock()
}

// clearQueryCancel removes the active query cancel function. Called
// after a query completes (success or error) so that a race-y cancel
// request arriving after the query has already finished is a no-op.
func (e *cancelEntry) clearQueryCancel() {
	e.mu.Lock()
	e.queryCancel = nil
	e.mu.Unlock()
}

// cancel fires the active query cancel function if the secret matches.
// Returns true when a cancel function was fired, false otherwise.
func (e *cancelEntry) cancel(secretKey uint32) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.secretKey != secretKey || e.queryCancel == nil {
		return false
	}
	e.queryCancel()
	return true
}

// backendCancelRegistry is a process-wide map from backend pid to its
// cancel entry. The registry is safe for concurrent use.
type backendCancelRegistry struct {
	mu      sync.Mutex
	entries map[uint32]*cancelEntry
}

func newCancelRegistry() *backendCancelRegistry {
	return &backendCancelRegistry{entries: make(map[uint32]*cancelEntry)}
}

// register adds a backend with the given pid and secretKey. The
// returned *cancelEntry is owned by the caller's goroutine; no other
// goroutine may write to it.
func (r *backendCancelRegistry) register(pid, secretKey uint32) *cancelEntry {
	e := &cancelEntry{secretKey: secretKey}
	r.mu.Lock()
	r.entries[pid] = e
	r.mu.Unlock()
	return e
}

// unregister removes the backend. Called from the backend goroutine's
// defer after the connection closes.
func (r *backendCancelRegistry) unregister(pid uint32) {
	r.mu.Lock()
	delete(r.entries, pid)
	r.mu.Unlock()
}

// cancelQuery looks up pid in the registry and fires its cancel
// function if secretKey matches. No-op when the pid is unknown, the
// key mismatches, or no query is in flight.
func (r *backendCancelRegistry) cancelQuery(pid, secretKey uint32) {
	r.mu.Lock()
	e, ok := r.entries[pid]
	r.mu.Unlock()
	if !ok {
		return
	}
	e.cancel(secretKey)
}
