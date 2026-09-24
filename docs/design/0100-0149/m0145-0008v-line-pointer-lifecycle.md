# M0145-0008v: heap line-pointer lifecycle (LP_DEAD, LP_UNUSED, recycling)

Status: **S1 and S2 LANDED 2026-09-25; S3 open.** Task: `.ralph/fix_plan.md`
M0145-0008v (Kind: impl, Parent: M0145-0008t). Found by M0145-0008t
(`m0145-0008t-prune-on-access.md`, "The premise that did not hold").

## Problem

goopg's `PageAddHeapTuple` always appends a new line pointer, and
`PageGetHeapFreeSpace` answers 0 once a page holds `MaxHeapTuplesPerPage`
(291) of them. A row updated 291 times therefore fills its page however much
the page is pruned, and every later version goes to a new page. pgbench's
2-row `pgbench_branches` reaches ~80 heap blocks in 30 s.

goopg cannot simply recycle unused slots today. `pagePruneCore` marks a dead
**non-HOT** tuple `LP_UNUSED` immediately, while index entries still point at
it. Reusing that slot would hand a stale index entry a new, unrelated tuple.

## PG's lifecycle

- **Prune** (`heap_page_prune_and_freeze`,
  `postgres/src/backend/access/heap/pruneheap.c`):
  - a dead HEAP_ONLY tuple has no index entry, so it becomes `LP_UNUSED`;
  - a dead non-HOT tuple, or a dead chain root with no live tip, becomes
    `LP_DEAD`, with no storage;
  - a chain root with a live tip becomes `LP_REDIRECT`.

  `mark_unused_now` makes dead items `LP_UNUSED` directly only for a VACUUM
  of a relation with **no indexes** (`vacuumlazy.c:1976`). The WAL record
  `xl_heap_prune` carries redirected / now-dead / now-unused sub-records
  (`XLHP_HAS_REDIRECTIONS` / `XLHP_HAS_DEAD_ITEMS` /
  `XLHP_HAS_NOW_UNUSED_ITEMS`, `heapam_xlog.h`).
- **VACUUM, first heap pass** (`lazy_scan_prune`): prunes, then collects
  every `LP_DEAD` item on the page into `dead_items`, including items left
  `LP_DEAD` by earlier on-access prunes.
- **Index vacuum:** the index AMs delete the entries pointing at `dead_items`.
- **VACUUM, second heap pass** (`lazy_vacuum_heap_page`, `vacuumlazy.c`):
  - each `LP_DEAD` becomes `LP_UNUSED`;
  - then `PageTruncateLinePointerArray`;
  - then a `PRUNE_VACUUM_CLEANUP` prune record with only now-unused items.
- **Reuse:** `PageRepairFragmentation` sets `PD_HAS_FREE_LINES` when unused
  items exist. `PageAddItemExtended` then scans for the first unused item
  without storage and reuses its offset (`bufpage.c`).

## Slices

1. **S1: prune writes `LP_DEAD`.**
   - `pagePruneCore` marks dead non-HOT tuples, and dead roots with no live
     tip, `LP_DEAD` with no storage; HEAP_ONLY tuples stay `LP_UNUSED`.
   - A VACUUM of an index-less relation keeps marking them unused
     (`mark_unused_now`).
   - The WAL record gains a now-dead list, and the redo arms apply it.
   - Every reader must treat `LP_DEAD` as "no tuple".
   - VACUUM gathers all `LP_DEAD` items into `DeadTIDs`.
   - No recycling yet, so the observable change is limited to index entries
     that on-access prunes used to leak: VACUUM now removes them.
2. **S2: the second heap pass.** After index cleanup, VACUUM turns the
   page's collected `LP_DEAD` items into `LP_UNUSED` and truncates the
   trailing unused items, WAL-logged as PG's cleanup record.
3. **S3: recycling.**
   - Compaction sets `PD_HAS_FREE_LINES` when unused items remain.
   - `PageAddHeapTuple` reuses the first unused item without storage.
   - `PageGetHeapFreeSpace` stops answering 0 at the line-pointer ceiling
     while a free line exists.
   - Measure pgbench small-table growth and TPC-B tps.

Each slice keeps the invariant that recycling depends on: an `LP_UNUSED`
item has no index entry pointing at it. S3 lands only after S1 and S2 hold
it for every producer of `LP_UNUSED` (prune, VACUUM, redo).

**A second precondition for S3: autovacuum must vacuum indexes.** Only the
manual VACUUM operator (`operators_vacuum.go`, `vacuumIndexes`) deletes
index entries for `Stats.DeadTIDs`. The autovacuum launcher
(`postmaster/autovacuum/launcher.go`) calls `vacuum.VacuumWithOptions` and
drops the TIDs. Index entries for tuples that autovacuum or an on-access
prune removed are therefore never deleted today. They are harmless only
while slots are never reused.

## S1 as landed (2026-09-25)

- **`storage.pagePruneCore`** records dead items the way
  `heap_prune_record_dead_or_unused` does:
  - a dead HEAP_ONLY tuple goes to `PruneResult.Unused`;
  - a dead non-HOT tuple, a dead HOT root with no live tip, and a
    **redirect whose whole chain died** go to `PruneResult.Dead`. The
    redirect case was previously left as a stale redirect and ledgered as
    unexpressible;
  - `PageVacuumPrune`'s `markUnusedNow` flag sends the dead items to
    `Unused` instead. Every current caller passes false: vacuumCore does not
    know whether the relation has indexes (ledgered).
- **`storage.PruneHeapPageBySlots(p, unused, lpDead)`** is the compaction
  that `VacuumHeapPageBySlots` now wraps. It writes `ItemIDDead` with no
  storage (offset 0, length 0) for the `lpDead` items, which may be
  LP_NORMAL or LP_REDIRECT, and repacks.
- **WAL.** `LogHeapPruneOptFunc` and `xlog.EncodeHeapPruneOptPG` take the
  dead list and emit PG's `XLHP_HAS_DEAD_ITEMS` sub-record, in PG's order
  between redirections and now-unused items.
  - `decodeXLogHeapPrune` returns the dead offsets; it used to read and
    discard them.
  - `replayDecodedXLogHeapPrune` applies them through the same
    `PruneHeapPageBySlots`, so runtime and redo stay byte-identical
    (`TestReplayPGHeapPruneDeadItemsLikeRuntime`).
- **VACUUM** puts every LP_DEAD item on the page into `Stats.DeadTIDs`
  (`storage.PageDeadItems`, PG's `deadoffsets`). That includes items an
  earlier on-access prune left dead, so manual VACUUM's index pass now
  removes their index entries. Before, the index entries of tuples an
  on-access prune removed were never deleted.
- **Readers** already treated any non-LP_NORMAL item as "no tuple". Two
  consequences:
  - `heapChainDeadToAll` already reports an LP_DEAD root as dead-to-all, so
    the index-scan kill list and the split-time purge now also clean entries
    that point at pruned tuples;
  - amcheck's heap check already accepts LP_DEAD.

**Measured.** pgbench TPC-B A/B across two rounds, eight runs: tps within
host noise (candidate mean 564 against 572 on HEAD; the late runs of each
round were slow on both arms). Table growth is unchanged, as expected:
nothing is recycled yet.

**Gates (staged tree).**
- units, `tpch-spotcheck` and acceptance (24 MATCH) all pass.
- `tpcds-sf025`: 96/96, 99/99 shapes.
- Isolation family and full regress: only the failures HEAD also has.
- pg_amcheck ports: PASS.
- Real-PG WAL consumption (`TestE2E_PGStandbyFullCycle`, the pg_waldump
  ports, the standby catch-up E2Es): PASS.
  `PgWaldumpVacuumPruneRoundtrip` **skips**, so it proved nothing (noted
  during S2, which verified the records with `pg_waldump` by hand). `TestE2E_PGColdStartOnGoopgDataDir` fails identically on a
  clean HEAD worktree, on goopg's `work_mem` line in `postgresql.conf`.

**Next: S2.** After `vacuumIndexes`, a second pass over the pages that had
LP_DEAD items turns them into LP_UNUSED and truncates the line-pointer
array. It is WAL-logged as a `PRUNE_VACUUM_CLEANUP` record with only
now-unused items. The redo needs a DEAD→UNUSED primitive, because
`VacuumHeapPageBySlots` skips non-NORMAL items. Autovacuum must run the
index pass too before S3.

## S2 as landed (2026-09-25)

- **The `heap_lp_lifecycle` cluster capability.** Clusters written before
  S1 hold LP_UNUSED items whose index entries were never deleted, so
  nothing may ever reuse a slot there.
  - `initdb` now names the capability in `global/pg_goopg_features`, the
    same marker the NULL-keyed index entries use.
  - Open sets `storage.HeapLinePointerLifecycle` from it.
  - A cluster without it keeps S1 behaviour: LP_DEAD items stay dead.
- **Audit of every LP_UNUSED writer in a capable cluster:**
  - the prune: dead HEAP_ONLY tuples, which have no index entry;
  - `PageRemoveHeapTuple`: the orphaned HOT tuple undo, never indexed;
  - the new second pass itself.
- **`storage.PageVacuumDeadItems`** is `lazy_vacuum_heap_page`'s page step:
  - LP_DEAD items (asserted storage-less) become LP_UNUSED;
  - then **`storage.PageTruncateLinePointerArray`**, a port of bufpage.c:
    it drops the trailing unused items, keeps item 1, and sets or clears
    `PD_HAS_FREE_LINES`.
  - No tuple byte moves, so the exclusive content lock suffices.
- **`vacuum.VacuumDeadItems`** is `lazy_vacuum_heap_rel`: it runs per block
  over `Stats.DeadTIDs`, skips any item that is no longer LP_DEAD, and logs
  `xlog.EncodeHeapVacuumCleanupPG`:
  - `XLOG_HEAP2_PRUNE_VACUUM_CLEANUP`, reason `PRUNE_VACUUM_CLEANUP`;
  - now-unused items only, with no `XLHP_CLEANUP_LOCK`;
  - through the new pool hook `LogHeapVacuumCleanup`.
- **The VACUUM operator** runs the second pass only when `vacuumIndexes`
  reports every index clean:
  - all indexes btree, where `USING hash` counts because it rides the
    btree substrate;
  - every one opened and vacuumed without error.
  - A GIN, GiST or BRIN index, or any failure, keeps the LP_DEAD items
    dead.
- **Redo.** `replayDecodedXLogHeapPrune` recognises the cleanup shape (no
  cleanup-lock flag; only now-unused items) and applies
  `PageVacuumDeadItems`, PG's `lp_truncate_only` arm. Freeze records also
  use this info, so the recognition is by content, not by opcode.
  - Byte-identical to runtime: `TestReplayPGHeapVacuumCleanupLikeRuntime`.
  - PG 18.3's `pg_waldump` decodes goopg's records as `PRUNE_ON_ACCESS …
    ndead: N, dead: [...]` and `PRUNE_VACUUM_CLEANUP … nunused: N, unused:
    [...]`. Checked on a fresh cluster with DELETE + VACUUM; the
    `pg_waldump` testport case skips.

**Consequence.** Truncation lets the next insert reuse the truncated
trailing offsets, which is PG's behaviour. `TestVacuumSecondHeapPassKeepsIndexesExact`
covers it end to end:
- after DELETE + VACUUM + INSERT, index-driven queries equal the heap
  (`+ 0` twins);
- a capable btree-only table has no LP_DEAD item left and shows
  `PD_HAS_FREE_LINES`;
- a legacy cluster and a GIN-indexed table keep their LP_DEAD items.

**Measured.** pgbench TPC-B A/B on fresh (capable) clusters: 635 / 639 tps
on HEAD against 637 / 636 on the candidate. Growth is unchanged: interior
unused items are only reused in S3, and autovacuum does not run the second
pass.

**Gates.**
- units, `tpch-spotcheck`, acceptance (24 MATCH) and `tpcds-sf025` (96/96,
  99/99 shapes) pass.
- Isolation and full regress: only HEAD's failures.
- Real-PG WAL / amcheck testport set: PASS.

**Found on the way** (pre-existing, filed): goopg writes an 11-byte
`xlhp_freeze_plan`, but PG's struct is 12 bytes, with one byte of padding
before `ntuples`. PG 18.3's `pg_waldump` therefore reads goopg's freeze
records as `ntuples: 256` with offsets `[768, 1280, …]`. goopg's own decoder
reads 11 bytes and agrees with itself; a real PG standby would misapply
every freeze plan.

**Next: S3.**
- Autovacuum's index pass plus the second pass.
- `PageAddHeapTuple` reuses the first unused item under
  `PD_HAS_FREE_LINES` (`PageAddItemExtended`), gated on the capability.
- `PageGetHeapFreeSpace` stops answering 0 at the ceiling while a free line
  exists.
- Audit tuple-grain SSI SIREAD locks on reused TIDs.
- Measure pgbench small-table growth.
