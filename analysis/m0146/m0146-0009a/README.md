# M0146-0009a evidence — Q7/Q27 `cd+dd+item+ss` estimate collapse

Task: recon for the EA finding
`Q7:customer_demographics+date_dim+item+store_sales` (slice-2 capture: est 47,
actual 1944; `pg_est` null). HEAD `414486122`.

## Verdict

Root cause found, and it is NOT the join arithmetic — it is a **base-rel
selectivity miss on `customer_demographics`**: the `char(20)` equality
`cd_education_status = 'College'` never hits the MCV list because ANALYZE
stamps `MCV.Value` with the blank-padded datum form (`"College             "`),
while `formatExprConstant` renders the literal unpadded (`"College"`). The
byte-equal compare at `internal/optimizer/selectivity.go:385`
(`eqSelectivityForColumn`) misses, the column is MCV-complete (7 distinct, 7
MCVs), so `remainingDistinct <= 0` and the clause falls through to
`defaultEqSelectivity` = 0.005 — 28.4x under the measured MCV frequency
0.1418.

## Measured numbers (SF0.25 private clone `tmp/m0146-0009a`, port 5592,
all tables ANALYZEd, seed equivalent to the gate)

Per-conjunct goopg estimates on `customer_demographics` (1,920,800 raw rows):

| predicate | goopg est | MCV freq | formula | verdict |
|---|---|---|---|---|
| `cd_gender = 'F'` (char(1)) | 969,683 | 0.5048 | 1,920,800 x 0.5048 = 969,683 | MCV hit |
| `cd_marital_status = 'W'` (char(1)) | 388,321 | 0.2022 | = 388,327 | MCV hit |
| `cd_education_status = 'College'` (char(20)) | **9,604** | 0.1418 | should be ~272,432 | **DEFAULT_EQ_SEL = 0.005** |
| `cd_education_status = 'Secondary'` | **9,604** | 0.1446 | should be ~277,720 | same miss (Q27) |

Three-conjunct filtered rel: goopg **980** = 1920800 x 0.5048 x 0.2022 x 0.005.
PG 18.3 (`:65438/tpcds025`, read-only EXPLAIN): **28,038** (own ANALYZE:
edu-alone est 274,098). Actual: 27,440.

## Join-level cascade

goopg DPREL whole-rel sizes (`GOOPG_PGSHAPED_DP_TRACE=1`, `dprel-sizes.txt`)
vs PG per-worker EXPLAIN (`q7-pg-explain.txt`):

| relset | goopg est (total) | PG est | actual |
|---|---|---|---|
| `{store_sales+date_dim}` | 3,422 | ~3,441 (1,110/wkr) | 136,830 |
| `{store_sales+customer_demographics}` | **351** | ~10,046 (3,245/wkr) | ~10,282+ |
| `{cd+dd+ss}` | 2 | n/a | — |
| `{cd+dd+item+ss}` | **2** | `pg_est` null | 1,944 (Q7) / 1,957 (Q27) |
| final Gather Merge | 2 | 46 | 1,904 |

goopg's join arithmetic itself is PG-exact once the inputs are right:
`{cd+ss}` = 719,876 x 980 x 4.978e-7 = 351, where 4.978e-7 is precisely
`eqJoinSelectivity` = (1-0.0442)/max(56,970, 1,920,800). The 28.6x
under-estimate at `{cd+ss}` (351 vs PG ~10k) is exactly the `cd.Rows` error
(980 vs ~28k), and it compounds: `{cd+dd+ss}` = 351 x 364 x 1.306e-5 = 1.7,
then `x 18,000 x 5.56e-5 = 2`.

Every upstream level is also PG-faithful: `{ss+dd}` goopg 3,422 vs PG 3,441
(shared ~40x under-estimate vs actual — a separate, upstream-shared error
class), `{dd+p+ss}` 3,273 vs ~3,286.

## Plan shapes

At HEAD goopg and PG pick the IDENTICAL Q7 shape: `Gather Merge -> Sort ->
NL(item) -> NL(cd) -> HJ(p) -> PHJ(ss,dd)` with pk index probes on cd and
item. The EA finding is a pure estimator artifact: the relset
`{cd+dd+item+ss}` exists as a goopg joinrel at rows=2 (`dprel-sizes.txt`
lev=4) but never surfaces as a plan node in either engine.

## Fix surface (filed as M0146-0009b)

`eqSelectivityForColumn` (selectivity.go:376) compares
`mcv.Value == formatExprConstant(literal)` byte-equal. For `bpchar(n)`
columns ANALYZE stamps the padded datum form; the literal is never padded.
PG coerces the const to `bpchar(20)` before `eqsel` runs, so the datums
compare equal. Equivalent fix: bpchar-aware normalization (pad literal to
typmod width, or strip trailing blanks on both sides — PG's bpchar equality
ignores trailing spaces).

Call sites that share the same literal-vs-MCV probe and move together
(sibling-paths rule):

- `selectivity.go:370` — `col = const` scan arm
- `selectivity.go:221` — IN-list / array element probing
- `selectivity.go:1184` — `selectivityEstimate` path (search-side)
- `histCmp` (selectivity.go:643) — range-op ordering against padded MCV /
  histogram bounds misorders `'College'` vs `'College  ...'` (prefix <
  padded). Second-order: only range predicates on char(N>1).
- `cardinality.go:1224`, `:2169` — MCV-vs-MCV pairing in `eqjoinsel` arms;
  same-type pairs are both padded so consistent today; cross-type
  (bpchar vs text/varchar) pairs would miss.

Expected movement: `Q7:customer_demographics+date_dim+item+store_sales`
clears, plus `Q27:customer_demographics+date_dim+item+store_sales`-class
recurrences; any `char(N>1)` equality predicate anywhere in the corpus has
the same blind spot (TPC-DS `char(16)` item/brand/cat ids, `char(20)`
demographic/status columns, etc.).

## Files

- `q7-goopg-ea.txt` — goopg `EXPLAIN (ANALYZE, TIMING OFF)` Q7, private clone
- `q7-pg-explain.txt` — PG 18.3 `EXPLAIN` Q7 on `:65438/tpcds025`
- `dprel-sizes.txt` — goopg `DPTRACE cost lev=` joinrel sizes for the 5-rel
  problem
