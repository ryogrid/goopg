# 0103-0009 — `ProjectSet` for aggregate-arg SRFs

Status: accepted (2026-05-14)

Milestone: M0103-0008 (probe-survival, final sub-step before
`fetch_table_list` runs end-to-end against a goopg publisher).

## Context

`0103-0007` landed parser support for the postfix `(expr).*` syntax and the
`array_agg(text)` aggregate; `0103-0008` propagated FROM-clause SRF column
schemas through derived subqueries. Two gaps remained before the libpqrcv
`fetch_table_list` probe runs end-to-end against a goopg publisher:

1. **ProjectSet for aggregate-arg SRFs** (this doc).
2. ~~Derived-subquery SRF schema propagation~~ — closed by `0103-0008`.

The probe shape:

```sql
SELECT DISTINCT n.nspname, c.relname, gpt.attrs
  FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
           FROM pg_publication
           WHERE pubname IN ( … ) ) AS gpt
    ON gpt.relid = c.oid
```

The inner derived subquery's target list contains
`(srf(array_agg(...))).*`. The aggregate must be evaluated first (against
the WHERE-filtered `pg_publication` rows), then its single text[] result is
passed to the SRF, which spreads its three-column composite back over
multiple rows. PostgreSQL plans this as `Aggregate → ProjectSet(srf(arg))`.
Before this loop, goopg's planner emitted a clean PlanError for the shape
because no ProjectSet operator existed.

## Decision

Add a single-SRF `ProjectSet` plan node + executor and lower the
aggregate-arg `IndirectionStar` shape into it directly inside `planSelect`.

### Plan node (`internal/planner/plan.go`)

```go
type ProjectSet struct {
    pos     int
    Child   Node     // typically Aggregate, possibly wrapped in HAVING Filter
    SrfName string   // currently only "pg_get_publication_tables"
    SrfArgs []Expr   // resolved args; ColumnRefs index into Child.Output()
    schema  Schema   // expanded composite — (relid oid, attrs text, qual text)
}
```

`SrfArgs` are pre-resolved against the aggregate's output schema, so each
aggregate-call invocation has been replaced with a `ColumnRef` into the
aggregate's output column. This mirrors how `resolveExprAfterAggregate`
already substitutes aggregate calls in plain expressions; the lowering
reuses that helper unchanged.

### Executor (`internal/executor/operators_project_set.go`)

```go
type projectSetOp struct {
    plan   *planner.ProjectSet
    child  Operator
    schema planner.Schema
    rows   []Row // materialised SRF output rows
    pos    int
}
```

`Open` opens the child, drains every row, evaluates `SrfArgs` against each
row, then dispatches on `SrfName` to the matching row-builder
(currently only `pg_get_publication_tables`). All SRF rows are buffered;
`Next` yields one at a time. For the probe shape the child is a
zero-GROUP-BY `Aggregate` that emits exactly one row, so buffering cost is
trivial; if future SRFs need streaming semantics the operator can be
turned inside-out without changing the plan node's shape.

The operator path reuses the new package-level row-builder
`buildPgGetPublicationTablesRows(ctx, []Datum)` extracted from
`pgGetPublicationTablesOp.Open`. The FROM-clause SRF operator itself was
refactored to evaluate its argument expressions to Datums then delegate
to the same builder, so both paths produce byte-identical rows.

### Planner integration (`internal/planner/planner.go`)

`rewriteIndirectionStarTargets` no longer surfaces `0A000` for
aggregate-arg cases; it leaves the `IndirectionStar` in the target list.

After `buildAggregateStage` returns, `planSelect` walks the target list. If
it finds an `IndirectionStar` whose `Source` is a `FuncCall` with a
supported composite (`projectSetCompositeSchema`), it:

1. Resolves every `FuncCall.Args[i]` through `resolveExprAfterAggregate`
   — aggregate calls become `ColumnRef`s into the Aggregate output;
   constants and other expressions pass through unchanged.
2. Builds a `ProjectSet{Child: node, SrfName, SrfArgs, schema: composite}`
   and assigns it to `node`.
3. Replaces `ctx` with a fresh `resolveContext` over the ProjectSet's
   expanded schema and clears `agg` so the downstream branches (`ORDER BY`,
   `LIMIT`, target resolution) hit the non-aggregate code paths.

The final `Project` becomes a per-column identity passthrough over the
ProjectSet's output, keeping downstream invariants (`tryPromoteIndexOnlyScan`,
the locking-clause wrapper, `Distinct` wrapping, etc.) intact.

### Scope

Only `pg_get_publication_tables` is wired through ProjectSet — the only
SRF the libpqrcv probe needs. Other SRFs that take aggregate-derived args
fall through to the existing `0A000` path. Multi-SRF target lists are
not supported (the lowering breaks after the first match); the probe
shape contains exactly one SRF.

## Tests

- `internal/executor/operators_pg_get_publication_tables_test.go::TestIndirectionStarWithAggregateArgument`
  — `(srf(array_agg(...))).*` over a 1-row source, asserts 3 columns +
  non-NULL relid.
- `internal/executor/operators_pg_get_publication_tables_test.go::TestIndirectionStarWithAggregateArgumentAndWhere`
  — same shape with a WHERE filter on the aggregate's source rows
  (mirrors the libpqrcv `fetch_table_list` probe's `WHERE pubname IN
  (...)` clause), asserts only the surviving publication's tables come
  through.

The previous `TestIndirectionStarRejectsAggregateArgument` (which pinned
the old `0A000` rejection) was replaced by the positive test above; the
rejection it pinned has been intentionally removed.

## Verification

```
$ go test -race -count=1 -timeout 300s ./internal/parser/ ./internal/planner/ \
    ./internal/analyzer/ ./internal/executor/ ./internal/server/ \
    ./internal/wal/ ./internal/catalog/
ok  internal/parser    1.050s
ok  internal/planner   1.062s
ok  internal/analyzer  1.038s
ok  internal/executor  2.619s
ok  internal/server    3.512s
ok  internal/wal       3.069s
ok  internal/catalog   1.019s
```

## Out of scope (next step in M0103-0008)

The `fetch_table_list` SQL parses, plans, and executes against an
in-memory fixture. The next step in M0103-0008 is to drop the `t.Skip`
on `TestPort_PgoutputInteropGoopgToPG` and confirm the full probe
sequence — including the outer `JOIN pg_namespace` / `JOIN pg_class` —
runs against a live goopg publisher with the apply-launcher path
end-to-end. Any further publisher-side gaps that the live test surfaces
will be filed as their own follow-ups.
