# Milestone 0113 — Heap-Based Index Recovery via pg_index

**Status:** planned
**Filed:** 2026-05-26
**Depends on:** M0111 (PG-format codec parity, accepted), M0079 (catalog + btree WAL recovery, accepted)
**Reference plan:** `.ralph/fix_plan.md` (M0113 section)

## Problem

goopg currently recovers index catalog entries at startup by replaying
`RecordKindIndexDDL` WAL records (`internal/wal/recovery.go:replayIndexDDLRecords`).
This is a goopg-private mechanism — it relies on goopg-specific WAL record
kinds that PG18 does not produce or understand.

PostgreSQL recovers its index catalog from heap tables: `pg_class` (one row
per index relation, `relkind='i'`) and `pg_index` (one row per index with
`indrelid`, `indkey`, `indisunique`, `indisprimary`, etc.).  On startup PG
reads these tables directly, with no reliance on a WAL side-channel.

goopg already writes `pg_class` rows for indexes via `syncIndexToCatalogHeap`
(called by `createBTreeIndex`).  The missing piece is writing and reading
`pg_index` rows so the startup path can reconstruct the full index descriptor
— including key columns, uniqueness, and primary-key flag — from heap pages
alone.

## Goal

Implement `pg_index` as a goopg heap table that mirrors the PG18 on-disk
layout.  `CREATE INDEX` and `ALTER TABLE ADD PRIMARY KEY` write a row to
`pg_index`; startup reads `pg_index` + `pg_class` to reconstruct index
catalog entries without WAL replay.  Once this path works, the `replayIndexDDLRecords`
WAL-replay fallback can be retained as a compatibility shim for clusters
created before this milestone, and eventually removed.

## Motivation

- **PG alignment**: PG's catalog recovery reads `pg_index` + `pg_class` on
  startup; goopg should follow the same pattern now that `pg_class` / `pg_attribute`
  are recovered from heap (M0030 / M0111 heap-only recovery).
- **WAL side-channel elimination**: `RecordKindIndexDDL` is a goopg-private
  WAL extension.  Removing the dependency on it simplifies the WAL format and
  makes the WAL stream closer to PG-canonical.
- **PG standby compatibility**: An attaching PG18 standby expects to read index
  metadata from `pg_index`; rows must be in PG18-canonical physical format.

## Key design areas

- Physical layout of `pg_index` rows in PG18 (19 columns, includes
  `indkey int2vector`, `indexprs pg_node_tree`, `indpred pg_node_tree` —
  two of these are varlena text columns that require careful encoding).
- Writing a `pg_index` row from `syncIndexToCatalogHeap`.
- Bootstrapping `pg_index` relfile during `initdb`.
- Reading `pg_index` at startup to complement `pg_class` index rows and
  populate `catalog.InMemory` index entries (replacing or supplementing
  `replayIndexDDLRecords`).
- Migration: clusters without `pg_index` rows fall back to the WAL-replay
  path; once the heap path is confirmed correct the fallback can be removed.

## Out of scope

- Expression indexes (`indexprs` non-null): placeholder empty value is
  acceptable initially.
- Partial indexes (`indpred` non-null): same.
- Full SQL visibility of `pg_index` via SeqScan (follow-up).
