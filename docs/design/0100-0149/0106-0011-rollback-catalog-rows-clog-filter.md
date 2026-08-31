# 0106-0011 — Rollback Catalog Heap Row Filter (Clog-Aware)

**Status:** accepted
**Milestone:** M0106-0011 (operational relcache/catcache maintenance)
**Date:** 2026-05-20

## Problem

After the M0106-0010 batched chain (35..55), runtime `CREATE TABLE` writes
`pg_class` / `pg_attribute` rows in the PG18-canonical fixed-offset heap-tuple
layout via `syncTableToCatalogHeap` (`internal/executor/operators_ddl.go`).
Recovery's `loadUserTablesFromHeap` (`internal/initdb/open.go`) decodes those
rows with `catalog.DecodePGClassPhysicalRow` /
`catalog.DecodePGAttributePhysicalRow`, which sets the `physicalRow` flag.

The historical clog-status filter only ran when `!physicalRow`:

```go
if !physicalRow && clog != nil && clog.GetStatus(ht.Header.Xmin) != mvcc.TxnStatusCommitted {
    continue
}
```

That carve-out was added so PG basebackup tuples — whose `xmin` is from the
upstream cluster and is unknown to goopg's local clog — pass through. But
goopg's own runtime-emitted PG-canonical rows now share that decoder path,
so a `BEGIN; CREATE TABLE …; ROLLBACK` left the rolled-back row visible to
the next `Open()`: the row's `xmin` was correctly stamped Aborted in the
local clog, but the filter skipped the check for any physical-layout row.

`TestRollbackedTableNotVisibleAfterRestart` proves the regression:

```
rollback_ghost reappeared after restart — catalog heap rows not stamped on rollback
```

## Fix

`loadUserTablesFromHeap` now also excludes any row whose `xmin` is explicitly
Aborted in the local clog, regardless of decoder branch:

```go
if clog != nil && clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
    continue
}
if !physicalRow && clog != nil && clog.GetStatus(ht.Header.Xmin) != mvcc.TxnStatusCommitted {
    continue
}
```

The same explicit-Aborted filter is applied to the `pg_attribute` scan in
the same function.

### Why `== TxnStatusAborted` (and not `!= TxnStatusCommitted`)

- **Goopg-native rows** (legacy short layout): the second filter still runs
  and excludes Aborted **and** Unknown — preserving the M0030-0007 crash-
  during-COMMIT safety net.
- **Goopg-emitted physical rows**: the new filter excludes Aborted only.
  Unknown is treated as "not aborted" so the basebackup pass-through still
  works.
- **PG basebackup physical rows**: `xmin` is out-of-range for the local
  clog, `GetStatus` returns `TxnStatusUnknown`, the row passes through.

The Unknown-but-not-Aborted edge case for runtime canonical rows would
correspond to a goopg crash between heap insert and `XactCommit` /
`XactAbort` WAL emission. Crash recovery's existing implicit-abort path
stamps such xids as Aborted before `loadUserTablesFromHeap` runs, so the
filter sees Aborted (not Unknown) for that case in practice.

## Why not stamp xmax via `deleteCatalogRowsForOID`

The earlier `802f3ad fix(catalog): stamp xmax on rolled-back catalog heap
rows at ROLLBACK time` commit chose page mutation: `rollbackDDLCreate`
called `deleteCatalogRowsForOID` → `stampCatalogRows`, which set xmax on
matching live tuples and emitted a `RecordKindHeapDelete` WAL record.

Two problems with reviving that approach for PG-canonical rows:

1. The `stampCatalogRows` match closures only decoded the goopg-native
   layout via `DecodePGClassRow` / `DecodePGAttributeRow`, so PG-canonical
   rows never matched and stayed live on disk. Teaching the closures to try
   the physical decoder too is one-line fix.
2. But the WAL emission path produces a heap-delete record for every
   stamped row. With the M0106-0010 PG18 WAL framing
   (`PageHeaders=true`), the extra records straddle WAL page boundaries
   differently and the recovery path of an unrelated `btree-newroot` record
   reproducibly fails with `wal: btree-newroot trailing bytes (68 remaining)`
   on `TestRollbackedTableNotVisibleAfterRestart`'s second `Open()`. That
   framing bug exists independently of rollback and needs its own design
   doc / fix; the clog-filter approach sidesteps it entirely because it
   needs no extra writes.

The clog-filter fix also has lower blast radius: it only changes recovery's
read path, not the rollback hot path, and so it preserves the producer-side
DDL WAL stream unmodified.

## Verification

- `go test -count=1 -timeout 60s -run TestRollbackedTableNotVisibleAfterRestart ./internal/initdb/` — PASS (0.63s).
- `go test -count=1 -timeout 120s ./internal/initdb/` — same 16 pre-existing
  baseline failures as `master` (`TestMigrationFromLegacyJSONCluster`,
  `TestCrashMidTransactionTableNotVisibleAfterRestart`,
  `TestCreateTableSyncsToPGClass`, …); no new regressions.
  `TestRollbackedTableNotVisibleAfterRestart` flipped from FAIL → PASS.
- `go test -count=1 ./internal/executor/ ./internal/catalog/ ./internal/mvcc/` — all PASS.
- `./internal/wal/` baseline pre-existing failures unchanged
  (`TestCheckpointerWritesCheckpointMarkers`,
  `TestEncodeRecordXLogClassifiesXactCommitXID`).

## Follow-up

Two known problems live outside this slice:

1. **`btree-newroot` trailing-bytes WAL framing bug** — surfaces under
   `PageHeaders=true` when extra WAL records push the original record over
   a WAL page boundary. Needed for the original xmax-stamping approach to
   work; not needed for the clog-filter approach. Likely owned by M0106-0013
   (PG-canonical control + WAL framing parity) or a dedicated wal/format
   loop.
2. **`TestCrashMidTransactionTableNotVisibleAfterRestart`** — adjacent
   crash-recovery test still red on `master`; same family of "catalog heap
   row leaks past rollback" but for the implicit-abort path. Should be
   triaged in the next M0106-0011 slice.
