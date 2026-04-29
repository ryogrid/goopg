# 0011-0001 — B-tree NUMERIC Key Ordering

**Status:** accepted
**Milestone:** [0011 — B-tree NUMERIC Key Support](../milestones/0011-btree-numeric-key-support.md)
**Spans seam:** key encoding + comparator contract
**Cross-links:**
[0002-0002](0002-0002-btree-concurrency.md) (B-tree concurrency / item layout),
[0003-0012](0003-0012-numeric-arithmetic.md) (NUMERIC datum carrier),
[0003-0004](0003-0004-hammerdb-tpch-integration.md) (TPC-H index-build flow).

## Context

goopg's B-tree v0 (`internal/access/btree`) currently supports only `int4`
keys. Keys are encoded with `EncodeInt4` (4-byte big-endian biased so a
bytewise compare matches numeric order) and item bodies already carry a
`keyLen` prefix, so variable-length keys are storable today — what's
missing is a numerically-correct byte encoding for `NUMERIC` and the
DDL/backfill plumbing that uses it.

The carrier for NUMERIC values is `(mantissa int64, scale int8)`, with the
real value `v = mantissa × 10^(-scale)`. Two NUMERIC values can be
numerically equal but have different `(mantissa, scale)` pairs (`1.0` is
`(10, 1)`, `1.00` is `(100, 2)`). The B-tree must treat them as identical
keys so UNIQUE/PRIMARY KEY rejects duplicates regardless of formatting and
range scans return consistent results.

## Goal

Define a **deterministic, fixed-format byte encoding** for NUMERIC B-tree
keys such that bytewise comparison via the existing `CompareKeys`
matches numeric order:

- Scale-invariant: `Encode(10, 1)` == `Encode(100, 2)` == `Encode(1, 0)`.
- Sign-correct: `Encode(-1, 0) < Encode(0, 0) < Encode(1, 0)` bytewise.
- Magnitude-correct across exponents and digit lengths.
- Range scan / equality lookup contract identical to int4 today: caller
  encodes once, btree compares bytes.

Decoding is **not** required for this seam — the B-tree never inverts the
encoding; index probes always re-encode from the live datum.

## Encoding

For a NUMERIC value `v = mantissa × 10^(-scale)`:

### Zero

Single-byte sentinel: `[0x01]`.

### Non-zero

Normalize: strip trailing decimal zeros from `|mantissa|`, decrementing
`scale` per zero stripped (allowing `scale` to go negative). After
normalization, `|mantissa|` has no trailing zero in base-10 and `scale`
captures the residual decimal place.

Compute scientific form `d_1.d_2…d_n × 10^E` where:

- `digits = strconv.FormatUint(|mantissa|, 10)` (after normalization),
- `ndig = len(digits)`,
- `E = ndig - 1 - scale` (position of the most-significant digit).

Layout:

```
sign(1) || exp(4) || digits(ndig) || terminator(1)
```

| Field      | Positive (sign=0x02)             | Negative (sign=0x00)                       |
|------------|----------------------------------|--------------------------------------------|
| sign       | `0x02`                            | `0x00`                                      |
| exp (BE)   | `uint32(int32(E) + 0x80000000)`   | `uint32(int32(0x7FFFFFFF) - E)` (inverted) |
| digit byte | `'0' + d` (ASCII `0x30..0x39`)    | `'0' + (9 - d)` (inverted)                 |
| terminator | `0x00` (less than any digit byte) | `0xFF` (greater than any digit byte)       |

### Why this works

**Sign:** `0x00 < 0x01 < 0x02` orders negative < zero < positive
unconditionally — sign byte alone settles cross-sign comparisons.

**Exponent (positives):** biased `E + 2^31` is a monotone unsigned
big-endian encoding — bigger `E` means bigger value, which is what we want.

**Exponent (negatives):** the inverted bias `2^31 - 1 - E` flips
monotonicity — bigger `E` means more negative (smaller value), which sorts
to a smaller byte string. Per-digit inversion (`9 - d`) flips digit
ordering for the same reason.

**Terminator (positives):** `0x00` is smaller than every digit byte
(`'0'..'9'` = `0x30..0x39`), so a shorter digit string sorts before a
longer prefix-equal one. With same `E`, more digits = larger value
(e.g. 1.9 vs 1.99), so shorter-sorts-first matches `1.9 < 1.99`.

**Terminator (negatives):** `0xFF` is greater than every inverted digit
byte (still `0x30..0x39`), so a shorter digit string sorts AFTER a
longer prefix-equal one. With same `E` and inverted digits, more digits
in the negative encoding = more negative (smaller), so longer-sorts-first
matches `-1.99 < -1.9`.

### Worked examples

| Value   | (m, s)   | Normalized (m', s') | E   | Encoding (hex)              |
|---------|----------|---------------------|-----|-----------------------------|
| `0`     | (0, 0)   | —                   | —   | `01`                        |
| `1`     | (1, 0)   | (1, 0)              | 0   | `02 80000000 31 00`         |
| `1.0`   | (10, 1)  | (1, 0)              | 0   | `02 80000000 31 00`         |
| `1.00`  | (100, 2) | (1, 0)              | 0   | `02 80000000 31 00`         |
| `1.5`   | (15, 1)  | (15, 1)             | 0   | `02 80000000 31 35 00`      |
| `1.50`  | (150, 2) | (15, 1)             | 0   | `02 80000000 31 35 00`      |
| `1.99`  | (199, 2) | (199, 2)            | 0   | `02 80000000 31 39 39 00`   |
| `0.5`   | (5, 1)   | (5, 1)              | -1  | `02 7FFFFFFF 35 00`         |
| `10`    | (10, 0)  | (1, -1)             | 1   | `02 80000001 31 00`         |
| `-1`    | (-1, 0)  | (1, 0)              | 0   | `00 7FFFFFFF 38 FF`         |
| `-1.5`  | (-15, 1) | (15, 1)             | 0   | `00 7FFFFFFF 38 34 FF`      |
| `-1.99` | (-199,2) | (199, 2)            | 0   | `00 7FFFFFFF 38 30 30 FF`   |

Eyeballing column 5: zero (`01`) sits between negatives (lead `00`) and
positives (lead `02`). Within positives, `0.5 < 1 = 1.0 = 1.00 < 1.5 < 1.99 < 10`
holds bytewise. Within negatives, `-1.99 < -1.5 < -1` holds bytewise
(after the shared `00 7FFFFFFF` prefix, `38 30 30 FF` < `38 34 FF` <
`38 FF`).

## Bounds and invariants

- `ndig` ≤ 19 (max decimal digits of `uint64(2^63)`).
- `scale` after normalization ∈ `[-19, 127]`; `E = ndig - 1 - scale` ∈
  approximately `[-127, 37]` — comfortably inside `int32`.
- Encoding length: `1 + 4 + ndig + 1` ≤ 25 bytes.
- `Encode(m1, s1) == Encode(m2, s2)` iff the two values are numerically
  equal — this is the UNIQUE/PRIMARY KEY semantics requirement
  (DoD #4).

## Out of scope

- Decoding (`bytes -> (mantissa, scale)`): not required by the access
  method; the B-tree compares encoded keys without inverting them.
- `int64`-overflow NUMERIC values: the carrier itself is int64-bounded
  per `0003-0012`. Encoding values whose canonical mantissa exceeds
  `uint64` is impossible by construction.
- Variable-length `HighKey` in `BTPageOpaque`: today the opaque area
  hard-codes a 4-byte `HighKey`. Lifting that to variable-length is
  required by `0011-0002` (B-tree build / uniqueness path) but not by
  this encoding contract.
- Multi-column composite NUMERIC keys: out of B-tree v0 scope.

## References

Upstream NUMERIC ordering reference: `postgres/src/backend/utils/adt/numeric.c`
(`numeric_cmp_abbrev`, `numeric_normalize`). goopg's encoding diverges
from upstream's abbreviated-key shape because we need full-precision
sortability, not abbreviated comparison — but the normalization rule
(strip trailing zeros, sort by sign/exponent/significand) is the same.
