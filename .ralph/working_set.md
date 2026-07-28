Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: **MEASUREMENT COMPLETE — 99/99 queries, 13/13 chunks.** The sweep itself
is finished; what remains is reporting, not measuring.

Files: `analysis/tpcds-sf1-resweep-20260728/{RESULTS.md,chunk-97-99.txt}`,
`.ralph/deferral_ledger.md` (one new row), `.ralph/fix_plan.md` (chunk-13
progress entry; M0124-0006 cell list 21→23; M0125-0009 evidence/acceptance).
No engine/harness code and no design doc changed — measurement only.

Findings:
- **Sweep baseline held to the end:** `engine-id
  bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`, `TIMEOUT_SEC=600`,
  `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold. Chunk 97-99 reprinted
  it unchanged → all 99 queries are ONE sweep at ONE budget under D4a.
- All 3 cells `OK` both engines, counts reproduce set A (1/2531/90), timings in
  noise, **no** new timeout/error/skip → every D6 list CLOSES as of Q1–Q96.
- **Q97 + Q99 are WRONG ANSWERS** behind matching row counts = M0125-0009
  instances **4 and 5** (evidence now Q43/Q50/Q66/Q97/Q99). Q97
  `392155|392155|392155` vs PG `541140|286927|161` — its 3 columns are disjoint
  by construction, so equal values are *impossible*; sharpest acceptance case.
  Q99 cols 2–5 replicate col 1 (`1231|1231|1231|1231|1231` vs `1231|1228|1289|0|0`).
- **Q98 values CORRECT**; 5068-line raw diff = 2 answer-neutral renderers:
  (a) `char(n)` not blank-padded — `octet_length(sm_type)` 30 (PG) vs 7 (goopg),
  both `character(30)`; `length()` agrees on both and is a TRAP (bpcharlen
  ignores trailing blanks). Already ledger row 2026-07-06 M0122-0005 — gains
  first real-workload evidence, NOT re-filed. (b) numeric division loses scale
  **only when the quotient is exactly zero** (`0.00` vs `0.00000000000000000000`;
  non-zero byte-identical) — narrows the chunk-12 "no select_div_scale" framing.
- False alarm ruled out: Q99's `31-INTERVAL '60 days'` headers are identical in
  PG's output — they live in `query99.sql:7` (TPC-DS generator artifact).
- Value-divergence list: 21 → **23** (`+Q97 +Q99`); Q98 explicitly NOT a member.

NEXT LOOP — banner still M0124 → M0125 (M-NIGHTLY PARKED: keep FILING `## AI-`
items, do not select; `ci/logs/action-items.md` unchanged since 2026-07-25, all
26 already filed as ID RANGES `-008..-026` etc., so a per-ID grep FALSE-NEGATIVES
— grep loosely). **No chunk left to run.** Two items remain under M0124, and
`M0124-0006` is stated "due before the merged deliverable", so take it first:
attribute the 23 value-divergent cells from the **on-disk** result files
(`bench/tpcds/runtime_goopg/tpcds-results/{goopg,pg}_q<N>_result.txt`) — **do NOT
re-run the sweep**. Normalise per field (strip padding — see the char(n) gap
above) and sort before diffing, else bpchar width and zero-scale masquerade as
value divergence. Record a verdict per query in `RESULTS.md`. Then the merged
deliverable `analysis/tpcds-sf1-goopg-20260728.md` (confirm/refute the 13 §13.3
projections at SF=1 values; Q88 is TIMEOUT 660 s at SF=1, not SF0.5's 228 s).
The engine-commit freeze lifts when that deliverable lands; **M0125-0009 is the
recommended first fix** (one-line root cause, 5 queries of evidence).

Gates run: the final harness chunk (exit 0, header verified against the sweep
baseline engine-id); read-only SQL probes against the running 65436/65438
clusters (no writes, baseline intact); `make ralph-state-guard`; pgbench smoke
via the commit hook. No Go code touched, so no unit-suite run was warranted.
In-flight: none.
