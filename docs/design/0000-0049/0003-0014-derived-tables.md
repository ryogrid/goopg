# Derived Tables — Subqueries in FROM (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [0003-0001-planner-overview.md](0003-0001-planner-overview.md), [0003-0008-subqueries.md](0003-0008-subqueries.md), [0003-0009-views.md](0003-0009-views.md) |
| Supersedes | —                                                      |

## Problem

TPC-H Q13 — count of customers grouped by their order count — uses
a derived table:

```sql
SELECT c_count, count(*) AS custdist FROM
  (SELECT c_custkey, count(o_orderkey) AS c_count
   FROM customer LEFT OUTER JOIN orders
     ON c_custkey = o_custkey AND o_comment NOT LIKE '%special%urgent%'
   GROUP BY c_custkey) AS c_orders
GROUP BY c_count
ORDER BY custdist DESC, c_count DESC;
```

Without `(SELECT …) AS alias` in the FROM clause, you can't write
this as a single SQL statement. v0's parser previously rejected
the construct at parse time, blocking Q13.

## Decisions

### Parser: extend RangeVar with an inner SelectStmt

`parser.RangeVar` grows a `Subquery *SelectStmt` field. When the
parser sees `(` followed by `SELECT` at FROM-item position, it
parses the inner SELECT, requires the `)`, then a mandatory alias
(matches upstream PG's "subquery in FROM must have an alias" rule).
The resulting `RangeVar` has `Name=""`, `Subquery!=nil`, and
`Alias=<required>`. Existing FROM-item handling — JOIN clauses,
multi-table comma-FROM — works on the outside unchanged.

The two-token lookahead `( + SELECT` disambiguates the
derived-table form from a parenthesised relation reference
(`( table_name )`); v0 doesn't yet support the latter and
upstream's grammar treats them identically modulo the alias
requirement. CTEs (`WITH ... AS`) are deferred.

### Analyzer: synthesize a `*catalog.Table` from the inner targets

`lookupTable(cat, rv)` now branches on `rv.Subquery != nil` and
calls `synthesizeSubqueryTable`. That helper:

1. Recursively analyzes the inner SELECT with no parent scope —
   v0 doesn't support LATERAL, so derived tables can't reference
   outer columns.
2. Walks the inner target list. Column names come from the
   explicit `AS alias`; otherwise from `deriveAnalyzerTargetName`
   (bare ColumnRef → its name, FuncCall → lower-cased function
   name); otherwise `?column?N`.
3. Calls `analyzeExpr` on each target against the inner scope to
   get the column's catalog.Type.
4. Returns an in-memory `*catalog.Table` with `Name=rv.Alias`
   and the synthesized columns. The table is never registered in
   the catalog — it lives only for the analyzer pass.

### Planner: plan the inner SELECT, expose its Output as the binding

`planScanRangeVar` mirrors the analyzer: when `rv.Subquery != nil`
it dispatches to `planSubqueryRangeVar`, which:

1. Plans the inner SELECT recursively via `Plan(rv.Subquery, cat)`.
2. Builds a synthetic schema from the inner plan's `Output()`,
   re-naming columns to match the analyzer's choices (alias /
   derived / `?column?N`).
3. Synthesizes a transient `*catalog.Table` so the rangeBinding
   contract — `binding.table` is `*catalog.Table` — holds for
   downstream column resolution.
4. Returns the inner plan node as-is. Outer ColumnRefs that
   reference `alias.col` resolve through the binding's column
   list, which carries the inner plan's types.

The outer planner (predicate pushdown, hash-join build-side
selection, ORDER BY/GROUP BY alias substitution) operates on the
inner plan's Output schema indistinguishably from a SeqScan over
a real relation. The inner plan can itself contain any planner
construct — joins, aggregates, sub-derived-tables, CASE, LIKE,
NUMERIC arithmetic — because we recurse through `Plan`.

## Verification

`TestPlanDerivedTable` pins four cases: explicit aliases, bare
ColumnRef target, derived-table-with-aggregate (Q13 shape), and
the parser-side missing-alias rejection.

End-to-end against `goopg start -D <dir>` with upstream psql 18.3:

```sql
CREATE TABLE customer (c_custkey int4, c_name text);
CREATE TABLE orders (o_orderkey int4, o_custkey int4, o_comment text);
INSERT INTO customer VALUES (1,'Alice'),(2,'Bob'),(3,'Carol'),(4,'Dave');
INSERT INTO orders VALUES (10,1,'normal'),(11,1,'normal'),
                          (12,2,'special urgent'),(13,4,'normal');

SELECT c_count, count(*) AS custdist FROM
  (SELECT c_custkey, count(o_orderkey) AS c_count
   FROM customer LEFT OUTER JOIN orders
     ON c_custkey = o_custkey AND o_comment NOT LIKE '%special%urgent%'
   GROUP BY c_custkey) AS c_orders
GROUP BY c_count
ORDER BY custdist DESC, c_count DESC;
-- 0 -> 2 (Carol, Bob — Bob's only order matched the LIKE filter)
-- 1 -> 1 (Dave)
-- 2 -> 1 (Alice)
```

## Out of scope (deferred)

- LATERAL — derived tables that reference outer columns. v0
  analyzes the inner with no parent scope.
- CTEs (`WITH name AS (SELECT …) SELECT …`). Upstream's planner
  treats them similarly to derived tables but the parser surface
  differs.
- `( table_name )` parenthesised relation references. The
  derived-table dispatch keys off `( + SELECT` so a bare
  parenthesised name still parses through the original RangeVar
  path — but v0 doesn't currently support it. (Upstream allows
  it as a pure-syntactic shape.)
- Set operations (`(SELECT ... UNION SELECT ...) AS alias`)
  inside a derived table. v0's set-op support lands later in M0003.
