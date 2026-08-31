# DU-002 slice 345 — function-level GRANT (`pg_proc.proacl`) round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)

## Problem

The table/schema/sequence GRANT slices (331–344) round-trip object ACLs through
`pg_class.relacl` and `pg_namespace.nspacl`. Functions were left out: goopg
projected `pg_proc.proacl = NULL` for every routine, so a function-level GRANT
was silently dropped from the dump.

PostgreSQL stores function privileges in `pg_proc.proacl`. The default ACL for a
function grants `EXECUTE` to **both** the owner and `PUBLIC`:

```sql
SELECT acldefault('f', 10);   -- {=X/postgres,postgres=X/postgres}
```

`=X/postgres` is the implicit `PUBLIC` EXECUTE grant; `postgres=X/postgres` is the
owner's. A grant to a new role materializes the array from this default:

```sql
CREATE FUNCTION public.grantfn(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$;
GRANT EXECUTE ON FUNCTION public.grantfn(integer) TO func_grantee;
-- proacl = {=X/postgres,postgres=X/postgres,func_grantee=X/postgres}
```

pg_dump's `getFuncs` reads `proacl` and `acldefault('f', proowner)` server-side,
then `buildACLCommands` (`dumputils.c`) diffs them client-side and emits, for the
new grantee only (verified byte-identical to pg_dump 18.3):

```sql
GRANT ALL ON FUNCTION public.grantfn(integer) TO func_grantee;
```

`EXECUTE` is the sole function privilege, so a grantee holding it has the object's
full privilege set and pg_dump renders `ALL` rather than `EXECUTE`.

## Design

`pg_proc` is a registered **virtual** table (`internal/initdb/pg_proc_view.go`),
unlike the heap-backed `pg_attribute`. The whole change is therefore a
projection: render `proacl` from the existing OID-keyed ACL store, no heap
re-sync. Three pieces:

1. **Catalog rendering** (`internal/catalog/catalog.go`). Add a function
   privilege order (`EXECUTE` → letter `X`, `functionACLPrivOrder`) and the
   owner-default string `ownerFunctionACLString = "X"`. `ProcACLText(procOID)`
   reuses the object-type-agnostic `relaclTextLockedFor` core (same path that
   renders table relacl, schema nspacl, sequence relacl). Routines share the
   OID-keyed `tableACLs` store; routine OIDs come from a disjoint range
   (`FirstRoutineOID = 1<<17`), so there is no collision with relation/schema
   OIDs.

2. **GRANT recorder** (`internal/server/grant_ddl.go`). `tryRecordTableGrant`
   gains `function`/`procedure`/`routine` branches that call
   `recordFunctionGrant`. It resolves the routine OID via
   `Routines().Lookup(name, argTypes)` (exact name + parsed arg-type signature,
   mirroring COMMENT ON / DROP FUNCTION), falling back to a unique by-name match
   when the arg-type spelling differs from the stored canonical name. A
   paren-aware splitter (`splitFunctionList`) keeps a multi-argument signature
   intact instead of tearing it at the inner comma. For each grant the recorder
   **seeds the implicit `PUBLIC` EXECUTE entry** that `acldefault('f', …)`
   carries, then records the grantee; the owner entry is supplied by the
   renderer's owner branch (`ownerFunctionACLString`). The materialized proacl
   therefore reproduces both default entries plus the grantee, and the
   client-side diff cancels the defaults, leaving exactly the new grant.

3. **View projection** (`internal/initdb/pg_proc_view.go`). User routines now
   project `cat.ProcACLText(r.OID)` for `proacl` instead of the constant `""`.
   Built-in stubs keep `NULL` (no GRANTs).

Rendered order is owner-first then grantees sorted, e.g.
`{postgres=X/postgres,func_grantee=X/postgres,=X/postgres}`. pg_dump parses the
aclitem array into a set, so the order difference vs PostgreSQL's
`{=X/postgres,postgres=X/postgres,func_grantee=X/postgres}` is irrelevant to the
emitted GRANT.

## Scope / non-goals

- **GRANT only.** A function-level `REVOKE` (e.g. `REVOKE EXECUTE … FROM PUBLIC`)
  still routes to the no-op path; the REVOKE recorder bails on `function`. That is
  a separate future slice.
- `WITH GRANT OPTION` on a function grant is not modelled (the common case is a
  plain EXECUTE grant).
- Column-level (`pg_attribute.attacl`, heap re-sync) and database (`datacl`,
  `--create`-only) GRANT projection remain open under M0119-0004.

## Oracle

PostgreSQL 18.3 (`postgres/local_install`): `acldefault('f', 10)`,
`pg_proc.proacl` after the grant, and the `pg_dump --no-sync` output were all
captured against a live server and pinned in the test below.

## Tests

- `TestProcACLText` (`internal/catalog/relacl_test.go`) — pins the proacl
  projection (NULL until first grant; owner + PUBLIC + grantee after; non-EXECUTE
  privileges ignored).
- `TestPort_PgDumpConnectionSetup` (`internal/testport/pgdump_connsetup_test.go`,
  slice 345) — drives the real pg_dump binary against a live goopg server and
  asserts `GRANT ALL ON FUNCTION public.grantfn(integer) TO func_grantee;`.

Gates: `internal/catalog`, `internal/server`, `internal/initdb`, and
`TestPort_PgDumpConnectionSetup` suites PASS; build clean; CI-parity pgbench
smoke PASS.
