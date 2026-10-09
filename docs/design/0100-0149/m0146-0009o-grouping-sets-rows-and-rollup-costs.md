# M0146-0009o — grouping-sets row estimates and rollup pricing follow get_number_of_groups / create_groupingsets_path

Status: done (2026-10-04). Parent: M0146-0009. Filed by M0146-0005 slice
115.

## The divergence

goopg's grouping-sets aggregates estimated about a quarter of PG's rows.

| query | goopg | PG |
|---|---|---|
| Q22 (MixedAggregate) | 12154 (its input) | 46681 |
| Q18 at SF0.25 | 12 | 49 |
| Q18 at SF1 | 49 | 213 |
| Q67 | 3140 | 18531 |

## Cause

**Rows.** `get_number_of_groups` (`./postgres/src/backend/optimizer/plan/planner.c`)
adds up `estimate_num_groups` over every grouping set, each against the
full input row count. goopg's EXPLAIN-side `estimateAggregate` already did
that. But the path search sized the grouped rel in `sizeGroupingRelFromAgg`
from the deduplicated union of all sets' expressions, which is one set's
estimate, clamped to the input. The plan's own top line carries that path
row count.

Fixing the rows exposed two pricing gaps against
`create_groupingsets_path` (`./postgres/src/backend/optimizer/util/pathnode.c`):

- **The hashed path was one hash table.** goopg priced it as a single
  `cost_agg(AGG_HASHED)` over the union's columns with the total group
  count. PG builds one hashed rollup per non-empty set, plus one sorted
  rollup holding the empty sets, which makes the strategy AGG_MIXED:
  - the first rollup reads the input;
  - each further rollup adds `cost_agg` with no input cost;
  - under AGG_MIXED, `cost_agg`'s CPU arm is the sorted one (startup = the
    input's), with the hash spill tail still charged.
- **The sorted rollup took the Group path's price.** `costAgg` turns an
  aggregate-free sorted grouping into `cost_group` (M0146-0023), which has
  no per-group emit charge. A grouping-sets path is always
  `cost_agg(AGG_SORTED)`.
- **The sorted rollup carried no pathkeys.** `create_groupingsets_path`
  gives a single sorted rollup the `group_pathkeys`. Those keep it in
  `add_path` beside a hashed MixedAggregate whose startup is far lower. The
  Sort above then elects between them on total cost: Q18 at SF0.25 keeps
  PG's GroupAggregate, which is 1.5 cheaper.
  - M0146-0020a had dropped the keys because PG 18's grouped outputs are
    RTE\_GROUP Vars nullable by the grouping step, so the keys never
    satisfy an ORDER BY on the plain columns.

## Change

- **Rows.** `sizeGroupingRelFromAgg` uses `estimateAggregate`. The per-set
  loop is factored into `groupingSetGroupCounts`, shared by both callers.
- **Hashed path.** `groupingSetsHashedCost` prices the hashed
  grouping-sets path rollup by rollup, as above.
- **Sorted rollup price.** `costAggSortedRollup` is `cost_agg`'s
  AGG\_SORTED arm without the `cost_group` shortcut. It is used by both
  sorted-rollup arms and by the empty-set rollup.
- **Sorted rollup keys.** `PathKey.GroupingNulled` marks a key on a
  grouping-nullable output. `pathKeyEqual` compares the flag, so the
  rollup's marked keys never satisfy an ORDER BY's unmarked keys, but they
  still beat a keyless path in `add_path`.
  - The node-side ordering derivation (`aggregateEmissionPathkeys`) already
    refuses grouping sets.

## Effect

PG 18.3 on the test fixture, against goopg now:

| query | PG | goopg |
|---|---|---|
| ROLLUP (a, b, c) | MixedAggregate 0.00..849.41, rows=4041 | 0.00..859.51, 4041 |
| GROUPING SETS ((a), (b), ()) | MixedAggregate 0.00..562.41, rows=341 | 0.00..563.26, 341 |
| GROUPING SETS ((a, b), (a)) | HashAggregate 459.00..579.40, rows=2040 | 459.00..584.50, 2040 |

- **Fire set.** Matches are flat (41 / 32).
  - CATEGORIES-EXCL-MATCH aggregation-strategy at SF0.25 is 12 → 11: Q67
    now elects PG's sorted rollup.
  - Top-level estimates now equal PG's: Q18 49 / 49 and 100 / 100, Q80
    39 / 39, Q27 89 vs 91, Q22's MixedAggregate 46681-shaped.
  - The rows-only first version lost Q18's match to the MixedAggregate;
    the rollup pathkeys restore it.
- **Regress `groupingsets`.**
  - The rows-only version flipped three small-input MixedAggregate /
    HashAggregate plans to a Sort-fed GroupAggregate. That is what exposed
    the two pricing gaps.
  - The final version matches PG on all three, and one more case now
    elects PG's MixedAggregate (diff 1000 → 993 lines).
- **Other gates.**
  - The sweep passed 96/96.
  - The TPC-H arm matched 24/24, and tpch-spotcheck passed.
  - ea-ratchet passed. Its "Q92 fixed" line predates this change.

Test: `TestGroupingSetsRowEstimateSumsSets` fails on HEAD. It checks:

- the three PG plans' aggregate kinds, startups and rows;
- that a sorted rollup forced with `enable_hashagg = off` still has a Sort
  above it for `ORDER BY a, b`, with the same values (PG: Limit → Sort →
  GroupAggregate → Sort).

## Found on the way (filed, S2)

One regress run, not reproduced on two reruns, hit
`DDL catalog sync: pg_class_relname_nsp_index: insert leaf blk 22 sys btree 2663: pin leaf blk 22: short read at block`.
The cluster's catalog index referenced a leaf block its file did not hold,
and every later CREATE failed. It was filed as M0146-0056; the evidence is
in `analysis/m0146/m0146-0056/`.

## Not done (ledgered)

- **HAVING and tlist costs.** `create_groupingsets_path`'s HAVING-qual and
  per-output-row tlist charges are not modelled. This is the remaining
  1–5 cost-unit gap on the fixture.
- **Mixed sorted-and-hashed rollups.** PG also considers AGG_MIXED paths
  that hash some rollups and sort the first over a presorted input (the
  `unhashed_rollup` arm), and multi-rollup sorted paths; goopg offers
  neither.
