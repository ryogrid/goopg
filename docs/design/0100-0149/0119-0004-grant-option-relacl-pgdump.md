# 0119-0004 — `GRANT … WITH GRANT OPTION` (`relacl` `*`) round-trip in pg_dump (DU-002 slice 332)

Status: accepted

## Problem

A `GRANT … WITH GRANT OPTION` lets the grantee further grant the privilege to
others:

```sql
CREATE TABLE public.grant_g (a integer);
CREATE ROLE grantee2_role;
GRANT SELECT ON TABLE public.grant_g TO grantee2_role WITH GRANT OPTION;
```

PostgreSQL records the grant option in `pg_class.relacl` by suffixing the
privilege letter with `*` — `aclitemout` renders the grantee entry as
`grantee2_role=r*/postgres` (vs `r` for a plain SELECT grant).

pg_dump's `buildACLCommands` (`src/bin/pg_dump/dumputils.c`) parses the
`aclitem[]` text **client-side** and splits each grantee's privileges into two
buckets: `privs` (no grant option) and `privswgo` (with grant option). The
`privswgo` bucket is emitted as a dedicated statement with the clause appended:

```sql
GRANT SELECT ON TABLE public.grant_g TO grantee2_role WITH GRANT OPTION;
```

Slice 331 surfaced the GRANT store into `relacl` but **dropped the grant
option**: `internal/server/grant_ddl.go` stripped a trailing `WITH GRANT OPTION`
clause off the role list and never recorded it, and the catalog ACL store had no
place to keep it. So a `WITH GRANT OPTION` grant round-tripped as a plain GRANT,
silently losing the grantee's ability to re-grant.

## Design

Track a per-(role, privilege) grant-option flag in the existing in-memory ACL
store and render the `*` in `relacl`. No new builtin — pg_dump does the
`privs`/`privswgo` split client-side.

### Catalog ACL store (`internal/catalog/catalog.go`)

The store's inner value changes from a set to a flag:

```
tableACLs map[uint32]map[string]map[string]struct{}   // before
tableACLs map[uint32]map[string]map[string]bool        // after — bool = grant option
```

`map[string]bool` is a drop-in for the set-membership reads (`_, ok := privs[priv]`
in `HasTablePrivilege` and `relaclTextLocked`), so the runtime-enforcement path
(`truncate-conflict`) is unaffected.

New interface method `GrantTablePrivilegeWithGrantOption(relOID, role, priv,
withGrantOption bool)`; the existing `GrantTablePrivilege` delegates to it with
`false`, so the three-arg callers and tests are unchanged. The flag is **OR-ed**
in (`privs[priv] = privs[priv] || withGrantOption`): once a privilege carries the
option, a later plain GRANT does not clear it, matching PostgreSQL, which retains
the grant option until `REVOKE GRANT OPTION FOR`.

`relaclTextLocked` now reads the bool: after writing the privilege letter in
canonical `ACL_ALL_RIGHTS_STR` order, it appends `*` when the flag is set
(`r` → `r*`, `ar` → `ar*`).

### GRANT recorder (`internal/server/grant_ddl.go`)

`tryRecordTableGrant` already split off a trailing `WITH …` clause from the role
list. It now inspects that clause: when the tail is exactly `grant option` it
sets `withGrantOption = true`, then calls
`GrantTablePrivilegeWithGrantOption(..., withGrantOption)`. `GRANTED BY` and any
other `WITH` tail are still stripped and recorded as plain grants (unchanged).

## Scope / non-goals

- **Dump fidelity only.** Runtime privilege enforcement is unchanged — goopg
  does not check the grant option when a role itself issues a GRANT.
- **Table-level GRANT only**, as in slice 331. Column-level / schema / database /
  sequence grants and `TO PUBLIC` are still left to the no-op path and project
  NULL `relacl`.
- **REVOKE GRANT OPTION FOR** is not modelled (the recorder has no REVOKE path).
- **Physical-heap `pg_class` path unaffected** — the standby-facing heap row
  still writes empty `{}` relacl (runtime-only catalog mutation; no in-place
  on-disk shared-catalog update).

## Zero blast radius

A relation with no recorded grant still projects `relacl` as `""`; a plain GRANT
(no option) renders exactly as in slice 331 (the flag defaults `false`, no `*`
appended). The `*` appears only for a privilege actually granted WITH GRANT
OPTION, so every existing pg_dump / isolation / regress expectation is
byte-identical.

## Testing

- `internal/catalog/relacl_test.go` — `TestRelaclTextGrantOption`: SELECT WITH
  GRANT OPTION → `{postgres=arwdDxtm/postgres,g_role=r*/postgres}`; a mixed plain
  INSERT keeps canonical order and retains the SELECT option (`ar*`); a redundant
  plain re-GRANT of SELECT does not clear the option.
- `internal/testport/pgdump_connsetup_test.go` — **DU-002 slice 332** added to
  `TestPort_PgDumpConnectionSetup`: `grant_g` + `CREATE ROLE grantee2_role` +
  `GRANT SELECT ON TABLE public.grant_g TO grantee2_role WITH GRANT OPTION`
  re-emits `GRANT SELECT ON TABLE public.grant_g TO grantee2_role WITH GRANT
  OPTION;` **byte-identical vs real pg_dump 18.3**.
- catalog / server / executor suites PASS; `truncate-conflict` isolation spec
  (ACL-store consumer) PASS; `go build ./...` clean; pgbench smoke via the
  pre-commit hook.

## Oracle

- `postgres/src/bin/pg_dump/dumputils.c` — `buildACLCommands` (the `privs` vs
  `privswgo` split and the ` WITH GRANT OPTION;\n` emission), `parseAclItem`
  (client-side `<letter>*` parse).
- `postgres/src/include/utils/acl.h` — `ACL_ALL_RIGHTS_STR` letter order;
  `ACL_GRANT_OPTION_FOR` semantics.

## Still open under M0119-0004

- Column-level / sequence / schema / database GRANT projection; REVOKE-of-default
  modelling.
- Reserved-keyword-named-role quoting (needs a keyword table).
- Extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement).
