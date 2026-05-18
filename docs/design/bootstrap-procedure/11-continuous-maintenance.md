# 11 — Continuous Maintenance: Per-Operation Artefact Matrix

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility) — cross-cutting
**Audience:** Claude Code wiring runtime mutators in
`internal/{executor,catalog,wal,checkpointer,replication,server}/`.

---

## Scope

This file is the **cross-cutting** companion to docs 01–09: it is
keyed by *operation* (every command or runtime event that mutates a
PG-readable artefact) rather than by artefact. For each operation it
names the upstream entry point, the artefact set touched (cross-
referencing the per-artefact doc), the WAL records emitted, the
cache-invalidation messages, and the goopg implementation status with
a responsible file under `internal/`.

A vanilla PG18 standby may attach at any moment in the goopg primary's
lifetime; every artefact a standby reads on attach must remain
PG-canonical between **every** commit, not only immediately after
`initdb`. This file is therefore the audit checklist: each row below
must be implemented before that operation can be issued against a
goopg primary that is — or is about to be — paired with a vanilla PG
standby.

The summary matrix at the end of the document is the centrepiece;
per-operation prose is intentionally terse.

## Upstream references

This file does not re-derive citations; it routes them through the
artefact docs:

- Directory and flat-file mutations: [`01-data-directory-layout.md`](01-data-directory-layout.md).
- `pg_control` rewrites and `UpdateControlFile` callers: [`02-pg-control-and-checkpoint.md`](02-pg-control-and-checkpoint.md).
- WAL-segment rotation, recycle/unlink, `archive_status/`: [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md).
- Shared-catalog DDL matrix: [`04-shared-catalog-bootstrap.md`](04-shared-catalog-bootstrap.md).
- Local-catalog DDL matrix and WAL-record IDs: [`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md).
- Non-nailed-catalog DDL paths (opclass/opfamily/cast/range/collation/conversion/aggregate/language/operator): [`06-bki-derived-catalog-seeds.md`](06-bki-derived-catalog-seeds.md).
- `pg_rewrite` and view rules: [`07-system-views-and-pg-rewrite.md`](07-system-views-and-pg-rewrite.md).
- Relcache init-file unlink protocol: [`08-relcache-init-and-version-files.md`](08-relcache-init-and-version-files.md).
- Signal files, timeline-history, replication slots, basebackup, `XLOG_PARAMETER_CHANGE`: [`09-streaming-replication-readiness.md`](09-streaming-replication-readiness.md).

Where this file needs an additional upstream line citation it uses
the convention `src/path:LINE`, with paths relative to
`/home/ryo/work/goopg/goopg/postgres/`.

## How to read this file

Each operation sub-section follows a fixed template:

- **Trigger** — the SQL command or runtime event that fires it.
- **Upstream entry point** — the top-level C function in PG18 that
  drives the operation, with `src/path:LINE`.
- **Artefacts touched** — link to the per-artefact section in
  docs 01–09 that documents the on-disk format; only the *changed*
  artefacts appear (a `CREATE INDEX` does not list `pg_database`).
- **WAL records** — `RmgrId` + `info` byte for each record emitted,
  in the order they are inserted within the operation's critical
  section.
- **Cache invalidation** — the catcache IDs and relcache messages
  the SI queue must carry; cross-referenced from each artefact doc.
- **goopg status** — `done` / `partial` / `missing` with the
  responsible file path under `internal/`.

The legend matches `README.md` § "Status legend".

## Operations

### `initdb` (one-shot)

- **Trigger:** `goopg init -D <path>` (equivalent to upstream
  `initdb`); fires `internal/initdb/initdb.go::Init`.
- **Upstream entry point:** `src/bin/initdb/initdb.c::initialize_data_directory:3044`.
- **Artefacts touched:** sums up the static state described in
  docs 01 (dir / flat files), 02 (`pg_control`), 03 (first WAL
  segment), 04 (shared catalogs + critical shared indexes), 05
  (local catalogs + critical local indexes), 06 (BKI-derived
  non-nailed catalogs), 07 (`system_views.sql`, `pg_rewrite`,
  `pg_stat_wal_receiver`), 08 (relcache init files, `PG_VERSION`).
  Does not touch 09's signal / history / slot files.
- **WAL records:** exactly one — `XLOG_CHECKPOINT_SHUTDOWN`
  (rmgr `RM_XLOG_ID`, info `0x00`) at LSN `0/01000028`. See
  [`03-`](03-wal-bootstrap-segment.md) §"First WAL record".
- **Cache invalidation:** none (no other backend exists).
- **goopg status:** `partial`. Carrier is
  `internal/initdb/initdb.go::Init`; the gaps are catalogued in
  docs 01–09's "What goopg must produce" tables — most material
  are the `checkPointCopy` substructure (02), the missing first
  WAL segment body (03), the ~3390-row pg_proc deficit (05), the
  ~860 missing pg_amop rows (06), and the relcache-init-file
  records past record #1 (08).

### `CREATE TABLE`

- **Trigger:** SQL `CREATE TABLE`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::DefineRelation:706` →
  `heap_create_with_catalog:1122` in `src/backend/catalog/heap.c`.
- **Artefacts touched:** pg_class +1 row + rowtype row; pg_attribute
  ×relnatts; pg_type ×2 (composite rowtype + array sibling); the
  new heap file `base/<dboid>/<relfilenode>`; pg_depend; pg_class
  index 2662; pg_attribute index 2659; pg_type indexes; namespace
  index. See [`05-`](05-local-catalog-bootstrap.md) §"Per-DDL
  mutation matrix" row `CREATE TABLE`.
- **WAL records:** `XLOG_HEAP_INSERT` (`RM_HEAP_ID`, `0x00`) ×
  (1 pg_class + relnatts pg_attribute + 2 pg_type + pg_depend rows);
  `XLOG_BTREE_INSERT_LEAF` (`RM_BTREE_ID`, `0x00`) × per index leaf;
  `XLOG_SMGR_CREATE` (`RM_SMGR_ID`, `0x10`) for the new relfile.
- **Cache invalidation:** `RELOID`, `RELNAMENSP`, `TYPEOID`,
  `TYPENAMENSP`; `RelcacheInval` on the new relation.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateTable:305` inserts
  into the in-memory catalog (`internal/catalog/catalog.go`) and
  writes a stub heap-page; it does *not* emit canonical
  `XLOG_HEAP_INSERT` against `base/<dboid>/1259` etc. or call any
  SI-equivalent fanout. A vanilla standby will not see the new
  table.

### `CREATE INDEX`

- **Trigger:** SQL `CREATE INDEX [CONCURRENTLY]`.
- **Upstream entry point:** `src/backend/commands/indexcmds.c::DefineIndex:542`
  → `src/backend/catalog/index.c::index_create:726`.
- **Artefacts touched:** pg_class +1 (index row); pg_attribute
  ×nkeys; pg_index +1; pg_depend; the new index relfile (one
  metapage + leaf pages). Indexes 2662, 2659, 2679 touched.
- **WAL records:** `XLOG_HEAP_INSERT` × (1 + nkeys + 1);
  `XLOG_BTREE_INSERT_LEAF` × 3 for the three critical-local indexes
  + the new index's own bulk-build records (`XLOG_BTREE_NEWROOT` or
  `XLOG_BTREE_BUILD` depending on size); `XLOG_SMGR_CREATE`.
- **Cache invalidation:** `RELOID` for parent table + new index;
  `INDEXRELID`; `RelcacheInval` on parent.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateIndex:823` builds a
  btree via `internal/executor/operators_ddl.go::bulkBuildBTree:1111`;
  same WAL/SI gap as `CREATE TABLE`.

### `DROP TABLE` / `DROP INDEX`

- **Trigger:** SQL `DROP TABLE … [CASCADE]`, `DROP INDEX …`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::RemoveRelations` →
  `src/backend/catalog/heap.c::heap_drop_with_catalog:1784`.
- **Artefacts touched:** pg_class DELETE; pg_attribute DELETE ×;
  pg_type DELETE × 2 (rowtype + array); pg_depend DELETE ×;
  pg_index DELETE for the dropped index; the relfile is queued for
  unlink at commit (PG defers unlink via `RelationDropStorage`).
- **WAL records:** `XLOG_HEAP_DELETE` × (`RM_HEAP_ID`, `0x10`);
  no btree-WAL on delete (btree marks tuples dead lazily);
  `XLOG_SMGR_TRUNCATE` (`RM_SMGR_ID`, `0x20`) on commit;
  `XLOG_XACT_COMMIT` with `RelfileLocatorDrop` array.
- **Cache invalidation:** `RELOID`, `RELNAMENSP`, `TYPEOID`,
  `INDEXRELID`; `RelcacheInval`.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execDropTable:744` /
  `execDropIndex:860` removes from the in-memory catalog; canonical
  WAL+SI dispatch and `XLOG_SMGR_TRUNCATE` are missing.

### `TRUNCATE`

- **Trigger:** SQL `TRUNCATE [ONLY] table_list [RESTART IDENTITY]`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::ExecuteTruncate:1861`.
- **Artefacts touched:** pg_class HOT-UPDATE (`relfilenode` bump
  for non-mapped relations); old relfile queued for unlink, new
  relfile created; relmap update if the truncated catalog is mapped
  (see [`08-`](08-relcache-init-and-version-files.md) §"Relmap
  dependency").
- **WAL records:** `XLOG_HEAP_TRUNCATE` (`RM_HEAP_ID`, `0x80`);
  `XLOG_SMGR_CREATE` for new relfile; `XLOG_SMGR_TRUNCATE` for old;
  `XLOG_HEAP_UPDATE` on pg_class; `XLOG_RELMAP_UPDATE`
  (`RM_RELMAP_ID`, `0x00`) if mapped — see
  `src/backend/utils/cache/relmapper.c:1096`.
- **Cache invalidation:** `RELOID`, `SMGR_RELATION`,
  `RELCACHEINITFILEINVAL` if the truncated rel is a nailed catalog.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execTruncate:1504` calls
  `internal/executor/operators_ddl.go::truncateRelation:1990`; no
  `XLOG_HEAP_TRUNCATE` emission, no relmap path. `TRUNCATE pg_class`
  would silently corrupt a standby.

### `ALTER TABLE … ADD COLUMN`

- **Trigger:** SQL `ALTER TABLE t ADD COLUMN c TYPE …`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::ATExecAddColumn:7217`.
- **Artefacts touched:** pg_attribute +1 row; pg_class HOT-UPDATE
  (`relnatts`, possibly `relhasindex`); for `NOT NULL` with default
  the heap may be rewritten (allocating a new relfilenode).
- **WAL records:** `XLOG_HEAP_INSERT` (pg_attribute);
  `XLOG_HEAP_UPDATE` (pg_class HOT); on rewrite path
  `XLOG_SMGR_CREATE` + `XLOG_HEAP_INSERT` per migrated tuple +
  `XLOG_HEAP_UPDATE` for relfilenode bump.
- **Cache invalidation:** `RELOID`, `ATTNUM`, `ATTNAME`;
  `RelcacheInval` on parent (TupleDesc changed).
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execAlterTableAddColumn:991`
  inserts via `internal/catalog/catalog.go::AddColumn`; canonical
  WAL+SI missing. The `relnatts` increment is particularly
  load-bearing because a standby's relcache rebuild needs the
  updated `pg_class.relnatts` to produce the right TupleDesc.

### `ALTER TABLE … DROP COLUMN`

- **Trigger:** SQL `ALTER TABLE t DROP COLUMN c`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::ATExecDropColumn`.
- **Artefacts touched:** pg_attribute UPDATE (`attisdropped = true`,
  `attname = "........pg.dropped.<attnum>"`); pg_class HOT-UPDATE
  (no relnatts change — dropped columns stay counted).
- **WAL records:** `XLOG_HEAP_UPDATE` × 2 (pg_attribute, pg_class).
- **Cache invalidation:** `RELOID`, `ATTNUM`, `ATTNAME`.
- **goopg status:** `missing` — no DROP COLUMN handler in goopg
  today; `internal/executor/operators_ddl.go::execAlterTable:904`
  switches only on `AddColumn` and `AddPrimaryKey`.

### `ALTER TABLE … ALTER COLUMN TYPE`

- **Trigger:** SQL `ALTER TABLE t ALTER COLUMN c TYPE …`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::ATExecAlterColumnType`.
- **Artefacts touched:** pg_attribute UPDATE (`atttypid`, `attlen`,
  `attbyval`, `attalign`, `attstorage`); pg_class UPDATE (relfilenode
  bump when the conversion is not binary-compatible); pg_depend
  rewrite; on rewrite the heap and every index are rebuilt.
- **WAL records:** `XLOG_HEAP_UPDATE` × 2; on rewrite
  `XLOG_SMGR_CREATE` + per-tuple `XLOG_HEAP_INSERT` + index rebuild
  records.
- **Cache invalidation:** `RELOID`, `ATTNUM`, `TYPEOID`.
- **goopg status:** `missing`.

### `ALTER TABLE … RENAME`

- **Trigger:** `ALTER TABLE … RENAME TO …`,
  `ALTER TABLE … RENAME COLUMN … TO …`.
- **Upstream entry point:** `src/backend/commands/tablecmds.c::RenameRelation:4206`
  (table) / `renameatt:4270` (column).
- **Artefacts touched:** pg_class UPDATE (`relname`) on rename
  table; pg_attribute UPDATE (`attname`) on rename column.
- **WAL records:** `XLOG_HEAP_UPDATE`.
- **Cache invalidation:** `RELOID`, `RELNAMENSP` for table;
  `ATTNAME` for column.
- **goopg status:** `missing`.

### `CREATE / OR REPLACE VIEW`

- **Trigger:** SQL `CREATE [OR REPLACE] VIEW v AS SELECT …`.
- **Upstream entry point:** `src/backend/commands/view.c::DefineView:356`
  → `src/backend/rewrite/rewriteDefine.c::DefineQueryRewrite:224`.
- **Artefacts touched:** pg_class +1 row (kind 'v', `relhasrules=t`);
  pg_attribute × ncols; pg_rewrite +1 `_RETURN` rule; pg_depend.
  See [`07-`](07-system-views-and-pg-rewrite.md) §"DDL operations
  affecting `pg_rewrite`".
- **WAL records:** `XLOG_HEAP_INSERT` ×; `XLOG_BTREE_INSERT_LEAF` ×
  (incl. `pg_rewrite_rel_rulename_index` 2693).
- **Cache invalidation:** `RELOID`, `RELNAMENSP`; `RelcacheInval` on
  the view (`relhasrules` flip).
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateView:672` registers
  the view in `internal/catalog/catalog.go` but does **not** insert
  a `pg_rewrite` row, so a standby reading from `pg_rewrite` will
  not find the rule and `EvaluateView` will FATAL.

### `DROP VIEW`

- **Trigger:** SQL `DROP VIEW v [CASCADE]`.
- **Upstream entry point:** `src/backend/rewrite/rewriteRemove.c::RemoveRewriteRuleById:33`.
- **Artefacts touched:** pg_class DELETE; pg_attribute DELETE × ;
  pg_rewrite DELETE; pg_depend DELETE.
- **WAL records:** `XLOG_HEAP_DELETE` ×.
- **Cache invalidation:** `RELOID`, `RELNAMENSP`.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execDropView:724`.

### `CREATE FUNCTION`

- **Trigger:** SQL `CREATE [OR REPLACE] FUNCTION …`.
- **Upstream entry point:** `src/backend/catalog/pg_proc.c::ProcedureCreate:98`.
- **Artefacts touched:** pg_proc +1 row; pg_depend; pg_proc indexes
  2690 (oid) and 2691 (proname_args_nsp).
- **WAL records:** `XLOG_HEAP_INSERT`; `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `PROCOID`, `PROCNAMEARGSNSP`.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateFunction:1553`
  registers via `internal/catalog/routines.go`; canonical pg_proc
  heap WAL not emitted.

### `DROP FUNCTION`

- **Trigger:** SQL `DROP FUNCTION f(arg, …)`.
- **Upstream entry point:** generic `src/backend/commands/dropcmds.c::RemoveObjects:46`.
- **Artefacts touched:** pg_proc DELETE; pg_depend DELETE.
- **WAL records:** `XLOG_HEAP_DELETE`.
- **Cache invalidation:** `PROCOID`, `PROCNAMEARGSNSP`.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execDropFunction:1691`.

### `CREATE TYPE` (composite + range + enum)

- **Trigger:** SQL `CREATE TYPE … AS (…)`, `… AS ENUM (…)`,
  `… AS RANGE (…)`.
- **Upstream entry point:** `src/backend/commands/typecmds.c` —
  `DefineCompositeType`, `DefineEnum:1097`, `DefineRange:1380`.
  `src/backend/catalog/pg_type.c::TypeCreate:195`.
- **Artefacts touched:** pg_type +1 row + array sibling; for
  composite: pg_class +1 (kind 'c') + pg_attribute × ncols; for
  enum: pg_enum × N rows; for range: pg_range +1 + pg_type +1
  (multirange) + pg_cast × constructor casts + pg_proc rows for
  canonical/subdiff. See [`06-`](06-bki-derived-catalog-seeds.md)
  §"User-DDL rules" row `CREATE TYPE … AS RANGE`.
- **WAL records:** `XLOG_HEAP_INSERT` × (1+1 base/array; +1
  composite pg_class; +ncols pg_attribute; +N pg_enum; +1 pg_range;
  +cast/proc rows); index inserts per touched index.
- **Cache invalidation:** `TYPEOID`, `TYPENAMENSP`; `RANGETYPE`,
  `RANGEMULTIRANGE` for range; `ENUMOID`, `ENUMTYPOIDNAME` for
  enum.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateType:2173`
  registers via `internal/catalog/`; canonical pg_type heap WAL
  missing; range/enum on-disk seeding entirely missing.

### `CREATE TRIGGER` / `DROP TRIGGER`

- **Trigger:** SQL `CREATE TRIGGER`, `DROP TRIGGER`.
- **Upstream entry point:** `src/backend/commands/trigger.c::CreateTriggerFiringOn:178`.
- **Artefacts touched:** pg_trigger +1 / −1 row; pg_class UPDATE
  (`relhastriggers` flip if first/last); pg_depend; pg_trigger
  index 2701.
- **WAL records:** `XLOG_HEAP_INSERT` / `XLOG_HEAP_DELETE`;
  `XLOG_HEAP_UPDATE` (pg_class); `XLOG_BTREE_INSERT_LEAF` × 1.
- **Cache invalidation:** `RELOID` (relcache must reload so the
  trigger fires); no syscache on pg_trigger itself.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateTrigger:1895` /
  `execDropTrigger:1921`; the pg_trigger heap file
  `base/<dboid>/2620` is not updated, leaving any standby trigger
  evaluation blind.

### `CREATE / DROP RULE`

- **Trigger:** SQL `CREATE RULE`, `DROP RULE`.
- **Upstream entry point:** `src/backend/rewrite/rewriteDefine.c::DefineRule`
  / `::DefineQueryRewrite:224`; remove via
  `src/backend/rewrite/rewriteRemove.c:33`.
- **Artefacts touched:** pg_rewrite +1 / −1; pg_class UPDATE
  (`relhasrules`); pg_depend.
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_HEAP_UPDATE`; `XLOG_BTREE_INSERT_LEAF` × 1.
- **Cache invalidation:** `RelcacheInval` on the relation.
- **goopg status:** `missing` — goopg's parser accepts CREATE RULE
  but `operators_ddl.go` does not dispatch it.

### `CREATE / DROP / ALTER ROLE`

- **Trigger:** SQL `CREATE/ALTER/DROP ROLE`.
- **Upstream entry point:** `src/backend/commands/user.c::CreateRole:132`,
  `AlterRole:619`, `DropRole:1090`.
- **Artefacts touched:** pg_authid +1/−1/HOT-UPDATE; pg_shdepend
  (owner edges); pg_db_role_setting (for `SET … IN ROLE`);
  pg_authid indexes 2676, 2677. See [`04-`](04-shared-catalog-bootstrap.md)
  §"Per-DDL mutation table" rows `CREATE/ALTER/DROP ROLE`.
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE` / `_UPDATE`;
  `XLOG_BTREE_INSERT_LEAF` × 2 (on key-change rename).
- **Cache invalidation:** `AUTHOID`, `AUTHNAME` — **cluster-wide
  dispatch** (`dbId = InvalidOid`).
- **goopg status:** `missing` — no role-DDL handler in
  `internal/executor/operators_ddl.go`.

### `GRANT` / `REVOKE` membership

- **Trigger:** SQL `GRANT role TO member`, `REVOKE … FROM …`.
- **Upstream entry point:** `src/backend/commands/user.c::AddRoleMems:1681` / `DelRoleMems`.
- **Artefacts touched:** pg_auth_members +1/−1 row; pg_auth_members
  indexes 2694, 2695, 6302, 6303.
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 4.
- **Cache invalidation:** `AUTHMEMROLEMEM`, `AUTHMEMMEMROLE` —
  cluster-wide.
- **goopg status:** `missing` — `operators_ddl.go:102` no-ops the
  generic GRANT path.

### `CREATE / DROP DATABASE`

- **Trigger:** SQL `CREATE DATABASE`, `DROP DATABASE`.
- **Upstream entry point:** `src/backend/commands/dbcommands.c::createdb:684` /
  `dropdb:1673`.
- **Artefacts touched:** pg_database +1/−1 row; pg_shdepend (owner
  edge); pg_database indexes 2671, 2672; on disk, `mkdir
  base/<newdb>/` cloned from `template1` (or `rmtree base/<dboid>/`).
- **WAL records:** `XLOG_DBASE_CREATE_WAL_LOG`
  (`RM_DBASE_ID`, `0x10`) or `XLOG_DBASE_CREATE_FILE_COPY` (`0x00`);
  `XLOG_DBASE_DROP` (`0x20`); `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `DATABASEOID`, `DATABASENAME` — cluster
  wide.
- **goopg status:** `partial` —
  `internal/catalog/catalog.go::CreateDatabase:575` /
  `DropDatabase:587` track in-memory; no pg_database heap-row WAL,
  no `XLOG_DBASE_CREATE*`, no on-disk `base/<oid>/` clone. The
  `internal/server/database_ddl.go` shim handles the on-the-wire
  command but does not WAL the file-tree copy.

### `ALTER DATABASE …` (datallowconn, datfrozenxid, etc.)

- **Trigger:** SQL `ALTER DATABASE name [WITH] …`,
  `ALTER DATABASE name SET parameter = value`,
  internal `vac_update_datfrozenxid`.
- **Upstream entry point:** `src/backend/commands/dbcommands.c::AlterDatabase:2368`
  / `AlterDatabaseSet:2638`; `src/backend/commands/vacuum.c::vac_update_datfrozenxid:1624`.
- **Artefacts touched:** pg_database UPDATE (HOT for `datallowconn`,
  `datistemplate`, `datconnlimit`); for `datfrozenxid` / `datminmxid`
  the path is an in-place update (`XLOG_HEAP_INPLACE`). For
  `ALTER … SET`: pg_db_role_setting upsert.
- **WAL records:** `XLOG_HEAP_UPDATE` (most fields) or
  `XLOG_HEAP_INPLACE` (`RM_HEAP_ID`, `0x90`) for vacuum;
  `XLOG_HEAP_INSERT`/`_UPDATE` on pg_db_role_setting.
- **Cache invalidation:** `DATABASEOID` (`CacheInvalidateHeapTupleInplace`
  for vacuum, `…HeapTuple` otherwise) — cluster wide.
- **goopg status:** `missing`. The `datfrozenxid` path becomes
  reachable once autovacuum lands; `ALTER DATABASE …` SQL is not
  routed today.

### `CREATE / DROP SCHEMA`

- **Trigger:** SQL `CREATE SCHEMA`, `DROP SCHEMA`.
- **Upstream entry point:** `src/backend/commands/schemacmds.c::CreateSchemaCommand:52`;
  drop via `src/backend/commands/dropcmds.c::RemoveObjects:46`.
- **Artefacts touched:** pg_namespace +1/−1; pg_depend;
  pg_namespace indexes (nspname 2684, oid 2685).
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `NAMESPACEOID`, `NAMESPACENAME`.
- **goopg status:** `missing` — no schema-DDL handler.

### `CREATE / DROP TABLESPACE`

- **Trigger:** SQL `CREATE TABLESPACE`, `DROP TABLESPACE`.
- **Upstream entry point:** `src/backend/commands/tablespace.c::CreateTableSpace:208`
  / `DropTableSpace`.
- **Artefacts touched:** pg_tablespace +1/−1; pg_shdepend;
  pg_tablespace indexes 2697, 2698; symlink `pg_tblspc/<oid>` →
  `<location>`; the canonical `PG_18_<catver>_<dboid>` sub-dir
  inside `<location>`.
- **WAL records:** `XLOG_TBLSPC_CREATE` (`RM_TBLSPC_ID`, `0x00`) /
  `XLOG_TBLSPC_DROP` (`0x10`); `XLOG_HEAP_INSERT`/`_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `TABLESPACEOID`.
- **goopg status:** `missing`. The `pg_tblspc/` directory exists
  (see [`01-`](01-data-directory-layout.md) §"Directory inventory")
  but no executor path issues the create/drop.

### `CREATE / DROP SUBSCRIPTION`

- **Trigger:** SQL `CREATE SUBSCRIPTION`, `DROP SUBSCRIPTION`,
  `ALTER SUBSCRIPTION`.
- **Upstream entry point:** `src/backend/commands/subscriptioncmds.c::CreateSubscription:539`
  / `AlterSubscription:1100` / `DropSubscription:1626`.
- **Artefacts touched:** pg_subscription +1/−1/UPDATE; pg_shdepend
  (owner edge); pg_subscription indexes 6114, 6115; pg_subscription_rel
  on `ALTER SUBSCRIPTION REFRESH PUBLICATION`.
- **WAL records:** `XLOG_HEAP_INSERT`/`_DELETE`/`_UPDATE`;
  `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `SUBSCRIPTIONOID`, `SUBSCRIPTIONNAME` —
  cluster-wide.
- **goopg status:** `partial` —
  `internal/executor/operators_ddl.go::execCreateSubscription:189` /
  `execDropSubscription:207` mutates the
  `internal/catalog/pubsub.go::PubSub` in-memory registry; no
  pg_subscription heap-row WAL.

### `CREATE / DROP / ALTER OPERATOR CLASS / FAMILY`

- **Trigger:** SQL `CREATE OPERATOR CLASS`,
  `CREATE OPERATOR FAMILY`, `ALTER OPERATOR FAMILY ADD/DROP`.
- **Upstream entry point:** `src/backend/commands/opclasscmds.c::DefineOpClass:333`,
  `DefineOpFamily:772`, `AlterOpFamily:870`. See [`06-`](06-bki-derived-catalog-seeds.md)
  §"User-DDL rules".
- **Artefacts touched:** pg_opclass / pg_opfamily / pg_amop /
  pg_amproc; pg_depend; indexes 2687 (opclass_oid), 2754 (opfamily
  am_name_nsp), 2655 (amproc fam_proc), 2602 (amop_opr_fam),
  2653/2654 (amop strat/proc).
- **WAL records:** `XLOG_HEAP_INSERT`/`_DELETE` ×;
  `XLOG_BTREE_INSERT_LEAF` × (1 per affected index).
- **Cache invalidation:** `CLAOID`, `CLAAMNAMENSP`,
  `OPFAMILYOID`, `OPFAMILYAMNAMENSP`, `AMOPOPID`, `AMOPSTRATEGY`,
  `AMPROCNUM`.
- **goopg status:** `missing`.

### `CREATE / DROP CAST`

- **Trigger:** SQL `CREATE CAST`, `DROP CAST`.
- **Upstream entry point:** `src/backend/commands/functioncmds.c::CreateCast:1539`
  → `src/backend/catalog/pg_cast.c::CastCreate:49`.
- **Artefacts touched:** pg_cast +1/−1; pg_depend; pg_cast indexes
  2660 (oid), 2661 (source_target).
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `CASTSOURCETARGET`.
- **goopg status:** `missing`.

### `CREATE / DROP COLLATION`

- **Trigger:** SQL `CREATE COLLATION`, `DROP COLLATION`.
- **Upstream entry point:** `src/backend/commands/collationcmds.c::DefineCollation:53`
  → `src/backend/catalog/pg_collation.c::CollationCreate:42`.
- **Artefacts touched:** pg_collation +1/−1; pg_depend; pg_collation
  indexes 3164 (name_enc_nsp), 3085 (oid).
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 2.
- **Cache invalidation:** `COLLNAMEENCNSP`, `COLLOID`.
- **goopg status:** `missing`.

### `CREATE / DROP CONVERSION`

- **Trigger:** SQL `CREATE CONVERSION`, `DROP CONVERSION`.
- **Upstream entry point:** `src/backend/commands/conversioncmds.c::CreateConversionCommand:32`
  → `src/backend/catalog/pg_conversion.c::ConversionCreate:38`.
- **Artefacts touched:** pg_conversion +1/−1; pg_depend; pg_conversion
  indexes 2667 (default), 2668 (name_nsp), 2670 (oid).
- **WAL records:** `XLOG_HEAP_INSERT` / `_DELETE`;
  `XLOG_BTREE_INSERT_LEAF` × 3.
- **Cache invalidation:** `CONDEFAULT`, `CONNAMENSP`, `CONVOID`.
- **goopg status:** `missing`.

### `CREATE AGGREGATE`

- **Trigger:** SQL `CREATE AGGREGATE`.
- **Upstream entry point:** `src/backend/commands/aggregatecmds.c::DefineAggregate:53`
  → `src/backend/catalog/pg_aggregate.c::AggregateCreate:46`.
- **Artefacts touched:** pg_aggregate +1; pg_proc +1 (the
  state-transition function); pg_depend; pg_aggregate index 2650
  (fnoid); pg_proc indexes 2690, 2691.
- **WAL records:** `XLOG_HEAP_INSERT` × 2;
  `XLOG_BTREE_INSERT_LEAF` × 3.
- **Cache invalidation:** `AGGFNOID`, `PROCOID`,
  `PROCNAMEARGSNSP`.
- **goopg status:** `missing`.

### `INSERT / UPDATE / DELETE` on a user table (everyday path)

- **Trigger:** SQL DML against any user heap.
- **Upstream entry point:** `src/backend/access/heap/heapam.c::heap_insert:2099`
  / `heap_update:3303` / `heap_delete:2796`.
- **Artefacts touched:** the user heap file
  `base/<dboid>/<relfilenode>`; every secondary index for the
  affected key; the visibility map (1st-time all-visible after
  `heap_update`); the FSM (any free-space change).
- **WAL records:** `XLOG_HEAP_INSERT` / `_UPDATE` / `_DELETE` /
  `_HOT_UPDATE` (`info = 0x40`) when HOT; `XLOG_BTREE_INSERT_LEAF`
  per touched index; `XLOG_HEAP2_VISIBLE` (`RM_HEAP2_ID`, `0x40`)
  on VM-bit set; `XLOG_HEAP2_PRUNE_ON_ACCESS` (`0x10`) on
  opportunistic page prune.
- **Cache invalidation:** none on a user table (no syscache).
- **goopg status:** `partial` —
  `internal/executor/operators_dml.go` (and friends) writes the
  heap-page tuples and emits `XLOG_HEAP_INSERT`/`_UPDATE`/`_DELETE`
  through `internal/wal/`; coverage of HOT updates and
  `XLOG_HEAP2_VISIBLE` VM emission is incomplete (cross-ref
  [`05-`](05-local-catalog-bootstrap.md) §"Continuous maintenance"
  on `XLOG_HEAP2_*` records). This is the most-exercised path and
  the most-tested.

### `CHECKPOINT` (manual or automatic)

- **Trigger:** SQL `CHECKPOINT`; or background `checkpoint_timeout`
  / `max_wal_size` trip.
- **Upstream entry point:** `src/backend/access/transam/xlog.c::CreateCheckPoint:6981`
  → `:7306` (post-flush `UpdateControlFile`).
- **Artefacts touched:** `global/pg_control` rewrite (`state`,
  `checkPoint`, `checkPointCopy`, `unloggedLSN`, `minRecoveryPoint=0`,
  `minRecoveryPointTLI=0` — see [`02-`](02-pg-control-and-checkpoint.md)
  §"`UpdateControlFile` call-site matrix"); stale
  `pg_logical/snapshots/*` cleaned (`snapbuild.c:1976-1993`);
  `pg_replslot/<slot>/state` rewritten for each slot whose
  `restart_lsn` advanced (see [`09-`](09-streaming-replication-readiness.md)
  §"Replication-slot lifecycle").
- **WAL records:** `XLOG_CHECKPOINT_SHUTDOWN` (`RM_XLOG_ID`, `0x00`)
  or `XLOG_CHECKPOINT_ONLINE` (`0x10`), written **before** the
  `UpdateControlFile`.
- **Cache invalidation:** none.
- **goopg status:** `partial` —
  `internal/wal/checkpointer.go::runCheckpoint:299` builds the
  checkpoint record but does not rewrite `pg_control` afterwards;
  cross-ref [`02-`](02-pg-control-and-checkpoint.md) §"Continuous-
  maintenance gaps". Slot-state rewrite stub at
  `internal/wal/slots.go::writeSlotLocked:393` uses JSON, not
  PG-binary.

### `RESTART POINT` (standby)

- **Trigger:** standby observes the primary's `XLOG_CHECKPOINT_*` and
  decides to materialise its own checkpoint state.
- **Upstream entry point:** `src/backend/access/transam/xlog.c::CreateRestartPoint:7789`
  (apply path) / `:7691` (skip path).
- **Artefacts touched:** `global/pg_control` (`checkPoint`,
  `checkPointCopy`, `state`, `minRecoveryPoint`,
  `minRecoveryPointTLI` updates).
- **WAL records:** none — restart-points only consume WAL.
- **Cache invalidation:** none.
- **goopg status:** `missing` — goopg's standby loop in
  `internal/wal/recovery.go` does not yet materialise restart-points
  into `pg_control`.

### `VACUUM`

- **Trigger:** SQL `VACUUM`; or autovacuum-launched.
- **Upstream entry point:** `src/backend/commands/vacuum.c::vacuum`
  → per-table `lazy_vacuum_rel` / `heap_vacuum_rel`.
- **Artefacts touched:** the heap (dead-tuple removal, line-pointer
  re-pack, page-prune); the VM fork (`_vm`); the FSM (`_fsm`); each
  secondary index (`amvacuumcleanup`); pg_class HOT-UPDATE
  (`relfrozenxid`, `relminmxid`, `relallvisible`, `relallfrozen`,
  `reltuples`, `relpages` — see [`05-`](05-local-catalog-bootstrap.md)
  §"Per-DDL mutation matrix" `VACUUM` row).
- **WAL records:** `XLOG_HEAP2_PRUNE_ON_ACCESS` (`RM_HEAP2_ID`,
  `0x10`); `XLOG_HEAP2_PRUNE_VACUUM_SCAN` (`0x20`);
  `XLOG_HEAP2_PRUNE_VACUUM_CLEANUP` (`0x30`); `XLOG_HEAP2_VISIBLE`
  (`0x40`); `XLOG_BTREE_VACUUM` (`RM_BTREE_ID`, `0xC0`);
  `XLOG_HEAP_INPLACE` on the pg_class row (`RM_HEAP_ID`, `0x90`).
  PG18 folded freeze into the prune records — there is *no*
  `XLOG_HEAP2_FREEZE_PAGE` in PG18.
- **Cache invalidation:** `CacheInvalidateHeapTupleInplace`
  (`RELOID`).
- **goopg status:** `partial` —
  `internal/executor/operators_vacuum.go::Open:26` →
  `internal/vacuum/vacuum.go::vacuumCore:85`. The pg_class HOT
  update lives at `internal/catalog/catalog.go` but the
  `XLOG_HEAP2_VISIBLE` / `XLOG_HEAP2_PRUNE_*` emission is
  incomplete in `internal/wal/recovery.go`.

### `VACUUM FREEZE`

- **Trigger:** SQL `VACUUM FREEZE` or aggressive-mode autovacuum
  (`vacuum_freeze_min_age` / `vacuum_freeze_table_age`).
- **Upstream entry point:** same as VACUUM, with `aggressive = true`
  in `heap_vacuum_rel`.
- **Artefacts touched:** same as VACUUM; every visible tuple has
  `t_infomask |= HEAP_XMIN_FROZEN`.
- **WAL records:** same as VACUUM; the freeze info travels inside
  `XLOG_HEAP2_PRUNE_VACUUM_*` payloads (PG18 unified freeze with
  prune; see [`05-`](05-local-catalog-bootstrap.md) §"WAL records
  (header constants)").
- **Cache invalidation:** same as VACUUM.
- **goopg status:** `partial` — same path; the freeze-mark code
  in `internal/vacuum/vacuum.go::vacuumCore` already sets
  `HEAP_XMIN_FROZEN`; WAL emission has the same gap as VACUUM.

### `VACUUM FULL` / `CLUSTER`

- **Trigger:** SQL `VACUUM FULL`, `CLUSTER`.
- **Upstream entry point:** `src/backend/commands/cluster.c::cluster_rel`
  → `make_new_heap:705` (rewrite) → `swap_relation_files`.
- **Artefacts touched:** pg_class HOT-UPDATE (`relfilenode` bump
  for non-mapped; for mapped catalogs the relmap path —
  `RelationMapUpdateMap` / `XLOG_RELMAP_UPDATE`); old relfile
  unlink; new relfile created and fully WAL-imaged; every index
  rebuilt (new relfilenode). See [`08-`](08-relcache-init-and-version-files.md)
  §"Per-DDL invalidation table" row `VACUUM FULL pg_class`.
- **WAL records:** `XLOG_SMGR_CREATE`; `XLOG_HEAP_INSERT` (or
  `XLOG_FPI`) per migrated tuple; `XLOG_HEAP_UPDATE` on pg_class;
  `XLOG_RELMAP_UPDATE` if mapped; `XLOG_BTREE_NEWROOT` /
  `_INSERT_LEAF` per index; `XLOG_SMGR_TRUNCATE` of the old file.
- **Cache invalidation:** `RELOID`, `SMGR_RELATION`;
  `RELCACHEINITFILEINVAL` for mapped catalogs.
- **goopg status:** `missing` —
  `internal/executor/operators_cluster.go` exists but the relmap
  + init-file-unlink wiring is absent. A `VACUUM FULL pg_class`
  against goopg today would silently corrupt a streaming standby.

### `REINDEX`

- **Trigger:** SQL `REINDEX [INDEX | TABLE | DATABASE]`.
- **Upstream entry point:** `src/backend/commands/indexcmds.c::ReindexIndex:2919`
  → `src/backend/catalog/index.c::index_drop` + `index_build`.
- **Artefacts touched:** for non-mapped indexes: pg_class HOT
  (relfilenode bump) + new relfile; for mapped critical indexes:
  `RelationMapUpdateMap` + `XLOG_RELMAP_UPDATE` + init-file
  unlink (see [`08-`](08-relcache-init-and-version-files.md)).
- **WAL records:** `XLOG_HEAP_UPDATE` (pg_class);
  `XLOG_RELMAP_UPDATE` for mapped; `XLOG_BTREE_NEWROOT` /
  `_INSERT_LEAF`; `XLOG_SMGR_TRUNCATE` of old index file.
- **Cache invalidation:** `RELOID` (parent + index);
  `RELCACHEINITFILEINVAL` for mapped.
- **goopg status:** `missing`.

### `ANALYZE`

- **Trigger:** SQL `ANALYZE`; or autovacuum.
- **Upstream entry point:** `src/backend/commands/analyze.c::do_analyze_rel:278`
  → `vac_update_relstats:1442`.
- **Artefacts touched:** pg_class HOT-UPDATE (`reltuples`,
  `relpages`, `relallvisible`, `relallfrozen`); pg_statistic
  INSERT/UPDATE per analysed column; pg_statistic_ext_data for
  extended stats.
- **WAL records:** `XLOG_HEAP_INPLACE` on pg_class (PG18 uses
  in-place);  `XLOG_HEAP_INSERT` / `_UPDATE` on pg_statistic.
- **Cache invalidation:** `RELOID` (`CacheInvalidateHeapTupleInplace`
  on pg_class);  `STATRELATTINH` for pg_statistic.
- **goopg status:** `partial` —
  `internal/executor/operators_analyze.go::analyzeRelationCtx:113`
  computes stats and writes them via
  `internal/vacuum/vacuum.go::Analyze:215`; the canonical
  `XLOG_HEAP_INPLACE` on the disk pg_class row is missing.

### `pg_create_physical_replication_slot`

- **Trigger:** SQL `SELECT pg_create_physical_replication_slot('name')`
  or replication-command `CREATE_REPLICATION_SLOT name PHYSICAL`.
- **Upstream entry point:** `src/backend/replication/slot.c::CreateSlotOnDisk:2258`
  → `SaveSlotToPath:2319`.
- **Artefacts touched:** `mkdir pg_replslot/<name>.tmp/`;
  `state.tmp` written with `ReplicationSlotOnDisk` binary header
  + `ReplicationSlotPersistentData` payload; atomic rename to
  `pg_replslot/<name>/state`. See [`09-`](09-streaming-replication-readiness.md)
  §"Replication-slot lifecycle".
- **WAL records:** none (slot creation is local-disk only on
  purpose — standbys never see slot directories appear during
  replay).
- **Cache invalidation:** none.
- **goopg status:** `partial` —
  `internal/wal/slots.go::Create:137` writes JSON instead of the
  PG-binary `SLOT_MAGIC = 0x1051CA1` header. A PG18 walreceiver
  attaching to the goopg primary cannot inherit the slot.

### `pg_create_logical_replication_slot`

- **Trigger:** SQL `SELECT pg_create_logical_replication_slot(name, plugin)`
  or `CREATE_REPLICATION_SLOT name LOGICAL plugin`.
- **Upstream entry point:** `slot.c::CreateSlotOnDisk:2258` (same
  path; `persistency = RS_PERSISTENT`, payload carries
  `plugin` and `database`).
- **Artefacts touched:** same as physical, plus
  `pg_logical/snapshots/<lsn>.snap` once the slot reaches
  consistent point.
- **WAL records:** none for the slot file; logical-decoding
  produces no extra WAL on its own (it *reads* the existing
  WAL stream).
- **Cache invalidation:** none.
- **goopg status:** `partial` —
  `internal/wal/slots.go::CreateLogical:171`; same JSON-format
  gap.

### `pg_replication_slot_advance` (slot persistence)

- **Trigger:** SQL `SELECT pg_replication_slot_advance(name, target_lsn)`;
  or `START_REPLICATION` advancing `restart_lsn`; or
  `CheckPointReplicationSlots` at clean shutdown.
- **Upstream entry point:** `src/backend/replication/slot.c::SaveSlotToPath:2319`.
- **Artefacts touched:** `pg_replslot/<name>/state.tmp` → atomic
  rename → `state`. The fsync is on both the file and the parent
  directory.
- **WAL records:** none.
- **Cache invalidation:** none.
- **goopg status:** `partial` —
  `internal/wal/slots.go::AdvanceConfirmedFlushLSN:257` calls
  `writeSlotLocked:393`; same JSON-vs-binary gap.

### `pg_drop_replication_slot`

- **Trigger:** SQL `SELECT pg_drop_replication_slot('name')` or
  `DROP_REPLICATION_SLOT name`.
- **Upstream entry point:** `src/backend/replication/slot.c::ReplicationSlotDropPtr:976`.
- **Artefacts touched:** `rmtree pg_replslot/<name>/`.
- **WAL records:** none.
- **Cache invalidation:** none.
- **goopg status:** `done` —
  `internal/wal/slots.go::Drop:206`.

### `pg_promote()` / `pg_ctl promote` (timeline switch)

- **Trigger:** SQL `SELECT pg_promote()`; `pg_ctl promote`;
  `promote.signal`.
- **Upstream entry point:** `src/backend/access/transam/xlog.c::StartupXLOG:5996-6021`
  (end-of-recovery path).
- **Artefacts touched:** `pg_wal/<newTLI>.history` written;
  `standby.signal` / `recovery.signal` removed; `pg_control`
  rewrite (`state = DB_IN_PRODUCTION`, new TLI in `checkPointCopy`,
  fresh `minRecoveryPoint = 0`); next WAL segment opened on the
  new TLI (either by `XLogFileCopy` or `XLogFileInit`). See
  [`09-`](09-streaming-replication-readiness.md) §"Timeline-history
  file write on promotion".
- **WAL records:** `XLOG_END_OF_RECOVERY` (`RM_XLOG_ID`, `0x90`)
  emitted just before `UpdateControlFile`;
  `XLOG_CHECKPOINT_SHUTDOWN` may follow on clean shutdown of the
  promoted primary.
- **Cache invalidation:** none (state transition; SI queue is
  flushed by post-recovery startup).
- **goopg status:** `partial` —
  `cmd/goopg/standby.go::runPromote:178` →
  `finalizePromotion:249` writes `pg_wal/<TLI>.history` via
  `internal/wal/timeline_history.go::WriteHistory:121` and removes
  `standby.signal`; the `XLOG_END_OF_RECOVERY` record emission and
  the `pg_control` state-transition rewrite are missing.

### `XLOG_PARAMETER_CHANGE` emission (GUC echo into ControlFile)

- **Trigger:** postmaster startup; or `SIGHUP` reload changing one
  of the eight tracked GUCs (`wal_level`, `wal_log_hints`,
  `MaxConnections`, `max_worker_processes`, `max_wal_senders`,
  `max_prepared_xacts`, `max_locks_per_xact`,
  `track_commit_timestamp`).
- **Upstream entry point:** `src/backend/access/transam/xlog.c::XLogReportParameters:8147-8199`.
- **Artefacts touched:** in-place rewrite of `pg_control` for the
  eight GUC echoes; standby's `xlog_redo(XLOG_PARAMETER_CHANGE)`
  applies the same fields to the standby's local `pg_control`
  (`xlog.c:8581-8587`).
- **WAL records:** `XLOG_PARAMETER_CHANGE` (`RM_XLOG_ID`, `0x40`)
  emitted **before** `UpdateControlFile`.
- **Cache invalidation:** none.
- **goopg status:** `missing` — see [`09-`](09-streaming-replication-readiness.md)
  §"What goopg must produce" row "`XLogReportParameters` equivalent";
  also [`02-`](02-pg-control-and-checkpoint.md) §"Continuous-
  maintenance gaps" row "GUC parameter change".

### `Backup start` (`do_pg_backup_start`)

- **Trigger:** SQL `SELECT pg_backup_start(label)`; or
  `BASE_BACKUP` replication command.
- **Upstream entry point:** `src/backend/access/transam/xlog.c::do_pg_backup_start:8842`.
- **Artefacts touched:** `pg_control` (`backupStartPoint`,
  `backupEndRequired = true`); `backup_label` returned to the
  caller (the *caller* writes it to disk on the standby side, not
  the primary).
- **WAL records:** `XLOG_BACKUP_START` is **not** emitted in PG —
  the start is recorded only in `pg_control`. A new checkpoint may
  be triggered if no recent online checkpoint exists.
- **Cache invalidation:** none.
- **goopg status:** `partial` —
  `internal/server/basebackup.go::replyBaseBackup:103` invokes
  `internal/initdb/pgcontrol.go::UpdateControlCheckpoint:153` for
  the redo-LSN imprint but does not set `backupStartPoint` or
  `backupEndRequired`; cross-ref [`02-`](02-pg-control-and-checkpoint.md)
  §"Continuous-maintenance gaps" row `pg_backup_start/stop`.

### `Backup stop` (`do_pg_backup_stop`)

- **Trigger:** SQL `SELECT pg_backup_stop()`; or end of
  `BASE_BACKUP` stream.
- **Upstream entry point:** `src/backend/access/transam/xlog.c::do_pg_backup_stop:9170`.
- **Artefacts touched:** `pg_control` (`backupEndPoint` on standby-
  side backups); the on-the-wire `backup_label` / `tablespace_map`
  / `backup_manifest` tar entries.
- **WAL records:** `XLOG_BACKUP_END` (`RM_XLOG_ID`, `0x50`).
- **Cache invalidation:** none.
- **goopg status:** `partial` —
  `internal/server/basebackup.go::replyBaseBackup:103` streams the
  required tar entries; `XLOG_BACKUP_END` emission and the
  `backupEndPoint` rewrite are missing.

---

## Summary matrix

| Operation | pg_control? | pg_internal.init? | Shared catalogs | Local catalogs | BKI catalogs | Views/rewrite | Replication files | Files/dirs | WAL records |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| `initdb` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✗ | ✓ | `XLOG_CHECKPOINT_SHUTDOWN` |
| `CREATE TABLE` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✓ (new relfile) | HEAP_INSERT, BTREE_INSERT_LEAF, SMGR_CREATE |
| `CREATE INDEX` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✓ | HEAP_INSERT, BTREE_INSERT_LEAF, BTREE_NEWROOT, SMGR_CREATE |
| `DROP TABLE/INDEX` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✓ (unlink) | HEAP_DELETE, SMGR_TRUNCATE, XACT_COMMIT (rel-drop array) |
| `TRUNCATE` | ✗ | partial (mapped) | ✗ | ✓ | ✗ | ✗ | ✗ | ✓ | HEAP_TRUNCATE, SMGR_CREATE, HEAP_UPDATE, RELMAP_UPDATE (mapped) |
| `ALTER TABLE ADD COLUMN` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | partial (rewrite) | HEAP_INSERT, HEAP_UPDATE |
| `ALTER TABLE DROP COLUMN` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | HEAP_UPDATE ×2 |
| `ALTER TABLE ALTER COLUMN TYPE` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | partial | HEAP_UPDATE; on rewrite SMGR_CREATE + HEAP_INSERT × |
| `ALTER TABLE RENAME` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | HEAP_UPDATE |
| `CREATE VIEW` | ✗ | ✗ | ✗ | ✓ | ✗ | ✓ | ✗ | ✗ | HEAP_INSERT, BTREE_INSERT_LEAF |
| `DROP VIEW` | ✗ | ✗ | ✗ | ✓ | ✗ | ✓ | ✗ | ✗ | HEAP_DELETE |
| `CREATE FUNCTION` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | HEAP_INSERT, BTREE_INSERT_LEAF |
| `DROP FUNCTION` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | HEAP_DELETE |
| `CREATE TYPE` (comp/range/enum) | ✗ | ✗ | ✗ | ✓ | ✓ (range, enum) | ✗ | ✗ | ✗ | HEAP_INSERT ×, BTREE_INSERT_LEAF × |
| `CREATE/DROP TRIGGER` | ✗ | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE, HEAP_UPDATE, BTREE_INSERT_LEAF |
| `CREATE/DROP RULE` | ✗ | ✗ | ✗ | ✓ | ✗ | ✓ | ✗ | ✗ | HEAP_INSERT/_DELETE, HEAP_UPDATE |
| `CREATE/DROP/ALTER ROLE` | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE/_UPDATE, BTREE_INSERT_LEAF ×2 |
| `GRANT/REVOKE` membership | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE, BTREE_INSERT_LEAF ×4 |
| `CREATE/DROP DATABASE` | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | ✓ (`base/<oid>/`) | DBASE_CREATE_*, DBASE_DROP, HEAP_INSERT/_DELETE |
| `ALTER DATABASE` | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | HEAP_UPDATE or HEAP_INPLACE (datfrozenxid) |
| `CREATE/DROP SCHEMA` | ✗ | ✗ | ✗ | ✓ (pg_namespace) | ✗ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE, BTREE_INSERT_LEAF ×2 |
| `CREATE/DROP TABLESPACE` | ✗ | ✗ | partial (pg_tablespace) | ✗ | ✗ | ✗ | ✗ | ✓ (`pg_tblspc/`) | TBLSPC_CREATE/_DROP, HEAP_INSERT/_DELETE |
| `CREATE/DROP SUBSCRIPTION` | ✗ | ✗ | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE/_UPDATE, BTREE_INSERT_LEAF ×2 |
| `CREATE/DROP/ALTER OPCLASS/OPFAMILY` | ✗ | ✗ | ✗ | ✓ | ✓ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE ×, BTREE_INSERT_LEAF × |
| `CREATE/DROP CAST` | ✗ | ✗ | ✗ | ✓ | ✓ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE, BTREE_INSERT_LEAF ×2 |
| `CREATE/DROP COLLATION` | ✗ | ✗ | ✗ | ✓ | ✓ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE, BTREE_INSERT_LEAF ×2 |
| `CREATE/DROP CONVERSION` | ✗ | ✗ | ✗ | ✓ | ✓ | ✗ | ✗ | ✗ | HEAP_INSERT/_DELETE, BTREE_INSERT_LEAF ×3 |
| `CREATE AGGREGATE` | ✗ | ✗ | ✗ | ✓ | ✓ | ✗ | ✗ | ✗ | HEAP_INSERT ×2, BTREE_INSERT_LEAF ×3 |
| `INSERT/UPDATE/DELETE` user table | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | partial (VM/FSM) | HEAP_INSERT/_UPDATE/_DELETE/_HOT_UPDATE, HEAP2_VISIBLE, BTREE_INSERT_LEAF |
| `CHECKPOINT` | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | partial (slot state) | partial (pg_logical/snapshots cleanup) | CHECKPOINT_SHUTDOWN/_ONLINE |
| `RESTART POINT` | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | none (consumer) |
| `VACUUM` | ✗ | partial (nailed) | partial | ✓ | ✗ | ✗ | ✗ | partial (VM/FSM) | HEAP2_PRUNE_*, HEAP2_VISIBLE, BTREE_VACUUM, HEAP_INPLACE |
| `VACUUM FREEZE` | ✗ | partial | partial | ✓ | ✗ | ✗ | ✗ | partial | same as VACUUM |
| `VACUUM FULL` / `CLUSTER` | ✗ | ✓ (mapped) | partial | ✓ | ✗ | ✗ | ✗ | ✓ (new relfile) | SMGR_CREATE, HEAP_INSERT ×, RELMAP_UPDATE (mapped), BTREE_NEWROOT, SMGR_TRUNCATE |
| `REINDEX` | ✗ | ✓ (mapped) | partial | ✓ | ✗ | ✗ | ✗ | ✓ | HEAP_UPDATE, RELMAP_UPDATE (mapped), BTREE_NEWROOT, SMGR_TRUNCATE |
| `ANALYZE` | ✗ | ✗ | ✗ | ✓ (pg_class, pg_statistic) | ✗ | ✗ | ✗ | ✗ | HEAP_INPLACE, HEAP_INSERT/_UPDATE |
| `pg_create_physical_replication_slot` | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ | ✓ (`pg_replslot/`) | none |
| `pg_create_logical_replication_slot` | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ | ✓ | none |
| `pg_replication_slot_advance` | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ | none |
| `pg_drop_replication_slot` | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ | ✓ (rmtree) | none |
| `pg_promote()` | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ (`<TLI>.history`, signal removal) | END_OF_RECOVERY, CHECKPOINT_SHUTDOWN |
| `XLOG_PARAMETER_CHANGE` emission | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | PARAMETER_CHANGE |
| `Backup start` | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | none on primary side |
| `Backup stop` | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | BACKUP_END |

Legend: ✓ = touched and must be maintained; partial = touched only
under specific sub-clauses (e.g. mapped relation, nailed catalog);
✗ = not touched. "pg_internal.init?" is ✓ when any nailed-rel
descriptor changes (init-file unlink required, see
[`08-`](08-relcache-init-and-version-files.md) §"Per-DDL invalidation
table").

## What goopg must produce

The table below collapses the per-operation `goopg status` rows. Path
column is the responsible Go file under `internal/`. The "Action"
column points to the relevant per-artefact doc for the canonical
implementation rule.

| Operation | Responsible file | Status | Action |
|---|---|:-:|---|
| `initdb` | `internal/initdb/initdb.go::Init` | partial | docs 01–09 "What goopg must produce" closures |
| `CREATE TABLE` | `internal/executor/operators_ddl.go::execCreateTable:305` | partial | wire `PgCanonicalHeapInsert` from [`05-`](05-local-catalog-bootstrap.md) §"Recommended Go entry points" |
| `CREATE INDEX` | `internal/executor/operators_ddl.go::execCreateIndex:823` | partial | same as above + `BTREE_INSERT_LEAF` emit |
| `DROP TABLE` | `internal/executor/operators_ddl.go::execDropTable:744` | partial | add `XLOG_SMGR_TRUNCATE` and commit-record rel-drop array |
| `DROP INDEX` | `internal/executor/operators_ddl.go::execDropIndex:860` | partial | same |
| `TRUNCATE` | `internal/executor/operators_ddl.go::execTruncate:1504` | partial | add `XLOG_HEAP_TRUNCATE` + relmap path for mapped catalogs ([`08-`](08-relcache-init-and-version-files.md)) |
| `ALTER TABLE ADD COLUMN` | `internal/executor/operators_ddl.go::execAlterTableAddColumn:991` | partial | canonical WAL for pg_attribute INSERT + pg_class HOT UPDATE |
| `ALTER TABLE DROP COLUMN` | new in `operators_ddl.go` | missing | dispatch `AlterTableDropColumn` from `execAlterTable:904` |
| `ALTER TABLE ALTER COLUMN TYPE` | new | missing | full rewrite path: relfilenode bump + reindex |
| `ALTER TABLE RENAME` | new | missing | pg_class / pg_attribute UPDATE + SI |
| `CREATE VIEW` | `internal/executor/operators_ddl.go::execCreateView:672` | partial | insert pg_rewrite row per [`07-`](07-system-views-and-pg-rewrite.md) |
| `DROP VIEW` | `internal/executor/operators_ddl.go::execDropView:724` | partial | pg_rewrite DELETE + relhasrules flip |
| `CREATE FUNCTION` | `internal/executor/operators_ddl.go::execCreateFunction:1553` | partial | canonical pg_proc heap WAL |
| `DROP FUNCTION` | `internal/executor/operators_ddl.go::execDropFunction:1691` | partial | same |
| `CREATE TYPE` | `internal/executor/operators_ddl.go::execCreateType:2173` | partial | range / enum sub-catalogs missing |
| `CREATE TRIGGER` | `internal/executor/operators_ddl.go::execCreateTrigger:1895` | partial | pg_trigger heap-page write |
| `DROP TRIGGER` | `internal/executor/operators_ddl.go::execDropTrigger:1921` | partial | same |
| `CREATE RULE` | new | missing | dispatch missing in `operators_ddl.go` |
| `DROP RULE` | new | missing | same |
| `CREATE/ALTER/DROP ROLE` | new `internal/executor/operators_role.go` | missing | [`04-`](04-shared-catalog-bootstrap.md) §"Recommended Go API"; cluster-wide SI |
| `GRANT/REVOKE membership` | new | missing | same, four indexes |
| `CREATE/DROP DATABASE` | `internal/catalog/catalog.go::CreateDatabase:575`, `internal/server/database_ddl.go` | partial | add `XLOG_DBASE_CREATE_WAL_LOG` + pg_database heap-row WAL |
| `ALTER DATABASE` | new | missing | `XLOG_HEAP_INPLACE` path for datfrozenxid (vacuum) |
| `CREATE/DROP SCHEMA` | new | missing | pg_namespace handlers |
| `CREATE/DROP TABLESPACE` | new `internal/commands/tablespace` | missing | `XLOG_TBLSPC_CREATE`/`_DROP`, symlink mgmt |
| `CREATE/DROP SUBSCRIPTION` | `internal/executor/operators_ddl.go::execCreateSubscription:189` | partial | canonical pg_subscription heap WAL |
| `OPCLASS/OPFAMILY` ops | new | missing | wire `DefineOpClass`/`AlterOpFamily` paths |
| `CAST` ops | new | missing | |
| `COLLATION` ops | new | missing | |
| `CONVERSION` ops | new | missing | |
| `AGGREGATE` ops | new | missing | |
| `INSERT/UPDATE/DELETE` (user) | `internal/executor/operators_dml.go` + `internal/wal/` | partial | finish HOT + `XLOG_HEAP2_VISIBLE` emission |
| `CHECKPOINT` | `internal/wal/checkpointer.go::runCheckpoint:299` | partial | call `updateControlFile` post-flush (see [`02-`](02-pg-control-and-checkpoint.md) §"Continuous-maintenance gaps") |
| `RESTART POINT` | new in standby loop | missing | new caller in `internal/wal/recovery.go` |
| `VACUUM` | `internal/executor/operators_vacuum.go`, `internal/vacuum/vacuum.go` | partial | finish `XLOG_HEAP2_PRUNE_*` and `_VISIBLE` records |
| `VACUUM FREEZE` | same | partial | same |
| `VACUUM FULL` / `CLUSTER` | `internal/executor/operators_cluster.go` | missing | relmap wiring; new relfile + `XLOG_RELMAP_UPDATE` |
| `REINDEX` | new | missing | mapped-index relmap path |
| `ANALYZE` | `internal/executor/operators_analyze.go::analyzeRelationCtx:113` | partial | `XLOG_HEAP_INPLACE` on disk pg_class row |
| `pg_create_physical_replication_slot` | `internal/wal/slots.go::Create:137` | partial | switch to PG-binary `SaveSlotToPath` format ([`09-`](09-streaming-replication-readiness.md) §"What goopg must produce" row "Slot file format") |
| `pg_create_logical_replication_slot` | `internal/wal/slots.go::CreateLogical:171` | partial | same |
| `pg_replication_slot_advance` | `internal/wal/slots.go::AdvanceConfirmedFlushLSN:257` | partial | same |
| `pg_drop_replication_slot` | `internal/wal/slots.go::Drop:206` | done | — |
| `pg_promote()` | `cmd/goopg/standby.go::runPromote:178` | partial | add `XLOG_END_OF_RECOVERY` emit + `pg_control` state rewrite |
| `XLOG_PARAMETER_CHANGE` emission | new `internal/wal/parameter_change.go` | missing | postmaster-startup + SIGHUP hook ([`02-`](02-pg-control-and-checkpoint.md) §"Recommended Go API") |
| `Backup start` | `internal/server/basebackup.go::replyBaseBackup:103` | partial | set `backupStartPoint`, `backupEndRequired=true` |
| `Backup stop` | same | partial | emit `XLOG_BACKUP_END` and clear backup fields |

## Verification

The per-artefact docs (01–09) each define byte-level diffs. This
file's verification surface is **per-operation behavioural**: every
operation must replay safely onto a vanilla PG18 standby attached to
the goopg primary, without FATAL or PANIC on the standby side.

The smoke harness — one entry per operation — pairs a goopg primary
with a vanilla PG18 standby in the same test process. The shared
fixture is:

```bash
goopg init -D /tmp/p && goopg start -D /tmp/p -p 5533 &
pg_basebackup -h 127.0.0.1 -p 5533 -D /tmp/s -X stream -R
PGCTLTIMEOUT=10 pg_ctl -D /tmp/s -l /tmp/s.log -o '-p 5534' start
```

Per-operation smoke (each line is a single `psql -p 5533 -c '…'`
against the goopg primary, followed by a `psql -p 5534` assertion
against the standby):

| Op | Primary command | Standby assertion |
|---|---|---|
| `CREATE TABLE` | `CREATE TABLE t(i int);` | `SELECT 1 FROM pg_class WHERE relname='t'` returns 1 |
| `CREATE INDEX` | `CREATE INDEX i_t ON t(i);` | `\d t` shows the index; `EXPLAIN SELECT * FROM t WHERE i=1` uses it |
| `DROP TABLE` | `DROP TABLE t;` | `SELECT 1 FROM pg_class WHERE relname='t'` returns 0 |
| `TRUNCATE` | `TRUNCATE t;` | `SELECT count(*) FROM t` returns 0 |
| `ALTER TABLE ADD COLUMN` | `ALTER TABLE t ADD c text;` | `\d t` shows column c |
| `ALTER TABLE DROP COLUMN` | `ALTER TABLE t DROP c;` | `\d t` no longer shows c |
| `ALTER TABLE ALTER COLUMN TYPE` | `ALTER TABLE t ALTER i TYPE bigint;` | `\d t` shows bigint |
| `ALTER TABLE RENAME` | `ALTER TABLE t RENAME TO t2;` | `\d t2` succeeds |
| `CREATE VIEW` | `CREATE VIEW v AS SELECT 1;` | `SELECT * FROM v` returns 1 |
| `DROP VIEW` | `DROP VIEW v;` | `\dv` empty |
| `CREATE FUNCTION` | `CREATE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';` | `SELECT f()` returns 1 |
| `DROP FUNCTION` | `DROP FUNCTION f();` | `\df` no longer shows f |
| `CREATE TYPE` | `CREATE TYPE ct AS (a int);` | `\dT ct` succeeds |
| `CREATE TRIGGER` | `CREATE TRIGGER tr BEFORE INSERT ON t2 EXECUTE FUNCTION f();` | `\dy` / `\d t2` shows tr |
| `CREATE ROLE` | `CREATE ROLE r;` | `\du` shows r |
| `GRANT … TO r` | `GRANT pg_read_all_data TO r;` | `pg_auth_members` row visible |
| `CREATE DATABASE` | `CREATE DATABASE d;` | `\l` shows d on standby |
| `ALTER DATABASE` | `ALTER DATABASE d CONNECTION LIMIT 5;` | `\l+` shows limit |
| `CREATE SCHEMA` | `CREATE SCHEMA s;` | `\dn` shows s |
| `CREATE TABLESPACE` | `CREATE TABLESPACE ts LOCATION '/tmp/ts';` | `pg_tblspc/<oid>` symlink present on standby |
| `CREATE SUBSCRIPTION` | `CREATE SUBSCRIPTION sub CONNECTION '...' PUBLICATION p WITH (connect=false);` | `pg_subscription` row visible |
| `OPCLASS/OPFAMILY` | `ALTER OPERATOR FAMILY int4_ops USING btree ADD OPERATOR 1 < (text,text);` | `pg_amop` row visible |
| `CAST` | `CREATE CAST (int AS text) WITH INOUT;` | `pg_cast` row visible |
| `COLLATION` | `CREATE COLLATION cc (LOCALE = 'C');` | `\dOS+` shows cc |
| `CONVERSION` | `CREATE DEFAULT CONVERSION dc FOR 'UTF8' TO 'LATIN1' FROM iso8859_1_to_utf8;` | `pg_conversion` row visible |
| `AGGREGATE` | `CREATE AGGREGATE agg(int) (SFUNC=int4pl, STYPE=int);` | `\da` shows agg |
| `INSERT/UPDATE/DELETE` | three statements against t2 | rowcounts match on standby |
| `CHECKPOINT` | `CHECKPOINT;` | `pg_controldata /tmp/s` `Latest checkpoint location` advances |
| `VACUUM` | `VACUUM t2;` | `pg_class.relfrozenxid` for t2 advances on standby |
| `VACUUM FREEZE` | `VACUUM FREEZE t2;` | every tuple shows `HEAP_XMIN_FROZEN` via `pageinspect` |
| `VACUUM FULL` | `VACUUM FULL t2;` | `pg_relation_filenode('t2')` changed; standby `\d t2` still works |
| `REINDEX` | `REINDEX INDEX i_t;` | standby `\d t2` still shows index |
| `ANALYZE` | `ANALYZE t2;` | `pg_class.reltuples` for t2 matches primary |
| `pg_create_physical_replication_slot` | `SELECT pg_create_physical_replication_slot('s1');` | standby start with `primary_slot_name='s1'` succeeds without FATAL |
| `pg_replication_slot_advance` | `SELECT pg_replication_slot_advance('s1', pg_current_wal_lsn());` | restart_lsn matches on `pg_replication_slots` |
| `pg_drop_replication_slot` | `SELECT pg_drop_replication_slot('s1');` | `pg_replslot/s1/` absent |
| `pg_promote` (on the *standby*) | `pg_ctl -D /tmp/s promote` | `/tmp/s/pg_wal/00000002.history` written; standby returns `f` from `pg_is_in_recovery()` |
| `XLOG_PARAMETER_CHANGE` | `ALTER SYSTEM SET max_connections=200; SELECT pg_reload_conf();` then restart primary | standby's `pg_controldata` shows 200 |
| `Backup start/stop` | `pg_basebackup -h 127.0.0.1 -p 5533 -D /tmp/clone -X stream` | `/tmp/clone/global/pg_control` parseable; `backup_label`/`tablespace_map` correctly **absent** post-stop |

The umbrella test
`internal/testport/e2e_failover_goopg_to_pg_test.go::TestE2E_FailoverGoopgToPG/async`
(see [`09-`](09-streaming-replication-readiness.md) §"Verification"
row 8) chains a subset of these into one run. Each new operation row
in the matrix should add or extend a `t.Run("op-name", …)` subtest
so a future regression on any one operation lands a named, focused
failure rather than a generic FATAL on the standby log.
