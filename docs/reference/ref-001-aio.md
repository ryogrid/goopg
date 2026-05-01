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

PostgreSQL's AIO subsystem (introduced incrementally from PG 17)
is structured around the concept of a **per-backend AIO context**.

### Per-Backend AIOContext

Each backend (client connection, WAL writer, checkpointer) owns an
`AIOContext` allocated from shared memory at backend startup.
The context contains:

- **Completion queue** — a lock-free SPSC (single-producer,
  single-consumer) ring buffer. I/O completions are pushed by the
  AIO engine's reaper and popped by the backend during `Wait()`.
  Because each backend has its own queue, there is no global lock
  for completion delivery.

- **Pending list** — issued-but-not-yet-completed operations for
  this backend. Used for cancellation and accounting.

- **Handle pool** — pre-allocated `AIOHandle` slots to avoid
  per-I/O allocation.

### AIO Engine

The engine (`pg_aio.c`) is a shared subsystem that manages the
physical I/O submission and reaping:

- **I/o_uring backend** — submits SQEs and reaps CQEs. Completions
  are routed to the owning backend's completion queue by looking
  up the `AIOContext` from the completion data.
- **Worker backend** — uses a thread pool. Each worker picks an
  I/O operation, executes it, and pushes the completion to the
  owning backend's completion queue.
- **Completion delivery** — completions are delivered lazily:
  `Wait()` checks the local completion queue and returns immediately
  if an operation is done; otherwise it parks the backend until
  a completion arrives.

### AIOHandle

Each `AIOHandle` embeds a completion callback (`io_complete_callback`)
that the engine invokes (from the reaper or worker context) when
the I/O finishes. The callback is responsible for pushing the
result to the backend's completion queue and waking the backend.

### Key Source Files

- `pg_aio.c` — core engine (Submit, Wait, completion routing).
- `aio_uring.c` — `io_uring`-specific backend.
- `aio_worker.c` — thread-pool backend.
- `aio_types.h` — `AIOHandle`, `AIOOperation`, `AIOContext` structs.

### Comparison with goopg

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Context scope | Single global `Engine` | Per-backend `AIOContext` |
| Completion delivery | Shared channel | Lock-free per-backend SPSC queue |
| Completion routing | N/A (single engine) | Lookup by `AIOContext` ID |
| Pre-allocated handles | None (per-I/O allocation) | Handle pool per context |
| Cancel support | None | Pending-list traversal |
| Callback model | None (synchronous Wait) | `io_complete_callback` on completion |

## Potential Optimisations or Corrections

### P1: Per-backend AIO Context

Replacing the single global `Engine` with per-backend contexts
would eliminate `inflightMu` contention under concurrent I/O.
Each backend would have its own completion queue and handle pool.

**Implementation sketch:**
```go
type BackendAIO struct {
    completed chan Result    // or lock-free ring buffer
    pool      sync.Pool      // Handle reuse
    engine    *Engine        // shared submission path
}
```

### P1: Lock-Free Completion Delivery

Replace the shared channel with a per-backend lock-free ring buffer
(similar to PostgreSQL's `IoCompleteList`). This eliminates the
channel send/receive overhead and the global completion lock.

### P2: Pre-allocated Handle Pool

Use `sync.Pool` or a pre-allocated slice of `Handle` structs to
avoid per-I/O heap allocation. The `Handle` is returned to the
pool after `Wait()`.

### P3: Completion Callback

Add an optional `OnComplete func(Result)` callback to `Handle`.
This would let callers (like the buffer pool) be notified of
completion without an extra goroutine context switch.

## References

- goopg: `internal/aio/aio.go`, `internal/aio/method_iouring_linux.go`
- goopg: `internal/aio/method_worker.go`
- PostgreSQL: `src/backend/utils/activity/pg_aio.c`
- PostgreSQL: `src/include/storage/aio.h`
- PostgreSQL: `src/backend/utils/activity/aio_uring.c`
