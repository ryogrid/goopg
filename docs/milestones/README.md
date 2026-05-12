# Milestones

This directory tracks scoped, sequential milestones for goopg. Each milestone has:

- A clearly bounded set of features to deliver.
- A set of design docs (under `../design/`) that must exist when the milestone is complete.
- A concrete Definition of Done with verifiable criteria.

Milestones are numbered with a 4-digit zero-padded prefix. Design-doc
filenames use `<milestone-or-spec-id>-NNNN-short-slug.md`: `root` for the
foundational requirements spec and the milestone ID (for example `0002`) for
milestone-scoped docs. `NNNN` is the per-identifier sequence.

The foundational requirements live in the top-level `REQUIREMENTS.md` at the repository root. That document plays the role of Milestone 0001 implicitly. New scope is captured in additional milestone documents in this directory.

## Status Values

- `planned` — Defined but not started.
- `in-progress` — Actively being implemented.
- `accepted` — Complete and merged. Definition of Done satisfied.
- `superseded` — Replaced by a later milestone. Document remains in place for history.
- `cancelled` — Abandoned, reason recorded inline.

When the agent begins work on a milestone, it must update the status field at the top of that milestone's file and update the table below in the same commit.

## Workflow Per Milestone

1. Read the milestone document and the relevant upstream sources under `./postgres/`.
2. Write the design docs listed under "Required Design Docs" first, with status `draft`.
3. Implement against those design docs. Update them to `accepted` when stable.
4. Verify every item in "Definition of Done".
5. Update milestone status to `accepted`.

## Index

| ID   | Title                                                  | Status   | Document                                          |
|------|--------------------------------------------------------|----------|---------------------------------------------------|
| 0001 | Foundational server (pgbench-able)                     | see root | `../../.ralph/specs/GOAL_AND_REQUIREMENTS.md`                           |
| 0002 | Production-grade checkpointing & concurrent B-tree     | planned  | `0002-durability-and-concurrent-storage.md`       |
| 0003 | HammerDB TPC-H workload coverage                       | planned  | `0003-tpch-workload.md`                           |
| 0004 | TAP test port & Go utility library                     | planned  | `0004-tap-test-port.md`                           |
| 0005 | Streaming replication support                          | planned  | `0005-streaming-replication-support.md`           |
| 0006 | Planner-grade statistics (MCV, histograms, cost-based join algo) | planned  | `0006-planner-statistics.md`                      |
| 0007 | WAL segment preallocation & `fdatasync`-based commit path        | planned  | `0007-wal-segment-preallocation.md`               |
| 0008 | Logical replication support                                      | planned  | `0008-logical-replication-support.md`             |
| 0009 | AIO subsystem (asynchronous I/O)                                 | planned  | `0009-aio-subsystem.md`                           |
| 0010 | WAL direct I/O writes & in-memory walsender handoff             | planned  | `0010-wal-direct-io-and-walsender-memory-handoff.md` |
| 0011 | B-tree NUMERIC key support                                       | planned  | `0011-btree-numeric-key-support.md`               |
| 0012 | Lock manager foundation & PostgreSQL-style deadlock detection   | planned  | `0012-lock-manager-and-deadlock-detection.md`     |
| 0013 | WAL buffers optimization with eviction-safe WAL-before-data durability | planned  | `0013-wal-buffers-optimization-with-eviction-safe-wal-before-data-durability.md` |
| 0014 | PostgreSQL-compatible WAL on-disk format                          | planned  | `0014-wal-compatibility-with-pg.md` |
| 0015 | PL/pgSQL stored routines (function-first delivery)                | planned  | `0015-plpgsql-stored-routines-function-first.md` |
| 0016 | WITH clause and CTE support                                       | planned  | `0016-with-clause-cte-support.md` |
| 0017 | UPSERT support (INSERT ... ON CONFLICT DO UPDATE)                 | planned  | `0017-upsert-on-conflict-do-update.md` |
| 0018 | EXPLAIN / EXPLAIN ANALYZE support                                 | planned  | `0018-explain-and-explain-analyze.md` |
| 0019 | Autovacuum support                                                 | planned  | `0019-autovacuum-support.md` |
| 0020 | Window function support (OVER, ROW_NUMBER, RANK, LAG/LEAD)       | planned  | `0020-window-functions-over-row-number-rank-lag-lead.md` |
| 0021 | Pessimistic row locking support (SELECT ... FOR UPDATE)          | planned  | `0021-pessimistic-lock-select-for-update.md` |
| 0022 | PostgreSQL-compatible `pg_stat_activity` support                 | planned  | `0022-pg-stat-activity-support.md` |
| 0023 | Comprehensive syntax integration test suite                      | planned  | `0023-comprehensive-syntax-integration-test-suite.md` |
| 0024 | Wait-event recording architecture: non-client-backend & cross-goroutine paths | planned  | `0024-wait-event-recording-architecture.md` |
| 0025 | OLTP performance analysis & bottleneck identification                      | accepted | `0025-oltp-performance-analysis.md` |
| 0026 | Concurrent WAL append & flush architecture                                  | accepted | `0026-concurrent-wal-append.md` |
| 0027 | Low-risk performance optimisations (readability-preserving)                  | planned  | `0027-readability-preserving-optimisations.md` |
| 0028 | goopg implementation reference: PostgreSQL logic documentation               | planned  | `0028-postgres-implementation-reference.md` |
| 0029 | HammerDB TPC-H end-to-end run on goopg                                      | planned  | `0029-hammerdb-tpch-run.md` |
| 0030 | Catalog persistence and DDL WAL                                             | planned  | `0030-catalog-persistence-and-ddl-wal.md` |
| 0031 | TPC-H Q2 memory estimation & GC leak code review                           | accepted  | `0031-tpch-q2-memory-analysis-and-gc-code-review.md` |
| 0032 | Buffer pool arena: mmap → Go heap replacement (incl. M0032-0005 HammerDA-shape load fix) | accepted   | `0032-buffer-pool-heap-arena.md` |
| 0033 | Planner-level subquery unnesting                                         | accepted    | `0033-subquery-unnesting.md` |
| 0034 | Bushy join tree / join-graph optimization                                | accepted     | `0034-bushy-join-optimization.md` |
| 0035 | Streaming hash join & bushy-unnest verification                          | accepted      | `0035-streaming-hash-join.md` |
| 0036 | Hash join lazy materialization (on-demand output)                        | accepted       | `0036-hash-join-lazy-materialization.md` |
| 0037 | Spill-to-disk hash join (Grace hash join)                                | accepted       | `0037-hash-join-spill-to-disk.md` |
| 0038 | Multi-way hash join                                                     | accepted        | `0038-multi-way-hash-join.md` |
| 0039 | Fix planner column-index alignment (correct join results)               | planned         | `0039-fix-planner-column-ref.md` |
| 0040 | Correlated subquery optimization (caching + IN-unnest)                  | accepted        | `0040-correlated-subquery-optimization.md` |
| 0041 | Close remaining TPC-H result-parity gaps                                | accepted        | `0041-close-parity-gaps.md` |
| 0042 | Align goopg's I/O subsystem with upstream PostgreSQL (buffered I/O, WAL writer, client backend) | planned | `0042-pg-io-alignment.md` |
| 0043 | MHJ executor optimisations (lazy iterator + predicate pushdown) | accepted | `0043-mhj-executor-optimisations.md` |
| 0044 | B-tree key support for HammerDB TPC-H schema types (varchar, char, timestamp, mixed compound) | planned | `0044-btree-tpch-key-types.md` |
| 0045 | Crash recovery from non-zero starting WAL segment       | planned  | `0045-wal-recovery-non-zero-start.md` |
| 0046 | Heap & MVCC maturation (HOT, FSM, VM, freezing, page pruning, TOAST) | planned | `0046-heap-mvcc-maturation.md` |
| 0047 | B-tree maturation: bulk load, page deletion, deduplication | planned | `0047-btree-maturation.md` |
| 0048 | Buffer pool concurrency hardening (IO_IN_PROGRESS, strategy ring, bgwriter, checkpoint pacing) | planned | `0048-buffer-pool-concurrency.md` |
| 0049 | Protocol parity: cancellation, error detail, SCRAM-SHA-256, COPY binary | planned | `0049-protocol-parity.md` |
| 0050 | Savepoints and subtransactions                                   | planned | `0050-savepoints-and-subtransactions.md` |
| 0051 | Planner expression-level improvements (constant folding, implicit coercion, keyword categorisation, LIKE→range) | planned | `0051-planner-expression-improvements.md` |
| 0052 | HammerDB TPC-H end-to-end regression on `perf-analysis` (oversized-message fix) | accepted | (tasks in `.ralph/fix_plan.md` — no separate milestone file; fixed via M0052-0001 + M0052-0002) |
| 0053 | HammerDB TPC-H complete run verification & English report | accepted (PARTIAL) | `0053-hammerdb-tpch-complete-run-verification.md` |
| 0054 | TPC-H performance & optimisation follow-through (closes M0053 deferrals: NLI, CREATE DATABASE WAL, EXPLAIN-driven index audit, pprof bottleneck survey, run-012 22/22 verification) | planned | `0054-tpch-performance-and-optimisation.md` |
| 0055 | Staged B-tree enhancement program (write-path CPU reduction, steady-state dedup, multi-writer split protocol, deletion/recycling hardening, external-sort index build) | planned | `0055-staged-btree-enhancement-program.md` |
| 0056 | Buffer-pool PinNew race & splitMu removal | planned | `0056-bufpool-pinnew-race-and-splitmu-removal.md` |
| 0057 | TPC-H measurement prerequisites (background-worker logging, checkpoint suppression, tpch-runner cancel, crash recovery) | planned | `0057-tpch-measurement-prerequisites.md` |
| 0058 | TPC-H SubPlan & join-unnesting performance fixes (non-correlated cache, EXISTS→semi-join, NUMERIC fast path, OR-of-ANDs join, TCP cancel) | planned | `0058-tpch-subplan-join-perf.md` |
| 0059 | Executor BorrowRow optimization (Volcano row-lifetime copy reduction) | planned | `0059-executor-borrowrow-optimization.md` |
| 0060 | PostgreSQL oracle test-port foundation (TAP/pg_regress/isolation/recovery/subscription/client-tools) | completed | `0060-postgres-oracle-test-port.md` |
| 0068 | Executor GC-Optimized Pipeline Refactor (compact Datum, TupleSlot, batch arena, slot pool — replaces BorrowSemantics) | accepted | `0068-executor-gc-pipeline-refactor.md` |
| 0069 | Executor slot-pipeline follow-through + GC + long-tail query fixes | accepted | `0069-executor-slot-pipeline-followthrough.md` |
| 0070 | Executor slot-pipeline completion + long-tail query closure | accepted | `0070-executor-slot-pipeline-completion.md` |
| 0071 | TPC-H correctness closure (planner-first) + slot-pipeline carry-forward (incl. M0071-0009 Q21 0→381 rows) | accepted | `0071-tpch-correctness-and-runtime-followup.md` |
| 0072 | TPC-H Q5/Q9 residual + slot-arena infra (M0072-0001 slot-aware BindOuter; M0072-0004 Arena type landed; M0072-0002 chained-NLI rebind reverted) | accepted | `0072-tpch-q5-q9-residual-and-slot-arena.md` |
| 0073 | OpCode int8 + Datum arena integration (Q5 heap −72 %: 1463 → 404 GB via arena wiring) | accepted | `0073-opcode-and-datum-arena-integration.md` |
| 0074 | CPU + numeric optimisation (M0074-0006 numericCmp/Add/Sub/Mul int64 fast-path; M0074-0001/0002/0003 forward-compat infra; mixed scope) | accepted | `0074-cpu-and-numeric-optimisation.md` |
| 0075 | TPC-H residual: Q5 plan-level / Q9 rebind / Datum packed / filter batch / numericDiv / build-toolchain (M0075-0005 numericDiv int64 FULL; 0001/0002/0003/0004/0007 PARTIAL — see Phase 7 handover) | accepted | `0075-tpch-residual-and-perf.md` |
| 0076 | M0075 carry-forward: arena retention audit + Datum packed re-attempt + filterOp batch wiring + cost-model refinement + Q9 chained-NLI + plan-snapshot regression harness | accepted (mixed scope) | `0076-m0075-carry-forward-and-plan-snapshot-harness.md` |
| 0077 | Q5 planner fix: binary-tree preservation + cost-model maturation (4-slice planner refactor per `docs/design/fix-for-q5/`) — Slice A local filter partition/attachment; Slice B filtered base-row estimates; Slice C build-side-aware 3-part hash-join cost; Slice D anchored equality synthesis | accepted | `0077-q5-planner-fix-binary-tree-and-cost-model.md` |
| 0078 | pgbench-compare re-validation post-M0079 catalog fix (re-run standard / simple-update / select-only against M0080-final binary; confirm the 0.86 TPS regression is resolved) | planned | `0078-pgbench-compare-revalidation.md` |
| 0079 | Catalog DDL WAL recovery (CREATE/DROP INDEX) + btree WAL parity (BtreeVacuum / BtreeUnlinkPage / BtreeNewRoot / BtreeMarkPageHalfDead) | accepted | `0079-catalog-and-btree-wal-recovery.md` |
| 0080 | Heap WAL parity (HeapFreeze + HeapUpdate / HeapMultiInsert / HeapVisible record infra) + persistent VM + persistent FSM | accepted | `0080-heap-wal-parity-and-vm-fsm-persistence.md` |
| 0081 | WAL record producer wiring (atomic HEAP_UPDATE, HEAP2_MULTI_INSERT, HEAP2_VISIBLE, BTREE_REUSE_PAGE, BTREE_META_CLEANUP, BTREE_MARK_PAGE_HALFDEAD) — replaces residual FPI paths with logical records | planned | `0081-wal-record-producer-wiring.md` |
| 0082 | Per-relation VM / FSM fork files (PG-aligned layout under `base/<DBOid>/<RelOid>_vm` / `_fsm`) — migration from M0080's global blobs | planned | `0082-vm-fsm-per-relation-fork-files.md` |
| 0083 | pg_multixact + multi-row locking metadata (XLOG_HEAP2_LOCK_UPDATED, MultiXactId persistence) | planned | `0083-multixact-multi-row-locking.md` |
| 0084 | PREPARE TRANSACTION + pg_twophase persistence (XLOG_XACT_PREPARE, distributed-txn 2PC support) | planned | `0084-two-phase-commit-prepare-transaction.md` |
| 0085 | pg_commit_ts (optional commit-timestamps subsystem; `track_commit_timestamp` GUC) | planned (low priority) | `0085-commit-timestamps-pg-commit-ts.md` |
| 0086 | Autovacuum needsVacuum PG-parity heuristics (dead/modified-tuple counters, GUC + per-table `reloptions`, `autovacuum_enabled`) | planned | `0086-autovacuum-needs-vacuum-pg-parity.md` |
| 0087 | Autovacuum `loadTables` via `catalog.Catalog` interface (remove `*catalog.InMemory` type assertion) | planned | `0087-autovacuum-load-tables-via-catalog-interface.md` |
| 0088 | WAL torn-tail recovery (treat zero-tail bytes after a corrupt record as end-of-WAL, mirroring PG crash-recovery semantics) | planned | `0088-wal-torn-tail-recovery.md` |
| 0089 | Checkpoint + stop durability + data-file fsync (wire `Manager.Sync` into checkpoint, audit dirty-tracking on extend, implicit shutdown checkpoint, final-Close checkpoint) | accepted (all 3 durability boundaries landed; scale-100 pgbench symptom now isolated to M0090) | `0089-checkpoint-stop-durability-and-fsync.md` |
| 0090 | pgbench scale-100 MVCC + INSERT bugs (TRUNCATE/DROP clear FSM+VM; HOT-update detects concurrent xmax stamp and aborts with SQLSTATE 40001 instead of silently overwriting) | accepted (3 workloads at scale 100 verified end-to-end; branches/tellers row counts exact, no MVCC drift) | `0090-pgbench-scale-100-mvcc-and-insert-bugs.md` |
| 0091 | Select-only TPS regression recovery (activity goroutineID closure-capture + btree.RangeScan zero-copy + post-M0091 pprof baseline archived) | partial — 350.89 → 510.52 TPS (1.45×); -0005 baseline pinned at `pprof-data/baseline/select-only-c10/`; residual deferred to M0092/M0093 | `0091-select-only-tps-regression-recovery.md` |
| 0092 | Lazy row emission in indexScanOp + projectOp (structural cloneRow elimination; Materialize-always-deep-copies contract; NLI currentOuter deep-copy; +5 follow-up sub-milestones for broadly-distributed allocation cuts) | structural changes landed; pgbench-c10 TPS did NOT improve (510 → 437 → 317 baseline; followup 283-342); per-commit WAL fsync identified as the actual bottleneck → M0093 | `0092-lazy-row-emission-in-scan-and-project.md` |
| 0093 | Skip WAL emission for read-only commits (PG-parity lazy XID assignment, Design B — `mvcc.TxnHandle` re-keys active set; `AssignXID` materialises XID on first write; `Context.MaterializeWriterXID` wired at every write site; OldestXmin tracks read-only RR snapshotXmin to preserve VACUUM) | accepted — pgbench-S 317 → 2,740 TPS (8.6×) at -c 10; walwriter flush count 19,600 → 0; M0091's ≥ 1,000 bar met | `0093-read-only-commit-skip-wal-emission.md` |
| 0094 | Replication E2E completion & TAP test porting (D-003 / D-004) | in-progress | `0094-replication-e2e-and-tap-test-porting.md` |
| 0095 | Client-tools TAP test porting | in-progress | `0095-client-tools-tap-test-porting.md` |
| 0096 | RC isolation-test suite: feature implementation & spec pass | in-progress | `0096-rc-isolation-suite-feature-implementation-and-spec-pass.md` |
| 0097 | pg_regress coverage: feature parity & test pass | in-progress | `0097-pg-regress-coverage-feature-parity-and-test-pass.md` |
| 0099 | M0098 remaining-work closure & target validation | planned | `0099-m0098-remaining-work-target-validation.md` |
