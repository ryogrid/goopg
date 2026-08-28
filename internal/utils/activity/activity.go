// Package activity provides the backend activity registry that backs
// pg_catalog.pg_stat_activity (M0022 Stage A).
//
// M0107-0005: ActivityRegistry replaces the old Registry's single
// sync.RWMutex hot path with per-backend 64 B cache-line-aligned
// activitySlot array.  WaitEventStart / WaitEventEnd are now O(1)
// atomic uint32 stores.  The exported name Registry is kept as a
// type alias for backward compatibility.
package activity

import (
	"fmt"
	"runtime"
)

// Registry is a backward-compat type alias for ActivityRegistry.
// All existing call sites using *Registry continue to work unchanged.
type Registry = ActivityRegistry

// NewRegistry creates an empty Registry (ActivityRegistry) using the
// default backend capacity (1024 regular + 16 background slots).
// Callers that know the exact capacity should use NewActivityRegistry.
func NewRegistry() *Registry {
	return NewActivityRegistry(1024)
}

// PID returns a string representation of a uint32 PID suitable for the registry.
func PID(pid uint32) string {
	return fmt.Sprintf("%d", pid)
}

// Wait event type constants (PG_WAIT_*  —  upstream-compatible).
const (
	WaitTypeIO        = "IO"
	WaitTypeLock      = "Lock"
	WaitTypeClient    = "Client"
	WaitTypeTimeout   = "Timeout"
	WaitTypeActivity  = "Activity"
	WaitTypeIPC       = "IPC"
	WaitTypeLWLock    = "LWLock"
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
	WaitRelationLock  = "relation"
	WaitTupleLock     = "tuple"
	WaitTransactionID = "transactionid"
	WaitPageLock      = "page"
	WaitExtendLock    = "extend"
	WaitAdvisoryLock  = "advisory"
	WaitVirtualXID    = "virtualxid"
	WaitObjectLock    = "object"
	WaitUserLock      = "userlock"
	WaitSpecToken     = "spectoken"
	// LWLock-class tranche names (upstream wait_event_names.txt); emitted at
	// their named choke points only (see not_ralph/pg-stat-activity-probes
	// 03-design §4 amendment).
	WaitWALWriteLock = "WALWriteLock"
	WaitWALInsert    = "WALInsert"
)

// Client-type events  (PG_WAIT_CLIENT).
const (
	WaitClientRead  = "ClientRead"
	WaitClientWrite = "ClientWrite"
)

// IPC-type events  (PG_WAIT_IPC).
const (
	WaitSyncRep            = "SyncRep"
	WaitCheckpointDone     = "CheckpointDone"
	WaitCheckpointStart    = "CheckpointStart"
	WaitBufferIO           = "BufferIo"
	WaitBackendTermination = "BackendTermination"
)

// Activity-type events  (PG_WAIT_ACTIVITY).
const (
	WaitCheckpointerMain    = "CheckpointerMain"
	WaitWalWriterMain       = "WalWriterMain"
	WaitWalSenderMain       = "WalSenderMain"
	WaitAutoVacuumMain      = "AutovacuumMain"
	WaitLogicalApplyMain    = "LogicalApplyMain"
	WaitLogicalLauncherMain = "LogicalLauncherMain"
	WaitBgwriterHibernate   = "BgwriterHibernate"
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
// This struct is used by Register() and Snapshot() for backward compat with
// the pg_stat_activity view and server.go's Register call.
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

// ——— Backward-compat goroutine registration —————————————————————————————
//
// RegisterCurrentGoroutine and ClearCurrentGoroutine are kept for backward
// compat with callers in initdb/open.go that pass (reg, pid) explicitly.
// New code should call SetCurrentGoroutine(reg, procNum) directly.

// RegisterCurrentGoroutine records (reg, procNum) for the calling goroutine
// by resolving pid to a procNum via reg's pid map.
// Kept for backward compat with background-worker OnLoopStart callbacks.
func RegisterCurrentGoroutine(reg *Registry, pid string) {
	reg.pidMu.RLock()
	procNum, ok := reg.pidMap[pid]
	reg.pidMu.RUnlock()
	if !ok {
		return
	}
	SetCurrentGoroutine(reg, procNum)
}

// LookupGoroutine returns the registry and pid string for the calling
// goroutine, or (nil, "") if not registered.
// Kept for callers that still use the (reg, pid) API (e.g. GetBackendType).
// New callers should use LookupCurrentGoroutine() which returns procNum directly.
func LookupGoroutine() (*Registry, string) {
	reg, procNum, ok := LookupCurrentGoroutine()
	if !ok {
		return nil, ""
	}
	// Reverse-map procNum → pid string for backward compat.
	reg.pidMu.RLock()
	pid := ""
	for p, n := range reg.pidMap {
		if n == procNum {
			pid = p
			break
		}
	}
	reg.pidMu.RUnlock()
	return reg, pid
}

// goroutineID returns the current goroutine's numeric ID as a string.
// Uses runtime.Stack; kept private, used only by the goroutine map helpers.
func goroutineID() string {
	// runtime.Stack header format: "goroutine N [running]:\n..."
	const prefix = "goroutine "
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	if n < len(prefix) {
		return "0"
	}
	s := string(buf[:n])
	if s[:len(prefix)] != prefix {
		return "0"
	}
	// Pre-fix bug (M0053-0006): the previous loop searched the WHOLE
	// string for the FIRST space — that landed on s[9]==' ' (the space
	// inside "goroutine "), returning an incorrect key.
	for i := len(prefix); i < n; i++ {
		if s[i] == ' ' {
			return s[len(prefix):i]
		}
	}
	return "0"
}
