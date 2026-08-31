# M0106-0010 Step 3bu: pg_publication nailed local rel

## Context

After Step 3bt seeded `pg_partitioned_table_partrelid_index` (OID 3351),
the next `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
re-run surfaced:

```
psql: error: connection to server at "127.0.0.1", port 34467 failed:
FATAL:  could not open relation with OID 6104
```

OID 6104 is `pg_publication` per
`postgres/src/include/catalog/pg_publication.h:29`
(`CATALOG(pg_publication,6104,PublicationRelationId)`).

PG's `RelationBuildDesc(6104) → ScanPgRelation(6104)` returned NULL
because no pg_class row was seeded for OID 6104; the backend FATALs
from `postgres/src/backend/access/common/relation.c:61`.

## Change

Pure catalog-seed addition mirroring the nailed-rel pattern of Steps 3w
(pg_aggregate=2600), 3aa (pg_cast=2605), 3ag (pg_conversion=2607), 3ak
(pg_default_acl=826), 3an (pg_enum=3501), 3ar (pg_event_trigger=3466),
3aw (pg_extension=3079), 3bb (pg_foreign_data_wrapper=2328), 3be
(pg_foreign_server=1417), 3bh (pg_foreign_table=3118), 3bm
(pg_opfamily=2753), 3bp (pg_parameter_acl=6243), and 3bs
(pg_partitioned_table=3350). No encoder, builder, or `Init` flow change.

### Files touched

- `internal/initdb/relcache_init.go`
  - New `pgPublicationAttrs()` returning the 10-column PG18 schema
    verbatim from `pg_publication.h`. All ten columns are fixed-width
    and NOT NULL:
    - oid          (oid 26 / 4)
    - pubname      (name 19 / 64)         — BKI_FORCE_NOT_NULL via NameData
    - pubowner     (oid 26 / 4)           — BKI_LOOKUP(pg_authid)
    - puballtables (bool 16 / 1)
    - pubinsert    (bool 16 / 1)
    - pubupdate    (bool 16 / 1)
    - pubdelete    (bool 16 / 1)
    - pubtruncate  (bool 16 / 1)
    - pubviaroot   (bool 16 / 1)
    - pubgencols   (char 18 / 1)
  - `nailedLocalRels` gains
    `{6104, "pg_publication", 83, 'r', 10, false, pgPublicationAttrs()}`.
    `RelType=83` is safe because pg_publication is not formrdesc'd (no
    `PublicationRelation_Rowtype_Id` constant in PG18), so Step 3v's
    `relation->rd_att->tdtypeid == relp->reltype` Phase-3 assertion does
    not fire.

- `internal/initdb/initdb.go`
  - `bootstrapMappedLocalCatalogHeaps` OID list gains `6104` so an
    `InitPage`-stamped 8 KiB empty heap is written to
    `base/{1,5}/6104` before PG's `mdopen`. The pre-existing stray
    `6003 // pg_publication` entry is retained (its comment is stale —
    no upstream catalog uses OID 6003 — but the placeholder file does
    no harm).
  - `localRelMap` gains `{6104, 6104}` so PG's relfilenode mapper
    resolves OID 6104 to its backing file. Same stale-comment caveat
    on `{6003, 6003}` applies.

### Companion indexes (deferred)

`pg_publication.h:69` declares two indexes:

- 6110 = `pg_publication_oid_index`
  (UNIQUE PRIMARY KEY, `btree(oid oid_ops)`, backs
  `MAKE_SYSCACHE(PUBLICATIONOID, …, 8)`).
- 6111 = `pg_publication_pubname_index`
  (UNIQUE non-PKEY, `btree(pubname name_ops)`, backs
  `MAKE_SYSCACHE(PUBLICATIONNAME, …, 8)`).

Deferred to subsequent steps in the established single-OID rhythm
(Steps 3w → 3aa → 3ag → 3ak → 3an → 3ar → 3aw → 3bb → 3be → 3bh →
3bm → 3bp → 3bs). The actual next E2E blocker is OID 6111 (verified
post-implementation).

## Threading

The new `nailedLocalRels` entry flows automatically through:

- `bootstrapPgClassTuples` (writes Form_pg_class row),
- `bootstrapPgAttributeTuples` (writes 10 Form_pg_attribute rows),
- `bootstrapPgClassOidIndex` (leaf for 6104 at file 2662),
- `bootstrapPgAttributeRelidAttnumIndex` (10 composite-key leaves at
  file 2659; Step 3av's bulk-load builder handles the bookkeeping),
- `writeRelcacheInitFile` (emits Form_pg_class + 10 Form_pg_attribute
  blob group).

## Regression pins

`internal/initdb/pg_publication_nailed_test.go`:

- `TestNailedLocalRelsContainsPgPublication` — full per-column
  `(Name, TypeOID, Num, Len, NotNull)` audit against
  `pg_publication.h`.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication` —
  asserts `base/{1,5}/6104` exists, is exactly 8 KiB, and
  InitPage-stamped.

Existing pin extended:

- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  gains 6104 so the placeholder OID list cannot silently drop
  pg_publication.

## Why this is safe

* Pure catalog-seed addition; no encoder / builder / Init flow change.
* All ten columns are fixed-width, so no varlena encoding is exercised
  (the heap is empty at bootstrap — no publications are created during
  initdb).
* `RelType=83` (non-formrdesc'd placeholder) — Step 3v's tdtypeid
  assertion only engages for catalogs with a BKI_ROWTYPE_OID, which
  pg_publication does not have.
* Mirrors the proven 10-column-or-fewer fixed-width nailed-rel pattern
  already in service for pg_default_acl (826), pg_event_trigger
  (3466), pg_opfamily (2753), and pg_partitioned_table (3350).

## Verification

* `go build ./...` — PASS.
* `go test -count=1 -run
  'TestNailedLocalRelsContainsPgPublication|TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedLocalRelsContainsPgPartitionedTable|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18'
  ./internal/initdb/` — PASS.
* `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bt (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
* Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.
* E2E re-run with `GOOPG_RUN_BLOCKED_M0102_E2E=1
  TestE2E_FailoverGoopgToPG/async` advances past OID 6104 and surfaces
  the next blocker `FATAL: could not open relation with OID 6111`
  (pg_publication_pubname_index — Step 3bv territory).

## Next blocker (Step 3bv)

OID 6111 = `pg_publication_pubname_index` (per `pg_publication.h:69`,
UNIQUE non-PKEY `btree(pubname name_ops)`, backing
`MAKE_SYSCACHE(PUBLICATIONNAME, …, 8)`). Same single-OID
catalog-seed-addition pattern applies, mirroring the proven name-index
pattern used for `pg_parameter_acl_parname_index` (Step 3bq).
