Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–96 DONE**. One chunk left.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-89-96.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (chunk-89–96
progress entry + new tasks **M0125-0007/-0008/-0009** and **M0124-0006**). No
engine/harness code and no design doc changed — measurement only.

Key symbols (READ this loop, NOT edited — the three new fix sites):
`parserExprKey` fallback `internal/planner/planner.go:7484` (`expr:%T`), its
caller `aggregateCallKey` `:6891`, dedup skip `:5844-5846`;
`time.Parse("2006-01-02",…)` `internal/executor/expr.go:2874`, sibling
`parseDateFields` `internal/pgnodes/datum.go:974`.

Findings:
- **Sweep baseline (every later chunk must reprint it unchanged):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
  Chunk 89-96 reprinted it — still ONE sweep under D4a. ~4 min, exit 0.
- All 8 cells `OK` on both engines, row counts reproduce set A, **no** new
  timeout/error/skip → every D6 list unchanged from Q1–Q88.
- By VALUE the worst chunk: **Q94/Q95 return `0/NULL/NULL`** vs PG
  `9|18130.71|-9444.12` and `57|85887.62|-27169.36`, at matching row count 1.
- Three defects root-caused by read-only probe, all filed, none fixed:
  **M0125-0007** unpadded date literal — PG takes `'2002-5-01'`, goopg's fixed Go
  layout doesn't; cast path ERRORs but comparison path silently matches 0 rows
  (Q16/Q94/Q95). **M0125-0008** SEMI+ANTI conjunction non-subset: EXISTS alone
  33/25=PG, NOT EXISTS alone 11/9=PG, both → goopg 25/18 vs PG 11/9 (a conjunct
  that GREW the result). **M0125-0009** `parserExprKey` fallback keys on the Go
  TYPE NAME, so all `sum(CASE …)` collapse to the first slot (`10435|10435` vs PG
  `10435|10436`); 17 expr types affected; same key feeds GROUP BY; THIRD
  recurrence (see `planner.go:6905-6909`, M0097-0032). Highest-value fix found.
- **Back-applied value diff to the whole sweep**: **Q16 wrong since chunk 2**
  (`OK / 1`, goopg 0 vs PG 45). 21 fresh OK/equal-count cells diverge by value,
  none ordering-only: Q2 Q7 Q16 Q21 Q26 Q27 Q28 Q39 Q40 Q43 Q46 Q50 Q59 Q62 Q66
  Q68 Q79 Q83 Q87 Q94 Q95. Attribution filed as **M0124-0006**.

NEXT LOOP — continue M0124-0001, **chunk `97-99` (FINAL)** (banner: M0124 →
M0125; M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them;
`ci/logs/action-items.md` unchanged since 2026-07-25, all 26 are already filed
as ID RANGES (`-008..-026` etc.), so a literal per-ID grep FALSE-NEGATIVES —
grep subjects/ranges loosely before concluding anything is unfiled):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 97-99 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-97-99.txt 2>&1`
Foreground. Size the Bash timeout from BOTH engine columns of set A
(`analysis/tpcds-sf1-goopg-20260727.md` §5.2, rows `^| 9[7-9] `). **Value-diff
every OK cell** — the on-disk `q97..q99` result files are STALE (set A) and were
excluded from the re-audit, so they are NOT evidence of agreement until re-run.
Then the sweep is COMPLETE: write the merged deliverable
`analysis/tpcds-sf1-goopg-20260728.md` (confirm/refute the 13 §13.3 projections)
and the engine-commit freeze LIFTS — M0125-0009 is the recommended first fix.

Gates run: one full harness chunk (exit 0, header verified against the sweep
baseline engine-id); read-only SQL probes against the running 65436/65438
clusters (no writes, baseline intact); `make ralph-state-guard` (self-repaired
a stale prev-loop `completed` marker, then OK); pgbench smoke via the commit
hook. No Go code touched, so no unit-suite run was warranted.
In-flight: none.
