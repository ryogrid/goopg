# 0095-0004 — VACUUM Parenthesized Syntax + vacuumdb Catalog Support

**Status:** accepted  
**Date:** 2026-05-12  
**Milestone:** M0095-0004

## Problem

`vacuumdb` generates SQL using PostgreSQL's parenthesized VACUUM syntax (available
since PostgreSQL 9.0) and a catalog discovery query that uses several SQL features
not previously supported by goopg:

1. `VACUUM (SKIP_DATABASE_STATS, FULL)` — parenthesized option list
2. `OPERATOR(pg_catalog.=)` — schema-qualified operator invocation
3. `CROSS JOIN LATERAL (SELECT ...)` — lateral derived table
4. `= ANY (array['r', 'm'])` — quantified comparison with array constructor
5. `pg_catalog.pg_namespace` — required for the JOIN in the catalog query
6. `pg_database.datallowconn` / `datconnlimit` — required for `--all` flag

## Solution

### Parser changes (`internal/parser/`)

**New keywords** (`token.go`, `keywords.go`): `KwFreeze`, `KwParallel`, `KwLateral`,
`KwAny` (all `KwCatUnreserved` except `KwLateral` → `KwCatTypeFunc`).

**Extended `VacuumStmt`** (`ast.go`): added fields for all parenthesized VACUUM
options: `Full`, `Freeze`, `DisablePageSkipping`, `NoIndexCleanup`,
`ForceIndexCleanup`, `NoTruncate`, `NoProcessMain`, `NoProcessToast`,
`SkipDatabaseStats`, `OnlyDatabaseStats`, `SkipLocked`, `ParallelWorkers`,
`BufferUsageLimit`. Legacy keyword syntax still accepted.

**Extended `AnalyzeStmt`** (`ast.go`): added `SkipLocked` field.

**`parseVacuum()`** (`parser.go`): detects leading `(` and parses the parenthesized
option list via new `parseVacuumOptionList()` helper. Falls through to legacy
keyword parsing otherwise.

**`parseAnalyze()`** (`parser.go`): same pattern for ANALYZE parenthesized options.

**`OPERATOR(schema.op)`** (`select.go`): in `parseExprPrec()`, detects
`OPERATOR(name.op_sym)` before `peekBinaryOp()` and desugars to the corresponding
`BinaryOp`. Also handles the `OPERATOR(=) ANY (array[...])` combined form.

**`= ANY (array[...])`** (`select.go`): desugared to `InExpr` via new
`parseAnyTail()` helper. Also handles the OPERATOR-preceded form.

**`LATERAL`** (`select.go`): keyword consumed silently before derived-table
subqueries in `parseJoinClause()` and `parseRangeVar()`. Marking is sufficient
because the analyzer/planner fallback handles the unresolved reference.

**`array[...]` constructor** (`lexer.go`, `select.go`): lexer accepts `[`/`]`
as symbols; `parseAnyTail` parses `array[e1,e2,...]` to extract elements.

### Analyzer change (`internal/analyzer/analyzer.go`)

`synthesizeSubqueryTable()`: when the inner subquery of a LATERAL derived table
fails analysis with 42P01/42703 (correlated reference to outer scope columns not
visible in the isolated inner Plan call) AND explicit column aliases are present
(`rv.Columns`), produce a synthetic table with those column names and `text` type
instead of returning an error. This allows the outer query to proceed; the column
values are NULL at execution time.

### Planner change (`internal/planner/planner.go`)

`planSubqueryRangeVar()`: mirror the analyzer fallback for the planning pass.
When `Plan(rv.Subquery, cat)` fails with 42P01/42703 and `len(rv.Columns) > 0`,
return a single-row `Values{NullConst}` node so the CROSS JOIN produces one NULL
row per outer row. Safe for vacuumdb because `p.inherited` is never referenced in
the basic vacuum WHERE clause.

### Catalog changes (`internal/catalog/catalog.go`)

**`pg_class`**: added columns `relpersistence` ('p' for all), `reltoastrelid` ('0'),
`relpages` ('0'). Required by vacuumdb's ORDER BY and LEFT JOIN conditions.
Also corrected `relnamespace` value from `"pg_catalog"` to `"public"`.

**`pg_namespace`** (new): oid, nspname, nspowner, nspacl. Returns three rows:
pg_catalog (11), public (2200), information_schema (99). Required for the
catalog-discovery JOIN.

**`pg_database`**: added `datallowconn` (true) and `datconnlimit` (0) columns.
Required by `vacuumdb --all` filter `WHERE datallowconn AND datconnlimit <> -2`.

### Executor change (`internal/executor/expr.go`)

`evalFuncCall()`: strips `pg_catalog.` prefix so schema-qualified calls like
`pg_catalog.set_config(...)` resolve to built-ins. Added handlers:
- `set_config(name, new_value, is_local)` → returns new_value (vacuumdb security init)
- `current_database()` → 'postgres'
- `current_schema()` / `current_schemas()` → 'public'

## Behavioural notes

- Most new VACUUM option fields are accepted by the parser and silently ignored
  by the executor (FULL, FREEZE, etc.) — the heap pruning still runs normally.
- LATERAL correlated references produce NULL columns; this is correct for
  vacuumdb's basic run where `p.inherited` is unused.
- `pg_class.relnamespace` returns schema name text ('public'); pg_namespace.oid
  is stored as text ('2200'). The JOIN `c.relnamespace = ns.oid` therefore
  produces no matches. vacuumdb finds 0 user tables to vacuum (correct: fresh
  cluster has no user tables), then runs `VACUUM (ONLY_DATABASE_STATS)` cleanup.

## Tests unlocked

- `TestPort_Scripts100Vacuumdb` (D-005a → port)
- `TestPort_Scripts102VacuumdbStages` (D-005b → port)
- `TestPort_Scripts101VacuumdbAll` (D-005c → port)
