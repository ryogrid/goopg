# Evaluation and implementation plan

## Task set for the ordinary loop

Build a frozen, stratified set from earlier broad goopg work, plus a prospective holdout selected after the temporary focus is lifted. Include successful, blocked, and previously regressed tasks across SQL compatibility, catalog/protocol, storage/WAL/recovery, lock/concurrency, planner/executor, and performance. Include small and large tasks. Exclude a task if its starting checkout, data fixtures, or oracle dependencies cannot be reconstructed. Record exclusion reasons. The `Current Priority` banner determines real task selection; the evaluator must not reward candidate-driven priority changes.

Use paired incumbent/candidate runs for each task. Fix the task start SHA, backend and model, resource envelope, prompt inputs, and database fixture versions. Repeat enough tasks or seeds to expose variance; a single lucky rollout is insufficient. Keep an untouched holdout until a candidate is frozen. Report timeouts and failed runs in the denominator. A benchmark score without patch verification is not a success.

## Metrics and acceptance

| Measure | Evidence | Rule |
| --- | --- | --- |
| Correct completion | Required tests, oracle checks, design/task-state review | Hard constraint; no new wrong rows, data loss, or unverified completion |
| Gate integrity | Exact command, exit status, gate stamp, staged diff identity | Hard constraint; no omitted, weakened, or bypassed required gate |
| State/priority discipline | `RALPH_STATUS`, `fix_plan.md`, working-set diff, commit scope | Hard constraint; no unauthorized priority or owner-file edits |
| Task throughput | Verified tasks per wall-clock day, paired completion rate | Primary improvement measure after hard constraints |
| Efficiency | Tokens/cost, elapsed time, tool retries, test-only loops | Secondary; optimize only among equally safe outcomes |
| Regression burden | Nightly action items, reopened tasks, revert/deferral rate | Track for several subsequent live batches |

Predeclare an acceptance threshold before each experiment. A practical pilot rule: all hard constraints pass, no significant loss in paired verified-completion rate, and a repeatable reduction in either median task time/cost or blocked/rework loops on both development tasks and holdout. Because task counts may be small, publish raw paired outcomes and uncertainty; do not claim a general gain from one aggregate percentage. Prefer a smaller, reproducible change to a noisy large one. Re-test against the current HEAD before adoption.

## Minimum viable experiment

1. Use `analysis/ralph-loop-kaizen/pipeline/` to refresh baseline loop telemetry. Save a versioned, read-only task/evidence manifest outside `.ralph/` and the live checkout. Use existing `ci/logs/action-items.md` only as a failure signal; re-run its reproduction command because it can be stale.
2. Pick one bounded intervention: route the existing practice cards based on the selected task's subsystem and failure type. Keep mandatory `AGENT.md` and `.ralph/PROMPT.md` text fixed. This directly tests context selection without changing gates or owner policy.
3. Implement a tiny sidecar runner that applies a candidate overlay to an isolated worktree and invokes the existing backend through a controlled wrapper. Write deterministic validation and scoring first. Use one task and one candidate to prove that the incumbent can be replayed, that the candidate cannot touch protected surfaces, and that both executions are independently capped.
4. Expand to a balanced replay set. Compare paired outcomes; manually inspect every apparent improvement and failure. Only then add Propose–Probe–Compose candidate generation and a constrained experience library. If desired, implement a Harness² `BenchmarkAdapter` at this point, using its `recursion.py`, `context.py`, and `library.py`, while retaining the goopg evaluator and incumbent selection rule outside its default selector.
5. Promote a winning guidance change through the normal repository review path. Run the existing unit/component and pgbench smoke gates plus any risk-specific checks. Observe live performance and nightly correctness before increasing the editable surface or automation level.

The pilot needs no live harness modification, no new production Go code, and no attempt to run the full Harness² public benchmarks. Its exit artifacts should be a replay manifest, paired-results table, candidate patch, gate evidence, and a clear adopt/reject decision.

## Failure modes to test deliberately

- **Shared state:** two runs write the same `.ralph/`, benchmark data directory, port, cgroup unit, or gate stamp. The isolation test must fail before task execution.
- **Reward hacking:** candidate shortens tests, skips oracle checks, edits scoring files, or marks tasks complete without a verified patch. Reject by path validation and independent grading.
- **Answer leakage:** candidate sees future commits, held-out tests, prior solved patches, or online grading material. Restrict inputs and log accesses; discard contaminated trials.
- **Nondeterminism:** a flaky test or model variance makes a candidate appear better. Repeat paired trials and show distributions, not just best runs.
- **Context drift:** accumulated experience cards become stale or conflict with current instructions. Version, cap, and revalidate retrieved lessons.
- **Cost/host pressure:** parallel probes start heavy servers and thrash WSL2. Cap each via `scripts/goopg-test-run.sh`, set distinct `GOOPG_CG_UNIT`, and start serially. Honor the project’s bounded-goopg rule even when using a new runner.
- **Concurrent loop WIP:** the live checkout or index changes during an experiment. Pin worktree commits and never stage, clean, stash, or commit the live loop's files from the controller.

## Go/no-go decision

Proceed with the sidecar pilot when the broad task stream is restored and the owner has specified the future editable surface. Do not require full Harness² integration to prove the method. Adopt a candidate only when paired evidence and independent goopg gates meet the predeclared bar. Otherwise keep the incumbent and use the recorded failures to improve the next proposal.
