# 0103-0006 — VARIADIC parser support and `pg_get_publication_tables` SRF

Status: accepted (2026-05-14)

Milestone: M0103-0008 (probe-survival prerequisite).

## Context

Upstream PG's `CREATE SUBSCRIPTION` runs `libpqrcv_exec` probes against the
publisher *before* it issues `START_REPLICATION`. M0103-0004 closed the first
probe failure (a per-query context cancellation in `runPostStartupLoop` that
killed the SQL fall-through for replication-mode connections). The next probe
to fall over against goopg is `subscriptioncmds.c::fetch_table_list`. For
server_version ≥ 16 it issues:

```sql
SELECT DISTINCT n.nspname, c.relname, gpt.attrs
  FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
           FROM pg_publication
           WHERE pubname IN ( … ) ) AS gpt
    ON gpt.relid = c.oid
```

goopg's parser rejected this with `syntax error at or near "expected
expression (got variadic)"`. Closing the failure requires:

1. parser-level acceptance of the `VARIADIC` keyword on a function-call
   argument, and
2. a `pg_get_publication_tables(...)` function the planner can route to a
   non-erroring execution path.

This loop lands both. The remaining gap — composite-expansion of
`(srf()).*` in scalar position — is documented as the next probe-survival
barrier and is out of M0103-0006's scope.

## Decision

### Parser

`FuncCall` gains `Variadic []bool`, a slice parallel to `Args`. When an
argument is prefixed with `VARIADIC`, the parser consumes the keyword and
records `true` at the matching position. The argument expression itself is
parsed unchanged — there is no per-element spread at parse time. Two parse
sites accept the marker:

- `parseFuncCallTail` (target-list / scalar-position function calls).
- The FROM-clause SRF arm of `parseRangeVar` (the `srfFuncName != ""`
  branch); the loop now lookups `KwVariadic` ahead of every `parseExpr`.

`KwVariadic` (`"variadic"`) is already a reserved keyword (`token.go:210`,
`keywords.go:192`); no lexer changes are required.

### `pg_get_publication_tables` SRF

`pg_get_publication_tables` is added to the FROM-clause SRF whitelist (alongside
`generate_series`, `pg_input_error_info`, `parse_ident`) and exposed through a
new `planner.PgGetPublicationTables` plan node and `executor.pgGetPublicationTablesOp`
operator. Output schema:

```
(relid oid, attrs text, qual text)
```

`attrs` and `qual` are always NULL since goopg does not model column lists
or row-filter quals on publications. `relid` is populated from the
`*catalog.Table.OID` resolved by walking the registered `*catalog.Publication`
list filtered by the argument set:

- Zero arguments → emit every (publication, table) pair the registry knows.
- One or more arguments → flatten each into a string set. A brace-wrapped
  string (the textual encoding of a goopg `text[]` Datum) is split on commas
  via `parseTextArray`; plain text values pass through unchanged. The
  `VARIADIC` marker is irrelevant at this layer because both shapes flatten
  to the same set.

`Publication.AllTables` expands by type-asserting `ctx.Catalog` to
`*catalog.InMemory` and calling `AllTables()` (the same pattern
`pg_publication_tables`'s `VirtualRows` uses). A non-`InMemory` catalog
yields zero rows for `AllTables` publications; this matches existing
behaviour elsewhere and is acceptable because all production catalogs are
`*InMemory`.

### Plan/executor wiring

- `internal/planner/plan.go` — `PgGetPublicationTables` plan node.
- `internal/planner/planner.go` — `planTableFuncRangeVar` dispatch +
  `planPgGetPublicationTables` builder.
- `internal/planner/foldconst.go` + `internal/planner/unnest.go` —
  expression walkers updated to descend into `Args`.
- `internal/executor/executor.go` — dispatch case routing the plan node to
  the operator constructor.
- `internal/executor/operators_pg_get_publication_tables.go` (new) — the
  operator, plus local helpers `flattenTextArg` and `splitQualifiedTable`.

## Out of scope (next loop)

The actual `fetch_table_list` query uses
`(pg_get_publication_tables(...)).*` in **scalar position**, expanding the
function's composite return type into multiple columns. goopg currently
plans `(srf(...))` as a single scalar expression; the `.*` postfix expansion
is unimplemented. That is the next M0103-0008 probe-survival step. The
parser+planner+executor surface this design lands is the foundation it
will build on: the SRF already returns the three-column shape that
upstream expects, so the composite expansion just needs to project from
the SRF's `Output()` schema.

`array_agg(text)` in the surrounding query has not been verified as
working. If it is missing, M0103-0008 will need to land that as well.

## Tests

- `internal/parser/select_test.go::TestParseFuncCallVariadicArgument` —
  pins parser acceptance of `pg_get_publication_tables(VARIADIC array_agg(x))`
  in target-list position; asserts `Variadic[0] == true`.
- `internal/parser/select_test.go::TestParseFuncCallVariadicMixed` —
  pins parser acceptance of a multi-arg call with VARIADIC on the
  trailing argument; asserts the parallel `Variadic` slice.
- `internal/executor/operators_pg_get_publication_tables_test.go` —
  four tests pin the SRF's behaviour against the live `*catalog.PubSub`
  registry through `runDDL`:
  - `TestPgGetPublicationTablesFromClauseFilter` — single-publication
    filter, asserts non-NULL relid.
  - `TestPgGetPublicationTablesVariadicArrayArgument` — `VARIADIC
    '{p,q}'` flattens to a 2-publication match.
  - `TestPgGetPublicationTablesEmptyFilter` — bare `()` emits every
    pair the registry has.
  - `TestPgGetPublicationTablesUnknownPublication` — unknown name
    emits zero rows.

## Verification

```
$ go test -count=1 -timeout 240s ./internal/parser/ ./internal/planner/ \
    ./internal/executor/ ./internal/server/ ./internal/wal/ ./internal/catalog/
ok  internal/parser     0.022s
ok  internal/planner    0.021s
ok  internal/executor   1.165s
ok  internal/server     1.808s
ok  internal/wal        1.929s
ok  internal/catalog    0.005s

$ go test -race -count=1 -timeout 300s ./internal/parser/ ./internal/planner/ \
    ./internal/executor/ ./internal/server/ ./internal/wal/ ./internal/catalog/
ok  (all six)
```
