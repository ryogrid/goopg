---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (69th slice)
---

# reg* output schema-qualification and identifier quoting

Closes ledger row 1304 (the 68th slice's carry-forward). `executor.RegOut`
now emits schema-qualified, quote_identifier'd object names the way upstream
`regclassout`/`regprocout`/`regroleout`/`regcollationout` do through
`quote_qualified_identifier`.

## 1. Problem

The 68th slice gave `RegOut` (the single OID→name renderer shared by SELECT
and COPY) a `qualify bool` flag but returned the BARE object name. Upstream,
every `reg*out` in `postgres/src/backend/utils/adt/regproc.c` schema-qualifies
a name whose schema is NOT on the session's effective search_path — the
`RelationIsVisible` / `FunctionIsVisible` / `CollationIsVisible` helpers in
that same file — and runs every emitted name through `quote_identifier`
(`postgres/src/backend/utils/adt/ruleutils.c`). A reg* column whose object
lived in a non-default schema therefore diverged from PG in the emitted text
(SELECT and COPY equally — an inherited SELECT-path gap the 68th slice did
not widen, matching the status quo).

## 2. The upstream rule

`regclassout` (regproc.c:951) resolves the OID to a relation and renders via
`quote_qualified_identifier`: `quote_identifier(qualifier) + "." +
quote_identifier(ident)`, or just `quote_identifier(ident)` when the object
is visible (qualifier empty). The family's qualification decision is
`*IsVisible`, which has one non-obvious arm: **pg_catalog is implicitly
searched by every search_path**, so an object there is never qualified.
`regprocout` is the same shape over routines; `regroleout` is the special
case — a role is never schema-qualified, only `quote_identifier`'d, and a
dangling role OID falls back to the unquoted `%u`. `regcollationout` mirrors
regclassout with `CollationIsVisible` and quote_identifiers every name.

Measured against PG 18.3 (throwaway oracle, port 5599):

| probe | PG 18.3 |
|---|---|
| `'My Table'::regclass` with public off the path | `public."My Table"` |
| `oid::regclass` of a public table with public off the path | `public.<name>` |
| `1259::regclass` (pg_class) with public off the path | `pg_class` |
| `950::regcollation` (C) | `"C"` |
| `100::regcollation` (default) | `"default"` |

Note the `public` qualifier itself is **not** quoted — `public` is an
unreserved keyword, so `quote_identifier("public")` returns it bare; only the
`"My Table"` name is quoted.

## 3. goopg implementation

Three pieces in `internal/executor` + one in `internal/catalog`:

- `quoteQualifiedIdentifier(qualifier, ident)` (`expr.go`, next to the
  existing `pgQuoteIdent` trio) — the literal `quote_qualified_identifier`
  port: bare `pgQuoteIdent(ident)` when the qualifier is empty, else
  `pgQuoteIdent(qualifier) + "." + pgQuoteIdent(ident)`. `pgQuoteIdent`
  already mirrors `quote_identifier` via the
  `internal/sqlkeywords.IsReservedForQuoting` guard.
- `regOutQualified(schema, name, qualify)` (`reg_identifier.go`) — the
  family's shared rule: an empty schema defaults to `public` (matching
  `userTypeNameForOID`), a `pg_catalog` schema forces `qualify = false` (the
  implicit-visibility arm), then either the bare quoted name or the
  `quoteQualifiedIdentifier` form.
- `RegOut` arms (`reg_identifier.go`) — regclass (table, then index) and
  regproc user-routine hits run through `regOutQualified`; a builtin proc
  (resolved via `catalog.RegprocName`) is in pg_catalog and stays bare;
  regrole uses `pgQuoteIdent` on the real role name (never qualified), with a
  dangling OID falling to the numeric `%u` fallback — distinguished by the
  new `InMemory.RoleNameAtOID`, since `RoleNameForOID` renders dangling roles
  numerically too; regcollation quote_identifiers every name and qualifies a
  user collation.
- `InMemory.RoleNameAtOID(oid)` (`internal/catalog/catalog.go`) — returns
  (name, true) for a real role (OID 10 = postgres fast-path, then the role
  map), (_, false) for a dangling OID.

The `qualify` flag callers are unchanged from the 68th slice: COPY computes
`!regObjectSchemaVisible(ctx, "public")`, SELECT
`!publicSchemaVisible(getSetting)`. Both parse the session `search_path` GUC;
the strengthened sibling test now exercises a REAL user table (created
through the planner→Build pipeline) so a disagreement between the two
computations is observable, which the pre-69th 1259-only test could not
catch.

## 4. Scope decisions

- **regtype** keeps its own `format_type_be` path — `RegtypeName` /
  `userTypeNameForOID` already schema-qualify. Not touched.
- **regprocedure** keeps the bare `format_procedure`-less signature
  (`int4out(integer)`). Porting `format_procedure`'s qualification is its own
  machinery; filed as a new deferral ledger row.
- **Mixed-case role names** are unreachable from the catalog today (the role
  store folds every name to lowercase), so regroleout's quoting of an
  uppercase name is proven through the shared `pgQuoteIdent` guard rather
  than a role fixture. Filed as a deferral.
- The regcollation qualify path hardcodes `"public"` as the user-collation
  qualifier (goopg creates user collations in the current schema); a
  non-public creation schema would diverge. Filed as a deferral.

## 5. Gates

Package suites (`internal/executor` + `internal/server` reg tests), pre-commit
units, `TestPort_RegressSuite`, and `scripts/tpch-spotcheck.sh` (executor
change, practice card).

## 6. Files

- `internal/executor/expr.go` — `quoteQualifiedIdentifier`
- `internal/executor/reg_identifier.go` — `regOutQualified`, `RegOut` arms
- `internal/catalog/catalog.go` — `RoleNameAtOID`
- `internal/executor/reg_qualify_test.go` — new 69th-slice tests
- `internal/executor/reg_copy_test.go`, `internal/server/reg_copy_sibling_test.go` — pins
