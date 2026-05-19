# M0106-0010 Step 3z — Seed `pg_auth_members_role_member_index` (OID 2694)

## Problem

After Step 3y landed `pg_amop_fam_strat_index` (OID 2653) into the
catalog seed, the E2E failover test
`TestE2E_FailoverGoopgToPG/async` advanced past the OID 2653 FATAL and
hit the next missing relation:

```
FATAL:  could not open relation with OID 2694
```

PG18's `RelationIdGetRelation(2694)` is reached from every backend's
authorization check path: any client connecting to a goopg-cloned
standby triggers the role-membership syscache (`AUTHMEMROLEMEM`), whose
backing index is `pg_auth_members_role_member_index`. Without a
pg_class row + relcache seed for that OID, the backend FATALs before
even returning the ReadyForQuery prompt.

## Authoritative source

`postgres/src/include/catalog/pg_auth_members.h:49`:

```c
DECLARE_UNIQUE_INDEX(pg_auth_members_role_member_index, 2694,
    AuthMemRoleMemIndexId, pg_auth_members,
    btree(roleid oid_ops, member oid_ops, grantor oid_ops));
MAKE_SYSCACHE(AUTHMEMROLEMEM, pg_auth_members_role_member_index, 8);
```

- OID 2694 = `AuthMemRoleMemIndexId`
- Parent heap = `pg_auth_members` (OID 1261, `BKI_SHARED_RELATION`)
- 3-column composite btree on `(roleid, member, grantor)`
- `DECLARE_UNIQUE_INDEX` (NOT `_PKEY` — the pkey index is OID 6303)
- Shared because the parent is shared

`pg_auth_members` attnums (from `pg_auth_members_d.h` + catalog
struct): `1=oid, 2=roleid, 3=member, 4=grantor, 5=admin_option,
6=inherit_option, 7=set_option`.

## Why the previous infrastructure already covers this case

Steps 3p–3y already wired the general "seed an empty btree placeholder
+ a pg_class/pg_index pair" pipeline:

- `pgIndexInitialEntries()` (`internal/initdb/initdb.go`) materialises
  the `pg_index` heap row.
- `nailedSharedRels` / `nailedLocalRels`
  (`internal/initdb/relcache_init.go`) materialise the matching
  `pg_class` row in both the relcache init file and `bootstrapPgClass`.
- `bootstrapPostgresDatabase` writes an empty btree page placeholder
  into `base/1/`, `base/5/`, and `global/` so the smgr open does not
  ENOENT.

Step 3z is a pure catalog-seed addition through the existing pipeline,
mirroring the sibling shared index `pg_auth_members_member_role_index`
(OID 2695) that has been seeded for many milestones.

## Changes

### `internal/initdb/initdb.go`

1. `bootstrapPostgresDatabase`: add `2694` to the shared-index OID list
   that writes empty btree placeholders into `global/`. Local lists
   (`base/1/`, `base/5/`) are not touched — 2694 is a shared catalog
   index, matching how the sibling 2695 has always been handled.
2. `pgIndexInitialEntries`: append a new `entry(2694, 1261, []int16{2,
   3, 4}, []uint32{oidOps, oidOps, oidOps}, []uint32{0, 0, 0}, true,
   false)` row directly next to 2695. `IsUnique=true`, `IsPrimary=false`
   because the declaration is `DECLARE_UNIQUE_INDEX`, not `_PKEY`.

### `internal/initdb/relcache_init.go`

Add `{2694, "pg_auth_members_role_member_index"}` to the `nailedSharedRels`
`idxSpec` list. `flattenRels` derives `RelNatts=3`, `RelKind='i'`,
`RelType=0` automatically via `indexNailed`.

### Test pinning

- `pg_index_indkey_test.go`: add `2694: {2, 3, 4}` to the pinned
  shared-catalogs map.
- `btree_index_bootstrap_test.go`: add `2694` to the
  `mustHave` OID list.
- `pg_auth_members_role_member_index_test.go` (new): two tests
  pinning the `pgIndexInitialEntries` row (heap OID 1261, indkey
  `[2 3 4]`, unique, not primary) and the `nailedSharedRels` entry
  (RelKind='i', RelNatts=3).

## Why the empty-btree placeholder is sufficient

PG's `load_critical_index` only needs the relcache descriptor +
*existence* of the relfile to avoid the FATAL — the index does not
need to contain any tuples during initial boot. The 8-key
`AUTHMEMROLEMEM` syscache will populate from on-disk pg_auth_members
content the moment the first lookup is needed; an empty btree returning
zero rows is the canonical "no role memberships yet" answer for a
freshly cloned standby. The composite-key encoder for 3-column oid_ops
btree leaves is not yet implemented; until it is needed, the
placeholder is correct.

## Verification

- `go test -count=1 -run "TestPgAuthMembersRoleMemberIndexSeededFromInitialEntries|TestNailedSharedRelsContainsPgAuthMembersRoleMemberIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18" ./internal/initdb/`
  PASS.
- `go build ./...` PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -timeout 180s -run "TestE2E_FailoverGoopgToPG/async" ./internal/testport/`
  advances past `FATAL: could not open relation with OID 2694` to the
  next blocker `FATAL: could not open relation with OID 2605`
  (`pg_cast` heap — Step 3aa territory).

Pre-existing failures in `./internal/initdb/` (`TestMigrationFromLegacyJSONCluster`,
`TestCommittedTableSurvivesCrashRestart`, ...) are unrelated and
reproduce on the parent commit `7f5703d` before this change.

## Next blocker (Step 3aa)

OID 2605 is `CastRelationId` (the `pg_cast` heap relation). Per
`grep -n "2605" postgres/src/include/catalog/pg_cast_d.h:23` this is
the heap, not an index. The next step needs to nail
`pg_cast` (and likely its companion indexes 2660 / 2661) following the
established heap+index seed pattern used for `pg_aggregate` in Step 3w.
