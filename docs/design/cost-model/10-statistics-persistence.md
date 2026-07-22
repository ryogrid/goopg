# 10 — Statistics Persistence

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) — **deferred; designed now, implemented last** |
| date | 2026-07-22 |
| depends on | [05](05-statistics-and-estimation-inputs.md) |
| premise | the milestone does **not** depend on this chapter — it rides the `estimate_rel_size` fallback ([05](05-statistics-and-estimation-inputs.md) §4) instead |

## 0. Why this chapter exists, and why it is last

The cost model needs stable row and width inputs. [05](05-statistics-and-estimation-inputs.md)
showed the milestone can get them **without** persistence — column statistics
already survive a restart, and the missing row count is reconstructed from the live
block count via the `estimate_rel_size` fallback. That is deliberate: persisting
table-level statistics the PG way requires machinery goopg does not yet have (a
runtime in-place update path for an on-disk system catalog), and pinning the
milestone to it would delay the whole bundle for a cold-start refinement.

So this chapter designs persistence **now, for later**: it specifies the PG-faithful
target so the fallback in [05](05-statistics-and-estimation-inputs.md) is a
compatible interim, not a dead end, and so the implementer who eventually lands it
inherits a design rather than a blank. It is the final roadmap phase
([11](11-roadmap.md) C7), reopened when the cold-start accuracy gap
([05](05-statistics-and-estimation-inputs.md) §4) is shown to matter for a real
workload.

## 1. What PostgreSQL persists, and where

Two catalogs, both already partly reproduced in goopg:

- **`pg_class`** — `reltuples` (float4 row estimate; `-1` = unknown) and `relpages`
  (int4). Updated by `vac_update_relstats` after ANALYZE/VACUUM. goopg **renders**
  these from in-memory `TableStats.RowCount`/`Pages` (`internal/catalog/catalog.go:6946`)
  but does not keep them as updatable on-disk values.
- **`pg_statistic`** — per-column `stanullfrac`, `stawidth`, `stadistinct` (signed:
  `>0` count, `<0` fraction, `0` unknown), and the slot arrays (MCV = stakind 1,
  histogram = stakind 2). goopg **already persists and restores** these
  (`internal/executor/pg18_user_catalog_rows.go:1295`, restore at
  `internal/initdb/open.go:3490`) — **except** `stawidth`, which is written as a
  placeholder `8` (`pg18_user_catalog_rows.go:1346`).

So the gap is narrow and precise: **`reltuples`/`relpages` are not persisted**
(ledger `pq-P10`), and **`stawidth` is a placeholder** ([05](05-statistics-and-estimation-inputs.md) §2).
Column statistics otherwise round-trip.

## 2. The obstacle: no runtime in-place update for on-disk shared catalogs

goopg's `pg_class` is **rendered virtually** from `catalog.Table`
([memory: pg_class is virtual, pg_attribute is heap]), and its on-disk heap is only
*appended* at CREATE TABLE. There is no runtime path that updates an existing
on-disk `pg_class` row in place — persisting a system-catalog field at runtime is a
capability goopg does not have ([memory: no runtime in-place update for on-disk
shared catalogs]). This is why `reltuples` persistence is not a one-liner and why
it is deferred rather than sneaked into the milestone.

## 3. The designed approach: append-and-reload, mirroring pg_statistic

goopg **already** solves this shape for `pg_statistic`: `persistStatsToPGStatistic`
(`internal/executor/operators_analyze.go:184`) **appends** a fresh heap row on each
ANALYZE, and the restore takes the last live tuple per key
(`internal/initdb/open.go:3490`). The designed `reltuples`/`relpages` persistence
follows the identical pattern:

1. On ANALYZE (and VACUUM), **append** a `pg_class`-shaped row carrying the new
   `reltuples`/`relpages` for the relation, into the session database's `pg_class`
   heap.
2. On restart, the `pg_class` reload takes the **last** live tuple per relation OID,
   recovering `reltuples`/`relpages` into `TableStats.RowCount`/`Pages` — the fields
   [05](05-statistics-and-estimation-inputs.md) currently leaves zero.
3. Use `UpdateRelStats` (`internal/catalog/catalog.go:12198`), which **merges**
   `RowCount`/`Pages` into existing `Stats` without discarding the column stats,
   rather than `SetTableStats`, which pointer-replaces and would clobber them
   ([parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §4.2).

`stawidth` persistence is folded in here: compute a real per-column width during
ANALYZE ([05](05-statistics-and-estimation-inputs.md) §2) and write it into the
`pg_statistic` row instead of the placeholder `8`, so the restored per-column width
sharpens the cost model's `Width` estimate on cold start.

## 4. The known unsolved bug, carried forward verbatim

A first implementation attempt is diagnosed in
[parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §4.2.1,
and this chapter inherits its two findings so the next attempt does not re-discover
them:

- **The append must target `catalogDBOids(ctx)`, not `catalog.DefaultDBOid`.**
  `pg_class` rows are written per database by CREATE TABLE, so an append that copies
  `persistStatsToPGStatistic`'s hardcoded `DefaultDBOid` lands in the wrong
  database's heap and is written, durable, and never read. Use the DDL path's
  `catalogDBOids(ctx)`.
- **A second ANALYZE + restart does not round-trip, and the cause is not yet
  established.** With the DB-OID fix, one ANALYZE + restart recovers `reltuples`
  correctly, but on the *third* server start the `pg_class` reload does not observe
  the relation's rows — something other than `loadUserTablesFromHeapForDB` supplies
  the relation on that path. The two catalog-DDL durability mechanisms (`pg_class`
  heap-append vs goopg-private WAL record + startup replay,
  [memory: two catalog-DDL durability mechanisms]) are the suspects. **The next
  attempt must start by establishing which code path reconstructs a relation on a
  start that follows a start which already reconstructed it** — the work was
  reverted rather than landed half-working, and that decision stands until this is
  understood.

## 5. A latent trap this fixes

Restoring `RowCount` also fixes a regression the statistics reload itself
introduced. `needsVacuum` (`autovacuum/launcher.go`) returns `false` when
`Stats != nil && Stats.RowCount == 0`, and the current reload makes `Stats` non-nil
with `RowCount == 0` ([parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §4.2.1),
so **autovacuum is suppressed on restarted servers**. Persisting `RowCount` restores
the non-nil-with-real-count state autovacuum expects. Noted so this chapter's value
is not read as cost-model-only.

## 6. Interim compatibility: the fallback is a floor, not a fork

The [05](05-statistics-and-estimation-inputs.md) §4 fallback and this chapter's
persistence are the **same value from two sources**, deliberately compatible:

- the fallback derives `rows` from `relpages · density` using the **live** block
  count when `RowCount == 0`;
- persistence restores an **exact** `RowCount` so the fallback branch is never
  taken.

When persistence lands, the fallback stays as the cold-start-before-first-ANALYZE
path (exactly as PG keeps `estimate_rel_size` for never-analysed relations). There
is no rework: the persistence phase removes the `RowCount == 0` case for analysed
relations and changes nothing else. This is why designing persistence now, even
deferred, is worth the page — it guarantees the interim fallback is on the same
axis as the destination.

## 7. Divergence from PostgreSQL

- **Persistence is append-and-reload, not in-place update** (§3). PG updates the
  `pg_class` row in place; goopg appends and takes the last live tuple, because it
  has no runtime in-place on-disk-catalog update path (§2). Same *durable value*,
  different *write mechanism*.
- **Until this lands, `reltuples` is a live-block estimate on cold start** (§6) —
  PG persists it; goopg reconstructs it. The difference is invisible to a warm,
  in-session-ANALYZE'd query and bounded (coarser, uniform-packing assumption) on a
  cold one.
- **This chapter is deferred by design** — it is the one part of the bundle that is
  future-facing scaffolding, present so the milestone's interim is forward-
  compatible, not so it ships with the milestone.
