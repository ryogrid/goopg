# Sampling, MCV Lists, and Equi-depth Histograms (Milestone 0006)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0006 — Planner-Grade Statistics                        |
| Refines    | [0003-0010-analyze-statistics.md](0003-0010-analyze-statistics.md), [0003-0003-statistics-and-cardinality.md](0003-0003-statistics-and-cardinality.md) |
| Supersedes | —                                                      |

## Problem

M0003's `analyzeRelation` walked every visible heap tuple and tracked
per-column distinct sets in memory, producing only `RowCount`, `Pages`,
`AvgWidth`, `NDistinct`, and `NullFrac`. The cost-based planner item in
M0003 surfaced two limitations almost immediately:

1. **Memory is unbounded for high-NDistinct columns.** `lineitem.l_orderkey`
   on TPC-H SF1 has ~1.5 M distinct values; the v0 distinct-set held the
   `datumKey` for every one. Growing to SF10+ is not viable.
2. **No predicate selectivity beyond NDistinct.** Equality on a skewed value
   (`o_orderstatus = 'F'`, ~80% of `orders`) collapses to `1/NDistinct = 1/3`.
   Range predicates (`o_orderdate < date '1995-01-01'`, half the table)
   collapse to a flat `1/3`. Both miss the actual selectivity by a wide
   margin.

This document covers the first slice of M0006: replace the full distinct-set
walk with **reservoir sampling**, derive **MCV (most-common-values) lists**
for skewed columns, and equi-depth **histograms** over the non-MCV portion.

Catalog persistence (`0006-0002`) and planner consumption (`0006-0003` /
`0006-0004`) are out of scope for this loop. The sampling collector lands
first because the persistence wire format and the selectivity rules both
need a stable shape to write against.

## Upstream reference

- `postgres/src/backend/commands/analyze.c` — `do_analyze_rel` driver,
  `compute_scalar_stats`, `compute_distinct_stats`, `acquire_sample_rows`.
  Sample size is `targrows = stats_target * 300` (see
  `analyze.c:do_analyze_rel`).
- `postgres/src/backend/utils/misc/guc_tables.c` —
  `default_statistics_target` defaults to 100, range `[1, 10000]`.
- `postgres/src/include/catalog/pg_statistic.h` — `stakind*`, `stanumbers*`,
  `stavalues*` slot layout. Slot kind 1 is MCV (`STATISTIC_KIND_MCV`); slot
  kind 2 is histogram (`STATISTIC_KIND_HISTOGRAM`).

## Decisions

### Sample size: `stats_target * 300`, capped by visible-tuple count

`Context.StatsTarget` carries the per-statement effective value of the
`default_statistics_target` GUC. The wire path populates it from the
session registry; tests leave it at zero, in which case `analyzeRelation`
falls back to the upstream default of `100`. Sample size:

```
targrows = statsTarget * 300                 // upstream's analyze.c
sampleSize = min(targrows, visibleTupleCount)
```

Per-column-statistics-target overrides (`ALTER TABLE … ALTER COLUMN …
SET STATISTICS n`) are out of scope — the parser stub will land in a
later loop.

### Reservoir sampling, walking every page

v0 still walks every page (necessary for the exact `Pages` and `RowCount`
fields, which the planner already consumes). Vitter's two-stage skipping
sampler is upstream's optimisation but isn't required for correctness; the
extra page reads are amortised across an explicit `ANALYZE`. Sampling is
done per visible tuple via Algorithm R:

```
i = 0
for each visible tuple t:
    if i < sampleSize:
        reservoir[i] = decodeRow(t)
    else:
        j = rand(0, i)
        if j < sampleSize:
            reservoir[j] = decodeRow(t)
    i += 1
RowCount = i
```

`rand` is seeded from the executor `Context.RandSource` when set, otherwise
from a fresh `math/rand.NewSource(time.Now().UnixNano())` per invocation.
Tests inject a deterministic seed for reproducibility.

Decoding happens during sampling, not after, so retained rows are typed
`Datum` slices ready for per-column analysis.

### Per-column MCV / histogram derivation

For each column, after sampling:

1. **Null fraction.** `NullFrac = nullCount / sampleSize`.
2. **Frequency table.** Count value occurrences (excluding nulls) keyed by
   `datumKey`. The canonical key collapses cross-scale numerics, etc.
3. **NDistinct.** Number of distinct keys observed. (Upstream's Haas-Stokes
   adjustment is a follow-up; the simple count is a sound lower bound and
   matches what M0003 already reported.)
4. **MCV split rule.** Sort the (key, count) pairs by count descending. A
   value is "common enough" to enter the MCV slot when its sample frequency
   exceeds the average frequency of the **remaining** (non-MCV) values by
   the upstream `MCV_THRESHOLD = 1.25` margin (see
   `analyze.c:compute_scalar_stats`):

   ```
   freq(v) >= 1.25 * avg_freq(non-MCV bucket)
   ```

   Cap the MCV slot count at `statsTarget` to mirror upstream's behavior.

5. **Histogram bounds.** From the **non-MCV** values, sort ascending and
   pick equi-depth boundaries — `bucketCount + 1` values, where
   `bucketCount = min(statsTarget, distinctNonMCV - 1)`. Bound `i` is the
   value at sample-rank `i * (count-1) / bucketCount`. If fewer than two
   distinct non-MCV values remain, the histogram is empty.

6. **Sortable kinds only.** The histogram requires a total order on the
   column type. v0 supports `int`, `bool`, `string`, `time`, and
   `numeric` via `compareDatum`. Bytes and intervals (no upstream-aligned
   total order in v0 yet) are skipped — `Histogram` is left empty for
   them, MCV still populates.

### Storage shape: canonical-string MCV / histogram values

`catalog.ColumnStats` grows two slots:

```go
type ColumnStats struct {
    NDistinct int64
    NullFrac  float64
    MCV       []MCVEntry  // sorted by Frequency desc
    Histogram []string    // bucketCount+1 boundaries, ascending
}

type MCVEntry struct {
    Value     string  // Datum.Format() output
    Frequency float64 // sample frequency 0..1
}
```

The values are stored as strings — specifically the `Datum.Format()` output
— because:

- The catalog package must not depend on the executor's `Datum` type
  (would invert the dependency direction).
- `0003-0010` already stores all column data as strings on the wire; the
  planner's literal-text comparison path is the natural consumer when
  `0006-0003` lands.
- Persistence (`0006-0002`) gets a trivial wire encoding — UTF-8 strings,
  no per-type custom serialisers.

Round-trip parsing on the planner side will reuse the existing literal-
parsing helpers when `0006-0003` consumes these values; no new lossy
conversion is introduced here.

### `default_statistics_target` GUC threading

The variable was already registered (`internal/config/defaults.go`,
`Context: ContextUserset`, `MinVal: 1`, `MaxVal: 10000`, `BootVal: 100`).
This loop wires it through:

- `executor.Context.StatsTarget int` (zero means "use upstream default 100").
- `internal/server/dispatch.go` reads `default_statistics_target` from the
  session registry at statement start and sets `ctx.StatsTarget`.
- The same wiring lives on the extended-query path
  (`dispatch_extended.go`) for parity.

### Backwards compatibility

- The existing `TestAnalyzeRelationPopulatesStats` continues to pass
  unchanged: with 7 rows seeded the sample size (7 ≤ 30 000) collects
  every tuple, so `RowCount=7` and per-column `NDistinct` numbers stay
  exact.
- Old code that reads `Stats.Columns[i].NDistinct` / `NullFrac` is
  unaffected — both fields keep their meaning.
- Catalog snapshots that don't yet carry MCV / histogram payloads still
  load (those fields are nil-valued by default).

## Verification

Unit tests in `internal/executor/operators_analyze_test.go`:

- `TestAnalyzeRelationPopulatesStats` (existing): pinning RowCount /
  NDistinct on a 7-row table.
- `TestAnalyzeBuildsMCVForSkewedColumn`: 1000 rows, label `'F'` on 800 of
  them, label `'O'` on 150, label `'P'` on 50. Asserts that `'F'` enters
  the MCV slot with frequency ≈ 0.8.
- `TestAnalyzeBuildsHistogramForOrderedColumn`: 1000 rows with `id` =
  1..1000 (uniformly distributed). Asserts that the histogram has ≥ 2
  boundaries, the first boundary is ≤ 200 and the last boundary is ≥ 800,
  and the boundaries are strictly ascending.
- `TestAnalyzeRespectsStatsTarget`: with `Context.StatsTarget = 1`, the
  sample size becomes 300; with `StatsTarget = 0`, it falls back to the
  upstream default of `100*300 = 30000`.

End-to-end verification against `goopg start -D <dir>` with psql 18.3 is
done after the GUC threading lands: `SET default_statistics_target = 50;
ANALYZE t;` followed by inspecting the in-memory stats via a debug accessor
(or, once `0006-0003` lands, an EXPLAIN of a predicate that consumes them).

## Out of scope (deferred to subsequent M0006 loops)

- **Catalog persistence of MCV / histogram payloads.** Snapshots still
  serialise tables but not their stats. Covered by `0006-0002`.
- **Planner consumption.** `EstimateRows` still uses the flat `1/3`
  multiplier for Filter; equality still divides by `NDistinct`. Covered by
  `0006-0003`.
- **Cost-driven join algorithm selection.** Hash / merge / nestloop choice
  remains rules-based. Covered by `0006-0004`.
- **Per-column statistics targets** (`ALTER TABLE … ALTER COLUMN … SET
  STATISTICS n`). Parser stub only in this milestone, full plumbing
  deferred.
- **Haas-Stokes NDistinct estimator.** v0 keeps the simple distinct-count;
  scaling up the sample-distinct count to a population estimate is a
  follow-up.
- **Vitter's two-stage skipping sampler.** v0 walks every page; switching
  to row-skipping is an optimisation, not a correctness gap.

## Cross-references

- M0006 milestone: `docs/milestones/0006-planner-statistics.md`.
- Predecessor design (M0003): `0003-0010-analyze-statistics.md` (now
  superseded for the sampling / MCV / histogram fields, kept for the
  `analyzeRelation` mechanics it still describes).
- Upstream `pg_statistic` shape: `postgres/src/include/catalog/pg_statistic.h`.
- Upstream sample-size formula: `postgres/src/backend/commands/analyze.c`,
  `do_analyze_rel`.
