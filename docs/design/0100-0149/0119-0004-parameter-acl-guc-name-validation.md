# 0119-0004 — `GRANT ... ON PARAMETER` GUC-name validation (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-03)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/utils/misc/guc.c`
(`check_GUC_name_for_parameter_acl`, `assignable_custom_variable_name`,
`valid_custom_variable_name`, `find_option`); `postgres/src/backend/catalog/
pg_parameter_acl.c` (`ParameterAclLookup`, `ParameterAclCreate`);
`postgres/src/backend/catalog/aclchk.c` (`objectNamesToOids`'s
`OBJECT_PARAMETER_ACL` case)

## Problem

`0119-0004-grant-on-parameter-pgdumpall.md` landed `GRANT/REVOKE ... ON
PARAMETER` end-to-end but deferred GUC-name validation entirely: goopg
accepted *any* string as a parameter name, materializing a `pg_parameter_acl`
row for it unconditionally. Real PostgreSQL's `ParameterAclCreate` calls
`check_GUC_name_for_parameter_acl` first, which accepts a name only if it is
either a real compiled-in GUC (`find_option`, no placeholder creation) or a
syntactically valid custom/extension name (`assignable_custom_variable_name`
— two or more dot-separated simple identifiers, not colliding with a loaded
extension's reserved namespace prefix). Anything else raises an error:
`42704` (undefined_object) for an unrecognized single-part name, `42602`
(invalid_name) for a dotted name that fails the syntax check.

Separately, `objectNamesToOids`'s REVOKE branch only *looks up* a name
(`ParameterAclLookup(parameter, missing_ok=true)`) — it never mints a new row
for a REVOKE naming a GUC with no existing ACL entry, since there is nothing
to remove. goopg's prior implementation called `ParameterACLOID` (which
always mints on first use) unconditionally on both the GRANT and REVOKE
paths, so `REVOKE ... ON PARAMETER never_granted_thing FROM someone` would
spuriously materialize an owner-only row for a parameter that was never
granted — a second, related divergence fixed in the same slice since it sits
in the exact code path being touched.

## Fix

### `internal/config` — exported custom-GUC-name syntax check

`session.go`'s pre-existing `isCustomGUCName` (used by `SET` on an
unregistered name) already implements `valid_custom_variable_name`'s core
rule (minus the reserved-extension-prefix half, which goopg does not track).
Exported as `IsCustomGUCName` so the executor's parameter-ACL package can
reuse the exact same syntax check `SET` uses — one rule, two call sites,
instead of a second copy drifting out of sync.

### `internal/catalog/catalog.go` — `HasParameterACL`

New `(*InMemory).HasParameterACL(parname string) bool`: a read-only lookup
(`parameterACLOIDs[parname]`, case-folded) that mirrors
`ParameterAclLookup(parameter, missing_ok=true)` without minting an entry —
unlike the pre-existing `ParameterACLOID`, which always creates one. Callers
use this to decide *whether* a name is brand-new before deciding whether to
validate it or mint it.

### `internal/executor/operators_ddl_parameter_acl.go`

- New `checkParameterACLName(name string) error` ports
  `check_GUC_name_for_parameter_acl` exactly: known via `ctx.GetSetting`
  (the session's compiled-in + already-`SET` custom GUCs) → accept;
  `config.IsCustomGUCName(name)` → accept (an as-yet-unset but syntactically
  valid custom/extension placeholder, matching `assignable_custom_variable_name`
  without creating a real placeholder GUC — parameter ACLs don't need one);
  a dotted name that fails the syntax check → `42602`; a bare unrecognized
  name → `42704`. The loaded-extension reserved-namespace-prefix check
  (`reserved_class_prefix`) is not ported — goopg has no notion of a loaded
  extension's GUC namespace — noted below as still open.
- `execParameterACLChange` restructured to match `objectNamesToOids`'s
  `OBJECT_PARAMETER_ACL` case timing exactly:
  - **GRANT**: only calls `checkParameterACLName` when
    `!im.HasParameterACL(name)` — i.e. the first time this name would
    materialize a new row (`ParameterAclCreate`'s own guard). A second GRANT
    on an already-materialized name skips re-validation entirely, matching
    PG (which never re-checks a name once its `pg_parameter_acl` row
    exists).
  - **REVOKE**: `if !im.HasParameterACL(name) { continue }` — a REVOKE
    naming a GUC with no ACL entry is a pure no-op and must not materialize
    one (fixes the ledgered divergence above).

## Verification

- `TestIsCustomGUCName` (`internal/config/is_custom_guc_name_test.go`):
  accepts a well-formed dotted name (including 3+ components), rejects a
  bare name, empty string, and every empty-component shape (leading/
  trailing/middle `.`), rejects a leading digit in a component.
- `TestHasParameterACL` (`internal/catalog/relacl_test.go`): false before
  any `ParameterACLOID` call for a name, true after (including a
  differently-cased lookup), and never itself mutates state.
- `TestCheckParameterACLName` / `TestExecParameterACLChangeRejectsUnknownName`
  / `TestExecParameterACLChangeAcceptsKnownAndCustomNames` /
  `TestExecParameterACLChangeRevokeOnUnknownNameIsNoop` /
  `TestExecParameterACLChangeSecondGrantSkipsRevalidation`
  (`internal/executor/operators_ddl_parameter_acl_test.go`): the four
  accept/reject branches directly, the executor wiring end-to-end (a
  rejected GRANT never materializes a row), the REVOKE-on-unknown-name
  no-op, and the "already-materialized name skips re-validation" timing
  (using a `GetSetting` stub that always reports "unknown" to prove the
  second GRANT doesn't call it).
- Live end-to-end `psql` smoke against a running goopg instance, cross-checked
  against real PostgreSQL 18.3 side-by-side: `GRANT SET ON PARAMETER
  work_mem` (known GUC) and `GRANT SET ON PARAMETER myext.enabled` (custom
  name) both succeed on both engines; `GRANT SET ON PARAMETER
  totally_bogus_setting` fails on both with byte-identical `ERROR:
  unrecognized configuration parameter "totally_bogus_setting"`; `GRANT SET
  ON PARAMETER "bad..name"` fails on both with byte-identical `ERROR: invalid
  configuration parameter name "bad..name"` / `DETAIL: Custom parameter
  names must be two or more simple identifiers separated by dots.`; `REVOKE
  SET ON PARAMETER never_granted_xyz` succeeds as a no-op on both engines and
  leaves `pg_parameter_acl` with no row for that name.
- `TestPort_PgDumpallParameterACL` (pre-existing, re-run unchanged) still
  PASS — its fixture only exercises known GUCs (`work_mem`,
  `statement_timeout`, `log_min_duration_statement`), so the new validation
  doesn't disturb it; re-confirms the REVOKE-then-dump path stays row-free.

Gates: `go build ./...`/`go vet ./...` clean; `internal/config`+
`internal/catalog`+`internal/executor`+`internal/parser`+`internal/server`+
`internal/wal`+`internal/initdb` suites PASS; `TestPort_PgDumpallParameterACL`
PASS against real `pg_dumpall` 18.3; `scripts/tpch-spotcheck.sh` PASS;
pgbench smoke = pre-commit hook.

## Deferred (ledger row appended)

- **Reserved-extension-namespace-prefix check** (`reserved_class_prefix` in
  `guc.c`): real PG additionally rejects a syntactically valid custom name
  that collides with a currently *loaded* extension's reserved GUC
  namespace (e.g. `pgaudit.foo` once `pgaudit` is loaded, even though
  `pgaudit` itself defines no such exact GUC). goopg has no notion of a
  loaded extension's GUC namespace registry to check against — unrelated to
  and out of scope for this slice, and low-impact (the false-accept only
  matters for an extension goopg cannot load anyway, since it has no
  extension-loading mechanism).
- **`ROLE_PG_DATABASE_OWNER` carve-out**, **`check_role_grantor`'s
  `WITH INHERIT FALSE` edge case**, and **predefined-role live registration**
  from prior M0119-0004-ACLHEAP rows are unchanged by this slice (unrelated
  code path).
