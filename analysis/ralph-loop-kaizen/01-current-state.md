# 01 — Current state: where time, tokens, and loops actually go

All numbers below come from the pipeline's **free stages** run once over the
full history (2026-04-28 → 2026-06-03). Figures are accurate as of that window;
re-run `pipeline/run.sh` to refresh. Caveats on coverage are called out inline.

## 1. Scale and cost

| metric | value | source |
|--------|-------|--------|
| Loop invocations started | **3,771** | `ralph.log` `=== Starting Loop` |
| Loops with a recorded result object | 3,716 | stage 1 |
| Recorded model spend | **≥ $8,186** | sum of `total_cost_usd` |
| Loops that recorded a cost | 1,115 | stage 1 |
| Cost / loop (loops with cost) | p50 **$5.05**, p90 $17, p99 $35, max **$108** | stage 1 |
| Date span | ~37 days | `metrics.jsonl` |

> **Coverage caveat:** only 1,115 of 3,716 loops recorded `total_cost_usd` (the
> rich result format; recent stream-format logs and fast-failures omit it). The
> **$8,186 is therefore a floor** — true spend is materially higher. A per-model
> cost split is available but not yet aggregated (see `cost_per_model_json` in
> `loops.csv`).

## 2. The loop population is bimodal — and most of it is waste

| metric | value |
|--------|-------|
| Success rate (`metrics.jsonl`) | **29.0%** (1,071 ok / 2,617 failed) |
| `ralph.log` `execution completed successfully` | 1,071 |
| `ralph.log` `execution failed` | **2,609** |
| `ralph.log` `execution timed out` | 27 |
| Duration: median | **5 s** |
| Duration: p90 / p99 / max | 717 s / 5,380 s / **93,244 s (~26 h)** |
| Turns: median | **1** |
| Turns: p90 / p99 / max | 71 / 305 / **1,282** |

The median loop runs **5 seconds and takes 1 turn** — i.e. it does essentially
nothing. Meanwhile the p99 loop runs ~90 minutes with hundreds of turns. The
loop count (3,771) is **inflated by a large mass of near-instant failed
invocations** stacked on top of a smaller body of genuinely productive long
loops.

**This is the single biggest finding.** ~71% of invocations are marked failed,
and the median failure is near-instant, which points at *driver-level* failure
(session-resume errors, CLI non-zero exit, or rapid retry) rather than the
agent doing expensive wrong work. These fast-fails waste little money but they:
fragment session continuity, churn `--continue` state, inflate every per-loop
average, and mean the "3,771 loops" headline overstates real progress by ~3×.
Root-causing them is the highest-leverage harness fix (see
[`03`](03-recommendations-harness.md)).

## 3. Tokens: caching is already excellent; size × turns is the lever

| metric | value |
|--------|-------|
| Cache-read fraction of input | **98.1%** |
| Output tokens / loop | p50 22k, p90 70k, p99 192k, max 654k |
| Cache-read tokens / loop | p50 5.8M, p90 **32.6M**, p99 95.7M, max **294M** |

Prompt caching is working as well as it can — 98% of input tokens are cache
reads, not fresh input. So the token lever is **not** "improve cache hit rate";
it is:

1. **Shrink the cached context.** Cache reads are billed (~10% of input price),
   and at 5.8M–95M tokens/loop they dominate input cost. Every loop re-reads the
   static instruction surface on every turn: **PROMPT.md (374 lines, ~14 KB) +
   AGENT.md (320 lines, ~14 KB)** always-on, plus the open-task slice of the
   **378 KB / 5,551-line `fix_plan.md`**, plus recalled memory. That ~28 KB of
   fixed prose + a large task slice is the trimmable denominator.
2. **Cut the turn count.** Cache-read volume scales with turns (each turn
   re-reads the growing context). The p99 loop at 381 turns reads ~96M cached
   tokens largely *because* it takes 381 turns. Fewer turns → quadratically less
   cache-read volume. This ties the TOKEN lever directly to the TIME lever.

## 4. How loops spend their turns

From a 250-loop deterministic sample (246 with activity):

| metric | value |
|--------|-------|
| Tool calls / loop (active) | mean 25, p90 68, p99 381, max 611 |
| Tool-call share | **Bash 55%**, Read 17%, Edit 7%, serena find_symbol 6%, serena replace_content 4%, Grep 3%, ToolSearch 1.5%, Write 1.4% |
| **Assistant turns to first edit** | p50 **25**, p90 104, max 288 |
| Loops with no edit at all | 183 / 246 |
| Tool-error rate | 3.8% |

The standout: the median productive loop takes **25 assistant turns before its
first edit** — a great deal of read/grep/bash exploration before acting. Much of
this is *re-discovery* the previous loop already did, lost because state isn't
carried forward in a structured way. Bash dominates the tool mix at 55% (go
test, grep, git, find), some of which is repeated build/test invocation. This is
the main **wall-clock** lever: shorten the path from loop-start to first useful
edit.

## 5. The status protocol is mostly not followed

| metric | value |
|--------|-------|
| Loops emitting `RALPH_STATUS` block | **28.3%** |
| Among those — IN_PROGRESS / BLOCKED / COMPLETE | 675 / **239** / 138 |
| Work type — IMPL / DEBUG / DOC / TEST / REFACTOR | 918 / 66 / 36 / 29 / 3 |

`PROMPT.md` says the `---RALPH_STATUS---` block must be emitted **ALWAYS** — the
circuit breaker, completion detection, and the test-only-loop guard all depend
on it. In practice it appears in **only 28% of loops**. So the harness's
self-regulation runs blind ~72% of the time. Among loops that *did* report,
**BLOCKED (239) outnumbers COMPLETE (138)** — blocked loops are a large,
under-managed category.

## 6. Permission denials and the circuit breaker

| metric | value |
|--------|-------|
| Loops with ≥1 permission denial | **437** (~12%) |
| Circuit-breaker trips (total) | **8** |
| CB trip reason | 8/8 "Permission denied in 2 consecutive loops" |
| CB trips on the 2,609 failures | **0** |

437 loops hit a permission denial. `ALLOWED_TOOLS` already permits all Bash
(`Bash(*)`) and all Serena tools, so the denials are **not** Bash — they are
newer built-in tools missing from the whitelist. The Stage-2 histogram names the
offenders by call volume: **ToolSearch (95), TodoWrite (75), TaskUpdate/TaskCreate
(41), Glob (17), Agent (16), Monitor (3), ScheduleWakeup (1)** — every one of
these is absent from `ALLOWED_TOOLS`. The circuit breaker has opened exactly
**8 times in 3,771 loops, always for permission denials** — never once for the 2,609 execution failures, because
`CB_SAME_ERROR_THRESHOLD` and `CB_OUTPUT_DECLINE_THRESHOLD` are set to **1000**
(effectively disabled). The breaker is not catching the dominant failure mode.

## 7. The harness today (for reference)

- **Driver:** `~/.ralph/ralph_loop.sh` (~2,300 lines, outside the repo) runs
  `claude < .ralph/PROMPT.md` with `--continue` (session continuity) and
  `--append-system-prompt` carrying loop number + remaining open tasks + CB state.
- **Context loading is entirely static:** `PROMPT.md` (always), `AGENT.md`
  (always), the user memory at
  `~/.claude/projects/.../memory/` (25 files, 180 KB), and a slice of the
  378 KB `fix_plan.md`. There is **no task-conditional loading** — every loop
  pays for every instruction whether or not it is relevant.
- **Config (`.ralphrc`):** `MAX_CALLS_PER_HOUR=100`, `CLAUDE_OUTPUT_FORMAT=json`,
  `SESSION_CONTINUITY=true`, `CB_NO_PROGRESS_THRESHOLD=3`,
  `CB_SAME_ERROR_THRESHOLD=1000`, `CB_OUTPUT_DECLINE_THRESHOLD=1000`.
  `ALLOWED_TOOLS` allows *all* Bash (`Bash(*)`) and all Serena tools, but omits
  several built-in tools the agent routinely calls — see §6.
- **Model:** a mix is already in use (Opus/Sonnet/Haiku appear in `modelUsage`;
  subagents use cheaper tiers), but the main-loop model and the
  delegation policy are not tuned against cost data.

## Summary of levers

| # | finding | lever |
|---|---------|-------|
| 2 | 71% fast-fail invocations inflate the loop count | **[WASTE]** |
| 3 | ~28 KB fixed prose + a 378 KB fix_plan slice re-read every turn; cost ∝ context × turns | **[TOKEN]** |
| 3/4 | 305-turn loops (p99); 25 turns to first edit (median) | **[TIME]** |
| 5 | 28% status compliance → blind self-regulation | **[WASTE]** |
| 6 | 437 permission-denial loops; CB disabled for failures | **[WASTE]** |
