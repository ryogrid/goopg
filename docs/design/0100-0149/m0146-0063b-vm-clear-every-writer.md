# M0146-0063b — every heap writer clears the visibility-map bits

Status: done 2026-10-07 (`ea45c154d`). Parent: M0146-0063, which stays open.

## Symptom

An index-only scan returned deleted row versions, with no crash involved.
On a throwaway server:

1. `up (k int PRIMARY KEY, v int)` with 2000 rows and an index on `v`, then
   VACUUM, which sets the map all-visible.
2. `INSERT … ON CONFLICT (k) DO UPDATE` twice, rewriting `v` on 12 rows.
3. `count(*) … WHERE v < 50 OR v > 99999` returned **61** by Index Only Scan
   and 49 by Seq Scan.

## Cause

PG clears a page's bits wherever it modifies an all-visible page:
- `heap_insert`, `heap_delete` and `heap_update` (HOT or not) clear both
  bits;
- `heap_lock_tuple` clears `VISIBILITYMAP_ALL_FROZEN` alone, because a locker
  xmax keeps every tuple visible to all but must still be frozen away.

goopg cleared the bits on its main INSERT, DELETE and non-HOT UPDATE paths
only (`ctx.VM.ClearBlock` beside `markHeapInsertDirty`, and
`markHeapDeleteDirtyAndClearVM`). These writers left them set:

| writer | site | what it left |
|---|---|---|
| upsert DO UPDATE delete half | operators_upsert.go (`markHeapDeleteDirty`) | the old version's page all-visible: the measured wrong result |
| speculative-insert cancel | operators_upsert.go `cancelSpeculativeRow` | the same |
| logical-apply delete | applyworker.go | the same, on a subscriber |
| pg_sequence / pg_proc / pg_namespace row delete | sys_pg_*.go | the same, on catalog relations |
| catalog xmax stamps | operators_ddl.go `stampCatalogRowsTuple` | the same |
| REFRESH MATERIALIZED VIEW stamps | operators_ddl.go `truncateRelation` | old rows' pages all-visible to an index-only scan of the matview |
| HOT update | operators_storage.go | both bits; an all-frozen page then holds an unfrozen xmin that VACUUM's all-frozen skip passes over |
| TOAST chunk insert | toast.go | both bits |
| row lock, plain and MultiXact | operators_lockrows.go | ALL_FROZEN, with the same freeze skip |

## Change

- The delete-shaped writers call `markHeapDeleteDirtyAndClearVM` (or
  `ctx.VM.ClearBlock` beside their own stamp loop).
- The HOT update and the TOAST inserts call `ctx.VM.ClearBlock` after their
  dirty-marking, as the main insert path does.
- `storage.VisibilityMap.ClearAllFrozen` clears ALL_FROZEN alone.
  `clearVMAllFrozenForLock(ctx, slot)` applies it at the three single-xid
  lock stamps and the two MultiXact lock stamps, keyed on the buffer slot's
  tag.
- Not changed: prune-on-access, which PG also leaves alone (a page holding
  dead tuples cannot be all-visible), and the initdb-time catalog writers.

## Verification

- `TestVMClearedByEveryHeapWriter` covers upsert DO UPDATE, HOT update,
  matview refresh and row lock (ALL_FROZEN cleared, ALL_VISIBLE kept).
  - Each table spans several pages, so a re-insert cannot clear page 0 by
    accident; with one small page two cases passed even without the fix.
  - All four subtests fail without the change.
- Server: the upsert scenario gives index-only 49 = heap 49. A refreshed
  matview's index-only count matches its heap (100).
- Gates:
  - units, tpch-spotcheck, arm 24/24 and ea-ratchet PASS;
  - fire set: no fires at SF0.25 or SF1;
  - sf025 96/96, plan shapes 99/99 the same.

## Not covered

- M0146-0063 itself: the bits are still not WAL-logged (see
  `m0146-0063a-vm-crash-discard.md`). Every clear site this slice made
  complete is a site the WAL-logged clear must also cover.
- `truncateRelation` (REFRESH MATERIALIZED VIEW) stamps xmax on the matview's
  rows with no `MarkDirty` and no WAL record. Its crash durability is
  unverified. Ledgered.
- Found while probing that: after a REFRESH and two clean restarts a
  materialized view comes back as `relkind = 'v'`, a plain view returning
  live data, and REFRESH fails. Filed as **M0146-0089** (S2, escalated).
