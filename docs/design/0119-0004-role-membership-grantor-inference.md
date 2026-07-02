# 0119-0004 — `check_role_grantor` implicit/explicit grantor inference (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`check_role_grantor`),
`postgres/src/backend/utils/adt/acl.c` (`select_best_admin`, `has_privs_of_role`)

## Problem

Every M0119-0004-ACLHEAP role-membership design doc since the original
GRANT/REVOKE ROLE slice carried the same forward note: goopg's
`execRoleMembershipChange` (`internal/executor/operators_ddl_role_membership.go`)
always recorded the grantor-of-record as `o.currentDDLOwnerOID()` (an explicit
`GRANTED BY <role>` aside), with no attempt to reproduce PG's
`check_role_grantor` (`user.c`), which the real `AddRoleMems`/`DelRoleMems`
call as the very first step of granting/revoking a role's membership:

- **No `GRANTED BY`**: if the current user is a superuser, the grantor is
  always the bootstrap superuser (`BOOTSTRAP_SUPERUSERID`) — grants recorded
  this way never depend on any other existing grant row. Otherwise, the
  grantor must be inferred: either the current user itself (if it directly
  holds ADMIN OPTION on the target role), or — if the current user only
  *inherits* that ADMIN OPTION from an ancestor role via an INHERIT-marked
  membership chain — the closest such ancestor (`select_best_admin`, fewest
  hops, preferring a direct hit).
- **Explicit `GRANTED BY <role>`**: the named role must be one whose
  privileges the current user possesses (`has_privs_of_role` — otherwise this
  is impersonation and is rejected with `42501`, for both GRANT and REVOKE).
  For GRANT specifically (not REVOKE — "no matching grant should exist
  anyway, but if it somehow does, let the user get rid of it"), the named
  grantor must ALSO directly hold ADMIN OPTION on the target role itself —
  not merely inherit it from a further ancestor — unless it is the bootstrap
  superuser, which is exempt.

Because `check_role_grantor` takes the *target role* as an argument,
resolution happens per-role, not once for the whole statement — but an
explicit `GRANTED BY` name is resolved to an OID exactly once, up front
(`roleSpecsToIds`/`get_rolespec_oid(missing_ok=false)` in
`ExecGrantRoleStmt`), shared across every role name in the statement's role
list.

Two smaller bugs rode along with this gap: goopg silently discarded a
`GRANTED BY` name that failed to resolve (falling back to
`currentDDLOwnerOID()`) instead of raising `42704` ("role does not exist"),
and `grantorOid` was computed once outside the per-target-role loop even
though real `check_role_grantor` depends on the target role.

## Fix

### Catalog primitives

Two new read-only queries on `*catalog.InMemory`
(`internal/catalog/catalog.go`), following `IsAdminOfRole`'s BFS shape but
gated on `InheritOption` (PG's `ROLERECURSE_PRIVS`, vs. `IsAdminOfRole`'s
unconditional `ROLERECURSE_MEMBERS`):

- `HasPrivsOfRole(memberOid, roleOid uint32) bool` — mirrors
  `has_privs_of_role`: `memberOid == roleOid`, a superuser `memberOid`, or
  `roleOid` reachable from `memberOid` via a chain of `InheritOption == true`
  `pg_auth_members` rows.
- `SelectBestAdmin(memberOid, roleOid uint32) uint32` — mirrors
  `select_best_admin`: breadth-first walk from `memberOid`, following only
  `InheritOption == true` edges, returning the first node (closest to
  `memberOid`, preferring `memberOid` itself) found holding a direct
  `AdminOption == true` row on `roleOid`. Returns 0 (PG's `InvalidOid`) if
  none exists — by policy `memberOid == roleOid` never qualifies (a role
  cannot have ADMIN OPTION on itself).

### Executor

New `checkRoleGrantor(im, currentUserID, roleOid, roleName, explicitGrantorOid,
haveExplicitGrantor, isGrant) (uint32, error)`
(`internal/executor/operators_ddl_role_membership.go`) ports `check_role_grantor`
exactly, called once per target `roleOid` in both the GRANT and REVOKE
branches of `execRoleMembershipChange`, immediately after
`checkRoleMembershipAuthorization` (matching `user.c`'s ordering:
`check_role_membership_authorization` runs in `ExecGrantRoleStmt` before
`AddRoleMems`/`DelRoleMems`, and `check_role_grantor` runs as the first thing
inside those):

- No explicit grantor: superuser current user → `BootstrapSuperuserOID`;
  otherwise `SelectBestAdmin(currentUserID, roleOid)`, falling back
  defensively to `currentUserID` if that returns 0 (real PG's
  `elog(ERROR, "no possible grantors")` — an internal-error assumption that
  authorization already ruled out the "impossible" case; goopg degrades
  gracefully instead of panicking, since a mixed inheritable/non-inheritable
  admin chain can theoretically still trigger it here).
- Explicit grantor: `HasPrivsOfRole(currentUserID, explicitGrantorOid)` gates
  impersonation (`42501`, message text differs for GRANT vs REVOKE, matching
  PG exactly); for GRANT only, `SelectBestAdmin(explicitGrantorOid, roleOid)
  != explicitGrantorOid` (i.e. the grantor's admin option must be its own,
  not inherited) is rejected unless `explicitGrantorOid ==
  BootstrapSuperuserOID`.

`explicitGrantorOid` is now resolved exactly once, up front (mirroring
`roleSpecsToIds`), and an unresolvable `GRANTED BY` name is a hard `42704`
error rather than a silent fall-back — the pre-existing bug fixed as part of
this slice. `grantorOid` moved from a single statement-wide variable to a
per-`roleOid` loop variable in the GRANT branch (the REVOKE branch already
looped per-role for other reasons), since `check_role_grantor` genuinely
depends on the target role.

## Tests

- `internal/catalog/role_membership_test.go`: new `TestHasPrivsOfRole` (self,
  superuser bypass, direct/transitive INHERIT chain, a non-inheritable edge
  blocking the walk) and `TestSelectBestAdmin` (self never qualifies, no
  grant → 0, direct ADMIN OPTION → self, transitive ADMIN OPTION via an
  INHERIT chain → the closest holder, a non-inheritable edge blocking the
  walk).
- `internal/executor/operators_ddl_role_membership_test.go`: new
  `TestExecRoleMembershipChangeInfersGrantorViaInheritedAdmin` (a non-admin
  current user who only inherits ADMIN OPTION via an intermediate role is
  recorded as having granted through that role, not itself),
  `TestExecRoleMembershipChangeGrantedByRequiresPrivsOfGrantor` (impersonation
  rejected with `42501` until the current user gains privileges of the named
  grantor, then the grant succeeds recording that grantor),
  `TestExecRoleMembershipChangeGrantedByRequiresDirectAdminOption` (an
  explicit grantor that only inherits ADMIN OPTION from a further ancestor is
  rejected for GRANT), `TestExecRoleMembershipChangeUnresolvableGrantedByErrors`
  (an unknown `GRANTED BY` name is `42704`, not a silent fall-back).

## Gates

`go build ./...`/`go vet ./...` clean; `internal/catalog`+`internal/executor`+
`internal/parser`+`internal/wal`+`internal/initdb`+`internal/server` suites
PASS; `TestPort_PgDumpallRoleMembership` PASS (unaffected — runs as the
bootstrap superuser, whose implicit-grantor path is unchanged);
`scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

## Still open

- The `ROLE_PG_DATABASE_OWNER` "cannot have explicit members" carve-out
  remains unported (unreachable today — unchanged from the prior doc).
- `GRANT ... ON PARAMETER` GUC-name validation (no compiled-in parameter
  table) remains open, unrelated to this slice.
- The defensive `SelectBestAdmin == 0` fallback (return `currentUserID`
  rather than PG's internal-error `elog`) is a deliberate divergence: goopg
  has no scenario today that can construct a chain where
  `IsAdminOfRole`(`ROLERECURSE_MEMBERS`, ignores INHERIT) succeeds but
  `SelectBestAdmin`(`ROLERECURSE_PRIVS`, requires INHERIT) finds nothing,
  since ADMIN OPTION grants are typically also INHERIT — but it is
  theoretically reachable via `WITH INHERIT FALSE` on an admin-bearing row,
  and would silently misattribute the grantor rather than erroring like PG
  does.
