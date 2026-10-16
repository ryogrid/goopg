# M0146-0214 slice 2 — Q64's GEQO walk is decided by physical inputs

- `goopg-sf025-pg_class.txt`, `pg-sf025-pg_class.txt`: relpages / relallvisible / reltuples for Q64's relations.
  goopg is a private clone of `bench/tpcds/runtime_goopg/data-sf025`; PG is `:65438 tpcds025`, read with SELECT
  only. Heap relpages and reltuples are identical. relallvisible differs on every table, because goopg is fully
  all-visible. Index pages differ: `store_returns_pkey` 277 vs 280, `store_sales_pkey` 2481 vs 2569.
- `q64-pg-scratch-refstats.plan.txt`: PG 18.3 (the `tmp/pgdbg` build) on a scratch cluster restored from
  `pg_dump --statistics` of `:65438 tpcds025`. It has the reference's exact column and relation statistics, but
  freshly CREATE INDEX-built indexes (`store_returns_pkey` 198 pages, `store_sales_pkey` 1976). PG picks a THIRD
  Q64 plan: cost 14022.76, against the reference's 13874.27 and goopg's 13866.65.
- Why: `plancat.c` reads index pages (`RelationGetNumberOfBlocks`, :475), tree height (:493) and heap curpages
  (:1097) from the physical relations; only allvisfrac comes from `relallvisible` (:1112-1169). GEQO's 250
  generations amplify sub-percent cost deltas into different tours.
