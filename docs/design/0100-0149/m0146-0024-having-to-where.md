# M0146-0024: aggregate-free HAVING conjuncts move into WHERE

Status: landed 2026-09-26 (plain GROUP BY; grouping sets and the wider
column rule open).

## PG behaviour

`subquery_planner` (`postgres/src/backend/optimizer/plan/planner.c`) walks
the HAVING conjuncts after expression preprocessing:

- A conjunct stays in HAVING when it contains an aggregate
  (`contain_agg_clause`), a volatile function or a SubPlan. With grouping
  sets it also stays when it reads the grouping RTE.
- Otherwise, with a GROUP BY and no empty grouping set, it **moves** to
  WHERE, appended after the existing WHERE quals. A group the clause
  rejects has none of its rows reach grouping, so the output is unchanged.
  The clause now filters the scan and can drive an index.
- Otherwise (an empty grouping set, or no GROUP BY) it is **copied** into
  WHERE and also kept in HAVING. A variable-free clause then gates the
  scan.

EXPLAIN shows the difference: `SELECT g, count(*) … GROUP BY g HAVING g > 1
AND count(*) > 2` prints `Filter: (g > 1)` on the Seq Scan and only
`count(*) > 2` on the aggregate. goopg kept both on the aggregate.

## Change

`moveHavingToWhere` (`internal/optimizer/havingtowhere.go`) runs in
`planSelectWithSettings` right after `prepareGroupingSets`. It returns a
copy of the statement (the parse tree is never mutated, so a re-plan does
not move a clause twice) with each movable conjunct appended to WHERE.

A conjunct is movable when `havingClauseMovable` accepts every node.

- Node kinds are an explicit list: literals, parameters, column refs,
  operators, casts, COLLATE, IS [NOT] NULL / boolean tests, IS DISTINCT
  FROM, IN with a value list, CASE, and function calls. Anything else,
  sublinks included, keeps the clause.
- A call must not be an aggregate (builtin or user-defined), a window
  function, or volatile. Volatility uses the same builtin list and routine
  lookup as the pull-up gate (`parserFuncVolatile`).
- Every column must appear verbatim as a plain GROUP BY item. PG checks
  grouping validity before planning, so `HAVING x > 1` over an ungrouped
  `x` must still raise "must appear in the GROUP BY clause". Moving it
  would silently accept an invalid query.

## Results

- Census-neutral. No TPC-DS query changed plan at either scale (their HAVING
  clauses all aggregate), and the TPC-H census is identical.
- Rows unchanged: sweep 96/96, spotcheck, acceptance arm 24 MATCH.
- Regress: the 41 pass-required cases, plus `select_having`, `aggregates`
  and `groupingsets`, match a HEAD worktree.
- Pinned by `TestExplainHavingWithoutAggregateMovesToWhere` against PG's
  output (`analysis/m0146/m0146-0024/pg-oracle.txt`).

## Open (ledgered)

1. Grouping sets keep HAVING whole. PG still moves a clause that reads no
   grouping column, and copies a variable-free clause when an empty set
   exists.
2. With no GROUP BY, PG copies a variable-free HAVING clause into WHERE
   (a gating Result) and keeps it.
3. The column rule is textual. A grouped expression (`GROUP BY a + b …
   HAVING a + b > 1`), a functionally dependent column, an outer
   reference or a differently qualified spelling keeps the clause in
   HAVING, although PG moves all of them.
