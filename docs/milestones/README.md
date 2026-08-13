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
| 0100 | RC isolation-test suite: runtime correctness closure & 21-spec pass (closes M0096-0005, M0096-0013) | accepted — all 23 dedicated `TestPort_Isolation*` PASS (0 FAIL/SKIP); pgbench-S 48,984 TPS at -c 10 (≥ 2,000 bar) | `0100-rc-isolation-runtime-correctness-and-spec-pass.md` |
| 0101 | WAL pg_waldump compatibility: enable PG-compatible format by default (implements M0014) | accepted | `0101-wal-pg-waldump-compatibility.md` |
| 0102 | Heterogeneous streaming-replication + SIGKILL-failover E2E (PG↔goopg, sync + async) | accepted | `0102-heterogeneous-replication-failover-e2e.md` |
| 0103 | Heterogeneous logical-replication + SIGKILL-failover E2E (PG↔goopg, sync + async) | accepted | `0103-heterogeneous-logical-replication-failover-e2e.md` |
| 0104 | SERIALIZABLE isolation via SSI anomaly prevention | planned | `0104-serializable-ssi-anomaly-prevention.md` |
| 0105 | goopg→PG data-file format parity (heap page, tuple header, catalog) | accepted | `0105-goopg-to-pg-heap-page-and-tuple-format-parity.md` |
| 0106 | PG relcache init file compatibility (backend startup from goopg backup) | planned | `0106-pg-relcache-init-file-compat.md` |
| 0107 | Performance optimization refactor (mctx + pointer-free Datum + concrete executor + MVCC/activity/bufpool/WAL/runtime contention fixes; keeps M0105/M0106 PG-compat invariants) | planned | `0107-performance-optimization-refactor.md` |
| 0108 | `postgresql.conf.sample` template + initdb wiring + registry↔template sync rule (AGENT.md rule landed at filing time) | accepted | `0108-postgresql-conf-sample-template.md` |
| 0112 | `pg_statistic` heap table for ANALYZE statistics persistence | planned | `0112-pg-statistic-heap-table-for-stats-persistence.md` |
| 0113 | Heap-based index recovery via `pg_index` | planned | `0113-heap-based-index-recovery-via-pg-index.md` |
| 0114 | `pg_internal.init` relcache fast-start cache | planned | `0114-pg-internal-init-relcache-fast-start-cache.md` |
| 0115 | Heap tuple hint bit caching (HEAP_XMIN_COMMITTED / HEAP_XMAX_INVALID read+write path in TupleVisible; FrozenTransactionID fast path; hint-bit-only page dirty without WAL) | planned | `0115-hint-bit-caching.md` |
| 0116 | Multi-column Index-Only Scan key decoding (composite B-tree key decode; planner column-coverage check; extends Visibility Map optimization to composite-PK tables) | planned | `0116-multi-column-index-only-scan.md` |
| 0117 | CLOG ↔ PostgreSQL subsystem alignment (runtime visibility fallback, `pg_subtrans` durability + `SUB_COMMITTED`, group commit + bounded SLRU buffer pool, async-commit LSN, wraparound-safe horizons) | planned | `0117-clog-postgresql-subsystem-alignment.md` |
| 0118 | Upstream isolation spec suite pass-through (post REPEATABLE READ + SERIALIZABLE; 112 targeted specs across SSI anomaly, predicate locks, row-locking/SKIP LOCKED/NOWAIT, deadlock, FK, MERGE/ON CONFLICT, DDL/VACUUM concurrency) | planned | `0118-isolation-spec-suite-passthrough.md` |
| 0119 | Deferral-Ledger Backlog Consumption (living milestone — tasks appended over time as `.ralph/deferral_ledger.md` accumulates open rows) | planned | `0119-deferral-ledger-backlog-consumption.md` |
| 0120 | WordPress WP-CLI verification execution & evidence capture (run the 40-item `wp/verification/CHECKLIST.md`, capture WP-CLI output + goopg statement log + PG4WP SQL + confirming reads, produce a PASS/FAIL report, triage failures) | planned | `0120-wordpress-wpcli-verification-execution.md` |
| 0121 | WordPress WP-CLI verification failure remediation (fix/implement the goopg gaps M0120 found, add regression tests + design docs, re-verify PASS) | planned | `0121-wordpress-wpcli-verification-remediation.md` |
| 0122 | Unimplemented-Feature Backlog Consumption (living milestone — consumes the 181 entries in `unimplemented_feat.json`; entries are a 2026-07-02 snapshot and may already be implemented, so verify against HEAD before building and mark resolved if present) | planned | `0122-unimplemented-feature-backlog-consumption.md` |
| 0123 | Canonical `pg_node_tree` serialization (new `internal/pgnodes`: resolver + outfuncs + readfuncs + binary datum encoding, so a real PG18 standby can evaluate/query goopg's user column DEFAULTs, extended-statistics expressions, and views — `relhasrules=true` with canonical `adbin`/`stxexprs`/`ev_action`; phased S0–S4, S0 landed) | in-progress | `0123-canonical-pg-node-tree-serialization.md` |
| 0124 | TPC-DS round-2 closeout: measurement baseline, gate discharge & ledger debt (§13.5 actions 1/5/6/7 plus §13.4 item 3 — the SF=1 dual-engine re-sweep at HEAD that turns §13.3's projection into a measurement, the retroactive TPC-H A/B and first committed round-2 plan snapshot for the ungated phases 1.2/1.3, the seven missing §10 deferral rows, Q35's never-recorded row count, and a value checksum for the SF0.5 oracle. No engine change; produces every instrument M0125 is judged with) | planned | `0124-tpcds-round2-closeout-measurement-and-gate-debt.md` |
| 0125 | TPC-DS timeout class & planner expression-walker extinction (§13.5 actions 2/3/4 — Q75's join-residual evaluation order, the `GOOPG_RELSIZE_FALLBACK` `table_block_relation_estimate_size` fallback staged by consumer behind a default-off flag, the shared `exprChildSlots` primitive plus a `go/ast` exhaustiveness gate, seven walker conversions one per commit, and the default flip as its own evidence-bearing task. Organised around the measured TPC-H trade-off — statistics regressed Q22 128×/Q4 79×/Q8 53×/Q2 26×, the cost-driven planner is 4 wins / 6 regressions — both invisible to a row-count gate) | planned | `0125-tpcds-timeout-class-and-walker-extinction.md` |
| 0126 | Cost-driven planning made production-viable (turns `analysis/cost-driven-second-try-200731/` into shipped behaviour: the packer key-count guard, `VirtualSlot.Materialize` arena clone and the `EstimateRows` MultiHashJoin arm that unconfound every later measurement, de-materialising the binary join seam on both execution paths, a conditional runtime join-fusion operator behind a default-off env switch, a symmetric-timeout re-validation of `GOOPG_COST_DRIVEN_JOINORDER`, bounded attribution-then-fix order work, retirement of `MultiHashJoin` as a plan node, then the conditional default flip — accepted only if 22/22 TPC-H SF1 complete within +20 % of the pinned R0 integer-planner baseline with no query above 2× and zero TPC-DS SF0.5 row/checksum deltas; on a no-go, one conditional remediation (build-side memory-aware `hashJoinCost`, the PG `cost_hashjoin`+`ExecChooseHashTableSize` analogue goopg has never had) and a re-measured final verdict) | **closed 2026-08-03 as documented no-go** (acceptance runs 1+2 failed on Q9; Q5 newly regressed by -0013's penalties; `GOOPG_COST_DRIVEN_JOINORDER` stays default off; MHJ retirement + Stage-0 de-materialisation + the estimateJoin cap ship; successor = M0127) | `0126-cost-driven-planning-production-viability.md` |
| 0127 | PG-shaped join search (implements the `docs/design/leftdeep-joins/` bundle — the M0126-0013 successor: PG-shaped binary plan trees (left-deep + bushy) exactly as PG 18.3's `join_search_one_level` produces them, the full three-phase level-wise DP over `RelOptInfo` pathlists with join methods costed inside the search, and a fusion-free join executor — seam de-materialisation, multi-column keys retiring `reselectDegenerateHashKeys`, hybrid-hash spill, streaming merge/outer-fill/Materialize, compiled key/residual eval — making `MultiHashJoin` and the permanently-off fusion operator deletable; acceptance = TPC-H SF1 22/22 with total ≤ 1.2× R0 (493.31 s) and Q9 ≤ 170.9 s, TPC-DS SF0.5 zero deltas, no MHJ/fusion in any plan, PG-identical bushy capability; staged S0–S7 behind `GOOPG_PGSHAPED_DP`) | planned | `0127-pg-shaped-join-search.md` |
| 0128 | Special-join inference + M0127 residuals (filed 2026-08-07 per user directive; priority immediately after M0127 — removes the leftdeep-joins 03 §4.4 v1 pin by porting `SpecialJoinInfo` construction + `join_is_legal`/`have_join_order_restriction` inference so LEFT/FULL/semi/anti joins enter the PG-shaped DP, unlocking the honest `GOOPG_PGSHAPED_COLLAPSE` re-measurement (09 §3.19; Q78's outer-link-buried-under-inner-joins shape); plus the M0127 non-goals (cooperative parallel hash build per parallel-query/10-roadmap's reopen condition, design-first bitmap heap scan) and the measured long-term residuals: per-column average-width stats feeding `avgVarBytes` + the 09 §3.23 one-scan/two-estimates fix, the portable `reduce_outer_joins` reduction half (09 §3.22), EXPLAIN range-table name dedup (Q2/Q8/Q17/Q18/Q22 clause-6 adjudication, 09 §3.11), `Rows Removed by …` lines (09 §3.17), the Q74 ~7× regression attribution, and the lockRows spill-time side-channel safety net + resjunk-ctid durable fix (ledger root-0038); acceptance = TPC-DS SF0.5 zero row/checksum deltas, TPC-H SF1 22/22 within the M0127 S5 bounds, and recorded verdicts for COLLAPSE and parallel hash build — a documented no-go is success, an unmeasured outcome is the only failure) | planned | `0128-special-join-inference-and-m0127-residuals.md` |
| 0129 | Q74 fix + M0128 verdict follow-ups + residual-ledger burn-down (CLOSED 2026-08-09; all S1–S10 [x]) | accepted | `0129-q74-fix-and-m0128-followups.md` |
| 0130 | Cluster-directory compat with PG 18.3 + PG physical replication (filed 2026-08-09; 10 tasks S1–S10 across 3 themes: cluster-dir format, WAL fidelity verification, replication PG-compat) | planned | `0130-cluster-dir-compat-and-pg-physical-replication.md` |
| 0131 | Bidirectional cluster-directory cold-start + real-PG system-view hosting (filed 2026-08-11; themes A–F: reverse/forward cold start without `pg_basebackup`, view hosting on a goopg catalog, crash-state interchange, and the two live data-loss bugs that filing exposed) | planned | `0131-bidirectional-cluster-dir-coldstart-and-system-views.md` |
| 0132 | Explicit transactions across the extended query protocol (filed 2026-08-12, promoted 2026-08-13; the extended path commits one transaction per `Execute` and ignores `BEGIN`/`COMMIT`/`ROLLBACK`, so **`ROLLBACK` does not roll back** — 13 slices S1–S13, with S2+S3+S4+S5 bound to land together) | in-progress | `0132-extended-protocol-explicit-transactions.md` |
