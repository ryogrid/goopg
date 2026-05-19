# 04 — Shared Catalogs and Critical Shared Indexes

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility); supersedes the
ad-hoc `.ralph/fix_plan.md` step-3{cf,ch,cu,cx,dh} reactive seeds for
shared catalogs.

---

## Scope

This file specifies the byte-level, runtime-stable state of every
**shared** system catalog living under `$PGDATA/global/` that a vanilla
PG18 backend opens before, or immediately after, it manages to read the
relcache init file — i.e. the five `formrdesc`-nailed catalogs
(pg_database, pg_authid, pg_auth_members, pg_shseclabel,
pg_subscription) and the six `load_critical_index`-nailed indexes that
the catcache cannot function without.

In scope:

- Heap-tuple inventory for every nailed shared catalog at initdb time:
  OID, rowtype OID, toast pair, per-column type/alignment/initdb
  value, full row list where rows are deterministic.
- The six **critical shared indexes**, key columns / opclasses,
  on-disk leaf-page contents at initdb time.
- Secondary shared indexes that round out the nailed-rel set
  (`pg_auth_members_role_member_index`,
  `pg_subscription_oid_index`, `pg_subscription_subname_index`,
  `pg_auth_members_oid_index`, `pg_auth_members_grantor_index`).
- Heap-tuple layout details that bit M0106:
  `t_infomask` bits, null bitmap, varlena encoding for
  `aclitem[]`, `text[]`, `pg_node_tree`, `name`.
- Phase-2 nailed-rel walk in
  `RelationCacheInitializePhase2` (`src/backend/utils/cache/relcache.c:4073-4087`)
  and the Phase-3 critical-shared-index walk
  (`relcache.c:4211-4229`).
- Continuous-maintenance rules for every PG DDL that mutates one of
  the above: which WAL records fire, which relmap or invalidation
  messages must be emitted alongside.

Out of scope (covered elsewhere):

- The local nailed catalogs pg_class, pg_attribute, pg_proc, pg_type
  and their seven critical indexes — see
  [`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md).
- Non-nailed *local* catalogs and the opfamily/opclass/amop rows
  needed for index access methods — see
  [`06-bki-derived-catalog-seeds.md`](06-bki-derived-catalog-seeds.md).
- View definitions (pg_rewrite, system_views.sql) — see
  [`07-system-views-and-pg-rewrite.md`](07-system-views-and-pg-rewrite.md).
- `global/pg_internal.init` byte layout — see
  [`08-relcache-init-and-version-files.md`](08-relcache-init-and-version-files.md).
- `global/pg_filenode.map` byte layout — see
  [`01-data-directory-layout.md`](01-data-directory-layout.md) §"Shared
  relmap".

Non-nailed shared catalogs (pg_tablespace, pg_shdepend,
pg_shdescription, pg_db_role_setting) live under `global/` but are
*not* on the Phase-2 critical path. A brief summary appears under
"Non-nailed shared catalogs" below; their full row-level seeds belong
to `06-`.

---

## Upstream references

All paths are relative to `postgres/` in the goopg repo. Line numbers
are against vendored PG 18.3.

| Symbol | File:line |
|---|---|
| `RelationCacheInitializePhase2` (nailed-shared walk) | `src/backend/utils/cache/relcache.c:4073` |
| `formrdesc` | `src/backend/utils/cache/relcache.c:1894` |
| `load_critical_index` | `src/backend/utils/cache/relcache.c:4392` |
| Critical-shared-index loop | `src/backend/utils/cache/relcache.c:4211-4229` |
| `NUM_CRITICAL_SHARED_RELS` (5) | `src/backend/utils/cache/relcache.c:4086` |
| `NUM_CRITICAL_SHARED_INDEXES` (6) | `src/backend/utils/cache/relcache.c:4226` |
| `pg_database` catalog struct | `src/include/catalog/pg_database.h:29` |
| `pg_authid` catalog struct | `src/include/catalog/pg_authid.h:31` |
| `pg_auth_members` catalog struct | `src/include/catalog/pg_auth_members.h:30` |
| `pg_shseclabel` catalog struct | `src/include/catalog/pg_shseclabel.h:28` |
| `pg_subscription` catalog struct | `src/include/catalog/pg_subscription.h:43` |
| `pg_database.dat` seed | `src/include/catalog/pg_database.dat` (1 row: `template1`) |
| `pg_authid.dat` seed | `src/include/catalog/pg_authid.dat` (17 rows incl. bootstrap) |
| `Template1DbOid` (1) | `src/include/catalog/pg_database_d.h:58` |
| `Template0DbOid` (4) | `src/include/catalog/pg_database_d.h:29` |
| `PostgresDbOid` (5) | `src/include/catalog/pg_database_d.h:30` |
| `BOOTSTRAP_SUPERUSERID` (10) | `src/include/catalog/pg_authid.dat:23` |
| `DEFAULTTABLESPACE_OID` (1663) | `src/include/catalog/pg_tablespace_d.h:42` |
| `GLOBALTABLESPACE_OID` (1664) | `src/include/catalog/pg_tablespace_d.h:43` |
| `RelationMapUpdateMap` | `src/backend/utils/cache/relmapper.c:325` |
| `write_relmap_file` | `src/backend/utils/cache/relmapper.c:889` |
| `XLOG_RELMAP_UPDATE` redo | `src/backend/utils/cache/relmapper.c:1096` |
| `CacheInvalidateHeapTuple` | `src/backend/utils/cache/inval.c:1571` |
| `RegisterCatcacheInvalidation` | `src/backend/utils/cache/inval.c:604` |
| `RegisterRelcacheInvalidation` | `src/backend/utils/cache/inval.c:632` |
| `XLOG_HEAP_INSERT` emit | `src/backend/access/heap/heapam.c:2164` |
| `XLOG_HEAP_DELETE` emit | `src/backend/access/heap/heapam.c:3136` |
| `XLOG_HEAP_UPDATE` emit | `src/backend/access/heap/heapam.c:8873` |
| `XLOG_HEAP2_VISIBLE` emit | `src/backend/access/heap/heapam.c:8837` |
| `visibilitymap_set` | `src/backend/access/heap/visibilitymap.c:246` |
| `createdb` | `src/backend/commands/dbcommands.c:684` |
| `dropdb` | `src/backend/commands/dbcommands.c:1673` |
| `AlterDatabase` | `src/backend/commands/dbcommands.c:2368` |
| `AlterDatabaseSet` | `src/backend/commands/dbcommands.c:2638` |
| `vac_update_datfrozenxid` | `src/backend/commands/vacuum.c:1624` |
| `CreateRole` | `src/backend/commands/user.c:132` |
| `AlterRole` | `src/backend/commands/user.c:619` |
| `DropRole` | `src/backend/commands/user.c:1090` |
| `AddRoleMems` | `src/backend/commands/user.c:1681` |
| `ExecSecLabelStmt` | `src/backend/commands/seclabel.c:115` |
| `SetSharedSecurityLabel` | `src/backend/commands/seclabel.c:329` |
| `CreateSubscription` | `src/backend/commands/subscriptioncmds.c:539` |
| `AlterSubscription` | `src/backend/commands/subscriptioncmds.c:1100` |
| `DropSubscription` | `src/backend/commands/subscriptioncmds.c:1626` |

---

## Initdb-time output

### Phase 2 nailed-rel inventory

Five shared catalogs are unconditionally built by
`formrdesc()` inside `RelationCacheInitializePhase2`
(`relcache.c:4075-4084`) when the shared relcache init file is
missing or rejected. Every backend at startup needs these five
descriptors before any catcache lookup is legal.

| Relation | OID | Rowtype OID | Toast table / index OID | Citation |
|---|---|---|---|---|
| pg_database | 1262 | 1248 | 4177 / 4178 | `pg_database.h:29`, `pg_database.h:98` |
| pg_authid | 1260 | 2842 | — (no varlen since rolpassword/rolvaliduntil are `text`+`timestamptz`, but rolpassword is short-enough varlena; PG18 declares no toast pair for pg_authid) | `pg_authid.h:31`, `pg_authid.h:58-59` |
| pg_auth_members | 1261 | 2843 | — (no varlen columns) | `pg_auth_members.h:30` |
| pg_shseclabel | 3592 | 4066 | 4060 / 4061 | `pg_shseclabel.h:28`, `pg_shseclabel.h:42` |
| pg_subscription | 6100 | 6101 | 4183 / 4184 | `pg_subscription.h:43`, `pg_subscription.h:101` |

Shared-relmap entry: every nailed shared catalog uses
`reltablespace = GLOBALTABLESPACE_OID = 1664`
(`pg_tablespace_d.h:43`) and is mapped through
`global/pg_filenode.map`, never through a `pg_class.relfilenode`
read. Goopg's relmap construction lives in
`internal/initdb/initdb.go::makeSharedRelMapFile`
(see `initdb.go:4172-4180`).

### Critical-shared-index inventory

Six indexes are loaded via `load_critical_index` in
`relcache.c:4213-4224` immediately after the nailed-rel descriptors,
before any catcache scan is allowed. These must exist on disk as valid
btree files at `global/<oid>` even if the leaves are empty, *and* must
contain populated leaves for the keys backing AUTHNAME / AUTHOID /
DATABASENAME / DATABASEOID syscaches — otherwise the catcache reports
a miss and the backend FATALs.

| Index | OID | On relation | Key columns (opclass) | Critical? | Citation |
|---|---|---|---|---|---|
| pg_database_datname_index | 2671 | pg_database (1262) | `(datname name_ops)` | yes (DATABASENAME) | `pg_database.h:100`, `relcache.c:4213` |
| pg_database_oid_index | 2672 | pg_database (1262) | `(oid oid_ops)` PKEY | yes (DATABASEOID) | `pg_database.h:101`, `relcache.c:4215` |
| pg_authid_rolname_index | 2676 | pg_authid (1260) | `(rolname name_ops)` | yes (AUTHNAME) | `pg_authid.h:58`, `relcache.c:4217` |
| pg_authid_oid_index | 2677 | pg_authid (1260) | `(oid oid_ops)` PKEY | yes (AUTHOID) | `pg_authid.h:59`, `relcache.c:4219` |
| pg_auth_members_member_role_index | 2695 | pg_auth_members (1261) | `(member oid_ops, roleid oid_ops, grantor oid_ops)` | yes (AUTHMEMMEMROLE) | `pg_auth_members.h:50`, `relcache.c:4221` |
| pg_shseclabel_object_index | 3593 | pg_shseclabel (3592) | `(objoid oid_ops, classoid oid_ops, provider text_ops)` PKEY | yes (no syscache, but rel has no other indexes) | `pg_shseclabel.h:44`, `relcache.c:4223` |

Ancillary (non-critical) indexes belonging to the nailed shared
relations are also opened during normal operation and therefore must
exist on disk:

| Index | OID | On relation | Key columns | Citation |
|---|---|---|---|---|
| pg_auth_members_oid_index | 6303 | pg_auth_members | `(oid oid_ops)` PKEY | `pg_auth_members.h:48` |
| pg_auth_members_role_member_index | 2694 | pg_auth_members | `(roleid, member, grantor)` (AUTHMEMROLEMEM) | `pg_auth_members.h:49` |
| pg_auth_members_grantor_index | 6302 | pg_auth_members | `(grantor oid_ops)` (non-unique) | `pg_auth_members.h:51` |
| pg_subscription_oid_index | 6114 | pg_subscription | `(oid oid_ops)` PKEY (SUBSCRIPTIONOID) | `pg_subscription.h:103` |
| pg_subscription_subname_index | 6115 | pg_subscription | `(subdbid oid_ops, subname name_ops)` (SUBSCRIPTIONNAME) | `pg_subscription.h:104` |

### Per-catalog row tables

#### pg_database (OID 1262)

Schema (18 columns, fixed-length prefix 1-12, varlena tail 13-18) per
`pg_database.h:29-89`. Alignment is x86_64 (`MAXIMUM_ALIGNOF = 8`).
The fixed prefix occupies exactly 64 bytes; the varlena tail starts
on the next 8-byte boundary after the null bitmap.

| # | Column | Type | typalign | Initdb value |
|---|---|---|---|---|
| 1 | oid | Oid | i (4) | `{1, 4, 5}` (Template1DbOid, Template0DbOid, PostgresDbOid) |
| 2 | datname | NameData (64) | c (1) | `{"template1", "template0", "postgres"}` |
| 3 | datdba | Oid | i (4) | `BOOTSTRAP_SUPERUSERID = 10` |
| 4 | encoding | int4 | i (4) | GUC `encoding`; PG_UTF8 = 6 by default |
| 5 | datlocprovider | char | c (1) | `'c'` (libc) or `'i'` (ICU) per initdb flags |
| 6 | datistemplate | bool | c (1) | template1=`t`, template0=`t`, postgres=`f` |
| 7 | datallowconn | bool | c (1) | template1=`t`, template0=`f`, postgres=`t` |
| 8 | dathasloginevt | bool | c (1) | `f` for all three (PG18 addition; `pg_database.h:53`) |
| 9 | datconnlimit | int4 | i (4) | `-1` (`DATCONNLIMIT_UNLIMITED`, `pg_database.h:117`) |
| 10 | datfrozenxid | TransactionId | i (4) | `FirstNormalTransactionId = 3` |
| 11 | datminmxid | TransactionId | i (4) | `FirstMultiXactId = 1` |
| 12 | dattablespace | Oid | i (4) | `DEFAULTTABLESPACE_OID = 1663` |
| 13 | datcollate | text (varlena) | i (4) | LC_COLLATE; `"C"` for `--locale=C` |
| 14 | datctype | text (varlena) | i (4) | LC_CTYPE; `"C"` for `--locale=C` |
| 15 | datlocale | text (varlena) | i (4) | NULL for libc, locale-id string for ICU |
| 16 | daticurules | text (varlena) | i (4) | NULL unless `--icu-rules` was used |
| 17 | datcollversion | text (varlena) | i (4) | NULL (BKI_DEFAULT, `pg_database.h:84`) |
| 18 | datacl | aclitem[] (varlena) | i (4) | NULL (means default public-CONNECT-and-TEMP) |

All three rows fit on heap block 0. Goopg writes them at offsets
1 and 2 (template1, postgres) today; `template0` (OID 4) is currently
**missing**.

#### pg_authid (OID 1260)

Schema (12 columns) per `pg_authid.h:31-49`. Fixed prefix 1-10
(76 bytes), varlena tail 11-12.

| # | Column | Type | typalign | Initdb value |
|---|---|---|---|---|
| 1 | oid | Oid | i | per `pg_authid.dat` |
| 2 | rolname | NameData | c | per `pg_authid.dat`; bootstrap row's `POSTGRES` is overwritten by initdb with the OS-supplied `--username` |
| 3 | rolsuper | bool | c | bootstrap=`t`, all 16 predefined roles=`f` |
| 4 | rolinherit | bool | c | bootstrap=`t`, predefined=`t` |
| 5 | rolcreaterole | bool | c | bootstrap=`t`, predefined=`f` |
| 6 | rolcreatedb | bool | c | bootstrap=`t`, predefined=`f` |
| 7 | rolcanlogin | bool | c | bootstrap=`t`, predefined=`f` |
| 8 | rolreplication | bool | c | bootstrap=`t`, predefined=`f` |
| 9 | rolbypassrls | bool | c | bootstrap=`t`, predefined=`f` |
| 10 | rolconnlimit | int4 | i | `-1` for all rows |
| 11 | rolpassword | text (varlena) | i | NULL for all rows |
| 12 | rolvaliduntil | timestamptz (8) | d (8) | NULL for all rows |

The bootstrap row's `rolname` field is rewritten by
`bootstrap.c::AuxiliaryProcessMain` to the OS `--username`, which is
why upstream's `pg_authid.dat` stores `"POSTGRES"` (uppercase) as a
placeholder. Goopg uses `"postgres"` directly for OID 10 and adds a
second row at `FirstNormalObjectId = 16384` for `$USER` when
`$USER != "postgres"`
(`internal/initdb/initdb.go:716-720`).

The complete initdb row list per `pg_authid.dat` (17 rows):

| OID | rolname | Notes |
|---|---|---|
| 10 | (bootstrap user, rewritten from `POSTGRES`) | superuser; only `rolsuper=t` row |
| 6171 | pg_database_owner | `ROLE_PG_DATABASE_OWNER` |
| 6181 | pg_read_all_data | `ROLE_PG_READ_ALL_DATA` |
| 6182 | pg_write_all_data | `ROLE_PG_WRITE_ALL_DATA` |
| 3373 | pg_monitor | `ROLE_PG_MONITOR` |
| 3374 | pg_read_all_settings | `ROLE_PG_READ_ALL_SETTINGS` |
| 3375 | pg_read_all_stats | `ROLE_PG_READ_ALL_STATS` |
| 3377 | pg_stat_scan_tables | `ROLE_PG_STAT_SCAN_TABLES` |
| 4569 | pg_read_server_files | `ROLE_PG_READ_SERVER_FILES` |
| 4570 | pg_write_server_files | `ROLE_PG_WRITE_SERVER_FILES` |
| 4571 | pg_execute_server_program | `ROLE_PG_EXECUTE_SERVER_PROGRAM` |
| 4200 | pg_signal_backend | `ROLE_PG_SIGNAL_BACKEND` |
| 4544 | pg_checkpoint | `ROLE_PG_CHECKPOINT` |
| 6337 | pg_maintain | `ROLE_PG_MAINTAIN` |
| 4550 | pg_use_reserved_connections | `ROLE_PG_USE_RESERVED_CONNECTIONS` |
| 6304 | pg_create_subscription | `ROLE_PG_CREATE_SUBSCRIPTION` |
| 6392 | pg_signal_autovacuum_worker | `ROLE_PG_SIGNAL_AUTOVACUUM_WORKER` |

The pg_dot-prefixed predefined-role rows are inherited-by-default
membership targets for `GRANT pg_read_all_data TO …` and friends;
their absence does not block boot but blocks any `GRANT pg_X TO Y`
statement at execution time.

#### pg_auth_members (OID 1261)

Schema (7 columns, all fixed-length) per `pg_auth_members.h:30-39`.
No varlena tail — `t_infomask` should not set `HEAP_HASVARWIDTH`.

| # | Column | Type | typalign | Initdb value |
|---|---|---|---|---|
| 1 | oid | Oid | i | (no initdb rows) |
| 2 | roleid | Oid | i | — |
| 3 | member | Oid | i | — |
| 4 | grantor | Oid | i | — |
| 5 | admin_option | bool | c | — |
| 6 | inherit_option | bool | c | — |
| 7 | set_option | bool | c | — |

Row inventory at initdb: **empty**. Memberships of predefined roles
into each other are not seeded by `pg_auth_members.dat`; they are
added at runtime through `GRANT` statements in `system_views.sql`
during `initdb`'s post-bootstrap SQL phase, which produces zero
membership rows on a `--locale=C` cluster.

#### pg_shseclabel (OID 3592)

Schema (4 columns) per `pg_shseclabel.h:28-38`.

| # | Column | Type | typalign | Initdb value |
|---|---|---|---|---|
| 1 | objoid | Oid | i | (no initdb rows) |
| 2 | classoid | Oid | i | — |
| 3 | provider | text (varlena) | i | — |
| 4 | label | text (varlena) | i | — |

Row inventory at initdb: **empty**. Rows appear only when a SECURITY
LABEL extension (e.g. `sepgsql`) is loaded and labels a database /
role / tablespace.

#### pg_subscription (OID 6100)

Schema (18 columns, fixed-length prefix 1-12, varlena tail 13-17,
NameData `subslotname` at column 15 stored in-line as a
NameData-sized varlena per PG18) per `pg_subscription.h:43-97`.

| # | Column | Type | typalign | Initdb value |
|---|---|---|---|---|
| 1 | oid | Oid | i | (no initdb rows) |
| 2 | subdbid | Oid | i | — |
| 3 | subskiplsn | XLogRecPtr (uint64) | d (8) | — |
| 4 | subname | NameData | c | — |
| 5 | subowner | Oid | i | — |
| 6 | subenabled | bool | c | — |
| 7 | subbinary | bool | c | — |
| 8 | substream | char | c | — |
| 9 | subtwophasestate | char | c | — |
| 10 | subdisableonerr | bool | c | — |
| 11 | subpasswordrequired | bool | c | — |
| 12 | subrunasowner | bool | c | — |
| 13 | subfailover | bool | c | — |
| 14 | subconninfo | text | i | — |
| 15 | subslotname | NameData (BKI_FORCE_NULL) | c | — |
| 16 | subsynccommit | text | i | — |
| 17 | subpublications | text[] | i | — |
| 18 | suborigin | text | i | — |

Row inventory at initdb: **empty**.

### Heap-tuple layout notes

Every shared-catalog heap row goopg writes must obey PG's HeapTuple
on-disk invariants:

- **HeapTupleHeader length / hoff:** the header is 23 bytes + null
  bitmap padded up to 8-byte alignment. `t_hoff` records the byte
  offset of the first column. Bitmap presence is required whenever any
  attribute is NULL (e.g. `pg_database.datlocale`, `daticurules`,
  `datcollversion`, `datacl`).
- **`HEAP_HASNULL` (`0x0001`)** must be set in `t_infomask` whenever
  the null bitmap is present. Goopg omits it for the postgres-role
  row (no NULLs) but sets it for pg_database (4 NULLs in the libc
  case).
- **`HEAP_HASVARWIDTH` (`0x0002`)** must be set whenever any attribute
  has `attlen < 0` (i.e. `text`, `aclitem[]`, `text[]`, `pg_node_tree`,
  or any toastable type). pg_database, pg_authid (`rolpassword`),
  pg_shseclabel, pg_subscription all set this bit; pg_auth_members
  must NOT set it.
  Failure mode: `nocachegetattr` (`src/backend/access/common/heaptuple.c`)
  takes the wrong branch and trips `Assert(j > attnum)` on the next
  syscache lookup that touches a varlena attribute. M0106-0010 step
  3ct documents the pg_database manifestation.
- **`HEAP_XMIN_FROZEN` / `HEAP_XMIN_COMMITTED`:** initdb rows are
  inserted by `BootstrapTransactionId = 1`; PG sets
  `HEAP_XMIN_FROZEN = HEAP_XMIN_COMMITTED | HEAP_XMIN_INVALID = 0x0700`
  to indicate the tuple is permanently visible without consulting
  CLOG. Goopg's `storage.NewHeapTuple` currently writes
  `xmin = 1, xmax = 0` but does NOT set `HEAP_XMIN_FROZEN`; if a
  vanilla backend's CLOG initialization races the first visibility
  check, the row reads as in-doubt.
- **`name` encoding:** NameData is a fixed 64-byte zero-padded blob,
  with `typalign = 'c'` so it follows the previous attribute without
  padding. `btnamecmp` does an unsigned-byte compare of all 64 bytes,
  so the trailing zero padding must be present.
- **`aclitem[]` encoding:** PG18 stores `aclitem` as a 12-byte fixed
  struct (`{grantor, grantee, privs, goptions}`); `aclitem[]` is an
  `ArrayType` varlena with `elemtype = ACLITEMOID (1033)`,
  `typalign = 'i'`. NULL `datacl` indicates "default ACL" — *not*
  `'{}'`. Goopg currently writes NULL for the initdb rows, matching
  PG.
- **`text[]` encoding:** `ArrayType` varlena with `elemtype = TEXTOID
  (25)`, `typalign = 'i'`. Each element is a long-form varlena
  (4-byte header + payload) packed onto a 4-byte boundary.
  Implementation reference: goopg's `textArrayBytes`
  (`internal/initdb/initdb.go:1545`).
- **`pg_node_tree`** does not appear in any shared catalog and is
  out of scope for this file (it appears in pg_proc.proargdefaults,
  pg_constraint.conbin, etc. — see `05-`).
- **TOAST detoasting:** none of the initdb rows above are large
  enough to be toasted (the longest varlena is `datcollate = "C"`
  at 5 bytes total). However, the toast pair OIDs in the inventory
  table must exist on disk as **empty** files because
  `RelationGetIndexList` may peek at `reltoastrelid` and try to
  `mdopen` the toast index even when no detoast is requested.

### Non-nailed shared catalogs

Four further shared catalogs live under `global/` but are not on
`RelationCacheInitializePhase2`'s critical path. They are loaded
through the normal relcache path after the init file is read.

- **pg_tablespace (OID 1213).** Schema in `pg_tablespace.h:29-42`
  (oid, spcname, spcowner, spcacl, spcoptions). Initdb seeds two
  rows: `pg_default = 1663` and `pg_global = 1664`
  (`pg_tablespace_d.h:42-43`). Indexes: pg_tablespace_oid_index
  (2697) and pg_tablespace_spcname_index (2698). Both indexes are
  exercised by relcache for any non-default-tablespace relation. The
  initdb rows must materialise the two default tablespace entries
  even though no on-disk `pg_tblspc/<oid>` symlink exists for them.
- **pg_shdepend (OID 1214).** Schema in `pg_shdepend.h:38-54`. Row
  inventory: empty at initdb (`pg_shdepend.h` comment lines 5-9).
  Index: pg_shdepend_depender_index (1232), pg_shdepend_reference_index
  (1233). Populated at runtime by every `GRANT … TO role`, role
  drop, database drop.
- **pg_shdescription (OID 2396).** Schema in `pg_shdescription.h:40-48`.
  Initdb rows: object comments for the bootstrap role and predefined
  roles, generated by `genbki.pl` from `descr =>` entries in
  `pg_authid.dat` and `pg_database.dat`. Toast pair 2846/2847.
- **pg_db_role_setting (OID 2964).** Schema in
  `pg_db_role_setting.h:34-45`. Row inventory: empty at initdb.
  Populated by `ALTER ROLE … SET parameter = value` and
  `ALTER DATABASE … SET parameter = value`. Toast pair 2966/2967;
  index pg_db_role_setting_databaseid_rol_index (2965).

These four catalogs are not critical for *standby boot* — PG's
Phase 2 walk does not reference them — so a standby can attach
even if their heap files are zero-row, provided the relmap entry
and an empty btree placeholder exist on disk for each declared
index.

---

## Continuous maintenance

A primary backend that mutates a row in any shared catalog must, in
the same critical section, (a) WAL-log the heap change, (b) WAL-log
every affected index, (c) optionally WAL-log a visibility-map bit
clear, and (d) queue cache-invalidation messages so that every other
backend (including the walreceiver-driven standby) re-reads the row.
Shared catalogs additionally cross database boundaries: the SI queue
is dispatched cluster-wide, not just within `MyDatabaseId`.

### Per-DDL mutation table

| Operation | Catalogs touched | Indexes touched | WAL records | SI invalidation | Citation |
|---|---|---|---|---|---|
| `CREATE ROLE` | pg_authid +1 row; pg_shdepend (for OWNED BY); pg_db_role_setting (`SET … IN ROLE`) | pg_authid_oid_index, pg_authid_rolname_index; pg_shdepend_*; pg_db_role_setting_* | `XLOG_HEAP_INSERT` ×N + `XLOG_BTREE_INSERT_LEAF` ×N | `CacheInvalidateHeapTuple(AUTHOID, AUTHNAME)`, `SHAREDINVALRELCACHE_ID` cluster-wide | `commands/user.c:132` (`CreateRole`) |
| `ALTER ROLE` | pg_authid 1 row | both pg_authid indexes if `rolname` changes; otherwise none | `XLOG_HEAP_UPDATE` (+ index inserts/deletes when key changes) | `CacheInvalidateHeapTuple(AUTHOID)` | `commands/user.c:619` (`AlterRole`) |
| `DROP ROLE` | pg_authid −1 row; pg_auth_members rows owned by the role; pg_shdepend cleanup | both pg_authid indexes; all four pg_auth_members indexes | `XLOG_HEAP_DELETE` ×N + index deletes | `CacheInvalidateHeapTuple` for each tuple removed | `commands/user.c:1090` (`DropRole`) |
| `GRANT role TO member` | pg_auth_members +1 row | pg_auth_members_oid_index (6303), _role_member_index (2694), _member_role_index (2695), _grantor_index (6302) | `XLOG_HEAP_INSERT` + 4 × `XLOG_BTREE_INSERT_LEAF` | `CacheInvalidateHeapTuple(AUTHMEMROLEMEM, AUTHMEMMEMROLE)` | `commands/user.c:1681` (`AddRoleMems`) |
| `CREATE DATABASE` | pg_database +1 row; pg_shdepend (owner edge) | pg_database_oid_index (2672), pg_database_datname_index (2671); pg_shdepend_* | `XLOG_DBASE_CREATE_WAL_LOG` *or* `XLOG_DBASE_CREATE_FILE_COPY` for the file-tree clone (see `01-`); then `XLOG_HEAP_INSERT` for the pg_database row | `CacheInvalidateHeapTuple(DATABASEOID)` cluster-wide | `commands/dbcommands.c:684` (`createdb`) |
| `DROP DATABASE` | pg_database −1 row; pg_db_role_setting prune; pg_shdepend prune; pg_subscription disallow | pg_database_oid_index, pg_database_datname_index | `XLOG_HEAP_DELETE` + `XLOG_DBASE_DROP` for the file tree | cluster-wide DATABASEOID invalidation | `commands/dbcommands.c:1673` (`dropdb`) |
| `ALTER DATABASE … (datallowconn, datconnlimit, datistemplate)` | pg_database 1 row | none (no key change) | `XLOG_HEAP_UPDATE` | `CacheInvalidateHeapTuple(DATABASEOID)` | `commands/dbcommands.c:2368` (`AlterDatabase`) |
| `ALTER DATABASE … SET parameter` | pg_db_role_setting upsert | pg_db_role_setting_databaseid_rol_index (2965) | `XLOG_HEAP_INSERT` or `XLOG_HEAP_UPDATE` | none (no syscache) | `commands/dbcommands.c:2638` (`AlterDatabaseSet`) |
| `vac_update_datfrozenxid` | pg_database 1 row (`datfrozenxid`, `datminmxid`) | none | `XLOG_HEAP_INPLACE` (PG18: in-place update, no UPDATE WAL) | `CacheInvalidateHeapTupleInplace(DATABASEOID)` | `commands/vacuum.c:1624` (`vac_update_datfrozenxid`) |
| `SECURITY LABEL ON ROLE/DATABASE/TABLESPACE` | pg_shseclabel insert/update/delete | pg_shseclabel_object_index (3593) | `XLOG_HEAP_INSERT`/`UPDATE`/`DELETE` + index WAL | none (no syscache on pg_shseclabel) | `commands/seclabel.c:115` (`ExecSecLabelStmt`); `:329` (`SetSharedSecurityLabel`) |
| `CREATE SUBSCRIPTION` | pg_subscription +1 row; pg_shdepend (owner) | pg_subscription_oid_index (6114), pg_subscription_subname_index (6115) | `XLOG_HEAP_INSERT` + 2 × `XLOG_BTREE_INSERT_LEAF` | `CacheInvalidateHeapTuple(SUBSCRIPTIONOID, SUBSCRIPTIONNAME)` cluster-wide | `commands/subscriptioncmds.c:539` (`CreateSubscription`) |
| `ALTER SUBSCRIPTION` | pg_subscription 1 row | both indexes only if `subname` or `subdbid` changes | `XLOG_HEAP_UPDATE` (+ index churn on key change) | SUBSCRIPTIONOID invalidation | `commands/subscriptioncmds.c:1100` (`AlterSubscription`) |
| `DROP SUBSCRIPTION` | pg_subscription −1 row; pg_shdepend prune | both indexes | `XLOG_HEAP_DELETE` + 2 × index delete | SUBSCRIPTIONOID + SUBSCRIPTIONNAME cluster-wide | `commands/subscriptioncmds.c:1626` (`DropSubscription`) |

### WAL records

Every insert into a shared-catalog heap emits `XLOG_HEAP_INSERT`
(`heapam.c:2164`). Updates emit `XLOG_HEAP_UPDATE` (`heapam.c:8873`).
Deletes emit `XLOG_HEAP_DELETE` (`heapam.c:3136`). Every index page
mutation emits its own `XLOG_BTREE_*` record. The first time a page
becomes all-visible after a heap update, the heap WAL is followed by
`XLOG_HEAP2_VISIBLE` (`heapam.c:8837`; `visibilitymap.c:246`) which
sets the corresponding `_vm` fork bit. Visibility-map bits are
material for hot-standby visibility checks; without them the standby
cannot satisfy index-only scans on the affected catalog.

`XLOG_RELMAP_UPDATE` (`relmapper.c:1096`) is emitted whenever the
shared relmap or a per-database relmap is rewritten. For shared
catalogs the only triggers are `REINDEX` on a nailed shared index and
`VACUUM FULL pg_authid` (or any other nailed shared rel), both of
which rewrite the catalog's relfilenode and call
`RelationMapUpdateMap` (`relmapper.c:325`). At initdb time the shared
relmap is written by `write_relmap_file(..., write_wal = false, ...)`
(`relmapper.c:637`) — no WAL is needed because the cluster has no WAL
stream yet.

### Relmap considerations

Shared catalogs use the **global relmap** at `global/pg_filenode.map`,
not the per-database relmap. Their `pg_class.relfilenode` value is
always `0`; the actual `RelFileNumber` comes from the relmap. Goopg's
relmap construction is in `internal/initdb/initdb.go:4170-4184`
(see `makeRelMapFile` callsites). Any runtime operation that triggers
`RelationMapUpdateMap` for a shared catalog must also `XLogInsert` the
`XLOG_RELMAP_UPDATE` record before the file rewrite, so a crashed
standby can `relmap_redo` (`relmapper.c:1096`) the change.

### Cache invalidation

Catalog row mutations register two kinds of SI messages
(`inval.c:599-635`):

- `RegisterCatcacheInvalidation` (`inval.c:604`) — one message per
  affected syscache (e.g. AUTHOID, AUTHNAME, DATABASEOID,
  DATABASENAME, AUTHMEMROLEMEM, AUTHMEMMEMROLE, SUBSCRIPTIONOID,
  SUBSCRIPTIONNAME).
- `RegisterRelcacheInvalidation` (`inval.c:632`) — one message per
  affected pg_class row; shared catalogs have `dbId = InvalidOid` and
  therefore the message is dispatched **cluster-wide** (every
  database's backend processes it). Catcache invalidations on shared
  catalogs likewise use `dbId = InvalidOid`.

`CacheInvalidateHeapTuple` (`inval.c:1571`) is the single entry point
all DDL handlers must call after mutating any catalog row.
`CacheInvalidateHeapTupleInplace` (`inval.c:1593`) is the
`vac_update_datfrozenxid` variant.

---

## What goopg must produce

Existing seeding sites in `internal/initdb/`:

- pg_database heap rows: `bootstrapPostgresDatabase`
  (`internal/initdb/initdb.go:763-867`) writes template1 (OID 1) and
  postgres (OID 5) at heap block 0 offsets 1 and 2.
- pg_database indexes: `bootstrapPgDatabaseOidIndex`
  (`internal/initdb/btree_index_bootstrap.go:729`) and
  `bootstrapPgDatabaseDatnameIndex` (`btree_index_bootstrap.go:791`)
  write 2-page populated btrees at `global/2671` and `global/2672`.
- pg_authid heap rows: `bootstrapPostgresRole`
  (`internal/initdb/initdb.go:671-749`) writes the bootstrap superuser
  (OID 10) plus a `$USER`-named row at OID 16384 (when `$USER !=
  "postgres"`).
- pg_authid indexes: `bootstrapPgAuthidIndexes`
  (`internal/initdb/btree_index_bootstrap.go:1618`) builds 2-page
  populated btrees at `global/2676` and `global/2677` from the entries
  returned by `bootstrapPostgresRole`.
- All other shared-catalog heaps (`pg_auth_members`, `pg_shseclabel`,
  `pg_subscription`, plus non-nailed pg_tablespace, pg_shdepend,
  pg_shdescription, pg_db_role_setting, pg_replication_origin,
  pg_parameter_acl) are zeroed empty heap pages written by
  `bootstrapSharedCatalogHeaps` (`internal/initdb/initdb.go:482-498`).
- Shared critical index placeholders: empty btree root pages written
  in `bootstrapPostgresDatabase`'s critical-index loop
  (`internal/initdb/initdb.go:1084-1165`) covering OIDs 2671, 2672,
  2676, 2677, 2694, 2695, 3593 and ancillary 6114/6115/2697/2698/2965.
  The placeholders are overwritten with real data only for the four
  pg_database/pg_authid indexes above; the rest stay empty.

### Per-catalog diff

| Catalog | Heap rows | Indexes | Status |
|---|---|---|---|
| pg_database (1262) | template1 (OID 1), postgres (OID 5) | datname_index (2671), oid_index (2672) populated | **partial** — missing `template0` (OID 4) row; `dathasloginevt` written `false` but column 8 must respect the canonical `false` value coherently with the libc default (currently OK); `datfrozenxid = 3` matches `FirstNormalTransactionId`; `HEAP_HASNULL` + `HEAP_HASVARWIDTH` set (`initdb.go:851`). |
| pg_authid (1260) | OID 10 (postgres) + OID 16384 ($USER if not "postgres") | rolname_index (2676), oid_index (2677) populated | **partial** — all 16 predefined-role rows from `pg_authid.dat` (`pg_database_owner`, `pg_read_all_*`, `pg_monitor`, `pg_signal_*`, etc.) are **missing**; `rolpassword`/`rolvaliduntil` written as empty-string + epoch instead of NULL (`initdb.go:702-703`); `HEAP_XMIN_FROZEN` not set (visibility race risk). |
| pg_auth_members (1261) | (empty) | role_member_index (2694), member_role_index (2695) empty placeholder, oid_index (6303), grantor_index (6302) missing entirely | **partial** — empty heap is correct at initdb; secondary indexes 6303 and 6302 are **missing** from goopg's critical-index loop; 2694 and 2695 exist as empty btree placeholders (matches an empty heap). |
| pg_shseclabel (3592) | (empty) | object_index (3593) empty placeholder | **done** — both heap and index correctly empty; matches PG. |
| pg_subscription (6100) | (empty) | oid_index (6114), subname_index (6115) empty placeholder | **done** — empty heap + empty index pair matches a fresh-initdb cluster. |
| pg_tablespace (1213) | (empty) — **missing** seeded rows for pg_default (1663) and pg_global (1664) | oid_index (2697), spcname_index (2698) empty placeholder | **partial** — heap rows for the two default tablespaces must be seeded; without them `pg_class.reltablespace` lookups via TABLESPACEOID syscache miss. |
| pg_shdepend (1214) | (empty) | depender_index (1232), reference_index (1233) absent | **missing** — heap exists as empty page but the two indexes are not written. |
| pg_shdescription (2396) | (empty) — **missing** the `descr =>` rows for OID 10, OID 1, OID 4, OID 5, plus the 16 predefined roles | oid_index (2397) absent | **missing** — empty heap is tolerated but `\h pg_database` from psql will show no descriptions. |
| pg_db_role_setting (2964) | (empty) | databaseid_rol_index (2965) empty placeholder | **done** — empty heap + empty index pair matches initdb. |

### Per-critical-index diff

| Index | OID | Populated? | Status |
|---|---|---|---|
| pg_database_datname_index | 2671 | yes (template1, postgres) | **partial** — needs a third leaf entry for template0 when row is seeded. |
| pg_database_oid_index | 2672 | yes (1, 5) | **partial** — needs OID 4 leaf entry. |
| pg_authid_rolname_index | 2676 | yes (postgres + $USER) | **partial** — must include 16 predefined-role rolnames once the heap rows are added. |
| pg_authid_oid_index | 2677 | yes | **partial** — must include 16 predefined-role OIDs once heap rows are added. |
| pg_auth_members_member_role_index | 2695 | placeholder empty | **done** (empty heap → empty index is correct). |
| pg_shseclabel_object_index | 3593 | placeholder empty | **done**. |

### Per-runtime-event diff

`internal/executor/` / `internal/catalog/` event coverage today:

| Trigger | Goopg site | Status | Action |
|---|---|---|---|
| `CREATE ROLE` | not implemented (`operators_ddl.go:102` no-ops GRANT/REVOKE/COMMENT and friends) | **missing** | New `internal/executor/operators_role.go` that inserts a pg_authid row, updates both pg_authid indexes, calls `CacheInvalidateHeapTuple`, emits `XLOG_HEAP_INSERT` + `XLOG_BTREE_INSERT_LEAF` ×2. |
| `ALTER ROLE` | not implemented | **missing** | Same plus rolname-key handling. |
| `DROP ROLE` | not implemented | **missing** | Heap delete + index delete + pg_auth_members cascade + pg_shdepend cascade. |
| `GRANT role TO member` | `operators_ddl.go:102` no-op | **missing** | Insert pg_auth_members row, update four indexes (2694, 2695, 6302, 6303), invalidate AUTHMEM* syscaches. |
| `CREATE DATABASE` | `catalog.InMemory.CreateDatabase` (`internal/catalog/catalog.go:575`) tracks an in-memory map only | **partial** — no pg_database row insert, no WAL emission, no index update, no SI invalidation. |
| `DROP DATABASE` | `catalog.InMemory.DropDatabase` (`catalog.go:587`) in-memory | **partial** — same gaps as CREATE DATABASE. |
| `ALTER DATABASE` | not implemented | **missing** |
| `vac_update_datfrozenxid` | not implemented (no autovacuum yet) | **missing** | Once vacuum lands, must emit `XLOG_HEAP_INPLACE` and `CacheInvalidateHeapTupleInplace`. |
| `SECURITY LABEL` | not implemented | **missing** | Both shared (ROLE/DATABASE/TABLESPACE) and local SECURITY LABEL paths must be added. |
| `CREATE SUBSCRIPTION` | `catalog.PubSub.CreateSubscription` invoked from `operators_ddl.go:189-198` | **partial** — in-memory only; no pg_subscription heap row, no index leaf, no WAL. |
| `ALTER SUBSCRIPTION` | partial (PubSub registry) | **partial** | Same. |
| `DROP SUBSCRIPTION` | partial | **partial** | Same. |
| `XLOG_RELMAP_UPDATE` on REINDEX nailed shared | not implemented | **missing** | No REINDEX path yet; placeholder. |
| Cluster-wide SI dispatch for shared catalogs | not implemented (`inval.go` equivalents in goopg are per-database) | **missing** | Required so a fan-out of backends across multiple `MyDatabaseId`s pick up shared-catalog mutations. |
| `XLOG_HEAP2_VISIBLE` for VM bits on shared catalogs | not implemented | **missing** | Standby visibility checks rely on VM bits; no goopg emit site yet. |

### Recommended Go API

The shared-catalog seeders should be consolidated into a single
`internal/initdb/sharedcatalog.go` (new file) exposing four entry
points:

```go
// SeedSharedCatalogs writes every nailed and non-nailed shared
// catalog heap, every critical index, and the global relmap. Called
// once from initdb.Init.
func SeedSharedCatalogs(dataDir string, params SharedSeedParams) error

// SeedTemplate0Row writes pg_database row OID 4 with
// datallowconn=false and datistemplate=true so PG's CREATE DATABASE
// can clone it. Currently missing.
func SeedTemplate0Row(dataDir string) error

// SeedPredefinedRoles writes the 16 predefined-role pg_authid rows.
// Currently missing.
func SeedPredefinedRoles(dataDir string) ([]pgAuthidEntry, error)

// SeedDefaultTablespaces writes pg_default (1663) and pg_global
// (1664) into pg_tablespace. Currently missing.
func SeedDefaultTablespaces(dataDir string) error
```

The runtime-mutation path needs a single `internal/catalog/shared.go`
that funnels every DDL through `MutateSharedRow(rel, op, old, new)`
which (a) writes the heap-page tuple, (b) updates every affected
index, (c) emits the heap/index WAL records, (d) queues the cluster-
wide SI invalidation. This single chokepoint mirrors PG's
`simple_heap_insert` + `CatalogIndexInsert` + `CacheInvalidateHeapTuple`
sequence in `src/backend/access/heap/heapam.c` /
`src/backend/catalog/indexing.c`.

---

## Verification

1. **Heap-byte diff vs vanilla initdb.**

   ```bash
   PGDATA=$(mktemp -d) initdb -D "$PGDATA" --locale=C --username=postgres
   GOOPG=$(mktemp -d) goopg init -D "$GOOPG"
   for f in 1262 1260 1261 3592 6100; do
     pg_filedump -i -f "$PGDATA/global/$f" > /tmp/pg-$f.txt
     pg_filedump -i -f "$GOOPG/global/$f"  > /tmp/gp-$f.txt
     diff -u /tmp/pg-$f.txt /tmp/gp-$f.txt
   done
   ```

   After the gaps in "What goopg must produce" are closed, every
   `pg_filedump` line outside the per-row `xmin` / TID hint-bit
   windows must match.

2. **psql introspection on a started standby.**

   ```sql
   \l                         -- expect 3 rows: template0, template1, postgres
   \du                        -- expect 17 rows: postgres + 16 predefined
   \dn+                       -- 3 schemas (pg_catalog, pg_toast, public)
   SELECT count(*) FROM pg_subscription;  -- 0
   SELECT count(*) FROM pg_shseclabel;    -- 0
   SELECT spcname FROM pg_tablespace ORDER BY oid;  -- pg_default, pg_global
   ```

3. **Critical-index lookup smoke tests.**

   ```sql
   SELECT oid FROM pg_database WHERE datname = 'postgres';   -- 5
   SELECT rolname FROM pg_authid WHERE oid = 6171;            -- pg_database_owner
   SELECT 1 FROM pg_auth_members WHERE member = 10;           -- 0 rows, no error
   ```

   A FATAL `cache lookup failed for database 5` or `role "postgres"
   does not exist` indicates an index miss; a wrong row count
   indicates a heap-row gap.

4. **E2E coverage.** `TestE2E_FailoverGoopgToPG/async` exercises the
   full surface: a vanilla PG18 backend's
   `RelationCacheInitializePhase2` must complete without FATAL, then
   `InitializeSessionUserId` must `SearchSysCache1(AUTHNAME, "postgres")`
   successfully, then `get_db_info("postgres")` must resolve OID 5.
   Today the test exits earlier (pg_control / first-WAL-segment gaps);
   once those land, the partial pg_authid / pg_tablespace seeds in
   this doc become the next observable failure surface.

5. **Runtime-mutation parity.** Once the runtime DDL handlers in
   "Per-runtime-event diff" land, an integration test runs
   `CREATE ROLE r1; GRANT pg_read_all_data TO r1; CREATE DATABASE d1
   OWNER r1; CREATE SUBSCRIPTION s1 …; DROP DATABASE d1; DROP ROLE r1`,
   restarts a vanilla PG18 standby, and asserts each step is visible
   on the standby through `pg_authid`, `pg_auth_members`,
   `pg_database`, `pg_subscription`. Index leaves must contain the
   same heap-TID pointers on both sides.

6. **Cluster-wide SI dispatch.** A two-connection psql test against
   the goopg primary: connection A is bound to `postgres`, connection
   B to a freshly-created `d1`. `CREATE ROLE r2` on connection A must
   become visible to connection B's next `\du`. If goopg's SI fan-out
   is still per-database, this test fails — that is the
   acceptance criterion for the "Cluster-wide SI dispatch for shared
   catalogs" gap above.
