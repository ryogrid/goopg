# M0106-0010 Step 3bj — pg_language_name_index (OID 2681) catalog seed

## Context

E2E test `TestE2E_FailoverGoopgToPG/async` (gated by
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) drives a real PG18 standby boot from
goopg's bootstrap output. After Step 3bi seeded
`pg_foreign_table_relid_index` (OID 3119), the standby advanced past
that FATAL and surfaced the next blocker:

```
FATAL: could not open relation with OID 2681
```

OID 2681 = `pg_language_name_index` per
`postgres/src/include/catalog/pg_language.h:69`:

```c
DECLARE_UNIQUE_INDEX(pg_language_name_index, 2681,
    LanguageNameIndexId, pg_language,
    btree(lanname name_ops));
MAKE_SYSCACHE(LANGNAME, pg_language_name_index, 4);
```

This is a `DECLARE_UNIQUE_INDEX` (not the `_PKEY` variant) — UNIQUE but
not PRIMARY. pg_language's primary key is OID 2682
(`pg_language_oid_index`, single oid_ops over the pg_class OID column).

The pg_language heap (OID 2612) is already a nailed local rel
(`internal/initdb/relcache_init.go:125`). The PG-rejection path is the
same as Steps 3bd/3bf/3bg/3bh/3bi: no `pg_class` row was being written
for the index OID, so `RelationIdGetRelation(2681)` returned NULL and
FATALed at `postgres/src/backend/access/common/relation.c:61`.

## Fix

Pure catalog-seed addition. No encoder, no builder, no `Init` flow
change. Mirrors the single-column `name_ops` UNIQUE pattern of
`pg_database_datname_index` (2671), `pg_authid_rolname_index` (2676),
`pg_namespace_nspname_index` (2684).

### (a) `internal/initdb/initdb.go::pgIndexInitialEntries`

After the Step 3bi 3119 entry, append:

```go
entry(2681, 2612, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false)
```

* `IndRelid = 2612` — pg_language heap OID
* `IndKey = [2]` — `Anum_pg_language_lanname = 2` (per pg_language_d.h)
* `IndClass = [nameOps]` (1986)
* `IndCollation = [cCollation]` (950 = C_COLLATION_OID; mandatory for
  name/text key columns)
* `IsUnique = true, IsPrimary = false`

### (b) `internal/initdb/relcache_init.go::nailedLocalRels`

Add a new `idxSpec` after the Step 3bi 3119 entry:

```go
{2681, "pg_language_name_index"},
```

`flattenRels` derives the nailed `nailedRel` via `pgIndexNattsByOID()`,
giving `RelKind='i'`, `RelNatts=1`. This satisfies
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
`postgres/src/backend/utils/cache/relcache.c:1492` (`pgIndexNattsByOID`
returns 1 for OID 2681 because `len(IndKey) == 1`).

### (c) Empty btree placeholder

Add `2681` to all three placeholder OID lists at
`bootstrapPostgresDatabase` (`base/1/`, `base/5/`, `global/`) so PG's
`mdopen` finds a valid empty-btree file before any later step
overwrites the metapage with a populated 2-page btree.

Step-3k empty-btree placeholder (`btm_root = P_NONE`) is sufficient
because pg_language is currently unpopulated — the index has zero rows
to point at. If a future step seeds language rows
(`internal`, `c`, `sql` per pg_language.dat), the populated btree will
need leaves keyed by name; that scope is deferred.

### TID flow-through

The new heap row for 2681 lands automatically through the existing
plumbing:

* `bootstrapPgClassTuples` writes the `Form_pg_class` row (via
  `flattenRels`).
* `bootstrapPgAttributeTuples` writes one `pg_attribute` row (the
  single-column key).
* `bootstrapPgIndexTuples` writes the `Form_pg_index` row and captures
  the heap TID in `pgIndexTIDs`.
* `bootstrapPgIndexIndexrelidIndex` adds a new leaf at file 2679 so
  `SearchSysCache1(INDEXRELID, 2681)` resolves.
* `bootstrapPgClassOidIndex` adds a leaf at file 2662 so
  `SearchSysCache1(RELOID, 2681)` resolves.
* `bootstrapPgAttributeRelidAttnumIndex` adds a composite-key leaf at
  file 2659 so PG's `RelationBuildTupleDesc` lookups resolve.

## Test pins

New file `internal/initdb/pg_language_name_index_test.go`:

* `TestPgLanguageNameIndexSeededFromInitialEntries` — asserts
  `(IndRelid=2612, IndKey=[2], IsUnique=true, IsPrimary=false,
  IndCollation=[950])`.
* `TestNailedLocalRelsContainsPgLanguageNameIndex` — asserts
  `RelName="pg_language_name_index", RelKind='i', RelNatts=1`.

Existing pins extended:

* `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `2681: {2}` to
  the authoritative map; the strict count guard auto-rejects future
  additions without the matching map update.
* `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — adds `2681` so the populated 2679 btree must include this OID's
  leaf (which it will, automatically, via the TID-flow plumbing).

## Verification

* `go test -count=1 -run 'TestPgLanguageNameIndex|TestNailedLocalRelsContainsPgLanguageNameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestPgForeignTableRelidIndex|TestNailedLocalRelsContainsPgForeignTableRelidIndex' ./internal/initdb/` — PASS.
* `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3bi (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
* `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next blocker (Step 3bk territory)

After Step 3bj clears OID 2681 in the next E2E re-run, the most
probable next FATAL is OID 2682 (`pg_language_oid_index` — the PKEY
companion to 2681); the placeholder is already in the OID list since
earlier steps but no `pgIndexInitialEntries` row exists for it.
