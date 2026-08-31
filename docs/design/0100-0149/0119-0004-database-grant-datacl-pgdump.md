# 0119-0004 — DATABASE GRANT (`pg_database.datacl`) round-trip in pg_dump (M0119-0004-ACLHEAP, datacl half)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dump.c` (`getDatabases`, `dumpDatabase`,
`dumpACL`/`buildACLCommands`); `postgres/src/backend/utils/adt/acl.c`
(`acldefault`, `OBJECT_DATABASE` case); `postgres/src/include/catalog/
pg_database.h`, `pg_shseclabel.h`, `pg_db_role_setting.h`

## Problem

`GRANT/REVOKE … ON DATABASE …` was silently dropped from every dump — the
last object class left open under M0119-0004-ACLHEAP (typacl/attacl/srvacl/
fdwacl already landed; loop #89's ledger row marked datacl "PERMANENTLY
DEFERRED" because `pg_database`'s ACL section is only emitted by pg_dump
under `-C`/`--create` (`dopt.outputCreateDB`), and no test harness exercised
`--create` — every existing `testport` pg_dump test ran the default
`--no-create` connection-setup dump). Two things had to land together:

1. A GRANT/REVOKE parser capture + executor writer for `pg_database.datacl`
   (a SHARED, cluster-wide catalog — one relfilenode at `global/1262`, not
   duplicated per connected database like `pg_type`/`pg_attribute`).
2. A `--create` pg_dump test harness — which, once built, immediately
   exposed that goopg's SQL-visible `pg_database` virtual catalog was far
   short of PG18's real column set (only 9 of pg_dump's needed ~19 columns),
   and that pg_dump's `--create` dump also queries two catalogs goopg never
   registered at all (`pg_shseclabel`, `pg_db_role_setting`).

## Fix

### ACL store + heap resync (parser → executor → catalog)

Mirrors the typacl/srvacl/fdwacl template exactly:

- **Parser** (`internal/parser/ast.go`, `parser.go`): `buildDatabaseACLChange`
  captures `GRANT/REVOKE … ON DATABASE …` into `CompatNoopStmt.DatabaseACLChange`
  (privileges/database names/grantees/`WITH GRANT OPTION`) alongside the
  pre-existing `DatabaseACL` bool (which independently drives the
  intra-grant-inplace xmax lock-wait mechanism, design 0118-0098 — left
  unconditional).
- **Server routing** (`internal/server/query.go`): `isHeapACLObject` gains
  `" ON DATABASE "` alongside `" ON TYPE "`/`" ON DOMAIN "` so the statement
  reaches the executor instead of the server's virtual-ACL fast path (which
  would otherwise short-circuit with an empty no-op completion before
  `execDatabaseACLChange` ever ran).
- **Executor** (`internal/executor/operators_ddl_database_acl.go`):
  `execDatabaseACLChange` updates the OID-keyed ACL store (shared with
  relations/schemas/routines/types/foreign objects) keyed by `im.DBOID()` —
  goopg v0 has one logical connected database, so a GRANT naming any other
  database name is a silent no-op (mirrors `persistDatFrozenXID`'s identical
  restriction). `databaseACLAllPrivs = {CREATE, TEMPORARY, CONNECT}` is the
  full `ACL_ALL_RIGHTS_DATABASE` set; `databaseACLPublicDefaultPrivs =
  {TEMPORARY, CONNECT}` is the `acldefault('d', owner)` world-default half —
  the one DATABASE-specific asymmetry vs TYPE/FUNCTION's uniform
  owner-equals-PUBLIC default (PostgreSQL withholds CREATE from PUBLIC on a
  database). `resyncDatabaseACLHeapRow` rewrites the physical heap-backed
  `pg_database` row (delete-old via xmax stamp + insert-new, matching
  `resyncTypeACLHeapRow`'s MVCC row-version pattern — NOT
  `persistDatFrozenXID`'s intentional in-place overwrite, since a GRANT is an
  ordinary transactional DDL statement).
- **Catalog** (`internal/catalog/catalog.go`): `databaseACLPrivOrder`
  (`CREATE`→`'C'`, `TEMPORARY`→`'T'`, `CONNECT`→`'c'`, aclitemout bit order)
  + `ownerDatabaseACLString = "CTc"` + `DatabaseACLText(dbOID)` delegating to
  the shared `relaclTextLockedFor` core.
- **Read path** (`internal/executor/operators_storage.go`): the physical
  heap-scan hook that decodes a `KindBytes` `_aclitem` blob back to
  aclitemout text (shared with typacl/attacl) gains a `pg_database.datacl`
  case, both in `seqScanOp.Next()`'s inline fast path and the shared
  `renderHeapACLColumnInto` (index-scan path).

### `pg_database` virtual-catalog column gap (the `--create` blocker)

goopg's SQL-visible `pg_database` (`c.tables["pg_catalog.pg_database"]`,
`Virtual: true`) carried only 9 columns (`oid, datname, datdba, encoding,
datallowconn, datconnlimit, datistemplate, datfrozenxid, datminmxid`) —
enough for HammerDB/vacuumdb probes and the M0117-0008 wraparound-horizon
columns, but pg_dump's `--create` `getDatabases` query additionally selects
`datcollate, datctype, datacl, acldefault('d', datdba), datlocprovider,
datlocale, datcollversion, daticurules, dattablespace` (correlated subquery
against `pg_tablespace`) and errored `42703 column "datcollate" does not
exist` at plan time — this had never been exercised because no prior test
ran `pg_dump --create`. This is a **separate, pre-existing gap** from the
ACL wiring above (goopg already has a fully PG18-shaped **heap** column
schema for `pg_database`, `PgDatabaseColumnsPG18` in
`internal/catalog/pg_database_schema.go`, used for on-disk/standby fidelity
— but the SQL query planner resolves `SELECT ... FROM pg_database` against
the sparse *virtual* definition, not that heap schema, so the heap schema's
completeness was invisible to goopg's own SQL layer).

Fix: extended `pg_database`'s `Columns`/`VirtualRows` with the missing
columns, values mirroring what a fresh `initdb --locale=C` libc cluster's
real `bootstrapPostgresDatabase` heap row carries (goopg v0 tracks no
per-database locale/tablespace override, so every row reports the bootstrap
default): `dattablespace=1663` (`pg_default`), `datcollate=datctype="C"`,
`datlocprovider='c'` (libc), `datlocale`/`daticurules`/`datcollversion` all
NULL (`catalog.VirtualNull` sentinel — required for every non-array column
type, since only `TypedVirtualCell`'s array-type branch treats a bare `""`
as NULL). Also corrected `datdba`'s declared type from `text` to `oid` and
`encoding` from `text` to `int4` — `acldefault('d', datdba)` and
`pg_encoding_to_char(encoding)` both read `.Int` off the evaluated datum,
which is only populated when `TypedVirtualCell` parses the column as a
numeric type.

`datacl` is looked up via `c.DatabaseACLText(c.DBOID())` **only** for the
`"postgres"` row (the only database `execDatabaseACLChange` can ever grant
into, per its single-live-database scope) — keyed by `c.DBOID()` (the real
on-disk OID read from the physical `global/1262` heap by
`detectCatalogDBOID` at startup, PG18's well-known postgres database OID 5),
**not** by this row's displayed `"oid"` column. An earlier version of this
fix changed the *displayed* `postgres` row's oid from the legacy `16384`
`firstNormalObjectOID` placeholder to `c.DBOID()` to make the two agree —
that broke `TestPort_PgDumpConnectionSetup`'s `CREATE SUBSCRIPTION`
round-trip, because `subdbid` (recorded elsewhere under the pre-existing
`16384` convention) no longer matched pg_dump's `WHERE subdbid = (SELECT
oid FROM pg_database WHERE datname = current_database())` join. The
display-oid and the ACL-store/heap-resync key are therefore intentionally
decoupled: the SQL-visible `oid` column keeps the legacy `16384` placeholder
every other subsystem already depends on, while `datacl` internally keys off
`c.DBOID()` (which is also what `execDatabaseACLChange`/
`resyncDatabaseACLHeapRow` use, since the heap resync must match the real
on-disk tuple's oid column). pg_dump never cross-checks that a row's `oid`
and `datacl` were "derived from the same key" — it only reads the two
columns of one projected row — so this has no visible effect on the dump.

Two more catalogs pg_dump's `--create` path queries directly (not through
the pre-existing `pg_seclabels` view) had never been registered at all:
`pg_shseclabel` (OID 3592, `dumpSecLabel` on the database object) and
`pg_db_role_setting` (OID 2964, `ALTER DATABASE ... SET ...` GUC overrides).
Both are registered as empty (`0`-row) virtual tables — correct, since
goopg supports neither `SECURITY LABEL` nor `ALTER DATABASE ... SET`, and an
empty table is what a fresh cluster with neither feature used would report.

Also added `shobj_description(oid, catalog_name)` (`internal/executor/
expr.go`), the shared-object sibling of the pre-existing `obj_description`
— `dumpDatabase` calls it to render `COMMENT ON DATABASE`. goopg has no
`COMMENT ON DATABASE`/`ROLE`/`TABLESPACE` writer, so it always resolves to
NULL (matching a cluster with no shared comments recorded); the classoid
lookup covers `pg_database`/`pg_authid`/`pg_tablespace`.

## Tests

- `internal/parser/op_grant_databaseacl_test.go`: `TestParseGrantDatabaseACL`
  (privilege/name/grantee/WGO capture across 9 GRANT/REVOKE shapes) +
  `TestParseGrantNonDatabaseLeavesDatabaseACLChangeNil` (scope isolation vs
  every other object class).
- `internal/catalog/relacl_test.go`: `TestDatabaseACLText` (NULL with no
  grants; PUBLIC's asymmetric `TEMPORARY+CONNECT` default vs the owner's full
  `CTc`; ignores a non-database privilege letter) +
  `TestDatabaseACLRevokeFromOwner` (owner-side REVOKE leaves the remaining
  explicit privileges, PUBLIC's default untouched).
- `internal/testport/pgdump_database_acl_test.go`:
  `TestPort_PgDumpDatabaseGrantACL` — the **first** `pg_dump --create` test
  in the suite. `GRANT CREATE ON DATABASE postgres TO PUBLIC` +
  `GRANT TEMPORARY ON DATABASE postgres TO grantee_db` against a real
  cluster, asserting the exact `REVOKE CONNECT,TEMPORARY ON DATABASE
  postgres FROM PUBLIC; GRANT ALL ON DATABASE postgres TO PUBLIC; GRANT
  TEMPORARY ON DATABASE postgres TO grantee_db;` sequence — confirmed
  against real pg_dump 18.3 output (PostgreSQL materializes an object's full
  default ACL into the array on its first GRANT/REVOKE, so granting CREATE
  to PUBLIC — which already implicitly holds TEMPORARY+CONNECT — collapses
  to `GRANT ALL`, not `GRANT CREATE`; the first draft of this test asserted
  the naive `GRANT CREATE` form and had to be corrected against the real
  binary).

## Gates

- `go build ./...` clean.
- `go vet ./internal/catalog/... ./internal/executor/... ./internal/parser/... ./internal/server/...` clean.
- `go test ./internal/catalog/... ./internal/parser/... ./internal/executor/... ./internal/planner/... ./internal/initdb/...` PASS.
- `go test -run TestPort_PgDumpDatabaseGrantACL ./internal/testport/` PASS (byte-identical vs real pg_dump 18.3, `--create`).
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS — regression-checked: an earlier draft that changed the displayed `postgres` row's oid broke this test's `CREATE SUBSCRIPTION` round-trip (subdbid join), caught by a stash/compare against the pre-change tree and fixed by decoupling the display-oid from the ACL-store key (see above).
- `go test -run TestPort_IsolationIntraGrantInplaceDb ./internal/testport/` PASS (confirms the pre-existing `DatabaseACL` bool / xmax lock-wait mechanism is untouched).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- pgbench smoke = pre-commit hook.

## Still open under M0119-0004

`pg_database.datacl` only round-trips for the single live-connected
database (goopg v0 has no true multi-database support — `execDatabaseACLChange`
silently no-ops a GRANT naming any other database name); `ALTER DATABASE …
SET …` (`pg_db_role_setting`) and `SECURITY LABEL` (`pg_shseclabel`) remain
unimplemented (both now surfaced as correctly-empty catalogs rather than
missing-relation errors, not implemented features). Extended-protocol
commit-time deferral (M0119-0004-ACLHEAP) remains open, as noted in every
prior slice in this series. With typacl/attacl/relacl/nspacl/proacl/srvacl/
fdwacl/datacl all landed, M0119-0004-ACLHEAP's object-class coverage is
complete; remaining M0119-0004 work is the extended-protocol thread and any
new gaps surfaced by future DU-002 slices.
