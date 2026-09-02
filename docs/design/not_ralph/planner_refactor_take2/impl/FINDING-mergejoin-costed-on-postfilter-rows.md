# FINDING — the merge join is costed on POST-filter rows while it emits PRE-filter rows

**Status:** open, actionable, not yet fixed. This is the mechanism behind the
P2-02b regression, superseding two earlier and wrong explanations in
`FINDING-planner-settings-not-propagated.md` (a propagation gap, then a
width/batching story). Both are withdrawn there.

## The defect

`joinpathsmerge.go:362`:

```go
cost := mergeJoinCost(cp, op.Cost, ip.Cost, op.Rows, ip.Rows, joinrel.Rows)
// The residual is evaluated on the tuples that already matched on the
// merge keys, i.e. the join's output — the same charge the hash arm makes
// (`final_cost_mergejoin`'s qpqual term on `mergejointuples`,
// costsize.c:4045).
cost.Total += qualEvalCost(cp, len(residual), joinrel.Rows)
```

The comment names the right quantity — `mergejointuples` — and the code passes
the wrong one. `joinrel.Rows` is the row count **after every join clause**,
including the `residual` clauses the merge cannot use. The merge operator emits
the tuples matching the MERGE clauses only; the residual filters them
afterwards. When the merge uses a strict subset of the equi-clauses those two
numbers differ, and goopg charges the smaller.

Upstream charges the larger (`final_cost_mergejoin`, costsize.c:4045):

```c
run_cost += cpu_per_tuple * mergejointuples;
```

`mergejointuples` comes from the merge clauses' selectivity, and `cpu_per_tuple`
already includes the qpqual evaluation — so PG charges both the per-tuple cost
AND the residual evaluation over the tuples actually produced.

## Evidence — TPC-H Q9, SF=1

The join is `lineitem x partsupp`, DP relation `{2,4}`. It has two equi-clauses
(`ps_partkey = l_partkey` and `ps_suppkey = l_suppkey`). goopg's merge join uses
**one**:

```
Merge Join  (cost=0.75..826630.32 rows=24989610 width=1094) (actual rows=24005020.00)
      Merge Cond: (partsupp.ps_partkey = lineitem.l_partkey)
      ->  Index Scan using partsupp_part_fkidx on partsupp   (cost=0.38..92032.11  rows=800000)
      ->  Index Scan using lineitem_part_supp_fkidx on lineitem (cost=0.38..657582.53 rows=6001255)
```

Arithmetic:

- Total 826 630.32 minus the two index scans (92 032.11 + 657 582.53 =
  749 614.64) leaves **77 015.68** for the join itself.
- It emits 24 989 610 tuples (actual 24 005 020). 77 015.68 / 24 989 610 =
  **0.0031 per tuple**, while `cpu_tuple_cost` alone is **0.01**.
- 6 001 255 x 0.01 = 60 012, which is the order of the charge actually made.
  The path was costed for the **6.0M** post-filter rows, not the **25.0M** it
  emits — an undercharge of ~4x.

`DPPATH` confirms every path for `{2,4}` agrees on `rows=6001255`, so the
joinrel's row estimate is correct and path-independent; only the merge's *cost*
is wrong.

## Why it shows up as a `work_mem` regression

The merge cost is `work_mem`-independent; the hash cost is not. Best totals for
`{2,4}`:

| budget | `join.hash` | `mergejoin` | winner |
|---|---|---|---|
| 1 GB (goopg's inflated default) | 627 414.38 | 826 630.32 | HASH |
| 128 MB (PG's `work_mem` x 2, i.e. correct) | 1 906 774.38 | 826 630.32 | MERGE |

At a realistic budget the hash path's price triples and crosses the
under-priced merge path. The plan then carries 24M rows instead of 6M through
the rest of Q9 and loses its `Gather`: **15.4 s -> 187 s**. PostgreSQL, given
the same budget, keeps a two-key parallel hash join and finishes in 6.2 s.

This is what made goopg's 512 MB `work_mem` default (128x PG's) load-bearing:
it was buying enough headroom to keep the hash path below a merge path that
should never have been cheaper.

## Fix

Charge the merge join over the tuples it emits:

```
mergeTuples = joinrel.Rows / selectivity(residual)
cost        = mergeJoinCost(..., mergeTuples)      // cpu_tuple_cost term
cost.Total += qualEvalCost(cp, len(residual), mergeTuples)
```

`tryMergeJoinPath` / `addMergeJoinPath` have `residual` but **not** a
`searchCtx`, and `joinClauseSelectivity` is a `searchCtx` method
(`joinselectivity.go:323`). Two options:

1. Thread `*searchCtx` (or just a selectivity closure) into the two merge-path
   helpers. Smallest change; the closure form avoids widening the helpers'
   coupling.
2. Cache each clause's selectivity on `restrictInfo` — PG's `norm_selec`. This
   is P1-12's still-open caching half, so the two items would land together.

Option 1 first is recommended; it is the one that unblocks P2-02b.

## Gates

A cost-model change reaching every merge join needs, per 09:

- `cmd/tpch-runner -digest` + `-diff`, all 24 items, **on values** — a plan-shape
  change is a wrong-answer class, and row counts alone have already passed a
  broken build once (P4-01b, Q18).
- TPC-H timing A/B, fresh server per arm, and the TPC-DS SF0.5 gate
  (0 MISMATCH).
- `RALPH_PRECOMMIT_SCOPE=units`.

Expect merge joins to lose ground broadly; that is the intent, but every query
whose plan moves must be timed, not just counted.

## Predicted follow-on

With this corrected, P2-02b (`work_mem` BootVal 512MB -> PG's 4MB) should cost
far less than the measured 245.7 s -> 314.4 s, because the flip it triggers is
this mispricing. That prediction is the acceptance test for this item.
