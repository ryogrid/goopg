# 0022-0001 — pg_stat_activity: catalog/view shape and column contract

## Status

draft

## Goal

Define the `pg_catalog.pg_stat_activity` virtual view for goopg v0,
specify the backend-activity tracker that backs it, and document the
Stage A column contract.

## Motivation

goopg lacks a `pg_stat_activity` surface, making it impossible for
operators and monitoring tools to introspect active backends, their
current query, transaction state, or wait events. This design doc
covers the Stage A delivery (view shape + backend lifecycle + timing
fields); Stage B (wait-event taxonomy and recording hooks) lands in a
follow-up design doc.

## Virtual View: `pg_catalog.pg_stat_activity`

### Column Definitions (Stage A)

Stage A exposes a PostgreSQL-compatible subset of the upstream
`pg_stat_activity` columns. All values are rendered as text (v0
catalog convention); the downstream client or language driver is
responsible for type coercion.

| # | Column           | Type   | Stage | Notes                                      |
|---|------------------|--------|-------|--------------------------------------------|
| 1 | `datid`          | text   | A     | Database OID; `"0"` for non-database processes |
| 2 | `datname`        | text   | A     | Database name                              |
| 3 | `pid`            | text   | A     | Backend PID (OS thread / goroutine ID)     |
| 4 | `leader_pid`     | text   | A     | Always NULL in v0 (no parallel query)      |
| 5 | `usesysid`       | text   | A     | User OID                                   |
| 6 | `usename`        | text   | A     | User name                                  |
| 7 | `application_name`| text  | A     | Client-supplied application name           |
| 8 | `client_addr`    | text   | A     | Client IP address (text), NULL for Unix    |
| 9 | `client_hostname`| text   | A     | Always NULL in v0 (no reverse DNS)         |
| 10| `client_port`    | text   | A     | Client port number, NULL for Unix          |
| 11| `backend_start`  | text   | A     | Timestamp when backend was started         |
| 12| `xact_start`     | text   | A     | Timestamp when current transaction began   |
| 13| `query_start`    | text   | A     | Timestamp when current query began         |
| 14| `state_change`   | text   | A     | Timestamp when state last changed          |
| 15| `wait_event_type`| text   | A     | Always NULL in Stage A                     |
| 16| `wait_event`     | text   | A     | Always NULL in Stage A                     |
| 17| `state`          | text   | A     | Backend state: `active`, `idle`, `idle in transaction` |
| 18| `backend_xid`    | text   | A     | Current transaction XID, NULL when none    |
| 19| `backend_xmin`   | text   | A     | Current snapshot xmin, based on MVCC horizon |
| 20| `query`          | text   | A     | Current query text (last 1 KB in v0)      |
| 21| `backend_type`   | text   | A     | Backend type: `client_backend`, `checkpointer`, `autovacuum_launcher`, etc. |

### Nullability

- Columns marked with "Always NULL" in Stage A return NULL text (empty
  string, rendered as SQL NULL by the protocol layer when the cell is
  nil).
- `client_hostname` is always NULL.
- `leader_pid` is always NULL.
- `backend_xid` and `backend_xmin` are NULL when no transaction is
  active.
- `wait_event_type` and `wait_event` are NULL in Stage A.

### VirtualRows Callback

The `VirtualRows` callback iterates a global backend-activity registry
and produces one output row per registered backend. Registration
happens at connection startup and deregistration at connection
teardown. The registry is a `sync.RWMutex`-guarded slice.

```go
tbl.VirtualRows = func() [][]string {
    snap := activity.Snapshot()
    rows := make([][]string, 0, len(snap))
    for _, b := range snap {
        rows = append(rows, []string{
            b.DatID,          // datid
            b.DatName,        // datname
            b.PID,            // pid
            "",               // leader_pid (always NULL)
            b.UserSysID,      // usesysid
            b.UserName,       // usename
            b.ApplicationName,// application_name
            b.ClientAddr,     // client_addr
            "",               // client_hostname (always NULL)
            b.ClientPort,     // client_port
            b.BackendStart,   // backend_start
            b.XactStart,      // xact_start
            b.QueryStart,     // query_start
            b.StateChange,    // state_change
            "",               // wait_event_type (Stage A: NULL)
            "",               // wait_event (Stage A: NULL)
            b.State,          // state
            b.BackendXID,     // backend_xid
            b.BackendXMin,    // backend_xmin
            b.Query,          // query
            b.BackendType,    // backend_type
        })
    }
    return rows
}
```

## Backend Activity Registry

### Location

New package: `internal/activity/`

### Structs

```go
package activity

type Backend struct {
    PID             string // OS PID (or synthetic ID)
    DatID           string
    DatName         string
    UserSysID       string
    UserName        string
    ApplicationName string
    ClientAddr      string
    ClientPort      string
    BackendStart    string // RFC3339 timestamp
    XactStart       string
    QueryStart      string
    StateChange     string
    State           string // "active", "idle", "idle in transaction"
    BackendXID      string
    BackendXMin     string
    Query           string
    BackendType     string // "client_backend" for normal connections
}

type Registry struct {
    mu       sync.RWMutex
    backends map[string]*Backend // keyed by PID
}
```

### Methods

```go
func NewRegistry() *Registry

// Register adds or updates a backend entry.
func (r *Registry) Register(b *Backend)

// Unregister removes a backend entry by PID.
func (r *Registry) Unregister(pid string)

// Snapshot returns a consistent copy of all backends.
func (r *Registry) Snapshot() []Backend

// UpdateState updates the state and query for a backend.
func (r *Registry) UpdateState(pid, state, query string)
```

### Thread Safety

- `Register` / `Unregister` / `UpdateState` are called from connection
  handler goroutines.
- `Snapshot` is called from plan-time (the VirtualRows callback).
- All access is protected by `sync.RWMutex`.

## Integration Points

### Server-side wiring

The `Registry` is created in `initdb.Open()` and stored in
`Runtime.Activity`. The `server.Config` gains an `Activity *activity.Registry`
field, similar to `LockMgr`, `PubSub`, etc.

### Connection lifecycle

1. **Connection accepted** (`server.serveConn`):
   - Backend registered with `pid`, `datname`, `usename`, `client_addr`,
     `client_port`, `backend_start = now()`, `state = "active"`.

2. **Query received** (`server.handleQuery`):
   - `UpdateState(pid, "active", query_text)` at the start of query processing.
   - Store `query_start = now()`.

3. **Query complete** (`server.dispatchSimpleQueryViaExecutor` returns):
   - `UpdateState(pid, "idle", last_query)`.
   - Record `state_change = now()`.

4. **Transaction begin**:
   - `UpdateState(pid, "idle in transaction", query)`.
   - Record `xact_start = now()`.

5. **Connection closed**:
   - `Unregister(pid)`.

### Query text truncation

v0 stores the last 1024 bytes of query text per backend. Longer queries
are truncated with a `…` suffix (or simply clipped — matching upstream's
`track_activity_query_size` GUC behaviour).

## Out of Scope (Stage B)

- Wait-event taxonomy and recording hooks
- Non-client backend types (checkpointer, autovacuum launcher, WAL writer)
- Lock-wait and backtrace introspection
- Full `track_activity_query_size` GUC (v0 hardcodes 1024)
- Permission/visibility filtering (all rows visible to all users in v0)

## References

- `docs/milestones/0022-pg-stat-activity-support.md`
- PostgreSQL `pg_stat_activity` columns:
  https://www.postgresql.org/docs/current/monitoring-stats.html#PG-STAT-ACTIVITY-VIEW
