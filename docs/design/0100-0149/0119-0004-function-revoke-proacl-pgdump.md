# DU-002 slice 346 — function-level REVOKE (`pg_proc.proacl`) round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)

## Problem

Slice 345 made a function-level **GRANT** round-trip through `pg_proc.proacl`, but
explicitly left a function-level **REVOKE** on the no-op path (the REVOKE recorder
bailed on the `function` object class). The most common real-world function ACL
mutation is `REVOKE EXECUTE … FROM PUBLIC` — it appears in essentially every
pg_dump of a hardened schema — so goopg silently dropped it, re-granting PUBLIC's
default EXECUTE on restore (a real privilege-escalation drift, not just cosmetic).

A function's implicit default proacl grants `EXECUTE` to **both** the owner and
`PUBLIC`:

```sql
SELECT acldefault('f', 10);   -- {=X/postgres,postgres=X/postgres}
```

`=X/postgres` is the implicit PUBLIC EXECUTE grant; `postgres=X/postgres` the
owner's. PostgreSQL leaves `proacl` NULL until the first GRANT/REVOKE
materializes the array. Revoking PUBLIC's default EXECUTE on a never-granted
routine therefore materializes proacl with the owner only:

```sql
CREATE FUNCTION public.revokefn(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$;
REVOKE EXECUTE ON FUNCTION public.revokefn(integer) FROM PUBLIC;
-- proacl = {postgres=X/postgres}
```

pg_dump's `getFuncs` reads `proacl` and `acldefault('f', proowner)` server-side,
then `buildACLCommands` (`dumputils.c`) diffs them client-side and emits (verified
byte-identical to pg_dump 18.3):

```sql
REVOKE ALL ON FUNCTION public.revokefn(integer) FROM PUBLIC;
```

`EXECUTE` is the sole function privilege, so the difference (PUBLIC lost EXECUTE)
renders as `REVOKE ALL … FROM PUBLIC`.

## Design

`pg_proc` is a registered **virtual** table, so this is a pure projection change
plus a server-side recorder — no heap re-sync. Server-only; the catalog
primitives are already in place from slices 340/345.

`tryRecordTableRevoke` (`internal/server/grant_ddl.go`) gains
`function`/`procedure`/`routine` branches mirroring the GRANT recorder's, each
dispatching to the new `recordFunctionRevoke`. That function:

1. Resolves each routine signature to its `pg_proc` OID via the shared
   `lookupFunctionOID` (exact name + parsed arg-type signature, unique-by-name
   fallback) and the paren-aware `splitFunctionList`.
2. **Materializes the owner's implicit default EXECUTE first** via the
   type-agnostic `MaterializeOwnerACL(oid, "postgres", ["EXECUTE"])` (slice 340).
   This seeds an explicit owner aclitem so proacl renders the surviving owner
   EXECUTE once any privilege is revoked. The PUBLIC half of the default is
   implicit and never stored, so revoking it is simply an omission.
3. Calls `RevokeTablePrivilege(oid, role, "EXECUTE")` for each role. For `FROM
   PUBLIC` the catalog lower-cases `PUBLIC` to the reserved `public` pseudo-role
   (the same key the GRANT recorder seeds); since PUBLIC was never explicitly
   stored on a never-granted routine, the revoke is a no-op against an absent
   entry and the owner-only array `{postgres=X/postgres}` is what `ProcACLText`
   renders.

The renderer (`relaclTextLockedFor` with `functionACLPrivOrder` /
`ownerFunctionACLString`) renders the owner entry from `byRole[postgres]` once it
is materialized, so the projected proacl is `{postgres=X/postgres}`, matching
PostgreSQL.

The recorder generalizes to a grantee revoke as well (e.g. GRANT to bob then
REVOKE from bob): after materializing the owner and removing bob, proacl equals
acldefault as a set and pg_dump emits nothing — the correct round-trip.

## Scope / non-goals

- This slice's pinned case is `REVOKE EXECUTE … FROM PUBLIC` on a never-granted
  routine. An owner-side `REVOKE EXECUTE … FROM postgres` empties proacl to `{}`
  via the shared `relACLEmptied` path (slice 341) — exercised by the generalized
  code but not separately pinned here.
- Explicit-transaction REVOKE still falls through to the executor no-op (the
  recorder intercepts only single-statement autocommit REVOKE, like the GRANT
  side).
- `WITH GRANT OPTION` on functions, column-level (`pg_attribute.attacl`), and
  database (`datacl`, `--create`-only) ACL projection remain open under
  M0119-0004.

## Oracle

PostgreSQL 18.3 (`postgres/local_install`): `acldefault('f', 10)`,
`pg_proc.proacl` after the REVOKE (`{postgres=X/postgres}`), and the
`pg_dump --no-sync` output (`REVOKE ALL ON FUNCTION public.revokefn(integer) FROM
PUBLIC;`) were captured against a live server and pinned in the test below.

## Tests

- `TestProcACLRevokeFromPublic` (`internal/catalog/relacl_test.go`) — pins the
  catalog primitive composition (materialize owner default + revoke PUBLIC →
  `{postgres=X/postgres}`).
- `TestPort_PgDumpConnectionSetup` (`internal/testport/pgdump_connsetup_test.go`,
  slice 346) — drives the real pg_dump binary against a live goopg server and
  asserts `REVOKE ALL ON FUNCTION public.revokefn(integer) FROM PUBLIC;`.

Gates: `internal/catalog`, `internal/server`, `internal/initdb`, and
`TestPort_PgDumpConnectionSetup` suites PASS; build clean; CI-parity pgbench
smoke PASS.
