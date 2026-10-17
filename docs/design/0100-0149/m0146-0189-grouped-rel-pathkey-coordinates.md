# M0146-0189 — every grouped-rel path names its ordering by the group keys

Status: closed 2026-10-14.

## Problem

TPC-DS Q77's `ws` subquery (web_sales ⋈ date_dim ⋈ web_page, grouped by
`wp_web_page_sk`) planned differently at SF0.25:

- PG 18.3 plans GroupAggregate (3068.93..8854.17) over Nested Loop(Index
  Only Scan web_page_pkey, Materialize(Gather(Parallel Hash Join)));
- goopg planned Finalize GroupAggregate → Gather Merge → Partial
  GroupAggregate → Sort → Hash Join (8697.61..8708.11).

## Diagnosis

**PG side.** With nested loops disabled, PG's serial alternative costs
8773.40..8775.09, cheaper in total than the plan it chose. Every candidate
sits within `STD_FUZZ_FACTOR`, and they all deliver the group ordering, so
`add_path` keeps the one that starts earliest: the nested loop. PG's group
pathkeys are canonical, so all the grouped paths' orderings are one pathkey.

**goopg side.** goopg built the same serial candidate:
`upper.groupagg.sort`, 3049.90..8771.07, sorted. It also built the split,
8697.61..8708.11. A debug print of the grouped rel's pathkeys showed the
cause:

- the serial arms keyed `wp_web_page_sk` at Index 62, the aggregate's input
  row;
- the sorted Finalize split keyed it at Index 0, the transport row of the
  Partial/Gather Merge (`mergeKeys`).

`add_path` compares pathkeys by expression, so the two were
PATHKEYS_DIFFERENT. Both survived, and the split won on total.

The presorted serial arm had the same kind of skew: it filed the input
node's pathkeys (`seedKeys`) instead of the group keys every other arm
uses.

## Change

- `finalizeGroupPathkeys` (partialaggupper.go) gives both sorted Finalize
  constructions the group keys over the aggregate's input
  (`groupKeysSortKeys`). The Gather Merge below still merges on the
  transport positions, which `createFinalizeAggSortedPlan` re-derives itself
  (`transportGroupSortKeys`). The path's pathkeys only serve comparison.
- The presorted serial arm (groupingpaths.go) files
  `pathkeysForSortKeys(keys)`, the group keys, like the sort and
  search-candidate arms.

## Evidence

Files are in `analysis/m0146/m0146-0189/`.

- **Standalone `ws`:** goopg now plans PG's GroupAggregate over Nested
  Loop(Index Only Scan web_page_pkey, Materialize(Gather(Parallel Hash
  Join))), 3049.91..8774.11.
- **Fire set at SF0.25:** match 76 → 77 (Q85). Categories: join-order
  18 → 17, join-method 10 → 9, aggregation-strategy 4 → 3, sort-strategy
  11 → 10, parallelism 11 → 8, qual-placement 4 → 3. Q19 and Q40 drop
  parallelism.
- **Fire set at SF1:** match 64 → 64.
  - Q85 now matches.
  - Q52 flips MATCH → SHAPE-DIFF on a tie: its Gather Merge-over-Sort arm
    (64034.05..64036.86) and PG's Sort-over-Gather (64036.36..64036.83)
    differ by 0.03 in total, and the earlier startup wins (COSTTIE).
  - join-order 31 → 30, join-method 14 → 12, scan-type 21 → 20,
    aggregation-strategy 10 → 9, qual-placement 6 → 5.
- **SF0.25 sweep:** 99 of 99 pass.
- **TPC-H:** the acceptance arm is value-identical.
- **Test:** `TestGroupedRelPathkeysShareOneCoordinate` checks that the
  Finalize and serial group pathkeys compare equal, and that add_path then
  keeps only the fast-start serial aggregate. It fails with the
  transport-coordinate keys.

## Not ported (ledgered)

- The grouped rel's pathkeys are still syntactic. A path whose input names
  the group column through another member of its equivalence class would
  diverge again; canonical group pathkeys would close it for good.
- SF1 Q52's COSTTIE flip is not filed.
- Q77 at SF0.25 still differs on its store-returns side (PG keeps the split
  there) and in the catalog-returns Materialize / Subquery Scan rendering.
