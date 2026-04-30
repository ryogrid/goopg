# REF-022: Session & Transaction Management

## Overview

Session management covers connection lifecycle, transaction state, MVCC snapshot management, and GUC configuration. Each client connection has a dedicated goroutine with an associated session registry.

## goopg Implementation

**Packages:** `internal/server/server.go`, `internal/mvcc/`, `internal/config/session.go`

### Connection Lifecycle

```
serveConn:
  ├─ TCP accept → goroutine
  ├─ Startup handshake (auth, parameter exchange)
  ├─ Register backend in activity registry
  ├─ Create SessionRegistry
  ├─ Loop: ReadFrame → handleQuery → WriteFrame
  └─ Connection close → cleanup
```

### Session Registry

`config.SessionRegistry` holds per-connection GUC values. It provides:
- `Get(name)` — read effective value.
- `Set(name, value, isLocal)` — set value.
- `Reset(name)` / `ResetAll()` — restore defaults.
- `SetReportableHook(func)` — emit ParameterStatus on changes.

### Transaction Management

`mvcc.Manager` manages transaction state:

```go
tx, err := mgr.Begin(IsolationReadCommitted)
snap, err := mgr.SnapshotFor(tx)
// ... execute queries ...
err = mgr.Commit(tx)   // or mgr.Rollback(tx)
```

Each simple-query batch runs inside an implicit
ReadCommitted transaction:

```
dispatchSimpleQueryViaExecutor:
  ├─ TxnMgr.Begin
  ├─ Refresh snapshot
  ├─ Execute statements
  ├─ TxnMgr.Commit (→ xactMarker → WAL Append)
  └─ Write ReadyForQuery
```

### GUC Configuration

GUCs are defined in `internal/config/defaults.go` with type,
default value, and category. The registry applies config file
entries on top of built-in defaults. Runtime changes (SET)
update the session registry.

### MVCC Snapshot

`SnapshotFor(tx)` returns a snapshot containing:
- `Xmin` — lowest in-progress XID.
- `Xmax` — highest completed XID + 1.
- `XipList` — XIDs in-progress at snapshot time.

Tuple visibility checks use the snapshot:
- `xmin < xmin`: committed → visible if not aborted.
- `xmin in xip`: in-progress → invisible.
- `xmax < xmin`: deleted by committed xact → invisible.
- `xmax in xip`: deleted by in-progress → visible (will be retried).

## PostgreSQL Implementation

PostgreSQL's session management is process-based:

- **Process model** — each connection is a separate OS process
  (`fork()`), not a goroutine. This gives stronger isolation but
  higher per-connection overhead.
- **Transaction state** — stored in `TopTransactionState` and
  `CurrentTransactionState` (process-local variables). goopg
  stores transaction state in the mvvcc Manager.
- **Subtransactions** — PostgreSQL supports `SAVEPOINT` and
  subtransaction nesting. goopg does not.
- **Prepared transactions** — PostgreSQL supports two-phase commit
  via `PREPARE TRANSACTION` / `COMMIT PREPARED`. goopg does not.
- **pg_stat_activity** — shared memory array, updated by each
  backend. goopg uses an in-memory registry.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Connection model | Goroutine per connection | Process per connection |
| Subtransactions | Not implemented | SAVEPOINT |
| Two-phase commit | Not implemented | PREPARE TRANSACTION |
| GUC storage | Per-session registry | Shared memory + process-local |
| Transaction manager | `mvcc.Manager` in-memory | Shared-memory transaction state |

## References

- goopg: `internal/server/server.go`
- goopg: `internal/mvcc/manager.go`
- goopg: `internal/config/session.go`
- PG transaction: `postgres/src/backend/access/transam/xact.c`
- PG session: `postgres/src/backend/tcop/postgres.c`
