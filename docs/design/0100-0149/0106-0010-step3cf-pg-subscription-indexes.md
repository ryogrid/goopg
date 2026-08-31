# M0106-0010 Step 3cf — pg_subscription indexes (6114 + 6115) seed

## Status

Landed 2026-05-18 (step 3cf). Closes the FATAL
`could not open relation with OID 6115` PG-standby boot blocker that
surfaced after Step 3ce seeded pg_statistic. E2E re-run confirms the
next FATAL is OID 6102 (pg_subscription_rel), to be handled by the next
step in the seed chain.

## Authoritative source

`postgres/src/include/catalog/pg_subscription.h`:

- `CATALOG(pg_subscription,6100,SubscriptionRelationId) BKI_SHARED_RELATION`
  — heap OID 6100, shared (lives under `global/`).
- `DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_oid_index, 6114,
  SubscriptionObjectIndexId, pg_subscription, btree(oid oid_ops));`
- `DECLARE_UNIQUE_INDEX(pg_subscription_subname_index, 6115,
  SubscriptionNameIndexId, pg_subscription,
  btree(subdbid oid_ops, subname name_ops));`
- `MAKE_SYSCACHE(SUBSCRIPTIONOID, pg_subscription_oid_index, 4);`
- `MAKE_SYSCACHE(SUBSCRIPTIONNAME, pg_subscription_subname_index, 4);`

The pg_subscription heap was already nailed by an earlier step
(`{6100, "pg_subscription", 6101, 'r', 9, true, pgSubscriptionAttrs()}`
in `nailedSharedRels`); only its two indexes were missing. PG's
`load_critical_index` pass opens every declared index of a nailed rel,
so both must be seeded together (family-complete).

## Heap attnums consulted

pg_subscription attribute layout (per `pgSubscriptionAttrs()` and
`pg_subscription_d.h`): 1=oid, 2=subdbid, 3=subskiplsn, 4=subname,
5=subowner, …

- 6114 (`pg_subscription_oid_index`) keys on attnum 1 (oid).
- 6115 (`pg_subscription_subname_index`) keys on attnums 2 (subdbid)
  and 4 (subname). NOTE: subname is heap col 4, NOT col 3 —
  subskiplsn (col 3) sits between subdbid and subname.

## Code changes

### `internal/initdb/relcache_init.go`

1. `nailedSharedRels` idxSpec list gains two entries after the Step 3ca
   6002 entry:
   ```go
   {6114, "pg_subscription_oid_index"},
   {6115, "pg_subscription_subname_index"},
   ```
   `flattenRels` consults `pgIndexNattsByOID()` and assigns
   `RelKind='i'`, `RelNatts=1` for 6114 and `RelNatts=2` for 6115
   automatically, so `RelationInitIndexAccessInfo`'s
   `relnatts == indnatts` check (`relcache.c:1492`) passes.

### `internal/initdb/initdb.go`

1. `pgIndexInitialEntries` shared section gains two rows after the
   Step 3ca 6002 entry:
   ```go
   entry(6114, 6100, []int16{1},
         []uint32{oidOps}, []uint32{0},
         true, true) // pg_subscription_oid_index
   entry(6115, 6100, []int16{2, 4},
         []uint32{oidOps, nameOps}, []uint32{0, cCollation},
         true, false) // pg_subscription_subname_index
   ```
   - `IndKey = {1}` for 6114, `{2, 4}` for 6115 (subskiplsn skipped).
   - `IndClass = {oid_ops}` for 6114; `{oid_ops, name_ops}` for 6115.
   - `IndCollation = {0}` for 6114; `{0, C_COLLATION_OID=950}` for
     6115 (name_ops uses collation, oid_ops does not).
   - 6114: `IsUnique=true, IsPrimary=true` (DECLARE_UNIQUE_INDEX_PKEY).
   - 6115: `IsUnique=true, IsPrimary=false` (DECLARE_UNIQUE_INDEX,
     not _PKEY).
2. Both "Critical index placeholder pages" OID lists at
   `bootstrapPostgresDatabase` gain `6114` and `6115` after the Step
   3ce 2696 entry. Empty PG18 btree metapages (Step 3k's
   `makeBtreeRootPage`) are correct because pg_subscription is empty
   at bootstrap.
3. No new entries in `bootstrapSharedCatalogPlaceholders` heapOIDs —
   `6100` (the pg_subscription heap file under `global/`) was already
   seeded by an earlier step.

No type-helper additions: `oid` (26) and `name` (19) are already
registered in `pgCatalogTypeOID` / `pgCatalogTypeLen` / `pgTypeByVal` /
`pgTypeAlignChar` / `pgTypeStorageChar`.

## Regression pins (new file `internal/initdb/pg_subscription_indexes_nailed_test.go`)

- `TestNailedSharedRelsContainsPgSubscriptionIndexes` — asserts both
  OIDs 6114 and 6115 appear in `nailedSharedRels` with `RelKind='i'`
  and matching `RelNatts`.
- `TestPgSubscriptionIndexInitialEntries` — pins
  `(IndRelid, IndKey, IndClass, IndCollation, IsUnique, IsPrimary)`
  for both rows.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds
  `6114: {1}, 6115: {2,4}` to the authoritative map (the strict count
  guard auto-rejects future additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with `6114, 6115` so the populated 2-page btree at file
  2679 must carry both OIDs' leaves.

## Verification

```
go test -count=1 -run \
  'TestNailedSharedRelsContainsPgSubscriptionIndexes|TestPgSubscriptionIndexInitialEntries|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages' \
  ./internal/initdb/
```
→ PASS.

```
go test -count=1 ./internal/initdb/
```
→ same 14 pre-existing baseline failures as Step 3ce
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

E2E re-run (`GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run
TestE2E_FailoverGoopgToPG/async ./internal/testport/`) confirms the
FATAL on OID 6115 is closed — the next FATAL is OID 6102
(pg_subscription_rel), to be handled by the next step.

## Forward look

OID 6102 (`pg_subscription_rel`) is the next per-database catalog
needing a family-complete seed (heap + its three indexes per
`postgres/src/include/catalog/pg_subscription_rel.h`). The seed chain
Step 3w → 3aa → 3ad … 3ce → 3cf continues by adding one
catalog-family per step.
