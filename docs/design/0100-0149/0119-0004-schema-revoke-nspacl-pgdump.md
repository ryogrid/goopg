# 0119-0004 — `GRANT … ON SCHEMA` then partial `REVOKE` (`nspacl`) round-trip in pg_dump (DU-002 slice 339)

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)
Date: 2026-06-30

## Problem

A schema GRANT followed by a partial REVOKE must round-trip through pg_dump.
This is the `nspacl` analogue of slice 338 (which covered table `relacl`):

```sql
CREATE SCHEMA revoke_sch;
CREATE ROLE revoke_sch_role;
GRANT USAGE, CREATE ON SCHEMA revoke_sch TO revoke_sch_role;
REVOKE CREATE ON SCHEMA revoke_sch FROM revoke_sch_role;
```

PostgreSQL clears the named bits from the grantee's aclitem mask:
`pg_namespace.nspacl` becomes `{postgres=UC/postgres,revoke_sch_role=U/postgres}`
(the lone `USAGE`). pg_dump's `getNamespaces` reads `n.nspacl`, and
`buildACLCommands` (`dumputils.c`) diffs it against
`acldefault('n', nspowner)` = `{postgres=UC/postgres}` client-side, so
`dumpACL` (objtype `SCHEMA`) re-emits only:

```sql
GRANT USAGE ON SCHEMA revoke_sch TO revoke_sch_role;
```

— **not** the revoked `CREATE`.

goopg's REVOKE recorder `tryRecordTableRevoke` (`internal/server/grant_ddl.go`,
slice 338) modelled only table/sequence `relacl`: `schema` is in
`nonTableGrantObjects`, so an `ON SCHEMA` REVOKE hit the non-table bail and was a
pure no-op. The earlier `GRANT … ON SCHEMA` had already materialized
`nspacl` with both `USAGE` and `CREATE` (slice 335), so the revoked `CREATE`
survived in `nspacl` and the dump over-emitted `GRANT CREATE, USAGE ON SCHEMA
revoke_sch TO revoke_sch_role;` — re-introducing the revoked privilege on restore
(silent ACL drift, exactly the failure slice 338 fixed for tables).

## Fix (dump-fidelity + ACL-store correctness)

Schemas already share the OID-keyed ACL store (`tableACLs`) and the
object-type-agnostic renderer `relaclTextLockedFor` with relations
(slice 335 keys schema grants by the `pg_namespace` OID, which never collides
with a relation OID because both are minted from the same `nextOID` counter).
The catalog method `RevokeTablePrivilege(oid, role, priv)` (slice 338) therefore
already does the correct thing for a schema OID — it removes the bit, drops the
grantee entry when its privilege set empties, and removes the whole `tableACLs`
entry when no grantees remain (`nspacl` back to NULL → pg_dump emits nothing).
**No catalog change is needed.**

The only gap is server-side dispatch. `internal/server/grant_ddl.go`:

- `tryRecordTableRevoke` gains an `ON SCHEMA` branch that mirrors the grant
  recorder's existing `ON SCHEMA` branch (slice 335): it strips the leading
  `SCHEMA` keyword and dispatches to a new `recordSchemaRevoke`, returning before
  the non-table bail.
- new `recordSchemaRevoke(objPart, rolePart, privPart)` is the mirror of
  `recordSchemaGrant`: it expands the privilege list against `allSchemaPrivileges`
  (`{USAGE, CREATE}`), resolves each schema to its OID via `Catalog.SchemaOID`
  (unknown schemas / empty priv lists skipped → still a successful no-op), and
  calls `Catalog.RevokeTablePrivilege(oid, role, priv)` for each (schema, role,
  privilege) triple.

## Blast radius

Zero. `RevokeTablePrivilege` only ever removes privilege bits already present, so
`HasTablePrivilege` / `truncate-conflict` enforcement can only become more
correct, never falsely grant. A schema GRANT with no following REVOKE renders
`nspacl` byte-identically to slice 335. System schemas never carry a recorded
grant → their `nspacl` stays NULL. The change is confined to the autocommit
single-statement REVOKE recorder; an explicit-transaction REVOKE still falls
through to the executor's unchanged no-op path.

## Gates

- `go test ./internal/catalog/ -run 'TestRevokeTablePrivilege|Relacl'` PASS
  (`RevokeTablePrivilege` already exercised; reused unchanged for schema OIDs).
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 339** — `revoke_sch` +
  `CREATE ROLE revoke_sch_role` + `GRANT USAGE, CREATE ON SCHEMA … TO …` +
  `REVOKE CREATE ON SCHEMA … FROM …` → pg_dump emits `GRANT USAGE ON SCHEMA
  revoke_sch TO revoke_sch_role;` and **NO** `CREATE ON SCHEMA revoke_sch TO
  revoke_sch_role`; byte-identical vs real pg_dump 18.3. PASS.
- `go build ./...` clean; pgbench TPC-B smoke = pre-commit hook.

## Oracle

- `postgres/src/bin/pg_dump/dumputils.c` — `buildACLCommands` / `acldefault`
  diff (client-side aclitem parse).
- `postgres/src/bin/pg_dump/pg_dump.c` — `getNamespaces` (reads `n.nspacl`),
  `dumpACL` (objtype `SCHEMA`).
- `postgres/src/backend/utils/adt/acl.c` — `aclitemout` / REVOKE mask clearing.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, heap re-sync) / database (`datacl`, only
dumped under `--create`) GRANT projection; REVOKE-of-default (owner-side
implicit-privilege) modelling; extended-protocol commit-time deferral
(architecturally entangled — see the per-statement autocommit model).
