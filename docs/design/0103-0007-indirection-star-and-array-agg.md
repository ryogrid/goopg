# 0103-0007 — `(srf(...)).*` IndirectionStar rewrite + `array_agg`

Status: accepted (2026-05-14)

Milestone: M0103-0008 (probe-survival, second sub-step).

## Context

The libpqrcv `fetch_table_list` probe upstream PG runs against goopg under
`CREATE SUBSCRIPTION` has the shape:

```sql
SELECT DISTINCT n.nspname, c.relname, gpt.attrs
  FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
           FROM pg_publication
           WHERE pubname IN ( … ) ) AS gpt
    ON gpt.relid = c.oid
```

`0103-0006` landed the `VARIADIC` keyword + `pg_get_publication_tables`
SRF. The next barrier was the `(srf(...)).*` indirection-star: PG's grammar
allows a parenthesised expression followed by `.*` to expand a composite
(record) return value into its constituent columns. goopg's parser
rejected the `.` after `)` with `expected ')' to close subquery`.

This loop closes:
1. The parser-level `(expr).*` syntax (new `IndirectionStar` AST node).
2. The `array_agg(expr)` aggregate (previously fell through to a stubbed
   default branch that returned NULL).
3. A planner-side path that maps a target-list `(srf(consts)).*` into a
   FROM-clause SRF reference, so simple non-aggregate-arg forms run
   end-to-end.

## Decision

### Parser (`internal/parser/expr.go`, `internal/parser/select.go`)

New AST node:

```go
type IndirectionStar struct {
    pos    int
    Source Expr
}
```

`parsePrimary` recognises the postfix after closing `)`:

```go
inner := <parenthesised expression>
if p.cur().Value == "." && p.peek(1).Value == "*" {
    p.advance(); p.advance()
    return &IndirectionStar{pos: t.Pos, Source: inner}, nil
}
return inner, nil
```

A new package-level helper `RewriteIndirectionStarTargets(s, onAggregate)`
walks the SelectStmt's target list and rewrites every `IndirectionStar`
whose Source is a `*FuncCall` into a FROM-clause SRF reference:

```text
Targets: [(srf(args)).*]   →  Targets: [__irs_0.*]
From:    [...]              →  From:    [..., RangeVar{TableFunc: {srf, args}, Alias: "__irs_0"}]
```

`FromExprs` is updated symmetrically when non-empty so the explicit-JOIN
path continues to see the SRF as a flat extra FROM item. When `FromExprs`
is empty (single-FROM fast path), it stays empty so `planSelect`'s
`isSimpleSingle` shortcut still fires.

When `fc.Args` contains an aggregate-function call (per a lightweight
`exprContainsAggregateCall` walker that mirrors `planner.isAggregateFunc`
), the rewrite leaves the `IndirectionStar` in place. The planner runs
the same helper at `Plan()` entry with a non-nil `onAggregate` callback
that surfaces a clean PG-compatible PlanError:

```text
0A000: set-returning function with aggregate argument
       (e.g. (srf(array_agg(...))).*) requires ProjectSet support —
       not yet implemented (M0103-0008 next sub-step)
```

`parseSelect` invokes `RewriteIndirectionStarTargets` as its final step
so every parsed SelectStmt (including nested subqueries inside derived
tables, subquery expressions, UNION branches) gets the rewrite for free.

### Analyzer (`internal/analyzer/analyzer.go`)

A passthrough `*parser.IndirectionStar` case in `analyzeExpr` walks the
source and returns `record`. This only fires for aggregate-arg cases
(simple cases never reach the analyzer because the parser-level rewrite
substituted a `StarExpr` already). The planner still rejects the
aggregate-arg case before execution; the analyzer pass just prevents the
generic "unsupported expression" error from masking the dedicated
PlanError.

### Planner (`internal/planner/planner.go`)

`Plan()` calls a thin adapter `rewriteIndirectionStarTargets(s)` that
re-runs the parser-level helper with a planner-supplied `onAggregate`
callback returning a `*PlanError` (code `0A000`). The same helper is
called from `planSelect` entry as well so nested-SELECT planning paths
(`planSelectWithParent`, UNION branches) that bypass `Plan()` still
trigger the rewrite. Idempotent — a second call on an already-rewritten
SelectStmt is a no-op.

`walkExpr` learnt a `*parser.IndirectionStar` case (walks `Source`) so
aggregate detection inside the source funcCall still works for the
parser-side `exprContainsAggregateCall` check.

### `array_agg` (`internal/executor/operators_join_agg.go`)

`aggRuntime` gains two parallel slices `arrayElems []string` and
`arrayElemNull []bool`. `applyAgg`'s `"array_agg"` case appends
`arg.Format()` per non-NULL row (NULLs are already short-circuited by
the IsNull early-return upstream of the switch — sufficient for the
probe's `array_agg(pubname::text)` since `pg_publication.pubname` is
NOT NULL). `finishAgg`'s `"array_agg"` case emits the PG text-array
literal via the existing `formatTextArray(elems)` helper:

```text
SELECT array_agg(k) FROM t WHERE k IN ('a','b','c');
 array_agg
-----------
 {a,b,c}
```

The `arrayElemNull` slice is reserved for the NULL-aware variant
(`{a,NULL,b}` shape) that becomes relevant once aggregate inputs lose
the IsNull short-circuit — out of scope for this loop.

## Out of scope (next sub-step)

Two gaps remain before `fetch_table_list` runs end-to-end against
goopg:

1. **ProjectSet for aggregate-arg SRFs.** The probe's actual shape
   `(pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*`
   has an aggregate inside the SRF argument list. The planner-side
   rewrite cannot move the SRF into the FROM clause because the
   aggregate must be evaluated first. PG's plan is
   `Aggregate → ProjectSet(srf(arg))`. goopg has no ProjectSet
   operator yet. This loop's planner emits a clean PlanError for
   this shape; closing it is the next M0103-0008 sub-step.
2. **Derived-subquery schema propagation.** Even after the parser
   rewrite fires inside the derived `(SELECT __irs_0.* …)` body,
   the outer SELECT cannot resolve `gpt.relid` because the analyzer
   /planner do not propagate a FROM-clause SRF's column list out
   through the wrapper subquery's `__irs_0.*` target. The simple
   `SELECT (srf(consts)).*` path proves the IndirectionStar rewrite
   itself is correct; what is missing is the subquery → outer-SELECT
   column flow for SRF-derived columns. Pinned as `t.Skip` in
   `TestIndirectionStarInsideDerivedSubquery` with a forward
   reference to the next sub-step.

## Tests

- `internal/parser/select_test.go::TestParseIndirectionStarFuncCall`
  pins the post-parse rewrite shape (`__irs_0.*` target +
  TableFuncRef FROM item).
- `internal/parser/select_test.go::TestParseIndirectionStarFetchTableList`
  pins parse of the full upstream `fetch_table_list` query (aggregate
  arg → IndirectionStar stays in place, no parse-time error).
- `internal/executor/operators_pg_get_publication_tables_test.go::TestIndirectionStarTargetListPlansAsFromSrf`
  end-to-end: `SELECT (pg_get_publication_tables('p')).*` returns the
  same rows as the equivalent FROM-clause form.
- `internal/executor/operators_pg_get_publication_tables_test.go::TestIndirectionStarRejectsAggregateArgument`
  pins the planner's PG-compatible rejection of aggregate-arg cases
  pending ProjectSet support.
- `internal/executor/operators_pg_get_publication_tables_test.go::TestArrayAggText`
  pins `array_agg(text)` → `{a,b,c}` literal.
- `internal/executor/operators_pg_get_publication_tables_test.go::TestIndirectionStarInsideDerivedSubquery`
  documents the next-sub-step gap via `t.Skip`.

## Verification

```
$ go test -count=1 -timeout 120s ./internal/parser/ ./internal/planner/ \
    ./internal/analyzer/ ./internal/executor/ ./internal/server/ \
    ./internal/wal/ ./internal/catalog/
ok  internal/parser   0.013s
ok  internal/planner  0.020s
ok  internal/analyzer 0.012s
ok  internal/executor 1.171s
ok  internal/server   1.766s
ok  internal/wal      1.900s
ok  internal/catalog  0.005s
```
