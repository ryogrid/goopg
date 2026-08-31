# M0106-0010 Step 3bs: pg_partitioned_table nailed local rel

## Context

After Step 3br seeded `pg_parameter_acl_oid_index` (OID 6247), the next
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` re-run
surfaced:

```
psql: error: connection to server at "127.0.0.1", port 39073 failed:
FATAL:  could not open relation with OID 3350
```

OID 3350 is `pg_partitioned_table` per
`postgres/src/include/catalog/pg_partitioned_table_d.h:23`
(`#define PartitionedRelationId 3350`).

PG's `RelationBuildDesc(3350) → ScanPgRelation(3350)` returned NULL
because no pg_class row was seeded for OID 3350; the backend FATALs
from `postgres/src/backend/access/common/relation.c:61`.

## Change

Pure catalog-seed addition mirroring the nailed-rel pattern of Steps 3w
(pg_aggregate=2600), 3aa (pg_cast=2605), 3ag (pg_conversion=2607), 3ak
(pg_default_acl=826), 3an (pg_enum=3501), 3ar (pg_event_trigger=3466),
3aw (pg_extension=3079), 3bb (pg_foreign_data_wrapper=2328), 3be
(pg_foreign_server=1417), 3bh (pg_foreign_table=3118), and 3bm
(pg_opfamily=2753). No encoder, builder, or `Init` flow change.

### Files touched

- `internal/initdb/relcache_init.go`
  - New `pgPartitionedTableAttrs()` returning the 8-column PG18 schema
    verbatim from `pg_partitioned_table.h`:
    - partrelid     (oid 26 / 4)            NOT NULL — BKI_LOOKUP(pg_class)
    - partstrat     (char 18 / 1)           NOT NULL
    - partnatts     (int2 21 / 2)           NOT NULL
    - partdefid     (oid 26 / 4)            NOT NULL — BKI_LOOKUP_OPT(pg_class)
    - partattrs     (int2vector 22 / -1)    NOT NULL — BKI_FORCE_NOT_NULL
    - partclass     (oidvector 30 / -1)     NOT NULL — BKI_LOOKUP(pg_opclass) BKI_FORCE_NOT_NULL
    - partcollation (oidvector 30 / -1)     NOT NULL — BKI_LOOKUP_OPT(pg_collation) BKI_FORCE_NOT_NULL
    - partexprs     (pg_node_tree 194 / -1) NULLABLE — CATALOG_VARLEN
  - `nailedLocalRels` gains
    `{3350, "pg_partitioned_table", 83, 'r', 8, false, pgPartitionedTableAttrs()}`.
    `RelType=83` is safe because pg_partitioned_table is not formrdesc'd
    (no `PartitionedRelation_Rowtype_Id` constant in PG18), so Step
    3v's `relation->rd_att->tdtypeid == relp->reltype` Phase-3
    assertion does not fire.

- `internal/initdb/initdb.go`
  - `bootstrapMappedLocalCatalogHeaps` OID list gains `3350` so an
    `InitPage`-stamped 8 KiB empty heap is written to
    `base/{1,5}/3350` before PG's `mdopen`.
  - `localRelMap` gains `{3350, 3350}` so PG's relfilenode mapper
    resolves OID 3350 to a backing file.

### Companion indexes (deferred)

`pg_partitioned_table.h:69` declares one index:

- 3351 = `pg_partitioned_table_partrelid_index`
  (UNIQUE PRIMARY KEY, `btree(partrelid oid_ops)`, backs
  `MAKE_SYSCACHE(PARTRELID, …, 32)`).

Deferred to the next step in the established single-OID rhythm
(Steps 3w → 3aa → 3ag → 3ak → 3an → 3ar → 3aw → 3bb → 3be → 3bh → 3bm).

## Threading

The new `nailedLocalRels` entry flows automatically through:

- `bootstrapPgClassTuples` (writes Form_pg_class row),
- `bootstrapPgAttributeTuples` (writes 8 Form_pg_attribute rows),
- `bootstrapPgClassOidIndex` (leaf for 3350 at file 2662),
- `bootstrapPgAttributeRelidAttnumIndex` (8 composite-key leaves at
  file 2659; Step 3av's bulk-load builder handles the bookkeeping),
- `writeRelcacheInitFile` (emits Form_pg_class + 8 Form_pg_attribute
  blob group).

## Regression pins

`internal/initdb/pg_partitioned_table_nailed_test.go`:

- `TestNailedLocalRelsContainsPgPartitionedTable` — full per-column
  `(Name, TypeOID, Num, Len, NotNull)` audit against
  `pg_partitioned_table_d.h` and `pg_partitioned_table.h`.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgPartitionedTable` —
  asserts `base/{1,5}/3350` exists, is exactly 8 KiB, and
  InitPage-stamped.

Existing pin extended:

- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  gains 3350 so the placeholder OID list cannot silently drop
  pg_partitioned_table.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgPartitionedTable|TestBootstrapMappedLocalCatalogHeapsIncludesPgPartitionedTable|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedLocalRelsContainsPgOpfamily|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3br
  (`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`,
  `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Next blocker (Step 3bt)

With pg_partitioned_table (OID 3350) now opened cleanly, the next E2E
re-run is expected to surface OID 3351
(`pg_partitioned_table_partrelid_index`), load-bearing via
`MAKE_SYSCACHE(PARTRELID, …)`. Same single-OID catalog-seed-addition
pattern applies.
