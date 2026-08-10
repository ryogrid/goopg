# 0130-0012 — `RM_BTREE` WAL content parity (M0130-S11.5)

Status: **in progress** — S11.5a (`XLOG_BTREE_NEWROOT`), S11.5b-1
(`XLOG_BTREE_SPLIT_R`), S11.5c (`XLOG_BTREE_VACUUM`) and S11.5b-3 (the split
record's block 3) landed 2026-08-10.
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

## S11.5b-3 — the split record's block 3

Upstream reference: `_bt_split`'s XLOG section (nbtinsert.c:1957 and :1989) and
`btree_xlog_split`'s opening arm (nbtxlog.c:203).

An internal B-tree page is never inserted into for its own sake: the only thing
that ever lands on one is a separator pushed up by a split one level down. That
child is still flagged `BTP_INCOMPLETE_SPLIT` at the moment the parent gains its
downlink, and upstream clears the flag **in the same critical section** as the
parent's split — `cpageop->btpo_flags &= ~BTP_INCOMPLETE_SPLIT` — then registers
the child as backup block 3 under `if (!isleaf)`. Its redo does the mirror
image, and does it *first*, before it locks anything else, with the comment that
REDO never needs to couple cross-level locks.

goopg had neither half. `splitPage` handed the flag clear to the caller, which
ran `clearIncompleteSplit` — a separate page record — after the parent insert
returned, and the split record named no child at all. The consequence is not a
lost hint: `XLogReadBufferForRedo` **PANICs** on an unregistered block id rather
than reporting it, so `btree_xlog_split`'s unconditional
`_bt_clear_incomplete_split(record, 3)` at `level > 0` takes a real standby down
on the first goopg internal split. This is the same trap S11.5a hit at block 1
of the newroot record, and it is why "the record is merely less detailed" is
never a safe reading of a missing block.

The fix follows upstream on both sides rather than only on the record:

- `insertIntoBlock` carries `childBlk` — upstream's `cbuf` — threaded from the
  three places a separator is pushed up (`splitPage`'s recursion, `finishSplit`,
  and `createNewRoot`'s lost-the-race fallback). It is `InvalidBlockNumber` for
  a leaf tuple, which is the only other way into that function.
- The split path pins the child while it still holds this level's latches
  (the **descent** direction, so it cannot deadlock against a reader), clears the
  flag, includes it in the record and stamps the record's LSN onto it.
- `clearIncompleteSplit` now returns without writing when the flag is already
  clear, so the caller's later call is a no-op after a cascading parent split
  and still does the work after a no-split parent insert.
- `EncodeBtreeSplitPG` refuses **both** violations of upstream's `!isleaf`
  condition: a `level > 0` record with no child, and a leaf record carrying one
  (a block PG's redo never reads). The block carries no data — the mutation is
  re-derived from the page.

### Guards — S11.5b-3

- `internal/wal/btree_split_pg_test.go` — block 3 present, correct, image-free
  and data-free on an internal record; both level-gate refusals; a replay
  reproduction asserting the child comes out unflagged at the record's LSN.
- `internal/access/btree/btree_test.go` — the writer side: 4000 wide-key inserts
  drive a real internal split, the hook asserts `childBlk` is valid exactly when
  the split page is internal, and every child named is verified unflagged on the
  page. An internal split failing to occur is a test **failure**, not a skip —
  otherwise the assertion would pass vacuously on a two-level tree.

## S11.5c — `XLOG_BTREE_VACUUM`

Upstream reference: `_bt_delitems_vacuum` (nbtpage.c:1250-1310) and
`btree_xlog_vacuum` (nbtxlog.c:479-528).

This record is the one FPI-only case that was **not** outright unreplayable.
`btree_xlog_vacuum` dereferences `xlrec` only inside its `BLK_NEEDS_REDO` arm,
which an applied image skips, so a real standby would survive it. It still lied
to everything that reads the record without replaying it — starting with
`pg_waldump`, whose `btree_desc` prints `ndeleted`/`nupdated` straight off the
end of a zero-length main-data area. Both forms goopg now emits carry the
struct:

| | main data | block 0 |
|---|---|---|
| incremental | `xl_btree_vacuum{ndeleted, 0}` | the deleted offset numbers (`uint16` each, ascending), **no image** |
| image | `xl_btree_vacuum{0, 0}` | full-page image, `BKPIMAGE_APPLY` |

The image form is upstream-legal for the same reason block 0 of the split record
is: redo takes `BLK_RESTORED` and never reaches the deletion.

### Why there are two forms, and who decides

goopg's VACUUM does not delete items in place. `VacuumIndexPages` parses the
page into an item list, filters it, and refills the page (`resetPageItems` +
re-marshal). That coincides with `PageIndexMultiDelete` — and is therefore
describable by offset numbers — only sometimes:

- **Posting lists break it.** `pageItemsWithDead` EXPANDS a posting tuple into
  one entry per TID, and the survivors are re-marshalled individually, so a
  posting tuple that lost one TID (or none) comes back as several ordinary
  tuples. That is a change of the page's item *count*, which offset numbers
  cannot express. Upstream instead rewrites the tuple in place and describes it
  with `xl_btree_update` — the `nupdated` half of the record. goopg never emits
  `nupdated > 0`, and its replay refuses a record that does rather than applying
  the deletions and silently leaving dead TIDs behind.
- **The page that went empty breaks it.** VACUUM additionally stamps
  `BTDeleted|BTHalfDead` (phase 1 of page deletion), and no `btree_xlog_vacuum`
  sets those flags.
- **The dedup-recovery rewrite is not a deletion at all.** `_bt_insertonpg`'s
  fallback path (`btree.go`, "dedup-recovery") reuses this record for a
  CONSOLIDATION; there are no deleted offsets to name.

Rather than enumerate those cases at the emit site and hope the list stays
complete, the decision is made by asking the two pages: `btree.CheckVacuumDelete`
replays the offsets against the pre-vacuum page and compares the result — items,
high key, and opaque — with the page VACUUM actually wrote. Mismatch ⇒ image.
This is the same stance as S11.5b's right-page opaque check, with a fallback
instead of a refusal: a record that describes a rewrite the primary did not
perform must never ship, but VACUUM must not fail either.

`ReplayVacuumDelete` (`internal/access/btree/pgvacuum.go`) is upstream's
`PageIndexMultiDelete` plus the unconditional hint clear (`btpo_cycleid = 0`,
`btpo_flags &= ~BTP_HAS_GARBAGE`; goopg has no cycle id). One documented
difference: it rebuilds the item area rather than compacting line pointers in
place, so a SURVIVING item that carried an `LP_DEAD` mark comes back unmarked.
That bit is an unlogged hint and goopg's VACUUM deletes every marked item
anyway, so the loss can cost a later re-check, never correctness.

## Guards — S11.5c

- `internal/wal/btree_vacuum_pg_test.go` — the incremental record's shape
  (`SizeOfBtreeVacuum` main data, `ndeleted`, `nupdated = 0`, the offset array as
  block data, an explicit **"block 0 carries no image"** assertion and a size
  bound, since shrinking the record is half the point); a replay reproduction
  against the pre-vacuum page on disk with the garbage hint cleared and
  same-LSN idempotency; and the fallback — a written page the offsets do not
  reproduce comes out as an image with `ndeleted = 0`.
- `internal/access/btree/pgvacuum_test.go` — the deletion itself (survivors keep
  their order and slide down, high key untouched), the offset-array validation
  (descending, duplicate, naming the high key, out of range), and
  `CheckVacuumDelete` accepting the exact result while rejecting a different
  item set, a differing opaque, and the page-went-empty flag stamp.
- `internal/access/btree/btree_vacuum_wal_test.go` — the end-to-end half no
  encoder test can see: the capture hook runs `CheckVacuumDelete` on the offsets
  `VacuumIndexPages` itself computes, and the test fails if NO emission named
  any (which would mean the incremental form is dead code behind a silent
  fallback).

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

## S11.5d-1 — `XLOG_BTREE_MARK_PAGE_HALFDEAD`

Page deletion is two records in upstream, and goopg's single native
`RecordKindBtreeUnlinkPage` covers the union of both. This slice lands the
phase-1 record's PG form; phase 2 (`XLOG_BTREE_UNLINK_PAGE`) is S11.5d-2 and the
emit-side rewiring of `unlinkEmptyLeaf` is S11.5d-3.

### The trap, one degree worse than S11.5a's

goopg *did* already emit an `XLOG_BTREE_MARK_PAGE_HALFDEAD`-tagged record
(`EncodeBtreeMarkPageHalfDead`, RecordKind 25): 16 bytes of `{relfilenode,
leafblk, flagsAfter}`, **no registered blocks at all**, under a header
announcing `RM_BTREE` / `0xB0`. Upstream's `btree_xlog_mark_page_halfdead` does
not merely misread that main data — it calls `XLogInitBufferForRedo(record, 0)`
unconditionally, and an unregistered block id is a PANIC inside
`XLogReadBufferForRedoExtended`, not a bad page. The same shape as the S11.5b-3
finding: the missing block is fatal, not lossy.

(The record also has zero live emit sites today — the `LogBtreeMarkPageHalfDead`
hook is wired all the way from `initdb/open.go` through `storage.Pool` and never
called, because VACUUM bundles the half-dead transition into the vacuum record's
opaque-flags trailer. S11.5d-3 is what gives it a caller.)

### Record

Upstream `_bt_mark_page_halfdead` (nbtpage.c), redo at nbtxlog.c:762-848:

| part | contents |
|---|---|
| main data | `xl_btree_mark_page_halfdead{poffset, leafblk, leftblk, rightblk, topparent}` — 20 bytes: a `uint16` then four `uint32`, so **2 bytes of C alignment padding after `poffset` are part of the wire format** |
| block 0 | the leaf, `WILL_INIT`, **no block data** |
| block 1 | the to-be-deleted subtree's parent |

`leftblk`/`rightblk` are read off the leaf page inside `EncodeBtreeMarkPageHalfDeadPG`
rather than accepted from the caller — the same rule S11.5a applied to `level`:
the record must not be able to describe a page differently from the page it is
logging. `poffset` is a **physical** `OffsetNumber`, not a data-slot index;
`P_FIRSTDATAKEY` is 2 on a parent with a high key and 1 on a rightmost one, and
the standby cannot re-derive which.

### Two things redo defines that goopg's page model did not have

**The half-dead page is defined by its contents.** Upstream recreates block 0
from scratch: empty item area plus one dummy high key of exactly
`SizeOfIndexTupleData` bytes whose `t_tid` block half is the *top parent* of the
subtree being deleted. That tuple is not decoration — `_bt_unlink_halfdead_page`
reads `BTreeTupleGetTopParent` off it to find the next page down to unlink, so
phase 2 is impossible without it. `ReplayMarkHalfDeadLeaf` builds it with
`PGBTPivotRaw(nil, topparent)`, which is byte-identical to upstream's
`trunctuple` (`BTreeTupleSetTopParent` and `BTreeTupleSetDownLink` write the
same field under two names). `topparent` is `InvalidBlockNumber` when the leaf is
itself the top of the subtree — the ordinary case for goopg's single-page
deletions — and `EncodeBtreeMarkPageHalfDeadPG` **refuses** a `topparent` that
literally names the leaf, because that record would make phase 2 descend into
the page it is deleting.

**The parent mutation is a retarget, not a delete.** Upstream points `poffset`'s
downlink at the *right neighbour's* child and deletes the neighbour's item,
keeping `poffset`'s own key. goopg's `ReplayRemoveParentDownlink` deletes
`poffset` outright. Both are self-consistent for an empty page, but they absorb
the deleted subtree's key range in **opposite directions** — rightward
(upstream, matching the direction the sibling chain was relinked) versus
leftward (goopg) — and so produce different parent pages from the same input.
`ReplayHalfDeadParent` is the upstream one, kept separate from goopg's rather
than replacing it: the primary still performs goopg's mutation, so switching
`unlinkEmptyLeaf` to the PG record means switching its parent mutation too.
That coupling is exactly what makes it S11.5d-3 rather than part of this slice,
and it is on the ledger.

### Replay

`replayDecodedXLogBtreeMarkPageHalfDead` applies block 1 first and block 0
second, upstream's order (it does the parent first precisely so it can drop the
internal page's lock without coupling it across levels). Each limb is
independently gated on its own `pd_lsn`, so a replay interrupted between the two
resumes correctly, and each falls back to a full-page image when a real-PG
record carries one.

### Guards — S11.5d-1

`internal/wal/btree_halfdead_pg_test.go` pins the 20-byte main data field by
field *including the alignment padding*, asserts block 0 is `WILL_INIT` with
zero data bytes and block 1 is neither, and fails if any block carries an image.
`TestEncodeBtreeMarkPageHalfDeadPGRefusesUndescribableRecords` covers the three
inputs whose record upstream could not replay as written (no parent block,
`poffset` 0, `topparent` = the leaf). `TestApplyRecordReplaysPGBtreeMarkPageHalfDead`
drives emit → encode → decode → `ApplyRecord` and checks the rebuilt half-dead
leaf and the retargeted parent, then re-applies at the same LSN.
`internal/access/btree/replay_halfdead_test.go` runs the parent mutation on
*both* page shapes (`P_FIRSTDATAKEY` 2 and 1) so the physical-offset handling
cannot regress into a data-slot index, and proves the rebuild discards live
items.

## S11.5d-2 — `XLOG_BTREE_UNLINK_PAGE`

Phase 2, the other half of the pair S11.5d-1 opened. Same trap in the same
shape: goopg's single native `RecordKindBtreeUnlinkPage` covers the union of
both phases and is framed under this record's `RM_BTREE`/`0x80` header, so a
real standby casts a 41-byte native payload to `xl_btree_unlink_page` and then
PANICs in `XLogInitBufferForRedo(record, 0)` on a block that was never
registered.

### Record

`EncodeBtreeUnlinkPagePG` (`internal/wal/pg_assembled_emit.go`) emits the
36-byte `xl_btree_unlink_page{leftsib, rightsib, level, safexid, leafleftsib,
leafrightsib, leaftopparent}`. Two layout details are wire format, not
artefacts: the 4 bytes of padding before `safexid` (a `FullTransactionId` is a
`uint64`, so the struct is 8-byte aligned and upstream casts rather than
parses), and the *absence* of the struct's own trailing padding —
`SizeOfBtreeUnlinkPage` is `offsetof(leaftopparent) + sizeof(BlockNumber)`, so
the record is 36 bytes and not 40.

Blocks: 0 the target `WILL_INIT` with no data, 1 the left sibling **only when
there is one**, 2 the right sibling unconditionally, 3 the half-dead leaf
`WILL_INIT` **only when the target is an internal page**, 4 the metapage on the
`_META` variant (`0x90`), carrying the same 28-byte `xl_btree_metadata` S11.5a
introduced.

Every structural field is read off a page rather than accepted from the caller —
the discipline S11.5a and S11.5d-1 established, that a record must not be able
to describe a page differently from the page it logs. `leftsib`/`rightsib`/
`level` come from the target's opaque (the unlink preserves all three, so the
pre- and post-mutation images agree); `leafleftsib`/`leafrightsib` from the
leaf's; and `leaftopparent` from **the leaf's dummy high key** — precisely the
tuple S11.5d-1 discovered a half-dead page is defined by. `_bt_unlink_halfdead_page`
writes the next child down into it and the next invocation reads it back out, so
phase 2 both consumes and reproduces phase 1's one-item page. That is what makes
the two records compose over a subtree of arbitrary depth, and it is why block 3
is `WILL_INIT` with no data: the leaf is *rebuilt*, by the same
`ReplayMarkHalfDeadLeaf` phase 1's block 0 uses.

The encoder refuses a **rightmost target**. This is structural, not defensive:
upstream's redo reads block 2 without testing `rightsib`, so "no right sibling"
has no representation in the record at all — and correspondingly `_bt_pagedel`
never deletes a rightmost page.

### The deleted page is also defined by its contents

`ReplayUnlinkTargetPage` (`internal/access/btree/pgpagedel.go`) is upstream's
`_bt_pageinit` + `BTPageSetDeleted`. As with the half-dead page, the rewrite is
not an optimisation over patching: a deleted page has **no line pointers**, its
`pd_lower` covers exactly one `BTDeletedPageData`, and its `pd_upper` is closed
up against `pd_special` so nothing can ever be added. Everything a reader may
still ask of it lives in the opaque and in that one 8-byte payload — which
siblings it used to sit between (`_bt_walk_left` follows the links of *deleted*
pages), what level it was on, and the `safexid` from which the block becomes
RECYCLABLE. None of it needs a full-page image and all of it is in the record.

`BTP_HAS_FULLXID` is the reason the sibling link fixes get their own helpers
(`ReplayUnlinkLeftSibling` / `ReplayUnlinkRightSibling`) instead of reusing
`ReplaySetSiblingNext` / `ReplaySetSiblingPrev`: those round-trip the opaque
through goopg's legacy `BT*` flag word, and `legacyFlags` has no counterpart for
`BTP_HAS_FULLXID` (or `BTP_META`), so it silently drops the bit. Dropping it on
a page that carries a `BTDeletedPageData` turns the `safexid` into garbage for
`BTPageIsRecyclable`. Upstream's link fix is one field write, so the new helpers
do exactly one field write. `ReplaySetSiblingNext`'s extra high-key handling is
also unnecessary here for a reason worth stating: `rightsib` is never `P_NONE`
in a legal record, so the left sibling stays non-rightmost and the separator it
already carries still bounds the same key range — the deleted page held no keys.

### Replay

`replayDecodedXLogBtreeUnlinkPage` applies block 1, then 0, then 2, then 3, then
4 — upstream's order, which is its **lock** order (left to right), preserved
because a torn replay must leave the sibling chain traversable in the direction
a live reader walks it. Each limb is independently gated on its own `pd_lsn` and
falls back to a full-page image when a real-PG record carries one.

### Guards — S11.5d-2

`internal/wal/btree_unlinkpage_pg_test.go` pins the 36-byte main data field by
field including both alignment holes, checks the leaf-target case registers
exactly blocks 0/1/2 (and that `leafleftsib`/`leafrightsib` are the target's own
links with `leaftopparent` invalid, which is what upstream writes), checks the
internal-target + metapage case adds blocks 3/4 with the `_META` opcode and
takes the leaf fields from the *leaf*, and fails if any block carries an image.
`TestEncodeBtreeUnlinkPagePGRefusesUndescribableRecords` covers six inputs
upstream's redo could not replay as written, the rightmost target among them.
`TestApplyRecordReplaysPGBtreeUnlinkPage` drives emit → encode → decode →
`ApplyRecord` over a five-block relation and checks all four limbs.
`internal/access/btree/replay_pagedel_test.go` pins the deleted page's shape
against `BTPageSetDeleted` (flags, header, safexid, byte-identical on re-replay)
and proves the sibling helpers are flag-lossless — including a case that
documents the legacy helper *is* the lossy one, so a later loop cannot fold them
back together.

## Remaining in S11.5

- **S11.5b-2, the incremental left half** — block-0 data (the new item, then
  the page's new high key) instead of the image, which needs goopg's dedup pass
  unbundled from the split or proven to be a no-op. See "Why goopg cannot emit
  the incremental left half yet" above.
- **The `XLOG_BTREE_INSERT_UPPER` child block** — the *other* half of upstream's
  incomplete-split clear. When the parent insert does **not** split, upstream
  logs the child as block 1 of an `XLOG_BTREE_INSERT_UPPER` record and clears
  the flag there. goopg still routes that insertion through its leaf-insert
  record and leaves the clear to a separate `clearIncompleteSplit` page record,
  so the split half is faithful (S11.5b-3 above) and the no-split half is not.
- **The `nupdated` half of `XLOG_BTREE_VACUUM`** — `xl_btree_update`, upstream's
  in-place rewrite of a posting tuple that lost a subset of its TIDs. goopg
  re-marshals the survivors as separate tuples instead, so a page with posting
  lists still falls back to a full-page image (S11.5c above).
- **Rewiring the emit sites (S11.5d-3)** — `unlinkEmptyLeaf` /
  `unlinkEmptyInternalPage` emit one native record covering the union of both
  phases, and they emit it **before** applying, then deliberately re-derive each
  sibling link under the write pin (the AI-20260709-010336-082 fix) because a
  split on another connection's `*BTree` can splice a page in between. That
  makes the record's own link values advisory, which no PG-shaped record may be.
  Upstream avoids it by holding all three page locks across the insert; goopg
  must move to the same protocol (pin left/target/right, compute, emit, write)
  and adopt upstream's retarget-and-delete parent mutation at the same time.
  This is the "documented reason to stay native"
  (`wal-pg-identical-stream/IMPLEMENTATION-TODO.md`, A8-unlinkpage) restated in
  terms of what actually blocks it.

Until those land, `relhasindex` stays false in `buildUserPGClassRow`
(S11.6): a PG standby that saw goopg's indexes would try to replay these
records.
