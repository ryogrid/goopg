# M0146-0181 — a column through an inlined CTE deparses in the body's own scope

Status: closed 2026-10-14.

## Problem

TPC-DS Q58 joins three grouped CTEs (`ss_items`, `cs_items`, `ws_items`) by
merge joins whose Join Filters compare their sums. At SF0.25 goopg printed
the `ws_items` sum wrong in both filters:

- the top join printed `sum(ss_items.item_id)`;
- the inner join printed `sum(cs_items.item_id)`.

PG 18.3 prints `(sum(web_sales.ws_ext_sales_price))` in both places. It also
wraps every such aggregate in parentheses, which goopg did not do for any of
the three.

The values were always right; only the EXPLAIN text was wrong. The bug
became visible after M0146-0177 let the upper narrowing reach these
aggregates, because the narrowed aggregate's argument moved to input
position 0.

## Diagnosis

A temporary stack print at the point where the column resolved to
`ss_items.item_id` gave the route:

1. `formatJoinFilter` renders the residual with `reg.joinRow` set to the
   join. That is how a residual's columns are resolved positionally
   (`joinResidualColumn`, M0146-0005as).
2. `ws_items.ws_item_rev` belongs to a transparent inlined CTE, so
   `formatThroughInlinedCTE` chases it into the CTE body
   (`resolveKeySource`). It gets `sum(ws_ext_sales_price)`, whose argument
   indexes the aggregate's own input.
3. The body expression was formatted with the join row still set. The
   argument's index 0 was then read against the join row, where position 0
   is the other CTE's `item_id`.

The other two sums printed correctly only because their argument indexes
happened not to resolve in the join row, so name-based resolution took over.

PG never mixes the two scopes. `get_variable` (ruleutils.c) resolves an
OUTER_VAR / INNER_VAR into the child plan's target list and deparses the
expression it finds in that plan's own context. A grouped CTE is not a
simple subquery (`is_simple_subquery`, prepjointree.c), so it stays a
subquery RTE. When `trivial_subqueryscan` removes its scan, the join's Var
points at the Agg's target list, and `get_variable` parenthesises the
non-Var target it prints there.

## Change

In `formatThroughInlinedCTE` (operators_explain.go):

- The body expression is formatted with the join row cleared
  (`reg.withJoinRow(nil, …)`), inside the body's scope, which the function
  already entered.
- A non-column result is parenthesised when the body is one PG keeps as a
  subquery. `cteBodyKeptAsSubquery` walks the body's top spine through
  Project, Materialize, Sort, Filter and Gather wrappers, and answers true
  when it reaches an Aggregate, WindowAgg, Distinct, DistinctOn or Limit
  before a scan or join. A pulled-up body's expression keeps its own
  rendering, since PG substitutes it directly and adds no parentheses.

## Evidence

- **Q58 at SF0.25:** both Join Filters now print the same terms as PG,
  parenthesised: `(sum(web_sales.ws_ext_sales_price))`,
  `(sum(catalog_sales.cs_ext_sales_price))`,
  `(sum(store_sales.ss_ext_sales_price))`. The clause order still differs
  because the two engines order the three branches differently.
- **Fixture:** two grouped CTEs joined on their key with a filter on their
  sums print PG 18.3's exact
  `Join Filter: ((sum(f1.v)) > (0.9 * (sum(f2.w))))`. HEAD printed
  `(sum(a.sa) > (0.9 * sum(a.sa)))`.
- **Test:** `TestExplainInlinedCTEAggregateInJoinFilter`. It fails with
  either half reverted: `sum(b.bk)` without the join-row reset, and no
  parentheses without the second half.

## Not ported (ledgered)

- A grouped CTE whose output PG does not keep trivially stays a
  `Subquery Scan on b`. PG then prints the alias (`b.sb`, `b.bk`), while
  goopg still resolves through the scan (`(sum(f2.w))`, `f2.k`). The Hash
  Cond half of this was already wrong before the change.
  `transparentCTEFor` does not consult whether the scan is elided
  (`walkPlanFiltered` elides an inlined CTE scan only when no filter is
  attached), and PG's own trivial-scan rule (`trivial_subqueryscan`)
  depends on the target lists.
