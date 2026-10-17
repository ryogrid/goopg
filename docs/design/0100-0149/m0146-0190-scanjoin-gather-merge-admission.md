# M0146-0190 — the worker-sort Gather Merge must survive the scan/join rel first

Status: closed 2026-10-14.

## Problem

TPC-DS Q26 at SF0.25 plans its GroupAggregate input differently:

- PG 18.3 plans GroupAggregate → Sort → Gather → Nested Loop;
- goopg planned GroupAggregate → Gather Merge → Sort → Nested Loop.

sort-strategy was the top first-divergence category in the S9 census
(`analysis/m0146/m0146-0014/routing-20261014/`), and Q26 was its record.

## Diagnosis

**PG side.** The PG OPTIMIZER_DEBUG build (`tmp/pgdbg`), instrumented for
this task, shows where the Gather Merge goes. Notes are in
`analysis/m0146/m0146-0190/pg-oracle-notes.md`.

- `generate_useful_gather_paths` builds the worker-sort Gather Merge on the
  topmost scan/join rel (`apply_scanjoin_target_to_paths`). It costs
  14286.46..14289.53, with the rel's 26 rows: `override_rows` is false there,
  so `compute_gather_rows` is not used.
- That rel already holds a Nested Loop over a lower Gather Merge,
  14285.24..14292.59, sorted on the same key. The two are fuzz-equal on both
  totals and startups, with equal pathkeys, rows and parallel safety.
- The tight-fuzz comparison finds the new path better in total but worse in
  startup. The rel considers startup (LIMIT 100), so the result is
  COSTS_DIFFERENT, and add_path rejects the new path.
- `add_paths_to_grouping_rel` aggregates only paths that survived, so PG
  never builds a GroupAggregate over that Gather Merge. The Sort-over-Gather
  aggregate, filed first, keeps the remaining near-tie.

**goopg side.** `addPartialAggSplitPath`'s gathered arm builds the same
Gather Merge (`workerSortGatherMergePath`). It files the aggregate over it
straight onto the grouped rel, without the scan/join contest. The
GroupAggregate over it (14200.35..14204.12) beat the Sort-over-Gather arm
(14203.27..14204.14) on both axes. goopg's search had built the same Nested
Loop over Gather Merge (`upper.groupagg.searchcand`, input
14199.13..14206.20), and that path would have rejected it.

## Change

`scanJoinRelKeepsGatherMerge` (partialaggupper.go) runs the scan/join rel's
add_path test before either worker-sort Gather Merge arm is filed: the group
keys arm and the ordered-PLAIN arm.

- **Incumbents.** The searched rel's `Pathlist`, keyed and sized as the
  searchcand arm takes them (`validatedSearchCandidateKeys`, the seed's
  rows), compared through `comparePaths` on a scratch rel. The scratch rel
  carries the searched rel's `ConsiderStartup` and its equivalence-class
  pathkey test.
- **Rows.** The probe carries the rel's rows, as PG does with
  `override_rows` false, and is re-priced by `gatherMergeCost` on them.
- **Gathered candidates skipped.** A candidate that is itself a Gather or
  Gather Merge does not count. It is goopg's copy of what
  `generate_useful_gather_paths` builds in this same call, and PG's rel holds
  no gathered path yet. Without this skip, TPC-DS Q28's scan rel rejected
  its own Gather Merge and every subquery flipped to Sort over Gather.
- **Fail-open.** With no searched rel the arm is filed as before.

## Evidence

Files are in `analysis/m0146/m0146-0190/`.

- **Q26 at SF0.25:** EXPLAIN (COSTS OFF) is identical to PG's over four
  ANALYZE samples.
- **Fire set at SF0.25:** match 77 → 79 (Q26, Q40). Categories:
  join-method 9 → 8, scan-type 7 → 6, aggregation-strategy 3 → 2,
  sort-strategy 10 → 7, parallelism 8 → 7; parameterisation 9 → 10 (Q19
  narrows to join-order and parameterisation).
- **Fire set at SF1:** match 64 → 65.
  - Q19 and Q40 now match.
  - Q45 flips MATCH → SHAPE-DIFF. goopg's search keeps a Nested Loop over
    Gather Merge (25028.54..25423.04) within 1.009 of the Gather Merge
    (25118.98..25196.71), so the contest now rejects it, and the sorted-input
    split wins. PG keeps its Gather Merge there. goopg's group-input estimate
    is 657 rows against PG's 473, which inflates the Nested Loop's
    inner-probe cost toward the fuzz edge. That is a row-estimate divergence,
    not this mechanism.
  - Categories: join-order 30 → 29, scan-type 20 → 19, sort-strategy
    17 → 16, parallelism 18 → 17, qual-placement 5 → 4; parameterisation
    11 → 12.
- **SF0.25 sweep:** 99 of 99 pass.
- **TPC-H:** the acceptance arm is value-identical; spotcheck passes.
- **Regress:** select_parallel, aggregates, partition_aggregate,
  groupingsets, write_parallel and union diff identically to HEAD.
- **Test:** `TestScanJoinRelDropsATiedGatherMerge` pins Q26's numbers. It
  covers the startup-interest arm, the dominating case, the skipped gathered
  twin, and the unsearched input.

## Not ported (ledgered)

- The filed arms still price their aggregate on `compute_gather_rows`
  (M0146-0009n); PG's scan/join Gather and Gather Merge carry the rel's rows.
  Only the admission probe uses the rel's rows.
- When the Gather Merge survives and dominates a search candidate, PG drops
  that candidate from the scan/join rel. goopg still aggregates over it.
- SF1 Q45's estimate gap (657 vs 473 group-input rows) is not filed.
- Q45 at SF0.25 now elects the sorted-input split, which is 0.14 cheaper
  than Sort over Gather in goopg but 0.13 dearer in PG.
