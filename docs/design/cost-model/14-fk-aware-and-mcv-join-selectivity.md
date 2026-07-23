# 14 — Uniqueness/FK-aware + MCV join selectivity, and PG-faithful statistics

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-23 |
| depends on | [05](05-statistics-and-estimation-inputs.md), [06](06-scan-and-join-path-costs.md), [07](07-cost-driven-join-order.md), [12](12-pg-style-join-path-enumeration.md) |
| relates to | [13](13-composite-nli-layout-reconciliation.md) (the same Q9 composite key, a different failure) |
| premise | join-ORDER quality is dominated by intermediate CARDINALITY, so accurate cardinality fixes the order even under goopg's crude linear cost model — the non-linear per-row cost is a separate (method-selection) axis |

## 0. Why this chapter exists

After the pushdown + fan-out-NLI-veto work (commits `17d11216`, `0c4b7b9b`) the
cost-driven planner reaches ~target on most of TPC-H, but **Q9 still times out**. Q9 is a
pure join-**ORDER** failure, not a method or filter-placement one: `estimateJoinCost`
(`bushy.go:1064`) estimates the composite `partsupp ⋈ lineitem` join — joined on the two-
column key `ps_partkey=l_partkey AND ps_suppkey=l_suppkey` — by dividing `|L|·|R|` by a
**single** column's NDistinct, producing a 7.4-billion-row intermediate estimate (EXPLAIN,
measured) where the true result is ~6M. The DP then picks an order that materialises a
huge intermediate.

Earlier attempts to "make cardinality accurate" (a global NDistinct-resolution fix plus a
crude multiply-by-both-NDVs divisor) were **net-negative**: they changed *every* join's
estimate and the crude divisor was itself *wrong* (it assumes column independence, which is
false for a correlated composite key — it under-estimates the composite join, see §4).
This chapter designs a **surgical, PostgreSQL-faithful** cardinality path that corrects
only the specific over-estimated joins, plus the two statistics-infrastructure improvements
(binary stat values, correlation) that round goopg's ANALYZE toward PG.

The controlling insight (why this can succeed where constant-calibration failed):

> **Join-order selection is cardinality-dominated, not constant-dominated.** goopg's cost is
> `out·1 + build·4 + probe·1` (`bushy.go:952`, `satCost`), so `cost(O1)/cost(O2)` is *not* an
> exact cardinality ratio — the `build·4` weight multiplies whichever input is smaller, which
> differs per order. It is instead *bounded* by the max cost-weight ratio: two orders whose
> intermediate sizes are within roughly a **6×** band can be flipped by the weight
> asymmetry, but beyond that the cardinality ratio dominates. Q9's good vs bad orders differ
> by ~10⁴ in intermediate size — far outside the ~6× band, and far outside any
> linear-vs-non-linear per-row error (goopg's GC cost is ~2–10× PG's). So **accurate
> cardinality fixes Q9's order even under goopg's crude linear cost model.** The non-linear
> per-row cost bites *method* selection (NLI vs hash — same output cardinality, decided by
> the constants), a separate axis handled by the fan-out veto (`nl_index_join.go`).
>
> **Honest boundary:** for orders within the ~6× band (or where the per-row GC non-linearity
> matters), accurate cardinality *can* still flip to a goopg-slower order. Such a flip is
> *diagnostic* of the cost-constant axis, not a reason to distrust accurate cardinality —
> the §8 regression sweep is where this is caught, not waved away.

## 1. What goopg estimates today, and against what oracle

`estimateJoinCost` (`bushy.go:1064`) and the display-path sibling `estimateJoin`
(`cardinality.go:192`) both compute the classic textbook equijoin size over **one** column
pair:

```
outputRows = |L| · |R| / max( NDistinct(L.key), NDistinct(R.key) )
```

This mirrors PostgreSQL's `eqjoinsel` base case (`selfuncs.c`, `eqjoinsel_inner`:
`selec = 1 / max(nd1, nd2)`), but PG refines it with three things goopg drops entirely:

1. **Uniqueness / FK containment** — a join to a provably unique/PK key emits ≤1 match per
   outer row (**no fan-out**), output ≈ `outer · inner / raw-unique-count`. In PG this is
   *not* an `eqjoinsel_inner` special case: it emerges from `get_variable_numdistinct`
   setting `nd = ntuples` when `isunique` (`selfuncs.c:6338`), where `isunique` comes from a
   **single-column** unique index (`has_unique_index`, `plancat.c:2244`). For a *composite*
   key PG instead relies on `get_foreign_key_join_selectivity` (`costsize.c:5650`, via
   `calc_joinrel_size_estimate`). §2 details how goopg substitutes composite-unique-index
   detection where the FK is absent.
2. **Multi-clause selectivity** — for a multi-column join PG multiplies the per-clause
   selectivities (`clauselist_selectivity`), i.e. divides by the *product* of the columns'
   NDVs, not one column.
3. **MCV-list matching** — `eqjoinsel_inner` matches the two columns' most-common-value
   lists exactly for the MCV-covered mass and uses NDistinct only for the remainder.

The read-only PG-18.3 tree under `postgres/` is the oracle for every formula below
(`src/backend/utils/adt/selfuncs.c`, `src/backend/optimizer/path/costsize.c`,
`src/backend/commands/analyze.c`).

**Metadata reality on the loaded TPC-H data (measured 2026-07-23):** `pg_constraint` has
**0** primary-key and **0** foreign-key rows — the HammerDB loader's index step ran but its
`ADD CONSTRAINT` step did not persist into this data dir. However the **unique PK indexes
are present and flagged unique** (`pg_index.indisunique='t'`): `partsupp_pk` on the
composite `(ps_partkey, ps_suppkey)`, `lineitem_pk`, `orders_pk`, etc. This drives the
design's priority order (§2 before §3): **composite-unique-index detection works on the data
we have; FK-constraint detection needs FKs that are not currently declared.** Note this
inverts PG's own reliance — PG gets composite no-fan-out from the FK (§3), *not* from a
unique index (`has_unique_index` is single-column, `plancat.c:2244`); §2 is goopg's
extension for the FK-less case (§9).

## 2. Unique-key-aware no-fan-out (the primary Q9 fix)

**How PG actually reaches no-fan-out (corrected per review).** `eqjoinsel_inner` has NO
unique-side branch (`selfuncs.c:2444-2632` is only MCV-vs-MCV vs the plain
`MIN(1/nd1,1/nd2)`). Uniqueness enters one level up: `get_variable_numdistinct`
(`selfuncs.c:6338`) does `if (vardata->isunique) stadistinct = -1` ⇒ `nd = ntuples`
(`:6365`); the ordinary MIN formula then divides by that raw count, so no-fan-out is
*emergent from `nd = raw tuple count`*, not a special case. And `isunique` is set by
`has_unique_index` (`selfuncs.c:5334`), which requires a **single-column** index
(`plancat.c:2244`, `nkeycolumns == 1`). So for Q9's two-column `partsupp_pk` PG's
`eqjoinsel` sees *neither* column as unique — real PG gets Q9's composite no-fan-out
**only** from the FK path (§3, `get_foreign_key_join_selectivity`, `costsize.c:5650`).

**goopg's substitute (a deliberate extension beyond `eqjoinsel`).** The loaded data has no
FKs but *does* carry composite unique PK indexes (§1), so goopg recognises composite unique
indexes directly to obtain the same no-fan-out PG gets from the FK, matching
`get_variable_numdistinct`'s `isunique ⇒ nd = ntuples`:

```
if some UNIQUE index on side S has Columns ⊆ (equated key columns on S):   // superkey test
    nd = raw_tuple_count(S)            // S's UNFILTERED cardinality (Stats.RowCount)
    outputRows = |L_filt| · |R_filt| / nd
```

Two corrections from the review, both load-bearing:
1. **Divisor is the RAW (unfiltered) unique-side count — not "output = other-side rows."**
   PG divides by the raw referenced count (`fkselec = 1/ref_tuples`, `costsize.c:5844`); when
   the unique side is *filtered* (`|R_filt| < raw`), output = `|L_filt|·|R_filt|/raw <
   |L_filt|` (a real match fraction). Q9 filters `partsupp` via the upstream `part`-name
   join, so the naive "output = lineitem rows" over-estimates; dividing by the raw
   `partsupp` count is correct.
2. **Superkey (⊆), not set-equality.** Fire when a unique index's columns are a *subset* of
   the equated key set. Strict equality would miss an `(a,b,c)`-unique index under an
   `(a,b,c,d)`-equated join; extra equated columns beyond the key add further per-clause
   selectivity (§4).

**goopg mapping.** In `estimateJoinCost`, gather the column set of ALL edges between the two
joined subsets (§4 enumeration), resolve each side's `*catalog.Table` via `g.tables[…]`, and
test with `cat.IndexesOnTable(tbl)` whether either side has an `idx.Unique` index whose
`idx.Columns ⊆` that side's key set. `catalog.Index` (`catalog.go:1648`) carries
`Columns`/`Unique`/`Primary`. On a match, divide by that side's raw `Stats.RowCount`.
**dbOid hazard:** call `IndexesOnTable` through the same `SearchPathCatalog` the planner
uses — the bare `InMemory` resolves `DefaultDBOid`'s indexes regardless of the active DB
(`catalog.go:22961`), misfiring uniqueness in a multi-DB scope.

**Why "surgical" — and its honest limit.** Surgical means the *mechanism* fires only on a
proven superkey, never on a guessed constant. It does **not** mean "few queries change":
TPC-H is all FK-chains onto unique PKs, so §2 corrects Q7/Q8/Q10/Q2/Q5 joins too — the same
breadth as the earlier blanket fix. The difference is that §2's estimate is *correct* (the
blanket divisor was *wrong* — it under-estimated, §4) and it never touches a *non-*superkey
join. A *correct* estimate can still flip an order the linear cost model then mis-costs (the
§0 boundary), so the §8 regression sweep is **load-bearing, not a formality**: the design
claims "no *wrong* estimate," not "no trade."

## 3. FK-aware join selectivity (complementary; PG `get_foreign_key_join_selectivity`)

When declared FKs exist, PG uses them directly (`selfuncs.c
get_foreign_key_join_selectivity`, called from `calc_joinrel_size_estimate`): a validated,
enforced FK from child→parent means the child is fully contained, selectivity =
`1 / rows(parent key)`, output ≈ child rows. This is the same no-fan-out outcome as §2 but
keyed on the constraint rather than a unique index — it additionally covers the case where
the parent key is unique by declaration but has no matching index.

**goopg mapping.** `catalog.Table.ForeignKeys []ForeignKey` (`catalog.go:483`; struct
`catalog.go:1443` carries `Columns`, `RefTable`, `RefColumns`, `NotValid`, `NotEnforced`).
Match the edge column-set against a child-side FK whose `Columns` equal the child key set
and whose `RefTable`/`RefColumns` equal the parent side; **gate on
`!fk.NotValid && !fk.NotEnforced`** (PG only trusts valid+enforced FKs for estimation);
resolve empty `RefColumns` against the parent's PK — and since the loaded data has **0 PK
constraints** (§1), "parent PK" itself resolves to the parent's unique *index*, so §3's
parent-key resolution reduces to the same unique-index probe as §2. **Status:** the loaded
TPC-H data has no
FKs (§1), so §2 is the mechanism that fires today; §3 is implemented as an equivalent branch
for schemas that *do* declare FKs (and is what a corrected TPC-H load should use). §2 and §3
agree on the no-fan-out output; whichever proves the uniqueness first wins.

## 4. Multi-clause composite-key selectivity (fallback; PG `clauselist_selectivity`)

When neither §2 nor §3 proves uniqueness, a multi-column join is still tighter than one
column. PG multiplies per-clause selectivities; goopg should divide `|L|·|R|` by the
**product** of the spanning edges' per-column `max(NDV_left, NDV_right)`:

```
outputRows = |L|·|R| / Π_edges max( NDistinct(edge.leftKey), NDistinct(edge.rightKey) )
```

**Edge enumeration.** `findEdgeBetweenIdx` (`bushy.go:510`) returns only the *first* edge
between two subsets; the estimator must instead scan `g.edges` for every edge whose
`{leftTable,rightTable}` straddles the two subset masks (the same predicate
`buildJoinFromDP`/`attachExtraEdgesLocal` already use to mark edges consumed) and combine
their NDVs. Reuse `sideKeyDistinct`/`accurateKeyDistinct` (`bushy.go:985-1027`) for each
column's unsaturated NDV.

**Honest limitation (why §4 is only a fallback).** The clause product assumes column
independence. For a correlated composite key it *under*-estimates: Q9's product is
`6M·800k/(200k·10k) = 2400`, far below the true 6M. That is why §2/§3 (uniqueness) — which
give the correct 6M — take precedence; §4 is used only where no uniqueness proof exists,
and under-estimating a non-unique multi-column join is far less harmful than the current
over-estimate. (PG has the same independence limitation and relies on the same uniqueness
short-circuit.)

## 5. MCV-based eqjoinsel (Phase 2 / optional; PG `eqjoinsel_inner`)

For the mass covered by both columns' MCV lists, PG computes the exact matched frequency
(sum over equal MCV values of `freq1·freq2`) and applies the NDistinct formula only to the
non-MCV remainder. goopg's ANALYZE already collects MCV lists with frequencies
(`ColumnStats.MCV []MCVEntry`, `operators_analyze.go:503-549`) — unused by both estimators
today. **Implement only if §2+§4 do not fully recover Q9**, since TPC-H's decisive joins are
key/FK joins that §2 already makes exact. Built on the §6 binary values so cross-column
equality is a typed byte comparison for the types with a true binary codec (int/float/date/
varchar). **Numeric is a known gap:** §6's codec stores numeric as varlena *text* bytes
(no `numeric_send`), so numeric-key MCV matching would still be a text compare and could
under-count matches PG's `numeric_eq` calls equal (`1.50` vs `1.5`) — flag and skip numeric
MCV matching until a `numeric_send` codec exists.

## 6. PG-faithful binary statistics representation (user-requested)

**Problem.** goopg stores MCV values and histogram bounds as `Datum.Format()` **text**
(`ColumnStats.MCV[].Value string`, `.Histogram []string`; `operators_analyze.go:544/590`).
This is documented (`catalog.go:1613-1617`, `docs/design/0006-0001-...md:146-159`,
`pgstats.go:27-30`) as an explicit **deviation from PostgreSQL** chosen only so the catalog
package need not import the executor's `Datum` type — *not* for any PG-compatibility reason.
PG stores `pg_statistic.stavaluesN` as an anyarray of the column's own type.

**Change.** Store the values in the column type's PG binary send-format:
`MCVEntry.Value string → []byte`, `Histogram []string → [][]byte`, produced by the executor
codec `encodeValuePG` (`codec.go:173`) and decoded by `decodePhysicalPGValueMctx`
(`codec.go:969`) — both already round-trip int2/4/8, float4/8, date (`date_send`-faithful),
and varlena char/varchar. `[]byte` is a builtin, so the catalog still never imports `Datum`.

**Layering solution.** The two consumers that read the text today —
`catalog/pgstats.go` (renders `most_common_vals`/`histogram_bounds`) and the planner's
`internal/planner/selectivity.go` (`eqSelectivityForColumn` `==`, `rangeOpSelectivity`
/`histCmp`) — cannot decode `[]byte` without the executor. Introduce a small **stat-value
codec interface** declared in the catalog package:

```go
type StatValueCodec interface {
    Format(v []byte, t Type) string          // []byte → display text (pg_stats)
    Compare(a, b []byte, t Type) int         // ordered compare (histogram bounds)
    EncodeLiteral(text string, t Type) []byte // query literal → same binary (equality)
}
```

implemented in the executor (backed by `decodePhysicalPGValueMctx` + `formatDatumDateStyle`
+ `encodeValuePG`) and injected at startup. `pgstats.go` and `selectivity.go` call the
interface; the pg_statistic heap builder (`pg18_user_catalog_rows.go:1295`) emits a real
element-type anyarray instead of the current `text[]`.

**Honest caveat.** goopg's codec stores **numeric** as varlena *text* bytes, not PG's
`numeric_send` short-int binary (`codec.go:520`). So numeric statistics remain text-shaped
bytes — "as PG-faithful as the codec allows"; a true `numeric_send` codec is a noted
follow-up, out of scope here.

**Invariant.** The binary must round-trip to the *same* `pg_stats` display text and the
*same* equality/range selectivity — no plan and no view output may change (the 608
regression anchors and the pg_stats/selectivity suites are the gate).

## 7. Correlation statistic (user-requested; PG `STATISTIC_KIND_CORRELATION`)

goopg does not collect column correlation (`pg_stats.correlation` is always NULL,
`pgstats.go` idx 10). Add `ColumnStats.Correlation float64`, computed in `computeColumnStats`
mirroring PG's `compute_scalar_stats` (`analyze.c:2494-2543,2875-2882`). **Not a free
reuse:** goopg's current stats path bucketises into distinct-value→count structures and
sorts *by count* (MCV) then expands non-MCV *by value* — **both discard physical row
position**, which correlation needs. So this requires a *new* retained array of
`(value, physicalSampleIndex)` over **non-null** values only, value-sorted with a
physical-index (`tupno`) tie-break (PG's `compare_scalars` / `tupnoLink`), then the
closed-form `corr = (n·Σ(i·tupno) − Σi·Σtupno) / sqrt(...)` per `analyze.c:2875-2882`. The
per-type ordering comes from the same §6 codec `Compare`. Expose in `pg_stats.correlation`
and the pg_statistic heap `stanumbers` slot for kind 3.
PG uses correlation in `cost_index` (`costsize.c`) to interpolate index scans between
sequential and random cost; **goopg collects and exposes it now; cost-model adoption is a
later step** (this chapter does not change any cost using it).

## 8. Surgical scoping, sibling-estimator reconciliation, and acceptance

- Apply §2–§5 in `estimateJoinCost` (`bushy.go`, the DP's internal cost). The display
  sibling `estimateJoin`/`EstimateRows` (`cardinality.go:192`) also single-column-divides,
  but it is **shared and UNgated** — `EstimateRows` (`cardinality.go:70`) feeds the NL-index
  **fan-out veto** (`nl_index_join.go:1252,1375`), the legacy planner build-side
  (`planner.go:2077`), pushdown build-side + `IsSmallDimensionSide` (`pushdown.go:241,251`),
  and memoize (`memoize.go:114`). So a naive change there would perturb the very fan-out veto
  §0 calls "a separate axis" and would silently change the *non*-cost-driven planner.
- **Scope EVERY join-selectivity change (in both `estimateJoinCost` and any `estimateJoin`
  branch) behind `costDrivenJoinOrder`.** Only then does "production integer DP + veto +
  memoize byte-identical" actually hold. If EXPLAIN-`rows=` parity between the DP and the
  display path under cost-driven is wanted, add the corrected branch to `estimateJoin`
  *inside* a `costDrivenJoinOrder` guard and MEASURE the veto/legacy/memoize impact
  explicitly (they must not shift); do not change the ungated path. §6/§7 are
  planner-agnostic stats-infrastructure and apply to both planners.
- **Acceptance (Tier-2 self-relative, per [09] §2):** Q9 recovers from timeout to ~its
  past-fastest serial (≈27s) on real SF1, and **no query regresses** — with explicit
  attention to the earlier trade victims Q7 (28.5s), Q8 (3.3s), Q10 (15.7s), Q2, Q5 (26s).
  A regression is triaged: a cardinality error (surgical-scoping bug — fix) vs a
  cost-constant flip (the §0 boundary — document, do not paper over with a heuristic).
  Correctness anchors ride along: vs-PG `TestTPCHResultParity` identical on all 22,
  `tpch-spotcheck` Q12=2/Q13=33. For §6/§7: the pg_stats/pg_statistic/selectivity suites
  stay green and `pg_stats.correlation` becomes populated in [-1,1].

## 9. Divergence from PostgreSQL

- **Numeric statistics are text-shaped bytes**, not `numeric_send` binary (§6 caveat) — the
  codec has no `numeric_send`. Allowed; noted follow-up.
- **goopg proves COMPOSITE-key uniqueness from a multi-column unique index (§2); PG does
  not.** PG's `eqjoinsel` uniqueness (`get_variable_numdistinct` `isunique`) is set only by a
  **single-column** unique index (`has_unique_index`, `plancat.c:2244`, `nkeycolumns==1`);
  PG's composite no-fan-out comes exclusively from `get_foreign_key_join_selectivity`
  (`costsize.c:5650`). goopg's §2 is a deliberate **extension** — it reads the composite
  unique index directly — because the loaded data declares the unique PK *indexes* but not
  the PK/FK *constraints* (§1). Same no-fan-out outcome and the same `nd = raw-tuple-count`
  arithmetic; different (broader) provenance.
- **Correlation is collected but not yet used in any cost** (§7) — PG uses it in
  `cost_index`. goopg defers the cost adoption; this is a temporary asymmetry, not a
  permanent divergence.
- **Extended/multivariate statistics** (`pg_statistic_ext` ndistinct/dependencies) that
  would capture composite-key correlation directly are NOT collected; §2's uniqueness
  short-circuit is the goopg substitute for the decisive TPC-H case — which is how PG itself
  handles it (uniqueness/FK, not multivariate stats).
