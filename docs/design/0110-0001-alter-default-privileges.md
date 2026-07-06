# M0110-0001 — `ALTER DEFAULT PRIVILEGES` parser + catalog fidelity

## Context

The 2026-07-04 deferral ledger entry for DU-002 slice 438 (SECURITY LABEL)
flagged `ALTER DEFAULT PRIVILEGES` as a fully unimplemented, Effort-L gap: no
parser support at all, and `pg_default_acl` (nailed as a permanently-empty
virtual view back at slice 20) never reflecting any stored default-ACL rule.
The catalog-level scaffolding for `pg_default_acl` (nailed relation OID 826,
its two indexes 827/828) had already landed in Steps 3ak/3al/3am — this loop
adds the actual SQL statement: parsing, storage, and `pg_default_acl` row
projection.

## Authoritative source

`postgres/src/backend/commands/aclchk.c`'s `ExecAlterDefaultPrivilegesStmt` /
`SetDefaultACL`, and `gram.y`'s `AlterDefaultPrivilegesStmt` /
`DefACLOptionList` / `DefACLAction` / `defacl_privilege_target` productions.

## Scope

**Landed this loop:**

- Parser: `internal/parser/ast.go`'s `AlterDefaultPrivilegesStmt`,
  `internal/parser/ddl.go`'s `parseAlterDefaultPrivileges` (dispatch from
  `parseAlter`), `internal/parser/parser.go`'s `buildAlterDefaultPrivileges` +
  `defaclObjTypeFromTarget`. Covers `[FOR ROLE|USER role_list]`
  `[IN SCHEMA schema_list]` (either order, repeatable), `GRANT`/`REVOKE`
  (including `REVOKE GRANT OPTION FOR`), all six
  `defacl_privilege_target` keywords (`TABLES`, `SEQUENCES`,
  `FUNCTIONS`/`ROUTINES`, `TYPES`, `SCHEMAS`, `LARGE OBJECTS`), `TO`/`FROM`
  grantee lists, and `WITH GRANT OPTION`/`CASCADE`/`RESTRICT` trailers.
  Test: `internal/parser/alter_default_privileges_test.go`
  (`TestParseAlterDefaultPrivileges`).
- Catalog storage: `internal/catalog/default_acl.go` — `defaultACLKey`
  (roleOID, schemaOID-or-0, objtype byte) lazily minted to a synthetic OID via
  `DefaultACLOID`, exactly like `ParameterACLOID`'s existing pattern. Grants
  ride the same synthetic-OID-keyed `tableACLs` store every other
  virtual-only ACL class already uses
  (`GrantDefaultACLPrivilege`/`RevokeDefaultACLPrivilege` are thin aliases for
  `GrantTablePrivilegeWithGrantOption`/`RevokeTablePrivilege`). A *global*
  entry (no `IN SCHEMA`) renders with the owning role's implicit
  `acldefault()` baseline via the existing `relaclTextLockedFor` owner-
  injection engine (`DefaultACLText`); a *schema-scoped* entry has no implicit
  baseline (`defaultACLTextNoOwnerLocked`), matching `SetDefaultACL`'s
  `make_empty_acl()` branch for a valid `nspid`. A row disappears from
  `DefaultACLEntries()` once its ACL is fully revoked back to empty, matching
  real PG's row deletion.
- Executor: `internal/executor/operators_ddl_default_privileges.go`'s
  `execAlterDefaultPrivileges`, dispatched from `operators_ddl.go`'s `ddlOp`
  (`planner.go`'s `Plan` routes `*parser.AlterDefaultPrivilegesStmt` through
  the generic `DDL` node; `dispatch.go`'s `ddlTag` returns
  `"ALTER DEFAULT PRIVILEGES"`). Resolves `FOR ROLE` (default: bootstrap
  superuser OID 10, matching `current_user`'s existing hardcoded resolution)
  and `IN SCHEMA` (default: schemaOID 0, global) targets, raising `42704`
  (unregistered role) / `3F000` (unrecognized schema) on a miss. Rejects
  `IN SCHEMA` combined with `ON SCHEMAS`/`ON LARGE OBJECTS` with `0LP01`,
  matching `SetDefaultACL`'s outright rejection. A bare (no `IN SCHEMA`)
  `LARGE OBJECTS` target is accepted syntactically but never materializes a
  row (goopg has no `pg_largeobject` subsystem at all to describe). `REVOKE`
  against a triple with no materialized row is a no-op (never mints a hollow
  entry), mirroring `execParameterACLChange`'s identical gate.
  Test: `internal/executor/operators_ddl_default_privileges_test.go`.
- `pg_default_acl` (`internal/catalog/catalog.go`'s `registerSystemTables`)
  now projects `DefaultACLEntries()` instead of always returning zero rows.

**Deliberately out of scope (deferral ledger row appended this loop):**
applying a stored default-ACL entry to an object actually created afterwards.
No `CREATE TABLE`/`SEQUENCE`/`FUNCTION`/`TYPE`/`SCHEMA` call site consults this
store yet — `ALTER DEFAULT PRIVILEGES` round-trips correctly through
`pg_default_acl` (what `pg_dump`'s `getDefaultACLs`/`dumpDefaultACL` query
against) but has no runtime effect on new objects' initial ACLs. This is a
separately bounded, larger gap spanning every object-creation path.

## Simplifications accepted (shared with existing ACL stores in this package)

- `FOR ROLE <other>`'s *implicit* baseline is not separately modeled — every
  ACL store here assumes the owning role is always `"postgres"` (goopg's
  `current_user` resolves unconditionally to it). Grants explicitly naming
  another role as a *grantee* still render correctly; only the synthetic
  "owner has full rights by default" baseline is postgres-only.
  `REVOKE GRANT OPTION FOR` revokes the privilege outright rather than just
  the grant-option bit, matching every other `GRANT`-family ACL change in
  this codebase (e.g. `buildDatabaseACLChange`).
- The non-text `EXPLAIN`-style "is `track_io_timing` on" question does not
  apply here; not relevant to this doc (see the sibling EXPLAIN I/O Timings
  work landing the same day for that unrelated simplification).

## Verification

- `go build ./...` — PASS.
- `go test ./internal/parser/... ./internal/catalog/... ./internal/executor/...`
  — PASS (includes the 9 new `TestExecAlterDefaultPrivileges*` cases and the
  pre-existing `TestParseAlterDefaultPrivileges`).
- `scripts/tpch-spotcheck.sh` — see loop's `RECOMMENDATION` line for result
  (executor/catalog change; run per the executor/planner practice card even
  though this feature has no join/predicate/row-count surface).

## Resume point

`internal/executor/operators_ddl.go`'s `CREATE TABLE`/`CREATE SEQUENCE`/
`CREATE FUNCTION`/`CREATE TYPE`/`CREATE SCHEMA` executors need to consult
`catalog.InMemory.DefaultACLEntries()` (filtered by the new object's owner
role and target schema) when seeding a fresh object's initial ACL, the same
way `acl_get_default` folds a matching default-ACL entry into
`heap_create_with_catalog`'s ACL seeding in real PostgreSQL.
