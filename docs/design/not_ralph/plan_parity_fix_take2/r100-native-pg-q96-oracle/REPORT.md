# R100 result: validated native PG18.3 Q96 oracle

R100 copied the verified-stopped native reference source
`bench/tpcds/runtime/pgdata` once to the disposable
`/tmp/r100-pg18-q96-oracle`. The source was not started or modified. Before
start, both `global/pg_control` files had SHA-256
`8cd2d7c54714d69bba4099bcec5f7631470ba3c4939fa30224f0b7b88f2c1386`;
the destination reported PG18 system identifier `7666072657399316226` and a
stopped cluster state. No native PG process used either path before the copy.

The destination alone ran PG18.3 on port 65440, database `tpcds025`, schema
`public`. Q96 was the tracked file with SHA-256
`76d9523e309ae62fcc54972aa1edf6026548ae86e2dd983be4ee2481fdb846c3`.
The planned session set `work_mem=64MB`,
`max_parallel_workers_per_gather=4`, and
`parallel_leader_participation=on`.

## Acceptance evidence

Two JSON EXPLAIN captures were byte-identical. Their node spine exactly
matches `bench/tpcds/plans-pg/Q96.txt`:

```
Limit → Sort → Finalize Aggregate → Gather(3) → Partial Aggregate
      → Nested Loop(time_dim_pkey)
      → Hash Join(store_sales × store)
      → Hash Join(store_sales × household_demographics)
```

The value query completed successfully with **266**, matching the tracked
historical PG result. There was no assertion, recovery, or server error.

The source's pre-existing SF0.25 identity is confirmed by relation metadata:
`store_sales=719876` tuples/12936 pages,
`household_demographics=7200`/53, `store=12`/1, and
`time_dim=86400`/1408. Q96's ten predicate/join-column `pg_stats` rows and
the four relevant indexes were captured from the destination; examples are
`ss_store_sk` NDV 6, `hd_dep_count` ten uniform values, and `t_hour`/`t_minute`
NDV 24/60. The full capture is retained under `/tmp/r100-q96-metadata.txt`.

Existing statistics already reproduce the historical plan, so R100 did not
run `ANALYZE` and made no mutation after starting the disposable copy. The
server was stopped. This is the valid native-PG oracle for later Q96 cost and
selectivity attribution; it supersedes only R99's invalid Goopg-data copy,
not any historical evidence.
