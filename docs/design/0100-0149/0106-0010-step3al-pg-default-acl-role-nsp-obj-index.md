# M0106-0010 Step 3al — pg_default_acl_role_nsp_obj_index (OID 827)

## Context

Step 3ak (2026-05-18) seeded the `pg_default_acl` heap relation (OID
826) as a nailed local catalog. Re-running the
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` PG-standby
boot is expected to surface the next blocker:

```
FATAL: could not open relation with OID 827
```

— `RelationCacheInitializePhase3`'s nailed-index walk hits the
`DEFACLROLENSPOBJ` syscache backing index after `pg_default_acl` itself
becomes openable. Without a pg_class row, `RelationBuildDesc(827) →
ScanPgRelation(827)` returns NULL and every forked backend FATALs at
`postgres/src/backend/access/common/relation.c:61`.

## Authoritative source

`postgres/src/include/catalog/pg_default_acl.h:54`:

```c
DECLARE_UNIQUE_INDEX(pg_default_acl_role_nsp_obj_index, 827,
    DefaultAclRoleNspObjIndexId, pg_default_acl,
    btree(defaclrole oid_ops, defaclnamespace oid_ops,
          defaclobjtype char_ops));
MAKE_SYSCACHE(DEFACLROLENSPOBJ, pg_default_acl_role_nsp_obj_index, 8);
```

`pg_default_acl_d.h`:

| attnum | name             | type      |
| ------ | ---------------- | --------- |
| 1      | oid              | oid       |
| 2      | defaclrole       | oid       |
| 3      | defaclnamespace  | oid       |
| 4      | defaclobjtype    | char      |
| 5      | defaclacl        | aclitem[] |

So OID 827 is the `DEFACLROLENSPOBJ` syscache backing index on
`pg_default_acl` (heap OID 826): three columns
`(defaclrole, defaclnamespace, defaclobjtype)`, UNIQUE but not PRIMARY
(`DECLARE_UNIQUE_INDEX`, not the `_PKEY` variant — the PKEY is OID 828,
`pg_default_acl_oid_index`, deferred to Step 3am). None of the three
keys are textual so all collation slots are `0` (`oid_ops` and
`char_ops` are typeless).

## Fix

Pure catalog-seed addition. No encoder, builder, or `Init` flow change.
Same single-OID rhythm as Steps 3w/3x/3y, 3aa/3ab/3ac, and
3ag/3ah/3ai/3aj.

### (a) `internal/initdb/initdb.go::pgIndexInitialEntries`

Gains:

```go
entry(827, 826, []int16{2, 3, 4},
    []uint32{oidOps, oidOps, charOps},
    []uint32{0, 0, 0},
    true, false), // pg_default_acl_role_nsp_obj_index
```

— composite key `{2, 3, 4}` matches the column order declared by
`DECLARE_UNIQUE_INDEX`; `IsUnique=true`, `IsPrimary=false`. Same
composite-UNIQUE pattern as `pg_amop_fam_strat_index` (2653, Step 3y),
`pg_collation_name_enc_nsp_index` (3164, Step 3ae), and
`pg_conversion_default_index` (2668, Step 3ah). Distinguished by the
`char_ops` third slot — the only existing seeded composite-UNIQUE that
uses `char_ops` is `pg_amop_opr_fam_index` (2654) where it sits in the
second slot.

### (b) `internal/initdb/relcache_init.go::nailedLocalRels`

idxSpec gains `{827, "pg_default_acl_role_nsp_obj_index"}`.
`flattenRels` consults `pgIndexNattsByOID()` (returns 3 for OID 827) so
the nailed rel carries `RelKind='i', RelNatts=3`, and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check
(relcache.c:1492) passes.

### (c) `internal/initdb/initdb.go::bootstrapPostgresDatabase`

The three placeholder OID lists (`base/1/`, `base/5/`, `global/`) gain
`827` so the Step-3k empty btree placeholder (`btm_root = P_NONE`) is
laid down at the correct relfile paths. The empty placeholder is
sufficient because pg_default_acl is currently unpopulated (no
default-ACL rows are bootstrapped) — a zero-row lookup is the expected
outcome.

The seed threads automatically through `bootstrapPgClassTuples` →
`bootstrapPgAttributeTuples` (3 indexKeyAttrs rows) →
`bootstrapPgIndexTuples` (writes Form_pg_index row with `indnatts=3` +
captures TID in `pgIndexTIDs[827]`) →
`bootstrapPgIndexIndexrelidIndex` (leaf at file 2679) →
`bootstrapPgClassOidIndex` (leaf at 2662) →
`bootstrapPgAttributeRelidAttnumIndex` (3 composite-key leaves at 2659).

### Companion index 828 (intentionally deferred)

`pg_default_acl_oid_index` (OID 828) is the UNIQUE PRIMARY KEY on
`btree(oid oid_ops)` (`pg_default_acl.h:55`). Deferred to Step 3am to
preserve the single-OID rhythm; once 827 stops FATALing, the next E2E
re-run will surface 828 (or a different `pg_*` OID).

## Tests

New pins in
`internal/initdb/pg_default_acl_role_nsp_obj_index_test.go`:

- `TestPgDefaultAclRoleNspObjIndexSeededFromInitialEntries` — asserts
  `(IndRelid=826, IndKey=[2 3 4], IsUnique=true, IsPrimary=false,
   IndCollation=[0 0 0])`.
- `TestNailedLocalRelsContainsPgDefaultAclRoleNspObjIndex` — asserts
  `RelName="pg_default_acl_role_nsp_obj_index", RelKind='i',
  RelNatts=3`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds
  `827: {2, 3, 4}` (strict count guard so future additions cannot
  bypass the registry).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 827, requiring the populated 2679 btree to carry this
  leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgDefaultAclRoleNspObjIndex|TestNailedLocalRelsContainsPgDefaultAcl|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestPgConversionDefaultIndex|TestPgConversionOidIndex|TestPgConversionNameNspIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestPgClassOidIndexHasSingleKeyColumn'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ak (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Next blocker (Step 3am)

The next E2E re-run is expected to surface OID 828
(`pg_default_acl_oid_index`) — the remaining `pg_default_acl`
companion index per `pg_default_acl.h:55` — or a different nailed-rel
FATAL at a later OID. Whichever surfaces follows the same single-OID
catalog-seed-addition pattern.

## Files

- `internal/initdb/initdb.go` — `pgIndexInitialEntries` entry +
  three placeholder OID lists.
- `internal/initdb/relcache_init.go` — `nailedLocalRels` idxSpec entry.
- `internal/initdb/pg_default_acl_role_nsp_obj_index_test.go` (new) —
  regression pins.
- `internal/initdb/pg_index_indkey_test.go` — `want` map extended.
- `internal/initdb/btree_index_bootstrap_test.go` — `mustHave` extended.
- `docs/design/0106-0010-step3al-pg-default-acl-role-nsp-obj-index.md`
  (this doc).
