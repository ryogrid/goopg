# M0106-0010 Step 3ck — Seed pg_ts_config (OID 3602) + indexes 3608 / 3712

Status: Landed 2026-05-18.

## Problem

After Step 3cj seeded the pg_ts_config_map family, the upstream PG18 binary
booted as a physical-replication standby against goopg's data directory
continues to FATAL on the next missing relation:

```
FATAL: could not open relation with OID 3602
```

OID 3602 is `pg_ts_config` per
`postgres/src/include/catalog/pg_ts_config.h:30`
(`CATALOG(pg_ts_config,3602,TSConfigRelationId)`). With no pg_class row,
`RelationBuildDesc(3602) → ScanPgRelation(3602)` returns NULL and the
backend panics.

## Fix overview

Seed a complete `pg_ts_config` rel family — heap + both declared indexes —
as nailed local rels so the relcache init file and pg_class bootstrap rows
include them, mirroring the family-complete template used in Steps 3ce /
3cf / 3cg / 3ch / 3ci / 3cj.

### Declared indexes (PG18 `pg_ts_config.h:50-51`)

| OID  | Name                          | Spec                                              | UNIQUE | PRIMARY |
|------|-------------------------------|---------------------------------------------------|--------|---------|
| 3608 | pg_ts_config_cfgname_index    | btree(cfgname name_ops, cfgnamespace oid_ops)     | yes    | no      |
| 3712 | pg_ts_config_oid_index        | btree(oid oid_ops)                                | yes    | yes     |

Syscaches:

```
MAKE_SYSCACHE(TSCONFIGNAMENSP, pg_ts_config_cfgname_index, 2);
MAKE_SYSCACHE(TSCONFIGOID,     pg_ts_config_oid_index,     2);
```

### Schema (PG18 `pg_ts_config.h:30-46`, `pg_ts_config_d.h`)

`Natts_pg_ts_config == 5`. All columns are fixed-width NOT NULL; pg_ts_config
DOES carry an `oid` system column (attnum 1):

| Anum | Name          | TypeOID | Type      | Len | Notes                              |
|------|---------------|---------|-----------|-----|------------------------------------|
| 1    | oid           | 26      | oid       | 4   | system column                      |
| 2    | cfgname       | 19      | NameData  | 64  | fixed 64-byte NameData             |
| 3    | cfgnamespace  | 26      | oid       | 4   | BKI_LOOKUP pg_namespace            |
| 4    | cfgowner      | 26      | oid       | 4   | BKI_LOOKUP pg_authid               |
| 5    | cfgparser     | 26      | oid       | 4   | BKI_LOOKUP pg_ts_parser            |

`RelType=83` is safe because there is no `TSConfigRelation_Rowtype_Id`
constant in PG18 headers — only pg_database / pg_authid / pg_auth_members /
pg_shseclabel / pg_subscription are formrdesc'd shared rels at
`postgres/src/backend/utils/cache/relcache.c:4075-4083`.

## Implementation

1. **`internal/initdb/relcache_init.go`**:
   - `nailedLocalRels` heap list gains
     `{3602, "pg_ts_config", 83, 'r', 5, false, pgTsConfigAttrs()}` after
     the Step 3cj 3603 entry; idxSpec list gains
     `{3608, "pg_ts_config_cfgname_index"}` and
     `{3712, "pg_ts_config_oid_index"}` after the Step 3cj 3609 entry.
   - `pgTsConfigAttrs()` returns the 5-column descriptor.

2. **`internal/initdb/initdb.go`**:
   - `pgIndexInitialEntries` local section gains
     `entry(3608, 3602, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false)`
     and
     `entry(3712, 3602, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
     after the Step 3cj 3609 entry. IndKey for cfgname leads on
     attnum 2 (cfgname), then attnum 3 (cfgnamespace); cfgname uses
     `C_COLLATION_OID = 950` because catalog name columns use C collation.
   - Both "Critical index placeholder pages" OID lists (per-DB block +
     global/ fallback block) gain `3608` and `3712` after the Step 3cj
     3609 entry.
   - `bootstrapMappedLocalCatalogHeaps` oids list and `localRelMap` gain
     `3602` (authoritative pg_ts_config OID per `pg_ts_config.h:30`).
     The pre-existing 3764 placeholder (mislabeled "pg_ts_config" —
     3764 has no upstream catalog assignment) is left in place as a
     harmless empty 8 KiB heap page; its comment is updated to flag
     the historical mislabel.

3. **Regression pins** (`internal/initdb/pg_ts_config_nailed_test.go`):
   - `TestNailedLocalRelsContainsPgTsConfig` — pins heap shape + all
     column descriptors.
   - `TestNailedLocalRelsContainsPgTsConfigIndexes` — pins both nailed
     index rels.
   - `TestPgTsConfigIndexInitialEntries` — pins
     IndKey / IndClass / IndCollation / IsUnique / IsPrimary for 3608 +
     3712 with strict count guard.
   - `TestPgTsConfigAttrsTypeOIDsMatchPG18` — pins exact TypeOIDs/Len.

4. **Existing-test extensions**:
   - `TestPgIndexInitialEntriesIndkeyMatchesPG18` (in
     `internal/initdb/pg_index_indkey_test.go`) map extended with
     `3608:{2,3}` and `3712:{1}` (strict count guard).
   - `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
     (in `internal/initdb/btree_index_bootstrap_test.go`) extended with
     3608 and 3712.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run '...' ./internal/initdb/` (the nine pinned tests
  named above) — PASS.
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures unchanged from Step 3cj (no new regressions). Failing tests:
  TestMigration{FromLegacyJSONCluster,Idempotent,PGAttributeRowsWritten},
  TestCommittedTableSurvivesCrashRestart,
  TestRuntimeCloseTriggersFinalCheckpoint,
  TestCreateTableSurvivesRestartViaCatalogHeap,
  TestMultipleTablesLoadFromHeap,
  TestCreateIndexSurvivesRestartViaWAL,
  TestCreateIndexRecoveredOIDDoesNotCollide,
  TestSystemCatalogRelfilesAreValidHeapPages,
  TestBootstrappedPGClassRowsReadable,
  TestBootstrappedPGAttributeRowsReadable,
  TestOpenOldClusterWithoutM0030FilesStillWorks,
  TestSynchronousCommitFlushesByDefault. All unrelated to step 3ck (each
  pre-dates this milestone and is tracked under a separate item).
- Cross-package smoke
  `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

The next FATAL after this step is expected at the next missing pg_class
OID along the PG-standby boot path; that OID will be addressed by the
following sub-step.
