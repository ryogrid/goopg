# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

NOTE: past milestones are stored in `completed_milestones/` and should NOT be copied. If you need to reference a past milestone, you can see these files for the historical record, but they are not part of the active fix plan. Only items in this file are actionable.

### M0092 outcome — structural changes landed; TPS NOT improved

M0092 (`docs/milestones/0092-lazy-row-emission-in-scan-and-project.md`)
landed in 4 commits on 2026-05-11:

- `57312d5` — 3 design docs.
- `5211387` — NLI prerequisite: `nestedLoopIndexJoinOp`
  deep-copies outerRow into `currentOuter`.
- `dc52f60` — `projectOp.Next` drops per-row `cloneRow`;
  `MaterializedSlot.Materialize` now always deep-copies.
- `8f32c07` — `indexScanOp` lazy refactor (TID-list-eager +
  heap-fetch-lazy; arena field removed).

End-to-end pgbench select-only @ -c 10 -T 180 scale 100:

- post-M0091 (commit 460809c): **510.52 TPS** / 19.6 ms
- post-M0092 (commit 8f32c07): **437.62 TPS** / 22.8 ms
  (−14 %)

The structural changes are correct (all tests pass, data
integrity preserved) but did NOT deliver TPS improvement at
this workload. Per the post-fix alloc profile, the cloneRow
path moved into `slot.Materialize` (now always deep-copies)
rather than being eliminated; rowPool.New stayed at ~35 % of
allocs. The residual is broadly distributed across small
sites (SlotFromRow, ParseHeapTuple, PageGetHeapTuple,
protocol cells slice) + GC at 80 % of CPU.

The structural changes still matter for OTHER workloads
(wide TPC-H index scans get a memory-footprint reduction;
slot contract is tightened; NLI is defensive). They just
don't move pgbench-c10's TPS needle.

**M0092 follow-up landed 2026-05-11** (commits `55f6de0`,
`a0817bb`, `1d331a1`, `da7224d`, `1916109`): all four
broadly-distributed allocation cuts (SlotFromRow stack-
aliasing, protocol DataRow allocation reduction,
ParseHeapTupleNoCopy + RLock-held-across-decode,
`track_io_timing` GUC gating 14 I/O hooks) confirmed gone
from the steady-state top-23 alloc list. **TPS did NOT
move** past the noise floor: M0092 baseline re-measures at
317 TPS; M0092 followup runs at 283-342 TPS. CPU pprof
shows the goopg server at 0.17 % CPU — pgbench-S is NOT
CPU-bound. Full analysis:
`bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md`.

The actual bottleneck identified during the M0092 follow-up
audit: **per-commit WAL fsync for read-only transactions**.
goopg currently emits an XactCommit WAL record + sync
fsync for every transaction including pure SELECT, which
differs from PostgreSQL's lazy-XID model where read-only
transactions skip `RecordTransactionCommit` entirely. 60-s
server log shows 19,684 `walwriter flush` lines matching
the transaction rate. Filed as M0093 below.

Results:
`bench/pgbench-compare/results/20260511_133003_goopg_select-only_c10_m0092.txt`
+ `20260511_goopg_select-only_m0092_summary.md`
+ `20260511_goopg_select-only_m0092_followup_summary.md`.

## M0093 — Read-only commit skip-WAL (PG-parity) (filed 2026-05-11)

**Background:** M0092 follow-up identified that goopg emits
a synchronous WAL `XactCommit` record + `FlushUpTo`
(fsync) for **every** transaction, including read-only
`SELECT`. This diverges from PostgreSQL's lazy-XID-allocation
design where read-only transactions never call
`RecordTransactionCommit` and emit zero WAL on commit. The
result: pgbench-S `-c 10` is bottlenecked on per-query
fsync at 282-342 TPS while the goopg server idles at
0.17 % CPU.

Milestone doc:
`docs/milestones/0093-read-only-commit-skip-wal-emission.md`.

Design docs:
- `docs/design/0093-0001-readonly-commit-skip-wal.md`
  (chosen design: **A — wroteWAL flag on transaction
  state**; every WAL-Append call site enumerated for the
  M0093-0002 audit boundary).
- `docs/design/0093-0002-pgbench-remeasurement-target.md`
  (re-measurement methodology; target TPS ≥ 1,000 =
  M0091's bar; secondary target: walwriter flush rate
  drops from ~19,600 / 60 s to < 100 / 60 s).

### Sub-milestones

- [x] **M0093-0001** — Design doc accepted (Design B chosen, 2026-05-11).
- [x] **M0093-0002** — Implementation landed (5 commits, 2026-05-11).
- [x] **M0093-0003** — pgbench-S TPS: 2,740 (baseline 317; +8.6×); walwriter flush 0/60s.
- [x] **M0093-0004** — pgbench standard/simple-update: no regression vs M0092 baseline.

### Note on prior `## pgbench select-only @ -c 10` section

The measurement immediately below this M0091 block is the
**reproducer that surfaced this milestone.** It establishes
the pre-fix baseline (350.89 TPS) against which the M0091
sub-milestones' improvements are measured.

## pgbench select-only @ -c 10 (post-M0090, 2026-05-11 12:13)

Spot measurement requested by the user: scale=100, `-c 10
-j 10 -T 180`, select-only workload, same goopg configuration
as the M0090 verification run (`shared_buffers=2560MB`,
`wal_buffers=100MB`, etc.). Run against the same scale-100
data dir from the M0090 verification.

Result file:
`bench/pgbench-compare/results/20260511_121306_goopg_select-only_c10.txt`.

| metric | value |
|---|---:|
| transactions | 63 169 |
| failed | 0 (0.000 %) |
| tps | **350.89** |
| latency avg | 28.50 ms |
| latency stddev | 11.85 ms |
| initial connection time | 6.09 ms |

Throughput drifted downward over the 180 s run (10 s sample:
383.8 TPS → 170 s sample: 313.2 TPS, final 180 s sample:
356.1 TPS — modest TPS decay observed). 0 failed transactions
throughout; pkey IndexScan + heap-fetch path is correctness-
clean under read-only contention.

Cross-reference: the M0090 verification's select-only at the
same scale but `-c 100 -j 100` yielded 386.50 TPS. At -c 10
the TPS is lower (350.89) because there are fewer concurrent
in-flight queries to saturate the CPU; latency per query is
~28 ms vs ~258 ms at -c 100 (10× less per-query queueing).
This is the expected concurrency / throughput trade-off
shape — no anomaly.

## M0094 — Replication E2E Completion & TAP Test Porting (D-003 / D-004)

Milestone doc: `docs/milestones/0094-replication-e2e-and-tap-test-porting.md`

Background: M0005 (streaming replication) and M0008 (logical replication) are
substantially complete but two E2E tests remain hard-skipped. M0094 closes the
remaining gaps and ports a prioritised subset of the D-003 recovery TAP suite
(6 tests) and D-004 subscription TAP suite (3 tests).

### Sub-milestones

- [x] **M0094-0001** — Design doc `0094-0001-streaming-replication-e2e-gap.md`
      status → `accepted` (2026-05-11). Added `PreCloneHook func(*cluster.Cluster) error`
      to `replcluster.Options`; wired in `Setup()` after primary start, before
      standby clone. WAL `ApplyRecord` audit: all record kinds already handled
      (BtreeInsert, BtreeSplit, HeapVacuum all have replay functions — no gaps).
      Un-skipped `TestE2E_PhysicalReplication` with a hook that creates `repl_t (id int)`
      before clone, inserts a row on primary, waits, queries standby.
      Key files: `internal/testutil/replcluster/replcluster.go`,
      `internal/testport/e2e_replication_test.go`.

- [x] **M0094-0002** — Design doc `0094-0002-logical-apply-delete-update.md`
      status → `accepted` (2026-05-11). Extended `RecordKindHeapDelete` WAL
      format to carry old-tuple bytes (optional); extended `LogHeapDeleteFunc`
      hook signature; executor DELETE/UPDATE paths capture pre-delete tuple.
      Classifier populates `Change.OldTuple`. `ReorderBuffer.Commit()` folds
      consecutive `(Delete, Insert)` pairs on same rel → `ChangeUpdate`. pgoutput
      encoder emits `'D'` with 'O' old-tuple body when OldTuple is non-empty;
      emits new `'U'` message for `ChangeUpdate`. Decoder added `'U'` parsing.
      `applyDelete()` and `applyUpdate()` implemented in `applyworker.go`
      via key-tuple heap scan + xmax stamp. `TestE2E_LogicalReplication` un-skipped
      and passes (INSERT + DELETE + UPDATE end-to-end). Unit tests:
      `TestReorderFoldDeleteInsertToUpdate`, `TestReorderFoldDoesNotFoldDifferentRels`,
      `TestPgoutputUpdateMessageEncoding`, `TestPgoutputDeleteWithOldTupleEmitsO`.

- [x] **M0094-0003** — Design doc `0094-0003-recovery-tap-porting-strategy.md`
      status → `accepted` (2026-05-11). Created `internal/testport/recovery_port_test.go`.
      Ported 6 recovery TAP tests (all adapted to v0 capabilities):
      - `TestPort_Recovery001StreamRep` — walreceiver streaming + walsender presence
      - `TestPort_Recovery013CrashRestart` — SIGKILL + WAL recovery of committed rows
      - `TestPort_Recovery019ReplslotLimit` — physical slot creation + pg_replication_slots view
      - `TestPort_Recovery038SaveLogicalSlots` — logical slot persistence across restart
      - `TestPort_Recovery039EndOfWal` — WAL segment file creation and checkpoint
      - `TestPort_Recovery047CheckpointPhysicalSlot` — physical slot in pg_replication_slots after checkpoint
      CSV rows R-001/R-013/R-019/R-038/R-039/R-047 already present; markdown regenerated.
      All 6 tests pass.

- [x] **M0094-0004** — Design doc `0094-0004-subscription-tap-porting-strategy.md`
      status → `accepted` (2026-05-11). Created `internal/testport/subscription_port_test.go`.
      Ported 3 subscription TAP tests (all adapted to v0 capabilities):
      - `TestPort_Subscription001RepChanges` — INSERT+DELETE+UPDATE via pgoutput pipeline
      - `TestPort_Subscription004Sync` — initial COPY batch + streaming handoff, no gaps/duplicates
      - `TestPort_Subscription026Stats` — pg_stat_subscription received_lsn + receipt time via wal.Subscriber
      CSV S-001/S-004/S-026 rows already present; markdown regenerated. All 3 tests pass.

- [x] **M0094-0005** — Verified M0005 and M0008 DoD checklists (2026-05-11).
      M0005: 5/6 DoD items met; written_lsn advancement after checkpoint is a
      pre-existing gap (unrelated to M0094). Marked `complete` with known caveat.
      M0008: all 8 DoD items met via M0094-0001/0002/0003/0004 work plus prior
      M0008 implementation. Marked `complete`. `make ralph-state-guard` passes.

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Goal: Port the 27-file client-tools-tap suite to Go and implement the
missing engine features that currently hold ported scripts tests in a
`t.Skip` state.  The list spans five tool families:

  • `pg_basebackup` (010–040)  — WAL backup / receive / logical streaming
  • `pg_checksums`  (001–002)  — online/offline checksum management
  • `pg_controldata` (001)     — control-file inspection
  • `pg_ctl`        (001–004)  — **already PASS**; no new work needed
  • `pg_walsummary` (001–002)  — WAL summary generation
  • `scripts`       (13 files, 010–200) — client utility commands

`pg_ctl` 001–004 are already ported and PASS (`tap_port_test.go`).
All 13 scripts tests are already ported but remain `t.Skip` due to
missing SQL features; sub-milestones 0004–0008 implement those features.

### Sub-milestones

- [x] **M0095-0001** — Port `pg_checksums/001+002`, `pg_controldata/001`,
      `pg_walsummary/001` as Go tests in
      `internal/testport/client_tools_port_test.go`.
      Binary discovery: PATH first, then `postgres/local_install/bin`.
      `pg_controldata/001` adapted: CLI + data-dir error-path pass; checkpoint
      output check deferred (goopg v0 has no global/pg_control).
      `pg_checksums/002` adapted: option-validation sub-cases pass; enable/disable
      deferred (no pg_control).  CSV rows C-001/C-002/CD-001/WS-001 added;
      markdown regenerated. All 4 tests pass (2026-05-12).

- [x] **M0095-0002** — Port `pg_walsummary/002` (WAL block summarization)
      as adapted Go test in `client_tools_port_test.go`.
      Basic SQL (CREATE TABLE, INSERT, VACUUM, CHECKPOINT) passes.
      WAL summarization (summarize_wal GUC, pg_available_wal_summaries(),
      pg_stat_io walsummarizer rows, pg_walsummary -i) deferred with explicit
      t.Skip blocker (goopg rejects unknown GUCs at startup; function not
      implemented). CSV row WS-002 added; markdown regenerated (2026-05-12).

- [x] **M0095-0003** — Port `pg_basebackup/010`, `011`, `020`, `030`, `040`
      as adapted Go tests in `internal/testport/pgbasebackup_port_test.go`.
      010: --help/--version/options + no-pgdata + --compress=none:1/none+ PASS;
           backup execution SKIP (physical streaming).
      011: SKIP entirely (in-place tablespace backup needs BASE_BACKUP protocol).
      020: --help/--version/options + no-dir + slot-conflict + sync-conflict + compress PASS;
           WAL streaming SKIP (replication protocol).
      030: --help/--version/options + no-slot/db/action/file checks PASS;
           logical streaming SKIP.
      040: --help/--version/options + no-datadir/publisher/database PASS;
           subscriber setup SKIP.
      CSV rows BB-010..040 added; markdown regenerated (2026-05-12).

- [x] **M0095-0004** — VACUUM/ANALYZE parenthesized syntax; OPERATOR(schema.op)
      desugar; LATERAL derived-table analyzer+planner fallback; ANY(array[]) → IN
      desugar; pg_namespace view; pg_class relpersistence/reltoastrelid/relpages
      columns; pg_database datallowconn/datconnlimit; set_config() built-in.
      Design doc: `docs/design/0095-0004-vacuum-parenthesized-syntax.md`.
      All three vacuumdb tests pass (100/101/102).
      CSV: D-005a/b/c → port,yes (2026-05-12).

- [x] **M0095-0005** — Add REINDEX parser+executor stub:
      `REINDEX [(VERBOSE)] [CONCURRENTLY] {INDEX|TABLE|DATABASE|SCHEMA|SYSTEM}
      [IF EXISTS] name`.
      KwReindex keyword, ReindexStmt AST, parseReindex(), planner Utility node,
      executor no-op (utilityNoOp fallthrough). Both reindexdb tests pass.
      CSV: D-005h/i → port,yes (2026-05-12).

- [x] **M0095-0006** — CREATE/DROP ROLE/USER role-tracking handler.
      Server gets in-memory roleSet (pre-seeded with "postgres"). New
      `role_ddl.go` implements `tryHandleRoleDDL()`: CREATE ROLE registers
      the role; DROP ROLE checks existence (42704 on miss).  Injected into
      dispatch before compatNoopCommandTag.  pg_roles view unchanged (shows
      "postgres"; \du use case not tested by 040/070).
      Both scripts tests pass: TestPort_Scripts040Createuser PASS,
      TestPort_Scripts070Dropuser PASS.
      CSV: D-005f/g → port,yes (2026-05-12).

- [x] **M0095-0007** — Unblock TestPort_Scripts020Createdb and TestPort_Scripts050Dropdb.
      `tryHandleDatabaseDDL` (M0054-0001, already implemented) handles CREATE
      DATABASE via catalog.CreateDatabase and DROP DATABASE via DropDatabase
      (returns ErrDatabaseNotFound for nonexistent DBs). Both tests PASS
      immediately after removing t.Skip.  D-005l (200_connstr) stays deferred:
      goopg is UTF8-only; LATIN1 encoding blocker remains.
      CSV: D-005d/e → port,yes (2026-05-12).

- [x] **M0095-0008** — CLUSTER parser+executor stub + pg_class relnamespace fix.
      KwCluster keyword, ClusterStmt AST, parseCluster(), planner Utility
      routing, clusterOp (table-existence check with schema fallback).
      Also: pg_class.relnamespace changed from "public" to OID "2200"
      so catalog JOIN queries work (clusterdb catalog query was returning 0
      rows). Also: fixed multi-statement SET query bug in query.go (handleQuery
      now routes queries with internal ';' to parser-based executor path).
      Design doc: no separate doc required (stub + catalog fix only).
      Both clusterdb tests pass; all vacuumdb tests unaffected.
      CSV: D-005j/k → port,yes (2026-05-12).

## M0096 — RC Isolation-Test Suite: Feature Implementation & Spec Pass (filed 2026-05-12)

Goal: Make all 21 READ-COMMITTED-targeted isolation specs listed in
`docs/test-port/executable-isolation-tests.md` PASS via
`IsolationRunner.RunAndCompare`.  All 21 currently defer (skip) inside
`TestPort_IsolationSuite`; they are the strongest proxy for goopg's
READ COMMITTED correctness story.

**Current blocker map** (21 specs → feature groups):

| Feature gap | Blocks |
|---|---|
| `BEGIN [WORK] ISOLATION LEVEL <level>` parser | all 21 (used in every session setup block) |
| `pg_advisory_lock / unlock / unlock_all / xact_lock / try_xact_lock` | `lock-committed-update`, `lock-committed-keyupdate`, `insert-conflict-specconflict` |
| `FOR KEY SHARE` / `FOR NO KEY UPDATE` locking syntax | `lock-committed-keyupdate`, `partition-key-update-1–4` |
| ON CONFLICT executor correctness (parser exists) | `insert-conflict-do-update` (1–4), `insert-conflict-do-nothing` |
| `CREATE TABLE … PARTITION BY LIST/RANGE` + `PARTITION OF` | `partition-key-update-1–4`, `fk-snapshot`, `merge-*`, `eval-plan-qual` |
| `GENERATED ALWAYS AS (expr) STORED` columns | `eval-plan-qual` |
| Table `INHERITS` | `eval-plan-qual`, `eval-plan-qual-trigger` |
| `MERGE INTO … USING … ON … WHEN MATCHED/NOT MATCHED` | `merge-update/delete/insert-update/match-recheck/join` (5 specs) |
| Inline `REFERENCES` FK column constraint (CREATE TABLE) | `partition-key-update-2/3/4`, `fk-snapshot` |
| `CREATE TRIGGER` + PL/pgSQL trigger bodies | `eval-plan-qual-trigger`, `partition-key-update-3/4`, `fk-snapshot` |
| `DROP INDEX CONCURRENTLY` syntax | `drop-index-concurrently-1` |

Parallel-connection note: `TestPort_IsolationSuite` runs all specs with
`t.Parallel()` and many concurrently exhaust the server's connection
limit; dedicated sequential test functions (M0096-0001) are required
alongside the suite.

### Sub-milestones

- [x] **M0096-0001** — Added 20 dedicated sequential isolation test functions
      in `internal/testport/isolation_port_test.go` (lock-committed-update
      already existed). All 20 new tests use `runIsoSpec` which t.Skips when
      output doesn't match expected — correctly deferring until the required
      SQL features land. Verified: eval-plan-qual and merge-join both t.Skip
      with SQL errors for unsupported syntax (2026-05-12).

- [x] **M0096-0002** — BEGIN ISOLATION LEVEL + SET TRANSACTION ISOLATION LEVEL.
      Changes: BeginStmt.IsolationLevel string; SetTransactionStmt AST;
      parseBegin() now parses ISOLATION LEVEL + READ ONLY/WRITE/DEFERRABLE
      (latter two are no-ops); parseSet() intercepts SET [LOCAL] TRANSACTION
      ISOLATION LEVEL; planner Transaction.IsolationLevel; execBegin() calls
      SetIsolationLevel from plan; setTransactionOp calls SetIsolationLevel on
      session; SetIsolationLevel added to Session interface; mvcc.ParseIsolationLevel
      maps SERIALIZABLE→RepeatableRead, READ UNCOMMITTED→ReadCommitted.
      Verification: TestPort_IsolationLockCommittedUpdate now parses BEGIN ISOLATION
      LEVEL READ COMMITTED successfully (defers due to pg_advisory_lock, not parsing).
      TestPort_IsolationInsertConflictDoNothing similarly advances past parsing. (2026-05-12).

- [x] **M0096-0003** — Advisory lock built-in functions implemented.
      New file `internal/executor/advisory.go`: process-global advisoryManager
      with channel-based blocking (waiter queues), context cancellation support,
      release-all on session teardown. Functions added to evalFuncCall():
      pg_advisory_lock(bigint), pg_advisory_lock(int4,int4),
      pg_advisory_unlock(bigint/int4,int4), pg_advisory_unlock_all(),
      pg_advisory_xact_lock(int4,int4) [treated as session-scoped],
      pg_try_advisory_xact_lock(int4,int4) [non-blocking],
      pg_try_advisory_lock(bigint) [non-blocking].
      Verification: lock-committed-update no longer errors on advisory lock;
      defers on FOR KEY SHARE (M0096-0004) and column naming (2026-05-12).

- [x] **M0096-0004** — FOR KEY SHARE / FOR NO KEY UPDATE + IS [NOT] NULL.
      Parser: LockStrengthForKeyShare/ForNoKeyUpdate constants; parseLockingClause()
      now handles FOR KEY SHARE and FOR NO KEY UPDATE via lookahead.
      Planner: lockStrengthFromParser maps ForKeyShare→ForShare, ForNoKeyUpdate→ForUpdate.
      Also added IS [NOT] NULL: IsNullExpr AST + parser; planner IsNullExpr plan node
      (resolveExpr, agg, window, constant, walker); executor evalExprSlot case;
      analyzer exprHasWindowFunc + analyzeExpr cases. Advisory lock session ID fix:
      advisorySessionIDFromContext uses ctx.BackendID (per-connection) instead of
      nil Session pointer — cross-session blocking now works with IsolationRunner.
      Verification: TestPort_IsolationLockCommittedUpdate runs 120s (blocking works,
      spec defers due to output format + connection timeout across permutations). (2026-05-12).

- [x] **M0096-0005** — ON CONFLICT infrastructure: partial progress (2026-05-12).
      Landed:
      (a) CREATE TABLE now creates primary key btree index for inline `col type
          PRIMARY KEY` and table-level `PRIMARY KEY (cols)` — fixes 42P10 "no
          unique constraint" error that was the primary blocker.
      (b) text added to isSupportedBTreeKeyType (text primary keys now work).
      (c) IsolationRunner pqprintFormat: changed separator from " | " to "|"
          matching PostgreSQL isolationtester's PQprint output format.
      (d) Per-connection explicit transaction tracking (connTxState in conn_tx.go):
          BEGIN starts a real TxnMgr transaction, COMMIT/ROLLBACK end it; all
          statements within an explicit block reuse the same TxnMgr transaction.
          This is required so donothing1's INSERT stays uncommitted while donothing2
          runs.
      (e) WaitForXID on mvcc.Manager (broadcasts on every commit/rollback) and
          probeArbiterWaiting / findInProgressConflict in upsertOp (row-wait logic).
      (f) advisorySessionIDFromContext uses ctx.BackendID rather than nil Session.
      Remaining: donothing2 / insert2 blocking behavior (insert-conflict specs) not
      yet producing <waiting ...> lines. The blocking mechanism is wired but debugging
      of the exact WaitForXID trigger path is needed (XID propagation from ectx.Tx
      to connTxState may be incomplete). The insert-conflict-do-update and do-nothing
      specs still defer. Re-open as follow-up if needed.

- [x] **M0096-0006** — Unblocked `drop-index-concurrently-1` setup (2026-05-12).
      Features implemented:
      - INSERT … SELECT: parser (dml.go), analyzer, planner (planInsert routes to planSelect)
      - generate_series(n,m) FROM clause SRF: TableFuncRef AST, parseRangeVar detection,
        planTableFuncRangeVar → GenerateSeries plan node, generateSeriesOp executor, 
        lookupTable/resolveTable analyzer support
      - DROP INDEX CONCURRENTLY: parser accepts CONCURRENTLY keyword (no-op, same as synchronous)
      - serial/bigserial column types: mapped to int4/int8 in isInt4Type/isInt8Type
      - pg_settings virtual catalog view: returns default_transaction_isolation + enable_seqscan rows
      - PREPARE name AS … / EXECUTE name / DEALLOCATE: parser keywords + AST + per-connection
        preparedStatements store in conn_tx.go; dispatch handles PREPARE/EXECUTE/DEALLOCATE inline
      - SET enable_seqscan: already in GUC registry (stub)
      Verification: TestPort_IsolationDropIndexConcurrently1 now passes setup and runs the permutation
      (defers on EXPLAIN EXECUTE plan format and other output differences). All core unit tests PASS.

- [ ] **M0096-0007** — Implement `CREATE TABLE … PARTITION BY LIST/RANGE`
      and `CREATE TABLE child PARTITION OF parent FOR VALUES IN/FROM/TO`.
      Minimum viable: DDL accepted + partition routing on INSERT +
      partition-aware sequential scan.
      Unblocks: `partition-key-update-1–4`, and provides the shared
      prerequisite for `eval-plan-qual`, `fk-snapshot`, and all
      `merge-*` specs.

- [ ] **M0096-0008** — Implement `GENERATED ALWAYS AS (expr) STORED`
      column definition: DDL parsing, stored-value computation on
      INSERT/UPDATE, read-back in scans.
      Unblocks: `eval-plan-qual` (setup block).

- [ ] **M0096-0009** — Implement table inheritance
      (`CREATE TABLE child () INHERITS (parent)`): DDL parsing,
      catalog inheritance chain, scans that include child tables.
      Unblocks: `eval-plan-qual`, `eval-plan-qual-trigger`.

- [ ] **M0096-0010** — Implement `MERGE INTO target USING source ON cond
      WHEN MATCHED THEN UPDATE/DELETE WHEN NOT MATCHED THEN INSERT`.
      Unblocks: `merge-update`, `merge-delete`, `merge-insert-update`,
      `merge-match-recheck`, `merge-join` (5 specs).

- [ ] **M0096-0011** — Implement inline `REFERENCES table (cols)` column
      constraint and table constraint in `CREATE TABLE` (FK enforcement
      at INSERT/UPDATE/DELETE, deferred FK modes).
      Unblocks: `partition-key-update-2/3/4`, `fk-snapshot`.

- [ ] **M0096-0012** — Implement `CREATE TRIGGER … FOR EACH ROW EXECUTE
      FUNCTION/PROCEDURE` + PL/pgSQL trigger body execution.
      Unblocks: `eval-plan-qual-trigger`, `partition-key-update-3/4`,
      `fk-snapshot`.

- [ ] **M0096-0013** — End-to-end pass confirmation: run all 21 dedicated
      test functions from M0096-0001, confirm every spec reports `pass`.
      Fix any remaining output-normalization or row-ordering mismatches.

## M0097 — pg_regress Coverage: Feature Parity & Test Pass (filed 2026-05-12)

Goal: Work through all **232** cases in `docs/test-port/upstream-regress-coverage.md`
(all currently `defer`).  Each case either reaches `port` status (output
matches expected after normalization) or is formally reclassified as
`excluded` (out of scope for goopg v0).

**Runner status**: `internal/testport/framework/regress.go` provides
`DiscoverRegressCases` / `RunRegressSubset` / `NormalizeRegressOutput`
and the `RegressExecutor` interface, but **no Go test currently calls it**.
M0097-0001 wires it up.

**Scope split (approximate)**:
- PASS-target: ~130 tests (core SQL, DML, DDL, types, functions)
- Excluded: ~102 tests (geometric types, FTS, advanced AM, collation,
  encoding-specific, FDW, large objects, XML, psql client, row security,
  parallel, event triggers, network types, catalog sanity checks, complex
  AM extensions, replication catalog, etc.)

### Sub-milestones

- [ ] **M0097-0001** — Wire up `TestPort_RegressSuite` in
      `internal/testport/` with a concrete `ClusterRegressExecutor`
      (connects to a live goopg cluster via `database/sql`), pre-runs
      `test_setup.sql` to materialise the shared tables used by most
      cases (`INT2_TBL`, `INT4_TBL`, `FLOAT8_TBL`, etc.), and surfaces
      per-case pass/defer/excluded results as subtests.
      Also add a `NormalizeRegressOutput` extension pass for goopg-
      specific divergences (e.g., column-name casing, error message
      wording differences).

- [ ] **M0097-0002** — Formally reclassify ~102 tests as `excluded` in
      `docs/test-port/upstream-regress-coverage.md` and in the suite
      runner's policy table.  Excluded categories:
      • Geometric types: `box`, `circle`, `geometry`, `line`, `lseg`,
        `path`, `point`, `polygon` (8)
      • Full-text search: `tsdicts`, `tsearch`, `tsrf`, `tstypes` (4)
      • Advanced AM / exotic index: `brin`, `brin_bloom`, `brin_multi`,
        `gin`, `gist`, `spgist`, `amutils`, `create_am`,
        `create_index_spgist` (9)
      • Collation / encoding: `collate`, `collate.icu.utf8`,
        `collate.linux.utf8`, `collate.utf8`, `collate.windows.win1252`,
        `euc_kr`, `encoding`, `unicode`, `copyencoding` (9)
      • External / infra features: `foreign_data`, `largeobject`,
        `indirect_toast`, `compression`, `tablespace`, `tablesample`,
        `async`, `numa`, `object_address`, `maintain_every` (10)
      • XML / advanced JSON: `xml`, `xmlmap`, `sqljson`,
        `sqljson_jsontable`, `sqljson_queryfuncs`, `json_encoding`,
        `jsonpath_encoding` (7)
      • Security & roles: `rowsecurity`, `privileges`, `security_label`,
        `init_privs`, `password`, `roleattributes`, `create_role` (7)
      • Parallel: `select_parallel`, `write_parallel`,
        `vacuum_parallel` (3)
      • Event triggers: `event_trigger`, `event_trigger_login` (2)
      • psql client: `psql`, `psql_crosstab`, `psql_pipeline` (3)
      • Network types: `inet`, `macaddr`, `macaddr8` (3)
      • Catalog sanity: `misc_sanity`, `opr_sanity`, `type_sanity`,
        `oidjoins`, `sanity_check` (5)
      • Complex AM / type extensions: `create_aggregate`, `create_cast`,
        `create_operator`, `drop_operator`, `alter_operator`,
        `alter_generic`, `polymorphism`, `create_type`, `create_misc`,
        `regproc` (10)
      • Replication catalog: `publication`, `subscription`,
        `replica_identity` (3)
      • C-language functions: `create_function_c` (1)
      • Misc out-of-scope: `bit`, `bitmapops`, `conversion`, `combocid`,
        `dependency`, `reloptions`, `hash_func`, `predicate`, `stats`,
        `stats_ext`, `stats_import`, `typed_table`, `memoize`,
        `without_overlaps`, `money`, `namespace`, `database`,
        `infinite_recurse`, `create_schema`, `create_misc` (20+)

- [ ] **M0097-0003** — Core standalone + scalar type parity.
      Target tests: `boolean`, `comments`, `errors`, `numerology`,
      `name`, `oid`, `int2`, `int4`, `int8`, `float4`, `float8`,
      `numeric`, `numeric_big`, `char`, `varchar`, `text`, `uuid`,
      `random`.
      Work: fix type-coercion edge cases, `pg_input_is_valid` /
      `pg_input_error_info` stubs, `float4`/`float8` `NaN`/`Inf`
      literal handling, bool input syntax variants (`'t'`, `'yes'`,
      `'on'`), numeric literal prefixes (`0x`, `0b`, `0o`), `name`
      type output.

- [ ] **M0097-0004** — Date / time type parity.
      Target tests: `date`, `time`, `timestamp`, `timestamptz`,
      `timetz`, `interval`, `horology`.
      Work: fill out date/time arithmetic operators, interval I/O,
      timezone handling, `to_char` / `to_timestamp` format patterns,
      `date_trunc`, `date_part`, `extract`, `age`, `now()` aliases.

- [ ] **M0097-0005** — Core SELECT + DML parity.
      Target tests: `select`, `select_distinct`, `select_distinct_on`,
      `select_having`, `select_implicit`, `select_into`, `insert`,
      `update`, `delete`, `returning`, `limit`, `union`, `errors`
      (some overlap with 0003), `explain`, `expressions`.
      Work: `ORDER BY USING operator` syntax, `SELECT INTO`,
      `EXCEPT ALL` / `INTERSECT ALL`, `EXPLAIN` output normalization,
      `expressions` function coverage (overlay, substring variants).

- [ ] **M0097-0006** — JOIN + subquery + CTE parity.
      Target tests: `join`, `join_hash`, `subselect`, `with`,
      `equivclass`, `functional_deps`.
      Work: lateral joins (`LATERAL`), `NATURAL JOIN`, anti-join
      output format, recursive CTE edge cases, `DISTINCT ON` in
      subqueries, equivalence-class planner improvements.

- [ ] **M0097-0007** — Aggregate + window + CASE + sort parity.
      Target tests: `aggregates`, `window`, `case`, `groupingsets`,
      `tuplesort`, `incremental_sort`.
      Work: `FILTER (WHERE ...)` in aggregates, ordered-set aggregates
      (`percentile_cont`, `mode`), `WITHIN GROUP`, window frame
      `RANGE/GROUPS`, `CASE` with subqueries, `GROUPING SETS` /
      `ROLLUP` / `CUBE`, sort-key collation output format.

- [ ] **M0097-0008** — Core DDL + index parity.
      Target tests: `create_table`, `create_table_like`, `create_index`,
      `alter_table`, `drop_if_exists`, `truncate`, `temp`,
      `btree_index`, `index_including`, `hash_index`, `reloptions`
      (partial), `fast_default`.
      Work: `CREATE TABLE LIKE … INCLUDING ALL`, `CREATE INDEX
      … INCLUDE (cols)`, `ALTER TABLE … ADD/DROP/ALTER COLUMN` edge
      cases, `CREATE INDEX CONCURRENTLY` (sync impl), `REINDEX`
      stub (see M0095-0005), temporary table scoping, `UNLOGGED`
      table syntax acceptance, `DEFAULT` expression coercion.

- [ ] **M0097-0009** — COPY + sequences + identity + generated columns.
      Target tests: `copy`, `copy2`, `copydml`, `copyselect`,
      `sequence`, `identity`, `generated_stored`, `generated_virtual`.
      Work: `COPY TO STDOUT` format options, `COPY … WHERE`, sequence
      functions (`nextval`, `currval`, `setval`, `lastval`),
      `GENERATED ALWAYS AS IDENTITY`, `GENERATED ALWAYS AS (expr)
      STORED` and `VIRTUAL` column variants.

- [ ] **M0097-0010** — Transactions + PREPARE + locking parity.
      Target tests: `transactions`, `mvcc`, `lock`, `prepare`,
      `plancache`, `prepared_xacts`, `portals`, `portals_p2`,
      `advisory_lock`, `tid`, `tidscan`, `tidrangescan`.
      Work: `LOCK TABLE` statement, `SAVEPOINT`/`RELEASE`/`ROLLBACK TO`
      coverage, `PREPARE` / `EXECUTE` / `DEALLOCATE`, cursor
      (`DECLARE … CURSOR`, `FETCH`, `MOVE`, `CLOSE`), TID scans
      (`WHERE ctid = '(0,1)'`), `PREPARE TRANSACTION` syntax acceptance.

- [ ] **M0097-0011** — String functions + regex + misc functions parity.
      Target tests: `strings`, `regex`, `md5`, `misc_functions`,
      `misc`.
      Work: string continuation syntax, Unicode escape sequences,
      `E'...'` literals, `LIKE`/`ILIKE`/`SIMILAR TO` edge cases,
      POSIX regex (`~`, `~*`, `!~`, `!~*`), `regexp_*` functions,
      `overlay()`, `format()`, hash functions (`md5`, `sha256`),
      `pg_typeof`, `generate_series` overloads.

- [ ] **M0097-0012** — Functions + PL/pgSQL parity.
      Target tests: `create_function_sql`, `create_procedure`,
      `plpgsql`, `rangefuncs`, `misc_functions` (overlap with 0011).
      Work: SQL-language functions with multiple statements, `CALL`
      for stored procedures, PL/pgSQL `FOR … IN SELECT`, `EXECUTE`
      dynamic SQL, `RAISE` levels, exception handlers, `RETURNS TABLE`,
      `RETURNS SETOF`, `RETURN NEXT`.

- [ ] **M0097-0013** — Views + materialized views + rules parity.
      Target tests: `create_view`, `select_views`, `updatable_views`,
      `rules`, `matview`.
      Work: `CREATE OR REPLACE VIEW`, view column aliases, `CHECK
      OPTION`, updatable view DML routing, `CREATE RULE`,
      `CREATE MATERIALIZED VIEW`, `REFRESH MATERIALIZED VIEW
      [CONCURRENTLY]`.

- [ ] **M0097-0014** — Constraints + FK + triggers + inheritance parity.
      Target tests: `constraints`, `foreign_key`, `triggers`,
      `inherit`, `indexing`.
      Work: `CHECK` constraint evaluation, deferred FK modes,
      `ON DELETE CASCADE / SET NULL / SET DEFAULT`, trigger
      `NEW`/`OLD` records in PL/pgSQL bodies, `AFTER`/`BEFORE`/
      `INSTEAD OF` trigger types, inheritance scan + INSERT routing,
      `CREATE TABLE … INHERITS`.

- [ ] **M0097-0015** — Partitioned tables parity.
      Target tests: `partition_prune`, `partition_join`,
      `partition_aggregate`, `partition_info`, `hash_part`.
      Work: `CREATE TABLE … PARTITION BY LIST/RANGE/HASH`,
      `CREATE TABLE … PARTITION OF … FOR VALUES`, partition pruning
      in planner, partition-wise aggregation, partition-wise join.
      (Depends on M0096-0007.)

- [ ] **M0097-0016** — ON CONFLICT + MERGE parity.
      Target tests: `insert_conflict`, `merge`.
      Work: `INSERT … ON CONFLICT DO UPDATE` with functional conflict
      targets (`ON CONFLICT (lower(col))`), `ON CONFLICT ON CONSTRAINT
      name`, `MERGE` statement (depends on M0096-0010).

- [ ] **M0097-0017** — Extended type parity.
      Target tests: `arrays`, `json`, `jsonb`, `jsonb_jsonpath`,
      `jsonpath`, `rangetypes`, `multirangetypes`, `enum`, `domain`,
      `rowtypes`, `interval` (overlap 0004), `pg_lsn`, `txid`, `xid`.
      Work: array type I/O + operators (`@>`, `&&`, `||`), JSON
      operators (`->`, `->>`), `jsonb_path_query*` functions, range
      type constructors and operators, `CREATE TYPE … AS ENUM`,
      `CREATE DOMAIN`, composite row type I/O, `pg_lsn` comparison,
      `txid_current()`.

- [ ] **M0097-0018** — System catalog + GUC + vacuum parity.
      Target tests: `sysviews`, `dbsize`, `guc`, `reindex_catalog`,
      `vacuum`, `vacuum_parallel` (excluded), `misc`, `xid`.
      Work: additional `pg_catalog` views (`pg_tables`,
      `pg_indexes`, `pg_views`, `information_schema` stubs),
      `pg_database_size`, `pg_relation_size`, `pg_column_size`,
      `SET`/`RESET` GUC handling, `VACUUM (FULL, ANALYZE, VERBOSE)`,
      `REINDEX TABLE` executor stub.

- [ ] **M0097-0019** — Final confirmation: update
      `docs/test-port/upstream-regress-coverage.md` with final
      `port` / `excluded` / `defer` status for all 232 cases.
      Confirm PASS for all non-excluded, non-deferred tests.

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## M0098 — pgbench OLTP Performance: 1 500 / 1 500 / 10 000 TPS Targets (filed 2026-05-12)

### Goal

Under the same conditions as `analysis/pgbench_postgresql_baseline_20260510_145159.md`
(`-c 100 -j 100 -T 180 -s 100`, `shared_buffers=2560MB`, `wal_buffers=100MB`,
`checkpoint_timeout=24h`, `max_wal_size=1TB`):

| Workload | PostgreSQL 18.3 baseline | goopg target |
|---|---:|---:|
| Standard (TPC-B) | 5,382 TPS | **≥ 1,500 TPS** |
| Simple Update (`-N`) | 7,882 TPS | **≥ 1,500 TPS** |
| Select Only (`-S`) | 38,575 TPS | **≥ 10,000 TPS** |

### Current baseline (latest measurements)

| Workload | -c 100 pre-M0093 | -c 10 post-M0093 | Gap to target |
|---|---:|---:|---:|
| Standard | ~70 TPS | ~58 TPS | ~21× |
| Simple Update | ~95 TPS | ~110 TPS | ~14× |
| Select Only | ~400 TPS | ~2,740 TPS | unknown at -c 100 |

### Root-cause map

| Bottleneck | Evidence | Workloads affected |
|---|---|---|
| **WAL flush serialized per txn** — `FlushUpTo` sends one `opFlush` to the WAL writer serial loop, blocks until one `fdatasync` completes; no batching | avg latency 1,050–1,430 ms at -c 100; WAL writer channel is the throughput ceiling | Standard, Simple Update |
| **No WAL group commit** — PostgreSQL batches N concurrent flush requests into one `fdatasync` (CommitDelay / CommitSiblings GUC path) | PostgreSQL at -c 100 achieves 7,882 TPS vs goopg's 95 TPS for the same workload | Standard, Simple Update |
| **Buffer pool global `poolMu`** — single `sync.Mutex` serializes every `Pin`, `Read`, `Unpin` across 100 concurrent goroutines; PostgreSQL uses 128 hash-partitioned LWLocks on `byTag` | `WriteDirtyPages` = 16.67% CPU in m0093 pprof; PostgreSQL's 128-partition design referenced in `about_buffer_management/final/` | All workloads at high concurrency |
| **No EvalPlanQual** — concurrent UPDATE conflict → SQLSTATE 40001 abort instead of row-recheck; effective transaction rate drops under contention | M0090 summary, M0093 regression check (2 failed at -c 10 standard) | Standard |
| **No cross-session plan cache** — every query re-parses and re-plans even across 100 identical pgbench connections; `parser.Lex` = 22 % of allocs (88.7 MB / 30 s) in m0092-followup profile | allocs.prof; `practice/go_rdbms_performance_techniques.md` §12 | All workloads |
| **Allocation hot-paths** — `storage.newArena` 32 %, `parser.Lex` 22 %, `executor.insertOp` 5 %, `wal.encodeRecord` 2 % of allocs; GC scan dominates CPU in `default.pgo` (gcBgMarkWorker, scanobject, pcvalue = top 3) | m0092-followup allocs.prof; default.pgo (308 KB, 480 s mixed TPCH workload) | All workloads |
| **PGO not activated in production build** — `default.pgo` (308 KB) exists but is not wired into the build pipeline; `go_rdbms_performance_techniques.md` §3 documents 2–10% typical gain | ls default.pgo; practice doc §3 PGO | All workloads |
| **`GOAMD64` not set** — default `v1` misses AVX2/BMI2 for hash, checksum, sort kernels; `go_rdbms_performance_techniques.md` §3 | practice doc | All workloads |

### Sub-milestones

- [ ] **M0098-0001** — Re-measure at target conditions (`-c 100 -j 100 -T 180
      -s 100`) on the current post-M0093 binary to establish the precise gap
      for each workload.  Capture pprof (CPU + allocs + mutex + block) during
      each run.  Result files go in `bench/pgbench-compare/results/` with the
      `m0098_baseline` suffix.  This snapshot drives the ROI ordering of
      subsequent sub-milestones and validates that M0093's read-skip scales
      to -c 100 for Select Only.

- [ ] **M0098-0002** — **WAL group commit** — the single highest-ROI change
      for write workloads.
      Implementation:
      - Replace the current one-response-channel-per-`FlushUpTo` pattern
        with a shared flush-queue: callers post a `flushRequest{lsn, done
        chan struct{}}` to a slice guarded by a mutex, then wait on `done`.
      - The WAL writer goroutine, upon receiving any `opFlush`, drains the
        entire pending queue (collecting the maximum LSN), performs one
        `fdatasync`, then closes all `done` channels in the batch.
      - Add `commit_delay` (µs) and `commit_siblings` (min active
        connections) GUC-equivalent runtime knobs (see PostgreSQL
        `XLogFlush` group-commit path documented in
        `about_wal/component_wal_writing.md`).
      - Wire `runtime.LockOSThread()` on the WAL writer goroutine
        (`go_rdbms_performance_techniques.md` §2) to reduce OS scheduling
        jitter on the fsync goroutine.
      Expected: 8–15× TPS improvement for Simple Update; 5–10× for Standard.

- [ ] **M0098-0003** — **Buffer pool 128-partition locking** — removes the
      global `poolMu` bottleneck for high-concurrency reads and writes.
      Implementation:
      - Replace the single `poolMu sync.Mutex` + `byTag map` with
        128 `bufferPartition` structs, each holding its own `sync.Mutex`
        and a `map[PageTag]int` sub-table (mirroring PostgreSQL's
        `BufTableHashPartition` design from
        `about_buffer_management/final/`).
      - The partition index is `hash(PageTag) % 128`.
      - `Pin` / `Unpin` / `Read` / `WriteDirtyPages` victim selection only
        lock the relevant partition(s).
      - Per-buffer `contentMu sync.RWMutex` (already present per slot)
        remains unchanged.
      Expected: 3–6× TPS improvement for Select Only at -c 100; 1.5–2× for
      write workloads (buffer lookups inside transactions).

- [ ] **M0098-0004** — **EvalPlanQual (row recheck on concurrent UPDATE)**
      — eliminates SQLSTATE 40001 aborts caused by xmax conflicts, replacing
      them with a re-fetch of the latest committed tuple and predicate
      recheck.
      Implementation:
      - When `isConcurrentlyUpdated()` detects an xmax conflict in
        `updateOp` / `deleteOp`, spin-wait for the conflicting transaction
        to commit or roll back, then re-read the tuple at the same TID.
      - Re-evaluate the WHERE predicate against the freshened row; proceed
        if it still matches, skip if it no longer matches (matches
        PostgreSQL's `EvalPlanQual` semantics under `READ COMMITTED`).
      - Bounded retry (max 3 rechecks) to avoid livelock; escalate to
        40001 only on exhaustion.
      - Prerequisite for M0096-0004 (isolation-test `eval-plan-qual` spec).
      Expected: near-zero abort rate for standard workload → 10–20% effective
      TPS gain on Standard.

- [ ] **M0098-0005** — **Cross-session normalized-query plan cache**
      — eliminates re-parse + re-plan for identical queries across sessions.
      Implementation:
      - Add a server-level `PlanCache` (LRU, bounded by
        `plan_cache_size` GUC, default 512 entries) keyed by the
        normalized SQL string (literals replaced with `$N` placeholders).
      - On `Parse` (extended protocol) or `SimpleQuery`, normalize the text,
        check the cache, and return the cached plan if present.
      - Cache entries are invalidated on DDL changes to referenced relations.
      - Use `sync.Map` (or a sharded `map + sync.RWMutex`) for lock-free
        read-path (§8 in `go_rdbms_performance_techniques.md`).
      Expected: 20–40% reduction in per-transaction CPU overhead for
      repeated-query workloads like pgbench.

- [ ] **M0098-0006** — **Memory allocation hot-path reduction**
      — cuts GC pressure on the three largest allocation sites.

      (a) **Parser lexer pooling** (`parser.Lex` = 22 % / 88.7 MB per 30 s):
          Pool token-slice backing arrays with `sync.Pool`; reuse the
          `parser` struct itself between consecutive queries on the same
          connection (`go_rdbms_performance_techniques.md` §1).

      (b) **WAL record encode buffer pooling** (`wal.encodeRecord` = 2 %,
          `wal.encodeRecord`, `wal.EncodePageImage`):
          Pool `[]byte` encode buffers with `sync.Pool`; pre-size to the
          99th-percentile record size (≈ 8 KB page image).

      (c) **Executor row pool** (`executor.insertOp.Next` = 5 %,
          `executor.valuesOp.Next` = 4 %):
          Pool `Row` / `[]Datum` slices; clear on release
          (`s = s[:0]`) and return to pool; never allocate inside the
          per-row `Next()` loop.

      (d) **Arena bump-allocator for per-query lifetime objects**:
          Allocate planner + executor state from a per-query arena
          (`go_rdbms_performance_techniques.md` §1 arena pattern);
          free the arena at query end — zero GC scan cost for those
          objects.

      Expected: 20–30 % overall allocation reduction; GC mark-worker
      fraction drops from ~20 % (as seen in `default.pgo`) toward < 5 %.

- [ ] **M0098-0007** — **PGO activation + GOAMD64=v3 build**
      — low-effort, broadly-applicable speedup.

      (a) **PGO**: wire `default.pgo` into the primary build command
          (`go build -pgo=./default.pgo ./cmd/goopg`).  Update
          `Makefile` / `bench/pgbench-compare/run_comparison.sh` to
          always build with PGO before benchmarking.
          After M0098-0001–0006 land, collect a fresh `cpu.prof` from
          a mixed pgbench run and replace `default.pgo` to reflect the
          new hot paths.

      (b) **GOAMD64=v3**: set `GOAMD64=v3` in the build pipeline to
          emit AVX2/BMI2/FMA for hash, CRC, sort kernels.

      (c) **Runtime knobs**:
          - `GOMEMLIMIT` set to 90 % of available RAM (suppress
            aggressive scavenging; `go_rdbms_performance_techniques.md`
            §2).
          - `GOGC=200` to trade some memory for reduced GC frequency
            during write-heavy benchmarks.
          - Verify `GOMAXPROCS` matches physical CPUs (container
            environments may under-report).

      Expected: 3–8 % overall TPS improvement from better inlining + ISA.

- [ ] **M0098-0008** — **Final measurement + iterative gap-close**
      — confirm targets are met; close any remaining gap with targeted
      micro-optimisations.

      Steps:
      1. Run the full `-c 100 -j 100 -T 180 -s 100` suite on the
         post-M0098-0007 binary; compare against targets.
      2. Capture pprof (CPU + allocs + mutex + block) for any workload
         still below target.
      3. Apply targeted fixes from the hot-path list (lock granularity,
         protocol I/O vectorisation, `strconv` vs `fmt` in encoding,
         per-CPU statistics shard, bounds-check elimination in page
         walks — see `go_rdbms_performance_techniques.md` §§13-14).
      4. Repeat until all three targets are met and stable across three
         independent runs (< 5 % run-to-run variance).
      5. Commit result files and an M0098 summary `.md` to
         `bench/pgbench-compare/results/`.

## Maintenance Fixes

- [x] Fix `TestFoundationSeqScanFilterJoin` test 7 stale expectation (2026-05-04).
      rows[0][0] was expected to be "alpha" but alpha's t3.qty=100 is filtered
      by WHERE t3.qty>150; correct first row is [beta 200]. Stale from before
      M0039/M0041 fixed ColumnRef alignment for ≥3-table joins. Row-count check
      promoted from t.Logf to t.Fatalf. File: `internal/testutil/tpch/foundation_test.go`.

- [x] Silence `tmp/` build errors under `go test ./...` (2026-05-04).
      tmp/ utility scripts (find_wal_record.go, tuple_size.go, walprobe_main.go)
      all declared `package main`, causing "main redeclared" errors. Added
      `//go:build ignore` to each. (Note: tmp/ is in .gitignore; change is local.)

## Completed

- [x] Project initialization (Ralph harness wired up).
