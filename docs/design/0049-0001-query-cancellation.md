# Query Cancellation (CancelRequest) — M0049-0001

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Without query cancellation, a long-running query (e.g. `pg_sleep(60)` or a
slow full-table scan) cannot be interrupted by the client. psql Ctrl-C sends
a PostgreSQL CancelRequest, which the server must dispatch to the running
query and return SQLSTATE 57014 within 200ms.

## 2. Design

### 2.1 Backend cancel registry (`internal/server/cancel.go`)

A server-wide registry maps backend pid → `cancelEntry`:

```go
type cancelEntry struct {
    mu          sync.Mutex
    secretKey   uint32
    queryCancel context.CancelFunc  // nil when idle
}

type backendCancelRegistry struct {
    mu      sync.Mutex
    entries map[uint32]*cancelEntry
}
```

`register(pid, secretKey)` is called in `serveConn` once per connection.
`unregister(pid)` is deferred. Both operations are guarded by the registry's
mutex.

### 2.2 Per-query cancellable context

In `runPostStartupLoop`, each `MsgQuery` and `MsgExecute` frame creates a
fresh per-query context:

```go
queryCtx, queryCancel := context.WithCancel(connCtx)
entry.setQueryCancel(queryCancel)
// ... dispatch query ...
entry.clearQueryCancel()
queryCancel()
```

The cancel function is stored in the `cancelEntry` so an incoming
`CancelRequest` can fire it. `clearQueryCancel` runs under the entry's mutex
so a race-y cancel arriving after the query has already finished is a no-op.

### 2.3 CancelRequest handler (`internal/server/server.go`)

`handleStartup` handles `protocol.CancelRequestCode` (magic code 80877102):

```go
case protocol.CancelRequestCode:
    if len(payload) == 8 {
        pid    := binary.BigEndian.Uint32(payload[0:4])
        secret := binary.BigEndian.Uint32(payload[4:8])
        s.cancelReg.cancelQuery(pid, secret)
    }
    return nil, errors.New("cancel request handled")
```

The canceling TCP connection is closed immediately without any reply,
as required by the PostgreSQL wire protocol specification.

### 2.4 Context propagation (`internal/executor/context.go`)

`executor.Context` gains a `Ctx context.Context` field. The dispatch
functions thread the per-query context:

- `dispatchSimpleQueryViaExecutor(ctx, ...)` → `ectx.Ctx = ctx`
- `executeExtendedQueryViaExecutor(ctx, ...)` → `ectx.Ctx = ctx`
- `dispatchCopyViaExecutor(ctx, ...)` → `ectx.Ctx = ctx`

Lock acquisition in `acquireRelLock` / `acquireTupleLock` substitutes
`ctx.Ctx` for `context.Background()` so blocked lock waits also unblock on
cancellation, mapping to the existing SQLSTATE 57014 path.

### 2.5 Operator poll points

Long-running operators check `ctx.Ctx.Err()` at natural loop boundaries:

| Location | Poll point |
|---|---|
| `seqScanOp.Next()` | Per-block boundary (before pinning a new page) |
| `aggregateOp.Open()` | Per-row in the aggregation drain loop |

When cancelled, operators return `&ExecError{Code: "57014", Message: "canceling statement due to user request"}`.

### 2.6 `pg_sleep()` (`internal/executor/expr.go`)

`pg_sleep(seconds)` accepts int or numeric. It sleeps using a `select` on
`time.After(d)` and `ctx.Ctx.Done()`:

```go
select {
case <-time.After(d):
case <-ctx.Ctx.Done():
    return Datum{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
}
```

Zero/negative duration is clamped to 0 and returns immediately. NULL returns NULL.

### 2.7 BackendKeyData

The secretKey is generated once per connection in `serveConn` (before
`sendStartupReply`) and stored in the cancel registry. `sendStartupReply`
accepts the pre-generated secret so the wire message and the registry entry
carry the same value.

## 3. Correctness

- `cancelEntry.mu` makes `setQueryCancel`/`clearQueryCancel`/`cancel` race-free.
- A late cancel arriving after `clearQueryCancel` is a no-op.
- A `cancelQuery` with a mismatched secretKey is silently ignored.
- The connection context (`connCtx`) remains uncancelled; only the per-query
  `queryCtx` is cancelled. The connection accepts the next query normally.

## 4. Tests

| Test | Coverage |
|---|---|
| `TestE2E_QueryCancellation_DoDPgSleep` | **DoD**: `pg_sleep(60)` with context cancel (lib/pq sends CancelRequest) returns within 200ms with SQLSTATE 57014 (actual: ~101ms) |
