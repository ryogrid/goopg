# M0146-0002d: a SubPlan-bearing restriction is not placed on its base relation

Status: recon complete 2026-09-24. Follow-up implementation: M0146-0002e,
M0146-0002f, M0146-0002g.
Task: `.ralph/fix_plan.md` M0146-0002d (Kind: recon, Parent: M0146-0002c).
Evidence: `analysis/m0146/m0146-0002d/` (PG 18.3 and goopg EXPLAIN of TPC-H Q16).

## The divergence

PG 18.3 on `:65432` (`q16-pg-plan.txt`):

```
Parallel Hash Join
  ->  Parallel Index Only Scan using partsupp_pk on partsupp
        Filter: (NOT (ANY (ps_suppkey = (hashed SubPlan 1).col1)))
  ->  Parallel Hash -> Parallel Seq Scan on part
```

goopg at `c2339a072` (`q16-goopg-plan.txt`): the same filter sits on a
serial `Hash Join` under a post-pass Gather, rendered
`NOT (partsupp.ps_suppkey = ANY (SubPlan 1))`.

## Findings: three independent causes

1. **Placement.** `conjunctIsLocalEligible`
   (`internal/optimizer/local_filters.go`) declines any conjunct that holds a
   sublink, correlated or not. `partitionConjunctsForJoinPlanning` therefore
   keeps Q16's NOT IN in the join residual, and the seam evaluates it above
   the join. PG distributes a restriction by the relids of its Vars
   (`distribute_qual_to_rels`, `initsplan.c`). An uncorrelated SubPlan adds no
   relids, so `ps_suppkey NOT IN (…)` becomes a `partsupp` base restriction.
   - The decline exists because `localizeExprToLeaf` rebases with
     `scopeVeto`, and a correlated inner plan's `OuterColumnRef`s are in the
     enclosing scope's coordinates.
   - An uncorrelated sublink (`IsNonCorrelated`) has no such references. Its
     only same-scope columns are in the testexpr (`InExpr.Left`), which
     `cloneExprRefs` rebases under `scopeIgnore`. `tableForCol` already
     attributes with `scopeIgnore`.
   - Correlated sublinks additionally need their outer refs remapped
     (`remapOuterRefsInSubplan`). That is out of scope for the first slice.
2. **Parallel safety.** `exprsParallelSafe` (`considerparallel.go`) marks every
   expression with an inner-plan slot parallel-unsafe. PG's
   `max_parallel_hazard_walker` (`clauses.c:900-912`) admits a SubPlan whose
   plan is `parallel_safe` and checks its testexpr with the SubPlan's
   `paramIds` counted as safe. So once the qual is on `partsupp`, goopg's
   `relConsiderParallel` would still refuse a partial path for that relation,
   and the Parallel Hash / Gather Merge shape stays unreachable.
   - Executor precondition: each worker evaluates the SubPlan in its own
     context (PG builds one hash table per worker). The sublink result caches
     (`subqCacheSafe` / `subqCacheScoped`, `subq_cache.go`) and the hashed
     probe (`subplan_hash.go`) must be proved worker-local or thread-safe,
     using a parallel-vs-serial identity test under `-race`.
3. **Label.** Execution is already hashed (`subplan_hash.go`, PG's
   `buildSubPlanHash` / `ExecHashSubPlan`). Only EXPLAIN differs: PG deparses
   a hashed ANY SubPlan as `ANY (x = (hashed SubPlan N).col1)`, with `NOT (…)`
   around it for NOT IN (`ruleutils.c`, the SubPlan arm of `get_rule_expr`).

## Follow-up

- **M0146-0002e (impl):** make an uncorrelated sublink conjunct local-eligible
  and rebase it with `scopeIgnore`. Expected movement: TPC-H Q16
  `qual-placement`. Correlated sublinks stay declined and are recorded in the
  ledger.
- **M0146-0002f (impl):** port PG's SubPlan parallel-hazard rule, together
  with worker-safe SubPlan evaluation. Expected movement: Q16 `parallelism`
  once 0002e has landed.
- **M0146-0002g (impl):** render the hashed SubPlan the way PG does. Expected
  movement: Q16 `rendering`.
