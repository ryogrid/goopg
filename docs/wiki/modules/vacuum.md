# Module: `internal/commands/vacuum`

The **VACUUM / ANALYZE** command engine — a Go port of PostgreSQL's
`src/backend/commands/vacuum.c` plus the storage-layer dead-tuple reclamation
(`PageVacuumPrune`), tuple freezing, visibility-map updates, and tail
truncation. It is invoked by the executor's `VACUUM`/`ANALYZE` utility
operators.

The package is deliberately thin at the command level: the heavy lifting lives
in `internal/storage` (`PageVacuumPrune`, `PageFreezeOldTuples`, VM/FSM fork
updates), and this package drives the per-block loop, the dead-TID collection
for index vacuum, the freeze pass, and the truncation decision.

## Key Files

- `vacuum.go` (398) — everything: `VacuumWithOptions`, `Vacuum`,
  `VacuumWithFSM`, `VacuumWithFSMAndVM`, `vacuumCore`, `Analyze`, and the
  `Stats`/`VacuumOptions`/`AnalyzeStats` structs.

## Public API

```go
type VacuumOptions struct {
    Aggressive bool; FreezeBelow TransactionID; FailsafeAge int
    CostDelayMS int; SkipLocked bool; Full bool
    ...
}
type Stats struct {
    Pages, Dead, Live int; DeadTIDs []ItemPointer
    SkippedAllVisible, SkippedAllFrozen int
    ...
}
func Vacuum(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, ...) (Stats, error)
func VacuumWithOptions(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, opts VacuumOptions, ...) (Stats, error)
func VacuumWithFSM(...) / VacuumWithFSMAndVM(...) (Stats, error)
func Analyze(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, mxs *multixact.Store) (AnalyzeStats, error)
```

## Internal structure

- **`vacuumCore`** — iterates heap blocks (`blk` 0..`nBlocks`):
  1. **VM skip** — a non-aggressive pass skips all-visible blocks (except the
     last, which is always scanned for truncation decisions); all-frozen skips
     are counted separately so they don't stall relfrozenxid advancement.
  2. **Dead-tuple reclamation** — `storage.PageVacuumPrune` repacks the page:
     HOT-chain-aware (dead chain roots become `ItemIDRedirect`), multixact-aware
     (an updater-bearing multi xmax resolves its updater before the horizon
     compare). Removed line pointers become LP_UNUSED.
  3. **Tuple freezing** — `PageFreezeOldTuples` rewrites old xmin →
     `FrozenTransactionID` for tuples older than `FreezeBelow`.
  4. **Dead-TID collection** — `pr.Unused` offsets become `DeadTIDs` for index
     vacuum (the btree/index access method purges matching entries).
  5. **Dirty-page stamping** — a `logPrune`/`logFrz` WAL hook or
     `MarkDirty`/`MarkDirtyChangeRecord` records the reclamation.
- **`Analyze`** — samples pages, computes per-column statistics (ndistinct,
  MCV/histograms via `internal/optimizer`'s stats estimators), and stores them
  in the catalog (`TableStats`/`ColumnStats`) for the planner's cardinality
  estimates.
- **Truncation** — the `lastNonEmpty` block is tracked; a tail of empty blocks
  may be truncated (`TruncateRelation`), advancing `relfrozenxid`/`relminmxid`.

## Dependencies

- **Used by** — `internal/executor` (VACUUM/ANALYZE utility operators),
  `internal/postmaster/autovacuum` (autovacuum launcher).
- **Uses** — `internal/storage` (page prune/freeze/VM/FSM), `internal/access/transam`
  (horizon, relfrozenxid), `internal/access/nbtree` (index vacuum),
  `internal/catalog` (stats storage), `internal/access/transam/xlog`
  (WAL logging of prune/freeze).

## Notable patterns / gotchas

- **VM skip ≠ "no live tuples"** — an all-visible block still holds live rows;
  `lastNonEmpty` must be tracked through the skipped blocks or tail truncation
  drops live all-visible blocks (data loss). The last block is always scanned.
- **HOT-chain pruning** — never remove a chain root while descendants exist;
  redirect it (`ItemIDRedirect`) so the index entry keeps resolving to the live
  tip.
- **`SkippedAllFrozen` vs `SkippedAllVisible`** — frozen-only skips count
  separately so `relfrozenxid` can advance; visible-but-not-frozen skips
  count against a non-aggressive pass's freeze progress.
- **Failsafe** — `FailsafeAge` (XID-age based) forces an aggressive pass and
  disables cost delays when the cluster approaches wrap-around.
- **Empty-page special case** — uninitialized pages (`storage.IsNew`) are
  skipped without being counted as scanned (an extension may have allocated
  the block without writing tuples).