# 0106-0010 Step 3ak — Seed `pg_default_acl` (OID 826) nailed rel

Status: accepted (2026-05-18)
Milestone: M0106-0010
Predecessor: Step 3aj (`pg_conversion_name_nsp_index`)

## Problem

After Step 3aj closed the last `pg_conversion` companion index, the next
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` re-run
surfaces a new PG-standby boot blocker: every forked client backend
FATALs with

```
FATAL:  could not open relation with OID 826
```

repeated on every connection attempt from the test harness's
`psql -c "SELECT status FROM pg_catalog.pg_stat_wal_receiver"` probe.

OID 826 is `pg_default_acl` per
`postgres/src/include/catalog/pg_default_acl.h:30`:

```
CATALOG(pg_default_acl,826,DefaultAclRelationId)
```

`pg_default_acl` carries the per-role / per-namespace default ACL
templates applied to new objects. The relation is opened during
backend `InitPostgres` via the standard catcache lookup path; without
a pg_class row, `RelationBuildDesc(826) → ScanPgRelation(826)` returns
NULL and the load_relation_oid PANIC path FATALs at
`postgres/src/backend/access/common/relation.c:61`.

## Fix

Mirror Steps 3w (pg_aggregate=2600), 3aa (pg_cast=2605), and 3ag
(pg_conversion=2607) — a pure catalog-seed addition with no encoder,
builder, or `Init` flow change.

### (a) `internal/initdb/relcache_init.go`

Add `pgDefaultAclAttrs()` returning the 5-column PG18 schema sourced
verbatim from `pg_default_acl.h` / `pg_default_acl_d.h`:

| # | Name              | TypeOID | Len | NotNull | Notes                              |
|---|-------------------|---------|-----|---------|------------------------------------|
| 1 | oid               |      26 |   4 | true    | oid                                |
| 2 | defaclrole        |      26 |   4 | true    | oid → pg_authid                    |
| 3 | defaclnamespace   |      26 |   4 | true    | oid → pg_namespace (0 = all)       |
| 4 | defaclobjtype     |      18 |   1 | true    | char (DEFACLOBJ_RELATION etc.)     |
| 5 | defaclacl         |    1034 |  -1 | true    | aclitem[] (BKI_FORCE_NOT_NULL)     |

Add the entry to `nailedLocalRels` right after the Step 3ag pg_conversion
row:

```go
{826, "pg_default_acl", 83, 'r', 5, false, pgDefaultAclAttrs()},
```

`RelType=83` is safe — `pg_default_acl` is not formrdesc'd (no
`DefaultAclRelation_Rowtype_Id` constant in PG18 headers), so Step 3v's
`relation->rd_att->tdtypeid == relp->reltype` Phase3 assertion does not
fire.

The varlena `defaclacl` column carries `BKI_FORCE_NOT_NULL` in the
upstream header even though `aclitem[]` is varlena (Len=-1). The heap
is unpopulated at boot (goopg does not bootstrap default-ACL rows), so
the varlena encoder is not exercised here.

### (b) `internal/initdb/initdb.go`

`localRelMap` (`bootstrapPostgresDatabase`) gains `{826, 826}`. Without
this entry PG's relfilenode mapper cannot resolve OID 826 to a backing
file.

`bootstrapMappedLocalCatalogHeaps` OID list gains `826`. The empty 8 KiB
`InitPage`-stamped heap is sufficient because pg_default_acl has zero
rows; the DEFACLROLENSPOBJ syscache lookup expects a NULL return at
early-boot time.

### Companion indexes (intentionally deferred)

Per `pg_default_acl.h:54-55` two indexes back this catalog:

- `pg_default_acl_role_nsp_obj_index` (OID 827) — UNIQUE non-PKEY on
  `btree(defaclrole oid_ops, defaclnamespace oid_ops, defaclobjtype
  char_ops)`. Backs `MAKE_SYSCACHE(DEFACLROLENSPOBJ, …, 8)`.
- `pg_default_acl_oid_index` (OID 828) — UNIQUE PRIMARY KEY on
  `btree(oid oid_ops)`.

Both are deferred to later steps to preserve the single-OID rhythm of
Steps 3w → 3x → 3y → … . Once 826 stops FATALing, the next E2E re-run
will surface 827 (or 828) as the next blocker and the same pattern
applies (Step 3al/3am).

## Test plan

New regression pin in
`internal/initdb/pg_default_acl_nailed_test.go::TestNailedLocalRelsContainsPgDefaultAcl`
asserts `(RelName="pg_default_acl", RelKind='r', RelNatts=5,
len(Attrs)=5)` and pins every `(Name, TypeOID, Num, Len, NotNull)`
against `pg_default_acl_d.h`. Rejects silent removal that would
re-introduce the FATAL.

Existing pin extended: `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
gains `826` so the placeholder list cannot silently drop pg_default_acl.

Verified:

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestNailedLocalRelsContainsPgDefaultAcl|TestNailedLocalRelsContainsPgConversion|TestNailedLocalRelsContainsPgCast|TestNailedLocalRelsContainsPgAggregate|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestPgClassOidIndexHasSingleKeyColumn|TestNailedIndexRelnattsAgreesWithIndnatts' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3aj (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Files

- `internal/initdb/relcache_init.go` — `pgDefaultAclAttrs()` + nailedLocalRels entry.
- `internal/initdb/initdb.go` — `localRelMap` + `bootstrapMappedLocalCatalogHeaps` OID list.
- `internal/initdb/pg_default_acl_nailed_test.go` — regression pin (new).
- `internal/initdb/pg_mapped_local_catalog_heap_test.go` — `wantOIDs` extended.
- `docs/design/0106-0010-step3ak-pg-default-acl-nailed-rel.md` — this doc.
