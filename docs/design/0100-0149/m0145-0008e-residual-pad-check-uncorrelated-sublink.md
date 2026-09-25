# M0145-0008e: the seam's residual pad check steps over an uncorrelated sublink

Status: landed 2026-09-24 `d7dad3c1a`. Movement: none on the category
instruments; TPC-H Q18 runs 13.17 s → 9.53 s on the jointree default.
Task: `.ralph/fix_plan.md` M0145-0008e (Kind: impl, Parent: M0145-0008b).
Evidence: `analysis/m0145/m0145-0008e/`: Q18 plans before and after on both
arms, and the DP trace lines before and after.

## The recon premise was wrong

M0145-0008b read the jointree arm's `Hash Join … width=1622` and its 869 MB,
2-batch hash as unnarrowed join tuples. A trace inside `narrowBuildInput`
refuted that: the jointree arm does narrow (`joinKeepSet`, keeping 2 of 8 and
5 of 6 columns). The difference is which side is hashed. The jointree arm
built its hash on the 6M-row `lineitem`; legacy built it on the 1.5M-row
`customer ⋈ orders`. The 1622 is just the join's output width.

## The real cause

The DP trace shows the search did its job. `{customer+lineitem+orders}` came
out cheapest as the legacy plan (total 353212.7), and then:

```
DPTRACE seam-decline reason=residual-hits-pad nrels=3 nleaves=3
```

- Q18's `o_orderkey IN (SELECT l_orderkey … GROUP BY … HAVING …)` is not
  pulled up (the ANY pull-up declines a grouped body), so it stays in the
  above-root residual.
- `searchedResidualHitsPad` walked the residual with `visitColumnRefsByName`,
  which reports any inner plan as partial. The fallback branch then declined
  whenever some padded name was statement-needed.
- The subquery's own `l_orderkey` / `l_quantity` are statement-needed and
  also name padded outer `lineitem` slots, so the seam threw the search away.
  The unsearched syntactic tree (customer, orders, lineitem left-deep,
  rule-era display costs) went to execution.

## Fix

- `residualColumnRefsByName` (`narrowoutput.go`): an uncorrelated inner plan
  (`planHasOuterRef` false) no longer makes the walk partial. An uncorrelated
  SubPlan reads the residual row only through its same-scope slots (the IN
  operand, PARAM_EXEC `Args`), which the walk visits as ordinary refs. A
  correlated plan still falls back, because its `OuterColumnRef`s are
  index-keyed and this check is name-keyed.
- `visitColumnRefsByName` and the new walker share one body,
  `walkColumnRefsByName` with a scope callback. The Expr-switch inventory
  key moved with it.

## Verification

- Q18 on the jointree arm: no seam decline; the plan is the searched one
  (probe `lineitem`, hash the narrowed `orders ⋈ customer`). Acceptance arm:
  24 MATCH, Q18 9.53 s.
- Gates: units, tpch-spotcheck, sf025 96/96 (99/99 shapes unchanged),
  fire-set, ea-ratchet 52/52. TPC-H categories unchanged.
- `TestScopedCacheDepthConsistency` had relied on the decline. Its bare `b IN
  (SELECT b FROM ht2)` now becomes the jointree ANY pull-up's semi join,
  which is PG's `convert_ANY_sublink_to_join`, so its IN moved under an OR.

## Found along the way

EXPLAIN omits one of two stacked Filters over a scan. For `SELECT a FROM ht1
WHERE a > 1 AND EXISTS (SELECT 1 FROM ht3)`, goopg prints only `Filter:
(EXISTS(SubPlan 1))` while both predicates execute (results are correct).
It happens at HEAD before this change too. Filed as M0145-0008j. PG prints
`a > 1` on the scan and the EXISTS as a One-Time Filter.
