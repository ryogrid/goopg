# M0106-0010 Step 3bw — pg_publication_oid_index (OID 6110)

**Status:** Landed 2026-05-18.

## Problem

After Step 3bv seeded `pg_publication_pubname_index` (OID 6111), PG-standby
boot's next FATAL is:

```
FATAL:  could not open relation with OID 6110
```

OID 6110 is `pg_publication_oid_index`, declared in
`postgres/src/include/catalog/pg_publication.h:72`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_publication_oid_index, 6110,
    PublicationObjectIndexId, pg_publication, btree(oid oid_ops));
...
MAKE_SYSCACHE(PUBLICATIONOID, pg_publication_oid_index, 8);
```

It is the UNIQUE PRIMARY companion of `pg_publication_pubname_index` (6111)
and backs the `PUBLICATIONOID` syscache. PG's
`RelationCacheInitializePhase3 → load_critical_index(6110)` requires both a
`pg_class` row (or equivalent nailed entry) and a `pg_index` row to be
present, plus an on-disk placeholder file so `mdopen` does not fail before
the relcache catches up.

## Fix

Pure catalog-seed addition mirroring the single-column `oid_ops` UNIQUE
PRIMARY pattern of Steps 3bk (pg_language_oid_index, 2682), 3ax
(pg_extension_oid_index, 3080), 3at (pg_event_trigger_oid_index, 3468),
3bd (pg_foreign_data_wrapper_oid_index, 112), 3bg
(pg_foreign_server_oid_index, 113), 3bo (pg_opfamily_oid_index, 2755), and
3br (pg_parameter_acl_oid_index, 6247). No encoder/builder/Init flow change.

### Edits

1. **`internal/initdb/initdb.go` — `pgIndexInitialEntries`**
   adds, after the Step 3bv 6111 row:

   ```go
   entry(6110, 6104, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_publication_oid_index
   ```

   UNIQUE PRIMARY single `oid_ops` key (no collation) over pg_publication
   heap OID 6104 (Step 3bu nailed local rel). pg_publication's `oid` system
   column is attnum 1.

2. **`internal/initdb/relcache_init.go` — `nailedLocalRels` idxSpec list**
   adds, after the Step 3bv `{6111, "pg_publication_pubname_index"}`:

   ```go
   {6110, "pg_publication_oid_index"},
   ```

   `flattenRels`+`pgIndexNattsByOID` derives `RelKind='i', RelNatts=1`, so
   the `relnatts==indnatts` check (relcache.c:1492) passes.

3. **`internal/initdb/initdb.go` — three critical-index placeholder OID
   lists in `bootstrapPostgresDatabase`** (`base/1/`, `base/5/`, `global/`)
   each gain `6110, // pg_publication_oid_index (Step 3bw)` after the
   Step 3bv 6111 entry. Empty-btree placeholder is sufficient because
   pg_publication is unpopulated at bootstrap (Step-3k
   `makeBtreeRootPage` with `btm_root=P_NONE`).

### Regression pins

`internal/initdb/pg_publication_oid_index_test.go`:

- `TestNailedLocalRelsContainsPgPublicationOidIndex` — RelName, RelKind='i',
  RelNatts=1.
- `TestPgPublicationOidIndexInitialEntry` — IndRelid=6104, IndKey=[1],
  IndClass=[1981] (oid_ops), IndCollation=[0], IsUnique=true,
  IsPrimary=true.

Existing test extensions:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` gains `6110:{1}` (strict
  count guard forces future additions to update).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave` gains
  `6110` so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgPublicationOidIndex|TestPgPublicationOidIndexInitialEntry|TestNailedLocalRelsContainsPgPublicationPubnameIndex|TestPgPublicationPubnameIndexInitialEntry|TestNailedLocalRelsContainsPgPublication|TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bv (no new regressions; tracked under M0106-0012 etc.).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## What's next

With Steps 3bu / 3bv / 3bw landed, the pg_publication family is fully
seeded (heap + both declared indexes — UNIQUE name PKEY 6111 and UNIQUE
PRIMARY oid PKEY 6110). The next E2E re-run is expected to advance past
OID 6110 and surface the next missing nailed catalog as the next
`could not open relation with OID …` FATAL.
