# 05 — Per-Backend `wait_event_info` (Activity)

This chapter replaces `activity.Registry`'s single `sync.RWMutex` +
`map[string]*Backend` with a flat per-backend slot array indexed by
`procNum` (the same index used by [[04-mvcc-procarray]]). Hot-path
`WaitEventStart` / `WaitEventEnd` become atomic stores on a packed
`uint32`. The pattern is PostgreSQL's `PGPROC->wait_event_info`
(`postgres/src/include/storage/proc.h:104`).

Cross-references: [[04-mvcc-procarray]] (shared `procNum` index),
[[08-runtime-internals]] (`runtime.nanotime` via `//go:linkname` for
the `StateChange` timestamp).

## 1. Current state

Verbatim from `internal/activity/activity.go:98-247` (struct + hot
methods):

```go
type Backend struct {
    PID, DatID, DatName, UserSysID, UserName, ApplicationName,
    ClientAddr, ClientPort, BackendStart, XactStart, QueryStart,
    StateChange, State, WaitEventType, WaitEvent, BackendXID,
    BackendXMin, Query, BackendType string
}

type Registry struct {
    mu       sync.RWMutex
    backends map[string]*Backend
}

func (r *Registry) WaitEventStart(pidStr, evtType, evt string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if b, ok := r.backends[pidStr]; ok {
        b.WaitEventType = evtType
        b.WaitEvent = evt
        b.StateChange = time.Now().UTC().Format(time.RFC3339Nano)
    }
}

func (r *Registry) WaitEventEnd(pidStr string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if b, ok := r.backends[pidStr]; ok {
        b.WaitEventType = ""
        b.WaitEvent = ""
        b.StateChange = time.Now().UTC().Format(time.RFC3339Nano)
    }
}
```

Evidence from `analysis/perf-optimize/04-contention.md` §4.3 (c=100
select-only mutex delta, after `pprof -base`):

```
2280 s  95.48 %  acceptLoop → serveConn → dispatch
2260 s  94.56 %  sync.(*Mutex).Unlock
1283 s  53.70 %  via activity.(*Registry).WaitEventStart
 893 s  37.37 %  via activity.(*Registry).WaitEventEnd
```

The Registry mutex accounts for ~91 % of all mutex delay at c=100 SO.
WaitEventStart/End are called per protocol frame (`server.go:680-695`)
and per IO operation (Pool.Pin, WAL writer): hundreds of times per
transaction at c=100, thousands of times per second system-wide.

Pointer-typed fields contributing to GC scan:
- `Registry.backends map[string]*Backend` — map with pointer values
  and string keys.
- `Backend.{Query, ClientAddr, ...}` — many string fields whose
  underlying byte pointers are scanned.

## 2. Target architecture

```go
// internal/activity/registry.go (new)

type ActivityRegistry struct {
    slots []ActivitySlot   // len == maxBackends; allocated once at server start
    // No mu. All hot-path access is atomic.
}

type ActivitySlot struct {
    // HOT FIELDS (read every protocol frame / IO op):
    waitEventInfo atomic.Uint32   // (eventType << 16) | event
    stateChange   atomic.Int64    // nanos since unix epoch (runtime.nanotime offset adjusted)

    // COLD POINTER (read only by pg_stat_activity SELECT):
    cold *coldActivity   // mctx-allocated; assigned once at Acquire, cleared at Release
    _pad [40]byte        // pad to 64 B
}

type coldActivity struct {
    // All fields allocated in sessionCtx mctx at backend start.
    PID             string
    DatID           string
    DatName         string
    UserSysID       string
    UserName        string
    ApplicationName string
    ClientAddr      string
    ClientPort      string
    BackendStart    int64   // unix nanos
    BackendType     string

    // Updated on transaction / query boundaries (relatively rare; locked
    // by an internal RWMutex on the coldActivity itself):
    xactMu      sync.RWMutex
    XactStart   int64
    QueryStart  int64
    State       atomic.Uint32  // packed enum: Idle / InTxn / Active / Aborted
    BackendXID  atomic.Uint64
    BackendXMin atomic.Uint64
    Query       atomic.Pointer[string]   // points to mctx-allocated query string; rare swap
}
```

Hot path (`WaitEventStart`/`End`) only touches `waitEventInfo` and
`stateChange` — pure atomic stores; **no mutex**. Cold path
(`pg_stat_activity`) walks the slots once per query, reads through
`cold` with its own RWMutex.

`unsafe.Sizeof(ActivitySlot{}) == 64` (one cache line per slot). With
100 backends, the slot array is 6.4 KB. Pointer-free at the hot-path
struct: the `cold *coldActivity` pointer is the only GC root per slot,
and it never changes during the slot's lifetime (set at Acquire,
nil'd at Release).

## 3. Wait-event identifier packing

The current `WaitEventType` / `WaitEvent` are `string` (e.g.,
`"Client"` / `"ClientRead"`). We replace with packed enums:

```go
type WaitEventType uint16
const (
    WaitTypeNone WaitEventType = iota
    WaitTypeActivity      // server background activity
    WaitTypeClient        // client read/write
    WaitTypeIPC           // IPC / shared memory
    WaitTypeIO            // disk / WAL IO
    WaitTypeLock          // heavyweight lock waits
    WaitTypeLWLock        // lightweight lock waits
    WaitTypeBufferPin     // buffer pin contention
    WaitTypeTimeout       // sleeping for a timer
    WaitTypeExtension     // user-defined
)

type WaitEvent uint16
// Per-type enum; e.g., WaitTypeClient → ClientRead, ClientWrite, ClientCheck.
// Defined in internal/activity/wait_events.go via go:generate from a YAML
// catalog (~100 events total; matches PG's wait_event.c table).

// Packed:
func packWait(t WaitEventType, e WaitEvent) uint32 {
    return uint32(t)<<16 | uint32(e)
}
func unpackWait(p uint32) (WaitEventType, WaitEvent) {
    return WaitEventType(p >> 16), WaitEvent(p & 0xFFFF)
}
```

String resolution happens **only at `pg_stat_activity` read time**:

```go
func (e WaitEventType) String() string { /* switch */ }
func waitEventString(t WaitEventType, e WaitEvent) string { /* table lookup */ }
```

PG counterpart: `postgres/src/include/utils/wait_event.h` defines
identical `WaitEventActivity` / `WaitEventClient` / etc. enums and a
table-based String lookup.

## 4. Hot-path methods

```go
// WaitEventStart records the start of a wait. O(1) atomic store; no mutex.
func (r *ActivityRegistry) WaitEventStart(procNum int32, t WaitEventType, e WaitEvent) {
    s := &r.slots[procNum]
    s.waitEventInfo.Store(packWait(t, e))
    s.stateChange.Store(nanotime())   // see [[08-runtime-internals]]
}

// WaitEventEnd records the end of a wait. Atomic store.
func (r *ActivityRegistry) WaitEventEnd(procNum int32) {
    s := &r.slots[procNum]
    s.waitEventInfo.Store(0)
    s.stateChange.Store(nanotime())
}
```

`nanotime()` is the runtime monotonic clock via `//go:linkname` —
~5 ns per call vs ~50 ns for `time.Now()`. Worth it given the call
rate. [[08-runtime-internals]] describes the linkname contract.

## 5. `pg_stat_activity` reader (cold path)

```go
// Snapshot returns a snapshot of all live backend activity records.
// Acquires no shared lock; iterates the slot array reading atomic
// state. Each cold record is read under its own RWMutex (rare update
// rate).
func (r *ActivityRegistry) Snapshot() []ActivitySnapshot {
    out := make([]ActivitySnapshot, 0, len(r.slots))
    for i := range r.slots {
        s := &r.slots[i]
        cold := s.cold
        if cold == nil {
            continue   // slot unclaimed
        }
        wInfo := s.waitEventInfo.Load()
        sc    := s.stateChange.Load()
        cold.xactMu.RLock()
        snap := ActivitySnapshot{
            ProcNum:          int32(i),
            PID:              cold.PID,
            DatName:          cold.DatName,
            UserName:         cold.UserName,
            ApplicationName:  cold.ApplicationName,
            ClientAddr:       cold.ClientAddr,
            BackendStart:     cold.BackendStart,
            BackendType:      cold.BackendType,
            XactStart:        cold.XactStart,
            QueryStart:       cold.QueryStart,
            StateChange:      sc,
            State:            decodeState(cold.State.Load()),
            BackendXID:       cold.BackendXID.Load(),
            BackendXMin:      cold.BackendXMin.Load(),
            WaitEventType:    WaitEventType(wInfo >> 16),
            WaitEvent:        WaitEvent(wInfo & 0xFFFF),
        }
        if qp := cold.Query.Load(); qp != nil {
            snap.Query = *qp
        }
        cold.xactMu.RUnlock()
        out = append(out, snap)
    }
    return out
}
```

The snapshot is mildly inconsistent (the cold fields and the hot
fields are not read atomically together) — same behaviour as PG's
`pg_stat_activity`, which is documented as a "best-effort" view.

## 6. Registration / lifecycle

```go
// Acquire claims a slot for a new backend. Returns the procNum
// (MUST be the same procNum returned by mvcc.ProcArray.Acquire so
// the two subsystems share identity).
func (r *ActivityRegistry) Acquire(procNum int32, cold *coldActivity) {
    s := &r.slots[procNum]
    s.cold = cold
    s.waitEventInfo.Store(0)
    s.stateChange.Store(nanotime())
}

func (r *ActivityRegistry) Release(procNum int32) {
    s := &r.slots[procNum]
    s.cold = nil
    s.waitEventInfo.Store(0)
}
```

Backend code in `internal/server/server.go::serveConn` allocates the
`coldActivity` once in `sessionCtx` (see [[01-memory-context]]):

```go
cold := mctx.AllocFor[coldActivity](b.sessionCtx)
*cold = coldActivity{
    PID:           pidStr,
    DatName:       cfg.DatName,
    UserName:      authUser,
    ClientAddr:    conn.RemoteAddr().String(),
    BackendStart:  nanotime(),
    BackendType:   "client backend",
}
cold.Query.Store(nil)
activityRegistry.Acquire(b.procNum, cold)
defer activityRegistry.Release(b.procNum)
```

The `cold` pointer lives in `sessionCtx`; it is freed automatically
when the session ends. No GC heap allocation per connection.

## 7. `activity.LookupGoroutine` removal

The current `internal/activity/activity.go` exposes a
`LookupGoroutine(reg, pidStr)` helper that allows WaitEvent call sites
(in `internal/storage`, `internal/wal`, etc.) to find a backend
without threading the PID through. It is implemented via a
goroutine-id → PID map (M0091-0001).

This indirection is **deleted**. The post-refactor contract is that
every call site that needs to record a wait event threads `procNum`
through its argument list. Specifically:

- `executor.Context` gains a `ProcNum int32` field. Operators that
  start waits use `ctx.ProcNum`.
- `storage.Pool.Pin(tag, procNum, ...)` and `Pool.Read(tag, procNum, ...)`
  take an explicit `procNum` arg.
- `wal.Writer.FlushUpTo(lsn, procNum)` similarly.

Call-site impact: ~50 sites across the codebase. All are mechanical
plumbing; no logic changes.

## 8. Per-statement updates (Query string)

When a new statement arrives:

```go
// In dispatch.go, before running the statement:
queryStr := mctx.AllocString(b.stmtCtx, sql)
// Wait — AllocString returns (offset, length). For the activity's
// Query field we want a *string. Use a small helper:
qp := mctx.AllocFor[string](b.sessionCtx)
*qp = mctx.MakeString(b.stmtCtx, sql)   // unsafe.String from mctx bytes
b.activity.cold.Query.Store(qp)
b.activity.cold.QueryStart = nanotime()
b.activity.cold.State.Store(uint32(StateActive))
```

The `Query` field's lifetime is the **statement**'s mctx; when the
statement ends (`stmtCtx.Release()`), the bytes are reclaimed. The
next statement overwrites the `cold.Query` atomic pointer. A
`pg_stat_activity` query that races with statement termination might
observe a stale pointer for a brief window — same as PG behaviour;
the snapshot is best-effort.

Alternative: store the query in `sessionCtx` (longer-lived) so the
ptr is stable. The trade-off is memory; we choose `stmtCtx` because
the typical case is the snapshot already finished reading by the
time the next statement starts.

## 9. PG counterparts

| goopg concept                  | PG counterpart                                            |
|--------------------------------|-----------------------------------------------------------|
| Per-backend `waitEventInfo`    | `PGPROC->wait_event_info` (`proc.h:104`)                  |
| Packed `(type, event)` uint32  | Same in PG (`wait_event.h`)                               |
| Slot array sized to maxBackends| `ProcGlobal->allProcs[]` (`proc.h`)                       |
| Lock-free pg_stat_activity     | `pgstat_read_current_status` (`postmaster/pgstat.c`)      |
| Per-backend cold fields        | `PGPROC->databaseId`, `PGPROC->roleId`, plus `PgBackendStatus` |
| Goroutine→PID indirection      | (none — PG has explicit `MyProc` global)                  |

## 10. Concurrency correctness

- `waitEventInfo` is uint32 — atomic read/write is on a single word.
  A reader sees either the old or the new value, never a torn read.
- `stateChange` is int64 atomic — same.
- `cold` pointer is set once at Acquire and not modified during
  slot lifetime; readers see a stable pointer or nil.
- `cold.xactMu.RWMutex` is per-slot, contended only between
  pg_stat_activity readers and the owning backend's xact / query
  boundary updates — both are rare events (sub-Hz).
- `cold.Query` `atomic.Pointer[string]` allows the hot path (dispatch
  installing a new query) to swap atomically without taking
  `xactMu.Lock`. The reader briefly holds RLock to read other fields
  consistently; the Query is loaded atomically and may be one
  generation older or newer than the rest of the snapshot. Acceptable.

## 11. Verification

After Phase D2 of [[09-migration-and-rollout]] ships:

- **Compile-time** — `grep -RIn 'Registry\.mu\.' internal/activity/`
  returns zero (the field is gone). `ActivityRegistry` struct contains
  no `sync.Mutex` / `sync.RWMutex`.
- **Mutex pprof** — re-run c=100 select-only; `activity.Registry.*`
  and `activity.Backend.*` do not appear in the top-20 contention
  list. Block profile shows zero waits routed through any
  `activity.*` symbol on the hot path.
- **TPS lift** — c=100 select-only TPS rises from **6 400 → ≥ 10 000**
  (08-recommendations.md #3 sized 1.5–2× lift). Combined with [[01]]
  + [[02]] + [[03]], the c=100 SO end-state target is ≥ 12 000.
- **pg_stat_activity correctness** — a stress test that queries
  pg_stat_activity once per second against 100 active backends running
  pgbench shows all backends visible, with WaitEventType / WaitEvent
  values matching the per-backend traces.
- **Race detector** — `go test -race ./internal/activity/...` passes.
- **WaitEvent table coverage** — every existing WaitEvent string in
  `internal/activity/activity.go` and call sites has a corresponding
  enum value; a `go:generate`'d table-completeness test asserts
  parity.

[[04-mvcc-procarray]] and this chapter share `procNum`; a unit test in
`internal/server/backend_test.go` asserts that both subsystems agree
on which slot each backend owns.
