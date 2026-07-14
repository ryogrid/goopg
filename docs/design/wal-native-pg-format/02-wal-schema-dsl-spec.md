# 02 — WAL schema DSL spec

| Field  | Value                                                        |
| ------ | ------------------------------------------------------------ |
| Status | draft                                                        |
| Date   | 2026-07-15                                                   |
| Scope  | The notation used by [doc 03](03-pg183-wal-record-schemas.md) |
| Basis  | [Kaitai Struct](https://kaitai.io/) + WAL-specific extensions |

This document defines **WAL-KS** — a small declarative DSL for writing down the
byte layout of PostgreSQL WAL records. It is a *documentation* notation: precise
enough to compare field-by-field against C structs and against goopg's encoder,
but deliberately not a code-generator input. Doc 03 is written entirely in it.

WAL-KS is Kaitai Struct with a handful of additions for the things that make WAL
harder than an ordinary binary format. This doc gives just enough Kaitai to read
doc 03, then defines the extensions.

---

## 1. Kaitai Struct in one page

Kaitai Struct is a DSL for **declaratively describing an existing binary
format**. A type is a sequence of fields (`seq`); each field has an `id` and a
`type` (or a `size`). The core constructs used here:

**Fixed fields** — a field with a primitive type:

```ksy
seq:
  - id: xl_tot_len
    type: u4        # unsigned, 4 bytes
  - id: xl_info
    type: u1
```

**Length-prefixed / sized fields** — a byte blob whose length is another field:

```ksy
seq:
  - id: len
    type: u2
  - id: data
    size: len       # `data` is `len` bytes
```

**Discriminated union (`switch-on`)** — pick the sub-type from a tag:

```ksy
seq:
  - id: rmid
    type: u1
  - id: body
    type:
      switch-on: rmid
      cases:
        10: heap_record      # RM_HEAP
        11: btree_record     # RM_BTREE
```

**Positional access (`instances` / `pos`)** — read a field, seek to the offset
it names, and parse there:

```ksy
seq:
  - id: offset
    type: u2
instances:
  payload:
    pos: offset
    type: payload_t
```

This maps naturally onto WAL's top structure: read `XLogRecord`, switch on
`xl_rmid`, then on the resource manager's opcode (`xl_info & 0xF0`) to reach
`XLOG_HEAP_INSERT`, `XLOG_HEAP_DELETE`, and so on.

### Primitive-type vocabulary (used throughout doc 03)

| WAL-KS type | meaning | bytes |
| --- | --- | --- |
| `u1` `u2` `u4` `u8` | unsigned int | 1 / 2 / 4 / 8 |
| `s1` `s2` `s4` `s8` | signed int | 1 / 2 / 4 / 8 |
| `bool` | boolean (1 byte, C `bool`) | 1 |
| `padN` | N padding bytes (must be zero) | N |
| `strz` | NUL-terminated C string (`cstring`) | var |

C-type ↔ WAL-KS mapping used in doc 03 (all PG base types resolve to these):
`TransactionId`/`Oid`/`RelFileNumber`/`MultiXactId`/`CommandId`/`BlockNumber`/
`TimeLineID`/`pg_crc32c` → `u4`; `XLogRecPtr` → `u8`;
`FullTransactionId` → `u8`; `OffsetNumber` → `u2`; `RmgrId` → `u1`;
`TimestampTz`/`pg_time_t` → `s8`; `int` → `s4`.

---

## 2. Endianness — declared per stream, not per field

The two WAL surfaces use **opposite** byte orders, so every schema in doc 03
declares its endianness up front with a `meta:` block:

```ksy
meta:
  endian: le      # on-disk WAL records (x86 in-memory layout, memcpy'd)
```

```ksy
meta:
  endian: be      # pgoutput logical-replication messages (network order)
```

- **On-disk WAL** — little-endian. Records are the backend's in-memory structs
  copied verbatim, so the byte order is the host's; goopg (and the reference
  here) targets little-endian.
- **pgoutput** — big-endian, the PostgreSQL frontend/backend network
  convention.

---

## 3. WAL-specific extensions

Plain Kaitai can express most of a WAL record, but four WAL patterns need
explicit, first-class notation to keep doc 03 readable. These are the **WAL-KS
extensions**. They are conventions layered on Kaitai, marked with a leading `@`
so they never collide with a Kaitai keyword.

### 3.1 `@if` — conditional fields (flag-gated)

A field (or block) present only when a flag bit is set. WAL uses this
pervasively — `xinfo` chunks, optional tuple images, `WILL_INIT`, etc.

```ksy
seq:
  - id: flags
    type: u1
  - '@if': flags & XLH_INSERT_ALL_FROZEN_SET
    id: frozen_data
    type: xl_heap_freeze_plan
```

Flag constants are written in `UPPER_SNAKE` and defined in an `@enums:` /
`@flags:` block per type (see §3.5), each with its PG hex value and source cite.

### 3.2 `@varlen` — variable-length trailing arrays

An array whose element count comes from an earlier field and whose bytes run to
the end of the (sub-)record. WAL main-data structs routinely end in one.

```ksy
seq:
  - id: ntuples
    type: u2
  - id: offsets
    type: u2
    '@varlen': ntuples          # repeat `ntuples` times
```

`@varlen: to-eor` means "repeat until end of record/region" when the count is
implicit (consumer reads until the block/main-data length is exhausted).

### 3.3 `@region` / `layout` — record assembly order

The single most important WAL-specific construct. A WAL record is **not** one
flat struct: it is a fixed header, then a run of *block-reference headers*, then
optional origin/xid chunks, then a *main-data header*, and only then the
**payload region** — each block's full-page image, then each block's rmgr data,
then the main data. Positions of the payload pieces are determined by lengths
declared earlier in the header region ("later data position determined by an
earlier header").

WAL-KS makes this explicit with a top-level `layout` block naming the regions in
wire order, and `@region:` tags on the pieces that live in the trailing payload:

```ksy
layout:                          # on-the-wire order of a full WAL record
  - XLogRecord                   # 24-byte fixed header
  - block_headers: XLogRecordBlockHeader[]   # one per registered block, id-ascending
  - origin:        '@if XLR_BLOCK_ID_ORIGIN present'
  - toplevel_xid:  '@if XLR_BLOCK_ID_TOPLEVEL_XID present'
  - main_data_hdr: XLogRecordDataHeader{Short,Long}
  - '@region payload':           # trailing bytes, consumed in this order:
      - block_images[]           #   per block with HAS_IMAGE: the FPI bytes
      - block_data[]             #   per block with HAS_DATA:  the rmgr block data
      - main_data                #   the main-data struct bytes
```

Each `block_headers[i]` carries the `data_length` / image `length` that fixes
how many bytes `block_data[i]` / `block_images[i]` occupy in the payload region —
that coupling is the essence of the WAL format and why a flat `seq` cannot
express it.

### 3.4 `@image` — full-page image with a hole

A block image is a page with its free-space "hole" removed: the on-wire bytes
are `[0 : hole_offset]` followed by `[hole_offset + hole_length : BLCKSZ]`,
optionally then compressed. WAL-KS notates it as:

```ksy
'@image':
  present_if: fork_flags & BKPBLOCK_HAS_IMAGE
  hole_offset: <from XLogRecordBlockImageHeader>
  hole_length: <0 if not BKPIMAGE_HAS_HOLE, else from XLogRecordBlockCompressHeader>
  compressed:  bimg_info & (BKPIMAGE_COMPRESS_PGLZ|LZ4|ZSTD)
  # reconstructed page = image[0:hole_offset] ++ zeros(hole_length) ++ image[hole_offset:]
```

### 3.5 `@enums` / `@flags` — opcode and flag tables

Opcodes (`xl_info & 0xF0`) and flag-bit macros are declared in a per-type table
with PG value and source cite, so doc 03 stays self-checking:

```ksy
'@flags xl_heap_insert.flags':
  XLH_INSERT_ALL_VISIBLE_CLEARED: 0x01   # heapam_xlog.h:72
  XLH_INSERT_LAST_IN_MULTI:       0x02   # heapam_xlog.h:73
  ...
```

---

## 4. Notation conventions in doc 03

- Each record is introduced by its **RMGR** and **opcode** (`xl_info & 0xF0`),
  both from `rmgrlist.h` / the RMGR's `*_xlog.h`.
- Field rows give **id · WAL-KS type · width · byte-offset · PG C type · cite**.
  Offsets are within the *struct* (the main-data region), not the whole record.
- `SizeOf*` / `MinSizeOf*` macros are noted where PG copies the struct by a
  size macro (which **drops trailing padding** but keeps *internal* padding —
  see doc 03's alignment note).
- Every field and flag cites `postgres/src/...:line`.
- A closing **"native vs PG delta"** note per record states what goopg's native
  encoder must change to reach the schema.

This is all the notation doc 03 uses. It is intentionally lightweight: a reader
who knows the four extensions (`@if`, `@varlen`, `layout`/`@region`, `@image`)
and the primitive vocabulary can read every schema in doc 03 and diff it against
both the PG source and goopg's Go encoder.
