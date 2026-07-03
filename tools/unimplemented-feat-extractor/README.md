# Unimplemented-Feature Extractor

Pipeline that mines `commit-log.json` (a JSON export of this repository's full
commit history) for **internal behaviors that were deferred and are still
unimplemented**, and writes them to `unimplemented_feat.json` at the repo root.

Built and first run on 2026-07-02/03 by a coding agent. This directory contains
the exact scripts, the intermediate data of that run, and this procedure doc so
a future agent can reproduce or refresh the result.

**Design constraint:** the commit log is ~2.7 M characters (≈ 700–900 k tokens),
far too large for the orchestrating agent to read. All bulk reading is done by
deterministic Python or cheap Haiku calls; the orchestrator only reads counts,
samples, and verification output.

## Output

`unimplemented_feat.json` (repo root, committed). Schema per item:

| field | meaning |
|---|---|
| `feature` | the missing internal behavior, 1–2 sentences |
| `task_id` | milestone/task id the gap belongs to (`M0110-0001`, `root-0022`, `DU-002`, …) or null |
| `deferred_date`, `first_deferred_commit` | when/where the gap was first recorded |
| `source_commits` | every commit whose message mentions this gap |
| `evidence` | verbatim phrases from the commit message proving the deferral |
| `resume_point` | file/function to resume at, when the commit recorded one |
| `resolution_check` | verdicts from the ledger / fix_plan / later-commit checks |
| `code_audit` | verdict from the per-item current-code audit, with `file:line` evidence |
| `confidence` | `high` (code-audit confirmed open) / `medium` (audit unclear) |

The complementary audit trail — every candidate that was **dropped as
implemented-later**, with evidence — is `data/resolved_dropped.json`.

## Deferral-expression vocabulary (discovery result)

A core intermediate result: the phrasings this project's commit messages use
to say "this was postponed / left unimplemented". `data/vocab.json` holds the
full machine-readable list used by the candidate filter.

- **Seed terms** (49, hand-listed from probing): `deferred`, `out of scope`,
  `follow-up`, `future loop/slice/milestone`, `not yet`, `unimplemented`,
  `unsupported`, `stub`, `no-op`, `parse-only`, `catalog-only`,
  `accepted-and-dropped`, `in-memory only`, `dump-fidelity only`,
  `silently ignored`, `blocked on`, `resume point`, `narrow fix`,
  `goopg does not`, `not wired`, `not persisted`, …
- **Discovered by Haiku** from a 54-commit stratified sample (44 phrases,
  many multi-word — the reason a fixed keyword list is not enough):
  `deferred (ledger):`, `remains deferred`, `stays defer`, `sql surface deferred`,
  `deferred with blockers`, `remaining blockers (ledgered)`,
  `remaining gaps documented in fix_plan`, `not ported`, `was not ported`,
  `t.skip`, `spec stays failed`, `tier 1 only`, `first slice`, `skeleton`,
  `empty btree placeholder`, `partial state`, `larger separate milestone`,
  `needs a prerequisite fix`, `hard-coded to null, dropping the optional`,
  `doesn't honour any of these semantically`, `silently swallowed`,
  `dump-fidelity only`, `emergency escape hatch`, `waits for a clean tree`,
  `tracked under`, `is tracked as`, `deeper fix`, `come next`, `(stretch)`, …

Work-unit id formats matched: `M\d{4}-\d{4}[a-z]?`, `root-\d{4}`,
`\d{4}-\d{4}[a-z]?` (design/spec ids), `[A-Z]{1,2}-\d{3}` (CSV row ids like
`DU-002`, `WD-001`), `slice \d+`, `loop #\d+`.

## Pipeline (9 phases)

```
commit-log.json (2,597 commits, 2.7M chars)
  │ phase1  prefilter: drop chore + empty descriptions      [python, 0 tokens]
  │ phase2  vocabulary discovery on stratified sample        [haiku ×9]
  │ phase3  regex candidates + ±2-line windows (2.7M→460k)  [python, 0 tokens]
  │ phase4  structured extraction → deferral items           [haiku ×103, resumable]
  │ phase5  merge dups; resolve vs ledger/fix_plan/timeline  [python + haiku adjudication]
  │ phase6  later-commit TITLE re-check + git-grep signal    [haiku ×~50]
  │ phase7  static code-line evidence pass                   [haiku ×~47]
  │ audit   per-item code audit: grep+read agents            [haiku Explore agents / claude -p]
  │ phase8  merge audit verdicts                             [python]
  └ phase9  deterministic rebuild (disaster recovery)        [python]
→ unimplemented_feat.json
```

Run order (from `scripts/`, after fixing `REPO` in `common.py` if the checkout
path differs):

```bash
python3 phase1_prefilter.py       # writes filtered.json next to the scripts
python3 phase2_discover.py        # writes vocab.json (merge of seeds + discovered)
python3 phase3_candidates.py      # writes candidates.json
python3 phase4_extract.py         # writes items_raw.json; resumable via phase4_done.json
python3 phase5_resolve.py         # writes unimplemented_feat.json v1 + resolved_dropped.json
python3 phase6_reverify.py        # drops items whose LATER COMMIT TITLES deliver the feature
python3 phase7_code_evidence.py   # drops items refuted by static grep lines (conservative)
# per-item code audit over all remaining items (see next section)
python3 phase8_merge_audit.py     # merges agent_verdict_*.json, final output
```

Haiku is called headlessly: `claude -p --model claude-haiku-4-5-20251001
--output-format json` (see `common.py::haiku_json`; strips ```json fences,
retries with backoff).

## The per-item code audit (the precision step — do not skip)

**Why it exists:** a 12-item hand audit of the phase-7 output found **7/12
false positives** — features recorded as deferred in April/May that later
commits implemented without resolution-style language ("feat: X" titles, no
"CLOSED/RESOLVED"). Message-only checking systematically over-reports on old
items. Static grep evidence was not enough either: judges without code access
stay too conservative in both directions.

**What works:** give a cheap tool-using agent (haiku) each batch of ~12 claims
and let it `grep` + `read` the current tree, with the rubric: *implemented only
if real logic exists — a TODO, stub, error-return, parse-only acceptance, or
catalog-recording-only does NOT count; judge the SPECIFIC claim, not the
feature area; when in doubt, open.* In the first run this dropped a further
94 items (87 by agents + 7 from the calibration sample).

Two interchangeable harnesses were used:
- Explore-type subagents (read-only), returning verdict JSON to the orchestrator,
  which persists them as `data/audit_verdicts/agent_verdict_NN.json`; or
- `scripts/run_audit_batches.sh`: headless `claude -p` with
  `--allowedTools "Read,Grep,Glob,Write" --permission-mode acceptEdits
  --add-dir <repo>` so each run **writes its own verdict file** (far less
  orchestrator context; preferred for >10 batches). Concurrency-capped at 2,
  one retry, idempotent (skips existing verdict files).

Batch inputs are `data/audit_batches/agent_batch_NN.json`. Keys are short
commit shas and **can repeat within a batch** (one commit, several gaps);
`phase8_merge_audit.py` matches verdicts by key + order of occurrence.

## Verification checklist (run after phase8)

1. `python3 -m json.tool unimplemented_feat.json` — valid JSON.
2. Known-open spot checks present (pick from the deferral ledger's open rows),
   e.g. window frame clauses, autovacuum, GiST/GIN AMs, SCRAM channel binding.
3. Known-implemented spot checks absent (pick from ledger resolved rows),
   e.g. pg_namespace, RETURNING, ILIKE, window functions.
4. Random-sample 10 items and hand-verify against the current code; if the
   false-positive rate is above ~10 %, re-run the code audit with a tighter
   rubric rather than shipping.

## Operational hazards hit during the first run (read before re-running)

- **Account usage limits:** a burst of 8 concurrent agents + 4-way `claude -p`
  tripped the 5-hour session limit; calls failed with empty stderr / a
  "session limit" message. Keep total concurrent LLM streams ≤ ~5, make every
  stage resumable (all phase scripts here are), and just re-run after reset.
- **Live Ralph loop on the same tree:** (a) a peer loop **commits whatever you
  have staged** — commit with an explicit pathspec (`git commit -m … -- <file>`)
  immediately after staging; (b) the pre-commit pgbench smoke takes ~2.5 min,
  during which the loop can move HEAD → `cannot lock ref` — retry; (c) the
  first `unimplemented_feat.json` written to the repo root **disappeared**
  between phases (cause never identified; loop logs showed only reads).
  `phase9_rebuild.py` reconstructs the final file deterministically from
  `items_raw.json` + `resolved_dropped.json` + verdict files — keep those safe.
- **Committed data:** `data/` files are the first run's state. `filtered.json`
  is not committed (3 MB, fully regenerable via phase1).

## Cost profile of the first run

| stage | calls | notes |
|---|---|---|
| phases 1,3,5,8,9 | 0 LLM | pure python |
| phase 2 | 9 haiku | ~60 k chars in |
| phase 4 | 103 haiku | 1,028 candidates, 10/batch, 464 k chars of windows |
| phase 5 adjudication | ~68 haiku | 407 ambiguous cases |
| phases 6+7 | ~97 haiku | titles + grep lines only |
| code audit | 22 agent runs | ~50–80 k tokens each (haiku) |
| orchestrator (frontier model) | — | read only counts/samples/verification |

Funnel: 2,597 commits → 1,028 candidates → 1,023 raw items → 893 merged →
**181 unimplemented** (711 dropped as resolved/implemented, all with evidence).
