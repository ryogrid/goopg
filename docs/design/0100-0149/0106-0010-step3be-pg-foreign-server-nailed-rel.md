# M0106-0010 Step 3be — Seed pg_foreign_server (OID 1417) as Nailed Rel

**Status:** accepted (2026-05-18)
**Milestone:** M0106 — PG Relcache Init File Compatibility
**Sub-milestone:** M0106-0010
**Predecessor:** Step 3bd (seeded pg_foreign_data_wrapper_oid_index, OID 112)

## Problem

After Step 3bd closed both pg_foreign_data_wrapper companion indexes (112,
548), the `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
PG-standby boot sequence advances and every new backend FATALs with:

```
FATAL:  could not open relation with OID 1417
```

OID 1417 is `pg_foreign_server` per
`postgres/src/include/catalog/pg_foreign_server_d.h:23`
(`#define ForeignServerRelationId 1417`). PG18's `InitPostgres` opens
this catalog during early backend setup (after FDW infrastructure
becomes reachable via the just-seeded pg_foreign_data_wrapper). Without
a `Form_pg_class` row, `RelationBuildDesc(1417) → ScanPgRelation(1417)`
returns NULL and the backend dies.

## Decision

Pure catalog-seed addition mirroring the nailed-rel pattern of Steps 3w
(pg_aggregate), 3aa (pg_cast), 3ag (pg_conversion), 3ak (pg_default_acl),
3an (pg_enum), 3ar (pg_event_trigger), 3aw (pg_extension), and 3bb
(pg_foreign_data_wrapper). No encoder, builder, or `Init` flow change.

The two companion indexes — `pg_foreign_server_oid_index` (113, UNIQUE
PKEY) and `pg_foreign_server_name_index` (549, UNIQUE) per
`pg_foreign_server.h:55-56` — are intentionally **deferred** until concrete
E2E blockers surface, preserving the single-OID rhythm.

## Changes

### `internal/initdb/relcache_init.go`

1. New `pgForeignServerAttrs()` helper returns the 8-column PG18 schema
   verbatim from `pg_foreign_server.h` / `pg_foreign_server_d.h`:

   | Anum | Name        | TypeOID         | Len | NotNull |
   |------|-------------|-----------------|-----|---------|
   | 1    | oid         | 26 (oid)        | 4   | true    |
   | 2    | srvname     | 19 (name)       | 64  | true    |
   | 3    | srvowner    | 26 (oid)        | 4   | true    |
   | 4    | srvfdw      | 26 (oid)        | 4   | true    |
   | 5    | srvtype     | 25 (text)       | -1  | false   |
   | 6    | srvversion  | 25 (text)       | -1  | false   |
   | 7    | srvacl      | 1034 (aclitem[])| -1  | false   |
   | 8    | srvoptions  | 1009 (text[])   | -1  | false   |

   srvtype/srvversion/srvacl/srvoptions live in the CATALOG_VARLEN block
   without `BKI_FORCE_NOT_NULL`, so all nullable.

2. `nailedLocalRels` gains
   `{1417, "pg_foreign_server", 83, 'r', 8, false, pgForeignServerAttrs()}`
   immediately after the Step 3bb pg_foreign_data_wrapper entry. RelType=83
   is safe because pg_foreign_server is not formrdesc'd (no
   `ForeignServerRelation_Rowtype_Id` constant in PG18 headers), so
   Step 3v's `relation->rd_att->tdtypeid == relp->reltype` Phase-3
   assertion does not fire.

### `internal/initdb/initdb.go`

3. `localRelMap` gains `{1417, 1417}` so PG's relfilenode mapper resolves
   OID 1417 to a backing file.

4. `bootstrapMappedLocalCatalogHeaps` OID list gains `1417` so an
   `InitPage`-stamped 8 KiB heap exists at `base/{1,5}/1417` before PG's
   mdopen runs. The Step-3w empty-heap pattern is sufficient because
   pg_foreign_server is currently unpopulated — any catcache probe
   correctly returns no row.

The single nailedLocalRels entry threads automatically through:

```
bootstrapPgClassTuples → bootstrapPgAttributeTuples →
bootstrapPgClassOidIndex (adds leaf at 2662) →
bootstrapPgAttributeRelidAttnumIndex (8 composite-key leaves at 2659)
```

and `writeRelcacheInitFile` emits a `Form_pg_class` + 8
`Form_pg_attribute` blob group for both `global/pg_internal.init` and
`base/{1,5}/pg_internal.init`.

## Regression Pins

- `TestNailedLocalRelsContainsPgForeignServer` — full per-column
  `(Name, TypeOID, Num, Len, NotNull)` audit of the 8 attributes.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignServer` — asserts
  `base/{1,5}/1417` exists, is exactly 8 KiB, and InitPage-stamped.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  extended with 1417 so the placeholder list cannot silently drop
  pg_foreign_server.

Both new tests live in
`internal/initdb/pg_foreign_server_nailed_test.go`.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgForeignServer|TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignServer|TestNailedLocalRelsContainsPgForeignDataWrapper|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestPgForeignDataWrapperOidIndex|TestPgForeignDataWrapperNameIndex|TestNailedIndexRelnattsAgreesWithIndnatts'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bd (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030FilesStillWorks`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`); no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Follow-up

The next FATAL surfacing once OID 1417 is satisfied will be a different
nailed-rel OID accessed by `InitPostgres` (likely a pg_foreign_server
companion index 113 / 549 if a syscache probe follows the relcache open,
or the next catalog in the boot path). The empirical signal of the next
E2E run drives Step 3bf.
