# M0106-0010 Step 3ce — pg_statistic nailed-rel + relid_att_inh_index seed

## Status

Landed 2026-05-18 (step 3ce). Closes the FATAL
`could not open relation with OID 2619` PG-standby boot blocker that
surfaced after Step 3cd seeded the pg_statistic_ext family.

## Authoritative source

`postgres/src/include/catalog/pg_statistic.h`:

- `CATALOG(pg_statistic,2619,StatisticRelationId)` — heap OID 2619, 31
  columns, per-database (non-shared).
- `DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_relid_att_inh_index, 2696,
  StatisticRelidAttnumInhIndexId, pg_statistic,
  btree(starelid oid_ops, staattnum int2_ops, stainherit bool_ops));`
- `MAKE_SYSCACHE(STATRELATTINH, pg_statistic_relid_att_inh_index, 128);`

`postgres/src/include/catalog/pg_statistic_d.h` confirms
`Natts_pg_statistic == 31` and `Anum_pg_statistic_*` 1..31 in the order
documented below.

## Column layout (31 columns)

| # | Name | TypeOID | Len | NotNull | Notes |
|---|------|---------|-----|---------|-------|
| 1 | starelid | 26 (oid) | 4 | yes | BKI_LOOKUP(pg_class) — index attnum 1 |
| 2 | staattnum | 21 (int2) | 2 | yes | index attnum 2 |
| 3 | stainherit | 16 (bool) | 1 | yes | index attnum 3 |
| 4 | stanullfrac | 700 (float4) | 4 | yes | |
| 5 | stawidth | 23 (int4) | 4 | yes | |
| 6 | stadistinct | 700 (float4) | 4 | yes | |
| 7–11 | stakind1..5 | 21 (int2) | 2 | yes | zero when slot unused |
| 12–16 | staop1..5 | 26 (oid) | 4 | yes | BKI_LOOKUP_OPT(pg_operator) — zero when unused |
| 17–21 | stacoll1..5 | 26 (oid) | 4 | yes | BKI_LOOKUP_OPT(pg_collation) — zero when unused |
| 22–26 | stanumbers1..5 | 1021 (_float4) | -1 | no | CATALOG_VARLEN — NULLABLE |
| 27–31 | stavalues1..5 | 2277 (anyarray) | -1 | no | CATALOG_VARLEN — NULLABLE |

pg_statistic has **no** `oid` system column — attnum 1 is `starelid`.

## Code changes

### `internal/initdb/relcache_init.go`

1. New `pgStatisticAttrs()` returns the 31-column slice above.
2. `nailedLocalRels` gains one heap row immediately after the Step 3cd
   pg_statistic_ext entry:
   ```go
   {2619, "pg_statistic", 83, 'r', 31, false, pgStatisticAttrs()},
   ```
   `RelType=83` is safe because pg_statistic is not formrdesc'd (no
   `StatisticRelation_Rowtype_Id` constant in PG18 headers), so Step
   3v's `relation->rd_att->tdtypeid == relp->reltype` Phase3 assertion
   does not fire.
3. `nailedLocalRels` idxSpec list gains one entry after the Step 3cd
   3379 entry:
   ```go
   {2696, "pg_statistic_relid_att_inh_index"},
   ```
   `flattenRels` consults `pgIndexNattsByOID()` and assigns
   `RelKind='i', RelNatts=3` automatically, so
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check
   (`relcache.c:1492`) passes.

### `internal/initdb/initdb.go`

1. `pgIndexInitialEntries` local section gains one new row after the
   Step 3cd 3379 entry:
   ```go
   entry(2696, 2619, []int16{1, 2, 3},
         []uint32{oidOps, int2Ops, boolOps},
         []uint32{0, 0, 0}, true, true) // pg_statistic_relid_att_inh_index
   ```
   - `IndKey = {1,2,3}` matches PG18 heap attnums (starelid, staattnum,
     stainherit).
   - `IndClass = {oidOps, int2Ops, boolOps}` matches the `oid_ops`,
     `int2_ops`, `bool_ops` opclasses in `pg_statistic.h:139`.
   - `IndCollation = {0,0,0}` (no collations apply to these opclasses).
   - `IsUnique=true, IsPrimary=true` — `DECLARE_UNIQUE_INDEX_PKEY` is
     the PKEY variant.
2. Both "Critical index placeholder pages" OID lists
   (`base/<dboid>/` block and `global/` fallback block) at
   `bootstrapPostgresDatabase` gain `2696` after the Step 3cd 3379
   entry. The placeholder is a valid empty PG18 btree metapage
   (Step 3k's `makeBtreeRootPage` writes `btm_root = P_NONE`), correct
   because pg_statistic is empty at bootstrap.
3. No new entries needed in `bootstrapMappedLocalCatalogHeaps` oid list
   or in `localRelMap` — both already contained `2619` from the Step
   3w baseline (the existing 2619 heap-page placeholder is sufficient
   because pg_statistic is unpopulated at bootstrap).

No new type-helper entries needed: `int2` (21), `bool` (16), `float4`
(700), `int4` (23), `_float4` (1021), `anyarray` (2277), `oid` (26) are
all already registered in `pgCatalogTypeOID` / `pgCatalogTypeLen` /
`pgTypeByVal` / `pgTypeAlignChar` / `pgTypeStorageChar`.

## Regression pins (new file `internal/initdb/pg_statistic_nailed_test.go`)

- `TestNailedLocalRelsContainsPgStatistic` — asserts the heap entry
  exists with `RelName="pg_statistic", RelType=83, RelKind='r',
  RelNatts=31, IsShared=false`, and every column descriptor matches
  PG18 verbatim.
- `TestNailedLocalRelsContainsPgStatisticRelidAttInhIndex` — asserts
  OID 2696 appears in the flattened nailed-rel list with
  `RelKind='i', RelNatts=3`.
- `TestPgStatisticIndexInitialEntries` — pins
  `(IndRelid=2619, IndKey=[1,2,3], IndClass=[oid_ops, int2_ops,
  bool_ops], IndCollation=[0,0,0], IsUnique=true, IsPrimary=true)`.
- `TestPgStatisticAttrsTypeOIDsMatchPG18` — pins every column's
  `(Name, TypeOID, Len, NotNull)`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `2696: {1,2,3}` to
  the authoritative map (strict count guard auto-rejects future
  additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with `2696` so the populated 2-page btree at file 2679 must
  carry this OID's leaf.

## Verification

```
go test -count=1 -run \
  'TestNailedLocalRelsContainsPgStatistic|TestPgStatisticIndexInitialEntries|TestPgStatisticAttrsTypeOIDsMatchPG18|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages' \
  ./internal/initdb/
```
→ PASS.

```
go test -count=1 ./internal/initdb/
```
→ same 14 pre-existing baseline failures as Step 3cd
(`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`,
`TestSynchronousCommitFlushesByDefault`,
`TestOpenOldClusterWithoutM0030*`,
`TestSystemCatalogRelfilesAreValidHeapPages`,
`TestCommittedTableSurvivesCrashRestart`,
`TestRuntimeCloseTriggersFinalCheckpoint`,
`TestMultipleTablesLoadFromHeap`) — no new regressions.

```
go test -count=1 ./internal/executor/ ./internal/server/ \
                 ./internal/storage/ ./internal/catalog/ ./internal/mvcc/
```
→ PASS.

## Forward look

The next E2E re-run will surface the next FATAL — likely another
shared/local catalog OID open. The seed chain Step 3w → 3aa → 3ad …
continues by adding one catalog/index per step.
