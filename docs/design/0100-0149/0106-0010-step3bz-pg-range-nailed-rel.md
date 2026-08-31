# M0106-0010 Step 3bz — pg_range nailed local relation + indexes

Date: 2026-05-18

## Background

Step 3by closed the `FATAL: could not open relation with OID 6106`
(`pg_publication_rel`) blocker on PG-standby boot from a goopg-cloned
data directory. Re-running `TestE2E_FailoverGoopgToPG/async` with
`GOOPG_RUN_BLOCKED_M0102_E2E=1` advanced past 6106 and surfaced the
next FATAL:

```
FATAL:  could not open relation with OID 3541
```

OID 3541 is `pg_range` per `postgres/src/include/catalog/pg_range.h`:

```c
CATALOG(pg_range,3541,RangeRelationId)
```

## Changes

### Schema

`pg_range` has **no `oid` system column** (unlike `pg_publication_rel`
which has `Anum_pg_publication_rel_oid = 1`).  attnums start at 1 =
rngtypid per `pg_range_d.h`:

| attnum | name           | type    | TypeOID | NotNull |
|--------|----------------|---------|---------|---------|
| 1      | rngtypid       | oid     | 26      | yes     |
| 2      | rngsubtype     | oid     | 26      | yes     |
| 3      | rngmultitypid  | oid     | 26      | yes     |
| 4      | rngcollation   | oid     | 26      | yes     |
| 5      | rngsubopc      | oid     | 26      | yes     |
| 6      | rngcanonical   | regproc | 24      | yes     |
| 7      | rngsubdiff     | regproc | 24      | yes     |

`BKI_LOOKUP_OPT` columns (`rngcollation`, `rngcanonical`, `rngsubdiff`)
are still NOT NULL in the catalog descriptor — the value `0` is a
sentinel meaning "no canonical / no subdiff / no collation".

### Indexes

Two indexes declared on pg_range (pg_range.h:60–61):

| OID  | name                         | Decl                          | columns         | syscache       |
|------|------------------------------|-------------------------------|-----------------|----------------|
| 3542 | pg_range_rngtypid_index      | DECLARE_UNIQUE_INDEX_PKEY     | rngtypid (1)    | RANGETYPE      |
| 2228 | pg_range_rngmultitypid_index | DECLARE_UNIQUE_INDEX          | rngmultitypid (3) | RANGEMULTIRANGE |

Both use `oid_ops` (uint32 1981); collation 0; uniqueness true.
3542 is the PKEY; 2228 is `_INDEX` not `_PKEY`.

### File-by-file

1. `internal/initdb/relcache_init.go`
   - New `pgRangeAttrs()` returns the 7-column descriptor above.
   - `nailedLocalRels` heap list gains
     `{3541, "pg_range", 83, 'r', 7, false, pgRangeAttrs()}` after
     the Step 3by pg_publication_rel entry. `RelType=83` reused per
     the established placeholder convention for catalogs not
     formrdesc'd (no `RangeRelation_Rowtype_Id` constant exists).
   - `nailedLocalRels` idxSpec list gains
     `{3542, "pg_range_rngtypid_index"}` and
     `{2228, "pg_range_rngmultitypid_index"}` after the Step 3by 6116
     entry; `flattenRels` + `pgIndexNattsByOID` derive
     `RelKind='i', RelNatts=1` for both so the `relnatts == indnatts`
     check (relcache.c:1492) passes.

2. `internal/initdb/initdb.go`
   - `bootstrapMappedLocalCatalogHeaps` OID list gains
     `3541, // pg_range (M0106-0010 step 3bz)` after the Step 3by 6106
     entry so PG's mdopen finds a valid 8-KiB heap file at
     `base/{1,5}/3541`.
   - `localRelMap` gains `{3541, 3541}` analogously.
   - Two critical-index placeholder OID lists (one under
     `base/<dboid>/`, one under `global/`) each gain
     `3542, // pg_range_rngtypid_index (Step 3bz)` and
     `2228, // pg_range_rngmultitypid_index (Step 3bz)`.
   - `pgIndexInitialEntries` gains two rows:
     - `entry(3542, 3541, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
     - `entry(2228, 3541, []int16{3}, []uint32{oidOps}, []uint32{0}, true, false)`

3. Regression pins (`internal/initdb/pg_range_nailed_test.go`):
   - `TestNailedLocalRelsContainsPgRange` — heap + 7-col schema.
   - `TestBootstrapMappedLocalCatalogHeapsIncludesPgRange` — heap
     placeholder at `base/{1,5}/3541` is a non-zero 8-KiB page.
   - `TestPgRangeRngtypidIndexInitialEntry` — 3542 PKEY.
   - `TestPgRangeRngmultitypidIndexInitialEntry` — 2228 UNIQUE.

4. Existing tests extended:
   - `TestPgIndexInitialEntriesIndkeyMatchesPG18`: `3542:{1}` + `2228:{3}`.
   - `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`:
     adds 3542 + 2228.
   - `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`:
     adds 3541.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run '<targeted set>' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures unchanged (no new regressions; same set as Step 3by).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next anticipated blocker

With pg_range (3541) + pg_range_rngtypid_index (3542) +
pg_range_rngmultitypid_index (2228) all seeded, the pg_range family
is fully wired. The next E2E re-run is expected to surface another
catalog FATAL — likely in the pg_subscription / pg_subscription_rel
or pg_statistic / pg_statistic_ext territory (Step 3ca).
