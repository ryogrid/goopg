# REF-001: AIO Subsystem

## Overview

The AIO (Asynchronous I/O) subsystem provides a unified interface
for submitting I/O operations (read/write) and waiting for their
completion, abstracting over `io_uring` (Linux) and a worker-pool
fallback. It is used by the storage layer (buffer pool writes) and
the WAL writer to overlap I/O with computation.

## goopg Implementation

**Package:** `internal/aio/`

### Key Types

- `Engine` — the central coordinator. Holds a `Method` (io_uring or
  worker pool), atomic counters for observability, an in-flight
  map, and per-target latency stats.
- `Handle` — returned by `Submit()`. Callers call `Handle.Wait()`
  to block until the I/O completes.
- `Method` interface — implementation of the actual `Submit` + `Wait`
  + reaping strategy.

### Data Flow

```
Caller (Pool.FlushAllPaced / state.writeAt)
  │
  ├─ Engine.Submit(Op) ──► Handle
  │     Method.Submit() copies/queues the request
  │
  └─ Handle.Wait()
        Method.Wait() blocks on completion
        (io_uring_enter(CQE_SOME) or chan receive for worker)
```

### Concurrency Model

- `nextID`, `inFlight`, and counter fields are `atomic`.
- The in-flight map (`inflight`) is protected by `inflightMu`
  (RWMutex). Inserted on Submit, deleted on completion.
- Per-target counters use `sync.Map`.
- `io_uring` reaper runs in a dedicated goroutine.
- Worker pool: one goroutine per worker, receives from a shared
  channel.

### Wait-Event Integration

Engine carries `OnWaitStart` / `OnWaitEnd` hooks (M0022 Stage B).
`Handle.Wait()` calls them before blocking and after unblocking,
using `activity.LookupGoroutine()` to find the correct backend PID.

## PostgreSQL Implementation

PostgreSQL's AIO (introduced in PG 17–18) is structured as:

- **`pg_aio.c`** (`src/backend/utils/activity/pg_aio.c`) — Core
  AIO engine with `io_uring` backend and a `worker` backend.
- **`AIOHandle`** — analogous to goopg's `Handle`. Per-I/O tracking.
- **`AIOContext`** — groups I/O operations by backend. Each backend
  has its own AIO context, so completions are routed to the correct
  backend without global locking.
- **`IoMethod`** — backend interface (`io_uring` or worker thread).

### Key Architectural Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Context | Single global `Engine` | Per-backend `AIOContext` |
| Completion delivery | `Handle.Wait()` blocks on shared channel | Completion events target specific backend |
| Locking | `inflightMu` RWMutex for in-flight map | Lock-free per-backend completion list |
| Worker pool | Shared channel, all workers identical | Per-backend `AIOContext` workers |

## Potential Optimisations or Corrections

- **Per-backend AIO context** would eliminate the global `inflightMu`
  contention under heavy concurrent I/O.
- **Completion list with lock-free push** (modelled on PG's
  `IoCompleteList`) would let backends poll their own completions
  without touching shared state.

## References

- goopg: `internal/aio/aio.go`
- goopg: `internal/aio/method_iouring_linux.go`
- goopg: `internal/aio/method_worker.go`
- PostgreSQL: `src/backend/utils/activity/pg_aio.c`
- PostgreSQL: `src/include/storage/aio.h`
