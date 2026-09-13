# Timing survey: TPC-DS SF0.25 goopg vs PG (all 96 plannable queries)

Question: how long does goopg take per query today (HEAD `254ab5c`),
against PG 18.3 on the same dataset? Method: private SF0.25 clone,
3 timed client runs per query (median), values checksum vs the PG
oracle on every query; PG secs from the oracle fixture itself
(`tpcds-results-sf025/oracle.txt`, plain-run secs). Harness (scratch,
not committed): `time-sweep.sh` equivalent — median of 3, TIMEOUT
300s/run. No repo code changed.

## Result

- Values: **96/96 PASS** (rows + checksums vs oracle, zero mismatches).
- Wall-clock totals: **goopg 170.2s vs PG 185.1s = 0.9x** — but this
  is NOT a speed-superiority claim. Per-query distribution is strongly
  PG-leaning: **88 queries PG-faster, 8 goopg-faster**. The near-parity
  total is arithmetic (a few heavy PG-side queries — Q74 53.5s, Q4
  35.7s, Q6 33.4s, Q1 15.2s — dominate PG's sum; those PG numbers
  themselves smell of cold-cache capture effects). Typical queries run
  10–20x slower on goopg. Caveats: goopg side is psql wall-clock
  (connect + plan + execute), PG side is oracle secs; S-cold first
  runs included in medians via 3-run median; single configuration
  (GOGC=100, GOMEMLIMIT=12GiB, 128MB pool default on the clone).
- Worst relative slowdowns (goopg/PG): Q61 70x (1.40/0.020),
  Q58 26x, Q55 23x, Q88 21x (7.29/0.34), Q24 21x, Q80 20x,
  Q32/Q43/Q71 ~19x, Q8 18x. These are the executor-side cliffs worth
  investigation independent of plan parity — with the R70 width gap
  (35x row widths → hash/sort memory) as prime suspect for the
  hash/sort-heavy ones.
- Q96 (the tracking query, PG-shaped parallel plan): goopg
  0.22–0.30s vs PG 0.044s (~5x).
- Queries where goopg was faster (8) — PG-side pathology, proven by
  fair rematch (all 96 re-timed on live `:65438` with
  `work_mem='512MB'` pinned, 3-run medians, values re-verified):
  Q1 0.24s vs 15.03s, Q74 1.75s vs 53.91s, Q6 3.51s vs 35.53s,
  Q81 0.46s vs 3.85s, Q30 0.16s vs 0.81s, Q4 7.02s vs 31.87s,
  Q11 3.55s vs 7.59s, Q54 1.59s vs 2.50s —
  the gaps do NOT collapse. CORRECTION of the earlier work_mem
  entry: the 4MB-vs-512MB asymmetry is real
  (`bench/tpcds/runtime/pgdata/postgresql.conf` leaves work_mem
  unset; goopg default 512MB; oracle secs unpinned) but it is NOT
  the cause — pinned PG stays slow, reproducibly warm (Q1 14.0s,
  Q74 53.9s re-runs). PG's own plans are pathologically slow on this
  sampled data (e.g. Q74: parallel nested-loop with per-row
  `customer_pkey` probes under a CTE Append). No unfair bias on the
  goopg side either (client wall-clock includes connect+plan, i.e.
  against goopg; 96/96 checksums rule out shortcut results).
  Fair-rematch totals: goopg 170.2s vs PG(512MB) 182.4s = 0.93x,
  with 9 queries goopg-faster — same reading guidance as above
  (aggregate, not superiority).

## SET-application audit (2026-09-13, on the rematch harness)

Q: did every rematch query actually run with work_mem=512MB (SET
is session-scoped)? A: yes by construction, proven live —
`psql ... -c "SET work_mem='512MB'" -f query` shares one session
(psql runs -c/-f in command-line order) and the identical string
returns `SHOW work_mem = 512MB` against live `:65438`. Documented
failure mode: a misspelled SET prints ERROR yet continues at 4MB —
not the case here (string proven valid), but the harness keeps no
per-query SET receipt (`$res` deleted per query); future runs should
retain one or prepend a SHOW guard.
Stronger still: the SET demonstrably had nothing to fix — zero of
96 queries moved >50% vs the unpinned oracle, and Q74's
`EXPLAIN (ANALYZE, BUFFERS)` at 4MB shows in-memory quicksort
(3MB) with no temp files and 60.2s execution. The 8 goopg-faster
cases are therefore genuine PG-plan pathology on this data, not a
measurement artefact of a missed SET.

## Reading guidance

Same-shape timing compares execution engines; different-shape timing
conflates planner + executor. Per-query shape verdicts were not
re-taken in this survey (PG reference down during the run); join the
per-query medians here against the next fresh census before blaming
any single query's gap on the executor. Raw medians: timing artefact
(not committed — rerun `time-sweep.sh` to reproduce).
