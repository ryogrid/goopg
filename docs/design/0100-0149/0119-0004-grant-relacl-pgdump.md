# 0119-0004 — Table-level GRANT (`relacl`) round-trip in pg_dump (DU-002 slice 331)

Status: accepted

## Problem

A table-level `GRANT` confers privileges on a non-owner role:

```sql
CREATE TABLE public.grant_t (a integer);
CREATE ROLE grantee_role;
GRANT SELECT ON TABLE public.grant_t TO grantee_role;
```

pg_dump must re-emit that `GRANT` so a dump/restore preserves the privilege.
pg_dump's `getTables` (`src/bin/pg_dump/pg_dump.c`) selects the stored ACL
column directly, plus the object's default ACL as a baseline:

```sql
c.relacl,
acldefault(CASE WHEN c.relkind = 'S' THEN 's'::"char" ELSE 'r'::"char" END,
           c.relowner) AS acldefault,
```

It then parses the `aclitem[]` text **client-side** in `buildACLCommands`
(`src/bin/pg_dump/dumputils.c`) — there is no server-side `aclexplode` /
`aclitemout` call — diffs `relacl` against `acldefault`, and emits the GRANT/
REVOKE delta. For a single grantee the delta is one line:

```sql
GRANT SELECT ON TABLE public.grant_t TO grantee_role;
```

(`buildACLCommands` emits `GRANT <privs> ON <type> <nsp>.<name> TO <grantee>;`
with `type = "TABLE"`, `name = fmtId(relname)`, `grantee = fmtId(grantee)`.)

PostgreSQL leaves `pg_class.relacl` **NULL** until the first GRANT, at which
point it materializes the array with the owner's full default privileges first
(grantor = owner) followed by each grantee's entry, e.g.
`{postgres=arwdDxtm/postgres,grantee_role=r/postgres}`. Because that owner entry
is byte-identical to `acldefault('r', 10)`, it cancels in the diff and only the
grantee's GRANT is emitted.

Before this slice goopg **always projected `relacl` as NULL** in its virtual
`pg_class`, even after a GRANT. goopg already *records* table grants in its
catalog ACL store (`Catalog.GrantTablePrivilege`, `internal/server/grant_ddl.go`
— added for the `truncate-conflict` isolation spec, M0118-0008), but that store
was never surfaced into `relacl`, so a granted privilege was silently lost on
dump/restore.

## Design

Project the existing in-memory GRANT store (`InMemory.tableACLs`,
`map[relOID]map[role]map[priv]struct{}`) as the materialized `relacl` text in
the `pg_class` virtual-row builder. No new builtin is required — `acldefault` is
already implemented (DU-002 slice 2) and pg_dump does the rest client-side.

### `relaclTextLocked` (`internal/catalog/catalog.go`)

New unexported method on `InMemory`, called from the `pg_class` `VirtualRows`
builder (which already holds `c.mu.RLock()`):

- Returns `""` (SQL NULL) when no privileges have been granted away for the
  relation — matching PostgreSQL's NULL-until-first-GRANT behavior, so pg_dump
  sees `relacl == acldefault` and emits nothing.
- Otherwise renders the aclitem[] literal: the owner entry
  `postgres=arwdDxtm/postgres` first (goopg's single bootstrap superuser
  `postgres`/OID 10 is every table's owner **and** grantor), then one entry per
  grantee `<role>=<letters>/postgres`.
- Grantee privilege letters are emitted in PostgreSQL's canonical
  `ACL_ALL_RIGHTS_STR` order for relkind `'r'` (`arwdDxtm`,
  `src/include/utils/acl.h`) via the new `tableACLPrivOrder` table, so the text
  matches what `aclitemout` would print and `buildACLCommands` re-emits the
  privilege list in the expected order.
- Grantee roles are emitted in a stable (sorted) order so the projection is
  deterministic across map iterations. (pg_dump emits one GRANT per grantee; the
  relative order of distinct-grantee GRANT lines is not load-bearing.)

`ownerTableACLString = "arwdDxtm"` is the owner's full table-privilege letter
string, equal to `acldefault('r', owner)`, so the owner entry round-trips to no
GRANT/REVOKE.

The `pg_class` regular-table builder's `relacl` cell (previously the literal
`""`) now calls `c.relaclTextLocked(t.OID)`. Every other relkind row (TOAST,
index, sequence, partitioned, composite) keeps its existing `""` — those are
never the target of a modelled table GRANT.

## Scope / non-goals

- **Dump fidelity only.** goopg's runtime privilege enforcement is unchanged:
  `HasTablePrivilege` still drives the `truncate-conflict` spec; this slice only
  *reads* the same store for projection.
- **Table-level GRANT only.** `internal/server/grant_ddl.go` already records only
  `GRANT <privs> ON [TABLE] <tables> TO <roles>` (no column-level, schema/
  database/sequence object classes, role membership, or `TO PUBLIC`); anything it
  declines to record projects as NULL `relacl` and dumps nothing, exactly as
  before. `WITH GRANT OPTION` is not stored, so it is not projected.
- **REVOKE** is still a no-op in the recorder, so a `REVOKE` of a
  default-privilege (e.g. revoking the owner's own rights, which PostgreSQL would
  represent as a shrunk `relacl`) is not modelled.
- **Physical-heap `pg_class` path unaffected.** `internal/executor/`
  `pg18_user_catalog_rows.go` (the heap row a PG *standby* reads) still writes an
  empty `{}` relacl: GRANT is a runtime-only catalog mutation and goopg does no
  runtime in-place update of on-disk shared catalogs (see the standing memo
  *No runtime in-place update for on-disk shared catalogs*). The two paths serve
  different consumers — pg_dump/psql read the virtual builder.

## Zero blast radius

A relation with no recorded grant projects `relacl` exactly as before (`""`), so
every existing `pg_class` row — and thus every existing pg_dump output and
isolation/regress expectation — is byte-identical. The new text appears only for
a relation that has actually been GRANTed away.

## Testing

- `internal/catalog/relacl_test.go` — `TestRelaclText`: NULL with no grants;
  single SELECT → `{postgres=arwdDxtm/postgres,grantee_role=r/postgres}`;
  multi-privilege canonical ordering (`INSERT|UPDATE` → `arw`); two grantees sort
  deterministically with the owner entry at the head; `DropTableACL` reverts to
  NULL.
- `internal/testport/pgdump_connsetup_test.go` — **DU-002 slice 331** added to
  `TestPort_PgDumpConnectionSetup`: `grant_t` + `CREATE ROLE grantee_role` +
  `GRANT SELECT ON TABLE public.grant_t TO grantee_role` re-emits
  `GRANT SELECT ON TABLE public.grant_t TO grantee_role;` **byte-identical vs
  real pg_dump 18.3**.
- catalog / server / executor suites PASS; `go build ./...` clean; pgbench smoke
  via the pre-commit hook.

## Oracle

- `postgres/src/bin/pg_dump/pg_dump.c` — `getTables` (`c.relacl` + `acldefault`),
  `dumpTableACL` (`objtype = "TABLE"`, `namecopy = fmtId(name)`).
- `postgres/src/bin/pg_dump/dumputils.c` — `buildACLCommands` /
  `parseAclItem` (client-side aclitem[] parse + GRANT emission).
- `postgres/src/include/utils/acl.h` — `ACL_ALL_RIGHTS_STR` privilege-letter
  order.

## Still open under M0119-0004

- Column-level / sequence / schema / database GRANT projection; `WITH GRANT
  OPTION`; REVOKE-of-default modelling.
- Reserved-keyword-named-role quoting (needs a keyword table).
- Extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement).
