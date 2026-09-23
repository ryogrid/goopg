# M0122-0008: refuse WITH GRANT OPTION to PUBLIC

Status: landed 2026-09-23 (`75140193f`). Task: `.ralph/fix_plan.md`
M0122-0008 (umbrella: auth / roles / multi-DB). Kind: impl. Origin: a
divergence ledgered by the M0122-0008a fixture, whose first draft used exactly
this statement and so pinned a state PostgreSQL cannot produce.

## PostgreSQL behaviour

`merge_acl_with_grant` (`./postgres/src/backend/catalog/aclchk.c:208`):

```c
if (is_grant && grant_option && aclitem.ai_grantee == ACL_ID_PUBLIC)
    ereport(ERROR,
            (errcode(ERRCODE_INVALID_GRANT_OPERATION),
             errmsg("grant options can only be granted to roles")));
```

Every `ExecGrant_*` object class and ALTER DEFAULT PRIVILEGES (`SetDefaultACL`)
pass through that one function, so the refusal applies to all of them. REVOKE
(`is_grant` false) is unaffected — `REVOKE GRANT OPTION FOR … FROM PUBLIC` is
legal. The upstream comment gives the reason: a user who re-granted a privilege
held only through PUBLIC could never be cleaned up when that user is dropped.

## goopg before

Every grant path accepted it and stored the grant-option bit:
`GRANT SELECT ON t TO PUBLIC WITH GRANT OPTION` returned `GRANT`.

## Design

One predicate, `catalog.GrantOptionToPublic(withGrantOption, grantees)`
(`internal/catalog/grant_option_public.go`), mirroring PG's single check.
PUBLIC is recognised the way gram.y's `RoleSpec` does: any `NonReservedWord`
equal to `public` becomes `ROLESPEC_PUBLIC`, so the unquoted keyword in any case
and the quoted lower-case `"public"` both count; a quoted `"PUBLIC"` names an
ordinary role.

It is consulted **before any ACL entry is written**. PG can fail in the middle
of its grantee loop because the transaction rolls back; goopg's ACL store is not
transactional, so a mid-loop refusal would leave earlier grantees recorded.

Call sites:

| path | where |
|---|---|
| TYPE / DOMAIN | `execTypeACLChange` |
| column | `execAttrACLChange` |
| DATABASE | the database ACL path (`operators_ddl_database_acl.go`) |
| PARAMETER | `operators_ddl_parameter_acl.go` |
| ALTER DEFAULT PRIVILEGES | `operators_ddl_default_privileges.go`, before its large-object early return (PG has none) |
| TABLE / SEQUENCE / SCHEMA / FUNCTION / FOREIGN SERVER / FDW (autocommit) | postmaster fast path `tryRecordTableGrant` → `errGrantOptionToPublic`, mapped by `query.go` to `errcodes.InvalidGrantOperation` |

The executor sites use `checkGrantOptionToPublic`, placed beside the existing
`checkGrantedByCurrentUser` precheck.

## Verification

A private PG 18.3 (`initdb` under `/tmp`, port 5535) and a goopg server ran the
same script. All nine autocommit forms (table, table with a role before PUBLIC,
column, function, schema, type, database, parameter, ALTER DEFAULT PRIVILEGES)
produce the identical SQLSTATE `0LP01`, message and statement line; a role
grantee and `REVOKE GRANT OPTION FOR … FROM PUBLIC` stay accepted on both.

Tests: `TestGrantOptionToPublic` (predicate, including the quoting cases);
`TestGrantOptionToPublicIsRejectedOnEveryTypedPath` (five typed paths plus
controls — non-vacuous: neutralising the check fails all six rejections);
`TestTryRecordTableGrantRejectsGrantOptionToPublic` (fast path records nothing).

## Remaining divergence

A table GRANT inside an explicit transaction does not take the fast path; it
reaches the executor as a `CompatNoopStmt`, which carries neither the grantees
nor the grant option — so it is still accepted there. That path records no ACL
change at all, the broader gap already ledgered by M0134-0069; the refusal
belongs in the same fix (parse the GRANT into a typed statement and route it
through the recorders).
