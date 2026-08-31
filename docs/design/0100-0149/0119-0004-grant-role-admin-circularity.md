# 0119-0004 — GRANT ROLE grantor-chain circularity check (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`AddRoleMems`,
`initialize_revoke_actions`, `plan_member_revoke`, `plan_recursive_revoke`)

## Problem

`0119-0004-revoke-role-option-for.md` and `0119-0004-grant-on-parameter-pgdumpall.md`
each carried forward the same open item: "the grantor-chain circularity check
(PG's second `AddRoleMems` guard, `user.c` ~1751, 'ADMIN option cannot be
granted back to your own grantor') is still not implemented." Before this
slice, `execRoleMembershipChange`'s only cycle guard was `RoleIsMemberOf`
(role-member loop: A member of B, B member of A). PG's `AddRoleMems` has a
*second*, independent guard for a WITH ADMIN TRUE grant: it simulates
cascading-revoking every existing `pg_auth_members` row for the target role
that the new grantee set implicates, then checks whether the grantor's own
admin-option row survives untouched. If it wouldn't, the grant chain would
become non-acyclic — `REVOKE .. CASCADE` could no longer unwind it cleanly —
so PG rejects it with `ERRCODE_INVALID_GRANT_OPERATION` ("ADMIN option cannot
be granted back to your own grantor"). Without this, goopg silently allowed
grant chains real PG rejects.

## Fix

### Catalog

New `catalog.InMemory.GrantRoleWouldCreateGrantorCycle(roleOid uint32,
newMemberOids []uint32, grantorOid uint32) bool`
(`internal/catalog/catalog.go`) is a direct port of `AddRoleMems`' guard,
scoped to the `pg_auth_members` rows for a single `roleOid` (matching
`SearchSysCacheList1(AUTHMEMROLEMEM, roleid)`'s scope — the guard never needs
rows for any other role):

1. If any new member is `BootstrapSuperuserOID` (10, exported alongside the
   function — PG's `BOOTSTRAP_SUPERUSERID` — since it needs no source
   grantor, granting it ADMIN OPTION is always rejected outright.
2. Simulate `plan_member_revoke`/`plan_recursive_revoke`: for each new
   grantee, walk (and mark "deleted") its own membership-in-`roleOid` row (if
   any), then cascade into every row *granted by* that member, deleting those
   too, unless the cascaded-from member would still retain admin option via
   an untouched row.
3. After the simulation, if `grantorOid` still has an untouched,
   `admin_option=true` row in the (unmodified) rows, the grant may proceed;
   otherwise it is circular.

goopg's `roleMembers` map is keyed by `(RoleOID, MemberOID)` only — one row
per pair, unlike real PG's `(roleid, member, grantor)` composite key which
allows *multiple* grantors per membership. This means the "would still have
admin option via *another* untouched grant" branch can never observe a second
surviving grantor for the same member in goopg's model — every row that gets
touched is the member's *only* row. This is a narrower approximation of PG's
real multi-grantor model (see Deferred).

### Executor

`internal/executor/operators_ddl_role_membership.go`'s
`execRoleMembershipChange` GRANT branch now: (1) runs the existing per-member
`RoleIsMemberOf` role-member-loop sanity check for the whole grantee list
first (unchanged, mirrors PG's `forboth` sanity loop), collecting resolved
member OIDs; (2) if `rc.AdminOption` is explicitly `true` and the grantor is
not the bootstrap superuser, runs `GrantRoleWouldCreateGrantorCycle` **once**
for the whole `rc.Grantees` batch (matching `AddRoleMems`' single whole-batch
call, not a per-member call — the simulated cascade needs the full new-member
set at once to be correct); (3) only then applies each grant. Matches PG's
three-phase ordering (sanity checks → whole-batch admin check → catalog
updates) exactly.

## Verification

- `TestGrantRoleWouldCreateGrantorCycle` (`internal/catalog/
  role_membership_test.go`): a one-hop grant-back (target has no existing
  membership-in-role row) is *not* circular; extending it into a two-hop
  chain (target now also holds a membership row granted by a third party)
  makes the same grant-back circular, since revoking the target's row now
  cascades to revoke the grantor's own row.
- `TestGrantRoleWouldCreateGrantorCycleRetainsUntouchedAdmin`: granting to an
  unrelated third role never implicates the grantor's own untouched row.
- `TestGrantRoleWouldCreateGrantorCycleRejectsBootstrapSuperuserGrantee`:
  granting ADMIN OPTION to the bootstrap superuser is always rejected.
- Live end-to-end smoke against a running goopg instance (`psql`):
  `GRANT rolex TO b WITH ADMIN OPTION` (by bootstrap superuser) → `SET
  SESSION AUTHORIZATION b` → `GRANT rolex TO a WITH ADMIN OPTION` succeeds
  (1-hop, no chain) → `SET SESSION AUTHORIZATION a` → `GRANT rolex TO c WITH
  ADMIN OPTION` succeeds → `SET SESSION AUTHORIZATION c` → `GRANT rolex TO a
  WITH ADMIN OPTION` fails with `ERROR: ADMIN option cannot be granted back
  to your own grantor` (the chained cycle case), matching real PG 18.3's
  documented behavior for this guard. A plain (non-`WITH ADMIN`) grant and an
  admin grant to an unrelated role are both confirmed unaffected.

Gates: `go build ./...`/`go vet` clean; `internal/catalog`+`internal/executor`+
`internal/parser`+`internal/server`+`internal/wal`+`internal/initdb` suites
PASS; `scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

## Deferred (ledger row appended)

- **Multiple grantors per `(roleid, member)`**: goopg's `roleMembers` map
  collapses to one row per `(RoleOID, MemberOID)` pair, unlike real PG's
  `(roleid, member, grantor)` composite key. This means the circularity
  check's "would still have admin via *another* grantor's row" escape hatch
  can never observe a legitimate second grantor for the same membership —
  goopg may reject a re-grant real PG would allow (because PG's finer-grained
  model has a second surviving row goopg cannot represent). Net-new,
  differently-scoped capability: reworking `roleMembershipKey` to include
  `GrantorOID` and updating every reader (`pg_auth_members.VirtualRows`,
  `RoleMembershipEntries`, dump/restore, recovery) — out of scope for this
  slice.
- **`GRANT ... ON PARAMETER`** `pg_parameter_acl` GUC-name validation
  (unrelated to this slice) remains open, unchanged from prior docs.
- **REVOKE's recursive/cascade dependent-privilege walk**
  (`plan_recursive_revoke`, triggered by a full revoke or `ADMIN OPTION FOR`)
  remains unimplemented — `CASCADE`/`RESTRICT` are parsed and trimmed but
  never read by the executor. Unchanged from prior docs.
