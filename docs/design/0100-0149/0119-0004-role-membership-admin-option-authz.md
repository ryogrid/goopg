# 0119-0004 — `check_role_membership_authorization` (ADMIN OPTION permission gate, M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`check_role_membership_authorization`),
`postgres/src/backend/utils/adt/acl.c` (`is_admin_of_role`, `roles_is_member_of`)

## Problem

Every M0119-0004-ACLHEAP role-membership design doc since the original GRANT/
REVOKE ROLE slice carried the same forward note: goopg's
`execRoleMembershipChange` (`internal/executor/operators_ddl_role_membership.go`)
applied any `GRANT <role> TO <member>` / `REVOKE <role> FROM <member>`
statement unconditionally — **any** DDL-owner role could grant or revoke
membership in **any** role, including making itself an admin of a role it had
no relationship to. Real PG gates every such statement through
`check_role_membership_authorization` (`user.c`), called once per target role
in the statement's role list, immediately after the role name resolves and
before `AddRoleMems`/`DelRoleMems` ever run:

1. `ROLE_PG_DATABASE_OWNER` can never be an explicit-membership target of a
   GRANT (`0A000`, "cannot have explicit members") — not reachable in goopg
   today (see "Still open").
2. If the target role is itself a superuser (`rolsuper`), only another
   superuser may grant or revoke membership in it (`42501`, regardless of
   ADMIN OPTION).
3. Otherwise, the invoking user must hold ADMIN OPTION on the target role —
   directly, or transitively through any membership chain — or be a
   superuser (`42501` if not, `is_admin_of_role` in `acl.c`).

## Fix

### Catalog primitives

Two new read-only queries on `*catalog.InMemory`
(`internal/catalog/catalog.go`), built entirely on state goopg already
tracks (`c.roleAttrs[name].Superuser`, `c.roleMembers`):

- `IsSuperuser(oid uint32) bool` — mirrors `superuser_arg`: the bootstrap
  superuser (`BootstrapSuperuserOID`, 10) is always superuser; otherwise
  resolves `oid` back to its registered name and checks
  `RoleAttrs.Superuser`.
- `IsAdminOfRole(memberOid, roleOid uint32) bool` — mirrors `is_admin_of_role`:
  a superuser `memberOid` is always admin of every role; by policy a role is
  never its own admin (`memberOid == roleOid` → `false`, an explicit
  carve-out — unlike `RoleIsMemberOf`, which treats self-membership as
  `true`); otherwise a breadth-first walk from `memberOid` over
  `c.roleMembers` (following every `key.MemberOID == cur` edge
  **unconditionally**, matching `roles_is_member_of`'s `ROLERECURSE_MEMBERS`
  mode, which ignores `inherit_option`/`set_option` while still recursing)
  returns `true` the moment it reaches a row `{RoleOID: roleOid, AdminOption:
  true}`. ADMIN OPTION is therefore inherited transitively through a chain
  even when an intermediate hop itself carries no ADMIN OPTION — only the
  *edge that matches the target* needs `AdminOption == true`, exactly as PG's
  traversal only tests `admin_option` against the `admin_of` role being
  searched for, not against every hop.

### Executor

`execRoleMembershipChange` now captures `currentUserID :=
o.currentDDLOwnerOID()` **before** computing `grantorOid` (which `GRANTED BY`
can redirect) — the two are the same value unless overridden, but represent
different PG concepts: `currentUserID` is `check_role_membership_authorization`'s
`currentUserId` (`GetUserId()`, the invoking session's own privileges), while
`grantorOid` is who gets recorded as grantor-of-record. New
`checkRoleMembershipAuthorization(im, currentUserID, roleOid, roleName,
isGrant) error` ports the three-branch PG check (superuser-role gate, then
`IsAdminOfRole`) and is called once per `roleOid` in the outer loop of BOTH
the GRANT and REVOKE branches, right after the role name resolves — matching
`user.c`'s call site, which runs before any grantee is even resolved (so an
unauthorized GRANT/REVOKE never touches an unrelated `role does not exist`
check on its grantee list first). Both error messages use PG's exact
`errmsg`/`errdetail` text, mapped onto `ExecError{Code: "42501", Message,
Detail}`.

## Tests

- `internal/catalog/role_membership_test.go`: new `TestIsSuperuser` (bootstrap
  OID 10, unknown OID, a plain role, a role with `RoleAttrs.Superuser` set);
  new `TestIsAdminOfRole` (superuser member, self-admin carve-out, unrelated
  roles, direct ADMIN OPTION grant, transitive inheritance through a
  non-admin hop, and a chain with no ADMIN OPTION anywhere).
- `internal/executor/operators_ddl_role_membership_test.go` (new file):
  `TestExecRoleMembershipChangeRequiresAdminOption` (non-admin rejected with
  `42501` and no state mutation, then succeeds once ADMIN OPTION is granted;
  bootstrap superuser always succeeds), `TestExecRoleMembershipChangeSuperuserRoleRequiresSuperuserGrantor`
  (ADMIN OPTION on a superuser-flagged role is not enough — only another
  superuser may grant/revoke it), `TestExecRoleMembershipChangeRevokeRequiresAdminOption`
  (mirrors the GRANT case for REVOKE).
- Live end-to-end `psql` smoke against a running goopg instance (built fresh,
  isolated port/data dir) confirmed: superuser GRANT succeeds; a non-admin
  `SET ROLE alice; GRANT grp TO newmember` fails with PG's exact
  `42501`/DETAIL text; the same statement succeeds once `alice` is granted
  ADMIN OPTION on `grp`; and `SET ROLE alice; GRANT super1 TO newmember`
  (`super1` is `SUPERUSER`) fails with the superuser-specific DETAIL text even
  though `alice` was separately made a (non-superuser) member of `super1`.

## Gates

`go build ./...`/`go vet ./...` clean; `internal/catalog`+`internal/executor`+
`internal/parser`+`internal/wal`+`internal/initdb`+`internal/server` suites
PASS; `TestPort_PgDumpallRoleMembership` PASS (unaffected — runs as the
bootstrap superuser); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pgbench smoke = pre-commit hook.

## Still open

- The `ROLE_PG_DATABASE_OWNER` "cannot have explicit members" carve-out is
  not ported: unreachable today anyway, since goopg's 16 PG predefined roles
  (`pg_database_owner`, `pg_read_all_data`, `pg_monitor`, …) are only
  heap-seeded at initdb (`internal/initdb/initdb.go`'s `pg_authid.dat`
  mirror) and never registered in the live `c.roles` name registry —
  `resolveRole("pg_database_owner")` already fails with `role does not exist`
  (`42704`) before this check would ever run. Registering the predefined
  roles into the live registry is a separate, differently-scoped capability
  (predefined-role name resolution generally, not this permission gate).
- `check_role_grantor`'s inherited-privilege/superuser fallback for a bare
  REVOKE's implicit grantor remains open (unchanged from the multi-grantor
  design doc) — goopg's session model has no separate "current user" from
  "effective DDL-owner role" to drive that fallback.
- `GRANT ... ON PARAMETER` GUC-name validation (no compiled-in parameter
  table) remains open, unrelated to this slice.
