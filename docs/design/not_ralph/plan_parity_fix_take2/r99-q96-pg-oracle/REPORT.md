# R99 result: the available data directory is not a native-PG oracle

R99 used PostgreSQL 18.3
`postgres/local_install/bin/postgres`, not the read-only `postgres/` source
tree. The source `/tmp/r97goopg/ds025` was stopped and had no native
`postgres` process. It was copied once to the new private destination
`/tmp/r99-pg18-q96-oracle`; their pre-start `global/pg_control` SHA-256 was
identical:

```
36a9b3b81874a6666ec3ea88bb632986ad5363dce9fc081ccd2b238d20be217c
```

No process used either path before the copy. Only the destination was started
(port 65439) and changed. The server was stopped at the end.

## What was reproducible

`ANALYZE VERBOSE public.store_sales, public.household_demographics,
public.store, public.time_dim` completed on the destination under PG18.3 with
`default_statistics_target=100`. It observed 719876, 7200, 12, and 86400 live
rows respectively. `pg_stats` then contained all ten Q96 predicate/join-column
rows (the raw catalog-only capture is `/tmp/r99-q96-stats.txt`): for example,
`ss_store_sk` has NDV 6 and its six MCVs; `hd_dep_count` has ten uniform MCVs;
and `t_hour`/`t_minute` have 24/60 MCVs. Two Q96 JSON EXPLAIN captures under
the specified 64 MiB/4-worker/leader-participation settings were byte
identical (`/tmp/r99-q96-plan-{1,2}.json`).

That plan is not the historical reference: it is
`Aggregate → Gather → Nested Loop → Nested Loop → Materialize`, not the
statistics-backed partial-aggregate/hash plan. This alone is an oracle-drift
finding, not a Goopg target.

## Native-PG incompatibility

The destination cannot serve as an execution oracle. A metadata query using
`pg_relation_size` failed because its SQL function body is the literal
`see system_functions.sql`. More decisively, Q96 value execution aborted PG
parallel workers at `nbtsearch.c:707` on
`_bt_check_natts(rel, key->heapkeyspace, page, offnum)`, signal 6; the
postmaster terminated workers and performed crash recovery. The journal names
the Q96 SELECT as the failed process.

Thus the copied Goopg data/catalog directory is not a compatible native PG18
data directory, even though some planner catalog reads and ANALYZE succeed.
It cannot establish PG relation/index metadata, Q96 values, or a plan oracle.
Do not use its post-ANALYZE plan, its stats, or any cost derived from them for
parity decisions.

## Ruling

**ORACLE INVALID.** R99 made no Goopg production change. A future comparison
requires a genuine native-PG18 SF0.25 dataset restored through PG-supported
tools and separately proven value-safe before it is used for plan/cost work.
The historical R95/R96 capture remains historical evidence only; R98's
UNOBSERVABLE ruling is unchanged.
