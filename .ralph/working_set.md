Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–72 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-65-72.txt,probe-q72-reprobe.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (banner cursor +
a "Chunk 65–72 DONE" progress entry).
No engine/harness code and no design doc changed — measurement only.

Key symbols: `reap_pg_orphans`, `engine_id`, `restart_goopg` (all in
`scripts/tpcds-bench-compare.sh`) — unchanged this loop.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 65-72 reprinted it — still ONE sweep under D4a. ~45 min, exit 0.
- **Q72 is the first set-A `OK` → HEAD `TIMEOUT`** (14 s/0 → 635 s, re-probed
  fresh at 636 s). Not server age: the re-probe followed the harness restart AND
  Q66/Q68 reproduce set A at the same age in this chunk.
- Reading (**hypothesis — no plan diff run**): the RC-1b fix `5db0a067`, which
  set A §2.1 predicted would touch Q72. Three family outcomes now: Q50 fixed
  0→6=PG, Q47 17→142 s still wrong, Q72 past budget. Q72's plan bottoms out in a
  4-table MHJ (warehouse/item/inventory/catalog_sales) with **no Filter on it**.
- **Q72's set-A row gap (0 vs 100) is now UNOBSERVABLE** — joins Q64 in the
  "unbounded AND unvalidatable" bucket. M0125 must validate Q72 on ROWS.
- Q65/67/69/71 reproduce set A timeouts; Q66 (5) / Q68 (100) match PG; Q70 is
  the known dsqgen ERROR, PG_SKIP by design — not a goopg defect.
- Timeout classification (D6), Q1–Q72: both-engine Q4; goopg-only unbounded
  Q5/Q10/Q14/Q30/Q31/Q54/Q64/**Q65/Q67/Q69/Q71/Q72**; goopg-only budget-marginal
  Q18/Q35/Q51; PG-only Q11; goopg ERROR Q8; not-a-goopg-error Q36/**Q70**.
- Row mismatches among OK queries Q1–Q72: Q47, Q49, Q51 (Q72's now masked).

NEXT LOOP — continue M0124-0001, **chunk `73-80`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` still unchanged since 2026-07-25, all 26 filed —
they are filed as brace-grouped entries, so grep each subject LOOSELY before
concluding anything is unfiled):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 73-80 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-73-80.txt 2>&1`
Foreground. Set A (`analysis/tpcds-sf1-goopg-20260727.md` §5.2, rows
`^| 7[3-9]|^| 80 `) shows only ONE timeout in range and it is **PG-side: Q74
(652 s; goopg OK 36 s)** — so `reap_pg_orphans` WILL fire here for the first time
in the sweep; budget ~11 min for it and do not read it as a goopg result. Every
other cell in range is OK on both engines (goopg 34–169 s), so ~25 min total.
Then append rows to `RESULTS.md`, update its Cursor, move the fix_plan banner.
No engine commit may land until the sweep reaches Q99; docs/tracker commits fine.

Gates run: one full harness chunk + one single-query re-probe (both exit 0,
headers verified against the sweep baseline engine-id); `make ralph-state-guard`;
pgbench smoke via the commit hook. No Go code touched, so no unit-suite run was
warranted.
In-flight: none.
