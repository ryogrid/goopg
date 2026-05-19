# M0106-0010 Step 3bp — Seed `pg_parameter_acl` (OID 6243) into the catalog bootstrap

## Context

After Step 3bo landed `pg_opfamily_oid_index` (OID 2755) and closed the
anticipated FATAL after Step 3bn's `pg_opfamily_am_name_nsp_index`,
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` advanced
to the next FATAL: `could not open relation with OID 6243`. Per
`postgres/src/include/catalog/pg_parameter_acl_d.h:23`
(`#define ParameterAclRelationId 6243`) and
`pg_parameter_acl.h:30`
(`CATALOG(pg_parameter_acl,6243,ParameterAclRelationId) BKI_SHARED_RELATION`),
OID 6243 is the `pg_parameter_acl` heap relation — a SHARED catalog (it
lives under `global/`, not `base/{1,5}/`).

`pg_parameter_acl` stores ACL entries for configuration parameters granted
via `GRANT … ON PARAMETER …`. Backs the `PARAMETERACLNAME` and
`PARAMETERACLOID` syscaches per `MAKE_SYSCACHE` macros in
`pg_parameter_acl.h:55-56`. Opened during every backend's `InitPostgres`
ACL-cache initialisation path.

## Root cause

PG-standby `RelationBuildDesc(6243) → ScanPgRelation(6243)` walks
`pg_class_oid_index` (OID 2662, populated since Step 3m) looking for the
`Form_pg_class` row for OID 6243. goopg's `bootstrapPgClassTuples` had no
entry for 6243 because it was neither in `nailedSharedRels` nor
`nailedLocalRels`; the scan returns zero rows and the backend FATALs
with `could not open relation with OID 6243`. Same shape as the
many heap-rel-only blockers from earlier (Steps 3w pg_aggregate,
3aa pg_cast, 3ag pg_conversion, 3ak pg_default_acl, 3an pg_enum,
3ar pg_event_trigger, 3aw pg_extension, 3ba pg_foreign_data_wrapper,
3bd pg_foreign_server, 3bh pg_foreign_table, 3bm pg_opfamily) — minus
the differences that come from being shared:

- The empty 8 KiB heap at `global/6243` is already produced by
  `bootstrapSharedCatalogPlaceholders` (not
  `bootstrapMappedLocalCatalogHeaps` which only fills local catalogs
  under `base/{1,5}/`). OID 6243 was already in the shared heap-OID
  list at `initdb.go:376` from an earlier sweep, so no edit is needed
  there.
- The pg_class row + 3 pg_attribute rows must still land in the local
  pg_class (`base/{1,5}/1259`) / pg_attribute (`base/{1,5}/1249`) heaps
  because pg_class and pg_attribute are themselves local catalogs and
  hold metadata for both local and shared relations. The existing
  `bootstrapPgClassTuples` and `bootstrapPgAttributeTuples` already
  iterate `nailedSharedRels` then `nailedLocalRels` — so adding the
  shared entry threads through both writers without flow changes.

## Fix

Single-OID catalog-seed addition; no encoder, builder, or `Init` flow
change.

1. `internal/initdb/relcache_init.go`
   - New `pgParameterAclAttrs() []nailedAttr` returning the 3-column
     PG18 schema sourced verbatim from
     `postgres/src/include/catalog/pg_parameter_acl.h:30-40` and
     `pg_parameter_acl_d.h:28-30`:

     | Attnum | Name    | TypeOID | Len | NotNull |
     |--------|---------|---------|-----|---------|
     | 1      | oid     | 26      | 4   | true    |
     | 2      | parname | 25      | -1  | true    |
     | 3      | paracl  | 1034    | -1  | false   |

     `parname` is `text` carrying `BKI_FORCE_NOT_NULL`; `paracl` is
     `aclitem[]` with `BKI_DEFAULT(_null_)` (nullable). TypeOID 1034 is
     PG18's `_aclitem` array, same as `pg_class.relacl`.

   - `nailedSharedRels` gains
     `{6243, "pg_parameter_acl", 83, 'r', 3, true, pgParameterAclAttrs()}`
     immediately after the existing `pg_subscription` entry.
     `IsShared=true` is the critical bit that propagates the right
     `relisshared` flag into the heap row and `Form_pg_class` blob.
     `RelType=83` is safe because pg_parameter_acl is not formrdesc'd
     (no `ParameterAclRelation_Rowtype_Id` constant in PG18 headers;
     only `pg_database`/`pg_authid`/`pg_auth_members`/`pg_shseclabel`/
     `pg_subscription` are formrdesc'd shared rels at
     `postgres/src/backend/utils/cache/relcache.c:4075-4083`). The
     Phase3 `relation->rd_att->tdtypeid == relp->reltype` assertion
     (`relcache.c:4293`) only runs over formrdesc'd rels, so the
     placeholder RelType does not trip it.

2. The empty 8 KiB heap at `global/6243` is already produced by
   `bootstrapSharedCatalogPlaceholders` (`initdb.go:367-389`). The OID
   already sits in the heap-OID list at line 376, so no edit is needed.

3. Companion indexes 6246 (`pg_parameter_acl_parname_index`, UNIQUE on
   `parname text_ops`) and 6247 (`pg_parameter_acl_oid_index`, UNIQUE
   PRIMARY on `oid oid_ops`) are intentionally **deferred** to follow-up
   steps. PG-standby will surface them in turn (the E2E re-run after
   this step's fix confirmed the next FATAL is `could not open relation
   with OID 6246`). Once those land, the standard
   `bootstrapPgClassTuples → bootstrapPgIndexTuples → 2679 btree`
   plumbing carries the index OIDs through automatically.

## Why per-loop instead of bundling all three OIDs

The Step 3w → 3bo cadence has held to one (or two tightly-paired)
catalog OIDs per loop precisely so each step can verify the FATAL it
targets actually disappears at the PG layer, rather than discovering a
sibling regression at re-test time. The pg_parameter_acl indexes will
land identically to the dozens of earlier index-only steps — splitting
keeps the design docs single-purpose and the regression pins narrowly
scoped.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run TestNailedSharedRelsContainsPgParameterAcl
  ./internal/initdb/` — PASS.
- `go test -count=1 -run
  'TestNailedSharedRelsContainsPgParameterAcl|TestNailedLocalRelsContainsPgOpfamily|TestPgOpfamilyOidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestPgClassOidIndexHasSingleKeyColumn|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestBootstrapPgIndexIndexrelidIndex|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedRelTypesMatchPG18FormrdescConstants'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bo (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` —
  the `could not open relation with OID 6243` FATAL is gone. Next
  blocker is `FATAL: could not open relation with OID 6246`
  (`pg_parameter_acl_parname_index`), confirming pg_parameter_acl is
  now loadable at relcache build time. Follow-up loops will close 6246
  / 6247.

## Regression pin

`internal/initdb/pg_parameter_acl_nailed_test.go::TestNailedSharedRelsContainsPgParameterAcl`
walks `nailedSharedRels`, asserts the OID 6243 row's
`(RelName, RelKind, IsShared, RelNatts, RelType)`, and pins every
column's `(Name, TypeOID, Num, Len, NotNull)` against PG18's
`pg_parameter_acl.h` definitions. Future edits that silently drop the
row or shift a column type re-introduce the FATAL and fail this test
loudly.
