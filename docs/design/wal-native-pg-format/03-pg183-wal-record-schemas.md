# 03 — PG 18.3 WAL record schemas

| Field  | Value                                                        |
| ------ | ------------------------------------------------------------ |
| Status | draft — **the parity target**; agent-reviewed field-by-field vs `postgres/` (1 blocker + 4 citation nits found and fixed, 2026-07-15) |
| Date   | 2026-07-15                                                   |
| Oracle | PostgreSQL 18.3, tree under [`postgres/`](../../../postgres/) |
| Notation | [WAL-KS](02-wal-schema-dsl-spec.md)                        |

This is the **target byte layout** goopg's native WAL encoder must produce, one
schema per currently-emitted record kind ([doc 01](01-emitted-wal-record-inventory.md)
section A + the generic framing + the `pgoutput` network messages). Every field
cites `postgres/src/...:line`. Each record ends with a **native vs PG delta**
note stating what goopg must change.

Records with no PG WAL analog (doc 01 section B — goopg-private catalog DDL) are
out of scope here; PG journals those as heap-tuple ops on `pg_catalog`.

---

## 0. Preliminaries

### 0.1 Base type → WAL-KS (verified widths)

| PG C type | WAL-KS | bytes | source |
| --- | --- | --- | --- |
| `XLogRecPtr` | `u8` | 8 | `access/xlogdefs.h:21` |
| `TransactionId`, `Oid`, `RelFileNumber`, `MultiXactId`, `MultiXactOffset`, `CommandId`, `BlockNumber`, `TimeLineID`, `pg_crc32c` | `u4` | 4 | `c.h`, `postgres_ext.h:32`, `storage/block.h:31`, `xlogdefs.h:60`, `port/pg_crc32c.h:38` |
| `FullTransactionId` (`struct { uint64 value; }`) | `u8` | 8 | `access/transam.h:65` |
| `OffsetNumber` | `u2` | 2 | `storage/off.h:24` |
| `RmgrId` | `u1` | 1 | `access/rmgr.h:11` |
| `TimestampTz`, `pg_time_t` | `s8` | 8 | `datatype/timestamp.h:39`, `pgtime.h:23` |
| `int` | `s4` | 4 | — |
| `bool` | `bool` | 1 | — |
| `RelFileLocator { Oid spcOid; Oid dbOid; RelFileNumber relNumber; }` | 3×`u4` | 12 | `storage/relfilelocator.h:58` |

### 0.2 Alignment rule (critical)

Only the fixed `XLogRecord` header starts on a `MAXALIGN` (8-byte) boundary in
the WAL file; **everything after it is packed unaligned on the wire**
(`xlogrecord.h:33-35, 100-101`). Main-data and block-data structs are `memcpy`'d
from their in-memory (LP64) form, so:

- **Internal** struct padding **is present** on the wire (e.g. the pad before
  `ntuples` in `xl_heap_multi_insert`, before `wal_level`/`time` in
  `CheckPoint`).
- **Trailing** padding is dropped when PG copies a struct by its `SizeOf*` /
  `MinSizeOf*` macro (which is `offsetof`-based).
- Some sub-records are deliberately stored **unaligned** (noted per record):
  `snapshot_conflict_horizon` after `xl_heap_prune.flags`, and `xl_xact_origin`.

Offsets in the field tables below are within the *struct*, computed for LP64.

### 0.3 Checksum

`xl_crc` is **CRC32C (Castagnoli)** over the record bytes *after* the fixed
header, then over the first 20 bytes of the header (with `xl_crc` zeroed) —
`COMP_CRC32C(crc, hdr + SizeOfXLogRecord, len - SizeOfXLogRecord)` then the
header, per `xloginsert.c` `XLogRecordAssemble` (`:904`). Not IEEE.

---

## 1. Generic framing

### 1.1 `XLogRecord` — fixed 24-byte header
`access/xlogrecord.h:41-55`. `endian: le`.

| id | WAL-KS | width | off | PG C type | note |
| --- | --- | --- | --- | --- | --- |
| `xl_tot_len` | `u4` | 4 | 0 | `uint32` | total record length incl. this header, **excl.** MAXALIGN tail pad |
| `xl_xid` | `u4` | 4 | 4 | `TransactionId` | |
| `xl_prev` | `u8` | 8 | 8 | `XLogRecPtr` | 0-based byte offset of the previous record |
| `xl_info` | `u1` | 1 | 16 | `uint8` | high 4 bits = opcode, low 4 = `XLR_*` |
| `xl_rmid` | `u1` | 1 | 17 | `RmgrId` | resource manager (§3) |
| — | `pad2` | 2 | 18 | — | **must be zero** |
| `xl_crc` | `u4` | 4 | 20 | `pg_crc32c` | CRC32C (§0.3) |

`SizeOfXLogRecord = 24` (`:55`). `xl_info` masks: `XLR_INFO_MASK 0x0F`,
`XLR_RMGR_INFO_MASK 0xF0` (`:62-63`); caller bits `XLR_SPECIAL_REL_UPDATE 0x01`
(`:82`), `XLR_CHECK_CONSISTENCY 0x02` (`:91`). On disk the whole record is
zero-padded to `MAXALIGN(8)`; the pad is **not** counted in `xl_tot_len`.

### 1.2 Block-reference sub-headers
`access/xlogrecord.h`.

**`XLogRecordBlockHeader`** (`:103-115`, `SizeOf = 4`):

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `id` | `u1` | 1 | 0 | block reference id (0..`XLR_MAX_BLOCK_ID 32`) |
| `fork_flags` | `u1` | 1 | 1 | fork number in low 4 bits + `BKPBLOCK_*` flags |
| `data_length` | `u2` | 2 | 2 | rmgr block-data byte count (excl. page image) |

`@flags fork_flags` (`:196-202`): `BKPBLOCK_FORK_MASK 0x0F`,
`BKPBLOCK_HAS_IMAGE 0x10`, `BKPBLOCK_HAS_DATA 0x20`, `BKPBLOCK_WILL_INIT 0x40`,
`BKPBLOCK_SAME_REL 0x80`.

**`XLogRecordBlockImageHeader`** (`:141-154`, `SizeOf = 5`; present iff
`BKPBLOCK_HAS_IMAGE`):

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `length` | `u2` | 2 | 0 | page-image bytes present (BLCKSZ − hole − compression) |
| `hole_offset` | `u2` | 2 | 2 | bytes before the hole |
| `bimg_info` | `u1` | 1 | 4 | `BKPIMAGE_*` flags |

`@flags bimg_info` (`:157-167`): `BKPIMAGE_HAS_HOLE 0x01`, `BKPIMAGE_APPLY 0x02`,
`BKPIMAGE_COMPRESS_PGLZ 0x04`, `BKPIMAGE_COMPRESS_LZ4 0x08`,
`BKPIMAGE_COMPRESS_ZSTD 0x10`; `BKPIMAGE_COMPRESSED` = any compress bit.

**`XLogRecordBlockCompressHeader`** (`:173-179`, `SizeOf = 2`; present iff
`BKPIMAGE_HAS_HOLE && BKPIMAGE_COMPRESSED`): `hole_length` `u2`.

**Main-data headers** (`:213-227`): `XLogRecordDataHeaderShort` =
`id=255 (XLR_BLOCK_ID_DATA_SHORT)` + `data_length u1` (`SizeOf 2`) when
len < 256; else `XLogRecordDataHeaderLong` = `id=254 (XLR_BLOCK_ID_DATA_LONG)` +
`data_length u4` **unaligned** (`SizeOf 5`). Reserved ids (`:241-246`):
`XLR_BLOCK_ID_ORIGIN 253`, `XLR_BLOCK_ID_TOPLEVEL_XID 252`.

### 1.3 Full record assembly order
`xlogrecord.h:20-40` (doc) + `backend/access/transam/xloginsert.c`
`XLogRecordAssemble` (`:566-889`):

```ksy
layout:                                   # endian: le
  - XLogRecord                            # 24 B fixed header, MAXALIGN'd
  - per block_id ascending:               # xloginsert.c:589-838
      - XLogRecordBlockHeader             #   4 B (:818)
      - '@if HAS_IMAGE': XLogRecordBlockImageHeader   # 5 B (:822)
      - '@if HAS_IMAGE && HAS_HOLE && COMPRESSED': XLogRecordBlockCompressHeader  # 2 B (:824)
      - '@if !SAME_REL': RelFileLocator   #   12 B (:831)
      - block_number: u4                  #   4 B (:836)
  - '@if origin':      { id: u1=253, RepOriginId: u2 }        # :844
  - '@if toplevel_xid':{ id: u1=252, TransactionId: u4 }      # :857
  - main_data_hdr: XLogRecordDataHeader{Short|Long}           # :862
  - '@region payload':                    # rdata chain order (:709-806, 886)
      - block_images[]                    #   each HAS_IMAGE block's FPI bytes (hole-split)
      - block_data[]                      #   each HAS_DATA block's rmgr data
      - main_data                         #   the main-data struct bytes
```

CRC (§0.3) covers the payload region + the header region, not the fixed
header's own first bytes until folded in last (`:904`).

---

## 2. Heap (`RM_HEAP_ID` = 10) and Heap2 (`RM_HEAP2_ID` = 9)

Opcodes in `xl_info & 0x70`; `XLOG_HEAP_INIT_PAGE 0x80` may be OR'd in.

**HEAP opcodes** (`access/heapam_xlog.h:33-47`): `XLOG_HEAP_INSERT 0x00`,
`_DELETE 0x10`, `_UPDATE 0x20`, `_TRUNCATE 0x30`, `_HOT_UPDATE 0x40`,
`_CONFIRM 0x50`, `_LOCK 0x60`, `_INPLACE 0x70`; `XLOG_HEAP_OPMASK 0x70`.
**HEAP2 opcodes** (`:59-66`): `XLOG_HEAP2_REWRITE 0x00`,
`_PRUNE_ON_ACCESS 0x10`, `_PRUNE_VACUUM_SCAN 0x20`, `_PRUNE_VACUUM_CLEANUP 0x30`,
`_VISIBLE 0x40`, `_MULTI_INSERT 0x50`, `_LOCK_UPDATED 0x60`, `_NEW_CID 0x70`.

Supporting sub-struct **`xl_heap_header`** (`:150-157`, `SizeOf 5`):
`t_infomask2 u2` @0, `t_infomask u2` @2, `t_hoff u1` @4 — precedes tuple data in
block 0's payload for insert/update/multi-insert.

### 2.1 `XLOG_HEAP_INSERT 0x00` — `xl_heap_insert`
`heapam_xlog.h:160-168`, `SizeOfHeapInsert = 3`. **Main data:**

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `offnum` | `u2` | 2 | 0 | inserted tuple offset |
| `flags` | `u1` | 1 | 2 | `XLH_INSERT_*` |

Block 0 (HAS_DATA): `xl_heap_header` + tuple data. `@flags flags` (`:72-79`):
`ALL_VISIBLE_CLEARED 1<<0`, `LAST_IN_MULTI 1<<1`, `IS_SPECULATIVE 1<<2`,
`CONTAINS_NEW_TUPLE 1<<3`, `ON_TOAST_RELATION 1<<4`, `ALL_FROZEN_SET 1<<5`.
> **native delta** (`RecordKindHeapInsert`): goopg's canonical form emits
> `offnum+flags` with an FPI and no `xl_heap_header`+tuple in block-0 data; PG
> stores the tuple in block 0. Native must emit block-0 `HAS_DATA`
> (`xl_heap_header` + tuple), set `CONTAINS_NEW_TUPLE`, and only add an FPI on
> first page touch, not per record.

### 2.2 `XLOG_HEAP_DELETE 0x10` — `xl_heap_delete`
`heapam_xlog.h:113-121`, `SizeOfHeapDelete = 8`.

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `xmax` | `u4` | 4 | 0 | xmax of deleted tuple |
| `offnum` | `u2` | 2 | 4 | |
| `infobits_set` | `u1` | 1 | 6 | `XLHL_*` (see §2.5) |
| `flags` | `u1` | 1 | 7 | `XLH_DELETE_*` |

`@flags flags` (`:102-106`): `ALL_VISIBLE_CLEARED 1<<0`,
`CONTAINS_OLD_TUPLE 1<<1`, `CONTAINS_OLD_KEY 1<<2`, `IS_SUPER 1<<3`,
`IS_PARTITION_MOVE 1<<4`.
> **native delta** (`RecordKindHeapDelete`): goopg sets `infobits_set=0`,
> `flags=0`; PG sets `infobits_set` from the tuple's infomask (note goopg's
> DELETE omits `HEAP_KEYS_UPDATED` today) and the visibility/old-key flags.
> Native must populate both.

### 2.3 `XLOG_HEAP_UPDATE 0x20` / `XLOG_HEAP_HOT_UPDATE 0x40` — `xl_heap_update`
`heapam_xlog.h:218-233`, `SizeOfHeapUpdate = 14`.

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `old_xmax` | `u4` | 4 | 0 | |
| `old_offnum` | `u2` | 2 | 4 | |
| `old_infobits_set` | `u1` | 1 | 6 | `XLHL_*` |
| `flags` | `u1` | 1 | 7 | `XLH_UPDATE_*` |
| `new_xmax` | `u4` | 4 | 8 | |
| `new_offnum` | `u2` | 2 | 12 | |

Block 0 = new page (new tuple: optional prefix/suffix `u2`, then
`xl_heap_header` + new tuple data); block 1 = old page (reference only, if
different). `@flags flags` (`:85-92`): `OLD_ALL_VISIBLE_CLEARED 1<<0`,
`NEW_ALL_VISIBLE_CLEARED 1<<1`, `CONTAINS_OLD_TUPLE 1<<2`,
`CONTAINS_OLD_KEY 1<<3`, `CONTAINS_NEW_TUPLE 1<<4`, `PREFIX_FROM_OLD 1<<5`,
`SUFFIX_FROM_OLD 1<<6`.
> **native delta** (`RecordKindHeapHotUpdate`): goopg emits a bespoke
> hot-update body and (for non-HOT) a Delete+Insert pair or an INPLACE link; PG
> uses one `xl_heap_update` with two block refs and `HOT_UPDATE` opcode. Native
> must adopt this two-block layout and the prefix/suffix compression flags, and
> route real non-HOT updates through `XLOG_HEAP_UPDATE` rather than
> Delete+Insert.

### 2.4 `XLOG_HEAP2_MULTI_INSERT 0x50` — `xl_heap_multi_insert`
`heapam_xlog.h:181-199`, `SizeOfHeapMultiInsert = offsetof(offsets) = 4`.

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `flags` | `u1` | 1 | 0 | `XLH_INSERT_*` |
| — | `pad1` | 1 | 1 | internal pad (present on wire) |
| `ntuples` | `u2` | 2 | 2 | |
| `offsets` | `u2` | var | 4 | `@varlen: ntuples`; omitted if `XLOG_HEAP_INIT_PAGE` |

Block 0 payload: per tuple `xl_multi_insert_tuple` (`:190-199`, `SizeOf 7`:
`datalen u2`@0, `t_infomask2 u2`@2, `t_infomask u2`@4, `t_hoff u1`@6) + tuple
data, aligned per tuple.
> **native delta:** goopg does not emit multi-insert (decode-only,
> `RecordKindHeapMultiInsert` 28). Parity is only needed if goopg begins batching
> inserts (e.g. COPY).

### 2.5 `XLOG_HEAP_LOCK 0x60` — `xl_heap_lock`
`heapam_xlog.h:396-404`, `SizeOfHeapLock = 8`.

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `xmax` | `u4` | 4 | 0 | may be a MultiXactId |
| `offnum` | `u2` | 2 | 4 | |
| `infobits_set` | `u1` | 1 | 6 | `XLHL_*` |
| `flags` | `u1` | 1 | 7 | `XLH_LOCK_ALL_FROZEN_CLEARED 0x01` |

`@flags infobits_set` (`:386-390`): `XLHL_XMAX_IS_MULTI 0x01`,
`XLHL_XMAX_LOCK_ONLY 0x02`, `XLHL_XMAX_EXCL_LOCK 0x04`,
`XLHL_XMAX_KEYSHR_LOCK 0x08`, `XLHL_KEYS_UPDATED 0x10`.
(`xl_heap_lock_updated`, `:407-415`, is byte-identical, `SizeOf 8`, opcode
`XLOG_HEAP2_LOCK_UPDATED 0x60`.)
> **native delta** (`RecordKindHeapLock`): map goopg lock-strength to
> `infobits_set` bits exactly.

### 2.6 `XLOG_HEAP_INPLACE 0x70` — `xl_heap_inplace`
`heapam_xlog.h:426-436`, `MinSizeOfHeapInplace = offsetof(nmsgs)+4 = 20`.
**Note: PG 18 added the inval fields.**

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `offnum` | `u2` | 2 | 0 | |
| — | `pad2` | 2 | 2 | |
| `dbId` | `u4` | 4 | 4 | `Oid` |
| `tsId` | `u4` | 4 | 8 | `Oid` |
| `relcacheInitFileInval` | `bool` | 1 | 12 | |
| — | `pad3` | 3 | 13 | |
| `nmsgs` | `s4` | 4 | 16 | |
| `msgs` | `SharedInvalidationMessage` | var | 20 | `@varlen: nmsgs` |

Block 0 (HAS_DATA): the new tuple bytes.
> **native delta** (`RecordKindHeapHotUpdate` chain link / datfrozenxid path):
> goopg's canonical inplace emits only `offnum`; PG requires
> `dbId/tsId/relcacheInitFileInval/nmsgs` + inval messages. Native must add
> them.

### 2.7 `XLOG_HEAP2_PRUNE_ON_ACCESS 0x10` / `_PRUNE_VACUUM_SCAN 0x20` / `_PRUNE_VACUUM_CLEANUP 0x30` — `xl_heap_prune`
`heapam_xlog.h:240-337`, `SizeOfHeapPrune = 2`. **This record is composite** —
the main data is tiny; the substance is sub-records in block 0's data.

**Main data:**

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `reason` | `u1` | 1 | 0 | debug/analysis only |
| `flags` | `u1` | 1 | 1 | `XLHP_*` |
| `snapshot_conflict_horizon` | `u4` | 4 | 2 | `@if flags & XLHP_HAS_CONFLICT_HORIZON`, **unaligned** |

`@flags flags` (`:299-333`): `XLHP_IS_CATALOG_REL 1<<1`,
`XLHP_CLEANUP_LOCK 1<<2`, `XLHP_HAS_CONFLICT_HORIZON 1<<3`,
`XLHP_HAS_FREEZE_PLANS 1<<4`, `XLHP_HAS_REDIRECTIONS 1<<5`,
`XLHP_HAS_DEAD_ITEMS 1<<6`, `XLHP_HAS_NOW_UNUSED_ITEMS 1<<7`.

**Block 0 data sub-records, in flag order** (`:245-370`):
```ksy
'@if XLHP_HAS_FREEZE_PLANS':
  xlhp_freeze_plans: { nplans: u2, pad2, plans: xlhp_freeze_plan[nplans] }
    # xlhp_freeze_plan: xmax u4, t_infomask2 u2, t_infomask u2, frzflags u1, ntuples u2
'@if XLHP_HAS_REDIRECTIONS':    xlhp_prune_items { ntargets: u2, data: u2[2*ntargets] }
'@if XLHP_HAS_DEAD_ITEMS':      xlhp_prune_items { ntargets: u2, data: u2[ntargets] }
'@if XLHP_HAS_NOW_UNUSED_ITEMS':xlhp_prune_items { ntargets: u2, data: u2[ntargets] }
frz_offsets: u2[sum(plan.ntuples)]      # trailing, after all sub-records
```
Freeze-plan flags (`:339-340`): `XLH_FREEZE_XVAC 0x02`, `XLH_INVALID_XVAC 0x04`.
Freeze plans come first because they need 4-byte alignment; all other
sub-records need only 2-byte.
> **native delta** (`RecordKindHeapPruneOpt` 14, `RecordKindHeapVacuum` 7,
> `RecordKindHeapFreeze` 26): goopg emits three separate bespoke records; PG 18
> unifies them into one `xl_heap_prune` with `XLHP_*` sub-records
> (`XLOG_HEAP2_FREEZE_PAGE` was removed in PG17). This is the **largest heap
> divergence** — native must fold prune/vacuum/freeze into the composite
> `xl_heap_prune` layout, choosing the opcode by reason.

### 2.8 `XLOG_HEAP2_VISIBLE 0x40` — `xl_heap_visible`
`heapam_xlog.h:440+` (`xl_heap_visible`: `snapshotConflictHorizon u4`,
`flags u1`; block 0 = VM buffer, block 1 = heap). Decode-only in goopg
(`RecordKindHeapVisible` 29) — parity deferred until goopg maintains a real
visibility map.

---

## 3. Btree (`RM_BTREE_ID` = 11)

Opcodes (`access/nbtxlog.h:27-44`): `INSERT_LEAF 0x00`, `INSERT_UPPER 0x10`,
`INSERT_META 0x20`, `SPLIT_L 0x30`, `SPLIT_R 0x40`, `INSERT_POST 0x50`,
`DEDUP 0x60`, `DELETE 0x70`, `UNLINK_PAGE 0x80`, `UNLINK_PAGE_META 0x90`,
`NEWROOT 0xA0`, `MARK_PAGE_HALFDEAD 0xB0`, `VACUUM 0xC0`, `REUSE_PAGE 0xD0`,
`META_CLEANUP 0xE0`.

Supporting **`xl_btree_metadata`** (`:49-58`): `version u4`, `root u4`,
`level u4`, `fastroot u4`, `fastlevel u4`, `last_cleanup_num_delpages u4`,
`allequalimage bool` — trails INSERT_META / UNLINK_PAGE_META / META_CLEANUP /
NEWROOT-with-meta records.

### 3.1 `XLOG_BTREE_INSERT_LEAF 0x00` — `xl_btree_insert`
`nbtxlog.h:79-87`, `SizeOfBtreeInsert = 2`: `offnum u2` @0. Payload: posting
split offset (`INSERT_POST` only) then the new index tuple.
> **native delta** (`RecordKindBtreeInsert` 5): goopg canonical form is FPI-only
> with `offnum`; PG stores the new tuple as block-0 data. Native must carry the
> tuple, not rely on a full-page image.

### 3.2 `XLOG_BTREE_SPLIT_L 0x30` / `_R 0x40` — `xl_btree_split`
`nbtxlog.h:153-161`, `SizeOfBtreeSplit = 10`.

| id | WAL-KS | width | off |
| --- | --- | --- | --- |
| `level` | `u4` | 4 | 0 |
| `firstrightoff` | `u2` | 2 | 4 |
| `newitemoff` | `u2` | 2 | 6 |
| `postingoff` | `u2` | 2 | 8 |

Blocks: 0 = orig/left, 1 = new right (all right-half items as data), 2 = next
(rightlink), 3 = child's left sibling (non-leaf).
> **native delta** (`RecordKindBtreeSplit` 3): match the 4-block layout and the
> right-half item payload.

### 3.3 `XLOG_BTREE_DEDUP 0x60` — `xl_btree_dedup`
`nbtxlog.h:170-177`, `SizeOfBtreeDedup = 2`: `nintervals u2` @0, then
`BTDedupInterval[]`. goopg does not emit dedup as a record (dedup is applied
in-place); parity only if it starts WAL-logging dedup passes.

### 3.4 `XLOG_BTREE_VACUUM 0xC0` — `xl_btree_vacuum`
`nbtxlog.h:223-237`, `SizeOfBtreeVacuum = 4`: `ndeleted u2`@0, `nupdated u2`@2.
Block 0 payload: deleted offsets, updated offsets, then `xl_btree_update` items
(`:264-271`: `ndeletedtids u2` + posting offsets).
> **native delta** (`RecordKindBtreeVacuum` 22): adopt `ndeleted/nupdated` +
> the block-0 offset/update payload.

### 3.5 `XLOG_BTREE_DELETE 0x70` — `xl_btree_delete`
`nbtxlog.h:239-256`, `SizeOfBtreeDelete = 9`.

| id | WAL-KS | width | off |
| --- | --- | --- | --- |
| `snapshotConflictHorizon` | `u4` | 4 | 0 |
| `ndeleted` | `u2` | 2 | 4 |
| `nupdated` | `u2` | 2 | 6 |
| `isCatalogRel` | `bool` | 1 | 8 |

Same block-0 payload shape as vacuum. (goopg does not currently emit btree
DELETE as a distinct record.)

### 3.6 `XLOG_BTREE_NEWROOT 0xA0` — `xl_btree_newroot`
`nbtxlog.h:344-350`, `SizeOfBtreeNewroot = 8`: `rootblk u4`@0, `level u4`@4.
Block 0 = new root (2 tuples if splitting old root), block 1 = left child,
block 2 = metapage.
> **native delta** (`RecordKindBtreeNewRoot` 24): match `rootblk/level` + the
> 3-block layout; no separate `xl_btree_metadata` needed (rootblk+level suffice).

### 3.7 `XLOG_BTREE_UNLINK_PAGE 0x80` / `_META 0x90` — `xl_btree_unlink_page`
`nbtxlog.h`, `SizeOfBtreeUnlinkPage = offsetof(leaftopparent)+4 = 36`.

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `leftsib` | `u4` | 4 | 0 | |
| `rightsib` | `u4` | 4 | 4 | |
| `level` | `u4` | 4 | 8 | |
| — | `pad4` | 4 | 12 | before 8-aligned `safexid` |
| `safexid` | `u8` | 8 | 16 | `FullTransactionId` (align 8) |
| `leafleftsib` | `u4` | 4 | 24 | |
| `leafrightsib` | `u4` | 4 | 28 | |
| `leaftopparent` | `u4` | 4 | 32 | |

`xl_btree_metadata` follows for `_META`. Blocks 0–4 per the header comment
(target, left sib, right sib, leaf, metapage).
> **native delta** (`RecordKindBtreeUnlinkPage` 23): adopt the full **36-byte**
> struct (goopg's native body is smaller) incl. the 4-byte pad at offset 12 and
> the 8-byte-aligned `FullTransactionId safexid` at offset 16 — an encoder that
> omits the pad emits a 32-byte body that PG misparses.

### 3.8 `XLOG_BTREE_MARK_PAGE_HALFDEAD 0xB0` — `xl_btree_mark_page_halfdead`
`nbtxlog.h`, `SizeOfBtreeMarkPageHalfDead = offsetof(topparent)+4 = 20`.

| id | WAL-KS | width | off |
| --- | --- | --- | --- |
| `poffset` | `u2` | 2 | 0 |
| — | `pad2` | 2 | 2 | (before `leafblk` u4) |
| `leafblk` | `u4` | 4 | 4 |
| `leftblk` | `u4` | 4 | 8 |
| `rightblk` | `u4` | 4 | 12 |
| `topparent` | `u4` | 4 | 16 |

Blocks: 0 = leaf, 1 = top parent.
> **native delta** (`RecordKindBtreeMarkPageHalfDead` 25): match the 5-field
> struct incl. the internal pad after `poffset`.

---

## 4. Transaction (`RM_XACT_ID` = 1)

Opcodes (`access/xact.h:169-179`): `XLOG_XACT_COMMIT 0x00`, `PREPARE 0x10`,
`ABORT 0x20`, `COMMIT_PREPARED 0x30`, `ABORT_PREPARED 0x40`, `ASSIGNMENT 0x50`,
`INVALIDATIONS 0x60`; `XLOG_XACT_OPMASK 0x70`; `XLOG_XACT_HAS_INFO 0x80`
(signals the `xinfo` chunk is present).

`@flags xinfo` (`xact.h:188-196`): `HAS_DBINFO 1<<0`, `HAS_SUBXACTS 1<<1`,
`HAS_RELFILELOCATORS 1<<2`, `HAS_INVALS 1<<3`, `HAS_TWOPHASE 1<<4`,
`HAS_ORIGIN 1<<5`, `HAS_AE_LOCKS 1<<6`, `HAS_GID 1<<7`, `HAS_DROPPED_STATS 1<<8`.
Completion bits (`:206-208`): `APPLY_FEEDBACK 1<<29`,
`UPDATE_RELCACHE_FILE 1<<30`, `FORCE_SYNC_COMMIT 1<<31`.

### 4.1 `XLOG_XACT_COMMIT 0x00` — `xl_xact_commit`
`xact.h:320-334`, `MinSizeOfXactCommit = 8`. **Fixed part:** `xact_time s8` @0.
Then, if `XLOG_XACT_HAS_INFO`, the chunk stream in this exact order (each chunk
is int32-aligned, `:237-239`):

```ksy
xl_xact_commit:
  - xact_time: s8
  - '@if HAS_INFO':            xl_xact_xinfo         { xinfo: u4 }               # :244
  - '@if HAS_DBINFO':          xl_xact_dbinfo        { dbId: u4, tsId: u4 }      # :255
  - '@if HAS_SUBXACTS':        xl_xact_subxacts      { nsubxacts: s4, subxacts: u4[nsubxacts] }        # :261
  - '@if HAS_RELFILELOCATORS': xl_xact_relfilelocators { nrels: s4, xlocators: RelFileLocator[nrels] } # :268
  - '@if HAS_DROPPED_STATS':   xl_xact_stats_items   { nitems: s4, items: xl_xact_stats_item[nitems] } # :295
  - '@if HAS_INVALS':          xl_xact_invals        { nmsgs: s4, msgs: SharedInvalidationMessage[nmsgs] } # :302
  - '@if HAS_TWOPHASE':        xl_xact_twophase      { xid: u4 }                 # :309
  - '@if HAS_GID':             gid: strz
  - '@if HAS_ORIGIN':          xl_xact_origin        { origin_lsn: u8, origin_timestamp: s8 }  # :314, UNALIGNED
```
`xl_xact_stats_item` (`:282`): `kind s4`, `dboid u4`, `objid_lo u4`,
`objid_hi u4`.
> **native delta** (`RecordKindXactCommit` 8, `RecordKindXactCommitInval` 32):
> goopg's canonical commit emits only `xact_time` (no `HAS_INFO`); the
> CommitInval variant needs the `HAS_INVALS` chunk. Native must set
> `XLOG_XACT_HAS_INFO` + `xinfo` and append the relevant chunks (at minimum
> `HAS_INVALS` for inval-carrying commits; `HAS_SUBXACTS` when sub-xids exist).

### 4.2 `XLOG_XACT_ABORT 0x20` — `xl_xact_abort`
`xact.h:336-350`, `MinSizeOfXactAbort = 8`. Same layout as commit **minus the
invalidations chunk** (`:340-348`): `xact_time` then xinfo / dbinfo / subxacts /
relfilelocators / stats / twophase / gid / origin.
> **native delta** (`RecordKindXactAbort` 9): same as commit — set `HAS_INFO` +
> the chunks goopg actually needs (subxacts for aborted subtrees).

---

## 5. XLOG rmgr (`RM_XLOG_ID` = 0)

Info codes (`catalog/pg_control.h:67-82`): `CHECKPOINT_SHUTDOWN 0x00`,
`CHECKPOINT_ONLINE 0x10`, `NOOP 0x20`, `NEXTOID 0x30`, `SWITCH 0x40`,
`BACKUP_END 0x50`, `PARAMETER_CHANGE 0x60`, `RESTORE_POINT 0x70`,
`FPW_CHANGE 0x80`, `END_OF_RECOVERY 0x90`, `FPI_FOR_HINT 0xA0`, `FPI 0xB0`,
`OVERWRITE_CONTRECORD 0xD0`, `CHECKPOINT_REDO 0xE0`.

### 5.1 `XLOG_CHECKPOINT_SHUTDOWN 0x00` / `_ONLINE 0x10` — `CheckPoint`
`catalog/pg_control.h:35-65`. Struct `sizeof = 88` (last field @84 → pad to 8).

| id | WAL-KS | width | off | note |
| --- | --- | --- | --- | --- |
| `redo` | `u8` | 8 | 0 | REDO start LSN |
| `ThisTimeLineID` | `u4` | 4 | 8 | |
| `PrevTimeLineID` | `u4` | 4 | 12 | |
| `fullPageWrites` | `bool` | 1 | 16 | |
| — | `pad3` | 3 | 17 | (before `wal_level`) |
| `wal_level` | `s4` | 4 | 20 | |
| `nextXid` | `u8` | 8 | 24 | `FullTransactionId` (forces 8-align pad @17) |
| `nextOid` | `u4` | 4 | 32 | |
| `nextMulti` | `u4` | 4 | 36 | |
| `nextMultiOffset` | `u4` | 4 | 40 | |
| `oldestXid` | `u4` | 4 | 44 | |
| `oldestXidDB` | `u4` | 4 | 48 | |
| `oldestMulti` | `u4` | 4 | 52 | |
| `oldestMultiDB` | `u4` | 4 | 56 | |
| — | `pad4` | 4 | 60 | (before `time`) |
| `time` | `s8` | 8 | 64 | `pg_time_t` |
| `oldestCommitTsXid` | `u4` | 4 | 72 | |
| `newestCommitTsXid` | `u4` | 4 | 76 | |
| `oldestActiveXid` | `u4` | 4 | 80 | `Invalid` for shutdown ckpt |

> **native delta** (`RecordKindCheckpoint` 2): goopg already emits an 88-byte
> `CheckPoint` via `EncodeCheckpointCompat` and dispatches it by **payload
> length** (`classifyXLogRecord`, 88 → `XLOG_CHECKPOINT_SHUTDOWN`). Native
> should tag the opcode explicitly (shutdown vs online) rather than by length,
> and populate `oldestActiveXid` for online checkpoints.

### 5.2 `XLOG_NEXTOID 0x30`
Main data: a bare `Oid` (`u4`) — the next free OID. (Emitted by PG on OID
wraparound; goopg would emit when it advances the OID counter durably.)

### 5.3 `XLOG_SWITCH 0x40`, `XLOG_NOOP 0x20`
No main-data struct. `SWITCH` forces the rest of the segment to be skipped;
`NOOP` is a pad record. goopg's segment pad (`segment_pad.go`) already emits a
genuine `RM_XLOG` / `XLOG_NOOP` record.
> **native delta:** the pad is already PG-shaped; verify `xl_info = 0x20` and
> `xl_rmid = 0`.

### 5.4 `XLOG_FPI 0xB0` / `XLOG_FPI_FOR_HINT 0xA0`
No main-data struct; the payload is a full-page image in block 0 (§1.2 image
header + hole handling).
> **native delta** (`RecordKindPageImage` 1): emit as `RM_XLOG` / `XLOG_FPI`
> with a proper `XLogRecordBlockImageHeader` (currently goopg writes full-page
> images with `hole_offset=0`, no hole removal — see the frame note below).

### 5.5 `XLOG_PARAMETER_CHANGE 0x60` — `xl_parameter_change`
`access/xlog_internal.h:273-283`. Struct `sizeof = 28` (2 trailing pad bytes).

| id | WAL-KS | width | off |
| --- | --- | --- | --- |
| `MaxConnections` | `s4` | 4 | 0 |
| `max_worker_processes` | `s4` | 4 | 4 |
| `max_wal_senders` | `s4` | 4 | 8 |
| `max_prepared_xacts` | `s4` | 4 | 12 |
| `max_locks_per_xact` | `s4` | 4 | 16 |
| `wal_level` | `s4` | 4 | 20 |
| `wal_log_hints` | `bool` | 1 | 24 |
| `track_commit_timestamp` | `bool` | 1 | 25 |

> **native delta:** goopg already emits this in canonical form
> (`EncodeParameterChange`); confirm the field order/widths match exactly and
> it is tagged `RM_XLOG` / `0x60`.

---

## 6. CLOG (`RM_CLOG_ID` = 3) and SMGR (`RM_SMGR_ID` = 2)

### 6.1 `CLOG_TRUNCATE 0x10` — `xl_clog_truncate`
`access/clog.h:32-37` (`sizeof = 16`); info codes `CLOG_ZEROPAGE 0x00`,
`CLOG_TRUNCATE 0x10` (`:55-56`).

| id | WAL-KS | width | off |
| --- | --- | --- | --- |
| `pageno` | `s8` | 8 | 0 |
| `oldestXact` | `u4` | 4 | 8 |
| `oldestXactDb` | `u4` | 4 | 12 |

`CLOG_ZEROPAGE` carries just the page number as main data.
> **native delta** (`RecordKindClogTruncate` 33): re-tag as `RM_CLOG` /
> `CLOG_TRUNCATE` with the 16-byte struct (note `pageno` is `int64`, not `int`).

### 6.2 `XLOG_SMGR_CREATE 0x10` — `xl_smgr_create`
`catalog/storage_xlog.h:34-38` (`sizeof = 16`): `rlocator RelFileLocator`
(12) @0, `forkNum s4` (`ForkNumber`) @12.
> **native delta** (`RecordKindSmgrCreate` 11): emit `RM_SMGR` / `0x10` with the
> `RelFileLocator`+`forkNum` body (goopg's native `EncodeSmgrCreate` uses a
> goopg `rel` descriptor — map to the PG `RelFileLocator`).

`XLOG_SMGR_TRUNCATE 0x20` — `xl_smgr_truncate` (`:46-51`): `blkno u4`@0,
`rlocator RelFileLocator`@4, `flags s4`@16 (`SMGR_TRUNCATE_HEAP 0x1 /
VM 0x2 / FSM 0x4`). Decode-only in goopg (`RecordKindSmgrTruncate` 12).

---

## 7. Standby (`RM_STANDBY_ID` = 8) — for completeness

Info codes (`storage/standbydefs.h:34-36`): `XLOG_STANDBY_LOCK 0x00`,
`XLOG_RUNNING_XACTS 0x10`, `XLOG_INVALIDATIONS 0x20`.

**`xl_running_xacts`** (`:47-57`):

| id | WAL-KS | width | off |
| --- | --- | --- | --- |
| `xcnt` | `s4` | 4 | 0 |
| `subxcnt` | `s4` | 4 | 4 |
| `subxid_overflow` | `bool` | 1 | 8 |
| — | `pad3` | 3 | 9 |
| `nextXid` | `u4` | 4 | 12 |
| `oldestRunningXid` | `u4` | 4 | 16 |
| `latestCompletedXid` | `u4` | 4 | 20 |
| `xids` | `u4` | var | 24 | `@varlen: xcnt + subxcnt` |

goopg **decodes** but does not emit this (`recovery.go:9257`). A PG standby
expects a `RUNNING_XACTS` after an online checkpoint to enter hot standby, so
this is a **parity gap to close if goopg is to drive hot-standby startup** —
listed here as the target layout for that future work.

---

## 8. pgoutput logical-replication messages (network transfer)

`internal/wal/pgoutput.go` (encoder), pgoutput **protocol v1**, `endian: be`
(the message **body**; the walsender wraps each in a `CopyData`/`'w'` frame).
Message-type byte first, then:

| msg | byte | body |
| --- | --- | --- |
| Begin | `'B'` | `final_lsn u8`, `commit_time s8` (PG-epoch µs), `xid u4` |
| Commit | `'C'` | `flags u1` (0), `commit_lsn u8`, `end_lsn u8`, `commit_time s8` |
| Relation | `'R'` | `rel_oid u4`, `schema strz`, `name strz`, `replident u1`, `natts u2`, then per col: `flags u1`, `name strz`, `type_oid u4`, `atttypmod s4` |
| Insert | `'I'` | `rel_oid u4`, `'N'`, `TupleData` |
| Delete | `'D'` | `rel_oid u4`, (`'O'`+`TupleData` old image \| `'K'`+`TupleData` key) |
| Update | `'U'` | `rel_oid u4`, opt (`'O'`\|`'K'` + `TupleData`), `'N'`, `TupleData` |
| Truncate | `'T'` | `nrelids u4`, `option_bits u1` (`CASCADE 0x1`, `RESTART_SEQS 0x2`), `relids u4[nrelids]` |

**`TupleData`**: `natts u2`, then per column a status byte — `'n'` null /
`'t'` text (+ `len u4` + bytes) / `'u'` unchanged-toast.

> **native delta (network):** current limitations vs PG pgoutput —
> - no `Type` (`'Y'`), `Origin`, streaming (`'S'`/`'E'`/`'A'`) or two-phase
>   messages;
> - `Relation.replident` hard-coded `'d'` (DEFAULT) and every column flagged as
>   replica-identity; PG reflects the real `REPLICA IDENTITY` (d/n/f/i);
> - `atttypmod` always `-1`;
> - `commit_time` uses wall-clock, not the real xact commit timestamp;
> - values always text (`'t'`), never binary (`'b'`).
> Native pgoutput must emit true replica identity, real typmod/commit-time, and
> (when negotiated) `Type`/binary/streaming to be a faithful v1+ publisher.

---

## 9. Consolidated native → PG delta summary

| goopg native record | PG 18.3 target (RMGR/op) | key changes |
| --- | --- | --- |
| HeapInsert (4) | HEAP/`0x00` | carry `xl_heap_header`+tuple in block-0 data; FPI only on first touch |
| HeapDelete (6) | HEAP/`0x10` | populate `infobits_set` + `XLH_DELETE_*` flags |
| HeapHotUpdate (13) / (non-HOT) | HEAP/`0x40` (`0x20`) | single `xl_heap_update`, 2 block refs, prefix/suffix flags; stop Delete+Insert for non-HOT |
| HeapPruneOpt (14)/HeapVacuum (7)/HeapFreeze (26) | HEAP2/`0x10`,`0x20`,`0x30` | fold all three into composite `xl_heap_prune` + `XLHP_*` sub-records |
| HeapLock (10) | HEAP/`0x60` | map lock strength → `infobits_set` |
| SmgrCreate (11) | SMGR/`0x10` | `RelFileLocator`+`forkNum` body |
| BtreeInsert (5) | BTREE/`0x00` | carry the new tuple as block-0 data |
| BtreeSplit (3) | BTREE/`0x30`/`0x40` | 4-block layout + right-half payload |
| BtreeVacuum (22) | BTREE/`0xC0` | `ndeleted/nupdated` + offset/update payload |
| BtreeNewRoot (24) | BTREE/`0xA0` | `rootblk/level` + 3-block layout |
| BtreeUnlinkPage (23) | BTREE/`0x80`/`0x90` | full 36-byte struct incl. pad@12 + 8-aligned `FullTransactionId safexid`@16 |
| BtreeMarkPageHalfDead (25) | BTREE/`0xB0` | 5-field struct + internal pad |
| XactCommit (8)/CommitInval (32) | XACT/`0x00` | set `HAS_INFO`+`xinfo`; append inval/subxact chunks |
| XactAbort (9) | XACT/`0x20` | set `HAS_INFO`+`xinfo`; append subxact chunks |
| Checkpoint (2) | XLOG/`0x00`/`0x10` | tag opcode explicitly (not by length); fill `oldestActiveXid` for online |
| PageImage (1) | XLOG/`0xB0` (`FPI`) | proper `XLogRecordBlockImageHeader` + hole removal |
| ClogTruncate (33) | CLOG/`0x10` | 16-byte `xl_clog_truncate` (`pageno` is `int64`) |
| ParameterChange | XLOG/`0x60` | already canonical — confirm field order |
| segment pad | XLOG/`0x20` (`NOOP`) | already PG-shaped |
| pgoutput B/C/R/I/D/U/T | v1 messages | real replica identity, typmod, commit-time; binary/Type/streaming later |

### Frame-level items (already parity in PG-compat mode, re-confirm during native rewrite)

- 24-byte `XLogRecord` header, LE, 2 zero pad bytes — **parity**
  (`internal/wal/xlog_record.go`).
- `CRC32C` (not IEEE) — **parity** in the canonical path; the legacy native
  frame still uses **IEEE** and an 8-byte `len+crc` header — must be replaced by
  the PG frame.
- `MAXALIGN(8)` record padding, not counted in `xl_tot_len` — **parity**.
- Page headers (magic `0xD118`, short/long PHD, contrecord) — **parity**
  (`internal/wal/xlog_page.go`, `xlog_emit.go`).
- `xl_prev` 0-based prev-link — **fixed** (`0101-0003`).
- **Block images:** goopg always writes the full 8192-byte page with
  `hole_offset=0` and never sets `BKPIMAGE_HAS_HOLE` or any compression bit —
  valid PG format but larger. Native should remove the hole (§1.2) to match PG
  byte-for-byte and reduce WAL volume.
