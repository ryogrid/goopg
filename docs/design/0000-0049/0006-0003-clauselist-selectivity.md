# Clauselist Selectivity (Milestone 0006)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0006 — Planner-Grade Statistics                        |
| Refines    | [0003-0003-statistics-and-cardinality.md](0003-0003-statistics-and-cardinality.md), [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md) |
| Supersedes | —                                                      |

## Problem

`0006-0001` populates per-column MCV lists and equi-depth histograms;
`0006-0002` makes them survive a clean stop / start. But the planner
still ignores them. `EstimateRows` for `*Filter` collapses every
predicate to a flat `1/3` selectivity (`defaultGenericSelectivity`) —
the same fraction `WHERE l_orderdate < date '1995-01-01'` produces as
`WHERE 1=1`. The cost-based planner reads through this estimate when
it picks join orders and join algorithms, so leaving it untouched
strands the data we now collect.

## Decision

Replace the flat `1/3` Filter multiplier with a recursive
`clauseSelectivity` walker shaped after upstream's
`postgres/src/backend/utils/adt/selfuncs.c:clauselist_selectivity`:

| Predicate shape                       | Selectivity formula                                     |
| ------------------------------------- | ------------------------------------------------------- |
| `col = const`                         | MCV-frequency lookup, then `(1-Σ MCV freq)/NDistinct(non-MCV)`, then `1/200` |
| `col <> const` / `col != const`       | `1 - eqSel(col, const)`                                 |
| `col < const`, `<=`, `>`, `>=`        | Histogram-bucket interpolation (boundaries are sorted)  |
| `col BETWEEN a AND b`                 | Already desugars to `(col >= a) AND (col <= b)` (M0003) |
| `col IN (v1, v2, …)`                  | `Σ eqSel(col, vi)` capped at 1.0                        |
| `expr1 AND expr2`                     | `sel(expr1) * sel(expr2)` (independence assumption)     |
| `expr1 OR expr2`                      | `sel(expr1) + sel(expr2) - sel(expr1)*sel(expr2)`       |
| `NOT expr`                            | `1 - sel(expr)`                                         |
| Unrecognised shape                    | `1/3` (`defaultGenericSelectivity` — the M0003 fallback) |

Both sides of every comparison are normalised at the entry point — if
the comparison is `const op col`, swap into `col op const` form so the
per-shape branches always see ColumnRef on the left. `col op col2`
(two-column predicates) falls through to the `1/3` constant; that
shape is rare in the TPC-H workload and upstream itself uses a
specialised path that's out of scope here.

### MCV / histogram lookup details

- **MCV equality.** Iterate `ColumnStats.MCV`; if any `Value` matches
  the formatted constant, return its `Frequency`. The constant is
  rendered through the executor's `Datum.Format()`-equivalent helper
  (already exposed for INSERT VALUES paths) so the comparison is
  string-equal to whatever `0006-0001` stamped at sample time.
- **Non-MCV equality.** Sum the MCV frequencies, subtract from 1 to
  get the non-MCV mass, and divide by the non-MCV NDistinct
  (`NDistinct - len(MCV)`). When `NDistinct == len(MCV)` the
  non-MCV bucket is empty — fall back to `1/200`.
- **Histogram inequalities.** With histogram boundaries `b[0], b[1],
  …, b[k]`, the selectivity of `col < c` is `i/k + (c - b[i]) /
  (b[i+1] - b[i]) / k` for `b[i] <= c < b[i+1]`. Boundaries cover
  the non-MCV portion only; multiply the result by `(1 - Σ MCV
  freq)` to scope it to that mass, then add the contribution from
  MCV values that satisfy the predicate. `col <=`, `>`, `>=`
  derive from the same machinery.
- **Numeric comparison of histogram boundaries.** Boundaries are
  stored as `Datum.Format()` strings; planner-side parsing reuses
  the existing literal-parsing helpers. Integer / numeric / time
  / string comparisons each take the natural per-type order. Bool
  histograms are degenerate (max two boundaries) and don't help
  enough to justify special handling — the equality / inequality
  branches do all the work for booleans.

### Surface: a single new file `internal/planner/selectivity.go`

The pass is implemented as `clauseSelectivity(expr Expr, child Node)
float64` returning a value in `[0, 1]`. `cardinality.go`'s `*Filter`
case becomes:

```go
case *Filter:
    child := EstimateRows(x.Child)
    if child <= 0 {
        return 0
    }
    return scaleByFloat(child, clauseSelectivity(x.Predicate, x.Child))
```

`columnStatsForChild(idx int, child Node) *catalog.ColumnStats` is the
new helper that walks the child tree the same way `columnNDistinctFor
Child` already does, but returns the full ColumnStats so the
selectivity rules can read MCV and Histogram directly.

### Why hashable-string comparison for MCV equality is correct

`0006-0001` stores MCV `Value`s as `Datum.Format()` output. A literal
like `'F'` parses to a `StringConst{Value: "F"}`; a literal like `1995`
parses to an `IntegerConst{Value: 1995}` whose `Format` is `"1995"`.
The planner-side rendering helper `formatExprConstant(expr Expr)`
mirrors that — same byte sequence, so `==` is the right matcher for
the MCV lookup. The handful of tricky cases (NUMERIC trailing zeros,
TIME format) round-trip through Format; they're stable across the same
process and across snapshot reload.

### Out of scope

- **Two-column predicates** (`a.x = b.x` outside JOIN context, `a.x <
  a.y`). Upstream uses functional dependencies for these; v0 keeps
  the fallback constant.
- **`IS NULL` / `IS NOT NULL`** as a recognised shape — these go through
  a `nullTest`-shaped AST node v0 doesn't yet emit. Falls through to
  the constant.
- **`LIKE` selectivity.** Upstream extracts a prefix and uses
  histogram interpolation on it. v0 keeps `1/3` for LIKE; the prefix-
  to-range translation is its own design doc.
- **Selectivity caching.** Each Filter recomputes from the catalog.
  TPC-H plans are tiny enough that caching adds complexity without
  payback at this scale.

## Verification

`internal/planner/selectivity_test.go`:

- `TestSelectivityEqualityHitsMCV`: a column with MCV `[("F", 0.8),
  ("O", 0.15)]` and `WHERE label = 'F'` → selectivity 0.8.
- `TestSelectivityEqualityFallsThroughMCV`: same column, `WHERE
  label = 'P'` (not in MCV, NDistinct=3, MCV mass=0.95) →
  selectivity `(1-0.95)/(3-2) = 0.05`.
- `TestSelectivityRangeUsesHistogram`: numeric column with
  histogram `[1, 100, 200, 300, 400, 500]` and `WHERE id < 200` →
  selectivity ≈ 0.4 (two of five buckets, modulo MCV scoping).
- `TestSelectivityAndProductRule`: `(a AND b)` selectivity is the
  product of the two leaf selectivities.
- `TestSelectivityOrInclusionExclusion`: `(a OR b)` selectivity is
  `a + b - a*b`.
- `TestSelectivityFallsBackToOneThirdWhenNoStats`: the same
  predicate against an unanalysed table still produces the
  `defaultGenericSelectivity` answer, so the existing M0003
  rules-only behaviour is the documented fallback.

The existing M0003 tests for `EstimateRows` against `*Filter` (which
expected `child / 3`) are updated to use unanalysed tables so they
continue to exercise the fallback path; new tests cover the
stats-aware path.

## Cross-references

- M0006 milestone: `docs/milestones/0006-planner-statistics.md`.
- Upstream selfuncs:
  `postgres/src/backend/utils/adt/selfuncs.c` —
  `clauselist_selectivity`, `eqsel`, `scalarineqsel`,
  `histogram_selectivity`, `mcv_selectivity`.
- The data this loop consumes:
  [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md).
- The persistence path so the planner sees stats after restart:
  [0006-0002-stats-persistence.md](0006-0002-stats-persistence.md).
