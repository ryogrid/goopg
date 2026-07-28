Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–64 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-57-64.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (banner cursor +
one consolidated "Chunks 41–64 DONE" progress entry).
No engine/harness code and no design doc changed — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 57-64 reprinted it — still ONE sweep under D4a. ~18 min, exit 0.
- **First fully uneventful chunk.** All seven OK cells match PG rows exactly
  (Q57 100, Q58 0, Q59 100, Q60 100, Q61 1, Q62 100, Q63 100) and reproduce
  set A within ±3 s (Q60 exact). No new row gap, no new runtime deviation.
- **Q58's 0 rows is NOT a gap — PG returns 0 too.** Do not read a bare "goopg
  returned 0" as wrong without checking PG's own count (that error mis-framed
  Q47 in chunk 41–48).
- **Q64** reproduces set A's goopg-only TIMEOUT (632 s vs 654 s). Unlike Q54,
  **PG returns 8 rows in 0 s**, so Q64 may ALSO carry a row gap that no run has
  ever been able to observe — new ledger row records that D6 cannot express
  "timed out AND answer unknown". Probe only after Q99.
- Timeout classification (D6), Q1–Q64: both-engine Q4; goopg-only unbounded
  Q5/Q10/Q14/Q30/Q31/Q54/**Q64**; goopg-only budget-marginal Q18/Q35/Q51;
  PG-only Q11; goopg ERROR Q8; not-a-goopg-error Q36.
- Row mismatches among OK queries Q1–Q64: Q47, Q49, Q51 (unchanged).

NEXT LOOP — continue M0124-0001, **chunk `65-72`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` still unchanged since 2026-07-25, all 26 filed —
they are filed as brace-grouped entries, so grep each subject LOOSELY before
concluding anything is unfiled):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 65-72 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-65-72.txt 2>&1`
Foreground. Check set A (`analysis/tpcds-sf1-goopg-20260727.md` §5.2, table rows
`^| 6[5-9]|^| 7[0-2] `) for the timeout count in range FIRST and size the Bash
`timeout` accordingly (~10 min per timeout cell + sum of OK runtimes). **`PG_SKIP`
covers Q70 in this range** — that cell has no PG arm by design, not a failure.
Then append rows to `RESULTS.md`, update its Cursor, move the fix_plan banner.
No engine commit may land until the sweep reaches Q99; docs/tracker commits fine.

Gates run: one full harness chunk (exit 0, header verified against the sweep
baseline engine-id); `make ralph-state-guard`; pgbench smoke via the commit hook.
No Go code touched, so no unit-suite run was warranted.
In-flight: none.
