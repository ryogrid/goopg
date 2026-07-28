Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–80 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-73-80.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (chunk-73–80
progress entry). No engine/harness code and no design doc changed — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 73-80 reprinted it — still ONE sweep under D4a. ~35 min, exit 0.
- **Q75 is the first set-A `OK` → HEAD `ERROR`** (47 s/100 → `division by zero`,
  query75.sql:67, 66 s). Server survives (Q76 ran next) = Q8 shape. This is the
  **predicted** M0125-0004 / ledger `tpcds-round2 Q75-eval-order` outcome, so the
  chunk promotes §13.3's projection to a MEASUREMENT at SF=1.
- **Do not read set A's Q75 `100` as a pass** — the ledger proves its CTE computed
  1,057,469 vs PG 2,368,670; 100 garbage rows matching only on COUNT under
  `LIMIT 100`. Concrete justification for M0124-0005's value checksum.
- RC-1b `5db0a067` now has **four** family outcomes: Q50 fixed 0→6=PG, Q47
  17→142 s still wrong, Q72 past budget, Q75 contained error.
- Q74 reproduces its **PG-side** TIMEOUT (goopg OK 34 s/100) — 2nd PG-only after
  Q11; `reap_pg_orphans` fired here (1 terminated). Q78 reproduces its goopg-only
  TIMEOUT. Q73/76/77/79/80 all = PG within ±5 s.
- Repaired chunk-9 gap: `RESULTS.md`'s table stopped at Q64; rows 65–72 backfilled
  from `chunk-65-72.txt`, no figure changed.
- D6, Q1–Q80: both-engine Q4; goopg-only unbounded
  Q5/Q10/Q14/Q30/Q31/Q54/Q64/Q65/Q67/Q69/Q71/Q72/**Q78**; budget-marginal
  Q18/Q35/Q51; PG-only Q11/**Q74**; goopg ERROR Q8/**Q75**; not-a-goopg-error
  Q36/Q70. Row mismatches among OK queries: Q47, Q49, Q51.

NEXT LOOP — continue M0124-0001, **chunk `81-88`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` unchanged since 2026-07-25, all 26 filed as
brace-grouped entries, so grep each subject LOOSELY before concluding anything
is unfiled):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 81-88 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-81-88.txt 2>&1`
Foreground. **Size the Bash timeout from BOTH engine columns of set A**
(`analysis/tpcds-sf1-goopg-20260727.md` §5.2, rows `^| 8[1-8] `; col 1 = goopg,
col 2 = PG) — this loop's inbound baton undercounted 73–80 by reading only the
goopg side and missed Q78's ~11-minute goopg timeout. Budget ~11 min per timeout
cell plus the goopg OK times, then append rows to `RESULTS.md`, update its Cursor,
move the fix_plan entry. No engine commit may land until the sweep reaches Q99;
docs/tracker commits fine.

Gates run: one full harness chunk (exit 0, header verified against the sweep
baseline engine-id); `make ralph-state-guard`; pgbench smoke via the commit hook.
No Go code touched, so no unit-suite run was warranted.
In-flight: none.
