# PG plan fixtures (A-02)

`plans-pg/` under `bench/tpch/` (22 files) and `bench/tpcds/` (99 files)
hold vanilla PostgreSQL 18.3 `EXPLAIN` output — the reference side of the
plan-parity diff (A-03). Nothing here is executed (`EXPLAIN` without
`ANALYZE`); multi-statement query files (TPC-DS Q14/23/24/39, TPC-H Q15)
have every statement `EXPLAIN`-prefixed so no second statement runs.

Provenance:

- TPC-H: split from the paired PG capture
  `analysis/leftdeep-joins/a01ii-cut3-paired.pg.plans.txt`
  (estimate-audit `--ref-port 65432`, db `tpch`, SF=1), one file per
  `=== QN` section.
- TPC-DS: captured per query against the reference cluster
  (`127.0.0.1:65438`, user `ryo`, db `tpcds05` = SF0.5, matching the
  goopg bench corpus) with the same EXPLAIN-prefix trick as the goopg
  sweep's plan channel (`sf05_capture_plans`).
- Q36/Q70/Q86 are `SKIP (oracle: SKIP_QUERYGEN)` — dsqgen artefacts that
  fail on PG too, mirroring the sweep.

Re-capture ONLY when the queries or the dataset change (new PG version,
new scale factor, regenerated `query*.sql`): re-run the two captures
above and diff the result against these fixtures before committing —
an unexpected move means the reference moved, not goopg.
