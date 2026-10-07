# M0146-0063a — a crash start discards the visibility-map forks

Status: done 2026-10-07 (`b80fc34a3`). Parent: M0146-0063, which stays open.

## Symptom

An index-only scan returned deleted rows after a crash. On a throwaway
cluster:

1. `CREATE TABLE vt (a int, b int)` with 5000 rows, `CREATE INDEX vt_a ON
   vt (a)`, then VACUUM (`relallvisible` 23 of 23 pages).
2. Clean stop and start: `SaveVM` wrote `base/5/<relfilenode>_vm`, and
   `VMLoadForks` loaded it.
3. `DELETE FROM vt WHERE a < 800`, CHECKPOINT, then `kill -9`.
4. Restart. With seq and bitmap scans off, `SELECT count(*) FROM vt WHERE
   a < 1000` is an Index Only Scan and returned **500**; the heap holds 200.
   `relallvisible` still read 23.

## Cause

goopg's map lives in memory (`storage.VisibilityMap`).
- **Persistence:** it reaches disk only from the clean-shutdown `SaveVM`
  (`cmd/goopg/main.go`).
- **Bit changes:** VACUUM sets bits and heap writes clear them
  (`ClearBlock`), with no WAL for either. The native `RecordKindHeapVisible`
  has a no-op redo, and goopg's heap redo clears only `PD_ALL_VISIBLE` on
  the heap page.
- **Crash restart:** `VMLoadForks` loaded the forks as they stood at the
  LAST CLEAN shutdown. A page modified afterwards still read all-visible,
  and an index-only scan skips the heap visibility check on such a page.

PG's map is crash-safe:
- `visibilitymap_set` is WAL-logged as `XLOG_HEAP2_VISIBLE`;
- every heap record that clears a page's bit carries
  `XLH_*_ALL_VISIBLE_CLEARED`, and its redo calls `visibilitymap_clear` on
  the fork (`heap_xlog_delete`, `heap_xlog_update`, `heap_xlog_insert`,
  `heap_xlog_multi_insert`, `heap_xlog_lock`, heapam_xlog.c).

## Change

- `storage.DiscardVMForks(dataDir)` removes every `_vm` fork under
  `base/<db>/` and `global/`.
  - Removing the files, rather than skipping them, matters. A later clean
    shutdown rewrites only the relations whose bits were set again, so a
    stale fork left beside them would be loaded on the next clean start.
- `initdb.Open` calls it instead of `VMLoadForks` when the start is a crash
  recovery (`recov.crashRecovery`: pg_control not `DB_SHUTDOWNED`, or an
  online checkpoint).
  - The map then starts empty, as on a fresh cluster. That is always
    correct: a cleared bit only means "check the heap".
  - A clean start loads the forks as before.

## Verification

- The scenario above: the index-only count is 200, matching the heap; the
  server logs `discarded visibility-map forks after crash recovery
  forks=1`; a fresh VACUUM restores `relallvisible` 23.
- `TestCrashStartDiscardsVMForks` (initdb) fails without the change on
  both of its assertions: the bits are trusted and the fork survives.
  `TestCleanStartLoadsVMForks` pins the clean half.
- Gates:
  - units, tpch-spotcheck, arm 24/24 and ea-ratchet PASS;
  - fire set: no fires at SF0.25 or SF1;
  - sf025 96/96, plan shapes 99/99 the same.

## Not covered (M0146-0063, still open)

- **A crash-safe, WAL-logged map.** Needed:
  - VACUUM's `SetAllVisible` / `SetAllFrozen` emit `XLOG_HEAP2_VISIBLE`
    (its redo, `replayDecodedXLogHeap2Visible`, already writes the fork);
  - heap DML sets `XLH_*_ALL_VISIBLE_CLEARED` when it clears a bit, and
    the heap redo arms clear the fork bit as well as `PD_ALL_VISIBLE`;
  - the map is written at checkpoint.

  Until then every crash start begins with a cold map. That includes the
  TPC-H arm's online clone of the bench cluster, which already had no
  `_vm` file.
- The entry's suggested interim, "a checkpoint that also saves the VM", is
  **unsafe** without the redo half: a fork saved at a checkpoint would go
  stale for every page modified after it, the very defect fixed here. It
  must land together with the WAL-logged clear.
