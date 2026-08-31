# M0134-0032: `pg_get_constraintdef` FK schema-qualification follows search_path visibility

- status: accepted
- date: 2026-08-20
- supersedes: none

## Problem

`inherit.sql` (regress-sql case, M0134-0032) diverges from the PG 18.3 oracle:
goopg's `\d+`/`pg_get_constraintdef` output for a FOREIGN KEY constraint always
schema-qualifies the referenced table —
`FOREIGN KEY (id1) REFERENCES public.test_primary_constraints(id)` — where PG
emits the unqualified form, `REFERENCES test_primary_constraints(id)`, whenever
the referenced schema is visible on the querying session's `search_path`.

`buildForeignKeyDefString` (`internal/executor/expr.go:6345`) is the single
shared renderer for `pg_get_constraintdef` FK output (sole call site:
`internal/executor/expr.go:10076`), used both by pg_dump (which always connects
with `search_path=''`, so full qualification is correct there — see
`ALWAYS_SECURE_SEARCH_PATH_SQL`) and by ordinary `\d+`/interactive-session
queries with the normal default `"$user", public` search_path, where PG's
`generate_relation_name` omits the schema once it resolves via
`RelationIsVisible`. The function currently has no notion of session
search_path at all — it unconditionally prefixes `refSchema + "."`.

## PG oracle

`postgres/src/backend/utils/adt/ruleutils.c`, `pg_get_constraintdef_worker`,
FK branch: builds the `REFERENCES` clause via
`generate_relation_name(fk->confrelid, NIL)`, which is schema-qualified only
when the target relation is not the first name-match found by searching
`search_path` (mirrors `RelationIsVisible`). FK constraints are not special
cased — the same schema-omission rule applies to every deparsed relation name.

## Fix

Thread `ctx *Context` into `buildForeignKeyDefString` (available at its sole
call site) and use the existing `RegObjectSchemaVisible(ctx, schema)` helper
(`internal/executor/expr.go:14435`, already backing the analogous
reg-type/reg-procedure visibility decision) to decide whether to qualify:

```go
if refSchema != "" && !RegObjectSchemaVisible(ctx, refSchema) {
    def = ... + refSchema + "." + refName + ...
} else {
    def = ... + refName + ...
}
```

This self-selects correctly for both existing callers without a caller-side
branch: pg_dump's `search_path=''` session makes `searchPathSchemas(ctx)`
empty, so `RegObjectSchemaVisible` returns `false` and the FK stays fully
qualified (pg_dump's existing pg_dump-facing tests must not regress); an
ordinary session has `public` on its default search_path, so the schema is
omitted, matching the oracle.

## Scope

- `internal/executor/expr.go`:
  - `buildForeignKeyDefString` — add a `ctx *Context` parameter, replace the
    unconditional `refSchema + "."` prefix with the `RegObjectSchemaVisible`
    branch above.
  - call site at `expr.go:10076` — pass `ctx` through (it is already in
    scope at that point).
- `internal/executor/operators_fk_constraintdef_test.go` — every existing
  call site (7+) needs an updated signature; tests that assert pg_dump-style
  (empty search_path) output should pass a `Context` whose
  `GetSetting("search_path")` returns `""` (or a nil `GetSetting`, which
  `searchPathSchemas` defaults to `"$user", public` — verify against the
  test's actual intent per call site, since the pg_dump tests need the FQ
  form preserved) to keep asserting the pg_dump-facing fully-qualified form;
  tests representing an ordinary interactive session should assert the new
  unqualified form when the referenced schema is `public`.

## Verification

- `go test ./internal/executor/ -run TestBuildForeignKeyDefString` (or
  whatever the existing test names are in
  `operators_fk_constraintdef_test.go`) — update expectations, must pass.
- `scripts/pg-regress-runner.sh --verbose inherit` — diff line 611's
  `test_foreign_constraints_id1_fkey` mismatch must disappear; overall diff
  line count for `inherit.sql` should drop from the 3310-line HEAD baseline
  (recorded in `.ralph/deferral_ledger.md` M0134-0032 row).
- Grep `postgres/src/test/regress/expected/*.out` for `REFERENCES public\.`
  to confirm no other regress case's golden output expects the old
  always-qualified form from a default-search-path session (checked clean
  during sizing — see M0134-0032 deferral ledger row).

## Out of scope (deferred — see `.ralph/deferral_ledger.md` M0134-0032 rows)

`inherit.sql` is dominated by much larger gaps not touched by this slice:
the ALTER TABLE inheritance validation matrix (inherited-column/constraint
rejection, multi-parent DEFAULT conflicts, propagation of parent DDL to
existing children), EXPLAIN plan-shape divergences on inherited/partitioned
tables, `pg_get_expr`/CHECK-constraint raw-text deparse (architectural,
shared with the `adbin`/`conbin` text-storage deviation), a `circle` GiST
opclass gap for `EXCLUDE USING gist`, an unconfirmed ORDER BY/Sort
correctness bug on an inherited-table query, and a DROP CASCADE
NOTICE/DETAIL ordering mismatch. All REFACTOR-tier or requiring their own
investigation; re-arm triggers recorded in the ledger.
