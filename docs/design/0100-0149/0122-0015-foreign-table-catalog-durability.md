# M0122-0015 foreign-table catalog durability

Status: **landed 2026-09-22**. Parent: M0122-0015. Predecessor: `8deb60881`
(the planner's no-handler refusal), whose ledger row filed this gap as the
blocker that made that refusal unreachable after a restart.

## The divergence

`CREATE FOREIGN TABLE` left nothing durable about the foreign-ness of the
relation. Measured on a throwaway goopg cluster before the fix, after a clean
stop/start:

- `pg_foreign_table` — 0 rows (before the restart: 1 row per foreign table)
- `pg_class.relkind` for the table — `'r'`, degraded from `'f'`
- `SELECT * FROM t0` — 0 rows, where PostgreSQL 18.3 and pre-restart goopg
  both raise `55000 foreign-data wrapper "dummy" has no handler`

The foreign SERVERS survived (B3.4 gave them a real `pg_foreign_server` heap
row); the table's association with them did not. A restarted cluster therefore
treated a foreign table as an ordinary empty heap relation — which is worse
than the original no-handler bug, because the data is not merely unreachable,
it is silently reported as absent.

Two causes, both on the pg_class round trip:

1. `buildUserPGClassRow` (`internal/executor/pg18_user_catalog_rows.go`) had
   no `'f'` arm, so the persisted heap row said `relkind='r'` while the
   VIRTUAL pg_class renderer in `internal/catalog/catalog.go` derived `'f'`
   from `Table.ForeignServerName`. The two renderings of the same relation
   disagreed — the failure mode `RelkindHasStorage` was introduced to prevent.
2. The reload filter in `internal/initdb/open.go` accepted `{r, m, v, S}`
   only, so even a correct `'f'` row would have been dropped on the floor.

And a third, structural: `pg_foreign_table` is rendered virtually from
`Table.ForeignServerName` (`PGForeignTableRowsForDBOid`), and that field had
no heap representation at all. Restoring `relkind` alone would have produced a
relation that claims to be foreign with no server to resolve.

## What landed

A real `pg_foreign_table` heap row (OID 3118) plus its reload, modelled on the
B3.4 foreign-data trio the same file already implements:

- `writeForeignTableCatalogRow` (`internal/executor/sys_pg_foreign.go`) writes
  `(ftrelid, ftserver, ftoptions)` with `ftserver` resolved to the server's
  OID at write time, and inserts the `pg_foreign_table_relid_index` (3119)
  entry. `PGForeignTableColumnsPG18` mirrors `FormData_pg_foreign_table`
  (`postgres/src/include/catalog/pg_foreign_table.h`); the catalog has no
  `oid` system column, `ftrelid` IS the key.
- It is called from `syncTableToCatalogHeap`, the single funnel every
  table-persisting DDL path passes through, so the ALTER paths re-stamp and
  re-write it alongside pg_class. It is a no-op for an ordinary relation.
- `stampForeignTableRows` is called from `deleteCatalogRowsForOID`, so a DROP
  or an ALTER re-sync leaves no stale `ftrelid` row — verified: after
  `DROP FOREIGN TABLE t1` and a restart, `pg_foreign_table` holds only `t0`.
- `reloadForeignTablesFromHeap`
  (`internal/initdb/catalog_heap_reload.go`) reverses `ftserver` through the
  server registry and re-attaches `ForeignServerName`/`ForeignOptions` to the
  loaded table. It runs LAST in `reloadForeignDataFromHeap`, which is itself
  well after `loadUserTablesFromHeapForDB` — both orderings are load-bearing.
- `buildUserPGClassRow` gains the `'f'` arm; the reload filter accepts `'f'`.
- 3119 is registered in `keyMetaForSysBtree`
  (`internal/executor/sys_catalog_btree_split.go`). This was NOT foresight:
  `TestEverySysBtreeInsertPathIndexHasSplitKeyMeta` caught the omission, which
  would otherwise have worked until the leaf-root filled and then failed the
  split.

## Measured

On a throwaway cluster (`/tmp/ftd`, port 5533), after a clean stop/start:

```
ftrelid | ftserver |         ftoptions
--------+----------+----------------------------
  16408 |    16407 |
  16409 |    16407 | {"delimiter=,","quote=\""}

 relname | relkind      select * from t0;
---------+---------     ERROR:  foreign-data wrapper "dummy" has no handler
 t0      | f
 t1      | f
```

`TestPort_PgDump003ForeignDataNoHandler` now stops and starts the cluster
between the DDL and the two `pg_dump` runs, and asserts the catalog state
across it. Upstream has no restart there; goopg needs one to be honest,
because without it the test would keep passing while the only state a real
cluster ever has — post-recovery state — was broken. Non-vacuity checked:
removing the `'f'` arm from the reload filter fails exactly that assertion
(`post-restart pg_foreign_table count = "0", want 2`).

## Deferred

The `pg_foreign_table` heap rows are written to `DefaultDBOid`'s heap and
mirrored to the postgres database, like every other foreign-data catalog since
B3.4 — they are not per-database, so a foreign table created inside a
`CREATE DATABASE` database reloads into the default namespace. That is
inherited scope, not new; ledgered with a resume point.

`CREATE FOREIGN TABLE` still returns the command tag `CREATE TABLE`
(PostgreSQL returns `CREATE FOREIGN TABLE`); the DROP tag is already correct.
Ledgered previously, still open.

## Gates

`internal/initdb`, `internal/catalog`, `internal/executor` unit tests;
`TestPort_PgDump003*`; the full `TestPort_RegressSuite`, whose failing set is
unchanged from HEAD (only `partition_aggregate`, which has its own `[!]` row);
`tpch-spotcheck` PASS (Q12=2, Q13=33); `tpcds-sf025` sweep
`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` with
`PLAN-SHAPE same=99 changed=0`; pgbench smoke.

PostgreSQL reference: `postgres/src/include/catalog/pg_foreign_table.h`,
`GetFdwRoutineByServerId` (`postgres/src/backend/foreign/foreign.c:403`).
