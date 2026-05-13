## M0001 — Listener, startup, minimal wire protocol

- [] Graceful shutdown path: `goopg stop` over control socket was deferred to M7 → implemented.

---

## M0002 — Checkpointing & concurrent B-tree

- [ ] `pg_stat_checkpointer`: `restartpoints_*`, `sync_time`, `buffers_written`, `slru_written` report 0 — deferred (design §7). `pg_stat_bgwriter` — out of scope (no separate bgwriter in v0).
- [ ] Crash-recovery test: WAL-volume optimisation (logical change records replacing per-dirty FPI).
- [ ] writer-vs-writer concurrency.


---

## M0004 — Configuration / GUC / testutil

- [ ] SHOW/SET/RESET: `pg_settings` / `current_setting()` / `set_config()`
- [ ] `internal/testutil/cluster`: multi-cluster API deferred.

---

## M0005 — Streaming replication

- [ ] `pg_stat_replication`: `write_lag`/`flush_lag`/`replay_lag` emit `00:00:00` placeholders; `client_addr` empty.
- [ ] End-to-end replication test: "row visibility" / "write to promoted node" pieces gated on catalog persistence.

---

## M0006 — Join cost / pgbench SQL

- [ ] Cost-driven INNER-join: `seq_page_cost` / `random_page_cost`, indexed-rescan nestloop pricing, `enable_*` GUC consultation.
- [ ] Statement parsers (BEGIN/COMMIT/ROLLBACK): carving GUC verbs out of `internal/server/query.go`s.
- [ ] COPY: binary mode (text mode sufficient for pgbench -i).

---

## M0007 — WAL preallocation / fdatasync / init

- [ ] WAL preallocation: `wal_recycle`, eager next-segment lookahead, counters/observability, pgbench latency measurement deferred.
- [ ] fdatasync: pgbench latency measurement, `wal_sync_method` GUC selector, segment-removal directory fsync.
- [ ] `goopg init`: `pg_control`.

---

## M0008 — Logical replication

- [ ] Logical replication slots: reorder buffer, snapshot builder, decoder loop, per-slot catalog-xmin retention in vacuum/pruning.
- [ ] Reorder buffer + decoder: WAL classifier + snapshot builder.
- [ ] WAL classifier: wire-layer emission of `EncodeXactCommit`/`EncodeXactAbort` at executor txn boundaries deferred.
- [ ] Long-lived classifier loop: snapshot builder.
- [ ] Apply worker: subscriber-side worker scaffolding, TCP transport, tablesync state machine, DELETE/UPDATE row resolution.
- [ ] SnapBuild state machine, schema-change replay across slot lifetime, per-slot catalog-xmin retention in vacuum/pruning.
- [ ] `pgoutput` plugin: UPDATE (`U`) messages.
- [ ] `pgoutput` constants `temporary`/`xmin`/`safe_wal_size`/`two_phase`/`active_pid` emit empty / `f` / `0`.

---

## M0009 — AIO subsystem

- [ ] Read-stream API: contiguous-merge (`io_combine_limit`), sequential ramp-up, `Reset()` for restartable scans.
- [ ] WAL writer AIO: real perf benefit — single-threaded loop means inline Wait gives no pipelining; loop redesign needed.

---

## M0012 — Lock manager

- [ ] Lock manager : per-tag lock-striping. Deadlock detection (M0012-0002) + executor integration (M0012-0003).
- [ ] Executor integration: DDL paths (DROP/ALTER), catalog-level locks, `lock_timeout` GUC, `pg_locks` view.

---

## M0014 — XLOG page/segment format

- [ ] XLOG page emission: `XLogRecord`-header switchover (M0014-0002) + `pg_waldump` validation (M0014-0003) — `PageHeaders=true` produces upstream-shaped pages with legacy record frames inside.

---

## M0015 — PL/pgSQL stored routines

- [ ] Stage A step 6: scalar-only in Stage A (SETOF).
- [ ] Stage A step 5: SPI bridge for embedded SQL.
- [ ] Stage A step 4a: DECLARE+assignment (4b), IF/ELSIF (4c), LOOP/FOR (4d), PERFORM (4e), SELECT INTO (4f), embedded DML (4g), exception blocks, RETURN NEXT/QUERY.
- [ ] Stage A step 3: PL/pgSQL parser/AST, interpreter/SPI bridge, function invocation in expressions, pg_proc persistence, WAL replay, multi-target DROP.
- [ ] Stage A step 2: analyzer wiring, executor Create/DropFunction operators, pg_proc persistence, PL/pgSQL body parser, interpreter, invocation resolver, numerical type-OID columns.
- [ ] Stage A step 1: pg_proc catalog wiring, PL/pgSQL parser+AST, interpreter+SPI bridge, function invocation in expression contexts. Stage B keywords (`Procedure`, `Call`, `Out`, `Inout`, `Variadic`, `Declare`).
- [ ] Stage B step 3: SQL-language procedures.


## M0017 — UPSERT

- [ ] UPSERT Stage A: concurrency hardening (speculative insert + MVCC-correct cleanup under contention).

---

## M0020 — Window functions

- [ ] Window-function parser: frame clauses (ROWS/RANGE/GROUPS); named windows + WINDOW definition clauses.
- [ ] Window-function executor: frame clauses + multiple window specs.
- [ ] lag/lead: frame clauses and named windows (lag/lead itself are already implemented).

---

## M0021 — SELECT … FOR UPDATE

- [ ] Parser: NO KEY UPDATE / KEY SHARE. Analyzer/planner/executor.
- [ ] Analyzer wiring: aggregate-functions-in-target detection; locking inside subqueries/CTEs.
- [ ] Planner: Stage A executor + NOWAIT/SKIP LOCKED + deadlock observability.
- [ ] NOWAIT/SKIP LOCKED runtime + observability counters + tuple-level pessimistic locking.
- [ ] NOWAIT runtime: SKIP LOCKED + observability; relation-coarse SKIP LOCKED.
- [ ] Tuple locking: executor wiring + `xl_heap_lock` WAL records + MultiXact infrastructure.
- [ ] Tuple locking: INSERT/UPDATE/DELETE conflict detection + IndexScan currentTID + MultiXact + streaming refactor.
- [ ] Tuple locking: IndexScan blocking + tuple-level NOWAIT/SKIP LOCKED + MultiXact + streaming-stamping + pg_locks introspection + lock-strength merge.
- [ ] Tuple locking: UPDATE/DELETE-via-IndexScan; tuple-level NOWAIT/SKIP LOCKED + MultiXact + streaming stamping.
- [ ] Tuple locking: index-driven UPDATE/DELETE optimisation + tuple-level NOWAIT/SKIP LOCKED + MultiXact + streaming stamping.
- [ ] Tuple locking: lock-strength promotion + persistent MultiXact + tuple-level NOWAIT/SKIP LOCKED + streaming + pg_locks introspection.

---

## M0030 — Catalog persistence

- [ ] System catalog heap substrate: DROP TABLE/INDEX sync and startup user-table load.
- [ ] WAL catalog recovery: JSON decommission.
- [ ] pg_attribute/pg_type SQL surface: `pg_index`.

---

## M0037 — Spill-to-Disk Hash Join

- [ ] Grace hash join.

---

## M0042 — I/O alignment with upstream

- [ ] WAL buffer/writer: XLogInsert/XLogFlush API rename, insertion-lock array, WAL ring page eviction blocking on writtenLSN.
- [ ] Client backend: bgwriter loop.

---

## M0046 — TOAST

- [ ] pglz compression deferred (values stored uncompressed).

---

## M0050 — Savepoints

- [ ] Wire-protocol session tx management across Query messages and `\set ON_ERROR_ROLLBACK on` implicit savepoints.

---

