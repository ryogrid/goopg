# M0146-0023: GROUP BY without aggregates is PG's Group node

Status: landed 2026-09-26 (label and cost; partial Group and HAVING-to-WHERE
open).

## PG behaviour

`add_paths_to_grouping_rel` (`postgres/src/backend/optimizer/plan/planner.c`)
builds a sorted grouping path two ways:

- `parse->hasAggs`: `create_agg_path(AGG_SORTED)`, printed `GroupAggregate`;
- otherwise, for a plain `GROUP BY` with no aggregates:
  `create_group_path`, a Group node printed `Group` (`explain.c`, `T_Group`).
  The label carries no Partial/Finalize prefix, even as the partial path under
  a Gather Merge (TPC-DS Q37/Q82 at SF0.25).

The two paths are costed differently.

- `cost_group` (`costsize.c`): the input cost, plus `cpu_operator_cost`
  per grouping column per input tuple, plus any HAVING quals.
- `cost_agg`'s AGG_SORTED arm: the same, plus transition and final costs,
  plus `cpu_tuple_cost` per output group.

The hashed path is still `create_agg_path(AGG_HASHED)`, which pays the
per-group emit charge. So an aggregate-free sorted grouping competes against
hashing `cpu_tuple_cost × numGroups` cheaper than goopg priced it.

## Change

- EXPLAIN (`operators_explain.go`): a sorted `Aggregate` with no aggregate
  calls, outside grouping sets, prints `Group`. DISTINCT is its own
  `Distinct` node, and the decorrelation aggregates in `unnest.go` always
  carry a call, so this condition is exactly PG's `!hasAggs` GROUP BY.
- Cost (`costAgg`): the sorted arm with `nAggs == 0` and at least one
  grouping column is `cost_group`, with no per-group emit charge. This
  covers the plain, index-ordered and partial sorted candidates, which all
  price through `costAgg`.

## Results

- SF0.25 census: Q97 → MATCH. Its `Group` over the merge-joined CTE outputs
  now wins as in PG, where goopg chose a HashAggregate. Q37 and Q82 move
  from depth 1 to depth 2 (next: PG's partial Group under Gather Merge).
- SF1 census: Q37 and Q82 move from depth 1 to 2 (next: PG's nested loop
  delivering presorted input).
- TPC-H census identical. Rows unchanged: sweep 96/96, fire set 2 fires,
  spotcheck, acceptance arm 24 MATCH, regress 41 = HEAD.
- Evidence: `analysis/m0146/m0146-0023/`.

## Open (ledgered)

1. PG's `subquery_planner` moves an aggregate-free HAVING clause into WHERE
   (no volatile functions, no SubPlan, no grouping sets). goopg keeps it
   as the grouping node's Filter.
2. `create_partial_grouping_paths` builds partial Group paths for an
   aggregate-free GROUP BY (Q37/Q82's `Group → Gather Merge → Group`).
   goopg's partial split does not offer one.
3. `cost_group` charges HAVING quals (`cost_qual_eval` and
   `clauselist_selectivity`); goopg's grouping cost does not.
