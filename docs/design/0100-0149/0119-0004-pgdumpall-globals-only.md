# 0119-0004 — `pg_dumpall --globals-only` unblocked; cluster-wide `ALTER ROLE ... SET` round-trip (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dumpall.c` (`dumpRoles`, `dumpUserConfig`,
`dumpRoleMembership`, `getParameterACLs`);
`postgres/src/include/catalog/pg_authid.h`;
`postgres/src/include/catalog/pg_auth_members.h`;
`postgres/src/include/catalog/pg_parameter_acl.h`

## Problem

`0119-0004-role-config-set-pgdump.md` (the previous slice in this thread)
closed the `IN DATABASE` half of `pg_db_role_setting`'s `setrole != 0` rows
and left the plain cluster-wide `ALTER ROLE ... SET` form (`setdatabase =
0`, dumped by `pg_dumpall`'s `dumpUserConfig`, not `pg_dump --create`'s
`dumpDatabaseConfig`) as an explicitly untested residual, on the assumption
(recorded in the deferral ledger) that closing it was "pure TAP-porting
work, not a new engine capability — engine storage already supports it."

That assumption was wrong in a way only running the actual binary revealed.
Probing `pg_dumpall --globals-only` against a fresh goopg cluster failed
immediately:

```
pg_dumpall: error: query failed: ERROR:  relation "pg_authid" does not exist
```

`pg_dumpall` does not read role state through `pg_roles` (the thin view
`pg_dump.c`/`pg_dumpall.c` use elsewhere) — `dumpRoles`/`dumpUserConfig`
query `pg_authid` directly, and goopg's virtual catalog had never registered
that relation at all (only `pg_roles`, a 4-column subset). Continuing to
probe surfaced two more missing relations before `--globals-only` could
complete: `pg_auth_members` (`dumpRoleMembership`) and `pg_parameter_acl`
(`getParameterACLs`).

## What landed

Three new virtual system catalogs in `internal/catalog/catalog.go`,
registered immediately after `pg_roles`:

1. **`pg_authid`** (OID 1260, upstream's `AuthIdRelationId`) — the real
   12-column role catalog `pg_roles` is a view over: `oid`, `rolname`,
   `rolsuper`, `rolinherit`, `rolcreaterole`, `rolcreatedb`, `rolcanlogin`,
   `rolreplication`, `rolbypassrls`, `rolconnlimit`, `rolpassword`,
   `rolvaliduntil`. Sourced from the same live `c.roles`/`c.roleAttrs`
   state `pg_roles` already reads (not the on-disk `global/1260` heap file
   `internal/executor/pg_authid_sync.go` maintains — that file is a
   separate crash-recovery mirror for auth credentials written on role DDL
   and read back only at startup, not a live SQL read path; see
   `[[pg_on_goopg_catalog_lacks_pg_stat_views]]`-style heap-vs-virtual
   split precedent). `rolpassword` reports the real stored verifier
   (`RoleAttrs.Secret`) when a credential is set (`CredType != 0`), NULL
   otherwise — same shape `pg_authid.rolpassword` has in real PG.
   Attributes goopg's role DDL never actually sets today
   (`rolinherit`/`rolcreaterole`/`rolcreatedb`/`rolreplication`/
   `rolbypassrls`/`rolconnlimit`/`rolvaliduntil`) report PG's `CREATE ROLE`
   defaults (`INHERIT`, `NOCREATEROLE`, `NOCREATEDB`, `NOREPLICATION`,
   `NOBYPASSRLS`, connlimit `-1`, valid-until NULL) since nothing in goopg
   can diverge them from those defaults yet — honest defaults, not a
   shortcut, because there is no code path that could produce a different
   value.

   `pg_roles.Table.OID` was `1260` before this change — a stale placeholder
   comment ("upstream's AuthIdRelationId") from before `pg_authid` existed
   as a distinct relation. Reassigned to a synthetic `1259102` (following
   the `pg_tables`/`1259101` synthetic-OID convention already in this file)
   now that the real `1260` is correctly claimed by `pg_authid` itself; no
   test or call site depended on `pg_roles`' specific OID value (verified —
   only row *content* is asserted anywhere).

2. **`pg_auth_members`** (OID 1261, upstream's `AuthMemRelationId`) —
   correctly-empty. Role membership (`GRANT <role> TO <role>`) is a
   genuinely separate capability from privilege GRANT (`GRANT <priv> ON
   <object> TO <role>`), and goopg's parser/executor has **no** support for
   it at all — no grammar, no catalog storage, confirmed by exhaustive grep
   (`GrantRole`/`RoleGrant`/`WITH ADMIN OPTION` have zero hits anywhere in
   `internal/`). Registering an always-empty relation is the same pattern
   `0119-0004bs` used for `pg_shseclabel`/`pg_db_role_setting` before those
   gained real backing stores: it turns a hard query failure into a
   correct (if incomplete) answer, and is honest today because no
   membership row can exist under any sequence of goopg-supported DDL.

3. **`pg_parameter_acl`** (OID 6243, upstream's `ParameterAclRelationId`) —
   likewise correctly-empty. `GRANT SET ON PARAMETER <guc> TO <role>`
   (PG 15+) is unimplemented; `pg_get_userbyid`/`acldefault` (the two
   builtins `getParameterACLs`'s query also calls) already existed from
   prior M0119-0004-ACLHEAP work, so only the base relation was missing.

With all three registered, `pg_dumpall --globals-only` runs to completion
(exit 0) against a live goopg cluster and correctly dumps `CREATE ROLE` +
`ALTER ROLE ... WITH <attrs>` for every role, plus — the actual target of
this slice — the cluster-wide `ALTER ROLE <name> SET <guc> TO <value>;`
form via `dumpUserConfig`'s `pg_db_role_setting`/`setdatabase = 0` query,
which the prior slice's engine plumbing (`catalog.InMemory.roleSettings`,
keyed `DBOid == 0` for cluster-wide) already supported end-to-end without
any further change — that half of the original ledger claim held.

## Not in scope / still open (deferral ledger)

- **`GRANT <role> TO <role>` (role membership)** — a new capability:
  parser grammar, `pg_auth_members` real storage, `dumpRoleMembership`'s
  full 9-column query (`admin_option`/`inherit_option`/`set_option`),
  interaction with privilege inheritance at authentication/EXECUTE time.
  Resume: `internal/parser` needs a `GRANT <role-list> TO <role-list> [WITH
  ADMIN OPTION]` production distinct from the existing object-privilege
  `GRANT`; `internal/catalog` needs a `roleMembers` store; `pg_auth_members
  .VirtualRows` swaps from the current `return nil` to reading it.
- **`GRANT ... ON PARAMETER <guc> TO <role>`** — same shape gap, smaller
  surface (single GUC-privilege object class, PG 15+ feature). Resume:
  parser `GRANT SET/ALTER SYSTEM ON PARAMETER` production +
  `pg_parameter_acl` real storage.
- **`rolinherit`/`rolcreaterole`/`rolcreatedb`/`rolreplication`/
  `rolbypassrls`/`rolconnlimit`/`rolvaliduntil`** — `CREATE ROLE`/`ALTER
  ROLE` already silently discard these keywords at parse time
  (`internal/server/role_ddl.go`'s own comment: "Unrecognised options
  (CREATEDB, REPLICATION, VALID UNTIL, ...) are ignored"). `pg_authid`
  reporting PG's defaults for them is therefore accurate today but will
  need real per-role storage (a `RoleAttrs` field each) the day any of
  these gets parser support — a full role-attribute-parity milestone, not
  a pg_dumpall-porting task.
- Everything already tracked as standing across the M0119-0004-ACLHEAP
  thread is unchanged: extended-protocol commit-time deferral;
  multi-database scope; `SET TIME ZONE`/`SET SESSION AUTHORIZATION`/`SET
  ... FROM CURRENT` special forms.

## Tests

- `internal/testport/pgdumpall_role_config_test.go`
  `TestPort_PgDumpallGlobalsOnly` — drives the real `pg_dumpall` 18.3
  binary (`postgres/local_install/bin`) against a fresh goopg cluster:
  `CREATE ROLE`, cluster-wide `SET`/`RESET`/re-`SET`, and an `IN DATABASE`
  override that must NOT leak into the cluster-wide dump surface. Format
  fidelity is inherent (the dump text is generated entirely by the real
  binary; only the underlying data comes from goopg), matching the
  "byte-identical vs real pg_dump/pg_dumpall 18.3" bar the rest of this
  series holds itself to.
- `internal/catalog` suite (regression: `pg_roles` row-content assertions
  in `TestRoleOIDRegistry` unaffected by the OID reassignment).
- `internal/testport` regression guards: `TestPort_PgDumpConnectionSetup`,
  `TestPort_PgDumpRoleConfigSet`, `TestPort_PgDumpDatabaseConfigSet`,
  `TestPort_PgDumpDatabaseGrantACL`, `TestPort_PgDump001Basic` — all PASS
  unchanged.

## Gates

`go build ./...` / `go vet ./internal/catalog/... ./internal/testport/...`
clean; `internal/catalog` suite PASS; the `internal/testport` regression
set above PASS; `internal/executor`+`internal/server`+`internal/planner`+
`internal/initdb` suites PASS (adding three read-only virtual tables
touches no write path); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pgbench smoke = pre-commit hook.
