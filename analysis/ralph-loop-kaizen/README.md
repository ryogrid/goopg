# Ralph Loop Kaizen

Efficiency analysis and improvement proposals for the **ralph** autonomous
coding loop that builds `goopg`. Goal: make future loops cheaper (tokens) and
faster (wall-clock) while wasting fewer loops on rework, regressions, and
blocked attempts.

## Why this exists

Over ~37 days (2026-04-28 → 2026-06-03) the loop ran **3,771 times**, recorded
**≥ $8,186** of model spend, produced 110 milestones and 600+ design docs — and
also recorded a **29% success rate**, **437 permission-denial loops**, and a
milestone history thick with *regression* / *deferred* / *partial-scope* notes.
There is large, cheap headroom. This directory finds it from the data the
harness already records, and proposes concrete changes.

The corpus is far too large to read by hand (~2.5 GB of logs). So the analysis
is produced by a **cheap-first preprocessing pipeline** (see
[`00-methodology.md`](00-methodology.md)) that distils everything into one
compact pack; an analyst (Claude) then writes the findings below.

## How to read this

| doc | what it covers |
|-----|----------------|
| [`00-methodology.md`](00-methodology.md) | The efficient-realization method: the preprocessing pipeline, the cheap-first / LLM-last strategy, costs, and how to reproduce + re-measure. |
| [`01-current-state.md`](01-current-state.md) | Data-grounded picture of where time, tokens, and loops actually go today. |
| [`02-pain-points.md`](02-pain-points.md) | The recurring struggle patterns mined from the milestone history and memory. |
| [`03-recommendations-harness.md`](03-recommendations-harness.md) | Driver / context / circuit-breaker / model-mix changes. |
| [`04-rules-and-practices.md`](04-rules-and-practices.md) | Rule settings + **task-conditional practice loading** (the headline proposal). |
| [`05-tooling.md`](05-tooling.md) | Tools to introduce or create (including the pipeline itself). |
| [`06-roadmap.md`](06-roadmap.md) | Prioritized, ROI-ranked rollout + how to measure impact. |
| [`07-implementation-status.md`](07-implementation-status.md) | **What was actually implemented (2026-06-04)**, what was skipped, and corrections implementation revealed. |
| [`pipeline/`](pipeline/) | The reusable preprocessing toolchain (built; free stages run, LLM stage gated). |
| [`practice-cards/`](practice-cards/) | The 7 task practice cards + `route.py` (wired) + `concurrency_guard.py`. |

## Scope and status

- **Design-only**: these are proposals. Nothing in the protected harness
  (`.ralph/PROMPT.md`, `.ralph/AGENT.md`, `.ralphrc`, the external
  `~/.ralph/ralph_loop.sh`) is modified here. Where a change is concrete enough
  to stage, a ready-to-apply artifact is provided but not wired in.
- The numbers in `01`/`02` come from the pipeline's **free stages**, which were
  run once over the full history to validate the parsers and ground the
  analysis. The **paid LLM lesson-mining stage was not run**; its output is the
  only placeholder.
- Reviewed by subagents (see the review note at the end of `06-roadmap.md`).

## The three levers (weighted equally)

Every recommendation is tagged with the lever(s) it pulls:

- **[WASTE]** — fewer wasted loops (rework, regressions, blocked / fast-fail).
- **[TOKEN]** — fewer tokens per loop (context size, output, model mix).
- **[TIME]** — less wall-clock per loop (turns, re-exploration, tooling).
