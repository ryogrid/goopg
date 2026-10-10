# M0141-S2a-fix2r-b — ANALYZE's stawidth is `VARSIZE_ANY`, truncated to int32

Status: done 2026-10-10 (93df279ce). Parent: M0141-S2a-fix2r.

## Problem as filed

M0141-S2a-fix2r-a found goopg's `pg_stats.avg_width` far below PG's on
the TPC-DS SF1 `customer` table:

| column | goopg | PG |
|---|---|---|
| `c_first_name char(20)` | 6 | 21 |
| `c_customer_id char(16)` | 16 | 17 |

It filed this as "ANALYZE omits the blank padding".

## What was actually wrong

There were two separate effects.

### The SF1 cluster's data is stored unpadded

On SF1, `customer.c_first_name` gives `pg_column_size` 6 while
`octet_length` gives 20.

- The values are stored without their padding, an artifact of that
  cluster's old load.
- Current goopg pads `char(n)` on both INSERT and COPY: a throwaway
  cluster stores 20 bytes for `'ab'::char(20)`.
- The SF0.25 cluster stores padded values, and its `avg_width` was 20.
- ANALYZE measures the stored datum faithfully, so this part needs no code
  change. It goes with the owner's pending SF1 reload.

### ANALYZE counted the body only and kept a float

PG's `compute_scalar_stats` / `compute_distinct_stats` add
`VARSIZE_ANY(DatumGetPointer(value))` to `total_width`
(analyze.c:2008, 2124, 2471). That is the stored varlena including its
header:

- 1 byte for a short varlena (body ≤ `VARATT_SHORT_MAX` − 1 = 126);
- 4 bytes for a longer one.

PG then assigns `total_width / nonnull_cnt` to the int32 `stawidth`,
which truncates it.

goopg's `datumVariablePayloadWidth` counted only the body of strings and
bytea, and goopg kept the quotient as a float. So:

- `char(16)` read 16 where PG reads 17;
- a text column with an average of 3.55 bytes showed `avg_width`
  3.5454545 where PG shows 3.

## Change

- **`analyzeVarsizeAny`** (operators_analyze.go) is the per-value width the
  ANALYZE loop now adds. It is `datumVariablePayloadWidth` plus the
  varlena header for KindString and KindBytes. Numeric already measured
  its whole varlena (M0138-0007).
- **`datumVariablePayloadWidth` is unchanged.** `spill_test.go` pins the
  executor's in-memory row estimate (`estimatedRowBytes`) to it, and that
  estimate is a different quantity.
- **Truncation.** `AvgWidth` for a variable-width column is
  `math.Trunc(total / nonNull)`.

## Verification

- **`TestAnalyzeVarsizeAny`** checks these values:
  - char(16) gives 17;
  - padded char(20) gives 21;
  - an empty string gives 1;
  - the 126/127-byte header boundary gives 127 and 131;
  - bytea and numeric are covered too.
- **`TestAnalyzePopulatesAvgWidth`** adds a case: `'ab','ab','abcd'`
  stores 3.
- **Throwaway cluster.** After ANALYZE: `char(20)` 21, a mixed varchar
  3.54, a mixed text 30.36. Each equals the hand-computed `VARSIZE_ANY`
  mean. The fractional display came from before truncation was added.
- **TPC-DS.** Stats are frozen at load time (the capture never ANALYZEs),
  so the fire set cannot see an ANALYZE change. It reported no fires at
  both scales.
  - **A/B method.** A private SF0.25 A/B (`tmp/m152/ab.sh`) cloned the
    cluster per arm and ANALYZEd every table with the HEAD binary and with
    the candidate (`GOOPG_ANALYZE_SEED=20260905`), then captured all 99
    plans.
  - **Result.** The two captures are byte-identical, including costs, with
    56 matches on both arms. goopg's plan widths and most cost terms read
    type widths rather than stawidth (see M0141-S2a-fix2r-c).
- **TPC-H.** Plans are identical to m151, and the acceptance arm matches on
  values.
- **Other gates.** Units, spotcheck, SF0.25 sweep (PASS=99) and
  ea-ratchet (1) all pass.
- **Regress A/B.** Only the join flap differs.
- **Isolation family.** Only ReadWriteUnique4 and TemporalRangeIntegrity
  fail, and both already fail in the nightly (AI-20261010-005735-001/002).

## Not covered (ledgered)

- **Compressed and toasted values.** PG measures `VARSIZE_ANY` of the raw
  sample datum. That is the compressed size for an inline-compressed
  value, and the 18-byte pointer for an external toast value. goopg
  decodes the sample rows, so it measures the uncompressed body.
- **Other varlena kinds.** jsonb, arrays, ranges and the like have
  `datumVariablePayloadWidth` 0, so their `avg_width` stays 0 (unknown).
- **cstring.** PG adds `strlen + 1`; goopg has no cstring columns in this
  path.
