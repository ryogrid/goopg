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
- Wall-clock totals: **goopg 170.2s vs PG 185.1s = 0.9x** — aggregate
  parity ballpark. Caveats: goopg side is psql wall-clock (connect +
  plan + execute), PG side is oracle secs; S-cold first runs included
  in medians via 3-run median; single configuration (GOGC=100,
  GOMEMLIMIT=12GiB, 128MB pool default on the clone).
- Worst relative slowdowns (goopg/PG): Q61 70x (1.40/0.020),
  Q58 26x, Q55 23x, Q88 21x (7.29/0.34), Q24 21x, Q80 20x,
  Q32/Q43/Q71 ~19x, Q8 18x. These are the executor-side cliffs worth
  investigation independent of plan parity — with the R70 width gap
  (35x row widths → hash/sort memory) as prime suspect for the
  hash/sort-heavy ones.
- Q96 (the tracking query, PG-shaped parallel plan): goopg
  0.22–0.30s vs PG 0.044s (~5x).

## Reading guidance

Same-shape timing compares execution engines; different-shape timing
conflates planner + executor. Per-query shape verdicts were not
re-taken in this survey (PG reference down during the run); join the
per-query medians here against the next fresh census before blaming
any single query's gap on the executor. Raw medians: timing artefact
(not committed — rerun `time-sweep.sh` to reproduce).
