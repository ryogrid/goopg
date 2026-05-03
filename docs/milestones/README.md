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
| 0032 | Buffer pool arena: mmap → Go heap replacement                          | accepted   | `0032-buffer-pool-heap-arena.md` |
| 0033 | Planner-level subquery unnesting                                         | accepted    | `0033-subquery-unnesting.md` |
| 0034 | Bushy join tree / join-graph optimization                                | accepted     | `0034-bushy-join-optimization.md` |
| 0035 | Streaming hash join & bushy-unnest verification                          | accepted      | `0035-streaming-hash-join.md` |
| 0036 | Hash join lazy materialization (on-demand output)                        | accepted       | `0036-hash-join-lazy-materialization.md` |
| 0037 | Spill-to-disk hash join (Grace hash join)                                | accepted       | `0037-hash-join-spill-to-disk.md` |
| 0038 | Multi-way hash join                                                     | accepted        | `0038-multi-way-hash-join.md` |
| 0039 | Fix planner column-index alignment (correct join results)               | planned         | `0039-fix-planner-column-ref.md` |
