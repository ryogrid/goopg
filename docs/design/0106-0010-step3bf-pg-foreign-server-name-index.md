# M0106-0010 Step 3bf: pg_foreign_server_name_index (OID 549)

## Context

After M0106-0010 Step 3be added `pg_foreign_server` (OID 1417) to
`nailedLocalRels`, rerunning
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` surfaced the
next PG-standby boot blocker:

```
FATAL:  could not open relation with OID 549
```

OID 549 is `pg_foreign_server_name_index`, per
`postgres/src/include/catalog/pg_foreign_server.h:55`:

```c
DECLARE_UNIQUE_INDEX(pg_foreign_server_name_index, 549,
    ForeignServerNameIndexId, pg_foreign_server,
    btree(srvname name_ops));
MAKE_SYSCACHE(FOREIGNSERVERNAME,
    pg_foreign_server_name_index, 2);
```

Step 3be intentionally deferred both companion indexes — OID 113
(`pg_foreign_server_oid_index`, UNIQUE PKEY) and OID 549
(`pg_foreign_server_name_index`, UNIQUE non-PKEY) — until a concrete E2E
blocker surfaced. The E2E test surfaced 549 first (not the OID companion
113), matching the FDW-side pattern observed in Steps 3bc → 3bd:
`process_settings → catcache init` opens `FOREIGNSERVERNAME` before
`FOREIGNSERVEROID`.

## Change

Pure catalog-seed change mirroring Step 3bc's
`pg_foreign_data_wrapper_name_index` (OID 548). No encoder, builder, or
`Init` flow change.

### `internal/initdb/initdb.go`

1. `pgIndexInitialEntries` gains one entry immediately after Step 3bd's
   OID 112 row:

   ```go
   entry(549, 1417, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false),
   ```

   - `IndexRelid = 549`
   - `IndRelid = 1417` (pg_foreign_server heap)
   - `IndKey = {2}` — `srvname` per `pg_foreign_server_d.h`
   - `IndClass = {nameOps}` (1986)
   - `IndCollation = {C_COLLATION_OID}` (950)
   - `IsUnique = true, IsPrimary = false` — matches PG's
     `DECLARE_UNIQUE_INDEX` (not `_PKEY`)

2. Three empty-placeholder OID lists (base/1/, base/5/, global/) gain
   `549` so PG's `mdopen` finds an InitPage-stamped placeholder file
   before `bootstrapPgIndexIndexrelidIndex` overwrites the metapage.

### `internal/initdb/relcache_init.go`

`nailedLocalRels` idxSpec list gains `{549, "pg_foreign_server_name_index"}`
after Step 3bd's OID 112. `flattenRels` consults `pgIndexNattsByOID()` —
which returns `1` for OID 549 thanks to the new `pgIndexInitialEntries`
row — so the nailed rel ends up with `RelKind='i', RelNatts=1` and
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` check passes
(relcache.c:1492).

### Flow-through

The single nailedLocalRels entry threads automatically through:

- `bootstrapPgClassTuples` — writes the `Form_pg_class` row for OID 549.
- `bootstrapPgAttributeTuples` — writes 1 `pg_attribute` row for the
  single-column index (TID `(srvname)` schema derived from
  `pgIndexNattsByOID`).
- `bootstrapPgClassOidIndex` — adds a leaf for OID 549 to
  `base/{1,5}/2662` + `global/2662`.
- `bootstrapPgAttributeRelidAttnumIndex` — adds 1 composite-key
  `(549, 1)` leaf to `base/{1,5}/2659` + `global/2659`.
- `bootstrapPgIndexTuples` — writes a `Form_pg_index` row keyed at
  IndexRelid=549 to `base/{1,5}/2610`.
- `bootstrapPgIndexIndexrelidIndex` — adds a leaf at IndexRelid=549 to
  `base/{1,5}/2679` + `global/2679`.
- `writeRelcacheInitFile` — emits a `Form_pg_class` + 1
  `Form_pg_attribute` blob group.

OID 113 (`pg_foreign_server_oid_index`, UNIQUE PKEY on `oid_ops`) is
intentionally deferred to Step 3bg — matching the single-OID-per-step
rhythm of Steps 3w → 3aa → 3ag → 3ak → 3an → 3ar → 3aw → 3bb → 3be →
3bf.

## Tests

### New file `internal/initdb/pg_foreign_server_name_index_test.go`

- `TestPgForeignServerNameIndexSeededFromInitialEntries` — asserts the
  `pgIndexInitialEntries` row exists with the canonical
  `(IndRelid, IndKey, IsUnique, IsPrimary, IndCollation)`.
- `TestNailedLocalRelsContainsPgForeignServerNameIndex` — asserts the
  `nailedLocalRels` entry exists with `RelKind='i', RelNatts=1`.

### Extended existing pins

- `pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18` —
  pinned map adds `549: {2}` so the count guard rejects future adds
  without map updates.
- `btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — list gains 549 so the SHARED critical-index Phase 3 pass cannot
  silently drop pg_foreign_server_name_index from the populated btree.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestPgForeignServerNameIndex|TestNailedLocalRelsContainsPgForeignServerNameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedLocalRelsContainsPgForeignServer|TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignServer|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3be (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

E2E re-run will close the OID 549 FATAL and surface the next blocker
(likely OID 113 = `pg_foreign_server_oid_index` once `FOREIGNSERVEROID`
opens, or a different relation if the catalog scan takes a different
path).

## References

- `postgres/src/include/catalog/pg_foreign_server.h:55` — DECLARE_UNIQUE_INDEX.
- `postgres/src/include/catalog/pg_foreign_server_d.h:24` —
  `ForeignServerNameIndexId = 549`.
- `postgres/src/backend/utils/cache/relcache.c:1492` —
  `relnatts == indnatts` assertion.
- M0106-0010 Step 3be: pg_foreign_server (OID 1417) nailed rel.
- M0106-0010 Step 3bc: pg_foreign_data_wrapper_name_index (OID 548) —
  template pattern.
