# 00 — Methodology: how to analyze a 2.5 GB loop history cheaply

This document is the answer to the hard half of the request: *not* "what should
we improve" but "how do we figure that out efficiently, given the data is far
too big to read." The principle is **cheap-first, LLM-last, output-small**.

## The problem: the corpus dwarfs any context window

| source | size | nature |
|--------|------|--------|
| `.ralph/logs/*.log` (per-loop result) | ~3,750 files | JSON (old) / JSONL stream (recent) |
| `.ralph/logs/*_stream.log` | ~3,450 files | JSONL streaming events (tool calls) |
| `.ralph/logs/metrics.jsonl` | 3,688 rows | loop spine: ts / duration / success |
| `.ralph/logs/ralph.log` | ~4 MB | driver narrative: timeouts / failures / CB |
| `docs/milestones/` + `completed_milestones/` + `docs/handover/` | ~2.5 MB | "what was hard" prose |
| **total** | **~2.5 GB** | |

Reading even 1% of this into a context would be wasteful and lossy. Naively
summarizing every loop with an LLM would cost hundreds of dollars. Neither is
acceptable.

## Key realization: most signal is already structured

The expensive-looking question "where do our tokens and time go?" is answerable
from data the harness *already records as JSON*. Cost, tokens, turns, model
mix, permission denials, and terminal reasons are all fields in the per-loop
result object; duration and success are in `metrics.jsonl`; timeouts, failures,
and circuit-breaker trips are greppable in `ralph.log`. **No LLM is needed for
~90% of the quantitative analysis.** The only genuinely semantic task —
"what lesson did this milestone teach" — is a small (~2.5 MB) text corpus that a
*cheap* model can map over.

So the pipeline is ordered by cost:

```
Stage 1  telemetry        pure Python   FREE    where tokens/time/loops go
Stage 2  tool usage       pure Python   FREE    how loops spend their turns
Stage 3  corpus anchors   pure Python   FREE    where the struggle is (grep)
Stage 4  lesson mining    Haiku -p      ~$15    what the struggle taught (gated)
Stage 5  analysis pack    pure Python   FREE    one compact file for the analyst
```

Stages 1–3 + 5 cost nothing and run in **~27 seconds** over the full history.
Only Stage 4 touches the API, and it uses the cheapest model, is **dry-run by
default**, and prints a cost estimate before spending a cent.

## The pipeline (see [`pipeline/`](pipeline/))

`pipeline/run.sh` is the single entrypoint. It is read-only against the corpus;
all output lands in the gitignored `pipeline/data/`.

1. **`extract_telemetry.py`** — parses every per-loop log (handling both the
   single-object "result" format and the newer JSONL "stream" format via
   `lib/common.py`), joins `metrics.jsonl`, tallies `ralph.log` driver events,
   and reads `.circuit_breaker_history`. Emits `loops.csv` + `telemetry_summary.json`.
2. **`extract_tool_usage.py`** — samples stream logs (evenly-spaced, deterministic)
   and counts tool calls, turns-to-first-edit, and tool-error rate.
3. **`assemble_corpus.py`** — greps the milestone history for "struggle" anchors
   (`regression`, `deferred`, `partial`, `blocker`, `root cause`, `silently`, …)
   and builds a milestone index.
4. **`mine_lessons.py chunk` → `mine_lessons.sh` → `mine_lessons.py reduce`** —
   chunks the ~2.5 MB curated corpus, maps a fixed extraction prompt over each
   chunk with `claude -p --model haiku`, then clusters the results **locally**
   (the dedup an LLM would otherwise be paid to do). Gated behind `--with-llm`
   and `--llm-confirm`.
5. **`build_analysis_pack.py`** — merges everything into `ANALYSIS_PACK.md`
   (~14 KB), small enough for one context to read in full.

### Why `claude -p` with Haiku, not the analyst model

Lesson extraction is a *map* over many independent chunks — exactly the shape a
small, cheap model handles well, and exactly the shape you do **not** want to
spend Opus tokens on. The estimated cost over the corpus is **≈ $15** (270
chunks, ~631k input tokens) — dominated not by the chunk text but by the
**per-call system-prompt overhead** (~$0.05/call: each `claude -p` is a fresh
process that re-pays cache-creation for its system prompt). Still cheap, and
still pennies vs. running the analyst model over the raw corpus. *(Estimate
only — Stage 4 was not run; the chunker prints the live estimate before any
spend.)* The expensive model (the analyst) only ever reads the 14 KB pack,
never the raw corpus. This is the core token-efficiency move, applied to the
analysis itself.

### Cost-safety design

- Dry-run is the default; `mine_lessons.sh` prints chunk count + cost estimate
  and exits unless `--confirm`.
- `--llm-max-chunks N` bounds a trial run.
- The extraction prompt forbids tool use and the call passes `--disallowed-tools`
  so a headless Haiku can't stall waiting on a tool permission.

## Reproducing and re-measuring

```bash
cd analysis/ralph-loop-kaizen/pipeline
./run.sh                 # free stages -> data/ANALYSIS_PACK.md  (~27s)
./run.sh --with-llm --llm-confirm   # add the ~$15 lesson layer
```

Stages 1–3 double as the **before/after instrument** for any harness change:
snapshot `telemetry_summary.json`, apply a change, let the loop run N times,
re-run, and diff cost / turns / blocked-rate / cache-read fraction. This is how
[`06-roadmap.md`](06-roadmap.md) proposes to prove each recommendation actually
helped, rather than assuming it did.

## What this method deliberately does *not* do

- It does **not** LLM-summarize raw loop transcripts (520 MB+ of stream events).
  Tool-call *shape* is extracted structurally (Stage 2); the semantic "why" is
  taken from the milestone history (Stage 4), which is already a human-curated
  summary of those transcripts. Summarizing the raw streams would cost
  100×–500× more for marginal added signal.
- It does **not** attempt a perfect per-loop join across all four sources
  (filename timestamp vs `metrics.jsonl` timestamp vs `ralph.log` ordering).
  Where exact attribution matters (e.g. "which model did the expensive loops
  use"), the pipeline exposes the raw columns (`cost_per_model_json` in
  `loops.csv`) for a targeted follow-up rather than guessing.
