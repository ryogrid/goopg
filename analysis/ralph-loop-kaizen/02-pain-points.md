# 02 — Pain points: what the loop repeatedly struggles with

This is the qualitative companion to [`01`](01-current-state.md). It is built
from **Stage 3** (free anchor-grep over the ~2.5 MB milestone history: 1,936
struggle anchors across 126 files — 110 milestones + 6 completed-plan journals +
10 handovers) cross-referenced with the existing
**memory files** (`~/.claude/projects/.../memory/`, 25 atomic lessons). The
**Stage 4** LLM lesson-clustering would sharpen and quantify these themes
further; it has not been run (see [`00`](00-methodology.md)).

## Anchor distribution (Stage 3, n = 1,936)

| anchor | hits | theme |
|--------|------|-------|
| regression / regress / reverted / revert | **608** | rework after a change broke something |
| deferred / partial (scope) | **614** | work closed at partial scope, carried forward |
| blocker / blocked | 252 | stalled awaiting a prerequisite or decision |
| timeout | 161 | runs exceeding the time budget |
| root cause | 92 | deep debugging narratives |
| silently | 84 | failures that produced no error |
| risk | 73 | explicit risk-management notes |

**97 of 110 milestones** carry at least one struggle anchor. Density concentrates
in the big retrospective journals (`completed_fix_plan_005.md`: 541 anchors,
`004.md`: 383) — these are the richest mineable source.

Two themes dominate, and both are *preventable with practice/process changes*.

## Theme A — Silent regressions (the most expensive failure mode)

The recurring shape: a planner/executor change passes its immediate check but
silently breaks a *different* query's row count, discovered loops later. Direct
quotes from the corpus:

- `0072-tpch-q5-q9-residual-and-slot-arena.md`: "triggers the **Q12=2/Q13=35
  silent-regression mode**" … "amortises the silent-regression bisect cost".
- `0075-tpch-residual-and-perf.md`: "hit the **M0071-Stage-B silent-regression
  pattern**".
- `0039-fix-planner-column-ref.md`: "causes the MultiHashJoin to **silently drop
  join conditions**".

This is already encoded in memory as the most-repeated lesson:

- [`m0071_stage_b_silent_regression.md`] — "Stage B deterministically breaks
  Q12/Q13 even when initial verification passes; complete retention-site audit +
  Q12/Q13 pre-commit test required."
- [`pattern_sibling_paths_must_agree.md`] — "recurring silent-bug source:
  encode/decode, fast-path/interpreted evaluator, column-lookup/star-expansion …
  unit test on one path passes while the other is wrong."
- [`feedback_tpch_pre_commit_gates.md`] — "Require fresh server restart +
  Q12/Q13 spot-check before every executor/planner commit (3 occurrences)."
- [`m0106_codec_regressed_6_regress_tests.md`] — "encode/decode must agree on
  type-set, Datum-Kind, AND fixed-width normalization; re-run full suite after
  codec changes."

**Why it costs so much:** each silent regression triggers a multi-loop bisect
(the corpus literally discusses "bisect cost"). A single regression can burn
several of the expensive long loops from [`01 §2`](01-current-state.md).

**Prevention is known but not enforced per-loop:** the pre-commit Q12/Q13
spot-check and sibling-path audit live in *memory* (loaded as background
context), not as a *gate* the harness runs. → addressed by the executor/planner
**practice card** + a verification gate in [`04`](04-rules-and-practices.md).

## Theme B — Partial scope and deferral churn

614 anchors mention deferral/partial scope. The pattern (from
`m0074_partial_scope_lessons.md`, `m0075_partial_outcomes_and_findings.md`):
under autonomous-mode risk management, central-type or high-risk changes land
"infrastructure only" with the real implementation deferred to a later
milestone — sometimes because a prior full attempt *hung* or regressed
(`M0072-0002` caused a runtime hang; `M0073/74` deferred three sub-milestones).

This is sometimes correct risk management, but it has a cost: a milestone
"closes" without finishing, the follow-up is re-loaded into context later, and
the same code is re-explored from scratch (the 25-turns-to-first-edit problem in
[`01 §4`](01-current-state.md)). The deferral rationale is captured in prose but
**not in a structured carry-forward** the next loop can load cheaply.

→ addressed by a **deferral ledger** convention + carry-forward context in
[`03`](03-recommendations-harness.md) and [`04`](04-rules-and-practices.md).

## Theme C — Concurrency / environment foot-guns

- [`concurrent_ralph_loops_corrupt_tree.md`] — "two loops on one working tree
  clobber each other's edits + shared `.ralph` state."
- [`goopg_manual_server_test_workflow.md`] — "`pkill -f` self-matches the Bash
  shell (exit 144); re-init data dir between runs; `-listen` not `-p`."
- [`pattern_ralph_isolation_ports_paths.md`] — port/path separation to avoid
  conflicts.
- [`m0107_gc_hotpath_fix.md`] — "`maybeForceGCAfterCommit` called `ReadMemStats`
  (STW) on EVERY query; 43% gcBgMarkWorker."

These are environment/tooling traps that waste whole loops when re-hit. They are
exactly the content that should be **loaded conditionally** for the relevant
task type (server testing, perf work) rather than always-on or — worse — learned
again by trial.

## Theme D — Timeouts on the long tail

161 timeout anchors, plus 27 driver-level `execution timed out` events, plus
in-flight notes like "the 30-minute run times out (suite needs ~38 min), but the
45-minute run passes." The full test suite sometimes exceeds the per-loop
timeout, so a loop is killed mid-verification — wasting the entire run. The fix
is partly budget tuning and partly **scoping verification to the affected
packages** rather than the whole suite (a practice-card concern).

## How the themes map to levers

| theme | dominant lever | fix locus |
|-------|----------------|-----------|
| A. Silent regressions | **[WASTE]** | verification gate + executor/planner practice card |
| B. Partial-scope churn | **[WASTE]** + **[TIME]** | deferral ledger + carry-forward context |
| C. Concurrency/env foot-guns | **[WASTE]** | task-conditional practice cards |
| D. Long-tail timeouts | **[TIME]** + **[WASTE]** | scoped verification + budget tuning |

The unifying observation: **the lessons already exist** (in 25 memory files and
97 milestone retrospectives), but they are delivered as *always-on background
prose*, not as *task-scoped, enforced practice*. The recommendations turn
existing knowledge into cheaper, better-targeted delivery.
