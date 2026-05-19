# M0106-0010 Step 3cc: pg_statistic_ext_data (OID 3429) nailed local rel + composite PKEY 3433

**Status**: LANDED 2026-05-18

## Problem

After Step 3cb seeded the `pg_sequence` (OID 2224) per-database catalog
plus its single UNIQUE PRIMARY index 5002, the next PG-standby boot FATAL
becomes:

```
WARNING:  you don't own a lock of type AccessShareLock
FATAL:  could not open relation with OID 3429
```

OID 3429 is `pg_statistic_ext_data` per
`postgres/src/include/catalog/pg_statistic_ext_data.h:31`
(`CATALOG(pg_statistic_ext_data,3429,StatisticExtDataRelationId)`). It is
opened very early during the PG18 phase-3 relcache initialisation pass, so
no user query can run until the catalog has a pg_class row.

A long-standing stale comment in `bootstrapMappedLocalCatalogHeaps` /
`localRelMap` mislabelled OID `6245` as `pg_statistic_ext_data`. The true
OID of `pg_statistic_ext_data` is **3429**; `6245` is
`PgParameterAclToastIndex`
(`postgres/src/include/catalog/pg_parameter_acl.h:51`). The 6245 entry is
left in place (it serves as a benign placeholder for the toast index that
is also referenced elsewhere) but the new 3429 entry now carries the
correct label.

## Decision

Family-complete seed in one step: heap 3429 + its single PG-declared
UNIQUE PRIMARY index 3433 (`pg_statistic_ext_data_stxoid_inh_index`,
**composite** btree on `(stxoid oid_ops, stxdinherit bool_ops)`). Index
3433 backs `MAKE_SYSCACHE(STATEXTDATASTXOID, …, 4)` — the only declared
syscache against pg_statistic_ext_data.

`pg_statistic_ext_data` is a per-database (non-shared) catalog, so it
follows the Step 3cb `pg_sequence` template, not a shared-rel template.
**First non-single-column nailed index** seeded in the M0106-0010 step 3
series — exercises the multi-column `IndKey/IndClass/IndCollation` slots
of `pgIndexInitialEntries`.

## Schema (PG18, verified against PostgreSQL 18.3 runtime pg_attribute)

| attnum | name              | typeOID | len | notnull | typalign | typstorage |
| ------ | ----------------- | ------- | --- | ------- | -------- | ---------- |
| 1      | stxoid            | 26      | 4   | true    | i        | p          |
| 2      | stxdinherit       | 16      | 1   | true    | c        | p          |
| 3      | stxdndistinct     | 3361    | -1  | false   | i        | x          |
| 4      | stxddependencies  | 3402    | -1  | false   | i        | x          |
| 5      | stxdmcv           | 5017    | -1  | false   | i        | x          |
| 6      | stxdexpr          | 10028   | -1  | false   | d        | x          |

`pg_statistic_ext_data` has **no `oid` system column** — attnums start at
1 = stxoid. RelType=83 is safe (no `StatisticExtDataRelation_Rowtype_Id`
constant in PG18 headers; the catalog is not formrdesc'd).

The CATALOG_VARLEN trailing columns (attnums 3..6) are nullable
`pg_ndistinct` / `pg_dependencies` / `pg_mcv_list` / `_pg_statistic` blobs.
`_pg_statistic` (TypeOID 10028, the array of `pg_statistic` rowtype) is
allocated in the FirstGenbkiObjectId range (10000..11999) and is stable
across PG18 installs because genbki assigns rowtype/array OIDs
deterministically.

Notable: `_pg_statistic`'s typalign is **'d'** (double, 8-byte), not 'i' —
because its element rowtype `pg_statistic` carries int8/float8-aligned
columns (`stanullfrac`/`stadistinct float4` padded to 8 bytes; `stavalues
anyarray`). Without honouring this, a future write into pg_statistic_ext_data
would deserialise the stxdexpr column at the wrong offset.

## Implementation

### `internal/initdb/relcache_init.go`

1. **`pgStatisticExtDataAttrs()`** new helper returns the 6-column PG18
   schema verbatim.

2. **`nailedLocalRels`** gains
   `{3429, "pg_statistic_ext_data", 83, 'r', 6, false, pgStatisticExtDataAttrs()}`
   right after the Step 3cb `pg_sequence` (2224) entry.

3. **`idxSpec`** list gains
   `{3433, "pg_statistic_ext_data_stxoid_inh_index"}` after the Step 3cb
   5002 entry.

### `internal/initdb/initdb.go`

4. **`pgIndexInitialEntries`** local section gains
   `entry(3433, 3429, []int16{1, 2}, []uint32{oidOps, boolOps},
   []uint32{0, 0}, true, true)` after the Step 3cb 5002 entry.
   **Composite** UNIQUE PRIMARY key (stxoid oid_ops + stxdinherit bool_ops,
   no collation). New `boolOps uint32 = 1984` constant (btree bool_ops,
   matches `boolBtreeOps` used elsewhere in this file).

5. **`bootstrapMappedLocalCatalogHeaps`** OID list gains
   `3429, // pg_statistic_ext_data (M0106-0010 step 3cc)` after the Step
   3cb `2224` entry.

6. **`localRelMap`** in `bootstrapPostgresDatabase` gains
   `{3429, 3429}` after the Step 3cb `{2224, 2224}` entry.

7. Both **"Critical index placeholder pages"** OID lists (the
   `base/<dboid>/` block and the `global/` fallback block) gain
   `3433, // pg_statistic_ext_data_stxoid_inh_index (Step 3cc)` after the
   Step 3cb `5002` entry.

   The empty-btree placeholder is sufficient because pg_statistic_ext_data
   is unpopulated at bootstrap (extended-statistics data is written only
   when ANALYZE runs against a CREATE STATISTICS object).

8. **`pgTypeAlignChar`** extended:
   - Case `i`: gains `3361, 3402, 5017` (pg_ndistinct / pg_dependencies /
     pg_mcv_list — all 4-byte aligned varlena).
   - Case `d`: gains `10028` (_pg_statistic, 8-byte aligned because its
     element rowtype pg_statistic uses int8/float8-aligned columns).

9. **`pgTypeStorageChar`** extended: case `x` (EXTENDED) gains
   `3361, 3402, 5017, 10028` so the nailed pg_attribute row for
   stxdndistinct/stxddependencies/stxdmcv/stxdexpr emits attstorage='x'
   instead of the wrong 'p' (PLAIN) default. Functionally a no-op while
   the heap stays empty, but a silent corruption hazard the moment any
   row gets written.

### Regression pins

New file `internal/initdb/pg_statistic_ext_data_nailed_test.go`:

- `TestNailedLocalRelsContainsPgStatisticExtData` — pins the heap entry
  and all 6 column descriptors verbatim.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgStatisticExtData` — pins
  that an 8-KiB initialised heap page is written at `base/1/3429` and
  `base/5/3429`.
- `TestPgStatisticExtDataStxoidInhIndexInitialEntry` — pins the
  `pgIndexInitialEntries` row for OID 3433: composite IndKey=[1,2],
  IndClass=[1981 oid_ops, 1984 bool_ops], IsUnique && IsPrimary.
- `TestNailedLocalRelsContainsPgStatisticExtDataStxoidInhIndex` — pins
  the flattenRels-derived index entry for OID 3433 (RelKind='i',
  RelNatts=2 — the **first composite nailed index**).
- `TestPgStatisticExtDataAttrsTypeOIDsMatchPG18` — pins exact TypeOID /
  Len / NotNull values against PostgreSQL 18.3 runtime pg_attribute
  lookup so a drift can't silently corrupt the relcache init file.
- `TestPgTypeAlignAndStorageFor_pg_statisticArray` — pins
  `pgTypeAlignChar(10028)='d'` and `pgTypeStorageChar({3361,3402,5017,10028})='x'`
  against PG18 pg_type rows.

Existing regression pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — `want` map gains
  `3433: {1, 2}` (strict count guard).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — extended with `3433`.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  — extended with `3429` (strict list guard).

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgStatisticExtData|TestBootstrapMappedLocalCatalogHeapsIncludesPgStatisticExtData|TestPgStatisticExtDataStxoidInhIndexInitialEntry|TestNailedLocalRelsContainsPgStatisticExtDataStxoidInhIndex|TestPgStatisticExtDataAttrsTypeOIDsMatchPG18|TestPgTypeAlignAndStorageFor_pg_statisticArray|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3cb (TestMigrationFromLegacyJSONCluster,
  TestMigrationIdempotent, TestMigrationPGAttributeRowsWritten,
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
  TestSynchronousCommitFlushesByDefault) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next anticipated blocker

With the heap (3429) + composite PKEY index (3433) both seeded,
`pg_statistic_ext_data` is fully wired for PG18 phase-3 relcache
initialisation. The next FATAL on E2E re-run is anticipated to lie in
the `pg_subscription` (6100) / `pg_subscription_rel` (6102) /
`pg_statistic` (2619) / `pg_statistic_ext` (3381) catalog territory
(Step 3cd).
