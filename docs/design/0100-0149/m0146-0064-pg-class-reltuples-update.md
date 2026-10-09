# M0146-0064 — `UPDATE pg_class SET reltuples` reaches the planner

Status: done 2026-10-07 (`ac3fd5323`). Parent: M0146.

## Symptom

Regress groupingsets runs `update pg_class set reltuples = 10 where
relname = 'gs_data_1'`, and the same for `bug_16784`. Regress join_hash does
the same for `bigger_than_it_looks`. PG then plans with the written row
count. goopg reported `UPDATE 1`, but:

- `SELECT reltuples FROM pg_class` still showed the ANALYZE value (2000);
- the plan kept 2000 rows, so the `enable_sort = off` CUBE over gs_data_1
  planned a sorted MixedAggregate where PG hashes every grouping set.

## Cause

- PG: `estimate_rel_size` (plancat.c) reads `rd_rel->reltuples`. An UPDATE
  of the pg_class tuple sends a relcache invalidation, which also resets
  every cached plan over the relation (plancache.c
  `PlanCacheRelCallback`), so the next statement in the session plans
  with the new value.
- goopg: `UPDATE pg_class` runs on the physical pg_class heap; the table's
  columns mirror that tuple descriptor (M0100-0010). Nothing reads that row
  back:
  - the planner and the virtual pg_class view read `catalog.Table.Stats`;
  - startup reloads `Stats` from the goopg_relstats sidecar heap
    (`persistRelSize`), not from pg_class.
- goopg's shared plan cache is invalidated only by DDL and ANALYZE/VACUUM
  statements.

## Change

`updateOp.syncPgClassRelStats`, called per updated row on both update paths
(the index-probe path and the scan path):

- **Gate.** It acts only when the target is `pg_catalog.pg_class` and the
  SET clause assigns `reltuples`. The heap row's reltuples can be older
  than `Stats`, since ANALYZE writes the sidecar, so an UPDATE of an
  unrelated pg_class column must not drag it back.
- **Apply.** The relation (by the row's oid) gets a pointer-replaced
  `Stats` with the new `RowCount`. `reltuples = -1` is PG's "never vacuumed
  or analyzed" and clears `Analyzed`. The value is appended to the sidecar
  in the same transaction, so an aborted UPDATE's row is dead to the
  startup reload.
- **Invalidate.** The shared plan cache is invalidated through
  `ctx.OnCommitDDL`, the hook DDL uses.
- **Rollback.** Inside an explicit transaction the previous `Stats` are
  recorded (`RelStatsUndoEntry` on the session). ROLLBACK restores them
  newest-first and invalidates again; `EndExplicitTransaction` drops the
  list once the transaction ends.

**relpages is deliberately not synced.** PG uses relpages only as the
denominator of the tuple density it rescales by the live block count, and
costs come from the live count. goopg costs a scan from `Stats.Pages` and
does not rescale an analyzed relation's reltuples (M0125-0003, ledgered).
A written relpages would become a fake page count in every cost; the probe
measured cost 1000.10 where PG has 9.00.

## Verification

- The regress groupingsets section (bug_16784's lateral CUBE, both
  gs_data_1 plans, and the gs_group_1 / gs_hash_1 comparison) is
  byte-identical to PG 18.3. HEAD planned the sorted MixedAggregate there.
- Probe: `reltuples = 10` gives `rows=10`, cost 9.10, as in PG. Inside
  BEGIN the value 500 is visible, and ROLLBACK restores 10.
- `TestUpdatePgClassReltuplesReachesPlanner` covers the sync, the
  other-column guard, the plan-cache invalidation, ROLLBACK and -1. The
  ROLLBACK check fails without the restore. The in-process fixture keeps no
  pg_class heap rows for user tables, so the test drives the per-row step
  directly.
- Regress A/B:
  - groupingsets −34 diff lines and join_hash −66;
  - privileges and reloptions identical.
- Gates:
  - units, tpch-spotcheck, arm 24/24 and ea-ratchet PASS;
  - fire set: no fires;
  - sf025 96/96, plan shapes 99/99 the same.

## Not covered

- **relpages**, and PG's density rescaling of an analyzed relation's
  reltuples by its live block count. Ledgered.
- **ROLLBACK TO SAVEPOINT** does not undo a reltuples UPDATE made after the
  savepoint, and an autocommit UPDATE that errors after syncing a row keeps
  the synced value. Ledgered.
- **Other pg_class columns** written by UPDATE (relhasindex, relallvisible,
  …) still change only the heap row. Ledgered.
- **join_hash `bigger_than_it_looks`.** With its lie believed, goopg plans
  PG's Parallel Hash. The participant build then outgrows hash_mem, and
  goopg has no parallel hash batching (`errParallelHashSpilled`), so the
  query errors where it used to answer 20000. Filed as **M0146-0090**: any
  underestimated Parallel Hash meets the same gap.
- goopg's `pg_class` view lists only user relations: 10 rows on a fresh
  database where PG has 416. Ledgered.
