# 0119-0004 — `pg_auth_members` multi-grantor rows (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/include/catalog/pg_auth_members.h`,
`postgres/src/backend/commands/user.c` (`AddRoleMems`, `DelRoleMems`,
`check_role_grantor`, `plan_single_revoke`)

## Problem

Every M0119-0004-ACLHEAP role-membership design doc since the original GRANT/
REVOKE ROLE slice carried the same forward note: goopg's `roleMembers`
registry (`internal/catalog/catalog.go`) was keyed by `(RoleOID, MemberOID)`
only, one row per membership regardless of who granted it. Real PG's
`pg_auth_members` unique index is the **triple** `(roleid, member, grantor)`
(`pg_auth_members_role_member_index`, `pg_auth_members.h`) — two different
admins can each independently `GRANT` the same role to the same member, and
each grant is its own row with its own `admin_option`/`inherit_option`/
`set_option`. Under the old single-row model, a second grantor's `GRANT`
silently overwrote the first grantor's row (`existing.GrantorOID =
grantorOid`) instead of creating an independent one — a real, demonstrable
divergence from PG: `REVOKE ... GRANTED BY <first grantor>` afterward would
incorrectly delete the membership entirely, when real PG leaves the second
grantor's row (and thus the membership) intact.

## Fix

### Catalog

`roleMembershipKey` (`internal/catalog/catalog.go`) gained a `GrantorOID`
field, making the map key the same triple as PG's unique index.

- `GrantRoleMembership` no longer treats a re-grant BY A DIFFERENT GRANTOR as
  an update to the existing row — since the grantor is now part of the key, a
  different grantor simply misses the lookup and mints a fresh, independent
  row (matching `AddRoleMems`' `SearchSysCache3(AUTHMEMROLEMEM, roleid,
  member, grantor)`). A re-grant BY THE SAME GRANTOR still upserts in place
  exactly as before.
- `RevokeRoleMembership` gained a `grantorOid` parameter and now only ever
  touches the ONE row identified by the full triple, leaving any other
  grantor's independent row on the same `(role, member)` pair untouched —
  mirrors `plan_single_revoke` operating on the specific tuple
  `check_role_grantor` resolved.
- `RevokeRoleMembershipCascadeSet` gained the same `grantorOid` parameter (to
  identify which specific row is the top of the cascade walk) and now returns
  `[]DependentRoleMembership{MemberOID, GrantorOID}` instead of a bare
  `[]uint32` — each dependent row found during the grantor-chain walk
  (`collectRoleMembershipCascadeKeysLocked`, unchanged) must be revoked at its
  OWN specific grantor, not an arbitrary row for that member. This also
  naturally implements `plan_recursive_revoke`'s "would the member still hold
  admin via ANOTHER untouched row" escape hatch, which was structurally
  unreachable under the single-row model (every design doc since the REVOKE
  CASCADE slice noted this explicitly) — a member can now genuinely hold
  independent admin-option rows from more than one grantor, and only the row
  implicated by the walk is torn down.
- `RoleMembershipEntries` sorts by `(RoleOID, MemberOID, GrantorOID)` — the
  same `(RoleOID, MemberOID)` pair may now legitimately appear more than once.
- `UnregisterRole` (DROP ROLE) now also purges any row where the dropped role
  is the **grantor** (`mk.GrantorOID == oid`), not just role/member — a
  pre-existing gap independent of multi-grantor support (even under the old
  single-row model a "pure grantor" role, neither the role nor the member of
  the row it created, could be dropped leaving a dangling `GrantorOID`), fixed
  incidentally while touching this sweep.

`pg_auth_members.VirtualRows` (unchanged) already iterated
`RoleMembershipEntries()` one row per entry, so it automatically renders
multiple grantor rows for the same `(roleid, member)` correctly.

### Executor

`execRoleMembershipChange` (`internal/executor/operators_ddl_role_membership.go`)
now resolves `grantorOid` (effective DDL-owner role, or an explicit `GRANTED
BY` override) ONCE, shared by both the GRANT and REVOKE branches — previously
only the GRANT branch resolved it at all; REVOKE silently ignored
`rc.GrantedBy` and touched whatever single row existed for `(role, member)`.
REVOKE now passes `grantorOid` through to `RevokeRoleMembershipCascadeSet`/
`RevokeRoleMembership`, and iterates cascade dependents by their own
`(MemberOID, GrantorOID)` pair rather than a bare member OID.

goopg has no full `check_role_grantor` (no per-session ADMIN-OPTION/
superuser/inherited-privilege inference) — reusing the DDL-owner resolution
GRANT already used is the same simplification this whole design-doc thread
has applied throughout (e.g. the grantor-cycle check gated on `grantorOid !=
BootstrapSuperuserOID`), correct for the common case (the granting/revoking
session actually holds the grant) that goopg's session model can represent
today.

### WAL / recovery

`EncodeRevokeRoleMembership`/`DecodeRevokeRoleMembership`
(`internal/wal/recovery.go`) gained a `grantorOid uint32` field (record
format: `kind(1) | roleOid(4) | memberOid(4) | grantorOid(4) |
revokeOption(1)`, 14 bytes, up from 10) so a specific grantor-scoped REVOKE
replays the correct row after a restart. `replayRoleMembershipRecords`
(`internal/initdb/role_membership_recovery.go`) threads the extra field
through unchanged otherwise.

## Tests

- `internal/catalog/role_membership_test.go`: `TestGrantRoleMembershipUpsertsInPlace`
  rewritten to test same-grantor in-place upsert vs. different-grantor
  independent-row minting; `TestGrantRoleMembershipInheritSetDefaults`'
  re-grant case switched to a same-grantor re-grant (was silently testing
  cross-grantor overwrite before); new
  `TestRevokeRoleMembershipTargetsOnlyItsOwnGrantorRow`; cascade tests updated
  for the `grantorOid` parameter and `DependentRoleMembership` return type.
- `internal/wal/role_membership_ddl_test.go`:
  `TestEncodeDecodeRevokeRoleMembershipRoundTrip`/
  `TestDecodeRevokeRoleMembershipRejectsTruncated` updated for the new field
  and 14-byte payload length.
- `internal/initdb/role_membership_recovery_test.go`: new
  `TestRoleMembershipRecoveryReplaysMultiGrantorRows` — two grantors' rows
  both survive a restart as independent entries, and a grantor-scoped REVOKE
  only removes its own row, confirmed after a fresh `Open`.

## Gates

`go build ./...`/`go vet ./...` clean; `internal/catalog`+`internal/executor`+
`internal/wal`+`internal/initdb`+`internal/parser`+`internal/server` suites
PASS; `scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

## Still open

- Real PG's grantor resolution for a bare (no `GRANTED BY`) REVOKE
  (`check_role_grantor`) additionally falls back to an inherited-privilege or
  superuser path when the current user does not directly hold `ADMIN OPTION`
  on the role — goopg has no per-session admin/inheritance model to drive
  that fallback, so it always uses the effective DDL-owner role. Unblocked by
  a future session-privilege-modeling milestone, not scoped here.
- `check_role_membership_authorization`'s permission check (only a role with
  `ADMIN OPTION` — or superuser for a superuser role — may GRANT/REVOKE a
  role) is still unimplemented; any DDL-owner role can currently perform any
  role-membership change. Unrelated to this slice.
