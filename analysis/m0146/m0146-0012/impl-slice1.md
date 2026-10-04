# M0146-0012 impl slice 1: a scalar sublink body's correlated restriction is a base-rel index qual (2026-10-05)

Code: `c21a02c93`. Test: `internal/executor/correlated_scalar_body_restrict_test.go`
(fails on HEAD `f87d2a404`, passes after). PG 18.3 values and shapes for the
test fixture: `impl-slice1/witness-pg.sql`, run on the scratch PG at :5534.

## What changed

- `planSubqueryExpr` marks the settings it hands a scalar sublink body
  (`PlannerSettings.scalarSublinkBody`, cleared one scope deep).
  `tryPGShapedJoinSearch` passes the mark to
  `partitionConjunctsForJoinPlanningScoped`, which admits correlated
  conjuncts as leaf restrictions in that multi-relation scope.
  EXISTS bodies stay excluded: the EXISTS→ANY pass reads their correlation
  off the body's top quals (TPC-DS Q35 lost its hashed ANY when they sank).
- `matchBitmapIndexQuals` records a same-type outer-param key's conjunct as
  `local`. `createBitmapHeapScanPlan` then makes it the Recheck Cond and
  drops it from the reinstated Filter, as the index-scan arm already does.
  This mirrors PG's `bitmapqualorig` vs `qpqual`. A literal key keeps its
  Filter, because this arm does not run `restrictionKeyUsable`'s cast checks.
- `BitmapHeapScan` embeds `searchedTree`. When the Filter was the leaf's
  only wrapper, the bare scan becomes a one-relation search root
  (`TestIndexKeyCorrelatedExistsDecorrelates` and
  `TestCompositeExistsBothIndexShapes` panicked in `markSearchedTree`
  without it).
- `dedupeUnnestParams` collapses repeated correlation pairs. One
  correlation used to reach the collectors up to three times (leaf Filter,
  probe `Key`, `Keys[0]`) and became three GROUP BY and hash keys.
  `Keys[0]` repeating `Key` was already the case on HEAD. A copy that is
  a distinct `*OuterColumnRef` is kept as an `Alias`, so the clone still
  replaces it.

## TPC-H (SF1, arm data clone, `GOOPG_ANALYZE_SEED` pinned)

- Full arm: 24/24 MATCH on values (`tmp/arm-m0012c.txt`).
- A/B at the same server age, `QUERIES=2,21,22`, two rounds per binary
  (`impl-slice1/arm-ab-*.txt`):

  | | HEAD r1 | HEAD r2 | new r1 | new r2 |
  |---|---|---|---|---|
  | Q2 | 1.32 s | 1.36 s | 0.19 s | 0.20 s |
  | Q21 | 4.76 s | 4.57 s | 3.82 s | 4.31 s |
  | Q22 | 0.29 s | 0.30 s | 0.29 s | 0.32 s |

- Plans (`impl-slice1/tpch-q2-q17-q20-{head,new}.plans.txt`):
  - **Q2**: HEAD decorrelated the min() into a HashAggregate over all of
    partsupp (800 000 rows). Now it keeps PG's SubPlan: an Aggregate over a
    Nested Loop whose Join Filter reads region first, probing
    `partsupp_part_fkidx` with `Index Cond: (ps_partkey = part.p_partkey)`.
    The 2026-09-25 note that keeping this SubPlan "costs 1.50 s → 307 s
    until the SubPlan inner can probe partsupp" no longer holds: the
    probe is now the inner.
  - **Q17 and Q20**: their single-relation bodies already probed by the
    outer key. They now print `Recheck Cond:` where they printed `Filter:`,
    and Q20 keeps only the shipdate window as Filter, as PG does.

## TPC-DS

- SF0.25 sweep PASS. Only Q32 and Q92 change plan, with identical
  checksums. Q32 goes 265 → 18 ms and Q92 36 → 21 ms.
- Both SubPlans now match PG's: Aggregate → Nested Loop → Bitmap Heap Scan
  on `catalog_sales`/`web_sales` with
  `Recheck Cond: (cs_item_sk = item.i_item_sk)`, then the `date_dim`
  probe (`impl-slice1/tpcds-sf025-Q{32,92}-new.plan.txt`).
- The outer joins still differ from PG (pre-existing): goopg nests
  item → sales; PG hash-joins sales⋈date_dim against item.
- Fire set (`tmp/fireset-m0012`): no category regresses.

  | category | SF0.25 before → after | SF1 before → after |
  |---|---|---|
  | aggregation-strategy | 11 → 9 | 19 → 17 |
  | parallelism | 29 → 28 | 44 → 44 |

  Match counts are unchanged (42 at SF0.25, 33 at SF1).
- `ea-ratchet` PASS, 1 fixed (`Q92:date_dim+web_sales`); baseline re-pinned
  (9 entries).

## Regress A/B (HEAD worktree vs new, 14 files)

The files: subselect, join, with, aggregates, create_index,
select_parallel, rowsecurity, updatable_views, inherit, partition_prune,
union, btree_index, select_distinct, plpgsql.

The only changed hunks are rowsecurity's printed pointer addresses and
plpgsql's known flap (`f1(42)`, polymorphic overloads).

## Residuals

- In goopg's own decorrelated shape (a WHERE scalar sublink the unnest pass
  still rewrites), the group key prints `cs_o.k` where HEAD printed `cs_s.k`,
  and the inner Hash Cond prints a bare `dk` (re-checked after the bitmap
  change). The sunk leaf conjunct's ColumnRef carries no source table. This is
  rendering only: `resolveSubColInSchema` bails to the SubPlan on an
  ambiguous name. PG never decorrelates a scalar sublink, so the shape has
  no PG twin.
- Still open in M0146-0012:
  - retire `flattenStrandedSeqScanFilters` (the fix_plan entry names it
    `flattenCorrelatedSeqScanFilters`) and the restoring rule;
  - multi-relation EXISTS bodies, which need the EXISTS→ANY pass to read
    correlation from leaves.
- A correlated MATERIALIZED CTE stays unsafe until M0146-0050. This slice
  sinks conjuncts only onto the body's own relations, never into a CTE
  body.
