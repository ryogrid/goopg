# Routing 2026-10-10: Q72 SF1 sort strategy, Q95 web_returns IOS

This is a read-only pass over the plan text in this directory, plus the goopg
optimizer code and the PG 18.3 source. No server was run. Costs below are
quoted from the captures. Where a number is derived, the formula is given.

## Q72 SF1: depth=3 [sort-strategy] under GroupAggregate

PG's plan (first-divergence-sf1.txt:72):

```
GroupAggregate  (cost=53196.45..53219.17 rows=2)
  ->  Nested Loop Left Join  (cost=53196.45..53219.12 rows=2)        -- promotion, Materialize inner
        ->  Nested Loop  (cost=53196.45..53198.37 rows=2)            -- d3 Index Scan 0.29..0.31
              ->  Gather Merge  (cost=53196.16..53196.73 rows=5)  Workers Planned: 1
                    ->  Sort  (cost=52196.15..52196.15 rows=3)  Key: i_item_desc, w_warehouse_name, d1.d_week_seq
                          ->  Nested Loop  (cost=44281.35..52196.12 rows=3)   -- {cs,inv,w,i,cd,hd,d1,d2}
```

goopg's plan:

```
GroupAggregate  (cost=61205.79..61205.86 rows=2)
  ->  Sort  (cost=61205.79..61205.79 rows=2)
        ->  Nested Loop Left Join  (cost=48936.50..61205.78 rows=2)
              ->  Nested Loop  (cost=48936.50..61186.03 rows=2)      -- d3 Bitmap Heap Scan 8.26..12.27
                    ->  Gather  (cost=48928.24..61108.13 rows=5)  Workers Planned: 1
                          ->  Nested Loop  (cost=47928.24..60107.63 rows=3)   -- same 8 rels
```

**Where the shapes split.** The node under both Gathers covers the same
eight relations: catalog_sales, inventory, warehouse, item,
customer_demographics, household_demographics, d1 and d2. That is the
`join_collapse_limit = 8` sub-joinlist. Q72's FROM is a chain of explicit
JOINs, and `catalog_returns` is removed as a useless left join. So the
top-level joinlist is `[sub8, d3, promotion]`. This is the split M0146-0141
already identified.

**PG keeps both gathered paths of sub8.**
- `standard_join_search` calls `generate_useful_gather_paths` on sub8,
  because sub8 is not `all_query_rels`.
- Gather8 is 52196.12 + 1000 + 0.1×5 = 53196.62.
- GM8 is Gather Merge over a worker Sort, 53196.16..53196.73. It is 0.11 more
  than Gather8.
- `add_path` (util/pathnode.c) compares GM8 with Gather8. Totals are fuzzily
  equal. Gather8's startup (45281.35) is better, so the result is
  COSTS_BETTER2. GM8 has PATHKEYS_BETTER1, so neither path is removed.
- `make_rel_from_joinlist` (path/allpaths.c) returns sub8's whole RelOptInfo
  to the enclosing search.
- In the enclosing search, `match_unsorted_outer` (path/joinpath.c) builds the
  d3 and promotion nested loops over GM8.
- `build_join_pathkeys` keeps the outer ordering through both loops, so
  `add_paths_to_grouping_rel` gets a presorted input and needs no Sort.

**goopg commits sub8 to one path.**
- `makeRelFromJoinlist` → `searchOneProblem` → `finalPath()`
  (internal/optimizer/relfromjoinlist.go:738) is called with
  `tupleFraction = 0`. It returns `CheapestTotal`, and
  `createPlanAtSearchRootRange` publishes that path as one Node.
- The enclosing problem admits that Node as a single `PathPrebuilt` leaf. The
  leaf has no pathkeys and no partial paths.
- goopg's sorted arm (`generateUsefulGatherPaths`, gatherpaths.go:230-258)
  does file GM8 on sub8. Priced by PG's formula:
  - worker Sort: 60107.63 + 0.005×3×log2(3) = 60107.654, run +0.0075.
  - GM: +0.01 heap, +1000, +5×(0.005 + 0.0025 + 0.105).
  - Total ≈ 61108.23.
- GM8 loses to Gather8 (61108.13) by +0.10 (0.0002%). It is discarded at the
  boundary.
- In the enclosing search no ordered sub8 path exists. No partial path exists
  for a Gather Merge at {sub8, d3} or at the top rel either. The only way to
  feed the GroupAggregate is the Sort above the join, which is what goopg
  prints.
- The relfromjoinlist.go file header documents this collapse ("the enclosing
  search can no longer pick a DIFFERENTLY-SORTED path for the sub-problem").
- The pathlist half of ledger row M0127-P5.9-a (`.ralph/deferral_ledger.md:833`)
  is still open. M0146-0141 fixed only the rows half.

**What does not decide it.**
- **Cardinality.** Gather rows are 5 = 5 and top rows are 2 = 2 on both sides.
- **Cost.** The two gathered paths are 0.10 apart in goopg and 0.11 apart in
  PG. PG keeps both paths and goopg keeps one.
- **B8 and RELPAGES.** goopg's larger totals (61205 vs 53219) come from below
  and beside the split, and do not move the sort placement:
  - the d1 Memoize probe instead of a Parallel Hash;
  - the inventory probe, 10.42 rows=3 vs 5.98 rows=1;
  - the d3 Bitmap Heap Scan at 12.27 vs 0.31 (B8).

Even at PG's exact costs, goopg could not build PG's shape, because the
ordered candidate never crosses the sub-problem boundary.

**Trace that would confirm it.** A goopg `addPath` trace at sub8's final rel
should show `gather.merge.sort` filed at about 61108.23 and `finalPath`
returning the Gather at 61108.13. A second check: goopg with
`join_collapse_limit = 10` makes the chain one problem, so the enclosing
levels see GM8. That run should reproduce PG's Gather Merge placement. It is
a probe only, because it changes PG's plan too.

**PG mechanism to port.** `make_rel_from_joinlist` hands the sub-problem's
full pathlist across the boundary: the sorted Gather Merge paths built by
`generate_useful_gather_paths`, and in general every pathkey-distinct
survivor. goopg would need to publish more than one path per sub-problem: at
minimum the cheapest path for each useful ordering, beside CheapestTotal.

ROUTE: NEW — sub-joinlist boundary collapses the pathlist (`make_rel_from_joinlist` returns the RelOptInfo; goopg's `searchOneProblem`/`finalPath` publishes only CheapestTotal = Gather 61108.13, discarding the sorted Gather Merge ≈61108.23 PG builds its NL chain on); ledger M0127-P5.9-a pathlist half

## Q95 SF0.25: depth=6 [scan-type] under Hash Join

Quoted lines:

```
PG    ->  Index Only Scan using web_returns_pkey on web_returns  (cost=0.29..134.71 rows=2 width=4)
            Index Cond: (wr_order_number = ws1.ws_order_number)
goopg ->  Index Scan using web_returns_pkey on web_returns       (cost=0.25..89.89 rows=2 width=348)
            Index Cond: (wr_order_number = ws1.ws_order_number)
```

**Why an index scan on a hash side.**
- `ws1.ws_order_number IN (SELECT wr_order_number FROM web_returns, ws_wh ...)`
  is pulled up as a semijoin with the RHS {web_returns, ws_wh_1}.
- The equivalence class {ws1.ws_order_number, wr_order_number,
  ws_wh_1.ws_order_number} lets PG parameterise that RHS by ws1. The
  Nested Loop Semi Join (outer rows=1) runs it once per outer row.
- Inside the RHS, the hashed side is a parameterised probe of web_returns.
  wr_order_number is the second key of `web_returns_pkey (wr_item_sk,
  wr_order_number)`, so this is a PG 18 skip-scan probe: 2 rows, 134.71.
- The probe side is the 1.75M-row CTE Scan, with PG's Join Filter
  (M0146-0135).
- goopg builds the same parameterised hash join and the same probe. Only the
  scan type differs.

**Why PG's probe is index-only.**
- `check_index_only` (path/indxpath.c) is true: the only web_returns column
  the query reads is wr_order_number, and the pkey stores it.
- `build_index_paths` then builds the one path for this index with
  `index_only_scan = true`. The visibility map (`cost_index`'s `allvisfrac`)
  only prices that path.
- So index-only is not a cost election against a plain Index Scan. There is
  no plain-scan candidate for this index.

**Why goopg's probe is a plain Index Scan.**
- The skip probe in `pathparamindex.go:589` makes the index-only decision
  only when `s.neededColsKnown`. So does the prefix probe at :395.
- For Q95 that flag is false.
- `planSelect` calls `neededColumnNames(s)` (planner.go:1967) on the
  statement with its WITH list still attached. The list is stripped only on
  the set-op arm, at planner.go:1410.
- `collectStmtColumnNames` declines any `s.With != nil` outright
  (internal/optimizer/pathindexonlyneed.go:412).
- The whole statement therefore has an unknown needed set, and no base rel in
  Q95's main body is offered an index-only path.

**Supporting evidence.**
- Q94 has no WITH clause and the same `web_returns_pkey` probe. There goopg
  prints `Index Only Scan using web_returns_pkey on web_returns wr1
  (cost=0.25..89.87 rows=2 width=4)`. So the producer, the skip probe and the
  visibility pricing all work. The gate is the only difference.
- The goopg IOS in the WITH queries Q23, Q39 and Q64 are all inside the
  bodies of their materialised CTEs. Q78's sit in its inlined CTE subqueries.
- Q77's main body (its CTEs are inlined) has PG's three IOS (store_pkey and
  web_page_pkey ×2) and goopg has none. That is the same gate, but it is not
  Q77's first divergence.

**What this is not.**
- Not visibility: the probe would cost 89.87 as IOS against 89.89 as a plain
  scan, and either way it is the only path for this index.
- Not cost.
- Not B8, B-15 or RELPAGES.
- PG's higher probe cost (134.71 vs goopg's 89.89) is a separate skip-scan or
  descent costing gap (B-15 family). It does not decide the scan type.

**Fix direction.**
- Attribute needed columns per relation, as PG does with `attr_needed` /
  `build_base_rel_tlists`, independent of the CTE list.
- Or teach `collectStmtColumnNames` that a WITH list hides no column of the
  main body's own base relations. A CTE body is planned by its own
  `planSelect`, and a CTE reference is a CTE Scan, not a base rel.

ROUTE: NEW — `collectStmtColumnNames` declines any statement with a WITH clause (pathindexonlyneed.go:412), so `neededColsKnown=false` and `check_index_only`'s analogue never marks the web_returns_pkey probe index-only (Q94's identical probe without WITH is IOS 0.25..89.87)

## Q95 SF1: depth=7 [scan-type] under Hash Join

Quoted lines:

```
PG    ->  Index Only Scan using web_returns_pkey on web_returns  (cost=0.29..538.54 rows=2 width=4)
goopg ->  Index Scan using web_returns_pkey on web_returns       (cost=0.25..359.10 rows=2 width=186)
            Index Cond: (wr_order_number = ws1.ws_order_number)   (both)
```

The depth is one deeper than at SF0.25 for two reasons:
- At SF1 both engines put a `Sort (ws1.ws_order_number)` above a plain Gather
  for the DISTINCT aggregate.
- At SF0.25 the ordering comes from a Gather Merge instead.

Everything above the probe now agrees, including the M0146-0135 Join Filter:
- the parameterised hash join, 538.56..169714.13 (PG) vs 359.13..167485.60
  (goopg);
- the Nested Loop Semi Join over a 3-worker Gather.

The mechanism is the same WITH gate:
- Q94 at SF1 has the identical probe as an IOS in goopg: `Index Only Scan
  using web_returns_pkey on web_returns wr1 (cost=0.25..359.09 rows=2
  width=4)`. That is 0.01 under the plain scan goopg prints for Q95.
- The cost gap (359 vs 538.54) is again the separate skip-scan costing term,
  not the scan-type decision.

ROUTE: NEW — same WITH-clause decline of the needed-column set (`neededColsKnown=false`) as SF0.25; Q94's identical probe at SF1 is IOS 0.25..359.09
