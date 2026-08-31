# 0044-0002 — `char(N)` B-tree Key Encoding

**Status:** accepted
**Parent milestone:** M0044
**Date:** 2026-05-04

## 1. Objective

Define a self-terminating, sortable byte encoding for `char(N)`
B-tree keys whose comparison semantics match PostgreSQL's blank-
padded character type:

```sql
'A'         = 'A         '   -- both stored as char(10)
```

PostgreSQL strips trailing spaces before comparing `char(N)`
values. Without that normalisation, a query that probes the
index with literal `'A'` would build a different key from the
backfilled row that stored `'A         '` — and the index lookup
would miss.

## 2. Approach

Encode the **trimmed** payload (trailing 0x20 bytes removed)
exactly the way `EncodeVarchar` (design doc 0044-0001) encodes a
variable-length payload. The two encodings share their
sort-order contract; the only difference is the trim step.

```go
// EncodeChar returns the sortable byte form of a CHAR(N)
// payload. Trailing 0x20 (space) bytes are stripped, after
// which the layout is identical to EncodeVarchar.
//
// Per PostgreSQL semantics, 'A' and 'A         ' (1-byte and
// 10-byte forms of the same conceptual value) must produce
// identical bytes. The trim achieves that.
func EncodeChar(payload []byte) []byte {
    return EncodeVarchar(bytes.TrimRight(payload, " "))
}
```

## 3. Why trim, not pad

Two valid normalisations exist:

1. **Pad to N**: every value becomes exactly N bytes. Compound
   keys do not need a terminator (fixed length). Drawback:
   wastes index pages; for `char(15)` columns containing 1-byte
   values, every leaf entry stores 14 bytes of `0x20`.
2. **Trim trailing spaces**: matches `EncodeVarchar`'s shape;
   keys are as compact as the meaningful part of the value.

Trim wins on both correctness (PostgreSQL parity) and space.
PostgreSQL's own B-tree (`bttextcmp` and the blank-padded
support functions) implements trim at compare time; we
materialise the trim into the encoded key so the bytewise
compare is correct.

## 4. Sort order

Trim-then-encode yields the right order:

```
'A'              → 'A' trimmed = 'A'         → 41 00
'A         '     → 'A' trimmed = 'A'         → 41 00   (== above)
'AB'             → 'AB' trimmed = 'AB'       → 41 42 00
'B'              → 'B' trimmed = 'B'         → 42 00
'         '      → '' trimmed = ''           → 00      (empty)
```

Compound-key example, index on
`(c_mktsegment char(10), c_custkey numeric)`:

```
('BUILDING  ', 5)   → 42 55 49 4C 44 49 4E 47 00 || encode_numeric(5)
('BUILDING  ', 7)   → 42 55 49 4C 44 49 4E 47 00 || encode_numeric(7)
('FURNITURE ', 1)   → 46 55 52 4E 49 54 55 52 45 00 || encode_numeric(1)
```

Sorts BUILDING:5 < BUILDING:7 < FURNITURE:1 — matches the SQL
multi-column ordering.

## 5. Implementation

- New helper `EncodeChar(payload []byte) []byte` in
  `internal/access/btree/btree.go` — single-line wrapper around
  `EncodeVarchar` with the trim.
- `encodeBTreeKeyForColumn` learns a `char` / `character` case;
  takes the runtime `KindString` Datum's bytes and routes
  through `EncodeChar`.
- `isSupportedBTreeKeyType` accepts type names `char`,
  `character`, and `bpchar` (PostgreSQL's internal name for
  blank-padded char).

## 6. Edge cases

- **All-spaces payload**: trims to empty, encodes to `[0x00]`.
  Equivalent to `''` — same as PostgreSQL.
- **Embedded leading or middle spaces**: NOT trimmed. Only
  trailing spaces are stripped.
- **Embedded `0x00`**: escaped via the `EncodeVarchar` rule.
  TPC-H never hits this; the encoding remains correct.
- **`char(0)` (zero-length)**: degenerate but encodes to
  `[0x00]` (empty payload). Unlikely in practice.

## 7. Verification

- `internal/access/btree/char_key_test.go` — assertions covering
  the trim semantics: `EncodeChar([]byte("A         ")) ==
  EncodeChar([]byte("A"))`, ordering tests across mixed-length
  inputs, and the all-spaces edge case.
- Integration test
  `internal/executor/storage_ddl_char_test.go` builds a
  `customer (c_mktsegment char(10))` index, inserts both
  trimmed and padded forms of the same value, and asserts the
  unique-key constraint (if `UNIQUE`) rejects the duplicate.
- `TestTPCHResultParity` regression gate.
