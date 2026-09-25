# M0146-0005 slice 9 (M0146-0005i): window run conditions carry no selectivity

Witness: TPC-DS Q44, first divergence `join-method` at depth 1. PG's
`v11`/`v21` subqueries keep 5424 rows. `rnk < 11` over `rank() OVER (…)`
becomes a WindowAgg `Run Condition`: `check_and_push_window_quals` /
`find_window_run_conditions` (allpaths.c) set `keep_original = false`, so
the qual leaves the subquery rel's `baserestrictinfo`. goopg priced it as a
Filter at the default 1/3 inequality (1831 rows).

## Change

`window_runcondition.go`:
- `windowRunConditionDropsQual` is find_window_run_conditions' operator
  rule over a bare window-function column. It resolves the column through
  identity Projects, Sorts and stacked WindowAggs.
- `windowFuncSupportMonotonic` gives the prosupport answers: row\_number,
  rank, dense\_rank, percent\_rank, cume\_dist and ntile are increasing;
  count depends on its frame.
- `filterSelectivity` and `applyLocalFilterSelectivity` drop those
  conjuncts before scoring.

goopg still evaluates the qual as a Filter, which returns the same rows for
a monotonic function. Only the estimate follows PG.

## Results (`q44-q67-plan-heads.txt`)

- Q44: the subquery now estimates 5495 rows (PG 5424). The `rnk = rnk`
  merge join still reads 5495, because goopg's all-default `max(l,r)` cap
  (M0126-0010, no PG counterpart) clips PG's 5424² / 200 = 147099. Filed
  as M0146-0005j. The first divergence is unchanged.
- Q67: the WindowAgg keeps its input's 17146 rows, as PG's does (18846,
  its input).
- The census is unchanged at both scales. Sweep 96/96 (2 plans changed).
  The fire set (Q44, Q67) has no timeouts. TPC-H: 5/22, spotcheck PASS,
  the acceptance arm has 24 MATCH.

## Not ported (ledgered)

- The executor's early stop and pass-through mode.
- EXPLAIN's `Run Condition:` line.
- The `=` rewrite into a `<=`/`>=` run condition that keeps the qual.
