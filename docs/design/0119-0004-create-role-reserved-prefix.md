# 0119-0004 — `CREATE ROLE`/`USER`/`GROUP` reserved-`pg_`-prefix rejection (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`CreateRole`)

## Problem

`0119-0004-predefined-role-resolution.md` (loop #43) registered goopg's 16
PG18 predefined `pg_*` roles into the live catalog role-name registry so
`GRANT pg_read_all_data TO alice` and the `ROLE_PG_DATABASE_OWNER`
membership carve-out both became reachable. That loop noted, but explicitly
deferred, a pre-existing sibling gap it found while scoping `RoleOID`'s
lookup-priority ordering: `tryHandleRoleDDL`'s `"create role "`/`"create
user "`/`"create group "` branch (`internal/server/role_ddl.go`) never
called `isReservedRoleName` — only the `ALTER ROLE ... RENAME TO` path did.
Real PG's `CreateRole` (`postgres/src/backend/commands/user.c`) rejects the
reserved `pg_` namespace unconditionally:

```c
/*
 * Check that the user is not trying to create a role in the reserved
 * "pg_" namespace.
 */
if (IsReservedName(stmt->role))
    ereport(ERROR,
            (errcode(ERRCODE_RESERVED_NAME),
             errmsg("role name \"%s\" is reserved", stmt->role),
             errdetail("Role names starting with \"pg_\" are reserved.")));
```

— checked **before** the `pg_authid` duplicate-name lookup (`get_role_oid`),
so `CREATE ROLE pg_x` fails with `42939` even if `pg_x` already exists as a
predefined role. Before this loop goopg silently accepted `CREATE ROLE
pg_custom`, registering an arbitrary user-created role under a name PG
reserves for the system.

## Fix

`internal/server/role_ddl.go`'s `tryHandleRoleDDL` CREATE branch now calls
the existing `isReservedRoleName`/`reservedRoleNameErr` helpers (already
used by `renameRole`) immediately after extracting the name, before any
attribute parsing or registration — matching `CreateRole`'s check-before-
lookup ordering. No new helper needed; this loop only wires the existing
RENAME TO check into the CREATE path, per the sibling-path rule.

The reserved-role registry added in the predefined-role-resolution loop
(`catalog.InMemory.predefinedRoles`) already defended against the OID-
shadowing risk this gap posed (a `CREATE ROLE pg_database_owner` could never
have shadowed the real predefined OID even before this fix, since `RoleOID`
checks `predefinedRoles` first) — this loop closes the gap at its source
instead of only defending downstream of it.

## Tests

`internal/server/role_ddl_create_reserved_test.go` (new):
- `TestCreateRoleRejectsReservedPgPrefix` — `CREATE ROLE`/`USER`/`GROUP
  pg_custom` and a mixed-case `PG_CUSTOM` variant all raise `42939`
  (`sqlstate.ReservedName`) and leave no registry trace.
- `TestCreateRoleAllowsNonReservedName` — a plain `CREATE ROLE alice` is
  unaffected, registered in both the server role set and the catalog.

Gates: `go build ./...`/`go vet ./...` clean; `internal/server`+
`internal/catalog`+`internal/executor`+`internal/initdb` suites PASS;
`TestPort_PgDumpallRoleMembership`+`TestPort_PgDumpConnectionSetup` PASS (no
regression); `scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit
hook.

## Deferred

None new. PG's `errdetail` line (`"Role names starting with \"pg_\" are
reserved."`) is not ported — `reservedRoleNameErr` (shared with the RENAME
TO path) carries only the `errmsg`, an existing simplification already
accepted for that path; `roleError` has no detail field today. Extending it
is a small, generically-useful follow-up (would also improve the RENAME TO
error) but out of scope for this single-check wiring fix.
