# Milestone 0024 — Wait-Event Recording Architecture: Non-Client-Backend & Cross-Goroutine Paths

**Status:** planned
**Depends on:** Milestone 0022 (pg_stat_activity infrastructure), Milestone 0023 (test suite for verification).
**Drives:** Complete wait-event coverage for all blocking operations in goopg, including those that execute in background goroutines or across goroutine boundaries.

## Context

goopg's current wait-event recording mechanism (`internal/activity/activity.go`) uses a goroutine-ID-keyed map (`goroutineMap`) that maps the calling goroutine to an `(activity.Registry, pid)` pair. The map is populated at `serveConn` entry and cleared at `serveConn` exit via `RegisterCurrentGoroutine` / `ClearCurrentGoroutine`.

This design works well for **client-backend goroutines** — every `serveConn` runs in its own goroutine, so `LookupGoroutine()` returns the correct backend. Hooks in `FrameReader`/`FrameWriter`, `Manager`, `acquireRelLock`, and `aio.Handle.Wait` all use this mechanism successfully.

However, several blocking operations execute in **different goroutines** or lack a goroutine registration entirely:

| Operation | Goroutine | Problem |
|-----------|-----------|---------|
| `WALWrite` / `WALRead` | `state.loop` goroutine (`wal/writer.go`) | The I/O happens in the WAL writer's background goroutine, not in the calling backend's goroutine. FlushUpTo is synchronous but the actual I/O is deferred to the state loop. |
| `BuffileRead` / `BuffileWrite` | Executor goroutine (client backend) | The `storage.Manager` hooks work, but buffiled I/O goes through a different code path (temporary files for sort/hash) that bypasses `Manager`. Requires dedicated hook fields. |
| `BufferPin` | Client-backend goroutine | `Pool.Pin` can block waiting for a buffer to be read from disk. The read itself goes through `Manager.ReadBlock` (which has the hook), but the pin wait time (before the read starts) is not recorded. |
| `WalWriterMain` | `state.loop` goroutine | The WAL writer's main-loop idle wait is not registered as a backend in the activity registry. |
| `WalSenderMain` / `WalReceiverMain` | Replication goroutines | Not registered in the activity registry. |
| `AutovacuumMain` | Autovacuum launcher goroutine | Not registered in the activity registry. |

## Proposed Architecture

### Option A: Propagate a "current wait event" through call chains

Add a `WaitEventCtx` (or similar) to the `executor.Context` that callee code can check. When a blocking I/O operation starts in a callee goroutine, it sets the wait event on a shared structure that the caller can observe.

**Pros:** No goroutine-ID hackery; explicit context threading.
**Cons:** Requires threading a new parameter through many function signatures; large refactoring.

### Option B: Background-goroutine registration

Add explicit `RegisterCurrentGoroutine` calls in every background goroutine's `Run()` method, along with corresponding `Backend` entries in the activity registry. This lets `LookupGoroutine()` find the correct backend for:

- WAL writer loop (`state.loop`)
- Checkpointer loop (`Checkpointer.Run` — already done)
- Autovacuum launcher loop (`AutovacuumLauncher.Run`)
- WAL sender / WAL receiver loops

**Pros:** Uses the existing mechanism; no new infrastructure.
**Cons:** Each background process needs a dedicated PID and BackendType; the WAL writer's `state.loop` is not a public method.

### Option C: Hook fields on subsystems

Add `OnWrite` / `OnRead` / `OnSync` callback fields to every subsystem that performs blocking I/O:

- `wal.Writer.OnWALWrite` / `OnWALRead` (partial — `OnWALSync` done)
- `storage.Pool.OnPinWait`
- `executor` temporary-file I/O callbacks

The callbacks are set from `initdb.Open` using closures that capture the activity registry and PID.

**Pros:** Minimal API surface change; no goroutine-ID dependency; works across goroutine boundaries when the callback is set by the goroutine that owns the subsystem.
**Cons:** Many callback fields; the WAL writer's state loop still needs goroutine registration to associate callbacks with a backend.

### Recommended Approach: B + C

1. **Register every background goroutine** (WAL writer loop, autovacuum launcher, WAL sender, WAL receiver) in the activity registry with a stable PID and `RegisterCurrentGoroutine`.
2. **Add callback fields** for `BuffileRead/Write`, `BufferPin`, and remaining WAL I/O operations.
3. **Wire all callbacks** in `initdb.Open` using `LookupGoroutine` (which now works because every goroutine is registered).

## In Scope

### Background Goroutine Registration

- Register the WAL writer's `state.loop` goroutine in the activity registry (needs a hook in `wal.NewWriter` or `initdb.Open`).
- Register autovacuum launcher goroutine (add `RegisterCurrentGoroutine` in `Launcher.Run`).
- Register WAL sender / WAL receiver goroutines (replication path).
- Ensure each registration has a unique PID and correct `BackendType` (`"walwriter"`, `"autovacuum_launcher"`, `"walsender"`, `"walreceiver"`).

### Buffile I/O Wait Events

- Add `OnBuffileRead` / `OnBuffileWrite` callback fields to the `Manager` or a dedicated buffile subsystem.
- Wire callbacks in `initdb.Open`.
- These fire when the executor reads/writes temporary sort/hash files.

### BufferPin Wait Event

- Add `OnPinWait` callback to `storage.Pool`.
- Fire when `Pool.Pin` cannot find the buffer in the pool and must wait for a disk read.
- The `Manager.ReadBlock` hook already covers the I/O read time; this covers the queue wait before the read.

### WAL I/O Completion

- Add `OnWALWrite` callback to `wal.Writer`, called from `state.loop` when a WAL page is written to disk.
- Already done: `OnWALSync` fires from `FlushUpTo`.

### Activity-Type Wait Events for Background Processes

- Each registered background process should report its main-loop idle wait:
  - `CheckpointerMain` — done.
  - `WalWriterMain` — the state loop should fire `WaitEventStart(WaitTypeActivity, WaitWalWriterMain)` during `select {}` idle.
  - `AutovacuumMain` — `Launcher.Run` ticks.

## Out of Scope

- Full upstream wait-event parity (100+ events).
- LWLock-level wait events (goopg has no LWLock subsystem).
- Injection-point / extension wait events.
- Per-subsystem latency histograms (those belong in the observability milestone).

## Required Design Docs

None — this milestone extends the existing architecture rather than introducing new infrastructure. The design decisions are documented above.

## Reference

- Existing goroutine-ID mechanism: `internal/activity/activity.go` (`goroutineMap`, `LookupGoroutine`)
- Existing hook pattern: `internal/storage/smgr.go` (`OnReadWait`, etc.)
- Existing background registration: `cmd/goopg/main.go` (checkpointer registration)
- Client-I/O hook wiring: `internal/server/server.go` (FrameReader/FrameWriter hooks)
- Pending-task descriptions in `.ralph/fix_plan.md` (Stage B sub-items)

## Definition of Done

1. All background goroutines (WAL writer loop, autovacuum launcher, WAL sender, WAL receiver) are registered in the activity registry and call `RegisterCurrentGoroutine`.
2. `BuffileRead` / `BuffileWrite` wait events are recorded for temporary-file I/O.
3. `BufferPin` wait event is recorded when a buffer pin misses the pool.
4. `WALWrite` / `WALRead` wait events are recorded from the WAL writer loop.
5. `WalWriterMain`, `AutovacuumMain`, `WalSenderMain` activity wait events fire during main-loop idle.
6. All existing tests remain green (`go test ./...`).
7. The `pg_stat_activity` integration test verifies that at least one non-client-backend type appears in the view.
8. The reference doc `docs/reference/wait-events.md` is updated to reflect completed coverage.
