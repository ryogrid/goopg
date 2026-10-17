# M0146-0149 — an Incremental Sort's rows are its input clamped to two

Status: done 2026-10-10 (4fb2cdd47). Parent: M0146-0014c.

## Problem

TPC-DS Q70 joins `s_state IN (…)` against the window subquery `tmp1`.

- **PG** estimates `tmp1` at 2 rows and hashes it in a Hash Semi Join.
- **goopg** estimated 1 row, unique-ified it, and ran it as the outer of
  a nested loop.

## PG behaviour

- **The clamp.** `cost_incremental_sort`
  (postgres/src/backend/optimizer/path/costsize.c:2025) clamps
  `input_tuples` to at least 2, "so the cost of a sort is never estimated
  as zero". It then sets `path->rows = input_tuples` (:2121), so the
  clamped value becomes the path's row count.
- **What follows from it.** An Incremental Sort over a one-row input
  carries two rows. In Q70 those two rows flow up through the window input
  sort, the WindowAgg and the Subquery Scan.

## Change

- **The path.** `incrementalSortPathOver` (incrementalsortpaths.go) sets
  `Rows` to the clamped value. M0146-0134 already used that value for the
  group estimate.
- **The node.** The `*IncrementalSort` arm of `EstimateRows`
  (cardinality.go) returns max(child, 2). A plan node's rows are its
  path's rows, so the two are twins and change together.

## Verification

- **Test.** `TestIncrementalSortRowsAreTheClampedInput` checks path rows
  1 → 2, 0 → 2 and 37 → 37, and node rows 1 → 2 and 37 → 37.
- **Q70, traced on the private SF0.25 clone.**
  - `tmp1` is now 2 rows.
  - The 4-relation joinrel's cheapest-total path is PG's Hash Semi Join
    (38367.56), ahead of the nested-loop semi join (38430.95) and the
    unique-ified nested-loop inner join (38440.70).
- **What still differs.** The plan still prints the ordered nested-loop
  inner join, because the grouping step is not seeded with the joinrel's
  cheapest-total path (M0146-0150).
- **TPC-DS fire set.** Q4, Q11, Q58, Q64 and Q70 move, with no category
  change except Q70's `parameterisation` +1 at both scales: its nested
  loop now has a Materialize inner over the 2-row outer. Matches are
  unchanged (56 / 42).
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases): unchanged, apart from `join`'s known row
  flap.
- **Gates.** Units, TPC-H spotcheck, SF0.25 sweep (PASS=99) and
  ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **Q70's grouping seed** (M0146-0150). PG's `create_grouping_paths`
  hashes over `input_rel->cheapest_total_path`. goopg's grouping step
  takes the search's winner, which here is the ordered nested loop.
