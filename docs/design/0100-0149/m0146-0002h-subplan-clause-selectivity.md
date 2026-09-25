# M0146-0002h: a SubPlan clause is estimated at PG's default selectivity

## PG behaviour

`clause_selectivity_ext` (postgres/src/backend/optimizer/path/clausesel.c)
has no arm for a SubPlan. A clause that stays a SubPlan, such as
`x IN (subquery)` or `EXISTS (subquery)`, reaches its final branch,
`boolvarsel` (selfuncs.c). With no statistics for the expression that
returns 0.5, and a `NOT` above it gives 1 - 0.5. TPC-H Q16's
`NOT (ANY (ps_suppkey = (hashed SubPlan 1).col1))` on partsupp is
therefore estimated at half the table: 100000 rows per worker over four
workers in PG.

## goopg before

`clauseSelectivity` and `clauseSelectivityWithSource` returned
`defaultGenericSelectivity` (1/3) for an `InExpr` with a subquery `Plan`,
and an `ExistsExpr` fell through to the same default. Q16's partsupp leaf
was estimated at 266667 rows (800000 / 3).

## Change

Both twin estimators return `subPlanClauseSelectivity` (0.5) for an
`InExpr` with a `Plan` and for any `ExistsExpr`, negated or not. The
second estimator reports it as unreliable, because it is a default. The
OR-arm estimator (`orInListSelectivity`) already declined these to 0.5.

## Results

- TPC-H Q16's partsupp leaf: 400000 rows, PG's total (100000 per worker ×
  4). goopg's `rows=` on a filtered parallel scan shows the total rather
  than the per-worker count; that is a pre-existing display difference.
- TPC-DS Q10 and Q35 change plan (their residual EXISTS SubPlans); the
  first-divergence census is unchanged at both scales. Row counts, TPC-H
  census, spotcheck, acceptance arm and the regress cases are unchanged.
  Evidence: `analysis/m0146/m0146-0002h/`.

## Not covered (ledgered)

- A scalar-subquery comparison (`x = (SELECT ...)`) still goes through
  the operator estimators with an unknown right side. PG's eqsel uses
  `var_eq_non_const` there.
- The per-worker row display of a filtered parallel scan.
