# 0079-0003 — Btree page-deletion + new-root logical WAL records

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-11 |
| scope | wal, access/btree |
| related | 0079-0001 (catalog DDL WAL recovery), 0079-0002 (vacuum
kept-items WAL), 0002-0002 (btree concurrency) |

## 1. Problem statement

M0079-0002 closed the highest-volume FPI fallback path on the btree
side (per-page kept-items rewrite during VACUUM) by adding
`RecordKindBtreeVacuum`. Three FPI fallback paths remain on the
audit table from 0079-0002 §2:

1. **Btree leaf unlink** (4 FPIs per page deletion: prev sibling
   Next, next sibling Prev, leaf flags after, parent downlink
   removal). Implemented by `unlinkEmptyLeaf` /
   `removeDownlinkFromParent` / `clearHalfDead` in
   `internal/access/btree/btree_vacuum.go`.
2. **New root** after a split-bubbled root creation
   (`updateRootMeta` after split; `resetToEmptyRoot` after
   full-vacuum-empty in `btree.go` / `btree_vacuum.go`).
3. **Mark-page-half-dead** — currently bundled into the
   `BtreeVacuum` record's `OpaqueFlags` trailer, but only when the
   leaf becomes empty as a side effect of vacuum filtering. A leaf
   that is already empty before a vacuum pass cannot be marked
   half-dead via that record.

This slice converts each of these into a logical WAL record
mirroring the PostgreSQL upstream:

| goopg record (new) | PostgreSQL counterpart |
| ------------------ | ---------------------- |
| `RecordKindBtreeUnlinkPage` (23) | `XLOG_BTREE_UNLINK_PAGE` |
| `RecordKindBtreeNewRoot` (24) | `XLOG_BTREE_NEWROOT` |
| `RecordKindBtreeMarkPageHalfDead` (25) | `XLOG_BTREE_MARK_PAGE_HALFDEAD` |

## 2. PostgreSQL reference shapes (src/include/access/nbtxlog.h)

**`xl_btree_unlink_page`** is a single atomic record covering 5
backup blocks: target page, left sibling, right sibling, leaf page
(if different from target — internal-page deletion case), and
metapage (if right sibling becomes the new fast root). The control
fields are `leftsib | rightsib | level | safexid` plus
`leafleftsib | leafrightsib | leaftopparent` for the
internal-target case. Replay reconstructs the half-dead leaf from
scratch using the control fields, applies sibling Next/Prev
patches via FPI on the sibling pages, and removes the parent
downlink also via FPI.

**`xl_btree_newroot`** is just `rootblk | level`. The new root
page's content is attached as "Backup Blk 0" (FPI). The metapage
update is implied by the record kind. PostgreSQL replays by
overwriting the root page from FPI and updating the metapage.

**`xl_btree_mark_page_halfdead`** is `poffset (parent slot of
deleted downlink) | leafblk | leftblk | rightblk | topparent`.
Replay reconstructs both the half-dead leaf and the top-parent
page from scratch using these fields plus the backup blocks.

## 3. goopg shape adaptation

goopg's WAL framework does not have backup-block FPIs as a
first-class concept (unlike PostgreSQL's XLogRegisterBuffer). All
goopg records are self-describing payloads. So our records must
either:

(a) Carry enough control data to reconstruct the post-mutation
    page from the on-disk pre-mutation page (preferred — small
    payload), or
(b) Carry the post-mutation page content inline (verbose but
    simpler).

For each record, we choose:

- **`UnlinkPage`** uses (a) for left/right siblings (just opaque
  Prev/Next pointer updates) and (a) for the leaf page (just
  `Flags` update). It uses (a) for the parent (carries the slot
  index of the downlink to remove). Replay reads each page,
  applies the small mutation, idempotency-checks via `pd_lsn`,
  writes back. Total: ~30-50 bytes per record vs 4 × 8 KiB FPI.

- **`NewRoot`** uses (b) for the root content (always 0 or 2
  items — small) and emits a control-only metapage update. Total:
  ~50-150 bytes vs 8 KiB FPI for the root + 8 KiB FPI for the
  metapage.

- **`MarkPageHalfDead`** uses (a): just `leafBlk | flagsAfter`.
  Replay reads the leaf page, sets `Flags = flagsAfter`, writes
  back. Total: 16 bytes vs 8 KiB FPI.

## 4. `RecordKindBtreeUnlinkPage` (24)

### Format

```
kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
leafBlk(4)        | leafFlagsAfter(2)         |
leftSibBlk(4)     | leftSibNext(4)            | leftSibValid(1) |
rightSibBlk(4)    | rightSibPrev(4)           | rightSibValid(1) |
parentBlk(4)      | parentRemoveSlot(2)       | parentValid(1)
```

`*Valid(1)` flags: 1 if the side has a sibling / parent to
update; 0 if the leaf was leftmost / rightmost / direct child of
the root with no parent in this subtree.

`parentRemoveSlot` is the 1-based slot index (in
`pageItems`-order) of the downlink whose `ptr.Block == leafBlk` —
captured at emit time so replay doesn't have to re-scan.

### Emit

`unlinkEmptyLeaf` collects all four mutations under
goopg's per-page write locks (current behaviour), then emits ONE
`RecordKindBtreeUnlinkPage`. Each page's MarkDirty is performed
inline — the WAL record is appended atomically before any of the
4 dirty buffers are flushed. The record's `EndLSN` becomes the
new `pd_lsn` for all 4 pages via
`MarkDirtyChangeRecord(slot, func() (LSN, error) { return ...})`
called once with the same LSN propagated to all 4 buffers.

### Replay

```
1. for each of (leftSib, rightSib, leaf, parent):
     read page; if pd_lsn >= record.EndLSN: skip this page
2. apply leftSib.opaque.Next = leftSibNext        (if leftSibValid)
3. apply rightSib.opaque.Prev = rightSibPrev      (if rightSibValid)
4. apply leaf.opaque.Flags = leafFlagsAfter
5. apply parent: pageItems → drop slot parentRemoveSlot →
   resetPageItems → re-add → maybe adjust leftmost-key (existing
   removeDownlinkFromParent semantics)                    (if parentValid)
6. set pd_lsn on all written pages
```

Idempotency: each of the four pages has its own `pd_lsn` check.
A crash between mutations is fine — re-applying the record only
re-applies the pages that haven't yet absorbed it.

## 5. `RecordKindBtreeNewRoot` (25)

### Format

```
kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
rootBlk(4) | level(4) | itemCount(2) |
[item0Len(2) | item0Bytes | ...]
```

### Emit

`updateRootMeta` (called by `insertIntoBlock` after a split bubbled
up to a new root) emits this record with the new root's content
(2 items: separator key from old root + one for the new right
sibling). `resetToEmptyRoot` (called by `VacuumIndexPages` when
the entire tree empties) emits with `itemCount = 0`.

### Replay

```
1. read root page; if pd_lsn >= record.EndLSN: skip
2. resetPageItems(rootPage)
3. set opaque.Level = level; opaque.Flags clear except BTRoot etc.
4. for each item: PageAddItemRaw(rootPage, item)
5. read metapage; if pd_lsn >= record.EndLSN: skip
6. set meta.Root = rootBlk, meta.Level = level
7. set pd_lsn on both
```

## 6. `RecordKindBtreeMarkPageHalfDead` (26)

### Format

```
kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
leafBlk(4) | flagsAfter(2)
```

### Emit

`VacuumIndexPages`'s pre-existing flag-set `op.Flags |= BTDeleted
| BTHalfDead` path is now emitted as this record when no items
are removed (page already empty pre-vacuum). When items ARE
removed, the same flags are carried in `BtreeVacuum`'s
`OpaqueFlags` trailer (existing M0079-0002 behaviour) — they are
not duplicated.

### Replay

```
1. read leaf page; if pd_lsn >= record.EndLSN: skip
2. opaque.Flags = flagsAfter (overwrite)
3. set pd_lsn
```

## 7. Hook wiring

Three new hooks in `storage.PoolConfig` mirroring the M0079-0002
pattern:

```go
LogBtreeUnlinkPage     LogBtreeUnlinkPageFunc
LogBtreeNewRoot        LogBtreeNewRootFunc
LogBtreeMarkPageHalfDead LogBtreeMarkPageHalfDeadFunc
```

`internal/initdb/open.go::Open` wires each to
`walWriter.Append(wal.Encode...)`. The btree producer paths
(`unlinkEmptyLeaf`, `updateRootMeta`, `resetToEmptyRoot`,
`VacuumIndexPages` flag-only path) check `Pool.Log...()` for
nil and fall back to the existing `markDirtyWithPageRecord` FPI
path when unset.

## 8. Tests

Per record:
- Encode/decode round-trip with edge cases (no left sibling, no
  right sibling, internal-page parent missing, root-empty case for
  NewRoot, max-page item list).
- Truncated-payload + wrong-kind guards.
- ApplyRecord routing.
- Producer-side: the existing `btree_vacuum_test.go` suite must
  continue to pass; new test files
  `btree_unlink_wal_test.go` / `btree_newroot_wal_test.go` /
  `btree_markhalfdead_wal_test.go` capture the WAL hook payloads
  and assert the projected post-state matches the pre-WAL FPI
  path's outcome.

## 9. Migration / backwards compatibility

Same posture as M0079-0002:

- Old clusters whose WAL contains the new record kinds 24/25/26 do
  not exist (this slice introduces them). Forward-compatibility is
  trivial.
- Old code reading new clusters: `ApplyRecord` returns
  `unsupported kind N` for the new bytes — the operator sees a
  hard recovery failure rather than silent data loss. Acceptable
  upgrade discipline.
- Pools without the hooks wired (test harnesses) keep using the
  FPI fallback; semantics unchanged, WAL larger.

## 10. Acceptance

- `go test ./internal/wal/... ./internal/access/btree/...
  ./internal/initdb/...` all green, including new
  encode/decode/replay tests.
- The existing `TestVacuumIndexPagesEmptiesTree` /
  `TestVacuumUnlinkLeafChain` / `TestVacuumResetToEmptyRoot` (or
  equivalents) pass — semantically the operations are unchanged,
  only the WAL representation changes.
- Spot-check WAL volume on a synthetic VACUUM-with-page-deletion
  workload: the unlink phase shrinks from ~32 KiB (4 × FPI) to
  ~50 bytes per deleted page; new-root from ~16 KiB (root + meta
  FPIs) to ~150 bytes typical; mark-half-dead from 8 KiB to 16
  bytes.

## 11. Out of scope

- `RecordKindBtreeReusePage` (PostgreSQL `XLOG_BTREE_REUSE_PAGE`):
  emitted when a recycled page is allocated to a new use; signals
  to standbys that the old contents are formally discarded.
  Depends on FSM integration that is not yet in goopg. Carry to
  M0080.
- `RecordKindBtreeMetaCleanup` (`XLOG_BTREE_META_CLEANUP`):
  metapage cleanup-XID update. Not surfaced by current goopg;
  carry to M0080+.
- `RecordKindBtreeDelete` (`XLOG_BTREE_DELETE`): bulk delete via
  `btvacuumcleanup`'s kill-prior-tuple path. goopg's vacuum uses
  the same kept-items rewrite for both bulk-delete and full-page
  vacuum, so the existing `BtreeVacuum` record covers this. No new
  record needed for goopg.
- `RecordKindBtreeDedup` (`XLOG_BTREE_DEDUP`): posting-list
  deduplication. goopg's posting-list compaction uses plain
  `MarkDirty` (FPI fallback). Carry to M0080.
- The `safexid` field PostgreSQL carries on `xl_btree_unlink_page`
  for visibility horizons in a hot-standby setting. goopg has no
  hot-standby read path that would consume it; deferred until
  M0008-pgoutput consumers need it.
