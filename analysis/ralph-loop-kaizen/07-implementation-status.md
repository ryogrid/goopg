# 07 — Implementation status (2026-06-04)

What was actually built when the proposals were implemented, what was
deliberately skipped, and — importantly — what implementation *taught us* that
corrects the earlier analysis. The loops were stopped first (a quiet tree) to
avoid the [[concurrent_ralph_loops_corrupt_tree]] failure mode.

## Implemented

| item | what changed | file(s) |
|------|--------------|---------|
| **H1 driver fix** (the big one) | 429 usage-limit responses (`is_error:true` + `api_error_status:429`) now `return 2` → the existing API-limit **wait** path, instead of `return 1` → 30s retry-storm. Session is **not** reset (it's valid; only the quota is exhausted). | `~/.ralph/ralph_loop.sh` (backup: `ralph_loop.sh.bak-kaizen-20260604`) |
| **H3 permission whitelist** | added the denied built-in tools (`ToolSearch`, `TodoWrite`, `Task*`, `Glob`, `Agent`, `Monitor`, `ScheduleWakeup`) to `ALLOWED_TOOLS`. | `.ralphrc` |
| **H4 circuit-breaker** | `CB_SAME_ERROR_THRESHOLD` 1000 → 5 (real backstop for any future failure storm); `CB_OUTPUT_DECLINE_THRESHOLD` 1000 → 70 (lib default; currently defined-but-unused). | `.ralphrc` |
| **T2 failure classifier** | `--classify-failures` mode that root-causes the failures via the exact ralph.log→.log join. | `pipeline/extract_telemetry.py` |
| **T1 loop-health metric** | `make ralph-metrics` prints success rate / cost / cache-read / status coverage / permission denials / failure breakdown. | `Makefile`, `pipeline/metrics_report.py` |
| **T3 practice cards** | the 4 remaining cards authored (wal-replication, tpch-perf, regress-port, catalog-ddl); all 7 now exist. | `practice-cards/*.md` |
| **Task-conditional practice loading** | `UserPromptSubmit` hook runs `route.py` (verified: the hook *does* fire for `claude -p`). Cards are injected only when the task/diff matches, capped at 3. | `.claude/settings.local.json`, `practice-cards/route.py` |
| **T7 concurrency guard** | `SessionStart` hook warns (never blocks) when a ralph loop is already running — exactly the situation that bit this session. | `.claude/settings.local.json`, `practice-cards/concurrency_guard.py` |

### H1 verified impact (from the classifier)

90.9% of the 2,609 "execution failed" loops were usage-limit 429s (median 0.8 s),
in retry storms up to **432 consecutive** failures. The fix converts those storms
into a single wait-for-reset, so the loop stops hammering the API after the
limit is hit. Re-run `make ralph-metrics` after the next active period to confirm
the failure rate drops.

## Deliberately skipped or deferred (with reasons)

- **H2 (split `fix_plan.md`, trim `PROMPT.md`) — SKIPPED.** Reading the driver
  showed `build_loop_context` injects only a *count* of remaining tasks
  (`grep -c '[ ]'`) and caps the appended context at 500 chars — **`fix_plan.md`
  content is never auto-injected**. Splitting it saves ~zero injected tokens
  while risking loss of open tasks. Trimming `PROMPT.md` saves only its one-time
  ~14 KB/loop, dwarfed by accumulated tool-results. Low value, protected-file
  risk → not done.
- **T5 (status-block enforcer Stop hook) — DEFERRED.** It would need to parse the
  transcript and synthesize/inject a status block coupled to the driver's
  response-analyzer — fragile to bolt onto the live loop's `Stop` path for
  marginal value (see the compliance correction below). Better done with care.
- **H6 (model-mix tuning) — follow-up.** Needs the per-model cost rollup first
  (`cost_per_model_json` in `loops.csv`); a one-off analysis, not a code change.
- **H7 (carry structured working-set across loops) — follow-up.** The genuine
  token/wall-clock lever (see correction), but a larger driver build.
- **T6 (deferral ledger) — follow-up.** Small; the practice cards already
  reference the convention.

## Corrections to the earlier analysis (learned while implementing)

Implementation falsified two assumptions in `01`/`03`; recording them so the
docs aren't trusted blindly:

1. **The static prompt / `fix_plan.md` are NOT the token bottleneck.** They are
   not auto-injected per turn. The 5.8M–95M cache-read tokens/loop come from
   **tool-results accumulating across turns** in long loops. The real
   token-and-wall-clock lever is **reducing turns / redundant re-exploration**
   (H7), not trimming static files. `03 H2` should be read with this caveat.
2. **The "28% status-block coverage" is largely a 429 artifact.** The ~2,372
   usage-limit failures produce a result but no status block, so they counted as
   non-compliant. With H1 removing them, apparent compliance rises without any
   prompt change — which is why the T5 enforcer is lower priority than `04 R4`
   implied.

## How to verify / re-measure

```bash
make ralph-metrics                         # loop-health headline (free)
bash -n ~/.ralph/ralph_loop.sh             # driver still parses
python3 -m py_compile analysis/ralph-loop-kaizen/pipeline/*.py analysis/ralph-loop-kaizen/practice-cards/*.py
python3 -c "import json;json.load(open('.claude/settings.local.json'))"  # settings valid
```

After the loop runs an active window post-fix, re-run `make ralph-metrics` and
confirm: failure rate ↓ from 71%, `usage_limit_429` share ↓, max-consecutive-
failures ↓ from 432, permission-denial loops ↓ from ~439.

**Revert path:** the driver backup is `~/.ralph/ralph_loop.sh.bak-kaizen-20260604`;
`.ralphrc`, `.claude/settings.local.json`, and `Makefile` changes are in the repo
git history.
