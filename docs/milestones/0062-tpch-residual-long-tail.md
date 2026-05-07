# Milestone 0062 — TPC-H Residual Long-Tail

**Status:** planned
**Depends on:** Milestone 0061 (commits `faf2e71` + `00ee40f` +
`e114ca7`, verified in
`analysis/tpch-m0061-followups-baseline-2026-05-07.md`)
**Drives:** Q5 / Q8 / Q15b / Q20 / Q21 each either complete with
the canonical TPC-H row count, or carry a follow-up that names a
concrete next step. NO-DEFERRAL POLICY identical to M0061.

## Context

The M0061-0003 22-query SF=1 sweep (2026-05-07) confirmed M0061's
EXISTS/NOT EXISTS unnesting and Q19 IN-list-pushdown wins, plus
the tpch-runner connection-isolation fix that resolved a
result-stream aliasing bug. After those, five queries still
either time out or return an unexpected row count:

| Q | Symptom | Pre-existing? |
|---|---------|---------------|
| Q5  | cancel at 600 s | yes (run-013 also slow) |
| Q8  | OK 0 rows | yes (also 0 in prior baseline) |
| Q15b| OK 0 rows | yes |
| Q20 | cancel at 600 s | yes (nested IN-IN-scalar) |
| Q21 | cancel at 600 s | yes (non-equijoin EXISTS) |

Two further immediate fixes were also in-scope of the M0061-0003
follow-up work but distinct from the M0062 long-tail bucket:

- **Q9 LIKE regression** — fixed via forward `KindBytes`
  acceptance in `internal/executor/expr.go` plus an upgraded
  diagnostic message. The exact breaking commit is identified by
  a separate bisect (see `analysis/tpch-m0062-q9-bisect-...md`).
- **Q13 cancel-propagation** — fixed via `ctx.Err()` in
  `joinOp.runNestedLoop`'s inner loop, plus defense-in-depth
  ctx checks in `sortOp.Open`, `filterOp.Next`, and
  `aggregateOp.Open`'s output-materialisation loop.

## Sub-tasks

- **M0062-0001** Q5 cancel-at-600 s. Investigate the slow
  six-table multi-way hash join probe; profile the path that
  consumes the 600 s and either add a hot-loop ctx check, lift
  a per-row allocation, or tune the build/probe order. Goal:
  Q5 OK in < 600 s OR cancel returned in < 1 s.
- **M0062-0002** Q8 0-rows correctness. Q8 SQL involves an
  outer SELECT that wraps a subquery with `extract(year from
  o_orderdate)` and a join on `n_regionkey`. Reproduce with a
  minimal end-to-end test; bisect to the first commit where
  the row count drops to zero. Likely culprits: date / extract
  evaluation, or a COUNTRY / REGION join condition that lost
  rows in a planner pass.
- **M0062-0003** Q15b 0-rows correctness. Q15b runs the
  `revenue0` view + a `WHERE total_revenue =
  (max-from-revenue0)` filter. The view body (Q15a) returns
  10 000 rows OK; Q15b's join + max-filter returns zero.
  Likely a view-rewriting / type-coercion bug.
- **M0062-0004** Q20 nested-IN decorrelation. Q20 is
  `s_suppkey IN (SELECT ps_suppkey FROM partsupp WHERE
  ps_partkey IN (SELECT p_partkey FROM part WHERE p_name LIKE
  'forest%') AND ps_availqty > (SELECT 0.5 * sum(...) FROM
  lineitem WHERE ...))`. Two nested IN levels plus a
  correlated scalar inside; out-of-scope for M0061-0001's
  IN/EXISTS unnesting because the outer-most IN's inner
  subquery itself contains a correlated scalar that has no
  equijoin reduction.
- **M0062-0005** Q21 non-equijoin EXISTS correlation.
  `... AND EXISTS (SELECT * FROM lineitem l2 WHERE
  l2.l_orderkey = l1.l_orderkey AND l2.l_suppkey <>
  l1.l_suppkey)`. The `<>` correlation predicate makes
  M0061-0001's equijoin gate decline; needs a range-correlation
  EXISTS path or per-outer-row indexed re-scan.

## Required Design Docs

Per sub-task, when implemented:

- `docs/design/0062-0001-q5-mhj-probe-tuning.md`
- `docs/design/0062-0002-q8-zero-rows.md` (after diagnosis)
- `docs/design/0062-0003-q15b-zero-rows.md` (after diagnosis)
- `docs/design/0062-0004-nested-in-decorrelation.md`
- `docs/design/0062-0005-non-equijoin-exists.md`

## Definition of Done

- [ ] M0062-0001 merged; Q5 OK in < 600 s on SF=1 OR cancel
      returns in < 1 s.
- [ ] M0062-0002 merged; Q8 returns the canonical TPC-H row
      count.
- [ ] M0062-0003 merged; Q15b returns the canonical row.
- [ ] M0062-0004 merged; Q20 OK in < 600 s on SF=1.
- [ ] M0062-0005 merged; Q21 OK in < 600 s on SF=1.
- [ ] `go test ./...` PASS after each sub-task.

## ⚠ NO-DEFERRAL POLICY

Identical to milestones 0058 / 0061: blocked sub-tasks must
carry `BLOCKED: <reason>` in `.ralph/fix_plan.md` and name a
concrete follow-up.
