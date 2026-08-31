# DU-002 slice 344 — owner-zero coexisting with a grantee (`{grantee=…/postgres}`) round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)

## Problem

Slices 341/342/343 modelled the *empty* ACL array `{}` produced by a full
owner-side `REVOKE ALL … FROM postgres` (table / schema / sequence). They
explicitly deferred the case where a grantee is added *after* the owner is
zeroed.

PostgreSQL keeps the owner at zero (absent from the array) once it has revoked
all of its implicit default privileges, even when later grants re-materialize the
array with grantee entries:

```sql
CREATE TABLE public.ownerzero_t (a integer);          -- relacl NULL
REVOKE ALL ON TABLE public.ownerzero_t FROM postgres;  -- relacl = {}
GRANT SELECT ON TABLE public.ownerzero_t TO bob;       -- relacl = {bob=r/postgres}
```

The owner has **no** aclitem — `relacl = {bob=r/postgres}`, not
`{postgres=arwdDxtm/postgres,bob=r/postgres}`. pg_dump's `buildACLCommands`
(`dumputils.c`) diffs that against `acldefault('r', 10)` =
`{postgres=arwdDxtm/postgres}` and emits **both** the owner's `REVOKE ALL` and the
grantee `GRANT` (verified byte-identical to pg_dump 18.3):

```sql
REVOKE ALL ON TABLE public.ownerzero_t FROM postgres;
GRANT SELECT ON TABLE public.ownerzero_t TO bob;
```

Before this slice goopg cleared the `relACLEmptied` flag on *any* GRANT and the
renderer always inserted the owner's full default when the owner key was absent
from a non-empty array. So goopg rendered
`{postgres=arwdDxtm/postgres,bob=r/postgres}`: pg_dump saw the owner holding its
full default, diffed clean, and **dropped** the `REVOKE ALL` — silently restoring
the owner's default privileges on restore.

## Fix

Catalog-only (server recording unchanged — the existing grant/revoke recorders
already call `GrantTablePrivilege`/`MaterializeOwnerACL`/`RevokeTablePrivilege`).
The `relACLEmptied` flag is re-interpreted from "the array is the empty `{}`
array" to the slightly broader **"the owner explicitly holds zero privileges
(absent from the rendered array)"**, which subsumes the `{}` case and now also
survives a grantee being added:

- **`GrantTablePrivilegeWithGrantOption`** (`internal/catalog/catalog.go`): only
  clear `relACLEmptied[relOID]` when the grant target is the owner
  (`role == aclOwnerRole`). A GRANT to a *grantee* leaves the owner zeroed.
- **`relaclTextLockedFor`**: when `relACLEmptied[relOID]` is set, suppress the
  leading `postgres=<default>/postgres` owner entry entirely; render only the
  grantee items. (When `byRole` is empty and the flag is set, the existing
  early-return still renders `{}`.)

The renderer invariant holds because `MaterializeOwnerACL` early-returns while the
flag is set, so the owner is never in `byRole` when zeroed; an owner-side GRANT
clears the flag *before* inserting the owner entry, so the two are never
inconsistent. The flag is OID-keyed and rendered by the shared
`relaclTextLockedFor`, so the behaviour is object-type-agnostic (tables, schemas
via `NamespaceACLText`, sequences via `relaclTextLockedSeq` all inherit it).

## Scope / non-goals

- Column-level (`pg_attribute.attacl`, heap re-sync) and database (`datacl`,
  `--create`-only) GRANT projection remain open.
- Extended-protocol commit-time deferral stays architecturally entangled.

## Blast radius

Near zero. The renderer change only fires when `relACLEmptied[relOID]` is set —
a state reachable only via a full owner-side `REVOKE ALL`. Every prior slice
331–343 fixture either never sets the flag (the owner holds its default or a
partial-revoke entry) or sets it with an empty `byRole` (rendered `{}` by the
unchanged early-return). The grant-clears-the-flag change is narrowed from
unconditional to owner-only, so a grantee GRANT after owner REVOKE ALL — the only
newly-affected path — now correctly keeps the owner absent. Privilege
*enforcement* is unaffected (the owner is a superuser, never gated by relacl).

## Verification

- `go test ./internal/catalog/` — new `TestRevokeAllFromOwnerThenGrantGrantee`
  (REVOKE ALL FROM owner then GRANT SELECT TO bob → `{bob=r/postgres}`; a second
  grantee priv → `{bob=rw/postgres}`; revoking the grantee back out → `{}` with
  the owner still zeroed; a later owner re-GRANT then grantee →
  `{postgres=r/postgres,bob=a/postgres}`) + all existing
  `TestRelaclText*` / `TestRevokeAllFrom*EmptyArray` / `TestMaterializeOwnerACL` /
  `TestNamespaceACLText` PASS (the slice-341 test's parenthetical is updated to
  point here).
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` — **DU-002
  slice 344**: asserts both the exact `REVOKE ALL ON TABLE public.ownerzero_t FROM
  postgres;` and `GRANT SELECT ON TABLE public.ownerzero_t TO bob;` lines appear,
  and that no privilege is re-granted to the zeroed owner. Byte-identical to real
  pg_dump 18.3.
- `go build ./...` clean; pgbench TPC-B smoke = pre-commit hook.

## Oracle

`postgres/src/bin/pg_dump/dumputils.c` (`buildACLCommands`),
`src/backend/utils/adt/acl.c` (`acldefault`, `aclitemout`). Behaviour compared
against `postgres/local_install` PG 18.3.
