Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–48 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-41-48.txt,
diag-q47-rerun.txt}`, `.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md`
(banner cursor). No engine/harness code and no design doc changed — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 41-48 reprinted it — still ONE sweep under D4a.
- No timeout, no restart, no reap in range; ~6 min, exit 0.
- 7/8 cells reproduce set A within ±2 s with rows = PG.
- **Q47 is the first RUNTIME deviation in Q1–Q48: `OK 17 s` → `OK 142 s` (8.4×),
  rows unmoved at 0 vs PG 100.** Reproduced at 143 s (`diag-q47-rerun.txt`).
  NOT GC/server age: Q44/Q46/Q48 on the same server at the same age match set A.
  Cause bounded to the 10 engine commits after set A's base (`ee86594e` +
  `b3493a6e`,`21301982`,`9740fce9`); prime suspect `5db0a067` (RC-1b MHJ
  filter-push) — post-dates set A, touches Q47's own family, and did NOT fix the
  row gap. Deliberately NOT root-caused (measurement task); ledger row appended.
- Timeout classification (D6) unchanged by this chunk: both-engine Q4;
  goopg-only unbounded Q5/Q10/Q14/Q30/Q31; goopg-only budget-marginal Q18/Q35;
  PG-only Q11; goopg ERROR Q8; not-a-goopg-error Q36. New separate
  **runtime-deviation** class: Q47.

NEXT LOOP — continue M0124-0001, **chunk `49-56`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` still unchanged since 2026-07-25, all 26 filed by
subject, so nothing newer exists):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 49-56 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-49-56.txt 2>&1`
Foreground. Set A shows ONE goopg timeout in 49–56 (Q54) → est. ~35 min; use a
55 min Bash `timeout`. **This chunk decides the Q47 question**: Q49/Q50/Q51 are
the other RC-1b members (set A 82 s / 15 s / 597 s). If they inflate like Q47 the
cause is `5db0a067`; if not, diff `goopg_q47_explain.txt` vs set A. **Q51 is
budget-marginal already at `OK 597 s`** — any inflation flips it to TIMEOUT, do
NOT score that as a new regression class without this context. Predict known
RC-1b row gaps at Q49 (30 vs 34) and Q50 (0 vs 6) — do not file as new. Then
append rows to `RESULTS.md`, update its Cursor, move the fix_plan banner. No
engine commit may land until the sweep reaches Q99; docs/tracker commits are fine.

Gates run: one full harness chunk + one diagnostic re-run (both exit 0, headers
verified against the sweep baseline engine-id); `make ralph-state-guard`; pgbench
smoke via the commit hook. No Go code touched, so no unit-suite run was warranted.
In-flight: none.
