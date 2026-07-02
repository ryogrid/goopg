# 0119-0004al — `GRANT … ON SCHEMA` (`nspacl`) round-trip in pg_dump (DU-002 slice 335)

Status: accepted

## Problem

A schema-level grant —

```sql
GRANT USAGE ON SCHEMA grant_sch TO schema_role;
```

must survive a pg_dump/restore round-trip. PostgreSQL stores schema privileges
in `pg_namespace.nspacl` as an `aclitem[]`. pg_dump's `getNamespaces`
(`src/bin/pg_dump/pg_dump.c`) reads `n.nspacl` and the owner baseline, then
parses the array client-side in `buildACLCommands` (`dumputils.c`) and emits the
GRANT/REVOKE diff against `acldefault('n', nspowner)`:

```sql
-- acldefault('n', 10) = {postgres=UC/postgres}
-- nspacl after the grant = {postgres=UC/postgres,schema_role=U/postgres}
GRANT USAGE ON SCHEMA grant_sch TO schema_role;
```

The owner's own entry (`postgres=UC/postgres`) cancels against the baseline, and
schemas grant nothing to PUBLIC by default (`ACL_ALL_RIGHTS_SCHEMA =
USAGE|CREATE`, world default `ACL_NO_RIGHTS`), so the diff is a single
`GRANT USAGE ON SCHEMA grant_sch TO schema_role;`.

goopg lost the privilege two ways:

1. `tryRecordTableGrant` (`internal/server/grant_ddl.go`) listed `schema` in
   `nonTableGrantObjects` and **bailed** — `GRANT … ON SCHEMA` was a pure no-op,
   nothing was recorded.
2. The `pg_namespace` virtual builder (`internal/catalog/catalog.go`)
   hard-coded `nspacl` to the constant `""` (NULL) for every schema, even after
   a GRANT.

So a schema grant was silently lost on dump/restore.

## Fix (dump-fidelity only)

goopg mints schema OIDs from the **same** `nextOID` counter that mints relation
OIDs (`RegisterSchema`), so a schema OID never collides with a relation OID.
That lets schemas reuse the existing OID-keyed ACL store (`tableACLs`) and the
object-type-agnostic renderer `relaclTextLockedFor(oid, privOrder, ownerString)`
introduced in slices 332–333 — only the privilege order and owner-default string
differ.

`internal/catalog/catalog.go`:

- New `schemaACLPrivOrder` = `USAGE('U')`, `CREATE('C')` — the canonical
  `aclitemout` bit order taken from `ACL_ALL_RIGHTS_STR` (`"…UC…"`, so USAGE
  precedes CREATE).
- New `ownerSchemaACLString = "UC"` — matches `acldefault('n', owner)`
  (`ACL_ALL_RIGHTS_SCHEMA = ACL_USAGE|ACL_CREATE`; PUBLIC gets nothing), so the
  owner's own entry cancels and produces no GRANT/REVOKE on round-trip.
- New `InMemory.NamespaceACLText(schemaOID)` takes `c.mu` (read) and delegates
  to `relaclTextLockedFor(schemaOID, schemaACLPrivOrder, ownerSchemaACLString)`,
  returning `""` (NULL) when no privileges have been granted away.
- The `pg_namespace` `VirtualRows` cell (previously the literal `""`) now calls
  `c.NamespaceACLText(s.oid)`. The lock is released before the row-building loop,
  so the per-row `NamespaceACLText` re-lock is safe.

`internal/server/grant_ddl.go`:

- New `allSchemaPrivileges` = `{USAGE, CREATE}` (the `ALL [PRIVILEGES]`
  expansion for a schema).
- `tryRecordTableGrant` strips a leading `SCHEMA` keyword (alongside the
  existing optional `TABLE` / `SEQUENCE`) and dispatches to a new
  `recordSchemaGrant`, which resolves each schema name to its OID via the
  existing `Catalog.SchemaOID(name)` and records the privileges under that OID
  with `GrantTablePrivilegeWithGrantOption`. Unknown schemas / empty privilege
  lists are skipped, leaving the statement a successful no-op (as before).

## Why this is safe

- The ACL store and `HasTablePrivilege` / `truncate-conflict` enforcement path
  are untouched — schemas write to the same map under a non-colliding OID, and
  only the *rendering* (privilege order + owner string) differs from tables.
- System schemas (`pg_catalog`, `public`, …) never have a recorded grant, so
  `NamespaceACLText` returns `""` for them — `nspacl` stays NULL exactly as
  before. The special PG-15+ `public`-schema default ACL is not modelled and is
  not needed here (goopg already emitted NULL for it).
- A schema with no recorded grant projects exactly as before → byte-identical
  existing output → zero blast radius.
- The grant-option `*` logic (slice 332) and the PUBLIC empty-grantee mapping
  (slice 334) are in the shared core, so `GRANT … ON SCHEMA … WITH GRANT OPTION`
  and `GRANT … ON SCHEMA … TO PUBLIC` round-trip too.

## Tests / gates

- `TestNamespaceACLText` (`internal/catalog/relacl_test.go`): NULL with no
  grants; `GRANT USAGE` → `{postgres=UC/postgres,schema_role=U/postgres}`;
  multi-priv canonical ordering `UC` with a table-only privilege (`SELECT`)
  dropped; grant-option `CREATE` → `UC*`; `TO PUBLIC` → empty grantee
  `{postgres=UC/postgres,=U/postgres}`.
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 335**
  (`internal/testport/pgdump_connsetup_test.go`): `CREATE SCHEMA grant_sch` +
  `CREATE ROLE schema_role` + `GRANT USAGE ON SCHEMA grant_sch TO schema_role`
  → real pg_dump 18.3 emits `GRANT USAGE ON SCHEMA grant_sch TO schema_role;`
  byte-identical.
- `TestRelaclText` / `TestRelaclTextGrantOption` / `TestRelaclTextSequence` /
  `TestRelaclTextPublic` + catalog/server suites + `truncate-conflict`
  isolation PASS; `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

- `src/backend/utils/adt/acl.c` — `aclitemout` (bit-order print via
  `ACL_ALL_RIGHTS_STR`), `acldefault` (`OBJECT_SCHEMA` →
  `ACL_ALL_RIGHTS_SCHEMA`, world default `ACL_NO_RIGHTS`).
- `src/include/utils/acl.h` — `ACL_ALL_RIGHTS_STR = "arwdDxtXUCTcsAm"`,
  `ACL_ALL_RIGHTS_SCHEMA = (ACL_USAGE|ACL_CREATE)`.
- `src/bin/pg_dump/pg_dump.c` (`getNamespaces`/`dumpNamespace`) +
  `src/bin/pg_dump/dumputils.c` (`buildACLCommands`).

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, needs heap re-sync) / database (`datacl`)
GRANT projection; REVOKE-of-default modelling; reserved-keyword-named-role
quoting; extended-protocol commit-time deferral.
