# M0106-0010 Step 3w — pg_aggregate as a nailed local catalog

Status: accepted (2026-05-18)

## Problem

After Step 3v cleared the relcache init-file `Assert` PANIC loop, every PG
standby backend the postmaster forks FATALs immediately after `PM_HOT_STANDBY`:

```
FATAL: could not open relation with OID 2600
```

OID 2600 is `pg_aggregate`. The error message is emitted from
`postgres/src/backend/access/common/relation.c:61`:

```c
r = RelationIdGetRelation(relationId);
if (!RelationIsValid(r))
    elog(ERROR, "could not open relation with OID %u", relationId);
```

i.e. `RelationBuildDesc(2600) → ScanPgRelation(2600)` returned no row.
goopg's `localRelMap` advertises `2600 → 2600` so the relfilenode mapping is
in place, but two things were missing:

1. No `pg_class` row for OID 2600. PG cannot construct a `RelationData` for
   an OID that has no `pg_class` tuple.
2. No heap file at `base/{1,5}/2600`. Even if the `pg_class` row existed,
   the first `RelationOpenSmgr → mdopen` from a real query would FATAL with
   `could not open file "base/5/2600"`.

`pg_aggregate` is probed during `InitPostgres`' role/database resolution
path before any user query, so the standby never reaches a usable state.

## Fix

Two-pronged, both targeted at goopg's `initdb`:

### 1. Add `pg_aggregate` to `nailedLocalRels`

`internal/initdb/relcache_init.go::nailedLocalRels` gains:

```go
{2600, "pg_aggregate", 83, 'r', 22, false, pgAggregateAttrs()},
```

`pgAggregateAttrs()` returns the 22-column PG18 schema sourced verbatim from
`postgres/src/include/catalog/pg_aggregate_d.h` (`Anum_pg_aggregate_*` 1–22)
and `pg_aggregate.h` (column type declarations). Per-column `(TypeOID, Len,
NotNull)` matches PG18 (regproc=24/4, oid=26/4, int2=21/2, int4=23/4,
char=18/1, bool=16/1, text=25/-1; `agginitval`/`aggminitval` nullable).

`RelType=83` (pg_class's rowtype) is safe because `pg_aggregate` is NOT
formrdesc'd — there is no `AggregateRelation_Rowtype_Id` constant in PG18
headers, so the Step 3v Phase3 assertion
`relation->rd_att->tdtypeid == relp->reltype` (`relcache.c:4293`) does not
fire for this OID. This mirrors the existing `83` convention used by every
non-formrdesc'd local nailed catalog (pg_attrdef, pg_namespace, pg_inherits,
pg_language, pg_amop, pg_description, pg_depend, …).

Adding pg_aggregate to `nailedLocalRels` automatically threads it through
the bootstrap flow with no per-catalog wiring:

- `bootstrapPgClassTuples` walks `nailedSharedRels + nailedLocalRels` and
  writes a 34-column PG18 `Form_pg_class` heap row for OID 2600 to
  `base/{1,5}/1259`.
- `bootstrapPgAttributeTuples` walks the same list, calling
  `pgAttrEntriesForRel` per relation; the 22 columns produce 22
  `pg_attribute` heap rows in `base/{1,5}/1249`.
- `bootstrapPgClassOidIndex` reads the per-OID heap-TID map and seeds a
  leaf entry in `base/{1,5}/2662 + global/2662` for OID 2600 so PG's
  `SearchSysCache1(RELOID, 2600)` resolves via index.
- `bootstrapPgAttributeRelidAttnumIndex` adds 22 composite-key
  `(attrelid=2600, attnum=N)` leaves to `base/{1,5}/2659 + global/2659`.
- `writeRelcacheInitFile` includes pg_aggregate's `Form_pg_class` blob in
  `pg_internal.init` (currently rejected by PG anyway because of the
  `rd_id` offset bug from Step 3v's carry-over, but harmless).

### 2. Seed empty heap files for mapped-but-undeseeded local catalogs

`internal/initdb/initdb.go` gains `bootstrapMappedLocalCatalogHeaps`,
wired into `Init` after `bootstrapPgAttributeRelidAttnumIndex`. It writes
an `InitPage`'d 8-KiB heap page to `base/{1,5}/<oid>` for every mapped
local catalog OID that lacks a dedicated bootstrapper:

```
2600 pg_aggregate          2620 pg_trigger
2604 pg_attrdef            3381 pg_statistic_ext
2605 pg_cast               3596 pg_seclabel
2606 pg_constraint         3764 pg_ts_config
2607 pg_conversion         3765 pg_ts_config_map
2608 pg_depend             3766 pg_ts_dict
2609 pg_description        3767 pg_ts_parser
2611 pg_inherits           3768 pg_ts_template
2612 pg_language           4044 pg_event_trigger
2613 pg_largeobject        6003 pg_publication
2614 pg_largeobject_meta…  6101 pg_publication_rel
2615 pg_namespace          6102 pg_sequence
2617 pg_operator           6137 pg_transform
2618 pg_rewrite            6245 pg_statistic_ext_data
2619 pg_statistic          9400 pg_db_role_setting
```

`pg_type` (1247) is deliberately excluded — `bootstrapSystemCatalogs`
already seeds it in goopg's internal row format and overwriting would
wipe the rows `TestBootstrappedPGTypeRowsReadable` depends on.
`pg_authid` (6239) is omitted because it is fundamentally shared and
already has a populated `global/1260` heap via `bootstrapPostgresRole`.

For Step 3w specifically only `2600` is load-bearing (the new
`nailedLocalRels` entry triggers an `RelationOpenSmgr → mdopen` on PG's
first query of `pg_aggregate`). The other OIDs are infrastructure for
follow-up steps so each future step doesn't have to add its own file.
Cost is one 8-KiB write per OID per database (~480 KiB total at init).

## Verification

`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` advances
past the `could not open relation with OID 2600` FATAL to the next
blocker, `FATAL: could not open relation with OID 2650` —
`pg_aggregate_fnoid_index`, Step 3x territory
(`postgres/src/include/catalog/pg_aggregate.h:113`:
`DECLARE_UNIQUE_INDEX_PKEY(pg_aggregate_fnoid_index, 2650, …)`).

Targeted tests:
- `TestNailedLocalRelsContainsPgAggregate` —
  `internal/initdb/pg_aggregate_nailed_test.go` (new). Asserts the
  `nailedLocalRels` entry's OID/Name/RelKind/RelNatts and spot-checks
  the first column matches `Anum_pg_aggregate_aggfnoid` = 1 + `regproc`
  type.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages` —
  `internal/initdb/pg_mapped_local_catalog_heap_test.go` (new). Pins
  the canonical OID list and asserts each file is 8 KiB,
  InitPage-stamped, present under both `base/1` and `base/5`.

Regression sweep: `go test -count=1 ./internal/initdb/` — same 14
pre-existing baseline failures as Step 3v (stash-baseline diff confirms
zero new regressions); `go test -count=1 ./internal/executor/
./internal/server/ ./internal/storage/ ./internal/catalog/
./internal/mvcc/` — PASS.

## Carry-over

- Step 3x: add `pg_aggregate_fnoid_index` (OID 2650) to
  `pgIndexInitialEntries` and `nailedLocalRels`. Single-column
  oid-keyed UNIQUE PRIMARY index on `aggfnoid` (attnum 1).
- Step 3v's open `rd_id` offset bug in `buildRelationDataBlob` still
  applies; PG rejects goopg's `pg_internal.init` and falls back to
  formrdesc + heap-based RelationBuildDesc each boot. Functionally
  correct, wastes init-file I/O.
