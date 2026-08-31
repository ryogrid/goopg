# M0106-0010 Step 3u — pg_attribute trailing array columns must be NULL

Status: Implemented (2026-05-18)
Owner: Ralph autonomous agent

## Blocker that motivated this step

After Step 3t added `pg_namespace_nspname_index` (OID 2684) and
`pg_namespace_oid_index` (OID 2685) to `pgIndexInitialEntries()` and
`nailedLocalRels`, every PG-standby backend started after
`PM_HOT_STANDBY` PANICed immediately with:

```
PANIC: XX000: ERRORDATA_STACK_SIZE exceeded
LOCATION: get_error_stack_entry, elog.c:762
```

No preceding ERROR/FATAL was logged from the same backend, which
ruled out a simple "missing catalog row" miss and pointed at recursive
`ereport` calls inside one of the early-startup catalog-lookup paths.

## Root cause

Instrumenting `get_error_stack_entry` with `backtrace_symbols_fd`
revealed an unbounded recursion in `RelationCacheInitializePhase3`:

```
RelationCacheInitializePhase3
  → RelationIdGetRelation(<some nailed index>)
    → RelationInitIndexAccessInfo
      → RelationGetIndexAttOptions      (utils/cache/relcache.c:5988)
        → index_opclass_options          (access/index/indexam.c:1073)
          → ereport(ERROR, errmsg("operator class %s has no options",
                                  generate_opclass_name(opclass)))
            → generate_opclass_name
              → OpclassIsVisible
                → get_namespace_oid("pg_catalog")
                  → SearchSysCache1(NAMESPACENAME, "pg_catalog")
                    → systable_beginscan(pg_namespace_nspname_index = 2684)
                      → index_open(2684)
                        → RelationIdGetRelation(2684)
                          → RelationInitIndexAccessInfo
                            → RelationGetIndexAttOptions
                              → index_opclass_options
                                → ereport(ERROR, ...)
                                  → generate_opclass_name → ... (loop)
```

`ereport` increments `errordata_stack_depth` for each call; after five
nested unfinished ereports the safety check at `elog.c:758` fires and
PANICs the backend.

`index_opclass_options` only emits the `ereport(ERROR, ...)` when **all**
of the following hold:

1. `indrel->rd_indam->amoptsprocnum != 0` — true for every btree
   (`bthandler` sets `amoptsprocnum = BTOPTIONS_PROC = 5`).
2. `index_getprocid(...)` returns `InvalidOid` — true for goopg because
   `pgAmprocInitialEntries` only seeds amprocnum ∈ {1,2,4}; there is no
   options-support proc (amprocnum=5) row.
3. `DatumGetPointer(attoptions) != NULL` — **this** is the bug we
   introduced. `pgAttributeRow` in `internal/initdb/initdb.go` wrote
   `executor.NewStringDatum("")` for `attoptions`, which
   `encodeValuePG` serialised as a 1-byte empty-varlena header
   (`0x03` = `SET_VARSIZE_1B(p, 1)`). PG read that back as a valid
   non-NULL text datum, satisfied the third condition, and entered the
   ereport path. The recursion was previously dormant because
   `criticalRelcachesBuilt = false` short-circuits the
   `get_attoptions` call at `relcache.c:6006`; Step 3o unblocked the
   shared-catalog critical-index pass that flips that flag.

The same `NewStringDatum("")` mistake was present on the three
sibling trailing columns — `attacl`, `attfdwoptions`, `attmissingval` —
which are also array/varlena typed and default to SQL NULL in PG18.

## Fix

`internal/initdb/initdb.go::pgAttributeRow` now emits `executor.NullDatum`
for `attacl`, `attoptions`, `attfdwoptions`, `attmissingval` instead of
`executor.NewStringDatum("")`. With `attoptions = NULL`, condition (3)
of `index_opclass_options` is false, the function returns early at
line 1062 without ereport, and the recursive opclass-name lookup never
fires.

`writeMultiPageHeapRows` (wired in Step 3i) routes NULL-bearing rows
through `NewHeapTupleWithNulls`, which writes the PG18 null bitmap
into `out[SizeOfHeapTupleHeaderData:]` with `bit=1` for NOT-NULL,
sets `HEAP_HASNULL` in `t_infomask`, and advances `t_hoff` to
`MAXALIGN(SizeofHeapTupleHeader + len(bitmap))`. PG's
`GETSTRUCT(tuple) = (Form_pg_attribute) ((char *) tuple + t_hoff)`
cast continues to work because `t_hoff` accounts for the bitmap
padding.

The relcache init file's `pgAttributeAttrs()` already declares
`attacl` as `aclitem[]` (TypeOID 1034); the other three trailing
columns still declare `attoptions` as `text` instead of `text[]`
(1009). PG ignores the type-OID metadata when the column is NULL,
so this mismatch is harmless for boot but should be cleaned up in a
follow-up step.

## Test

New `internal/initdb/pg_attribute_null_attoptions_test.go ::
TestPgAttributeRowEmitsNullForOptionalArrayColumns` asserts that
`pgAttributeRow` returns `NullDatum` for column indices 20–23
(attacl, attoptions, attfdwoptions, attmissingval). Future
refactors that accidentally revert the column back to a non-NULL
default will trip this pin loudly.

## Verification

- `go test -count=1 -run TestPgAttributeRowEmitsNullForOptionalArrayColumns ./internal/initdb/` PASS
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures (Step 3t list) unchanged; no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
  advances past the `ERRORDATA_STACK_SIZE` PANIC. The PG standby
  reaches `PM_HOT_STANDBY` and stays there without crashing; new
  blocker (Step 3v territory) is that the test's
  `SELECT status FROM pg_catalog.pg_stat_wal_receiver` probe hangs
  rather than crashing.

## Carry-over for Step 3v+

1. `pgAttrColDefs()` and `pgAttributeAttrs()` still type
   `attoptions`/`attfdwoptions`/`attmissingval` as plain `text`
   instead of `text[]` / `anyarray`. Fix when a concrete symptom
   surfaces.
2. `pgAttributeAttrs()` is also missing the PG18 trailing columns
   (currently declares only 22 of 26 columns); Step 3v may need to
   align it with `pgAttrColDefs()` (24 columns) and the upstream
   PG18 `Form_pg_attribute`.
3. The new wal_receiver query hang requires its own diagnosis
   (likely a goopg primary issue, not a standby issue).
