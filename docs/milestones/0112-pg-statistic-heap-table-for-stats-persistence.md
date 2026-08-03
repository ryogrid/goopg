# Milestone 0112 — pg_statistic Heap Table for ANALYZE Statistics Persistence

**Status:** planned (partially landed — see "Status update 2026-07-30" below)
**Filed:** 2026-05-26
**Depends on:** M0111 (PG-format codec parity, accepted), M0030 (catalog persistence and DDL WAL, accepted)
**Reference plan:** `.ralph/fix_plan.md` (M0112 section)

## Problem

goopg's `ANALYZE` command computes per-column statistics (row count, NDistinct,
null fraction, MCVs, histogram) and stores them in the in-memory catalog
(`catalog.InMemory`).  Because they are never written to any heap table,
statistics are lost on every server restart — the planner falls back to
hard-coded defaults until `ANALYZE` is re-run.

PostgreSQL stores column statistics in the `pg_statistic` system catalog
(a heap table, OID 2619).  On restart, the planner reads `pg_statistic`
to restore the saved estimates without requiring a re-analysis pass.

## Goal

Implement `pg_statistic` as a goopg heap table that mirrors the PG18 on-disk
layout.  `ANALYZE` writes updated rows to this table; startup reads them back
into the in-memory planner statistics.  The table must be readable by an
attaching PG18 standby using the same fixed-offset physical decoder pattern
established for `pg_attribute` in M0111.

## Motivation

- **Query plan stability across restarts**: TPC-H and pgbench workloads require
  `ANALYZE` results to persist so the planner can choose optimal join orders
  immediately after restart, not only after a warm-up `ANALYZE` pass.
- **PG alignment**: PG's catalog recovery path reads `pg_statistic` on startup;
  goopg should follow the same pattern now that it uses heap-based catalog
  recovery for `pg_class` / `pg_attribute`.
- **PG standby compatibility**: A PG18 standby attaching to goopg will scan
  `pg_statistic` to populate its own planner statistics; the rows must be
  written in PG18-canonical physical format.

## Key design areas

- Physical layout of `pg_statistic` rows (27 fixed columns in PG18, most are
  nullable arrays — the `anyarray` varlena encoding is the main challenge).
- Encoding `stakind` / `stavalues` / `stanumbers` arrays in PG-physical format
  so a standby's planner can decode them.
- Reading `pg_statistic` at startup to populate `catalog.InMemory` column stats.
- Bootstrapping the `pg_statistic` relfile during `initdb`.
- Handling `DROP TABLE` / `ALTER TABLE DROP COLUMN` invalidation.

## Out of scope

- Full `pg_statistic_ext` (extended statistics) support.
- `pg_statistic` visibility via SQL `SELECT` (system catalog SeqScan) can be
  a follow-up once the physical format is correct.

## Status update 2026-07-30

**Partially landed.** The write and reload halves exist:
`persistStatsToPGStatistic` (`internal/executor/operators_analyze.go:184`)
writes PG18-canonical per-column rows on every ANALYZE, and
`loadStatisticsFromHeap` (`internal/initdb/open.go:3479`) restores them at
startup. Three gaps remain, measured and recorded in
`docs/design/0125-0028-warm-stats-programme.md`:

1. Both halves are wired to the **default database only** (`DefaultDBOid` /
   `cat.DBOID()`), so per-DB tables never round-trip.
2. `RowCount`/`Pages` (reltuples/relpages) are **not persisted at all**
   (ledger `pq-P6`) — pg_statistic has no slot for them and goopg's
   `pg_class` is virtual, so the planner's size consumers stay blind after a
   restart even where column stats return.
3. ANALYZE results were measured invisible across connections (2026-07-23),
   which defeats persistence end-to-end.

**M0125-0029 (user directive 2026-07-30) closes all three as an authorized
interim**: the user explicitly waived the PG-faithfulness bar for the
RowCount/Pages persistence mechanism, so it may land goopg-private — with the
constraint that the PG-scannable pg_statistic rows stay PG18-canonical
(additive only). This milestone stays open as the **PG-faithful end-state**
(standby-readable statistics story, full `anyarray` fidelity) and must not be
marked complete by M0125-0029's landing.
