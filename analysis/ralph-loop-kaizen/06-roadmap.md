# 06 — Roadmap, prioritization, and measurement

A rollout ordered by ROI, balanced across the three levers, with a concrete way
to prove each step worked. Effort is S/M/L; lever tags from
[`README`](README.md).

## Sequencing principle

**Measure the population before tuning it.** ~71% of loops are fast-fails
([`01 §2`](01-current-state.md)), so today's averages describe a population that
is mostly noise. H1 (root-cause fast-fails) and T2 (failure classifier) come
first so every later before/after comparison is trustworthy.

## Phase 0 — Instrument (do first)

| step | lever | effort | exit criterion |
|------|-------|--------|----------------|
| Run `pipeline/run.sh` and snapshot `telemetry_summary.json` as the baseline | — | S | baseline committed to `analysis/` |
| **T2** failure classifier; **H1** root-cause the fast-fails | WASTE | M | the 2,609 failures bucketed; top cause identified |
| Add `make ralph-metrics` (**T1**) | — | S | one command prints loop-health headline |

## Phase 1 — Cheap, high-certainty wins

| step | lever | effort | expected signal |
|------|-------|--------|-----------------|
| **H3** widen `ALLOWED_TOOLS` to the tools loops actually use | WASTE | S | permission-denial loops ↓ from 437 |
| **H1 fix** for the dominant fast-fail cause | WASTE | M–L | success rate ↑ from 29%; loop count deflates |
| **T7** concurrency guard (SessionStart hook) | WASTE | S | no tree-corruption incidents |

## Phase 2 — Token & self-regulation

| step | lever | effort | expected signal |
|------|-------|--------|-----------------|
| **H2** split `fix_plan.md`, trim `PROMPT.md` | TOKEN | S–M | cache-read p50/p90 tokens ↓ |
| **T5/H5/R4** status enforcer (Stop hook) | WASTE | M | status coverage ↑ from 28% |
| **H4** real circuit-breaker thresholds (after H1+H5) | WASTE | S | repeated failures halt in ≤5 loops, not ∞ |

## Phase 3 — Practice loading & state carry

| step | lever | effort | expected signal |
|------|-------|--------|-----------------|
| Author remaining 4 practice cards (**T3**) | WASTE | M | cards exist for all task types |
| Wire `route.py` via `UserPromptSubmit` hook | TOKEN/WASTE | S | per-loop context ↓; right gate present |
| **R1** verification gate (executor/planner) | WASTE | M | silent-regression bisects ↓ |
| **T6/R2** deferral ledger | TIME/WASTE | S | resumed tasks reach first edit faster |
| **H7** carry structured working-set across loops | TIME/TOKEN | M | turns-to-first-edit ↓ from 25 (median) |

## Phase 4 — Ongoing

| step | lever | effort | cadence |
|------|-------|--------|---------|
| **H6** model-mix tuning (after per-model cost rollup) | TOKEN | S–M | one-off + revisit |
| **T4** lesson distiller folds new retrospectives into cards | WASTE | — (~$1.5) | monthly / per milestone batch |
| Re-run `make ralph-metrics`, diff vs baseline | — | S | every phase |

## How to measure impact (the contract)

For each change, the claim is a hypothesis until the instrument confirms it:

1. Snapshot `telemetry_summary.json` (+ `tool_usage_summary.json`) as `before`.
2. Apply exactly one change.
3. Let the loop run a comparable batch (≥ ~50 loops, similar task mix).
4. Re-run the free stages; diff the metric the change targets:

| change | primary metric to move |
|--------|------------------------|
| H1 fast-fail fix | success rate; median duration; loop count |
| H2 context trim | cache-read tokens p50/p90 |
| H3 whitelist | permission-denial loops |
| H4 CB thresholds | loops between first failure and halt |
| H5 status enforcer | RALPH_STATUS coverage |
| H6 model mix | per-model cost share; cost/loop |
| H7 / R2 carry-state | turns-to-first-edit |
| R1 verification gate | regression anchors in new milestones (Stage 3) |

If the targeted metric does not move, **revert** — none of these touch protected
files irreversibly, and the WASTE lever is only worth pulling if it measurably
reduces waste.

## Rough impact estimate (to be confirmed by measurement)

The prizes pull different levers, and it matters not to conflate them:

- **H1 is a wall-clock + measurement-hygiene win, not a money win.** The fast-
  fails are near-instant and mostly cost ~$0 ([`01 §2`](01-current-state.md)
  notes they "waste little money"); fixing them roughly doubles real throughput
  per wall-clock hour and makes every per-loop average trustworthy. It ranks #1
  on ROI because it is cheap, high-certainty, and unblocks measurement — *not*
  because it saves the most dollars.
- **H2 + H7 are the actual cost win.** Dollars are concentrated in the few
  hundred long loops ($31–$108 each, 100s–1000s of turns); cache-read volume
  (5.8M–95M tokens/loop) is driven by context size × turn count. Trimming static
  context (H2) and cutting re-exploration turns (H7) compound on exactly those
  expensive loops.
- **H3 and the hook-based enforcers (T5, T7) are near-free** and remove whole
  waste classes.

Everything is incremental and reversible, so the roadmap can be run
opportunistically between feature milestones rather than as a stop-the-world
project.

---

## Review note

Two subagents reviewed this work after drafting and their findings were folded
back before finalizing:

- **Scripts** (correctness / robustness / cost-safety): found and fixed three
  parser bugs — stream-format token/turn double-counting (one logical assistant
  message spans several JSONL lines sharing a `message.id`; now deduped), the
  same inflation in turns-to-first-edit, and a percentile off-by-one. The cost
  estimate was corrected upward (~$1.4 → ~$15) to include per-call system-prompt
  overhead, and `--max-turns 1` was added to the gated LLM call. Cost-safety
  (dry-run-by-default) was verified sound.
- **Docs** (groundedness / feasibility / balance): every quantitative claim was
  re-checked against `pipeline/data/*.json`. Corrected: the permission-denial
  cause (`.ralphrc` already allows `Bash(*)` — the real offenders are
  non-whitelisted built-in tools like `ToolSearch`/`TodoWrite`/`Task*`), counts
  (110 milestones, ~2.5 MB corpus), and softened the "fast-fails are
  driver-level" claim to a hypothesis pending the T2 classifier. The
  load-bearing `UserPromptSubmit`-fires-on-piped-stdin assumption in §4 was
  elevated to a must-verify caveat with a `SessionStart` fallback.
