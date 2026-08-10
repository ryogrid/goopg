# 0130-0011 — nbtree PG-identical on-disk format

**Milestone:** M0130 (Cluster-directory compat with PG 18.3 + PG physical replication)
**Status:** draft (S11.1 + S11.2 + S11.3 + S11.4 slices 1-2-3a-3b-1-3b-2a-3b-2b-3b-2c-i-3b-2c-ii-A-3b-2c-ii-B1-3b-2c-ii-B2-a landed 2026-08-10; S11.4 slices 3b-2c-ii-B2-b/3b-2c-ii-B2-c/3b-3, S11.5, S11.6 not started)
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
       legacy layout's so split points do not move.

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
          - **3b-2c-ii-B2-b — the format-resolution sites (open). No on-disk
            change.** Teach the `blobFormat` sites — amcheck's four exported
            page readers and the two redo entry points — to resolve a per-index
            format, which is behaviour-preserving while every index still
            resolves to blob.
          - **3b-2c-ii-B2-c — the flip (open). REINDEX-required.**
            `encodeCompositeBTreeKey` / `encodeIndexKeyFromCols` /
            `encodeArbiterKey` → `pgIndexTupleKey` under the same
            `Context.pgIndexKeyDesc` the tree took, search keys included;
            `pgIndexTupleKeys` on; and the explicit dual-format decision for the
            indexes the resolver refuses. Gates: `scripts/tpch-spotcheck.sh` and
            the TPC-DS SF0.5 gate (re-pin after a REINDEX).
    - **3b-3 — collect the deferrals (open).** With the key length
      descriptor-derived, restore `index_form_tuple`'s MAXALIGN of the tuple
      size and `BTreeTupleSetPosting`'s MAXALIGNed posting offset, implement
      `_bt_keep_natts` suffix truncation (pivot natts < nkeyatts at last), and
      retire `MaxHighKeyLen` / `bulkHighKeyReserve` in favour of
      `BTMaxItemSize`.
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
