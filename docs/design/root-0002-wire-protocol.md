# 0002 — Wire Protocol (v0: Listener, Framing, Startup Handshake)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 1 in `.ralph/fix_plan.md` calls for a TCP listener that accepts
PostgreSQL frontend connections, performs the v3 startup handshake, and is
ready to send `ReadyForQuery`. No SQL execution yet; no authentication
beyond `trust`-equivalent acceptance. This doc records the scope of v0,
the framing model, and the growth path so that later docs (`0003-…` for
auth, milestone-2 work for the simple Query path, milestone-6 for extended
query) can extend this without re-litigating fundamentals.

References into upstream:

- `postgres/src/include/libpq/pqcomm.h` — protocol version constants and
  the `MAX_STARTUP_PACKET_LENGTH` cap.
- `postgres/src/backend/tcop/backend_startup.c:492` — `ProcessStartupPacket`
  (length read, special codes, version range check, key/value payload
  parsing).
- `postgres/src/backend/tcop/postgres.c:4255-4339` — cancel-key generation
  and the `BackendKeyData` emission pattern.
- `postgres/src/include/libpq/protocol.h` — `PqMsg_*` byte tags
  (`'R'` AuthenticationRequest, `'S'` ParameterStatus, `'K'` BackendKeyData,
  `'Z'` ReadyForQuery, `'E'` ErrorResponse).
- `postgres/src/backend/utils/misc/guc_tables.c` — entries flagged with
  `GUC_REPORT` are the canonical list of ParameterStatus settings
  reported at startup.

## Decision

### Scope of v0

`internal/protocol` provides:

1. A wire-level **frame reader** that consumes one PostgreSQL backend
   message at a time from an `io.Reader`, and a **frame writer** that
   emits messages to an `io.Writer`. Both are byte-oriented; no message
   semantics live in this layer.
2. A **startup-packet decoder** that reads the length-prefixed startup
   packet (no leading type byte, per protocol), recognises the
   `CancelRequest`, `SSLRequest`, and `GSSENCRequest` magic codes, and
   otherwise parses protocol version + key/value parameter pairs.
3. **Encoders** for the messages a v0 server emits:
   `AuthenticationOk`, `ParameterStatus`, `BackendKeyData`,
   `ReadyForQuery`, `ErrorResponse`, and `NoticeResponse`.

`internal/server` ties them together:

- Listens on a configurable TCP `host:port` (default `127.0.0.1:5432` in
  v0; `0.0.0.0` only when explicitly requested, matching the security
  posture of a fresh `initdb`).
- Spawns one goroutine per accepted connection.
- For each connection, performs the startup sequence:
  - Reject `SSLRequest` and `GSSENCRequest` with the single-byte `'N'`
    response and re-read the next startup packet (the spec does not
    require TLS for v0; clients fall back to cleartext).
  - Reject `CancelRequest` (silently close — v0 has no backends to
    cancel).
  - Refuse protocol versions outside `[3.0, 3.0]` with a v0
    `ErrorResponse` and disconnect. v3.2 negotiation, including the
    `NegotiateProtocolVersion` message, is deferred.
  - Send `AuthenticationOk` (no real auth in v0; pretend the client
    is trusted).
  - Send the `ParameterStatus` block listed below.
  - Send `BackendKeyData` with `pid` = a stable per-connection ID
    minted by the server and `secret_key` = 4 random bytes (matching
    the protocol-3.0 length).
  - Send `ReadyForQuery('I')`.
  - Sit in a loop reading messages; respond to anything other than
    `Terminate('X')` with an `ErrorResponse` (`SQLSTATE 0A000`,
    "feature not supported") and continue.
- A graceful shutdown closes the listener and cancels the per-connection
  contexts. Connection goroutines exit on context cancellation, on
  client `Terminate`, or on EOF.

### Framing model

Every PostgreSQL backend/frontend message after the startup packet has
the same shape:

```
+--------+-------------+-----------------------------+
| 1 byte | 4 bytes BE  | (length-4) bytes of payload |
| type   | length      | (excludes type byte)        |
+--------+-------------+-----------------------------+
```

The startup packet is the only exception: no type byte. Its first 4 bytes
are the big-endian length (including itself), and its first 4 payload
bytes are the protocol version (or one of the special codes).

The frame reader enforces:

- Minimum length: 4 (the length field itself).
- Maximum length: 1 MiB for normal messages (defensive); 10 000 bytes for
  the startup packet, matching `MAX_STARTUP_PACKET_LENGTH`.
- Reads happen through a `*bufio.Reader` so that small headers don't
  cost a syscall apiece.
- A `ReadFrame` call returns the type byte and a slice borrowed from an
  internal buffer; the caller must finish using the slice before the
  next `ReadFrame` call. (No pooling yet; pool when a profile motivates
  it.)

The frame writer takes a type byte and a payload `[]byte`, prepends the
header, and writes them in a single `Write` for small payloads and via a
two-call sequence backed by a `*bufio.Writer` for larger ones. We do not
expose vectored writes to keep the surface small; payload assembly uses a
`bytes.Buffer`-style helper that grows once and writes once.

### Startup packet decoding

After reading the length-prefixed payload:

- Read the first 4 bytes as the protocol version (`uint32` BE).
- If it is `CANCEL_REQUEST_CODE` (0x04D2_162E), `NEGOTIATE_SSL_CODE`
  (0x04D2_162F), or `NEGOTIATE_GSS_CODE` (0x04D2_1630), the rest of the
  payload is interpreted accordingly.
- Otherwise it is a regular startup packet: a sequence of NUL-terminated
  C-strings forming `key`, `value` pairs, terminated by an empty key.
  Both `pq_getbytes` upstream and our parser require the *whole* buffer
  to be present before parsing, so there is no streaming string read.

The parsed startup parameters are returned as a `map[string]string`. We
preserve the *first* occurrence of duplicate keys, matching upstream's
behavior of treating the parameter list as an associative array. Unknown
keys are passed through untouched and mostly ignored in v0; `user`,
`database`, and `application_name` are read explicitly.

### ParameterStatus block

The v0 server sends ParameterStatus messages with the following keys, in
this order:

| Key                         | v0 value                               | Source                                         |
| --------------------------- | -------------------------------------- | ---------------------------------------------- |
| `server_version`            | `"18.3"`                               | docs/design/root-0001-architecture-overview.md §5   |
| `server_encoding`           | `"UTF8"`                               | spec §4.2                                      |
| `client_encoding`           | `"UTF8"`                               | spec §4.2                                      |
| `application_name`          | echoed from `StartupMessage` (or `""`) | spec §4.2                                      |
| `is_superuser`              | `"off"`                                | safest default before auth lands               |
| `session_authorization`     | echoed `user` parameter                | matches upstream                               |
| `DateStyle`                 | `"ISO, MDY"`                           | upstream default                               |
| `IntervalStyle`             | `"postgres"`                           | upstream default                               |
| `TimeZone`                  | `"UTC"`                                | conservative default                           |
| `integer_datetimes`         | `"on"`                                 | spec §4.2                                      |
| `standard_conforming_strings` | `"on"`                               | spec §4.2                                      |
| `in_hot_standby`            | `"off"`                                | matches upstream                               |
| `default_transaction_read_only` | `"off"`                            | matches upstream                               |

The set is deliberately small enough to type out by hand for v0. Once
the GUC registry exists (milestone 4), the list collapses to "every GUC
flagged `GUC_REPORT`", driven by `internal/config` rather than hard-coded
in `internal/server`.

### BackendKeyData

For protocol 3.0 the cancel key is 4 bytes (`MyCancelKeyLength == 4` at
`postgres/src/backend/tcop/postgres.c:4269`). v0 emits:

- `pid` (`int32`): a per-connection ID minted by an `atomic.Uint32`
  starting at 1. This is *not* an OS PID; v0 has no real backend
  processes. A future doc will replace this with whatever identity we
  use for per-session pg_stat rows.
- `secret_key` (`int32`): 4 cryptographically-random bytes from
  `crypto/rand.Read`.

We track `(pid, secret_key) -> connection` in a server-scoped map so
that, when CancelRequest support arrives, lookup is O(1). The map exists
in v0 but holds no consumers yet.

### ErrorResponse and NoticeResponse encoding

`ErrorResponse` (`'E'`) and `NoticeResponse` (`'N'`) share the same body
format: a sequence of (1-byte field code, NUL-terminated string) pairs
terminated by a single NUL byte. v0 emits these field codes:

- `'S'` Severity (`ERROR`, `FATAL`, `WARNING`, `NOTICE`, …).
- `'V'` Severity (non-localised; same value).
- `'C'` SQLSTATE (5-character code, e.g. `0A000` for not-supported,
  `08P01` for protocol violation).
- `'M'` Message (human-readable, ASCII).
- `'F'` File (Go source file, debug aid).
- `'L'` Line (line number, debug aid).
- `'R'` Routine (Go function name, debug aid).

The full PostgreSQL field set (`D` detail, `H` hint, `P` position,
`p` internal position, `q` internal query, `W` where, `s` schema, `t`
table, `c` column, `d` data type, `n` constraint) is intentionally
deferred — v0 has no SQL execution path that would have meaningful
values for those.

SQLSTATE codes are mirrored from
`postgres/src/backend/utils/errcodes.txt`. v0 needs only a handful;
milestone 2 will introduce a generated `sqlstate` package with the full
table.

### Graceful shutdown

`server.Server.Run(ctx)` blocks until `ctx` is cancelled or `Accept` errors
permanently. Cancellation flow:

1. `cancel()` (from `goopg stop` or a translated `SIGTERM`) cancels the
   listener context.
2. The accept loop notices, calls `listener.Close()`, and stops
   accepting.
3. Each connection goroutine sees its own context cancelled. It writes
   an `ErrorResponse` ("server is shutting down", SQLSTATE `57P01`,
   matching upstream's `ADMIN_SHUTDOWN`) and closes the connection.
4. `Run` waits on a `sync.WaitGroup` until all connection goroutines
   return, then returns the original cancellation cause.

The translation from POSIX signals to context cancellation lives in
`cmd/goopg/main.go` (the `start` subcommand) — `internal/server` itself
neither imports `os/signal` nor knows about CLI flags.

### What this doc does NOT cover

- The simple Query protocol path (`'Q'` → RowDescription/DataRow/…).
  → milestone 2 work.
- The extended Query protocol (Parse/Bind/Describe/Execute/Sync, portals,
  prepared statements). → `0013-extended-query-protocol.md` (milestone 6).
- Authentication beyond v0's "always say AuthenticationOk".
  → `0003-authentication.md` (milestone 3).
- TLS or GSSAPI encryption. v0 always answers SSLRequest/GSSENCRequest
  with `'N'`. A future doc will introduce TLS once auth is in place.
- The CancelRequest flow. v0 silently drops it.
- Protocol-version negotiation (3.1, 3.2). v0 only accepts exactly 3.0.
  When we widen support, `NegotiateProtocolVersion` will be added in a
  follow-up doc.

## Alternatives Considered

- **Use a third-party library (`jackc/pgproto3`).** Rejected: this is
  a server, and `pgproto3` is primarily a frontend codec; mixing roles
  ends up importing more than we need. Also, the protocol surface is
  the project's bread and butter — owning it directly keeps us honest
  and avoids a layer that would have to be peeled back as we widen
  support (e.g. for protocol 3.2 negotiation, custom auth flows).
- **Eagerly support TLS in v0.** Rejected as scope creep. The spec lists
  TLS as part of the auth surface; folding it into the listener before
  authentication exists adds work without unblocking pgbench.
- **Type-safe message structs from day one.** Rejected. The set of
  messages a v0 server *sends* is small and well-known; the set it
  *receives* in v0 is just the startup packet plus "anything else, which
  becomes an error". A few hand-rolled encoders are clearer than a
  reflective marshalling layer at this stage.
