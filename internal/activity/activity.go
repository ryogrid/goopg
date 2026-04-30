// Package activity provides the backend activity registry that backs
// pg_catalog.pg_stat_activity (M0022 Stage A).
package activity

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

// Wait event type constants (PG_WAIT_*  —  upstream-compatible).
const (
	WaitTypeIO       = "IO"
	WaitTypeLock     = "Lock"
	WaitTypeClient   = "Client"
	WaitTypeTimeout  = "Timeout"
	WaitTypeActivity = "Activity"
	WaitTypeIPC      = "IPC"
	WaitTypeLWLock   = "LWLock"
	WaitTypeBufferPin = "BufferPin"
)

// Wait event name constants  (pgstat_get_wait_* display names  —
// upstream-compatible where a semantic equivalent exists in goopg).

// IO-type events  (PG_WAIT_IO).
const (
	WaitAIO              = "AIO"
	WaitDataFileRead     = "DataFileRead"
	WaitDataFileWrite    = "DataFileWrite"
	WaitDataFileExtend   = "DataFileExtend"
	WaitDataFileSync     = "DataFileSync"
	WaitDataFileFlush    = "DataFileFlush"
	WaitDataFilePrefetch = "DataFilePrefetch"
	WaitWALRead          = "WALRead"
	WaitWALWrite         = "WALWrite"
	WaitWALSync          = "WALSync"
	WaitWALInitWrite     = "WalInitWrite"
	WaitWALInitSync      = "WalInitSync"
	WaitControlFileRead  = "ControlFileRead"
	WaitControlFileWrite = "ControlFileWrite"
	WaitControlFileSync  = "ControlFileSync"
	WaitBuffileRead      = "BuffileRead"
	WaitBuffileWrite     = "BuffileWrite"
)

// Lock-type events  (PG_WAIT_LOCK).
const (
	WaitRelationLock    = "relation"
	WaitTupleLock       = "tuple"
	WaitTransactionID   = "transactionid"
	WaitPageLock        = "page"
	WaitExtendLock      = "extend"
	WaitAdvisoryLock    = "advisory"
	WaitVirtualXID      = "virtualxid"
	WaitObjectLock      = "object"
	WaitUserLock        = "userlock"
	WaitSpecToken       = "spectoken"
)

// Client-type events  (PG_WAIT_CLIENT).
const (
	WaitClientRead   = "ClientRead"
	WaitClientWrite  = "ClientWrite"
)

// IPC-type events  (PG_WAIT_IPC).
const (
	WaitSyncRep           = "SyncRep"
	WaitCheckpointDone    = "CheckpointDone"
	WaitCheckpointStart   = "CheckpointStart"
	WaitBufferIO          = "BufferIo"
	WaitBackendTermination = "BackendTermination"
)

// Activity-type events  (PG_WAIT_ACTIVITY).
const (
	WaitCheckpointerMain      = "CheckpointerMain"
	WaitWalWriterMain         = "WalWriterMain"
	WaitWalSenderMain         = "WalSenderMain"
	WaitAutoVacuumMain        = "AutovacuumMain"
	WaitLogicalApplyMain      = "LogicalApplyMain"
	WaitLogicalLauncherMain   = "LogicalLauncherMain"
	WaitBgwriterHibernate     = "BgwriterHibernate"
)

// Timeout-type events  (PG_WAIT_TIMEOUT).
const (
	WaitPgSleep              = "PgSleep"
	WaitCheckpointWriteDelay = "CheckpointWriteDelay"
	WaitVacuumDelay          = "VacuumDelay"
)

// BufferPin events.
const WaitBufferPin = "BufferPin"

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

// --- Goroutine-level backend registration --------------------------------
//
// The AIO wait-event hooks (Engine.OnWaitStart/OnWaitEnd) are called from
// Handle.Wait, which runs inside a backend's goroutine but has no direct
// reference to the activity registry or backend PID.  We bridge that gap
// with a goroutine-ID → (registry, pid) map.
//
// The dispatch code calls RegisterCurrentGoroutine before executing a
// query and ClearCurrentGoroutine after.  When Handle.Wait fires, the
// hooks look up the calling goroutine's entry and call WaitEventStart /
// WaitEventEnd on the correct backend.

type goroutineEntry struct {
	reg *Registry
	pid string
}

var (
	goroutineMu sync.RWMutex
	goroutineMap = make(map[string]goroutineEntry) // goroutineID → entry
)

// RegisterCurrentGoroutine records the backend (registry, pid) for the
// calling goroutine.  Must be called before any blocking I/O that might
// trigger AIO wait-event hooks.
func RegisterCurrentGoroutine(reg *Registry, pid string) {
	id := goroutineID()
	goroutineMu.Lock()
	goroutineMap[id] = goroutineEntry{reg: reg, pid: pid}
	goroutineMu.Unlock()
}

// ClearCurrentGoroutine removes the calling goroutine's entry.
func ClearCurrentGoroutine() {
	id := goroutineID()
	goroutineMu.Lock()
	delete(goroutineMap, id)
	goroutineMu.Unlock()
}

// LookupGoroutine returns the registry and pid associated with the
// calling goroutine, or nil if none.
func LookupGoroutine() (*Registry, string) {
	id := goroutineID()
	goroutineMu.RLock()
	entry, ok := goroutineMap[id]
	goroutineMu.RUnlock()
	if !ok {
		return nil, ""
	}
	return entry.reg, entry.pid
}

func goroutineID() string {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	s := string(buf[:n])
	// "goroutine N [running]:..." → extract "N"
	for i := 0; i < n; i++ {
		if s[i] == ' ' {
			return s[9:i]
		}
	}
	return "0"
}

