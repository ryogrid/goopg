# 0044-0004 — Compound B-tree Indexes Over Mixed Types

**Status:** draft
**Parent milestone:** M0044
**Date:** 2026-05-04

## 1. Objective

Verify that the existing
`internal/executor/operators_ddl.go::encodeCompositeBTreeKey`
function — which simply concatenates per-column encodings — produces
correctly-ordered keys when those columns mix `int4`, `int8`,
`numeric`, `varchar`, `char`, and `timestamp` per the new
encodings landed in 0044-0001 / 0044-0002 / 0044-0003.

## 2. Why this is non-trivial

`encodeCompositeBTreeKey` does **no** separator handling. Its
correctness rests entirely on every per-column encoding being
**self-terminating**:

> A self-terminating encoding is one in which no encoded value is
> a prefix of another encoded value (formally: the encoding is a
> prefix code).

If any encoding in the composite chain violated this rule, two
distinct (col1_a, col2) and (col1_b, col2) pairs could produce
encoded keys that compare in the wrong order at column 2 because
the col1 prefix was ambiguous.

## 3. Self-termination of each encoding

| Type | Encoding | Self-terminating? | How |
|---|---|---|---|
| `int4` | 4 bytes BE sign-flipped | ✓ | fixed length |
| `int8` | 8 bytes BE sign-flipped | ✓ | fixed length |
| `numeric` (zero) | `[0x01]` | ✓ | reserved single byte; no other encoding starts with `0x01` followed by nothing |
| `numeric` (non-zero) | sign(1) + exp(4 BE) + digits(N) + term(1) | ✓ | terminator byte (0x00 for positive, 0xFF for negative) is outside the digit range `'0'..'9'` |
| `varchar` | escaped payload + `0x00` terminator | ✓ | terminator outside escaped-byte range |
| `char` | trim then varchar-encode | ✓ | inherits varchar's self-termination |
| `timestamp` | 8 bytes BE sign-flipped | ✓ | fixed length |

Every entry in the table is a prefix code, so concatenations of
any sequence of them are themselves prefix codes. ∎

## 4. Worked compound examples

### 4.1 `(timestamp, numeric)` — `lineitem(l_shipdate, l_orderkey)`

```
('1995-01-15', 1)  → ts(1995-01-15) || num(1)
('1995-01-15', 2)  → ts(1995-01-15) || num(2)
('1995-01-16', 1)  → ts(1995-01-16) || num(1)
```

The two `('1995-01-15', …)` rows share the same 8-byte timestamp
prefix; comparison continues into `num(1)` vs `num(2)`. The
`('1995-01-16', 1)` row differs at byte 7 of the timestamp, so
its compare resolves before the numeric prefix ever matters.

### 4.2 `(varchar, varchar)` — `customer(c_name, c_phone)`

```
('Customer#1', '13-1') → 43 75 73 74 6F 6D 65 72 23 31 00 || 31 33 2D 31 00
('Customer#10', '13-1') → 43 75 73 74 6F 6D 65 72 23 31 30 00 || 31 33 2D 31 00
```

`'Customer#1'` and `'Customer#10'` differ at byte 10
(`0x00` vs `0x30`), and `0x00 < 0x30`, so `Customer#1` sorts
first. The 0x00 terminator is what makes that comparison
*correct* — without it, `Customer#1` would appear to share a
prefix with `Customer#10` and the column-2 phone numbers would
incorrectly become the deciding bytes.

### 4.3 `(char, numeric)` — `customer(c_mktsegment, c_custkey)`

`c_mktsegment` is `char(10)`; values pad to 10 bytes on disk
but the trim-then-varchar-encode rule collapses them:

```
('BUILDING  ', 5) → 'BUILDING' encoded || num(5)
('BUILDING  ', 7) → 'BUILDING' encoded || num(7)   (same prefix as above)
('FURNITURE ', 1) → 'FURNITURE' encoded || num(1)  (different prefix)
```

The trimmed-then-encoded form is identical for any padding
amount of the same logical value, so two on-disk rows that
PostgreSQL considers equal land at the same composite key.

## 5. Implementation

`encodeCompositeBTreeKey` already works as-is — no code change.
What changes:

- The DDL guard `isSupportedBTreeKeyType` accepts mixed-type
  compound keys provided every column type is in the new
  combined set.
- A new property test
  `internal/access/btree/composite_key_test.go` builds random
  small relations with random column-type combinations from
  `{int4, int8, numeric, varchar, char, timestamp}`, encodes
  every (row_a, row_b) pair both via the per-column encoders
  (round-tripping through `encodeCompositeBTreeKey`) and via a
  reference column-by-column comparator, and asserts that the
  byte order matches the SQL order.

## 6. Symmetry between backfill and probe

The existing `encodeBTreeKeyForColumn` is used for both
backfill (table → index) and probe (literal → lookup). All new
type cases must route through the same function so the two
paths produce byte-identical keys for byte-identical inputs.

The planner's index-scan eligibility check (design doc
0044-0005) constructs probe keys via the same path; if a
compound key includes a type whose probe value is missing
(e.g., a `BETWEEN` on column 2 with column 1 fixed by `=`), the
planner must still form a valid prefix probe. The probe-prefix
contract is unchanged from M0011 era — the new types do not
require any new mechanics.

## 7. Verification

- `internal/access/btree/composite_key_test.go` — randomised
  property tests over the mixed type matrix.
- Integration test `internal/executor/storage_ddl_compound_test.go`
  builds the four following indexes:
  1. `(l_shipdate timestamp, l_orderkey numeric)`
  2. `(c_mktsegment char(10), c_custkey numeric)`
  3. `(p_type varchar(25), p_partkey numeric)`
  4. `(o_orderdate timestamp, o_custkey numeric, o_orderkey numeric)`
  and verifies row-count parity between IndexScan and SeqScan.
- `TestTPCHResultParity` regression gate.
