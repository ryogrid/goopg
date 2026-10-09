# M0146-0005ad — Q59 routing: no FROM-subquery pull-up (2026-09-28, HEAD 9486e60a5)

- `q59-dptrace-problems.txt`: `GOOPG_PGSHAPED_DP_TRACE=1` on a private SF0.25
  clone (:5544). goopg solves three separate join problems — `{wss,store,d}`
  twice (subqueries y and x) and `{y,x}` — so PG's 6-relation order
  (x's `wss_1 ⋈ store_1` joined to all of y, `date_dim d_1` last through a
  nested loop over a Materialize) is not in goopg's search space.
- Cause: `planSubqueryRangeVar` (planner.go) plans every FROM-clause
  subquery as its own scope; M0146-0005w only drops the `Subquery Scan`
  label for simple bodies. PG's `pull_up_simple_subquery`
  (prepjointree.c) splices a simple body's FROM items into the parent
  jointree. Not affected by `GOOPG_INDEX_PROBE_MULT=1`.
- Reach (paren-balanced scan of the 99 TPC-DS queries for simple
  multi-relation FROM subqueries): Q2, Q51, Q59, Q93. Only Q2 and Q59 have
  a sibling FROM item, so only they can change join order; Q2 currently
  diverges earlier (aggregation strategy under its CTE). TPC-H Q7/Q8/Q9
  wrap their whole join in the lone FROM subquery, so their inner search
  already covers every relation.
- Filed: M0146-0028 (impl).
