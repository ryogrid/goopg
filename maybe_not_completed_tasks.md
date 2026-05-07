# Deferred / Out-of-Scope Items

Tasks with deferred or out-of-scope content remaining after M0000–M0053.
**Generated:** 2026-05-07 / summarized 2026-05-08

---

## M0001 — Listener, startup, minimal wire protocol

- [x] Graceful shutdown path: `goopg stop` over control socket was deferred to M7 → implemented.

---

## M0002 — Checkpointing & concurrent B-tree

- [ ] `pg_stat_checkpointer`: `restartpoints_*`, `sync_time`, `buffers_written`, `slru_written` report 0 — deferred (design §7). `pg_stat_bgwriter` — out of scope (no separate bgwriter in v0).
- [ ] Crash-recovery test: WAL-volume optimisation (logical change records replacing per-dirty FPI) deferred to post-M0002 milestone.
- [ ] Landing 2: writer-vs-writer concurrency deferred to Landing 3.

---

## M0003 — Authentication

- [ ] scram-sha-256: SASLprep and channel binding deferred (next-loop, 0003-authentication.md).

---

## M0003 — HammerDB TPC-H workload

- [ ] HammerDB DDL: real NUMERIC arithmetic + `numeric(p,s)` enforcement deferred to type-system milestone.
- [ ] Foreign-key: FK enforcement deferred (parser-only acceptance).
- [x] EXPLAIN: `ANALYZE` / `FORMAT JSON` options were deferred → EXPLAIN ANALYZE implemented in M0018.
- [ ] ANALYZE: MCV lists, histograms, sampling, stats-persistence deferred.
- [ ] Subqueries: decorrelation, initplan caching, ANY/SOME/ALL, LATERAL deferred.
- [ ] Date/interval: fractional-second EXTRACT, timestamp-timestamp interval deferred.
- [ ] Derived tables: LATERAL, CTEs, parenthesised plain relations deferred.
- [ ] NUMERIC: arbitrary-precision, `%`/`^`, binary wire format deferred.
- [ ] LIKE: ILIKE, ESCAPE clause, prefix-anchor index extraction deferred.
- [ ] Views: DML on views, catalog persistence deferred.
- [x] (DEFERRED) All 22 queries (Q1–Q22) execute end-to-end → TPC-H parity 22/22 confirmed.
- [ ] (DEFERRED) HammerDB Power Test at SF1 — deferred (long-running benchmark; M0053 partial only).

---

## M0004 — Configuration / GUC / testutil

- [ ] SHOW/SET/RESET: `pg_settings` / `current_setting()` / `set_config()` deferred to catalog work (M5).
- [ ] `internal/testutil/cluster`: multi-cluster API deferred.

---

## M0005 — Streaming replication

- [ ] `pg_stat_replication`: `write_lag`/`flush_lag`/`replay_lag` emit `00:00:00` placeholders (deferred); `client_addr` empty (one-line follow-up).
- [ ] End-to-end replication test: "row visibility" / "write to promoted node" pieces gated on catalog persistence.

---

## M0006 — Join cost / pgbench SQL

- [ ] Cost-driven INNER-join: `seq_page_cost` / `random_page_cost`, indexed-rescan nestloop pricing, `enable_*` GUC consultation deferred.
- [ ] Statement parsers (BEGIN/COMMIT/ROLLBACK): carving GUC verbs out of `internal/server/query.go` deferred until executor lands.
- [ ] COPY: binary mode deferred (text mode sufficient for pgbench -i).

---

## M0007 — WAL preallocation / fdatasync / init

- [ ] WAL preallocation: `wal_recycle`, eager next-segment lookahead, counters/observability, pgbench latency measurement deferred.
- [ ] fdatasync: pgbench latency measurement, `wal_sync_method` GUC selector, segment-removal directory fsync deferred.
- [x] `goopg init`: system catalog bootstrap + `pg_control` file were deferred → catalog bootstrap done (M0030), `pg_control` still pending.

---

## M0008 — Logical replication

- [ ] Logical replication slots: reorder buffer, snapshot builder, decoder loop, per-slot catalog-xmin retention in vacuum/pruning deferred to subsequent loops.
- [ ] Reorder buffer + decoder: WAL classifier + snapshot builder deferred.
- [ ] WAL classifier: wire-layer emission of `EncodeXactCommit`/`EncodeXactAbort` at executor txn boundaries deferred.
- [ ] Long-lived classifier loop: snapshot builder skeleton deferred.
- [ ] Apply worker: subscriber-side worker scaffolding, TCP transport, tablesync state machine, DELETE/UPDATE row resolution still deferred (first slice = pgoutput decoder only).
- [ ] SnapBuild state machine, schema-change replay across slot lifetime, per-slot catalog-xmin retention in vacuum/pruning remain deferred.
- [ ] `pgoutput` plugin: UPDATE (`U`) messages deferred. 0008-0003 (publication/subscription DDL + catalog) pending.
- [ ] `pgoutput` constants `temporary`/`xmin`/`safe_wal_size`/`two_phase`/`active_pid` emit empty / `f` / `0` — not tracked yet.

---

## M0009 — AIO subsystem

- [ ] Read-stream API: contiguous-merge (`io_combine_limit`), sequential ramp-up, `Reset()` for restartable scans deferred.
- [ ] WAL writer AIO: real perf benefit deferred — single-threaded loop means inline Wait gives no pipelining; loop redesign needed.

---

## M0010 — WAL direct I/O

- [ ] Phase 1: GUC probe + plumbing; Phase 2 explicitly deferred. (described as "landed" but code not confirmed).
- [ ] Phase 2: AIO+DirectIO RMW bypasses engine (synchronous); engine-side aligned-copy is perf follow-up (Phase 2.b). Block-size hard-coded at 4 KiB; `STATX_DIOALIGN` deferred.

---

## M0012 — Lock manager

- [ ] Lock manager v0: per-tag lock-striping deferred (single `sync.Mutex`). Deadlock detection (M0012-0002) + executor integration (M0012-0003) separate.
- [ ] Executor integration: DDL paths (DROP/ALTER), catalog-level locks, `lock_timeout` GUC, `pg_locks` view are separate follow-up.

---

## M0014 — XLOG page/segment format

- [ ] XLOG types/helpers: cross-arch byte-order transfer out of scope.
- [ ] XLOG page emission: `XLogRecord`-header switchover (M0014-0002) + `pg_waldump` validation (M0014-0003) remain deferred — `PageHeaders=true` produces upstream-shaped pages with legacy record frames inside.

---

## M0015 — PL/pgSQL stored routines

- [ ] Stage A step 6: scalar-only in Stage A (SETOF deferred).
- [ ] Stage A step 5: SPI bridge for embedded SQL stays deferred to step 4g.
- [ ] Stage A step 4a: out of scope — DECLARE+assignment (4b), IF/ELSIF (4c), LOOP/FOR (4d), PERFORM (4e), SELECT INTO (4f), embedded DML (4g), exception blocks, RETURN NEXT/QUERY.
- [ ] Stage A step 3: out of scope — PL/pgSQL parser/AST, interpreter/SPI bridge, function invocation in expressions, pg_proc persistence, WAL replay, multi-target DROP.
- [ ] Stage A step 2: out of scope — analyzer wiring, executor Create/DropFunction operators, pg_proc persistence, PL/pgSQL body parser, interpreter, invocation resolver, numerical type-OID columns.
- [ ] Stage A step 1: out of scope — pg_proc catalog wiring, PL/pgSQL parser+AST, interpreter+SPI bridge, function invocation in expression contexts. Stage B keywords (`Procedure`, `Call`, `Out`, `Inout`, `Variadic`, `Declare`) stay deferred.
- [ ] Stage B step 3: SQL-language procedures remain deferred (0A000 diagnostic).

---

## M0016 — CTE (WITH clause)

- [ ] CTE observability: materialise-once optimisation and runtime CTE counters in `pg_stat_*` views remain out of scope (inlining model).

---

## M0017 — UPSERT

- [ ] UPSERT Stage A: concurrency hardening (speculative insert + MVCC-correct cleanup under contention) deferred to follow-on slice.

---

## M0020 — Window functions

- [ ] Window-function parser: frame clauses (ROWS/RANGE/GROUPS) deferred (Stage B); named windows + WINDOW definition clauses also deferred.
- [ ] Window-function executor: Stage B items — frame clauses + multiple window specs remain deferred follow-up.
- [x] Stage B lag/lead: frame clauses and named windows remain deferred (lag/lead itself implemented).

---

## M0021 — SELECT … FOR UPDATE

- [ ] Parser: NO KEY UPDATE / KEY SHARE deferred. Analyzer/planner/executor for steps 2–4 all deferred at step-1 time.
- [ ] Analyzer wiring: aggregate-functions-in-target detection deferred; locking inside subqueries/CTEs also deferred.
- [ ] Planner: Stage A executor + NOWAIT/SKIP LOCKED + deadlock observability all deferred to M0021-0003/0004.
- [ ] Stage A executor: NOWAIT/SKIP LOCKED runtime + observability counters + tuple-level pessimistic locking deferred.
- [ ] NOWAIT runtime: SKIP LOCKED + observability deferred to tuple-level locking follow-up; relation-coarse SKIP LOCKED returns 0A000.
- [ ] Tuple locking step 1: executor wiring + `xl_heap_lock` WAL records + MultiXact infrastructure all deferred.
- [ ] Tuple locking step 2a: INSERT/UPDATE/DELETE conflict detection + IndexScan currentTID + MultiXact + streaming refactor all stay deferred.
- [ ] Tuple locking step 2b: IndexScan blocking + tuple-level NOWAIT/SKIP LOCKED + MultiXact + streaming-stamping + pg_locks introspection + lock-strength merge all deferred.
- [ ] Tuple locking step 2c: UPDATE/DELETE-via-IndexScan out of scope; tuple-level NOWAIT/SKIP LOCKED + MultiXact + streaming stamping all remain deferred.
- [ ] Tuple locking step 2d: index-driven UPDATE/DELETE optimisation + tuple-level NOWAIT/SKIP LOCKED + MultiXact + streaming stamping remain deferred.
- [ ] Tuple locking step 4: lock-strength promotion + persistent MultiXact + tuple-level NOWAIT/SKIP LOCKED + streaming + pg_locks introspection all stay deferred.

---

## M0030 — Catalog persistence

- [ ] System catalog heap substrate: DROP TABLE/INDEX sync and startup user-table load deferred.
- [ ] WAL catalog recovery: JSON decommission deferred to M0030-0004.
- [ ] pg_attribute/pg_type SQL surface: `pg_index` deferred.

---

## M0032 — Buffer Pool Arena

- [ ] HammerDB load drop fix: per-row `writeHeapRow` refactor deferred (acceptance criterion met without it).

---

## M0037 — Spill-to-Disk Hash Join

- [ ] Grace hash join (Phase B) deferred.

---

## M0039 — Planner column-index alignment

- [ ] Fix A: global→local ColumnRef remap deferred.

---

## M0042 — I/O alignment with upstream

- [ ] WAL buffer/writer: XLogInsert/XLogFlush API rename, insertion-lock array, WAL ring page eviction blocking on writtenLSN deferred.
- [ ] Client backend: bgwriter loop deferred (TODO: §4 of 0042-0001).

---

## M0044 — B-tree key support (TPC-H schema types)

- [ ] Wall-time benchmark gate requires actual HammerDB run-008 (deferred to human run). Range-predicate scans (BETWEEN/</>) need planner follow-up.
- [ ] Full benchmark deferred to human run.

---

## M0045 — Crash recovery (non-zero WAL start)

- [ ] HammerDB SF=1 end-to-end validation deferred as manual acceptance gate.

---

## M0046 — TOAST

- [ ] pglz compression deferred (values stored uncompressed).

---

## M0050 — Savepoints

- [ ] Wire-protocol session tx management across Query messages and `\set ON_ERROR_ROLLBACK on` implicit savepoints deferred.

---

## M0052 — HammerDB TPC-H regression

- [ ] (Root cause identified & fixed — oversized message handling. Task complete.)

---

## M0053 — HammerDB TPC-H complete run

- [ ] Full SF=1 Power Test not completed (partial: schema, load, index, ANALYZE done; Q14/Q2/Q9 completed; budget exhausted at Q20). Catalog non-persistence after server crash observed — out of scope (see M0030).
