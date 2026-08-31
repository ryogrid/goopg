# 0103-0012: Derived-subquery `(srf(<agg>)).*` composite expansion (rung 7 closure)

Status: accepted
Milestone: M0103-0008
Date: 2026-05-14

## Background

`docs/design/0103-0010-lateral-from-srf-arg-resolution.md` and
`docs/design/0103-0011-lateral-from-srf-executor-bind.md` closed rung 6
(LATERAL FROM-clause SRF arg resolution + executor binding). After those
two slices landed, dropping the `t.Skip` on
`internal/testport/pgoutput_interop_test.go::TestPort_PgoutputInteropGoopgToPG`
exposed `libpqrcv`'s `fetch_table_list` probe rather than the
column-list probe. The shape ships an aggregate inside the
set-returning function and `.*`-expands the composite return type from a
derived subquery:

```sql
SELECT gpt.attrs
  FROM pg_class c
  JOIN pg_namespace n ON ...
  JOIN (
    SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
      FROM pg_publication
     WHERE pubname IN (...)
  ) AS gpt ON gpt.relid = c.oid
```

Two earlier loops landed the surrounding pieces:

* Loop 3 (`docs/design/0103-0007-indirection-star-and-array-agg.md`)
  parsed `(expr).*`, rewrote *non-aggregate* IndirectionStar targets
  into a FROM-clause SRF, and added `array_agg(text)`.
* Loop 5 (`docs/design/0103-0009-projectset-for-aggregate-arg-srfs.md`)
  added planner-side `ProjectSet` lowering for the *aggregate-arg*
  variant so it plans and executes — but only at the top level. Inside
  a derived subquery the analyzer's `synthesizeSubqueryTable` still
  walked the inner target list and treated the IndirectionStar as a
  single unnamed column (`?column?1`), so outer references like
  `gpt.attrs` raised `42703: column "attrs" does not exist`.

This slice closes that analyzer gap.

## Change

### `internal/analyzer/analyzer.go`

Added a package-private helper:

```go
func compositeFuncColumns(funcName string) []catalog.Column
```

which returns the composite return-column shape for a set-returning
function known to expand into multiple columns via `(srf(...)).*` in
target-list position. Currently the only entry is
`pg_get_publication_tables` (relid oid, attrs text, qual text). The
helper mirrors `planner.projectSetCompositeSchema`; the two stay in
lockstep — the planner's ProjectSet lowering already emits exactly this
schema for the inner SELECT, so the analyzer's synthesized table
matches the plan's `inner.Output()` byte-for-byte.

`synthesizeSubqueryTable`'s inner-target walk gained a new branch
between the existing `*parser.StarExpr` case and the generic
`analyzeExpr` path:

```go
if is, ok := tgt.Expr.(*parser.IndirectionStar); ok {
    if fc, ok2 := is.Source.(*parser.FuncCall); ok2 {
        if comp := compositeFuncColumns(fc.Name.Name); comp != nil {
            for _, c := range comp {
                cols = append(cols, catalog.Column{Name: c.Name, Type: c.Type})
            }
            continue
        }
    }
}
```

The branch only fires for SRFs with a known composite schema; unknown
sources fall through to the existing path unchanged.

### Why analyzer + planner duplicate the schema

`synthesizeSubqueryTable` is invoked from `lookupTable` / outer-scope
construction *before* the inner SELECT is planned; it cannot reach the
planner's `inner.Output()` without circular dependencies. The
duplicated three-row table is intentional: both `compositeFuncColumns`
and `projectSetCompositeSchema` are short, single-case switches today,
and the parity is enforced by `TestPlanFetchTableListAggDerivedSubquery`
(planner) plus the existing pinning tests in the executor package.

## Tests

* `internal/planner/planner_test.go::TestPlanFetchTableListAggDerivedSubquery`
  — pre-existing pin (was `t.Skip` for rung 7). Now asserts a positive
  plan with a single output column named `attrs`; with the analyzer fix
  the outer `gpt.attrs` reference resolves to the SRF's composite
  `attrs` column.

* All regressions (`parser`, `planner`, `analyzer`, `executor`,
  `server`, `wal`, `catalog`) stay green:

  ```
  go test -race -count=1 -timeout 300s \
      ./internal/parser/ ./internal/planner/ ./internal/analyzer/ \
      ./internal/executor/ ./internal/server/ ./internal/wal/ \
      ./internal/catalog/
  ```

## Remaining gap (rung 8+)

`TestPort_PgoutputInteropGoopgToPG` stays `t.Skip` for now. With rung 7
closed `fetch_table_list` parses, plans, analyses, and produces the
expected outer-scope schema; the next live-probe step is to drop the
`t.Skip` and observe whatever the apply launcher ships next (most
likely the column-list probe deferred from rung 6 or the per-table
replica-identity check). That work is intentionally left for the next
M0103-0008 loop so each rung lands with its own design doc.
