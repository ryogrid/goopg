# 0119-0004 — Function GRANT … WITH GRANT OPTION (`pg_proc.proacl`) round-trip in pg_dump (DU-002 slice 348)

Status: accepted
Milestone: M0119-0004 (pg_dump TAP port DU-002, slice 348)
Date: 2026-06-30

## Problem

Slice 345 made a plain function-level GRANT (`GRANT EXECUTE ON FUNCTION f TO r`)
round-trip through pg_dump from `pg_proc.proacl`. It explicitly deferred the
grant-option variant: a function grant recorded with `GrantTablePrivilege`
dropped the WITH-GRANT-OPTION flag, so

```
GRANT EXECUTE ON FUNCTION public.gofn(integer) TO gofn_grantee WITH GRANT OPTION
```

restored as a plain `GRANT ALL …;` — the grantee lost its ability to re-grant.

A function's `acldefault('f', 10)` grants `EXECUTE` to both the owner and PUBLIC,
so the grant with grant option materializes (PG 18.3):

```
{=X/postgres,postgres=X/postgres,gofn_grantee=X*/postgres}
```

The grantee's `EXECUTE` carries the grant-option `*`. pg_dump's `getFuncs` diffs
`proacl` against `acldefault('f', proowner)` and `buildACLCommands` routes the
grant-option privilege to its `privswgo` branch, emitting a single

```
GRANT ALL ON FUNCTION public.gofn(integer) TO gofn_grantee WITH GRANT OPTION;
```

(EXECUTE is the sole function privilege, so the grantee's full set renders `ALL`;
verified byte-identical against `./postgres/local_install` PG 18.3.)

## Fix

Server-only — the catalog grant-option primitive already exists. The table
grant-option slice 332 added `GrantTablePrivilegeWithGrantOption(relOID, role,
priv, withGrantOption)`, which OR-s a per-(role,priv) grant-option flag that
`renderACLLetters` projects as a trailing `*`. That store and renderer are
object-type-agnostic (routines share the OID-keyed `tableACLs` store), so the fix
is to thread the already-parsed flag through to the function grantee.

`tryRecordTableGrant` (`internal/server/grant_ddl.go`) already parses the trailing
`WITH GRANT OPTION` into a `withGrantOption` bool for the table/schema branches.
`recordFunctionGrant` gains a `withGrantOption bool` parameter (passed from the
`function`/`procedure`/`routine` branches) and records the grantee's `EXECUTE`
with it:

```go
s.cfg.Catalog.GrantTablePrivilege(oid, "PUBLIC", "EXECUTE")        // implicit default — always plain
for _, role := range roles {
    s.cfg.Catalog.GrantTablePrivilegeWithGrantOption(oid, role, "EXECUTE", withGrantOption)
}
```

The implicit owner/PUBLIC default entries stay plain — `acldefault` carries no
grant option, so seeding them with the flag would mis-render the defaults and
break the pg_dump diff. Only the explicit grantee gets the `*`.

`ProcACLText` then projects `{...,gofn_grantee=X*/postgres}` via the shared
`relaclTextLockedFor` core with no function-specific change.

## Scope / non-goals

- Pinned case: `GRANT EXECUTE ON FUNCTION … TO <role> WITH GRANT OPTION`,
  single-statement autocommit.
- Function `REVOKE GRANT OPTION FOR …` (clears only the flag, not the privilege)
  is still routed to the no-op path, as for tables.
- Column-level (`pg_attribute.attacl`, heap re-sync) and database (`datacl`,
  `--create`-only) ACL projection remain open.
- Extended-protocol commit-time deferral stays architecturally entangled.
- Dump-fidelity only — goopg does not enforce function EXECUTE privileges.

## Blast radius

Near-zero. The change adds one bool parameter and swaps the grantee's plain
`GrantTablePrivilege` for `GrantTablePrivilegeWithGrantOption`. A plain function
GRANT (`withGrantOption == false`) lowers to the identical store mutation as
before (`GrantTablePrivilege` already delegates to the WithGrantOption form with
`false`), so slices 345/346/347 render byte-identically.

## Tests / gates

- `internal/catalog/relacl_test.go` new `TestProcACLGrantWithGrantOption`
  (seed PUBLIC plain + grant grantee EXECUTE with grant option →
  `{postgres=X/postgres,grantee_fn=X*/postgres,=X/postgres}`); existing
  `TestProcACLText` / `TestProcACLRevokeFromPublic` / `TestProcACLRevokeFromOwner`
  / `TestRelaclTextGrantOption` PASS.
- `internal/testport` `TestPort_PgDumpConnectionSetup` **DU-002 slice 348**
  asserts the exact `GRANT ALL ON FUNCTION public.gofn(integer) TO gofn_grantee
  WITH GRANT OPTION;` line — byte-identical vs real pg_dump 18.3 (the test drives
  the real pg_dump binary against goopg).
- `internal/server` + `internal/initdb` suites PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.
