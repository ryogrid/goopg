# Module: `internal/postmaster`

The goopg server process: the TCP listener, the per-connection backend
lifecycle, the PostgreSQL v3 wire-protocol frame loop, SQL dispatch (simple +
extended + COPY), and the operator control plane. One goroutine per client
connection (`serveConn`) is the analogue of one PG backend process, owning the
active MVCC transaction/snapshot, pinned buffers, WAL inserts, and the
synchronous-commit flush. The `autovacuum/` subpackage is the ticker-driven
background vacuum/analyze launcher. **17,296 LOC** in the parent package.

## Key Files (by LOC)

| File                 | LOC  | Role |
|----------------------|------|------|
| `dispatch.go`        | 4,453 | Simple-query dispatcher: `dispatchSimpleQueryViaExecutor`, plan cache, auto-commit, per-statement loop, DDL bypasses, GC throttle, `executeOneSimpleStmt`. The largest file — the simple-protocol heartbeat. |
| `database_ddl.go`    | 2,018 | String-prefix DDL bypasses for CREATE/ALTER/DROP DATABASE: `classifyDatabaseDDL`, `handleDatabaseDDLBypass`, `tryHandleDatabaseDDL`, WAL-recording of database registration. |
| `server.go`          | 1,905 | `Server` listener; accept loop, per-conn goroutine, startup/auth handshake, frame loop, control plane, `Config`/`New`/`Run`. |
| `role_ddl.go`        | 1,374 | CREATE/ALTER/DROP ROLE/USER string-prefix bypasses: `tryHandleRoleDDL`, `splitLeadingRoleDDL`, role tracking in catalog. |
| `copy.go`            | 942   | COPY routing: CopyTo stream via `runCopyToStream`; CopyFrom arms `copyInState` and routes `CopyData`/`CopyDone`/`CopyFail` frames; text and CSV EOD detection. |
| `extended.go`        | 934   | Extended-protocol frames: Parse/Bind/Describe/Execute/Close handlers, `extendedState` (statements+portals), `extendedQueryResult` with row materialization, `payloadReader`. |
| `conn_tx.go`         | 857   | `connTxState`: explicit-txn holder, session identity trio, cursors, prepared statements, notify buffer, `OnCommitActions`, prepared-transaction GID. |
| `dispatch_extended.go`| 733  | Extended-query executor path: `executeExtendedQueryViaExecutor`, `tryHandleDatabaseOrRoleDDLExtended`, `tryCompatNoopExtended`, `paramsToDatums`. |
| `query.go`           | 698   | Legacy string-match fast paths (`SELECT 1`, `SHOW`, `SET/RESET`, `SET ROLE`/`SESSION AUTHORIZATION`), `writeQueryError`, `setIsSuperuserGUC`. |
| `txn_verb.go`        | 525   | Shared BEGIN/COMMIT/ROLLBACK/SAVEPOINT state machine: `applyTransactionVerb`, `txnVerbOutcome`, 25P01 warning, deferred FK/SSI/DDL-applier sequence. |
| `statement_log.go`   | 385   | `logStatement`/`logDuration` per-statement query logging (`GOOPG_LOG_STATEMENT`, `log_min_duration_statement`). |
| `notify.go`          | 329   | LISTEN/NOTIFY hub: `notifyHub` with `listeners[channel]` set and per-session `pending` FIFO; commit-visibility delivery. |
| `twophase.go`        | 321   | PREPARE TRANSACTION/COMMIT PREPARED/ROLLBACK PREPARED store: prepared-GID registry, 2PC persistence. |
| `plancache.go`       | 175   | Cross-session plan cache: 16 shards × 32 entries = 512 total, FNV-1a shard hash, doorkeeper admission filter, DDL invalidation. |
| `cancel.go`          | 154   | Cancel registry: `CancelBackend`/`pg_cancel_backend`/`pg_terminate_backend` by PID or procNum. |
| `eof_watch.go`       | 133   | Client-EOF watcher: per-query goroutine `recvfrom(MSG_PEEK)` every 500 ms cancels compute-bound queries when the client vanishes. |
| `grant_ddl.go`       | 834   | GRANT/REVOKE DDL handling, privilege checks, role-based access control. |
| `autovacuum/launcher.go` | 526 | Background autovacuum/autoanalyze launcher with its own Pool/TxnMgr/Catalog handles. |

## Public API

```go
func New(cfg Config) *Server
type Config struct{ Address, ServerVersion string; Logger; Policy auth.Policy;
    UserStore auth.UserStore; Registry *misc.Registry; Catalog catalog.Catalog;
    Pool *storage.Pool; TxnMgr *transam.Manager; LockMgr *lmgr.LockManager;
    Activity *activity.Registry; ShutdownDeadline; HandshakeTimeout; /* … */ }
type Server struct{ /* … */ }
func (s *Server) Run(ctx) error
func (s *Server) Addr() *net.TCPAddr / Ready() <-chan struct{}
func (s *Server) CancelBackend(pid int32) error / TerminateBackend(pid int32) error
func (s *Server) QueueNotify(sess *misc.SessionRegistry, channel, payload string)
func (s *Server) SetForcedGCEnabled(v bool)
type Notification struct{ PID uint32; Channel, Payload string }
func NewLauncher(pool, txnMgr, cat) *Launcher   // autovacuum subpackage
```

## Internal structure

### Connection lifecycle (`server.go`)

```mermaid
sequenceDiagram
    participant C as Client
    participant S as Server.acceptLoop
    participant SC as serveConn
    participant A as checkAuth
    participant T as TxnMgr
    participant P as runPostStartupLoop

    C->>S: TCP connect
    S->>SC: spawn goroutine per conn
    SC->>SC: handleStartup (parse StartupMessage params)
    SC->>A: checkAuth (trust/md5/scram/reject)
    A-->>SC: method response
    SC->>SC: sendStartupReply (ParameterStatus + BackendKeyData)
    SC->>SC: role/database/datconnlimit admission (3D000, 55000, 53300)
    SC->>T: AcquireConnSlot → procNum
    SC->>SC: register pg_stat_activity slot
    SC->>P: frame loop (MsgQuery, MsgParse, ..., MsgTerminate)
    P-->>SC: loop exits (EOF / Terminate / error)
    SC->>SC: rollbackOpenTxnOnTeardown, cleanupSessionTempObjects
```

`Run` binds the listener, sweeps stray temp files, starts the control plane /
apply-launcher / autovacuum, then `acceptLoop` spawns one goroutine per conn
via `serveConn`. `serveConn` runs the handshake, `checkAuth`, role/database/
datconnlimit admission, registers an activity slot, and enters
`runPostStartupLoop`.

Authentication methods: trust (loopback default), md5, SCRAM-SHA-256 (with
`scram_iterations` configurable per role), reject. The auth policy is the
`(conn-type, remote, database, user)` tuple from the StartupMessage; replication
connections skip the database-existence check.

Admission checks in order:
1. Unknown role → FATAL `28000` `role "x" does not exist`.
2. Unknown database → FATAL `3D000` `database "x" does not exist` (skipped for
   replication connections).
3. `datconnlimit = -2` (mid-DROP) → FATAL `55000` `cannot connect to invalid database`.
4. Positive `datconnlimit` exceeded (non-superuser) → FATAL `53300` `too many
   connections for database` (self-inclusive count, matching
   `CountDBConnections`).
5. Proc-slot exhaustion → `57314`-class reject.

### Simple protocol (`dispatch.go`)

```mermaid
flowchart TD
    A[MsgQuery] --> B{handleQueryOrCopy}
    B -->|COPY stmt| C[dispatchCopyViaExecutor]
    B -->|normal string| D{handleQuery fast path}
    D -->|SELECT 1 / SHOW / SET / RESET| E[query.go string-match]
    D -->|else| F[dispatchSimpleQueryViaExecutor]
    F --> G{classifyDatabaseDDL pre-parse}
    G -->|DROP DATABASE| H[handleDatabaseDDLBypass]
    G -->|no| I[parser.Parse]
    I -->|syntax error| J{DB/ROLE/noop bypass chain}
    I -->|ok| K[per-statement loop]
    K --> L{planCache lookup}
    L -->|hit| M[executor.BuildFastIterator]
    L -->|miss| N[parser plan + optimizer.Plan + cache Put]
    M --> O[Open/Next/Close + CommandComplete]
    O --> P[ReadyForQuery + maybeForceGCAfterCommit]
```

The simple-query path parses the entire SQL string; multi-statement batches
split semicolon-wise. Errors abort the run — every later statement in the same
Query message is skipped (upstream "abort-on-error"). The statement loop runs
`parser.Parse → plan-cache lookup → optimizer.Plan → executor.BuildFastIterator
→ Open/Next/Close → CommandComplete`, then a single ReadyForQuery.

The DDL bypass chain (all before real parsing fails):
- `DROP DATABASE` (which HAS parser grammar via `DropCompatStmt`) is
  intercepted *before* `parser.Parse` so the stub arm cannot shadow the real
  catalog-backed DROP.
- `CREATE/ALTER/DROP DATABASE` and `CREATE/ALTER/DROP ROLE` have no parser
  grammar; `handleDatabaseDDLBypass` / `tryHandleRoleDDL` run string-prefix
  matches, update the catalog, and emit a WAL record for crash survival.
- Multi-statement batches starting with role-DDL are peeled and recursed.
- Compat no-op forms (`GRANT`, `SCHEMA`, various no-ops) absorb whole
  statements with `compatNoopCommandTag`.

### Extended protocol (`extended.go` / `dispatch_extended.go`)

```mermaid
sequenceDiagram
    participant C as Client
    participant S as Server
    participant E as extendedState
    participant P as Planner

    C->>S: Parse (name, query, param OIDs)
    S->>P: plan (unless cached) → preparedStatement
    C->>S: Bind (portal, statement, params, formats)
    S->>E: portalState{Params, Result}
    C->>S: Describe (portal/statement)
    S->>S: describeExtendedQuery → RowDescription
    C->>S: Execute (portal, maxRows)
    S->>E: executeExtendedQueryViaExecutor → materialize ALL rows into Result
    S->>E: drain portal by maxRows batches (RowPos suspension)
    C->>S: Close (portal/statement)
    S->>E: delete from statements/portals
    C->>S: Sync
    S->>S: ReadyForQuery (or ErrorResponse on error, syncRequired)
```

- `extendedState` holds `statements` (prepared-statement map) and `portals`
  (portal map). Prepared statements carry name/query/param-count;
  portals carry statement ref + bound params + materialized result + RowPos.
- Executes **materialize all rows** into `extendedQueryResult`, then drain the
  portal with `maxRows` suspension. Out-of-block Executes auto-commit per
  statement (PG parity).
- `paramsToDatums` (dispatch_extended.go) feeds text-format bound params into
  `executor.Context.Params`; binary parameters are rejected at Bind time.
- Extended errors set `syncRequired` so the next Sync emits the error; a
  PARSE-time error inside an explicit transaction aborts the block (25P02).
- The extended path reuses `applyTransactionVerb` from txn_verb.go for
  BEGIN/COMMIT/ROLLBACK — no second implementation (M0132-S2).

### COPY (`copy.go`)

- CopyTo: `runCopyToStream` / `handleCopyToStdoutQuery` stream rows with
  CopyOutResponse + CopyData frames.
- CopyFrom: `handleCopyInFrame` arms a `copyInState` and routes
  `CopyData`/`CopyDone`/`CopyFail`; `consumeExecCopyData` (executor-backed) or
  `consumeTextCopyData` (text-mode) drain the frames; `commitCopyTx` finalizes.
- EOD detection (`isCopyTextEOD`) handles `\.` and `\.\n` text-mode terminators.

### Transaction state machine (`txn_verb.go`)

`applyTransactionVerb` is the single shared BEGIN/COMMIT/ROLLBACK/SAVEPOINT
implementation used by BOTH protocols. It returns a `txnVerbOutcome` (Handled,
Warn, Tag, Err) and each caller renders it in its own protocol shape. Key rules:
- COMMIT in a failed block → ROLLBACK tag.
- COMMIT/ROLLBACK outside a block → 25P01 WARNING, not an error.
- The inline deferred FK/UNIQUE/EXCLUDE checks, SSI pre-commit check, deferred
  DROP INDEX / ATTACH PARTITION / INHERIT / DROP TABLE / DROP FUNCTION
  appliers, and enum-DDL undo run here because this path bypasses
  `transactionOp.execCommit`.
- SAVEPOINT / ROLLBACK TO / RELEASE fall through to the executor (Handled=false).

### LISTEN/NOTIFY (`notify.go`)

`notifyHub` multiplexes all connections in one OS process. Sessions are
identified by their stable `*config.SessionRegistry`. LISTEN registers a
session in `listeners[channel]`; NOTIFY queues into `pending[session]` FIFO;
deliveries become visible only when the notifying transaction COMMITs
(commit-visibility, matching async.c). `bufferNotify`/`notifySavepoint`/
`notifyReleaseSavepoint`/`notifyRollbackToSavepoint` on `connTxState` handle
savepoint scoping. Delivered at command boundary (before ReadyForQuery).

### Plan cache (`plancache.go`)

- 16 shards, each `RWMutex` + `map[string]optimizer.Node` + FIFO `order`.
- Max 32 entries/shard = 512 total.
- Key: `planCacheKey(sql, dbOid)` — `normalizeCompatSQL(sql)` (lowercase,
  whitespace-collapsed, trailing-semicolon stripped) prefixed with the
  connection's effective namespace OID. Matches across sessions with minor
  whitespace differences but NOT across different namespaces (a plan embeds
  resolved `*catalog.Table`/`*catalog.Index` pointers).
- **Doorkeeper admission**: a key earns a slot only on its SECOND Put (one
  atomic `Swap` per Put, lock-free). Marks survive `Invalidate()` — "this SQL
  has been seen before" persists across DDL so hot statements re-admit
  immediately. This was added because under pgbench's simple protocol every
  statement had client-substituted literals, unique keys, all cache misses, and
  a write-lock storm on every Put (66% of remaining mutex delay).
- `Invalidate()` clears all shards atomically after every DDL statement.
- `planCacheIsCacheable` excludes DDL/Transaction/Copy nodes.

### Control plane & shutdown

`startControlPlane` handles operator commands; `stopControlPlane` on shutdown.
Shutdown ladder (`ShutdownDeadline`, default 120 s): drain in-flight
connections after the accept loop exits; `STOPIMMEDIATE` bypasses the deadline
(0 s wait, mirroring SIGQUIT). `reloadConfig` reloads GUCs.

### GC throttling (`dispatch.go`)

`maybeForceGCAfterCommit` is counter-first (no STW in the hot path): the atomic
`queriesWithoutFreeCounter` is evaluated first; `runtime.ReadMemStats` (which
requires a brief STW) only when a round is due. Real `runtime.GC()` +
`debug.FreeOSMemory()` only past `heapReleaseThresholdBytes` (4 GiB HeapInuse)
or every `queriesPerForcedFree` (10,000) queries. Gated by `GOOPG_FORCED_GC`
env (default OFF) — `SetForcedGCEnabled` for tests. The 10,000 counter was
raised from 8 because the original pgbench-8 caused a STW ReadMemStats on every
query and full GC every ~8 queries (43% CPU at c=10 SO).

### autovacuum subpackage

Standalone `Launcher` with its own Pool/TxnMgr/Catalog handles, ticks every
60 s, scans `AllTables()`, calls `vacuum.VacuumWithOptions`/`vacuum.Analyze`.

## Dependencies

- **Uses** `catalog`, `executor`, `parser`, `optimizer`, `libpq` (+`auth`),
  `storage` (+`file`, `lmgr`), `access/transam` (+`multixact`, `xlog`,
  `control`), `backup`, `replication`, `postmaster/autovacuum`, `utils/*`,
  `port/gls`. Hands off `parser.Parse → optimizer.Plan → executor.BuildFastIterator`.
- **Used by** `cmd/goopg/main.go`, `internal/replication/*`,
  `internal/backup/basebackup.go`. The executor wires callbacks back into
  `Server` (`CancelBackend`, `QueueNotify`, `PLpgSQLCommitChain`, …).

## Notable patterns / gotchas

- **Per-connection goroutine model** — the client goroutine is the backend; it
  exclusively owns WAL insert + sync-commit flush; checkpointer/walwriter/
  autovacuum are separate goroutines. Pool flush is checkpointer-only (asserted
  via `Pool.OnFlushAll`).

- **Two protocol paths must agree** — simple (`dispatch.go`) and extended
  (`dispatch_extended.go`) are siblings sharing `applyTransactionVerb`,
  `commandTagFor`, the plan cache, and `paramsToDatums`; each has been a silent
  divergence source (M0134-0009 pairs SET ROLE / SET SESSION AUTHORIZATION
  fast-path closures).

- **Parser gap = wire-level DDL bypass** — CREATE/ALTER/DROP DATABASE and ROLE
  have no parser grammar; they are intercepted by string-prefix matching in both
  protocols. Compat no-op forms absorb GRANT/SCHEMA/etc. The bypass chain must
  be replicated in both protocol paths — a missed arm is a silent divergence.

- **Identity trio on `connTxState`** — `LoginUser` / `SessionUser` /
  `NonSuperuserRole` (+`SetRoleIsActive`), mirrored into `ectx` and restored at
  `End()`. `SET SESSION AUTHORIZATION` changes SessionUser AND clears SET ROLE
  (PG parity); `SET ROLE` changes only the effective role, never session_user.

- **Aborted-block (25P02) gates** ahead of every fast path — only COMMIT/
  ROLLBACK/ROLLBACK TO/2PC pass; extended errors set `syncRequired`. A
  PARSE-time error inside an explicit transaction also aborts the block
  (M0134-0155).

- **Extended Executes auto-commit per statement** (PG parity).

- **Materialize-then-drain in extended** — Executes materialize ALL rows into
  `extendedQueryResult` first, then drain the portal by `maxRows`. This is a
  memory-inefficiency vs PG's streaming, but required for `maxRows`
  suspension and re-execution semantics.

- **Package name vs doc comment** — file doc says "Package server…" but the
  package name is `postmaster` (cosmetic mismatch).

- **Oversized frames** — drained to resync the stream, emits `08P01`.

- **Client-EOF watcher** — per-query goroutine `recvfrom(MSG_PEEK)` every
  500 ms cancels compute-bound queries when the client vanishes (never armed
  for replication; x86_64-Linux-only).

- **Doorkeeper plan-cache admission** — the SECOND-sighting rule means a
  repeated statement is cached from its third execution. Marks survive DDL
  invalidation on purpose.

- **procNum is connection-lifetime** — assigned once via `TxnMgr.AcquireConnSlot`
  (not PID-modulo), used for WaitEventStart, pg_stat_activity
  `RegisterAt`, and `gls.SetBackendID` for WAL stripe pick. The
  PID-modulo fallback only serves Manager-less unit harnesses.

- **`datconnlimit` self-inclusive count** — the check registers the backend
  FIRST so `CountByDatName` includes the new connection, matching
  `CountDBConnections`. Superusers are counted but skip the reject check.

- **GC free is opt-in** — the forced-GC trigger ships DISABLED
  (`GOOPG_FORCED_GC` unset). Benchmark runs that want deterministic memory
  return must set it, but the default avoids pgbench STW regressions.

- **NOTIFY commit-visibility** — notifications queue on `pending` but are only
  delivered once the notifying transaction commits; ROLLBACK discards them.
  Savepoint operations (RELEASE/ROLLBACK TO) re-scope buffered notifies.