# REF-019: pg_stat_activity & Wait Events

## Overview

`pg_catalog.pg_stat_activity` exposes information about active server backends: their PID, current query, state, transaction timestamps, and wait events. goopg implements a virtual view backed by an in-memory registry, with wait events recorded at blocking-operation boundaries.

## goopg Implementation

**Package:** `internal/activity/`

### Registry

`activity.Registry` is a concurrency-safe (`sync.RWMutex`) map of
backend PID → `Backend` struct. Each backend entry contains:

```go
type Backend struct {
    PID, State, Query, QueryStart, XactStart, StateChange string
    WaitEventType, WaitEvent                               string
    BackendType, UserName, DatName                         string
    ClientAddr, ClientPort                                  string
    BackendStart, BackendXID, BackendXMin                   string
}
```

### Backend Lifecycle

1. **Connection accepted** (`serveConn`): backend registered with
   state = "active", backend_type = "client_backend".
2. **Query dispatched**: state → "active", query text updated.
3. **Query completes**: state → "idle".
4. **Connection closed**: backend unregistered.

### Virtual View

The view is registered in `initdb.Open` via `registerPgStatActivityView`.
Its `VirtualRows` callback calls `Registry.Snapshot()` and formats
each backend entry into a `[][]string` row.

### Wait Event Recording

Wait events are recorded at blocking-operation boundaries:

| Wait Type | Wait Name | Hook Location |
|-----------|-----------|---------------|
| Client | ClientRead | `protocol.FrameReader` (before every read) |
| Client | ClientWrite | `protocol.FrameWriter` (before every write) |
| IO | AIO | `aio.Handle.Wait` (engine hooks) |
| IO | DataFileRead/Write/Extend/Sync | `storage.Manager` hooks |
| IO | WALSync | `wal.Writer.FlushUpTo` |
| IO | WALWrite | `state.writeAt` |
| Lock | relation | `executor.acquireRelLock` |
| BufferPin | BufferPin | `storage.Pool.Pin` |

The goroutine-ID lookup mechanism (`LookupGoroutine`) maps the
calling goroutine to the correct (Registry, PID) pair.

## PostgreSQL Implementation

PostgreSQL's `pg_stat_activity` is backed by the `pgstat` subsystem
(`pgstat.c`):

- **Shared memory** — backend entries live in shared memory
  (`PgBackendStatus` array). Each backend writes its own entry;
  readers snapshot the entire array.
- **Wait event reporting** — `pgstat_report_wait_start(event_id)`
  and `pgstat_report_wait_end()` are called explicitly at wait
  boundaries. The event ID is a `uint32` encoding the wait class
  and event number. goopg uses string-based type + name.
- **Wait event taxonomy** — PostgreSQL has ~200 named wait events
  across 10 classes. goopg supports a subset (~30 events).
- **Backend type** — PostgreSQL distinguishes client backends,
  background workers, WAL sender, WAL receiver, checkpointer,
  autovacuum launcher, etc. goopg now supports client_backend,
  checkpointer, and walwriter types.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Backend tracking | In-memory Go map (RWMutex) | Shared memory array (per-process slot) |
| Wait event mechanism | goroutine-ID → (Registry, PID) lookup | `pgstat_report_wait_start/end` |
| Wait event encoding | String (type + name) | `uint32` (class bitmask + event ID) |
| Number of wait events | ~30 | ~200 |
| Non-client backend types | client_backend, checkpointer, walwriter | 20+ types |
| Lock wait events | relation only | relation, page, tuple, transactionid, … |

## References

- goopg: `internal/activity/activity.go`
- goopg: `docs/reference/wait-events.md`
- PG backend status: `postgres/src/backend/utils/activity/backend_status.c`
- PG wait event: `postgres/src/backend/utils/activity/wait_event.c`
- PG wait event names: `postgres/src/backend/utils/activity/wait_event_names.txt`
