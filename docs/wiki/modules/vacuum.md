# Module: `internal/commands/vacuum`

The **VACUUM / ANALYZE** command engine — a Go port of PostgreSQL's
`src/backend/commands/vacuum.c` plus the storage-layer dead-tuple reclamation
(`PageVacuumPrune`), tuple freezing, visibility-map updates, and tail
truncation. It is invoked by the executor's `VACUUM`/`ANALYZE` utility
operators and by the autovacuum launcher.

The package is deliberately thin at the command level: the heavy lifting lives
in `internal/storage` (`PageVacuumPrune`, `PageFreezeOldTuples`, VM/FSM fork
updates), and this package drives the per-block loop, the dead-TID collection
for index vacuum, the freeze pass, and the truncation decision. Scope and growth
path are documented in `docs/design/0016-vacuum-and-analyze.md`: v0 reclaims
dead heap tuples at page granularity, returns full-scan ANALYZE statistics, and
provides a REINDEX bridge for B-tree cleanup until per-entry index removal
lands.

## Key Files

| File | LOC | Role |
|---|---|---|
| `vacuum.go` | 398 | Everything: `VacuumWithOptions`, `Vacuum`, `VacuumWithFSM`, `VacuumWithFSMAndVM`, `vacuumCore`, `Analyze`, and the `Stats`/`VacuumOptions`/`AnalyzeStats` structs. |
| `vacuum_test.go` | 468 | Unit suite over a synthetic heap: dead-tuple reclamation, freeze, VM/FSM updates, truncation, cost delays, horizon handling. |
| `lpdead_hook_test.go` | 17 | `TestMain` — installs the permissive `storage.XidCommitted` test hook (synthetic tuples carry literal xids with no clog). |

## Public API

```go
type VacuumOptions struct {
    Aggressive bool; FreezeBelow TransactionID; FailsafeAge int
    CostDelayMS int; CostLimit int; CostPageHit/Miss/Dirty int
    SkipLocked bool; Truncate bool
    FSM *storage.FSM; VM *storage.VisibilityMap
    Horizon TransactionID
    ...
}
type Stats struct {
    Pages, Live, Dead, Frozen int
    DeadTIDs []ItemPointer
    SkippedAllVisible, SkippedAllFrozen int
    OldestXmin, NewFrozenXID TransactionID
    ...
}
type AnalyzeStats struct{ Pages, Rows int; AvgWidth float64 }

func Vacuum(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, ...) (Stats, error)
func VacuumWithOptions(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, opts VacuumOptions, ...) (Stats, error)
func VacuumWithFSM(...) / VacuumWithFSMAndVM(...) (Stats, error)
func Analyze(pool *storage.Pool, mgr *transam.Manager, rel storage.RelFileNode, mxs *multixact.Store) (AnalyzeStats, error)
```

### `Stats` semantics

- `Pages` — blocks visited (skipped blocks count via `SkippedAllVisible`/
  `SkippedAllFrozen`, not `Pages`).
- `Live` / `Dead` — tuples that survived / were reclaimed this pass (`Dead`
  counts both `Redirects` and `Unused` slots).
- `Frozen` — tuples whose xmin was rewritten to `FrozenTransactionID`.
- `DeadTIDs` — the (block, offset) pointers of fully-removed `Unused` slots,
  the input to index vacuum (`M0047-0002`).
- `OldestXmin` — the horizon actually used (resolved, possibly the caller's
  `opts.Horizon`).
- `NewFrozenXID` — the lowest unfrozen xmin after this pass (0 = all frozen);
  the caller's input to `pg_class.relfrozenxid`.

### `VacuumOptions` semantics

- `FSM` / `VM` — optional forks updated per page (free-space reuse, index-only
  scan visibility).
- `CostDelayMS` + `CostLimit`/`CostPageHit`/`CostPageMiss`/`CostPageDirty` —
  upstream's `vacuum_cost_*` pacing; zero delay disables pacing entirely (PG's
  default for manual VACUUM).
- `Truncate` — drop trailing all-empty blocks (`vacuum_truncate` GUC /
  reloption / statement param).
- `FailsafeAge` — when the horizon's XID age reaches it, force aggressive +
  disable cost delays (upstream `vacuum_failsafe_age`).
- `Aggressive` — force a full scan ignoring VM skips (upstream
  `DISABLE_PAGE_SKIPPING`; set by anti-wraparound autovacuum and
  `VACUUM (FREEZE)`).
- `FreezeBelow` — tuple-freeze cutoff (any `xmin < FreezeBelow` rewritten to
  `FrozenTransactionID`).
- `Horizon` — override the reclamation cutoff (used by the VACUUM operator for
  TEMPORARY relations).

## Internal structure

```mermaid
flowchart TD
    subgraph vacuumCore
        H[Resolve horizon<br/>opts.Horizon ?? mgr.OldestXmin]
        FA{Failsafe?<br/>nextXID-horizon >= FailsafeAge}
        FA -- yes --> AG[Aggressive=true,<br/>CostDelay=0]
        LOOP[for blk 0..nBlocks]
        VM{VM skip?<br/>!aggressive && AllVisible<br/>&& !isLastBlock}
        VM -- skip --> LNE[lastNonEmpty = blk<br/>+ SkippedAll{Frozen,Visible}++]
        VM -- scan --> PIN[Pin + Lock slot]
        PIN --> PRUNE[PageVacuumPrune<br/>HOT/multixact-aware]
        PRUNE --> DIRTY{reclaimed > 0?}
        DIRTY -- yes --> LOG[logPrune hook or MarkDirty]<br/>+ DeadTIDs collect<br/>+ lastNonEmpty = blk
        PRUNE --> FREEZE{FreezeBelow > 0?}
        FREEZE -- yes --> FRZ[PageFreezeOldTuples<br/>+ logFreeze hook<br/>+ NewFrozenXID tracking]
        FREEZE --> FSM[FSM.RecordFreeSpaceForPage]
        FSM --> VMU[VM: SetAllFrozen /<br/>SetAllVisible / ClearBlock]
        VMU --> COST[Cost-based throttle]
        COST --> UNPIN[Unlock + Unpin<br/>Pages++]
        LNE --> LOOP
        UNPIN --> LOOP
        LOOP --> TRUNC{Truncate?}
        TRUNC -- yes --> TT[pool.TruncateRelationTail<br/>keep = lastNonEmpty+1]
    end
```

### `vacuumCore` — the per-block loop

`vacuumCore` iterates heap blocks (`blk` 0..`nBlocks`):

1. **Horizon resolution** — the reclamation cutoff is `opts.Horizon` if set
   (the VACUUM operator passes the session-local
   `mgr.OldestXminForProc()` for TEMPORARY relations so a concurrent session's
   older snapshot does not pin temp rows), else `mgr.OldestXmin()`.
2. **Failsafe escalation** — when `opts.FailsafeAge > 0` and
   `nextXID - horizon >= FailsafeAge`, the pass becomes aggressive and cost
   delays drop to zero, so the pass finishes fast and advances relfrozenxid.
3. **VM skip** — a non-aggressive pass skips all-visible blocks (except the
   last, which is always scanned for truncation decisions); all-frozen skips
   are counted separately (`SkippedAllFrozen`) so they don't stall relfrozenxid
   advancement. A skipped block still advances `lastNonEmpty` — it is
   all-visible, which for a heap page means it holds live tuples.
4. **Dead-tuple reclamation** — `storage.PageVacuumPrune` repacks the page:
   HOT-chain-aware (dead chain roots become `ItemIDRedirect`), multixact-aware
   (an updater-bearing multi xmax resolves its updater before the horizon
   compare). Removed line pointers become `LP_UNUSED`. The old naive
   "xmax < horizon → remove slot" pass broke HOT chains and treated a raw
   MultiXactId as an xid (freeze-the-dead spec, M0118-0009).
5. **WAL stamping** — when the pool has a `LogHeapPruneOpt` hook wired (the
   normal runtime case), each pruned page emits a logical prune redo record via
   `MarkDirtyChangeRecord`; replay reproduces the same redirects bit-for-bit.
   Without the hook (test pools), `MarkDirty` falls back to the
   FPI-on-every-dirty path.
6. **Dead-TID collection** — only the fully-removed (`Unused`) line pointers may
   carry an index entry that must be cleared; redirected roots keep their index
   entry valid. HOT-only `Unused` tuples have no index entry, so removing a
   (nonexistent) entry for their TID is a harmless no-op.
7. **Tuple freezing** — `PageFreezeOldTuples` rewrites old xmin →
   `FrozenTransactionID` for tuples older than `FreezeBelow`. A `logFrz` WAL
   hook (`LogHeapFreeze`) records the frozen slots; `MarkDirty` is the fallback.
   The minimum unfrozen xmin across all pages is tracked into
   `NewFrozenXID` for relfrozenxid advancement.
8. **FSM / VM updates** — free-space is recorded per page
   (`FSM.RecordFreeSpaceForPage`); VM bits are set: `ALL_FROZEN` when the freeze
   pass ran and every live tuple sits at-or-below the cutoff, `ALL_VISIBLE` when
   every remaining tuple is visible to the horizon, otherwise `ClearBlock`.
9. **Cost-based throttling** — the vacuum-cost family is modelled: per-page cost
   accumulates (first touch = miss, later touches = hit, dirty pages add dirty
   cost); when over `CostLimit`, the pass sleeps a proportional slice capped at
   `4 × CostDelayMS`. `costSeen` tracks which blocks were already touched this
   pass.
10. **Tail truncation** — when `opts.Truncate`, `keep = lastNonEmpty + 1` is
    passed to `pool.TruncateRelationTail` (WAL-first emission + invalidation +
    smgr shrink encapsulated in the pool helper). If `keep < nBlocks`, the tail
    is dropped; a missing hook (test harnesses) makes truncation a no-op rather
    than an unsafe shrink.

### `storage.PageVacuumPrune` contract (the reclamation engine)

`PageVacuumPrune(page, horizon)` returns `(pr, liveOnPage, err)` where `pr`
carries `Redirects` and `Unused` slot lists:

- Dead HOT-chain roots become `ItemIDRedirect` (so the index entry keeps
  resolving to the live tip), never removed.
- A multixact xmax that bears an updater resolves to its updater before the
  horizon compare — a live, only-row-locked tuple is not reclaimed.
- `liveOnPage` is the surviving-tuple count used for `Stats.Live` and the
  `lastNonEmpty` heuristic (a page with `cnt > 0 || liveOnPage > 0` is
  non-empty).

### `Analyze`

Walks every block of `rel` with a fresh read-committed snapshot from `mgr`,
counts "currently live" tuples (matching upstream's reltuples definition) and
accumulates average tuple width including the header. A `multixact.Store`
(`mxs`) is threaded from the caller (the autovacuum Launcher's process-shared
store; nil disables the multi path) so an updater-bearing multi xmax resolves to
its updater before the visibility judgement — a live, only-row-locked tuple is
not undercounted as invisible. LP_UNUSED/DEAD/REDIRECT slots are skipped via
`errors.Is(err, storage.ErrUnsupportedItem)`.

### Freeze rules

- `FreezeBelow` is the cutoff: any tuple with `xmin < FreezeBelow` is rewritten
  to `FrozenTransactionID` (2) so XID wraparound cannot make it invisible.
- `PageAllFrozen(page, cutoff)` gates the `ALL_FROZEN` VM bit; `PageAllVisible`
  gates `ALL_VISIBLE` (every remaining tuple visible to the horizon).
- `NewFrozenXID` (0 = all frozen) is the caller's input to the `pg_class`
  `relfrozenxid` update; the `SkippedAllFrozen` counter lets that advance even
  when whole blocks were skipped.

## Dependencies

- **Used by** — `internal/executor` (VACUUM/ANALYZE utility operators),
  `internal/postmaster/autovacuum` (autovacuum launcher).
- **Uses** — `internal/storage` (page prune/freeze/VM/FSM, `TruncateRelationTail`,
  `IsNew`, `PageLinePointerCount`, `PageGetHeapTuple`, `ErrUnsupportedItem`,
  `BufferTag`, `Pin`/`Unpin`/`Lock`, `MarkDirty`, `MarkDirtyChangeRecord`,
  `LogHeapPruneOpt`, `LogHeapFreeze`, `PageAllVisible`, `PageAllFrozen`,
  `FSM.RecordFreeSpaceForPage`, `VisibilityMap.AllVisible/AllFrozen/SetAllFrozen/
  SetAllVisible/ClearBlock`), `internal/access/transam` (horizon,
  `OldestXmin`, `OldestXminForProc`, `Begin`, `SnapshotFor`,
  `TupleVisible`, `IsolationReadCommitted`, `InvalidCommandId`, `InvalidTransactionID`),
  `internal/access/transam/multixact` (the updater-resolution `Store`),
  `internal/access/transam/xlog` (WAL logging of prune/freeze via pool hooks).

## Notable patterns / gotchas

- **VM skip ≠ "no live tuples"** — an all-visible block still holds live rows;
  `lastNonEmpty` must be tracked through the skipped blocks or tail truncation
  drops live all-visible blocks (data loss). The last block is always scanned.
- **HOT-chain pruning** — never remove a chain root while descendants exist;
  redirect it (`ItemIDRedirect`) so the index entry keeps resolving to the live
  tip. The old naive "xmax < horizon → remove slot" pass broke HOT chains and
  treated a raw MultiXactId as an xid (freeze-the-dead spec, M0118-0009).
- **`SkippedAllFrozen` vs `SkippedAllVisible`** — frozen-only skips count
  separately so `relfrozenxid` can advance; visible-but-not-frozen skips count
  against a non-aggressive pass's freeze progress (vacuumlazy.c `skippedallvis`
  guard). Callers must NOT advance relfrozenxid when `SkippedAllVisible > 0` on
  a non-aggressive pass.
- **Failsafe** — `FailsafeAge` (XID-age based) forces an aggressive pass and
  disables cost delays when the cluster approaches wrap-around.
- **Empty-page special case** — uninitialized pages (`storage.IsNew`) are
  skipped without being counted as scanned (an extension may have allocated
  the block without writing tuples).
- **Temp-relation horizon** — a plain `mgr.OldestXmin()` pins reclamation of
  temp-table rows that a concurrent session's older snapshot cannot see; the
  VACUUM operator passes the session-local horizon via `opts.Horizon` so temp
  tables are actually reclaimable.
- **Test hook dependency** — the vacuum unit suite constructs synthetic tuples
  with literal xids and no clog; since C3-S3, `storage.TupleDeadToAll` requires
  the `XidCommitted` hook (production wires `CLog.DidCommit`), so `TestMain`
  installs a permissive stub (`lpdead_hook_test.go`).
- **WAL hooks** — the logical prune/freeze redo records are only emitted when
  the pool's hooks (`LogHeapPruneOpt`, `LogHeapFreeze`) are wired; test pools
  without WAL degrade to `MarkDirty` (FPI). This keeps the page change durable
  without a redo log.
- **VACUUM does not touch indexes** — v0 relies on a REINDEX bridge for B-tree
  cleanup until per-entry index removal (page deletion) lands; only `DeadTIDs`
  collection prepares the index-vacuum path (M0047-0002).
- **Cost-pacing first touch** — a page's first touch in the pass counts as a
  `CostPageMiss` (it had to come from disk); later touches as `CostPageHit`; a
  page that was dirtied adds `CostPageDirty`. The sleep is
  `CostDelayMS * costBalance / CostLimit`, capped at `4 × CostDelayMS`.