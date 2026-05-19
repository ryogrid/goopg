# M0106-0010 Step 3bg — pg_foreign_server_oid_index (OID 113) catalog seed

## Context

Step 3bf (2026-05-18) closed the FATAL `could not open relation with OID 549`
PG-standby boot blocker by seeding `pg_foreign_server_name_index` (OID 549,
UNIQUE non-PKEY on `srvname name_ops`). Re-running
`TestE2E_FailoverGoopgToPG/async` after Step 3bf surfaces the next blocker —
the companion deferred at Step 3bf's tail:

```
FATAL: could not open relation with OID 113
```

OID 113 is `pg_foreign_server_oid_index`, the UNIQUE PRIMARY KEY backing
`FOREIGNSERVEROID` syscache.

## Authoritative source

`postgres/src/include/catalog/pg_foreign_server.h:58`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_server_oid_index, 113,
    ForeignServerOidIndexId, pg_foreign_server,
    btree(oid oid_ops));
MAKE_SYSCACHE(FOREIGNSERVEROID, pg_foreign_server_oid_index, 2);
```

- Heap OID 1417 = `pg_foreign_server` (Step 3be nailed rel).
- Single key `oid` (attnum 1) under `oid_ops`, collation 0.
- UNIQUE **PRIMARY** KEY (`DECLARE_UNIQUE_INDEX_PKEY`, not the bare
  `DECLARE_UNIQUE_INDEX` of 549).

## Change

Pure catalog-seed addition — no encoder, builder, or `Init` flow change.
Mirrors Step 3bd (`pg_foreign_data_wrapper_oid_index`, OID 112) — also a
single-column oid PKEY companion to a sibling `name_ops` UNIQUE.

1. `internal/initdb/initdb.go::pgIndexInitialEntries` appends after the
   Step 3bf entry:
   ```go
   entry(113, 1417, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)
   ```
2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec list gains
   `{113, "pg_foreign_server_oid_index"}` after the Step 3bf entry.
   `flattenRels` + `pgIndexNattsByOID()` derives `RelKind='i', RelNatts=1`
   so PG's `RelationInitIndexAccessInfo` `relnatts == indnatts` check
   (relcache.c:1492) passes.
3. Three empty-placeholder OID lists at `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain `113` so PG's `mdopen` finds a
   valid empty-btree file before `bootstrapPgIndexIndexrelidIndex`
   overwrites the metapage. The Step-3k empty btree is sufficient because
   `pg_foreign_server` is currently unpopulated.

The seed flows automatically through `bootstrapPgClassTuples` →
`bootstrapPgAttributeTuples` → `bootstrapPgIndexTuples` (writes
`Form_pg_index` row + captures TID in `pgIndexTIDs[113]`) →
`bootstrapPgIndexIndexrelidIndex` (adds leaf to populated 2-page btree at
file 2679) → `bootstrapPgClassOidIndex` (leaf at file 2662) →
`bootstrapPgAttributeRelidAttnumIndex` (composite-key leaf at file 2659).

## Tests

- New file `internal/initdb/pg_foreign_server_oid_index_test.go`:
  - `TestPgForeignServerOidIndexSeededFromInitialEntries` — pins
    `(IndRelid=1417, IndKey=[1], IsUnique=true, IsPrimary=true,
    IndCollation=[0])` against `pg_foreign_server.h:58`.
  - `TestNailedLocalRelsContainsPgForeignServerOidIndex` — pins
    `RelName="pg_foreign_server_oid_index", RelKind='i', RelNatts=1`.
- Existing pins extended:
  - `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `113:{1}`
    (strict count guard catches future adds without map updates).
  - `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
    extended with 113 so the populated 2679 btree must carry the leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestPgForeignServerOidIndex|TestNailedLocalRelsContainsPgForeignServerOidIndex|TestPgForeignServerNameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgForeignServer|TestBootstrapMappedLocalCatalogHeaps'
  ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bf (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.

E2E re-run (`GOOPG_RUN_BLOCKED_M0102_E2E=1
TestE2E_FailoverGoopgToPG/async`) should advance past the
`could not open relation with OID 113` FATAL to the next blocker
(Step 3bh territory).
