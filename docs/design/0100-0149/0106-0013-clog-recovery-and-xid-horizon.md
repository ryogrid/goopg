# 0106-0013: CLOG Crash-Recovery and XID Horizon

**Status:** accepted  
**Milestone:** M0106-0013  
**Author:** Ralph (autonomous)

## Problem

After a `kill -9` (SIGKILL) of the goopg server, a restart failed to restore
committed user-table rows. Two coupled bugs caused the failure:

### Bug 1 — SLRU not loaded on restart

`mvcc.CLog` uses two durability channels:
- A flat file `global/pg_xact` — written with `os.WriteFile` (no fsync, just OS
  page cache).
- A PG-canonical SLRU at `pg_xact/` — written with `f.Sync()` (fsynced to
  stable storage on every commit, introduced in M0106-0010 batched-44).

`EnablePGSLRUMirror` was write-only: it backfilled SLRU from `c.data` but
never read the SLRU into `c.data`. On restart, `OpenCLog` populated `c.data`
from the flat file. If the flat file was stale (e.g. truncated by a failed
`os.WriteFile` mid-write at crash time), committed XIDs appeared as
`TxnStatusUnknown` in `c.data`.

`MarkUnknownAsAborted` then wrongly marked those XIDs as Aborted (and ORed
the aborted bit into the SLRU, producing `0x03` = sub-committed, a corruption
artifact). `loadUserTablesFromHeap` saw the pg_class row's xmin as Aborted and
skipped it — the table was never registered in the catalog after restart.

### Bug 2 — txnMgr.NextXID too low after crash

`txnMgr.NextXID()` was advanced only as far as the highest XID observed in
the system catalog heap pages (`pg_class`, `pg_attribute`) via
`highestCatalogXID`. For the typical crash scenario (CREATE TABLE + N INSERTs
+ SIGKILL), the catalog XIDs are low (CREATE TABLE = XID K) but the INSERT
XIDs are K+1..K+N.

`captureSnapshotLocked` sets `Snapshot.Xmax = m.nextXID`. The first SELECT
after restart captured a snapshot with `Xmax = K+1`. `SeesCommittedXID(K+2)`
returned false (`xid >= Xmax`) for every INSERT row, making them invisible
even though the clog correctly showed them as committed.

## Fix

### Fix 1: Load SLRU into `c.data` on restart (`internal/mvcc/clog.go`)

`EnablePGSLRUMirror` now calls `loadFromSLRULocked(dir)` before the backfill
pass. For each existing SLRU segment file the 2-bit-per-XID encoding is
decoded and merged into `c.data`:

- SLRU bits `01` (committed) → `c.data[xid] = TxnStatusCommitted`
- SLRU bits `10` (aborted)   → `c.data[xid] = TxnStatusAborted`
- SLRU bits `11` (0x03, corruption artifact) → treated as committed (the
  committed bit was set at some point; the aborted bit was ORed by a previous
  buggy `MarkUnknownAsAborted` run)
- SLRU bits `00` → skip (unknown; MarkUnknownAsAborted handles it)

The SLRU is the authoritative source because it is fsynced at every
commit/abort. The flat file has no fsync guarantee.

### Fix 2: Advance NextXID from `clog.HighestKnownXID()` (`internal/initdb/open.go`)

After `EnablePGSLRUMirror` (which now populates `c.data` from SLRU), a new
`clog.HighestKnownXID()` method returns the maximum XID with a terminal status
in `c.data`. `txnMgr.SetNextXID(highClogXID + 1)` is called at two points:

1. After `MarkUnknownAsAborted` — ensures the catalog-load and first
   user-space snapshot have a high enough Xmax.
2. After `replayCLogFromWAL` — re-applies in case WAL replay stamped more XIDs.

### Fix 3: WAL-based CLOG stamping (`internal/initdb/xact_recovery.go`)

`replayCLogFromWAL` does a second-pass WAL scan (like `replayIndexDDLRecords`)
and for each commit/abort record stamps the XID into the clog and advances
`txnMgr.NextXID`. This covers the narrow window where the WAL fsync completed
but both the flat-file and SLRU writes were lost (e.g. power failure).

Supported record types:
- Native: `RecordKindXactCommit`, `RecordKindXactCommitInval`,
  `RecordKindXactAbort` — XID at payload bytes 1..4.
- Canonical (M0106-0010 batched-46): `RmgrXact / xlogXactCommit|xlogXactAbort`
  — XID in `XLogRecord.Header.XID`.

## Why it works

After the fix, the startup sequence for a crash-recovery Open is:

1. Physical WAL replay — heap pages restored.
2. `OpenCLog` reads flat file (may be stale).
3. `EnablePGSLRUMirror` → **loadFromSLRULocked** → `c.data` now reflects
   the fsynced SLRU state (all commits/aborts from before the crash).
4. `loadCatalogSnapshot` (JSON, may be absent).
5. `highestCatalogXID` → `txnMgr.SetNextXID(catalogHighXID + 1)`.
6. `MarkUnknownAsAborted(txnMgr.NextXID())` — only sweeps catalog-range XIDs;
   user-table INSERT XIDs are already committed in `c.data` (from SLRU) and
   are not touched.
7. **`txnMgr.SetNextXID(clog.HighestKnownXID() + 1)`** → NextXID now covers
   all committed XIDs including user-table INSERTs.
8. Catalog tables loaded from heap.
9. `replayCLogFromWAL` — belt-and-suspenders WAL scan (no-op in SIGKILL case
   where SLRU was already correct).
10. **Re-advance NextXID** from `HighestKnownXID()` again.
11. Server ready; first SELECT snapshot has `Xmax ≥ all committed XIDs`.

## Tests

- `TestEnablePGSLRUMirrorLoadsFromDisk` — SLRU committed/aborted bits merged
  into `c.data`; empty flat file case.
- `TestEnablePGSLRUMirrorFlatFileWinsIfNewer` — flat-file Committed survives
  when SLRU is Unknown for the same XID.
- `TestHighestKnownXIDReturnsMaxTerminalXID` — returns highest non-Unknown XID.
- `TestHighestKnownXIDEmptyReturns0` — empty clog returns 0.
- `TestLoadFromSLRU_SegFileNotMultipleOfBlockSize` — partial segment file.
- `TestReplayCLogFromWAL_NativeCommit` — native commit record stamps clog.
- `TestReplayCLogFromWAL_NativeAbort` — native abort record stamps clog.
- `TestReplayCLogFromWAL_CommitInvalAlsoStamps` — CommitInval = commit.
- `TestReplayCLogFromWAL_MissingWalDir` — missing WAL dir is no-op.
- `TestKillKillRecovery` — re-enabled; 100 rows survive SIGKILL + restart.
- `TestPort_Recovery013CrashRestart` — re-enabled; committed row survives crash.
