# Module: `internal/postmaster`

The goopg server process: the TCP listener, the per-connection backend
lifecycle, the PostgreSQL v3 wire-protocol frame loop, SQL dispatch (simple +
extended + COPY), and the operator control plane. One goroutine per client
connection (`serveConn`) is the analogue of one PG backend process, owning the
active MVCC transaction/snapshot, pinned buffers, WAL inserts, and the
synchronous-commit flush. The `autovacuum/` subpackage is the ticker-driven
background vacuum/analyze launcher.

## Key Files

- `server.go` (1,881) — `Server` listener; accept loop, per-conn goroutine,
  startup/auth handshake, frame loop, control plane.
- `dispatch.go` (4,453) — simple-query dispatcher: parse→plan→execute loop,
  plan cache, auto-commit, CommandComplete/RFQ, GC-throttle.
- `query.go` (698) — legacy string-match fast paths (`SELECT 1`, `SHOW`,
  `SET/RESET`), `writeQueryError`.
- `copy.go` (942) — COPY routing: CopyTo stream / CopyFrom frame drain.
- `extended.go` (934) — extended-protocol frames: Parse/Bind/Describe/Execute/
  Close, portal drain with `maxRows` suspension.
- `dispatch_extended.go` (733) — extended-query executor path.
- `database_ddl.go` (2,018) / `role_ddl.go` (1,374) — string-prefix DDL bypasses
  for CREATE/ALTER/DROP DATABASE and ROLE.
- `conn_tx.go` (857) — `connTxState`: explicit-txn holder, session identity,
  cursors, prepared statements, notify buffer.
- `txn_verb.go` (525) — shared BEGIN/COMMIT/ROLLBACK/SAVEPOINT state machine.
- `notify.go` / `cancel.go` / `eof_watch.go` / `twophase.go` / `plancache.go` —
  LISTEN/NOTIFY hub; cancel registry; client-EOF watcher; 2PC store; plan cache.
- `autovacuum/launcher.go` — background autovacuum/autoanalyze launcher.

## Public API

```go
func New(cfg Config) *Server
type Config struct{ Address, ServerVersion string; Logger; Policy auth.Policy;
    UserStore auth.UserStore; Registry *misc.Registry; Catalog catalog.Catalog;
    Pool *storage.Pool; TxnMgr *transam.Manager; LockMgr *lmgr.LockManager; /* … */ }
type Server struct{ /* … */ }
func (s *Server) Run(ctx) error
func (s *Server) Addr() *net.TCPAddr / Ready() <-chan struct{}
type Notification struct{ PID uint32; Channel, Payload string }
func NewLauncher(pool, txnMgr, cat) *Launcher   // autovacuum subpackage
```

## Internal structure

- **Connection lifecycle** — `Run` binds the listener, sweeps stray temp files,
  starts the control plane / apply-launcher / autovacuum, then `acceptLoop`
  spawns one goroutine per conn via `serveConn`. `serveConn` runs the handshake,
  `checkAuth`, role/database/datconnlimit admission, registers an activity slot,
  and enters `runPostStartupLoop`.
- **Simple protocol** — `MsgQuery` → `handleQueryOrCopy` → `handleQuery` (string
  fast paths) or `dispatchSimpleQueryViaExecutor`: pre-parse DATABASE/ROLE
  bypasses → `parser.Parse` → per-statement loop → plan-cache lookup →
  `optimizer.Plan` → `executor.BuildFastIterator` + `Open`/`Next`/`Close` →
  CommandComplete + ReadyForQuery.
- **Extended protocol** — `MsgParse`/`MsgBind`/`MsgExecute`/`MsgDescribe`/
  `MsgClose`/`MsgSync`; Executes **materialize all rows** into a result, then
  drain the portal with `maxRows` suspension. Out-of-block Executes auto-commit
  per statement.
- **COPY** — CopyTo streams via `runCopyToStream`; CopyFrom arms `copyInState`
  and routes `CopyData`/`CopyDone`/`CopyFail` frames.
- **autovacuum subpackage** — standalone `Launcher` with its own Pool/TxnMgr/
  Catalog handles; ticks every 60 s, scans `AllTables()`, calls
  `vacuum.VacuumWithOptions`/`vacuum.Analyze`.

## Dependencies

- **Uses** `catalog`, `executor`, `parser`, `optimizer`, `libpq` (+`auth`),
  `storage` (+`file`, `lmgr`), `access/transam` (+`multixact`, `xlog`, `control`),
  `backup`, `replication`, `postmaster/autovacuum`, `utils/*`, `port/gls`.
  Hands off `parser.Parse → optimizer.Plan → executor.BuildFastIterator`.
- **Used by** `cmd/goopg/main.go`, `internal/replication/*`,
  `internal/backup/basebackup.go`. The executor wires callbacks back into
  `Server` (`CancelBackend`, `QueueNotify`, `PLpgSQLCommitChain`, …).

## Notable patterns / gotchas

- **Per-connection goroutine model** — the client goroutine is the backend; it
  exclusively owns WAL insert + sync-commit flush; checkpointer/walwriter/
  autovacuum are separate goroutines.
- **Two protocol paths must agree** — simple (`dispatch.go`) and extended
  (`dispatch_extended.go`) are siblings sharing `applyTransactionVerb`,
  `commandTagFor`, the plan cache, and `paramsToDatums`; each has been a silent
  divergence source.
- **Parser gap = wire-level DDL bypass** — CREATE/ALTER/DROP DATABASE and ROLE
  have no parser grammar; they are intercepted by string-prefix matching in both
  protocols. Compat no-op forms absorb GRANT/SCHEMA/etc.
- **Identity trio on `connTxState`** — `LoginUser` / `SessionUser` / `NonSuperuserRole`
  (+`SetRoleIsActive`), mirrored into `ectx` and restored at `End()`.
- **Aborted-block (25P02) gates** ahead of every fast path — only COMMIT/
  ROLLBACK/ROLLBACK TO/2PC pass; extended errors set `syncRequired`.
- **Extended Executes auto-commit per statement** (PG parity).
- **Package name vs doc comment** — file doc says "Package server…" but the
  package name is `postmaster` (cosmetic mismatch).
- **Oversized frames** — drained to resync the stream, emits `08P01`.
- **Client-EOF watcher** — per-query goroutine `recvfrom(MSG_PEEK)` every 500 ms
  cancels compute-bound queries when the client vanishes (never armed for
  replication; x86_64-Linux-only).
- **GC throttling** — `maybeForceGCAfterCommit` is counter-first (no STW in the
  hot path); real `runtime.GC()` only past a 4 GiB HeapInuse or 10 000-query threshold.
