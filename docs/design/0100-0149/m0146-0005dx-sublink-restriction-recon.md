# M0146-0005dx — a correlated sublink restriction of one rel: recon and split

Status: done (2026-10-04, recon). Parent: M0146-0005. Filed by slice 115.
Split into M0146-0005dx1 and M0146-0005dx2.

## The divergence

TPC-DS Q10 and Q35 filter `customer c` with
`(EXISTS (… web_sales … c.c_customer_sk …) OR EXISTS (… catalog_sales …))`.

| | where the qual sits | how it is priced |
|---|---|---|
| PG 18.3 | `Filter:` on the `customer_pkey` Index Scan, the inner of a nested loop over the unique-ified `store_sales ⋈ date_dim` (1141 loops) | per row, by the plain correlated SubPlan |
| goopg | `Filter:` on the Gather above the whole join tree | after the search, outside it |

Both engines execute the qual as hashed `ANY` SubPlans.

## PG's mechanism

1. **Placement.** `distribute_qual_to_rels` (`./postgres/src/backend/optimizer/plan/initsplan.c`)
   distributes a clause by its relids. A SubPlan's correlation arguments
   (`args`) read only `c`, so the OR is a `baserestrictinfo` of `c`. It is
   evaluated wherever `c` is scanned: a seq scan's filter, or an index
   probe's.
2. **Pricing.** `make_subplan` builds both a plain and a hashed SubPlan and
   wraps them in an `AlternativeSubPlan`. `cost_qual_eval_walker`
   (`./postgres/src/backend/optimizer/path/costsize.c:5027-5038`) prices an
   AlternativeSubPlan by its *first* alternative, the plain correlated
   SubPlan. That makes the restriction cost about 21123 per `c` row in Q10.
   PG therefore drives from the cheapest outer, the unique-ified
   `store_sales` semi-join side (M0146-0005dy), and probes `c` once per
   distinct customer. setrefs keeps the hashed alternative for execution.

## goopg today

- **The decline.** `conjunctLocalEligibility` (local\_filters.go) refuses a
  correlated sublink as a leaf restriction. A correlated sublink's plan
  addresses the scope's joined row through `OuterColumnRef`s, which would
  have to be rebased into the leaf's coordinates. That is the documented
  decline; `remapOuterRefsInSubplan` already exists, with one live caller in
  predp.go. So the conjunct stays in the residual above the search.
- **The hashed form.** `rewriteExistsToAny` (exists\_to\_any.go) produces
  it after planning. It walks `Filter`, `Join` and NLI predicates, but not
  an index or bitmap scan's own `Cond`.
- **Pricing.** No search-time cost for a correlated sublink restriction
  exists.

## Why one slice is not enough

- **Admitting the qual at the leaf alone** would place it at the `c` scan,
  priced near free. The search would then keep `c` early, the opposite of
  PG's choice.
- **Pricing alone** has nothing to price, because the qual never reaches
  the search.
- **Risk.** M0146-0015a measured an earlier attempt to sink correlated
  quals: it broke the EXISTS→ANY rewrite and timed out Q35. Each step needs
  its own fire-set measurement.

## Split

- **M0146-0005dx1:** admit a correlated sublink conjunct whose correlation
  reads exactly one base rel as that rel's restriction.
  - Rebase the sublink's plan with `remapOuterRefsInSubplan`.
  - Extend `rewriteExistsToAny` to scan `Cond`s (IndexScan, IndexOnlyScan,
    BitmapHeapScan), so a qual carried by an index probe still becomes a
    hashed ANY.
  - Land together with dx2.
- **M0146-0005dx2:** price a restriction holding a correlated sublink per
  row by the plain SubPlan's per-call cost (`cost_subplan`), as
  `cost_qual_eval` prices an AlternativeSubPlan.

Q10, Q35 and Q69 also need M0146-0005dy, the unique-ified multi-relation
semi-join inner, to take PG's join order.
