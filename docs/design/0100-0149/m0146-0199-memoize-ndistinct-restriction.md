# M0146-0199 — Memoize ndistinct follows estimate_num_groups, and a cloned SubPlan keeps its cache

Status: done 2026-10-16 — TPC-DS SF1 Q32 and Q92 match PG, with the Memoize
over the `date_dim_pkey` probe inside SubPlan 1.

## Problem

In TPC-DS SF1 Q32, SubPlan 1 nested-loops
`catalog_sales_1` (filtered to 88 rows by `cs_item_sk = item.i_item_sk`) into
a `date_dim_pkey` probe. PG puts a `Memoize` over the probe (Nested Loop
996.41); goopg probed bare (1009.29). Q92 is the same shape over `web_sales`.
Two independent causes:

1. **ndistinct.** `cost_memoize_rescan` (costsize.c:2581) takes the cache-key
   ndistinct from `estimate_num_groups` with `calls` as the input row count.
   For each relation, `estimate_num_groups` clamps the product of its keys'
   ndistincts to the relation's tuples. When the relation is filtered
   (`rel->rows < rel->tuples`, selfuncs.c:3721), it also applies the
   Yao/Dell'Era term n·(1 - ((N-p)/N)^(N/n)).

   goopg's `memoizeKeyNDistinct` multiplied the raw per-column counts and
   nothing else. With 1830 distinct `cs_sold_date_sk` values, the `calls`
   clamp pinned the count at 88, so the hit ratio was 0 and the bare probe
   won. PG gets 86 of 88, a hit ratio of about 2%, and its Memoize wins by
   1.3%.
2. **The cache was lost after costing.** The kept-sublink rebase
   (`keptRebase.rebaseOne`, pulledsublink.go) clones the SubPlan's plan
   through `clonePlanReplacingOuter`. The clone's `NestedLoopIndexJoin` arm
   set `InnerMemo = nil`, because `InnerMemo.Child` aliases the old `Inner`.
   With fix 1 alone, the join carried PG's Memoize cost (996.41) but ran and
   printed without the node. `remapSourceTableIdx` had the same arm.

## Change

- `memoizeKeyNDistinct` (`joinpathsmemoize.go`) is now `estimate_num_groups`
  over the bare-column keys:
  - keys are grouped by base relation, and a key repeated in the list is
    counted once (`add_unique_group_var`);
  - each relation's product is clamped to its tuples, or to a tenth of them
    when it contributes several keys, but never below the largest single
    ndistinct;
  - the Yao/Dell'Era term applies when the relation's filtered rows are below
    its tuples, followed by `clamp_row_est`;
  - the result is the product over relations, rounded up with `ceil`.

  The `SELFLAG_USED_DEFAULT` → `calls` rule is unchanged.
- `rebindInnerMemo` (`unnest.go`) rebuilds the cache over the cloned or
  remapped inner. The keys are read back from the new probe, so cache key and
  probe key stay one list. It is used by both `clonePlanReplacingOuter` and
  `remapSourceTableIdx`. An inner that is no longer a keyed probe gets no
  cache.
- Tests:
  - `TestMemoizeKeyNDistinctAppliesRestrictionTerm`: 1830 → 86 at 88 of
    1441557 rows, a repeated key counted once, and the rescan pricing below
    the bare probe.
  - `TestClonedNLIKeepsItsCache`: both clone paths keep the cache, which
    wraps the new probe on its own keys.

## Measured

Captures in `analysis/m0146/m0146-0199/`, compared with the M0146-0198
captures:

- **TPC-DS SF1:** match 69 → 71 (Q32, Q92). Only those two plans changed.
  The fire set executed both, and the values are identical to the baseline
  and to PG's (Q32 19064.45, Q92 NULL).
- **TPC-DS SF0.25 and TPC-H:** plan text byte-identical (84/99, 15/22).

## Residual

- `estimate_multivariate_ndistinct` is not consulted for multi-key caches.
  The GROUP BY port's exact-set cut (`groupComboNDistinct`) reads plan nodes,
  not the search's relations.
- goopg keys its cache on binary Datums, so it is always in PG's binary
  mode. EXPLAIN prints `Cache Mode: logical` as PG does for these keys
  (ledgered at P5.4b).
