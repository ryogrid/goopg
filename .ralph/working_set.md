Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–40 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-33-40.txt}`,
`.ralph/fix_plan.md` (chunk-5 note + banner cursor + the task's NEXT line). No
engine/harness code and no design doc changed this loop — measurement only; the
D6 sub-classes the doc needed already landed with chunks 3 and 4.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 5 reprinted it — still ONE sweep under D4a.
- No split needed (one set-A timeout in range); ran in ~25 min, exit 0.
- All 8 cells reproduce set A; largest delta 5 s (Q37 311→316 s); every
  completed cell matches PG rows, incl. Q39's 236 [230+6].
- **Q35 = second budget-marginal member (with Q18), NOT unbounded.** Both
  sweeps cut it (651 s set A, 628 s here) but the 2026-07-26 baseline completed
  it at `OK 525 s` → a later `OK` is a re-rolled coin, not a fix. It does carry
  a PG row count (100), so a genuine fix is validatable on rows (Q18 is not).
- **Q36 is not a goopg defect** — dsqgen emits text PG rejects too
  (`PG_SKIP="36 70 86"`); it gets its own D6 bucket, not the goopg-ERROR one.
- Running D6, Q1–Q40: both-engine Q4; goopg-only unbounded Q5/Q10/Q14/Q30/Q31;
  goopg-only budget-marginal Q18/Q35; PG-only Q11; goopg ERROR Q8; not-a-goopg-
  error Q36.
- Restart after Q35 moved the image `46632999aa3f5c75`→`9a6a5c070ad7364d` with
  `engine-id` unmoved — third confirmation that a docs-only commit re-stamps
  `vcs.revision`; the binary sha is provenance, never the comparability key.

NEXT LOOP — continue M0124-0001, **chunk `41-48`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them; the 26
`AI-20260725-*` items are filed BY SUBJECT, and `ci/logs/action-items.md` is
still unchanged since 2026-07-25, so nothing newer exists):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 41-48 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-41-48.txt 2>&1`
Foreground. Set A shows NO timeout in 41–48 (slowest Q44 58 s) → one call,
est. ~5 min; a 20 min Bash `timeout` is ample. **Predict the known RC-1b row
gap at Q47** (goopg 0 rows vs PG 100) — already-known correctness delta, do NOT
file it as a new finding. Then append rows to `RESULTS.md`, update its Cursor,
and move the fix_plan banner + task NEXT lines. No engine commit may land until
the sweep reaches Q99 — a docs/tracker commit is fine.

Gates run: one full harness chunk (exit 0, header verified against the sweep
baseline engine-id); `make ralph-state-guard`; pgbench smoke via the commit
hook. No Go code touched, so no unit-suite run was warranted.
In-flight: none.
