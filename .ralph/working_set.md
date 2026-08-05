(idle — nothing in flight)

Last loop: **M0127-P5.9-l-ii CLOSED — clause 6 MEASURED and PASSES (09 §3.13).**
`enum_controls=2/2 enum_controls_oos=1 enum_candidates_offered=2/2` — Q7's
`{customer+lineitem+n2+orders} ⋈ {n1+supplier}` and Q8's
`{lineitem+orders+part} ⋈ {customer+n1+region}` were both OFFERED to
`makeJoinRel` at phase=2, so the divergence is cost/stats, which §4 admits.
P5.9-l and P5.9-l-ii ticked in fix_plan + IMPLEMENTATION-TODO; 1 ledger row.

NEXT LOOP (subject to the fix_plan `## Current Priority` banner, which wins):
with clause 6 discharged, all six acceptance clauses of 09 §4 now read PASS at
run 4 — the remaining P5.9 step is the **flip decision** for
`GOOPG_PGSHAPED_DP` (re-run clause 6 alone / attribute, per the P5.9 item).
Check fix_plan for the successor item before assuming it.

Two things a later loop may want:
- a plan-only 22-query sweep is now cheap (`PLAN_ONLY=1 DP_TRACE=1 PGSHAPED=1
  scripts/tpch-estimate-audit-arm.sh <label>` with no `--queries`); this run
  swept only §3.11's 3-query candidate set. Ledger row 2026-08-06.
- the `CROSS-QUERY-LEVEL` **candidate** branch is unit-tested only — no such
  candidate occurred live.

Gates run: `go build ./...`; `go vet` (cmd/estimate-audit, internal/estimateaudit);
`go test ./internal/estimateaudit/ ./internal/planner/` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` no FAIL;
pgbench smoke via the commit hook; `make ralph-state-guard` OK (auto-repaired).
No planner/executor behaviour change (the trace gate stays off by default and
`--plan-only` is a tool flag), so no spotcheck/DS05 arm.

In-flight: none. The measurement arm ran to completion in ~4 min beside the
nightly CI batch — legitimately, because `PLAN_ONLY=1` executes nothing and
records no timing (see the arm script's PLAN_ONLY header note). The throwaway
server was stopped by the script's own trap.
