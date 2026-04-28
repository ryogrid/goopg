# 0006 — On-Disk Page and Tuple Format (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 needs a concrete on-disk page format. Spec §5.2 says the
on-disk format should be PostgreSQL-compatible "where doing so does not
impose excessive cost". Adopting upstream's layout costs nothing here
and makes the code easy to cross-check with `pg_filedump` and similar
tools, so we mirror it.

References into upstream:

- `postgres/src/include/storage/bufpage.h` — `PageHeaderData`,
  `ItemIdData`.
- `postgres/src/include/storage/itemid.h` — line pointer
  encoding (15-bit offset + 2-bit flags + 15-bit length).
- `postgres/src/include/access/htup_details.h` — `HeapTupleHeaderData`.
- `postgres/src/include/storage/relfilelocator.h` — `RelFileLocator`.

## Decision

### Page anatomy

Every page is `BlockSize = 8192` bytes laid out as upstream does:

```
+-------------------+
|  PageHeaderData   |   24 bytes (fixed)
+-------------------+
|  ItemId array     |   grows downward
|  pd_linp[0]..[N]  |   4 bytes per slot
+-------------------+
|  ... free space ...
+-------------------+
|  Tuple bodies     |   grow upward from pd_upper
|  (variable size)  |
+-------------------+
|  Special space    |   index AMs use this; heap leaves it empty
+-------------------+
```

`PageHeaderData` (matches `bufpage.h:159`):

| Offset | Size | Field | Notes |
| ------ | ---- | ----- | ----- |
| 0 | 8 | `pd_lsn` | upper-4 / lower-4 little-endian, mirroring upstream's `PageXLogRecPtr` |
| 8 | 2 | `pd_checksum` | CRC; 0 if checksums disabled |
| 10 | 2 | `pd_flags` | `PD_HAS_FREE_LINES` 0x0001, `PD_PAGE_FULL` 0x0002, `PD_ALL_VISIBLE` 0x0004 |
| 12 | 2 | `pd_lower` | offset to start of free space (== end of ItemId array) |
| 14 | 2 | `pd_upper` | offset to end of free space (== start of tuple region) |
| 16 | 2 | `pd_special` | offset to special space (== `BlockSize` for heap) |
| 18 | 2 | `pd_pagesize_version` | `(BlockSize | PG_PAGE_LAYOUT_VERSION)` = 0x2004 |
| 20 | 4 | `pd_prune_xid` | hint: oldest prunable XID |
| 24 | … | `pd_linp[]` | line-pointer array, grows from offset 24 upward |

`SizeOfPageHeaderData = 24`. `PG_PAGE_LAYOUT_VERSION = 4` (matches
upstream as of PG 18). `BlockSize | 4 = 8196 = 0x2004` for the
`pd_pagesize_version` word.

A blank page produced by `InitPage` has:

- `pd_lsn = 0`, `pd_checksum = 0`, `pd_flags = 0`.
- `pd_lower = 24`, `pd_upper = 8192`, `pd_special = 8192`.
- `pd_pagesize_version = 0x2004`.
- All other bytes zero.

### Line pointers

Each `ItemIdData` is 4 bytes encoding three fields (matches
`itemid.h`):

```
bits 0..14    lp_off     (15-bit offset of tuple from page start)
bits 15..16   lp_flags   (2-bit: UNUSED=0, NORMAL=1, REDIRECT=2, DEAD=3)
bits 17..31   lp_len     (15-bit tuple length)
```

We pack the three values into a `uint32` little-endian on the wire,
matching upstream. The 15-bit fields cap a single tuple at 32 KiB and
a page at 32 KiB, which is fine because `BlockSize = 8 KiB`.

### RelFileNode (RelFileLocator)

`RelFileNode` is the (database OID, table-or-index OID, fork number)
triple every IO call carries. v0 only uses fork `Main = 0`; the FSM
(`Free Space Map`, fork 1) and visibility map (fork 2) are recognised
but not used until VACUUM lands.

### Tuple format (HeapTupleHeader)

We mirror the upstream HeapTupleHeader fields (`htup_details.h`) but
do not implement them in this milestone — heap insert / scan land
later in milestone 5 with their own design doc when needed. The
fields that *do* show up in this loop are:

- `xmin` / `xmax` / `xvac` (32-bit transaction IDs).
- `t_ctid` (6-byte item pointer: 4-byte block + 2-byte item).
- `t_infomask`, `t_infomask2` (16-bit flag words).
- `t_hoff` (1-byte length of tuple header, including any null bitmap).

The full layout, with NULL bitmap and OID-bearing variants, is
deferred until heap insert / scan / VACUUM design docs.

### What this doc does NOT cover

- The heap access method (insert / delete / update / scan). Comes in
  a later design doc once milestone 5 needs writes.
- The btree access method on-disk layout. Comes in `0009-btree.md`.
- WAL record formats. → `0008-wal-and-recovery.md`.
- The `RelFileLocator` to filename mapping; that's smgr's
  responsibility documented in `0005-buffer-manager.md`.

## Alternatives Considered

- **Bigger block size (16 KiB or 32 KiB).** Rejected: upstream's tools
  (`pg_filedump`, `pageinspect`) all assume 8 KiB. Operators carrying
  PostgreSQL muscle memory expect 8 KiB blocks.
- **Custom flat tuple layout (no line pointers).** Rejected: line
  pointers are how PostgreSQL keeps tuple offsets stable across
  in-page compaction. Heap UPDATE in particular *requires* the
  REDIRECT line-pointer flag for HOT chains; building heap on top of
  a flat layout means re-litigating upstream's MVCC.

## Consequences

- A `pg_filedump` of a goopg page file looks structurally like a
  PostgreSQL page file. Field-by-field tooling agreement is a useful
  early diagnostic.
- The 8 KiB / 4 KiB direct-I/O alignment story stays consistent: file
  offsets are `n * 8192`; buffer pool slots come from a 4 KiB-aligned
  arena.
