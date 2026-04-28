# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

## Milestone 0 — Project skeleton and design process

- [x] Initialize `go.mod` at repo root (`github.com/goopg/goopg`).
- [x] Replace placeholder `AGENT.md` with Go-specific build/test/run commands.
- [x] Update `.gitignore` for a Go project.
- [x] Stub `cmd/goopg/main.go` with subcommand routing for
      `init|start|stop|restart|reload|status` (subcommands print "not yet
      implemented"; the binary builds and exits 0 on `--help`).
- [x] Establish `docs/design/` with a `README.md` index and the first design
      document (`0001-architecture-overview.md`) describing the high-level
      architecture, the upstream-reference policy, and the choice of reported
      `server_version`.

## Milestone 1 — Listener, startup, and minimal wire protocol

- [x] Implement TCP listener bound to a configurable host/port (default 5432)
      that accepts connections and per-connection goroutines.
- [x] Implement protocol v3 startup handshake: read `StartupMessage`, reply
      with `AuthenticationOk`, parameter status messages
      (`server_version`, `server_encoding=UTF8`, `client_encoding=UTF8`,
      `DateStyle`, `TimeZone`, `integer_datetimes=on`,
      `standard_conforming_strings=on`, `application_name`), `BackendKeyData`,
      and `ReadyForQuery('I')`.
- [x] Implement message framing for both directions (length-prefixed frames,
      bounded read buffers, graceful disconnect on malformed input).
- [x] Add a graceful shutdown path driven by `context.Context` so that
      `goopg stop` and `SIGTERM` both translate into the same internal
      shutdown sequence (close listener, wait for connections, drain).
      (SIGTERM/SIGINT done via `signal.NotifyContext` in `goopg start`.
      `goopg stop` over a control socket is deferred to milestone 7.)
- [x] Write a design doc `0002-wire-protocol.md` covering the chosen subset
      and the intended growth path.
- [x] Smoke test: a Python protocol probe connects, completes the handshake,
      and receives `R/S×13/K/Z`; v0 has no SQL execution path so the
      "any command returns an error cleanly" property is exercised by
      the unit test that sends an unknown frame and reads back
      ErrorResponse + ReadyForQuery. (psql itself is not installed in the
      Ralph workspace; install it locally to exercise the libpq stack.)

## Milestone 2 — Simple query protocol and a fixed response

- [x] Implement the simple `Query` message path returning a hand-rolled
      `RowDescription` + `DataRow` + `CommandComplete` + `ReadyForQuery`
      sequence for `SELECT 1`. (See `internal/server/query.go`.)
- [x] Implement `ErrorResponse` for unrecognized statements with realistic
      `SQLSTATE` codes sourced from `postgres/src/backend/utils/errcodes.txt`.
      `internal/sqlstate` is generated from the upstream file by
      `cmd/gen-sqlstate`; existing magic strings replaced with typed codes.
- [x] Add `pgx`/`psql` integration tests that exercise the path.
      `./postgres/local_install/bin/psql` (PostgreSQL 18.3) is now
      available. Manual smoke runs (recorded against this build) cover
      `SELECT 1`, `SHOW server_version`, `SET application_name +
      SHOW`, `SHOW ALL`, an unrecognised statement (`CREATE TABLE x()`
      → clean ErrorResponse, connection survives), `\conninfo`, and
      SCRAM-SHA-256 with both correct and wrong passwords. One
      regression surfaced — `SHOW ALL` was being routed through the
      per-name SHOW arm — fixed in this same loop with a unit test
      pinning the contract.

## Milestone 3 — Authentication

- [x] Implement `trust` auth (the simplest case) end-to-end with a
      `pg_hba.conf`-style file parser. `internal/auth` provides Method
      and ConnType enums covering every upstream method, a tokenizer +
      parser with include / include_if_exists / include_dir support,
      and a first-match matcher with explicit/implicit reject. Server
      replaces the unconditional AuthenticationOk with a policy-driven
      decision; default policy trusts loopback. `goopg start --hba`
      points at a real file. `reject` and implicit-reject emit FATAL
      ErrorResponse with SQLSTATE 28000.
- [x] Implement `password` (cleartext) and `md5` auth.
      `internal/auth.UserStore`/`Credential` carry plaintext and
      pg_authid-style `md5HEX` formats. `auth.Exchange` drives the
      AuthRequest+PasswordMessage round-trip for both methods. Salt is
      4 bytes from `crypto/rand`; comparisons are constant-time.
      Unknown-user and wrong-password paths report identical FATAL
      ErrorResponse (SQLSTATE 28000) so the wire can't distinguish.
      Server `Config.UserStore` is the seam; nil is acceptable for
      trust-only deployments.
- [x] Implement `scram-sha-256` auth (preferred default).
      `internal/auth/scram.go` implements RFC 5802 + 7677. PBKDF2-HMAC-
      SHA-256 pinned to RFC 7914 known-answer; SaltedPassword /
      ClientKey / StoredKey / ServerKey derivation matches
      postgres/src/common/scram-common.c. SASL framing
      (AuthenticationSASL / SASLContinue / SASLFinal) lives in
      internal/protocol; SASLInitialResponse and SASLResponse parsing
      lives in auth.Exchange. PasswordSCRAMSHA256 credential format
      mirrors upstream's `SCRAM-SHA-256$<iter>:<salt>$<sk>:<svk>`
      rolpassword. Doomed exchanges (unknown user, wrong-format
      credential) run to completion against a mock secret for timing
      parity, then fail with ErrInvalidPassword. SASLprep and channel
      binding are deferred — documented as next-loop work in
      0003-authentication.md.
- [x] Design doc `0003-authentication.md`.

## Milestone 4 — Configuration and GUC system

- [x] Implement `postgresql.conf` parser (key=value, comments, includes).
      `internal/config` parses single/double-quoted values (with `''`
      escapes), bareword multi-token sequences (`DateStyle = ISO, MDY`),
      and the include / include_if_exists / include_dir directives with
      cycle detection.
- [x] Implement the GUC registry: name, type, unit, range, default, source,
      scope (server/database/role/session/transaction). Variable carries
      Type / Unit / Context / Source / Scope / VarFlag; Registry seeds
      the variables the server already advertises;
      ApplyConfigEntries bypasses Context gating for file-driven sets.
      Unit conversions cover both bytes (B/KB/MB/GB/TB) and time
      (us/ms/s/min/h/d) families.
- [x] Wire `SHOW`, `SET`, `SET LOCAL`, `RESET`, `RESET ALL` into the
      simple-query path. SessionRegistry layers transaction → session
      → global. FlagReport variables emit ParameterStatus on change.
      `pg_settings` / `current_setting()` / `set_config()` are deferred
      with the catalog work in milestone 5; `SHOW ALL` covers the
      inspection use case until then.
- [x] Design doc `0004-configuration-and-guc.md`.

## Milestone 5 — Storage, MVCC, WAL

- [x] Buffer manager with O_DIRECT-aligned page buffers.
      `internal/storage` ships an mmap-backed page-aligned arena
      carved into BlockSize=8192 slots, a smgr (Manager) that opens
      per-relation files with optional `O_DIRECT|O_DSYNC` and exposes
      `ReadBlock`/`WriteBlock`/`Extend`/`NBlocks`/`Sync` at block
      offsets, and a clock-sweep `Pool` with `Pin`/`PinNew`/`Unpin`/
      `MarkDirty`/`FlushAll`. PageHeaderData layout matches upstream
      byte-for-byte (`InitPage` writes pd_lower=24, pd_upper=8192,
      pd_pagesize_version=0x2004). Tests pin the header layout, smgr
      round-trip, dirty-eviction-flushes-back, and the
      `ErrNoBuffer`/recovery path.
- [x] Design docs: `0005-buffer-manager.md`, `0006-storage-format.md`.
- [ ] Heap and tuple format with xmin/xmax visibility metadata.
- [ ] Snapshot manager with `READ COMMITTED` and `REPEATABLE READ` semantics.
- [x] WAL writer with `fdatasync` on commit.
      `internal/wal` now provides segmented WAL files under `pg_wal/`,
      append/flush worker serialization, record framing with CRC, and
      `FlushUpTo` durability semantics. `internal/storage.Pool`
      integrates WAL-before-data ordering by flushing WAL up to
      page `pd_lsn` before dirty-page writeback.
- [ ] Checkpointer goroutine.
- [x] Crash recovery (replay WAL up to the last consistent checkpoint).
      `internal/wal/recovery.go` adds page-image replay and checkpoint
      marker handling (`RecordKindCheckpoint`), replaying records up to
      the latest consistent checkpoint boundary.
- [ ] B-tree index access method.
- [ ] `VACUUM` and `ANALYZE` minimal implementations.
- [ ] Design docs: `0007-mvcc-and-snapshots.md`, `0009-btree.md`.
- [x] Design doc: `0008-wal-and-recovery.md`.

## Milestone 6 — SQL surface for pgbench

- [ ] Parser/analyzer covering `CREATE TABLE`, `CREATE INDEX`, `INSERT`,
      `UPDATE`, `DELETE`, `SELECT` with the joins/aggregates pgbench needs,
      `BEGIN`/`COMMIT`/`ROLLBACK`, `VACUUM`, `ANALYZE`, prepared statements.
- [ ] Planner sufficient for pgbench's workload.
- [ ] Executor with the operators the planner emits.
- [ ] Extended query protocol (Parse/Bind/Describe/Execute/Sync).
- [ ] `COPY FROM STDIN` and `COPY TO STDOUT` (text and binary) sufficient for
      `pgbench -i`.
- [ ] Design docs: `0010-parser.md`, `0011-planner.md`, `0012-executor.md`,
      `0013-extended-query-protocol.md`, `0014-copy.md`.

## Milestone 7 — pgbench end-to-end and admin tooling

- [ ] `goopg init` creates a data directory layout (`base/`, `global/`,
      `pg_wal/`, `pg_xact/`, etc.).
- [ ] `goopg start|stop|restart|reload|status` operate the running server.
- [ ] `pgbench -i` succeeds against goopg.
- [ ] `pgbench` default and `--select-only` scripts run to completion under
      concurrent clients with MVCC-consistent results.

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## Completed

- [x] Project initialization (Ralph harness wired up).
