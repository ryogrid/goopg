# M0106-0010 Step 3cb: pg_sequence (OID 2224) nailed local rel

**Status**: LANDED 2026-05-18

## Problem

After Step 3ca seeded the `pg_replication_origin` (OID 6000) shared
catalog plus its two indexes (6001 / 6002), the next PG-standby boot
FATAL becomes:

```
WARNING:  you don't own a lock of type AccessShareLock
FATAL:  could not open relation with OID 2224
```

OID 2224 is `pg_sequence` per
`postgres/src/include/catalog/pg_sequence.h:23`
(`CATALOG(pg_sequence,2224,SequenceRelationId)`). Every PG18 backend
hits this lookup very early during phase-3 relcache initialization, so
no user-issued query can run until the catalog is opened.

## Decision

Family-complete seed in one step: heap 2224 + its single PG-declared
UNIQUE PRIMARY index 5002 (`pg_sequence_seqrelid_index`, btree on
`seqrelid oid_ops`). Index 5002 is the only declared index — backs
`MAKE_SYSCACHE(SEQRELID, …, 32)`.

`pg_sequence` is a per-database (non-shared) catalog, so it follows the
Step 3bz `pg_range` template, not the Step 3ca `pg_replication_origin`
shared-rel template.

## Implementation

### `internal/initdb/relcache_init.go`

1. **`pgSequenceAttrs()`** new helper that returns the 8-column PG18
   schema (verbatim from
   `postgres/src/include/catalog/pg_sequence.h:23-33`):

   | attnum | name         | typeOID | len | notnull |
   | ------ | ------------ | ------- | --- | ------- |
   | 1      | seqrelid     | 26      | 4   | true    |
   | 2      | seqtypid     | 26      | 4   | true    |
   | 3      | seqstart     | 20      | 8   | true    |
   | 4      | seqincrement | 20      | 8   | true    |
   | 5      | seqmax       | 20      | 8   | true    |
   | 6      | seqmin       | 20      | 8   | true    |
   | 7      | seqcache     | 20      | 8   | true    |
   | 8      | seqcycle     | 16      | 1   | true    |

   pg_sequence has no `oid` system column — attnums start at 1.
   RelType=83 is safe because `pg_sequence` is not formrdesc'd (no
   `SequenceRelation_Rowtype_Id` constant in PG18 headers).

2. **`nailedLocalRels`** gains
   `{2224, "pg_sequence", 83, 'r', 8, false, pgSequenceAttrs()}` heap
   entry right after the Step 3bz `pg_range` (3541) entry.

3. **`idxSpec`** list gains
   `{5002, "pg_sequence_seqrelid_index"}` after the Step 3bz 2228 entry.

### `internal/initdb/initdb.go`

4. **`pgIndexInitialEntries`** local section gains
   `entry(5002, 2224, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
   after the Step 3bz 2228 entry. UNIQUE PRIMARY (PKEY) single oid_ops
   key (no collation) over pg_sequence heap OID 2224. Without this row
   `RelationIdGetRelation(5002)` FATALs.

5. **`bootstrapMappedLocalCatalogHeaps`** OID list gains
   `2224, // pg_sequence (M0106-0010 step 3cb)` after the Step 3bz
   `3541` entry. Also fixes a long-standing stale comment that
   mis-labelled OID `6102` as `pg_sequence`; the true OID of
   `pg_subscription_rel` is 6102, and the true OID of `pg_sequence` is
   2224 (the new entry).

6. **`localRelMap`** in `bootstrapPostgresDatabase` gains
   `{2224, 2224}` after the Step 3bz `{3541, 3541}` entry. Same stale
   `6102 → pg_sequence` comment fixed.

7. Both "Critical index placeholder pages" OID lists (the
   `base/<dboid>/` block and the `global/` fallback block) gain
   `5002, // pg_sequence_seqrelid_index (Step 3cb)` after the
   Step 3bz `2228` entries.

   The empty-btree placeholder is sufficient because pg_sequence is
   unpopulated at bootstrap (sequences are created by `CREATE SEQUENCE`
   at runtime, not at initdb).

### Regression pins

New file `internal/initdb/pg_sequence_nailed_test.go`:

- `TestNailedLocalRelsContainsPgSequence` — pins the heap entry and
  all 8 column descriptors verbatim.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgSequence` — pins
  that an 8-KiB initialized heap page is written at `base/1/2224`
  and `base/5/2224`.
- `TestPgSequenceSeqrelidIndexInitialEntry` — pins the
  `pgIndexInitialEntries` row for OID 5002 (UNIQUE PRIMARY, IndKey=[1]).
- `TestNailedLocalRelsContainsPgSequenceSeqrelidIndex` — pins the
  flattenRels-derived index entry for OID 5002 (RelKind='i',
  RelNatts=1).

Existing regression pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — map gains
  `5002: {1}` (strict count guard).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — extended with `5002`.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  — extended with `2224` (strict list guard).

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgSequence|TestBootstrapMappedLocalCatalogHeapsIncludesPgSequence|TestPgSequenceSeqrelidIndexInitialEntry|TestNailedLocalRelsContainsPgSequenceSeqrelidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3ca (`TestMigrationFromLegacyJSONCluster`,
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
  `TestSynchronousCommitFlushesByDefault`); no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next anticipated blocker

With the heap (2224) + PKEY index (5002) both seeded, `pg_sequence` is
fully wired for PG18 phase-3 relcache initialization. The next FATAL on
E2E re-run is anticipated to lie in the `pg_subscription` /
`pg_subscription_rel` / `pg_statistic` / `pg_statistic_ext` catalog
territory (Step 3cc).
