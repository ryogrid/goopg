# REF-021: Protocol & Wire Format

## Overview

goopg implements the PostgreSQL wire protocol (version 3.0) for client-server communication. This covers the startup handshake, simple-query, extended-query, COPY, and replication protocols.

## goopg Implementation

**Package:** `internal/protocol/`

### Message Flow

```
Client                          Server
  │                               │
  ├─ StartupMessage ─────────────►│
  │                               ├─ AuthenticationOk
  │                               ├─ ParameterStatus (×N)
  │                               ├─ BackendKeyData
  │                               └─ ReadyForQuery
  │                               │
  ├─ Query (SELECT 1) ───────────►│
  │                               ├─ RowDescription
  │                               ├─ DataRow (×N)
  │                               ├─ CommandComplete
  │                               └─ ReadyForQuery
  │                               │
  ├─ Query (INSERT …) ───────────►│
  │                               └─ CommandComplete
  │                               └─ ReadyForQuery
```

### Frame Format

Each message (after startup) has:
```
┌─────────────────────────────────┐
│ Type byte (1)                    │  e.g., 'Q' = Query, 'P' = Parse
│ Length (4) big-endian           │  includes self + payload
│ Payload (variable)              │
└─────────────────────────────────┘
```

### Supported Message Types

| Byte | Message | Support |
|------|---------|---------|
| `Q` | Simple Query | Full |
| `P` | Parse (extended) | Stub (returns 0A000) |
| `B` | Bind | Stub |
| `E` | Execute | Stub |
| `X` | Terminate | Full (connection close) |
| `H` | Flush | Stub |
| `S` | Sync | Stub |
| `d` | CopyData | Full |
| `c` | CopyDone | Full |
| `f` | CopyFail | Full |

### Startup Packet

The startup packet contains protocol version and key-value parameters:
- `user`, `database`, `application_name`, `replication`, etc.

goopg supports:
- PostgreSQL 3.0 protocol (version 196608 = 3.0).
- SSLRequest (gated by `Policy`).
- GSSENCERequest (rejected).

### Reply Messages

| Byte | Message | Used for |
|------|---------|----------|
| `R` | AuthenticationOk / AuthenticationMD5Password | Auth handshake |
| `S` | ParameterStatus | Session settings |
| `K` | BackendKeyData | Cancel key |
| `Z` | ReadyForQuery | Transaction status |
| `T` | RowDescription | Column metadata |
| `D` | DataRow | Row data |
| `C` | CommandComplete | Statement completion tag |
| `E` | ErrorResponse | Error with SQLSTATE |
| `N` | NoticeResponse | Warning / notice |
| `1` | ParseComplete | Extended-query |
| `2` | BindComplete | Extended-query |
| `3` | CloseComplete | Extended-query |
| `n` | NoData | Extended-query |
| `s` | PortalSuspended | Extended-query |
| `W` | CopyInResponse | COPY FROM |
| `G` | CopyOutResponse | COPY TO |

## PostgreSQL Implementation (Deep Dive)

### Extended Query Protocol

PostgreSQL's extended-query protocol (`FE_BE_Exec.c`) supports:

- **Parse** — prepares a statement. The SQL text is parsed,
  analysed, and planned. The resulting `PreparedStatement` is
  cached in a backend-local hash table.
- **Bind** — supplies parameter values (`$1`, `$2`, …). Creates
  a portal with a bound plan.
- **Describe** — returns row metadata (column names, types).
- **Execute** — runs the portal, returning rows (or a completion
  tag).
- **Close** — deallocates the prepared statement or portal.
- **Sync** — commits the current transaction.

goopg stubs all extended-query messages with 0A000
(feature-not-supported).

### CancelRequest

PostgreSQL's `CancelRequest` message uses a separate TCP
connection to send the target backend's PID + secret key. The
cancel handler (`HandleCancelRequest`) sets the target backend's
`QueryCancelPending` flag. The backend checks this flag at
cancellation points during query execution.

goopg does not implement cancellation. A slow query cannot be
cancelled without closing the connection.

### COPY Binary Format

PostgreSQL's COPY supports two formats:
- **Text** — tab-delimited rows, with escape handling.
- **Binary** — a header (11 bytes: signature + flags + extension
  length) followed by column values in their native binary
  representation. The binary format is faster for large data
  loads because it avoids text encode/decode.

goopg only supports the text format for COPY.

### SCRAM-SHA-256 Authentication

PostgreSQL 10+ uses SCRAM-SHA-256 as the default password
authentication method. The protocol exchanges:
1. `SASLInitialResponse` (client sends client-first-message).
2. `AuthenticationSASLContinue` (server sends server-first-message).
3. `SASLResponse` (client sends client-final-message).
4. `AuthenticationSASLFinal` (server sends server-final-message).

goopg supports MD5 (`AuthenticationMD5Password`) and trust.
SCRAM is not implemented.

### Error and Notice Fields

PostgreSQL's `ErrorResponse` (and `NoticeResponse`) can include
multiple fields:

| Field | Code | Meaning |
|-------|------|---------|
| Severity | `S` | ERROR, FATAL, PANIC, WARNING, NOTICE, etc. |
| SQLSTATE | `C` | Error code (e.g., `42P01`) |
| Message | `M` | Human-readable message |
| Detail | `D` | Additional detail |
| Hint | `H` | Suggestion |
| Position | `P` | Character position in the original query |
| Internal Position | `p` | Position in internal query |
| Internal Query | `q` | The internal query text |
| Where | `W` | Context (e.g., function call stack) |
| Schema | `s` | Schema name |
| Table | `t` | Table name |
| Column | `c` | Column name |
| Data type | `d` | Type name |
| Constraint | `n` | Constraint name |
| File | `F` | Source file name |
| Line | `L` | Source file line number |
| Routine | `R` | Function name |

goopg sends only severity, SQLSTATE, and message.

## goopg Improvement Analysis

### P1: Extended Query (Parse/Bind/Execute)

Implement Parse/Bind/Execute for parameterised queries:
- Parse: full parser + analyzer + planner pass, cache the plan.
- Bind: store bound parameter values.
- Execute: run the plan with the bound values.

**Impact:** Eliminates parse/plan overhead for repeated queries.
pgbench's built-in queries would benefit significantly (the
same query text is executed hundreds of times).

### P2: Query Cancellation

Add a `CancelRequest` handler. Store each backend's cancel key
(PID + secret). On receiving a CancelRequest, set a
`cancelled` flag on the target context. Check this flag in
`evalExpr` and `Next()` loops.

### P2: Error Detail Fields

Populate more ErrorResponse fields (Detail, Hint, Position)
to improve developer experience.

## References

- goopg: `internal/protocol/frame.go`
- goopg: `internal/server/query.go`
- PG protocol: `postgres/src/backend/libpq/pqformat.c`
- PG extended query: `postgres/src/backend/tcop/postgres.c`
- PG cancel: `postgres/src/backend/tcop/postgres.c` (`HandleCancelRequest`)
- PG protocol docs: https://www.postgresql.org/docs/current/protocol.html
