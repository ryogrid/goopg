# M0146-0009a — Q7/Q27 four-rel estimate collapse: `char(20)` MCV probe misses blank-padded entries

Status: recon complete 2026-09-28 — root cause verified end-to-end; the fix
is filed as **M0146-0009b** (impl).

## Mandate

M0146-0009a owns the NEW EA-RATCHET finding
`Q7:customer_demographics+date_dim+item+store_sales` (slice-2 capture: est
47, actual 1944, qerr 41.4; `pg_est` null — PG has no node at this relset).
Deliverable: identify which clause's selectivity collapses.

## Finding

**The collapse is not in the join estimator — it is a base-rel MCV lookup
miss on `customer_demographics.cd_education_status` (`char(20)`).**

- ANALYZE stamps `catalog.MCVEntry.Value` via `formatDatumDateStyle`
  (`internal/executor/operators_analyze.go:1534`) — the datum's canonical
  form, which for `bpchar(20)` is **blank-padded**: `"College             "`.
- The planner renders the query literal via `formatExprConstant`
  (`internal/optimizer/selectivity.go:964`) — `*StringConst` gives the raw
  text `"College"`, unpadded.
- `eqSelectivityForColumn` (`selectivity.go:376`) probes
  `mcv.Value == literal` byte-equal → miss.
- The column is MCV-complete (`n_distinct` 7 = `len(MCV)` 7), so
  `remainingDistinct <= 0` and the clause returns `defaultEqSelectivity`
  = **0.005** instead of the measured MCV frequency **0.1418** — a 28.4x
  under-estimate.

`char(1)` columns are unaffected — a one-character literal pads to itself;
`cd_gender='F'` and `cd_marital_status='W'` hit their MCVs exactly.

## Cascade arithmetic (all goopg numbers reproduce bit-close)

| step | goopg | PG-faithful value | check |
|---|---|---|---|
| `cd` filtered rows | 980 | 1920800 x 0.5048 x 0.2022 x 0.005 = **980** | formula-exact given the bad edu sel |
| | | with edu sel 0.1418: **27,807** (PG est 28,038, actual 27,440) | |
| `{cd+ss}` | 351 | 719876 x 980 x 4.978e-7 | `eqJoinSelectivity` = (1-0.0442)/max(56970, 1920800) — PG formula, right inputs would give ~10,046 = PG's own est |
| `{cd+dd+ss}` | 2 | 351 x 364 x 1.306e-5 = 1.67 | `{dd+ss}` sel also PG-faithful |
| `{cd+dd+item+ss}` | 2 | 2 x 18000 x 5.56e-5 = 2 | the EA finding (rows=47 in the older capture, rows=2 at HEAD after plan churn) |

Everything above `cd.Rows` divides cleanly by the same 28.4x factor — one
broken conjunct explains the whole finding, and explains the identical Q27
finding signature (`cd_education_status='Secondary'`, same column, same miss;
goopg Gather Merge rows=2 vs actual 1,957).

## PG anchor

`eqsel` → `var_eq_const` (`postgres/src/backend/utils/adt/selfuncs.c`)
probes the MCV array with `datumIsEqual`-style typed equality after the
const has been coerced to the column type — `'College'::bpchar(20)` pads to
width 20 and matches. PG's `bpchar` equality treats trailing blanks as
insignificant, so equivalently the compare may strip trailing spaces on
both sides.

## Fix surface for M0146-0009b

Primary: `eqSelectivityForColumn` needs the column's type (and typmod width)
so the literal and/or MCV side can be normalized for `bpchar` before the
byte-equal probe — the function currently receives `(stats, val, tuples)`
and loses the type at the boundary.

Sibling-path audit (must move together per the sibling rule):

1. `selectivity.go:370` — `col = const` scan arm.
2. `selectivity.go:221` — IN-list element probing (`ANY`/`IN`).
3. `selectivity.go:1184` — `selectivityEstimate` (search-side twin).
4. `histCmp`/`rangeOpSelectivityStats` (`selectivity.go:643`) — range
   ordering against padded MCV/histogram entries misorders a literal that
   is a prefix of its padded form; lower-impact second sibling.
5. `cardinality.go:1224`, `:2169` — MCV-vs-MCV pairing in the eqjoinsel
   arms; same-type pairs are both padded today (consistent), cross-type
   bpchar-vs-text pairs would miss.

Normalization choice: right-padding the literal to the column width
mirrors PG's coercion site most literally; stripping trailing blanks from
both operands mirrors `bpchar` equality semantics and is typmod-free.
Either is defensible — decide at impl; the MCV side is the padded form
regardless of which side moves.

## Expected movement

EA-RATCHET: `Q7:customer_demographics+date_dim+item+store_sales` clears
(the `Q27` sibling relset clears with it if it re-fires). Beyond the
finding: every `char(N>1)` equality predicate corpus-wide shares the blind
spot — TPC-DS `i_item_id`/`i_brand_id`/`i_category_id` (char(16)),
`s_*`/promotion/customer char columns, etc. — so the measurement on
M0146-0009b is the full `make ea-ratchet` sweep, not just Q7.

## Evidence

`analysis/m0146/m0146-0009a/` — captured plans, DPREL joinrel sizes,
per-conjunct arithmetic.
