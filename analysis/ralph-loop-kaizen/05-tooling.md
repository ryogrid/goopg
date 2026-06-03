# 05 — Tooling to introduce or create

Tools that make the recommendations in [`03`](03-recommendations-harness.md) and
[`04`](04-rules-and-practices.md) cheap to run and to verify. Two are already
built in this directory; the rest are small, scoped follow-ups.

## T1. Loop-health pipeline — **built** ([`pipeline/`](pipeline/))

The preprocessing pipeline is itself the most important new tool: it converts
2.5 GB of opaque logs into a 14 KB pack and a `loops.csv`/`telemetry_summary.json`
in ~27 s, for free. Beyond producing this analysis, it is the permanent
**before/after instrument** for every change below.

- **Proposal:** add a `make ralph-metrics` target that runs the free stages and
  prints the headline numbers (success rate, cost/loop, cache-read p50/p90,
  blocked-rate, permission-denial loops). Wire it as a periodic check so
  regressions in *loop health itself* are visible — today nobody is watching the
  29% success rate.
- **Reuse:** `lib/common.py` already handles both log formats; new analyses
  (e.g. T2) are small additions, not new parsers.

## T2. Failure classifier — small follow-up ([`03 H1`](03-recommendations-harness.md))

A `--classify-failures` mode for `extract_telemetry.py` that joins, per loop,
`metrics.success` × `ralph.log` event × `.log` `is_error`/`stop_reason` and
buckets the 2,609 failures (CLI non-zero / session-resume / stderr-written /
stream-parse-failure / timeout / fast-exit). This turns "71% fail" from a
mystery into a ranked, actionable list. **Effort: S** — the data is already
loaded by Stage 1; this adds the cross-source join the pipeline deliberately
left out.

## T3. Practice-card router — **built** ([`practice-cards/route.py`](practice-cards/route.py))

The cheap, deterministic pre-loop context builder from
[`04 §4.2`](04-rules-and-practices.md). Already runnable and validated. The
remaining work is authoring the four not-yet-written cards (wal-replication,
tpch-perf, regress-port, catalog-ddl) and wiring the `UserPromptSubmit` hook.

## T4. Lesson distiller — uses the pipeline's Stage 4

Stage 4 (`mine_lessons.*`) is not just for this one analysis; run periodically
(monthly, or after each batch of milestones lands) it keeps the practice cards
and memory current **for ~$1.5 per run**:

```
completed_milestones/ + new handovers  --Haiku map-->  raw lessons
                                        --local reduce-->  clustered lessons
                                        --human/Opus curate-->  updated cards
```

This closes the loop: the loop's own retrospectives feed back into the cards
that make the next loops cheaper. Without it, the cards rot and drift from
reality — exactly the failure the always-on memory system has today (25 files,
manually maintained, easy to forget).

## T5. Status-block enforcer — Stop hook ([`03 H5`](03-recommendations-harness.md) / [`04 R4`](04-rules-and-practices.md))

A `Stop`-hook script (the `Stop` hook is already used for Serena cleanup, so the
slot is proven) that:
1. checks the transcript for a `---RALPH_STATUS---` block;
2. if absent, synthesizes a minimal one from the turn (files modified from
   `git diff`, tests status from the last test invocation);
3. writes it where the driver's response-analyzer reads it.

Moves status reliability from 28% (model discipline) toward ~100% (deterministic
harness), restoring the circuit breaker / completion detection. **Effort: M.**

## T6. Deferral ledger — convention + tiny tool ([`04 R2`](04-rules-and-practices.md))

A single `deferral-ledger.md` with structured one-liners and a `route.py`-style
helper that surfaces only the ledger lines relevant to the current milestone, so
a resumed task loads its resume-point cheaply instead of re-reading a 700 KB
completed-plan journal. **Effort: S.**

## T7. Concurrency guard — SessionStart hook ([`04 R5`](04-rules-and-practices.md))

A `pgrep -f ralph_loop`-style check at `SessionStart` that warns/aborts if a
second loop is running on the same tree — encoding
`concurrent_ralph_loops_corrupt_tree` as a guard instead of a hope. **Effort: S.**

## Build-vs-reuse summary

| tool | status | effort | reuses |
|------|--------|--------|--------|
| T1 loop-health pipeline | **built** | — | — |
| T2 failure classifier | follow-up | S | Stage 1 data |
| T3 practice-card router | **built** | — (author 4 cards + wire) | — |
| T4 lesson distiller | built (Stage 4), run periodically | — | pipeline Stage 4 |
| T5 status enforcer | follow-up | M | existing `Stop` hook slot |
| T6 deferral ledger | follow-up | S | `route.py` pattern |
| T7 concurrency guard | follow-up | S | existing `SessionStart` hook slot |

Everything either exists or is a small addition that reuses an existing hook
slot or the pipeline — no large greenfield build is required.
