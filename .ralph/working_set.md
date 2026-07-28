Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–88 DONE**.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-81-88.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (chunk-81–88
progress entry + new task **M0125-0006**). No engine/harness code and no design
doc changed — measurement only.

Key symbols (READ this loop, NOT edited — M0125-0006's fix sites):
`parseParenthesisedSelectStmt` `internal/parser/select.go:1005` + `:1007-1039`;
flattening loop `internal/planner/planner.go:686-701`, break at `:696-698`;
`SelectStmt.Parenthesized` `internal/parser/ast.go:861-867`.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 81-88 reprinted it — still ONE sweep under D4a. ~40 min, exit 0.
- All 8 cells reproduce set A in class AND row count. **By row count the chunk is
  uneventful; by value it is not.** Acting on chunk 10's Q75 lesson I diffed
  result VALUES vs PG for every OK cell (first time in the sweep) and caught
  **Q87: 1 row both engines, goopg 47218 vs PG 47049.**
- Q87 root-caused by read-only probe: branches match (47428/31680/11744),
  `A except B` matches (47117), but goopg's 3-way (47218) EXCEEDS its own 2-way —
  impossible left-associatively — and equals PG's right-associated reading.
  **Trigger = per-branch parenthesisation**, not subquery context: bare
  `A except B except C` correct; `(A) except (B) except (C)` wrong; `except all`
  wrong; mixed `(A) union (B) except (C)` wrong ({1,2,3} vs {1,2}).
  UNION/INTERSECT-only chains unaffected only because they are associative.
  Filed **M0125-0006**; NOT fixed (no engine commit before Q99).
- Answer-neutral gaps from the same diff: **Q83** numeric-division result scale
  (`0.0` vs PG `0.00000000000000000000`, no `select_div_scale`); **Q82** 1-char
  column-width delta (likely trimmed trailing space).
- **Q82 is budget-marginal**: passed at 556 s with only 44 s headroom — narrowest
  OK margin of the sweep.
- D6, Q1–Q88: both-engine Q4; goopg-only unbounded
  Q5/Q10/Q14/Q30/Q31/Q54/Q64/Q65/Q67/Q69/Q71/Q72/Q78/**Q81**/**Q88**;
  budget-marginal Q18/Q35/Q51/**Q82**; PG-only Q11/Q74; goopg ERROR Q8/Q75;
  not-a-goopg-error Q36/Q70/**Q86**. Answer mismatches among OK queries: Q47,
  Q49, Q51 by row count **plus Q87 by value at a matching count**.

NEXT LOOP — continue M0124-0001, **chunk `89-96`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` unchanged since 2026-07-25, all 26 are
`AI-20260725-011243-001..-026` and all already filed, so grep subjects LOOSELY
before concluding anything is unfiled):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 89-96 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-89-96.txt 2>&1`
Foreground (or background + same-turn `tail --pid`). **Size the Bash timeout from
BOTH engine columns of set A** (`analysis/tpcds-sf1-goopg-20260727.md` §5.2, rows
`^| 89 ` and `^| 9[0-6] `; col 1 = goopg, col 2 = PG); budget ~11 min per timeout
cell plus the goopg OK times. **Then VALUE-DIFF every OK cell** —
`diff bench/tpcds/runtime_goopg/tpcds-results/{goopg,pg}_q<N>_result.txt`,
normalising whitespace to separate psql rendering from real divergence; this is
now part of the per-chunk procedure. Append rows to `RESULTS.md`, update its
Cursor, move the fix_plan entry. No engine commit may land until Q99;
docs/tracker commits fine.

Gates run: one full harness chunk (exit 0, header verified against the sweep
baseline engine-id); read-only SQL probes against the running 65436/65438
clusters (no writes, baseline intact); `make ralph-state-guard`; pgbench smoke
via the commit hook. No Go code touched, so no unit-suite run was warranted.
In-flight: none.
