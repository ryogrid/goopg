# M0106-0010 Step 3cd: pg_statistic_ext (OID 3381) nailed local rel + 3 indexes

**Status**: LANDED 2026-05-18

## Problem

After Step 3cc seeded the `pg_statistic_ext_data` (OID 3429) per-database
catalog plus its single composite UNIQUE PRIMARY index 3433, the next
PG-standby boot FATAL becomes:

```
FATAL:  could not open relation with OID 3381
```

OID 3381 is `pg_statistic_ext` per
`postgres/src/include/catalog/pg_statistic_ext.h:33`
(`CATALOG(pg_statistic_ext,3381,StatisticExtRelationId)`). It is opened
very early during the PG18 phase-3 relcache initialisation pass and
again whenever the planner consults extended-statistics objects, so no
user query can run until the catalog has a real `pg_class` row.

The 8 KiB heap placeholder for OID 3381 has been written by
`bootstrapMappedLocalCatalogHeaps` since Step 3w, and the local
`pg_filenode.map` already contains `{3381, 3381}` — but no
`nailedLocalRels` entry existed, so PG's `RelationBuildDesc(3381) →
ScanPgRelation(3381)` returned NULL.

## Decision

Family-complete seed in one step: heap 3381 + **all three** declared
indexes from `pg_statistic_ext.h:73..75`.

`pg_statistic_ext` is a per-database (non-shared) catalog, so it
follows the Step 3cb (`pg_sequence`) / Step 3cc (`pg_statistic_ext_data`)
template — heap goes to `base/{1,5}/3381`; the index placeholder files
go to both `base/<dboid>/<oid>` and `global/<oid>` (the global copy is
a fallback for PG's `formrdesc` path that may use `InvalidOid` for
`dbNode` on nailed relations).

### Schema (Anum_pg_statistic_ext_* 1..9, Natts == 9)

PG18 declaration:

```
CATALOG(pg_statistic_ext,3381,StatisticExtRelationId)
{
    Oid     oid;
    Oid     stxrelid    BKI_LOOKUP(pg_class);
    NameData stxname;
    Oid     stxnamespace BKI_LOOKUP(pg_namespace);
    Oid     stxowner    BKI_LOOKUP(pg_authid);
    int2vector stxkeys  BKI_FORCE_NOT_NULL;
#ifdef CATALOG_VARLEN
    int16   stxstattarget BKI_DEFAULT(_null_) BKI_FORCE_NULL;
    char    stxkind[1]    BKI_FORCE_NOT_NULL;
    pg_node_tree stxexprs;
#endif
}
```

Verbatim against `pg_statistic_ext_d.h:28..38` and PostgreSQL 18.3
runtime `pg_attribute`:

| attnum | name          | type         | TypeOID | Len | NotNull | rationale |
|--------|---------------|--------------|---------|-----|---------|-----------|
| 1      | oid           | oid          | 26      | 4   | true    | system column (CATALOG block declares `Oid oid`) |
| 2      | stxrelid      | oid          | 26      | 4   | true    | BKI_LOOKUP(pg_class) |
| 3      | stxname       | name         | 19      | 64  | true    | NameData |
| 4      | stxnamespace  | oid          | 26      | 4   | true    | BKI_LOOKUP(pg_namespace) |
| 5      | stxowner      | oid          | 26      | 4   | true    | BKI_LOOKUP(pg_authid) |
| 6      | stxkeys       | int2vector   | 22      | -1  | true    | BKI_FORCE_NOT_NULL (varlena) |
| 7      | stxstattarget | int2         | 21      | 2   | false   | BKI_FORCE_NULL — declared inside CATALOG_VARLEN block but still fixed-width |
| 8      | stxkind       | _char        | 1002    | -1  | true    | BKI_FORCE_NOT_NULL (varlena) |
| 9      | stxexprs      | pg_node_tree | 194     | -1  | false   | default nullable (varlena) |

Note: pg_statistic_ext **does** have an `oid` system column, unlike
pg_statistic_ext_data (Step 3cc, which has none). This is the first
nailed local rel since Step 3cc that carries `oid` as attnum 1 — i.e.
no off-by-one between attnum and declared-column order.

All type OIDs / type-helper entries (alignment, byval, storage,
nullability) are already registered by prior steps:

* `int2vector` (22) — already mapped: align `'i'`, byval false,
  storage default `'p'` (PLAIN; PG's runtime row carries
  `typstorage = 'p'`).
* `name` (19) — handled by every other nailed catalog with a name
  column.
* `_char` (1002) — registered in Step 3a (empty-array `ArrayType`
  encoding) and Step 3aq (typstorage `'x'`).
* `pg_node_tree` (194) — registered in Step 3aq (typstorage `'x'`).
* `int2` (21), `oid` (26) — fixed-width base types.

No new `pgCatalogTypeOID` / `pgCatalogTypeLen` / `pgTypeByVal` /
`pgTypeAlignChar` / `pgTypeStorageChar` entries are required for
Step 3cd.

### Indexes (pg_statistic_ext.h:73..75)

```
DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_ext_oid_index, 3380,
    StatisticExtOidIndexId, pg_statistic_ext, btree(oid oid_ops));
DECLARE_UNIQUE_INDEX(pg_statistic_ext_name_index, 3997,
    StatisticExtNameIndexId, pg_statistic_ext,
    btree(stxname name_ops, stxnamespace oid_ops));
DECLARE_INDEX(pg_statistic_ext_relid_index, 3379,
    StatisticExtRelidIndexId, pg_statistic_ext, btree(stxrelid oid_ops));
```

`pg_statistic_ext` declares **two** syscaches:

```
MAKE_SYSCACHE(STATEXTOID,     pg_statistic_ext_oid_index,  4);
MAKE_SYSCACHE(STATEXTNAMENSP, pg_statistic_ext_name_index, 4);
```

`pg_statistic_ext_relid_index` (3379) has no syscache but is used by
`RemoveStatisticsExtById` and dependency cleanup paths to enumerate
extended-statistics objects defined on a given relation. PG's
`load_critical_index` opens **every** declared index of a nailed rel,
so all three must be seeded to avoid an immediate FATAL on the
nailed-rel boot path.

`pgIndexInitialEntries`:

| index | indrelid | indkey | indclass | indcollation | unique | primary |
|-------|----------|--------|----------|--------------|--------|---------|
| 3380  | 3381     | {1}    | {1981}   | {0}          | true   | true    |
| 3997  | 3381     | {3, 4} | {1986, 1981} | {950, 0} | true   | false   |
| 3379  | 3381     | {2}    | {1981}   | {0}          | false  | false   |

`1981 = oid_ops` (btree), `1986 = name_ops` (btree), `950 =
C_COLLATION_OID` (for the name column — same convention as e.g.
`pg_class_relname_nsp_index`, `pg_namespace_nspname_index`).

The empty-btree placeholder pages written by `makeBtreeRootPage` are
sufficient because pg_statistic_ext is unpopulated at bootstrap
(extended statistics are only created via `CREATE STATISTICS`).

## Changes

### `internal/initdb/relcache_init.go`

1. New `pgStatisticExtAttrs()` returning the 9-column schema above.
2. `nailedLocalRels` heap section gains
   `{3381, "pg_statistic_ext", 83, 'r', 9, false, pgStatisticExtAttrs()}`
   after the Step 3cc `pg_statistic_ext_data` entry.
3. `nailedLocalRels` idx section gains three new specs:
   `{3380, "pg_statistic_ext_oid_index"}`,
   `{3997, "pg_statistic_ext_name_index"}`,
   `{3379, "pg_statistic_ext_relid_index"}`,
   each with prose pinning to its upstream header.

### `internal/initdb/initdb.go`

1. `pgIndexInitialEntries` gains three new entries (3380, 3997, 3379)
   after the Step 3cc `3433` entry, with full PG18 indkey / indclass /
   indcollation / IsUnique / IsPrimary values pinned per the schema
   table above.
2. The "Critical index placeholder pages" lists at
   `bootstrapPostgresDatabase` (both the per-database `base/<dboid>/`
   block and the `global/` fallback block) gain `3380`, `3997`, `3379`
   after the Step 3cc `3433` entry.
3. No new entries needed in `bootstrapMappedLocalCatalogHeaps` oid
   list or in `localRelMap` — both already contained `3381` from
   the Step 3w / 3cc baseline.

### Regression pins

* `TestNailedLocalRelsContainsPgStatisticExt` — schema column-for-column.
* `TestNailedLocalRelsContainsPgStatisticExtIndexes` — RelKind='i',
  RelNatts={1,2,1} for OIDs {3380,3997,3379}.
* `TestPgStatisticExtIndexInitialEntries` — pgIndexInitialEntries
  row pinning for all 3 indexes.
* `TestPgStatisticExtAttrsTypeOIDsMatchPG18` — TypeOIDs/Len/NotNull
  against PostgreSQL 18.3 runtime pg_attribute.
* `TestPgIndexInitialEntriesIndkeyMatchesPG18` — map extended with
  `3380:{1}`, `3997:{3,4}`, `3379:{2}` (strict count guard).
* `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 3380, 3997, 3379.

## Verification

* `go build ./...` PASS.
* `go test -count=1 -run 'TestNailedLocalRelsContainsPgStatisticExt|TestPgStatisticExtIndexInitialEntries|TestPgStatisticExtAttrsTypeOIDsMatchPG18|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts' ./internal/initdb/` PASS.
* `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3cc (no new regressions; confirmed via baseline diff
  with the changes stashed).
* Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` PASS.
* E2E re-run: `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test
  -run TestE2E_FailoverGoopgToPG/async ./internal/testport/` — the
  `FATAL: could not open relation with OID 3381` PG-standby boot
  blocker is closed. Next anticipated FATAL: OID 2619
  (`pg_statistic`), to be handled by Step 3ce.

## Next

Step 3ce: seed pg_statistic (OID 2619) — the legacy per-column
single-key statistics table, much higher traffic than
pg_statistic_ext_data so its empty heap is still fine but the
relcache row must exist.
