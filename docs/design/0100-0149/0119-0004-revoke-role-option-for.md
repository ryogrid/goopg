# 0119-0004 — `REVOKE { ADMIN | INHERIT | SET } OPTION FOR` generalization (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`GrantRole`, `DelRoleMems`,
`plan_single_revoke`); `postgres/src/backend/parser/gram.y` (`RevokeRoleStmt`)

## Problem

`0119-0004-grant-role-inherit-set.md` (loop earlier today) landed GRANT's
full `WITH { ADMIN | INHERIT | SET } { OPTION | TRUE | FALSE }` option list
but left its own "Deferred" section noting the REVOKE-side counterpart was
still unimplemented: `buildRoleMembershipChange`'s option-for prefix check
only recognized the literal three-token run `ADMIN OPTION FOR`, even though
PG's `RevokeRoleStmt` grammar accepts any `ColId` there:

```
REVOKE [ { ADMIN | INHERIT | SET } OPTION FOR ] role_name [, ...]
    FROM role_specification [, ...] [ GRANTED BY role_specification ]
    [ CASCADE | RESTRICT ]
```

`REVOKE INHERIT OPTION FOR admin FROM alice` or `REVOKE SET OPTION FOR admin
FROM alice` therefore fell through unrecognized: the leading `INHERIT`/`SET`
token was swallowed into the role list via `splitTokRoles`, corrupting the
parse, and even had it been recognized, `catalog.InMemory.RevokeRoleMembership`
had no parameter to say *which* option to clear — only a `bool
adminOptionOnly` toggle between "clear admin_option" and "delete the row".

PG's own `plan_single_revoke` (`user.c`) confirms `INHERIT`/`SET` OPTION FOR
behave exactly like `ADMIN OPTION FOR` at the storage layer: the named flag
is cleared and the row survives, with no cascade to dependent grants (only
revoking the grant entirely, or its ADMIN option, walks the recursive
dependent-privilege check — `RRG_REMOVE_INHERIT_OPTION`/
`RRG_REMOVE_SET_OPTION` are simple in-place updates).

## Fix

### Parser

`internal/parser/parser.go`'s `buildRoleMembershipChange` prefix check now
matches `{admin|inherit|set} OPTION FOR` (case-insensitive) instead of only
`admin`, capturing the lower-cased option name. An unrecognized leading ColId
followed by `OPTION FOR` (real PG raises `ERROR: unrecognized role option`
for this via `GrantRole`) is left unrecognized here too — falls through to
the pre-existing lenient-parse posture of this builder (treated as the start
of the role list), matching the codebase's established pattern of not
duplicating every server-side validation error at parse time.

`parser.RoleMembershipChange.AdminOptionOnly bool` is replaced by
`RevokeOption string` — `""` for a plain `REVOKE role FROM member` (full
delete) or one of `"admin"`/`"inherit"`/`"set"` for the three OPTION FOR
forms.

### Catalog

`catalog.InMemory.RevokeRoleMembership`'s third parameter changed from
`adminOptionOnly bool` to `revokeOption string`, switching on `""` (delete
the row) / `"admin"` / `"inherit"` / `"set"` (clear just that one
`RoleMembership` field, row survives). Each of the three single-flag clears
is independent — clearing `InheritOption` never touches `AdminOption` or
`SetOption`, matching `DelRoleMems`'s per-action tuple update in real PG.

### WAL / restart persistence

`EncodeRevokeRoleMembership`/`DecodeRevokeRoleMembership`
(`internal/wal/recovery.go`) changed their bool parameter to the same
`revokeOption string`, packed as a single wire byte via a 4-entry lookup
table (0=full revoke, 1=admin, 2=inherit, 3=set). Payload size is unchanged
(10 bytes — the byte was already reserved, just binary before).
`internal/initdb/role_membership_recovery.go`'s replay interface and call
site thread the string through unchanged.

### Executor

`internal/executor/operators_ddl_role_membership.go`'s
`execRoleMembershipChange` passes `rc.RevokeOption` straight through to both
`RevokeRoleMembership` and `EncodeRevokeRoleMembership` (previously
`rc.AdminOptionOnly`).

## Verification

- `TestParseGrantRoleMembership` (`internal/parser/op_grant_rolemembership_test.go`):
  new `REVOKE INHERIT OPTION FOR ...`/`REVOKE SET OPTION FOR ...` cases
  alongside the existing `REVOKE ADMIN OPTION FOR ...` case, all asserting
  `RevokeOption`.
- `TestRevokeRoleMembership` (`internal/catalog/role_membership_test.go`):
  extended to exercise all three OPTION FOR selectors in sequence on one row,
  confirming each clears only its own flag and the row survives until the
  final plain (`""`) revoke deletes it.
- `TestEncodeDecodeRevokeRoleMembershipRoundTrip` (`internal/wal/
  role_membership_ddl_test.go`): round-trips all four `revokeOption` values.
- `TestRoleMembershipRecoveryReplaysGrantThenRevoke`
  (`internal/initdb/role_membership_recovery_test.go`): unchanged assertions,
  updated call sites (`""`/`"admin"` instead of `false`/`true`).

Gates: `go build ./...`/`go vet` clean; `internal/parser`+`internal/catalog`+
`internal/wal`+`internal/initdb`+`internal/executor` suites PASS; `-race`
`internal/wal`+`internal/mvcc` PASS (WAL record payload semantics touched,
though not its byte length); `scripts/tpch-spotcheck.sh` Q12=2/Q13=33 PASS;
pgbench smoke = pre-commit hook.

## Deferred (ledger row appended)

- **The grantor-chain circularity check** (PG's second `AddRoleMems` guard,
  `user.c` ~1751, "ADMIN option cannot be granted back to your own grantor")
  is still not implemented. Unchanged from the prior docs.
- **`GRANT ... ON PARAMETER`** (`pg_parameter_acl`) remains unimplemented.
  Unchanged from the prior docs.
- **REVOKE's recursive/cascade dependent-privilege walk**
  (`plan_recursive_revoke`, only triggered by a full revoke or `ADMIN OPTION
  FOR`, never by `INHERIT`/`SET OPTION FOR`) is not implemented. The parser
  recognizes and trims a trailing `CASCADE`/`RESTRICT` keyword but the
  executor never reads it — a REVOKE is always applied as a single-row
  operation with no dependency check. This is the same pre-existing gap the
  original `0119-0004-grant-role-membership.md` doc scoped out
  ("self+transitive circularity only, no grantor-chain or dependent-privilege
  walk"), now confirmed still open after this slice.
