# M0106-0010 Step 3br — `pg_parameter_acl_oid_index` catalog seed

## Problem

After Step 3bq seeded `pg_parameter_acl_parname_index` (OID 6246),
`TestE2E_FailoverGoopgToPG/async` (with `GOOPG_RUN_BLOCKED_M0102_E2E=1`)
is expected to surface the next PG-standby boot FATAL:

```
FATAL:  could not open relation with OID 6247
```

PG's `RelationIdGetRelation(6247) → ScanPgRelation(6247)` returns NULL
because no `pg_class` row is seeded for OID 6247, and PG's relcache
build falls back to `formrdesc()` for well-known catalog OIDs only —
FATALs for anything else.

OID 6247 is `pg_parameter_acl_oid_index` per
`postgres/src/include/catalog/pg_parameter_acl.h:54`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_parameter_acl_oid_index, 6247,
    ParameterAclOidIndexId, pg_parameter_acl, btree(oid oid_ops));

MAKE_SYSCACHE(PARAMETERACLOID, pg_parameter_acl_oid_index, 4);
```

This is the PRIMARY KEY of `pg_parameter_acl` (heap OID 6243, nailed
shared rel since Step 3bp). It backs the `PARAMETERACLOID` syscache
used by every `SearchSysCache1(PARAMETERACLOID, …)` probe in PG.

## Fix

Pure catalog-seed addition on the **shared** track (companion to
Step 3bq's name-keyed UNIQUE non-PKEY 6246), mirroring the
single-column `oid_ops` UNIQUE PKEY pattern of Steps:

- 3bk (`pg_language_oid_index`, OID 2682)
- 3l  (`pg_opclass_oid_index`, OID 2687)
- 3ax (`pg_extension_oid_index`, OID 3080)
- 3at (`pg_event_trigger_oid_index`, OID 3468)
- 3bd (`pg_foreign_data_wrapper_oid_index`, OID 112)
- 3bg (`pg_foreign_server_oid_index`, OID 113)
- 3bo (`pg_opfamily_oid_index`, OID 2755)

No encoder, builder, or `Init` flow change.

### Changes

1. **`internal/initdb/initdb.go::pgIndexInitialEntries`** appends after
   the Step 3bq 6246 entry on the shared slice:

   ```go
   entry(6247, 6243, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_parameter_acl_oid_index
   ```

   `IndKey={1}` = `Anum_pg_parameter_acl_oid` (1-based, `oid` column).
   `oid_ops` carries no collation. `IsUnique=true, IsPrimary=true`
   because the declaration is `DECLARE_UNIQUE_INDEX_PKEY`.

2. **`internal/initdb/relcache_init.go::nailedSharedRels`** idxSpec
   gains `{6247, "pg_parameter_acl_oid_index"}` after the Step 3bq
   6246 entry. `flattenRels` + `pgIndexNattsByOID` derives
   `RelKind='i', RelNatts=1` so the `relnatts == indnatts` check
   (`relcache.c:1492`) passes.

3. **`internal/initdb/initdb.go`** — the `global/` empty-placeholder
   OID list in `bootstrapPostgresDatabase` gains
   `6247, // pg_parameter_acl_oid_index (Step 3br)` immediately after
   the Step 3bq 6246 entry. The Step-3k empty btree placeholder
   (`makeBtreeRootPage`, `btm_root = P_NONE`) is sufficient because
   pg_parameter_acl is currently unpopulated — any
   `SearchSysCache1(PARAMETERACLOID, …)` probe correctly returns no
   row. Shared indexes only live in `global/` (not `base/{1,5}/`), so
   no per-DB list additions.

Seed threads automatically through
`bootstrapPgClassTuples` → `bootstrapPgAttributeTuples`
(1 row for the `oid` key column) → `bootstrapPgIndexTuples`
(writes Form_pg_index row, captures TID in `pgIndexTIDs`) →
`bootstrapPgIndexIndexrelidIndex` (adds leaf to populated 2-page btree
at file 2679) → `bootstrapPgClassOidIndex` (leaf at file 2662).

### Regression pins

- `TestPgParameterAclOidIndexSeededFromInitialEntries` — pins
  `(IndRelid=6243, IndKey=[1], IsUnique=true, IsPrimary=true,
  IndClass=[1981 oid_ops], IndCollation=[0])`.
- `TestNailedSharedRelsContainsPgParameterAclOidIndex` — pins
  `RelName="pg_parameter_acl_oid_index", RelKind='i', RelNatts=1`.
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` extended with
  `6247:{1}` (strict count guard forces future additions to update).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with `6247` so the populated 2679 btree must carry this
  leaf.

Both new tests live in
`internal/initdb/pg_parameter_acl_oid_index_test.go`.

## Verification

- `go build ./...` — PASS.
- Targeted: `go test -count=1 -run
  'TestPgParameterAclOidIndex|TestNailedSharedRelsContainsPgParameterAclOidIndex|TestPgParameterAclParnameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedRelTypesMatchPG18FormrdescConstants|TestNailedSharedRelsContainsPgParameterAcl|TestNailedLocalRelsContainsPgOpfamily|TestPgOpfamilyOidIndex'
  ./internal/initdb/` — PASS.
- Full initdb suite: same 14 pre-existing baseline failures as Step 3bq
  (no new regressions).
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Follow-up

This closes Step 3bq's deferred companion — both pg_parameter_acl
indexes (6246 parname text_ops UNIQUE + 6247 oid_ops UNIQUE PKEY) are
now seeded. The next PG-standby FATAL surfaced by
`TestE2E_FailoverGoopgToPG/async` (with `GOOPG_RUN_BLOCKED_M0102_E2E=1`)
should be the next missing catalog OID; resolution tracked under the
next step letter in fix_plan.md M0106-0010.
