# 0119-0004 — REVOKE ROLE CASCADE/RESTRICT dependent-privilege walk (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`DelRoleMems`,
`initialize_revoke_actions`, `plan_single_revoke`, `plan_recursive_revoke`)

## Problem

Every M0119-0004-ACLHEAP role-membership row since the original slice carried
the same open item forward: `CASCADE`/`RESTRICT` on `REVOKE role FROM member`
were parsed and trimmed from the token scan but never read by the executor —
a REVOKE that other role memberships transitively depend on was applied as an
unconditional single-row operation regardless of which keyword (or neither)
was given. Real PG's `plan_recursive_revoke` walks the grantor chain: revoking
a role membership that carried `ADMIN OPTION` also invalidates any further
grants the revoked member made *using* that admin option — PG refuses the
revoke (`ERRCODE_DEPENDENT_OBJECTS_STILL_EXIST`, "dependent privileges exist",
hint "Use CASCADE to revoke them too.") unless `CASCADE` is given, in which
case it recursively revokes the whole dependent chain too. `RESTRICT` and the
unwritten default behave identically (`DROP_RESTRICT` is PG's default
`opt_drop_behavior`).

## Fix

### Parser

`RoleMembershipChange` (`internal/parser/ast.go`) gained a `Cascade bool`
field. `buildRoleMembershipChange` (`internal/parser/parser.go`) now records
which of `CASCADE`/`RESTRICT` was seen (`RESTRICT` and absence both leave it
`false`) instead of only trimming the token. Fixed a latent ordering bug while
doing this: the `GRANTED BY <role>` case previously `break`-terminated the
option scan unconditionally, so `REVOKE ... GRANTED BY x CASCADE` would never
reach the `CASCADE` token (`opt_granted_by` precedes `opt_drop_behavior` in
`RevokeRoleStmt`, gram.y) — it now `continue`s past the consumed `BY <role>`
pair, mirroring the `WITH ...` case's existing "resume scanning" comment.

### Catalog

New `catalog.InMemory.RevokeRoleMembershipCascadeSet(roleOid, memberOid
uint32, cascade bool) (dependentMembers []uint32, blocked bool)`
(`internal/catalog/catalog.go`) is a **read-only** simulation mirroring
`plan_recursive_revoke`'s grantor-chain walk, scoped to `roleOid`'s rows only
(matching `SearchSysCacheList1(AUTHMEMROLEMEM, roleid)`'s scope, same as the
sibling `GrantRoleWouldCreateGrantorCycle` check from the prior slice):

1. If `memberOid`'s own row doesn't exist or its `AdminOption` is already
   `false`, there is nothing to cascade — a member with no ADMIN OPTION on
   `roleOid` could not have re-granted it to anyone (PG's early return in
   `plan_recursive_revoke` when the revoked row's `admin_option` is already
   false). Returns `(nil, false)`.
2. Otherwise, `collectRoleMembershipCascadeKeysLocked` walks every row
   `granted BY memberOid` (its dependents), and recurses past a dependent only
   if *that* dependent row's own `AdminOption` is `true` — mirroring
   `plan_recursive_revoke`'s per-row full-delete + conditional recursion. Every
   row reached this way is a `dependentMembers` entry regardless of its own
   `AdminOption` (PG always fully deletes a cascaded row, never just clears one
   flag on it — `plan_recursive_revoke`'s recursive call always passes
   `revoke_admin_option_only=false`).
3. If dependents exist and `cascade` is `false`, `blocked=true` and
   `dependentMembers=nil` — the caller must raise the error and apply nothing.
   If `cascade` is `true`, `dependentMembers` lists every member that must
   *also* be revoked of `roleOid`.

goopg's `roleMembers` map is keyed `(RoleOID, MemberOID)` only (single
grantor per pair — see the prior slice's deferred multi-grantor note), so
`plan_recursive_revoke`'s "would the member still have admin via *another*
untouched row" escape hatch can never apply here: every row this walk touches
is that member's only row for `roleOid`.

### Executor

`execRoleMembershipChange`'s REVOKE branch (`internal/executor/
operators_ddl_role_membership.go`) now, for each grantee, before applying the
per-member `RevokeRoleMembership` call: if `rc.RevokeOption` is `""` (whole
row) or `"admin"` (`ADMIN OPTION FOR`) — the only two forms `plan_single_revoke`
ever routes into recursion, `INHERIT`/`SET OPTION FOR` never cascade — calls
`RevokeRoleMembershipCascadeSet(roleOid, memberOid, rc.Cascade)`. A `blocked`
result returns `ERRCODE_DEPENDENT_OBJECTS_STILL_EXIST` (`2BP01`) with PG's
exact message/hint and aborts before touching any row. Otherwise, each
returned dependent member is fully revoked (`RevokeRoleMembership(roleOid,
dep, "")`, WAL-logged) *before* the original grantee's own revoke is applied —
matching PG's own catalog-update ordering is not load-bearing here (goopg's
`roleMembers` map has no visibility ordering dependency), but applying
dependents first keeps a partially-applied cascade consistent with "the whole
chain was removed" if a later step in the same statement were to fail (it
cannot currently, but this ordering is the safer default). No new WAL record
kind was needed — cascaded rows reuse the existing `EncodeRevokeRoleMembership`
wire format with `revokeOption=""`, so WAL replay/recovery requires no changes
at all (recovery replays each row's delete independently, exactly as if the
original statement had been N separate plain `REVOKE`s).

## Verification

- `TestParseGrantRoleMembership` (`internal/parser/
  op_grant_rolemembership_test.go`): new `REVOKE ... CASCADE`, `REVOKE ...
  RESTRICT`, and `REVOKE ADMIN OPTION FOR ... GRANTED BY ... CASCADE` cases
  (the last one exercising the `GRANTED BY` + `CASCADE` ordering fix).
- `TestRevokeRoleMembershipCascadeSetNoAdminOptionNeverCascades`,
  `TestRevokeRoleMembershipCascadeSetBlocksWithoutCascade`,
  `TestRevokeRoleMembershipCascadeSetWalksTransitiveChain`
  (`internal/catalog/role_membership_test.go`): no-admin-option early return;
  RESTRICT blocks with dependents untouched; CASCADE walks a 3-hop transitive
  chain, correctly stopping past a non-admin dependent and ignoring an
  unrelated sibling grant.
- Live end-to-end `psql` smoke against a running goopg instance: `GRANT r_e TO
  r_f WITH ADMIN OPTION` (by bootstrap superuser) → `SET SESSION AUTHORIZATION
  r_f` → `GRANT r_e TO r_g` → `RESET SESSION AUTHORIZATION` → plain `REVOKE
  r_e FROM r_f` fails with `ERROR: dependent privileges exist` / `HINT: Use
  CASCADE to revoke them too.`, both rows (`r_f`'s and `r_g`'s) left intact →
  `REVOKE r_e FROM r_f CASCADE` succeeds, deletes both rows. Separately
  confirmed a plain revoke with no dependents still deletes normally, and
  `REVOKE ADMIN OPTION FOR ... FROM ...` with no dependents clears only the
  flag and leaves the row (pre-existing behavior, unaffected by this change).

Gates: `go build ./...`/`go vet` clean; `internal/catalog`+`internal/executor`+
`internal/parser`+`internal/server`+`internal/wal`+`internal/initdb` suites
PASS; `scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

## Deferred (ledger row appended)

- **Multiple grantors per `(roleid, member)`** (unrelated to this slice,
  carried from the prior grantor-circularity slice): unchanged, still open.
- **`GRANT ... ON PARAMETER`** `pg_parameter_acl` GUC-name validation
  (unrelated to this slice): unchanged, still open.
- **CASCADE on `INHERIT`/`SET OPTION FOR`**: correctly a no-op per PG's own
  semantics (`plan_single_revoke` never recurses for these two forms) — not a
  gap, documented here to make the scoping explicit.
