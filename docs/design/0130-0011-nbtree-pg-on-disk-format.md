# 0130-0011 — nbtree PG-identical on-disk format

**Milestone:** M0130 (Cluster-directory compat with PG 18.3 + PG physical replication)
**Status:** draft (S11.1 landed 2026-08-10; S11.2–S11.6 not started)
**Predecessor:** `0130-0010-pg183-standby-e2e-harness.md` — this doc exists because
that harness's blocker #12 is milestone-sized and does not belong in an addendum.

## Problem

`TestE2E_PGStandbyFullCycle` phase D attaches a real PostgreSQL 18.3 to a goopg
cluster directory. The heap side works: the standby replays goopg's WAL and the
rows are present. The **index** side does not, and the two remaining blockers of
that harness are one dependency chain:

- **Blocker #10** — `pg_class.relhasindex` is hardcoded `false` in
  `buildUserPGClassRow` (`internal/executor/pg18_user_catalog_rows.go`). PG's
  `ExecInitModifyTable` gates `ExecOpenIndices` on it and `plancat.c`'s
  `get_relation_info` gates `RelationGetIndexList` on it, so PG treats every
  goopg relation as index-less and never index-scans.
- **Blocker #12 — this doc.** #10 cannot simply be flipped. goopg's user B-tree
  files are a **goopg-private page format**, and the very first thing PG does
  with an index page is `_bt_checkpage`
  (`postgres/src/backend/access/nbtree/nbtpage.c`):

  ```c
  if (PageGetSpecialSize(page) != MAXALIGN(sizeof(BTPageOpaqueData)))
      ereport(ERROR, (errcode(ERRCODE_INDEX_CORRUPTED),
                      errmsg("index \"%s\" contains corrupted page at block %u", ...)));
  ```

  goopg reserves **272** bytes of special area; upstream requires exactly **16**.
  Measured: setting `relhasindex = true` today makes the E2E fail *earlier*, in
  Phase B, with `index "s10_t_val_idx" contains corrupted page at block 0`
  (XX002). So #12 gates #10, and #10 gates the harness.

## Format gap inventory

| # | Area | goopg today (`internal/access/btree`) | Upstream PG 18.3 |
|---|------|----------------------------------------|------------------|
| 1 | Special area size | `SizeOfBTPageOpaque = 272` | `MAXALIGN(sizeof(BTPageOpaqueData)) = 16` |
| 2 | Opaque fields | `Prev, Next, Level, Flags, HighKeyLen, HighKey[256]` | `btpo_prev, btpo_next, btpo_level, btpo_flags, btpo_cycleid` |
| 3 | High key | stored **inside** the opaque | a normal index tuple at `P_HIKEY` (offset 1), `BTREE_VERSION 4` pivot tuple |
| 4 | Sibling sentinel | `storage.InvalidBlockNumber` (0xFFFFFFFF) | `P_NONE` = `0` (block 0 is the metapage, so 0 is free) |
| 5 | Flag bits | `BTHasHighKey = 0x0008` collides with `BTP_META`; `BTIncompleteSplit`/`BTHalfDead` are swapped vs `BTP_HALF_DEAD`/`BTP_SPLIT_END` | `BTP_LEAF/ROOT/DELETED/META/HALF_DEAD/SPLIT_END/HAS_GARBAGE/INCOMPLETE_SPLIT/HAS_FULLXID` |
| 6 | Metapage | goopg `BTreeMeta` (6 fields, 24 bytes) | `BTMetaPageData`, 48 bytes, `pd_lower` advanced past it |
| 7 | Item body | goopg `item` encoding (prefix + comparison-key bytes) | `IndexTupleData` (8-byte header: `t_tid` 6 + `t_info` 2) + null bitmap + PG binary datums |
| 8 | Downlinks | goopg internal-page encoding | child `BlockNumber` in the pivot tuple's `t_tid` |
| 9 | WAL | goopg-private btree records | `nbtxlog.c` `XLOG_BTREE_*` under `RM_BTREE_ID` |

Note items 1, 2 and 6 are *page-shape* gaps and 3, 7, 8 are *tuple-shape* gaps;
9 is only needed for a PG standby to **replay** goopg index changes, not to
read a basebackup snapshot. That split is what the slicing below follows.

Two things already agree and must not be "fixed": `BTREE_MAGIC` (`0x053162`)
and `BTREE_VERSION` (`4`) — goopg copied both correctly early on. The page
*header* (`PageHeaderData`, 24 bytes) and the line-pointer array already match
upstream too, because goopg's B-tree stores items through
`storage.PageInsertItemRawAt`.

## Layout verification

Sizes and offsets are not taken from reading the headers. Two independent
sources agree:

1. A `sizeof`/`offsetof` probe compiled against the oracle headers
   (`gcc -Ipostgres/src/include`):

   ```
   sizeof(BTPageOpaqueData)=16  prev=0 next=4 level=8 flags=12 cycleid=14
   sizeof(BTMetaPageData)=48    magic=0 version=4 root=8 level=12 fastroot=16
                                fastlevel=20 delpages=24 heaptuples=32
                                allequalimage=40
   sizeof(IndexTupleData)=8     BTREE_VERSION=4  BTREE_MAGIC=0x53162
   ```

2. A metapage a real PostgreSQL 18.3 actually wrote — block 0 of an empty
   catalog index in the TPC-H reference cluster
   (`bench/tpch/runtime/pgdata/base/1`). Its bytes are the golden in
   `TestInitPGMetaPageMatchesRealPG`:

   ```
   pd_lower/upper/special = 72 / 8176 / 8176
   BTMetaPageData         = 62310500 04000000 00000000 00000000 00000000
                            00000000 00000000 00000000 000000000000f0bf 01 <7 pad>
   opaque                 = 00000000 00000000 00000000 0800 0000
   ```

   The golden pins three things a field-by-field encoder gets wrong silently
   because every named field still round-trips: the 4-byte **alignment hole**
   at struct offset 28 (before the `float8`), the **`-1.0` sentinel** in
   `btm_last_cleanup_num_heap_tuples` (not `0.0` — `_bt_vacuum_needs_cleanup`
   reads `0` as a real tuple count), and the **7 bytes of tail padding** after
   `btm_allequalimage`.

## Slice plan

- **S11.1 — PG format layer (LANDED 2026-08-10).** `internal/access/btree/pgformat.go`:
  `SizeOfBTPageOpaquePG`/`SizeOfBTMetaPageDataPG`, the upstream `BTP_*` flag set,
  `P_NONE`, `PGBTPageOpaque` + `Read/WritePGOpaque`, `PGBTMetaPage` +
  `Read/WritePGMetaPage`, `InitPGBTPage` (`_bt_pageinit`), `InitPGMetaPage`
  (`_bt_initmetapage`), and `CheckPGBTPage` — a Go transcription of
  `_bt_checkpage`, so goopg can gate its own writers on the oracle's acceptance
  criterion instead of learning about a mismatch from a live standby's XX002.
  Deliberately additive: the legacy layout in `btree.go` is untouched, so the
  two coexist while the writers migrate. Guards in `pgformat_test.go`, both
  padding clears mutation-verified.
- **S11.2 — page shape.** Switch `readOpaque`/`writeOpaque`/`ParseOpaque` to the
  16-byte form and move the high key out of the opaque into a `P_HIKEY` item at
  offset 1. Translate the sibling sentinel (`InvalidBlockNumber` → `P_NONE`) and
  the flag bits at the same time — they are the same edit surface, and splitting
  them leaves an intermediate format that is neither goopg's nor PG's.
  Writers: `btree.go` (split/insert/newroot), `bulkload.go`, `btree_vacuum.go`.
  Readers that must move in lockstep (sibling-path rule): `internal/amcheck`
  (`ParseOpaque` consumer) and `replay.go`.

  Split into two commits, because the *primitives* the flip needs are additive
  and testable on their own while the flip itself is all-or-nothing:

  - **S11.2a — page-shape primitives (LANDED 2026-08-10).**
    `internal/storage/linepointer.go`: `PageReserveLinePointer` and
    `PageDeleteLinePointerAt`, the two line-pointer-array operations upstream
    needs but goopg had no way to express (`PageAddItemRaw` always allocates
    payload; `PageRemoveHeapTuple` blanks a slot in place instead of sliding
    the array down, which would leave a hole where `P_FIRSTDATAKEY` is
    expected). `internal/access/btree/pgpage.go`: `P_HIKEY`/`P_FIRSTKEY`,
    `PGFirstDataKey` (`P_FIRSTDATAKEY`), the data-slot accessor wrappers, the
    high-key item accessors, `pgReserveHiKeySlot`/`pgSlideLeft`, and the
    sentinel/flag translators. Guards in `pgpage_test.go` +
    `linepointer_test.go`; the `P_FIRSTDATAKEY` bias is mutation-verified.
  - **S11.2b — the flip.** Point the writers and readers at those primitives in
    one commit.

  Three mechanisms decided here, so S11.2b is a mechanical edit rather than a
  redesign:

  1. **Data slots, not physical slots.** Moving the high key onto the page means
     a line-pointer offset is no longer a data-item index — on a non-rightmost
     page every data item shifts right by one. Instead of respelling that at
     each of the ~45 `storage.PageXxx(p, slot)` call sites in the package (and
     getting one wrong), S11.2b swaps them for the `pgXxx(p, slot)` wrappers,
     which take a 1-based *data* slot and apply the `P_FIRSTDATAKEY` bias
     themselves. Binary search, split, dedup and vacuum keep counting from 1 and
     do not change. The wrappers read the bias from the page's own opaque rather
     than accepting a caller-supplied one: a threaded opaque eventually goes
     stale, and a stale rightmost bit shifts every subsequent read by one slot.
  2. **High-key presence is derived, never flagged.** Upstream has no
     "has high key" bit — `P_FIRSTDATAKEY` keys off `P_RIGHTMOST`, and 0x0008
     is `BTP_META`. So `BTHasHighKey` is *deleted* in the flip, not renumbered,
     and `HasHighKey()` becomes `!IsRightmost()`. This is why the sibling-link
     update and the high-key write must happen in the same critical section:
     between them the page's own accessors disagree about where its data starts.
     `pgPromoteToNonRightmost` (rightmost page acquiring a right sibling at
     split) and `pgSetHighKeyRaw` (replacing an existing separator) are separate
     entry points precisely so that transition is explicit at each call site.
  3. **Bulk load reserves `P_HIKEY` up front.** `_bt_buildadd` does not know
     whether a page will end up rightmost until the level is finished, so
     `_bt_blnewpage` bumps `pd_lower` by one `ItemIdData` to "make the P_HIKEY
     line pointer appear allocated", data items start at `P_FIRSTKEY`, and a
     page that turns out rightmost gets the placeholder removed by
     `_bt_slideleft` (`postgres/src/backend/access/nbtree/nbtsort.c`).
     `pgReserveHiKeySlot`/`pgSlideLeft` are the Go equivalents; `bulkload.go`'s
     current "set the high key at flush time" shape maps onto them directly.
- **S11.3 — metapage.** `BTreeMeta` → `PGBTMetaPage` at block 0 via S11.1's
  codec, including the `pd_lower` advance.
- **S11.4 — tuple shape.** goopg `item` → `IndexTupleData` + null bitmap + PG
  binary datums; downlinks into `t_tid`. This is the largest slice and the one
  that couples the index format to the type-codec layer.
- **S11.5 — `RM_BTREE` WAL.** PG-faithful `XLOG_BTREE_*` emission/replay per
  `nbtxlog.c`, so a PG standby can replay goopg index maintenance rather than
  only read a snapshot.
- **S11.6 — unblock #10.** Flip `relhasindex` in `buildUserPGClassRow`, re-run
  `TestE2E_PGStandbyFullCycle` end to end, and add `pg_amcheck` over a
  goopg-written user index as the standing gate.

## Risks

- **On-disk break.** S11.2–S11.4 change the format of every existing goopg
  index. There is no in-place upgrade path and none is planned: goopg has no
  released on-disk compatibility promise, and existing clusters REINDEX. This
  must be stated in the S11.2 commit message, not discovered by a user.
- **Sibling-path rule.** `internal/amcheck` validates the opaque through
  `ParseOpaque` and would silently accept whatever it is handed; the vacuum and
  bulkload writers each construct pages independently. A slice that changes one
  and not the others produces a tree that passes goopg's own tests and fails
  `_bt_checkpage`. `CheckPGBTPage` exists so each slice can assert the oracle's
  criterion directly.
- **Scope.** S11.4 and S11.5 are each plausibly multi-loop. They are listed as
  single slices for planning, and may be decomposed further when reached.
