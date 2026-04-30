# REF-013: Tuple Format & Codec

## Overview

goopg stores table rows as binary-encoded tuples on 8 KiB heap pages. The codec package converts between Go `Row` slices ([]Datum) and the wire-format byte representation used in heap pages, WAL records, and the pgoutput logical replication protocol.

## goopg Implementation

**Package:** `internal/executor/codec.go` (column encode/decode)

### Row Format

A `Row` is `[]Datum` where each `Datum` is a tagged union:

```go
type Datum struct {
    Kind            DatumKind
    Int             int64
    NumericMantissa uint64
    NumericScale    int64
    String          string
    Bool            bool
}
```

### On-Disk Tuple Format

Each tuple has a header followed by column data:

```
┌──────────────────────┐
│ HeapTupleHeader       │
│  - xmin (4 bytes)     │
│  - xmax (4 bytes)     │
│  - t_ctid (4 bytes)   │
│  - t_infomask (2 bytes)│
│  - t_hoff (1 byte)    │
├──────────────────────┤
│ Column data            │
│  col 1: null-flag(1) + value(N) │
│  col 2: null-flag(1) + value(N) │
│  …                      │
└──────────────────────┘
```

Each column value is encoded by `encodeValue` based on its type:
- `int4`/`integer`/`int`: 4 bytes big-endian.
- `int8`/`bigint`: 8 bytes big-endian.
- `text`/`varchar`: length-prefixed string (2 byte length + data).
- `numeric`: mantissa + scale (variable-length).
- `bool`: 1 byte (0 = false, 1 = true).
- `timestamp`/`timestamptz`: 8 bytes big-endian micros.

NULL columns are encoded as a single 0x01 flag byte (no value bytes).

### Codec Functions

- `EncodeRow(cols, row) → []byte` — serialises a Row into binary.
- `DecodeRow(cols, data) → Row` — deserialises binary into a new Row allocation.
- `DecodeRowInto(dst, cols, data) → error` — fills an existing Row slice (no allocation, M0027).
- `encodeValue(t, d) → []byte` — single-column serialisation.
- `decodeValue(t, buf) → (Datum, bytesUsed, error)` — single-column deserialisation.

## PostgreSQL Implementation

PostgreSQL's tuple format (`htup.h`, `tupdesc.h`) is similar but
more complex:

- **HeapTupleHeaderData** — includes `t_xmin`, `t_xmax`, `t_cid`,
  `t_ctid`, `t_infomask`, `t_infomask2`, `t_hoff`. goopg omits
  `t_cid` (command ID for subtransaction visibility) and
  `t_infomask2` (for HOT and key information).
- **Null bitmap** — PostgreSQL stores a per-tuple bitmap of NULL
  columns rather than per-column flags. goopg uses a 1-byte flag
  per column, which adds overhead for wide tables.
- **TOAST** — PostgreSQL's Tuple Oversized Attribute Storage
  Technique (TOAST) moves large column values (> 2 KB) to a
  separate table. goopg does not implement TOAST.
- **Datum type** — PostgreSQL's Datum is a `uintptr` that either
  contains the value inline (pass-by-value types: int, bool, oid)
  or points to a separately-allocated struct (pass-by-reference:
  text, numeric). goopg's `Datum` is a tagged struct.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| NULL representation | 1-byte flag per column | Bitmap in tuple header |
| TOAST | Not implemented | Out-of-line large values |
| t_cid / infomask2 | Omitted | Present for subtransactions / HOT |
| Datum type | Tagged Go struct (`DatumKind` union) | `uintptr` (inlined or pointer) |
| Numeric encoding | Mantissa + scale | NumericVar with base-10000 digits |

## References

- goopg: `internal/executor/codec.go`
- PG tuple header: `postgres/src/include/access/htup.h`
- PG tuple descriptor: `postgres/src/include/access/tupdesc.h`
- PG Datum: `postgres/src/include/postgres.h`
