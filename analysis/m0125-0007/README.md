# M0125-0007 — unpadded month/day date decode: measurement record

Date: 2026-07-30. Branch `tpcds-fix2`, fix applied on top of `337526b1`.

## The defect, reproduced against PG 18.3

Throwaway goopg on :5533 and a throwaway PG 18.3 on :5534, same literals:

| literal | PG 18.3 | goopg @337526b1 | goopg (fixed) |
|---|---|---|---|
| `'2002-5-01'::date` | `2002-05-01` | ERROR 22007 | `2002-05-01` |
| `'2002-05-1'::date` | `2002-05-01` | ERROR 22007 | `2002-05-01` |
| `'2002-5-1'::date` | `2002-05-01` | ERROR 22007 | `2002-05-01` |
| `' 2002-5-1 '::date` | `2002-05-01` | ERROR 22007 | `2002-05-01` |
| `'002-1-1'::date` | `0002-01-01` | ERROR 22007 | (accepted; see year-range row below) |
| `'3:4:5'::time` | `03:04:05` | ERROR 22007 | `03:04:05` |
| `'03:4:05'::time` | `03:04:05` | ERROR 22007 | `03:04:05` |
| `'2002-5-1 3:4:5'::timestamp` | `2002-05-01 03:04:05` | ERROR 22007 | `2002-05-01 03:04:05` |
| `d_date = '2002-5-01'` (1 matching row) | 1 | **0, no error** | 1 |
| `ts = '2002-05-01 03:04:05'` (1 matching row) | 1 | **0, no error** | 1 |

The last two lines are the wrong answers. The cast path errored loudly; the
comparison path coerced the unknown literal through `tryParseStringAs`, failed
the same parse, reported the failure as "leave it a string", and the comparison
simply came out false.

Still rejected after the fix, unchanged from `337526b1` and still a gap versus
PG (see the deferral rows): `2002-May-1`, `May 1, 2002`, `20020501`, `2002/5/1`,
`5-1-2002`, `02-5-1`, `2002-005-01` (PG's day-of-year form), `2002-5-1 BC`, and
`d_date = 'garbage'` (PG raises 22007, goopg still silently compares unequal).

## Pre-existing defects this measurement surfaced but did NOT introduce

Both reproduce identically at `337526b1` with fully padded literals:

* dates outside Go's `time.Time` nanosecond range round-trip wrong —
  `'0002-01-01'::date` → `1755-08-30`, `'0500-01-01'::date` → `2253-08-30`,
  `'1000-01-01'::date` → `2169-02-08`. Widening input acceptance makes the
  range reachable by more spellings; it did not cause it.
* `'2002-05-01 03:04:05.25-04'::timestamp` → `07:04:05.25`; PG keeps
  `03:04:05.25` (a plain `timestamp` ignores the offset).

## TPC-DS acceptance (SF0.5, subset probe)

`FORCE=1 QUERIES="16 94 95" scripts/tpcds-sf05-regression.sh sweep`
→ `sf05-probe/sweep-20260730-022350.txt`. FORCE was needed because the nightly
CI batch held the host; this is a VALUE probe, so the timings in that report are
not usable and no timing claim is made from it.

All three queries carry unpadded date literals (`'2002-4-01'`, `'2002-5-01'`,
`'2001-4-01'`) and all three previously returned the same `0 / NULL / NULL`
answer under the single checksum `512b5fdab820c47b`. After the fix:

| query | goopg before | goopg after | PG 18.3 (tpcds05) |
|---|---|---|---|
| Q16 | `0 / NULL / NULL` (ck `512b5fdab820c47b`) | `63 / 319602.45 / -91294.46` (ck `863c4e96d8930d66`) | `23 / 93334.17 / -35323.69` (ck `40dbec0df91d2438`) |
| Q94 | `0 / NULL / NULL` (ck `512b5fdab820c47b`) | `7 / 10534.30 / 7178.64` (ck `fb2c619e9bcb6bae`) | `2 / 5037.18 / 1067.82` (ck `04afc1b69831a5ea`) |
| Q95 | `0 / NULL / NULL` (ck `512b5fdab820c47b`) | `5 / 11180.00 / -6205.20` (ck `663cec31dac6449c`) | `23 / 45031.03 / -1282.36` (ck `e498634c02595c29`) |

The predicted signature landed exactly: one goopg checksum became three distinct
ones. It did NOT become the oracle's three, because a second defect sits behind
each query — and the probe identifies which:

* **Q16 and Q94 OVER-count** (63 vs 23, 7 vs 2). Both are `EXISTS` + `NOT
  EXISTS` on the same outer relation, the conjunction-grows-the-result shape
  already filed as **M0125-0008**. Q16 was not previously named in that item;
  it is now, with numbers.
* **Q95 UNDER-counts** (5 vs 23) and has no `EXISTS` at all — it gates on two
  `ws_order_number IN (subquery)` over the `ws_wh` CTE. Different mechanism,
  separately filed as **M0125-0023**.

## Gates

* `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
* Regress-port (Hard-won Rule #5, the M0106 gate): `pg-regress-runner.sh` quick
  set (52 tests) plus `date time timetz timestamp timestamptz horology`, run
  against a HEAD-`337526b1` worktree binary and the fixed binary. `1/52 PASS` on
  both; after normalising the embedded temp paths and diff headers, **51 of 52
  per-test diffs are byte-identical** and the 52nd (`uuid`) differs only in the
  wall-clock instants a `uuidv7` extraction test prints. No regression, and no
  visible improvement at this granularity either — the six datetime suites fail
  far upstream of anything this change touches.
* `scripts/tpch-spotcheck.sh` — PASS (Q12 rows=2, Q13 rows=35).
* Full 99-query SF0.5 gate — NOT run; the nightly CI batch owned the host for
  the whole loop. Owed, and recorded in the deferral ledger.
