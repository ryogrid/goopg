# 0119-0004 — `GRANT`/`REVOKE` role membership (`pg_auth_members`) round-trip in pg_dumpall (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`AddRoleMems`/`DelRoleMems`,
`roleSpecsToIds`); `postgres/src/backend/utils/adt/acl.c`
(`get_rolespec_oid`); `postgres/src/bin/pg_dump/pg_dumpall.c`
(`dumpRoleMembership`); `postgres/src/include/catalog/pg_auth_members.h`

## Problem

`0119-0004-pgdumpall-globals-only.md` (loop #18) registered `pg_auth_members`
as a virtual catalog so `pg_dumpall --globals-only` stopped erroring with
"relation \"pg_authid\" does not exist", but left it correctly-empty: goopg's
parser had **no grammar at all** for `GRANT <role> TO <role>`/`REVOKE <role>
FROM <role>` (role membership — the PG concept behind role-based privilege
inheritance, e.g. `GRANT admin TO alice`). This is architecturally distinct
from every object-privilege `GRANT ... ON <object> TO <role>` this codebase
already models: it has no `ON <object>` clause at all, and a real client
issuing it against goopg was silently swallowed by the server's own
virtual-ACL fast path (`query.go`'s `tryRecordTableGrant`, which requires
`" on "` and bails immediately without it) — the statement reported success
but changed nothing, and `pg_dumpall`'s "Role memberships" section was always
empty.

## Fix

### Parser: absence of `ON` is the discriminator

`internal/parser/parser.go`'s `case "grant", "revoke":` (the shared
token-scanning loop that already builds `TypeACLChange`/`DatabaseACLChange`/
`AttrACLChange` for the heap-backed ACL variants) now tracks `sawOn bool` —
true the moment ANY `on` token appears anywhere in the statement. Every
privilege-GRANT variant requires an `ON <object>` clause; role membership
never has one. When the loop finishes with `sawOn == false`, the new
`buildRoleMembershipChange(revoke, toks)` (mirrors `buildDatabaseACLChange`'s
token-index-scanning shape) parses:

- `GRANT <role>[, ...] TO <role>[, ...] [WITH ADMIN OPTION] [GRANTED BY
  <role>]`
- `REVOKE [ADMIN OPTION FOR] <role>[, ...] FROM <role>[, ...] [GRANTED BY
  <role>] [CASCADE|RESTRICT]`

into a new `parser.RoleMembershipChange` (`Revoke`, `AdminOptionOnly`
— REVOKE's `ADMIN OPTION FOR` prefix, which only clears the flag instead of
deleting the row —, `WithAdminOption`, `Roles`, `Grantees`, `GrantedBy`),
attached to `CompatNoopStmt.RoleMembership`. An unparseable form (rare
malformed input) returns `nil`, falling back to the pre-existing no-op.

### Server: exclude role membership from the virtual-ACL fast path

`internal/server/query.go`'s single-statement autocommit GRANT/REVOKE fast
path (`isHeapACLObject`, already excluding `ON TYPE`/`ON DOMAIN`/`ON
DATABASE`/column-level grants so they reach the executor) now also excludes
any statement with no `" ON "` substring at all — the same discriminator the
parser uses. Such statements fall through to
`dispatchSimpleQueryViaExecutor`, where the parser's `RoleMembershipChange`
is available. The extended (prepared-statement) protocol never had this
server-level fast path to begin with, so it required no change.

### Executor: OID-keyed `roleMembers` registry, no heap row

Unlike every other `CompatNoopStmt` ACL variant `execCompatNoop`
(`internal/executor/operators_ddl.go`) dispatches, role membership has no
object to re-sync a heap row for — `pg_auth_members` is virtual, sourced
entirely from a new registry. `execCompatNoop` gained a
`s.RoleMembership != nil` branch calling the new
`(*ddlOp).execRoleMembershipChange`
(`internal/executor/operators_ddl_role_membership.go`), which:

1. Resolves every role/grantee name to its stable catalog OID via
   `catalog.InMemory.RoleOID`. An unresolvable name — including `PUBLIC`,
   which is never registered as a role — raises `42704 "role ... does not
   exist"`, matching `get_rolespec_oid`'s `ROLESPEC_PUBLIC` case exactly (PG
   rejects `GRANT role TO PUBLIC` with the identical error, so no
   PUBLIC-specific check was needed).
2. For GRANT: resolves the grantor (`GRANTED BY <role>`, or the session's
   current effective role via the pre-existing `currentDDLOwnerOID()` helper
   — `NonSuperuserRole` if `SET ROLE`/`SET SESSION AUTHORIZATION` is active,
   else the bootstrap superuser). Rejects a membership that would create a
   cycle — including the trivial self-grant — via the new
   `catalog.InMemory.RoleIsMemberOf(memberOid, roleOid)` traversal, mirroring
   `is_member_of_role_nosuper`'s check in `AddRoleMems`
   (`ERRCODE_INVALID_GRANT_OPERATION`, `0LP01`, message `role "X" is a member
   of role "Y"`). Then calls `catalog.InMemory.GrantRoleMembership`.
3. For REVOKE: calls `catalog.InMemory.RevokeRoleMembership`, which either
   clears just `AdminOption` (`ADMIN OPTION FOR`) or deletes the row.
   Revoking a non-existent membership is a silent no-op, matching this
   codebase's other ACL REVOKE paths (`RevokeTablePrivilege` et al.).

New `catalog.InMemory.roleMembers map[roleMembershipKey]*RoleMembership`
(`roleMembershipKey{RoleOID, MemberOID}`) mirrors `roleSettings`'s
established shape. `GrantRoleMembership` upserts in place: a re-grant keeps
the row's OID stable and always updates the grantor. `RoleMembershipEntries()`
returns a sorted snapshot for the `pg_auth_members.VirtualRows` projection.
`UnregisterRole` (DROP ROLE) now also purges any membership row referencing
the dropped role's OID on either side, mirroring PG's automatic
membership-removal cascade on role drop.

**Superseded 2026-07-03** (`0119-0004-grant-role-inherit-set.md`): the
original `AdminOption`-only upsert (an unconditional "OR" that could only
ever upgrade, never downgrade, and left `inherit_option`/`set_option`
hardcoded `t`/`f`) was replaced by a tri-state `admin, inherit, set *bool`
upsert matching PG's `GRANT_ROLE_SPECIFIED_*` bitmask semantics exactly —
see that doc for the corrected behavior and the `set_option` default bug it
also fixed.

### WAL / restart persistence

Two new WAL kinds mirror the `ALTER ROLE ... SET` restart-persistence
pattern (`RecordKindAlterRoleSetConfig` et al.): `RecordKindGrantRoleMembership`
(79, `kind | roleOid | memberOid | grantorOid | adminOption`) and
`RecordKindRevokeRoleMembership` (80, `kind | roleOid | memberOid |
adminOptionOnly`). Physical WAL replay ignores both (no on-disk page data);
`internal/initdb/role_membership_recovery.go` walks the WAL once after
physical replay and re-applies each record via `GrantRoleMembership`/
`RevokeRoleMembership`. Unlike the `roleSettings` sibling, this pass is
deliberately positioned in `open.go` **after**
`LoadRolesFromAuthidHeap`/`replayRoleDDLRecords`, not alongside
`replayRoleConfigRecords`: those calls load role OIDs preserved from before
the crash (`RegisterRoleWithOID`) and can advance the catalog's `nextOID`
counter well past its pre-replay value, and `GrantRoleMembership` always
mints a **fresh** OID per row via `AllocOID` (`pg_auth_members.oid` is never
dumped by `pg_dump`/`pg_dumpall`, so cross-restart OID stability is not
required) — running this pass first would risk a numeric collision with a
role OID loaded afterward.

## Verification

- `TestParseGrantRoleMembership`/`TestParseGrantOnObjectLeavesRoleMembershipNil`
  (`internal/parser/op_grant_rolemembership_test.go`): all `buildRoleMembershipChange`
  shapes (GRANT/REVOKE, `WITH ADMIN OPTION`, `ADMIN OPTION FOR`, `GRANTED
  BY`, multi-role/multi-grantee lists, `CASCADE`) plus the negative case
  (every `ON <object>` GRANT variant leaves `RoleMembership` nil).
- `TestGrantRoleMembershipUpsertsInPlace`/`TestRevokeRoleMembership`/
  `TestRoleIsMemberOfDetectsSelfAndTransitiveCycles`/
  `TestRoleMembershipEntriesDeterministicOrder`/
  `TestUnregisterRoleDropsMembershipRows`
  (`internal/catalog/role_membership_test.go`): registry upsert/downgrade
  guard, both REVOKE forms, the self+transitive circularity traversal, sort
  order, and the DROP ROLE cascade.
- `TestEncodeDecodeGrantRoleMembershipRoundTrip`/
  `TestEncodeDecodeRevokeRoleMembershipRoundTrip` + truncated/wrong-kind
  guards (`internal/wal/role_membership_ddl_test.go`).
- `TestRoleMembershipRecoveryReplaysGrant`/
  `TestRoleMembershipRecoveryReplaysGrantThenRevoke`/
  `TestReplayRoleMembershipRecordsHandlesMissingWalDir`
  (`internal/initdb/role_membership_recovery_test.go`): full `Init`→`Open`→
  crash→`Open` restart cycle.
- `TestPort_PgDumpallRoleMembership` (`internal/testport/
  pgdumpall_role_membership_test.go`): byte-identical vs real `pg_dumpall`
  18.3 — `GRANT admin TO alice WITH ADMIN OPTION` and a plain `GRANT admin TO
  bob`, both rendered with the exact `WITH ADMIN OPTION, INHERIT TRUE, SET
  FALSE GRANTED BY postgres` clause real `pg_dumpall` emits for a
  bootstrap-superuser-granted, non-inherit-tracked membership on a
  `server_version >= 160000` connection (goopg reports 18.3); a
  subsequently-`REVOKE`d membership correctly does not appear.

Gates: `go build ./...`/`go vet` clean; `internal/parser`+`internal/catalog`+
`internal/wal`+`internal/initdb`+`internal/executor`+`internal/server` suites
PASS; `TestPort_PgDumpallRoleMembership`/`TestPort_PgDumpallGlobalsOnly`/
`TestPort_PgDumpRoleConfigSet`/`TestPort_PgDumpDatabaseConfigSet` PASS;
`scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

## Deferred (ledger row appended)

- ~~**`WITH INHERIT {TRUE|FALSE}`/`WITH SET {TRUE|FALSE}` clauses are not
  parsed.**~~ RESOLVED 2026-07-03, see `0119-0004-grant-role-inherit-set.md`
  (which also fixed a wrong `set_option` default this doc's implementation
  had introduced).
- **The grantor-chain (member-grantor loop) circularity check is not
  implemented** — PG's second `AddRoleMems` check (`user.c` lines
  1751–1800-ish, "ADMIN option cannot be granted back to your own grantor")
  guards against `A` granting ADMIN OPTION on `X` to `B`, then `B` (with no
  other ADMIN OPTION source on `X`) granting it back to `A`. Only the
  simpler role-member-loop check (`RoleIsMemberOf`, self-grant + transitive
  membership cycles) is implemented.
- **`GRANT ... ON PARAMETER` (GUC-level ACLs, `pg_parameter_acl`) remains
  unimplemented** — unchanged from the loop #18 ledger row, a structurally
  separate capability from role membership.
- **A dropped role's `roleSettings`/`dbRoleSettings` entries are not
  purged** on `UnregisterRole`, unlike the `roleMembers` cleanup this slice
  added — a pre-existing gap in the `ALTER ROLE ... SET` sibling feature,
  not introduced here, but sharing the same "DROP ROLE should cascade"
  concern.
- Standing M0119-0004-ACLHEAP items (extended-protocol commit-time-deferral
  entanglement, multi-database scope) unchanged.
