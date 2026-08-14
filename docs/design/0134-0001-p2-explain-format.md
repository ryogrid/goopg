# M0134-0001 P2 — `EXPLAIN` text/plan-format divergence (cross-case)

**Status:** accepted
**Date:** 2026-08-15
**Task:** M0134-0001 (`.ralph/fix_plan.md`) — the P2 pattern from
`docs/design/0134-0001-aggregates-sql-divergence.md`. Characterises the
EXPLAIN-format gap and decomposes it into formatter slices (S1–S5) and
planner-behavior slices (S6–S10).
**Gate:** `scripts/pg-regress-runner.sh aggregates` → `tmp/regress-diffs/aggregates.diff`.

## Scope and share

After P1/P3a/P3b/P4 landed, `aggregates.diff` is 1628 lines (941 changed).
**794 changed lines (84% of changed, ~49% of the diff) are EXPLAIN plan output** —
the parent note's "P2 = 20.4%" was measured against the pre-fix 2759-line diff.
61 `EXPLAIN` statements exist in `aggregates.sql`; **50 produce divergent plan
output**. P2 is therefore the dominant remaining class, not a 20% tail.

## Diff-direction correction (binds every slice below)

In this diff **`-` = PG 18.3, `+` = goopg** (the parent note listed the patterns
without direction). Decisive evidence: the `-` side carries PG's canonical
min/max `Result/InitPlan/Limit/Index Only Scan` plans and functional-dep-pruned
`Group Key:` lines; the `+` side carries Go type names (`*planner.GenerateSeries`)
and goopg-only annotations (`(stats)`, `(N keys)`). Consequently the two
"missing" annotations are **goopg-only additions PG never emits** — the fix is to
remove them, not add them.

## Class map (10)

| # | class | goopg side (`file:symbol`) | PG side (`postgres/src/...`) | kind |
|---|-------|---------------------------|------------------------------|------|
| 1 | `(stats)` suffix on Seq Scan | `internal/executor/operators_explain.go` `describePlan` 1665-1682, `describePlanVerbose` 1548-1551 | `explain.c` T_SeqScan 1432-1433 (no suffix) | formatter |
| 2 | `HashAggregate (N keys)` suffix + missing `Group Key:` line | `operators_explain.go` 1661 (`len(p.GroupExprs)`), 1638 | `explain.c` T_Agg 1531-1555; `show_sort_group_keys` 2627/2755 | formatter |
| 3 | `*planner.GenerateSeries` / `*planner.ProjectSet` node leak | `describePlan` `%T` fallback 1777; `planChildren` 1870 | T_FunctionScan 1465-1466 + `Function Call:` 2067/2081 | formatter |
| 4 | missing min/max → InitPlan+IndexOnlyScan | no `preprocess_minmax_aggregates`; `buildAggregateStage` `planner.go:6259` | `planagg.c:73`, called `planner.c:1618` | planner |
| 5 | GROUP-BY functional-dep removal | no prune; `GroupExprs` = full GROUP BY `planner.go:6260-6548`; exec-time `funcDepCols` `5651/6893/7055` | `initsplan.c:412 remove_useless_groupby_columns`, `planmain.c:173` | planner |
| 6 | presorted aggregation (Sort/GroupAggregate) | hash-only executor `operators_join_agg.go:1926/1967` | `planner.c:3230 adjust_group_pathkeys_for_groupagg`, 3780 | planner |
| 7 | join algorithm + `(INNER)`/`(CROSS)` annotation | `describePlan` 1590-1609, 1739-1744; `joinTypeName` 1835 | `explain.c` 1422, suffix-only-for-non-inner 1754-1763 | formatter + planner |
| 8 | index cond `<`→`<=` + redundant Filter | `formatIndexCond` 777-812; inclusive `LowKey/HighKey` `plan.go:697-698`; range builder `planner.go:8657-8689` | original op kept in `Index Cond` | planner+formatter |
| 9 | parallel-agg split (incl. a **runtime correctness bug**) | `parallel.go:597 splitAggregate`; whitelist `parallel_agg.go:28`; `parallel_agg_combine.go:145` | explain.c T_Agg split; parallel agg paths | planner |
| 10 | VERBOSE `Output:` naming | `operators_explain.go:492/1353`, `schemaColumnNames` 1192 | `show_upper_qual` (qualified + full expr) | formatter |

## Root causes (top notes)

- **Classes 1–3, 7-annotation, 8-format, 10 are emit-site/formatter divergences**:
  goopg's `describePlan` prints annotations PG does not (`(stats)`, `(N keys)`,
  `(INNER)`/`(CROSS)`) and falls back to Go `%T` for SRF nodes it has no case for.
- **Classes 4–6, 7-choice, 9 are planner-behavior gaps** that change plan shape
  (node types / scan choices), not just text — they also explain why nearly every
  `(N rows)` count differs (different nodes ⇒ different estimates).
- **Class 9a is a correctness bug, not text**: `balk` (`aggregates.sql:1395`,
  `COMBINEFUNC = balkifnull` + `PARALLEL = SAFE`) makes goopg's planner split the
  aggregate (`AggregateIsDecomposable` accepts any user agg with a combinefunc) but
  the executor combine whitelist (`combineAggRuntime`, builtins only) has no rule
  for it → `ERROR: internal error: no combine rule for aggregate "balk"` where PG
  returns one row.

## Decomposition

Formatter/emit-site slices (self-contained, no planner behavior change):

- **S1 (class 1)** — remove the `(stats)` suffix at `describePlan` 1665-1682 /
  `describePlanVerbose` 1548-1551. It is deliberate M0006 ANALYZE instrumentation;
  the parity rule makes it wrong on the wire, so drop it from default output (or
  gate behind an off-by-default debug GUC). 46 `+` lines, ~15 blocks.
- **S2 (class 3)** — add `describePlan` + `planChildren` cases for `GenerateSeries`,
  `ProjectSet`, `UserSrfScan`, `FromUnnest`, `GenerateSubscripts` → `Function Scan on
  <schema>.<func> <alias>` + `Function Call:` detail. Needs alias plumbing (PG shows
  `s1`/`s2`; goopg's `GenerateSeries` node does not store the RTE alias).
- **S3 (class 7a)** — Join/`NestedLoopIndexJoin` label → PG suffix rule (annotate
  only non-inner; spellings ` Left Join`/` Semi Join`/…). Touches every join plan
  line suite-wide → verify across TPC-H/TPC-DS gates.
- **S4 (class 8)** — preserve the original comparison op on `IndexScan` (new field),
  render in `formatIndexCond`, drop the redundant `Filter`. Contained to
  `planner.go:8657-8689` + `formatIndexCond` 777-812.
- **S5 (classes 2+5 rendering half)** — replace `(N keys)` with PG's
  `Group Key: <exprs>` detail line (`show_sort_group_keys` equivalent).

Planner-behavior slices (own efforts, larger):

- **S6 (class 4)** — port `preprocess_minmax_aggregates` (`planagg.c:73`): rewrite
  min/max into InitPlan+Limit+IndexOnlyScan (incl. Merge Append inheritance,
  parameterized subqueries, constant `max(100)`). Depends on goopg's existing
  SubPlan/InitPlan infra.
- **S7 (class 5 planning half)** — port `remove_useless_groupby_columns`
  (`initsplan.c:412`) to prune `GroupExprs` at plan build; reconcile with the
  exec-time `funcDepCols`/`Passthrough` mechanism.
- **S8 (class 6)** — presorted aggregation: sorted/GroupAggregate strategy in the
  executor (currently hash-only) + pathkey ordering. Largest slice; ties into the
  cost-model line.
- **S9 (class 7b)** — join-shape choice (Hash vs NestedLoopIndexJoin vs Merge) —
  cost-driven; part of the leftdeep-joins/cost-model line, not an EXPLAIN-format task.
- **S10 (class 9)** — parallel agg: (a) fix the `balk` runtime error (invoke the
  user combinefunc in `combineAggRuntime`, or refuse the split when no rule exists);
  (b) extend the decomposable whitelist and make `findPartialSubtree` split over
  UNION-ALL `Append` subtrees.

**Sequencing:** S1+S2 (pure text, unblock the 50-block count) → S5 rendering half
(no planner change) → S6 (biggest single win: 25 InitPlan + 32 IndexOnly lines) →
S7 → S10a (correctness) → S4 (small, independent, anytime). S8/S9 are cost-model
line work.

## Cross-case relevance

Every M0134 regress case whose `.sql` emits `EXPLAIN` inherits the formatter
classes (S1–S5) verbatim; `explain.sql` (M0134-0017) is the direct beneficiary of
the whole set. The planner classes (S6–S10) also move `explain.sql` and any case
with min/max or grouped aggregates. Slices should be planned as cross-case
emitter/planner fixes, then re-measured per case, not hardcoded to `aggregates.sql`.

## PG oracle citations

- `postgres/src/backend/commands/explain.c` — T_SeqScan 1432, T_Agg 1531-1555,
  T_FunctionScan 1465-1466, NestLoop 1422, non-inner suffix 1754-1763,
  `show_sort_group_keys` 2627/2755, `Function Call:` 2067/2081, Incremental Sort 1526.
- `postgres/src/backend/optimizer/plan/planagg.c:73 preprocess_minmax_aggregates`
  (called `planner.c:1618`).
- `postgres/src/backend/optimizer/plan/initsplan.c:412 remove_useless_groupby_columns`
  (called `planmain.c:173`).
- `postgres/src/backend/optimizer/plan/planner.c:3230 adjust_group_pathkeys_for_groupagg`.
