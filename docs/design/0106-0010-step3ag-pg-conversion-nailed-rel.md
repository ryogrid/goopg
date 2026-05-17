# M0106-0010 Step 3ag — Seed `pg_conversion` (OID 2607) into the catalog bootstrap

## Context

After Step 3af landed `pg_collation_oid_index` (OID 3085) and cleared the
`FATAL: could not open relation with OID 3085` PG-standby boot blocker,
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` advanced
to the next FATAL: `could not open relation with OID 2607`. Per
`postgres/src/include/catalog/pg_conversion_d.h:23`
(`#define ConversionRelationId 2607`) and `pg_conversion.h:29`
(`CATALOG(pg_conversion,2607,ConversionRelationId)`), OID 2607 is the
`pg_conversion` heap relation, not an index. The fix_plan's earlier
hypothesis pointing at `pg_class_tblspc_relfilenode_index` was incorrect:
that index's authoritative OID is 3455, not 2607
(`pg_class.h:160`: `DECLARE_INDEX(pg_class_tblspc_relfilenode_index, 3455, …)`).

## Root cause

PG-standby `RelationBuildDesc(2607) → ScanPgRelation(2607)` walks
`pg_class_oid_index` (OID 2662, populated since Step 3m), gets zero
rows because goopg's `bootstrapPgClassTuples` has no `Form_pg_class` row
for `pg_conversion`, returns NULL, and the caller FATALs with
`could not open relation with OID 2607`. Same shape as Steps 3w
(pg_aggregate, OID 2600) and 3aa (pg_cast, OID 2605): the empty 8 KiB
`InitPage`-stamped heap file at `base/{1,5}/2607` is already produced by
`bootstrapMappedLocalCatalogHeaps` (Step 3w infrastructure), so the
missing piece is purely the `nailedLocalRels` entry that drives
`bootstrapPgClassTuples` to write the pg_class row and
`bootstrapPgAttributeTuples` to write the 8 pg_attribute rows.

## Fix

Single-OID catalog-seed addition; no encoder, builder, or `Init` flow
change.

1. `internal/initdb/relcache_init.go`
   - New `pgConversionAttrs() []nailedAttr` returning the 8-column
     PG18 schema sourced verbatim from
     `postgres/src/include/catalog/pg_conversion.h:29-52` and
     `pg_conversion_d.h:28-37`:
     | Attnum | Name           | TypeOID | Len | NotNull |
     |--------|----------------|---------|-----|---------|
     | 1      | oid            | 26      | 4   | true    |
     | 2      | conname        | 19      | 64  | true    |
     | 3      | connamespace   | 26      | 4   | true    |
     | 4      | conowner       | 26      | 4   | true    |
     | 5      | conforencoding | 23      | 4   | true    |
     | 6      | contoencoding  | 23      | 4   | true    |
     | 7      | conproc        | 24      | 4   | true    |
     | 8      | condefault     | 16      | 1   | true    |
   - `nailedLocalRels` gains
     `{2607, "pg_conversion", 83, 'r', 8, false, pgConversionAttrs()}`
     immediately after the Step-3aa pg_cast entry. `RelType=83` is safe
     because pg_conversion is not formrdesc'd (no
     `ConversionRelation_Rowtype_Id` constant in PG18 headers); Step
     3v's `relation->rd_att->tdtypeid == relp->reltype` Phase3
     assertion does not fire.

2. The empty 8 KiB heap at `base/{1,5}/2607` is already produced by
   `bootstrapMappedLocalCatalogHeaps` (Step 3w) — OID 2607 was added
   to its OID list at `internal/initdb/initdb.go:430` and the
   `localRelMap` at line 731 already advertised the mapping.

## Threading through the existing bootstrap flow

The single `nailedLocalRels` entry threads automatically:

- `bootstrapPgClassTuples` writes the `Form_pg_class` row for OID 2607.
- `bootstrapPgAttributeTuples` writes 8 `Form_pg_attribute` rows.
- `bootstrapPgClassOidIndex` adds a leaf for 2607 to the populated
  btree at `base/{1,5}/2662 + global/2662`.
- `bootstrapPgAttributeRelidAttnumIndex` adds 8 composite-key leaves
  to file 2659.
- `writeRelcacheInitFile` emits a `Form_pg_class` + 8
  `Form_pg_attribute` blob group.

The three companion indexes (`pg_conversion_default_index`=2668,
`pg_conversion_name_nsp_index`=2669, `pg_conversion_oid_index`=2670 per
`pg_conversion.h:60-62`) are intentionally deferred to subsequent steps
to keep the per-loop single-OID rhythm of Steps 3w → 3aa → 3ag.
pg_conversion is currently unpopulated (no conversion rows are
bootstrapped), so the Step-3k empty btree placeholders for those three
indexes are sufficient for early-boot lookups that expect zero rows.

## Tests

New regression pin: `TestNailedLocalRelsContainsPgConversion` in
`internal/initdb/pg_conversion_nailed_test.go` asserts
`(RelName, RelKind, RelNatts, len(Attrs)) = ("pg_conversion", 'r', 8, 8)`
and pins every `(Name, TypeOID, Num, Len, NotNull)` against
`pg_conversion_d.h` authoritative definitions. Rejects silent
re-emergence of the FATAL.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgConversion|TestNailedLocalRelsContainsPgCast|TestNailedLocalRelsContainsPgAggregate|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestPgClassOidIndexHasSingleKeyColumn|TestNailedIndexRelnattsAgreesWithIndnatts'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3af (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next blocker

After Step 3ag the next E2E re-run is expected to surface either:
1. One of the pg_conversion companion indexes (2668 / 2669 / 2670) if
   `find_default_conversion` or `FindConversion` runs during early
   backend startup, or
2. A different nailed-rel FATAL at a later OID (e.g.
   `pg_class_tblspc_relfilenode_index` = 3455, or pg_database /
   pg_foreign_* / pg_publication catalogs).

Whichever surfaces is Step 3ah territory and follows the same
catalog-seed-addition pattern.
