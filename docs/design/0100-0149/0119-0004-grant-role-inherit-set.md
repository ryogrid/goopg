# 0119-0004 — `GRANT ... WITH INHERIT/SET` role-membership option parsing (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/user.c` (`GrantRole`,
`InitGrantRoleOptions`); `postgres/src/backend/parser/gram.y`
(`GrantRoleStmt`/`grant_role_opt_list`); `postgres/src/bin/pg_dump/pg_dumpall.c`
(`dumpRoleMembership`)

## Problem

`0119-0004-grant-role-membership.md` (loop #17/interactive session) landed
`GRANT <role> TO <role> [WITH ADMIN OPTION] [GRANTED BY <role>]` but its own
"Deferred" section left the rest of PG 16+'s `grant_role_opt_list` grammar
unparsed:

```
GRANT role_name [, ...] TO role_specification [, ...]
    [ WITH { ADMIN | INHERIT | SET } { OPTION | TRUE | FALSE } ]
    [ GRANTED BY role_specification ]
```

`buildRoleMembershipChange` only recognised the literal three-token run
`WITH ADMIN OPTION`; any `WITH INHERIT ...`/`WITH SET ...` clause, or a
comma-separated multi-option list (`WITH ADMIN OPTION, INHERIT FALSE`), fell
through unrecognised (its tokens ended up swallowed into the grantee-role
list via `splitTokRoles`, silently corrupting the parse). `pg_auth_members`'s
`inherit_option`/`set_option` columns were hardcoded `"t"`/`"f"` regardless of
what the statement requested.

**A second, independent bug was found and fixed while verifying this
change**: the hardcoded `set_option` literal was `"f"`, with a comment
claiming that reflected "PG default (NOSET pre-PG16 semantics)". Cross-checked
against a live `postgres/local_install` PG 18.3 instance (bootstrapped fresh,
see Verification): `InitGrantRoleOptions` (`user.c`) actually sets
`popt->set = true`, and a fresh `GRANT r TO m` (no `WITH SET` clause) reports
`set_option = t`. The already-landed `TestPort_PgDumpallRoleMembership` had
been written to match goopg's own (wrong) `"f"` output rather than real PG's,
so its golden strings included a `SET FALSE` clause real `pg_dumpall` never
emits for an unspecified `SET` option. Both the engine default and the test
are corrected by this change.

## Fix

### Parser: full `grant_role_opt_list` support

`internal/parser/parser.go`'s `buildRoleMembershipChange` now delegates its
`WITH` handling to a new `parseGrantRoleOptList(toks, from)`, which walks a
comma-separated run of `{ ADMIN | INHERIT | SET } { OPTION | TRUE | FALSE }`
pairs (case-insensitive `ColLabel` + value, mirroring gram.y's
`grant_role_opt_list`/`grant_role_opt`/`grant_role_opt_value` productions) and
returns three tri-state `*bool` results — `nil` when that option was never
named in the statement, matching PG's `GRANT_ROLE_SPECIFIED_ADMIN/INHERIT/SET`
bitmask (`user.c`) exactly, rather than collapsing "unspecified" and
"specified false" into the same `false` value the old single `bool` did.
`RoleMembershipChange` gained `AdminOption`/`InheritOption`/`SetOption *bool`
fields; the pre-existing `WithAdminOption bool` is kept for source
compatibility, now derived as `AdminOption != nil && *AdminOption`.

A side effect fixes a latent bug in the original single-clause scanner: it
`break`-exited the whole scan loop on the `WITH` branch, so `WITH ADMIN
OPTION GRANTED BY x` silently dropped the `GRANTED BY` grantor. The new
scanner advances past the parsed option list and keeps scanning, so a
trailing `GRANTED BY`/`CASCADE`/`RESTRICT` after the `WITH` clause is now
found correctly (exercised by the new `WITH INHERIT TRUE, SET FALSE GRANTED
BY postgres` test case).

### Catalog: tri-state upsert semantics

`catalog.RoleMembership` gained `InheritOption`/`SetOption bool` fields.
`GrantRoleMembership`'s signature changed from `(roleOid, memberOid,
grantorOid uint32, adminOption bool)` to `(roleOid, memberOid, grantorOid
uint32, admin, inherit, set *bool)`:

- **Fresh row (insert):** falls back to `InitGrantRoleOptions`' defaults for
  any nil pointer — `admin=false`, `set=true`, `inherit=true` (goopg has no
  per-role `NOINHERIT` tracking, so every role's `rolinherit` is always
  `true` — coincidentally identical to PG's real rule "default to the
  grantee's `rolinherit`" for every role goopg can represent).
- **Existing row (re-grant):** a `nil` pointer leaves that option's current
  value **untouched** (a plain re-grant with no matching `WITH` clause never
  changes an option it didn't name); a non-nil pointer **always applies**,
  including a legitimate downgrade (e.g. explicit `WITH ADMIN FALSE` after an
  earlier `WITH ADMIN OPTION`) — this is a strict correctness fix over the
  prior implementation, which could only ever upgrade `admin_option` to
  `true` and had no way to explicitly clear it via GRANT (only via `REVOKE
  ADMIN OPTION FOR`).

`pg_auth_members.VirtualRows` (`internal/catalog/catalog.go`) now projects
the real per-row `InheritOption`/`SetOption` instead of the two hardcoded
literals.

### WAL / restart persistence

`EncodeGrantRoleMembership`/`DecodeGrantRoleMembership` (`internal/wal/
recovery.go`) changed from a single `adminOption bool` byte to a tri-state
options byte: 3 (specified, value) bit pairs, one per admin/inherit/set.
Payload length is unchanged (14 bytes — the byte was already reserved, just
under-used). `internal/initdb/role_membership_recovery.go`'s replay interface
and call site updated to thread all three tri-state pointers through to
`GrantRoleMembership` on replay, so a restart reconstructs the exact
requested (not merely "some admin flag") option state.

### Executor

`internal/executor/operators_ddl_role_membership.go`'s `execRoleMembershipChange`
now passes `rc.AdminOption`/`rc.InheritOption`/`rc.SetOption` straight through
to `GrantRoleMembership` and `EncodeGrantRoleMembership` (previously
`rc.WithAdminOption`, a plain `bool`).

## Verification

Live PG 18.3 cross-check (`postgres/local_install`, fresh `initdb` + a scratch
instance on a throwaway port): `CREATE ROLE memadmin; CREATE ROLE memalice
LOGIN; CREATE ROLE membob LOGIN; GRANT memadmin TO memalice WITH ADMIN
OPTION; GRANT memadmin TO membob;` then `SELECT ... FROM pg_auth_members` and
`pg_dumpall --globals-only` confirmed `set_option = t` for both rows (no
`WITH SET` requested) and a `dumpRoleMembership` output of exactly `GRANT
memadmin TO memalice WITH ADMIN OPTION, INHERIT TRUE GRANTED BY postgres;` /
`GRANT memadmin TO membob WITH INHERIT TRUE GRANTED BY postgres;` — no `SET
FALSE` clause.

- `TestParseGrantRoleMembership` (`internal/parser/op_grant_rolemembership_test.go`):
  new cases for `WITH INHERIT TRUE, SET FALSE GRANTED BY postgres` (comma
  list + trailing GRANTED BY) and `WITH ADMIN FALSE, INHERIT FALSE`
  (multi-option, explicit-false capture).
- `TestGrantRoleMembershipUpsertsInPlace` (extended): explicit `WITH ADMIN
  FALSE` now downgrades an existing `true` row, unlike an unspecified
  re-grant.
- `TestGrantRoleMembershipInheritSetDefaults` (new,
  `internal/catalog/role_membership_test.go`): fresh-row defaults
  (admin=false, inherit=true, set=true), explicit override on insert, and
  untouched-on-unspecified-re-grant.
- `TestEncodeDecodeGrantRoleMembershipRoundTrip` (extended,
  `internal/wal/role_membership_ddl_test.go`): tri-state round-trip across
  all-nil / all-true / all-false / mixed combinations.
- `TestPort_PgDumpallRoleMembership` (`internal/testport/
  pgdumpall_role_membership_test.go`): golden strings corrected to
  `WITH ADMIN OPTION, INHERIT TRUE`/`WITH INHERIT TRUE` (dropping the
  incorrect `SET FALSE`), re-verified against the live PG 18.3 instance
  above.

Gates: `go build ./...`/`go vet` clean; `internal/parser`+`internal/catalog`+
`internal/wal`+`internal/initdb`+`internal/executor`+`internal/server` suites
PASS; `-race` `internal/wal`+`internal/mvcc` PASS (WAL record format
touched); `TestPort_PgDumpallRoleMembership`/`TestPort_PgDumpallGlobalsOnly`/
`TestPort_PgDumpRoleConfigSet`/`TestPort_PgDumpDatabaseConfigSet` PASS;
`scripts/tpch-spotcheck.sh` Q12=2/Q13=33 PASS; pgbench smoke = pre-commit
hook.

## Deferred (ledger row appended)

- **The grantor-chain circularity check** (PG's second `AddRoleMems` guard,
  `user.c` ~1751, "ADMIN option cannot be granted back to your own grantor")
  is still not implemented — only the simpler `RoleIsMemberOf` self/transitive
  membership cycle check exists. Unchanged from the prior doc.
- **`GRANT ... ON PARAMETER`** (`pg_parameter_acl`) remains unimplemented —
  unchanged from the prior doc.
- **`REVOKE { ADMIN | INHERIT | SET } OPTION FOR`** is still ADMIN-only:
  `buildRoleMembershipChange`'s `adminOptionOnly` prefix check only matches
  the literal `admin` `ColLabel`, though PG's `RevokeRoleStmt` grammar
  (`ColId OPTION FOR`) accepts any of the three, and `RevokeRoleMembership`
  has no "which option to clear" parameter to receive it. Newly noted by this
  slice (not previously called out), since it is the natural REVOKE-side
  counterpart of the GRANT-side feature landed here.
