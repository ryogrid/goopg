# 0119-0004 — `GRANT/REVOKE ... ON PARAMETER ...` (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/catalog/pg_parameter_acl.c`
(`ParameterAclLookup`/`ParameterAclCreate`); `postgres/src/backend/catalog/
aclchk.c` (`ExecGrant_Parameter`); `postgres/src/backend/utils/misc/guc.c`
(`convert_GUC_name_for_parameter_acl`); `postgres/src/bin/pg_dump/
pg_dumpall.c` (`dumpRoleGUCPrivs`); `postgres/src/backend/parser/gram.y`
(`PARAMETER parameter_name_list` in `privilege_target`)

## Problem

`0119-0004-revoke-role-option-for.md` (and every GRANT/REVOKE role-membership
slice before it) left `GRANT ... ON PARAMETER ...` (GUC-level ACLs,
`pg_parameter_acl`) as an open deferral: the catalog table was registered
correctly-empty (loop #91, pg_dumpall globals-only work) purely so
`pg_dumpall`'s `getParameterACLs`/`dumpRoleGUCPrivs` query resolved instead of
failing with "relation does not exist" — there was no parser or executor
support for the GRANT itself.

Unlike `TypeACLChange`/`DatabaseACLChange` (heap-backed pg_type/pg_database,
M0119-0004-ACLHEAP's original scope), `pg_parameter_acl` is *not* a real
on-disk PG object goopg needs standby/basebackup parity for — a GUC name is
not a catalog row. This makes the feature strictly simpler than TYPE/DATABASE:
no heap re-sync step, no PUBLIC default privilege to seed (a parameter's
`acldefault('p', owner)` gives PUBLIC `ACL_NO_RIGHTS`), and no real-object
existence check (GUC names are a compiled-in table PG validates against;
goopg accepts any name unconditionally as a bounded simplification).

## Fix

### Parser (`internal/parser/parser.go`, `internal/parser/ast.go`)

- New `ParameterACLChange` struct (mirrors `DatabaseACLChange`): `Revoke`,
  `Privileges`, `ParamNames`, `Grantees`, `WithGrantOption`. `ParamNames` are
  **raw dotted strings**, not `ObjectName` — GUC names may themselves contain
  a dot (`pgaudit.log`), which `gram.y`'s `parameter_name` production treats
  as part of the name, not a schema separator the way `qualified_name` does.
  A new `splitTokDottedNames` helper concatenates each comma-separated token
  run (preserving embedded `.` tokens verbatim) and lower-cases the result,
  mirroring `convert_GUC_name_for_parameter_acl`'s case-folding.
- The GRANT/REVOKE object-class switch gained a `case
  strings.EqualFold(next.Value, "parameter")` branch (checked before the
  pre-existing `grantNonTableClass` catch-all, which already listed
  `"parameter"` as a recognized-but-ignored class) setting `parameterACL =
  true`; the classification chain then calls `buildParameterACLChange`.
- `CompatNoopStmt.ParameterACLChange *ParameterACLChange` carries the parsed
  clause to the executor, alongside the existing `TypeACL`/
  `DatabaseACLChange`/`RoleMembership` fields.

### Server routing (`internal/server/query.go`)

`isHeapACLObject` (despite the name — it now means "route to the executor
instead of the virtual-ACL fast path") gained `strings.Contains(upper, " ON
PARAMETER ")`. Without this, a single-statement autocommit `GRANT ... ON
PARAMETER ...` would hit the pre-existing fast path (`tryRecordTableGrant` —
a no-op, since `nonTableGrantObjects` already excludes `"parameter"` — then an
immediate `WriteCommandComplete("GRANT")`) and `execParameterACLChange` would
never run, exactly the failure mode the role-membership slice's own doc
documented for `pg_auth_members`.

### Catalog (`internal/catalog/catalog.go`)

- `parameterACLOIDs map[string]uint32` / `parameterACLNames map[uint32]string`
  — a lazy name→OID registry, mirroring PostgreSQL's own lazy
  `ParameterAclCreate` (a GUC gets a `pg_parameter_acl` row only on its first
  GRANT). `ParameterACLOID(parname)` mints one via the shared `nextOID`
  counter (`AllocOID`'s locked body) and is idempotent for the same
  case-folded name.
- `parameterACLPrivOrder` (`SET`='s', `ALTER SYSTEM`='A' — PG's canonical
  `ACL_ALL_RIGHTS_STR` bit order) and `ownerParameterACLString = "sA"`
  (`ACL_ALL_RIGHTS_PARAMETER_ACL`). The privilege store itself is the
  **existing shared OID-keyed `tableACLs` map** — parameters are just another
  OID namespace sharing it with relations/schemas/routines/types/databases,
  same as every prior ACL class in this family.
- `ParameterACLText(oid)` renders the aclitem[] text via the existing
  `relaclTextLockedFor` helper (identical machinery to `TypeACLText`/
  `DatabaseACLText`).
- `ParameterACLEntries()` returns every ever-granted `(oid, parname)` pair
  sorted by `parname`, backing `pg_parameter_acl`'s `VirtualRows` (mirrors
  `pg_dumpall`'s `ORDER BY 1`).

### Executor (`internal/executor/operators_ddl_parameter_acl.go`, new file)

`execParameterACLChange` mirrors `execDatabaseACLChange`'s shape but skips its
heap-resync step entirely (there is no `pg_parameter_acl` heap relfilenode):
for each parameter name, mint/lookup its OID, normalize privileges (`SET` /
`ALTER SYSTEM` / `ALL [PRIVILEGES]` → both), then `GrantTablePrivilegeWithGrantOption`
or (seeding the owner default first, on an empty ACL) `RevokeTablePrivilege`
per grantee — the same two calls every other ACL class in this family uses.
Wired into `execCompatNoop` (`operators_ddl.go`) alongside `DatabaseACLChange`.

### `pg_get_userbyid` (new builtin, `internal/executor/expr.go`)

`pg_dumpall`'s `dumpRoleGUCPrivs` query calls
`pg_get_userbyid(10)` (`BOOTSTRAP_SUPERUSERID`) to resolve `pg_parameter_acl`'s
implicit owner display name — this builtin did not exist in goopg at all
(present in `pg_proc_seed_data.go`'s catalog seed, but with no
`evalFuncCall` case, it would 42883 at execution). Added a case delegating to
new `catalog.InMemory.RoleNameForOIDOrUnknown(oid)`, which mirrors
`ruleutils.c`'s `pg_get_userbyid` exactly — including its literal `"unknown
(OID=n)"` fallback text — deliberately kept separate from the pre-existing
`RoleNameForOID` (whose bare-numeral fallback serves ACL-text rendering
internals, a different contract).

## Verification

- `TestParseGrantParameterACL` / `TestParseGrantNonParameterLeavesParameterACLChangeNil`
  (`internal/parser/op_grant_parameteracl_test.go`): privilege/name/grantee/
  WGO capture across GRANT/REVOKE, `ALL`/`ALL PRIVILEGES` expansion, a dotted
  GUC name (`pgaudit.log`, confirming the dot is preserved not split), a
  quoted mixed-case name folding to lower case, and every other GRANT object
  class leaving `ParameterACLChange` nil.
- `TestParameterACLOID` / `TestParameterACLText` / `TestParameterACLRevokeFromOwner`
  / `TestParameterACLEntries` (`internal/catalog/relacl_test.go`): OID
  idempotency + case-folding, aclitem[] projection (including the "PUBLIC
  gets nothing by default" asymmetry vs. `DatabaseACLText`), owner-side
  partial revoke, and the sorted `VirtualRows` source.
- `TestRoleNameForOIDOrUnknown` (`internal/catalog/relacl_test.go`): the
  bootstrap-superuser/registered-role/unknown-OID cases.
- `TestPort_PgDumpallParameterACL` (`internal/testport/
  pgdumpall_parameter_acl_test.go`): end-to-end against the **real**
  `pg_dumpall` 18.3 binary — `GRANT SET`/`GRANT ALTER SYSTEM ON PARAMETER ...`
  round-trip byte-identically in the `-- Role privileges on configuration
  parameters --` section, and a fully-revoked parameter drops out of the dump
  entirely.

Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
`internal/catalog`+`internal/executor`+`internal/server` suites PASS;
`TestPort_PgDumpallParameterACL` PASS against real `pg_dumpall` 18.3;
`scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

## Deferred (ledger row appended)

- **GUC-name validation** (`check_GUC_name_for_parameter_acl`) is not
  implemented — goopg accepts any string as a parameter name unconditionally.
  Real PG validates against its compiled-in GUC table (or
  `assignable_custom_variable_name` for a dotted custom-extension name) and
  raises an error for a nonexistent parameter. A bounded simplification
  (goopg has no compiled-in GUC registry to validate against in the first
  place), consistent with how `GRANT ... ON TYPE`/`DATABASE` never re-validate
  role names either.
- The three pre-existing GRANT/REVOKE role-membership follow-ups from
  `0119-0004-revoke-role-option-for.md` are unchanged by this slice: the
  grantor-chain circularity check, `GRANT ... ON PARAMETER`'s own **row** is
  now closed by this doc, but the recursive/cascade `CASCADE`/`RESTRICT`
  dependent-privilege walk for role-membership REVOKE remains open.
