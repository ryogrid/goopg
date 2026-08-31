# 0103-0008 — Derived-Subquery SRF Schema Propagation

Status: accepted (2026-05-14)

Milestone: M0103-0008 (probe-survival, third sub-step).

## Context

The libpqrcv `fetch_table_list` probe upstream PG runs against goopg under
`CREATE SUBSCRIPTION` wraps the `pg_get_publication_tables` SRF inside a
derived subquery and reads individual columns from the wrapper:

```sql
SELECT DISTINCT n.nspname, c.relname, gpt.attrs
  FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
           FROM pg_publication
           WHERE pubname IN ( … ) ) AS gpt
    ON gpt.relid = c.oid
```

The previous loop (`0103-0007`) landed:
- The `(expr).*` IndirectionStar AST node.
- A parser-level rewrite that pivots non-aggregate-arg cases from a
  target-list IndirectionStar into a FROM-clause SRF reference
  (`__irs_0` alias).
- The `array_agg(text)` aggregate, returning a PG text-array literal.

But the derived-subquery shape `( SELECT (srf(consts)).* ) AS gpt`
remained broken: even with the parser rewrite firing inside the inner
SELECT, the outer SELECT could not resolve `gpt.relid` —
`42703: column "relid" does not exist`. This loop closes that gap.

## Root cause

Two independent layers were dropping the SRF's column list before it
reached the outer SELECT's column-resolution pass.

### (1) Analyzer — `lookupTable` returned a generate_series-shaped table

`analyzer.lookupTable` had a single hard-coded branch for every
`*parser.TableFuncRef`: one int8 column named after the alias. That was
sufficient when the only FROM-clause SRF was `generate_series(start,
stop[, step])`, but the planner already supports four SRFs with
distinct return shapes:

| SRF                            | Columns                                                    |
| ------------------------------ | ---------------------------------------------------------- |
| `generate_series`              | `<alias>` int8                                             |
| `pg_input_error_info`          | `message` text, `detail` text, `hint` text, `sql_error_code` text |
| `parse_ident`                  | `<alias>` text[]                                           |
| `pg_get_publication_tables`    | `relid` oid, `attrs` text, `qual` text                     |

When the analyzer pre-validated the inner SELECT
`SELECT __irs_0.* FROM pg_get_publication_tables('p') AS __irs_0`,
`buildSelectScope → lookupTable` produced a synthetic table with a
single int8 column. The downstream star expansion then handed
`synthesizeSubqueryTable` a one-column shape, which the planner faithfully
propagated to the outer scope.

### (2) Planner — `planSubqueryRangeVar` walked the inner *target list*

`planSubqueryRangeVar` built the derived table's column list by
iterating over `rv.Subquery.Targets`. After the IndirectionStar rewrite,
the inner SELECT's target list contained a single `*parser.StarExpr`
entry (`__irs_0.*`), even though the inner plan's `Output()` schema had
three entries (one per SRF return column). The planner's loop therefore
emitted a single derived-table column with `deriveSubqueryTargetName(*StarExpr)`
returning `""` and falling back to `?column?1`.

Both layers had to be fixed in lockstep: the analyzer because it runs
first and its synthetic table shapes the column-resolution scope; the
planner because it owns the final binding the outer SELECT sees.

## Decision

### Analyzer (`internal/analyzer/analyzer.go`)

Replace `lookupTable`'s hard-coded TableFunc branch with a new
`tableFuncColumns(funcName, alias, colAliases) []catalog.Column` helper.
The helper mirrors the planner's `planTableFuncRangeVar` dispatch
function-name-for-function-name:

```go
switch strings.ToLower(funcName) {
case "pg_get_publication_tables":
    // 3 cols: relid oid, attrs text, qual text
case "pg_input_error_info":
    // 4 cols: message/detail/hint/sql_error_code text
case "parse_ident":
    // 1 col: <alias> text[]
default:
    // generate_series + unknown: 1 col <alias> int8 (preserves pre-fix behaviour)
}
```

Each branch honours an explicit column-alias list (`AS t(c1, c2)`)
positionally, falling back to the SRF's canonical names. The helper is
package-local because the planner already has its own, more elaborate
per-function planner that needs distinct expression-resolution context;
duplicating the column shape — three short string slices — is cheaper
than spinning up a planner from the analyzer.

### Planner (`internal/planner/planner.go::planSubqueryRangeVar`)

Replace the target-list walk with an `innerSchema` walk. The inner
`Plan(rv.Subquery, cat)` call already expanded any star targets into
individual `SchemaColumn` entries with correct names (via
`expandStarTarget`) and types (via `targetMeta`); we simply project that
schema as the derived table's column list, applying the explicit
`(SELECT …) AS t (c1, c2)` aliases when present:

```go
innerSchema := inner.Output()
cols := make([]catalog.Column, 0, len(innerSchema))
schema := make(Schema, 0, len(innerSchema))
for i, sc := range innerSchema {
    name := sc.Name
    if i < len(rv.Columns) && rv.Columns[i] != "" {
        name = rv.Columns[i]
    }
    if name == "" {
        name = fmt.Sprintf("?column?%d", i+1)
    }
    cols = append(cols, catalog.Column{Name: name, Type: sc.Type})
    schema = append(schema, SchemaColumn{Name: name, Type: sc.Type})
}
```

This is a strict generalisation: `innerSchema` already contains the
names `targetMeta` would have produced for non-star targets (alias →
ColumnRef name → FuncCall name → typed-string-literal type-name →
`?column?`), so no existing derived-subquery shape changes meaning.

## Tests

- `internal/executor/operators_pg_get_publication_tables_test.go::TestIndirectionStarInsideDerivedSubquery`
  (was `t.Skip`) now passes: outer `SELECT gpt.relid` resolves against
  the wrapper's `relid` column.
- `TestIndirectionStarInsideDerivedSubqueryStarSelect` pins that
  `SELECT * FROM ( SELECT (srf(...)).* ) AS gpt` returns three columns,
  not one.
- `TestIndirectionStarDerivedSubqueryExplicitAliases` pins
  `… AS gpt (r, a, q)` — explicit column aliases override the SRF's
  default names and remain resolvable at the outer scope.

## Out of scope

The libpqrcv `fetch_table_list` probe's *actual* shape is
`(pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*` with
an aggregate in the SRF argument list. The IndirectionStar rewrite
explicitly refuses to move that into a FROM-clause SRF — the aggregate
must be evaluated first, which requires a ProjectSet-style plan node
(`Aggregate → ProjectSet(srf(arg))`). That is the next M0103-0008
sub-step.

This loop's deliverable narrows the probe-survival gap to a single
remaining piece: ProjectSet. The derived-subquery schema-propagation
hole is closed.

## Verification

```
$ go test -race -count=1 -timeout 300s \
    ./internal/parser/ ./internal/planner/ ./internal/analyzer/ \
    ./internal/executor/ ./internal/server/ ./internal/wal/ \
    ./internal/catalog/
ok  internal/parser    1.049s
ok  internal/planner   1.068s
ok  internal/analyzer  1.040s
ok  internal/executor  2.627s
ok  internal/server    3.563s
ok  internal/wal       3.128s
ok  internal/catalog   1.020s
```

## Follow-up: `tableFuncColumns` never threaded `WITH ORDINALITY` (2026-07-04)

A later loop (M0122-0002's FROM-clause `regexp_matches` follow-up) discovered
that `WITH ORDINALITY AS t(m, n)` raised `42703: column "n" does not exist`
whenever *either* the element column or the ordinality column was named
explicitly in the outer `SELECT` list, even though `SELECT *` over the same
FROM item worked. An initial diagnosis pointed at the planner, but
`wrapOrdinality`/`planFromUnnest`/`planFromRegexpMatches`
(`internal/planner/planner.go`) were always correct and never even run
before the failure — `planner.Plan()` calls `analyzer.Analyze()` first and
returns on its error (`internal/planner/planner.go`).

The real bug was in this design's own `tableFuncColumns`
(`internal/analyzer/analyzer.go`, called from `lookupTable`): it took the
bare function name and never received `rv.TableFunc.WithOrdinality` at all,
and had no `unnest`/`regexp_matches` cases — both silently fell to the
`default:` branch's single generic `int8` column named after the alias. The
ordinality column and the SRF's real per-element columns therefore never
existed in the analyzer's synthetic scope table, so naming either one
explicitly hit `lookupColumn` → `42703`. `*` was unaffected only because
`analyzeStar` returns immediately for an unqualified `*` with no
column-existence check at all — the real columns used downstream come from
`planSelect`, which the analyzer never cross-checks.

Fixed by changing `tableFuncColumns`'s signature to take the whole
`*parser.TableFuncRef` instead of a bare name string: it now strips the
trailing ordinality alias before dispatch (mirroring
`wrapOrdinality`/`planFromUnnest`'s `colAliases[:len-1]` slicing) and
re-appends a real `int8` ordinality column afterward, and gained `unnest`
(N-column zip sized to `len(tf.Args)`) and `regexp_matches` (`text[]`)
cases that previously hit the wrong generic default. The `unnest` element
type is a `text` placeholder rather than the array argument's real element
type, since `lookupTable` runs during scope-*building* with no
resolveContext/scope yet available to `analyzeExpr` the argument — recorded
as a known imprecision in `.ralph/deferral_ledger.md`, not a fixed bug (no
failing test currently depends on it; the pre-existing `default:` fallback
was equally imprecise, returning `int8` instead of any real element type).

Tests: `internal/analyzer/analyzer_test.go`'s
`TestAnalyzeWithOrdinalityNamedColumn` (named-column resolution across
`unnest`/`generate_series`/`regexp_matches`, single- and multi-arg
`unnest`, plus a genuine `42703` case). Verified end-to-end against a live
goopg server with a real PostgreSQL 18.3 `psql` binary.
