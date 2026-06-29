# DU-002 slice 342 — full owner-side `REVOKE ALL ON SCHEMA` → empty `nspacl` array `{}` round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)

## Problem

Slice 341 modelled the full owner-side `REVOKE ALL` for a **table**: the owner
strips every implicit default privilege and `pg_class.relacl` becomes a non-NULL
but **empty** aclitem array `{}` (distinct from the NULL of a never-granted
relation), which pg_dump re-emits as a bare `REVOKE ALL ON TABLE …`. Slice 341
explicitly deferred the **schema** (and sequence) owner-revoke wiring.

PostgreSQL applies the same rule to a namespace. `REVOKE ALL ON SCHEMA s FROM
postgres` strips the owner's implicit default schema privileges (USAGE, CREATE)
and leaves `pg_namespace.nspacl` as `{}`:

```sql
CREATE SCHEMA ownrevall_sch;                              -- nspacl NULL
REVOKE ALL ON SCHEMA ownrevall_sch FROM postgres;
-- pg_namespace.nspacl = {}     (array_length = 0, nspacl IS NOT NULL)
```

`pg_dump`'s `buildACLCommands` diffs `{}` against `acldefault('n', 10)` =
`{postgres=UC/postgres}` and emits a **bare** `REVOKE ALL` with no re-GRANT:

```sql
REVOKE ALL ON SCHEMA ownrevall_sch FROM postgres;
```

Before this slice goopg's schema REVOKE recorder `recordSchemaRevoke`
(`internal/server/grant_ddl.go`, slice 339) modelled only **grantees** — the
owner is implicit in the OID-keyed ACL store, so a `REVOKE … FROM postgres`
found no owner entry to clear and left nspacl NULL. pg_dump diffs NULL against
acldefault and emits nothing → the owner's privilege change was silently lost on
restore (the owner's full default schema privileges came back).

## Fix

Server-only. The catalog primitives are already object-type-agnostic: slice
340's `MaterializeOwnerACL` records the owner's full default set as an explicit
aclitem, and slice 341's `relACLEmptied` empty-array state is keyed by OID and
rendered by the shared `relaclTextLockedFor` (reached for schemas through
`NamespaceACLText`). The only gap was that the schema revoke path never
materialized the owner entry, so there was nothing to empty.

- **server** (`internal/server/grant_ddl.go`): `recordSchemaRevoke` now mirrors
  the table path in `tryRecordTableRevoke` — when the revoked role is the owner
  (`aclOwnerRole` = `"postgres"`) it calls
  `Catalog.MaterializeOwnerACL(oid, "postgres", allSchemaPrivileges)` before the
  per-privilege `RevokeTablePrivilege`. `allSchemaPrivileges` (`{USAGE, CREATE}`)
  is the owner's full default schema set, so a `REVOKE ALL` expands to every
  schema privilege, empties the materialized owner entry, and the catalog records
  `nspacl = {}` via the shared `relACLEmptied` path.

No catalog change.

## Scope / non-goals

- **Empty array only.** As with slice 341, a GRANT to a *non-owner* role after an
  owner REVOKE ALL would in PostgreSQL keep the owner absent/zero
  (`{bob=U/postgres}`) and pg_dump would emit both the `REVOKE ALL` and the
  grantee GRANT; goopg's owner-rendering falls back to the owner's full default
  whenever the owner key is absent from a non-empty array. The persistent
  "owner explicitly zero coexisting with grantees" model remains a follow-up.
- Owner REVOKE ALL on a **sequence** is still not wired (the catalog primitive is
  generic; the server triggers only on the table and schema paths now). The
  sequence branch is a one-line add following the same shape.
- Column-level (`pg_attribute.attacl`) and database (`datacl`, `--create`-only)
  projection remain open.

## Blast radius

Near zero. The new `MaterializeOwnerACL` call fires only on a
`REVOKE … ON SCHEMA … FROM postgres` (previously a silent no-op for the owner)
and is itself a no-op once an owner entry exists or once the array is already
empty (slice 341's early-return). The grantee-revoke and never-granted-schema
paths (every prior slice 335/339 fixture) are unchanged. Privilege *enforcement*
is unaffected (the owner is a superuser, never gated by nspacl).

## Verification

- `go test ./internal/catalog/` — new `TestRevokeAllFromSchemaOwnerEmptyArray`
  (REVOKE ALL ON SCHEMA FROM owner → `{}`; a second owner revoke stays `{}`; an
  unrelated schema stays NULL; an owner re-GRANT clears `{}` →
  `{postgres=U/postgres}`) + all existing `TestNamespaceACLText` /
  `TestRevokeAllFromOwnerEmptyArray` / `TestMaterializeOwnerACL` PASS.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` — **DU-002
  slice 342**: asserts the exact `REVOKE ALL ON SCHEMA ownrevall_sch FROM
  postgres;` line and that no `GRANT … ON SCHEMA ownrevall_sch … TO postgres`
  follows. Byte-identical to real pg_dump 18.3.
- `go build ./...` clean; pgbench TPC-B smoke = pre-commit hook.

## Oracle

`postgres/src/bin/pg_dump/dumputils.c` (`buildACLCommands`),
`src/backend/utils/adt/acl.c` (`acldefault` for `'n'`, `aclitemout`). Behaviour
compared against `postgres/local_install` PG 18.3.
