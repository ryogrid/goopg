# 0001 — Architecture Overview

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

`goopg` is a from-scratch reimplementation of PostgreSQL in Go. The
authoritative requirements live in `.ralph/specs/GOAL_AND_REQUIREMENTS.md`.
This document captures the architectural decisions that shape every
subsystem that follows; any later design doc that contradicts this one must
either supersede it or argue, in writing, why the deviation is justified.

The relevant constraints from the requirements doc, summarised:

- Wire-compatible with stock `libpq` clients (psql, pgbench, pgx, JDBC,
  psycopg, etc.) over PostgreSQL protocol v3.0.
- Single-process, multi-threaded model on top of Go's runtime — no
  per-connection backend processes, no `postmaster` fork/exec, no SysV/POSIX
  shared memory.
- x86_64 Linux only. `O_DIRECT` for primary data files and WAL.
- MVCC with Snapshot Isolation matching PostgreSQL semantics for
  `READ COMMITTED` (default) and `REPEATABLE READ`. `SERIALIZABLE` is
  desirable but not required for the initial pgbench milestone.
- No PostgreSQL extensions, no procedural languages, no `MemoryContext`
  port; rely on Go's GC.

## Decision

### 1. Concurrency model

One OS process hosts the entire server. Each PostgreSQL "process" maps to
one or more goroutines:

| PostgreSQL                       | goopg                                                  |
| -------------------------------- | ------------------------------------------------------ |
| `postmaster`                     | `main` in `cmd/goopg`, plus a top-level supervisor goroutine. |
| Per-connection backend           | One connection-handler goroutine, optionally fanning out to worker goroutines for parallel-safe operators. |
| `checkpointer`, `bgwriter`, `walwriter`, `autovacuum launcher`, `stats collector`, `logical replication launcher` | Long-lived goroutines started during startup, torn down during shutdown. |
| SysV shm regions, lwlocks        | `sync.Mutex` / `sync.RWMutex` / `atomic` / channels.   |
| `ProcArray`, `SnapshotData`      | A snapshot manager package guarded by a mutex; immutable snapshot values handed to executors. |

Coordination, not just data sharing, also moves to Go primitives. Cross-goroutine
cancellation flows through `context.Context`; long blocking I/O is wrapped so
that a cancelled context unblocks it.

### 2. Memory management

The Go garbage collector replaces PostgreSQL's `MemoryContext` hierarchy.
Patterns we will use in place of memory contexts:

- **Per-query / per-tuple lifetime**: scoped allocations and
  `bytes.Buffer.Reset()`-style reuse, not nested context resets.
- **Hot reuse**: `sync.Pool` for transient objects on hot paths
  (e.g. message frames, tuple slots). Pool eviction is the GC's problem.
- **Large fixed-size structures**: pre-allocated and pooled. The buffer
  pool, in particular, lives in a single contiguous slice of page-sized
  buffers, indexed by buffer ID. This keeps GC pressure low and lets us
  pin alignments compatible with `O_DIRECT`.
- **Cancellation**: `context.Context` is the only signal that an in-flight
  operation should abandon its work. There is no global "abort" flag to
  poll.

We do **not** port `palloc`, `pfree`, `MemoryContextSwitchTo`,
`AllocSetContextCreate`, etc. Code that ports an algorithm from upstream
must translate the lifetime intent into Go terms in the comment header,
not preserve the original calls verbatim.

### 3. Signal handling replacement

`goopg` uses dedicated administrative commands instead of POSIX signals
for control-plane operations. The CLI is rooted at the top-level
`goopg` binary (see `cmd/goopg/main.go`):

| PostgreSQL signal / pg_ctl invocation | goopg subcommand                          |
| ------------------------------------- | ----------------------------------------- |
| `pg_ctl reload`, `SIGHUP`             | `goopg reload`                            |
| `pg_ctl stop -m smart`                | `goopg stop -mode=smart`                  |
| `pg_ctl stop -m fast`                 | `goopg stop -mode=fast` (default)         |
| `pg_ctl stop -m immediate`, `SIGQUIT` | `goopg stop -mode=immediate`              |
| `pg_ctl status`                       | `goopg status`                            |
| `pg_ctl restart`                      | `goopg restart`                           |
| `initdb`                              | `goopg init`                              |
| `SIGUSR1`, `SIGUSR2` (internal)       | Internal channels; no operator surface.   |

The running server still installs handlers for `SIGINT` and `SIGTERM`
because the OS or terminal will send them anyway, but they are translated
internally into the same shutdown path as `goopg stop -mode=fast`. Other
signals are explicitly *not* used for control. `SIGPIPE` is ignored at
process scope; broken-pipe write errors are handled per-connection.

`goopg stop`, `reload`, etc. communicate with the running server via a
local control channel — a Unix domain socket inside the data directory.
Detailed wire format and authentication for that socket are deferred to a
later design doc; for the v0 scaffold the subcommand stubs only document
the flag surface.

**`goopg restart` (2026-07-08):** since v0's server always runs in the
foreground (no postmaster fork/daemonize — see the constraint list above),
`restart` cannot spawn a background replacement the way `pg_ctl restart`
does. `runRestart` (`cmd/goopg/main.go`) instead stops whatever instance
owns `-D`'s `postmaster.pid` (skipping the stop step entirely if the pidfile
is absent or stale — no live process at that PID), waits for it to exit,
then hands off to the same startup path `goopg start` uses, so the CLI
process itself becomes the new server. `-config`/`-hba` keep `goopg
start`'s existing `<datadir>/postgresql.conf` / `<datadir>/pg_hba.conf`
auto-discovery; `-listen` additionally defaults to the stopped instance's
own listen address (read from its `postmaster.pid`) when not given
explicitly, since — unlike PostgreSQL, where the listen address lives in
`postgresql.conf` — goopg's is a start-time CLI flag with nothing else to
recover it from. Tested via `TestRunRestartWithStarter`
(`cmd/goopg/main_test.go`), which injects a fake starter so the
stop-then-start orchestration can be verified without blocking on a real
foreground listener, plus a live e2e run of the real binary (stop the
running instance, confirm the PID changes and the listen address carries
over unchanged).

### 4. Upstream-reference policy

The clone at `./postgres/` is read-only reference material. Concretely:

- It is not in the Go module's `go.mod` and never will be.
- It is never modified, vendored, or copied into the goopg source tree.
- When a goopg file ports or mirrors upstream behavior, the Go file or
  design doc cites the upstream path (e.g.
  `postgres/src/backend/storage/buffer/bufmgr.c:512`) so reviewers can
  cross-check.
- GNU GLOBAL tags are pre-generated under `./postgres/`; use `global -x`
  and `global -rx` to navigate.

When upstream and our requirements conflict, the requirements doc wins for
**architectural** decisions (process model, memory model, signals) and
upstream wins for **observable** decisions (wire format, on-disk format,
error codes, GUC names and defaults, system catalog shape).

### 5. Reported `server_version`

The upstream tree at `./postgres/` is PostgreSQL **18.3** (see
`postgres/configure.ac:20`). Tying our reported version to the tree we
read from minimises the gap between "what the source says" and "what we
emit on the wire". `goopg` therefore reports:

- `server_version = "18.3"` in the parameter status messages sent during
  startup.
- `server_version_num = 180003` in `pg_settings`.

These values are subject to revision if a client we care about (psql,
pgbench, pgx, JDBC) refuses to negotiate against `18.3` — in that case we
will pin to the highest version the client accepts and document the
override here. Until then `18.3` is the canonical answer; protocol-level
code may treat it as a constant.

The binary's own version string (`goopg version`) is independent and
follows semver-ish progression starting at `0.0.0-dev`.

### 6. Repository layout

```
cmd/goopg/        # CLI entry point; subcommand routing.
internal/server/  # Listener, connection lifecycle, shutdown supervisor.
internal/protocol/
internal/config/
internal/storage/
internal/wal/
internal/mvcc/
internal/catalog/
internal/parser/
internal/planner/
internal/executor/
internal/access/
internal/auth/
docs/design/      # This directory.
postgres/         # Read-only upstream reference.
```

Subdirectories under `internal/` are added on demand. Empty placeholders
are not created up front — every directory should ship with at least one
file that pulls its weight.

### 7. What this document does not decide

The following are explicitly out of scope here and will be addressed in
their own design docs:

- The exact wire-protocol parser implementation strategy (hand-rolled
  scanner vs. generated). → `root-0002-wire-protocol.md`.
- Authentication mechanism choices and `pg_hba.conf` parsing.
  → `root-0003-authentication.md`.
- GUC registry data structures and SET/RESET semantics.
  → `root-0004-configuration-and-guc.md`.
- The buffer manager replacement strategy and victim selection.
  → `root-0005-buffer-manager.md`.
- On-disk page format and tuple layout.
  → `root-0006-storage-format.md`.
- Snapshot manager, transaction IDs, freeze logic.
  → `root-0007-mvcc-and-snapshots.md`.
- WAL records, segment lifecycle, recovery.
  → `root-0008-wal-and-recovery.md`.

## Alternatives Considered

- **Process-per-connection (mirror upstream).** Rejected: the requirements
  doc forbids it, and Go's runtime gives us cheap goroutines that make
  per-connection processes redundant. Operators lose `pg_top` style
  per-backend visibility, but we can replace that with structured logging
  and `pg_stat_*` views.
- **CGo-bridged libpq protocol parser.** Rejected: introduces a build
  dependency, drags in upstream's allocation patterns, and undermines the
  "no `MemoryContext` port" decision. We will hand-roll protocol code in
  Go.
- **Pluggable storage engine from day one.** Rejected as premature. The
  requirements target pgbench against a single MVCC heap; designing a
  table-AM abstraction before we have one working AM is speculative
  generality.

## Consequences

- The codebase will look meaningfully different from upstream in
  *structure*, while staying close in *observable behavior*. Reviewers
  comparing files line-for-line will be confused; reviewers comparing
  outputs will not.
- Concurrency bugs surface as Go data races, not as shared-memory
  corruption. `go test -race` becomes a first-class quality gate;
  `internal/server/` and `internal/mvcc/` should be exercised with the
  race detector in CI from the moment they exist.
- Clients gating on `server_version` need an entry in the test matrix to
  catch regressions when we eventually re-tune the reported version.
