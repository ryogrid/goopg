# pg_foreign_table nailed-rel seed (M0106-0010 step 3bh)

Status: accepted

## Blocker closed

`FATAL: could not open relation with OID 3118` on every PG-standby backend
`InitPostgres`, surfaced after Step 3bg seeded
`pg_foreign_server_oid_index` (OID 113). OID 3118 is `pg_foreign_table`
per `postgres/src/include/catalog/pg_foreign_table_d.h:23`
(`#define ForeignTableRelationId 3118`).

## Root cause

`pg_foreign_table` is referenced by PG18's `RelationCacheInitFile` /
`load_relcache_init_file` flow once the surrounding FDW catalogs
(`pg_foreign_data_wrapper`, `pg_foreign_server`) and their indexes are
addressable. Without a `Form_pg_class` row, `RelationBuildDesc(3118) →
ScanPgRelation(3118)` returns NULL and the backend FATALs.

## Fix

Pure catalog-seed change mirroring the nailed-rel pattern of Steps 3w
(`pg_aggregate`), 3aa (`pg_cast`), 3ag (`pg_conversion`), 3ak
(`pg_default_acl`), 3an (`pg_enum`), 3ar (`pg_event_trigger`), 3aw
(`pg_extension`), 3bb (`pg_foreign_data_wrapper`), and 3be
(`pg_foreign_server`). No encoder/builder/`Init` flow change.

1. `internal/initdb/relcache_init.go::nailedLocalRels` gains
   `{3118, "pg_foreign_table", 83, 'r', 3, false, pgForeignTableAttrs()}`.
   `RelType=83` is safe because `pg_foreign_table` is not formrdesc'd
   (no `ForeignTableRelation_Rowtype_Id` constant in PG18 headers).
2. New `pgForeignTableAttrs()` defines the 3-column PG18 schema sourced
   verbatim from `postgres/src/include/catalog/pg_foreign_table.h` and
   `pg_foreign_table_d.h` (`Anum_pg_foreign_table_* 1–3`,
   `Natts_pg_foreign_table == 3`):
   - `ftrelid` (oid, TypeOID 26) NOT NULL — primary key, references
     `pg_class.oid`.
   - `ftserver` (oid, TypeOID 26) NOT NULL — references
     `pg_foreign_server.oid`.
   - `ftoptions` (text[], TypeOID 1009) nullable — CATALOG_VARLEN block,
     no BKI_FORCE_NOT_NULL.

   Unlike most system catalogs, `pg_foreign_table` has no `oid` system
   column — `ftrelid` is the primary key (this is also why the single
   unique index `pg_foreign_table_relid_index` (OID 3119) keys on
   `ftrelid oid_ops` per `pg_foreign_table.h:47`).
3. `internal/initdb/initdb.go::bootstrapMappedLocalCatalogHeaps` OID list
   and `bootstrapPostgresDatabase` `localRelMap` both gain `3118` so the
   on-disk empty heap file exists at `base/{1,5}/3118` before PG's
   `mdopen` runs against the resolved relation.

## Why companion index (OID 3119) is deferred

`MAKE_SYSCACHE(FOREIGNTABLEREL, pg_foreign_table_relid_index, 4)`
(`pg_foreign_table.h:49`) makes 3119 load-bearing for the
`FOREIGNTABLEREL` syscache — but PG's standby boot only opens an index
relation when its parent heap actually needs to be searched. Since
goopg's `pg_foreign_table` is currently empty (no DDL has created a
foreign table), the syscache scan returns zero rows whether the index
file resolves or not. We follow the established single-OID rhythm: this
step closes the heap blocker; the index seed will land in step 3bi if a
concrete E2E re-run surfaces `could not open relation with OID 3119`.

## Regression pins

- `TestNailedLocalRelsContainsPgForeignTable`
  (`internal/initdb/pg_foreign_table_nailed_test.go`): per-field
  `(OID, RelName, RelKind, RelNatts, len(Attrs))` audit; per-column
  pin of `(Name, TypeOID, Num, Len, NotNull)` against PG18.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignTable`
  (`internal/initdb/pg_foreign_table_nailed_test.go`): asserts
  `base/{1,5}/3118` exists, is `storage.BlockSize` bytes, and InitPage
  was applied (page is not all-zero).
- Existing `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages`
  `wantOIDs` list extended with `3118`.

## Verification

- `go build ./...` PASS.
- `go test -count=1 ./internal/initdb/` shows the same 14 pre-existing
  baseline failures as Step 3bg (no new regressions).
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
  re-run expected to advance past the OID 3118 FATAL to the next
  catalog/index blocker.
