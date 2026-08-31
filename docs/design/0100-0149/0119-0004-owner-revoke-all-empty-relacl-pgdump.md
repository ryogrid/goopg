# DU-002 slice 341 — full owner-side `REVOKE ALL` → empty `relacl` array `{}` round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)

## Problem

Slice 340 modelled a *single-privilege* owner-side REVOKE: `REVOKE TRIGGER … FROM
postgres` materializes `pg_class.relacl` as the owner default minus the revoked
bit. It explicitly deferred the **full** REVOKE.

PostgreSQL treats a full owner REVOKE distinctly. `REVOKE ALL` strips every one
of the owner's implicit default privileges and leaves relacl as a non-NULL but
**empty** aclitem array `{}` — distinct from the NULL of a never-granted table:

```sql
CREATE TABLE public.ownrevall_t (a integer, b text);   -- relacl NULL
REVOKE ALL ON TABLE public.ownrevall_t FROM postgres;
-- pg_class.relacl = {}     (array_length = 0, relacl IS NOT NULL)
```

`pg_dump`'s `buildACLCommands` diffs `{}` against `acldefault('r', 10)` and emits
a **bare** `REVOKE ALL` with no re-GRANT (verified byte-identical to pg_dump
18.3):

```sql
REVOKE ALL ON TABLE public.ownrevall_t FROM postgres;
```

Before this slice goopg's `RevokeTablePrivilege` dropped the owner's now-empty
entry and then the whole relation entry, returning relacl to NULL. pg_dump diffs
NULL against acldefault and emits nothing → the owner's privilege change was
silently lost on restore (the owner's full default privileges came back).

## Fix

Server recording is unchanged (the slice-340 owner-revoke path already calls
`MaterializeOwnerACL` then `RevokeTablePrivilege` for each privilege; `REVOKE
ALL` simply expands to every table privilege). The change is in the catalog: it
must record and render the "materialized-then-emptied" state.

- **catalog** (`internal/catalog/catalog.go`): new field `relACLEmptied
  map[uint32]bool` records relations whose ACL array was explicitly emptied by an
  owner REVOKE ALL.
  - `RevokeTablePrivilege`: when the relation's *last* remaining aclitem is the
    owner's own entry and it is removed (`role == aclOwnerRole` and `byRole`
    becomes empty), set `relACLEmptied[relOID]`. A trailing *grantee* revoke
    keeps the slice-338 behaviour (relacl returns to NULL).
  - `relaclTextLockedFor`: when `byRole` is empty, return `"{}"` if
    `relACLEmptied[relOID]` is set, else `""` (NULL) as before.
  - `GrantTablePrivilege` / `DropTableACL`: clear `relACLEmptied[relOID]` (a new
    grant re-materializes real aclitems; a dropped relation forgets the state).
  - `MaterializeOwnerACL`: early-return if `relACLEmptied[relOID]` is set, so a
    second owner-side REVOKE after the array is already empty does not resurrect
    the owner's privileges.

Because the empty-array state is OID-keyed and rendered by the shared
`relaclTextLockedFor`, it is object-type-agnostic; only the table/sequence
owner-revoke path is wired in the server (matching slice 340).

## Scope / non-goals

- **Empty array only.** A GRANT to a *non-owner* role after an owner REVOKE ALL
  is, in PostgreSQL, `{bob=r/postgres}` — the owner entry stays absent (zero
  privileges) and pg_dump emits *both* `REVOKE ALL … FROM postgres;` and the
  grantee GRANT. goopg's owner-rendering model falls back to the owner's full
  default whenever the owner key is absent from a non-empty array, so it would
  render `{postgres=arwdDxtm/postgres,bob=r/postgres}` and drop the owner REVOKE.
  Modelling a persistent "owner explicitly zero" entry that coexists with
  grantees is a follow-up (it requires the owner aclitem to survive as an
  empty-but-present entry rather than being dropped).
- Owner REVOKE ALL on a **schema** / **sequence** is not wired (the catalog
  primitive is generic; the server triggers only on the table path).
- Column-level (`attacl`) and database (`datacl`) projection remain open.

## Blast radius

Near zero. `relACLEmptied` is only ever set on the final removal of an owner's
own aclitem — a state that previously reverted to NULL and emitted nothing. The
NULL path (never-granted relations, every prior slice 331–340 fixture) is
unchanged: `relaclTextLockedFor` still returns `""` unless the emptied flag is
set. Privilege *enforcement* is unaffected (the owner is a superuser and is never
gated by relacl).

## Verification

- `go test ./internal/catalog/` — new `TestRevokeAllFromOwnerEmptyArray`
  (REVOKE ALL FROM owner → `{}`; a second owner revoke stays `{}`; an unrelated
  relation stays NULL; an owner re-GRANT clears `{}` → `{postgres=r/postgres}`) +
  all existing `TestRelaclText*` / `TestRevoke*` / `TestMaterializeOwnerACL` /
  `TestNamespaceACLText` PASS.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` — **DU-002
  slice 341**: asserts the exact `REVOKE ALL ON TABLE public.ownrevall_t FROM
  postgres;` line and that no `GRANT … ownrevall_t … TO postgres` follows.
  Byte-identical to real pg_dump 18.3 (ground truth captured from a fresh PG 18.3
  `initdb`).
- `go build ./...` clean; pgbench TPC-B smoke = pre-commit hook.

## Oracle

`postgres/src/bin/pg_dump/dumputils.c` (`buildACLCommands`),
`src/backend/utils/adt/acl.c` (`acldefault`, `aclitemout`). Behaviour compared
against `postgres/local_install` PG 18.3.
