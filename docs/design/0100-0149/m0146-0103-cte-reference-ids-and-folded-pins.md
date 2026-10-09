# M0146-0103 — CTE references carry their own relation id; an operator over literals pins

Status: done 2026-10-08 (6da04bc86). Parent: M0146-0042.

## Problem

TPC-DS Q39 joins two references to one CTE and orders by both:

```sql
from inv inv1, inv inv2
where ... and inv1.d_moy = 1 and inv2.d_moy = 1+1
order by inv1.w_warehouse_sk, inv1.i_item_sk, inv1.d_moy, inv1.mean, inv1.cov,
         inv2.d_moy, inv2.mean, inv2.cov
```

```
PG:     Sort Key: inv1.w_warehouse_sk, inv1.i_item_sk, inv1.mean, inv1.cov, inv2.mean, inv2.cov
goopg:  Sort Key: inv1.w_warehouse_sk, inv1.i_item_sk, inv1.mean, inv1.cov, inv1.d_moy, inv1.mean, inv2.cov
```

There were two independent causes. The executed rows were already in the
right order; only the plan text differed.

- **Labels.** A `CTEScan` published the CTE body's schema unchanged. The
  body's relation ids number the CTE's own query level, so both references
  carried the same ids. A narrowing Project above the self-join copies the
  join's ids into its targets. The Sort Key chase stops at the Project
  target for a materialized CTE and renders it by that id, which names
  `inv1`. A minimal case, `order by b.v, a.v` over `c a, c b`, printed
  `Sort Key: a.v, a.v`.
- **Pin.** `orderItemPinnedByWhere` (M0146-0005ak) accepted only a literal,
  possibly cast, as the constant side of a WHERE equality. `inv2.d_moy =
  1+1` was not recognised, so `inv2.d_moy` stayed in the Sort Key.

## PG behaviour

- Each CTE reference is its own range-table entry, and a Var over it
  carries that RTE's index. `get_variable` (ruleutils.c) names the column
  by that entry's alias.
- `preprocess_expression` runs `eval_const_expressions` on the quals before
  `deconstruct_jointree` builds equivalence classes. `1+1` is folded to the
  Const `2`, so `inv2.d_moy`'s class holds a constant, and
  `pathkey_is_redundant` drops it from `sort_pathkeys`. A built-in operator
  is never volatile, so the unfolded form is just as constant.

## Change

- **`cteRefSchema`** (planner.go). A `CTEScan` publishes the body's columns
  stamped with the reference's own range-table id, as a base scan does
  through `tableSchemaWithSource`.
- **`parserPseudoConstant`** (groupkeyconst.go) also accepts a unary or
  binary operator over pseudo-constants. It is shared by the ORDER BY pin
  and the GROUP BY pruning (`redundantConstGroupKeys`), so `GROUP BY m`
  under `m = 3-1` is pruned as PG prunes it.

## Verification

- **Probe.** Plans and results are identical to PG 18.3 for:
  - a self-join of a materialized CTE ordered by `b.v, a.v`;
  - the same with `b.m = 1+1` pinning `b.m`;
  - `m = 1+1`, `m = -(-2)` and `m = 3-1` pinning an ORDER BY or GROUP BY
    key, over matching and empty input;
  - `m = 1 + k`, which pins nothing.

  A self-joined data-modifying CTE already printed `Sort Key: b.v, a.v`.
- **Test.** `TestExplainCTESelfJoinSortKeys`. Reverting the schema stamp
  fails both cases; reverting the operator arm fails the pinned case.
- **TPC-DS fire set.** Only Q39 changes, and executed results are
  identical. Its Sort Key matches PG's at both scales: rendering 2 → 1 at
  SF0.25 and at SF1. Q39 still differs from PG in its CTE body's join
  order.
- **TPC-H.** Plans are byte-identical; acceptance arm values identical.
- **Regress A/B.** Eleven files (with, subselect, join, aggregates, union,
  window, groupingsets, select_distinct, matview, rangefuncs, select) are
  identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS; only Q39 changes shape) and ea-ratchet all PASS.
