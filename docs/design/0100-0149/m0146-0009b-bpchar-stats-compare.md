# M0146-0009b — bpchar truelen semantics in statistics comparisons

Status: landed 2026-09-28

## Context

M0146-0009a (`m0146-0009a-bpchar-mcv-eqsel-miss.md`, evidence under
`analysis/m0146/m0146-0009a/`) measured TPC-DS Q7/Q27's four-rel joinrel
`{customer_demographics+date_dim+item+store_sales}` estimating rows=2 where
the gate's pinned ANALYZE corpus actually produces ~1,900. The collapse was
a **base-rel MCV probe miss**, not join math:

- `cd_education_status` is `char(20)`. ANALYZE stamps `MCV.Value` with the
  padded datum form (`"College             "`, via `formatDatumDateStyle`),
  while `formatExprConstant` renders the query literal `"College"`.
- `eqSelectivityForColumn` byte-compared the two, missed, and the
  MCV-complete column fell through to `DEFAULT_EQ_SEL` (0.005):
  `1920800 × 0.005 = 9604` vs the MCV-implied `~0.1418 × 1920800 ≈ 272k`
  (28.4x). The filtered `cd` scan then read 980 rows vs PG's ~27–28k, and
  every joinrel above it inherited the under-estimate with otherwise
  PG-exact arithmetic.

The planner-side twin of M0146-0015f's executor work: that task made the
executor's comparisons trailing-blank-insensitive everywhere a declared
bpchar operand is visible (`comparisonOperandsAsBpchar`/`bcTruelen`); this
task applies the same PG semantics to the **statistics** side, where the
probe is a rendered literal vs a stored sample value.

## PG semantics (citations)

- `bpchartruelen` — `postgres/src/backend/utils/adt/varchar.c:675`:
  trailing blanks are stripped before comparison.
- `bpchareq` — varchar.c:743: equality compares truelen values.
- `bpcharcmp` — varchar.c:909: ordering compares truelen values.
- `var_eq_const`/`eqsel` — `selfuncs.c`: the const is coerced to the
  column's type before the MCV probe, so a `bpchar(N)` literal vs a padded
  MCV datum compares by truelen — the behavior this file reproduces at the
  string level.

## Decision

Never pad the literal. Compare under truelen, exactly as `bpchareq`/`bpcharcmp`:

- `isBpcharTypeName(typeName)` — the `char`/`bpchar`/`character` family.
- `statLiteralEqual(stamped, literal, typeName)` — `TrimRight(…, " ")` on
  both sides for bpchar, byte-equal otherwise. Used by every
  literal-vs-MCV probe.
- `histCmp` / `bucketFraction` — truelen-strip bounds and literal before
  ordering/interpolation for bpchar columns (histogram bounds are
  statistics-stamped, i.e. padded).
- MCV-vs-MCV join pairing (`eqjoinselSemiCore`, `eqjoinselInnerMCV`) —
  `bpchar = bpchar` resolves to `bpchareq`, so lists pair on truelen keys
  when **both** sides' catalog types are bpchar-family; any other pairing
  stays byte-equal.

Type names are threaded explicitly: `eqSelectivityForColumn` and
`indexKeyEqSelectivity` take `typeName`; catalog-path callers resolve via
the new `columnTypeByName` (a stats row carries no type — ColumnStats has
no type field); join-key pairing derives it with `joinKeyTypeName`
(`*ColumnRef.Type.Name` / `*OuterColumnRef` → `resolveBaseColumn`).

## Fix surface (all sites moved together)

- `selectivity.go`: `eqSelectivityForColumn` (scan arm :~415, IN-list
  element probe :~218, `selectivityEstimate` :~1238 all share it),
  `histCmp`, `bucketFraction`.
- `joinselectivity.go`: `orEqualitySelectivity`, `orInListSelectivity`,
  and `eqJoinSelectivitySemi` (`joinVarStats.typeName` already carried
  the type).
- `pathindexrestrict.go` / `pathbitmap.go`: `indexKeyEqSelectivity` gained
  `typeName`; `patternsel.go`: exact-pattern and prefix-eq arms.
- `cardinality.go`: `eqjoinselSemiCore` (type params from all three
  callers) and `eqjoinselInnerMCV` (truelen pairing keys).

`joinKeyTypeName` is registered as a `nonRecursiveClassifier` in
`exprwalk_inventory_test.go` — it inspects only the top expr node and
fails closed (`""` → byte-equal), so the RC-1a hazard does not apply.

## Measurement

Before → after (private SF0.25 clone, pinned ANALYZE):

| site | before | after | PG oracle |
|---|---|---|---|
| `cd_education_status='College'` | 9,604 | **272,433** | ~274k est / 27,440 filtered actual |
| 3-conjunct `cd` scan | 980 | **27,805** | 28,038 |
| Q7 Gather Merge | rows=2 | **rows=45** | rows=46 |
| Q27 Gather | rows=2 | **rows=45** | — |

Corpus (baseline HEAD `e7683534c` vs candidate):

- TPC-DS SF0.25 plan parity: **MATCH 12 → 15** (Q12, Q20, Q98 flipped;
  zero MATCH losses). Q27 narrowed to fewer divergence categories; Q73's
  move into Nested Loop converged *toward* PG (categories 7 → 4).
- TPC-DS SF0.25 fire set: 25 changed plans, all executed PASS both arms.
- TPC-DS SF1: zero plan changes.
- TPC-H: floor capture match=6 (floor ≥3, held); acceptance-arm digest
  24/24 value-identical.
- `make ea-ratchet`: the Q7/Q27 target findings cleared
  (`FIXED Q27:customer_demographics+date_dim+item+store_sales`, the Q7
  relset no longer flagged). 5 NEW findings surfaced (Q78, Q83, Q85×3) —
  **all on plans byte-identical baseline-vs-candidate**, i.e. pre-existing
  drift unmasked by fixing the gate environment (see below); filed as
  M0146-0009c with ledger rows.

## Gate-integrity finding (filed M0146-0009d)

`estimate-parity-gate.sh` reported a vacuous PASS ("52 fixed") twice this
loop because port 5534 was held by a **foreign PostgreSQL** (pid 1748354,
not the gate's own server): `pg_isready` answered, `start_server`
short-circuited, and all 99 captures were `relation does not exist` ERRORs
→ 0 nodes scored → every baseline finding trivially "FIXED". A stale
`tmp/c20a/data-sf025` clone hid the same fault earlier. The gate needs a
foreign-server check (verify the served binary/datadir) and a non-empty
capture floor (0 scored must never print PASS/FIXED).
