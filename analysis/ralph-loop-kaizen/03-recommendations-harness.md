# 03 — Recommendations: harness

Changes to the driver (`~/.ralph/ralph_loop.sh`), config (`.ralphrc`), and the
static prompt (`.ralph/PROMPT.md`). **All of these files are protected/external;
this document proposes, it does not apply.** Each item is tagged with its lever
and given a rough effort/impact. Ordered by ROI.

> Re-measure every change with the Stage 1–3 instrument (see [`06`](06-roadmap.md)).
> Don't trust these as guaranteed wins — they are hypotheses with a measurement plan.

## H1. Root-cause the 71% fast-fail rate — **[WASTE]** — highest ROI

[`01 §2`](01-current-state.md): 2,609 "execution failed" events, median failure
near-instant (5 s / 1 turn). This is the dominant pathology. The near-instant
median **strongly suggests** a *driver-level* cause (session resume, CLI
non-zero exit, stream-parse failure) rather than the agent doing expensive wrong
work — but that is a hypothesis, and confirming it is exactly what T2 below does.
**Do not fix blind: classify first.**

- **Investigate first (cheap):** the pipeline can join `metrics.success` ×
  `ralph.log` event × the loop's `.log` `is_error`/`stop_reason` to classify the
  2,609 failures (CLI non-zero exit? `--continue` session-resume failure? stderr
  written? "Could not find result message" = 65 cases of stream-parse failure?).
  Add a `--classify-failures` mode to `extract_telemetry.py`.
- **Likely fixes:** the driver currently treats stderr output and certain stop
  reasons as failure; `--continue` session resumption may be failing and forcing
  fresh sessions (39 `session_reset` events). Each fast-fail also *discards the
  loop's turn* of useful planning.
- **Impact:** if even half of the 2,609 failures are spurious, the effective
  loop count and every per-loop average are distorted ~2×; fixing it both saves
  wall-clock churn and makes all other metrics trustworthy.
- **Effort:** M (driver investigation). **Do this before tuning anything else** —
  the other metrics are measured against a population that is 71% noise.

## H2. Shrink the always-on static context — **[TOKEN]**

[`01 §3`](01-current-state.md): cache-read volume is 5.8M (p50) to 95M (p99)
tokens/loop, billed every loop. The always-on instruction surface is PROMPT.md
(~14 KB) + AGENT.md (~14 KB) ≈ 28 KB of fixed prose, plus the open-task slice of
the **378 KB / 5,551-line `fix_plan.md`**, re-read every turn.

- **Split `fix_plan.md`:** keep only *active* milestones in the file the loop
  loads; move closed milestones to `completed_milestones/` (the convention
  already exists — extend it). The driver already injects "remaining unchecked
  tasks," but the file it slices from is huge; a smaller active file means a
  smaller slice and less risk of dragging in stale context.
- **Trim `PROMPT.md`:** at ~370 lines it embeds six full RALPH_STATUS example
  blocks and six exit scenarios. Compress to one example + a reference; the
  detail can live in a loaded-on-demand doc.
- **Move always-on memory to conditional loading** (see [`04`](04-rules-and-practices.md)):
  25 memory files (180 KB) are background context every loop; most are
  irrelevant to any given task.
- **Impact:** every KB removed from static context is multiplied by *turns* in
  cache-read billing. On a 305-turn loop (p99), trimming 100 KB of context saves
  on the order of tens of millions of cache-read tokens. **Measure** cache-read
  p50/p90 before/after.
- **Effort:** S–M.

## H3. Fix the permission whitelist — **[WASTE]**

[`01 §6`](01-current-state.md): 437 loops hit a permission denial; all 8 circuit-
breaker trips were permission-denial trips.

- The cause is precise and already in the data. `.ralphrc` `ALLOWED_TOOLS`
  permits **all Bash** (`Bash(*)`) and all Serena tools — so the denials are
  **not** Bash. They are newer **built-in tools missing from the whitelist**,
  named by the Stage-2 histogram in call-volume order: `ToolSearch` (95),
  `TodoWrite` (75), `TaskUpdate`/`TaskCreate` (41), `Glob` (17), `Agent` (16),
  `Monitor` (3), `ScheduleWakeup` (1).
- **Fix:** add these tools to `ALLOWED_TOOLS` (or switch to an allow-by-default
  policy with a deny-list for genuinely dangerous operations). This is a
  one-line config change with a directly-evidenced target.
- **Impact:** removes a class of wasted turns and the only failure mode that
  currently trips the breaker (often spuriously). **Effort:** S.

## H4. Make the circuit breaker catch the real failure mode — **[WASTE]**

[`01 §6`](01-current-state.md): `CB_SAME_ERROR_THRESHOLD=1000` and
`CB_OUTPUT_DECLINE_THRESHOLD=1000` are effectively off; the breaker never fired
on the 2,609 failures.

- Set realistic thresholds (e.g. same-error 3–5, output-decline 3) so repeated
  identical failures actually halt instead of burning loops — *after* H1, so the
  thresholds are tuned against a clean failure signal.
- The PROMPT.md "stuck on recurring error" scenario (Scenario 3) is currently
  unreachable; this makes it real. **Effort:** S (config), but gated on H1.

## H5. Restore status-block compliance — **[WASTE]**

[`01 §5`](01-current-state.md): only 28% of loops emit `RALPH_STATUS`, yet the
breaker / completion / test-only detection all depend on it.

- The block is long and hand-formatted; compliance decays. Two options:
  (a) shrink it to a few required fields and have the driver *parse loosely*;
  (b) better — have the **Stop hook** (already wired for Serena in
  `.claude/settings.local.json`) validate that a status block was emitted and,
  if not, synthesize a minimal one from the transcript. This moves enforcement
  off the model's discipline and onto deterministic harness code.
- **Impact:** restores the harness's ability to self-regulate (detect stuck/
  blocked/complete), which underpins H4 and the WASTE lever generally.
- **Effort:** M.

## H6. Tune the model mix against cost data — **[TOKEN]**

A mix is already used, but not chosen against the cost split. The pipeline
exposes `cost_per_model_json` per loop; aggregate it (small follow-up) to see
where Opus tokens go.

- **Delegate more to Haiku/Sonnet:** mechanical sub-tasks (log triage, doc-index
  updates, test-name lookups, the lesson-mining of this very analysis) do not
  need the top model. The `pattern_parallel_agent_exploration` memory already
  favors subagents; bias those subagents to cheaper tiers explicitly.
- **Keep Opus for the hard core** (planner/executor reasoning, root-causing
  silent regressions).
- **Impact:** the top cost loops are $63–$108 each; shifting their exploration
  legs to cheaper models is real money. **Effort:** S–M; **measure** per-model
  cost share before/after.

## H7. Carry structured state across loops — **[TIME]** + **[TOKEN]**

[`01 §4`](01-current-state.md): 25 assistant turns to first edit (median); much
is re-discovery.

- Have each loop append a tiny structured "working set" note (files touched,
  key symbols, current hypothesis, next step) that the driver injects into the
  next loop's context — cheaper and more reliable than re-grepping. This is
  distinct from the `--continue` session (which carries the *conversation*, not
  a *curated* state) and survives the frequent session resets.
- Pairs with the **deferral ledger** ([`02 Theme B`](02-pain-points.md)): closed-
  at-partial-scope items record exactly where to resume.
- **Effort:** M.

## Priority order

1. **H1** (root-cause fast-fails) — unblocks trustworthy measurement of all else.
2. **H3** (permission whitelist) — cheap, removes a whole waste class.
3. **H2** (shrink static context) — biggest steady-state token win.
4. **H5** (status compliance) — re-enables self-regulation.
5. **H4** (CB thresholds) — cheap, but gated on H1+H5.
6. **H6** (model mix) — real money, needs the cost-split follow-up.
7. **H7** (carry state) — TIME win, larger build.
