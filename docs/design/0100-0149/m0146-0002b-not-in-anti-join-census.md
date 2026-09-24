# M0146-0002b: `NOT IN` becomes an anti join, where PG keeps a hashed SubPlan

Status: recon complete 2026-09-24. Follow-up implementation: M0146-0002c.
Task: `.ralph/fix_plan.md` M0146-0002b (Kind: recon, Parent: M0146-0002).
Raw evidence: `analysis/m0146/m0146-0002b/not-in-probe.txt`.

## The divergence

PG never turns `x NOT IN (SELECT y …)` into a join.
`pull_up_sublinks_qual_recurse` converts ANY_SUBLINK and EXISTS_SUBLINK only
(`./postgres/src/backend/optimizer/prep/prepjointree.c:665/731`). `NOT IN` is
`<> ALL`, an ALL_SUBLINK, so it stays a filter on the scan, hashed when the
subquery is uncorrelated: `Filter: (NOT (ANY (x = (hashed SubPlan 1).col1)))`.

goopg's legacy unnest pass still converts it on the jointree default arm
(`canUnnestInExprDepth` and the `effNegated` arm of the IN unnest in
`internal/optimizer/unnest.go`), into a null-aware `Hash Anti Join`. The
jointree pull-up (M0145-0003) already refuses NOT IN, as PG does. The
conversion happens later, in the shared post-hoc unnest.

## Findings

1. **Correctness: refuted.** The 2026-09-21 ledger row asked whether the
   conversion returns rows PG would not when the inner column holds NULLs.
   On HEAD `9aa73d50c`, goopg and PG 18.3 return identical results for six
   cases: an inner NULL, an outer NULL, an empty inner, an inner emptied by a
   filter, `IS NOT NULL` inner, and a correlated form. goopg's anti join is
   null-aware (`NullAware`, and the executor's `antiBuildHasNull` decides the
   three-valued result), so this is purely a plan-shape divergence.
2. **Corpus reach: one query.** The canonical slice-2 captures carry anti
   joins in TPC-DS Q16, Q69, Q78 and Q94, all from NOT EXISTS; no TPC-DS query
   uses NOT IN. In TPC-H, Q21 and Q22 are NOT EXISTS and **Q16** is the
   only NOT-IN-derived anti join.
3. **What it costs on Q16** (from M0146-0002's slice 3). The anti join sits
   outside the join search, so the Gather above it is added by the post-pass
   over a serial plan. That post-pass can elect neither the Parallel Hash
   (filed and cheapest inside the search) nor PG's Gather Merge +
   GroupAggregate shape.

## Follow-up

M0146-0002c (impl): drop the NOT IN (`Negated`) arm of the legacy IN unnest,
so NOT IN plans as the hashed SubPlan PG builds. goopg already runs hashed
SubPlans (`(hashed SubPlan N)`, `TestHashedInProbeActuallyFires`).
- **Expected movement:** TPC-H Q16 loses the anti-join-driven divergences
  (`parallelism`, `aggregation-strategy`, `join-order`), measured on the
  canonical parallel TPC-H capture.
- **Risk to watch:** tests that pin the NOT IN → anti conversion. The legacy
  unnest's other arms (IN / EXISTS / NOT EXISTS) are untouched.
