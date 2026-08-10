# 0130-0012 — `RM_BTREE` WAL content parity (M0130-S11.5)

Status: **in progress** — S11.5a (`XLOG_BTREE_NEWROOT`) landed 2026-08-10.
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

## Guards

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

- **`XLOG_BTREE_SPLIT_L`/`_R`** — `xl_btree_split{level, firstrightoff,
  newitemoff, postingoff}`, the new item and the left page's new high key as
  block-0 data, and the right page's tuples as block-1 data in `_bt_restore_page`
  form (which this slice's helpers now provide). The largest remaining piece.
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
