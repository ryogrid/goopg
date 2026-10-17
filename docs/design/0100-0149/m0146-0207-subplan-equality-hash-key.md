# M0146-0207 — a PARAM_EXEC SubPlan equality is a hash clause

Status: done 2026-10-16. TPC-H Q2's plan shape now matches PG's (measurements below).

## Problem

TPC-H Q2 keeps `ps_supplycost = (SELECT min(ps_supplycost) ... WHERE p_partkey = ps_partkey ...)` as a correlated
SubPlan. PG's top join is:

```
Hash Join
  Hash Cond: ((part.p_partkey = partsupp.ps_partkey) AND ((SubPlan 1) = partsupp.ps_supplycost))
  ->  Gather  (part, 779 rows)                      -- outer, probes
  ->  Hash    (nation ⋈ region ⋈ supplier ⋈ partsupp, 160000 rows)
```

1. **Relids.** The SubPlan's only argument is `part.p_partkey`, so `pull_varnos` gives the SubPlan the relids {part}.
   The equality is therefore a join clause with one operand on {part} and the other on {partsupp}.
2. **Hashable.** `check_hashjoinable` (`postgres/src/backend/optimizer/plan/initsplan.c:3833`) rejects only a
   pseudoconstant clause, a clause that is not a two-argument operator clause, and a volatile clause. A SubPlan is
   none of these, so the equality is a hash clause.
3. **Cost.** `cost_qual_eval_walker` adds the SubPlan's `per_call_cost` to the clause's per-tuple cost
   (`postgres/src/backend/optimizer/path/costsize.c:5019`). `hash_qual_cost` is that cost over the hash clauses.
   `final_cost_hashjoin` charges it per outer row on the bucket walk (`costsize.c:4458`, `:4497`). With an inner-unique
   inner it also charges it on the unmatched outer rows (`:4475`, × 0.05).
4. **Direction.** Probing with 160000 `partsupp` rows would pay the SubPlan about 160000 × 46.9 × 0.05. That is why
   PG puts `part` outer.

PG's own pathlist confirms this. A PG 18.3 `-DOPTIMIZER_DEBUG` build, loaded with the five Q2 tables from `:65432`,
keeps only the part-outer HashPath (47670.17) at the top joinrel.

goopg kept every sublink-bearing equality as a residual (`exprCarriesSublink`, M0146-0012a). It printed
`Join Filter: (partsupp.ps_supplycost = (SubPlan 1))`. As a residual, the SubPlan's cost was charged on the hash
matches of an inner-unique `part` build. Those are few, so the partsupp-outer order won (27264.24).

## Change

- **Clause (`buildRestrictInfosExcluding`, joinrestrict.go).** An equality whose sublinks are all PARAM_EXEC scalar
  SubPlans is an equijoin: `paramExecScalarSublinksOnly`, sublinkjoinclause.go. That means `len(Args) ==
  len(ParParam) > 0`, and the body has no outer reference left (`planHasOuterRef`). Its operand relids are those of
  its Args, which is pull_varnos. The clause is marked `restrictInfo.subplanKey`. The rule is off under
  `GOOPG_SCALAR_UNNEST`, as in M0146-0203.
- **Hash only.** The merge arms (`mergeableKeys`, joinpaths.go) move a `subplanKey` clause into their residual.
  `indexableJoinClausesFor`, `mergeableColumnExprsFor` and the useful-pathkeys census skip it. PG's
  `check_mergejoinable` (initsplan.c:3796) would make it a mergeclause too. goopg does not sort on, or probe an
  index with, an expression that runs a SubPlan. This divergence is ledgered.
- **Cost.** No new term is needed. `hashClausesPerTuple` already prices a sublink through `qualEvalOps`
  (`priceSublinks=true`), so the SubPlan cost moves from `qp_qual_cost` to `hash_qual_cost` as in PG. An extra
  `subPlanJoinQualOps` term would count it twice.
- **Published keys.** `fillOneJoinHashKeys` rebuilds `Join.HashKeys` from the predicate after planning, through
  `forEachEqualityForHash`. `residualExcluding` drops the same conjuncts through `conjunctIsOneOfKeys`. Both now use
  `hashKeyExprSide`, which puts a PARAM_EXEC SubPlan on the side of its Args: `exprSideUnder` with `scopeIgnore`.
  `exprSide` itself is unchanged for its other callers (cardinality.go, pushdown.go).
- **Executor.** No change. A hash key is evaluated against its side's merged-layout slot
  (`lazyBuildKeySlot.rebind`, operators\_join\_agg.go). A SubqueryExpr reaches `evalSubquery`, which binds Args from
  that row. A key whose type `pairIsHashSafe` cannot resolve stays in the residual (`ExecHashKeyPlan`). The value is
  then re-checked per match, which is always correct.
- **Tests.** `TestParamSubPlanEqualityIsAHashKey` covers the clause, the split, the hash qual cost, the merge and
  index exclusion, and the fact that an unlowered sublink stays a residual. `TestHashKeySideReadsSubPlanArgs` covers
  the key list, the residual, and `exprSide` being unchanged. The expr-switch inventory key moves from
  `exprSide` to `exprSideUnder`.

## Measured

- **Q2 EXPLAIN** on the reloaded TPC-H corpus (audit arm, plan-only, parallel):
  `Hash Join (cost=23707.08..50310.94)` with `Hash Cond: ((part.p_partkey = partsupp.ps_partkey) AND ((SubPlan 1) =
  partsupp.ps_supplycost))`, `Gather` over `part` as the outer side. Text-shape-identical to PG.
- **Remaining cost gap** (50310.96 vs PG 48872.73): the `part` estimate, 899 vs PG's 779 for `p_type LIKE '%BRASS'
  AND p_size = 15`. This is a separate restriction-selectivity question.
- **Parity captures** in `analysis/m0146/m0146-0207/`, compared with the M0146-0203 captures:
  - TPC-H match 19 → 20 (Q2 MATCH). Q2 is the only changed plan. CATEGORIES-EXCL-MATCH join-order 1 → 0 and
    parameterisation 1 → 0. The remaining non-matches, Q13 and Q16, are scan-type: the M0146-0019 owner residual.
  - TPC-DS SF0.25 and SF1: the fire-set A/B found no changed plan (84/99, 71/99).
- **Values:** the TPC-H acceptance arm executed every query and matched the baseline on values (24 MATCH; Q2 469
  rows).
