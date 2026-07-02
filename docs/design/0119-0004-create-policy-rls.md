# DU-002 slice 323 — CREATE POLICY round-trip in pg_dump

**Milestone:** M0119-0004 (pg_dump 002–010 TAP / catalog-view parity battery)
**Status:** landed
**Oracle:** `postgres/src/bin/pg_dump/pg_dump.c` `getPolicies` / `dumpPolicy`

## Problem

`pg_dump` dumps a table's row-level security policies as `CREATE POLICY`
statements. Its `getPolicies` reads `pg_catalog.pg_policy`:

```sql
SELECT pol.oid, pol.tableoid, pol.polrelid, pol.polname, pol.polcmd,
       pol.polpermissive,
       CASE WHEN pol.polroles = '{0}' THEN NULL ELSE
         pg_catalog.array_to_string(ARRAY(SELECT pg_catalog.quote_ident(rolname)
           FROM pg_catalog.pg_roles WHERE oid = ANY(pol.polroles)), ', ') END AS polroles,
       pg_catalog.pg_get_expr(pol.polqual, pol.polrelid)      AS polqual,
       pg_catalog.pg_get_expr(pol.polwithcheck, pol.polrelid) AS polwithcheck
FROM unnest('{...}'::pg_catalog.oid[]) AS src(tbloid)
JOIN pg_catalog.pg_policy pol ON (src.tbloid = pol.polrelid)
```

and `dumpPolicy` re-emits:

```
CREATE POLICY <name> ON <table>[ AS RESTRICTIVE][ FOR <cmd>][ TO <roles>]
  [ USING (<polqual>)][ WITH CHECK (<polwithcheck>)];
```

Before this slice goopg had **no** CREATE POLICY support: the statement was a
hard parse error, and `pg_catalog.pg_policy` was an empty stub
(`VirtualRows → nil`, M0097-0023). A row-security policy was therefore silently
lost on dump/restore, and goopg could not restore its own dump if the schema
contained any policy. (The RLS *enable* flag — `relrowsecurity` — already
round-tripped after slice 322; this slice adds the per-policy half.)

## Feasibility: the query already runs

Slice 322's `rls_t` fixture proves `getPolicies` already executes successfully
against goopg with `pg_policy` returning **zero** rows — so the `unnest … JOIN`,
the `CASE`, and the two `pg_get_expr` calls all plan correctly. The only missing
piece is populating `pg_policy.VirtualRows`.

For a **PUBLIC** policy `polroles = '{0}'`, so:

- the `CASE` `WHEN` branch is true → returns `NULL` → no `TO` clause. goopg's
  `evalCaseExpr` is **lazy** (`internal/executor/expr.go`), so the `ELSE`
  `array_to_string(ARRAY(SELECT … FROM pg_roles …))` correlated subquery — the
  one risky construct — is never evaluated.
- `pg_get_expr(polqual, …)` is a **pass-through** in goopg
  (`internal/executor/expr.go`): it returns the stored text verbatim.

Named-role policies would force the `ELSE` subquery to evaluate and need a
per-role OID registry goopg does not have; they are deferred (see Limitations).

## Implementation

Dump-fidelity only — **goopg does not enforce RLS.** Mirrors the slice-322 RLS
ENABLE approach.

### Parser (`internal/parser/ast.go`, `internal/parser/ddl.go`)

- `CreatePolicyStmt` / `DropPolicyStmt` AST nodes.
- `parseCreatePolicyTail`: `CREATE POLICY name ON table [AS {PERMISSIVE|
  RESTRICTIVE}] [FOR {ALL|SELECT|INSERT|UPDATE|DELETE}] [TO role[, …]]
  [USING (expr)] [WITH CHECK (expr)]`. `USING`/`WITH CHECK` are parsed with the
  general `parseExpr` (not raw-token capture) so the stored form is a proper AST.
- `parseDropPolicyTail`: `DROP POLICY [IF EXISTS] name ON table [CASCADE|RESTRICT]`.
- Dispatch wired in `parseCreate` (`policy` ident) and `parseDrop`.

### Catalog (`internal/catalog/catalog.go`)

- `PolicyInfo` struct + `Table.Policies []PolicyInfo`.
- `formatExprForAttrdef` gained a `*parser.ColumnRef` case (renders the bare
  column name). It previously lacked one — a column ref fell through to
  `fmt.Sprintf("%v")`, emitting a Go pointer string. This is the catalog-side
  `pg_get_expr` deparser; it fully parenthesizes every binary node, so a policy
  qual `a > 0` renders `(a > 0)`.
- `pg_policy`: `polqual`/`polwithcheck` retyped `text → pg_node_tree` (matching
  real PG) so an empty cell decodes to **SQL NULL** via
  `planner.TypedVirtualCell` — `pg_get_expr(NULL,…)` returns NULL and `dumpPolicy`
  omits the absent clause. `VirtualRows` now walks every user table's `Policies`
  and projects `oid, polname, polrelid, polcmd, polpermissive, polroles ({0} for
  PUBLIC), polqual, polwithcheck`.

### Executor (`internal/executor/operators_ddl.go`)

- `execCreatePolicy`: resolves the table, rejects a duplicate policy name
  (42710), maps the command to `polcmd` (`* r a w d`), maps `TO PUBLIC`/default
  to `{0}` (named roles → 0A000 "not yet supported"), assigns an OID
  (`Catalog.AllocOID`), appends to `Table.Policies`. **No** heap re-sync is
  needed — `pg_policy` is virtual and reads `Table.Policies` live.
- `execDropPolicy`: removes by name (IF EXISTS → notice).
- Dispatch wired; `CreatePolicyStmt`/`DropPolicyStmt` added to the planner's DDL
  passthrough list (`internal/planner/planner.go`).

## Parenthesization

`pg_get_expr` fully parenthesizes (`(a > 0)`); `dumpPolicy` wraps once more
(`USING (%s)`), so real pg_dump emits `USING ((a > 0))`. goopg stores the
fully-parenthesized form in `polqual` and the same double layer results. Because
the stored form comes from the parsed AST (not raw token capture), the rendering
is **idempotent** across re-dumps — a strict improvement over the raw-text CHECK
path. Verified byte-identical to real PG 18.3.

## Tests

- `internal/parser/policy_test.go` — `TestParseCreatePolicy` (4 forms) +
  `TestParseDropPolicy`.
- `internal/executor/storage_ddl_test.go` — `TestDDLCreatePolicyRoundTrip`:
  CREATE → `pg_policy` projection (polcmd/polpermissive/polroles/polqual/
  polwithcheck) → DROP → named-role rejection.
- `internal/testport/pgdump_connsetup_test.go` (slice 323) — real `pg_dump 18.3`
  emits all three `CREATE POLICY` forms byte-identically (`p_simple` PERMISSIVE
  FOR ALL, `p_restr` AS RESTRICTIVE FOR SELECT, `p_check` FOR INSERT WITH CHECK).

## Limitations / follow-ups

- **Named-role policies** (`TO role`) are rejected (0A000). goopg has no per-role
  OID registry (`pg_roles` projects only the bootstrap superuser); the
  `getPolicies` `ELSE array_to_string(ARRAY(SELECT … pg_roles …))` path is also
  unverified. A follow-up slice adds role OIDs + the named-role round-trip.
- goopg does not enforce RLS at query time; policies are catalog/dump state only.
