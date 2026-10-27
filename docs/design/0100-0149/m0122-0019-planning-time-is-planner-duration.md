# M0122-0019 — EXPLAIN "Planning Time" is the planner's duration

Status: landed 2026-10-27 (loop #186). Area: `internal/optimizer` (Explain plan
node) and `internal/executor` (EXPLAIN driver). Plan-neutral.

## Symptom

On the SF0.25 clone, `EXPLAIN (ANALYZE)` of TPC-DS Q64 printed
`Planning Time: 0.169 ms`, while a plan-only EXPLAIN of the same text took
1.6–2.0 s wall. 96 % of that time sat under `optimizer.PlanWithSettings` →
`geqoSearch`.

## Cause

`explainOp.Open` started its "planning" timer around `buildNode`, the
construction of the executor's operator tree. The optimizer had already run
before the executor existed, when the planner's `*parser.ExplainStmt` arm
planned the inner statement, and nothing timed it. The timer was also on the
wrong side:

- PG's `ExplainOneQuery` times `pg_plan_query` alone (explain.c:350-356).
- Its Execution Time starts before `ExecutorStart`, so `ExecInitNode` is
  counted there (explain.c:525-565).
- `buildNode` is goopg's `ExecInitNode`.

## Change

- `optimizer.Explain` gains `PlanningNs`. The planner's `*parser.ExplainStmt`
  arm times its `PlanWithSettings` call on the inner statement and stamps the
  result on the node.
- `explainOp.Open` (ANALYZE) prints `o.plan.PlanningNs` as Planning Time. Its
  execution timer now starts before `buildNode`, so building the operator
  tree counts as execution, as in PG.
- Plain `EXPLAIN (SUMMARY ON)` (no ANALYZE) now prints `Planning Time` in
  TEXT and in the structured formats. PG's `ExplainQuery` sets
  `es->summary = summary_set ? summary : analyze`, and `ExplainOnePlan`
  prints the planner time whenever summary is on (explain.c:624). goopg
  used to drop the summary entirely without ANALYZE. Plain EXPLAIN keeps
  the summary off by default.

## Evidence

`TestExplainPlanningTimeIsPlannerDuration`:

- The planner stamps `PlanningNs > 0`.
- A sentinel of 1234567890 ns on the node reaches ANALYZE TEXT
  (`Planning Time: 1234.568 ms`), ANALYZE JSON, and plain SUMMARY ON in
  TEXT and JSON.
- Plain EXPLAIN prints no summary.

Live on the SF0.25 clone (`tmp/q64-clone`, private binary on :5592), Q64
under `EXPLAIN (ANALYZE, TIMING OFF)`:

| run | Planning Time | Execution Time | psql wall |
|---|---|---|---|
| 1 | 835.969 ms | 631.654 ms | 1.47 s |
| 2 | 842.868 ms | 315.308 ms | 1.16 s |

Planning plus execution now accounts for the wall time. Before the change,
planning was 0.17 ms.

RegressSuite: the `explain` case gains the expected `Planning Time` line, so
its diff shrinks from 733 to 732 lines. Every other case is unchanged except
the plpgsql flapper.

## Residual (ledgered)

goopg's `PlanWithSettings` also does parse analysis and view expansion,
which PG performs before `pg_plan_query` (parse analysis, then
`QueryRewrite`), outside its planning timer. goopg's Planning Time
therefore includes analysis and rewrite. The gap is small next to a join
search, but it is not zero.
