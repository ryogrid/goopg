# M0106-0010 Step 3aw — pg_extension nailed-rel seed

## Context

Step 3at landed the pg_event_trigger index pair (3467 / 3468). The next
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` blocker
surfaces as

```
FATAL: could not open relation with OID 3079
```

OID 3079 is `pg_extension` per
`postgres/src/include/catalog/pg_extension_d.h:23`
(`#define ExtensionRelationId 3079`). PG's backend `InitPostgres` opens
`pg_extension` during early startup; without a `pg_class` row,
`RelationBuildDesc(3079) → ScanPgRelation(3079)` returns NULL and the
backend FATALs in `relation.c:61`.

Step 3au's investigation note flagged a hidden prerequisite:
`bootstrapPgAttributeRelidAttnumIndex` (OID 2659) builds a populated
btree from every `attnum > 0` row across all nailed rels, and the
legacy single-leaf-root encoder caps at 407 fixed-size tuples. Adding
pg_extension's 8 pg_attribute rows pushes the tuple count past that cap
and triggers `btree leaf overflow inserting tuple at lower=…`. Step 3av
unblocked this by replacing `pgBuildBtreeMetapageWithRoot ‖
pgBuildBtreeLeafRootPage` with `pgBuildBtreeBulkLoad`, which falls
through to a PG18 nbtsort-format 2-level tree (metapage + N leaves +
internal root) when the input exceeds the single-leaf cap. Step 3aw
applied here is therefore a pure catalog-seed change.

## Authoritative source

`postgres/src/include/catalog/pg_extension.h`:

```
CATALOG(pg_extension,3079,ExtensionRelationId)
{
    Oid       oid;
    NameData  extname;
    Oid       extowner BKI_LOOKUP(pg_authid);
    Oid       extnamespace BKI_LOOKUP(pg_namespace);
    bool      extrelocatable;
#ifdef CATALOG_VARLEN
    text      extversion BKI_FORCE_NOT_NULL;
    Oid       extconfig[1] BKI_LOOKUP(pg_class);
    text      extcondition[1];
#endif
} FormData_pg_extension;
```

The header comment at line 39 documents the nullability split:
"extversion may never be null, but the others can be." Companion
declarations at lines 56-57 declare two indexes:

```
DECLARE_UNIQUE_INDEX_PKEY(pg_extension_oid_index, 3080,
                          ExtensionOidIndexId, pg_extension,
                          btree(oid oid_ops));
DECLARE_UNIQUE_INDEX(pg_extension_name_index, 3081,
                     ExtensionNameIndexId, pg_extension,
                     btree(extname name_ops));
```

Indexes 3080 / 3081 are deferred to follow-up steps (3ax / 3ay) to
preserve the single-OID rhythm. Empty-btree placeholders for these
OIDs already exist via the Step 3k metapage seed (they were present in
the placeholder OID lists before Step 3aw).

## Changes

### `internal/initdb/relcache_init.go`

- New `pgExtensionAttrs()` returning the 8-column PG18 schema:
  - oid (TypeOID 26, Len 4, NOT NULL)
  - extname (TypeOID 19 name, Len 64, NOT NULL)
  - extowner (TypeOID 26, Len 4, NOT NULL)
  - extnamespace (TypeOID 26, Len 4, NOT NULL)
  - extrelocatable (TypeOID 16 bool, Len 1, NOT NULL)
  - extversion (TypeOID 25 text, Len -1, NOT NULL via BKI_FORCE_NOT_NULL)
  - extconfig (TypeOID 1028 oid[], Len -1, nullable)
  - extcondition (TypeOID 1009 text[], Len -1, nullable)
- `nailedLocalRels` gains
  `{3079, "pg_extension", 83, 'r', 8, false, pgExtensionAttrs()}`. RelType
  83 is safe — no `ExtensionRelation_Rowtype_Id` constant exists in PG18
  headers so the Step 3v Phase-3 assertion does not fire.

### `internal/initdb/initdb.go`

- `bootstrapMappedLocalCatalogHeaps` OID list gains `3079` so an
  `InitPage`-stamped 8 KiB heap exists at `base/{1,5}/3079` before PG's
  `mdopen` is exercised.
- `localRelMap` gains `{3079, 3079}` so the relfilenode mapper resolves
  OID 3079 to its own backing file.

The seed threads automatically through the existing bootstrap flow:
`bootstrapPgClassTuples` writes the Form_pg_class row,
`bootstrapPgAttributeTuples` writes 8 pg_attribute rows,
`bootstrapPgClassOidIndex` adds the leaf for 3079 at file 2662, and
`bootstrapPgAttributeRelidAttnumIndex` (which now uses Step 3av's
multi-leaf `pgBuildBtreeBulkLoad`) adds 8 composite-key leaves to
`base/{1,5}/2659 + global/2659`.

## Multi-leaf cap crossover

With pg_extension's 8 attnum>0 rows added, the total leaf-tuple count
for the populated `pg_attribute_relid_attnum_index` btree (OID 2659)
crosses the 407-tuple single-leaf threshold for the first time. The
runtime confirms the slow-path activates and produces a 4-block file
(metapage at block 0 + 2 leaves at blocks 1-2 + internal root at block
3). Step 3av's `TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18` pins
the on-disk format; this loop verifies the end-to-end test pin
(`TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree`) was
already future-proofed by relaxing its size check from a hard `2 *
BlockSize` to a positive multiple, walking each leaf block in turn and
skipping P_HIKEY at slot 1 on non-rightmost leaves.

## Regression pins

New file `internal/initdb/pg_extension_nailed_test.go`:

- `TestNailedLocalRelsContainsPgExtension` — asserts every per-column
  `(Name, TypeOID, Num, Len, NotNull)` against the pg_extension.h
  declarations. Prevents future column-rearrangement bugs.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgExtension` — asserts
  `base/{1,5}/3079` exists with InitPage-stamped 8 KiB content.

Existing pins extended:

- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  gains `3079` so the placeholder list cannot silently drop it.
- `TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree`
  rewritten to walk the metapage's root pointer and iterate each leaf
  page (skipping P_HIKEY at slot 1 on non-rightmost leaves), so the
  pin survives the bulk-load fast/slow path crossover.

## Verified

- `go build ./...` PASS
- `go test -count=1 -run 'TestNailedLocalRelsContainsPgExtension|TestBootstrapMappedLocalCatalogHeapsIncludesPgExtension|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree|TestPgBuildBtreeBulkLoad' ./internal/initdb/`
  PASS
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3at, no new regressions
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS

## Next blocker (Step 3ax candidate)

The pg_extension companion indexes 3080
(`pg_extension_oid_index`, UNIQUE PRIMARY KEY on `oid`) and 3081
(`pg_extension_name_index`, UNIQUE non-PKEY on `extname name_ops`) are
expected to surface in turn on the next E2E re-run. Both follow the
established single-OID catalog-seed patterns documented in Steps
3ab/3ai/3am/3ao/3at (oid PKEY) and Steps 3t/3as/3aj (name_ops single-key).
