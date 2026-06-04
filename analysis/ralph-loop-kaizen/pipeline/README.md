# Ralph-loop-kaizen preprocessing pipeline

A small, reusable toolchain that distils the ralph loop's history into a compact
`ANALYSIS_PACK.md` a single Claude context can read in full. It is the
"efficient realization" layer described in [`../00-methodology.md`](../00-methodology.md):
**cheap-first, LLM-last, output-small.**

> Status: the **free stages (1, 2, 3, 5) have been run once** to validate the
> parsers and to ground the sibling analysis docs with real numbers; their
> outputs live in the gitignored `data/` dir and are not committed. The
> **paid LLM stage (4) has not been run** — its lesson clusters are the only
> `<run pipeline to populate>` placeholder in the docs.

## Why a pipeline (not just "read the logs")

The corpus is too big to read directly:

| source | size | what it holds |
|--------|------|---------------|
| `.ralph/logs/` | ~2.5 GB, ~7,300 files | per-loop result JSON + JSONL stream events |
| `.ralph/logs/metrics.jsonl` | ~330 KB, 3,688 rows | loop spine: timestamp / duration / success |
| `.ralph/logs/ralph.log` | ~4 MB | driver narrative: timeouts / errors / CB trips |
| `docs/milestones/` + `completed_milestones/` + `docs/handover/` | ~2.5 MB | "what was hard" prose |

99% of the useful *quantitative* signal is already structured (JSON / JSONL),
so stages 1–3 + 5 extract it with **pure Python stdlib — no API cost**. Only the
*qualitative* lesson extraction (stage 4) needs an LLM, and that runs on the
cheapest model (`claude -p --model haiku`), gated and dry-run by default.

## Quick start

```bash
cd analysis/ralph-loop-kaizen/pipeline

./run.sh                      # free stages (1,2,3,5) -> data/ANALYSIS_PACK.md
./run.sh --sample 300         # widen stage-2 stream-log sampling
./run.sh --with-llm           # also chunk + DRY-RUN the LLM map (no API calls)
./run.sh --with-llm --llm-confirm           # actually call Haiku (costs ~$15)
./run.sh --with-llm --llm-confirm --llm-max-chunks 20   # bound the spend
./run.sh --stages 1,5         # run a subset
```

All inputs are read **read-only**; all outputs land in `./data/` (gitignored).

## Stages

| # | script | cost | output |
|---|--------|------|--------|
| 1 | `extract_telemetry.py` | free | `loops.csv`, `telemetry_summary.json` |
| 2 | `extract_tool_usage.py` | free | `tool_usage_summary.json` |
| 3 | `assemble_corpus.py` | free | `anchors.jsonl`, `milestone_index.csv`, `corpus_stats.json` |
| 4 | `mine_lessons.py chunk` + `mine_lessons.sh` + `mine_lessons.py reduce` | **paid (Haiku), gated** | `chunks/`, `lessons_raw.jsonl`, `lessons_clustered.md` |
| 5 | `build_analysis_pack.py` | free | `ANALYSIS_PACK.md` |

`lib/common.py` holds the shared, format-agnostic log parsing (handles both the
single-object "result" logs and the newer JSONL "stream" logs).

## Stage 4 cost model & safety

- Default model: `haiku` (alias → latest Haiku 4.5). Override with
  `KAIZEN_LLM_MODEL` or `mine_lessons.sh --model`.
- **Dry-run by default.** `mine_lessons.sh` prints the chunk count and a cost
  estimate and exits unless `--confirm` is passed. `run.sh` only confirms when
  you pass `--llm-confirm`.
- The chunker prints an up-front estimate (≈ `chars/4` input tokens + ~600
  output tokens/chunk + **~$0.05/call system-prompt overhead** — each `claude -p`
  is a fresh process that re-pays cache-creation for its system prompt). The
  ~2.5 MB corpus is ~270 chunks → **≈ $15**, dominated by that per-call
  overhead. Cheap, and far below the "hundreds of dollars" a raw-transcript pass
  would cost.
- `--llm-max-chunks N` caps spend for a trial run.
- The extraction prompt forbids tool use and the call passes
  `--disallowed-tools` so the headless model can't stall on a tool request.

> Validate flags against your CLI once before the first paid run:
> `claude --help | grep -E 'print|model|output-format|disallowed'`
> (built against Claude Code 2.1.161).

## Re-measuring impact

Stages 1–3 are the "before/after" instrument. Snapshot `telemetry_summary.json`
before a harness change, re-run after N loops, and diff cost / turns / blocked-
rate / cache-read fraction to quantify the improvement. See
[`../06-roadmap.md`](../06-roadmap.md).

## Idempotency / reproducibility

Re-running overwrites `data/` deterministically (stage-2 sampling is by
evenly-spaced index, not RNG; stage-4 clears stale chunks first). Safe to run
repeatedly.
