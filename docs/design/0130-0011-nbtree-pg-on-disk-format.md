# 0130-0011 — nbtree PG-identical on-disk format

**Milestone:** M0130 (Cluster-directory compat with PG 18.3 + PG physical replication)
**Status:** draft (S11.1 + S11.2 + S11.3 + S11.4 slices 1-2-3a-3b-1-3b-2a-3b-2b-3b-2c-i-3b-2c-ii-A-3b-2c-ii-B1-3b-2c-ii-B2-a-3b-2c-ii-B2-b-3b-2c-ii-B2-b-ii-3b-2c-ii-B2-b-iii-3b-2c-ii-B2-b-iv-3b-2c-ii-B2-c-i-3b-2c-ii-B2-c-ii-3b-2c-ii-B2-c-iii-3b-2c-ii-B2-c-iv-3b-2c-ii-B2-c-v-3b-2c-ii-B2-c-vi-3b-2c-ii-B2-c-vii 3b-2c-ii-B2-c + 3b-3a landed 2026-08-10; S11.4 rest of 3b-3, S11.5, S11.6 not started)
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
  - **S11.2b — the flip (LANDED 2026-08-10).** Writers and readers point at
    those primitives; `readOpaque`/`writeOpaque` now encode upstream's 16-byte
    `BTPageOpaqueData` and translate at that single boundary. Every existing
    goopg index is unreadable from this commit on and must be REINDEXed —
    `BTREE_VERSION` could not be used as a break marker because upstream's
    `_bt_getmeta` insists it is 4.

    Three things the slice discovered that the plan above did not anticipate:

    1. **The in-memory spelling did not have to change with the on-disk one.**
       `BTPageOpaque` keeps `storage.InvalidBlockNumber` for "no sibling" and
       the legacy `BT*` flag bits; `pgSibling`/`legacySibling` and
       `pgFlags`/`legacyFlags` translate in `readOpaque`/`writeOpaque`. Both
       directions come from one `flagTranslation` table so the encode/decode
       pair cannot drift. Converting the ~65 `InvalidBlockNumber` sites too
       would have multiplied the flip's blast radius for no on-disk gain
       (ledger row, S11.2b).
    2. **Repointing `btpo_next` is a high-key operation.** Because presence is
       derived, a page that loses its right sibling (page deletion relinking a
       left sibling past an unlinked page) must slide its separator away in the
       same step, or `P_FIRSTDATAKEY` drops onto the stale separator and every
       data slot is off by one from then on. `pgWriteNextSibling` pairs the two
       and refuses the reverse transition, which only a split may make.
    3. **The separator is now paid for out of page space.** It used to sit in
       the 272-byte opaque's 256-byte reserve. `resetPageItems` therefore
       preserves and re-installs it (all four whole-page rewrite paths rely on
       that), the dedup-recovery budget subtracts `pageHighKeyFootprint`, and
       the bulk loader withholds `bulkHighKeyReserve` — deliberately the worst
       case, which keeps its per-page data capacity within twelve bytes of the
       legacy layout's so split points do not move. (Slice 3b-3d later replaced
       that constant with the exact separator body, once suffix truncation made
       one worth computing.)

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
- **S11.3 — metapage (LANDED 2026-08-10).** `BTreeMeta`/`parseMeta`/`writeMeta`
  are deleted; block 0 is built by `initMetaPage` → S11.1's `InitPGMetaPage`
  (`_bt_initmetapage`), so it is a PG-shaped page (16-byte special area,
  `BTP_META`) carrying the 48-byte `BTMetaPageData` at `PageGetContents` with
  `pd_lower` advanced past it. Four creation sites (`Create`,
  `BulkCreateWithOptions` × 2 limbs, `BulkCreateNoDedup` × 2 limbs) now
  initialise; the three root-pointer writers (`updateRootMeta`, the newroot-WAL
  limb in `insertIntoBlock`, `resetToEmptyRoot`) and `ReplayMetaSetRoot` are
  read-modify-write via `ReadPGMetaPage`/`WritePGMetaPage`, because
  `btm_last_cleanup_num_*` and `btm_allequalimage` belong to other writers.
  Two decisions the slice plan did not spell out:
  - `readMeta` now gates block 0 on `CheckPGBTPage` before decoding. Without it
    the format break is silent, not loud: a pre-S11.3 metapage has the same
    magic/version *offsets* (both layouts start the payload at
    `PageGetContents`), so `Open` would succeed and then read `Root`/`Level`
    out of bytes that no longer mean that. `Open` also relaxed its version test
    to `_bt_getmeta`'s `[BTREE_MIN_VERSION, BTREE_VERSION]` range.
  - `btm_allequalimage` is written unconditionally `true`. goopg deduplicates
    without consulting an opclass, and upstream `amcheck` errors on posting
    lists in a `!allequalimage` index; the faithful per-opclass
    `_bt_allequalimage` computation (support function 4) is a ledger row.
- **S11.4 — tuple shape.** goopg `item` → `IndexTupleData` + null bitmap + PG
  binary datums; downlinks into `t_tid`. This is the largest slice and the one
  that couples the index format to the type-codec layer. Decomposed into three
  loops; **slices 1 (the codec) and 2 (the writer flip) landed 2026-08-10**,
  slice 3 (the comparison layer + pivot truncation) is open.
  - **S11.4 slice 1 — the codec, additive.** `internal/access/btree/pgtuple.go`
    is `index_form_tuple`/`index_deform_tuple`
    (`postgres/src/backend/access/common/indextuple.c`, over `heaptuple.c`'s
    `heap_compute_data_size`/`heap_fill_tuple`) plus the `t_info` accessors from
    `itup.h` and the alternative-TID overlay from `nbtree.h`
    (`BTreeTupleIsPivot`/`IsPosting`, `Set`/`GetNAtts`, the downlink and the
    tiebreaker heap TID), `BTMaxItemSize` and `_bt_check_third_page`'s
    criterion. No writer is touched: `item.marshal`/`parseItem` still emit
    goopg's private body.
    Two things drove the shape of this slice. First, goopg **already** emits
    real `index_form_tuple` output — but only from ten hand-rolled fixed-shape
    encoders in `internal/initdb/btree_index_bootstrap.go`, each with its size
    worked out in a comment. Those are validated by a real PG 18.3 reading the
    bootstrap catalog indexes, which makes them an oracle: the new general
    codec is byte-compared against eight of them by
    `TestPGIndexTupleMatchesBootstrapEncoders` (internal/initdb), and they
    become its callers rather than staying duplicates. Second, the codec takes
    a local `PGIndexAttr` (attlen/attbyval/attalignby/attstorage) instead of a
    catalog `TupleDesc`, so the on-disk layer keeps its single dependency on
    `internal/storage` — S11.4 must not be the slice that couples
    `internal/access/btree` to the type layer, even though slice 3 must
    consume it.
    The traps the guards pin: `BlockIdData` is `(bi_hi, bi_lo)` uint16 halves,
    not a flat LE uint32 (encoding block 3 flat makes PG read 196608); the null
    bitmap lives at offset `sizeof(IndexTupleData)` = 8, **not** at `hoff` —
    the MAXALIGN pad between bitmap and data belongs to the data area; a set
    bitmap bit means the value is PRESENT; and a packable varlena short enough
    for a 1-byte header is re-headered **and** loses its alignment padding
    entirely (`fill_val`'s "convert to short varlena -- no alignment" branch),
    which is the single easiest way to produce a tuple PG reads one byte off.
  - **S11.4 slice 2 — flip the writers.** `(item).marshal`/`parseItem`/
    `parseItemNoCopy` in `btree.go` and the whole of `posting.go` now emit and
    read upstream `IndexTupleData`; the goopg-private body (`itemPrefixSize` =
    keyLen | flat LE uint32 block | offset) is deleted, and with it the `item`
    struct's redundant `keyLen` field — the key length is now recovered from
    `t_info`'s size, which is the single fact the whole slice turns on. The
    `item` struct survives as an in-memory (key, pointer) pair only.
    Three consequences the plan did not spell out:
    1. **The posting discriminator had to move in the same commit.** goopg
       marked a deduplicated item by setting the high bit of the leading
       `keyLen` field. Those two bytes are now `t_tid`'s `bi_hi` half, so a
       leaf item whose heap TID sits in a high enough block would have read
       back as a posting list. `posting.go` is therefore rewritten onto
       upstream's own discriminator and layout: `INDEX_ALT_TID_MASK` in
       `t_info`, `BT_IS_POSTING|nhtids` in `t_tid`'s offset half, the TID array
       at the byte offset carried in `t_tid`'s block half
       (`BTreeTupleSetPosting`, new in `pgtuple.go` this slice). Upstream
       requires ≥ 2 TIDs in a posting list, so the bulk loader's chunking now
       emits a plain item for a one-TID remainder.
    2. **Symmetrically, `parseItem` must reject `INDEX_ALT_TID_MASK`.** A pivot
       or posting tuple decoded as a plain item hands the caller nbtree status
       bits as a heap TID, and the size check does not catch it (a posting
       tuple's `t_info` size is correct).
    3. **Two MAXALIGNs are deferred to slice 3.** `index_form_tuple` rounds the
       tuple size up ("be conservative") and `BTreeTupleSetPosting` asserts a
       MAXALIGNed posting offset. goopg cannot honour either while its key is
       one opaque blob, because the key's length is recoverable only as
       `size - sizeof(IndexTupleData)` and padding destroys it. Neither stops a
       non-assert PG build from reading the page — it reaches every field
       through `t_info` and the line pointer — and slice 3 restores both once
       the key length comes from the index descriptor. Ledger row.
    Downlinks moved into `t_tid`'s block half via the new `downlinkItem`
    constructor (upstream's `BTreeTupleSetDownLink`), which is also the single
    site slice 3 converts into a truncated pivot tuple. The one encoder is
    exported as `PGBTItemRaw` so `internal/amcheck`'s page fixtures build pages
    through the engine's encoder instead of the hand-rolled second (and third,
    and fourth) transcription they carried before.
  - **S11.4 slice 3a — pivot tuples.** Slice 2 left every on-page tuple a
    *plain* one: an internal-page downlink was an `IndexTupleData` whose t_tid
    happened to hold a child block, and the P_HIKEY separator was an ordinary
    item. Upstream has a third tuple class for exactly these two slots — the
    PIVOT tuple, `_bt_truncate`'s output: `INDEX_ALT_TID_MASK` in `t_info`, the
    kept-key-attribute count in t_tid's offset half (`BTreeTupleSetNAtts`), the
    downlink in its block half. `PGBTPivotRaw` is now the ONE encoder for both
    slots, and the `item` struct carries an in-memory `pivot` flag so the
    parse/re-marshal round trip cannot demote one.
    Three things this slice turns on:
    1. **`parseItemBody` decodes pivots instead of rejecting them.** Slice 2's
       blanket alt-TID rejection was too strong once downlinks became pivots:
       every generic page reader (`pageItems`, `PageItemKeys`, `readPageItem`,
       the range scan) walks internal pages too. The rejection narrows to
       BT_IS_POSTING, which keeps its own decoder in `posting.go`. The pivot's
       t_tid halves are translated exactly once, here: block half → downlink,
       offset half → status data that is DROPPED rather than handed on as part
       of a heap TID.
    2. **The minus-infinity downlink is a zero-attribute pivot.** Upstream's
       leftmost internal item has no key at all (`nbtsort.c`); goopg's three
       "the leftmost item adopts a nil key" sites (`removeDownlinkFromParent`,
       its vacuum twin and the WAL replay path) rebuild it through
       `downlinkItem`, because an `item{ptr: …, key: nil}` literal would drop
       the pivot flag and re-emit a plain tuple.
    3. **The initdb bootstrap builder is the oracle again.**
       `pgBuildBtreeMinusInfinityDownlink` writes the leftmost downlink of every
       bootstrap catalog index and a real PG 18.3 descends those indexes, so its
       8 bytes are a validated reference — `PGBTPivotRaw(nil, child)` is
       byte-compared against it
       (`TestPGBTPivotRawMatchesBootstrapMinusInfinityDownlink`).
    Two upstream properties stay deferred (ledger rows): natts is 1 for every
    keyed pivot, because goopg's key is still ONE opaque blob and a composite
    index's separator cannot be truncated to a prefix of its attributes without
    slice 3b's per-attribute datums; and the tiebreaker-heap-TID pivot
    (`_bt_truncate`'s `keepnatts > nkeyatts` branch) is not written, because
    goopg's descent treats the separator as inclusive rather than as a strict
    lower bound, so adopting it would change routing rather than only bytes.
  - **S11.4 slice 3b — comparison layer.** Key bytes become per-attribute PG
    binary datums, so `CompareKeys`/`bytes.Compare` gives way to type-aware
    comparison and `FormPGIndexTuple`/`DeformPGIndexTuple` reach the writer
    path. That is what makes the key length descriptor-derived, and with it the
    two MAXALIGNs slice 2 deferred, real suffix truncation (`_bt_keep_natts`)
    and the retirement of `MaxHighKeyLen` / `bulkHighKeyReserve` in favour of
    `BTMaxItemSize` become expressible.

    3b is itself milestone-sized — it is the slice that couples the on-disk
    format to the type layer — so it decomposes into three:

    - **3b-1 — the descriptor and the comparator, additive (landed
      2026-08-10).** `internal/access/btree/pgcompare.go`. Upstream's bridge
      out of "nbtree knows no types" is the opclass support function
      (`BTORDER_PROC`) that `_bt_mkscankey` installs into a `BTScanInsert`'s
      `ScanKey`s; goopg takes the same seam. `PGKeyAttr` = the physical layout
      the codec already needs (`PGIndexAttr`) plus the three ordering
      properties `_bt_compare` consults — the comparator, `SK_BT_DESC` and
      `SK_BT_NULLS_FIRST` — and `PGIndexKeyDesc` is the per-index vector of
      them (key attributes only, matching
      `IndexRelationGetNumberOfKeyAttributes`, so INCLUDE columns need no
      special case). `ComparePGIndexTuples` is `_bt_compare`'s body for the
      tuple-vs-tuple case. Three upstream rules an attribute loop otherwise
      misses, each mutation-verified in `pgcompare_test.go`: a truncated
      attribute is **minus infinity**, not absent (upstream's
      `key->keysz > ntupatts ⇒ 1`, so the shorter side sorts first — this is
      what makes the zero-attribute minus-infinity downlink order correctly
      without a special case); the **heap TID is the last key attribute**
      (heapkeyspace), and a pivot whose tiebreaker TID was truncated away is
      minus infinity there too; and NULL ordering is **per attribute**, never
      global. `PGAttrComparator` is a plain left-vs-right comparator rather
      than upstream's flipped-argument convention (upstream calls
      `sk_func(index_datum, sk_argument)` and inverts for ASC), so `DESC` is
      one negation applied in exactly one place. A **nil** `Compare` means
      `CompareKeys`, which is the honest description of goopg's current
      order-preserving encodings (`EncodeInt4`, `EncodeNumericKey`,
      `EncodeVarchar`, …) — that is deliberate, and is what lets 3b-2 migrate
      one type at a time instead of as a flag day. Additive: no writer,
      descent path or split builds a descriptor yet, so nothing on disk moves.
      Not modelled, because no user at this layer needs them: collations
      (goopg's collation handling sits above the AM), cross-type comparison
      (tuple-vs-tuple is always same-type), and posting-list tuples (which of
      its heap TIDs would break the tie? — rejected rather than guessed).
    - **3b-2 — thread the descriptor and retire `CompareKeys`.** Build a
      `PGIndexKeyDesc` from the catalog (`pg_index.indoption` carries the
      DESC and NULLS FIRST bits independently — neither is derivable from the
      other), hand it to `btree.Options`, and convert the ~20 `CompareKeys`
      call sites in `btree.go` / `bulkload.go` / `internal/amcheck` to
      `ComparePGIndexTuples`. The writer path switches from one opaque key to
      `FormPGIndexTuple` over per-column datums at the same time; the two must
      move together (sibling-path rule), since a descriptor-derived reader
      against a blob-writing writer reads garbage. Three steps:

      - **3b-2a — the opclass comparators (landed 2026-08-10).**
        `internal/access/btree/pgcompare_types.go`. 3b-1 left `PGKeyAttr.Compare`
        with only its nil default, which means `bytes.Compare` and is correct
        exactly while the key bytes are goopg's order-preserving encodings.
        The moment 3b-2b's writer stores the datum's real PG binary image,
        bytewise ordering is wrong for nearly every type, so the opclass
        support-function-1 side has to exist FIRST or the flip has nothing
        correct to switch to. This lands `btint2cmp`/`btint4cmp`/`btint8cmp`
        (signed, LITTLE-endian native — a datum's on-disk image is its native
        memory image on the x86-64 PG 18.3 target, the same assumption
        `encodeValuePG` already makes), `btoidcmp` (**unsigned** — OIDs above
        2^31 exist), `btboolcmp`, `btcharcmp` (unsigned by upstream's explicit
        choice), `btfloat4cmp`/`btfloat8cmp` (`float8_cmp_internal`: NaN is
        larger than every non-NaN and equal to itself, and −0 = +0 — two rules
        a bit-pattern compare gets wrong), `byteacmp` and `bttextcmp` under the
        C collation, `bpcharcmp` (blank-padded: `bcTruelen` strips trailing
        spaces, so reusing the text comparator on a bpchar column is a silent
        wrong answer on every padded value), `btnamecmp`, `uuid_cmp`,
        `timetz_cmp` (ordered by GMT-equivalent time, then local time, then
        zone) and `numeric_cmp` over the on-disk `NumericData`
        (`cmp_numerics` → `cmp_var_common` → `cmp_abs_common`, including the
        −Infinity < finite < +Infinity < **NaN** ladder and the short header's
        sign-extended 7-bit weight). date/timestamp/timestamptz/time reuse the
        int4/int8 comparators, as upstream does. `PGAttrComparator` has no
        error return by design — it is the innermost loop of a descent and
        upstream's `sk_func` cannot fail either — so a datum whose length does
        not match its attlen falls back to `bytes.Compare` (deterministic and
        total, so a split still terminates) instead of panicking; corruption is
        amcheck's business. Additive: still nothing builds a descriptor.
      - **3b-2b — build the descriptor from the catalog (landed 2026-08-10).**
        `internal/executor/pgindex_keydesc.go`: `buildPGIndexKeyDesc(idx
        *catalog.Index) (*btree.PGIndexKeyDesc, error)`, goopg's
        `_bt_mkscankey` minus everything that belongs to a scan. It reads what
        `pg_index` records — the key columns' types (indkey), DESC / NULLS
        FIRST (indoption, via `ColDescending` / `ColNullsFirst`, both **empty
        when every column is the default ASC NULLS LAST**, so every read is
        bounds-checked, and both carried across independently because
        `... DESC NULLS LAST` is legal), the opclass (indclass) and the
        collation (indcollation) — and projects each column's type OID through
        `userTypeAttrsForOID` to a `PGIndexAttr` and through
        `pgIndexComparatorForOID` to its 3b-2a comparator. The mapper lives
        executor-side because `internal/access/btree` deliberately does not
        import `catalog`.

        The design decision that matters is that it is **conservative to the
        point of erroring**: a non-btree access method, an expression key, an
        explicit operator class (goopg records the spelling but has no
        `pg_opclass` registry to resolve it against), a non-bytewise collation,
        an array/enum/user type, or any type without a 3b-2a comparator yields
        an error, never a descriptor with a nil `Compare`. Nil means bytewise,
        which is right only while the on-page key is one of goopg's
        order-preserving encodings; the instant the writer stores real datums
        it silently mis-orders the tree. Erroring makes that failure mode
        unreachable — a caller that cannot get a descriptor stays on the
        pre-S11.4 path instead of writing a corrupt index. For the same reason
        the type resolution is a private switch over the built-in spellings
        rather than `buildUserPGAttributeRow`'s, whose `text` fallback for an
        unknown name is right for pg_attribute and catastrophic here (an enum
        column would take the text comparator while goopg orders enums by sort
        order). Guards: `pgindex_keydesc_test.go`.
      - **3b-2c — flip the writer and route every comparison through the
        descriptor. REINDEX-required.** The writer flip
        (`encodeCompositeBTreeKey`, `encodeIndexKeyFromCols`,
        `encodeArbiterKey` → per-column datums through `FormPGIndexTuple`) was
        originally planned for 3b-2b; building the mapper made clear it cannot
        land there. The sibling-path rule is symmetric: a descriptor-derived
        reader against a blob-writing writer reads garbage, **and** a
        datum-writing writer against the surviving `CompareKeys` sites orders
        garbage — real datums are not order-preserving under `bytes.Compare`
        for any type but bytea/text. So the writer flip and the comparison
        rerouting are one atomic change, and 3b-2b is exactly the additive part
        that could be separated from it. The remaining work is the seam: the
        ~20 in-package `CompareKeys` sites compare *key payloads*, whereas
        `ComparePGIndexTuples` needs whole tuples (it reads t_info's null
        bitmap and t_tid's natts/heap TID), so one `BTree`/bulk-builder
        comparison method over tuple-shaped operands has to exist before either
        half can move. That is what finally retires `CompareKeys`. Split in
        two, because the seam is behaviour-preserving and the flip is not:

        - **3b-2c-i — the seam (landed 2026-08-10). No on-disk change.**
          `internal/access/btree/pgkeycmp.go`: `keyComparer`, one per-index
          comparer holding an optional `*PGIndexKeyDesc`, plus
          `Options.KeyDesc` / `BTree.cmp` / `(*BTree).keyCmp()` to carry it
          from the catalog to every ordering decision. All ~20 in-package
          `CompareKeys` call sites now route through
          `keyComparer.compare` — the descent (`descendToLeaf`,
          `findChildBlockDirect`), both high-key overshoot tests
          (`keyExceedsHighKey`, `itemOvershootsHighKey`), the insert-slot
          binary search (`insertItemSorted`), the rightmost-cache range check,
          the split-path page rewrite (`appendSorted`, `dedupConsolidate`),
          `Search`, `rangeScanPos`, and both bulk-load sorts plus
          `deduplicateToRawItems`. The free helpers take the comparer as a
          parameter rather than becoming methods, so a caller cannot silently
          get the wrong index's order. `CompareKeys` survives only as the
          seam's nil-descriptor branch and as the exported name amcheck and
          `bt_index_check` use for goopg's default key order.

          Two properties are load-bearing and guarded (`pgkeycmp_test.go`, 4
          tests, 3 mutations caught). First, `compare` returns no error: it is
          called from `sort.Search` predicates and the innermost descent loop,
          the same constraint upstream places on `sk_func`, so an operand
          `ComparePGIndexTuples` refuses (a posting-list tuple, whose heap-TID
          tiebreak is ambiguous) falls back to the bytewise order — total and
          deterministic, which is what makes a split terminate; the corruption
          itself stays amcheck's business. Second, "key operand" is
          deliberately the index's *own* key representation, not a fixed
          layout: today the opaque blob, after 3b-2c-ii a whole index tuple,
          on both the stored and the search-key side.

          One gap the seam exposed and did not close: `ApplyInsertRecord` (WAL
          redo) has no `BTree` handle and therefore no route to a descriptor,
          so it passes the bytewise comparer explicitly. A descriptor-ordered
          tree replayed bytewise would insert at the wrong slot, so 3b-2c-ii
          must carry a per-relation descriptor lookup into the redo path.
          Ledger row.
        - **3b-2c-ii — the flip.** Split in two, because "did every
          btree-opening site learn about descriptors?" is a question about
          nineteen call sites and is far cheaper to answer against a tree whose
          behaviour has not moved:
          - **3b-2c-ii-A — the plumbing (landed 2026-08-10). No on-disk
            change.** `internal/executor/pgindex_btree.go` adds the three
            choke points `openIndexBTree` / `createIndexBTree` /
            `bulkCreateIndexBTree`; the executor's nineteen direct
            `btree.Open` / `CreateWithXID` / `BulkCreateWithXID` calls are gone
            (grep-enforceable: the package now names a btree constructor only
            inside that file). Each resolves `buildPGIndexKeyDesc` and passes
            it as `Options.KeyDesc`, memoised per statement on
            `Context.pgKeyDescCache` — keyed by index OID, with a
            present-but-nil entry caching the REFUSAL, since the callers are
            per-row index maintenance and re-deriving a refusal per row is the
            expensive case. `bulkBuildBTreeFull` gained an `*catalog.Index`
            parameter (not derivable from the relfilenode: REINDEX
            CONCURRENTLY builds into a shadow relfile). `btree.PoolLogSplit`
            is exported so an assembled `Options` cannot silently drop split
            WAL logging, and `(*BTree).KeyDesc()` makes "the descriptor
            reached the tree" observable. The gate `pgIndexTupleKeys` is
            **false**, so every descriptor is nil and the ordering is
            `CompareKeys`, byte for byte; it is a var, not a const, precisely
            so the plumbing is testable ahead of the flip
            (`pgindex_btree_test.go`, 5 tests). An index this layer refuses to
            describe (expression key, explicit opclass, non-bytewise
            collation, a type with no comparator) yields nil and keeps the
            blob path — so the flip cannot simply delete that path.
          - **3b-2c-ii-B1 — the item-codec seam (landed 2026-08-10). No
            on-disk change.** The descriptor decides a third thing besides the
            ordering: what `item.key` IS. In the blob format the key is a
            header-less payload and `(item).marshal` SYNTHESISES the
            `IndexTupleData` header from `item.ptr`; in the tuple format the
            key is the whole `FormPGIndexTuple` image (header included),
            because that is the only operand shape `ComparePGIndexTuples` can
            order. So ~30 sites that said `it.marshal()` / `parseItem(raw)` /
            `SizeOfIndexTupleData + len(it.key)` now ask the index's format.
            `keyComparer` was accordingly renamed `indexFormat` and grew the
            codec (`pgitemcodec.go`): `marshal` / `parse` / `parseNoCopy` /
            `bodySize` / `itemEncodedSize` / `pageHasSpaceFor` /
            `pageItems` / `pageItemsWithDead` / `pageHighKey` / `readPageItem`
            / `byteAwareSplitLoc` / `compactRawSize` / `itemsToRawItems` /
            `pageHasSpaceForBulk` / `snapshotPageItemsAsLog`. Ordering and
            layout are ONE object on purpose — they are one decision (`desc`),
            and letting them disagree yields a silently mis-ordered tree rather
            than a parse failure. Every production descriptor is still nil, so
            every method takes its blob branch. Sites with no index identity
            name `blobFormat` explicitly (greppable): the exported page readers
            (`PageItemKeys` / `PageLeafItems` / `PageDownlinks` /
            `PageHighKey`, used by internal/amcheck) and the two redo entry
            points (`ApplyInsertRecord`, `ReplayRemoveParentDownlink`) — the
            two places ii-B2 must teach that the format is a per-index catalog
            property. Guards (`pgitemcodec_test.go`, 6): the blob branch is
            pinned against `PGBTItemRaw`/`PGBTPivotRaw` directly, and the
            tuple branch is driven end to end — a descriptor-bearing tree of
            3000 int4 keys, inserted out of order across the sign boundary,
            scans in exact `btint4cmp` order through splits and multi-level
            descent, with the on-page bytes asserted to BE the tuple (ordering
            alone does not catch a layout slip: a nested tuple compares
            correctly and reads as garbage to PG). One bug found and fixed:
            `ComparePGIndexTuples` PANICKED on an operand shorter than a tuple
            header, which is what a minus-infinity search key
            (`rangeScanPos(nil, …)`, upstream's keysz = 0) is; it now errors,
            so `compare` falls back to the bytewise order where an empty key
            sorts first — which IS minus infinity.
          - **3b-2c-ii-B2-a — the key encoder (landed 2026-08-10). No
            on-disk change, no REINDEX.**
            `internal/executor/pgindex_tuplekey.go` adds `pgIndexTupleKey` /
            `pgIndexTupleKeyFromRow` / `pgIndexKeyColumns`: the converter from a
            row's key datums to the `FormPGIndexTuple` image, which no layer
            could produce before (3b-2b said how the attributes are laid out and
            ordered; nothing turned a `Datum` into the attribute image). The
            per-attribute bytes come from `encodeValuePG` — the heap's own
            encoder — because an index datum and a heap datum of the same type
            are the SAME physical image upstream (`index_form_tuple` and
            `heap_form_tuple` share `heap_fill_tuple`), and a second encoder
            would be exactly the sibling-pair divergence the ledger keeps
            recording. A prefix key (a scan positioning on part of a composite
            index) is stamped as a pivot via `BTreeTupleSetNAtts`, without which
            `BTreeTupleGetNAtts` would claim the index's full key count for a
            tuple that physically holds fewer attributes and
            `DeformPGIndexTuple` would read past the data area; with it,
            `ComparePGIndexTuples`' truncation rule reproduces the "position at
            the first entry matching this prefix" semantics the blob path got
            free from a bytewise prefix compare. Nothing calls it on a
            production path yet.
            **Discovery: goopg's stored image for `numeric` and `uuid` is not
            PostgreSQL's.** `encodeValuePG` writes the decimal string and the
            36-character canonical UUID as text varlenas, so `PGCompareNumeric`
            (which decodes base-10000 `NumericData`) orders `-1000` above `0`,
            and a `uuid` key would be a 16-byte window onto a 37-byte varlena.
            Both are HEAP-side divergences — a real PG 18.3 already misreads a
            goopg `numeric`/`uuid` column — so `buildPGIndexKeyDesc` now refuses
            those two types (`pgIndexKeyImageIsPGFaithful`) and they keep the
            blob key path; fixing the image belongs to `encodeValuePG`.
          - **3b-2c-ii-B2-b — the format-resolution sites (landed 2026-08-10).
            No on-disk change, no REINDEX.** The six sites B1 left naming
            `blobFormat` — the four exported page readers (`PageItemKeys`,
            `PageLeafItems`/`PageLeafEntries`, `PageDownlinks`, `PageHighKey`)
            and the two redo entry points (`ApplyInsertRecord`,
            `ReplayRemoveParentDownlink`) — move onto the exported
            `btree.IndexFormat` (`pgitemcodec.go`), so the format is now an
            ARGUMENT the caller supplies rather than an assumption the reader
            makes. `IndexFormatFor(desc)` builds one from a catalog descriptor
            and `(*BTree).Format()` from a live tree, keeping the one
            nil-means-blob convention already used by `Options.KeyDesc` and
            `(*BTree).KeyDesc`. The zero value is the blob format, so every
            caller resolves to blob today and neither the bytes nor the
            behaviour move; what changes is that "which sites still cannot
            resolve a descriptor?" became a compile-time question about a
            parameter. `internal/executor`'s `bt_index_check` resolves the
            format for real — from `Context.pgIndexKeyDesc(idx)`, the same
            catalog entry it already resolves the sibling per-index property
            (the opclass `KeyComparator`) from — and threads it through
            `btIndexVerify` → `btIndexLeftmostByLevel`.
            Two callers still cannot answer, and each now says so in exactly
            one named place with its own ledger row:
            `internal/amcheck`'s `blobIndexFormat` (its tiers take an
            `indexName`, not an index; threading the executor's resolved format
            down through them is **B2-b-iii**) and `internal/wal`'s
            `redoBlobIndexFormat` (recovery holds a relfilenode and has no
            catalog to resolve — upstream sidesteps this because nbtree redo
            re-inserts at the RECORDED offset, never by key; **B2-b-ii**).
            Both must land before B2-c: under the flip a wrongly-blob decode
            does not merely lose precision, it strips the header the comparison
            and amcheck read, so the tree would be verified with the wrong
            decoder and replayed into the wrong slot.
          - **3b-2c-ii-B2-b-iii — amcheck takes the format (landed
            2026-08-10). No on-disk change, no REINDEX.** The five
            `internal/amcheck` tiers that decode key bytes —
            `VerifyBtreeItemOrderCmp`, `VerifyBtreeParentDownlinks`,
            `VerifyBtreeUnique`, `CollectBtreeLeafEntries` and
            `VerifyBtreeHeapAllIndexedRelation` (plus the internal
            `leftmostLeafBlock` descent they share) — take a
            `btree.IndexFormat` from their caller, exactly as
            `cmpKeys amcheck.KeyComparator` is threaded, and
            `amcheck.blobIndexFormat` is gone. `internal/executor`'s
            `bt_index_check` already resolved the format in B2-b and now passes
            it down through `btIndexVerify` and `btIndexCheckUnique`, so the
            one caller that holds a catalog entry resolves for real and every
            other caller (page-bytes-only tests, the `VerifyBtreeItemOrder`
            convenience wrapper) states the blob choice explicitly as the zero
            `IndexFormat`. Guard:
            `internal/amcheck/verify_nbtree_tupleformat_test.go` builds a REAL
            400-key tuple-format tree with splits and asserts the leaf-walk
            collector under the resolved format returns whole index tuples
            (`PGIndexTupleSize` == len, `PGIndexTupleTID` == the reported TID),
            that NOT ONE of those keys decodes identically under the blob
            format, and that the item-order tier finds the tree clean when it
            is given the format together with the descriptor's comparator —
            because a tier that agreed with either decoder would make the
            parameter decorative.
            Discovered and deferred here: `VerifyBtreeParentDownlinks` compares
            its down-link lower bound with `btree.CompareKeys` and takes no
            comparator at all, so under the flip it would byte-compare whole
            index tuples. It has its own ledger row and is a B2-c prerequisite
            (B2-b-iv, below).
          - **3b-2c-ii-B2-b-iv — the parent-downlink comparator (landed
            2026-08-10). Behaviour-changing for damaged opclasses, no on-disk
            change, no REINDEX.** `VerifyBtreeParentDownlinks` now takes
            `cmpKeys amcheck.KeyComparator` next to the `keyFmt` B2-b-iii gave
            it (nil ⇒ `btree.CompareKeys`, the same convention as the other
            tiers) and evaluates the down-link lower bound through it;
            `btIndexVerify` passes the comparator it already holds. This is what
            upstream does — EVERY key comparison amcheck makes on an index goes
            through that index's support function 1, so
            `bt_child_check` → `invariant_l_nontarget_offset`
            (`verify_nbtree.c:2500-2540`) and `bt_target_page` share one
            ordering. Two consequences: opclass damage on a separator key is now
            reported by the cross-level tier as well as the item-order tier, and
            under the flip the bound compares key columns instead of whole
            tuples (whose leading `t_tid` header would dominate a byte compare).
            Guard: section (d) of `verify_nbtree_tupleformat_test.go` runs the
            tier over every internal page of the tuple-format tree — clean with
            the descriptor's comparator, and reporting on at least one page with
            a nil comparator, so the argument cannot go decorative. The same
            change raised that test's tree from 400 to 1200 keys: 400 int4
            tuples still fit on ONE leaf page, so it had no internal level and
            the cross-level tiers were never exercised.
          - **3b-2c-ii-B2-b-ii — the redo path's descriptor (landed
            2026-08-10). WAL-format change (no on-disk page change, no
            REINDEX; a pre-slice WAL stream is not replayable).** The other
            B2-b straggler is resolved by REMOVING the question rather than
            answering it, which is also what upstream does: nbtree redo never
            compares keys.
            - `btree.ApplyInsertRecord` (parse under a format, re-insert by
              key) is replaced by `btree.ApplyInsertRecordAt(page, raw,
              offnum)`, one `PageInsertItemRawAt` at the recorded physical
              offset number — upstream `btree_xlog_insert`'s single
              `PageAddItem` at `xlrec->offnum`
              (`postgres/src/backend/access/nbtree/nbtxlog.c:56-70`). Recorded
              bytes at a recorded offset need neither a parse nor a
              comparison, so the format parameter is gone.
            - The offset is now emitted for real. `LogBtreeInsertFunc` takes an
              `offnum`, the three `btree.go` emit sites compute it with the new
              `pgPhysOffnum` from the data index `insertItemSorted` already
              returned (an insert never touches `btpo_next`, so
              `P_FIRSTDATAKEY` is the same before and after), and
              `EncodeBtreeInsertPG` stops hard-coding 0. That closes the
              wal-pg-identical-stream A5 parity gap in the same move: a real-PG
              standby applies `xl_btree_insert` at `offnum`, so the placeholder
              was a latent divergence for a heterogeneous standby, not merely a
              goopg-internal shortcut. The native `RecordKindBtreeInsert`
              header grew 14 → 16 bytes to carry it; `offnum == 0` is rejected
              (a pre-slice record) instead of being guessed at.
            - `ReplayRemoveParentDownlink` becomes format-free by working on
              RAW item bytes: survivors are re-added verbatim (never parsed, so
              blob or tuple is immaterial) and the two things it must know live
              in the IndexTupleData HEADER, which is format-independent — "does
              the new leftmost item still carry key attributes?" is
              `len(raw) > SizeOfIndexTupleData`, and its child is t_tid's block
              half (`BTreeTupleGetDownLink`). The rebuilt minus-infinity pivot
              is `PGBTPivotRaw(nil, child)`, which is byte-identical to what
              the tuple format's `marshal` produces for the same item — a
              zero-attribute pivot has no key bytes to encode.
            - `internal/wal`'s `redoBlobIndexFormat` is therefore gone, and
              with it the last untrue "every tree on this cluster is
              blob-formatted" claim outside the writers. **B2-c is unblocked.**
            Guards: `internal/access/btree/replay_offnum_test.go` replays the
            emitted (raw, offnum) pairs onto a fresh page in BOTH formats and
            on both page shapes (rightmost / high-key-bearing, so the
            `P_FIRSTDATAKEY` bias is exercised rather than assumed) and demands
            byte-identical items; a second test keeps the RETIRED by-key body
            as an executable counter-example and asserts it disagrees with the
            writer on a tuple-format page — int4 `-1` is `0xffffffff` on disk,
            so bytewise order puts it after `+1` while the descriptor's
            comparator puts it before, which is exactly the silent
            standby-only divergence the slice removes.
          - **3b-2c-ii-B2-c-i — the prefix upper bound (landed 2026-08-10).**
            NO on-disk change, no REINDEX. A range scan's two bounds stop being
            symmetric the moment keys are tuples, and nothing in the engine said
            so. A search key naming only the first *k* key attributes (a
            composite index probed on its leading column) is a pivot, and
            `ComparePGIndexTuples` makes the shorter operand MINUS infinity
            beyond the attributes it stores. For the LOW bound that is exactly
            right — the descent lands on the first member of the prefix group.
            For the HIGH bound it is exactly wrong: `compare(entry, hi) > 0`
            holds for the group's very first member, so the scan would return
            ZERO rows. The blob format never had to say this out loud because it
            fakes plus infinity with bytes —
            `appendCompositeUpperPadding` (internal/executor/operators_index.go)
            appends 64 `0xFF` bytes so `bytes.Compare` orders the group below the
            bound — and there is no tuple equivalent: `0xFF` bytes are a
            malformed attribute image, not a large one. Upstream does not invent
            a maximal key either; it carries the bound's strategy in the scan key
            and stops when the compared ATTRIBUTES exceed it
            (`_bt_check_compare`, nbtutils.c). So the sense of a truncated bound
            became a property of the comparison, one per end of the range:
            `indexFormat.compare` is the low end (and every other ordering
            decision — descent, insert slot, split point), `indexFormat.
            compareHigh` is the high end, and `rangeScanPos`' two `hi` tests use
            it. For `desc == nil` `compareHigh` IS `CompareKeys`, byte for byte,
            which is what let this land ahead of the flip. Guards:
            `internal/access/btree/prefix_highbound_test.go` — blob-format
            equivalence over six pairs, the asymmetry at comparison level (the
            same pair reads `>0` under `compare` and `0` under `compareHigh`), a
            full-attribute bound agreeing with `compare` including the heap-TID
            tiebreak, and a 1200-entry two-column tree scanned across leaf-page
            boundaries with a prefix pivot as BOTH bounds, asserting the group is
            complete AND exclusive of the next one. Mutation-checked: reverting
            `rangeScanPos` to `compare` turns the 30-row group into 0 rows.
          - **3b-2c-ii-B2-c-ii — the upper-bound funnel (landed 2026-08-10).**
            NO on-disk change, no REINDEX. B2-c-i gave the *scan* a high end that
            reads a truncated bound as plus infinity; this slice gives the
            *probes* one place to decide what to hand it. Six sites — index scan
            (equality and range), index-only scan (equality and range), bitmap
            index scan, and the storage UPDATE-by-index path — each open-coded
            `appendCompositeUpperPadding(key)`, i.e. each independently asserted
            that a prefix upper bound is spelled with 64 `0xFF` bytes. That is
            true of exactly one of the two formats. All six now call
            `(*Context).compositeUpperBound(idx, key)`, which resolves the same
            `pgIndexKeyDesc` the tree took and returns the padded blob bound for
            `desc == nil` and the prefix pivot UNCHANGED otherwise. With the gate
            off every site is byte-for-byte what it was, so the funnel lands
            ahead of the flip; with the gate on the flip no longer has to touch
            these six files at all. Note what the funnel also does to the sites'
            `len(Index.Columns) > 1` guard: under the tuple format widening is a
            no-op, so that test degrades from a correctness condition to a cheap
            skip — a bound naming every key attribute compares identically under
            `compareHigh` and `compare`, heap-TID tiebreak included. An index the
            resolver refuses keeps the padding even with the gate on, which is
            the dual-format property stated as a test rather than assumed.
            Guards: `internal/executor/pgindex_upperbound_test.go` — blob
            equality with `appendCompositeUpperPadding` (plus no aliasing of the
            caller's key, which is simultaneously the LOW bound), the tuple
            branch returning the prefix with no `0xFF` run, the undescribable
            index keeping padding under the gate, and a source scan asserting
            `compositeUpperBound` is the ONLY caller of the padding helper —
            mutation-checked by reverting the bitmap site, which the scan
            reports by file and line. That last one matters because a seventh
            site added later would fail as wrong ROWS in a scan, never as a
            compile error.
          - **3b-2c-ii-B2-c-iii — the probe-key funnel (landed 2026-08-10).**
            NO on-disk change, no REINDEX. The low-end twin of B2-c-ii. The same
            six scan sites built the key they POSITION with — an equality probe,
            or one end of a range — by calling `encodeBTreeKeyForColumn` once
            per key attribute and concatenating the results, at ten call sites.
            That concatenation is not an encoder detail; it is the blob format's
            entire key layout, asserted ten times. Under the tuple format a page
            key is one `FormPGIndexTuple` image (null bitmap, per-attribute
            alignment padding, heap TID) and the concatenation of per-column
            blobs is not a degenerate version of it — it is not one at all. All
            ten now go through `(*Context).indexProbeKey(idx, parts)`, which
            resolves the same `pgIndexKeyDesc` the tree took: concatenation for
            `desc == nil`, `pgIndexTupleKey` under a ZERO `ItemPointer`
            otherwise. The zero TID is deliberate — heapkeyspace's final
            tiebreaker reads it as minus infinity, so an equality probe still
            lands before every real entry sharing its key attributes, which is
            what the blob path got by having no TID at all. A probe naming fewer
            than every key attribute becomes a pivot stamped with its own
            attribute count, i.e. minus infinity beyond what it names — the
            low-end mirror of the plus infinity B2-c-i taught `compareHigh`.
            Two consequences worth stating. First, under the tuple format there
            is no fallback: the tree IS tuple-shaped, so a refusal by
            `pgIndexTupleKey` (an unresolved TOAST pointer, an over-size key)
            surfaces as an error rather than quietly emitting a blob key that
            would scan the wrong range; indexes the resolver refuses never reach
            that branch, since they resolve to `desc == nil` and keep the blob
            path whole. Second, the funnel takes its columns from
            `pgIndexKeyColumns(idx)` and *checks* the caller's against them: the
            blob format tolerated a site that probed a non-leading attribute (it
            merely matched nothing), whereas a pivot silently means "the first N
            attributes", so a mismatch is now named at the call. Guards:
            `internal/executor/pgindex_probekey_test.go` — blob equality with
            the concatenation (multi-attribute and leading-attribute), the tuple
            branch equal to `pgIndexTupleKey` with `BTreeTupleGetNAtts` = 2 and a
            one-attribute probe that is a 1-natts pivot and NOT a byte prefix of
            it, the undescribable index staying blob under the gate, the
            non-leading and over-long probes rejected, and a source scan over the
            three scan files pinning `indexProbeKey` as the only scan-side
            encoder — mutation-checked by reverting the bitmap site, reported by
            file and line. `operators_storage.go` is out of the scan's scope on
            purpose: its remaining direct `encodeBTreeKeyForColumn` callers are
            the writer-side uniqueness and index-maintenance paths, which B2-c
            converts to `pgIndexTupleKeyFromRow` — a different funnel.
          - **3b-2c-ii-B2-c-iv — the row-key funnels (landed 2026-08-10).**
            NO on-disk change, no REINDEX. The writer-side counterpart of
            B2-c-iii, and the discovery that reshaped the remaining flip: one
            function, `encodeIndexKeyFromCols`, has served *four* distinct jobs
            since M0100-0005, and the blob format is what made that invisible.
            The four are (a) the key an index ENTRY is stored under, (b) the key
            a uniqueness / exclusion probe POSITIONS with, (c) a VALUE
            FINGERPRINT compared with `bytes.Equal` (`indexKeyColumnsChanged`,
            deciding whether an UPDATE touched an index at all), and (d) a value
            fingerprint HASHED into an SSI bucket page tag
            (`ssiRecordHashIndexInsert`). A blob key is TID-free, so all four are
            the same bytes and no caller could ask for the wrong one. A tuple key
            is not: the heap TID lives *inside* the image (`t_tid`) and is the
            final tiebreaker of the heapkeyspace ordering. Under the tuple format
            (a) must carry the row's real TID so duplicates of one key sort by
            physical position exactly as `_bt_compare` orders them; (b) must
            carry the ZERO TID, which is minus infinity in that position, so a
            probe lands *before* every real entry sharing its key attributes —
            handing a probe an entry key would start a duplicate scan after some
            of its own matches, a silent under-read; and (c)/(d) must stay
            TID-free entirely, because two heap versions of one logical row would
            otherwise never compare equal (every UPDATE would report "key
            changed") and a writer would hash into a different bucket than its
            readers. So the split is three-way, not two-way: `(*Context).
            indexEntryKey(idx, cols, row, tid)` and `(*Context).indexRowProbeKey
            (idx, cols, row)` (both over one `indexRowKey`, blob-identical when
            `desc == nil`), and the two fingerprint callers deliberately LEFT on
            `encodeIndexKeyFromCols`, each with a comment saying why it must not
            move. Seven call sites routed: three entry sites
            (`maintainUniqueIndexesForInsert`, and the upsert operator's two
            non-arbiter maintenance paths) and four probe sites
            (`checkUniqueIndexesForInsert`, `checkUniqueIndexesForUpdate`,
            `checkExclusionConstraintsForInsert`, `queueDeferredExclusionCheck`).
            The shared PROJECTION — resolve each `idx.Columns` name in the
            caller's `cols`, read the row at that position, normalise enum labels
            to `KindEnum` — was factored out as `indexRowKeyValues` so the two
            formats cannot derive the key columns differently; `ok == false`
            keeps the encoder's long-standing "nil key means this row is not in
            this index" answer (NULL key column, expression key). One subtlety
            the split exposed: `maintainNonArbiterIndexesForUpdate` reuses the
            key cached from the SPECULATIVE insert to avoid re-evaluating a
            side-effectful expression index. A blob key is TID-free so the spec
            row's key *is* the updated row's key; a tuple key is not, and this
            insert points at a different heap tuple, so the cache is now bypassed
            whenever a descriptor exists (such an index is never an expression
            index — `buildPGIndexKeyDesc` refuses those — so re-encoding costs
            only a projection). Guards:
            `internal/executor/pgindex_rowkey_test.go` — blob entry key ==
            probe key == `encodeIndexKeyFromCols` byte for byte with a real TID
            supplied; tuple entry != probe, entry is not a pivot, `probe <
            entry` under `ComparePGIndexTuples`, and both deform to identical
            attributes; a NULL key attribute still yields `nil, nil` on both
            funnels (indexing NULLs is a semantic change, not a format one — see
            the ledger row for B2-a); an undescribable index keeps the blob key;
            and a source scan over `operators_upsert.go` and
            `deferred_exclusion.go` pinning the funnels as the only tree-key
            writers there, mutation-checked by reverting the deferred-exclusion
            site. `operators_storage.go` and `ssi.go` are out of the scan's scope
            on purpose — they are exactly where the two fingerprint uses live.
          - **3b-2c-ii-B2-c-v — the build path's key, order and duplicate test
            (landed 2026-08-10).** NO on-disk change, no REINDEX. The last of
            the three writer funnels, and the one with a property the runtime
            writers do not have: a bulk build SORTS its entries and then decides
            uniqueness by comparing neighbours. Both steps read the key, and both
            were written against the blob format's two silent assumptions —
            "a key is a byte string whose bytewise order is the index order" and
            "two rows with equal key values produce equal key bytes". Under the
            tuple format both are false, and each fails differently:
            a bytewise sort of tuple images files entries where no `_bt_compare`
            descent looks for them (a real PG datum is order-preserving under
            `bytes.Compare` for no type but bytea/text), while `bytes.Equal` on
            tuple images can NEVER report a duplicate, because the heap TID is
            inside the image and distinct by construction — so a unique build
            over genuinely duplicated data would succeed and produce a unique
            index containing duplicates. Upstream keeps the two questions apart
            inside one comparator: `comparetup_index_btree`
            (`src/backend/utils/sort/tuplesortvariants.c:1668`, PG 18.3) walks
            the key attributes, raises 23505 the moment they all compare equal,
            and only THEN falls through to the ItemPointer tiebreak "required for
            btree indexes, since heap TID is treated as an implicit last key
            attribute". Landed: `(*Context).indexBuildEntryKey` (blob ⇒
            `encodeCompositeBTreeKeyWithExprs` verbatim; tuple ⇒
            `pgIndexTupleKey` with the row's REAL heap TID — the same fact that
            travels beside the key in `BulkEntry.Ptr`), `btree.ComparePGIndexTupleKeyAttrs`
            (`ComparePGIndexTuples` minus the TID tiebreak), and
            `sortBuildEntriesFindDuplicate`, which folds the former
            `sortBulkEntriesByKey` + `bytesEqual` into one format-aware place —
            both deleted rather than left unused, since a surviving copy is what
            a later build path reaches for. `collectBTreeEntries` now takes the
            `*catalog.Index` it is building. `backfillBTree` (the pre-M0047-0001
            one-at-a-time build) is NOT routed: it has no callers, and its
            comment now says a caller may not be re-added without routing it.
            Guards: `internal/executor/pgindex_buildkey_test.go` — blob key is
            the old encoder verbatim for both a zero and a real TID; the tuple
            key carries the row's TID, is not a pivot, orders two duplicates by
            TID, and compares EQUAL under `ComparePGIndexTupleKeyAttrs`; NULL key
            column still yields `hasNullKey` in both formats; an undescribable
            index keeps the blob key; the sort puts 1 before 256 where the
            bytewise order is the opposite (same TID on both, since `t_tid` leads
            the image and would otherwise dominate the byte comparison); the
            duplicate test fires for two rows with one value; and a scoped source
            scan over `operators_ddl.go` allowing only the two encoders
            themselves and dead `backfillBTree`. Mutation-checked: forcing the
            blob branch and giving the duplicate test the TID tiebreak each fail
            with the message naming the defect.
          - **3b-2c-ii-B2-c-vi — posting lists group by the KEY ATTRIBUTES
            (landed 2026-08-10).** NO on-disk change, no REINDEX. Found by
            B2-c-v, and the same distinction one level down: `_bt_load` closes a
            posting run when the KEY attributes stop matching
            (`_bt_keep_natts_fast`, `src/backend/access/nbtree/nbtutils.c`),
            never with `_bt_compare`, because a heapkeyspace tree's ordering
            breaks ties on the heap TID and therefore reports NO two entries
            equal. `deduplicateToRawItems` grouped with `indexFormat.compare`,
            so under the tuple format every run would close at length 1: same
            rows, same order, one line pointer per TID — deduplication silently
            off, and an index several times its proper size. A row-count gate
            cannot see that, so the guard is structural.
            Landed: `indexFormat.compareKeyAttrs` (nil desc ⇒ `CompareKeys`,
            byte for byte; else `ComparePGIndexTupleKeyAttrs`) and the grouping
            switched onto it. The posting LAYOUT had to move with it, because a
            run that now forms is a run that gets marshalled: `marshalPosting` /
            `parsePostingRaw` became `indexFormat` methods, with
            `postingOffsetFor` naming the split — blob keys stay at
            `[8:8+len(key)]` byte for byte (the un-MAXALIGNed offset remains a
            3b-3 deferral), while a tuple key IS the tuple, so it sits at
            `[0:MAXALIGN(len(key))]` exactly as `_bt_form_posting`
            (`nbtdedup.c`) copies `keysize` bytes of its base and puts the array
            at `MAXALIGN(keysize)`. Parsing a tuple-format posting returns the
            PLAIN leaf tuple it stands for — size restamped, alt-TID bit
            cleared, first heap TID back in `t_tid` — which is both what
            `ComparePGIndexTuples` will accept (it refuses posting tuples
            outright) and the `base` a re-marshal takes back, so the round trip
            closes. The new `indexFormat.postingItems` centralises the four page
            readers' expansion of a posting into one item per TID, which is not
            "the same key repeated" once keys are tuples: each item's key is
            stamped with its OWN TID, or `item.key` would disagree with
            `item.ptr` and `marshal` would write the disagreement to the page.
            Guards: `internal/access/btree/pgposting_format_test.go` — five
            same-key/different-TID entries produce ONE posting while the
            ordering still calls all five distinct; the blob path reproduces the
            pre-seam bytes and the `[8:]` key position; the tuple layout puts
            the array at the key tuple's own length (24 would be the blob
            answer) and round-trips through parse and re-marshal; expansion
            stamps per-TID keys that re-marshal to themselves; and an
            oversized 4000-duplicate run still chunks under `maxRawItemSize`
            while holding every TID. Mutation-checked: grouping by `compare`,
            using the blob offset under the tuple format, and dropping the
            per-TID stamp each fail by name.
          - **3b-2c-ii-B2-c-vii — the arbiter-key funnel (landed
            2026-08-10).** NO on-disk change, no REINDEX. The last writer
            conflation, and B2-c-iv's split applied to the one index the upsert
            path does not maintain through those funnels: the ON CONFLICT
            arbiter. `encodeArbiterKey` builds ONE key that is both PROBED with
            (`probeArbiterByKey`, `findInProgressConflictKey`,
            `probeSpeculativeConflict`) and INSERTED (`maintainArbiter`). Under
            the blob format those are the same bytes, and reusing them is
            deliberate — it is what keeps a side-effectful arbiter expression
            (`blurt_and_lock_*`, insert-conflict-specconflict.spec) from being
            evaluated twice, including the Phase-B key `applyInsert` computes
            BEFORE the heap write. Under the tuple format they are two keys: the
            entry carries the row's heap TID, the probe the zero TID that is
            heapkeyspace's minus infinity. Using one for the other files the
            entry among the wrong duplicates or starts the conflict probe after
            some of its own matches — a MISSED conflict, i.e. a duplicate row,
            which no format check would report.
            Landed: `Context.arbiterProbeKey` / `arbiterEntryKey` over one
            `arbiterKey` (blob branch = `encodeArbiterKey` verbatim), the nine
            call sites in `operators_upsert.go` routed by role, and
            `applyInsert` rebuilding the entry key from the Phase-B probe key
            once the heap TID exists — only under the tuple format, where the
            arbiter index provably has no expression key column, so no side
            effect is repeated. `oc.ArbiterColumns` is in the arbiter INDEX's
            key order (`resolveArbiterIndex` walks `idx.Columns`), but its
            ordinals address the table the upsert runs against, which need not
            be the table the index was resolved on (the partitioned path probes
            with the parent's row), so the two sides are reconciled BY NAME and
            a disagreement is reported rather than encoded — a blob key built
            from the wrong column matched nothing, a tuple key matches something
            else. The same slice made the three `encodeExprIndexKey` fallbacks
            blob-only: that fallback concatenates per-column blobs, so under the
            tuple format it would file an unparseable key instead of skipping a
            row, and the only way to reach it WITH a descriptor is a refusal by
            the tuple encoder itself, which must not be papered over.
            Guards: `internal/executor/pgindex_arbiterkey_test.go` — blob probe
            and entry are byte-identical to `encodeArbiterKey`; the tuple pair
            differ, compare equal on key attributes and the probe sorts first;
            a NULL conflict column still answers (nil, nil) in both roles; an
            expression arbiter keeps the blob answer; swapped and out-of-range
            ordinals error; and a source scan keeps `operators_upsert.go` from
            calling `encodeArbiterKey` outside the funnel. Mutation-checked:
            dropping the entry TID and dropping the name reconciliation each
            fail by name.
          - **3b-2c-ii-B2-c-viii — the fingerprint funnel (landed).** The
            counterpart of B2-c-iii..vii: after every tree-key producer had a
            name that switches format, what was left calling the raw blob
            encoders was a different kind of caller, and this slice named it.
            A FINGERPRINT is an encoding compared with — or hashed alongside —
            another fingerprint of the same index, never handed to a btree.
            Two shapes, six sites: whole-key `indexKeyFingerprint`
            (`indexKeyColumnsChanged`, which decides whether an UPDATE touched
            an index at all; `ssiRecordHashIndexInsert`, which hashes into an
            SSI bucket page tag) and per-column `indexColumnFingerprint` (the
            three NULLS NOT DISTINCT sites — `nndKeyColumnsEqual`,
            `resolveNNDKeyColsFromRow`, `scanNNDLiveMatches` — where a NULL key
            column means the row has no btree entry at all, so uniqueness is
            decided by a heap scan). Both live in
            `internal/executor/pgindex_fingerprint.go`, take no `*Context`, no
            descriptor and no `storage.ItemPointer`, so they cannot acquire a
            heap TID even by accident.
            **The named invariant this establishes:** after the flip goopg
            computes a key TWO ways for a describable index — the tuple image
            for the tree, the blob concatenation for the fingerprints — so
            `encodeIndexKeyFromCols` and `encodeBTreeKeyForColumn` SURVIVE the
            flip rather than being deleted by it. Every one of the six compares
            bytes derived from DIFFERENT heap tuples, and routing any of them
            costs wrong behaviour, not an error: every UPDATE would report "key
            changed" and re-probe every unique index, an SSI writer would hash
            into a bucket no reader holds, and the NND heap scan would stop
            finding duplicates — a unique constraint silently admitting a second
            NULL-keyed row. The equivalence they rely on is also not the tree's:
            blob column encodings are injective per type, so equal bytes means
            equal values under the type's normalisation, whereas the tuple
            format answers equality with `ComparePGIndexTupleKeyAttrs` over
            datums. The two agree for every type `buildPGIndexKeyDesc` accepts
            (bytewise collations only); a non-deterministic collation is the
            first place they could diverge, and the resolver refuses those.
            One pairing the slice made explicit: the SSI hash bucket is computed
            from the WRITER's fingerprint but from the READER's *scan search
            key* (`ssiRecordHashBucketRead` is handed `operators_index.go`'s
            `loBytes`, which comes from the format-aware scan funnel), so the
            two agree only while a hash index is undescribable —
            `buildPGIndexKeyDesc`'s access-method refusal is load-bearing for
            SSI, and is now guarded as such.
            Guards: `internal/executor/pgindex_fingerprint_test.go` — with the
            gate on and a describable index the fingerprint still equals the
            blob and differs from `indexEntryKey`; equal key values fingerprint
            identically across two row versions; the per-column fingerprint
            matches `encodeBTreeKeyForColumn`; `nndKeyColumnsEqual` still reports
            "unchanged" (NULL == NULL included) under the gate; a hash index is
            never describable; and a function-scoped source scan over
            `operators_storage.go` + `ssi.go` allows the raw encoders only inside
            `encodeIndexKeyFromCols` and `encodeExprIndexKey`. Mutation-checked:
            reverting one NND site and removing the access-method refusal each
            fail by name.
          - **3b-2c-ii-B2-c — the flip (landed). REINDEX-required.**
            `pgIndexTupleKeys` is now **true**: every index the resolver can
            describe is written and read as PostgreSQL index tuples, and every
            index it refuses (an expression key, an explicit operator class, a
            non-bytewise collation, a type with no 3b-2a comparator, and the two
            types whose goopg stored image is not PG's — numeric and uuid) keeps
            the blob path. **A tree's key format is therefore a per-INDEX
            property, derived from the catalog, with nothing on disk recording
            it.** That is the dual-format decision this slice owed, and it is
            deliberate: the metapage is byte-faithful `BTMetaPageData` whose
            version must stay 4 for a real PG to accept it (S11.3), so there is
            no field to stamp a goopg-private format into. The consequence is
            the REINDEX requirement — an index built before the flip is read
            afterwards under the tuple comparator, which cannot deform it.

            The eight preceding funnels made every key PRODUCER format-aware.
            What the flip itself uncovered is that three CONSUMERS were not, and
            all three were invisible while the two formats coincided:

              1. **`(*BTree).Search` asked for full-key equality.** Its match
                 test was `compare(entry, key) == 0`, and under the tuple format
                 the heap TID is inside the key: a search key carries the zero
                 TID, a stored entry its row's real one, so equality is
                 impossible by construction and EVERY unique-index probe
                 reported "no such key". It now asks `compareKeyAttrs` — the
                 grouping question B2-c-vi named — at the scan's front door.
                 The same zero TID also makes the probe minus infinity among its
                 own duplicates, so a group beginning exactly at a page boundary
                 sits one page RIGHT of where the probe descends; `Search` now
                 steps right on an exhausted page, as `_bt_first` does via
                 `_bt_stepright` (nbtsearch.c).
              2. **`indexFormat.compareHigh` weighed the heap TID.** A range
                 bound is a bound on the KEY — `indexProbeKey` builds it from
                 expressions, so it always carries the zero TID. Weighing it put
                 every real entry above an upper bound naming its exact key, so
                 an equality scan stopped before its first row. The bound is now
                 evaluated on key attributes alone (upstream's split: the scantid
                 participates in `_bt_compare` during descent, while the scan's
                 stop condition is `_bt_check_compare` over scan keys). The LOW
                 end deliberately keeps the tiebreak, where minus infinity is
                 exactly "start at the first duplicate".
              3. **The index-only scan decoded keys as blobs.** IOS is the only
                 reader that runs the funnels backwards — it answers a query FROM
                 the index entry. Its running-offset walk over concatenated
                 order-preserving segments cannot read a tuple image (it consumed
                 a type's width out of a null bitmap and an alignment hole).
                 `pgIndexTupleKeyDatums` is the inverse of `pgIndexTupleKey`,
                 built on `DeformPGIndexTuple` + `decodePhysicalPGValueMctx` —
                 the same function that reads a heap tuple's data area, which is
                 the inverse of the `encodeValuePG` that wrote the key.

            A fourth consumer was found by the amcheck port rather than by the
            suite: **`checkunique` compared keys bytewise**, so with the TID
            inside the key it found every entry distinct and stopped detecting
            duplicates — an under-report by a CORRUPTION CHECKER, which is worse
            than a wrong answer from a query. The tier now runs under
            `IndexFormat.CompareKeyAttrs`. A user-opclass comparator never
            coexists with a descriptor (the resolver refuses such an index), so
            the two comparator sources cannot collide.

            The suite's own format assumptions were the rest of the work, and
            separating the two kinds mattered: the byte-for-byte blob guards are
            still LIVE code (the refused indexes use it) and now pin the gate off
            with `withBlobIndexKeys`, whereas the DDL type-acceptance suites were
            asserting the format only incidentally — "can a timestamp column be
            indexed and found again?" is a question about the type — so they were
            routed through the engine's own `openIndexBTree` / `indexProbeKey`
            and now track whichever format the index resolves to.

            Gates: units PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2,
            Q13 rows=35); pgbench smoke PASS — and its `select only` arm is an
            index scan on a freshly built tuple-format PK at 12.8k TPS, the
            first end-to-end read of the new format under concurrency.
            **TPC-DS SF0.5 PASS=95 ERROR=0 MISMATCH=0 CKMISMATCH=0, plan shapes
            identical** — but only after a REINDEX, and what that REINDEX
            revealed is worth more than the flip's own gate result: the first
            sweep came back with 42 queries turned ERROR, and they were NOT this
            slice's doing. Every index scan on both bench clusters was failing
            `btree: index contains corrupted page at block 0: special size 0,
            want 16` — an old-format page read by the 16-byte-opaque reader —
            *identically on a binary rebuilt with the gate OFF*, and the last
            green sweep (5ea5078b) predates the entire S11.2/S11.3 series. The
            clusters had been carrying those two REINDEX-required breaks
            un-remediated ever since. REINDEXing all 24 SF0.5 PKs under the
            flipped binary (46s) restored the pre-S11.2 baseline exactly.
            The general lesson, now a ledger row: a REINDEX-required change is
            only half done when the code lands — nothing re-runs it on the
            long-lived bench clusters, and the gate that should have caught it
            (`tpch-spotcheck.sh`) happens to run two seq-scan plans. The TPC-H
            cluster is still un-remediated because `REINDEX` inside db `tpch`
            hits the per-DB catalog scoping gap the ledger already records for
            ANALYZE, so SF=1-scale index behaviour stays ungated.

            **Addendum (2026-08-11, M-NIGHTLY AI-20260811-014635-012) — the
            other half of that lesson arrived eleven hours later, and it says
            "every cluster", not "the cluster you were looking at".** The
            remediation above REINDEXed the **SF=0.5** TPC-DS cluster, because
            SF=0.5 is what the fast regression gate sweeps. The nightly batch
            clones **SF=1** (`bench/tpcds/runtime_goopg/data` →
            `tmp/goopg-nightly-tpcds-data`), which nobody had touched, so run
            `20260811-014635` came back with 25 of 99 queries ERRORing on the
            identical `special size 0, want 16` — q1 q6 q7 q9 q13 q17 q18 q19
            q22 q26 q27 q29 q34 q40 q44 q48 q61 q68 q73 q75 q79 q80 q84 q85
            q91. Reproduced at HEAD `c58650b7` on port 65436, so the "is it
            stale?" step resolved to *real*, and the cause is unambiguously the
            un-run REINDEX: nothing in the two intervening commits
            (`02995818`, `46103e4e`) touches nbtree, and REINDEXing the 24
            SF=1 PKs under the current binary (95 s, all 24 ok) turned every
            one of the six spot-checked failures back into rows.

            Two things follow, and only the first is done. (1) The remediation
            is now a script rather than a remembered `psql` session —
            `bench/reindex_cluster.sh <port> [db]` enumerates
            `pg_indexes WHERE schemaname='public'` and REINDEXes each,
            reporting per-index timing so a stale cluster is a one-command fix
            for whichever port the next flip breaks. (2) *Detection* is still
            missing, and that is the part that would have made this a
            non-event: the tpcds stage's own spotcheck (Q3, Q98) passed on the
            broken cluster because both are seq-scan plans, exactly the way
            `tpch-spotcheck.sh` passed on the broken SF=0.5 cluster in August
            10's entry. A cheap fix exists — have the stage run one
            `CheckPGBTPage` probe per index (or one guaranteed index-scan
            query) before the sweep and fail with "cluster needs REINDEX"
            instead of 25 opaque query errors — and it is filed as a ledger
            row, not built here, so this loop stays one task. The SF=0.5
            cluster was re-verified green in passing (q1 returns rows on
            65437); the TPC-H cluster's `tpch` db remains blocked on the
            per-DB scoping gap as recorded above.

    - **3b-3 — collect the deferrals (2026-08-10, complete: 3b-3a…3b-3d).** With
      the key length descriptor-derived, restore `index_form_tuple`'s MAXALIGN of
      the tuple size and `BTreeTupleSetPosting`'s MAXALIGNed posting offset (3b-3b
      below — both were already satisfied per-format, and the live gap turned out
      to be `_bt_form_posting`'s total), implement
      `_bt_keep_natts` suffix truncation (pivot natts < nkeyatts at last), and
      retire `MaxHighKeyLen` / `bulkHighKeyReserve` in favour of
      `BTMaxItemSize`.

        - **3b-3a — MAXALIGNed item placement (2026-08-10, landed).** The
          first of the deferred MAXALIGNs, and the one that never actually
          needed a descriptor. Slice 2 filed *two* of them together —
          `index_form_tuple`'s rounding of the tuple SIZE, and
          `PageAddItemExtended`'s rounding of the item's PLACEMENT — under one
          reason: a blob key's length is recoverable only as
          `size - SizeOfIndexTupleData`, so padding destroys it. That reason
          holds for the first and not the second. Upstream's placement
          arithmetic is

              alignedSize = MAXALIGN(size);
              upper -= alignedSize;
              ItemIdSetNormal(itemId, upper, size);

          — the *allocation* is aligned, the *line pointer* keeps the exact
          size. The padding therefore lands between items, where no reader
          addresses it, and `lp_len - SizeOfIndexTupleData` is still the blob
          key's length. So it lands now, for both formats and for the
          pre-existing pages of both: alignment only governs where the NEXT
          item goes, so nothing on disk has to be rewritten and this slice is
          *not* REINDEX-required (unlike every S11.2–S11.4 slice before it).

          `storage.PageAddItemRaw`, `PageInsertItemRawAt` and
          `PageReplaceItemRaw`'s grow branch now allocate `maxAlign8(len(raw))`
          — the same helper `PageAddHeapTuple` has used since M0106-0010, so
          the heap and index sides of the page layer finally agree. The
          replace path's *in-place* branch deliberately does NOT reuse an
          item's own alignment padding: a page written before this slice has
          none, and growing into it would clobber the neighbouring item.

          The budget had to move with the writer or root-0040 re-opens from
          the other side (caller told a page has room, writer answers
          `ErrNoSpaceInPage`): `indexFormat.itemEncodedSize` — the single
          source both `pageHasSpaceFor` and the insert path read — now charges
          `MaxAlign(bodySize)`, and the two sites that compute a footprint
          without it (`pageHighKeyFootprint`, the bulk loader's raw-append
          check) match. `bulkHighKeyReserve` is unchanged: its body term is
          already a multiple of 8.

          Guard: `TestRawItemPlacementIsMaxAligned`
          (`internal/storage/page_item_maxalign_test.go`) pins both halves at
          once — every residue mod 8 through all three writers, asserting
          `lp_off` and `pd_upper` are aligned, `lp_len` is *not* rounded, and
          no neighbour is clobbered. Asserting only alignment would let a
          future "simplification" round `lp_len` too and silently corrupt
          every blob key.

          Still deferred at the time of 3b-3a, but see 3b-3b below, which
          revisits the premise: `index_form_tuple`'s tuple-size MAXALIGN and
          `BTreeTupleSetPosting`'s posting offset, both of which pad INSIDE
          the tuple; `_bt_keep_natts`; `BTMaxItemSize`.
    - **3b-3b — the tuple-INTERNAL MAXALIGNs (2026-08-10).** Filed as "blocked
      until every index is descriptor-bearing", and the block turns out to have
      been the wrong question. The two MAXALIGNs it named apply to the format
      that can EXPRESS them, and both were satisfied the moment the format split
      landed: `index_form_tuple`'s `size = MAXALIGN(hoff + data_size)` is
      `FormPGIndexTuple`'s `size := MaxAlign(hoff + dataSize)` (`pgtuple.go`),
      and `BTreeTupleSetPosting`'s `Assert((size_t) postingoffset ==
      MAXALIGN(postingoffset))` is `postingOffsetFor`'s `MaxAlign(len(key))`
      (`posting.go`). What was blocked was applying them to a BLOB key, which is
      not what upstream does — and is not a wait, it is permanent: an expression
      key, an explicit operator class, a non-bytewise collation or a
      non-PG-faithful stored image each drop an index to the blob format for
      good (`buildPGIndexKeyDesc`), and such a key has no per-attribute layout
      to align to. Ledger row.

      The real gap was a THIRD MAXALIGN the item never named — `_bt_form_posting`'s
      TOTAL (nbtdedup.c):

      ```c
      if (nhtids > 1)
          newsize = MAXALIGN(keysize + nhtids * sizeof(ItemPointerData));
      else
          newsize = keysize;
      Assert(newsize == MAXALIGN(newsize));
      ```

      A six-byte `ItemPointerData` array leaves the tuple unaligned even when its
      key material is not, so goopg's exact `postingOffset + n*6` differed from
      upstream on *every* posting it wrote. That only becomes reachable with a
      real PG in the picture, which is precisely M0130's subject: after a
      failover the promoted PG writes MAXALIGNed postings into these indexes, and
      goopg's `postingBounds` rejected the padding by construction
      (`postingOffset+n*6 != size`) — a clean parse failure on every deduplicated
      leaf entry PG had touched, i.e. the reverse-attach direction the S10
      harness exercises.

      `indexFormat.postingSizeFor` now rounds, tuple format only: the blob
      posting offset is unaligned by construction (`8 + len(key)`, a blob key
      having no tuple of its own), so rounding its total would rewrite on-disk
      bytes for no upstream property. `postingBounds` tolerates a tail of at most
      seven bytes — the array's location and length are both stated in `t_tid`,
      so the padding is inert — while still rejecting a full unexplained MAXALIGN
      unit (that is a TID the count does not admit to) and an array running past
      the declared size. Old unrounded postings keep parsing, so NO REINDEX is
      required.

      Guards: `TestPostingBoundsToleratesAlignmentPaddingOnly` and
      `TestPostingBlobFormatSizeStaysExact`
      (`internal/access/btree/pgposting_format_test.go`), plus
      `TestPostingTupleFormatLayoutAndRoundTrip`, which now pins the rounded
      total rather than the exact one.
    - **3b-3c — `_bt_truncate` suffix truncation (2026-08-10).** A separator is
      not a key: it is a BOUNDARY, and it only has to sit strictly above every
      key on the left page and at-or-below every key on the right one. Upstream
      keeps just the attributes that distinguish `lastleft` from `firstright`
      (`_bt_keep_natts` + `index_truncate_tuple`, nbtutils.c); goopg stored the
      whole right-hand key, because before the flip `item.key` was one opaque
      blob with no attribute boundary to cut at. `indexFormat.truncateSeparator`
      (`internal/access/btree/pgtruncate.go`) is that cut, and it is applied at
      every separator producer: the split path (`insertIntoBlock`) and both bulk
      loader levels (`buildLevel`, `buildLevelRaw`), leaf levels only — an
      internal separator is already a truncated pivot, which is why upstream
      copies it verbatim and why `truncateSeparator` returns a pivot operand
      unchanged.

      The split path additionally stopped re-deriving the parent's downlink key
      from `rightItems[0]`: upstream's `_bt_insert_parent` builds it from the
      LEFT page's high key, and once that key is truncated a parent stating the
      untruncated one routes descents to a boundary the level below no longer
      draws.

      The half that is a correctness fix rather than a size one is
      `_bt_truncate`'s second branch. When the split falls between two entries
      whose key attributes are all equal — a duplicate-heavy index, or a posting
      run chunked across pages — upstream keeps `lastleft`'s heap TID as the
      implicit final key attribute (`BT_PIVOT_HEAP_TID_ATTR`). goopg wrote a
      pivot with NO heap TID, which `ComparePGIndexTuples` reads as minus
      infinity there, so every left-page entry sharing that key value compared
      GREATER than its own page's high key and a descent walked right past the
      page holding it. Mutation-checked: with truncation disabled, a point
      descent for the first of 1500 duplicates returns the WRONG heap TID
      ({12,25} instead of {0,1}). It was unreachable before the flip (a blob key
      carries no TID, so the two sides compared equal), which is why the
      tiebreaker lands here rather than staying a deferral.

      `indexFormat.marshal` had to learn the flag too: it re-stamps every
      pivot's natts on the way to the page, and stamping without
      `BT_PIVOT_HEAP_TID_ATTR` would leave the trailing `ItemPointerData` on the
      tuple while telling every reader those six bytes are key data.

      NOT REINDEX-required: an untruncated separator is still a legal one, so
      pre-slice pages stay readable and only new splits write the shorter form.

      Guard: `internal/access/btree/pgtruncate_test.go` — blob-format
      byte-for-byte no-op; keep-natts at the first distinguishing attribute (on
      a THREE-column descriptor, since `FormPGIndexTuple` MAXALIGNs and a 2→1
      cut fits inside the same 8-byte block); the tiebreaker pivot's TID, its
      MAXALIGNed size and the leaf `key <= HighKey` invariant it restores, with
      the pre-slice separator kept in the test as the mutation reference; the
      marshal round trip; and the end-to-end 1500-duplicate tree where both the
      point descents and the prefix range scan must find every entry.
    - **3b-3d — `_bt_check_third_page` replaces `MaxHighKeyLen` (2026-08-10).**
      The last of 3b-3's deferrals, and the one that only became *correct* once
      3b-3c existed.

      goopg bounded the wrong object. `MaxHighKeyLen = 256` was a ceiling on the
      SEPARATOR, checked in the split path, and there was no bound on a leaf row
      at all — the opposite of upstream, which bounds the ROW at a third of a
      page (`BTMaxItemSize`, `_bt_check_third_page`) and lets the separator be
      whatever suffix truncation leaves. The consequence was not academic: an
      over-wide index row was admitted happily and the failure surfaced later,
      at the unrelated split that had to turn it into a high key, as a message
      about a key length no user ever wrote.

      `CheckPGBTThirdPage(leaf, size)` (`pgtuple.go`) is now that gate, run
      where upstream runs it — `BTree.Insert` (`_bt_doinsert`'s "1/3 of a page
      restriction"; goopg needs it at the one door `tryInsertNoSplit` and
      `insertIntoBlock` share, since it does not depend on which page wins) and
      both bulk-loader levels (`_bt_buildadd`). The split path's old
      `MaxHighKeyLen` test becomes the same check on the resulting pivot at the
      INTERNAL bound. That leaf/internal asymmetry is the whole reason upstream
      has two constants: a leaf tuple is charged `BTMaxItemSize` because
      3b-3c's `_bt_truncate` may append a tiebreaker heap TID to the separator
      derived from it, and the internal level is charged the 8-byte-looser
      `BTMaxItemSizeNoHeapTid` precisely so it can accept that grown pivot.
      Upstream says it by passing `needheaptidspace = isleaf`.

      The second half is `bulkHighKeyReserve` → an exact reserve. The bulk
      loader cannot know a page's high key while filling it — the separator is
      the first key of the NEXT page — so it withholds body space for it; the
      old constant withheld the worst case, `4 + 8 + MaxHighKeyLen = 268`
      bytes of every page. Upstream reserves only
      `MAXALIGN(sizeof(ItemPointerData))` and pays for the rest by MOVING the
      page's last tuple to the new page and overwriting its slot with the
      separator, which it must do because `_bt_buildadd` is a streaming writer
      that has not seen the next tuple yet. goopg's loader has the whole sorted
      run in hand and moves nothing, so it can do better than either: it forms
      the actual separator for the next boundary (`separatorAt(i+1)`) and
      reserves exactly its body. The separator is computed one iteration ahead
      and carried in `pendingSep`, so the check that holds the space and the
      flush that spends it are the same bytes rather than two independent
      estimates — the failure mode a re-derivation would reintroduce is a page
      that fills completely and then cannot hold its own high key.

      A fresh page can never reject its own first item, which is what keeps the
      loader from looping: the gate caps a data item at 2704 and a separator at
      2712, and 2708 + 2712 is well under the 8148 bytes a freshly initialised
      page has after its header, special area and reserved `P_HIKEY` line
      pointer.

      **Posting tuples are exempt from the gate in `buildLevelRaw`, and that is
      a recorded gap.** Upstream never has to check them because the writer that
      builds them bounds them first: `_bt_dedup_pass` caps a posting list at
      `dstate->maxpostingsize ≤ BTMaxItemSize` (`nbtdedup.c`) and starts a new
      list at the cap. goopg's `deduplicateToRawItems` has no such cap and hands
      the loader posting tuples of several thousand bytes; rejecting them here
      would break bulk index creation on duplicate-heavy columns instead of
      fixing anything. The fix belongs to the still-missing `_bt_dedup_pass` —
      ledger row.

      NOT REINDEX-required: nothing about the on-disk shape changes. Pages
      written before this slice are readable, and the only visible difference is
      that new bulk-loaded pages pack tighter.

      Guard: `internal/access/btree/pgthirdpage_test.go` — the leaf/internal
      boundary table (including the 8-byte band an internal page must accept and
      a leaf must not), the oversized row refused at `Insert` with upstream's
      "index row size … exceeds btree version 4 maximum" wording, and a
      variable-width bulk load asserting both directions of the reserve at once:
      every non-rightmost page still carries a parseable pivot high key (a
      reserve that under-shot would show up as a flush error or a missing
      separator), and at least one leaf is packed tighter than the retired
      268-byte constant ever allowed — the assertion that fails if a later
      cleanup quietly restores a worst-case constant.

- **S11.5 — `RM_BTREE` WAL.** PG-faithful `XLOG_BTREE_*` emission/replay per
  `nbtxlog.c`, so a PG standby can replay goopg index maintenance rather than
  only read a snapshot.
- **S11.6 — unblock #10 (LANDED 2026-08-10).** The slice this whole theme
  existed to make safe. `pg_class.relhasindex` for a user table was hardcoded
  `false`; PG's `get_relation_info` (plancat.c) will not call
  `RelationGetIndexList` without it and `ExecInitModifyTable` will not call
  `ExecOpenIndices` without it, so a real PG 18.3 on a goopg cluster planned
  only seq scans and — the damaging half — silently skipped index maintenance
  for its own post-failover INSERTs. It could not be flipped before S11.2b /
  S11.3 / S11.4 because the flag would only have converted that silent gap into
  `index "…" contains corrupted page at block 0` (blocker #12).

  **The flag is not `len(cat.IndexesOnTable(tbl)) > 0`.** After S11.4 the key
  format is a per-INDEX property with nothing on disk recording it: an index
  `buildPGIndexKeyDesc` describes stores real per-attribute PG datums, one it
  refuses (expression key, explicit opclass, non-bytewise collation, a type
  with no 3b-2a comparator, and `numeric`/`uuid` whose goopg heap image is not
  PG's) keeps goopg's order-preserving key BLOB. A blob tree is a structurally
  valid nbtree — PG pages, PG metapage, real `IndexTupleData` items, so
  `_bt_checkpage` accepts it — but its keys are ordered by goopg's encoding, not
  by the opclass, so PG would descend it with the wrong comparator: wrong rows,
  and inserts filed where goopg's own descent never looks. `relhasindex` is
  per-RELATION and `RelationGetIndexList` reads every `pg_index` row once it is
  set, so there is no way to expose only the describable ones. Hence
  `pgClassRelhasindex` (`internal/executor/pg18_user_catalog_rows.go`) is
  all-or-nothing: true only when the table has at least one index and EVERY
  index on it is descriptor-bearing. A mixed table keeps the pre-slice
  behaviour, which is recoverable (goopg maintains its own indexes) where a
  mis-ordered write is not.

  **The flag has to be re-derived on CREATE INDEX, in both directions.** goopg
  writes a table's `pg_class` heap row at CREATE TABLE, when no index exists, and
  `syncIndexToCatalogHeap` only ever wrote the INDEX's own rows — so the flag
  would have stayed false forever. `resyncTableClassHeapRowForIndexSet`
  (`internal/executor/operators_ddl.go`) is upstream's `index_create` →
  `index_update_stats` → `heap_inplace_update`, and it runs downward too:
  adding an undescribable index to a table that had the flag must take it back
  off. DROP INDEX deliberately does not touch it — `index_drop` does not either,
  a stale true is harmless (PG finds no `pg_index` rows), and it cannot become
  unsafe because true implies every index was describable, so removing one
  leaves the survivors describable.

  **Discovery: `pg_index.indcollation` was InvalidOid for every implicit
  collation.** `ResolveIndexColumnCollationOID` returned nonzero only for an
  explicit `COLLATE` clause. Upstream's `ComputeIndexAttrs`
  (src/backend/catalog/index.c) fills `collationIds[]` from the heap attribute's
  `attcollation`, and `_bt_mkscankey` hands that OID to the comparison function
  — so a zero on a collatable key makes PG fail *every* scan and *every* insert
  on the index with `42P22: could not determine which collation to use for
  string comparison`. This was invisible for as long as PG never opened a goopg
  index; the flip surfaced it immediately (the E2E's `s10_t_val_idx` is on a
  `text` column). `IndexKeyColumnCollationOID` now supplies the column's own
  collation (its per-column `COLLATE`, else `pg_type.typcollation`: 100 for
  text/varchar/bpchar, 950 for `name`, 0 for non-collatable), and the
  checkpointed-restart reload (`internal/initdb/open.go`) compares the decoded
  OID against that same value before calling it an explicit clause — upstream's
  rule in `pg_get_indexdef_worker`, without which a restart would invent a
  `COLLATE "default"` that `\d`/pg_dump then print.

  Gate: `TestE2E_PGStandbyFullCycle` now asserts three separate facts after the
  promotion, each of which failed independently on the way here — `relhasindex`
  is set on the promoted PG's `pg_class`; an `enable_seqscan=off` lookup finds
  the row PG itself inserted after promotion (so `ExecInsertIndexTuples` wrote
  into goopg's tree); and the same forced-index lookup finds a row goopg wrote
  before the failover (so PG's `_bt_compare` descent agrees with goopg's writer
  about key order). `pg_amcheck` over a goopg-written user btree was already a
  standing gate — `TestPort_PgAmcheckBtreeIndexCheck` and
  `TestPort_PgAmcheckAllTables` — and both still pass.

  Guards: `internal/executor/pgindex_relhasindex_test.go` (no index / describable
  / undescribable / mixed-takes-it-down / gate-off / index-relation-stays-false).

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
