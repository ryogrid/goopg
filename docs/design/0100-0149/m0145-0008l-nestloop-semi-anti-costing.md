# M0145-0008l: nested-loop SEMI/ANTI costing follows final_cost_nestloop

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008l (Kind:
impl, Parent: M0145-0008g). Evidence: `analysis/m0145/m0145-0008l/`.

## Defect

goopg's `nestloopCost` charged the full inner rescan for every outer row of a
SEMI or ANTI nested loop. The executor stops at the first match
(`finishOuter`, join_nl_stream.go), and PG prices that early exit. On TPC-H
Q22, the NL anti join was priced far above the hash anti over all of
`orders`, so the search never elected PG's Nested Loop Anti Join.

## What PG does (postgres/src/backend/optimizer/path/costsize.c)

- **`compute_semi_anti_join_factors`**, called once per join pair by
  `add_paths_to_joinrel` for SEMI/ANTI:
  - `outer_match_frac = jselec`, the SEMI-arm selectivity of the join quals
    (for ANTI, the pushed-down quals are excluded as non-join quals);
  - `match_count = max(1, nselec * innerrel->rows / jselec)`, where `nselec`
    is the INNER selectivity of the same quals (1 when `jselec` is 0).
- **`initial_cost_nestloop`** charges the rescan startup for every rescan,
  then defers the inner run cost to `final_cost_nestloop` for SEMI/ANTI.
- **`final_cost_nestloop`**'s SEMI/ANTI branch:
  - `outer_matched = rint(outer_rows * outer_match_frac)`, and
    `inner_scan_frac = 2 / (match_count + 1)`.
  - `ntuples = outer_matched * inner_rows * inner_scan_frac`.
  - With `has_indexed_join_quals` (every join qual is an index qual of a
    parameterised index or simple bitmap scan, and no join qual is left for
    the join), a matched row scans `inner_scan_frac` of the inner. An
    unmatched row costs `inner_rescan_run / inner_rows` and adds no tuples.
  - Otherwise an unmatched row scans the whole inner and all its pairs
    count. The first scan is charged in full once, attributed to the first
    unmatched row.
  - The CPU per tuple (`cpu_tuple_cost` plus the qual cost) rides `ntuples`.

## What changed

- `internal/optimizer/nestloop_semianti.go`:
  - `semiAntiJoinFactorsFor` ports `compute_semi_anti_join_factors`. It uses
    the sizer's own per-clause estimators (`joinClauseSelectivityForJoin`
    for the SEMI/ANTI arm, `joinClauseSelectivityExt` for INNER), over
    `oneClausePerEquivClass`. Like PG, it applies no FK selectivity.
  - `nestloopCostSemiAnti` ports the SEMI/ANTI branch, and charges qual CPU
    on its own `ntuples`.
  - `hasIndexedJoinQuals` ports `has_indexed_join_quals`: an empty residual
    plus a parameterised `PathIndexScan`, or a `PathBitmapHeapScan` over a
    single `PathBitmapIndexScan`. A Memoize inner is false, as in PG.
- `joinpaths.go` computes the factors once per pair, only when `jt` is SEMI or
  ANTI; a unique-ified side has already been demoted to INNER, as in PG. It
  passes them to all three nested-loop producers: the plain NL
  (`addNestLoopPath`), the parameterised NLI (`addNLIPaths`), and the partial
  NL (`addPartialNestLoopPaths`). Every other join type keeps `nestloopCost`
  unchanged.
- Pinned by `TestNestloopCostSemiAntiMatchesFinalCostNestloop`: the
  non-indexed, indexed and no-match arms, with values worked by hand from
  PG's formulas.

## Measured

Fire-set gate, HEAD `2e923ff37` vs staged. The TPC-H lane is opt-in.

- **TPC-H: Q4, Q21, Q22 moved.** All three leave the `join-method` category
  (`CATEGORIES-EXCL-MATCH join-method 14 → 11`).
  - Q22 now elects PG's `Nested Loop Anti Join` over
    `order_customer_fkidx`, and its estimate falls from 63,838 to 28,044
    (PG: 13,200).
  - Other categories move by at most one (join-order 16→17, scan-type
    13→12, parameterisation 6→7, aggregation 3→4, sort 8→9, rendering
    4→3). `match` is unchanged at 3/22.
  - Values are identical.
  - Fire-set execution times, taken with the nightly CI batch running (so
    indicative only, but both arms ran back to back under the same load):
    Q4 5.45 → 1.90 s, Q22 1.85 → 0.56 s, Q21 3.86 → 4.77 s.
- **TPC-DS SF0.25: Q64 and Q94 moved.**
  - Q94 moved in cost only.
  - Q64, already a MISSING-NODE mismatch, reordered some inner joins.
  - Both executed with `introduced=none`, and every category is unchanged.
- **TPC-DS SF1:** `fires=none`.

Q22's remaining gap to PG:
- The inner is a Bitmap Heap Scan where PG uses an Index Only Scan. goopg
  generates no index-only inner for a join.
- The anti join runs serially where PG runs it under Gather Merge.
  Partial-NL ANTI is still refused at the filing gate (ledger
  `m0137-0019b-partial-nl-left-anti-still-refused`).

## Gates (staged tree)

- units PASS; `tpch-spotcheck` PASS.
- fire-set (TPC-DS SF0.25/SF1 and TPC-H): PASS, `introduced=none`.
- `tpcds-sf025 sweep`: 96/96, `changed (2): Q64 Q94`.
- acceptance arm: 24 MATCH on values.
- ea-ratchet: 52/52.
- The nightly CI batch was running during the acceptance arm and the sweep,
  so both ran with `FORCE=1`: they are valid for values, not for timing.

Movement: none. `join-method` moved by −3, which is not beyond ±3, and the
match counts are unchanged.

## Not ported (ledgered)

- PG also takes this branch for an INNER join whose inner rel is proven
  unique (`extra->inner_unique`). goopg's nested loop does not yet; its hash
  join already has an inner-unique arm (`hashJoinFinalCostInputFor`).
- `has_indexed_join_quals` checks each parameterised clause against the
  index clauses. goopg uses the empty residual as the equivalent evidence.
