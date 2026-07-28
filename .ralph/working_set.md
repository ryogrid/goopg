Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–24 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-17-24.txt}`,
`docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md` (D6 sub-class),
`docs/design/README.md` (index line), `.ralph/fix_plan.md` (progress + banner
cursor). No engine/harness code changed this loop — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  chunk-17-24 reprinted it — still ONE sweep under D4a.
- Q17/Q19–Q24 reproduce set A within 6 s. **Q18 flipped as predicted:** set A
  `OK 626 s/100`, now `TIMEOUT 627 s/0` — 1 s apart, same work, opposite side of
  the cut. Recorded as budget noise; NOT re-run.
- **D6 now has a budget-marginal sub-class** (landed in the design doc): a
  verdict flip on Q18 (or Q82, ~576 s) is a coin re-roll and must never be
  reported as a fix/regression; Q5/Q10/Q14 are unbounded-above and ARE signal.
- Running D6, Q1–Q24: both-engine Q4; goopg-only unbounded Q5/Q10/Q14;
  goopg-only budget-marginal Q18; PG-only Q11; goopg ERROR Q8.
- Binary image moved again on the post-Q18 restart (`01bb0f65…`→`22110d95…`)
  with `engine-id` unmoved — known `vcs.revision` stamp effect, not a change.

NEXT LOOP — continue M0124-0001, **chunk `25-32`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them; the 26
`AI-20260725-*` items are already filed and nothing newer exists as of this loop):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 25-32 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-25-32.txt 2>&1`
Foreground, Bash `timeout` 55 min. **Check set A first** (`analysis/
tpcds-sf1-goopg-20260727.md` §5.2, rows 25–32) and SPLIT into `25-28`/`29-32` if
it shows ≥2 timeouts in the range — that estimator has been right twice now.
Watch for another budget-marginal cell (any set-A `OK` above ~570 s).
Then append rows to `RESULTS.md` and move its cursor. No engine commit may land
until the sweep reaches Q99 — a docs/tracker commit is fine (engine-id unmoved).

Gates run: one full harness chunk (exit 0, header verified against the baseline
engine-id); `make ralph-state-guard` OK (self-repaired a stale progress marker);
pgbench smoke via the commit hook. No Go code touched, so no unit-suite run was
warranted.
In-flight: none.
