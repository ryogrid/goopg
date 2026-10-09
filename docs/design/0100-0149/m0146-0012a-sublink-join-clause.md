# M0146-0012a — a correlated-sublink clause is a join clause

Status: done 2026-10-05. Slices A (placement), B (pricing) and C (the
hash join's quals charged on hashjointuples) landed. Parent: M0146-0012.
Evidence: `analysis/m0146/m0146-0012a/`.

## PG mechanism

- A correlated SubPlan's outer Vars become PARAM_EXEC Params, and its `args`
  are this scope's Vars. `pull_varnos` counts them toward the clause's
  relids, so `distribute_qual_to_rels` (initsplan.c) files TPC-H Q17's
  `l_quantity < (SubPlan on p_partkey)` as a {lineitem, part} join clause.
- `create_hashjoin_plan` prints it as the Hash Join's `Join Filter`. On a
  parameterised nested loop it is a ppi clause of the inner index scan, and
  prints as that scan's `Filter`.
- Costing: `cost_qual_eval` charges the SubPlan's per-call cost
  (`cost_subplan`: correlated EXPR_SUBLINK = plan run cost plus startup).
  - The nested loop pays it on every inner row; on Q17 that is about 5,940
    evaluations (+746k).
  - The inner-unique hash join pays it on `outer_matched_rows`, with
    `outer_match_frac = joinrel.rows / (outer.rows × inner.rows)`; on Q17
    that is 10 rows (+1.2k). That is why PG hash-joins Q17. Measured in
    `analysis/m0146/m0146-0005/slice3/`.

## Slice A — placement (landed 2026-10-05)

- **`sublinkjoinclause.go`** runs before the seam partitions conjuncts.
  - A conjunct holding only correlated scalar sublinks is cloned and
    pre-lowered with M0146-0015c's kept-subplan rebase, run with no pulled
    body (`rebaseQualKeptSubplans(cl, nil, …)`).
  - Each outer reference naming this scope becomes an ExecParamRef
    sentinel, with an Arg ColumnRef in problem space. `relidsOfExpr`
    counts the Args, and `translateToLayout` re-bases them onto the join
    row the search picks; `lowerSubPlanParams` renumbers the sentinels.
  - The clone is used only when its relids span two or more relations and
    every Arg names a base relation of the scope (non-zero rtid, not a CTE
    reference).
- **Declined, unchanged from HEAD:**
  - EXISTS and IN bodies: the unnest and EXISTS→ANY passes read their
    correlation off the body.
  - A sublink the unnest pass would decorrelate (`canUnnestSubquery`): PG
    keeps the SubPlan, and goopg's decorrelation is left exactly as it was.
  - Scopes with pulled-up FROM subqueries or sublinks: problem space is not
    the FROM layout there.
- **Hash and merge keys:** a sublink-bearing equality is never a hash or
  merge key (`joinrestrict.go`). PG does hash on `(SubPlan 1) =
  ps_supplycost` (TPC-H Q2); this is ledgered.
- **Clone aliasing fix:** `cloneExprReplacingOuter` deep-copies the kinds
  `cloneExprLeaf` returned shared (IS NULL, LIKE, COALESCE, …). The
  in-place rebase of a "clone" wrote `$-1` into the original plan of
  regress join's placeholder query ("SubPlan parameter $-1 read before
  assignment").
- **EXPLAIN:** a SubPlan in a Join Filter or in a parameterised inner's
  Filter records its PARAM_EXEC owner (the join or the loop) when
  assigned. A Level-1 OuterColumnRef Arg (a NestLoop param) counts as a
  source, so the body prints `pk = p.pk`, not `$0`.

### Result

- **TPC-H Q17:** the SubPlan filters the parameterised `lineitem` scan, and
  the join is serial above `Gather(part)`. This is PG's nested-loop shape;
  PG elects the hash join on cost, which is slice B. Runtime 0.40 → 0.43 s,
  24/24 MATCH.
- **TPC-DS Q32/Q92:** the SubPlan moves from above the join into the
  parameterised `catalog_sales`/`web_sales` scan. Checksums equal; fire-set
  categories unchanged at both scales.
- **Regress A/B, 14 files:** unchanged apart from known flaps. A probe of
  the placeholder case with a NULL `t2.q2`
  (`placeholder-null-probe.sql`) matches PG.

## Slice B — pricing (landed 2026-10-05)

- `subplan_cost.go` adds `joinQualPerTuple` and `joinQualEvalCost`: the flat
  per-conjunct charge plus `subPlanCostOps`' per-call cost for every
  correlated sublink. The saved slice-3 patch is re-based onto the existing
  `cost_subplan` port, not added as a duplicate.
  - It applies at the nested-loop, NLI, merge, hash, partial hash/merge,
    parallel hash and parameterised hash join sites, and in the semi/anti
    nested loop.
- `subPlanCostOps` takes `lowered`: a pre-lowered sublink is correlated.
  Before, it was priced as an InitPlan, at zero per call.
- An uncorrelated ANY is not charged per joined tuple. PG hashes it
  (`build_subplan` sets `useHashTable`, and `cost_subplan` charges it once at
  startup). `subPlanCostOps` prices the plain form instead, so charging it
  moved TPC-DS Q45's `OR i_item_id IN (…)` join above its Gather Merge
  (ledgered).
- Result: TPC-H Q17 and TPC-DS Q32/Q92 elect PG's Hash Join with the
  SubPlan as Join Filter.

  | fire set | before | after |
  |---|---|---|
  | SF1 join-order | 55 | 53 |
  | SF1 join-method | 24 | 22 |
  | SF1 parallelism | 44 | 42 |
  | SF0.25 join-method | 26 | 25 |
  | SF0.25 parameterisation | 26 | 25 |

  No category regresses; the TPC-H arm is 24/24 MATCH.
- Wall time, reported not judged (Goal): TPC-H Q17 0.43 → 6.2 s on PG's
  plan, where PG 18.3 itself takes 1.6–2.3 s. TPC-DS SF0.25 Q32 went
  16 → 240 ms and Q92 20 → 184 ms. Ledgered.
- ea-ratchet has one new key, `Q92:date_dim+web_sales` (232 vs 4795). PG
  estimates the same join at 234, so it is PG-shared and was re-pinned
  under G4 (`ea-repin-attribution.md`).

## Slice C — a hash join's quals charged on hashjointuples (landed 2026-10-05)

- The inner-unique factors were already PG's (M0146-0005e):
  `outer_match_frac` is the pair's inner-join selectivity, and
  `match_count` is the inner rows. The slice-3 README's "missing
  `/ inner.rows`" predates that work.
- The gap was where the residual was charged. `final_cost_hashjoin`
  charges `cpu_tuple_cost + qp_qual_cost.per_tuple` on `hashjointuples`:
  the matched outer rows of an inner-unique, semi or anti join, and the
  hash clauses' `approx_tuple_count` otherwise. goopg added the residual
  separately on the join's output rows.
- `hashJoinInputs.qualPerTuple` now carries `joinQualPerTuple(residual)`
  into `hashJoinCost`. The serial, parameterised, partial and Parallel Hash
  sites drop their separate charge.
- **TPC-H:** Q17 costs 198k (PG 212k, was 448k). All 22 plans keep their
  shape, and the arm is 24/24 MATCH.
- **TPC-DS:** Q32, Q65, Q92 and Q95 change cost or shape, with checksums
  equal.
  - Q92 now joins `web_sales` to `item` first, as PG does, and runs in
    118 ms (was 184).
  - That join runs under a Gather where PG's is serial: the
    parallel-restricted SubPlan gap.
  - Fire-set categories are unchanged.
- **ea-ratchet:** 10 → 9. `Q92:date_dim+web_sales` is FIXED, because Q92
  no longer forms that relation set; re-pinned in a standalone commit.
- **Fixture witness:** `TestCorrelatedScalarSublinkIsJoinClause` now
  elects PG 18.3's plans — the parameterised nested loop by default, and
  the Hash Join Join Filter with `enable_nestloop` off.

## Open

- **Parallel safety:** the SubPlan under a parallel nested loop still runs
  in workers for Q32/Q92, where PG keeps a correlated SubPlan
  parallel-restricted. This is pre-existing (HEAD ran it under the Gather
  too) and ledgered.
- **Hash keys:** a sublink equality as a hash key (PG Q2's
  `(SubPlan 1) = ps_supplycost`).
- **Hashed ANY:** an uncorrelated hashed ANY's cost (`cost_subplan`'s
  `useHashTable` arm) in `subPlanCostOps`.
