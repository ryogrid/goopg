# M0146-0063 — the visibility map is WAL-logged and checkpointed

Status: done 2026-10-07 (`de6c9b67f`). Children: M0146-0063a (crash-start
discard, `b80fc34a3`) and M0146-0063b (every heap writer clears the map,
`ea45c154d`).

## Problem

PG's visibility map is crash-safe. `visibilitymap_set` is WAL-logged
(`XLOG_HEAP2_VISIBLE`), and every heap record that clears a bit carries
`XLH_*_ALL_VISIBLE_CLEARED`, so redo re-applies the clear
(heapam_xlog.c). A crash restart, a base backup or a standby therefore has
the primary's map.

goopg's map (`storage.VisibilityMap`) lived in memory:
- its `_vm` forks were written only by the clean-shutdown `SaveVM`;
- no change of it was WAL-logged;
- after a crash the forks were the last clean shutdown's, and an index-only
  scan returned deleted rows. M0146-0063a made a crash start discard them,
  so every crash start, an online clone included, began cold. The TPC-H
  arm's clone of the bench cluster read `relallvisible = 0`.

## Design

The map's own mutators are the choke point every writer already goes
through: VACUUM's `SetAllVisible`/`SetAllFrozen`, DML's `ClearBlock`, row
locks' `ClearAllFrozen`, and TRUNCATE/DROP's `DropRelation`. The hook is
put there rather than in each heap record. Per-record flags would miss the
writes that emit only an FPI or no WAL record at all: catalog xmax stamps,
REFRESH's stamps, MultiXact lock stamps.

- **Change record.** `VisibilityMap.SetWALHook` installs a writer called
  under the map's mutex for every change of a block's bits, and never for a
  no-op (most inserts land on an already-clear page).
  - VACUUM's set and DML's clear both run under the heap buffer's
    exclusive lock, so per block the WAL order is the apply order.
  - The record is the native `RecordKindHeapVisible` (29), long in the WAL
    vocabulary with a no-op redo. Its flags byte is `storage.VMWAL*` =
    `xlog.HeapVisible*`: set ALL_VISIBLE (0x01), set ALL_FROZEN (0x02),
    clear ALL_FROZEN only (0x04), drop the relation (0x08), or none = clear
    both.
- **Ordering against the heap change.** The clear is logged after the heap
  record and before the transaction's commit record or any index insert.
  WAL is sequential:
  - if the clear is lost, the heap change's transaction is uncommitted and
    its index entries are lost too, so an all-visible page is still right
    for every surviving tuple;
  - if the commit survives, the clear does too.
- **Redo** (`replayHeapVisible`) applies the record to the `_vm` fork
  through smgr: an OR for a set (`redoVMPageForBlock`, `VMPageSetBits`), the
  existing `redoClearVMBitsForHeapBlock` for a clear, and an emptied fork for
  a drop.
  - A clear or drop on an absent fork does nothing, and creates none.
  - A vm page that fails checksum verification is replaced by an
    initialised one (`RBM_ZERO_ON_ERROR`); clearing is the safe direction.
- **Checkpoint.** The flush phase (`FlushCLOGFn`) calls `VMSaveForks`
  before the checkpoint record, so the forks are current as of a point at
  or after the redo pointer, and replay applies every change logged after
  it.
  - The save snapshots under the lock and writes outside it, so an insert
    never waits on the forks' fsyncs.
  - A failed save fails the checkpoint, and the redo pointer stays put.
  - The save also removes the fork of every relation `DropRelation`
    forgot. A TRUNCATE keeps its relfilenode, and the old fork used to be
    loaded on the next start.
- **Checksums.** `WriteVMFork` stamps `pd_checksum`: redo reads the forks
  through smgr, which verifies it when the cluster has checksums.
- **Capability** `visibility_map_wal_logged` in `global/pg_goopg_features`:
  - initdb writes it;
  - an existing cluster gets it from the first checkpoint that saves its
    forks with the hook installed. Only from then are the forks
    checksummed and current, and changes logged;
  - a crash start loads the forks when the capability is present, after
    replay; without it, the last run predates the logging and the
    M0146-0063a discard still applies.

## Verification

Throwaway server, `kill -9` as the crash:

| scenario | index-only / heap count | relallvisible |
|---|---|---|
| VACUUM, clean restart, DELETE, crash | 200 / 200 | 19 of 23 (deleted pages cleared, rest warm) |
| VACUUM, CHECKPOINT, DELETE, crash | 400 / 400 | 20 |
| VACUUM, no checkpoint, crash | 999 / 999 | 23 (rebuilt by replaying the set records) |
| TRUNCATE, aborted insert, clean restart | 100 / 100 | 0 |
| the same again, then crash | 0 / 0 | 0 |

No fork was discarded and no replay error was logged.

Tests:
- `TestVisibilityMapWALHookLogsEveryChangeOnce`: one record per change,
  none for a no-op.
- `TestVMSaveRemovesDroppedRelationsFork`.
- `TestNativeHeapVisibleRedoKeepsTheForkCurrent`: redo of set, clear,
  frozen-only clear and drop, read back as startup reads the fork.
- `TestCrashStartWithCapabilityLoadsVMForks`,
  `TestCrashStartWithoutCapabilityDiscardsVMForks`, and
  `TestCleanStartLoadsVMForks` (the shutdown checkpoint records the
  capability).
- `TestGoopgFeaturesMarker`, for `addGoopgFeature`.

Gates:
- units, tpch-spotcheck, arm 24/24 and ea-ratchet PASS;
- fire set: no fires at SF0.25 or SF1;
- sf025 96/96 twice, plan shapes 99/99 the same. The runtime moves of the
  first run reversed on the second.

## Not covered

- **Record format.** The record is goopg-native, not PG's
  `XLOG_HEAP2_VISIBLE` plus `XLH_*_ALL_VISIBLE_CLEARED` flags on the heap
  records. A heterogeneous PG standby therefore gets no visibility-map
  bits. That is safe (it fetches the heap), but not PG's WAL. goopg also
  does not maintain `PD_ALL_VISIBLE` on heap pages at runtime. Ledgered.
- **Fork rewrites.** `VMSaveForks` rewrites every tracked relation's fork at
  every checkpoint; there is no dirty tracking. The forks are small, one
  page per 32k heap blocks. Ledgered.
- **Downgrade.** A binary older than this, run on a cluster with the
  capability, logs nothing, and a crash after it would load stale forks.
  Downgrade is unsupported; the capability is never removed. Ledgered.
