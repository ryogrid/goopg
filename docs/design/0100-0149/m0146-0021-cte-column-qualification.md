# M0146-0021: CTE consumer columns render qualified, as PG prints them

Status: landed 2026-09-26. The derived-table (`FROM (SELECT …) alias`)
output chase is open.

## PG behaviour

`ruleutils.c` qualifies every Var in a multi-relation plan
(`deparse_context.varprefix`). How it names a column read from a CTE
depends on what the planner did with the reference:

- **Kept CTE** (`MATERIALIZED`, or referenced more than once): the Var
  points at the `CTE Scan` RTE, so the column prints under the
  reference's alias: `t_s_secyear.customer_id`.
- **Inlined CTE** (`inline_cte`, M0146-0007): the reference is a
  subquery RTE. When setrefs removes a trivial `Subquery Scan`
  (`trivial_subqueryscan`), the parent's Vars resolve through the body's
  target list, so they print as the body's source column. For example,
  `WITH x AS (SELECT id FROM va) … x.id` prints `va.id`. A grouped body
  prints the grouping column: TPC-DS Q77 prints
  `store.s_store_sk = store_1.s_store_sk`.

goopg printed such columns bare (`customer_id = customer_id`,
`s_store_sk = s_store_sk`). The CTE scan carried no per-level source
index, so `columnIn` could not tie the ColumnRef to a scan.

## Change

- `CTEScan` and `MaterializedCTEScan` carry `SourceIdx`, the consumer
  binding's per-level SourceTableIdx (set in `planScanRangeVar`'s CTE
  arm). `explainSingleSourceIdx` returns it, so a kept reference
  registers under its alias and qualifies like any scan.
- `explainNames.transparentCTEFor(at, src)` walks up from the rendering
  node to the scan owning `src` at that query level. It returns the
  scan when that scan is an inlined `CTEScan` that EXPLAIN renders
  transparently (no `Subquery Scan`: no attached non-`true` Filter).
- `formatThroughInlinedCTE` matches the column by name in the scan's
  output (a duplicate name declines). It chases the position through the
  body with `resolveKeySource` and renders the result inside the body's
  scope. A decline falls back to the alias-qualified text.
- `resolveKeySource` gains two steps:
  - Gather and Gather Merge pass their child's columns through, as Sort
    does.
  - An Aggregate grouping position whose key is a base-table column
    stops and renders that column, as the Project arm already did. The
    parallel form of a grouped body (Aggregate → Gather Merge → Sort →
    join) has no narrowing Project for the chase to stop at. That was
    Q77's `sr` side. A key whose qualifier names a CTE still declines.

## Results

- SF0.25 census: Q97 moves past its Merge Join. It now first diverges at
  `PG Group | goopg HashAggregate`. Rendering records fall 29 → 25.
- SF1 census: Q97 moves the same way. Rendering records fall 29 → 23 and
  qual-placement records 24 → 23.
- Q77 SF0.25 prints PG's text (`store.s_store_sk = store_1.s_store_sk`)
  and stays at its earlier depth-6 record. An intermediate build that
  qualified by alias alone moved it *earlier*.
- Q74/Q31 now print `t_s_secyear.customer_id = …`. Their remaining
  record is a real difference: goopg keeps every transitive equality as
  a Join Filter conjunct, while PG's EquivalenceClass keeps one per join.
- Row counts unchanged, and the TPC-H census is identical. Evidence:
  `analysis/m0146/m0146-0021/`.

## Open (ledgered)

1. Derived-table outputs (TPC-DS Q46/Q68 `bought_city`): PG prints
   `addr.ca_city` through the removed `Subquery Scan`. goopg's derived
   table is a planned subtree without a `CTEScan` node, so the chase has
   no source-index anchor. The same helper applies once the subquery
   reference carries one.
2. `NOT MATERIALIZED` multi-reference CTEs (M0146-0007 item 2) plan
   per reference in PG. Their columns will chase once goopg plans them
   separately.
