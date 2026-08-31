# 0119-0004 — Named-role `CREATE POLICY ... TO <role>` round-trip in pg_dump (DU-002 slice 330)

Status: accepted

## Problem

A row-level-security policy can restrict its grantees to one or more named
roles:

```sql
CREATE ROLE pol_role;
CREATE POLICY p_role ON public.pol_rt FOR SELECT TO pol_role USING (a > 0);
```

pg_dump's `getPolicies` (`src/bin/pg_dump/pg_dump.c`) reads `pg_policy` and,
for the `polroles` column, resolves the OID array back to role names with:

```sql
CASE WHEN pol.polroles = '{0}' THEN NULL ELSE
  pg_catalog.array_to_string(
    ARRAY(SELECT pg_catalog.quote_ident(rolname)
          FROM pg_catalog.pg_roles WHERE oid = ANY(pol.polroles)), ', ')
END AS polroles
```

`dumpPolicy` then emits ` TO <names>` between the `FOR <cmd>` clause and
`USING (...)` (order: `ON … [AS RESTRICTIVE][FOR cmd][TO roles][USING][WITH
CHECK]`). The `'{0}'` sentinel is PUBLIC and maps to a NULL → omitted TO clause.

Before this slice goopg had **no per-role OID registry**: `CREATE ROLE`
recorded only the role *name* (`map[string]struct{}`), `pg_roles` exposed solely
the seeded `postgres` superuser, and `pg_policy.polroles` could hold only the
`{0}` PUBLIC sentinel. `execCreatePolicy` therefore *rejected* any named role
(`0A000 … not yet supported`). Named-role policies could not be created at all,
let alone dumped — the prior slices (323–325) covered only PUBLIC policies.

A second, latent bug surfaced once the registry existed: goopg's `quote_ident`
SQL function **unconditionally** wrapped its argument in double quotes, so the
getPolicies query above rendered ` TO "pol_role"` where real PostgreSQL emits the
bare ` TO pol_role`. PG's `quote_identifier` only quotes when the identifier
would not survive a re-parse unquoted (uppercase, special chars, leading digit,
empty, or a reserved keyword).

## Fix

Dump-fidelity only — goopg enforces no row-level security; the policy and its
role list are recorded purely so they round-trip through pg_dump.

1. **Per-role OID registry** (`internal/catalog/catalog.go`). `InMemory.roles`
   changes from `map[string]struct{}` to `map[string]uint32` (lower-cased name →
   OID). `RegisterRole` mints a fresh OID from the running catalog counter the
   first time a name is seen and is idempotent on re-registration (so a policy's
   `polroles` entry stays valid for the session). New `RoleOID(name) (uint32,
   bool)` on the `Catalog` interface resolves a registered role, special-casing
   the seeded `postgres` superuser to OID 10 (`BOOTSTRAP_SUPERUSERID`).

2. **pg_roles exposure**. The `pg_roles` virtual `VirtualRows` now emits the
   seeded `postgres` row plus every registered role (sorted by name) with its
   minted OID, so the getPolicies subquery resolves `oid = ANY(polroles)` back to
   the role name.

3. **execCreatePolicy** (`internal/executor/operators_ddl.go`) resolves each
   `TO` role name to its OID via `Catalog.RoleOID`, raising `42704 role "<name>"
   does not exist` for an unknown role (matching PG, which requires the role to
   exist at CREATE POLICY time). A `PUBLIC` element anywhere — or an empty role
   list — collapses to the `{0}` sentinel (PUBLIC subsumes every named grantee).
   The OID array lands in `pg_policy.polroles` via the existing projection.

4. **quote_ident correctness** (`internal/executor/expr.go`). The `quote_ident`
   builtin now delegates to the existing `pgQuoteIdent` helper (the same
   conditional-quoting logic used elsewhere) instead of always quoting, so a
   plain lowercase identifier is returned bare — byte-identical to PG's
   `quote_identifier` for the common case. (Reserved-keyword-named roles are not
   yet quoted; that matches the pre-existing `pgQuoteIdent` behaviour and is a
   separate follow-up.)

The existing pg_policy projection already formatted a multi-OID `polroles`
array, and goopg's planner/executor already supported the
`ARRAY(SELECT … WHERE oid = ANY(arr))` / `array_to_string` / `quote_ident`
query stack the getPolicies SQL relies on (the PUBLIC fixtures exercised the
`CASE … = '{0}'` short-circuit). The only missing pieces were the registry, the
pg_roles exposure, and the quote_ident fix.

## Blast radius

- PUBLIC policies (slices 323–325) are byte-unchanged: an empty/`PUBLIC` role
  list still maps to `{0}`.
- `quote_ident` is strictly more PG-accurate; the executor / catalog / parser /
  server / pg_dump suites all pass unchanged.
- The role-map type change touches only `RegisterRole`/`UnregisterRole`/
  `RoleExists`/the new `RoleOID`; no on-disk format change (roles are in-memory
  only, as before).

## Out of scope / follow-ups

- Runtime RLS enforcement (goopg evaluates no policy).
- GRANT/ACL (`relacl`) round-trip — also needs the per-role OID registry (now
  available) plus per-relation ACL projection; a distinct slice.
- Reserved-keyword-named role quoting in `quote_ident` (needs a keyword table).
- Multi-role `TO a, b` ordering: the getPolicies ARRAY subquery has no `ORDER
  BY`, so multi-role output order is scan-dependent; the fixture uses a single
  role to stay deterministic.

## Tests

- `internal/catalog/role_oid_test.go::TestRoleOIDRegistry` — OID minting,
  `postgres`→10, case-insensitivity, stable re-registration, pg_roles exposure,
  unregister.
- `internal/testport/pgdump_connsetup_test.go` **DU-002 slice 330** — `pol_rt` +
  `CREATE ROLE pol_role` + `p_role FOR SELECT TO pol_role USING (a > 0)`; asserts
  `CREATE POLICY p_role ON public.pol_rt FOR SELECT TO pol_role USING ((a > 0));`
  byte-identical vs real pg_dump 18.3.
- Gates: `internal/catalog` + `internal/executor` + `internal/parser` +
  `internal/server` suites PASS; `go build ./...` clean; pgbench smoke via the
  pre-commit hook.
