# M0138-0004 — MCV, histogram and correlation from the shared sample

Status: accepted (landed 2026-09-15)

## Task

Per `.ralph/fix_plan.md`'s M0138 milestone: apply PG's `compute_scalar_stats`
(`postgres/src/backend/commands/analyze.c:2402-2919`) selection rule to the
sample M0138-0002 now produces, so every stats slot (MCV, histogram,
correlation) is computed from the same rows PG would have seen and by the
same algorithm — not merely "a plausible algorithm that happens to also
produce an MCV list and a histogram."

## Starting point: uncommitted work already in flight

A prior loop iteration had already applied three fixes from the M0138-0001
census before being cut off (visible as an uncommitted diff to
`internal/executor/operators_analyze.go`/`operators_analyze_test.go` at the
start of this task, with bench servers for `:65433`/`:65437` left running
from its own measurement — reaped via `goopg stop -D` per the orphaned-server
protocol before the gates below could run):

1. **`AvgWidth` typlen fallback** — PG's `is_varwidth` guard
   (`analyze.c:2565-2569`, plus the too-wide-only and all-null branches at
   `:2965`/`:2975`) means a fixed-width (non-varlena) type's `stawidth` is
   simply the type's catalog `typlen`, never a measured value. goopg's
   per-row loop only ever measured `datumVariablePayloadWidth`, which is 0
   for every fixed-width kind, so every `int2/int4/int8/date/…` column
   reported `AvgWidth=0` (census finding 3: 32/61 TPC-H, 70/121 TPC-DS
   columns). Fixed by looking up `colTypeDescriptor(colType).TypLen` up
   front and short-circuiting the measured-payload branch when it is `>= 0`.
2. **Correlation tie-break** — PG's `compare_scalars`/`tupnoLink`
   (`analyze.c:2438-2550,2931-2950`) breaks a value-sort tie deterministically
   by original scan position ("for equal datums, sort by tupno"); `sort.Slice`
   is documented non-stable, so two duplicate values could land in either
   relative order run-to-run. Fixed by building `corrPairs` in ascending
   scan-position order (already true) and sorting with `sort.SliceStable`
   instead.
3. **MCV bucket tie-break** — PG walks values in ascending sorted order and
   only replaces the track-list tail on a *strictly greater* count
   (`dups_cnt > track[track_cnt-1].count`, `analyze.c:2552-2554`), so a count
   tie at the truncation boundary keeps whichever value was encountered
   first — the smaller one. goopg's bucket sort was count-only, leaving ties
   in Go map iteration order (unspecified, and could vary run to run for the
   same input). Fixed by adding an ascending-value secondary sort key for
   orderable kinds.

This task builds on that WIP (rather than discarding it) and adds three more
divergences found by reading `compute_scalar_stats` line-by-line against
`computeColumnStats`.

## Three more divergences found and fixed this task

### 1. The "complete list" shortcut admitted singleton-tail columns

PG's MCV completeness shortcut is:

```c
if (track_cnt == ndistinct && toowide_cnt == 0 &&
    stats->stadistinct > 0 && track_cnt <= num_mcv)
    num_mcv = track_cnt;        /* keep the whole tracked list verbatim */
```

`track[]` is populated *only* for a value that repeated in the sample
(`dups_cnt > 1` gates every insertion, `analyze.c:2549-2552`), so
`track_cnt == ndistinct` can only be true when **every** distinct sample
value repeated at least once — a single singleton disqualifies the column
from the complete-list shortcut regardless of how small its total distinct
count is, and PG instead runs `analyze_mcv_list`'s significance test over
just the repeated values.

goopg's `completeAndFits` was `len(buckets) <= statsTarget` — the total
distinct-value count alone, with no check that every one of those values was
multiply-occurring. A column with, say, 60 distinct values that each
repeated plus 40 distinct singletons under a stats target of 100
(`60 + 40 = 100 <= 100`) wrongly took the "complete" branch and returned all
60 repeated values verbatim, skipping `analyzeMCVList`'s significance test
entirely — over-admitting borderline-common values PG would have narrowed or
rejected. Fixed: `completeAndFits := nmultiple == len(buckets) &&
len(buckets) <= statsTarget && stats.StaDistinct() > 0`, reusing
`nmultiple` (already computed for the ndistinct estimator) as the
"no singletons" check and `StaDistinct()` (M0138-0003's accessor) as the
`stadistinct > 0` guard the old condition never consulted at all.

Verified with `TestAnalyzeMCVExcludesSingletonsFromCompletenessAndCandidates`:
60 values at count 2 (near-uniform — none individually significant) plus 40
singletons, target 100. Before the fix: `MCV` held all 60 repeated values.
After: `MCV` is empty, matching the near-uniform rejection
`TestAnalyzeMCVListMatchesUpstream`'s "near-uniform column admits nothing"
case already established for `analyzeMCVList` on its own.

### 2. `analyze_mcv_list`'s candidate list was padded with singleton noise

On the ordinary (non-complete) path, PG caps the candidate list handed to
`analyze_mcv_list` by `track_cnt`, not by the total distinct count:

```c
if (num_mcv > track_cnt)
    num_mcv = track_cnt;
```

Since `track[]` structurally never holds a singleton, this is really
`num_mcv = min(attstattarget, nmultiple)`. goopg capped the candidate count
by `len(buckets)` (== `ndistinct`) instead of `nmultiple`, so whenever a
column had singletons, `analyze_mcv_list`'s significance walk started with a
candidate array whose tail was singleton noise rather than stopping at the
last genuinely-repeated value. Fixed: cap `mcvCount` by `nmultiple` before
building the `counts` slice fed to `analyzeMCVList`. Since `buckets` is
sorted descending by count, `buckets[:nmultiple]` are exactly the
multiply-occurring entries, matching PG's candidate set by construction.
The pre-existing trailing-trim loop (`buckets[mcvCount-1].count <= 1`) is now
a proven no-op given fixes 1 and 2 together — left in place as a safety net,
its comment updated to say so.

### 3. The histogram deduped adjacent equal boundaries — PG does not

`compute_scalar_stats`'s histogram loop (`analyze.c:2806-2836`) copies
exactly `num_hist` evenly-spaced values out of the sorted non-MCV array with
**no distinctness check at all**. A value that is common but never became an
MCV *candidate* (case 2 above: ranked below the `min(attstattarget,
nmultiple)` cutoff, or declined by `analyze_mcv_list`'s significance test)
can therefore legitimately occupy several adjacent histogram slots. goopg's
`computeColumnStats` deduped the boundary array after building it, under a
comment claiming upstream also produces distinct boundaries — checked against
`analyze.c` and that claim is false. The consumer already anticipated
duplicate boundaries on both sides: PG's own `ineq_histogram_selectivity`
falls back to `binfrac = 0.5` when a bin's two boundaries compare equal
("cope if bin boundaries appear identical", `selfuncs.c:1234-1237`), and
`internal/optimizer/selectivity.go`'s `bucketFraction` /
`convertStringBucketScales` already implement that exact 0.5 fallback. The
dedup was pure removed information: it silently shrank the stored histogram
(and therefore every selectivity computed against it) relative to what PG
would have stored for the identical sample. Fixed by storing the raw
evenly-spaced `bounds` slice as-is.

Verified with `TestAnalyzeHistogramKeepsAdjacentDuplicateBoundaries`: two
dominant values take both MCV slots under `statsTarget=2`; a third value
repeats 500 times but is ranked 3rd (never an MCV *candidate* per fix 2), and
under the resulting equi-depth spacing the same value is picked at multiple
consecutive boundary positions. Before the fix those collapsed to one entry;
after, they are all present.

## Scope not covered — `compute_distinct_stats` (non-orderable kinds)

PG uses a **different** algorithm, `compute_distinct_stats`
(`analyze.c:2059-2338`), for a type with only an `=` operator (no ordering):
a bounded `track_max = max(2*num_mcv, 10)` insertion-ordered scan with FIFO
eviction of the oldest singleton — not a sort-then-truncate at all. goopg's
`!orderable` branch (bytea, interval, …) still uses the same count-sort as
the orderable path, just without the ascending-value tie-break (which has no
PG-defined meaning there). Left unported: this task's text is specifically
the `compute_scalar_stats` selection rule, and neither TPC-H nor TPC-DS has a
non-orderable column, so there is no corpus measurement to validate a port
against yet. Deferral ledger row `m0138-0004` (compute_distinct_stats).

## Gates

- `go build ./...` clean.
- `go test ./internal/executor/...` full package green, including the three
  new/updated tests above plus every pre-existing ANALYZE test
  (`TestAnalyzeBuildsMCVForSkewedColumn`, `TestAnalyzeBuildsHistogramForOrderedColumn`,
  `TestAnalyzeRespectsStatsTarget`, `TestAnalyzeRespectsPerColumnStatTarget`,
  `TestAnalyzeMCVListMatchesUpstream`, …).
- `go test ./internal/optimizer/...` green (consumer of `ColumnStats`, no
  source change but exercises `bucketFraction`'s duplicate-boundary
  fallback this task now actually reaches in production).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: green for
  every package this task touched (`internal/executor`, `internal/optimizer`).
  `internal/parser` fails 60 test functions and `github.com/goopg/goopg/bak`
  fails to build — both pre-existing and unrelated (the `RangeVar.
  GroupedJoinUnaliased` AST-drift filed 2026-09-15 under "Manually
  discovered" in `fix_plan.md`, and untracked `bak/` scratch respectively);
  confirmed by the diff being executor-only.
- `scripts/tpch-spotcheck.sh`: `RESULT=PASS`, Q12=2 rows / Q13=34 rows
  (canonical anchors), after reaping the two orphaned bench servers left by
  the interrupted prior loop so the snapshot-clone step could get a
  quiescent copy.
- Per M0138's own task breakdown, the corpus-wide plan/timing re-measurement
  this change implies is M0138-0005's job ("corpus re-measure at a declared
  epoch"), not repeated here — this task's job was the selection-rule
  mechanism, applied faithfully regardless of where the numbers land.
