Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–16 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-9-12.txt,
chunk-13-16.txt}` (this loop), `.ralph/fix_plan.md` (progress + banner cursor).
No engine/harness code changed this loop — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunks 9-12 and 13-16 both reprinted it — they are ONE sweep under D4a.
- Q9–Q16 reproduce set A cell-for-cell (largest delta 25 s, on the 600 s-budget
  timeouts where the excess is teardown). Q11 is the notable cell: **PG** times
  out, goopg finishes in 79 s/95 rows.
- Running D6 timeout class, Q1–Q16: both-engine Q4; goopg-only Q5/Q10/Q14;
  PG-only Q11; goopg ERROR Q8 (server survives).
- Splitting a 16-query range into two harness calls is safe and is the right
  default: set A's own numbers are the estimator, and the 8-query chunk size in
  the fix_plan note assumes ≤2 timeouts.

NEXT LOOP — continue M0124-0001, **chunk `17-24`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them; the 26
`AI-20260725-*` items are already filed and nothing newer exists):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 17-24 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-17-24.txt 2>&1`
Foreground, Bash `timeout` 55 min. Set A has NO timeout in this range and sums
to ~21 min goopg + ~12 s PG, so one call suffices. **Watch Q18:** set A is
`OK 626 s` against a 600 s budget (elapsed includes the ≤30 s EXPLAIN capture),
so it sits right on the edge and may flip to TIMEOUT — that is budget noise, not
a regression; record it as such rather than re-running.
Then append rows to `RESULTS.md` and move its cursor. No engine commit may land
until the sweep reaches Q99 — a docs/tracker commit is fine (engine-id unmoved).

Gates run: two full harness chunks (exit 0 each, headers verified against the
baseline engine-id); `make ralph-state-guard` OK; pgbench smoke via the commit
hook. No Go code touched, so no unit-suite run was warranted.
In-flight: none.
