# goopg — Requirements

**Project codename:** goopg
**Driver:** ralph-claude-code (autonomous, looped Claude Code agent)
**Document status:** Initial requirements (v0.1)
**Target audience:** The autonomous coding agent and any human reviewers auditing its work

---

## 1. Project Overview

`goopg` is a from-scratch reimplementation of PostgreSQL written in Go. It aims to be wire-compatible with stock PostgreSQL clients (including `psql` and any client built on `libpq`), while internally adopting an architecture that fits the Go runtime rather than mirroring PostgreSQL's C/process-based design.

The project is being built **iteratively by an autonomous agent (ralph-claude-code)**. The agent must therefore treat this document, the design documents it produces along the way, and the cloned upstream PostgreSQL source tree as its primary sources of truth.

### 1.1 Goals

- Provide a Go-native PostgreSQL server that real-world PostgreSQL clients can connect to without modification.
- Implement enough of the SQL engine, storage layer, and concurrency control to **run `pgbench` end-to-end** under realistic workloads.
- Implement **MVCC with Snapshot Isolation** semantics consistent with PostgreSQL's behavior at the `READ COMMITTED` and `REPEATABLE READ` isolation levels.
- Use **Direct I/O** for all primary data file access to bypass the OS page cache and keep buffer-management behavior predictable.
- Keep behavior, configuration surface, and observable semantics **as close to PostgreSQL as practical**, so that operators and tools have a familiar experience.

### 1.2 Non-Goals

- Cross-platform support. The only supported platform is **x86_64 Linux**.
- PostgreSQL **extensions** (`CREATE EXTENSION`, dynamically loaded `.so` modules, contrib modules, FDWs, etc.) are explicitly out of scope.
- **Stored procedures** and the `PL/*` procedural languages (PL/pgSQL, PL/Python, PL/Perl, etc.) are out of scope.
- A C-compatible memory-context allocator. Go's garbage collector replaces this concern entirely; do not port `palloc`/`pfree`/`MemoryContext`.
- Bug-for-bug parity with PostgreSQL. Match observable semantics where it matters for correctness and client compatibility; do not mimic implementation quirks for their own sake.

---

## 2. Reference Oracle: Upstream PostgreSQL

A clone of the upstream PostgreSQL repository is available at `./postgres/` inside the project directory. This clone is the **reference oracle** for goopg.

The agent must use it as follows:

- Whenever a question of semantics, wire format, on-disk format, GUC default, error code, system catalog shape, or SQL behavior arises, **consult the corresponding C source, header, or documentation under `./postgres/`** before guessing or inventing.
- When porting a concept, cite the upstream file path (e.g. `postgres/src/backend/storage/buffer/bufmgr.c`) in the design document or code comment that introduces the Go equivalent.
- The `./postgres/` tree is **read-only reference material**. Do not modify it. Do not vendor it into the Go module. Do not import its build artifacts.
- When upstream and this requirements document disagree, this document wins for *architecture* (e.g. threads vs. processes), and upstream wins for *protocol and on-disk semantics that clients depend on*.

---

## 3. Architectural Requirements

### 3.1 Concurrency Model

PostgreSQL uses a multi-process model with one backend process per connection, plus auxiliary processes (checkpointer, bgwriter, autovacuum launcher/workers, WAL writer, etc.) and a `postmaster` supervisor.

`goopg` must instead use a **single-process, multi-threaded** model built on Go's runtime:

- One OS process hosts the entire server.
- Each client connection is handled by one or more goroutines.
- Auxiliary subsystems (checkpointer, background writer, WAL writer, autovacuum, stats collector, etc.) are implemented as long-lived goroutines, not separate processes.
- Inter-"backend" coordination uses Go primitives (channels, `sync.Mutex`, `sync.RWMutex`, `atomic`, `context.Context`) instead of POSIX shared memory, semaphores, and SysV/POSIX shm regions.
- There is no `postmaster` fork/exec model. Startup is a single Go `main` that brings subsystems up in dependency order and tears them down on shutdown.

### 3.2 Memory Management

- Rely on Go's garbage collector. Do not implement PostgreSQL's `MemoryContext` hierarchy.
- Where PostgreSQL's design uses memory-context lifetime to bound allocations (e.g. per-query, per-tuple), use idiomatic Go equivalents: scoped allocations, `sync.Pool` for hot reuse, explicit reset of reusable buffers, and `context.Context` for cancellation.
- Buffer pool pages and other large fixed-size structures should still be pre-allocated and pooled to keep GC pressure low; this is a performance choice, not a memory-context port.

### 3.3 Signal Handling Replacement

PostgreSQL uses POSIX signals (`SIGHUP` for config reload, `SIGINT`/`SIGTERM`/`SIGQUIT` for shutdown modes, `SIGUSR1`/`SIGUSR2` for various internal events, etc.) extensively for control-plane operations.

`goopg` must **not** rely on signal-based control beyond the bare minimum required to handle process termination cleanly. Instead:

- Provide **dedicated administrative commands** (CLI subcommands and/or SQL-level functions) for every operator-facing action that PostgreSQL drives via signals. Examples:
  - Configuration reload (replacing `pg_ctl reload` / `SIGHUP`).
  - Graceful, fast, and immediate shutdown modes (replacing `pg_ctl stop -m smart|fast|immediate`).
  - Triggering checkpoints, log rotation, stats reset, etc.
- These commands should be discoverable via a single top-level CLI (e.g. `goopg ctl <subcommand>`) and documented in `--help` output.
- Signals that the OS forces on the process (`SIGTERM`, `SIGINT` from a terminal, `SIGPIPE` on broken connections) must still be handled, but only to translate them into the equivalent dedicated-command path internally.

### 3.4 Platform Constraints

- Build target: `GOOS=linux GOARCH=amd64`.
- Direct I/O is required for primary data files and WAL (see §5.2). Code paths may assume Linux `O_DIRECT` semantics and a filesystem that supports it (ext4, xfs).
- No attempt should be made to keep the code portable to macOS, Windows, or other Unixes. Build tags and conditional code for non-Linux platforms are unnecessary and should be avoided to keep the codebase small.

---

## 4. Protocol and Client Compatibility

### 4.1 Wire Protocol

- Implement PostgreSQL's frontend/backend wire protocol such that **unmodified `libpq`-based clients** (psql, pg_dump within the supported feature subset, application drivers like `pgx`, JDBC, psycopg, etc.) can connect, authenticate, run queries, and receive results.
- Support at least protocol version 3.0. Earlier protocol versions are not required.
- Implement the simple query protocol and the extended query protocol (Parse / Bind / Describe / Execute / Sync), including portals and prepared statements.
- Support `COPY FROM STDIN` and `COPY TO STDOUT` (both text and binary formats) at least to the level required for `pg_dump`/`pg_restore`-style flows used by `pgbench` initialization (`pgbench -i`).

### 4.2 libpq Compatibility

- Compatibility is at the **wire** level: any client linked against `libpq` should work.
- Implement the authentication methods required to support `pgbench` and `psql` out of the box: at minimum `trust`, `password`, `md5`, and `scram-sha-256`. Document which methods are supported in the design docs as they are added.
- Startup parameters, error/notice message fields, parameter status messages (`server_version`, `server_encoding`, `client_encoding`, `DateStyle`, `TimeZone`, `integer_datetimes`, `standard_conforming_strings`, `application_name`, etc.) must be populated with values that clients expect.
- The reported `server_version` string must be parseable by clients that gate behavior on PostgreSQL version. Choose and document a compatibility version (e.g. report a real PostgreSQL major version that goopg's behavior matches) in the design docs.

### 4.3 SQL Surface

- Support the SQL needed to execute the standard `pgbench` workload (TPC-B–like) including: `pgbench -i` (table creation, data load, index creation, vacuum) and the default and `--select-only` test scripts.
- Required SQL features include but are not limited to: `CREATE TABLE`, `CREATE INDEX` (at least B-tree), `INSERT`, `UPDATE`, `DELETE`, `SELECT` with joins and aggregates sufficient for pgbench, `BEGIN`/`COMMIT`/`ROLLBACK`, `VACUUM`, `ANALYZE`, prepared statements.
- Beyond the pgbench-required surface, prioritize features by what real `libpq` clients send during connection setup and introspection (catalog queries from `psql \d`, driver probes, etc.). The agent should incrementally widen support, recording each expansion in design docs.

---

## 5. Storage and Durability

### 5.1 MVCC and Isolation

- Implement multi-version concurrency control with semantics aligned to PostgreSQL's: tuple headers carry transaction visibility info (xmin/xmax-equivalent), and visibility is determined against a transaction snapshot.
- Implement at least **Snapshot Isolation** sufficient to provide the `READ COMMITTED` (default) and `REPEATABLE READ` isolation levels with PostgreSQL-compatible observable behavior. `SERIALIZABLE` (SSI) is desirable but not required for the initial pgbench milestone.
- The transaction ID space, snapshot mechanics, and vacuum/freeze logic should follow upstream's model closely enough that operators familiar with PostgreSQL can reason about goopg. Deviations must be documented in design docs.

### 5.2 File I/O

- All access to heap files, index files, and WAL must use **Direct I/O** (`O_DIRECT` on Linux), with appropriate alignment of buffers and sizes to the underlying device's logical block size.
- Implement an internal buffer pool / page cache; goopg must not depend on the OS page cache for correctness or performance of primary data access.
- WAL writes must be durable on commit (acknowledged to client only after `fdatasync` or equivalent on the WAL segment), consistent with `synchronous_commit = on` semantics by default.
- The on-disk page format should be compatible with PostgreSQL's where doing so does not impose excessive cost. Where it diverges, the divergence must be documented and justified in a design doc, and any tooling that reads on-disk files must be goopg's own.

### 5.3 Crash Recovery

- Implement WAL-based crash recovery sufficient to bring the database back to a consistent state after an unclean shutdown.
- Implement checkpoints driven by a dedicated goroutine on a configurable interval, analogous to PostgreSQL's checkpointer.

---

## 6. Configuration

### 6.1 Configuration File

- Provide a `postgresql.conf`-style configuration file with the same name and the same `key = value` line-based syntax, including comment handling (`#`) and `include`/`include_dir` directives where reasonable.
- Provide a `pg_hba.conf`-style host-based authentication file with the same syntax and semantics for the supported auth methods.
- Data directory layout should mirror PostgreSQL's at the directory level (`base/`, `global/`, `pg_wal/`, `pg_xact/`, etc.) closely enough that an operator familiar with PostgreSQL can navigate it. Internal file formats may differ; document the mapping in a design doc.

### 6.2 GUCs

- Implement Grand Unified Configuration parameters with the **same names, units, and value ranges** as PostgreSQL wherever the corresponding feature exists in goopg.
- For each implemented GUC, the default value should match PostgreSQL's unless there is a documented reason to differ (e.g. `shared_buffers` semantics differ because there is no SysV shm; `max_connections` defaults can stay aligned).
- GUCs corresponding to features that goopg does not implement (extensions, replication-specific knobs not yet built, etc.) should either be silently accepted as no-ops with a startup warning, or rejected with a clear error — pick one policy per category and document it.
- Support the same scoping levels: server-wide (config file), per-database, per-role, per-session (`SET`), per-transaction (`SET LOCAL`).
- Expose `SHOW`, `pg_settings`, and `current_setting()` / `set_config()` so client tools that introspect GUCs work.

---

## 7. Tooling and Operability

- Provide a `goopg` binary with subcommands roughly mirroring `pg_ctl`, `initdb`, and a server entry point. Suggested layout (final naming to be decided in a design doc):
  - `goopg init` — initialize a data directory (replaces `initdb`).
  - `goopg start` / `goopg stop` / `goopg restart` / `goopg reload` — lifecycle and the signal-replacement commands from §3.3.
  - `goopg status` — health/state inspection.
  - Server itself runs in the foreground by default and is supervised externally (systemd, container runtime); daemonization is not a goal.
- Logging: structured logs to stderr by default, with a format/verbosity controlled by GUCs that mirror PostgreSQL's (`log_min_messages`, `log_line_prefix`, etc.) where applicable.
- Provide the system catalog views and functions that `psql` meta-commands (`\d`, `\dt`, `\df`, `\du`) hit, at least to the depth required for those commands to return sensible results on a goopg database.

---

## 8. Implementation Constraints

### 8.1 Language and Dependencies

- Implementation language: Go. Use a Go version supported upstream at the time the agent picks one; document the choice and update it deliberately.
- **Third-party Go libraries are permitted.** Prefer well-maintained, widely-used libraries over rolling everything from scratch. Each non-trivial dependency must be justified briefly in the design doc that introduces it (what it provides, why hand-rolling is worse, license compatibility).
- CGo should be avoided unless a specific feature genuinely requires it (e.g. a syscall not exposed by `golang.org/x/sys/unix`). If introduced, justify in a design doc.

### 8.2 Repository Hygiene

- All goopg source code lives outside the `./postgres/` reference tree.
- Standard Go project conventions: `go.mod` at the repo root, `cmd/` for binaries, `internal/` for packages not intended for external import.
- The build must be reproducible with `go build ./...` from a clean checkout on x86_64 Linux with no system dependencies beyond a Go toolchain and standard libc.

---

## 9. Design Documentation Process

The agent is expected to **produce and maintain design documents incrementally** as it builds the system. This is a hard requirement, not a nice-to-have.

### 9.1 What to Write

- Whenever a non-trivial subsystem is about to be designed or significantly changed, write a design document **before or alongside** the code.
- Each document should cover: the problem, the relevant upstream PostgreSQL behavior (with file references into `./postgres/`), the chosen approach in goopg, alternatives considered, and any deviations from upstream with their rationale.
- Subsystems that warrant their own design doc include at minimum: wire protocol handler, query parser/analyzer, planner, executor, buffer manager, storage manager, WAL & recovery, transaction manager & MVCC, lock manager, catalog, GUC system, authentication, and the CLI / signal-replacement commands.

### 9.2 Where to Put Them

- Store design docs under `docs/design/` in the repository.
- Use a clear naming scheme, e.g. `docs/design/NNNN-short-slug.md`, with `NNNN` a zero-padded sequence number assigned at creation time.
- Maintain an index (`docs/design/README.md`) listing every design doc with its status (`draft`, `accepted`, `superseded`, `historical`) and a one-line summary.

### 9.3 Treat Them as Permanent

- Design docs are **not** scratch notes. They are part of the deliverable. Even superseded ones stay in the repo with their status updated, so the evolution of the system is auditable.
- When the agent makes a decision that contradicts an earlier design doc, it must update that doc's status and link forward to the doc that supersedes it.

---

## 10. Definition of Done (Initial Milestone)

The initial milestone is considered complete when **all** of the following hold on x86_64 Linux:

1. A clean checkout builds with `go build ./...` and produces a working `goopg` binary.
2. `goopg init` creates a data directory; `goopg start` brings up a server that listens on the standard PostgreSQL port (default 5432).
3. `psql` connects successfully using a supported authentication method and can execute basic queries interactively.
4. `pgbench -i` initializes a database against goopg without errors.
5. `pgbench` runs the default TPC-B–like workload to completion, with results consistent with MVCC + Snapshot Isolation semantics under concurrent clients.
6. All primary data and WAL files are accessed via `O_DIRECT`.
7. Configuration reload, graceful shutdown, fast shutdown, and immediate shutdown are reachable through dedicated `goopg` CLI subcommands, not via signal-sending tooling.
8. `docs/design/` contains an indexed set of design documents covering every major subsystem implemented to reach this milestone.

Subsequent milestones (broader SQL coverage, replication, SSI, performance work, etc.) are out of scope for this requirements document and will be defined in follow-up requirements as they become relevant.

See docs/milestones/README.md for follow-up milestones.