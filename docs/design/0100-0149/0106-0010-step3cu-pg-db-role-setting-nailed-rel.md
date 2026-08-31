# M0106-0010 Step 3cu — Seed `pg_db_role_setting` (OID 2964) + companion index 2965

## Blocker

E2E run `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
after Step 3ct unblocked `pg_database` row decoding produces the first
post-`CheckMyDatabase` FATAL on every PG-standby user backend:

```
FATAL: XX000: could not open relation with OID 2964
```

`process_settings(MyDatabaseId, GetSessionUserId())` is invoked at the
tail of `InitPostgres` (postgres/src/backend/utils/init/postinit.c).
It opens `pg_db_role_setting` to apply per-database/per-role GUC
defaults. With no `pg_class` row for 2964, `RelationBuildDesc(2964) →
ScanPgRelation(2964)` returns NULL and the relation_open path FATALs.

The cascading `invalid attalign value:` FATALs reported on follower
backends are the Step 3cs catcache-stale pattern; they disappear
automatically once the first FATAL is closed.

## Authoritative PG18 source

`postgres/src/include/catalog/pg_db_role_setting.h`:

```c
CATALOG(pg_db_role_setting, 2964, DbRoleSettingRelationId) BKI_SHARED_RELATION
{
    Oid  setdatabase BKI_LOOKUP_OPT(pg_database);
    Oid  setrole     BKI_LOOKUP_OPT(pg_authid);
#ifdef CATALOG_VARLEN
    text setconfig[1];          /* GUC settings to apply at login */
#endif
} FormData_pg_db_role_setting;

DECLARE_UNIQUE_INDEX_PKEY(pg_db_role_setting_databaseid_rol_index,
    2965, DbRoleSettingDatidRolidIndexId, pg_db_role_setting,
    btree(setdatabase oid_ops, setrole oid_ops));
```

`pg_db_role_setting` is shared (BKI_SHARED_RELATION) — heap files live
under `global/`, not `base/<dboid>/`. There is no MAKE_SYSCACHE on the
index; `process_settings` reads rows via a direct sysscan on the
composite (setdatabase, setrole) key.

`pg_db_role_setting` is not formrdesc'd (no
`DbRoleSettingRelation_Rowtype_Id` constant in PG18 headers; only
pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
are formrdesc'd shared rels at relcache.c:4075-4083), so `RelType=83`
is safe — Step 3v's Phase3 `relation->rd_att->tdtypeid == relp->reltype`
assertion (relcache.c:4293) does not fire.

## Fix (pure catalog-seed addition; no encoder, builder, or `Init`
flow change)

1. `internal/initdb/relcache_init.go` gains
   `pgDbRoleSettingAttrs()` returning the 3-column PG18 schema:

   | # | Name        | TypeOID | Len | NotNull |
   |---|-------------|---------|-----|---------|
   | 1 | setdatabase | 26 (oid)  |  4 | true  |
   | 2 | setrole     | 26 (oid)  |  4 | true  |
   | 3 | setconfig   | 1009 (text[]) | -1 | false |

2. `nailedSharedRels` gains
   `{2964, "pg_db_role_setting", 83, 'r', 3, true, pgDbRoleSettingAttrs()}`
   placed immediately after the Step 3ch pg_tablespace entry.

3. `nailedSharedRels` idxSpec list gains
   `{2965, "pg_db_role_setting_databaseid_rol_index"}` so `flattenRels`
   derives `RelKind='i', RelNatts=2` via `pgIndexNattsByOID`.

4. `internal/initdb/initdb.go::pgIndexInitialEntries` gains
   `entry(2965, 2964, []int16{1,2}, []uint32{oidOps, oidOps},
   []uint32{0,0}, true, true)`. UNIQUE PRIMARY composite over the
   pg_db_role_setting heap (OID 2964).

5. `bootstrapSharedCatalogPlaceholders` heap list gains `2964` so the
   empty 8 KiB heap page at `global/2964` exists before PG's `mdopen`.

6. The shared-index placeholder loop at the bottom of
   `bootstrapPostgresDatabase` gains `2965` (alongside 2671/2/6/7,
   2694, 2695, 3593, 6246/7, 6001/2) so the Step-3k empty btree
   placeholder lands at `global/2965` before the populated 2-page
   2679 btree gets a 26th leaf via the existing
   `bootstrapPgIndexIndexrelidIndex` plumbing.

The seed threads automatically through the existing flow:

`bootstrapPgClassTuples` → `bootstrapPgAttributeTuples`
(3 indexKeyAttrs rows) → `bootstrapPgIndexTuples` (writes Form_pg_index
row with `indnatts=2` + captures TID in `pgIndexTIDs[2965]`) →
`bootstrapPgIndexIndexrelidIndex` (leaf at file 2679) →
`bootstrapPgClassOidIndex` (leaf at 2662) →
`bootstrapPgAttributeRelidAttnumIndex` (2 composite-key leaves at 2659).

## Regression pins

In `internal/initdb/pg_db_role_setting_nailed_test.go`:

- `TestNailedSharedRelsContainsPgDbRoleSetting` — asserts the heap
  catalog entry's `(OID, RelName, RelKind, RelNatts, RelType, Attrs)`
  including the 3-column schema sourced from pg_db_role_setting_d.h.
- `TestNailedSharedRelsContainsPgDbRoleSettingDatabaseidRolIndex` —
  asserts the companion index 2965's
  `(RelName, RelKind='i', RelNatts=2)`.
- `TestPgDbRoleSettingDatabaseidRolIndexSeededFromInitialEntries` —
  asserts `(IndRelid=2964, IndKey=[1,2], IsUnique=true, IsPrimary=true)`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `2965: {1, 2}`
  (strict count guard auto-rejects future additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 2965 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestPgDbRoleSetting|TestNailedSharedRelsContainsPgDbRoleSetting|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn'
  ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same 15 pre-existing baseline
  failures as Step 3ct (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`); no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` PASS.

## Next-blocker hypothesis

With 2964 seeded, the next `process_settings` lookup against
`pg_db_role_setting_databaseid_rol_index` reads an empty btree and
returns zero matches — same outcome as a stock initdb cluster with
no per-DB/per-role settings. The user backend should advance past
`process_settings` into `process_session_preload_libraries` or the
post-init `ClientAuthentication` flow. The next FATAL is expected to
surface a missing catalog row (pg_authid lookup of session user, or a
pg_class probe for a non-nailed catalog opened during AuthN); the
exact OID is established by the next E2E re-run.
