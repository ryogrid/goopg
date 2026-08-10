# 0130-0012 — `RM_BTREE` WAL content parity (M0130-S11.5)

Status: **in progress** — S11.5a (`XLOG_BTREE_NEWROOT`) and S11.5b-1
(`XLOG_BTREE_SPLIT_R`) landed 2026-08-10.
Series: M0130 Theme D, after 0130-0011 (the on-disk format).

## The gap this closes

S11.1–S11.4 made goopg's B-tree **pages** byte-faithful to PostgreSQL 18.3.
A real PG standby can therefore READ a basebackup of a goopg index. It still
cannot **replay** goopg's index maintenance, and the reason is narrower than
"the records are goopg-native": the records already carry PG headers.

`internal/wal/rmgr_map.go` maps every btree RecordKind onto `RM_BTREE_ID` with
the right `nbtxlog.h` opcode, and `pg_assembled_emit.go` emits them through
`assembleXLogRecord`, so the *envelope* is PG's. What differed is the *content*.
Before this slice only `XLOG_BTREE_INSERT_LEAF` carried the struct upstream
declares; split, vacuum and newroot carried **no main data at all** and shipped
full-page images instead:

| record | header | body before S11.5 |
|---|---|---|
| `INSERT_LEAF` | PG | PG-identical (`xl_btree_insert` + block-0 tuple) |
| `SPLIT_L` | PG | FPI-only — no `xl_btree_split` |
| `VACUUM` | PG | FPI-only — no `ndeleted`/`nupdated` arrays |
| `NEWROOT` | PG | FPI-only — no `xl_btree_newroot` |
| `UNLINK_PAGE` | PG | goopg-native body (deliberate; see below) |

An FPI-only record is not merely "less faithful". It is **unreplayable by the
engine it is shaped for**. PostgreSQL dispatches on `xl_rmid`/`xl_info` and then
runs the rmgr's redo function unconditionally — the presence of a backup image
does not skip it. `btree_xlog_newroot` starts with

```c
xl_btree_newroot *xlrec = (xl_btree_newroot *) XLogRecGetData(record);
```

so a record with zero main data hands it whatever follows in the buffer. The
FPI form worked only because *goopg's own* replay had a default arm that
restored the images and never looked at the main data. It was a goopg-to-goopg
protocol wearing PG's header.

## S11.5a — `XLOG_BTREE_NEWROOT`

Upstream reference: `_bt_newroot` (nbtinsert.c:2556-2597) and
`btree_xlog_newroot` (nbtxlog.c:764-800).

### Record

```
main data  xl_btree_newroot{ rootblk uint32, level uint32 }        (8 bytes)
block 0    the new root, WILL_INIT
           block data = the item area in _bt_restore_page form     (level > 0)
block 1    the left child — redo clears its incomplete-split flag  (level > 0)
block 2    the metapage, WILL_INIT
           block data = xl_btree_metadata                          (28 bytes)
```

`EncodeBtreeNewRootPG` derives `level` and the item area from the root page it
is given, and the metadata from the metapage, so the caller cannot describe the
record inconsistently with the pages it is logging. The only new parameter is
`leftChildBlk`, which is not derivable from either page.

Three details are load-bearing:

- **Block 1 is mandatory when `level > 0`.** Upstream's redo calls
  `_bt_clear_incomplete_split(record, 1)` in that branch, and
  `XLogReadBufferForRedo` **PANICs** on an unregistered block id — it does not
  return "not found". Emitting a level > 0 newroot without a left child would
  therefore take down a PG standby, so the encoder refuses it. goopg's runtime
  clears the flag in a separate step (`clearIncompleteSplit`), which makes the
  redo a no-op on a goopg primary; it is applied anyway, because a PG standby
  replaying the same record WILL clear it and the two engines must not disagree
  about the flag at any LSN.

- **`xl_btree_metadata` is 28 bytes, padding included.** Six `uint32`s, a
  `bool`, and 3 bytes of tail padding to the struct's 4-byte alignment.
  `_bt_restore_meta` asserts the block-data length is exactly `sizeof`, so the
  padding is wire format, not an artefact. `btm_magic` and
  `btm_last_cleanup_num_heap_tuples` are **not** carried: redo re-asserts the
  magic and resets num_heap_tuples to `-1.0`.

- **The metapage is rebuilt, not read-modify-written.** `_bt_restore_meta`
  `_bt_pageinit`s the page and writes every field from the record, then advances
  `pd_lower` past the struct — without which a later full-page image would
  compress the metadata into the free-space hole and lose it. goopg's
  `ReplayMetaSetRoot` (read-modify-write) survives only for the goopg-native
  `RecordKindBtreeNewRoot`, which carries only `(root, level)` and so has to
  preserve the rest.

### `_bt_restore_page` and its producer

`internal/access/btree/pgnewroot.go` holds both halves of the block-0 payload,
in one file on purpose. The payload is an **untagged** run of MAXALIGNed
`IndexTupleData` images: no count, no offsets, no terminator. Its only framing
is each tuple's own `t_info` size with a MAXALIGN stride, so a disagreement
between producer and consumer is a silently mis-built page rather than a parse
error — the sibling-path rule (CLAUDE.md Hard-won Rule #2).

The payload is in **descending** offset-number order; upstream compensates by
adding the items to the page in reverse (`PageAddItem(..., nitems - i)`).
Upstream produces it by slicing the page's `[pd_upper, pd_special)` region and
relying on PageAddItem having allocated downward in increasing offset order.
`PGRestorePageData` builds the same bytes explicitly from the line pointers
instead. That is deliberate: goopg inserts index items at a *computed physical
offset* (`storage.PageInsertItemRawAt`), which shifts line pointers while
leaving the data area in allocation order, so the region and the offset order
are not guaranteed to agree on a page that was not built by pure appends. The
two forms are byte-identical on the pages this record actually logs.

Everything in `pgnewroot.go` is **format-free** — items move as raw bytes and
are never parsed as keys — for the same reason `ApplyInsertRecordAt` is:
recovery holds a relfilenode and has no catalog to resolve a `PGIndexKeyDesc`
from, so it cannot order a descriptor-ordered tree. Placing recorded bytes at
recorded offsets needs neither a parse nor a comparison.

### Replay

`replayDecodedXLogBtreeNewRoot` (recovery.go) applies the three limbs, each
separately idempotent via its own `pd_lsn`, so a replay interrupted between
blocks resumes correctly. Two small helpers name the distinction the block
flags encode:

- `replayInitedXLogBlock` — a WILL_INIT block. Prior contents are irrelevant, so
  the block need not exist; it is extended when the record is the first thing to
  touch it (PG's `XLogInitBufferForRedo`).
- `replayExistingXLogBlock` — a block mutated in place; the page must exist and
  be initialised, because the mutation reads it.

Each limb still takes the FPI branch first when a block carries an apply-image,
so a record written by a real PG (which will emit images under
`full_page_writes`) replays on goopg unchanged.

### goopg's second emit site

goopg logs a newroot from two places: `createNewRoot` (the split bubbling past
the root — upstream's `_bt_newroot`) and VACUUM's `resetToEmptyRoot`, which
installs an **empty level-0 leaf root** after a vacuum empties the tree.
Upstream has no `_bt_newroot` counterpart for the latter, but its *redo* handles
it with no special case: `level == 0` means `BTP_ROOT|BTP_LEAF`, no item
restore, and no block 1. That is why goopg can log it as a newroot at all, and
the encoder emits blocks 0 and 2 only.

## S11.5b-1 — `XLOG_BTREE_SPLIT_R`

Upstream reference: `_bt_split`'s XLOG block (nbtinsert.c:1966-2060) and
`btree_xlog_split` (nbtxlog.c:180-352).

### Record

```
main data: xl_btree_split{level, firstrightoff, newitemoff, postingoff}  (10 B)
block 0:   the left half — FULL-PAGE IMAGE
block 1:   the new right sibling, WILL_INIT, block data = its item area in
           `_bt_restore_page` order — NO image
block 2:   the page that was left's right sibling, registered with no data
           (non-rightmost split only)
```

Two of those three choices are forced by the redo function, and the third is
the slice's honest boundary.

**Block 1 must be content.** `btree_xlog_split` reconstructs the right page
from scratch on *every* replay — `XLogInitBufferForRedo`, `_bt_pageinit`, then
`_bt_restore_page` — and ignores the return code that would tell it an image
was restored. A record that carried only an image would have the image written
and then overwritten by an *empty* item area. This is the mirror image of the
`XLOG_BTREE_NEWROOT` trap from S11.5a: there the missing main data made the
record unreplayable, here it would be the missing block data.

The right page's **opaque header is not carried at all**; redo derives it from
`xlrec->level` and the record's own block tags (`btpo_prev` = block 0's tag,
`btpo_next` = block 2's tag or `P_NONE`, `btpo_flags` = `BTP_LEAF` or zero,
`btpo_cycleid` = 0). So the primary and the replayed page agree only if the
primary wrote exactly that header. `btree.SplitRightPageOpaque` is that single
definition, `splitPage` now stamps it, and `EncodeBtreeSplitPG` **refuses** a
right page that does not match — the divergence would otherwise be silent,
since every field involved is metadata rather than an item.

This tightened goopg's runtime slightly: the right sibling used to inherit the
split page's `BTP_HAS_GARBAGE`, which was stale-set from birth anyway (the
refill writes only live items — `pageItems` skips dead-marked ones). Upstream
`_bt_split` clears `BTP_ROOT|BTP_SPLIT_END|BTP_HAS_GARBAGE` for the same
reason; it keeps `btpo_cycleid` and may add `BTP_SPLIT_END`, so a *real* PG
primary and standby genuinely disagree on those two after replaying one split
record. Both are VACUUM hints `btree_mask` excludes from WAL consistency
checking. goopg has neither, so pinning the runtime to the redo rule makes the
two byte-identical.

**Block 0 as an image is upstream-legal, not a way around it.** PG logs the
left half incrementally (the new item, then the page's new high key), but its
redo reaches that path only under `BLK_NEEDS_REDO`; with a backup image the
left half takes `BLK_RESTORED` and the whole incremental rebuild — along with
`firstrightoff`, `newitemoff` and `postingoff` — is skipped. Upstream says so
itself, in the comment above its own `XLogRegisterBufData`: "If XLogInsert
decides that it can omit orignewitem due to logging a full-page image of the
left page, everything still works out."

### Why goopg cannot emit the incremental left half yet (S11.5b-2)

The incremental form describes exactly one transformation: *origpage's items
from `P_FIRSTDATAKEY` to `firstrightoff`, with `newitem` spliced in at
`newitemoff`*. goopg's split is not that. `splitPage` reads the whole page out,
appends the new item, runs a **dedup consolidation pass** over the merged list
(`dedupConsolidate`, M0055-0003 Phase B), and refills both halves — so the left
half can contain posting tuples that were never on the original page. Upstream
reaches the same state through two records: an `XLOG_BTREE_DEDUP` followed by a
plain split. Emitting the incremental form therefore needs either the split
point threaded down to the encoder *and* a proof that the dedup pass was a
no-op, or goopg's dedup unbundled into its own record. That is S11.5b-2; the
image keeps the record replayable in the meantime.

The offsets are still filled in coherently rather than zeroed. The record is
logged as `SPLIT_R` (new item on the right), `firstrightoff` is where the right
half begins in the split page's offset numbering — `P_FIRSTDATAKEY` is 2, since
the post-split left page links to the new right page and so is never rightmost
— `newitemoff` equals it, and `postingoff` is 0. A reader that ignores the
image and believes the main data gets a consistent story, just not the one the
primary executed.

### Replay

`replayDecodedXLogBtreeSplit` follows upstream's block order (right page first,
then left, then the sibling back-link), each limb idempotent on its own
`pd_lsn` via `replayInitedXLogBlock`/`replayExistingXLogBlock`. A block 0 with
**no** image is a record a real PG primary produced; goopg returns an explicit
"not implemented" rather than silently leaving the left half pre-split.

### Guards

- `internal/wal/btree_split_pg_test.go` — record shape (the 10-byte main data
  and each field, block 0 imaged, block 1 **not** imaged and WILL_INIT with the
  `_bt_restore_page` payload, block 2 carrying nothing), the rightmost variant
  with no block 2, three mutations of the right page's opaque each rejected by
  name, and a replay reproduction asserting the rebuilt right page matches the
  writer's items **at the same offsets** plus the same opaque, the restored left
  half, the swung back-link, and same-LSN idempotency.
- The obsolete `TestEncodeBtreeSplitPGFPIReplay`, which asserted the FPI form of
  block 1, is deleted — it pinned the property this slice removes.

## Guards — S11.5a

- `internal/wal/btree_newroot_pg_test.go` — record shape (main data, the three
  block ids, WILL_INIT placement, the 28-byte metadata) with an explicit **"no
  block carries a full-page image"** assertion, since that is the property the
  slice exists to establish; the missing-child refusal; a replay reproduction
  asserting the replayed root's items match the writer's **at the same offsets**
  plus the metapage, the cleared child flag and same-LSN idempotency; and the
  level-0 leaf-root variant.
- `internal/access/btree/pgnewroot_test.go` — the producer/consumer round trip
  over mixed-length keys (so at least one item sits off a MAXALIGN boundary),
  an explicit assertion that the payload starts with the **last** item, malformed
  runs rejected rather than truncated, the metapage rebuild, and the
  incomplete-split clear.
- Mutation-checked: emitting the item area in ascending order, and dropping the
  block-1 limb, each fail by name.

## Remaining in S11.5

- **S11.5b-2, the incremental left half** — block-0 data (the new item, then
  the page's new high key) instead of the image, which needs goopg's dedup pass
  unbundled from the split or proven to be a no-op. See "Why goopg cannot emit
  the incremental left half yet" above.
- **S11.5b-3, block 3 on an internal split** — upstream registers the child
  buffer whose incomplete-split flag the split completes, and redo calls
  `_bt_clear_incomplete_split(record, 3)` for every `level > 0` record.
  `XLogReadBufferForRedo` **PANICs** on an unregistered block id (the same trap
  S11.5a hit at block 1), so a real PG standby still cannot replay a goopg
  INTERNAL split. goopg clears the flag in a separate step (`clearIncompleteSplit`)
  and has no child block at the emit site today.
- **`XLOG_BTREE_VACUUM`** — `xl_btree_vacuum{ndeleted, nupdated}` plus the
  deleted/updated offset arrays.
- **`XLOG_BTREE_UNLINK_PAGE`** — the one with a *documented* reason to stay
  native (`wal-pg-identical-stream/IMPLEMENTATION-TODO.md`, A8-unlinkpage): an
  emit-time FPI can be stale against a concurrent split on another `*BTree`, and
  the PG-faithful form needs incremental link patches rather than a page
  snapshot.

Until all four land, `relhasindex` stays false in `buildUserPGClassRow`
(S11.6): a PG standby that saw goopg's indexes would try to replay these
records.
