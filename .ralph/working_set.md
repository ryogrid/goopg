Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–32 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-25-28.txt,
chunk-29-32.txt}`, `.ralph/fix_plan.md` (chunk-4 note + banner cursor + the
task's NEXT line). No engine/harness code and no design doc changed this loop —
measurement only; the D6 sub-class the doc needed landed with chunk 3.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Both chunk-4 calls reprinted it — still ONE sweep under D4a.
- Chunk 4 was SPLIT (`25-28` + `29-32`) per the ≥2-set-A-timeouts rule; the
  estimator was right a third time (Q30, Q31 both timed out).
- All 8 cells reproduce set A; largest delta 5 s (Q27 239→234 s).
- **Q30/Q31 are the cleanest D6 goopg-only members yet** — PG answers both
  cheaply and exactly (13 s/63, 12 s/43); goopg has never completed either in
  any run of either sweep (649/647 s set A, 627/629 s here — all four are the
  harness cutting a still-running execution). **Unbounded above**, like
  Q5/Q10/Q14, NOT budget-marginal like Q18. Valid M0125 targets, and they carry
  a PG row count to validate a fix against.
- Running D6, Q1–Q32: both-engine Q4; goopg-only unbounded Q5/Q10/Q14/Q30/Q31;
  goopg-only budget-marginal Q18; PG-only Q11; goopg ERROR Q8.
- Both restarts reported the SAME post-restart image (`46632999aa3f5c75`) —
  the `vcs.revision` stamp moves with the build commit, not per restart.

NEXT LOOP — continue M0124-0001, **chunk `33-40`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them; the 26
`AI-20260725-*` items are filed BY SUBJECT, not by id, and nothing newer exists):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 33-40 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-33-40.txt 2>&1`
Foreground, Bash `timeout` 55 min. Set A rows 33–40 show only ONE goopg timeout
(Q35) → **no split needed**; est. ~30 min. **Predict Q35 as budget-marginal**:
set A `TIMEOUT 651 s` but the 2026-07-26 baseline had it `OK 525 s`, so an `OK`
here is a re-rolled coin, NOT a fix — classify it as the Q18 sub-class. Also
expect Q36 `ERROR 0 s` on goopg / `SKIP` on PG (known dsqgen artifact, fails on
PG too — not a goopg error). Then append rows to `RESULTS.md`, update its
Cursor, and move the fix_plan banner + task NEXT lines. No engine commit may
land until the sweep reaches Q99 — a docs/tracker commit is fine.

Gates run: two full harness chunks (both exit 0, headers verified against the
baseline engine-id); `make ralph-state-guard`; pgbench smoke via the commit hook.
No Go code touched, so no unit-suite run was warranted.
In-flight: none.
