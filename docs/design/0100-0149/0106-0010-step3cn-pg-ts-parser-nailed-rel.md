# M0106-0010 Step 3cn — pg_ts_parser nailed local rel + indexes 3606/3607

Status: accepted
Date: 2026-05-18

## Problem

After Step 3cm cleared pg_ts_dict (OID 3600 plus indexes 3604/3605),
PG-standby boot's next FATAL is `could not open relation with OID 3601`.
OID 3601 is `pg_ts_parser` per
`postgres/src/include/catalog/pg_ts_parser.h:29`
(`CATALOG(pg_ts_parser,3601,TSParserRelationId)`). Without a pg_class
row, `RelationBuildDesc(3601) → ScanPgRelation(3601)` returns NULL and
the backend dies.

Per-database (non-shared) catalog. PG18 declares two unique indexes on
`pg_ts_parser`:

```
DECLARE_UNIQUE_INDEX(pg_ts_parser_prsname_index, 3606,
    TSParserNameNspIndexId, pg_ts_parser,
    btree(prsname name_ops, prsnamespace oid_ops));
DECLARE_UNIQUE_INDEX_PKEY(pg_ts_parser_oid_index, 3607,
    TSParserOidIndexId, pg_ts_parser,
    btree(oid oid_ops));
```

backing `MAKE_SYSCACHE(TSPARSERNAMENSP, …, 2)` and
`MAKE_SYSCACHE(TSPARSEROID, …, 2)` respectively. Closing the heap FATAL
without also nailing both indexes would just relocate the FATAL onto
`RelationIdGetRelation(3606)` next, so this step seeds the full family.

## Schema (PG18 `pg_ts_parser.h:29-54` + `pg_ts_parser_d.h`)

`Natts_pg_ts_parser == 8`. pg_ts_parser DOES carry an `oid` system
column — attnums start at 1 = oid. All 8 columns are fixed-width
NOT NULL; no varlena columns at all (simpler than pg_ts_dict's
`dictinitoption`).

| attnum | name         | typoid | len | not null | notes |
|--------|--------------|--------|-----|----------|-------|
| 1      | oid          | 26     | 4   | yes      | system OID |
| 2      | prsname      | 19     | 64  | yes      | NameData |
| 3      | prsnamespace | 26     | 4   | yes      | BKI_LOOKUP pg_namespace |
| 4      | prsstart     | 24     | 4   | yes      | regproc, BKI_LOOKUP pg_proc |
| 5      | prstoken     | 24     | 4   | yes      | regproc, BKI_LOOKUP pg_proc |
| 6      | prsend       | 24     | 4   | yes      | regproc, BKI_LOOKUP pg_proc |
| 7      | prsheadline  | 24     | 4   | yes      | regproc, BKI_LOOKUP_OPT (target may be 0) |
| 8      | prslextype   | 24     | 4   | yes      | regproc, BKI_LOOKUP pg_proc |

`prsheadline`'s `BKI_LOOKUP_OPT` qualifier permits the looked-up proc
to be missing — the column itself is still NOT NULL with value 0
(InvalidOid) when absent.

RelType=83 is safe (no `TSParserRelation_Rowtype_Id` constant in PG18
headers — only pg_database/pg_authid/pg_auth_members/pg_shseclabel/
pg_subscription are formrdesc'd shared rels at
`postgres/src/backend/utils/cache/relcache.c:4075-4083`).

## Changes

### `internal/initdb/relcache_init.go`

- New `pgTsParserAttrs()` returns the 8-column descriptor list above.
- `nailedLocalRels` heap list gains
  `{3601, "pg_ts_parser", 83, 'r', 8, false, pgTsParserAttrs()}` after
  the Step 3cm pg_ts_dict entry.
- `idxSpec` list gains `{3606, "pg_ts_parser_prsname_index"}` and
  `{3607, "pg_ts_parser_oid_index"}` after the Step 3cm 3605 entry.

### `internal/initdb/initdb.go`

- `pgIndexInitialEntries` local section gains
  `entry(3606, 3601, []int16{2, 3}, []uint32{nameOps, oidOps},
  []uint32{cCollation, 0}, true, false)` and
  `entry(3607, 3601, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
  after the Step 3cm 3605 entry. `prsname` uses C_COLLATION_OID because
  PG's catalog `name` columns are always C-collation; `prsnamespace`
  carries no collation.
- Both "Critical index placeholder pages" OID lists
  (`base/<dboid>/` block + `global/` fallback block) at
  `bootstrapPostgresDatabase` gain 3606 and 3607 after the Step 3cm
  3605 entry.
- `bootstrapMappedLocalCatalogHeaps` `oids` slice and `localRelMap`
  gain 3601 (the authoritative OID per `pg_ts_parser.h:29`). The
  pre-existing 3767 placeholder (which had a bare "pg_ts_parser"
  comment) is relabelled as the stale alias — 3767 has no upstream
  catalog assignment; the canonical OID is 3601. This mirrors the
  conservative approach taken in Step 3ci (3576/6137), Step 3cj
  (3603/3765), Step 3ck (3602/3764), and Step 3cm (3600/3766).
- No new type-helper entries needed: oid (26), name (19), regproc (24)
  are all already registered in
  `pgCatalogTypeOID` / `pgCatalogTypeLen` / `pgTypeByVal` /
  `pgTypeAlignChar` / `pgTypeStorageChar` (regproc was wired in Step 3a
  for pg_proc bootstrap).

### Regression pins

New file `internal/initdb/pg_ts_parser_nailed_test.go`:

- `TestNailedLocalRelsContainsPgTsParser` — heap entry shape and all 8
  attr descriptors.
- `TestNailedLocalRelsContainsPgTsParserIndexes` — both index entries
  (3606 / 3607) by name + relnatts.
- `TestPgTsParserIndexInitialEntries` — index columns / opclass /
  collation / unique / primary flags.
- `TestPgTsParserAttrsTypeOIDsMatchPG18` — typoids + lens + nullability
  for every column.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains
  `3606:{2,3}` and `3607:{1}` (the strict count guard at the bottom
  of the test would otherwise FAIL with an "entries, want N" mismatch).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3606 and 3607.

## Verification

- `go build ./...` — PASS.
- Targeted suite —
  `go test -count=1 -run 'TestNailedLocalRelsContainsPgTsParser|TestPgTsParserIndexInitialEntries|TestPgTsParserAttrsTypeOIDsMatchPG18|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3cm (`TestMigrationFromLegacyJSONCluster`,
  `TestMigrationIdempotent`, `TestMigrationPGAttributeRowsWritten`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestCreateTableSurvivesRestartViaCatalogHeap`,
  `TestMultipleTablesLoadFromHeap`,
  `TestCreateIndexSurvivesRestartViaWAL`,
  `TestCreateIndexRecoveredOIDDoesNotCollide`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestBootstrappedPGClassRowsReadable`,
  `TestBootstrappedPGAttributeRowsReadable`,
  `TestOpenOldClusterWithoutM0030FilesStillWorks`,
  `TestSynchronousCommitFlushesByDefault`). No new regressions.
- Cross-package smoke — `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Follow-ups (not in this step)

- Step 3co will seed pg_ts_template (OID 3764 in the genbki dat /
  3764 placeholder OID in goopg's current map — authoritative OID
  per `pg_ts_template.h` is 3764 with indexes 3766/3767 NOT to be
  confused with goopg's stale `3766`/`3767` placeholders). The same
  family-complete recipe applies once the next E2E run surfaces it.
