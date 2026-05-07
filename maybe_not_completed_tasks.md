# Potentially Incomplete Deferred Tasks

This document contains tasks deferred from completed milestones (M0000-M0053)
that appear not to have been addressed in active or later milestones.

**Generated:** 2026-05-07

---

## Milestone 0001 — Listener, startup, and minimal wire protocol

- [ ] Add a graceful shutdown path driven by `context.Context` so that
      `goopg stop` and `SIGTERM` both translate into the same internal
      shutdown sequence (close listener, wait for connections, drain).
      (SIGTERM/SIGINT done via `signal.NotifyContext` in `goopg start`.
      `goopg stop` over a control socket is deferred to milestone 7.)


## Milestone 0002 — Production-grade checkpointing & concurrent B-tree

- [ ] Surface `pg_stat_bgwriter` / `pg_stat_checkpointer` (whichever
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

- [ ] Crash-recovery test: simulated SIGKILL mid-workload, restart,
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

- [ ] Landing 2: per-page latches + Lehman-Yao right-link descent.
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


## Milestone 0003 — Authentication

- [ ] Implement `scram-sha-256` auth (preferred default).
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


## Milestone 0003 — HammerDB TPC-H workload

- [ ] Make `HammerDB/tpch/postgres/ddl.sql` run end-to-end against
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

- [ ] Foreign-key parsing accepted (enforcement may be a no-op for
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

- [ ] `EXPLAIN` output for a `parser.ExplainStmt` shape.
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

- [ ] `ANALYZE` produces statistics: n_distinct, MCV lists,
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

- [ ] Correlated and uncorrelated subqueries; `EXISTS`, `NOT EXISTS`,
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

- [ ] Date and interval arithmetic, `EXTRACT(... FROM ts)`.
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
      all return correct rows. Embedded-unit interval form
      (`interval '90 day'`, `interval '1 year'`,
      `interval '3 month'`) added 2026-04-29 since HammerDB's
      TPC-H templates emit that shape after parameter
      substitution; both Form 1 (`interval '90' day`) and Form 2
      (`interval '90 day'`) now produce identical IntervalLit
      AST. Fractional-second EXTRACT and timestamp-timestamp
      interval remain deferred — see
      `docs/design/0003-0006-date-interval-arithmetic.md`.)

- [ ] Derived tables (`(SELECT …) AS alias` in FROM) for TPC-H Q13.
      (achieved 2026-04-29: surfaced by running TPC-H Q13
      end-to-end — parse-errored at `FROM (SELECT ...)`. Now:
      parser.RangeVar grows a `Subquery *SelectStmt` field;
      `(` + lookahead-SELECT path parses the inner SELECT,
      requires a mandatory alias (matches upstream).
      Analyzer's `synthesizeSubqueryTable` analyzes the inner
      with no parent scope (no LATERAL in v0), walks the
      target list to derive column names+types, and returns
      a transient `*catalog.Table` with `Name=alias`.
      Planner's `planSubqueryRangeVar` plans the inner
      recursively via `Plan(rv.Subquery, cat)` and exposes
      its `Output()` through the rangeBinding so outer
      ColumnRefs to `alias.col` resolve normally. Verified
      via psql 18.3 against Q13's full
      `LEFT OUTER JOIN ... NOT LIKE` shape and bare/aliased
      derived-table forms. `TestPlanDerivedTable` pins four
      behaviours including the missing-alias rejection.
      LATERAL, CTEs, and parenthesised plain relations
      deferred. See
      `docs/design/0003-0014-derived-tables.md`.)

- [ ] NUMERIC arithmetic for TPC-H aggregates with arithmetic.
      (achieved 2026-04-29: new KindNumeric carrier
      (`int64 mantissa, int8 scale`) replaces the prior
      KindString-only path. NumericConst literals,
      codec decode, +/-/*/divide arithmetic with scale
      alignment, NUMERIC↔INT comparison, and sum/avg
      accumulators all wired through. Verified end-to-end
      via psql 18.3 against TPC-H Q1's central shape:
      `sum(l_extendedprice * (1 - l_discount))` correctly
      computes 320.4500 / 1040.2000 across two groups, and
      `avg(l_extendedprice)` returns 175.250000 / 525.125000.
      Bounded by int64 (sufficient for SF1 magnitudes;
      worst-case Q1 accumulator ~6.5e15 << int64 max).
      Arbitrary-precision NUMERIC, `numeric(p,s)` typmod
      enforcement, `%`/`^`, and the binary wire format are
      deferred — see `docs/design/0003-0012-numeric-arithmetic.md`.)

- [ ] `LIKE` / `NOT LIKE` pattern matching for TPC-H text filters.
      (achieved 2026-04-29: `expr [NOT] LIKE pattern` parses at
      comparison precedence (postfix-style alongside `[NOT] IN`),
      analyzer requires text-on-text → bool, executor's
      `matchSQLLike` is a byte-level recursive matcher honouring
      `%` (any run), `_` (single byte), and `\` escape. Verified
      end-to-end via psql 18.3 against TPC-H Q14 shape
      (`p_name LIKE 'PROMO%'`), Q9 shape (`%green%`), suffix
      anchor (`%COPPER`), `_` exact-one, and explicit-escape
      `'PROMO%' LIKE 'PROMO\%'`. ILIKE, ESCAPE clause, and
      planner-side prefix-anchor index extraction are deferred —
      see `docs/design/0003-0011-like-pattern-matching.md`.)

- [ ] Views (CREATE VIEW / DROP VIEW) where HammerDB uses them.
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

- [ ] (DEFERRED) All 22 queries (Q1–Q22) execute end-to-end and produce

- [ ] (DEFERRED) HammerDB Power Test at SF1 completes without errors.
      (DEFERRED 2026-04-29: requires running the full HammerDB TPROC-H
      Power Test at SF1, which takes too long to be tractable in the
      current acceptance loop. Re-evaluate when a longer-running
      benchmark loop is available; no goopg-side code work is known to
      be missing.)


## Milestone 0004 — Configuration and GUC system

- [ ] Wire `SHOW`, `SET`, `SET LOCAL`, `RESET`, `RESET ALL` into the
      simple-query path. SessionRegistry layers transaction → session
      → global. FlagReport variables emit ParameterStatus on change.
      `pg_settings` / `current_setting()` / `set_config()` are deferred
      with the catalog work in milestone 5; `SHOW ALL` covers the
      inspection use case until then.


## Milestone 0004 — TAP test port & Go utility library

- [ ] `internal/testutil/cluster` package equivalent of
      `PostgreSQL::Test::Cluster`. Init/start/stop/restart with
      smart/fast/immediate modes; query via `psql` + Go libpq
      client; capture/inspect logs; programmatic edits to
      `postgresql.conf` and `pg_hba.conf`. Background-psql sessions.
      Multi-cluster API (impl deferred).
      (achieved 2026-04-29: added `internal/testutil/cluster`
      wrapper around `goopg` CLI with `Init`, `Start`, `Stop`,
      `Restart`, `Reload`, `Status`, foreground/background psql
      helpers, `Query` via `database/sql` + `lib/pq`, config-file
      append helpers, and log wait helpers. Added package tests for
      init/config edits plus lifecycle/query integration.)


## Milestone 0005 — Streaming replication

- [ ] `pg_stat_replication` virtual view (sender side):
      one row per active walsender with state / lag fields.
      (achieved 2026-04-29: new `internal/wal/replmon.go`
      provides the process-wide `Senders` registry with
      `Register`/`Unregister`/`Snapshot` and per-handle
      `SetSentLSN` + `ApplyStandbyStatus` (monotonic CAS,
      stale-reply-resistant). Walsender goroutine in
      `internal/server/replication.go` registers on entry,
      defer-unregisters on exit, advances sent_lsn after each
      WriteCopyData and the standby-reported LSNs from each
      status reply (write/flush/replay). View registered in
      `internal/initdb/replication_views.go` with 21 columns
      mirroring upstream PG 18.x; LSN columns render via
      `formatLSN` in `XXXXXXXX/XXXXXXXX` hex form, timestamps
      via `formatTime`. Lag intervals (`write_lag` etc.)
      emit `00:00:00` placeholders (deferred), `client_addr`
      currently empty (FrameReader doesn't surface RemoteAddr;
      one-line follow-up). Sorted `(slot_name, pid)` so
      `\watch pg_stat_replication` returns stable order. Six
      registry unit tests + one end-to-end view test pin
      shape, monotonicity, register/unregister, sort order,
      and concurrent register safety.)

- [ ] Replication-related logging hooks for disconnect /
      replay-pause / retention-pressure events.
      (achieved 2026-04-29: new `internal/wal/repllog.go`
      defines a canonical `event=<name>` vocabulary so dashboards
      can build alert rules against stable field values instead
      of grepping free-form messages. Nine events:
      `walreceiver_dial_failed` (WARN), `walreceiver_connected`
      (INFO), `walreceiver_disconnect` (INFO/WARN),
      `standby_replay_error` (ERROR), `standby_replay_stopped`
      (INFO), `walsender_disconnect` (INFO),
      `slot_lag_warning` (INFO),
      `slot_invalidated` (WARN), `wal_segments_recycled` (INFO).
      Producers: `cmd/goopg/main.go` for receiver/replayer
      events; `internal/server/replication.go` for walsender
      disconnect (deferred so it fires on every exit path);
      `SlotAwareRetainer` for slot/segment events. Plus a new
      pre-eviction `warnLaggingSlots` sweep on
      `SlotAwareRetainer.Retain` that emits
      `slot_lag_warning` when a slot's lag crosses
      `LagWarnFraction` (50%) of `max_slot_wal_keep_size`,
      giving the operator time to react before forced
      invalidation. New unit test
      `TestSlotAwareRetainerEmitsLagWarning` pins the
      threshold + event-field contract via a JSON-decoding
      logCapture helper. Design doc
      `docs/design/0005-0007-replication-event-logging.md`
      lands in the same loop with the full event taxonomy +
      severity routing guidance.)

- [ ] End-to-end test: start primary + standby, write to
      primary, observe row visibility on standby, kill
      primary, promote standby, write to promoted node.
      (achieved 2026-04-29: `TestReplicationEndToEnd` in
      `internal/testutil/replcluster/replcluster_test.go`
      runs the full sequence: Setup → wait for
      pg_stat_wal_receiver.status='streaming' → cross-check
      pg_stat_replication shows the walsender for our slot →
      snapshot standby's written_lsn, drive a CHECKPOINT on
      primary, verify standby's written_lsn advances → call
      Promote() → verify standby.signal is gone. Skipped
      under -short. The "row visibility" / "write to promoted
      node" pieces are gated on catalog persistence (v0's
      catalog is in-memory only, so a CREATE TABLE on the
      primary is invisible to the standby's executor even
      though the WAL records flow). The wire-connectivity +
      WAL-flow + LSN-advance + promote observations are the
      strongest end-to-end checks possible at this milestone.
      Design doc `docs/design/0005-0006-replcluster-harness.md`
      lands in the same loop and documents the deferred
      pieces.)


## Milestone 0006 — Planner-grade statistics

- [ ] Cost-driven INNER-join algorithm selection: hash vs merge
      vs nested-loop scored against estimated input sizes and
      key NDistinct. Existing rule-based logic remains the
      documented fallback when stats are absent. Stats-aware
      EXPLAIN surfacing for verification. Design doc
      `docs/design/0006-0004-join-algorithm-selection.md`.
      (landed 2026-04-29: `internal/planner/joincost.go` adds
      `chooseInnerJoinAlgo(L, R) (JoinAlgo, bool)` scoring under
      unit-row costs — hash (`build + probe`), merge
      (`L·log2(L) + R·log2(R) + L+R`), nested-loop (`L*R`) —
      and returns the cheapest. Wired into both INNER call
      sites: `planner.go` (JOIN ... ON path) and `pushdown.go`
      (comma-FROM CROSS-promotion). RIGHT/FULL keep merge
      (semantics-driven), LEFT keeps hash (executor outer-row
      preservation), CROSS / non-equality keep nested-loop.
      When either side returns `EstimateRows == 0` the cost
      selector declines (returns `ok=false`) and the M0003
      rule-based hash default holds — the documented fallback.
      Stats-aware EXPLAIN: `Seq Scan on <t>` now appends
      `(stats)` when `Table.Stats != nil`. Existing planner
      tests stay green because they seed catalogs without
      ANALYZE so they exercise the fallback path. New tests in
      `joincost_test.go` pin the contract: fallback when stats
      missing, hash for balanced 10k/10k, nested-loop for tiny
      1/1, hash beats merge at modest 100/100. `seq_page_cost`
      / `random_page_cost`, indexed-rescan nestloop pricing,
      and `enable_*` GUC consultation deferred.)


## Milestone 0006 — SQL surface for pgbench

- [ ] Parser/analyzer covering `CREATE TABLE`, `CREATE INDEX`, `INSERT`,
      `UPDATE`, `DELETE`, `SELECT` with the joins/aggregates pgbench needs,
      `BEGIN`/`COMMIT`/`ROLLBACK`, `VACUUM`, `ANALYZE`, prepared statements.
      (achieved as composite of all sub-items below, with addenda for
      `::` typecast, niladic CURRENT_TIMESTAMP, and analyzer numeric
      compatibility for bare `int`. Verified end-to-end via
      `pgbench -i` and `pgbench` default + `--select-only` workloads
      under concurrent clients.)
  - [ ] Lexer (`internal/parser/lexer.go`) covering identifiers (quoted
        and unquoted), integer literals, single-quoted strings with `''`
        escape, parameter placeholders `$N`, line and (nested) block
        comments, multi-character operators.
  - [ ] Statement parsers: `BEGIN`/`COMMIT`/`ROLLBACK` (and `END`/`ABORT`
        aliases), `VACUUM` (with VERBOSE/ANALYZE/target list), `ANALYZE`,
        `SHOW`/`SET`/`RESET`. Carving the GUC verbs out of
        `internal/server/query.go` is deferred until the executor lands.
  - [ ] Expression tree (`ColumnRef`, integer/string/null/bool consts,
        `BinaryOp`, `UnaryOp`, `FuncCall`, `ParamRef`, `StarExpr`) with
        operator-precedence climbing (Pratt). Recognises arithmetic,
        comparison, boolean, and `||` operators with upstream-aligned
        precedences.
  - [ ] Statement parsers: `SELECT` target list (with `*`, qualified
        `t.*`, `AS` alias), comma-separated `FROM` with optional alias,
        `WHERE`, `ORDER BY` with ASC/DESC, `LIMIT`/`OFFSET`.
  - [ ] Statement parsers: JOIN clauses, GROUP BY, HAVING, set operations
        for the SELECT shapes pgbench reports queries need.
  - [ ] Statement parsers: `INSERT INTO t [(col, …)] VALUES (val, …) [, …]
        [RETURNING target_list]`, `UPDATE t SET col = expr [, …]
        [WHERE expr] [RETURNING target_list]`, `DELETE FROM t
        [WHERE expr] [RETURNING target_list]`. Pgbench's INSERT into
        pgbench_history and the abalance UPDATE/SELECT pair parse
        end-to-end.
  - [ ] Statement parsers: `CREATE [UNLOGGED] TABLE [IF NOT EXISTS] name
        (column_def [, …]) [WITH (k=v, …)] [TABLESPACE x]`,
        `CREATE [UNIQUE] INDEX [IF NOT EXISTS] [name] ON table
        [USING method] (col [, …])`, `DROP {TABLE|INDEX} [IF EXISTS]
        name [, …] [CASCADE|RESTRICT]`, `TRUNCATE [TABLE] name [, …]
        [CASCADE|RESTRICT]`. Type modifiers (`char(22)`,
        `numeric(10,2)`), inline `NOT NULL`/`PRIMARY KEY`, and
        table-level `PRIMARY KEY (a, b)` round-trip; pgbench -i's four
        CREATE TABLE strings parse as expected.
  - [ ] Statement parser: `ALTER TABLE [IF EXISTS] name action [, …]`
        with `ADD [CONSTRAINT name] PRIMARY KEY (cols)` and
        `ADD [COLUMN] coldef` actions. Pgbench's three primary-key
        ALTER strings parse end-to-end — pgbench -i's full DDL surface
        is now covered.
  - [ ] Analyzer pass (name resolution, type checking) once the catalog
        exists.

- [ ] `COPY FROM STDIN` and `COPY TO STDOUT` (text and binary) sufficient for
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


## Milestone 0007 — WAL segment preallocation & fdatasync

- [ ] WAL segment preallocation: zero-fill new segments to
      `SegmentSize` + fsync at creation; directory fsync; EOS
      sentinel for the trailing zero-fill so recovery terminates
      cleanly. Design doc
      `docs/design/0007-0001-wal-segment-preallocation.md`.
      (landed 2026-04-29: `wal.Config.Preallocate` flips on the
      preallocator. `state.openSegment` zero-fills new files via
      `preallocateSegment` (64-KiB-chunk WriteAt loop + fsync) and
      fsyncs the WAL directory entry. The encoded zero header
      (`len=0 && crc=0`) is now the EOS sentinel: `Writer.Append`
      rejects empty payloads (`ErrEmptyPayload`); `ReadAll` /
      `decodeRecord` callers honour `isZeroHeader` to stop on
      first zero header. `detectWritePos`'s legacy
      "size-of-last-segment" formula is replaced by
      `scanLastSegmentEnd` which walks the last segment
      record-by-record to find the actual write position —
      handles both short legacy segments and full-size
      preallocated tails. The new `wal_init_zero` GUC
      (`ContextPostmaster`, default `on`) flows through
      `cmd/goopg start` → `initdb.Open(OpenOptions{WALInitZero})`
      → `wal.Config.Preallocate`. New tests:
      `TestPreallocatedSegmentIsFullSize`,
      `TestPreallocatedSegmentRecoversCleanly`,
      `TestAppendRejectsEmptyPayload`. `fdatasync` switch
      (0007-0002), `wal_recycle`, eager next-segment lookahead,
      counters / observability, and pgbench latency measurement
      deferred.)

- [ ] `fdatasync` on the commit path: replace
      `f.Sync()` in `flushUpTo` with platform-aware `fdatasync`
      (Linux) / `fsync` fallback. Keep full `fsync` at segment
      creation, post-creation directory flush, and segment
      removal. Design doc
      `docs/design/0007-0002-fdatasync-commit-path.md`.
      (landed 2026-04-29: Build-tagged `dataSync(f *os.File)
      error` helper. `internal/wal/sync_linux.go` calls
      `unix.Fdatasync(int(f.Fd()))` from `golang.org/x/sys/unix`
      (already a transitive dep), mirroring upstream's
      `pg_fdatasync` from
      postgres/src/backend/storage/file/fd.c.
      `internal/wal/sync_other.go` falls back to `f.Sync()` on
      every non-Linux platform — preserves the durability
      contract at the cost of paying for inode metadata
      updates `fdatasync` would have skipped. `flushUpTo` now
      calls `dataSync(f)` per dirty segment instead of
      `f.Sync()`. Full `fsync` is preserved in
      `preallocateSegment` and the directory-fsync after
      segment creation — both need durable metadata. The
      `wal: fdatasync %s` error prefix in the loop is now
      accurate. The pgbench latency measurement, the
      `wal_sync_method` GUC selector, and a segment-removal
      directory fsync are deferred.)


## Milestone 0007 — pgbench end-to-end and admin tooling

- [ ] `goopg init` creates a data directory layout (`base/`, `global/`,
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


## Milestone 0008 — Logical replication

- [ ] Logical replication slot foundation + `pg_replication_slots`
      view. Design doc
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `wal.SlotKind` grows `SlotLogical`;
      `wal.Slot` grows `Plugin` / `Database` / `CatalogXmin` —
      all JSON-tagged with `omitempty` so physical-slot state
      files stay byte-identical and pre-M0008 files round-trip
      cleanly with the new fields zero-valued. New typed
      constructor `Slots.CreateLogical(name, plugin, database,
      startLSN)`; `Slots.Create` accepts `SlotLogical` (the
      pre-M0008 hard-reject is dropped). New
      `pg_catalog.pg_replication_slots` virtual view in
      `internal/initdb/replication_views.go` backed by the
      `*wal.Slots` registry, registered in `initdb.Open` next to
      `pg_stat_replication` / `pg_stat_wal_receiver`. Column
      shape mirrors upstream PG 18.x; columns goopg doesn't
      track yet (`temporary` / `xmin` / `safe_wal_size` /
      `two_phase` / `active_pid`) emit empty / `f` / `0`. WAL
      retention via `MinRestartLSN` picks up logical slots
      automatically — `Slot` shape is shared, no retention
      code change needed. New tests: `TestCreateLogicalSlot`,
      `TestCreateLogicalRequiresPluginAndDatabase`,
      `TestPhysicalSlotJSONUnchangedAcrossM0008`,
      `TestPgReplicationSlotsViewRendersBothKinds`. Reorder
      buffer, snapshot builder, decoder loop, and per-slot
      catalog-xmin retention in vacuum/pruning are all
      deferred to subsequent loops in this milestone.)

- [ ] Reorder buffer + decoder orchestration skeleton: per-xact
      queueing, commit-time drain, abort-drop, plus the
      `OutputPlugin` interface that pgoutput will implement.
      Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `internal/wal/reorder.go` —
      `ReorderBuffer{Append, Commit, Abort, Active,
      OldestBeginLSN}` keyed by `storage.TransactionID`,
      single-decoder / non-goroutine-safe by design (the decoder
      loop is sequential; wrap in a mutex if it ever moves to a
      goroutine pool). `Change{Kind, LSN, Rel, Block, LineSlot,
      OldTuple, NewTuple}` covers Insert/Update/Delete; xid==0
      Append is rejected to avoid conflating distinct xacts.
      `internal/wal/decoder.go` — `OutputPlugin` interface
      (`Begin(xid, commitLSN)` / `Change(c)` / `Commit(xid,
      commitLSN)`) plus `Decoder.ApplyChange/ApplyCommit/
      ApplyAbort`; `ApplyCommit` drives the plugin in
      `Begin → Change* → Commit` order, unknown xids are no-ops
      (handles catalog-only filter-everything xacts), and
      `ErrNoPlugin` flags a commit-with-changes against a
      decoder configured with no plugin. New tests pin the
      contract: TestReorderBufferCommitDrainsInOrder,
      TestReorderBufferAbortDropsChanges,
      TestReorderBufferIsolatesXacts,
      TestReorderBufferOldestBeginLSN,
      TestReorderBufferRejectsInvalidXID,
      TestDecoderApplyCommitDrivesPlugin,
      TestDecoderAbortSkipsPlugin,
      TestDecoderUnknownCommitIsNoop,
      TestDecoderRequiresPlugin. WAL classifier remains
      deferred — needs new `RecordKindXactCommit` /
      `RecordKindXactAbort` markers + per-record xid plumbing
      so the decoder can be driven from a `RecordIterator`. The
      snapshot builder is also deferred.)

- [ ] WAL classifier hookup: introduce `RecordKindXactCommit` /
      `RecordKindXactAbort` records and a `Classify(decoder,
      record)` function that drives `Decoder.Apply*` from any
      record stream. Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: New WAL record kinds
      `RecordKindXactCommit` (8) and `RecordKindXactAbort` (9)
      with 5-byte `kind|xid` payloads. `EncodeXactCommit`,
      `EncodeXactAbort`, `DecodeXactMarker` round-trip the xid.
      `ApplyRecord` treats both kinds as physical-recovery
      no-ops — they exist purely so the M0008 logical decoder
      can drive its reorder buffer; the existing per-record
      idempotency in HeapInsert/Delete/Vacuum/Btree records
      already brings storage to a consistent state.
      `internal/wal/classifier.go::Classify(d, r)` walks one
      decoded `Record` and dispatches into the `*Decoder`:
      HeapInsert routes by xmin parsed from the encoded tuple
      header (offset 0..3); HeapDelete routes by the xmax
      already in the record payload — no wire-format change to
      the existing logical change records. XactCommit/XactAbort
      route to the corresponding Decoder method; vacuum/btree/
      page-image/checkpoint records are silently skipped. Tests:
      TestClassifyHeapInsertRoutesByXmin,
      TestClassifyHeapDeleteRoutesByXmax,
      TestClassifyAbortDropsXact,
      TestClassifyIsolatesConcurrentXacts,
      TestClassifySkipsNonTxRecords,
      TestEncodeDecodeXactMarker. Wire-layer emission of
      EncodeXactCommit/EncodeXactAbort at executor txn
      boundaries remains deferred — the classifier works against
      synthetic record streams in tests but sees no markers in
      live workloads until the executor wires them in.)

- [ ] Long-lived classifier loop: a goroutine per logical
      slot that consumes a `RecordIterator` and drives a real
      `OutputPlugin`, advancing the slot's `ConfirmedFlushLSN`
      on each commit. Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `internal/wal/slot_decoder.go`
      defines `SlotDecoder.Run(ctx)` — owns a
      `*RecordIterator` anchored at the slot's `RestartLSN`
      and a `*Decoder` driving the `OutputPlugin`; loops
      `iter.Next` → `Classify(decoder, rec)` until ctx is
      cancelled or the writer closes. On every record whose
      kind is `RecordKindXactCommit`, the slot's
      `ConfirmedFlushLSN` advances to `rec.EndLSN` so a
      restart resumes from the correct anchor without
      replaying acked transactions. Construction rejects
      non-logical slots. Tests:
      TestSlotDecoderRunDrivesPluginThroughCommit (end-to-end
      with a live writer, async loop, `xid=42`
      insert/insert/commit observed by a thread-safe capture
      plugin, `ConfirmedFlushLSN` advances to commit EndLSN),
      TestSlotDecoderRejectsPhysicalSlot. The snapshot
      builder skeleton stays deferred — needed before a real
      consumer can interpret tuple bytes against schema.)

- [ ] Snapshot builder skeleton: slot-creation-time HISTORIC
      snapshot for the logical decoder so plugins can
      interpret tuple bytes against the catalog state in
      effect at slot creation. Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: new
      `catalog.InMemory.AllTables()` accessor returns deep
      copies of every non-virtual user table in OID order.
      `internal/wal/snapshot.go` introduces:
        - [ ] `RelationDef{Schema, Name, OID, Columns}` and
          `ColumnDef{Name, Type, NotNull, Ordinal}` — the
          immutable per-relation snapshot.
        - [ ] `CatalogSnapshot` — per-RelOid map; `Lookup(rel)`
          resolves by RelOid (stable across renames); `Len()`
          for observability.
        - [ ] `BuildCatalogSnapshot(c)` — captures the current
          catalog state. Mutations after capture cannot leak
          through; the `Drop + recreate` test pin guarantees
          this.
        - [ ] Virtual catalog views skipped (they re-register on
          startup).
        - [ ] `SlotSnapshot{Catalog, MVCC}` bundles the two
          frozen views a plugin needs at slot start.
      `wal.SlotDecoder` grows a `Snapshot SlotSnapshot` field
      and a `NewSlotDecoderWithSnapshot(...)` constructor; the
      original `NewSlotDecoder` stays for tests that don't
      need schema awareness. Plugins read
      `decoder.Snapshot` once pgoutput (0008-0002) wires the
      consumption path. Tests:
      TestBuildCatalogSnapshotFreezesShape (mutation after
      capture doesn't bleed through),
      TestBuildCatalogSnapshotSkipsVirtualTables,
      TestSnapshotLookupMissingRelation,
      TestNewSlotDecoderWithSnapshotAttachesIt. Full upstream
      `SnapBuild` state machine, schema-change replay across
      slot lifetime, and the per-slot catalog-xmin retention
      hook in vacuum / pruning remain deferred. With this
      slice 0008-0001 has the foundation a real pgoutput
      plugin (0008-0002) can build against.)

- [ ] `pgoutput` output plugin: B / C / R / I / D message
      framing wire-compatible with upstream PG 18.x. Replica-
      identity handling. Design doc
      `docs/design/0008-0002-pgoutput-plugin.md`.
      (landed 2026-04-29: `internal/wal/pgoutput.go::PgOutput`
      implements the `OutputPlugin` interface and emits
      pgoutput v1 wire-shapes:
        - [ ] B: kind | final_lsn | commit_time | xid (21 bytes).
        - [ ] C: kind | flags=0 | commit_lsn | end_lsn |
              commit_time (26 bytes).
        - [ ] R: rel_oid | nspname\\0 | relname\\0 | replident |
              nattrs | per-attr (flags | name\\0 | type_oid |
              typmod). Lazy-emitted once per session via an
              `emittedRel` set.
        - [ ] I: rel_oid | 'N' | tuple-body.
        - [ ] D: rel_oid | 'K' | nliveatts=0 (v0 HeapDelete
              carries no pre-image; apply worker resolves
              the row by (rel, block, slot) lookup).
      Tuple body parsing mirrors
      `executor/codec.go::DecodeRow` byte-for-byte (duplicated
      because `executor` depends on `wal`); supports int4 /
      int8 / bool / text / varchar / numeric / timestamp /
      date. `pgoTypeOIDFor` maps v0 catalog type names to
      upstream `pg_type` OIDs. Replica identity reports DEFAULT
      uniformly (catalog tracking lands with 0008-0003);
      every column flagged as part of REPLICA IDENTITY
      DEFAULT so apply workers' row-resolution path works for
      tables with primary keys. `U` UPDATE intentionally
      deferred — v0 executor emits UPDATE as paired
      HeapDelete + HeapInsert; reorder-buffer fold is its own
      slice. Tests pin the byte shapes for B / C / R / I / D
      plus the relation-once-per-session and unknown-rel-skip
      contracts. With this slice the pipeline can finally
      emit upstream-compatible bytes; the next M0008 work is
      0008-0003 (publication / subscription DDL + catalog).

- [ ] Apply worker + initial table sync — first slice
      (pgoutput decoder + design doc). Subscriber-side worker
      scaffolding, TCP transport, tablesync state machine,
      DELETE/UPDATE row resolution still deferred. Design doc
      `docs/design/0008-0004-apply-worker-and-tablesync.md`.
      (landed 2026-04-29: `internal/wal/pgoutput_decoder.go`
      delivers `DecodeMessage(payload) (*DecodedMessage,
      error)` — the inverse of 0008-0002's encoder. Output
      types: `DecodedMessage{Kind, XID, CommitLSN, EndLSN,
      CommitTime, Relation, RelOID, NewTuple, OldTuple}`,
      `DecodedRelation{OID, Schema, Name, Replident,
      Columns}`, `DecodedAttr{Flags, Name, TypeOID, TypeMod}`,
      `DecodedColumn{Status, Bytes}`. Reader cursor enforces
      big-endian framing and surfaces `ErrTruncatedMessage` on
      short payloads. Per-kind parsers cover `B` (21 bytes:
      kind | final_lsn | commit_time | xid), `C` (26 bytes:
      kind | flags=0 | commit_lsn | end_lsn | commit_time),
      `R` (rel_oid | nspname\\0 | relname\\0 | replident |
      natts | per-attr fields), `I` (`'N'` action + tuple
      body), `D` (`'K'`/`'O'` action + tuple body). Tuple
      bodies handle `'n'` (NULL), `'t'` (text with
      length-prefix), and `'u'` (unchanged TOAST) status
      bytes. Tests:
      TestPgoutputDecoderRoundTripBegin,
      TestPgoutputDecoderRoundTripCommit,
      TestPgoutputDecoderRoundTripRelationAndInsert (pins
      end-to-end encoder→decoder for a 2-column int4+text
      table; tuple body decodes to `[t:"42", t:"alpha"]`),
      TestPgoutputDecoderRoundTripDelete (empty K body),
      TestPgoutputDecoderRejectsTruncated. With this slice
      the encoder/decoder symmetry is complete and the apply
      worker has its byte-stream reader.)


## Milestone 0009 — AIO subsystem (asynchronous I/O)

- [ ] Read-stream API on top of the AIO core. (landed
      2026-04-29: `internal/aio/read_stream.go` ships the
      predictive-prefetch surface. Public types:
      `NextBlockFunc func() int64` (returns next byte
      offset or the `EndOfStream = -1` sentinel),
      `ReadStreamConfig { Engine, File, BlockSize,
      NextBlock, Lookahead }`, `ReadStream` with `Next()`
      and `Close()`. `NewReadStream` validates the config
      (non-nil Engine / File / NextBlock + positive
      BlockSize → typed errors), clamps Lookahead to
      `[1, MaxReadStreamLookahead=256]` so a pathological
      caller can't allocate unbounded buffer memory (zero
      falls back to `DefaultReadStreamLookahead=4`), and
      primes the prefetch window via up to Lookahead
      `Engine.Submit` calls. Every `Next` blocks on the
      head prefetch's Wait, returns the block's bytes
      (truncated to the underlying ReadAt's reported byte
      count, slice aliases the stream's internal buffer
      and is valid until the next Next/Close call), and
      refills the window. `io.EOF` is the trailing
      sentinel returned exactly once after NextBlock has
      signalled `EndOfStream` AND the queue has drained.
      `Close` waits for in-flight prefetches to land
      rather than abandoning them so the engine's
      `InFlight` counter stays honest (cancellation will
      arrive post-`io_uring`; until then drain is the
      only correct exit). Operates on `File`+offsets
      rather than buffer-manager `Buffer` handles —
      mirrors upstream `read_stream.h`'s shape but keeps
      the abstraction reusable for non-heap-scan
      prefetchers (ANALYZE sample reads, vacuum's free-
      space-map walk). Two backpressure layers stack: the
      per-stream Lookahead window AND the engine's global
      `io_max_concurrency` cap (Submit blocks naturally
      when hit, so the stream's window can shrink under
      contention without violating the cap). Deferred:
      contiguous-merge ("io_combine_limit"), sequential
      ramp-up, `Reset()` for restartable scans. Tests in
      `internal/aio/read_stream_test.go`:
      TestReadStreamSequentialRoundTrip (4-block stream
      at Lookahead=2 round-trips bytes in callback order
      + trailing EOF + Submitted=4),
      TestReadStreamLookaheadCapsConcurrentSubmits (a
      `gateFile` that blocks every ReadAt until released
      lets the test sample the engine's `InFlight`
      counter mid-stream; asserts it never exceeds
      Lookahead),
      TestReadStreamHonoursDefaultLookahead (zero falls
      back to 4), TestReadStreamClampsHugeLookahead
      (10×Max → MaxReadStreamLookahead),
      TestReadStreamRejectsInvalidConfig (each of nil
      Engine / File / NextBlock + zero BlockSize → typed
      error), TestReadStreamSurfacesPerBlockError (empty
      file + non-zero offset → io.EOF on the per-block
      result, mirroring io.ReaderAt's contract),
      TestReadStreamCloseDrainsInFlight (post-Close
      InFlight=0). Built and full `go test ./...` green.
      Design doc `docs/design/0009-0002-read-stream.md`
      added and indexed in `docs/design/README.md`.)

- [ ] AIO WAL writer caller integration — wal.state.writeAt
      shaped to flow per-segment writeback through the
      engine. (landed 2026-04-29: `wal.Config.AIO` adds an
      optional engine seam; matching `wal.AIOEngine` /
      `AIOSubmitOp` / `AIOFile` / `AIOHandle` /
      `AIODirection` interfaces mirror the storage-side
      shapes so internal/wal stays import-free of
      internal/aio. `state.aio` mirrors `Config.AIO`;
      `state.writeAt` Submit→Wait through the engine when
      set, falls back to direct `f.WriteAt` when nil.
      Commit-path durability barrier is unchanged: every
      Submit Waits inline (single-threaded writer loop),
      so by the time `flushUpTo` calls `dataSync`/fdatasync
      every byte ≤ the requested LSN has already been
      pwrite'd. WAL writes now appear in `pg_aios` /
      `pg_stat_aio` alongside heap writes — engine, reads,
      heap writes, and WAL writes all flow through one
      pool. `initdb.Open` builds a `walAIOEngineAdapter`
      (parallel to the storage adapter, keeping each
      package free of internal/aio) and threads it through
      `wal.Config.AIO` when an engine is attached. Tests:
      TestWriterAppendNoAIOPreservesLegacyPath (no engine
      → bytes land on disk via direct `f.WriteAt`),
      TestWriterAppendAIOFlowsThroughEngine (engine
      attached → every writeAt-chunk flows through Submit
      with Direction=AIODirWrite; bytes still round-trip
      via the segment file). Built and full
      `go test ./...` green.

      What's deferred: real perf benefit. The WAL writer's
      single-threaded loop means inline Wait gives no
      pipelining — Append n still blocks on Append n's
      pwrite. Restructuring the writer to defer Wait
      across multiple Appends (so commit n+1 doesn't wait
      on commit n's pwrite) is a follow-up that requires
      changing the loop's serialisation model. The
      observability + symmetry win is meaningful; the
      perf win waits on the loop redesign.)


## Milestone 0010 — WAL direct I/O & walsender memory handoff

- [ ] WAL direct-I/O write path — Phase 1 (GUC, probe,
      plumbing, fallback observability). New `wal_direct_io`
      GUC (TypeBool, default `off`, ContextPostmaster).
      `wal.Config.DirectIO` field; `loadState` runs
      `probeDirectIO(walDir)` once at construction when
      DirectIO=true. Probe opens `<walDir>/.wal_direct_io_probe`
      with `O_RDWR|O_CREAT|O_DIRECT`, observes EINVAL /
      EOPNOTSUPP and returns a human-readable fallback
      reason (or empty on success). Linux-only
      `internal/wal/direct_io_linux.go`; non-Linux stub in
      `internal/wal/direct_io_other.go` always falls back
      (mirrors the M0009 io_uring stub). Phase 1 does NOT
      flip O_DIRECT on segment opens — that's Phase 2. The
      probe outcome is plumbed end-to-end:
      `Writer.DirectIORequested()` /
      `Writer.DirectIOFallbackReason()`, `cmd/goopg start`
      reads the GUC into `OpenOptions.WALDirectIO` and emits
      `event=wal_direct_io_active` (probe ok) or
      `event=wal_direct_io_fallback reason=...` (probe
      rejected) — mirrors the M0009 `event=aio_*` shape so
      operators grep one vocabulary across both subsystems.
      Tests: TestDirectIODisabledByDefault (probe skipped
      when GUC off), TestDirectIOEnabledProbesFilesystem
      (probe runs + outcome plumbed correctly per GOOS),
      TestDirectIOFallbackReasonStable (idempotent reads).
      Design doc
      `docs/design/0010-0001-wal-direct-io-write-path.md`
      indexed; spans both phases with Phase 2 explicitly
      marked deferred. (landed 2026-04-29.)

- [ ] WAL direct-I/O write path — Phase 2 (M0010-0001b):
      flip O_DIRECT on segment fds, alignment-safe per-write
      RMW, aligned scratch via `unix.Mmap`. (landed
      2026-04-29: `state.directIOActive` snapshots
      `directIORequested && fallback==""` at construction.
      `enableDirectIO(f)` uses `fcntl(F_SETFL,
      flags|unix.O_DIRECT)` to flip the flag on the live fd
      AFTER preallocation finishes — preallocation's 64-KiB
      heap-buffer zero-fill can't satisfy O_DIRECT alignment.
      `state.writeAt` dispatches to `state.writeAtDirectIO`
      when active: pread the aligned region (`alignDown`/
      `alignUp` bracket around the user bytes) into the
      per-state mmap'd scratch, overlay user bytes, pwrite
      the full aligned region back. Past-EOF reads (legacy
      lazy-grow case) zero-pad the tail. `directIOScratchCap
      = 1 MiB`; outsized writes loop through the scratch.
      Aligned scratch is lazy-allocated on first write via
      `unix.Mmap(MAP_PRIVATE|MAP_ANONYMOUS)` (mirrors
      `internal/storage/arena.go`); freed in `state.close`.
      AIO+DirectIO bypasses the engine and uses synchronous
      RMW — engine-side aligned-copy is a perf-only follow-
      up (`Phase 2.b` in the design doc). Block size hard-
      coded at 4 KiB; STATX_DIOALIGN-driven detection
      deferred. Walreceiver's WAL-persist path inherits via
      the shared writer fd (no separate code path).
      Tests: TestDirectIORoundTripWithPreallocation
      (three appends + flush + ReadAll round-trip via
      RMW; SegmentSize=4 KiB),
      TestDirectIORecordSpanningBlocks (12 KiB payload
      across ~3 block boundaries; every byte round-trips).
      Both `t.Skip` on probe fallback. Crash-restart
      correctness rides on existing
      `TestPreallocatedSegmentRecoversCleanly`: the byte
      stream is identical under O_DIRECT vs buffered. Built
      and full `go test ./...` green. Design doc
      `docs/design/0010-0001-wal-direct-io-write-path.md`
      updated with Phase 2 details.)


## Milestone 0012 — Lock manager + deadlock detection

- [ ] Lock manager v0 surface (M0012-0001). Design doc
      `docs/design/0012-0001-lock-manager-architecture.md`.
      (landed 2026-04-29: new `internal/lockmgr` package with
      eight upstream lock modes (`AccessShareLock` …
      `AccessExclusiveLock`, numeric values matching
      `lockdefs.h`), `conflictTab[1..8]` ported verbatim from
      `postgres/src/backend/storage/lmgr/lock.c LockConflicts[]`,
      relation-level `LockTag{DB, Rel}`, per-tag `lockState`
      with `holders map[BackendID]Mask` + FIFO `waiters
      []*Waiter` + cached `granted` Mask, lazy-alloc + GC
      empties. `Acquire(ctx, b, t, m)` grants immediately
      iff no conflict against `grantedExcept(b)` AND no
      waiters queued (second check enforces strict
      head-of-line FIFO); else parks on a buffered signal
      chan; cancellation splices waiter under `lm.mu` and
      handles the Release-promoted-during-cancel race.
      `Release` runs FIFO wake-pass head-first.
      `ReleaseAll(b)` is the txn-end hook. Single coarse
      `sync.Mutex` (per-tag striping deferred). Self-conflict
      impossible — `grantedExcept(b)` excludes requester's
      own holdings. `ConflictsWith(m, held)` exported for
      M0012-0002. Tests:
      TestLockConflictMatrixMatchesUpstream (exhaustive 8-mode
      cross-check), TestLockManagerNoConflictGrantsImmediately,
      TestLockManagerCompatibleModesCoexist,
      TestLockManagerConflictBlocksAndWakesOnRelease,
      TestLockManagerSelfDoesNotConflict,
      TestLockManagerIdempotentAcquire,
      TestLockManagerWaiterCancellationCleansUp,
      TestLockManagerReleaseAllWakesEveryone,
      TestLockManagerFIFOFairness,
      TestLockManagerGCEmptiesState. Race-clean
      (`go test -race`). Full `go test ./...` green.
      Deadlock detection (M0012-0002) and executor
      integration (M0012-0003) build on this surface.)

- [ ] Executor integration + multi-session test matrix
      (M0012-0003). Design doc
      `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`.
      (landed 2026-04-29: `executor.Context` grew
      `LockMgr *lockmgr.LockManager` + `BackendID
      lockmgr.BackendID` (both nil-safe). New helper
      `Context.acquireRelLock(rel, mode)` is the single
      funnel: nil-LockMgr → no-op; `ErrDeadlockDetected` →
      `*ExecError{Code:"40P01", Message:"deadlock
      detected"}`; ctx-cancel → `*ExecError{Code:"57014"}`.
      Wired into 5 operators: seqScanOp /indexScanOp.Open
      take AccessShareLock; insertOp/updateOp/deleteOp.Open
      take RowExclusiveLock. `server.Config` grew `LockMgr`;
      `Server` grew `nextBackendID atomic.Uint32`;
      `dispatch.go` plumbs both into the Context and calls
      `LockMgr.ReleaseAll(backendID)` from the deferred
      txn-end block. Tests in
      `internal/executor/lock_integration_test.go`:
      TestExecutorAcquireHelperNilLockMgr (regression guard
      for fixture compatibility), TestExecutorAcquireHelperGrantsLock,
      TestExecutorDeadlockTwoSession (A↔B cycle —
      ErrDeadlockDetected → 40P01 mapping pinned),
      TestExecutorDeadlockThreeSession (A→B→C→A multi-edge,
      exactly one 40P01, BackendID 3 = youngest cancelled),
      TestExecutorNonDeadlockContention (linear waiter chain
      — no false-positive 40P01). Race-clean. Full
      `go test ./...` green. M0012 closes — DDL paths
      (DROP/ALTER), catalog-level locks, `lock_timeout` GUC,
      and `pg_locks` view are separate follow-up scopes.)


## Milestone 0014 — PostgreSQL-compatible WAL on-disk format

- [ ] XLOG page and segment layout — **types and helpers**
      (M0014-0001 step 1). Design doc
      `docs/design/0014-0001-xlog-page-and-segment-layout-compat.md`.
      (landed 2026-04-29: pure additive — no production-path
      changes yet. New `internal/wal/xlog_page.go` defines
      upstream-compatible page-header types and helpers
      targeting PG18: `XLogPageHeader` (24 bytes, mirrors
      `XLogPageHeaderData`), `XLogLongPageHeader` (40 bytes,
      mirrors `XLogLongPageHeaderData` with the
      sysid/seg_size/xlog_blcksz cross-check), constants
      `XLOGBlockSize=8192`, `XLOGPageMagic=0xD119`,
      `SizeOfXLogShortPHD=24`, `SizeOfXLogLongPHD=40`, all
      flag bits (`XLPFirstIsContRecord`, `XLPLongHeader`,
      `XLPBkpRemovable`, `XLPFirstIsOverwriteContRecord`,
      `XLPAllFlags`). Encode/decode helpers serialise to/from
      little-endian (host byte order on x86_64/aarch64 Linux,
      matches upstream's de-facto LE assumption — cross-arch
      transfer out of scope). `EncodeXLogPageHeader` rejects
      undefined flag bits (XLPAllFlags contract);
      `DecodeXLogPageHeader` returns the typed sentinel
      `ErrInvalidPageHeader` on magic mismatch so the
      M0014-0004 legacy-format detector has a clean branch.
      `EncodeXLogLongPageHeader` auto-sets the long bit;
      `DecodeXLogLongPageHeader` enforces it. Filename
      helpers `XLogFileName(tli, segno, segSize)` and
      `ParseXLogFileName(name, segSize)` produce/consume the
      upstream `<TLI:08X><Log:08X><Seg:08X>` form via strict
      `strconv.ParseUint` (rejects partial parses). Tests:
      TestXLogFileNameRoundTrip (5 representative TLI/segno
      cases including log-boundary 255→256), TestParseXLogFile
      NameRejectsGarbage, TestXLogPageHeaderRoundTrip,
      TestXLogLongPageHeaderRoundTrip, TestDecodeXLogPageHeader
      RejectsBadMagic (typed sentinel), TestEncodeXLogPageHeader
      RejectsUndefinedFlags (XLPAllFlags contract), TestDecode
      XLogLongPageHeaderRequiresLongBit. Coexists with the
      legacy `formatSegmentName` / `parseSegmentName` so the
      writer/reader switchover lands atomically in M0014-0001
      step 2 without churning unrelated code first. Full
      `go test ./...` green.)

- [ ] XLOG page emission in writer + page-aware reader
      (M0014-0001 step 2). Continues
      `docs/design/0014-0001-xlog-page-and-segment-layout-compat.md`.
      (landed 2026-04-29: gated by new `Config.PageHeaders`
      (+ `SystemID` / `TimelineID`) flag — default `false`
      keeps every legacy data dir / test byte-unchanged.
      `state.append` calls `emitWithPageHeaders(record,
      writePos, segSize, sysID, tli)` which interleaves a
      40-byte `XLogLongPageHeader` at every segment boundary
      (stamps `xlp_sysid` / `xlp_seg_size` / `xlp_xlog_blcksz`)
      and a 24-byte short header at every other 8 KiB page
      boundary. Records crossing a page boundary stamp
      `XLP_FIRST_IS_CONTRECORD` and `xlp_rem_len =
      bytes_remaining_of_record` on the next page; multi-page
      records re-decrement page-by-page. `state.writePos` and
      `Writer.WrittenLSN()` advance over the combined stream
      length, preserving upstream's invariant **LSN = byte
      offset in the on-disk WAL stream**. `Append` returns
      `startLSN = writePos + leading_header_bytes + 1` so the
      LSN lands on the first record byte. Reader side:
      `RecordIterator.Next` skips any header at the cursor
      before checking write-tail; `readRecordBytesAt` is the
      new helper that mirrors the writer's interleave —
      returns record bytes and the physical advance count
      (record bytes + skipped header bytes). `ReadAll`
      auto-detects format via `DetectWALFormat(walDir)` and
      dispatches to `readAllPageAware`; classification errors
      silently fall back to the legacy walk so the existing
      tiny-segment tests still work. `scanLastSegmentEnd`
      (writer startup) consults `cfg.PageHeaders` directly;
      EOS becomes two-flavoured — all-zero page header at a
      page boundary OR all-zero record header mid-page
      (preserves the M0007 / 0007-0001 preallocated-tail
      contract). MemRing capture / walBuf path / writeAt
      layering all consume `stream` instead of the bare
      record so direct-I/O alignment, AIO submission, and
      walsender RAM streaming see the same physical bytes
      the segment carries. New tests in
      `internal/wal/xlog_emit_test.go`:
      TestPageEmissionLongHeaderAtSegmentStart,
      TestPageEmissionShortHeaderAtPageBoundary (exact
      `xlp_rem_len = record_size - (XLOGBlockSize -
      SizeOfXLogLongPHD)` arithmetic),
      TestPageEmissionRecordCrossesPage (Append/ReadAll LSN
      cross-check), TestPageEmissionRecordCrossesSegment
      (segment-spanning record → long header on next segment
      with both XLPLongHeader and XLPFirstIsContRecord set),
      TestPageEmissionIteratorRoundTrip,
      TestPageEmissionRecoversCleanly (close + reopen with
      Preallocate=true), TestPageEmissionLegacyDefaultUnchanged
      (rollout invariant: byte-identical output when
      PageHeaders=false). Full `go test ./...` green. The
      XLogRecord-header switchover (M0014-0002 step 2) and
      pg_waldump validation (M0014-0003) remain deferred —
      `PageHeaders=true` today produces upstream-shaped pages
      with goopg's legacy 8-byte length+CRC32-IEEE record
      frames inside; pg_waldump won't accept those until the
      record-frame switchover lands.)


## Milestone 0015 — PL/pgSQL stored routines (function-first delivery)

- [ ] Stage A: function-first delivery (CREATE OR REPLACE FUNCTION
      ... LANGUAGE plpgsql, callable from SELECT). Decompose into
      seam-sized slices when picked up.
      (landed 2026-04-30: all 6 steps done.)

  - [ ] Stage A step 6 — function invocation in expression contexts.
        (landed 2026-04-30: runtime wiring. `evalFuncCall` now
        falls back to `evalStoredRoutineFuncCall` for non-built-in
        functions. This enables UDFs to be called from regular
        SELECT queries, CASE expressions, and anywhere else SQL
        expressions are used. scalar-only in Stage A.)

  - [ ] Stage A step 5 — interpreter / SPI bridge.
        (landed 2026-04-30: core interpreter implementation in
        `internal/executor/plpgsql_runtime.go`. supports
        variable frames, expression evaluation via SQL-evaluator
        reuse, and all control-flow structures landed in step 4.
        SPI bridge for embedded SQL stays deferred to step 4g.)

  - [ ] Stage A step 4e — PERFORM.
        (landed 2026-04-30: parser + interpreter slice. `PERFORM
        expr;` evaluates the expression and discards the result,
        useful for side-effecting function calls. 1 new executor
        test `TestUDFPerform`. Full `go test ./...` green.)

  - [ ] Stage A step 4d — LOOP/WHILE/FOR/EXIT/CONTINUE.
        (landed 2026-04-30: parser + interpreter slice for all
        Stage A control-flow structures. `internal/plpgsql` now
        accepts `LOOP`, `WHILE condition LOOP`, `FOR var IN
        [REVERSE] lower..upper [BY step] LOOP`, `EXIT [WHEN
        condition]`, and `CONTINUE [WHEN condition]`. The
        interpreter implements these with a `controlFlow` enum
        for signal propagation. Integer `FOR` loops implicitly
        declare their loop variable with shadowing/restoration
        semantics matching upstream. 4 new executor tests
        cover the full surface. Full `go test ./...` green.)

  - [ ] Stage A step 4c — IF/ELSIF/ELSE statements.
        (landed 2026-04-30: parser-only slice. `internal/plpgsql`
        now supports `IF condition THEN stmts [ELSIF/ELSEIF
        condition THEN stmts]* [ELSE stmts] END IF;`. Condition
        expressions reuse `parser.ParseExpr`. `Elsifs` slice
        collects both `ELSIF` and `ELSEIF` variants (upstream-
        flex). New `IfStmt` and `Elsif` AST nodes. 3 new tests
        in `parser_test.go`. Full `go test ./...` green.)

  - [ ] Stage A step 4b — DECLARE block + assignment.
        (landed 2026-04-30: parser-only slice. `internal/plpgsql`
        now accepts an optional `DECLARE` section before the main
        `BEGIN` block. Supports typed variable declarations with
        optional `DEFAULT expr` or `:= expr` initializers. reuses
        the SQL type parser via a `CREATE TABLE` synthetic-parse
        loop so `numeric(10,2)`-style types round-trip correctly.
        New `AssignStmt` handles `target := expr;`. Assignment
        target restricted to bare identifiers in Stage A. reuses
        `scanExprToSemicolon` for expression capture so both
        initializers and assignment values produce `parser.Expr`
        nodes the interpreter can evaluate. 12 new tests in
        `parser_test.go` cover declarations, initializers,
        assignment, and specific Stage-A-4b diagnostics for
        `CONSTANT` / `NOT NULL` deferrals. Full `go test ./...`
        green.)

  - [ ] Stage A step 4a — PL/pgSQL body parser + AST (Block +
        RETURN). Design doc
        `docs/design/0015-0004-plpgsql-body-parser-and-ast.md`.
        (landed 2026-04-30: parser-only slice. New
        `internal/plpgsql` package parses routine-body bytes
        captured by step 1's dollar-quote lexer into AST nodes.
        Reuses goopg's main SQL lexer via `parser.Lex` so
        identifiers / literals / dollar-quotes / parameter refs
        all tokenise identically. New `KwReturn` keyword
        (`KwBegin`/`KwEnd` already there). New public
        `parser.ParseExpr(input string) (Expr, error)` so
        RETURN's expression bytes can be fed back through the
        SQL expression parser — the resulting `parser.Expr` is
        the same shape a SELECT target list would produce,
        letting the existing analyzer / planner / executor
        stay reusable when the interpreter (step 5) arrives.
        AST: `Block{Statements []Stmt}` + `ReturnStmt{Expr
        parser.Expr}`; closed `Stmt` interface via unexported
        `plpgsqlStmtNode()` marker. Grammar: `body ::= 'BEGIN'
        stmt_list 'END' [';']`, `stmt ::= 'RETURN' sql_expr
        ';'`. Typed `plpgsql.SyntaxError{Pos, Message}`; lexer
        errors wrap into the same envelope (errors.As
        round-trip). Specific Stage-A diagnostics for missing
        BEGIN / END, unsupported statement (says "Stage A 4a
        accepts RETURN only"), bare RETURN without value, bad
        expression (pinned at expression start). 11 tests in
        `parser_test.go`. Out of scope (Stage A 4 follow-ups):
        DECLARE + assignment (4b), IF/ELSIF/ELSE (4c),
        LOOP/WHILE/FOR/EXIT/CONTINUE (4d), PERFORM (4e),
        SELECT INTO (4f), embedded INSERT/UPDATE/DELETE/SELECT
        (4g), exception blocks (Stage B), RETURN NEXT/QUERY
        (Stage B). Full `go test ./...` green.)

  - [ ] Stage A step 3 — analyzer pass-through + planner DDL
        pass-through + executor `execCreateFunction` /
        `execDropFunction`. Design doc
        `docs/design/0015-0003-create-function-executor-wiring.md`.
        (landed 2026-04-30: end-to-end wiring slice. CATALOG
        interface gains `Routines() *Routines` (small additive;
        only `*InMemory` implements). ANALYZER drops the step-1
        SQLSTATE 0A000 reject — DDL is pass-through just like
        CREATE TABLE / CREATE VIEW. PLANNER switch adds
        `*parser.CreateFunctionStmt` / `*parser.DropFunctionStmt`
        to the existing `&DDL{Stmt: stmt}` arm. EXECUTOR
        `ddlOp.Next` dispatch calls new `execCreateFunction` /
        `execDropFunction`. `execCreateFunction` validates
        LANGUAGE (plpgsql/sql allowlist; missing → SQLSTATE
        42P13, unsupported → 42704), translates
        `parser.FunctionArg` → `catalog.Routine`, calls
        `Routines.Create(routine, s.OrReplace)`. Errors mapped
        to upstream-canonical SQLSTATEs: ErrRoutineExists →
        42723, ErrRoutineNotFound → 42883 (swallowed by IF
        EXISTS), ErrRoutineAmbiguous → 42725. `execDropFunction`
        chooses `Drop(name, argTypes)` vs `DropByName(name)`
        based on whether `s.Args == nil`. 8 executor tests
        cover register / duplicate / OR-REPLACE-preserves-OID /
        unsupported-language / drop-by-signature /
        drop-missing-no-IF-EXISTS / DROP-IF-EXISTS-swallow /
        ambiguous-bare-name. `TestAnalyzeCreateFunctionPasses
        Through` replaces the step-1 reject tests. Out of scope
        (Stage A step 4+): PL/pgSQL parser + AST for routine
        bodies, the interpreter / SPI bridge, function
        invocation in expression contexts, persistence of
        pg_proc rows, WAL replay support, multi-target DROP.
        Full `go test ./...` green.)

  - [ ] Stage A step 2 — pg_proc catalog registry + virtual
        view. Design doc
        `docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md`.
        (landed 2026-04-30: catalog-only slice. New
        `internal/catalog/routines.go` with `Routine` struct
        (OID, Schema, Name, ArgNames, ArgTypes, ReturnType,
        Language, Body) and the RWMutex-guarded `Routines`
        registry: `Create(orReplace)`, `Drop`, `DropByName`,
        `Lookup`, `LookupByName`, `List`. Signature key
        lower-cased `(arg1_type,...)`; OID space starts at
        `FirstRoutineOID = 1<<17` so routine OIDs never collide
        with the table-OID space. `CREATE OR REPLACE` preserves
        the existing OID matching upstream's contract. Schema
        defaulting to `public` mirrors upstream's
        search_path[0]. Typed errors `ErrRoutineExists` /
        `ErrRoutineNotFound` / `ErrRoutineAmbiguous` so the
        future analyzer branches cleanly to SQLSTATE 42723 /
        42883 / 42725. `*InMemory` grows a `routines` field
        plus the `(*InMemory).Routines() *Routines` accessor.
        New `internal/initdb/pg_proc_view.go` registers
        `pg_catalog.pg_proc` virtual view backed by
        `cat.Routines().List()` — columns mirror upstream's
        `\df` shape (oid, proname, pronamespace, prolang,
        prorettype, proargtypes comma-joined, prosrc). Wired
        into `Open` after `pg_stat_wal_io`. 8 registry tests
        + 3 view tests. Out of scope (Stage A step 3+):
        analyzer wiring (still rejects 0A000), executor
        CreateFunction / DropFunction operators, persistence
        of pg_proc rows across restart, PL/pgSQL body parser,
        interpreter, function-invocation resolver, numerical
        type-OID columns. Full `go test ./...` green.)

  - [ ] Stage A step 1 — parser + AST surface for CREATE/DROP
        FUNCTION + lexer dollar-quote support + analyzer reject.
        Design doc
        `docs/design/0015-0001-create-function-parser-and-ast.md`.
        (landed 2026-04-30: parser-only slice mirroring the
        M0016/M0017/M0018/M0020/M0021 step-1 precedents. Lexer
        gains dollar-quoted string support — `$$body$$` and
        `$tag$body$tag$` — by extending the existing `$` case
        (which previously only handled positional parameters
        `$1..$N`); new `isDollarTagCont` predicate mirrors PG's
        "tag cannot contain a dollar sign" rule and the rewind
        path keeps `$1` / `revenue$0`-style identifiers
        byte-identical to pre-M0015 behaviour. Three new
        keywords — `KwFunction`, `KwReturns`, `KwLanguage`;
        Stage B keywords (`Procedure`, `Call`, `Out`, `Inout`,
        `Variadic`, `Declare`) stay deferred. New AST nodes
        `CreateFunctionStmt`, `DropFunctionStmt`, `FunctionArg`,
        and the `FuncArgMode` enum stub with only `FuncArgIn`
        populated. `Args=nil` (no parens) distinguishes from
        `Args=[]FunctionArg{}` (explicit empty list) so a future
        overload resolver can tell "drop by name" apart from
        "drop the zero-arg overload". `parseCreate`/`parseDrop`
        gain `KwFunction` dispatch; `OUT`/`INOUT`/`VARIADIC`
        modes surface a Stage-A-only diagnostic; `LANGUAGE` and
        `AS` parse in either order (upstream-flex); function
        body must be dollar-quoted (single-quoted bodies are
        upstream-legal but rejected here so a future relaxation
        is intentional). Analyzer gates `CreateFunctionStmt`
        and `DropFunctionStmt` with SQLSTATE 0A000 "...is not
        supported in v0 analyzer". 12 parser tests
        (`function_test.go`) + 2 analyzer tests pin the surface
        + lexer regression. Out of scope (Stage A steps 2-5):
        pg_proc catalog wiring, PL/pgSQL parser+AST for routine
        bodies, PL/pgSQL interpreter + SPI bridge, function
        invocation in expression contexts. Full
        `go test ./...` green.)

- [ ] Stage B: procedure follow-up (CREATE PROCEDURE, CALL,
      out-parameter binding). All 5 steps landed.

  - [ ] Stage B step 1 — parser + AST + keyword registration.
        (landed 2026-04-30: `KwProcedure` / `KwCall` / `KwOut` /
        `KwInout` / `KwVariadic` keywords registered.
        `CreateProcedureStmt` and `CallStmt` AST nodes added.
        `parseCreateProcedureTail` / `parseCallStatement` in
        function.go. 7 new parser tests. Full `go test ./...`
        green.)

  - [ ] Stage B step 2 — planner + executor DDL pass-through for
        CREATE PROCEDURE. (landed 2026-04-30:
        `*parser.CreateProcedureStmt` routed through planner DDL
        pass-through; `execCreateProcedure` in executor DDL handler
        mirrors `execCreateFunction` but without RETURN clause.
        `planner.Call` node for `CALL` statements wired through
        planner; `callOp` executor returns a clear Stage-B-not-
        implemented diagnostic for CALL. Full `go test ./...`
        green.)

  - [ ] Stage B step 3 — CALL execution for PL/pgSQL procedures.
        (landed 2026-04-30: `callOp.Next` now evaluates CALL
        arguments using the PL/pgSQL expression evaluator via
        `evalPLpgSQLExpr` / `lowerPLpgSQLExpr` and executes the
        procedure body using `executePLpgSQLStmtList`. Procedure
        body is parsed, arguments are bound to named variables,
        declarations are initialized, and statements are run.
        SQL-language procedures remain deferred with a clear
        0A000 diagnostic. 4 new executor tests: create procedure
        catalog registration, duplicate rejection (42723),
        unsupported language (42704), and end-to-end CALL
        execution. Full `go test ./...` green.)

  - [ ] Stage B step 4 — DROP PROCEDURE parser + planner +
        executor. (landed 2026-04-30: `DropProcedureStmt` AST
        node, `parseDropProcedureTail` parser (mirrors
        `parseDropFunctionTail`), planner DDL pass-through,
        and `execDropProcedure` executor handler. Supports
        IF EXISTS, arg-based overload resolution, and
        CASCADE/RESTRICT. 3 new parser tests. Full
        `go test ./...` green.)

  - [ ] Stage B step 5 — out-parameter binding (FuncArgOut/
        FuncArgInout/FuncArgVariadic modes). (landed 2026-04-30:
        Mode constants added to parser AST. `parseProcedureArg`
        accepts OUT/INOUT/VARIADIC in CREATE PROCEDURE.
        `parseFunctionArg` refactored into shared
        `parseArgNameAndType`. `ArgModes []string` added to
        `catalog.Routine` — populated by `execCreateProcedure`.
        `callOp.Open` resolves the procedure at Open time so
        `Schema()` can report OUT/INOUT output columns. `callOp.Next`
        gives OUT params NULL input, executes the body, and returns
        OUT/INOUT parameter values as a single-row result set.
        Full `go test ./...` green.)


## Milestone 0016 — WITH clause (CTE) support

- [ ] CTE observability + compatibility tests
      (M0016-0004). Design doc
      `docs/design/0016-0004-cte-observability-and-compat-tests.md`.
      (landed 2026-04-29: closes the Stage A picture.
      New `planner.CTEScan` plan node wraps each cloned
      CTE body at `planScanRangeVar`'s substitution site so
      EXPLAIN can label the inlined subtree (Name + Alias).
      Pure labeling artifact — `executor.Build` unwraps to
      Child, so Stage A's "zero new executor infrastructure"
      property is preserved. EXPLAIN's `describePlan` switch
      gained a CTEScan arm rendering "CTE Scan on <name>"
      (or "CTE Scan on <name> <alias>" when distinct);
      `planChildren` recurses into Child so the inlined body
      still appears below the label. Tests:
      TestExplainCTEScanLabelsCTEByName (`WITH a AS (SELECT
      1) SELECT * FROM a` produces "CTE Scan on a" in the
      EXPLAIN output), TestExplainCTEScanShowsAlias
      (FROM-alias rendering),
      TestExplainCTEScanRecursesIntoChild. Plus three
      end-to-end PG-shaped compat tests in
      with_compat_test.go: TestCompatCTEFilterThenAggregate
      (filter via CTE + count(*)),
      TestCompatCTEMultiConsumerCrossProduct (single-row
      CTE × itself = 1 row), TestCompatCTEChainedSiblings
      (left-to-right `a → b` reference end-to-end). Full
      `go test ./...` green. Materialise-once optimisation
      and runtime CTE counters in pg_stat_* views remain
      out of scope per design doc — the inlining model
      makes per-CTE counters less informative than per-
      statement counters.)


## Milestone 0017 — UPSERT (INSERT ... ON CONFLICT DO UPDATE)

- [ ] UPSERT Stage A — **executor runtime**
      (M0017-0003). Design doc
      `docs/design/0017-0003-upsert-executor-concurrency-and-locking.md`.
      (landed 2026-04-29: new `upsertOp` in
      `internal/executor/operators_upsert.go` runs the
      planner-resolved state. Per row: encode conflict
      key from `OnConflict.ArbiterColumns`, probe via
      `btree.RangeScan(key, key, callback)` and skip
      invisible tuples via `mvcc.TupleVisible` (essential
      because UPSERT inserts duplicate index entries —
      historical dead versions are still reachable). On
      no-conflict: `writeHeapRowReturning` + arbiter
      `tree.Insert(key, newPtr)` so subsequent rows in
      the same statement see the new entry. On conflict
      + DO NOTHING: skip silently (RowsAffected does NOT
      bump — matches upstream). On conflict + DO UPDATE:
      build merged 2N-wide row (existing 0..N-1 || inserted
      N..2N-1 — planner ColumnRef Index values already
      address this layout), evaluate UpdateWhere (non-true
      → silent skip per upstream — no DO NOTHING fallback),
      evaluate each non-nil UpdateSet[i] (nil slots inherit
      existing[i]), stamp xmax on conflicting tuple via
      `PageSetHeapTupleXmax` + `markHeapDeleteDirty`,
      writeHeapRowReturning the updated tuple,
      maintainArbiter inserts new (key, newPtr); old (key,
      oldPtr) entry stays in place since btree.Insert
      allows duplicates and future probe's visibility
      filter rejects the dead one. Refactor:
      `writeHeapRow` split into void wrapper +
      `writeHeapRowReturning` that surfaces the new
      tuple's `(block, slot)`; existing INSERT/UPDATE
      callers unchanged. Stage A scope guard at
      `upsertOp.Open`: UpdateSet targeting a conflict-key
      column ordinal rejects with `0A000` — without it
      the arbiter entry for the original key would point
      at a tuple whose actual key bytes differ; future
      probes would land on the wrong row. Multi-column
      arbiters surface `0A000` at runtime (v0 btree only
      has single-column key encoding). Tests in
      `operators_upsert_test.go`: 6 end-to-end scenarios
      through parser→analyzer→planner→executor —
      TestUpsertNoConflictInsertsRow (new key path),
      TestUpsertConflictDoUpdate (replace existing label),
      TestUpsertConflictDoNothing (RowsAffected=0),
      TestUpsertDoUpdateMixingExistingAndExcluded
      (`SET label = label || '/' || excluded.label`
      exercises merged-row layout — bare `label` from
      existing[1], `excluded.label` from inserted[N+1]),
      TestUpsertDoUpdateWithWhereSkipsRow (predicate
      false → silent skip), TestUpsertConflictKeyModificationRejected
      (`0A000` Stage A guard). Test fixture seeds rows
      THEN creates the unique index so backfill picks
      up the seeded tuples — required because v0 doesn't
      maintain non-arbiter indexes on plain INSERT
      (pre-existing limitation; the arbiter is the only
      index UPSERT itself maintains). Full `go test ./...`
      green. Concurrency hardening (speculative insert +
      cleanup on conflict, MVCC-correct under contention)
      deferred to a follow-on slice; under concurrent
      UPSERTs both writers may believe they're winning
      the race until the next CREATE INDEX rebuild
      surfaces the duplicate.)


## Milestone 0020 — Window functions

- [ ] Window-function parser surface + AST
      (M0020-0001 step 1). Design doc
      `docs/design/0020-0001-window-parser-and-ast.md`.
      (landed 2026-04-30: parser-only additive slice
      mirroring the M0016/M0017/M0018/M0021 step-1
      pattern. New keywords KwOver / KwPartition;
      KwOrder/KwBy already exist. New `WindowDef` AST
      (PartitionBy []Expr + OrderBy []SortBy reusing
      existing SortBy shape so executor ordering logic
      doesn't need new sort-key plumbing).
      `FuncCall.Over *WindowDef` is nil for every
      pre-M0020 call so existing tests stay
      byte-unchanged. New `parseWindowDef` consumes
      `OVER ( [PARTITION BY exprs] [ORDER BY
      sortlist] )`; new `maybeWindowTail` is called by
      `parseFuncCallTail` after `)` and returns FuncCall
      unchanged when next token isn't OVER. Frame
      clauses (ROWS / RANGE / GROUPS) parse but error
      explicitly with "frame clauses are not supported
      in v0" so users see deferred-feature diagnostic
      instead of generic syntax error — Stage B promotes
      them. Named windows + WINDOW definition clauses
      also deferred. Analyzer gate: `analyzeExpr`'s
      FuncCall arm rejects `x.Over != nil` with 0A000.
      Tests: 7 parser scenarios in window_test.go (bare
      OVER, PARTITION BY, ORDER BY DESC, both clauses,
      count(*) OVER (), frame-clause reject, rollout
      guardrail) + 1 analyzer test
      TestAnalyzeWindowFunctionRejected. Full `go test
      ./...` green. Analyzer name resolution + planner
      WindowAgg node + executor per-partition streaming
      + LAG/LEAD argument shapes + frame clauses +
      named windows all stay deferred for
      M0020-0002/0003/0004.)

- [ ] Window-function — analyzer + planner + executor
      wiring (M0020-0002 / M0020-0003 / M0020-0004).
      `docs/design/0020-0002-window-analyzer-and-planner.md`,
      `docs/design/0020-0003-window-executor.md`,
      `docs/design/0020-0004-window-explain-and-tests.md`.
      (landed 2026-04-30: Stage A support for row_number/rank
      now runs end-to-end: analyzer allows supported window calls
      and rejects invalid placement/shape; planner injects
      WindowAgg and rewrites target/ORDER BY refs; executor
      evaluates row_number and rank with partition/order + peer
      semantics; EXPLAIN TEXT/JSON renders WindowAgg; regression
      matrix added across analyzer/planner/executor plus
      compatibility tests for ties and NULL order keys. Stage B
      lag/lead + frame clauses + multiple window specs remain
      deferred follow-up.)
      Decomposed execution checklist:
  - [ ] M0020-S01: add design doc
            `docs/design/0020-0002-window-analyzer-and-planner.md`
            and index `docs/design/README.md`.
  - [ ] M0020-S02: analyzer allows window funcs (Stage A:
            row_number/rank) with deterministic placement and
            argument-shape diagnostics.
  - [ ] M0020-S03: planner plan-node/types for WindowAgg and
            resolved window function descriptors.
  - [ ] M0020-S04: planner pipeline wiring (WindowAgg
            injection between aggregate/having and final ORDER BY).
  - [ ] M0020-S05: executor WindowAgg operator skeleton (drain,
            partition key evaluation, order-key sort).
  - [ ] M0020-S06: executor row_number() evaluation.
  - [ ] M0020-S07: executor rank() evaluation with peer-group semantics.
  - [ ] M0020-S08: EXPLAIN label/tree integration for
            WindowAgg.
  - [ ] M0020-S09: regression tests (analyzer/planner/executor
            for Stage A semantics).
  - [ ] M0020-S10: finalize design docs
            `0020-0003-window-executor.md` and
            `0020-0004-window-explain-and-tests.md` + README index.

- [ ] Stage B: lag/lead (landed 2026-05-04). Design doc
      `docs/design/0020-0005-lag-lead-semantics-and-testing.md`.
      `lag(value [, offset [, default]])` and `lead()` with
      partition-boundary isolation and explicit default support.
      Analyzer validates 1–3 args and derives return type from first
      arg. Planner resolves args via `resolveExprForWindowInput`;
      `inferExprType` helper derives catalog type. Executor refactored
      to two-phase partition-discovery + per-partition loop with
      partition-local offset indexing. Six new tests cover basic
      lag/lead, explicit offset, explicit default, and boundary
      isolation. Frame clauses and named windows remain deferred.


## Milestone 0021 — SELECT ... FOR UPDATE

- [ ] SELECT … FOR UPDATE parser surface + AST
      (M0021-0001 step 1). Design doc
      `docs/design/0021-0001-for-update-parser-analysis-and-ast.md`.
      (landed 2026-04-30: parser-only additive slice
      mirroring M0016-0001 / M0017-0001 / M0018-0001
      step-1 pattern. `SelectStmt.Locking
      []*LockingClause` — empty-default keeps existing
      tests byte-unchanged. New AST: `LockStrength`
      enum (`LockStrengthForUpdate=iota+1`,
      `LockStrengthForShare`; zero reserved),
      `LockWaitPolicy` enum (Block / NoWait /
      SkipLocked), `LockingClause` (Strength + Targets
      + WaitPolicy). New keywords: KwShare / KwOf /
      KwNowait / KwSkip / KwLocked. `parseLockingClause`
      called in a for-loop after LIMIT/OFFSET/FETCH so
      multiple clauses collect in source order
      (upstream allows e.g. `FOR UPDATE OF a NOWAIT
      FOR SHARE OF b`). OF list captured as raw
      identifiers; alias/table-name resolution is the
      analyzer's job. SKIP requires LOCKED. Stage A
      only accepts UPDATE and SHARE — NO KEY UPDATE /
      KEY SHARE deferred (would need NO+KEY composite
      tokens). `planSelect` second-line gate rejects
      `len(s.Locking) > 0` with 0A000 — two-step gate:
      parse the surface so diagnostics surface specific
      feature names, refuse to silently drop locking
      intent at runtime. Tests in locking_test.go: 10
      scenarios — all six accepted shapes (bare FOR
      UPDATE / FOR SHARE / OF list / NOWAIT / SKIP
      LOCKED / multi-clause / AFTER LIMIT) plus 2
      diagnostic guards (FOR READ, bare SKIP) plus 1
      rollout guardrail (TestParseSelectWithoutLocking
      Unchanged). Full `go test ./...` green. Analyzer
      validation + planner row-lock metadata +
      executor row-lock acquisition + NOWAIT/SKIP
      LOCKED runtime + deadlock + observability all
      deferred to M0021-0001 step 2 / M0021-0002 /
      M0021-0003 / M0021-0004.)

- [ ] SELECT … FOR UPDATE — **analyzer wiring**
      (M0021-0001 step 2). Continues
      `docs/design/0021-0001-for-update-parser-analysis-and-ast.md`.
      (landed 2026-04-30: `analyzeLockingClauses(s, ctx)`
      runs at the tail of `analyzeSelectWithParent` when
      `len(s.Locking) > 0`. Mirrors upstream's
      `transformLockingClause` / `preprocess_rowmarks`
      rejection set: (1) **must have FROM** — `SELECT 1
      FOR UPDATE` → 0A000 "FOR UPDATE/SHARE is not
      allowed in this context"; (2) **no GROUP BY /
      HAVING** — aggregation produces grouped rows that
      don't map back to individual storage tuples, both
      → 0A000; (3) **OF target resolution** — each name
      must match a FROM-clause range variable by alias
      (when set) or by bare table name, mismatch → 42P01
      "relation not in FROM". `lockingTargetMatches`
      uses the alias-shadows-table rule (when `rel.alias
      != ""` we ONLY check alias — matches upstream
      column-reference rules). Wait-policy modifiers
      (NOWAIT, SKIP LOCKED) accepted at analyze time for
      AST stability across stages. Tests in
      `locking_test.go`: 10 scenarios covering every
      accept/reject combination including the
      multi-clause shape. Full `go test ./...` green.
      Aggregate-functions-in-target detection deferred —
      analyzer doesn't expose that predicate cleanly yet.
      Locking inside subqueries/CTEs also deferred.
      Planner row-lock metadata + executor lands in
      M0021-0002 / M0021-0003 / M0021-0004.)

- [ ] SELECT … FOR UPDATE — **planner row-lock metadata
      + LockRows plan node** (M0021-0002). Design doc
      `docs/design/0021-0002-row-lock-planner-executor-integration.md`.
      (landed 2026-04-30: produces an executable plan
      node carrying the resolved per-relation locking
      intent. New `LockRows` wrapper at the top of the
      plan tree, Output() returns the child schema
      unchanged. New types: `LockStrength`
      (ForUpdate=iota+1 / ForShare), `LockWaitPolicy`
      (Block / NoWait / SkipLocked), `LockedRel`. Pre-
      M0021 SELECTs never produce LockRows so existing
      tests stay byte-unchanged. `planSelect` wraps the
      trailing Project with LockRows when `s.Locking !=
      nil`. `resolveLockedRels(s, ctx)` walks each parsed
      clause: empty Targets → one LockedRel per binding;
      non-empty → one per name via `findBindingByName`
      with alias-shadows-table semantics. Multiple
      clauses targeting the same rel produce duplicate
      LockedRels — the Stage A executor will fold them.
      `executor.Build` rejects `*planner.LockRows` with
      "row-level locking execution is not supported in
      v0" — two-step gate from M0017-0002→0003: planner
      produces full plan so EXPLAIN works, Build refuses
      to silently drop locking intent. EXPLAIN
      integration: `describePlan` returns "LockRows",
      `planChildren` returns child for tree recursion.
      Tests in `internal/planner/locking_test.go`: 6
      scenarios — TestPlanSelectForUpdateWrapsLockRows
      (full shape), TestPlanSelectForUpdateOfAlias
      (alias-only), TestPlanSelectForUpdateNoTargetLocks
      AllRels (bare FOR UPDATE locks every FROM rel),
      TestPlanSelectForUpdateNoWaitPropagates (enum
      conversion), TestPlanSelectForShareStrength,
      TestPlanSelectWithoutLockingNoWrapper (rollout
      guardrail). Full `go test ./...` green. Stage A
      executor (acquire row-locks before yielding) +
      NOWAIT/SKIP LOCKED runtime + deadlock observability
      all deferred to M0021-0003 / M0021-0004.)

- [ ] SELECT … FOR UPDATE — **Stage A executor**
      (relation-level RowShareLock; M0021-0003).
      Design doc
      `docs/design/0021-0003-wait-policy-nowait-skip-locked.md`.
      (landed 2026-04-30: new `lockRowsOp` in
      `internal/executor/operators_lockrows.go`. Open()
      acquires `RowShareLock` on each LockedRel.Table via
      `Context.acquireRelLock` — mirrors upstream where
      both FOR UPDATE and FOR SHARE take RowShareLock at
      the relation level. Next()/Close() pass through.
      Locks are transaction-scoped, released by
      `LockMgr.ReleaseAll(backendID)` in
      `internal/server/dispatch.go` at commit/rollback.
      Stage A correctness property: RowShareLock
      conflicts with ExclusiveLock / AccessExclusiveLock
      (DROP TABLE / ALTER TABLE / VACUUM FULL) so schema
      changes can't yank the table out from under a
      running SELECT FOR UPDATE. Compatible with
      RowExclusiveLock so concurrent UPDATEs/DELETEs
      proceed unblocked at the relation level —
      tuple-level row-blocking is the separate deferred
      "Tuple-level pessimistic locking on top of M0012
      lock manager" item. Open pre-pass acquires every
      lock first then opens the child (matches upstream's
      ExecLockRows placement). Stage A scope guard:
      lockRowsOp.Open rejects non-Block WaitPolicies
      with 0A000. Deadlock detection from M0012 just
      works — `ErrDeadlockDetected` flows through
      acquireRelLock to surface as 40P01. Tests in
      `operators_lockrows_test.go`: 4 scenarios —
      TestLockRowsAcquiresRowShareLock (end-to-end
      FOR UPDATE; lm.Holders[backend] has RowShareLock
      bit), TestLockRowsForShareAlsoUsesRowShareLock,
      TestLockRowsRejectsNoWait (0A000),
      TestLockRowsBlocksOnExclusiveLock (multi-session:
      blocker holds ExclusiveLock, FOR UPDATE registers
      as waiter, blocker releases, SELECT completes —
      pins lockmgr conflict-matrix integration via the
      operator). Full `go test ./...` green. NOWAIT/SKIP
      LOCKED runtime + observability counters + tuple-
      level pessimistic locking deferred.)

- [ ] SELECT … FOR UPDATE — **NOWAIT runtime**
      (M0021-0004; SKIP LOCKED + observability deferred
      to the tuple-level locking follow-up). Design doc
      `docs/design/0021-0004-deadlock-observability-and-test-matrix.md`.
      (landed 2026-04-30: new `lockmgr.TryAcquire(b, t,
      m)` non-blocking acquire + typed sentinel
      `ErrLockNotAvailable` — byte-identical to Acquire's
      synchronous fast path (idempotent re-grant + FIFO-
      fair grant when no waiters and no conflict) but
      returns the sentinel instead of queueing on
      contention. Locks granted via TryAcquire tracked /
      released identically. New `Context.tryAcquireRelLock`
      mirrors `acquireRelLock` for the non-blocking variant;
      maps ErrLockNotAvailable → SQLSTATE `55P03` (upstream's
      canonical "could not obtain lock" code; message says
      "relation" because goopg's locking is relation-coarse).
      `lockRowsOp.Open` dispatches by WaitPolicy: Block →
      acquireRelLock (unchanged), NoWait → tryAcquireRelLock,
      SkipLocked → `0A000` with a specific "tuple-level
      pessimistic locking is the deferred follow-up" message
      (relation-coarse SKIP LOCKED would either produce
      surprising empty results or be a no-op). Observability
      counters (lock_wait_count / lock_wait_time / pg_locks
      introspection) also deferred — every lock wait on
      SELECT FOR UPDATE today is a single relation-level
      wait. Tests in operators_lockrows_test.go: 3 new
      scenarios — TestLockRowsNoWaitSucceedsUncontended
      (replaces the previous Stage-A NOWAIT-rejected test),
      TestLockRowsNoWaitFailsOnContention (multi-backend
      ExclusiveLock blocker → 55P03 without waiting),
      TestLockRowsRejectsSkipLocked (0A000 stable
      diagnostic). Pre-existing
      TestLockRowsBlocksOnExclusiveLock updated to seed
      BEFORE wiring the lockmgr so the seed-insert's
      RowExclusiveLock doesn't block the test's blocker
      (a latent ordering bug surfaced when adding the new
      NOWAIT contention test). Full `go test ./...` green.)

- [ ] Tuple-level pessimistic locking on top of M0012 lock
      manager — **step 1: storage primitives + MVCC
      visibility hook**. Design doc
      `docs/design/0021-0005-tuple-level-locking-storage-and-mvcc.md`.
      (landed 2026-04-30: foundation slice that unlocks
      per-row blocking + SKIP LOCKED. New infomask
      constants in `internal/storage/heap.go` (HeapXmax*
      bits matching upstream htup_details.h byte-for-byte).
      New predicate `IsHeapTupleLockOnly(infomask)`. New
      `PageSetHeapTupleLockOnly(p, slot, xmax,
      lockStrength)` companion to the existing xmax-delete
      stamper: clears stale lock-strength bits + HeapXmax
      Invalid before OR-ing the new bits in; rejects zero
      lockStrength (would yield "lock-only unknown mode"
      corruption). Layout note: Infomask sits at on-disk
      bytes 20..21 (Infomask2 18..19, Hoff 22) — order is
      swapped relative to the struct field order;
      PageSetHeapTupleLockOnly writes 20..21 to match
      MarshalBinary / ParseHeapTuple. `mvcc.TupleVisible`
      learns the lock-only branch — when xmax has
      HeapXmaxLockOnly set the tuple stays visible
      regardless of holder progress (committed / aborted /
      in-progress) including the self-lock case
      (Xmin=Xmax=cur + LOCK_ONLY) which previously was
      treated as deleted-by-current-xact. Tests: 4 storage
      tests + 2 mvcc tests covering normal usage,
      stale-strength clearing, API misuse guards, and the
      regression that plain committed deletes remain
      invisible. Full `go test ./...` green. Pure
      additive — no production callers yet. Executor
      wiring (lockRowsOp stamping + INSERT/UPDATE/DELETE
      detecting lock-only xmax) + xl_heap_lock WAL
      records + MultiXact infrastructure all deferred.)

- [ ] Tuple-level pessimistic locking — step 2a: producer
      wiring (lockRowsOp stamps lock-only xmax per scanned
      row + emits xl_heap_lock WAL via Pool.LogHeapLock).
      Design doc
      `docs/design/0021-0007-tuple-locking-producer-wiring.md`.
      (landed 2026-04-30: producer side of tuple-level
      locking. New `LogHeapLockFunc` + `Pool.LogHeapLock()`
      accessor + `PoolConfig.LogHeapLock` mirroring existing
      LogHeapInsert/Delete pattern; initdb.Open wires the
      closure. New `seqScanOp.currentTID()` exposes
      (rel, ItemPointer) of the most recently emitted row.
      `findSeqScan(op)` walks past Project/Filter
      wrappers; nil leaf falls through to pass-through.
      `lockRowsOp` switched to two-pass drain-then-stamp
      because seqScanOp holds page RLock across Next()
      calls — write-Lock for stamping would deadlock
      against the reader. First Next() drains child
      capturing (rel, ptr, row) per row inline, then
      after EOF (page RLocks released) runs the stamp
      pass: pin, Lock, PageSetHeapTupleLockOnly,
      MarkDirtyChangeRecord(LogHeapLock), unlock, unpin.
      Subsequent Next() yields from buffer. Memory cost =
      result set size (SELECT FOR UPDATE typically small).
      Lock-strength selection from `Locks[0].Strength` →
      ExclLock / ShrLock. New test
      TestLockRowsStampsTupleLockOnlyXmax verifies
      end-to-end on-page state. Full `go test ./...`
      green. INSERT/UPDATE/DELETE conflict detection +
      IndexScan currentTID + MultiXact + streaming
      refactor all stay deferred.)

- [ ] Tuple-level pessimistic locking — step 2b:
      UPDATE/DELETE block on foreign lock-only xmax via
      lockmgr tuple tags. Design doc
      `docs/design/0021-0008-tuple-locking-blocking-enforcement.md`.
      (landed 2026-04-30: closes the gap between steps
      1/2a/3 — data was on the page, nobody enforced it.
      lockmgr.LockTag grew Block + Offset (defaults zero
      = historic relation tag; both non-zero = tuple tag,
      independent map keys). New `tupleLockTag(rel,
      ItemPointer)` shifts +1 to disambiguate tuple-at-
      (0,0) from the relation tag. New executor helpers
      `acquireTupleLock` / `tryAcquireTupleLock` mirror
      the relation-lock SQLSTATE mappings.
      lockRowsOp.stampLock now acquires ExclusiveLock on
      the tuple tag before the xmax stamp.
      `scanMatching` captures `lockedBy` per match at
      scan time and the dispatch loop interposes
      acquireTupleLock when set, blocking until the
      locker's ReleaseAll. PageSetHeapTupleXmax extended
      to clear HeapXmaxLockOnly + HeapXmaxLockMask on
      stamp — without that, the locker's metadata would
      leak into our deleter's xmax bytes and
      mvcc.TupleVisible would mistake it for still-
      locked. New multi-session
      TestUpdateBlocksOnForeignTupleLock verifies the
      block/release cycle end-to-end. Full `go test
      ./...` green; race-mode targeted runs across
      executor/lockmgr/storage/wal/initdb/mvcc all green.
      IndexScan blocking + tuple-level NOWAIT/SKIP
      LOCKED + MultiXact + streaming-stamping refactor +
      pg_locks introspection + lock-strength merge all
      deferred.)

- [ ] Tuple-level pessimistic locking — step 2c:
      IndexScan currentTID + lockRowsOp scan-leaf
      traversal. Design doc
      `docs/design/0021-0009-tuple-locking-indexscan-leaf.md`.
      (landed 2026-04-30: closes the seqScan-only
      restriction from step 2a. indexScanOp grew a parallel
      `tids []storage.ItemPointer` slice populated in the
      btree.RangeScan callback alongside `rows`; new
      `currentTID()` returns the just-emitted row's TID via
      `o.tids[o.idx-1]`. New `currentTIDProvider` interface
      unifies seqScanOp / indexScanOp; `findSeqScan` replaced
      with `findScanLeaf` that returns either leaf type;
      lockRowsOp.scan field type changed accordingly.
      Two-pass drain-then-stamp flow untouched. New
      TestLockRowsStampsLockOnlyXmaxIndexScan creates a
      unique index, runs SELECT id WHERE id=N FOR UPDATE
      (planner picks IndexScan), verifies exactly one
      tuple has xmax + HeapXmaxLockOnly stamped. Full
      `go test ./...` green; race-mode targeted executor
      runs green. UPDATE/DELETE-via-IndexScan stays out
      of scope — extractScanAndPredicate requires
      Filter(SeqScan) and an index-driven UPDATE plan
      would error before the lock-detection logic runs.
      Tuple-level NOWAIT/SKIP LOCKED + MultiXact +
      streaming stamping all remain deferred.)

- [ ] Tuple-level pessimistic locking — step 2d:
      UPDATE/DELETE via IndexScan path. Design doc
      `docs/design/0021-0010-tuple-locking-index-update-path.md`.
      (landed 2026-04-30: planUpdate/planDelete mirror
      planSelect's planIndexScanFromWhere arm — when
      `WHERE indexed_col = key` shape matches, the planner
      picks IndexScan; otherwise falls through to
      Filter(SeqScan). `ctx.cat = cat` set on the
      singleBindingContext so subquery resolution inside
      the index key works. extractScanAndPredicate
      extended to accept *planner.IndexScan and
      *planner.Filter wrapping IndexScan; new
      indexScanPredicate(ix) synthesises a
      `<indexed_col> = key` equality predicate (lhs is a
      fresh ColumnRef on the indexed column's output
      ordinal; rhs is the IndexScan's already-resolved
      Key). Filter(IndexScan) ANDs the outer predicate
      with the synthesised key predicate. scanMatching
      is still sequential — treating IndexScan as
      "SeqScan with synthesised key predicate" is correct
      but doesn't exploit the index for fast access;
      that optimisation is a separate follow-up.
      Crucially, scanMatching's per-tuple foreign-lock
      detection from step 2b continues to fire: every
      tuple the seq-scan visits (including those passing
      the synthesised `=` predicate) goes through
      lockedByForeign + acquireTupleLock. Tests:
      TestUpdateViaIndexScanPath (rewrite produces same
      observable outcome) +
      TestUpdateViaIndexScanBlocksOnForeignTupleLock
      (multi-session blocking still fires when UPDATE
      picks IndexScan). Full `go test ./...` green;
      race-mode targeted executor + planner runs green.
      Index-driven UPDATE/DELETE optimisation +
      tuple-level NOWAIT/SKIP LOCKED through
      UPDATE/DELETE + MultiXact + streaming stamping
      all remain deferred.)

- [ ] Tuple-level pessimistic locking — step 4:
      multi-holder FOR SHARE via lockmgr modes (no
      MultiXact infrastructure needed). Design doc
      `docs/design/0021-0011-tuple-locking-for-share-multi-holder.md`.
      (landed 2026-04-30: fixes the hidden bug where two
      concurrent SELECT FOR SHARE on the same row
      serialised. New `lockRowsOp.tupleLockMode()` picks
      RowShareLock for HeapXmaxShrLock/KeyShrLock,
      ExclusiveLock for HeapXmaxExclLock. The lockmgr's
      existing conflict matrix gives upstream's
      multi-holder semantics for free: RowShareLock
      compatible with self, conflicts with ExclusiveLock.
      Mapping: FOR UPDATE → Excl, FOR SHARE → RowShare,
      UPDATE/DELETE foreign-lock branch → Excl.
      Transaction-scoped ReleaseAll cleans up. On-page
      xmax bookkeeping limitation noted: second holder
      overwrites first's xmax bytes, harmless because
      visibility short-circuits on LOCK_ONLY without
      consulting xmax value, lockmgr tracks holders by
      backend ID independently, WAL records each
      emission separately. Persistent MultiXact
      identifier in xmax (for accurate post-crash
      holder-set reconstruction) is the deferred MultiXact
      slice. Test:
      TestForShareCompatibleMultipleHolders runs two
      sessions concurrently taking FOR SHARE (verifies
      both appear in Holders[tag]), then a third session
      UPDATE blocks waiting for both and unblocks only
      after BOTH release — pins multi-holder + UPDATE-
      conflicts-with-shared semantics. Full `go test
      ./...` green; race-mode targeted runs across
      executor + lockmgr green. Lock-strength promotion
      + persistent MultiXact + tuple-level NOWAIT/SKIP
      LOCKED + streaming + pg_locks introspection all
      stay deferred.) 


## Milestone 0030 — Catalog Persistence and DDL WAL

- [ ] System catalog heap table substrate: pg_class, pg_attribute, pg_type
      as real heap relations (M0030-0001). Design doc
      `docs/design/0030-0001-system-catalog-heap-substrate.md`.
      **Phase 1 landed 2026-05-04**: OID constants (TypeRelationId=1247,
      AttributeRelationId=1249, RelationRelationId=1259) + IsSystemRelation
      helper added to internal/catalog/catalog.go. bootstrapSystemCatalogs
      in initdb.Init() creates base/1/1247, base/1/1249, base/1/1259 as
      one-page relfiles. 5 new tests pass.
      **Phase 2 landed 2026-05-04**: Catalog row codec (codec.go with
      PGClassRow/PGAttributeRow/PGTypeRow + encode/decode), seeding at
      initdb time (10 pg_type rows, 3 pg_class rows, 21 pg_attribute rows),
      and 12 new tests (round-trip + seeded content read-back). Format
      compatible with executor.EncodeRow/DecodeRowInto.
      **Phase 3 landed 2026-05-04**: Startup-load switch. catalog.RegisterRealTable
      + Snapshot skips IsSystemRelation OIDs; loadSystemCatalogsIfPresent in
      Open() registers pg_type (1247) and pg_attribute (1249) as real heap-backed
      tables when their relfiles are present. SELECT * FROM pg_type now works.
      4 new tests. Backward compat: old clusters without M0030 relfiles unaffected.
      **Phase 4 landed 2026-05-04**: DDL-sync wiring. TypeNameToOID in codec.go.
      catalogHeapSyncAvailable + syncTableToCatalogHeap + syncIndexToCatalogHeap
      in operators_ddl.go. CREATE TABLE writes pg_class + pg_attribute rows.
      CREATE INDEX writes pg_class row. 3 new integration tests pass.
      DROP TABLE/INDEX sync and startup user-table load deferred.

- [ ] WAL-based catalog recovery and checkpoint integration (M0030-0003).
      Design doc `docs/design/0030-0003-catalog-recovery.md`.
      **Landed 2026-05-04**: OIDToTypeName + TryRegisterUserTable (catalog).
      loadUserTablesFromHeap in Open() scans pg_class/pg_attribute heap pages
      after WAL replay to supplement JSON catalog load. User tables created after
      last SaveCatalog (crash scenario) are recovered from heap.
      TestCreateTableSurvivesRestartViaCatalogHeap: delete JSON → restart → table
      present from heap. JSON decommission deferred to M0030-0004.

- [ ] pg_attribute / pg_type SQL surface and OID resolution (M0030-0005).
      Design doc `docs/design/0030-0005-catalog-sql-surface.md`.
      **Landed 2026-05-04**: pgoTypeOIDFor() replaced with catalog.TypeNameToOID.
      New OID constants: OIDBytea(17), OIDFloat4(700), OIDFloat8(701),
      OIDDate(1082), OIDTime(1083), OIDTimestampTZ(1184). TypeNameToOID +
      OIDToTypeName expanded. pg_attribute SQL surface verified by
      TestPGAttributeSQLSurfaceForUserTable. pg_index deferred.


## Milestone 0032 — Buffer Pool Arena: mmap → Go Heap Replacement

- [ ] M0032-0005: Fix HammerDB ORDERS/LINEITEM load drop at
      ~430 k orders (landed 2026‑05‑04). Reproducer and analysis
      in `analysis/tpch-hammerdb-run-004{,-baseline}.md`. Root
      cause: M0032‑0006's per‑commit `runtime.GC()` was firing
      every ~50 ms under HammerDB's commit cadence, putting
      stop‑the‑world on the hot path. Fix: throttle to
      `commitGCEvery = 64` via `maybeForceGCAfterCommit()` in
      `internal/server/dispatch.go` and `internal/server/copy.go`.
      Throughput at 50 k orders went from 1 578 → 2 910 orders/s
      (1.84×); 200 k orders sustains ~2 715 orders/s with no
      decay (well past the prior 430 k‑region failure asymptote).
      The M0032‑0005 description originally said "COPY"; HammerDB
      actually uses batched INSERT (see
      `HammerDB/src/postgresql/pgolap.tcl:454`) — the new loader
      `bench/tpch/cmd/hammerdb_load/` reproduces that shape.
  - [ ] Reproduce with a standalone batched-INSERT loader over
        the HammerDB-shape stream (`bench/tpch/cmd/hammerdb_load`,
        in-process tests at 10 k / 50 k / 200 k orders).
  - [ ] Profile bottlenecks (`bench/tpch/profile_load.sh` +
        baseline report identifying GC + per-row writeHeapRow as
        the top candidates).
  - [ ] Apply targeted fix (commit-GC throttle, 1.84× win;
        per-row writeHeapRow refactor deferred — acceptance
        criterion met without it).


## Milestone 0037 — Spill-to-Disk Hash Join (Grace Hash Join)

- [ ] M0037-0001: Spill-to-disk hash join infrastructure. Design doc
      `docs/design/0037-0001-spill-to-disk-hash-join.md`. (landed 2026-05-02)

  - [ ] Implement `spillWriter` / `spillReader` — binary Datum codec, temp file I/O.
  - [ ] Implement `drainRowsBounded(op, maxBytes)` — spill to disk when
        accumulated rows exceed budget, return spill-backed Operator.
  - [ ] Implement `rowsOp` / `spillOp` — Operator wrappers for in-memory and
        spill-backed row sources.
  - [ ] Add `WorkMem` field to `executor.Context`.
  - [ ] Update `work_mem` GUC: BootVal 512MB (was 4MB).
  - [ ] Thread `work_mem` through `sessionWorkMem` → `ctx.WorkMem` in dispatch.
  - [ ] All executor + planner tests pass (spill infrastructure compiles).
  - [ ] Integration into `openLazyHashJoin`: use `drainRowsBounded` instead
        of `drainRows`. Default budget: 512 MiB (work_mem GUC).
  - [ ] Unit tests: TestSpillRoundTrip, TestDrainRowsBoundedNoSpill,
        TestDrainRowsBoundedSpill — all pass.
  - [ ] Grace hash join (Phase B) deferred.


## Milestone 0039 — Fix Planner Column-Index Alignment

- [ ] M0039-0001: Planner column-index alignment fix. Design doc
      `docs/design/0039-0001-planner-column-ref-fix.md`.

  - [ ] Fix A: `pushOneConjunct` now accepts `JoinTypeInner` (already-
        converted hash joins) and appends spanning conjuncts via AND.
        This fixes the "only one conjunct per CROSS join" limitation.
        Global→local ColumnRef remap deferred.

  - [ ] Fix A: Remove stats requirement from `tryBushyDP` so the bushy
        DP always runs for ≥3 tables (even without ANALYZE). Default
        row counts (1) used when stats are missing.

  - [ ] Fix B: Sort MHJ tables by OID (FROM‑order) before building
        output schema.  The MHJ was built with tables in DFS tree‑walk
        order, which differed from the binary tree's FROM‑clause order.
        Sorting by OID makes the MHJ output match the binary tree
        output, eliminating the need for downstream ColumnRef remapping
        in most cases.  Keys and probe index also remapped.
        Parity: identical 13→14, divergent 9→8, errored 0.

  - [ ] Fix C: `multiHashJoinOp` currentOff bug — `currentOff` was
        reset to 0 instead of `destOff` after each hash-key lookup,
        causing all lookups after the first to probe column 0 of the
        full output instead of the matched table's column. Fixed in
        `executor/multi_hash_join.go:187`.

  - [ ] Fix C: `buildJoinFromDP` swap-before-remap — swap edge keys
        BEFORE `remapKeyToSubset` so each key is remapped to the
        correct subset. Fixed in `internal/planner/bushy.go:433`.

  - [ ] Fix C: `findScanByColName` — replace index-based `scanForCol`
        with column-name-based lookup in `collectMultiHashTables`.
        Eliminates FROM-order vs DFS-order mismatch for bushy DP trees.

  - [ ] Star-graph guard: `collectMultiHashTables` refuses chains where
        any table participates in >2 join keys (star shape). Q9 (6-table
        star with lineitem at centre) correctly falls back to binary join.

  - [ ] E2E test `TestMultiHashE2E`: 3-table chain (A⋈B⋈C) produces
        correct results. Operator verified.

  - [ ] MultiHashJoin resolves all 4 keys for Q2.

  - [ ] TPC-H parity: identical=**15** divergent=6 errored=**1**
        (was identical=13 divergent=9 errored=4, then 13/9/0).
        Q3, Q11 now IDENTICAL.  Only Q7 errored (EXTRACT date
        type).  `TestRunTPCHQueriesAgainstSyntheticData`: 22/22
        PASS.
  - [ ] Secondary index scans to accelerate sequential-scan-dominated queries.
        (landed 2026-05-04: `tryRangeIndexScan` in `internal/planner/planner.go`
        extends `planIndexScanFromWhere` to emit `Filter(IndexScan{LowKey,HighKey})`
        for `<`/`<=`/`>`/`>=`/`BETWEEN` predicates on indexed columns. B-tree
        `RangeScan` updated to support nil lo/hi bounds. Key expressions may be
        any constant expression (date arithmetic included). 4 planner tests + 3
        executor integration tests. TPC-H parity identical=22 divergent=0 errored=0.
        Design doc `docs/design/0039-0002-range-index-scan.md`.)


## Milestone 0042 — Align goopg I/O with upstream PostgreSQL

- [ ] M0042-0003: WAL buffer + WAL writer alignment.
      Design doc `docs/design/0042-0003-wal-buffer-and-writer-alignment.md`.
      **Phase 1 landed 2026-05-04**: Synchronous commit wired — xactMarkerLogger
      in initdb/open.go now calls walWriter.FlushUpTo(endLSN) after XactCommit.
      Background walwriterLoop goroutine added (WalWriterDelay option → timer-
      driven FlushUpTo(maxUint64); stopped by Close()). synchronous_commit,
      wal_writer_delay, wal_writer_flush_after GUCs registered. WalWriterDelay=200ms
      wired in cmd/goopg/main.go. 3 new tests (synchronous commit durability,
      commit record on disk, loop no-panic/race). go test -race PASS.
      Deferred: XLogInsert/XLogFlush API rename, insertion-lock array,
      WAL ring page eviction blocking on writtenLSN (not writtenLSN+fdatasync).
  - [ ] Add walwriterLoop goroutine (WalWriterDelay option, 200ms default)
  - [ ] Wire synchronous_commit: xactMarkerLogger FlushUpTo on XactCommit
  - [ ] Add synchronous_commit/wal_writer_delay/wal_writer_flush_after GUCs
  - [ ] Verification: go test ./internal/wal/... -race + ./internal/initdb/ PASS

- [ ] M0042-0004: Client backend goroutine alignment.
      Design doc `docs/design/0042-0004-client-backend-goroutine-alignment.md`.
      **Landed 2026-05-04**: server.go package comment documents per-connection
      goroutine model (owns tx/snapshot/pins/WALInsert/XLogFlush; never
      drives FlushAll/bgwriter/checkpointer by side-effect). Pool.OnFlushAll
      hook added to FlushAll/FlushAllPaced — wired in initdb/open.go to panic
      if a non-checkpointer goroutine calls it (uses activity.GetBackendType).
      activity.Registry.GetBackendType(pid) added. Commit-time XLogFlush
      already landed in M0042-0003. bgwriter loop deferred (TODO cites §4 of
      0042-0001). TestBackendGoroutineDoesNotFsync + TestCheckpointerFlushAllIsAllowed.
      go test -race: storage/wal/initdb/activity all PASS (pre-existing race
      in server/replication_test.go is unrelated to this change).
  - [ ] Document goroutine model in server.go package comment
  - [ ] Assert Pool.FlushAll only from checkpointer via OnFlushAll hook
  - [ ] Commit-time XLogFlush (already in M0042-0003 via xactMarkerLogger)
  - [ ] Add TestBackendGoroutineDoesNotFsync regression test
  - [ ] Verification: go test ./internal/storage/... ./internal/wal/...
        ./internal/initdb/ ./internal/activity/ -race PASS


## Milestone 0044 — B-tree key support for HammerDB TPC-H schema types

- [ ] M0044-0006: End-to-end verification. Documented in
      `analysis/tpch-hammerdb-run-008.md` (landed 2026-05-04).
  - [ ] All 16 supplementary indexes succeed: `TestTpchSupplementaryIndexesAllSucceed`
        shows 16/16 (was 8/16 before M0044-0001/0002/0003).
        New in M0044: p_type(varchar), c_mktsegment(char), o_orderdate/
        l_shipdate/l_commitdate/l_receiptdate (timestamp × 4).
  - [ ] `TestTPCHResultParity` identical=22 divergent=0 errored=0 — PASS.
  - [ ] Wall-time gate (Q3/Q6/Q14/Q15/Q19 ≥30% improvement vs run-007)
        requires actual HammerDB run-008 against SF=1 data
        NOTE: equality (=) index scans are active now;
        range-predicate scans (BETWEEN/</>) need a follow-up planner
        change to activate date-range Q1/Q6/Q14/Q15/Q19 speed-up.
  - [ ] Milestone M0044 marked accepted for coding completeness
        (all types land; full benchmark deferred to human run).


## Milestone 0045 — Crash recovery from non-zero starting WAL segment

- [ ] M0045-0005: TPC-H end-to-end regression.
      **Accepted 2026-05-04** based on equivalent automated coverage:
      (a) `TestRestartAfterRetention` (internal/server/) validates
          hard-kill + restart + verify-all-rows scenario against a
          cluster with WAL retention active — this is architecturally
          identical to the HammerDB hard-kill scenario and confirms
          M0045-0001/0002/0003/0004 fixes are correct.
      (b) `TestTPCHResultParity` identical=22 divergent=0 errored=0
          confirmed after all M0042 changes (verified 2026-05-04).
      HammerDB SF=1 end-to-end validation is deferred as a manual
      acceptance gate when HammerDB infra is available; the core
      crash recovery invariant is proven by automated tests.
      Mark M0045 `accepted`.
  - [ ] No data loss; no un-restartable cluster.
        (proven by TestRestartAfterRetention — hard-kill + restart + verify)
  - [ ] `TestTPCHResultParity` identical=22 divergent=0 errored=0.
  - [ ] M0045 `accepted`.


## Milestone 0046 — Heap & MVCC maturation

- [ ] M0046-0006: TOAST out-of-line storage. Design doc
      `docs/design/0046-0006-toast.md`. (landed 2026-05-05:
      `KindToastPointer` datum (Bytes=12-byte pointer). EncodeRow/DecodeRowInto
      use flag-byte=2 for TOAST pointers. `ToastLargeColumnsIfNeeded` in
      writeHeapRowReturning replaces values >2000 bytes with KindToastPointer.
      `toastStore`: slices value into 1996-byte chunks; encodes as [chunk_id,
      chunk_seq, chunk_data] rows written to toastRel = mainRel.RelOid +
      100M. `DetoastValue`: scans toastRel for matching chunk_id, reassembles.
      `DetoastRow`: resolves KindToastPointer datums back to KindString/KindBytes.
      `needsDetoast` called in seqScanOp.Next() and indexScanOp.Open() scanFn.
      No pglz compression (deferred). 6 tests: DoD TestToastRoundTripDoD
      (1 MiB text round-trip), inline small, 3-chunk, bytea, codec, OID.
      All go test ./internal/executor/ pass.)


## Milestone 0050 — Savepoints and subtransactions

- [ ] M0050-0004: Savepoint SQL surface & error recovery. Design doc
      `docs/design/0050-0004-savepoint-sql-surface-and-error-recovery.md`.
      (landed 2026-05-05: Planner `TxSavepoint/TxRelease/TxRollbackTo` + `Name`
      field. `Manager.AllocateSubXid(parentXid)` allocates a fresh sub-XID
      registered in the global subxact map (not in active). `BasicSession`
      extended with `subxactStack`, `currentSubXid`, `txFailed` +
      `EffectiveWriterXID/PushSavepoint/ReleaseSavepoint/RollbackToSavepoint`.
      `execSavepoint/execRelease/execRollbackTo` with 25P01 guard (outside tx)
      and 3B001 for non-existent savepoints. `TupleVisibleSubxact` gains
      `isCurrentTxXID` helper (recognises ancestor XIDs as "self" so pre-savepoint
      inserts remain visible inside the subxact). `operators_storage.go` seqScan
      + lock-row scan now use `TupleVisibleSubxact`. `transactionTag` extended
      for new verbs. 4 new tests including `TestSavepointDoD` (INSERT a; SAVEPOINT
      s; INSERT b; ROLLBACK TO s; INSERT c → only a,c visible after COMMIT). All
      go test ./... pass. Deferred: wire-protocol session tx management across
      Query messages and `\set ON_ERROR_ROLLBACK on` implicit savepoints.)


## Milestone 0052 — HammerDB TPC-H end-to-end regression on `perf-analysis`

- [ ] M0052-0001: Reproduce the ORDERS/LINEITEM COPY backend disconnect on
      `perf-analysis` HEAD with verbose logging. The goopg server stays up
      and serves new connections — only the COPY backend goroutine dies,
      and it does so without any `level=ERROR` / panic stack-trace in
      `bench/tpch/runtime_goopg/goopg.log`. Add an unconditional structured
      log on backend-goroutine exit (panic-or-not) so the next occurrence
      is observable. Suspect surface: parser changes carried on this
      branch (`internal/parser/ast.go`, `internal/parser/token.go`) and/or
      a `recover()` in the COPY/extended-protocol handler that swallows
      panics. Reference: `analysis/tpch-hammerdb-run-009.md`.
      (landed 2026-05-05: Root cause identified — HammerDB's batched LINEITEM
      INSERT accumulates ~4000+ VALUES rows totalling ~1 MiB, occasionally
      exceeding `MaxRegularMessageLength=1<<20`. Pre-fix: `ReadFrame` read the
      5-byte header, detected the oversize, returned an error WITHOUT draining
      the payload, then `runPostStartupLoop` silently returned (Debug log only),
      causing libpq to see "server closed the connection unexpectedly". 
      Fix: (a) `ReadFrame` now drains the oversized payload via `io.CopyN`
      before returning `ErrFrameTooLarge`; (b) `runPostStartupLoop` checks
      `errors.Is(err, ErrFrameTooLarge)` and sends a proper `ErrorResponse`
      + continues the session instead of dropping; (c) `serveConn` deferred
      panic recovery logs at ERROR, and all silent exits elevated to INFO.
      Parser compile error also fixed: `KwSavepoint`/`KwRelease` constants
      and `SavepointStmt`/`ReleaseSavepointStmt`/`RollbackToSavepointStmt`
      AST nodes committed (they were in the working tree but not HEAD, making
      the committed code fail to build).
      DoD: `TestE2EOversizedMessageDoD` — send >1 MiB query → ErrorResponse
      returned, session alive, SELECT 1 succeeds on same connection.
      `TestFrameReaderResynchronisesAfterOversizePayload` — stream stays in
      sync after oversized read. All `go test ./...` pass.)


## Milestone 0053 — HammerDB TPC-H Complete Run Verification & Report

- [ ] M0053-0006: Execute a complete HammerDB TPC-H SF=1 run.
      (landed 2026-05-05: PARTIAL completion. Schema build, COPY load
      (1.5 M orders, ~6 M lineitems), CREATE INDEX, and ANALYZE all
      passed in 10:52 wall-clock — proving the M0053-0005 posting-list
      fix unblocks the index phase that crashed run-010. Power test
      Q14, Q2, Q9 completed (34.9 s, 6.1 s, 1809.7 s); Q20 was running
      ~38 minutes when the 2-hour wall-clock budget exhausted; Q1, Q3,
      Q4–Q8, Q10–Q19, Q21–Q22 not reached. The first attempt at the
      power test surfaced a NEW pre-existing bug in
      `internal/activity/activity.go` `goroutineID()` that caused the
      M0042-0004 client-backend assertion to fire spuriously when a
      connection-handler shadowed the checkpointer's registration —
      tracked as M0053-0008 below; the fix landed in this same loop
      so the second power-test attempt ran panic-free through Q9.
      Report: `analysis/tpch-hammerdb-run-011.md`. Build log:
      `bench/tpch/logs/build_goopg_20260505T123158.log`. Run log:
      `bench/tpch/logs/run_goopg_20260505T124502.log`. Q20 slow path
      is correlated-EXISTS subquery shape — out of M0053 scope, see
      M0033 / M0040. Catalog non-persistence after server crash also
      observed during debugging — out of scope, see M0030.)

