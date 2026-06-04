# 04 — Rules and task-conditional practice loading

This is the headline proposal. Today **all** guidance is delivered always-on:
`PROMPT.md` + `AGENT.md` + 25 memory files + the full milestone slice, loaded
every loop regardless of the task. That is expensive ([`01 §3`](01-current-state.md))
*and* low-signal — a WAL change loads TPC-H perf lore it will never use, while
the one Q12/Q13 regression gate that would have saved a multi-loop bisect is
buried in background prose.

The fix: a small **practice-card library** plus a cheap **router** that loads
only the cards relevant to the current task. This pulls **[TOKEN]** (less
context per loop) *and* **[WASTE]** (the right gate is present when it matters)
*and* **[TIME]** (less re-discovery).

## 4.1 Practice cards

A *practice card* is a short (≤ ~60 line) markdown file scoped to one task type,
containing only what changes how you work on that task: the must-run
verification gates, the known foot-guns, the sibling paths to keep in sync, and
the relevant memory/design-doc pointers. Cards live in
[`practice-cards/`](practice-cards/) (sample artifacts shipped here, not yet
wired into the harness).

Proposed initial set (derived directly from [`02`](02-pain-points.md) and the
memory files). The trigger column below is *descriptive*; the precise match
rules are in `route.py`, which is authoritative:

| card | loads when the task touches… | encodes |
|------|------------------------------|---------|
| `executor-planner-change.md` | `internal/planner`, `internal/executor` | **Q12/Q13 pre-commit gate**, sibling-path audit, silent-regression checklist |
| `codec-storage-change.md` | `internal/access`, `internal/storage`, encode/decode | encode/decode type-set + Datum-Kind + fixed-width agreement; re-run full suite |
| `wal-replication-change.md` | `internal/wal`, `internal/mvcc`, replication | race/concurrency coverage; recovery TAP; standby visibility |
| `tpch-perf.md` | TPC-H, benchmarking, pprof | isolation ports/paths, pprof env vars, memory-cap wrapper, cost-model traps |
| `server-test.md` | starting a goopg server, manual testing | `pkill -f` self-match, re-init data dir, `-listen` flag, cgroup cap |
| `regress-port.md` | oracle/TAP test porting | the CSV status workflow, deferred-suite unlock conditions |
| `catalog-ddl.md` | `internal/catalog`, DDL, `pg_*` views | view→constraint dependency tracking, functional-deps validation |

Each card is a distilled, *enforced-as-checklist* version of knowledge that
already exists as passive memory. Example: the executor/planner card turns
[`feedback_tpch_pre_commit_gates.md`] + [`m0071_stage_b_silent_regression.md`] +
[`pattern_sibling_paths_must_agree.md`] into a 3-item gate the loop runs before
committing — instead of hoping the model recalls the background lesson.

## 4.2 The router (cheap, deterministic)

A small classifier picks the cards for the current loop. **It must add ~zero
token/latency cost**, so the default is pure rules, not an LLM:

```
inputs (all cheap, local):
  - top open task text from fix_plan.md (milestone id + summary)
  - touched packages from `git diff --name-only` since the branch point
  - keywords in the task (wal, codec, tpch, regress, catalog, server …)
classify -> set of task-type tags
emit -> concatenated matching cards (capped, e.g. ≤ 2-3 cards / ~150 lines)
```

A reference classifier (`practice-cards/route.py`, shipped here) maps
package-path and keyword signals to card files. Ambiguous loops fall back to a
single general card; they never load *everything*.

**Wiring (proposed, not applied):** a `UserPromptSubmit` hook in
`.claude/settings.local.json` (which is editable — *not* a protected file, and
already hosts the Serena hooks) runs `route.py` and injects the selected cards
as `additionalContext`. This is the concrete, allowed injection point — the same
mechanism the Serena `SessionStart`/`Stop` hooks already use in this repo. The
ralph driver submits `PROMPT.md` each loop, so `UserPromptSubmit` fires per loop
— exactly the per-task granularity we want.

```jsonc
// proposed addition to .claude/settings.local.json (NOT applied here)
"hooks": {
  "UserPromptSubmit": [
    { "hooks": [ {
        "type": "command",
        "command": "python3 analysis/ralph-loop-kaizen/practice-cards/route.py"
    } ] }
  ]
}
```

`route.py` prints the selected card text on stdout; Claude Code injects a
`UserPromptSubmit` hook's stdout as additional context (this part is a confirmed
Claude Code feature).

> **Load-bearing caveat — verify before building.** The whole §4 design assumes
> `UserPromptSubmit` *fires* for ralph's invocation style: `claude < PROMPT.md`
> piped on stdin with `--continue`. Whether the hook fires for a piped-stdin
> headless `--continue` call (vs only interactive prompts) is **not confirmed**
> and is the single point of failure for the proposal. Verify it first. If it
> does **not** fire, fall back to a **`SessionStart` hook** — already proven to
> fire in this repo (it runs the Serena activate hook) — for per-session card
> loading at coarser granularity. Either way the wiring lives in the editable
> `.claude/settings.local.json`, never in a protected file.

## 4.3 Rule settings (non-card)

Process rules that are not task-specific but address [`02`](02-pain-points.md)
themes:

- **R1 — Verification gate before commit (executor/planner).** Make the Q12/Q13
  + row-count spot-check a *gate*, not advice. Best enforced as a `PreToolUse`
  hook on `git commit` that checks a freshness marker, or as a `make` target the
  card mandates. **[WASTE]**
- **R2 — Deferral ledger.** When a task closes at partial scope, require a
  one-line structured entry (`milestone, what landed, what deferred, resume
  point, why`) in a single `deferral-ledger.md`. The next loop loads only the
  relevant ledger lines — cheap carry-forward instead of re-reading a 700 KB
  completed-plan journal. Addresses Theme B churn. **[TIME]** **[WASTE]**
- **R3 — Scoped verification.** The card for a package change mandates running
  *that package's* tests first, broadening only on risk — directly attacks the
  Theme D timeouts where the full suite exceeds the loop budget. **[TIME]**
- **R4 — Status block is harness-enforced, not model-enforced.** Per
  [`03 H5`](03-recommendations-harness.md), validate/synthesize the status block
  in the `Stop` hook rather than relying on the model emitting it (28%
  compliance today). **[WASTE]**
- **R5 — Concurrency guard.** The `concurrent_ralph_loops_corrupt_tree` lesson
  becomes a `SessionStart` hook check (`pgrep` for a second loop on the same
  tree → warn/abort), not a memory the model might forget. **[WASTE]**

## 4.4 Migration path: memory → cards (incremental, low-risk)

1. Keep the memory system as-is initially (no regression risk).
2. Author the 7 cards from existing memory + milestone retrospectives (the
   Stage 4 lesson-mining output accelerates this).
3. Wire `route.py` via the `UserPromptSubmit` hook; **measure** context size and
   blocked-rate before/after over N loops.
4. Once cards prove out, demote the now-redundant always-on memory files to
   card-only loading, shrinking static context further.

This is deliberately reversible at each step — if a card hurts, unwire the hook;
nothing in the protected harness changed.

## 4.5 Why this beats "just add more to PROMPT.md"

PROMPT.md is already ~370 lines and only 28% of loops even comply with its
status rule — adding more *reduces* compliance and *raises* every loop's token
cost. Conditional loading does the opposite: each loop sees *less* total
guidance but *more* of the guidance that matters for its task.
