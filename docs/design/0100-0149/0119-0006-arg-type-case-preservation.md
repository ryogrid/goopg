# 0119-0006 — Arg-type name case preservation (deferral row 1344)

## Problem

`CREATE FUNCTION f(offpath."MyType")` folds the argument type's quoted name to
lowercase at capture, so goopg stores `ArgTypes[i].Name = "mytype"` and the
regprocedure arglist renders `offpath.mytype`, where PostgreSQL 18.3 renders
`offpath."MyType"`.

The divergence is in `format_type_be` (the arg-type renderer every reg*out /
regprocedure / pg_get_function_* path shares): PG stores `pg_type.typname`
verbatim and emits it through `quote_identifier`, so a mixed-case user type is
quoted. goopg loses the case one step upstream — at CREATE capture — so the
renderers (which already quote correctly) never receive a case-preserved name.

## Root cause

`routineArgTypeName` (`internal/executor/operators_call.go:671`) runs
`strings.ToLower(t.Name)`. The parser already delivers the correct string:
unquoted `TokenIdent` is lowercased by `identText`, and quoted
`TokenQuotedIdent` is preserved verbatim. The `ToLower` is therefore a no-op for
unquoted names and destructive only for quoted ones — it is the single fold that
loses the case.

## Fix

### 1. Capture — drop the fold

`routineArgTypeName`: `name := t.Name` (was `strings.ToLower(t.Name)`).

Safe because every downstream consumer of `Routine.ArgTypes[i].Name` is
case-insensitive:

- `Routine.Signature()` (`internal/catalog/routines.go`) re-lowercases each part,
  so ALTER/DROP/COMMENT matching stays case-insensitive.
- `TypeNameToOID` (`internal/executor/codec.go`) resolves names case-insensitively.
- `ArgTypeDisplayAlias` (`internal/catalog/catalog.go`) is the `format_type_be`
  switch, keyed on lowercase builtin spellings only — a mixed-case user type
  falls through to the default path, exactly as PG's does.
- `argTypeOID` (`operators_ddl.go`) keys on the RAW `a.Name == "char"`; a quoted
  `"char"` is still `"char"`, and a user type `"Char"` now (correctly) resolves
  to OID 0 instead of being folded into the char arm.

### 2. Render — quote mixed-case user types in the visible arm

The executor `regprocedureArglist` (`internal/executor/reg_identifier.go:660`)
fall-through arm (schema non-empty, non-pg_catalog, `visible(schema)` true)
changes from `name = base` to `name = pgQuoteIdent(base)` — the
`quote_identifier(typname)` that `format_type_be`'s default path applies. The
off-path arm already quotes via `quoteQualifiedIdentifier` and is fixed by (1)
alone. This arm fires only for genuine user types (a builtin arg carries a
`""`/`pg_catalog` schema and takes the alias arm), so `pgQuoteIdent` never runs
on a builtin alias.

The catalog sibling `argListTypeDisplay` is deliberately NOT changed: it is
name-only (no schema; `OID` populated only for the `char` spelling) and is not
on the wire path — the executor re-renders from `RegprocedureNameParts`. A
name-only renderer cannot tell a builtin display string that passes through
`ArgTypeDisplayAlias` unchanged (`character`, `numeric`, `interval`, `bit`,
`json`) from a same-named user type, so the `alias == base` user-type signal is
unsound there (recorded in the ledger; see "Not in scope").

The char/bpchar OID disambiguation (row 1351) is unaffected: it runs *before*
the alias step and only rewrites `base` for the `char` spellings.

### 3. Not in scope

- The catalog `argListTypeDisplay` sibling is left bare: a name-only renderer
  cannot quote mixed-case user types without quoting builtin display strings that
  pass through `ArgTypeDisplayAlias` unchanged (`character`, `numeric`,
  `interval`, `bit`, `json`). The faithful fix is OID-keying the catalog arglist
  renderer (a `format_type_be` switch port), which is blocked on resolving
  arg-type OIDs for all types — row 1343's namespace/OID work. Deferred to the
  ledger.
- Bare (schema-`""`) user-type namespace resolution is row 1343, not this row.
- The `canonicalTypeName` `char`→`character` arm of the pg_get_function_* family
  (row 1358) is a separate name-keyed renderer.
- A `Quoted` flag on `parser.ColumnType` is NOT required here (the lexer already
  distinguishes quoted vs unquoted) — it is needed only for the `"char"(N)`
  `len(Args)` heuristic, a separate residual.

## Oracle

- `format_type_be` — `postgres/src/backend/utils/adt/format_type.c:343`
  (BPCHAROID switch arm `:207-220`; default path `:303-326` → `quote_identifier`).
- `quote_identifier` — `postgres/src/backend/utils/adt/ruleutils.c:13029`.
- `format_procedure_extended` — `postgres/src/backend/utils/adt/regproc.c:326`
  (per-arg `format_type_be` at `:368`).
- `pg_type.typname` stored verbatim — `src/backend/commands/typecmds.c`.

## Tests

- `TestCreateFunctionCapturesArgTypeCase` — `offpath."MyType"` stores
  `ArgTypes[0].Name == "MyType"` and `ArgTypeSchemas[0] == "offpath"`.
- `TestRegprocedureArglistQuotesMixedCaseUserType` — off-path `offpath."MyType"`
  and on-path `"MyType"` both render quoted; lowercase `mytype` stays bare.
- Extend `TestRegprocedureArglistCatalogAndExecutorAgree` with a mixed-case
  user-type arg (pins the two siblings byte-identical).
- A case-insensitive-matching guard: `DROP FUNCTION` by the lowercase spelling
  still resolves the `"MyType"` routine (Signature() re-folds).

## Gates

- `go test ./internal/executor/ ./internal/catalog/`
- `scripts/ralph-precommit-test.sh` (units)
- `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=35)
- `go test -v -run TestPort_RegressSuite ./internal/testport/`
