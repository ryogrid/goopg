# M0146-0143 — aggregates written inside a CASE, a window spec or an IN are collected

Status: done 2026-10-10 (6285285fe). Parent: M0146-0140.

## Problem

TPC-DS Q70 failed on goopg with `aggregate call could not be resolved`.
PG returns 3 rows.

- **The query.** Q70's IN subquery ranks states with
  `rank() over (partition by s_state order by sum(ss_net_profit) desc)`.
  The `sum` appears nowhere else.
- **Minimal repros.** `select a, rank() over (order by sum(b)) from t
  group by a` and `select a, case when sum(b) > 2 then 1 end from t group
  by a` failed the same way.

## Cause

`walkExpr` (planner.go) is the walk behind `collectAggregateCalls`,
`exprHasAggregate`, `firstAggregatePos`, `needsAggregateStage` and the
sublink pull-up predicate.

- **What it walked.** It descended only operators, casts, IS tests and
  function arguments.
- **What that missed.** An aggregate written only inside a CASE, an IN
  list, EXTRACT, COLLATE, a row/array/subscript/field expression or a
  window's PARTITION BY / ORDER BY was never collected. The aggregate stage
  then had no slot for it.
- **What PG does.** PG registers every Aggref at its query level wherever
  it is written (`transformAggregateCall`, parse_agg.c). Window
  definitions are transformed in the query's own parse state
  (`transformWindowDefinitions`).

## Change

### Collection

- **`walkExpr`** descends every expression node that can hold a call:
  CASE, IN list, EXTRACT, COLLATE, LIKE ESCAPE / SIMILAR TO, field select,
  ROW, array subscript, ARRAY[...], and a call's window spec through the
  new `walkWindowDef`.
- **What it still skips:**
  - a subquery (SubqueryExpr, EXISTS, an IN's subquery, ARRAY(SELECT)),
    because its calls belong to the inner level;
  - a call's FILTER and its own ORDER BY, where an aggregate is a
    separate PG error.
- **Named windows.** `collectAggregateCalls` and `needsAggregateStage`
  also visit the named WINDOW clause, for
  `... OVER w WINDOW w AS (ORDER BY sum(b))`.

### Resolution

Resolution follows collection, because the two are sibling paths
(Hard-won rule 2).

- **List-form IN.** After the aggregate stage, a list-form IN resolves its
  operand and items through the aggregate surface
  (`resolveInListAfterAggregate`). It keeps planInExpr's one-element `=`
  rewrite.
- **Subquery-form IN** (`resolveInSubqueryAfterAggregate`). The left
  operand resolves through the aggregate surface, and only the body is
  planned in the HAVING-parent context. `planInExpr` used to resolve the
  operand in that parent context as well, which names a level that does
  not exist from here (`outer column ref ... out of range`). Two shapes
  that now work:
  - `having sum(b) in (select 3)`;
  - regress subselect's bug #19037 case,
    `(1 = any(array_agg(f1))) = any (select false)`.

### Sublink pull-up side effect

The sublink pull-up predicate (`selectListOrQualDisqualifies`) now also
sees calls inside a CASE or an IN list. Its rule is deliberately
conservative ("any call in the target list disqualifies"), so the change
can only stop a pull-up.

- **The fix it brings.** Before, an aggregate hidden in a body's CASE
  (`a in (select case when count(*) > 0 then 1 end from t)`) went
  undetected.
- **What changed in the corpus.** The fire set moved no plan other than
  Q70.

### Harness

Oracle row 70 flips from `SKIP_ENGINE_GAP` to `OK`. `ENGINE_GAP` in
`tpcds-sf025-regression.sh` is now empty.

## Verification

- **Tests.**
  - `TestAggregatesWrittenOnlyInsideCaseOrWindowSpecArePlanned` plans 12
    shapes. On the old code 5 of them fail with the M0146-0143 error.
  - `TestWalkExprStaysAtItsQueryLevel` checks the walk never enters a
    subquery or a FILTER.
- **PG probes.** A battery of 14 shapes run against :65438 with inline
  VALUES. All match except the two pre-existing gaps below.
- **Q70.** At SF0.25, Q70 returns PG's 3 rows with the identical
  checksum. The sweep is now PASS=99 with SKIP=0 (it was 98/1), so every
  TPC-DS query is value-verified.
- **TPC-DS fire set.** Only Q70 fires, at both scales; it was a capture
  error before. Q70 enters the census as
  `[join-order, join-method, scan-type, sort-strategy, parallelism,
  rendering]` at both scales.
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases). `subselect` loses 23 diff lines (the bug
  #19037 block passes). `stats_ext` shows only its known listing-order
  noise.
- **Gates.** Units, TPC-H spotcheck and ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **Array subscript after the aggregate stage.** `(array[sum(b), max(b)])[1]`
  is collected but fails with
  `unsupported expression *parser.ArraySubscriptExpr`, because
  `resolveExprAfterAggregate` has no ArraySubscriptExpr arm. This failed
  before too.
- **An aggregate inside a WHERE-clause CASE.** goopg reports
  `function sum does not exist`; PG reports `aggregate functions are not
  allowed in WHERE`. This error text predates the change.
- **An outer-level aggregate inside a HAVING IN subquery's WHERE.**
  `having a in (select b from t t2 where t2.b = sum(t.b) - 2)` fails with
  `aggregate functions are not allowed in WHERE`, where PG returns a row.
  HEAD fails the same way.
- **Q70's plan divergence.** It goes to the next parity-closure sweep.
