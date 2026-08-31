# Module: `cmd/goopg`

The goopg server binary — a single-process amalgam of what PostgreSQL splits
across `initdb`, `postmaster`, and `pg_ctl`. It exposes nine subcommands that
initialize a data directory, run the server in the foreground, drive an operator
control plane over a Unix socket, and run a streaming standby.

## Responsibilities

- **`init`** — bootstrap a data directory, mirroring upstream `initdb`
  flag-for-flag (locale, auth, checksums, `-c` GUC overrides).
- **`start`** — build the GUC registry from `postgresql.conf`, open the storage
  runtime, wire WAL/checkpointer/standby machinery, then hand off to
  `internal/postmaster`.
- **`stop` / `reload` / `checkpoint` / `promote` / `status`** — operator control
  plane: read the pidfile and send a one-line command over the control socket.
- **Standby** — walreceiver + WAL replay goroutines, promotion, `promote.signal`
  watcher.

## Key Files

- `cmd/goopg/main.go` (1,388 LOC) — CLI dispatch + subcommand implementations;
  `runStart` (`main.go:251`) is the server startup path.
- `cmd/goopg/standby.go` (497 LOC) — streaming-standby lifecycle and promotion.
- `cmd/goopg/main_test.go`, `cmd/goopg/standby_test.go` — tests (`runRestart`
  is factored as `runRestartWithStarter` at `main.go:1154` so tests can inject a
  fake starter).

## Public API / CLI surface

Dispatch: `main` → `run(args, stdout, stderr) int` (`main.go:77`), matching the
`subcommands` table (`main.go:65`). Exit 2 on unknown command, 0 on bare
invocation. Each subcommand uses its own `flag.FlagSet`; `-h` works per-subcommand.

- **`init`** (`runInit`, `main.go:137`) — `-D`, `-U/--username`, `-X/--waldir`,
  `-N/--no-sync`, `-k/--data-checksums` (default ON, PG 18 parity), `-A/--auth`
  + `--auth-host/--auth-local/--pwfile`, the full locale family
  (`--locale-provider`, `--locale`, `--lc-*`, `--icu-locale`, `--icu-rules`),
  and repeatable `-c/--set NAME=VALUE` (collected as `initdb.GUCSetting`).
- **`start`** (`runStart`, `main.go:251`) — `-D` (data dir; empty = protocol-only
  mode), `-config` / `-hba` (auto-discover `<datadir>/postgresql.conf` and
  `<datadir>/pg_hba.conf` when omitted), `-listen host:port` (default
  `127.0.0.1:5432`).
- **Control-plane commands** — `stop/reload/checkpoint/promote/status` take `-D`
  and an optional `-t` timeout; they parse the pidfile (`control.ParsePIDFile`)
  and send `STOP`, `STOPIMMEDIATE`, `RELOAD`, `CHECKPOINT`, `PROMOTE`, or `PING`
  over the socket (`control.Send`).
- **`stop`** — `-mode smart|fast|immediate` (default fast). `smart`/`fast` →
  graceful `STOP` (final checkpoint); `immediate` → `STOPIMMEDIATE` (skips the
  shutdown checkpoint).
- **`status`** — exit 0 running, **3** not running / stale pidfile, **4** alive
  but control socket unresponsive, 1 other error (pg_ctl parity).

## Internal structure

`runStart` startup sequence (`main.go:251-896`):

1. Env tuning — `GOOPG_MUTEX_PROFILE_RATE`/`GOOPG_BLOCK_PROFILE_RATE`, the
   `GOOPG_DISABLE_NLI` planner gate; forces `GOGC=200` when unset.
2. pprof HTTP endpoint on `127.0.0.1:6060` (override `GOOPG_PPROF_ADDR`).
3. Signal handling via `signal.NotifyContext(ctx, os.Interrupt, SIGTERM)`.
4. GUC registry — `misc.BuildDefaultRegistry()`; an `OnChange` bridge connects
   SQL `SET enable_*` to planner process-global switches; `misc.ParseConfigFile`
   + `ApplyConfigEntries` when a config exists.
5. Storage open — `initdb.Open(...)` returns `*initdb.Runtime`, reading ~20 GUCs
   into `OpenOptions` (`shared_buffers`, `wal_buffers`, `fsync`, `io_*`,
   `commit_delay`, bgwriter/checkpointer settings).
6. Runtime wiring — deferred `SaveCatalog`/`SaveVM`/`SaveFSM`/`Close`; MultiXact
   store; `OnStopImmediate`; `SystemID`/`Timeline`; SyncRep priming.
7. Auth/user store — `auth.NewMapUserStore()` seeded from
   `catalog.InMemory.AllRoleStates()` + the optional `<datadir>/pg_auth` overlay.
8. Checkpointer goroutine on a child context (cancelled on exit).
9. Recovery/standby branch — archive recovery, or `startStandby`.
10. Handoff — `postmaster.New(cfg)` → `srv.Run(ctx)`; graceful drain bounded by
    `ShutdownDeadline`.

Standby (`standby.go`): `startStandby` launches the `xlog.StreamReplayer` and
`replication.StartWalReceiver`; `promoteSignalWatcher` polls `promote.signal`;
`Promote` cancels the receiver, drains replay, then `finalizePromotion`
(timeline +1, history file, pg_control TLI update, remove `standby.signal`).

## Dependencies

- **Uses** `internal/initdb` (bootstrap + storage runtime), `internal/postmaster`
  (accept loop + control plane), `internal/utils/misc` (GUC registry),
  `internal/access/transam/{control,xlog,multixact}`, `internal/replication`
  (walreceiver), `internal/storage`, `internal/catalog`, `internal/libpq/auth`,
  `internal/optimizer` (planner flag bridges).

## Notable patterns / gotchas

- **`-D` empty on `start` = protocol-only mode** — no storage handles.
- **`-data-checksums` defaults ON** — PG 18 parity (commit 04bec894).
- **Config auto-discovery is explicit** — `-config`/`-hba` fall back to
  `<datadir>/…` before built-in defaults (motivated by a silent-ignore bug).
- **Foreground-only model** — no daemonize/fork; `restart` re-enters `runStart`
  in-process; the CLI process becomes the server.
- **Shutdown ladder** — graceful drain bounded by `ShutdownDeadline`; `stop
  immediate` bypasses it; `runStop` waits for process exit so a follow-up `start`
  doesn't race.
- **Cross-file sibling twins** — `standbyController.finalizePromotion`
  (`standby.go:276`) and `promoteAfterRecovery` (`standby.go:432`) are
  near-identical promotion sequences and must stay in sync.
