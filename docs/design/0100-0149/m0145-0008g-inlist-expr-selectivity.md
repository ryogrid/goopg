# M0145-0008g: IN list over an expression operand estimated as PG's scalararraysel

Status: landed 2026-09-24 `70d0478bd`. Movement: yes (CATEGORIES-EXCL-MATCH,
TPC-H Q22). Follow-up: M0145-0008l (the nestloop semi/anti costing arm that
still keeps Q22 off PG's NL anti join).
Task: `.ralph/fix_plan.md` M0145-0008g (Kind: impl, Parent: M0145-0008b).
Evidence: `analysis/m0145/m0145-0008g/`.

## PG

`scalararraysel` (`selfuncs.c:1821`) calls the element operator's own
estimator for every element and merges the results: OR for ANY, with the
equality masses summed when the sum stays in [0,1]. For
`substr(c_phone, 1, 2) = '13'` that estimator is eqsel → `var_eq_const`. With
no statistics for the expression, `var_eq_const` answers
`1 / get_variable_numdistinct`, i.e. 1/DEFAULT_NUM_DISTINCT = 1/200 (or
1/ntuples on a relation smaller than that). TPC-H Q22's 7-element list is
therefore 0.035: PG estimates 5250 of 150000 customers.

## goopg before

Both IN-list arms (`clauseSelectivity`, `clauseSelectivityWithSource`)
returned `defaultGenericSelectivity` (1/3) for any operand that was not a bare
column. Q22's customer leaf was over-estimated about 10x, and everything above
it was priced from that.

## Change (`internal/optimizer/selectivity.go`)

- `inListMergeSelectivity`: the scalararraysel merge, factored out of
  `inListSelectivity` so both operand kinds share it.
- `inListExprSelectivity`: for an expression operand, each element's estimate
  is the scalar clause estimator applied to the synthesized
  `operand <op> element` (`=` for IN, `<>` for `!= ANY`, else the ANY
  operator). By construction it agrees with the written-out comparison.
- Both arms use it. `clauseSelectivityWithSource` marks the result
  unreliable, because PG's own answer is the `isdefault` fallback.

## Result

- Q22's customer leaf: 1750 rows (PG: 1750 total).
- The upper plan became PG's GroupAggregate over Gather Merge.
- TPC-H categories: Q22 drops `parameterisation`, `aggregation-strategy` and
  `sort-strategy`, and gains `rendering` (net −2). TPC-DS SF0.25 categories
  are unchanged. ea-ratchet 52/52.

## Why Q22 still hash-anti-joins (M0145-0008l)

The DP trace for `{customer+orders}` keeps one path (hash). With hash joins
disabled, goopg's NL anti costs `5450 + 1500 × 55.85 = 89470`: the full
bitmap-heap inner is charged for every outer row. PG differs in two ways:
- It prices the NL anti join through `final_cost_nestloop`'s semi/anti branch
  (`outer_matched_rows`, `inner_scan_frac`: the scan stops at the first
  match).
- Its inner is a parameterized Index Only Scan on `order_customer_fkidx`
  (4.01 per probe).

goopg's `nestloopCost` has no semi/anti branch, and joins get no index-only
inner path. Separately, the customer leaf is not parallel-safe once it
carries the InitPlan qual (M0146-0002f).
