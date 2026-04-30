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

## PostgreSQL Implementation

PostgreSQL's wire protocol (`pqformat.c`, `pqcomm.c`) is the same
protocol goopg implements. The key differences are in scope:

- **Extended query** — PostgreSQL fully implements Parse/Bind/Execute
  with prepared statements, parameter types, and portal semantics.
  goopg only supports the simple-query path.
- **COPY** — PostgreSQL supports COPY with binary and text formats,
  with full WAL-logging for COPY FROM. goopg's COPY support is
  basic.
- **Cancellation** — PostgreSQL allows cancelling a running query
  via a separate CancelRequest message. goopg does not implement
  cancellation.
- **SSL** — PostgreSQL supports TLS via SSLRequest. goopg supports
  configurable auth policies.
- **SCRAM-SHA-256** — PostgreSQL 10+ supports SCRAM authentication.
  goopg supports MD5 and trust.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Extended query | Stub (0A000) | Full Parse/Bind/Execute |
| Query cancellation | Not implemented | CancelRequest message |
| COPY format | Text only | Text + Binary |
| SCRAM auth | Not implemented | SCRAM-SHA-256 |
| GSSAPI | Not implemented | Supported |

## References

- goopg: `internal/protocol/frame.go`
- goopg: `internal/server/query.go`
- PG protocol docs: https://www.postgresql.org/docs/current/protocol.html
- PG protocol implementation: `postgres/src/backend/libpq/pqformat.c`
