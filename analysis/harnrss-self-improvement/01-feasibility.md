# Feasibility and choice of method

## Harness² mechanism and fit

The local Harness² release describes two levels of recursion: Propose–Probe–Compose on a task, then reflection that stores adopted harnesses and evidence-backed lessons for later tasks. Parallel mode probes general and domain proposals; sequential mode refines across repeated steps. The model weights remain fixed. Its editable tree covers prompts, guardrails, memory, tool guidance, subagents, skills, scripts, and plugins. These are real files, validated against a substrate-specific `SurfaceSpec`; see upstream `README.md`, `docs/EXPERIMENTS.md`, `harness2/core/{recursion,tree,context,library}.py` at the pinned commit.

The release runs three benchmark adapters (JobBench, LAB, WorkBuddy) and OpenCode or Codex substrates. It does **not** include a goopg/Ralph task adapter. Its standard `selection_pool()` offers only final compositions, excluding base runs, and `k=1` delivers its single final without a selector (`harness2/core/selection.py`). That behavior is reasonable for its published experiments, but goopg must explicitly retain the incumbent as an adoption candidate and must use external correctness gates. A delivered artifact or favorable trajectory alone cannot prove that a PostgreSQL compatibility patch is correct.

An adapter would need to map a selected, frozen goopg task to `task_prompt`, source material, isolated execution, trajectory, deliverable, and grading evidence; map the Ralph prompt/skills to an editable tree; and preserve Ralph's protected files and task authority. Those are substantial new components. Harness² can supply candidate scheduling, evidence packaging, and experience storage, but not the goopg-specific safety or score. Its released experiments do not establish improvement on this repository.

## Existing goopg assets

- The loop driver (`~/.ralph/ralph_loop.sh`) already records per-loop metrics, progress, output, and circuit-breaker signals. `.ralph/PROMPT.md` requires the final `RALPH_STATUS` block; `.ralph/fix_plan.md` supplies task order. `AGENT.md` defines tests, memory caps, commit hygiene, and completion rules.
- `analysis/ralph-loop-kaizen/pipeline/` already extracts loop telemetry and builds a compact analysis pack. Its documentation says the free stages ran and the LLM lesson-mining stage had not. `practice-cards/` contains task-specific guidance. These offer a first evidence and intervention surface without replacing the driver.
- `ci/design/07-ralph-feedback.md` documents a nightly-failure feedback path: batch output to `ci/logs/action-items.md`, then agent filing to `.ralph/fix_plan.md`. It also records a temporary priority override. Harness tuning must not become another authority for task order.
- The existing unit/component gate, pgbench smoke hook, oracle/compatibility tests, race checks, and subsystem gates form a useful independent evaluation layer. Heavy server work must use `scripts/goopg-test-run.sh` or capped `make` targets (`AGENT.md`).

## Recommendation

Build a **sidecar improvement controller** first. It can reuse the Harness² method and its terminology, but needs only an evidence extractor, candidate patcher, isolated replay runner, and evaluator. Store candidate harness patches outside the live repository until promotion. A future Harness² adapter is sensible if the sidecar has demonstrated benefit and the team wants its parallel/sequential scheduling or experience library. A direct CLI hookup today would create more integration and state-isolation risk than value.

The self-improvement loop should optimize *how Ralph works*: task-context assembly, retrieval of relevant practice cards, failure classification, checkpoint quality, and choosing the appropriate verification recipe. It should never invent product requirements, redefine PostgreSQL oracle semantics, lower gates, or make the optimizer's goal simply “more commits.” The broad post-focus task stream should span SQL semantics, storage/WAL, concurrency, planner/executor, protocol/client tooling, and performance, instead of centering plan parity alone.

## Key differences from the published setting

| Aspect | Harness² release | goopg adaptation |
| --- | --- | --- |
| Task | Bounded benchmark task with an adapter | One frozen Ralph task, often with dependencies and existing WIP |
| Output | Deliverable from a rollout | Verified code/doc change plus task-state evidence |
| Selection | Candidate final artifacts; base excluded from standard pool | Incumbent retained; reject all candidates if no safe gain |
| Feedback | Judge after selection; grader output withheld from improver | Held-out gates decide adoption; expose only training-task evidence to proposer |
| State | Per-domain library in a run namespace | Versioned lessons keyed by subsystem and failure pattern, with provenance and expiry |
| Deployment | Saved adopted harness tree | Separate owner-controlled promotion into Ralph's live harness |

Harness² documentation notes that its public benchmarks can leak grading rubrics through unrestricted executor network access (`docs/EXPERIMENTS.md`). For goopg, keep held-out tests and scoring files outside candidate worktrees and restrict the candidate's read access to them. Network access and credentials should be scoped to what a replay needs. This protects evaluation validity as well as repository state.
