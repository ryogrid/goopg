Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–56 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-49-56.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (banner cursor).
No engine/harness code and no design doc changed — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 49-56 reprinted it — still ONE sweep under D4a. ~29 min, exit 0.
- **The Q47 runtime-deviation class is CLOSED, empty.** Decisive cell was Q50,
  not Q47: **Q50 row gap 0 → 6 = PG**, so RC-1b fix `5db0a067` DID land and DID
  change plans in that family (set A had Q50 wrong + fix deferred). The ledger's
  `tpcds-round2 RC-1b` residual (a) already recorded it: Q47's CTE body is now
  correct (661185, was 0) and "14s->143s confirms real work". This sweep's
  17 s → 142 s is that same effect at SF=1 — cost of newly-correct input, NOT a
  regression. Chunk 41–48's "rows didn't move ⇒ not a newly-correct plan" was
  wrong; the final 0 is a separate DOWNSTREAM defect.
- Q49/Q51 did NOT inflate (79 s vs 82 s; 587 s vs 597 s) — the RC-1b effect is
  not uniform across the family, which is why the original decision rule
  ("if they inflate, blame 5db0a067") would have mis-answered.
- **Q51 did not flip** to TIMEOUT: `OK 587 s`, 13 s headroom → budget-marginal.
- Timeout classification (D6), Q1–Q56: both-engine Q4; goopg-only unbounded
  Q5/Q10/Q14/Q30/Q31/**Q54**; goopg-only budget-marginal Q18/Q35/**Q51**;
  PG-only Q11; goopg ERROR Q8; not-a-goopg-error Q36.
- Row mismatches among OK queries Q1–Q56: Q47, Q49, Q51 (set A also had Q50).

NEXT LOOP — continue M0124-0001, **chunk `57-64`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` still unchanged since 2026-07-25, all 26 filed —
note they are filed as brace-grouped entries, so grep each subject LOOSELY
before concluding anything is unfiled):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 57-64 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-57-64.txt 2>&1`
Foreground. Check set A (`analysis/tpcds-sf1-goopg-20260727.md` §5.2, table rows
`^| 5[7-9]|^| 6[0-4] `) for the timeout count in range FIRST and size the Bash
`timeout` accordingly (~10 min per timeout cell + sum of OK runtimes). Then
append rows to `RESULTS.md`, update its Cursor, move the fix_plan banner cursor.
No engine commit may land until the sweep reaches Q99; docs/tracker commits fine.

Gates run: one full harness chunk (exit 0, header verified against the sweep
baseline engine-id); `make ralph-state-guard`; pgbench smoke via the commit hook.
No Go code touched, so no unit-suite run was warranted.
In-flight: none.
