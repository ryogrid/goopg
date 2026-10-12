# M0146-0089 — a refreshed materialized view keeps its kind and its rows across restarts

Status: landed 2026-10-12.
Parent: none (S2, placed into banner item 2a by the owner on 2026-10-12).
Filed by M0146-0063b.

## Problem

The repro from the task:

1. `CREATE TABLE s(a int)` with 3000 rows, then
   `CREATE MATERIALIZED VIEW mv AS SELECT a FROM s`, then `CHECKPOINT`.
2. `DELETE FROM s WHERE a > 1000`, then `REFRESH MATERIALIZED VIEW mv`,
   then `CHECKPOINT`.
3. Two clean restarts.

On the filing build (`ea45c154d`) `mv` came back from the second restart
with `relkind = 'v'`. It read the base table live, and REFRESH failed with
`"mv" is not a materialized view`. PostgreSQL keeps `relkind = 'm'` and the
rows stored by the last REFRESH.

Two separate defects produced this.

1. **The kind.** The second start registered user tables from the M0114 JSON
   catalog cache, and that cache did not carry the matview flag.
   - `17e9db192` (M0141-S2a-fix2r-c, 2026-10-10) retired the cache, so every
     start now rebuilds from the catalog heap.
   - Measured on that commit's parent: `relkind` goes `m` → `v` after the
     second restart. On the commit itself `m` holds.
   - This half was already fixed at HEAD and only needed a regression pin.
2. **The rows.** This defect was still live at HEAD. If nothing scans `mv`
   between the REFRESH and the restarts, the pre-REFRESH rows reappear next to
   the new ones: `count(*) = 3938`, values 1..2938, of which 1..1000 appear
   twice. An immediate-mode crash after the REFRESH gives the same result.
   - The task's repro did not show this, because it scanned `mv` before
     restarting. The scan's hint-bit writes dirtied the pages.

## Cause of the row resurrection

goopg's REFRESH deletes the old rows in place and re-inserts.
`truncateRelation` stamps `xmax` on every visible tuple, and then
`materializeView` inserts the new result.

The stamp was written into the buffer, but the buffer was never marked dirty
and no WAL was written. Checkpoints skip clean buffers, so the deletes never
reached disk. Redo had nothing to replay either.

The count shows exactly what was lost. 3000 `int` rows fill 13 pages of 226
plus one page of 62. The last page was dirtied by the re-population's
inserts, so its 62 stamps survived. The other 2938 rows came back.

The same helper serves the logical-replication apply worker's TRUNCATE, which
had the same durability hole.

## Fix

`truncateRelation` now does what `heap_delete` does after setting `xmax`.

- **Dirty and log each stamp.** Each stamped tuple goes through
  `markHeapDeleteDirtyAndClearVM`, under the page's content lock. That is the
  same helper DELETE, MERGE and the apply worker's keyed delete use. It marks
  the buffer dirty and emits the heap-delete WAL record, so checkpoints write
  the page and crash recovery replays the delete.
- **Visibility map.** The helper also clears the page's visibility-map bit,
  which replaces the explicit VM clear M0146-0063b added there.
- **Failures.** A slot the stamp cannot address (pruned or redirected) is
  skipped, as before. A WAL or dirty-marking failure now fails the statement
  instead of being ignored.

## Verification

- **Manual repro on the fixed binary.**
  - Three clean restarts keep `relkind = 'm'` with 1000 distinct rows. A
    further base-table delete does not show through, and REFRESH still works.
  - An immediate stop right after REFRESH recovers to 1000 rows. The pre-fix
    binary gives 3938.
- **`TestPort_MatviewSurvivesRefreshAndTwoRestarts`.** It checks REFRESH
  followed by two fast restarts with no intervening scan, then `relkind`, the
  stored rows, a REFRESH after the restarts, and a REFRESH followed by an
  immediate-mode crash. It fails at HEAD with 3938.
- **Regress.** `matview`, `create_am` and `select_into` are identical to
  HEAD.
- **Logical replication.** `TestPort_PgoutputInteropPGToGoopgTruncate`
  passes; it exercises the apply worker's TRUNCATE.
- **Gates.**
  - Units and executor pass. In postmaster, one run failed and the rerun
    passed; the failing test was not captured, so this was taken as a flake.
  - tpch-spotcheck (Q12=2, Q13=33), the acceptance arm (values identical) and
    sf025 PASS=99.

## Residuals (ledgered)

- **REFRESH is not a heap swap.** PostgreSQL's non-concurrent REFRESH builds
  a new heap and swaps relfilenodes (`refresh_by_heap_swap`, `make_new_heap`,
  `finish_heap_swap`), so the old file is dropped at commit. goopg deletes in
  place and re-inserts, which leaves the old tuples as dead space until
  VACUUM. It also writes one WAL delete record per old row where PG writes
  none.
- **No `cmax` on the stamp.** The in-place delete still does not stamp `cmax`
  (heap\_delete does). Changing it touches the unique-check visibility of the
  re-population, which is a separate change.
