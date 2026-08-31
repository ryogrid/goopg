# 0119-0004 — Predefined-role name resolution + `ROLE_PG_DATABASE_OWNER` carve-out (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`check_role_membership_authorization`),
`postgres/src/backend/catalog/pg_shdepend.c` (`checkSharedDependencies`,
`IsPinnedObject`), `postgres/src/include/access/transam.h` (`FirstUnpinnedObjectId`)

## Problem

The `0119-0004-role-membership-admin-option-authz.md` row (loop that landed
`checkRoleMembershipAuthorization`'s ADMIN OPTION/superuser gate) left one
residual explicitly unported: PG's `ROLE_PG_DATABASE_OWNER` carve-out —

```c
if (is_grant && roleid == ROLE_PG_DATABASE_OWNER)
    ereport(ERROR, errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
            errmsg("role \"%s\" cannot have explicit members", ...));
```

— was **unreachable**, not merely unimplemented: goopg's 16 PG18 predefined
"pg_\*" roles (`pg_database_owner`, `pg_read_all_data`, `pg_monitor`, ...) were
only ever heap-seeded into `global/1260` at `initdb` time
(`internal/initdb/initdb.go`'s `predefined` slice) or heap-resynced by
`executor.SyncPgAuthidFile`'s hardcoded `pgAuthidPredefined` list — never
registered into the LIVE catalog role-name registry
(`catalog.InMemory.roles`). `im.RoleOID("pg_database_owner")` — and therefore
`execRoleMembershipChange`'s `resolveRole` — already failed every such
statement with a hard `42704 "role does not exist"` before
`checkRoleMembershipAuthorization` ever ran.

This also meant the standard, documented PG idiom `GRANT pg_read_all_data TO
alice;` (granting a predefined role's privileges to a real role) simply did
not work in goopg at all — a real gap beyond the one ledger residual.

## Fix

### `catalog.InMemory.predefinedRoles` — a dedicated, read-only registry

A **separate** map from `roles` (`internal/catalog/catalog.go`), keyed by the
same lower-cased name convention, populated once at `NewInMemory()`
construction from a new package-level `predefinedRoleSeeds` list (name + fixed
OID for all 16 roles — a third, deliberately independent copy of the same data
already duplicated between `internal/initdb/initdb.go`'s `predefined` slice
and `internal/executor/pg_authid_sync.go`'s `pgAuthidPredefined`; `internal/
catalog` cannot import either without an import cycle).

Kept **structurally separate** from `roles` on purpose, so none of `roles`'
mutators (`RegisterRole`, `UnregisterRole`, `RenameRole`, `SetRoleAttrs`) or
readers (`AllRoleStates`) ever see it:

- `RoleOID`/`RoleExists` now also consult `predefinedRoles` — `RoleOID`
  checks it **first** (predefined roles are a fixed, install-time fact; an
  exact-name collision from a hypothetical `CREATE ROLE pg_database_owner`
  — goopg does not yet port PG's `IsReservedName` "pg\_" rejection at CREATE
  ROLE time, a separate, pre-existing gap — must never let a user-registered
  entry shadow the real predefined OID).
- New `IsPredefinedRole(name string) bool` (added to the `Catalog` interface)
  backs the DROP ROLE pinned-object guard below.
- `RoleNameForOID`/`RoleNameForOIDOrUnknown` (the reverse direction) gained a
  shared `roleNameForOIDLocked` helper that also checks `predefinedRoles`, so
  a predefined-role OID recorded in an ACL/membership row renders back to its
  name instead of falling back to the bare numeral — this reverse path is
  reachable the moment the forward path (`RoleOID`) succeeds for the first
  time, so it had to move together (sibling-path rule).
- **Deliberately excluded from `AllRoleStates()`**: `executor.SyncPgAuthidFile`
  already has its own dedicated predefined-role writer (`pgAuthidPredefined`);
  if `predefinedRoles` entries leaked into `AllRoleStates()`, the heap sync
  would emit a duplicate `pg_authid` row for every predefined role.

### `checkRoleMembershipAuthorization`'s `ROLE_PG_DATABASE_OWNER` carve-out

`internal/executor/operators_ddl_role_membership.go`: the carve-out is now
checked **first**, unconditionally, before the superuser/admin-option
branches — matching `check_role_membership_authorization`'s own ordering —
and only for `isGrant` (REVOKE is unaffected, matching PG). Raises `0A000`
with PG's exact `errmsg` text, `role "pg_database_owner" cannot have explicit
members"`.

### DROP ROLE/USER/GROUP pinned-object guard

Registering predefined names into a globally-resolvable map introduced a new
risk this loop had to close in the same pass: `execDropCompat`'s
`role`/`user`/`group` arm (`internal/executor/operators_ddl.go`) resolves
existence via `Catalog.RoleExists` directly against the catalog (a *different*
path from `ALTER`/`RENAME ROLE`, which gate through the separate
server-level `s.roles` registry in `internal/server/role_ddl.go` and are
therefore unaffected by this change) — so `DROP ROLE pg_read_all_data` would
now resolve as "exists" and proceed to `UnregisterRole`, silently succeeding
on an operation real PG hard-rejects.

Verified against a real, separately-`initdb`'d PG 18.3 instance
(`postgres/local_install`, scratch port 5599): `DROP ROLE pg_read_all_data`
and `DROP ROLE IF EXISTS pg_read_all_data` (IF EXISTS does **not** suppress
this — the role DOES exist; IF EXISTS only suppresses "does not exist") both
raise:

```
ERROR:  cannot drop role pg_read_all_data because it is required by the database system
```

— PG's generic "pinned object" guard (`checkSharedDependencies`,
`pg_shdepend.c`: `IsPinnedObject` covers any OID below
`FirstUnpinnedObjectId` = 12000; all 16 predefined role OIDs qualify), using
`errcode(ERRCODE_DEPENDENT_OBJECTS_STILL_EXIST)` = `2BP01`, message format
`"cannot drop %s because it is required by the database system"` with an
**unquoted** `role %s` object description (`objectaddress.c`'s
`AuthIdRelationId` case) — distinct from the usual quoted `role "%s"`
convention used elsewhere in this codebase. `execDropCompat` now checks
`IsPredefinedRole` before the existing `RoleExists` branch and returns this
error verbatim, for every name in the statement's list, regardless of
`IF EXISTS`.

## Tests

- `internal/catalog/role_membership_test.go`:
  `TestPredefinedRoleResolution` — forward/reverse resolution, case
  insensitivity, non-existent `pg_`-prefixed name still reports false,
  `RegisterRole`/`UnregisterRole` cannot shadow or remove a predefined entry,
  `AllRoleStates()` excludes every predefined role.
- `internal/executor/operators_ddl_role_membership_test.go`:
  `TestExecRoleMembershipChangeGrantsPredefinedRole` (`GRANT pg_read_all_data
  TO alice` now succeeds), `TestExecRoleMembershipChangeGrantPgDatabaseOwnerRejected`
  (`0A000`, no state mutation, REVOKE unaffected),
  `TestExecDropCompatPredefinedRolePinned` (`2BP01` for both `IF EXISTS`
  states, predefined role survives, a plain registered role is unaffected).
- `internal/initdb/role_ddl_recovery_test.go`'s `TestPgAuthidSyncLoadRoundTrip`
  updated: its old assertion ("predefined role must never appear in
  `RoleExists`") encoded exactly the gap this loop closes; now asserts
  `RoleExists` is true (resolves) while `AllRoleStates()` still excludes it
  (no heap-sync duplication).

Gates: `go build ./...`/`go vet ./...` clean; `internal/catalog`+
`internal/executor`+`internal/server`+`internal/parser`+`internal/wal`+
`internal/initdb` suites PASS; `TestPort_PgDumpallRoleMembership`+
`TestPort_PgDumpConnectionSetup` PASS (no regression); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke = pre-commit hook.

## Deferred

`reserved_class_prefix` extension-namespace validation for `GRANT ... ON
PARAMETER` remains open, unrelated to this slice.

## Follow-up: `pg_authid`/`pg_roles` predefined-role rows (loop #46)

Closes discovery (a) above: `internal/catalog/catalog.go`'s `pgRoles`/
`pgAuthid` `VirtualRows` closures now append a second row-emission pass over
`predefinedRoleSeeds` (sorted by name for deterministic output), after the
existing `c.roles` loop, using each predefined role's fixed OID from
`c.predefinedRoles`:

- `pg_roles`: `rolsuper='f'`, `rolcanlogin='f'` — every PG18 predefined role's
  real `pg_authid.dat` shape.
- `pg_authid`: reuses the existing `rowFor` helper with a zero-value
  `&RoleAttrs{}` — `rowFor`'s own default-flip logic (`!a.CanLogin` →
  `rolcanlogin='f'`) combined with its hardcoded `rolinherit="t"` literal
  already produces exactly PG's predefined-role row shape (rolsuper/
  rolcanlogin=`f`, rolinherit=`t`, rolconnlimit=`-1`, rolpassword=`NULL`) with
  no new per-row logic.

This is a read-only *view* addition — `predefinedRoles` itself (the OID
registry) is unchanged, still structurally separate from `roles`, still
excluded from `AllRoleStates()` (no heap-sync duplication risk).

Verified end-to-end against the exact failure mode described above:
`TestPort_PgDumpallPredefinedRoleMembership`
(`internal/testport/pgdumpall_role_membership_test.go`) grants
`pg_read_all_data` to a real role, then a second membership sorting after it
by role name, and asserts real `pg_dumpall --globals-only` emits both `GRANT`
lines with no "orphaned pg_auth_members entry" warning — before this fix, the
NULL `ur.rolname` for the predefined-role grant made `pg_dumpall` treat it as
orphaned and (since rows are `ORDER BY role` and NULLs sort last, so all
subsequent rows are also incorrectly at risk once a NULL is hit) `break` out
of its membership loop.

Unit coverage: `TestPgCatalogBootstrapViews` (`pg_roles` extended to assert 17
rows: 1 bootstrap superuser + 16 predefined, `pg_read_all_data`'s
`rolsuper`/`rolcanlogin` both `f`); new `TestPgAuthidExposesPredefinedRoles`
(`pg_authid` — 18 rows including a registered user role, `pg_read_all_data`'s
full attribute row incl. fixed OID 6181) (`internal/catalog/catalog_test.go`).

Gates: `go build ./...`/`go vet ./...` clean; `internal/catalog` suite PASS;
`TestPort_PgDumpallPredefinedRoleMembership` (new) +
`TestPort_PgDumpallRoleMembership` (no regression) PASS against real
`pg_dumpall` 18.3.
