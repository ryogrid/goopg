// Package activity provides the backend activity registry that backs
// pg_catalog.pg_stat_activity (M0022 Stage A).
package activity

import (
	"fmt"
	"sync"
	"time"
)

// Backend represents one server backend/connection for pg_stat_activity.
type Backend struct {
	PID             string
	DatID           string
	DatName         string
	UserSysID       string
	UserName        string
	ApplicationName string
	ClientAddr      string
	ClientPort      string
	BackendStart    string
	XactStart       string
	QueryStart      string
	StateChange     string
	State           string
	WaitEventType   string
	WaitEvent       string
	BackendXID      string
	BackendXMin     string
	Query           string
	BackendType     string
}

// Registry is a concurrency-safe collection of backend activity entries.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]*Backend
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{backends: make(map[string]*Backend)}
}

// Register adds or replaces a backend entry.
func (r *Registry) Register(b *Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[b.PID] = b
}

// Unregister removes a backend entry by PID.
func (r *Registry) Unregister(pid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.backends, pid)
}

// Snapshot returns a consistent copy of all registered backends.
func (r *Registry) Snapshot() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Backend, 0, len(r.backends))
	for _, b := range r.backends {
		out = append(out, *b)
	}
	return out
}

// UpdateState updates the state and query for a backend identified by PID.
// If the backend does not exist, this is a no-op.
func (r *Registry) UpdateState(pid, state, query string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[pid]
	if !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if state != "" {
		b.State = state
		b.StateChange = now
	}
	if query != "" {
		b.Query = query
		b.QueryStart = now
	}
}

// BeginTransaction marks the start of a transaction for the backend.
func (r *Registry) BeginTransaction(pid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[pid]
	if !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	b.State = "idle in transaction"
	b.StateChange = now
	b.XactStart = now
}

// EndTransaction clears the transaction state for the backend.
func (r *Registry) EndTransaction(pid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[pid]
	if !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	b.State = "idle"
	b.StateChange = now
	b.XactStart = ""
	b.BackendXID = ""
}

// WaitEventStart records that the backend is waiting for an event of the
// given type (e.g. "IO") and name (e.g. "AIO"). Sets wait_event_type,
// wait_event, and state_change. Stage B of M0022.
func (r *Registry) WaitEventStart(pid, waitType, waitName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[pid]
	if !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	b.WaitEventType = waitType
	b.WaitEvent = waitName
	b.StateChange = now
}

// WaitEventEnd clears the wait event for the backend, returning
// wait_event_type and wait_event to empty string (SQL NULL).
func (r *Registry) WaitEventEnd(pid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[pid]
	if !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	b.WaitEventType = ""
	b.WaitEvent = ""
	b.StateChange = now
}

// PID returns a string representation of a uint32 PID suitable for the registry.
func PID(pid uint32) string {
	return fmt.Sprintf("%d", pid)
}
