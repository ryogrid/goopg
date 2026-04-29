# 0016-0004 — CTE Observability and Compatibility Tests

**Status:** accepted (Stage A coverage)
**Milestone:** [0016 — WITH Clause (CTE) Support](../milestones/0016-with-clause-cte-support.md)
**Spans seam:** EXPLAIN labels for CTE producers/consumers, end-to-end
PG-shaped CTE compat tests.
**Cross-links:**
[0016-0001](0016-0001-with-parser-ast-and-name-resolution.md)
(parser AST + analyzer scope rules),
[0016-0002](0016-0002-nonrecursive-cte-planner-executor.md)
(non-recursive CTE planning + execution — Stage A inline substitution),
[0003-0007](0003-0007-explain.md) (existing EXPLAIN baseline).

## Context

M0016-0001 + M0016-0002 landed parser, analyzer, planner and
executor support for non-recursive CTEs. The Stage A
implementation inline-substitutes each CTE consumer with a freshly-
cloned plan of the CTE body — so by default an `EXPLAIN WITH ... `
output shows the inlined subtree with no indication that a CTE was
involved. That makes operator triage on CTE-heavy workloads
unnecessarily harder than upstream.

This slice closes the Stage A picture by adding **CTE Scan labels
to EXPLAIN output** and an **end-to-end compat-test suite** that
exercises representative PG-shaped CTE patterns. Recursive CTE
support (M0016-0003) remains blocked on `UNION ALL` (which goopg's
planner does not yet support); the runtime counter and full
materialise-once optimisation also wait until that work lands.

## CTEScan plan node

New planner node:

```go
type CTEScan struct {
    pos    int
    Name   string  // CTE name as written in the WITH list
    Alias  string  // alias used at this consumer site (defaults to Name)
    Child  Node    // the cloned CTE body
    schema Schema  // mirrors Child.Output() with alias-renamed names if (col, ...) present
}
```

`Output()` returns `schema`. `Pos()` returns `pos`. The wrap is
purely a label — the executor's `Build` recurses through `CTEScan`
to its `Child` and runs the cloned body unchanged. Stage A's
"zero new executor infrastructure" property is preserved.

Where the wrap is added: `planScanRangeVar`'s CTE-substitution
branch. Today it returns the cloned body directly; with this
slice it wraps the cloned body in a `CTEScan{Name, Alias, Child}`
so EXPLAIN can label it. The existing `rangeBinding` returned
alongside is unchanged — it carries the synthetic `*catalog.Table`
the rest of name-resolution uses.

## EXPLAIN integration

`internal/executor/operators_explain.go`'s switch gains a `case
*planner.CTEScan` arm that prints:

```
CTE Scan on a
  -> <Child>
```

(Two-line shape mirrors upstream's `EXPLAIN` output for `Subquery
Scan on …`. The CTE name prints unquoted; the alias prints
separately when distinct.)

## Executor Build integration

`internal/executor/executor.go`'s `Build` switch gains a `case
*planner.CTEScan` arm that simply calls `Build(p.Child)` and
returns the resulting Operator. No new operator type is needed
because the wrap is purely a labeling artifact at plan time.

## Compat tests

A new `internal/executor/with_compat_test.go` runs three
representative PG-shaped CTE patterns end-to-end to give the
milestone a meaningful "this works against PG-shaped queries"
gate:

1. **Filter-via-CTE** — `WITH big AS (SELECT * FROM t WHERE
   x > 100) SELECT count(*) FROM big`. Pins basic
   filter-then-aggregate.
2. **Multi-consumer-cross-product** — `WITH small AS (SELECT 1
   AS x) SELECT a.x, b.x FROM small a, small b`. Pins inlining +
   alias propagation.
3. **Chained-CTEs** — `WITH a AS (SELECT 1 x), b AS (SELECT
   x+1 y FROM a) SELECT y FROM b`. Pins left-to-right scope at
   the executor level (analyzer + planner already pin it).

## Out of scope

- Recursive CTE support — M0016-0003 (blocked on `UNION ALL`).
- Materialise-once optimisation — defer until recursive support
  lands; the optimisation is a perf win, not a correctness
  requirement, and would be most cleanly expressed as a shared
  Materialize node feeding multiple CTEScans.
- Runtime CTE counters in `pg_stat_*` views — defer; the
  inlining model makes per-CTE counters less informative than
  per-statement counters.
- TPC-H integration tests using CTEs — TPC-H Q15 uses a CTE
  (`revenue0`); flips to using the analyzer/planner CTE path
  automatically through this slice's plumbing, but TPC-H's
  Q15 also needs `CREATE OR REPLACE VIEW` semantics that goopg
  already supports separately. Specific TPC-H validation is
  follow-up.
