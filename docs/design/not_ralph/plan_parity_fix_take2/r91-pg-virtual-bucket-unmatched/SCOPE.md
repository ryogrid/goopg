# R91 SCOPE — PG virtual buckets for inner-unique unmatched probes

R91 follows R90 report commit `e6bcb19e6`. It authorizes one narrow Hash Join
final-cost addition: the PostgreSQL 18.3 unmatched-probe charge for the
already-proved INNER `inner_unique` case. It does not treat Goopg's executor
map sizing as PostgreSQL bucket geometry, and it does not alter Goopg's actual
hash-table memory or spill behavior.

## 1. Problem and source boundary

R90 carries fail-closed inner-unique evidence and prices only the matched
branch of `final_cost_hashjoin`. Its unmatched branch is deliberately zero,
because Goopg's executor looks up a canonical key in `map[K][]Row`: a miss has
no candidate slice to walk. That is an executor fact, but it is not the
PostgreSQL planner model. PostgreSQL 18.3 prices an unmatched probe at
`hash_qual_cost.per_tuple * (outer_rows - outer_matched_rows) *
clamp_row_est(inner_rows / virtualbuckets) * 0.05`.

Here `virtualbuckets = numbuckets * numbatches`, with both components from
`ExecChooseHashTableSize` over a packed `HashJoinTuple` and the inner path's
`pathtarget->width` (`costsize.c:4295-4314, 4434-4481`; `nodeHash.c:658-955`).
The Goopg executor's `hashsize.Choose` instead sizes `map[K][]Row` entries as
`48 * columns + 24` plus 48-byte map slots. R90 correctly forbids using its
`NBuckets` or `NBatch` as the PostgreSQL denominator.

Q96 remains the focused witness. Its two live PostgreSQL Hash Joins are
`Inner Unique: true`, partial outer / complete inner paths. R90 preserves
their values and changes the top cost from `25434.49` to `24541.49`, but its
natural tree remains `(store_sales -> store) -> household_demographics` rather
than PostgreSQL's `(store_sales -> household_demographics) -> store`. This
scope does **not** promise that the missing unmatched term flips that election.

## 2. Authorized design

Add an optimizer-private, pure PostgreSQL virtual-geometry helper. It has no
executor dependency and no executor consumer.

* Its inputs are the build path's row count, its emitted target byte width, and
  the session hash-memory limit already represented by `costParams.workMem`.
  It reproduces the non-parallel `ExecChooseHashTableSize(..., useskew=true,
  try_combined_hash_mem=false)` arithmetic: plausible-row fallback, packed
  tuple size `HJTUPLE_OVERHEAD + MAXALIGN(MinimalTupleHeader) +
  MAXALIGN(width)`, the 2% skew reservation, pointer/bucket caps and powers of
  two, multi-batch recomputation, and PG18's batch walk-back. Its public
  result for this caller is only a positive, overflow-safe
  `virtualbuckets = numbuckets * numbatches`.
* Do **not** call or modify `internal/executor/hashsize.Choose`. That helper
  remains the sole source of Goopg executor map capacity, batches, and spill
  I/O. R91 must keep its map-width `NCols`/`AvgVarBytes` inputs and its
  Goopg-width spill charge byte-for-byte unchanged.
* Carry an emitted-byte-width field on a `Path` and provide one nil-safe
  `pathWidth` accessor, analogous to `pathNCols`/`pathAvgVarBytes`. A path
  whose target is not narrower falls back to `RelOptInfo.Width`. An index-only
  path sets that field only from the existing PG-style `TupleWidth`/`tupleWidth`
  calculation over its exact `IndexOnlyCovered` schema; it must never infer a
  byte width from `NCols` or `AvgVarBytes`. Thread that width only to the
  planner-private geometry input. Do not change EXPLAIN width rendering or any
  path comparator outside the final Hash Join charge.
* Split R90's unique branch. Preserve its matched-probe price only when
  `innerBucketSize > 0`, but add PostgreSQL's unmatched term whenever R90 has
  `innerUnique` evidence and this new virtual geometry is valid, even if no
  bucket statistic exists. It uses the candidate's `outerRows -
  RoundToEven(outerRows * outerMatchFrac)`, `clamp_row_est(innerRows /
  virtualbuckets)`, and the `0.05` factor. The current Goopg surrogate for
  `hash_qual_cost.per_tuple` remains exactly `cpuOperatorCost *
  numHashClauses`; R91 claims no general `QualCost` model. The existing
  `innerBucketSize` remains the matched-probe statistic; R91 does not
  implement `UniquePath`'s `1/virtualbuckets` rule, multivariate bucket stats,
  or MCV suppression.
* Apply the term only where R90 has already produced sound INNER unique
  evidence and all geometry inputs are valid. A zero/unknown path width or
  an overflow/invalid geometry result declines the new term and preserves the
  R90 cost. LEFT, SEMI, ANTI, non-unique INNER, parameterized build paths,
  parallel-hash/combined-memory paths, and Goopg's spill calculation retain
  current behavior.

## 3. Required proof and tests

Before changing production behavior, add focused tests that prove:

1. the virtual-geometry helper follows the PG18 source at the bucket floor,
   `MAXALIGN` width boundary, skew-reserved single-batch case, multi-batch
   case, and walk-back case; its result is always positive and overflow-safe;
2. the Q96-shaped complete 720-row width-48 build at a 128 MiB hash limit
   yields `1024 * 1` virtual buckets, independent of Goopg map geometry;
3. an index-only/narrow path uses `TupleWidth(IndexOnlyCovered)` with
   deliberately unequal column widths rather than its relation's full width
   or either map-width proxy, while an ordinary path falls back to the relation
   width and the geometry changes accordingly;
4. an inner-unique serial and partial cost each adds exactly PG's unmatched
   formula, shares R90's total-relation match fraction, and uses only its own
   outer path rows; and
5. a unique input with valid geometry and a zero/missing bucket statistic gets
   exactly the unmatched-only delta, while a non-unique no-stats input retains
   its old cost; and
6. zero/invalid geometry, LEFT/SEMI/ANTI, and the Goopg spill
   `hashsize.Choose` result retain their old costs exactly.

After code, run focused optimizer tests, then the full optimizer/executor/
estimateaudit test and vet package groups plus `git diff --check`. Capture Q96
natural A/A, natural versus `GOOPG_GATHER_PATHS=top`, PG value equality, and
Q9/Q41/Q91/Q96 trace-on/off controls. Because this changes a production cost,
run the TPC-H digest, the SF0.25 sweep, and a fresh live-PG plan census before
the implementation report.

## 4. Explicit non-goals and process

R91 does not force Q96 or any other join order; change a GUC, selectivity,
executor map construction, actual batching, or spill I/O; use executor
`NBuckets * NBatch` as a substitute; add a guessed MCV/QualCost/pathtarget
constant; or implement `UniquePath`, multivariate, SEMI, ANTI, or parallel
Hash geometry. Any evidence that the exact PG geometry cannot be represented
from a build path's row count and emitted byte width must stop the change and
be reported rather than approximated.

This scope requires agent review, correction, `git commit -n`, and push before
implementation. Implementation and its evidence report require separate
reviews and commits/pushes.
