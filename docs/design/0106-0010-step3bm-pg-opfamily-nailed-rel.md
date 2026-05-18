# M0106-0010 Step 3bm: pg_opfamily nailed local rel

## Context

After Step 3bl seeded `pg_operator_oprname_l_r_n_index` (OID 2689), the
next `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
re-run surfaced:

```
psql: error: connection to server at "127.0.0.1", port 45035 failed:
FATAL:  could not open relation with OID 2753
```

OID 2753 is `pg_opfamily` per
`postgres/src/include/catalog/pg_opfamily_d.h:23`
(`#define OperatorFamilyRelationId 2753`).

PG's `RelationBuildDesc(2753) → ScanPgRelation(2753)` returned NULL
because no pg_class row was seeded for OID 2753; the backend FATALs
from `postgres/src/backend/access/common/relation.c:61`.

## Change

Pure catalog-seed addition mirroring the nailed-rel pattern of Steps 3w
(pg_aggregate=2600), 3aa (pg_cast=2605), 3ag (pg_conversion=2607), 3ak
(pg_default_acl=826), 3an (pg_enum=3501), 3ar (pg_event_trigger=3466),
3aw (pg_extension=3079), 3bb (pg_foreign_data_wrapper=2328), 3be
(pg_foreign_server=1417), and 3bh (pg_foreign_table=3118). No encoder,
builder, or `Init` flow change.

### Files touched

- `internal/initdb/relcache_init.go`
  - New `pgOpfamilyAttrs()` returning the 5-column PG18 schema verbatim
    from `pg_opfamily.h`: oid (26/4), opfmethod (26/4 → pg_am), opfname
    (19 name/64), opfnamespace (26/4 → pg_namespace), opfowner (26/4 →
    pg_authid). All NOT NULL — pg_opfamily has no CATALOG_VARLEN
    columns.
  - `nailedLocalRels` gains
    `{2753, "pg_opfamily", 83, 'r', 5, false, pgOpfamilyAttrs()}`.
    `RelType=83` is safe because pg_opfamily is not formrdesc'd (no
    `OperatorFamilyRelation_Rowtype_Id` constant in PG18), so Step 3v's
    `relation->rd_att->tdtypeid == relp->reltype` Phase-3 assertion
    does not fire.

- `internal/initdb/initdb.go`
  - `bootstrapMappedLocalCatalogHeaps` OID list gains `2753` so an
    `InitPage`-stamped 8 KiB empty heap is written to
    `base/{1,5}/2753` before PG's `mdopen`.
  - `localRelMap` gains `{2753, 2753}` so PG's relfilenode mapper
    resolves OID 2753 to a backing file.

### Companion indexes (deferred)

`pg_opfamily.h` declares two indexes:

- 2754 = `pg_opfamily_am_name_nsp_index` (UNIQUE,
  `btree(opfmethod oid_ops, opfname name_ops, opfnamespace oid_ops)`,
  backs `MAKE_SYSCACHE(OPFAMILYAMNAMENSP, …)`).
- 2755 = `pg_opfamily_oid_index` (UNIQUE PRIMARY KEY,
  `btree(oid oid_ops)`, backs `MAKE_SYSCACHE(OPFAMILYOID, …)`).

Both deferred to subsequent steps in the single-OID rhythm of Steps 3w
→ 3aa → 3ag → 3ak → 3an → 3ar → 3aw → 3bb → 3be → 3bh.

## Threading

The new `nailedLocalRels` entry flows automatically through:

- `bootstrapPgClassTuples` (writes Form_pg_class row),
- `bootstrapPgAttributeTuples` (writes 5 Form_pg_attribute rows),
- `bootstrapPgClassOidIndex` (leaf for 2753 at file 2662),
- `bootstrapPgAttributeRelidAttnumIndex` (5 composite-key leaves at
  file 2659; Step 3av's bulk-load builder handles the bookkeeping),
- `writeRelcacheInitFile` (emits Form_pg_class + 5 Form_pg_attribute
  blob group).

## Regression pins

`internal/initdb/pg_opfamily_nailed_test.go`:

- `TestNailedLocalRelsContainsPgOpfamily` — full per-column
  `(Name, TypeOID, Num, Len, NotNull)` audit against
  `pg_opfamily_d.h` and `pg_opfamily.h`.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgOpfamily` — asserts
  `base/{1,5}/2753` exists, is exactly 8 KiB, and InitPage-stamped.

Existing pin extended:

- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  gains 2753 so the placeholder OID list cannot silently drop
  pg_opfamily.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgOpfamily|TestBootstrapMappedLocalCatalogHeapsIncludesPgOpfamily|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedLocalRelsContainsPgForeignTable|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3bl
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

## Next blocker (Step 3bn)

With pg_opfamily (OID 2753) now opened cleanly, the next E2E re-run is
expected to surface either OID 2754
(`pg_opfamily_am_name_nsp_index`) or 2755 (`pg_opfamily_oid_index`) —
both load-bearing via `MAKE_SYSCACHE(OPFAMILYAMNAMENSP)` and
`MAKE_SYSCACHE(OPFAMILYOID)`. Same single-OID
catalog-seed-addition pattern applies.
