package postmaster

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
	// terminate cancels the connection's ROOT context (set once at connection
	// start). Firing it makes the backend's serve loop tear the connection down
	// — the engine behind pg_terminate_backend(pid) for a peer backend. nil
	// until set by the backend goroutine. M0118-0009.
	terminate context.CancelFunc
}

// setTerminate installs fn as the connection-termination function (the
// connection's root-context cancel). Called once at connection start.
func (e *cancelEntry) setTerminate(fn context.CancelFunc) {
	e.mu.Lock()
	e.terminate = fn
	e.mu.Unlock()
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

// cancelNoSecret fires the active query cancel function without checking the
// secret key. Used by the in-server, privileged pg_cancel_backend(pid) path
// (the caller is an authenticated backend signalling a peer, not an
// unauthenticated CancelRequest off the wire). It is a no-op when the backend
// is idle (queryCancel == nil); the caller treats the mere existence of the
// entry as "the backend exists" for the returned boolean.
func (e *cancelEntry) cancelNoSecret() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.queryCancel != nil {
		e.queryCancel()
	}
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

// terminateByPID terminates the backend with the given pid by firing its
// connection-termination function (cancelling its root context, which makes
// the serve loop emit a FATAL and close the connection) — the privileged,
// in-server path behind pg_terminate_backend(pid) for a PEER backend. Returns
// true if a backend with that pid is registered, false if the pid is unknown.
// Self-termination never reaches here (handled inline via ErrSelfTerminate).
func (r *backendCancelRegistry) terminateByPID(pid uint32) bool {
	r.mu.Lock()
	e, ok := r.entries[pid]
	r.mu.Unlock()
	if !ok {
		return false
	}
	e.mu.Lock()
	fn := e.terminate
	e.mu.Unlock()
	if fn != nil {
		fn()
	}
	return true
}

// cancelByPID cancels the in-flight query of the backend with the given pid
// WITHOUT a secret key — the privileged, in-server path behind the
// pg_cancel_backend(pid) SQL function. Returns true if a backend with that pid
// is registered (the signal was "sent", mirroring PG returning true when the
// target is a live backend even if idle), false if the pid is unknown (PG emits
// a WARNING and returns false in that case).
func (r *backendCancelRegistry) cancelByPID(pid uint32) bool {
	r.mu.Lock()
	e, ok := r.entries[pid]
	r.mu.Unlock()
	if !ok {
		return false
	}
	e.cancelNoSecret()
	return true
}
