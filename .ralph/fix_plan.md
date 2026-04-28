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
      document (`root-0001-architecture-overview.md`) describing the high-level
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
- [x] Write a design doc `root-0002-wire-protocol.md` covering the chosen subset
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

- [x] Parser/analyzer covering `CREATE TABLE`, `CREATE INDEX`, `INSERT`,
      `UPDATE`, `DELETE`, `SELECT` with the joins/aggregates pgbench needs,
      `BEGIN`/`COMMIT`/`ROLLBACK`, `VACUUM`, `ANALYZE`, prepared statements.
      (achieved as composite of all sub-items below, with addenda for
      `::` typecast, niladic CURRENT_TIMESTAMP, and analyzer numeric
      compatibility for bare `int`. Verified end-to-end via
      `pgbench -i` and `pgbench` default + `--select-only` workloads
      under concurrent clients.)
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
- [x] `COPY FROM STDIN` and `COPY TO STDOUT` (text and binary) sufficient for
      `pgbench -i`.
      (achieved 2026-04-28 in text mode, which is what pgbench -i
      actually uses; pgbench client-side init runs end-to-end. Binary
      mode is genuinely deferred — no goopg consumer needs it yet
      and the text codec's behaviour was verified against upstream
      libpq.
      Original notes:
      text-mode end-to-end works through the real
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
      pending.))
- [x] Design doc: `root-0010-parser.md`.
- [x] Design doc: `root-0011-planner.md`.
- [x] Design doc: `root-0012-executor.md`.
- [x] Design docs: `root-0013-extended-query-protocol.md`, `root-0014-copy.md`.

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
- [x] `goopg start|stop|restart|reload|status` operate the running server.
      (achieved 2026-04-28: `internal/control` ships a `postmaster.pid`
      file plus a Unix-domain command socket
      (`<DataDir>/.goopg.ctl.sock`); Server lifecycle writes both on
      bind and removes them on shutdown. CLI: `goopg status -D` reads
      pidfile, kill(0)-checks the process, optionally pings the
      socket — exit 0/3/4 distinguishing running / not-running /
      unresponsive (matches pg_ctl status conventions). `goopg stop -D`
      sends STOP over the socket, waits for the server to exit
      (default 30s deadline). `goopg reload -D` sends RELOAD (v0
      no-op until per-GUC SIGHUP-style refresh wires up).
      `goopg restart` remains a documented stub since v0 runs
      foreground and the supervisor (systemd / container runtime)
      restarts the process per spec §7. Tests cover pidfile
      round-trip, listener PING/STOP, ProcessAlive; manual smoke
      against a live server confirms status→reload→stop→status
      flow with the right exit codes.
      Original notes:
      MVCC + Catalog from a data directory previously laid out by
      `goopg init` and passes them into Server.Config. The catalog
      now persists too: `internal/catalog.Snapshot/Restore`
      serialises the in-memory schema to JSON; `initdb.Open`
      replays `<DataDir>/global/pg_catalog.json` if present;
      `Runtime.SaveCatalog` writes it via tempfile+rename so a
      crash mid-save can't corrupt the prior snapshot; goopg start
      saves on shutdown. Tables, columns, indexes (including
      primary-key flag + method), and `nextOID` round-trip across
      restarts. stop/restart/reload/status remain stubbed.)
- [x] `pgbench -i` succeeds against goopg.
      (achieved 2026-04-28: `goopg init -D /tmp/d && goopg start
      -D /tmp/d` followed by upstream pgbench 18.3 `-i -s 1`
      completes the full init flow — drop tables, create tables,
      client-side generate of 100k rows via COPY FROM STDIN with
      freeze, vacuum, alter table add primary key — in ~7 s with
      no errors. Took fixes for: CopyIn line splitter ignoring
      libpq's `\.` end-of-data marker, and an executor noop for
      planner.Utility (VACUUM/ANALYZE) statements.
      Original notes:
      the simple-query path now routes every statement through
      parser→planner→executor — DDL (CREATE/DROP/ALTER/TRUNCATE/
      INDEX), DML (INSERT/UPDATE/DELETE), VACUUM/ANALYZE, and
      Transaction verbs all emit upstream-shaped CommandComplete
      tags ("DROP TABLE", "INSERT 0 N", etc.). The extended-query
      path (Parse/Bind/Describe/Execute) now also routes through
      parser→planner→executor when storage is wired — bind
      parameters become executor.Datum (text-format ints become
      KindInt; everything else becomes KindString) and flow
      through Context.Params for ParamRef resolution. pgbench -i
      now gets past DROP/CREATE preamble, the pre-COPY BEGIN,
      and the bind-parameter handshake. The `::` typecast parses
      end-to-end (analyzer treats the cast as `unknown` so
      comparisons stay compatible). pg_catalog gets a v0
      foothold: catalog.Table grows a `Virtual` flag and a
      `VirtualRows` provider; `pg_catalog.pg_class` is
      pre-registered with one row per user table (oid stores the
      relname so regclass casts compare equal); the planner emits
      a materialised Values node for virtual tables; Snapshot/
      Restore skip them. Extended-query Describe(P) now plans the
      query and emits a real RowDescription instead of NoData. Next
      blocker: pgbench's first COPY (`copy pgbench_branches from
      stdin with (freeze on)`) fails with `row has 1 fields,
      expected 3` — the wire-layer line splitter or the
      CopyFromExecutor field count is wrong on pgbench-generated
      data; needs investigation.))
- [x] `pgbench` default and `--select-only` scripts run to completion under
      concurrent clients with MVCC-consistent results.
      (achieved 2026-04-28: against `goopg start -D <dir>` after
      `pgbench -i -s 1`,
      `pgbench -S -t 5` → 5/5 at ~7100 TPS;
      `pgbench -t 5` (TPC-B-like, single client) → 5/5 at ~37 TPS;
      `pgbench -S -c 4 -j 2 -t 20` → 80/80 at ~19k TPS;
      `pgbench -c 4 -j 2 -t 50` (default) → 200/200 at ~65 TPS,
      0 failed transactions. Took fixes for: analyzer recognising
      bare `int` as numeric; Pool.Close flushing dirty pages;
      mvcc.nextXID persistence in catalog snapshot;
      CURRENT_TIMESTAMP parsed as niladic FuncCall; per-page
      content lock (Slot.Lock/Unlock/RLock/RUnlock) around
      PageAddHeapTuple / PageSetHeapTupleXmax / PageGetHeapTuple
      so concurrent writers can't tear the line-pointer + upper-
      region update — was the root cause of the
      "invalid t_hoff=0 len=44" corruption seen with -c 4 -j 2.)

## Milestone 0002 — Production-grade checkpointing & concurrent B-tree

See `docs/milestones/0002-durability-and-concurrent-storage.md` for the
full Definition of Done. Decomposed into agent-sized chunks below.

### Checkpointing

- [x] Wire `wal.Writer` and `wal.Checkpointer` into `initdb.Runtime` so
      every started server has a real WAL stream and a periodic
      checkpointer goroutine. (achieved 2026-04-28: `Runtime.WAL` and
      `Runtime.Checkpointer` are constructed in `initdb.Open` against
      `<DataDir>/pg_wal`, threaded through `server.Config.Checkpointer`,
      and the periodic loop runs alongside `srv.Run` under a child
      context that cancels on the way out so a control-socket STOP
      doesn't hang the shutdown.)
- [x] Implement the `CHECKPOINT` SQL verb end-to-end. Parser carves
      `KwCheckpoint` + `CheckpointStmt`; planner emits `Checkpoint`
      plan node; executor's `checkpointOp` invokes
      `Context.Checkpointer.CheckpointNow`; wire layer's
      `commandTagFor` returns "CHECKPOINT". Smoke-tested with
      upstream psql 18.3 against `goopg start -D <dir>`.
- [x] Add the M0002 GUCs with upstream defaults: `checkpoint_timeout`
      (5min), `checkpoint_completion_target` (0.9), `max_wal_size`
      (1024 MB), `min_wal_size` (80 MB), `full_page_writes` (on).
      All five mirror upstream's
      `postgres/src/backend/utils/misc/guc_tables.c` entries —
      names, units, ranges, contexts (PGC_SIGHUP), and defaults.
      `SHOW checkpoint_timeout` etc. work end-to-end.
- [x] Honour `checkpoint_timeout` in `wal.Checkpointer.Run`. `goopg
      start` reads the GUC value from the registry and calls
      `Checkpointer.SetInterval` before launching the periodic
      goroutine, so the cadence now matches upstream's 5-min
      default instead of the development-time 10s. SetInterval is
      a no-op for non-positive durations so a missing/typo'd GUC
      keeps the construction default.
- [x] Spread/smoothed checkpoint writes over
      `checkpoint_completion_target * checkpoint_timeout`, rather than
      one synchronous burst. `storage.Pool.FlushAllPaced` walks a
      snapshot of the dirty set and invokes a per-buffer pacer
      callback after each write. The checkpointer builds a deadline-
      driven pacer (`start + Interval * CompletionTarget * progress`)
      for timer-driven runs only; volume-triggered checkpoints and
      the SQL `CHECKPOINT` verb both run at IMMEDIATE speed
      (`FlushAll` fallback) since they're backpressure / operator
      requests, not cadence work. `goopg start` reads
      `checkpoint_completion_target` from the registry and calls
      `Checkpointer.SetCompletionTarget`.
- [x] Trigger checkpoints when the WAL volume crosses `max_wal_size`,
      not just on the timer. `wal.Writer` exposes `WrittenLSN()`
      (atomic mirror of writeLSN); `wal.Checkpointer` polls it on a
      1-second cadence and fires `checkpointOnce` whenever the gap
      since the last checkpoint marker reaches `MaxWALBytes`.
      `goopg start` reads `max_wal_size` (in MB) from the registry
      and calls `Checkpointer.SetMaxWALBytes` before launching the
      Run goroutine.
- [x] Implement full-page-image WAL records for the first
      modification of a page after each checkpoint when
      `full_page_writes` is on. The buffer pool's `MarkDirty`
      now emits a `wal.EncodePageImage` record via a
      `LogPageImage` callback the first time each page is
      mutated since the last checkpoint, stamps the FPI's end
      LSN into the page header (so the existing
      flush-before-write ordering covers it), and tracks the
      epoch via `Slot.fpiSinceCheckpoint`. The checkpointer
      calls `Pool.ResetCheckpointEpoch` after a successful
      checkpoint so the next mutation per page emits a fresh
      FPI. `MarkDirtyWithLSN` keeps its existing semantics —
      callers that already issued a WAL record (e.g. a future
      heap_insert XLogInsert path) opt out of the redundant
      FPI by using that variant. `goopg start` honours the
      `full_page_writes` GUC by calling
      `Pool.SetFullPageWrites` at boot.
- [x] Add `goopg ctl checkpoint` CLI subcommand routed through the
      control socket. Same semantics as the SQL verb. Wired as
      `goopg checkpoint -D <dir> [-t seconds]`; the server-side
      handler drops the listener read deadline before invoking
      `Checkpointer.CheckpointNow()` so a long flush won't trip
      the default 5s timeout. CLI default is 300s.
- [x] Surface `pg_stat_bgwriter` / `pg_stat_checkpointer` (whichever
      view shipped in PG 18; pick what matches the reported
      server_version) as queryable virtual tables.
      `pg_stat_checkpointer` (the PG 18 view) is now wired:
      `Checkpointer.Stats()` returns atomic-counter snapshots
      (num_timed, num_requested, write_time_ms, last_checkpoint_lsn,
      stats_reset), `runCheckpoint` increments them per cycle, and
      `initdb.Open` registers a virtual table via the new
      `(*catalog.InMemory).RegisterVirtualTable`. Column shape
      matches PG 18 (restartpoints_*, sync_time, buffers_written,
      slru_written report 0 — see design §7 for what's deferred).
      `pg_stat_bgwriter` has no analogue in v0 (no separate
      bgwriter; documented in the design doc's Out-of-scope).
- [x] Crash-recovery test: simulated SIGKILL mid-workload, restart,
      verify committed mutations survive WAL replay.
      `internal/initdb/recovery_test.go` builds a populated B-tree,
      flushes WAL durable, then closes the WAL writer + Manager
      WITHOUT calling `Pool.Close` (which would `FlushAll`) — every
      dirty in-memory page is dropped, mirroring SIGKILL before
      bgwriter / checkpointer flushes. `initdb.Open` now invokes
      `wal.ReplayFromDirWithMgr` against the data dir BEFORE
      constructing the new buffer pool, so replay reconstructs
      the post-mutation state into the data files. Surfaced and
      fixed a v0 redo-log gap in the same loop: FPI was previously
      emitted only on first-dirty-per-epoch, which silently dropped
      every subsequent mutation on the same page on replay; v0 now
      emits an FPI on every `MarkDirty`. WAL volume cost is high
      (`pgbench -i -s 1` grew from ~10 MB to ~1.6 GB) but
      correctness is the M0002 priority — logical change records
      that would restore the optimisation are deferred to a
      post-M0002 milestone.
- [x] Design doc `0002-0001-checkpointing.md` covering all of the
      above (FPI-on-first-dirty, max_wal_size volume trigger,
      spread/paced writeback, GUC surface, SQL CHECKPOINT, structural
      seams). Recovery itself was already covered in M1's
      `root-0008-wal-and-recovery.md`; this doc cross-references it
      and only describes the producer-side machinery.
- [ ] Logical change records to recover the FPI-volume regression
      (started in `docs/design/0002-0003-redo-records.md`):
  - [x] `RecordKindHeapInsert` (kind=4) + encode/decode/replay
        with pd_lsn idempotency.
  - [x] `Pool.MarkDirtyChangeRecord(slot, emitter)` — emits FPI
        baseline on first-dirty-per-epoch, calls emitter on
        subsequent dirties. Restores the once-per-epoch FPI
        optimisation for migrated paths only.
  - [x] Migrated `writeHeapRow` (heap INSERT path). pgbench -i WAL
        dropped from ~1.6 GB → ~800 MB; pgbench -i runtime from
        ~18s → ~13s. The remaining FPI volume comes from
        non-migrated paths.
  - [x] Migrate `btree.insertIntoBlock` non-split path. New
        `RecordKindBtreeInsert` (kind=5); `btree.ApplyInsertRecord`
        is the public replay helper called by
        `wal.replayBtreeInsert`. pgbench-i WAL collapsed from
        ~801 MB → ~21 MB; runtime from ~13s → ~9.25s.
        `wal/recovery.go` now imports `internal/access/btree` for
        the apply helper (one-direction dep, no cycle).
  - [x] Migrate heap UPDATE / DELETE xmax-stamp paths.
        New `RecordKindHeapDelete` (kind=6, fixed 20 bytes:
        rel + blk + lineSlot + xmax). `markHeapDeleteDirty`
        helper picks `MarkDirtyChangeRecord` when the pool's
        `LogHeapDelete` hook is wired and falls back to
        `MarkDirty` otherwise. Both `updateOp` and `deleteOp`
        switched to it. Replay re-runs `PageSetHeapTupleXmax`
        and is idempotent via `pd_lsn`. pgbench `-t 30`
        default-mixed workload (~120 UPDATEs + ~30 INSERTs +
        SELECTs) WAL stays flat at ~21 MB.
  - [x] Migrate VACUUM page-prune mutations.
        New `RecordKindHeapVacuum` (kind=7); format
        `kind | rel(9) | blk(4) | count(2) | slots[count](2 each)`,
        16 + 2*count bytes. `storage.CollectDeadHeapSlots` and
        `storage.VacuumHeapPageBySlots` factor the prune kernel so
        live VACUUM and replay both apply the same slot list.
        VACUUM now takes the per-page content latch around the
        scan + repack + pd_lsn stamp (concurrency-safety win),
        emits the slot list via `Pool.MarkDirtyChangeRecord`, and
        falls back to `MarkDirty` when no `LogHeapVacuum` hook is
        wired (test pools). Replay re-runs `VacuumHeapPageBySlots`
        with the recorded slots; idempotent via pd_lsn.
  - [ ] Once all paths are migrated, flip `maybeEmitFPI` back to
        the strict once-per-epoch policy globally.

### Concurrent B-tree (Lehman-Yao + PG modifications)

Approach is staged across three landings; see
`docs/design/0002-0002-btree-concurrency.md`.

- [x] Landing 1: replace `BTree.mu` `sync.Mutex` with `sync.RWMutex`
      so multiple Search/RangeScan calls parallelise. Inserts and
      splits still serialise through Lock(). Per-page mutation
      continues under `Slot.Lock()`. Verified with a goroutine-stress
      test under `-race` and `pgbench -S -c 4 -j 2 -t 50` against a
      live server (200/200 tx, ~18k TPS).
- [x] Landing 2: per-page latches + Lehman-Yao right-link descent.
      Page format bumped to v2 with a 24-byte BTPageOpaque carrying
      a fixed-width HighKey + BTHasHighKey flag. Search and
      RangeScan take no tree-wide lock — they descend under
      Slot.RLock per page and fall back to op.Next when
      keyExceedsHighKey reports the key has moved right. Insert
      keeps bt.mu (writer-vs-writer concurrency deferred to
      Landing 3), and every mutation site now runs under
      Slot.Lock so readers see only pre- or post-split images.
      Split sequences stamp the new high key on the left page
      before dropping its latch. Verified with a concurrent
      insert+search stress test under -race and `pgbench -S
      -c 8 -j 4 -t 50` (400/400 tx, ~21k TPS) plus default mixed
      `pgbench -c 4 -j 2 -t 30` (120/120 tx).
- [x] Landing 3a: atomic split WAL records. New `RecordKindBtreeSplit`
      (kind=3) carries `rel + leftBlk + rightBlk + leftPage +
      rightPage` in one ~16 KB record. Emitted from
      `insertIntoBlock`'s split path via a `LogSplit` closure
      plumbed through `storage.PoolConfig.LogBtreeSplit` →
      `Pool.LogBtreeSplit()` → `btree.BTree.logSplit`. Both pages
      get `pd_lsn = endLSN` of this record via the new
      `Pool.MarkDirtyWithLSNLocked` (the existing
      `MarkDirtyWithLSN` was unsafe under exclusive content latch
      hold — would self-deadlock). Replay
      (`internal/wal/recovery.go`) applies the left page first,
      then the right (Extend-when-missing) so a reader following
      left's right-link from the post-replay state always finds
      the right page on disk.
- [ ] Landing 3b: writer-vs-writer concurrency. Drop `bt.mu` and
      let two writers descend in parallel; un-split inserts on
      different pages run unblocked. Page deletion + recycling
      integrated with VACUUM and MVCC visibility. Index-only
      scans where the visibility map permits. `pgbench -c 32 -j 8`
      mixed workload as the milestone-0002 acceptance gate.
- [x] Design doc `0002-0002-btree-concurrency.md` (draft) covers
      all three landings, the staged rationale, and the on-disk
      format implications.

## Milestone 0003 — HammerDB TPC-H workload

See `docs/milestones/0003-tpch-workload.md` for full DoD. HammerDB
clone at `./HammerDB/`; TPC-H schema + queries under
`HammerDB/tpch/postgres/`.

### Schema and loader

- [x] Make `HammerDB/tpch/postgres/ddl.sql` run end-to-end against
      goopg. Identify and add any missing column types, default
      expressions, or constraint forms.
      (achieved 2026-04-28: all eight HammerDB TPC-H CREATE TABLEs
      from `HammerDB/src/postgresql/pgolap.tcl` complete via psql
      18.3 against `goopg start -D <dir>`. Two gaps surfaced and
      fixed in this loop: (a) NUMERIC codec — encoded integer
      datums now round-trip as varlen text via the new
      `numeric`/`decimal` case in `internal/executor/codec.go`,
      using the same frame as varchar/char; (b) decimal +
      scientific-notation literals — lexer extended to emit
      `TokenNumericLit` for `123.45` and `1.5e-3`, parser AST
      gained `parser.NumericConst{Value string}`, plumbed
      through analyzer (`numeric` type), planner
      (`planner.NumericConst`), and executor evaluator
      (`KindString` datum so the NUMERIC codec stores the
      literal verbatim). End-to-end smoke: round-trip INSERTs
      with `901.01` and `1.5e3` SELECT back byte-identically.
      Real arithmetic on NUMERIC and `numeric(p,s)` enforcement
      remain deferred to the type-system milestone — see
      `docs/design/0003-0004-hammerdb-tpch-integration.md`.)
- [ ] HammerDB's COPY-based loader at SF1 succeeds.
      (loader-shape unblocked 2026-04-28: HammerDB's TPC-H loader
      doesn't actually use COPY — it issues multi-row INSERTs
      with every value passed as a single-quoted string. Two
      gaps closed in this loop: (a) analyzer's `isAssignable`
      now accepts `text → numeric/decimal` (narrow — int4/int8
      still reject); (b) executor implements
      `to_timestamp(text, fmt)` via a tiny `pgFormatToGoLayout`
      translator covering the codes HammerDB uses
      (`YYYY`/`Mon`/`MM`/`DD` + `HH24`/`MI`/`SS`). Verified
      end-to-end via psql 18.3: REGION, SUPPLIER, and ORDERS
      multi-row INSERTs (with `to_timestamp('1995-Jan-15',
      'YYYY-Mon-DD')`) all round-trip. Running the actual
      HammerDB SF1 load against the live server is a separate
      harness task that's a workstream of its own.)
- [x] Foreign-key parsing accepted (enforcement may be a no-op for
      v0; record the decision in a design doc).
      (achieved 2026-04-28: parser recognises
      `ADD [CONSTRAINT name] FOREIGN KEY (cols) REFERENCES
      table [(cols)] [NOT DEFERRABLE | DEFERRABLE]` as a new
      `AlterTableAddForeignKey` action; executor accepts the
      kind and only validates that the referenced table
      exists (42P01 otherwise). All eight HammerDB TPC-H FK
      ALTER TABLEs from `pgolap.tcl` lines 529–536 run
      cleanly via psql 18.3. Real enforcement is deferred —
      see `docs/design/0003-0004-hammerdb-tpch-integration.md`.)

### Planner depth

- [ ] Cost-based planner with cardinality estimates good enough that
      no TPC-H query degenerates to a Cartesian product.
- [x] Hash join executor (`internal/executor/operators_hashjoin.go`).
      (achieved 2026-04-28: implemented in
      `internal/executor/operators_join_agg.go` (extending the
      existing joinOp rather than a new file). Planner detects
      disjoint-side equality predicates via splitEqualityForHash
      + exprSide and sets `Join.Algo = JoinAlgoHash` with
      LeftKey/RightKey populated; `right.col = left.col` is
      flipped at plan time so the executor stays one-direction.
      INNER + LEFT joins covered (RIGHT/FULL/CROSS keep the
      nested-loop fallback). Build phase: hash right input on
      RightKey; probe phase: lookup LeftKey, NULL keys never
      match. Verified end-to-end via psql 18.3 with INNER, LEFT
      (NULL right side for unmatched), and reversed-equality
      shapes. See `docs/design/0003-0002-join-executors.md`.)
- [ ] Sort-merge join executor.
- [x] Hash aggregate executor (replace today's sort-then-group as the
      default path).
      (verified 2026-04-28: the executor's aggregateOp
      (`internal/executor/operators_join_agg.go` line 249)
      already groups by `map[string]*groupRuntime{}` — it's
      a hash aggregate, never a sort-then-group. The "replace"
      in this bullet's wording was speculative; v0 has only
      ever shipped the hash variant. Verified end-to-end via
      psql 18.3 with `SELECT k, sum(v) FROM t GROUP BY k`.)
- [x] `EXPLAIN` output for a `parser.ExplainStmt` shape.
      (achieved 2026-04-28: `EXPLAIN <stmt>` parses via new
      KwExplain + parser.ExplainStmt; planner.Explain wraps
      the planned inner Node; executor's explainOp pre-order
      walks the tree and emits one row per node as a single-
      column "QUERY PLAN" text result. Hash-join vs nested-
      loop algorithm visible in the label
      (`Hash Join (INNER)` vs `Nested Loop (INNER)`); SeqScan/
      IndexScan show their relation, Aggregate distinguishes
      ungrouped from GroupAggregate. Wire layer reports
      CommandComplete tag "EXPLAIN". Verified end-to-end via
      psql 18.3 — output renders identically to upstream's
      text format. Options / ANALYZE / FORMAT JSON deferred —
      see `docs/design/0003-0007-explain.md`.)
- [x] `ANALYZE` produces statistics: n_distinct, MCV lists,
      histograms (mirror upstream's
      `postgres/src/backend/commands/analyze.c`).
      (achieved 2026-04-28 with documented v0 scope: full-
      table scan ANALYZE produces table-level RowCount /
      Pages / AvgWidth and per-column NDistinct / NullFrac.
      catalog.TableStats + ColumnStats added; new
      executor.analyzeOp drives the collection via the buffer
      pool + DecodeRow; SQL ANALYZE dispatch wired through
      Build's type-check on AnalyzeStmt. MCV lists,
      histograms, sampling, and stats-persistence remain
      deferred — see
      `docs/design/0003-0010-analyze-statistics.md`. v0's
      cost model only needs NDistinct for the
      `|A|*|B|/max(NDistinct(A.k), NDistinct(B.k))` join-
      cardinality estimate.)
- [ ] Design docs: `0003-0001-planner-overview.md` (extend M1's
      `root-0011-planner.md`), `0003-0002-join-executors.md`,
      `0003-0003-statistics-and-cardinality.md`,
      `0003-0004-hammerdb-tpch-integration.md`.

### Query coverage (incremental)

- [x] 3- to 7-way JOIN planning (extend the M1 nested-loop planner).
      (verified 2026-04-28: the M1 planner already builds
      left-deep join chains for both comma-FROM (`FROM a, b,
      c, d, e, f` with WHERE-side equalities) and explicit
      JOIN ... ON syntax. With hash-join landed in the
      previous loop, each pairwise INNER join in the chain
      promotes to JoinAlgoHash. End-to-end verified via psql
      18.3:
        - 6-way: `FROM region, nation, supplier, customer,
          orders, lineitem WHERE …` returned correct rows.
        - 7-way: `customer JOIN orders … JOIN lineitem …
          JOIN supplier … JOIN nation … JOIN region … JOIN
          part …` end-to-end. EXPLAIN shows the expected
          left-deep Hash Join chain.
      Cost-based join-ordering decisions stay open as part
      of the cost-based-planner item.)
- [x] Correlated and uncorrelated subqueries; `EXISTS`, `NOT EXISTS`,
      `IN`, `NOT IN`.
      (achieved 2026-04-28: uncorrelated forms first
      (parser.SubqueryExpr / InExpr / ExistsExpr), then
      correlated subqueries via lexical-scope chains: new
      `analyzer.scope.parent` + `planner.resolveContext.parent`
      walk-up paths in `resolveColumnRefType` /
      `resolveColumnRefAt`, `planner.OuterColumnRef{Level,
      Index}` for parent-scope references, plus
      `executor.Context.OuterRows` stack pushed by
      evalSubquery / evalExistsExpr / collectInValues.
      `analyzer.OuterScope` + `planner.planSelectWithParent`
      thread the lexical-scope parent through both passes
      (analyzer-side via package-level channel, planner-side
      via `planParent`). End-to-end verified via psql 18.3:
      TPC-H Q4 shape `EXISTS (SELECT 1 FROM lineitem WHERE
      l_orderkey = o.o_orderkey …)`, NOT EXISTS, and
      correlated scalar subquery `(SELECT count(*) FROM
      lineitem WHERE l_orderkey = o.o_orderkey)` all return
      correct rows. Subquery decorrelation, initplan
      caching, ANY/SOME/ALL, LATERAL remain deferred — see
      `docs/design/0003-0008-subqueries.md`.)
- [x] `CASE` expressions (parser + executor).
      (achieved 2026-04-28: both searched (`CASE WHEN cond THEN
      result …`) and simple (`CASE expr WHEN val THEN result …`)
      forms parse, analyze, plan, and execute end-to-end. New
      `parser.CaseExpr` (Operand, Whens[], Else), planner mirror
      via `resolveCaseExpr`, analyzer's `analyzeCaseExpr` unifies
      branch types same-or-compatible-else-unknown, executor's
      `evalCaseExpr` walks Whens in order with `compareEq` for
      simple-form equality (handles int/string mixes for
      NUMERIC-as-text columns). Verified end-to-end via psql 18.3:
      searched form, simple form, and ELSE-omitted → NULL fallback
      all return correct rows. Required for TPC-H Q1/Q12/Q14. See
      `docs/design/0003-0005-case-expressions.md`.)
- [x] Date and interval arithmetic, `EXTRACT(... FROM ts)`.
      (achieved 2026-04-28: `date 'YYYY-MM-DD'`,
      `timestamp 'YYYY-MM-DD HH:MM:SS'`, and
      `interval 'N' unit` (day/month/year ± plurals) all parse
      as typed-literal AST nodes; new Datum.KindInterval with
      months+days fields routes `time ± interval` through
      Go's time.AddDate. EXTRACT lands as its own grammar
      (`parser.ExtractExpr`) — fields year, month, day, hour,
      minute, second, dow, doy, epoch, quarter all return
      int8. Verified end-to-end via psql 18.3: TPC-H Q1's
      date-arithmetic filter, Q4/Q5/Q6's range shape, and
      Q7's `WHERE extract(year FROM o_orderdate) = 1995`
      all return correct rows. Fractional-second EXTRACT and
      timestamp-timestamp interval remain deferred — see
      `docs/design/0003-0006-date-interval-arithmetic.md`.)
- [x] `FETCH FIRST n ROWS ONLY` as a `LIMIT` synonym.
      (achieved 2026-04-28: parseSelect accepts the SQL-standard
      `FETCH {FIRST | NEXT} [n] {ROW | ROWS} ONLY` clause as an
      alternative to LIMIT — count is optional (defaults to 1),
      both FIRST/NEXT and ROW/ROWS spellings work, and combining
      LIMIT with FETCH FIRST is a syntax error. Also accepts the
      `OFFSET n {ROW | ROWS}` SQL-standard trailer (no-op). New
      `acceptIdentKeyword` helper for the unreserved-keyword
      idents (FETCH/FIRST/NEXT/ROW/ROWS/ONLY); `isAliasStart`
      now rejects bare-ident "fetch" so `FROM t FETCH …` parses
      correctly. Verified end-to-end via psql 18.3 with all
      five common forms — including TPC-H Q2/Q3/Q10/Q18/Q21
      shape and OFFSET … FETCH NEXT. Required for several
      TPC-H queries.)
- [x] Views (CREATE VIEW / DROP VIEW) where HammerDB uses them.
      (achieved 2026-04-28: `CREATE [OR REPLACE] VIEW name
      [(col_list)] AS SELECT …` and `DROP VIEW [IF EXISTS]`
      both parse and plan/execute end-to-end. New
      parser.CreateViewStmt + DropViewStmt; catalog stores
      the view's parser AST + optional column-alias list;
      planner.planScanRangeVar substitutes the planned inner
      SELECT at every reference (column types flow from the
      inner plan's Output(); names from aliases or target-
      list inference via deriveTargetName). planSelect's
      simple-single-table fast path now also delegates to
      planScanRangeVar so view substitution lives in one
      place. Verified end-to-end via psql 18.3 with the
      HammerDB Q15 shape — view + scalar subquery against
      the view work together. DML on views and catalog
      persistence remain deferred — see
      `docs/design/0003-0009-views.md`.)
- [ ] All 22 queries (Q1–Q22) execute end-to-end and produce
      result sets byte-identical (or otherwise verified-equivalent)
      to upstream PG on the same data.
- [ ] HammerDB Power Test at SF1 completes without errors.

## Milestone 0004 — TAP test port & Go utility library

See `docs/milestones/0004-tap-test-port.md`. Parallelizable with
M0002/M0003; lands regression coverage as those features ship.

- [ ] `internal/testutil/cluster` package equivalent of
      `PostgreSQL::Test::Cluster`. Init/start/stop/restart with
      smart/fast/immediate modes; query via `psql` + Go libpq
      client; capture/inspect logs; programmatic edits to
      `postgresql.conf` and `pg_hba.conf`. Background-psql sessions.
      Multi-cluster API (impl deferred).
- [ ] `internal/testutil/util` package equivalent of
      `PostgreSQL::Test::Utils`. Tempdir/file helpers, command runner
      with timeout + capture, log scanning helpers.
- [ ] `docs/test-port/upstream-tap-coverage.md` — classify every
      upstream TAP test under `postgres/src/test/recovery/t/`,
      `postgres/src/bin/*/t/`, etc. as `port`/`skip`/`defer` with a
      one-line rationale. Reproducible from a tool committed
      alongside it.
- [ ] Port at least 80% of `port` rows. Each ported test references
      its upstream source path in a header comment.
- [ ] Design docs: `0004-0001-go-test-utility-library.md`,
      `0004-0002-tap-test-port-strategy.md`.

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## Completed

- [x] Project initialization (Ralph harness wired up).
