# 0022-0002 — Backend status lifecycle and snapshot model

## Status

draft

## Goal

Formalise the backend state machine, timing-field semantics, and
concurrency model that back `pg_catalog.pg_stat_activity` in goopg v0.
This doc is the companion to 0022-0001 (column contract) and satisfies
the Stage A gate requirement for a merged lifecycle design doc.

## Backend State Machine

```
  ┌──────────┐
  │  startup │  (connection accepted, before auth completes)
  └────┬─────┘
       │
       ▼
  ┌──────────┐
  │  active  │  ←──────┐
  └────┬─────┘         │
       │               │
       ├── query completes → ───┐
       │                        │
       ▼                        ▼
  ┌──────────────┐       ┌────────┐
  │idle in tx    │       │  idle  │
  └──────┬───────┘       └───┬────┘
         │                   │
         ├── tx commits ─────┘
         │               │
         │               ├── new query ───→ active
         │               │
         │               └── connection close ───→ END
         │
         └── tx aborts ───────┐
                              ▼
                     ┌────────────────┐
                     │idle in tx (aborted)│
                     └───────┬────────┘
                             │
                             └── ROLLBACK ───→ idle
```

### State transitions

| From | To | Trigger |
|------|----|---------|
| startup | active | Auth + startup reply complete |
| active | idle | Query completes (commit/rollback of implicit txn) |
| active | idle in tx | BEGIN / START TRANSACTION |
| idle in tx | active | New statement within transaction |
| idle in tx | idle | COMMIT / ROLLBACK (explicit) or statement-end implicit commit |
| idle in tx | idle in tx (aborted) | Statement error within transaction |
| idle in tx (aborted) | idle | ROLLBACK / COMMIT |
| idle | active | New query received |
| idle | END | Connection closed |

### v0 simplification

goopg v0 wraps every simple-Query batch in an implicit
ReadCommitted transaction (see `dispatchSimpleQueryViaExecutor`).
This means the "idle in transaction" state is not reachable via the
simple-query protocol path — it becomes relevant when the extended
query protocol (`BEGIN` / `COMMIT` as separate messages) lands.

Stage A tracks only `active` ↔ `idle` transitions at the
simple-query dispatch boundary. Extended-query transaction state
tracking is deferred.

## Timing-Field Semantics

### `backend_start`

Set once at connection register time in `serveConn`:
```go
BackendStart: time.Now().UTC().Format(time.RFC3339Nano)
```
Never updated after registration.

### `query_start`

Set at the start of each `dispatchSimpleQueryViaExecutor` invocation
via `UpdateState(pid, "active", query)`. The `UpdateState` method
stamps `time.Now()` onto both `query_start` and `state_change`.

### `xact_start`

Set when `BeginTransaction(pid)` is called. In v0's simple-query path,
the implicit transaction begins before `dispatchSimpleQueryViaExecutor`
and commits after it returns, so `xact_start` would always equal
`query_start` for single-statement batches. Extended protocol support
will drive `BeginTransaction` / `EndTransaction` explicitly.

Stage A leaves `xact_start` as the empty string (SQL NULL) since the
simple-query path provides no clean hook point for a separate
transaction-start timestamp.

### `state_change`

Updated by every call to `UpdateState`, `BeginTransaction`, or
`EndTransaction`. Always set to `time.Now().UTC().Format(RFC3339Nano)`.

### Timestamp format

All timestamps use `time.RFC3339Nano` (e.g.
`2026-04-30T12:34:56.123456789Z`). Downstream wire-protocol rendering
is text — clients parse via `timestamptz`-capable drivers.

## Concurrency Model

### The Registry

`internal/activity.Registry` uses a single `sync.RWMutex` protecting
the `backends map[string]*Backend`:

| Operation | Lock | Called from |
|-----------|------|-------------|
| Register | Write | serveConn goroutine |
| Unregister | Write | serveConn goroutine (deferred) |
| UpdateState | Write | dispatchSimpleQueryViaExecutor goroutine |
| BeginTransaction | Write | Future: extended query path |
| EndTransaction | Write | Future: extended query path |
| Snapshot | Read | VirtualRows callback (plan-time, any goroutine) |

### Consistency model

- `Snapshot` acquires a read lock and copies all `*Backend` values
  by value (shallow copy of the struct). The returned slice is
  independent of the live map.
- Mutating operations (`Register`, `UpdateState`, `Unregister`)
  acquire a write lock and modify in place.
- A snapshot taken while `dispatchSimpleQueryViaExecutor` has set
  state to "active" but has not yet returned it to "idle" will
  reflect "active". This is intentional: a concurrent `SELECT * FROM
  pg_stat_activity` sees the state that was true at the instant of
  the snapshot.

### Race safety

- No backends field can be modified without holding `mu`.
- `Unregister` during a `Snapshot` is safe: the snapshot copies
  before the delete.
- `Register` for an existing PID replaces the old entry atomically
  (map assignment under write lock).

## Query Text Capture

### Scope

Every statement dispatched through `dispatchSimpleQueryViaExecutor`
captures its text. The fast-path queries (SHOW, SET, RESET) do not
update the query text.

### Truncation

Captured text is truncated to 1024 bytes (matching upstream's default
`track_activity_query_size`). Longer queries are silently clipped;
there is no `…` suffix added.

### Memory

The `Backend.Query` field is a Go string sharing the underlying
backing array with the wire-protocol payload for the duration of the
text. After the first garbage collection this is just the string
header (24 bytes) plus the text (up to 1024 bytes). No per-query
allocation beyond the string itself.

## Integration Points

### Lifecycle hooks already wired

| Hook | Location | Effect |
|------|----------|--------|
| Register | `serveConn` after auth | Creates backend entry, state="active" |
| UpdateState("active", sql) | `dispatchSimpleQueryViaExecutor` before loop | Sets state, query, query_start |
| UpdateState("idle", "") | `dispatchSimpleQueryViaExecutor` after loop | Clears query, sets state="idle" |
| Unregister | `serveConn` defer | Removes backend entry |

### Future hooks

| Hook | Location (planned) | Effect |
|------|--------------------|--------|
| BeginTransaction | Extended-query Parse/Bind/Execute loop | state="idle in transaction", xact_start |
| EndTransaction | Extended-query Sync/Flush | state="idle", clears xact |
| WaitEvent(start/end) | Stage B: lock manager, AIO, client I/O | Sets wait_event_type/wait_event |

## References

- PostgreSQL backend status tracking:
  `postgres/src/backend/utils/activity/backend_status.c`
- Upstream state transitions:
  `postgres/src/backend/tcop/postgres.c` (especially `exec_simple_query`,
  `PostgresMain`, `ReadyForQuery`)
- `docs/design/0022-0001-pg-stat-activity-catalog-and-column-contract.md`
