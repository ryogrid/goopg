# 0044-0001 — `varchar(N)` B-tree Key Encoding

**Status:** accepted
**Parent milestone:** M0044
**Date:** 2026-05-04

## 1. Objective

Define a self-terminating, sortable byte encoding for `varchar(N)`
B-tree keys that:

- compares correctly under bytewise (`bytes.Compare`) order,
  matching `LC_COLLATE='C'` semantics in PostgreSQL,
- is **self-terminating** — i.e., the encoded form of one value
  cannot be a prefix of another's, so concatenation in compound
  keys is unambiguous,
- accepts arbitrary byte content, including embedded `0x00` (TPC-H
  data has none, but the encoding must not silently corrupt them),
- mirrors the existing pattern used by `EncodeNumericKey` (sign +
  payload + terminator) so reviewers can read both encodings the
  same way.

## 2. Layout

```
varchar payload(b1, b2, ..., bn) →
    escape(b1) || escape(b2) || ... || escape(bn) || 0x00
```

where `escape(b)` is:

| input byte | output bytes |
|---|---|
| `0x00` | `0x01 0x01` |
| `0x01` | `0x01 0x02` |
| any other | the byte unchanged |

The literal `0x00` byte is reserved as the **end-of-key**
terminator — it cannot appear inside the payload, hence the
escape. `0x01` is the escape introducer; it is itself escaped to
preserve self-decoding.

## 3. Sort order

Bytewise comparison of the encoded form matches bytewise
comparison of the input:

- For two inputs that share a common prefix, the byte that first
  differs is at the same position in both encoded forms (the
  escape rule maps each input byte to a fixed-length 1-or-2-byte
  output, with the longer encoding only when the input byte is
  `0x00` or `0x01`, both of which sort low — preserving order).
- A shorter input has a `0x00` terminator at the same offset
  where the longer input still has payload (escaped or not), so
  the shorter sorts first — matching standard string ordering.

Worked example (no escapes needed):

```
"BUILDING"   → 42 55 49 4C 44 49 4E 47 00
"BUILD"      → 42 55 49 4C 44 00
"FURNITURE"  → 46 55 52 4E 49 54 55 52 45 00
"AUTOMOBILE" → 41 55 54 4F 4D 4F 42 49 4C 45 00
```

Sorted ascending under `bytes.Compare`: AUTOMOBILE, BUILD,
BUILDING, FURNITURE — the expected SQL order.

Worked example with escapes (synthetic; TPC-H never hits this):

```
"\x00x"      → 01 01 78 00
"\x01x"      → 01 02 78 00
"\x02x"      → 02 78 00
```

Sorts as `\x00x < \x01x < \x02x`, matching input order.

## 4. Compound-key composition

`encodeCompositeBTreeKey` (in
`internal/executor/operators_ddl.go`) appends per-column
encodings without separators. With the 0x00 terminator the
varchar encoding is **self-terminating**: for any two encodings
that differ inside their varchar prefix, the comparison resolves
inside the prefix; for two encodings that share the entire
varchar prefix (including the terminator), comparison continues
into the next column. This matches the SQL multi-column ordering
contract.

Example: index on `(c_mktsegment varchar(10), c_custkey numeric)`:

```
("BUILDING", 1)   → 42 55 49 4C 44 49 4E 47 00 || encode_numeric(1)
("BUILDING", 2)   → 42 55 49 4C 44 49 4E 47 00 || encode_numeric(2)
("FURNITURE", 1)  → 46 55 52 4E 49 54 55 52 45 00 || encode_numeric(1)
```

Bytewise compare yields BUILDING:1 < BUILDING:2 < FURNITURE:1
— correct.

## 5. Implementation

New helper in `internal/access/btree/btree.go`:

```go
// EncodeVarchar returns a sortable, self-terminating byte string
// for a variable-length payload. Bytewise comparison of the
// result matches bytewise comparison of the input, with the
// 0x00 terminator ensuring that a shorter input sorts before a
// longer input that shares its prefix.
func EncodeVarchar(payload []byte) []byte
```

`encodeBTreeKeyForColumn` (in
`internal/executor/operators_ddl.go`) gains a `varchar` case
that takes the runtime `KindString` Datum's bytes and routes
through `EncodeVarchar`.

`isSupportedBTreeKeyType` accepts type names `varchar`,
`character varying`, and `text` (the last one is reserved for a
future, unbounded-payload variant — initially errors with
"varchar required" until M0044 follow-up).

## 6. Edge cases

- **Empty string**: encodes to a single `0x00`. Unique against
  any non-empty input.
- **NULL**: rejected at backfill / insert with the existing
  `42804` error ("column %q is null and cannot be indexed"). No
  encoding required.
- **Length truncation**: the type's declared length is **not**
  enforced at the encoding layer. The executor is responsible
  for rejecting `INSERT … VALUES ('AAAAAAAAAAAA')` into a
  `varchar(5)` column. The B-tree only sees what survives that
  length check.
- **NUL inside payload**: encoded via the `0x01 0x01` escape; no
  data loss.

## 7. Verification

- `internal/access/btree/varchar_key_test.go` — new file,
  exhaustive ordering tests over a curated set of inputs
  including escapes, prefix relationships, and empty strings.
- Composite-key test in
  `internal/executor/storage_ddl_varchar_test.go` builds a
  `(c_mktsegment varchar(10), c_custkey numeric)` index and
  verifies the on-disk leaf order via `tree.RangeScan`.
- `TestTPCHResultParity` still identical=22 (regression gate).
