// Package-level ActivityRegistry — per-backend 64B slot array.
// M0107-0005: replaces Registry's single RWMutex hot path with
// per-slot atomic stores.  WaitEventStart / WaitEventEnd become
// O(1) atomic uint32 stores with no global mutex.
package activity

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/goopg/goopg/internal/runtimeshim"
)

// BackgroundWorkerSlots is the number of extra slots reserved beyond the
// regular-backend range for system background workers (WAL writer, etc.).
const BackgroundWorkerSlots = 16

// Background worker slot indices (relative to bgBase).
const (
	WalWriterIdx    = 0
	CheckpointerIdx = 1
	AutovacuumIdx   = 2
	// Indices 3–15 available for walsenders and future workers.
)

// activitySlot is a 64-byte cache-line-aligned per-backend state block.
// Index in the slots array is the backend's procNum.
//
// Layout (verified by the compile-time assertion below):
//
//	offset  0: waitInfo    (4 B)
//	offset  4: _pad0       (4 B)
//	offset  8: stateChange (8 B)
//	offset 16: cold        (8 B)
//	offset 24: _pad1       (40 B)
//	total:                 64 B
type activitySlot struct {
	waitInfo    atomic.Uint32  // packed (typeCode<<16)|eventCode; 0 = no wait
	_pad0       [4]byte        //nolint:structcheck // explicit alignment padding
	stateChange atomic.Int64   // unix nanos; updated on every WaitEvent* call
	cold        *coldActivity  // set once at Acquire; nil when slot is free
	_pad1       [40]byte       //nolint:structcheck // pad to 64 B
}

// Compile-time assertion: activitySlot must be exactly 64 bytes.
var _ [0]struct{} = [unsafe.Sizeof(activitySlot{}) - 64]struct{}{}
var _ [0]struct{} = [64 - unsafe.Sizeof(activitySlot{})]struct{}{}

// coldActivity holds infrequently-updated backend fields.
// Immutable fields (PID, DatName, etc.) are set once at Register/Acquire and
// never modified; they are safe to read without the mutex.
// Mutable cold fields (XactStart, QueryStart, State, BackendXID/XMin, Query)
// are accessed via their own atomic types so pg_stat_activity readers never
// block query execution.
type coldActivity struct {
	// Immutable after Acquire.
	PID             string
	DatName         string
	UserName        string
	ApplicationName string
	ClientAddr      string
	ClientPort      string
	BackendStart    int64 // unix nanos; 0 = not set
	BackendType     string

	// Mutable; updated at transaction / query boundaries by the owning backend.
	// Atomic so Snapshot() readers see consistent values without taking a lock.
	XactStart  atomic.Int64  // unix nanos; 0 = not in transaction
	QueryStart atomic.Int64  // unix nanos; 0 = no active query
	State      atomic.Uint32 // backendStateCode
	BackendXID atomic.Uint64
	BackendXMin atomic.Uint64
	Query      atomic.Pointer[string] // nil = no active query
}

// backendStateCode enumerates the pg_stat_activity state values.
type backendStateCode uint32

const (
	bsIdle        backendStateCode = 0
	bsActive      backendStateCode = 1
	bsIdleInTxn   backendStateCode = 2
	bsIdleAborted backendStateCode = 3
)

func (s backendStateCode) String() string {
	switch s {
	case bsActive:
		return "active"
	case bsIdleInTxn:
		return "idle in transaction"
	case bsIdleAborted:
		return "idle in transaction (aborted)"
	default:
		return "idle"
	}
}

func parseBackendState(s string) backendStateCode {
	switch s {
	case "active":
		return bsActive
	case "idle in transaction":
		return bsIdleInTxn
	case "idle in transaction (aborted)":
		return bsIdleAborted
	default:
		return bsIdle
	}
}

// ActivityRegistry is the per-backend wait-event registry.
// Hot-path WaitEventStart / WaitEventEnd are O(1) atomic stores with
// no global mutex — they write to the owning backend's private slot.
//
// This type replaces the old Registry (which serialised all WaitEvent* calls
// on a single sync.RWMutex and accounted for ~91 % of mutex delay at c=100).
// The old exported name Registry is kept as a type alias for backward compat.
type ActivityRegistry struct {
	slots  []activitySlot // len = nReg + BackgroundWorkerSlots
	nReg   int            // number of regular backend slots
	bgBase int32          // first background-worker slot index

	// pid → procNum mapping; updated only at Register / Unregister (rare).
	// Hot-path WaitEventStart callers that already hold procNum bypass this.
	pidMu  sync.RWMutex
	pidMap map[string]int32

	// Monotonic↔wall conversion epoch. All slot timestamps
	// (stateChange, XactStart, QueryStart) are stored as
	// runtimeshim.Nanotime() readings — monotonic since the
	// runtime started, which is ~10× cheaper than time.Now() on the
	// per-frame WaitEvent path. Snapshot() converts each stored
	// reading to a wall-clock nanos value via wallEpoch + (mono - monoEpoch)
	// for wire-format output (pg_stat_activity expects wall-clock
	// timestamps).
	//
	// The two epochs are captured atomically-as-possible in
	// NewActivityRegistry. Sub-microsecond skew between the two reads
	// is acceptable: pg_stat_activity timestamps are intended for
	// human consumption at second/millisecond resolution.
	monoEpoch int64
	wallEpoch int64
}

// NewActivityRegistry creates an ActivityRegistry with nRegular slots for
// regular backends plus BackgroundWorkerSlots for system background workers.
// nRegular should equal mvcc.DefaultProcArraySize (1024).
func NewActivityRegistry(nRegular int) *ActivityRegistry {
	total := nRegular + BackgroundWorkerSlots
	// Capture the mono↔wall epoch as tightly as possible. Any skew
	// between the two reads (typically tens of nanoseconds) shifts
	// every subsequent Snapshot timestamp by the same constant
	// amount — irrelevant for pg_stat_activity consumers.
	monoEpoch := runtimeshim.Nanotime()
	wallEpoch := time.Now().UnixNano()
	return &ActivityRegistry{
		slots:     make([]activitySlot, total),
		nReg:      nRegular,
		bgBase:    int32(nRegular),
		pidMap:    make(map[string]int32),
		monoEpoch: monoEpoch,
		wallEpoch: wallEpoch,
	}
}

// ——— Background worker lifecycle ————————————————————————————————————————

// RegisterBackground claims a background-worker slot and returns its procNum.
// idx must be in [0, BackgroundWorkerSlots).  Thread-safe.
func (r *ActivityRegistry) RegisterBackground(idx int, b *Backend) int32 {
	procNum := r.bgBase + int32(idx)
	cold := coldFromBackend(b)
	r.acquire(procNum, cold)
	r.pidMu.Lock()
	r.pidMap[b.PID] = procNum
	r.pidMu.Unlock()
	return procNum
}

// ReleaseBackground releases a background-worker slot.
func (r *ActivityRegistry) ReleaseBackground(idx int, pid string) {
	procNum := r.bgBase + int32(idx)
	r.release(procNum)
	r.pidMu.Lock()
	delete(r.pidMap, pid)
	r.pidMu.Unlock()
}

// ——— Regular backend lifecycle ——————————————————————————————————————————

// Register adds a backend entry.  procNum is derived from the numeric PID.
// Replaces the old Registry.Register(*Backend) method.
func (r *ActivityRegistry) Register(b *Backend) {
	procNum := r.procNumForPID(b.PID)
	cold := coldFromBackend(b)
	r.acquire(procNum, cold)
	r.pidMu.Lock()
	r.pidMap[b.PID] = procNum
	r.pidMu.Unlock()
}

// Unregister removes a backend entry by PID.
func (r *ActivityRegistry) Unregister(pid string) {
	r.pidMu.Lock()
	procNum, ok := r.pidMap[pid]
	if ok {
		delete(r.pidMap, pid)
	}
	r.pidMu.Unlock()
	if ok {
		r.release(procNum)
	}
}

// ——— Hot-path wait-event methods ————————————————————————————————————————

// WaitEventStart records the start of a wait event for procNum.
// O(1) atomic stores; no mutex.  Called per protocol frame (~c×100k/s).
func (r *ActivityRegistry) WaitEventStart(procNum int32, waitTypeStr, waitEventStr string) {
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	s := &r.slots[procNum]
	s.waitInfo.Store(packWaitStrings(waitTypeStr, waitEventStr))
	s.stateChange.Store(runtimeshim.Nanotime())
}

// WaitEventEnd clears the wait event for procNum.
// O(1) atomic stores; no mutex.
func (r *ActivityRegistry) WaitEventEnd(procNum int32) {
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	s := &r.slots[procNum]
	s.waitInfo.Store(0)
	s.stateChange.Store(runtimeshim.Nanotime())
}

// ——— Cold-path update methods ————————————————————————————————————————————

// UpdateState updates state and optionally the active query for procNum.
// Called at statement boundaries (~1/statement, not per-frame).
func (r *ActivityRegistry) UpdateState(procNum int32, state, query string) {
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	c := r.slots[procNum].cold
	if c == nil {
		return
	}
	now := runtimeshim.Nanotime()
	r.slots[procNum].stateChange.Store(now)
	if state != "" {
		c.State.Store(uint32(parseBackendState(state)))
	}
	if query != "" {
		c.Query.Store(&query)
		c.QueryStart.Store(now)
	} else if state == "idle" {
		// Clear query on return to idle.
		c.Query.Store(nil)
	}
}

// UpdateStateByPID is the backward-compat string-PID variant of UpdateState.
func (r *ActivityRegistry) UpdateStateByPID(pid, state, query string) {
	r.pidMu.RLock()
	procNum, ok := r.pidMap[pid]
	r.pidMu.RUnlock()
	if ok {
		r.UpdateState(procNum, state, query)
	}
}

// BeginTransaction marks the start of a transaction for the given PID.
func (r *ActivityRegistry) BeginTransaction(pid string) {
	r.pidMu.RLock()
	procNum, ok := r.pidMap[pid]
	r.pidMu.RUnlock()
	if !ok {
		return
	}
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	c := r.slots[procNum].cold
	if c == nil {
		return
	}
	now := runtimeshim.Nanotime()
	c.XactStart.Store(now)
	c.State.Store(uint32(bsIdleInTxn))
	r.slots[procNum].stateChange.Store(now)
}

// EndTransaction clears the transaction state for the given PID.
func (r *ActivityRegistry) EndTransaction(pid string) {
	r.pidMu.RLock()
	procNum, ok := r.pidMap[pid]
	r.pidMu.RUnlock()
	if !ok {
		return
	}
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	c := r.slots[procNum].cold
	if c == nil {
		return
	}
	now := runtimeshim.Nanotime()
	c.XactStart.Store(0)
	c.BackendXID.Store(0)
	c.State.Store(uint32(bsIdle))
	r.slots[procNum].stateChange.Store(now)
}

// GetBackendType returns the BackendType for the backend at procNum.
func (r *ActivityRegistry) GetBackendType(procNum int32) string {
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return ""
	}
	c := r.slots[procNum].cold
	if c == nil {
		return ""
	}
	return c.BackendType
}

// GetBackendTypeByPID returns the BackendType for the backend with PID string.
func (r *ActivityRegistry) GetBackendTypeByPID(pid string) string {
	r.pidMu.RLock()
	procNum, ok := r.pidMap[pid]
	r.pidMu.RUnlock()
	if !ok {
		return ""
	}
	return r.GetBackendType(procNum)
}

// Snapshot returns a point-in-time copy of all active backend records.
// WaitEventType and WaitEvent are now populated from the atomic hot-path state.
func (r *ActivityRegistry) Snapshot() []Backend {
	out := make([]Backend, 0, 32)
	for i := range r.slots {
		s := &r.slots[i]
		c := s.cold
		if c == nil {
			continue
		}
		wInfo := s.waitInfo.Load()
		sc := s.stateChange.Load()

		b := Backend{
			PID:             c.PID,
			DatName:         c.DatName,
			UserName:        c.UserName,
			ApplicationName: c.ApplicationName,
			ClientAddr:      c.ClientAddr,
			ClientPort:      c.ClientPort,
			BackendStart:    formatNanos(c.BackendStart),
			BackendType:     c.BackendType,
			State:           backendStateCode(c.State.Load()).String(),
			StateChange:     formatNanos(r.monoToWall(sc)),
		}
		if xs := c.XactStart.Load(); xs != 0 {
			b.XactStart = formatNanos(r.monoToWall(xs))
		}
		if qs := c.QueryStart.Load(); qs != 0 {
			b.QueryStart = formatNanos(r.monoToWall(qs))
		}
		if xid := c.BackendXID.Load(); xid != 0 {
			b.BackendXID = fmt.Sprintf("%d", xid)
		}
		if xmin := c.BackendXMin.Load(); xmin != 0 {
			b.BackendXMin = fmt.Sprintf("%d", xmin)
		}
		if qp := c.Query.Load(); qp != nil {
			b.Query = *qp
		}
		b.WaitEventType, b.WaitEvent = unpackWaitStrings(wInfo)
		out = append(out, b)
	}
	return out
}

// monoToWall converts a runtimeshim.Nanotime() reading taken via this
// registry into a wall-clock nanos value suitable for time.Unix.
// Readings of 0 (uninitialised cold-field XactStart/QueryStart) pass
// through unchanged so formatNanos still emits the empty string.
func (r *ActivityRegistry) monoToWall(mono int64) int64 {
	if mono == 0 {
		return 0
	}
	return r.wallEpoch + (mono - r.monoEpoch)
}

// ——— Internal helpers ——————————————————————————————————————————————————

func (r *ActivityRegistry) acquire(procNum int32, cold *coldActivity) {
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	s := &r.slots[procNum]
	s.cold = cold
	s.waitInfo.Store(0)
	s.stateChange.Store(runtimeshim.Nanotime())
}

func (r *ActivityRegistry) release(procNum int32) {
	if procNum < 0 || int(procNum) >= len(r.slots) {
		return
	}
	s := &r.slots[procNum]
	s.cold = nil
	s.waitInfo.Store(0)
}

// procNumForPID computes the slot index for a numeric PID string.
// For non-numeric PIDs (background workers), returns bgBase.
func (r *ActivityRegistry) procNumForPID(pid string) int32 {
	var u uint64
	for i := 0; i < len(pid); i++ {
		c := pid[i]
		if c < '0' || c > '9' {
			return r.bgBase
		}
		u = u*10 + uint64(c-'0')
	}
	if u == 0 {
		return 0
	}
	return int32((u - 1) % uint64(r.nReg))
}

func coldFromBackend(b *Backend) *coldActivity {
	c := &coldActivity{
		PID:             b.PID,
		DatName:         b.DatName,
		UserName:        b.UserName,
		ApplicationName: b.ApplicationName,
		ClientAddr:      b.ClientAddr,
		ClientPort:      b.ClientPort,
		BackendType:     b.BackendType,
	}
	c.State.Store(uint32(parseBackendState(b.State)))
	return c
}

// formatNanos converts unix nanos to RFC3339Nano string.  0 → "".
func formatNanos(ns int64) string {
	if ns == 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}

// ——— Wait-event packing ————————————————————————————————————————————————
//
// packed uint32: high 16 bits = wait-type code, low 16 bits = wait-event code.
// String → code maps are read-only after init; no locks needed.

var waitTypeStringCode = map[string]uint32{
	WaitTypeIO:        1 << 16,
	WaitTypeLock:      2 << 16,
	WaitTypeClient:    3 << 16,
	WaitTypeTimeout:   4 << 16,
	WaitTypeActivity:  5 << 16,
	WaitTypeIPC:       6 << 16,
	WaitTypeLWLock:    7 << 16,
	WaitTypeBufferPin: 8 << 16,
}

// waitTypeCodeString maps high-word code back to string (built from waitTypeStringCode).
var waitTypeCodeString [9]string

var waitEventStringCode = map[string]uint32{
	// IO events
	WaitAIO:              1,
	WaitDataFileRead:     2,
	WaitDataFileWrite:    3,
	WaitDataFileExtend:   4,
	WaitDataFileSync:     5,
	WaitDataFileFlush:    6,
	WaitDataFilePrefetch: 7,
	WaitWALRead:          8,
	WaitWALWrite:         9,
	WaitWALSync:          10,
	WaitWALInitWrite:     11,
	WaitWALInitSync:      12,
	WaitControlFileRead:  13,
	WaitControlFileWrite: 14,
	WaitControlFileSync:  15,
	WaitBuffileRead:      16,
	WaitBuffileWrite:     17,
	// Lock events
	WaitRelationLock:  20,
	WaitTupleLock:     21,
	WaitTransactionID: 22,
	WaitPageLock:      23,
	WaitExtendLock:    24,
	WaitAdvisoryLock:  25,
	WaitVirtualXID:    26,
	WaitObjectLock:    27,
	WaitUserLock:      28,
	WaitSpecToken:     29,
	// Client events
	WaitClientRead:  30,
	WaitClientWrite: 31,
	// IPC events
	WaitSyncRep:            40,
	WaitCheckpointDone:     41,
	WaitCheckpointStart:    42,
	WaitBufferIO:           43,
	WaitBackendTermination: 44,
	// Activity events
	WaitCheckpointerMain:    50,
	WaitWalWriterMain:       51,
	WaitWalSenderMain:       52,
	WaitAutoVacuumMain:      53,
	WaitLogicalApplyMain:    54,
	WaitLogicalLauncherMain: 55,
	WaitBgwriterHibernate:   56,
	// Timeout events
	WaitPgSleep:              60,
	WaitCheckpointWriteDelay: 61,
	WaitVacuumDelay:          62,
	// BufferPin
	WaitBufferPin: 70,
}

// waitEventCodeString maps low-word code back to string (built in init).
var waitEventCodeString [71]string

func init() {
	for name, code := range waitTypeStringCode {
		idx := code >> 16
		if int(idx) < len(waitTypeCodeString) {
			waitTypeCodeString[idx] = name
		}
	}
	for name, code := range waitEventStringCode {
		if int(code) < len(waitEventCodeString) {
			waitEventCodeString[code] = name
		}
	}
}

func packWaitStrings(typeStr, eventStr string) uint32 {
	return waitTypeStringCode[typeStr] | waitEventStringCode[eventStr]
}

func unpackWaitStrings(packed uint32) (typeStr, eventStr string) {
	tc := packed >> 16
	ec := packed & 0xFFFF
	if int(tc) < len(waitTypeCodeString) {
		typeStr = waitTypeCodeString[tc]
	}
	if int(ec) < len(waitEventCodeString) {
		eventStr = waitEventCodeString[ec]
	}
	return
}

// ——— Package-level goroutine → (ActivityRegistry, procNum) map ———————————
//
// Replaces the old goroutine→(Registry,pid) map from activity.go.
// Used by pool/AIO/spill hooks that don't have a direct procNum reference.
// Hot-path client-I/O hooks in serveConn bypass this entirely via closures.

type goroutineActivityEntry struct {
	reg     *ActivityRegistry
	procNum int32
}

var (
	goroutineActivityMu  sync.RWMutex
	goroutineActivityMap = make(map[string]goroutineActivityEntry)
)

// SetCurrentGoroutine records the (registry, procNum) for the calling
// goroutine.  Called once at connection start (serveConn) and at
// background-worker goroutine start.
func SetCurrentGoroutine(reg *ActivityRegistry, procNum int32) {
	id := goroutineID()
	goroutineActivityMu.Lock()
	goroutineActivityMap[id] = goroutineActivityEntry{reg: reg, procNum: procNum}
	goroutineActivityMu.Unlock()
}

// ClearCurrentGoroutine removes the calling goroutine's entry.
func ClearCurrentGoroutine() {
	id := goroutineID()
	goroutineActivityMu.Lock()
	delete(goroutineActivityMap, id)
	goroutineActivityMu.Unlock()
}

// LookupCurrentGoroutine returns the registry and procNum for the calling
// goroutine, or (nil, 0, false) if not registered.
func LookupCurrentGoroutine() (*ActivityRegistry, int32, bool) {
	id := goroutineID()
	goroutineActivityMu.RLock()
	entry, ok := goroutineActivityMap[id]
	goroutineActivityMu.RUnlock()
	if !ok {
		return nil, 0, false
	}
	return entry.reg, entry.procNum, true
}
