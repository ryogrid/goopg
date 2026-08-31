# 0057-0003 — tpch-runner Per-Query Cancellation

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| milestone  | 0057 |

## 1. Problem

When `context.WithTimeout` fires in tpch-runner, `database/sql`
closes the TCP connection. The server-side backend goroutine keeps
running the query until it tries to write to the now-closed socket
(on the NEXT row batch emission), at which point it gets a SIGPIPE /
broken-pipe error and exits. For Q9/Q20 (very long-running queries),
this means the server continues consuming CPU for seconds or minutes
after the client has "given up".

**Expected behaviour:** the client sends a PostgreSQL `CancelRequest`
message; the server cancels the query via `context.Cancel` on the
backend goroutine; the backend returns SQLSTATE 57014 to the client;
the TCP connection stays alive; the client reports the error and moves
on to the next query.

## 2. Protocol

The PostgreSQL wire protocol defines `CancelRequest`:
1. A NEW TCP connection is opened to the same port.
2. The startup packet contains: `int32(16), int32(80877102),
   int32(pid), int32(secret)` where pid+secret were sent by the
   server in the backend's startup response
   (`AuthenticationOK / ReadyForQuery` phase, via `BackendKeyData`
   message).
3. The server finds the matching backend, signals its cancel context,
   and closes the cancel connection.

## 3. Server-side implementation

### 3.1 BackendKeyData emission

During startup (`internal/server/server.go`), after authentication
succeeds, send:
```
'K' | int32(12) | int32(pid) | int32(secret)
```
The `pid` is the internal backend PID already tracked in the activity
registry. The `secret` is a random 32-bit value generated per
connection.

Store `(pid, secret, cancelFunc)` in a process-global map protected
by a mutex: `cancelRegistry map[int32]cancelEntry`.

### 3.2 CancelRequest handling

In the server's TCP listener loop, detect a startup packet with
`protocol = 80877102` (the cancel magic constant). Read pid + secret.
Look up `cancelRegistry[pid]`. If secret matches, call
`entry.cancelFunc()`. Close the cancel connection immediately.

The matching backend's `ctx` gets cancelled. The query executor
propagates `ctx.Err()` as SQLSTATE 57014.

### 3.3 SQLSTATE 57014 propagation

The executor must check `ctx.Err()` at natural yield points
(per-row in SeqScan, per-block in IndexScan). This should already be
wired from the existing `context.WithTimeout` in tests; verify that
it reaches the backend's error writer as:
```
ErrorResponse{Code: "57014", Message: "canceling statement due to user request"}
```

## 4. Client-side implementation (tpch-runner)

### 4.1 BackendKeyData parsing

After the startup exchange, parse the `BackendKeyData` message ('K')
to extract pid + secret and store in the query runner.

### 4.2 `--cancel-after=<duration>` flag

When the deadline fires:
1. Open a fresh TCP connection to the same host:port.
2. Send the CancelRequest packet.
3. Close the cancel connection.
4. The primary connection receives `57014` and the query-context error
   surfaces via `rows.Err()`.
5. Log: `Q<N>: CANCELLED elapsed=Xs`.

### 4.3 Backward compat

If the server does not send `BackendKeyData` (e.g., older goopg
without this feature), skip the cancel and fall back to connection
close (existing behaviour).

## 5. Files

- `internal/server/server.go` — emit `BackendKeyData`, handle
  `CancelRequest`, cancel registry.
- `internal/protocol/messages.go` — add `BackendKeyData` message type.
- `cmd/tpch-runner/main.go` — parse `BackendKeyData`, implement
  `--cancel-after`.

## 6. Tests

- `TestCancelRequest` in `internal/server/` — confirm a concurrent
  `CancelRequest` surfaces as SQLSTATE 57014.
- `tpch-runner --queries=9 --cancel-after=5s` exits within ~6s.

## 7. Acceptance

See milestone doc M0057-0004 acceptance criterion.

## 8. References

- PostgreSQL wire protocol: `FrontendMessage CancelRequest`.
- `internal/server/server.go::handleConn`.
- `cmd/tpch-runner/main.go`.
