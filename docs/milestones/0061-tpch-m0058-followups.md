# Milestone 0061 — TPC-H M0058 Follow-ups

**Status:** planned
**Depends on:** Milestone 0058 (commit `d509107`, verified in
`analysis/tpch-m0058-verification-2026-05-07.md`)
**Drives:** Q4 / Q21 / Q22 completion within 60 s; Q19 within 60 s;
final 22/22 SF=1 sweep inside a 600 s/query budget.

## Context

The M0058 verification run (2026-05-07) confirmed five of the six
landed sub-tasks but exposed three follow-ups that the milestone
explicitly carved out:

1. **M0058-0002 was deferred** because EXISTS/NOT EXISTS unnesting is
   a ~300-line cross-module change that the autonomous-mode loop
   landing M0058-0001/0003/0004/0005/0006 declined to bundle. Q22
   continues to time out at 300 s and Q4 / Q21 remain blocked.
2. **Q19 still cancels at 300 s** even though M0058-0004 successfully
   removed the CROSS JOIN: the residual three-branch OR-of-ANDs
   filter is evaluated row-by-row and dominates at SF=1.
3. **The verification re-run only re-checked six queries** that were
   directly affected by M0058. A full 22-query sweep with a wider
   budget is needed to confirm no regression on Q1/Q3/Q5/Q6/Q9/Q10/etc.
   and to capture the new long-tail after the above two fixes land.

This milestone collects the three follow-ups under one header so the
NO-DEFERRAL POLICY (see milestone 0058 §"⚠ NO-DEFERRAL POLICY") is
honoured — every deferred sub-task names a concrete successor.

## Sub-tasks

- **M0061-0001** EXISTS / NOT EXISTS unnesting to semi-join /
  anti-join (formerly `M0058-0002-followup`; ~300 lines; unblocks
  Q4, Q21, Q22).
- **M0061-0002** Q19 residual-OR optimisation. Either vectorise
  the three-branch OR-of-ANDs filter, or teach the planner to
  rewrite it as `UNION ALL` of three independent joins with
  branch-specific build-side filters. (Q19 currently cancels at
  300 s after M0058-0004 turned the CROSS into a Hash Join.)
- **M0061-0003** Re-baseline the full 22-query TPC-H SF=1 sweep at a
  600 s/query budget once M0061-0001 lands. Captures regressions and
  remaining long-tail; supersedes the partial six-query report in
  `analysis/tpch-m0058-verification-2026-05-07.md`.

## Required Design Docs

- `docs/design/0061-0001-exists-anti-semi-join-unnesting.md`
  (new; covers `JoinTypeSemi` / `JoinTypeAnti`, the planner pass that
  recognises EXISTS / NOT EXISTS with an equijoin correlation
  predicate, inner-side de-duplication, and interaction with the
  existing M0040 IN-unnesting pass).
- `docs/design/0061-0002-q19-or-of-ands-residual.md` (new; documents
  the chosen approach — vectorised branch evaluation vs. UNION ALL
  rewrite — with cost-model evidence).

## Definition of Done

- [ ] M0061-0001 merged; Q4, Q21, Q22 each complete in < 60 s.
      EXPLAIN for each shows `Semi Join` / `Anti Join` (or hash-keyed
      equivalent), not a SubPlan re-evaluated per outer row.
- [ ] M0061-0002 merged; Q19 completes in < 60 s. EXPLAIN no longer
      shows the OR-of-ANDs as the residual filter on the join output
      (or the per-branch path is inexpensive by construction).
- [ ] M0061-0003 report committed under `analysis/` showing all
      22 queries' status / elapsed / rows for SF=1, with regression
      callouts for any query that got slower vs. the M0058 baselines.
- [ ] `go test ./...` PASS after each sub-task.

## ⚠ NO-DEFERRAL POLICY

Identical to milestone 0058: blocked sub-tasks must carry
`BLOCKED: <reason>` in `fix_plan.md` and name a concrete follow-up.
Silent close is not allowed.
