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
- [x] Heap and tuple format with xmin/xmax visibility metadata.
      `internal/storage/heap.go` adds tuple header
      (`xmin`/`xmax`/`xvac`/`ctid`/`infomask*`), line-pointer packing,
      and page-level tuple add/get helpers; tests pin metadata
      round-trip and page slot behavior.
- [x] Snapshot manager with `READ COMMITTED` and `REPEATABLE READ` semantics.
      `internal/mvcc` now tracks active xids and provides
      statement-snapshot acquisition (`SnapshotFor`) with per-statement
      refresh for READ COMMITTED and first-snapshot pinning for
      REPEATABLE READ. `TupleVisible` evaluates
      `storage.HeapTupleHeader` against `(xmin,xmax,in-progress[])`
      horizons and current xid.
- [x] WAL writer with `fdatasync` on commit.
      `internal/wal` now provides segmented WAL files under `pg_wal/`,
      append/flush worker serialization, record framing with CRC, and
      `FlushUpTo` durability semantics. `internal/storage.Pool`
      integrates WAL-before-data ordering by flushing WAL up to
      page `pd_lsn` before dirty-page writeback.
- [x] Checkpointer goroutine.
      `internal/wal/checkpointer.go` runs periodic checkpoints by
      flushing dirty pages (`FlushAll`), appending a checkpoint marker,
      and syncing WAL to the checkpoint LSN. Failed flushes skip marker
      emission for that tick and are retried on the next interval.
- [x] Crash recovery (replay WAL up to the last consistent checkpoint).
      `internal/wal/recovery.go` adds page-image replay and checkpoint
      marker handling (`RecordKindCheckpoint`), replaying records up to
      the latest consistent checkpoint boundary.
- [x] B-tree index access method. `internal/access/btree` provides a
      single-column int4 B-tree on top of the buffer pool: 24-byte page
      header + ItemId array + tuple region + 16-byte BTPageOpaque,
      metapage at block 0, recursive leaf+internal splits with new-root
      lift, byte-order-preserving `EncodeInt4`, forward range scan via
      right-sibling pointers, single-mutex concurrency.
- [x] `VACUUM` and `ANALYZE` minimal implementations. `internal/vacuum`
      drives a heap page-prune across every block using
      `mvcc.Manager.OldestXmin()` as the horizon, marking dead tuples
      LP_UNUSED and repacking survivors against pd_special; ANALYZE
      walks the heap and returns row count + average tuple width over
      visible rows. SQL surface (`VACUUM`/`ANALYZE` statements) waits on
      milestone 6's parser; the package functions are the seam.
- [x] Design doc: `0009-btree.md`.
- [x] Design doc: `0016-vacuum-and-analyze.md`.
- [x] Design doc: `0007-mvcc-and-snapshots.md`.
- [x] Design doc: `0008-wal-and-recovery.md`.

## Milestone 6 — SQL surface for pgbench

- [ ] Parser/analyzer covering `CREATE TABLE`, `CREATE INDEX`, `INSERT`,
      `UPDATE`, `DELETE`, `SELECT` with the joins/aggregates pgbench needs,
      `BEGIN`/`COMMIT`/`ROLLBACK`, `VACUUM`, `ANALYZE`, prepared statements.
  - [x] Lexer (`internal/parser/lexer.go`) covering identifiers (quoted
        and unquoted), integer literals, single-quoted strings with `''`
        escape, parameter placeholders `$N`, line and (nested) block
        comments, multi-character operators.
  - [x] Statement parsers: `BEGIN`/`COMMIT`/`ROLLBACK` (and `END`/`ABORT`
        aliases), `VACUUM` (with VERBOSE/ANALYZE/target list), `ANALYZE`,
        `SHOW`/`SET`/`RESET`. Carving the GUC verbs out of
        `internal/server/query.go` is deferred until the executor lands.
  - [x] Expression tree (`ColumnRef`, integer/string/null/bool consts,
        `BinaryOp`, `UnaryOp`, `FuncCall`, `ParamRef`, `StarExpr`) with
        operator-precedence climbing (Pratt). Recognises arithmetic,
        comparison, boolean, and `||` operators with upstream-aligned
        precedences.
  - [x] Statement parsers: `SELECT` target list (with `*`, qualified
        `t.*`, `AS` alias), comma-separated `FROM` with optional alias,
        `WHERE`, `ORDER BY` with ASC/DESC, `LIMIT`/`OFFSET`.
  - [x] Statement parsers: JOIN clauses, GROUP BY, HAVING, set operations
        for the SELECT shapes pgbench reports queries need.
  - [x] Statement parsers: `INSERT INTO t [(col, …)] VALUES (val, …) [, …]
        [RETURNING target_list]`, `UPDATE t SET col = expr [, …]
        [WHERE expr] [RETURNING target_list]`, `DELETE FROM t
        [WHERE expr] [RETURNING target_list]`. Pgbench's INSERT into
        pgbench_history and the abalance UPDATE/SELECT pair parse
        end-to-end.
  - [x] Statement parsers: `CREATE [UNLOGGED] TABLE [IF NOT EXISTS] name
        (column_def [, …]) [WITH (k=v, …)] [TABLESPACE x]`,
        `CREATE [UNIQUE] INDEX [IF NOT EXISTS] [name] ON table
        [USING method] (col [, …])`, `DROP {TABLE|INDEX} [IF EXISTS]
        name [, …] [CASCADE|RESTRICT]`, `TRUNCATE [TABLE] name [, …]
        [CASCADE|RESTRICT]`. Type modifiers (`char(22)`,
        `numeric(10,2)`), inline `NOT NULL`/`PRIMARY KEY`, and
        table-level `PRIMARY KEY (a, b)` round-trip; pgbench -i's four
        CREATE TABLE strings parse as expected.
  - [x] Statement parser: `ALTER TABLE [IF EXISTS] name action [, …]`
        with `ADD [CONSTRAINT name] PRIMARY KEY (cols)` and
        `ADD [COLUMN] coldef` actions. Pgbench's three primary-key
        ALTER strings parse end-to-end — pgbench -i's full DDL surface
        is now covered.
  - [x] Analyzer pass (name resolution, type checking) once the catalog
        exists.
- [x] Planner sufficient for pgbench's workload. `internal/catalog`
      provides the in-memory schema (Table/Column/OID, with a
      Catalog interface the planner takes by value); `internal/planner`
      maps each parser.Stmt shape to a plan-node tree
      (SeqScan/Filter/Project/Sort/Limit/Values/Insert/Update/Delete/
      DDL/Transaction/Utility) and resolves names with SQLSTATE-aligned
      errors (42P01, 42703, 42601, 0A000). pgbench's three load-bearing
      DML queries plus SELECT *, BEGIN, DROP TABLE, and VACUUM ANALYZE
      all plan end-to-end.
- [x] Planner: index-scan rule (`col = const` / `col = $N` against a
      btree-indexed column). Planner now emits `IndexScan` when a
      single-column btree index exists for an equality predicate;
      executor opens the btree and probes by encoded int4 key
      (`const` and `$N` forms, including commuted `const = col`).
- [x] Planner: multi-table FROM, joins, GROUP BY/HAVING, aggregates
      (planner emits Join/Aggregate trees including INNER/LEFT/RIGHT/
      FULL/CROSS joins and grouped aggregates).
- [x] Executor with the operators the planner emits.
  - [x] Volcano Open/Next/Close iterator scaffold (`internal/executor`):
        Datum union with KindNull/Bool/Int/String/Bytes/Time, expression
        evaluator (arithmetic/comparison/`||`/Kleene AND/OR/NOT, ParamRef
        lookup, current_timestamp etc. via in-tree registry), Values /
        Project / Filter / Limit / Sort operators, Build(plan) wiring,
        Run helper. SELECT 1, parameterised expressions, LIMIT/OFFSET,
        ORDER BY, division-by-zero (22012), and current_timestamp work
        end-to-end without storage.
  - [x] Planner-parity operators: Join + Aggregate. Executor now builds
        and runs nested-loop joins (INNER/LEFT/RIGHT/FULL/CROSS with
        NULL-extension semantics) and grouped aggregates
        (COUNT/SUM/AVG/MIN/MAX, DISTINCT support, HAVING via Filter
        over Aggregate).
  - [x] Heap-touching operators: SeqScan + Insert. Row codec
        (`internal/executor/codec.go`) marshals Datum rows ↔ heap-tuple
        bytes for v0's typed columns (int4/int8/bool/timestamp/text);
        Context grows Pool/Catalog/TxnMgr/Tx/Snap fields; SeqScan walks
        the buffer pool with mvcc.TupleVisible filtering; Insert
        extends via PinNew when the last block is full. End-to-end
        round-trip + relation-extension tests pass.
  - [x] Heap-touching operators: Update + Delete. Update follows the
        upstream "delete + insert" pattern — stamp xmax on the old
        tuple via storage.PageSetHeapTupleXmax, then writeHeapRow the
        new image with xmin = current xid. Delete just stamps xmax.
        Both extract their predicate from a child Filter(SeqScan) or
        bare SeqScan; v0 doesn't yet handle JOIN/USING. Pgbench-shaped
        UPDATE-then-SELECT and DELETE-by-id round-trip end-to-end.
  - [x] DDL operator path: CREATE TABLE / DROP TABLE / TRUNCATE wired
        through the catalog + smgr. New smgr methods DropRelation
        (close + unlink the file) and TruncateRelation (file size to
        0); Pool.InvalidateRel evicts unpinned slots before the file
        change so subsequent reads see the new state. SQLSTATE
        alignment: 42P07 duplicate_table on CREATE, 42P01
        undefined_table on DROP without IF EXISTS. End-to-end tests
        run CREATE/DROP/TRUNCATE through the parser→planner→executor
        stack.
  - [x] DDL operator path: CREATE INDEX / DROP INDEX / ALTER TABLE
        wired through the B-tree + catalog. `internal/catalog` now
        tracks index metadata; executor DDL handles CREATE INDEX
        (single-column int4 btree build/backfill), DROP INDEX,
        ALTER TABLE ADD COLUMN, and ALTER TABLE ADD PRIMARY KEY
        (unique btree). TRUNCATE now resets dependent index files as
        well.
  - [x] Transaction operator: BEGIN/COMMIT/ROLLBACK plumbed to
        `mvcc.Manager` and per-session state. `internal/executor`
        now has `transactionOp` + `Session` abstraction
        (`BasicSession`), with BEGIN allocating xid/snapshot,
        COMMIT/ROLLBACK finishing the active xid, nested BEGIN as
        no-op, and explicit-tx lifecycle tests.
- [x] Extended query protocol (Parse/Bind/Describe/Execute/Sync).
- [ ] `COPY FROM STDIN` and `COPY TO STDOUT` (text and binary) sufficient for
      `pgbench -i`.
      (text-mode end-to-end works through the real
      parser→planner→executor stack: parser produces a
      `parser.CopyStmt` (FROM/TO, STDIN/STDOUT/file/PROGRAM,
      parenthesised + legacy WITH options including FORCE_QUOTE *,
      FORCE_NOT_NULL/(cols)); planner resolves it to `planner.Copy`
      (catalog table + column ordinals, query-form plans the inner
      SELECT, plan-time option validator with SQLSTATE
      42P01/42703/42701/42601/0A000); executor has a COPY TEXT codec
      and the `RunCopyTo` / `CopyFromExecutor` bidirectional
      drivers. The wire layer (`internal/server/copy.go`) now
      dispatches via parser+planner+executor when Server.Config has
      Catalog/Pool/TxnMgr handles configured (begins a per-COPY
      ReadCommitted transaction; CopyOut streams `EncodeCopyTextRow`
      output through `WriteCopyData`; CopyIn buffers CopyData
      payloads, splits on `\n`, calls `PushLine` for each row, and
      reports `COPY N` with the inserted-row count). Without
      handles, the v0 string-matching path stays as a fallback so
      protocol-only tests keep working. Binary mode is still
      pending.)
- [x] Design doc: `0010-parser.md`.
- [x] Design doc: `0011-planner.md`.
- [x] Design doc: `0012-executor.md`.
- [x] Design docs: `0013-extended-query-protocol.md`, `0014-copy.md`.

## Milestone 7 — pgbench end-to-end and admin tooling

- [x] `goopg init` creates a data directory layout (`base/`, `global/`,
      `pg_wal/`, `pg_xact/`, etc.). `internal/initdb` writes the
      load-bearing subdirs at mode 0700, a `base/<DefaultDBOid>`
      subdir for the default database, plus PG_VERSION (matching the
      reported `server_version` major) and sample `postgresql.conf`
      / `pg_hba.conf` files (loopback-trust, reject everything else
      — same defaults `goopg start` ships with). Refuses to clobber
      a non-empty target directory; an existing-but-empty target is
      accepted. Wired through `goopg init -D <dir>`. Bootstrap of a
      system catalog and a `pg_control` file are deferred to the
      on-disk catalog work.
- [ ] `goopg start|stop|restart|reload|status` operate the running server.
      (`goopg start -D <dir>` now opens a storage Manager + Pool +
      MVCC + in-memory Catalog from a data directory previously
      laid out by `goopg init` and passes them into Server.Config,
      so the binary serves the same parser→planner→executor path
      the test harness exercises. Catalog persistence is still
      deferred — schema declared via SQL during a session vanishes
      when the process exits, but heap data files do persist via
      the storage manager. stop/restart/reload/status remain
      stubbed.)
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
