# M0106-0010 Step 3ca — pg_replication_origin nailed shared relation + indexes

Date: 2026-05-18

## Background

Step 3bz closed the `FATAL: could not open relation with OID 3541`
(`pg_range`) blocker on PG-standby boot from a goopg-cloned data
directory. Re-running `TestE2E_FailoverGoopgToPG/async` with
`GOOPG_RUN_BLOCKED_M0102_E2E=1` advanced past 3541 and surfaced the
next FATAL:

```
FATAL:  could not open relation with OID 6000
```

OID 6000 is `pg_replication_origin` per
`postgres/src/include/catalog/pg_replication_origin.h:30`:

```c
CATALOG(pg_replication_origin,6000,ReplicationOriginRelationId) BKI_SHARED_RELATION
```

This is the first **shared** nailed-rel addition since Step 3br
(pg_parameter_acl_oid_index). The empty 8-KiB heap file at
`global/6000` is already produced by `bootstrapSharedCatalogPlaceholders`
(heapOIDs list); the missing piece is the `nailedSharedRels` entry plus
the index Form_pg_index rows.

## Changes

### Schema

`pg_replication_origin` has **no `oid` system column** — attnums start
at 1 = roident per `pg_replication_origin_d.h`. Two columns total:

| attnum | name    | type | TypeOID | Len | NotNull | Notes                              |
|--------|---------|------|---------|-----|---------|------------------------------------|
| 1      | roident | oid  | 26      | 4   | yes     | manually allocated (value fits u16) |
| 2      | roname  | text | 25      | -1  | yes     | BKI_FORCE_NOT_NULL                  |

The upstream `pg_replication_origin.h` comment notes "Needs to fit into
an uint16, so we don't waste too much space in WAL records" — but the
column type is still `Oid` (4-byte storage); only the value-allocation
policy differs from normal pg_class.oid.

### Indexes

Two indexes declared on pg_replication_origin (pg_replication_origin.h:57–58):

| OID  | name                                  | Decl                          | columns          | syscache       |
|------|---------------------------------------|-------------------------------|------------------|----------------|
| 6001 | pg_replication_origin_roiident_index  | DECLARE_UNIQUE_INDEX_PKEY     | roident (1)      | REPLORIGIDENT  |
| 6002 | pg_replication_origin_roname_index    | DECLARE_UNIQUE_INDEX          | roname (2)       | REPLORIGNAME   |

6001 uses `oid_ops` (uint32 1981), collation 0. 6002 uses `text_ops`
(uint32 3126) with `C_COLLATION_OID` 950 — same convention as
`pg_parameter_acl_parname_index` (6246, Step 3bq) and the text_ops
slot of `pg_shseclabel_object_index` (3593).

### File-by-file

1. `internal/initdb/relcache_init.go`
   - New `pgReplicationOriginAttrs()` returns the 2-column descriptor.
   - `nailedSharedRels` heap list gains
     `{6000, "pg_replication_origin", 83, 'r', 2, true, pgReplicationOriginAttrs()}`
     after the Step 3bp pg_parameter_acl entry. `RelType=83` reused per
     the established placeholder convention for catalogs not
     formrdesc'd (no `ReplicationOriginRelation_Rowtype_Id` constant
     exists).
   - `nailedSharedRels` idxSpec list gains
     `{6001, "pg_replication_origin_roiident_index"}` and
     `{6002, "pg_replication_origin_roname_index"}` after the Step 3br
     6247 entry; `flattenRels` + `pgIndexNattsByOID` derive
     `RelKind='i', RelNatts=1` for both so the `relnatts == indnatts`
     check (relcache.c:1492) passes.

2. `internal/initdb/initdb.go`
   - Heap file at `global/6000` already created by
     `bootstrapSharedCatalogPlaceholders` (heapOIDs already lists 6000);
     no change needed there.
   - `pg_filenode.map` already has `{6000, 6000}` (mapper unchanged).
   - "Shared critical indexes (under global/)" placeholder OID list at
     `bootstrapPostgresDatabase` gains
     `6001, // pg_replication_origin_roiident_index (Step 3ca)` and
     `6002, // pg_replication_origin_roname_index (Step 3ca)` after the
     Step 3br 6247 entry. Empty-btree placeholder is sufficient because
     pg_replication_origin is unpopulated at bootstrap.
   - `pgIndexInitialEntries` shared section gains two rows after the
     Step 3br 6247 entry:
     - `entry(6001, 6000, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
     - `entry(6002, 6000, []int16{2}, []uint32{textOps}, []uint32{cCollation}, true, false)`

3. Regression pins (new test files in `internal/initdb/`):
   - `pg_replication_origin_nailed_test.go` →
     `TestNailedSharedRelsContainsPgReplicationOrigin` — heap +
     2-col schema, IsShared=true.
   - `pg_replication_origin_roiident_index_test.go` →
     `TestPgReplicationOriginRoiidentIndexSeededFromInitialEntries`
     (Form_pg_index pin) +
     `TestNailedSharedRelsContainsPgReplicationOriginRoiidentIndex`
     (pg_class pin).
   - `pg_replication_origin_roname_index_test.go` →
     `TestPgReplicationOriginRonameIndexSeededFromInitialEntries`
     (Form_pg_index pin with text_ops + cCollation) +
     `TestNailedSharedRelsContainsPgReplicationOriginRonameIndex`
     (pg_class pin).

4. Existing tests extended:
   - `pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`:
     map gains `6001:{1}` + `6002:{2}` (strict count guard).
   - `btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`:
     adds 6001 + 6002.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedSharedRelsContainsPgReplicationOrigin|TestPgReplicationOriginRoiidentIndex|TestPgReplicationOriginRonameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestNailedIndexRelnattsAgreesWithIndnatts'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures unchanged (no new regressions; same set as Step 3bz).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next anticipated blocker

With pg_replication_origin (6000) + pg_replication_origin_roiident_index
(6001) + pg_replication_origin_roname_index (6002) all seeded, the
pg_replication_origin family is fully wired. The next E2E re-run is
expected to surface another catalog FATAL — likely in the
pg_subscription_rel / pg_statistic / pg_statistic_ext territory
(Step 3cb).
