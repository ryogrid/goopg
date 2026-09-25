# R32 — Report: targetlist subplan display (K42) implemented

*Slice 1 of the K37 campaign. Design: `DESIGN.md` (reviewed
APPROVE-WITH-NOTES, notes applied; commit `e068cdcde`).*

## Change

`internal/executor/operators_explain.go`, two skip sites only:

- `walkPlanFiltered` Project arm: visit `p.Targets` via
  `formatExprQual` (text discarded, assign side effect kept) before
  recursing to `Child` — extends the `Result`-Targets precedent.
- `walkPlanAnalyzeFiltered` Project arm: identical twin visit, or the
  ANALYZE text path silently diverges.

TEXT only; JSON (`planToJSONNamed` family) explicitly out of scope.
No planner, executor, or costing input touched — the plan struct is
read-only on this path.

## What it fixed

Q9 (fifteen scalar subqueries over `store_sales` in CASEs, outer over
`reason`): EXPLAIN went from a bare 2-line plan to the full tree with
InitPlan 1..15 in CASE order under the top IndexScan — PG's placement
and numbering. The newly visible inner plans are serial Aggregates:
the K44 real gap (partial aggregation inside subqueries) is now
measurable. Q1/Q32/Q81/Q92 deltas were RECLASSIFIED during
implementation (see DESIGN.md §3 test 2): their sublinks live in
Filter/Join-Filter quals where the planner decorrelates while PG
keeps SubPlan — planner strategy, not display; referred onward.

## Gates (all 2026-09-09)

- Units: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  — pass (Part 2 pgbench smoke deferred to the commit hook).
- TPC-H spotcheck: Q12/Q13 PASS (rows=2/34).
- TPC-DS SF0.5 sweep: `PASS=95 (57 ck-verified) MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`; plan-shape channel: 98 same,
  changed=1 (Q9, intended).
- Parity guards, base-HEAD vs r32 binaries on identical data, pinned
  env: TPC-DS 98/99 byte-identical (Q9 intended; Q36/70/86 differ only
  in capture-PID inside shared-syntax-error text); TPC-H 22/22
  byte-identical modulo header.

## Evidence

- `/tmp/pp2/k37-ds-r32.plans.txt` (r32 default-mode full capture)
- `/tmp/pp2/tpch-r32guard-base.txt`,
  `/tmp/pp2/tpch-r32guard-r32.txt` (TPC-H A/B)
- Sweep: `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260909-153927.txt`
