# M0106-0010 Step 3ch — pg_tablespace nailed shared relation + indexes

Date: 2026-05-18

## Background

Step 3cg closed the `FATAL: could not open relation with OID 6102`
(`pg_subscription_rel`) blocker on PG-standby boot from a goopg-cloned
data directory. Re-running `TestE2E_FailoverGoopgToPG/async` with
`GOOPG_RUN_BLOCKED_M0102_E2E=1` advanced past 6102 and surfaced the
next FATAL:

```
FATAL:  could not open relation with OID 1213
```

OID 1213 is `pg_tablespace` per
`postgres/src/include/catalog/pg_tablespace.h:29`:

```c
CATALOG(pg_tablespace,1213,TableSpaceRelationId) BKI_SHARED_RELATION
```

This is a **shared** nailed-rel addition like Step 3ca
(pg_replication_origin). The empty 8-KiB heap file at `global/1213` is
already produced by `bootstrapSharedCatalogPlaceholders` (heapOIDs
list); the missing pieces are the `nailedSharedRels` entry plus the
two index Form_pg_index rows.

## Changes

### Schema

`pg_tablespace` has 5 columns per `pg_tablespace.h:29-41` + `pg_tablespace_d.h`
(Natts_pg_tablespace == 5):

| attnum | name       | type      | TypeOID | Len | NotNull | Notes                                                |
|--------|------------|-----------|---------|-----|---------|------------------------------------------------------|
| 1      | oid        | oid       | 26      | 4   | yes     | system column                                        |
| 2      | spcname    | NameData  | 19      | 64  | yes     |                                                      |
| 3      | spcowner   | oid       | 26      | 4   | yes     | BKI_DEFAULT(POSTGRES) BKI_LOOKUP(pg_authid)          |
| 4      | spcacl     | aclitem[] | 1034    | -1  | no      | CATALOG_VARLEN; BKI_DEFAULT(_null_)                  |
| 5      | spcoptions | text[]    | 1009    | -1  | no      | CATALOG_VARLEN; BKI_DEFAULT(_null_)                  |

### Indexes

Two indexes declared on pg_tablespace (`pg_tablespace.h:52-53`):

| OID  | name                          | Decl                      | columns        | syscache       |
|------|-------------------------------|---------------------------|----------------|----------------|
| 2697 | pg_tablespace_oid_index       | DECLARE_UNIQUE_INDEX_PKEY | oid (1)        | TABLESPACEOID  |
| 2698 | pg_tablespace_spcname_index   | DECLARE_UNIQUE_INDEX      | spcname (2)    | (no syscache)  |

2697 uses `oid_ops` (1981), collation 0. 2698 uses `name_ops` (1986)
with `C_COLLATION_OID` 950 — same convention as
`pg_database_datname_index` (2671) / `pg_authid_rolname_index` (2676)
and other shared-catalog name-keyed indexes.

### File-by-file

1. `internal/initdb/relcache_init.go`
   - New `pgTablespaceAttrs()` returns the 5-column descriptor.
   - `nailedSharedRels` heap list gains
     `{1213, "pg_tablespace", 83, 'r', 5, true, pgTablespaceAttrs()}`
     after the Step 3ca pg_replication_origin entry. `RelType=83`
     reused per the established placeholder convention for catalogs
     not formrdesc'd (no `TableSpaceRelation_Rowtype_Id` constant in
     PG18 headers; only pg_database/pg_authid/pg_auth_members/
     pg_shseclabel/pg_subscription are formrdesc'd shared rels at
     `postgres/src/backend/utils/cache/relcache.c:4075-4083`).
   - `nailedSharedRels` idxSpec list gains
     `{2697, "pg_tablespace_oid_index"}` and
     `{2698, "pg_tablespace_spcname_index"}` after the Step 3cf 6115
     entry; `flattenRels` + `pgIndexNattsByOID` derive
     `RelKind='i', RelNatts=1` for both so the `relnatts == indnatts`
     check (relcache.c:1492) passes.

2. `internal/initdb/initdb.go`
   - Heap file at `global/1213` already created by
     `bootstrapSharedCatalogPlaceholders` (heapOIDs already lists 1213);
     no change needed there.
   - `pg_filenode.map` already has `{1213, 1213}` (mapper unchanged).
   - Both "Critical index placeholder pages" OID lists (`base/<dboid>/`
     block + `global/` fallback block) at `bootstrapPostgresDatabase`
     gain `2697` and `2698` after the Step 3cg 6117 entry. Empty-btree
     placeholder is sufficient because pg_tablespace is unpopulated at
     bootstrap.
   - `pgIndexInitialEntries` shared section gains two rows after the
     Step 3cf 6115 entry:
     - `entry(2697, 1213, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
     - `entry(2698, 1213, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false)`

3. Regression pins (new test file in `internal/initdb/`):
   - `pg_tablespace_nailed_test.go` →
     `TestNailedSharedRelsContainsPgTablespace` (heap + 5-col schema,
     IsShared=true),
     `TestNailedSharedRelsContainsPgTablespaceIndexes` (both indexes
     pinned with RelKind='i', RelNatts=1),
     `TestPgTablespaceIndexInitialEntries` (Form_pg_index pins for
     2697 + 2698),
     `TestPgTablespaceAttrsTypeOIDsMatchPG18` (per-column TypeOID/Len
     pin).

4. Existing tests extended:
   - `pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`:
     map gains `2697:{1}` + `2698:{2}` (strict count guard).
   - `btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`:
     adds 2697 + 2698.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedSharedRelsContainsPgTablespace|TestPgTablespaceIndexInitialEntries|TestPgTablespaceAttrsTypeOIDsMatchPG18|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures unchanged (no new regressions; same set as Step 3cg).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- E2E re-run (`GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run
  TestE2E_FailoverGoopgToPG/async ./internal/testport/`) confirms FATAL
  on 1213 is closed — next FATAL is OID 3576 (`pg_transform`), to be
  handled by Step 3ci.

## Next anticipated blocker

With pg_tablespace (1213) + pg_tablespace_oid_index (2697) +
pg_tablespace_spcname_index (2698) all seeded, the pg_tablespace family
is fully wired. The next E2E re-run surfaced OID 3576
(`pg_transform`) per
`postgres/src/include/catalog/pg_transform.h:29` — a per-database
catalog with 4 columns + 2 indexes. Step 3ci will follow the Step 3ce
(per-database, no oid system column) template.
