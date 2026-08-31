# M0106-0010 Step 3cg — pg_subscription_rel (OID 6102) + PKEY 6117 seed

## Status

Landed 2026-05-18 (step 3cg). Closes the FATAL `could not open relation
with OID 6102` PG-standby boot blocker that surfaces after Step 3cf
seeded the pg_subscription index pair.

## Authoritative source

`postgres/src/include/catalog/pg_subscription_rel.h`:

- `CATALOG(pg_subscription_rel,6102,SubscriptionRelRelationId)` — heap
  OID 6102, per-database (NO `BKI_SHARED_RELATION`).
- `DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_rel_srrelid_srsubid_index,
  6117, SubscriptionRelSrrelidSrsubidIndexId, pg_subscription_rel,
  btree(srrelid oid_ops, srsubid oid_ops));`
- `MAKE_SYSCACHE(SUBSCRIPTIONRELMAP,
  pg_subscription_rel_srrelid_srsubid_index, 64);`

PG's `load_critical_index` pass opens every declared index of a nailed
rel, so the heap (6102) and its single index (6117) must be seeded
together (family-complete).

## Heap schema

`pg_subscription_rel_d.h` (Anum_pg_subscription_rel_* 1..4,
Natts_pg_subscription_rel == 4):

| Attnum | Name        | TypeOID | Len | NotNull | Note                              |
|--------|-------------|---------|-----|---------|-----------------------------------|
| 1      | srsubid     | 26      | 4   | true    | oid                               |
| 2      | srrelid     | 26      | 4   | true    | oid                               |
| 3      | srsubstate  | 18      | 1   | true    | char                              |
| 4      | srsublsn    | 3220    | 8   | false   | pg_lsn, BKI_FORCE_NULL in VARLEN  |

`srsublsn` is fixed-width pg_lsn (8 bytes) but the header declares it
inside `#ifdef CATALOG_VARLEN` with `BKI_FORCE_NULL` so that NULL is
allowed for the pre-sync state. The pg_attribute row must reflect
`attnotnull = false`.

pg_subscription_rel has no `oid` system column — attnums start at
1 = srsubid.

## Code changes

### `internal/initdb/relcache_init.go`

1. New helper `pgSubscriptionRelAttrs()` returning the 4-column
   descriptor above.
2. `nailedLocalRels` heap list gains
   `{6102, "pg_subscription_rel", 83, 'r', 4, false, pgSubscriptionRelAttrs()}`
   after the Step 3ce 2619 entry.
3. `nailedLocalRels` idxSpec list gains
   `{6117, "pg_subscription_rel_srrelid_srsubid_index"}` after the
   Step 3ce 2696 entry. `flattenRels` consults `pgIndexNattsByOID()`
   and assigns `RelKind='i'`, `RelNatts=2` automatically, so
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check
   (`relcache.c:1492`) passes.

### `internal/initdb/initdb.go`

1. `pgIndexInitialEntries` local section gains one row after the
   Step 3ce 2696 entry:
   ```go
   entry(6117, 6102, []int16{2, 1},
         []uint32{oidOps, oidOps}, []uint32{0, 0},
         true, true) // pg_subscription_rel_srrelid_srsubid_index
   ```
   - `IndKey = {2, 1}` — leads on srrelid (attnum 2), then srsubid
     (attnum 1).
   - `IndClass = {oid_ops, oid_ops}`.
   - `IndCollation = {0, 0}` — oid_ops carries no collation.
   - `IsUnique=true, IsPrimary=true` (DECLARE_UNIQUE_INDEX_PKEY).
2. Both "Critical index placeholder pages" OID lists at
   `bootstrapPostgresDatabase` gain `6117` after the Step 3cf 6115
   entry. Empty PG18 btree metapages (Step 3k's `makeBtreeRootPage`)
   are correct because pg_subscription_rel is empty at bootstrap.
3. **Type-helper additions for pg_lsn (3220).** Per
   `postgres/src/include/catalog/pg_type.dat:410-413`:
   - `pgTypeByVal(3220) → true` (FLOAT8PASSBYVAL on 64-bit).
   - `pgTypeAlignChar(3220) → "d"` (8-byte alignment).
   - `pgTypeStorageChar(3220) → "p"` (PLAIN, default; no entry needed).

   Pre-existing latent issue: `pg_subscription.subskiplsn` (TypeOID 3220)
   has been nailed since an earlier step but pg_lsn was never registered
   in the type helpers, so its pg_attribute row silently emitted
   `attbyval=false, attalign='i'` instead of the correct
   `attbyval=true, attalign='d'`. This step fixes both that pre-existing
   bug and seeds `pg_subscription_rel.srsublsn` correctly from the start.
4. No new entries in `bootstrapMappedLocalCatalogHeaps` oid list or in
   `localRelMap` — both already contained `6102` from a long-standing
   baseline placeholder (the comment that previously labelled 6102 as
   "pg_sequence" was corrected by Step 3cb). The existing empty
   heap-page placeholder is sufficient because pg_subscription_rel is
   unpopulated at bootstrap (rows are added only when a subscription's
   tablesync workers register relations).

## Regression pins (new file `internal/initdb/pg_subscription_rel_nailed_test.go`)

- `TestNailedLocalRelsContainsPgSubscriptionRel` — pins heap entry +
  every column descriptor.
- `TestNailedLocalRelsContainsPgSubscriptionRelSrrelidSrsubidIndex` —
  asserts OID 6117 appears with `RelKind='i'` and `RelNatts=2`.
- `TestPgSubscriptionRelIndexInitialEntries` — pins
  `(IndRelid, IndKey, IndClass, IndCollation, IsUnique, IsPrimary)`.
- `TestPgSubscriptionRelAttrsTypeOIDsMatchPG18` — pins TypeOID/Len/
  NotNull for each of the 4 columns.
- `TestPgLsnTypeHelpersMatchPG18` — pins the new pg_lsn (3220)
  entries in `pgTypeByVal` / `pgTypeAlignChar` / `pgTypeStorageChar`
  so future drift is caught.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `6117: {2,1}` to
  the authoritative map (the strict count guard auto-rejects future
  additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with `6117` so the populated btree at file 2679 must carry
  the new leaf.

## Verification

```
go test -count=1 -run \
  'TestNailedLocalRelsContainsPgSubscriptionRel|TestPgSubscriptionRelIndexInitialEntries|TestPgSubscriptionRelAttrsTypeOIDsMatchPG18|TestPgLsnTypeHelpersMatchPG18|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs' \
  ./internal/initdb/
```

```
go test -count=1 ./internal/initdb/
```
Expect: same baseline failures as Step 3cf (no new regressions).

Cross-package smoke:

```
go test -count=1 ./internal/executor/ ./internal/server/ \
                 ./internal/storage/ ./internal/catalog/ ./internal/mvcc/
```

E2E re-run (`GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run
TestE2E_FailoverGoopgToPG/async ./internal/testport/`) should confirm
the FATAL on OID 6102 is closed.

## Forward look

With pg_subscription_rel (6102) + PKEY (6117) seeded, the
pg_subscription_rel family is fully wired. The next FATAL in the
seed chain will be observed by re-running the E2E.
