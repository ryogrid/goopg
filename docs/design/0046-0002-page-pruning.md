# Opportunistic Page Pruning — M0046-0002

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

With HOT updates (M0046-0001), new tuple versions stay on the same heap page. This
preserves space while the HOT chain is short, but once the page fills up with
accumulated dead versions, `tryApplyHOTUpdate` falls back to a cross-page
normal update. The page becomes permanently bloated until VACUUM runs.

PostgreSQL's opportunistic pruning (`heap_page_prune_opt`, upstream
`postgres/src/backend/access/heap/pruneheap.c`) addresses this by reclaiming
universally-dead tuple slots **inline**, without waiting for a dedicated VACUUM
pass. "Universally dead" means the deleting transaction committed before all
currently-active snapshots began: `xmax < OldestXmin`.

## 2. Design

### 2.1 pd_prune_xid tracking

The page header already has a `pd_prune_xid` field (`PageHeader.PruneXID()`,
bytes 20–23). It stores the maximum dead-tuple xmax seen on this page.

`PageSetHeapTupleXmax` and `PageStampHotOldTuple` now advance `pd_prune_xid`
whenever `xmax > current_prune_xid`. Opportunistic pruning checks this field
as a fast gate:

```go
if pruneXID == InvalidTransactionID || pruneXID >= oldestXmin {
    return result, nil // fast path: nothing prunable yet
}
```

### 2.2 PagePruneOpt (`internal/storage/prune.go`)

`PagePruneOpt(p Page, oldestXmin TransactionID) (PruneResult, error)` is the
new pruning entry point. For each `ItemIDNormal` tuple that satisfies the dead
predicate (`xmax != Invalid && !lockOnly && xmax < oldestXmin`):

| Infomask flags | Action |
|---|---|
| `HeapOnlyTuple` | `ItemIDUnused` — not indexed, slot is fully reusable |
| `HeapHotUpdated` (chain root) | `ItemIDRedirect` → live chain tip; tuple data freed |
| Neither | `ItemIDUnused` — standalone delete |

`PruneResult` carries `Redirects [][2]uint16` (old→new slot pairs) and
`Unused []uint16` (slots marked unused). Callers pass both to the WAL encoder.

After processing all dead slots:
- `VacuumHeapPageBySlots(p, result.Unused)` repacks the live tuples and updates `pd_upper`.
- `pd_prune_xid` is cleared (no more prunable dead tuples after this pass).

### 2.3 ItemIDRedirect support

`PageSetItemIDRedirect(p, slot, targetSlot)` converts a line pointer to
`ItemIDRedirect{Offset: targetSlot, Length: 0}`. The index entry still points
to the old slot; `followHOTChain` transparently follows it to the live version.

`PageGetItemID(p, slot)` (new exported helper) returns the raw `ItemID` so
chain-walking code can check flags before decoding tuple bytes.

`followHOTChain` in `internal/executor/operators_index.go` now handles
`ItemIDRedirect` inline:
```go
if item.Flags == storage.ItemIDRedirect {
    cur = item.Offset  // follow redirect
    continue
}
```

### 2.4 Pruning trigger (`internal/executor/operators_storage.go`)

Pruning fires in `tryApplyHOTUpdate` when `PageAddHeapTuple` returns
`ErrNoSpaceInPage`:

```go
if ctx.EnableOpportunisticPrune && ctx.TxnMgr != nil {
    oldestXmin := ctx.TxnMgr.OldestXmin()
    result, err := storage.PagePruneOpt(s.Page(), oldestXmin)
    if err == nil && len(result.Redirects)+len(result.Unused) > 0 {
        markHeapPruneOptDirty(...)  // WAL first
        newSlot, addErr = storage.PageAddHeapTuple(s.Page(), tup)  // retry
    }
}
```

The WAL record for the prune is emitted **before** the HOT-insert WAL record
so replay restores page space before attempting the insert.

### 2.5 WAL (`internal/wal/recovery.go`)

`RecordKindHeapPruneOpt = 14` with format:
```
kind(1) | rel(9) | blk(4) | nRedirects(2) | nUnused(2) |
redirects[nRedirects * 4] (source(2)+target(2)) |
unused[nUnused * 2]
```

Replay:
1. Apply redirect line pointer conversions (`PageSetItemIDRedirect`).
2. Call `VacuumHeapPageBySlots(page, unused)` to compact the page.
3. Clear `pd_prune_xid`; update `pd_lsn`.

### 2.6 GUC and Context

`enable_opportunistic_prune` (TypeBool, default on, ContextUserset) registered
in `internal/config/defaults.go`. Wired to `ctx.EnableOpportunisticPrune` in
`internal/server/dispatch.go` via `sessionOpportunisticPrune(sess)`.

## 3. Invariants

| Property | Guarantee |
|---|---|
| Universal-dead predicate | Only prunes `xmax < OldestXmin` — safe for all current and future snapshots |
| Redirect validity | Index entry (old slot) stays navigable via `ItemIDRedirect` |
| WAL ordering | Prune record precedes HOT-insert record in WAL stream |
| Idempotent replay | `pd_lsn` check skips already-applied records |
| GUC control | `enable_opportunistic_prune = off` disables the prune-and-retry path |

## 4. Tests

| Test | Location | Coverage |
|---|---|---|
| `TestPagePruneOptBasic` | `internal/storage/prune_test.go` | Standalone dead tuple → ItemIDUnused; pd_prune_xid cleared |
| `TestPagePruneOptFastPathSkips` | storage | pd_prune_xid=0 fast path |
| `TestPagePruneOptSkipsLiveTuples` | storage | `xmax >= oldestXmin` → not pruned |
| `TestPageSetHeapTupleXmaxUpdatesPruneXID` | storage | pd_prune_xid advances |
| `TestPagePruneOptSkipsLockOnly` | storage | Lock-only xmax → not pruned |
| `TestPagePruneOptHOTChainRedirect` | storage | HOT chain root → ItemIDRedirect to live tip |
| `TestOpportunisticPruneReclaims` | `internal/executor/prune_test.go` | DoD: full page pruned in-place, no page extension |
| `TestPageSetXmaxTracksPruneXID` | executor | pd_prune_xid bookkeeping end-to-end |

## 5. Deferred

- **Prune on every buffer pin** (not just HOT-update full-page path) — requires
  an `OldestXminFunc` hook in the pool and a per-page check on every pin.
- **Dead slots (`ItemIDDead`)** — upstream's `heap_prune_chain` additionally
  marks LP_DEAD entries before a VACUUM clears them; deferred to M0046-02 follow-up.
- **FSM integration** — `RecordPageWithFreeSpace` after pruning so future inserts
  can find the reclaimed space without scanning (M0046-0003).
