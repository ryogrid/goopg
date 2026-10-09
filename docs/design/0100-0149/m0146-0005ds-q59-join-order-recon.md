# M0146-0005ds — recon: Q59's join order after the expression class member

Status: done (2026-10-03, recon). Parent: M0146-0005 (banner item 3).

## Question

After M0146-0005do, goopg joins `d` to `{wss_1, store_1, d_1}` first, on
the derived clause `(wss_1.d_week_seq - 52) = d.d_week_seq`. PG joins
`{wss, store, d}` to `{wss_1, store_1}` on two conditions, then `d_1` by
Nested Loop over a Materialize. goopg's Sort above the final join also
reads 4811 rows over a 15-row join. Why?

## Findings

1. **The clause space is the same.** Since 0005do the class
   `{wss.d_week_seq, d.d_week_seq, wss_1.d_week_seq - 52}` exists in both
   engines, so every join order PG considers is open to goopg.
   - The derived join's selectivity is the same reasoning in both:
     `eqjoinsel` with the expression side unstatted takes
     `1 / max(nd1, nd2)`. For an unstatted expression PG's
     `get_variable_numdistinct` returns `DEFAULT_NUM_DISTINCT` (200,
     `isdefault`), so `d.d_week_seq`'s about 10k wins the `max`.
   - goopg's 377 × 133 / 10k ≈ 5 rows is what PG would compute for the
     same join.
   - So the order difference is an election on cost, not a missing
     candidate.
2. **The 4811 is not a joinrel size.** Join paths take `joinRel.Rows`
   (pathgen.go `addHashJoinPath`, joinpathsnli.go, joinpathsmerge.go), so
   paths and rels agree. The Sort above is a legacy-priced node:
   - `DeriveLegacyDisplayCost` reads its child through `EstimateRows`;
   - `EstimateRows`' `*Join` arm is `return estimateJoin(x)`
     (cardinality.go), the pre-search estimator. It ignores the join's
     stamped `PlanCost.PlanRows`, the number the search chose.
   - `stampedUpperRows` already prefers the stamp for `*Distinct` /
     `*DistinctOn` (M0146-0005bg), but not for joins.
   - In PG an upper path reads its input path's `rows`
     (`create_sort_path`, `cost_sort(…, subpath->rows, …)`), which is the
     joinrel's.
   - The 0005do baseline already showed this disagreement in 13 queries
     (Q43, Q46, Q47, Q57, Q62, Q65, Q66, Q77, Q81, Q99); Q59 joined them.
   - Every legacy upper node above a searched join (Sort, Aggregate group
     estimates, Limit, WindowAgg) is sized by the wrong estimator, and so
     is its cost.

## Routing

- Finding 2 is a cardinality defect. It is filed as **M0146-0009k**
  (statistics/cardinality family, interleaveable per the banner), with
  its expected movement.
- Finding 1 stays a cost-election question. It should be re-measured
  after 0009k, because the legacy rows feed the costs of the upper nodes
  the search compares above Q59's final join.
