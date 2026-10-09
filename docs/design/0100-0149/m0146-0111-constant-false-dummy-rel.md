# M0146-0111 — a constant-false WHERE makes the scope's relation dummy

Status: done 2026-10-08 (831e03903). Parent: M0146-0005.

## Problem

`SELECT … WHERE false` (or `1 = 2`, or `null::boolean`) planned every
scan and join under a `Filter: (false)`:

- a Seq Scan;
- a Merge Join with two Sorts;
- a fenced subquery together with its InitPlan.

PG 18.3 prints a single childless `Result  One-Time Filter: false`.

## PG behaviour

- `eval_const_expressions` folds the WHERE conjunct to a constant.
- A constant-false (or NULL) restriction makes the base relation dummy
  (`relation_excluded_by_constraints`, then `set_dummy_rel_pathlist`).
- An inner join over a dummy input is dummy itself (`is_dummy_rel` in the
  join path builders).
- A dummy rel plans as a childless Result whose `resconstantqual` is
  false.
- Upper stages (Aggregate, Sort, Group, Limit) stay above that Result.

## Change

`gatePseudoconstantQuals` runs on every scope's FROM/WHERE tree after the
join search. It now first checks that tree's top Filter chain with
`hasConstantFalseConjunct`; casts are looked through, so `null::boolean`
counts. A constant FALSE or NULL conjunct replaces the whole tree with a
childless `Result{OneTimeFilter: false}`, which publishes the same output
schema. The NOT NULL reduction already builds that node for its
always-false case.

## Verification

- **Probe** (PG 18.3 vs goopg). Each of these is identical to PG:
  - WHERE false on a scan, alongside a real qual, and over a join;
  - a fenced subquery that holds an InitPlan;
  - `1 = 2` and `null::boolean`;
  - count(\*), ORDER BY, GROUP BY and LIMIT above the dummy rel;
  - `EXISTS (… where false)`;
  - a correlated scalar subquery with `and false`;
  - result counts.
- **Test.** `TestExplainConstantFalseWhereIsDummy`. Disabling the check
  fails all five shapes.
- **Regress A/B** (22 files). join 14911 → 14876 diff lines. Error counts
  and results are unchanged apart from join's known row-order flap.
- **TPC-DS / TPC-H.** No plan changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.

## Deferred (ledgered)

Dummy propagation outside the scope's top residual:

- **LEFT JOIN … ON false.** PG makes the inner side dummy (`->  Result
  One-Time Filter: false`) and prints `Join Filter: false` without parens.
- **IN over a dummy subquery.** PG makes the whole semi join dummy.
- **A dummy UNION ALL arm.** PG drops it from the Append (`Seq Scan on f2`
  alone).
