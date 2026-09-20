# M0144-0011 — vertical slice 1: TPC-DS SF0.25 Q8 five-layer trace

**Slice pick.** Top first-divergence cluster (m0144-0002 census):
`Limit → PG {GroupAggregate | Incremental Sort | Sort+CTE} | goopg Sort`
at depth 1 — ~25 records SF0.25, ~20 SF1. Representative: **Q8** —
`dominated-noncost`, candm −46.8% (m0144-0007): the PG-family candidate
exists and is cheaper, so the blocker is an ordering claim, not cost or
mechanism breadth. SHAPE-DIFF categories: join-order, join-method,
scan-type, sort-strategy, parallelism + MISSING-NODE (Materialize).

**Evidence lane.** Private clone `tmp/c20a/data-sf025` on `:5595`, binary
`tmp/m0144-0011-bin/goopg` sha256 `ae57cbc7273fb1a4` (HEAD `30e8ea711`),
`GOOPG_PGSHAPED_DP_TRACE=1`, session GUCs `work_mem=64MB`,
`max_parallel_workers_per_gather=4` (the TPC-DS capture convention).
Plan reproduced byte-identical to the committed capture.

## Layer verdicts

### 1. Admission — no gap

The ORDERED upper-rel step runs: `planner.go:1979` calls
`electOrderedGrouping`, falls back to `createOrderedPaths`
(`planner.go:1984`); `upper.ordered.*` producers fire in DPPATH. PG's
route reaches `create_ordered_paths` the same way
(`postgres/src/backend/optimizer/plan/planner.c:5308`).

### 2. Candidate generation — GAP (the first-divergence cause)

DPPATH (Q8, HEAD):

```
DPPATH path producer=upper.groupagg.sort   ... total=19523.12 pathkeys=1 verdict=accepted
DPPATH path producer=upper.groupagg.hashed ... total=19481.50 pathkeys=0 verdict=dominated
DPGROUP loop-decline reason=cands<2(1)
DPPATH candidates producer=upper.ordered.candidates searchedrel=true candidates=10 nonemptykeys=0
DPPATH seed producer=upper.ordered.seed kind=0 keys=0 contained=false ncommon=0 totalcost=19524.47
DPPATH path producer=upper.ordered.sort    ... verdict=accepted   <- the redundant top Sort
```

Two seams, one mechanism family:

- **(a) `inputNodePathkeys` has no `*Aggregate` case**
  (`internal/optimizer/upperorderedinput.go:176`). The seed's ordering
  claim is derived only from `*Sort` tops, `searchedTreePathkeys`, and
  `*Filter`/`*Limit` descent — `default: return nil`. The finished
  `*Aggregate` node IS sorted-emitting (its winning PathAgg carried
  `pathkeys=1` at `upper.groupagg.sort`), but nothing transfers that
  claim to the seed → `keys=0` → `contained=false` → arm-2 stacks the
  Sort. PG equivalent: `create_agg_path` sets `AGG_SORTED` pathkeys =
  subpath keys head-truncated to `num_groupby_pathkeys`
  (`pathnode.c:3412-3416`), which `create_ordered_paths` then reads via
  `pathkeys_count_contained_in` (`planner.c:5344`). The translation
  machinery already exists: `groupingEmissionPathkeys`
  (`upperorderedgrouping.go:78`) computes an agg candidate's emission
  ordering in output coordinates — it is simply not consulted for the
  seed.
- **(b) `electOrderedGrouping` declines single-candidate rels**
  (`upperorderedgrouping.go`: `cands<2(1)`). The GROUP_AGG rel legitimately
  holds one PathAgg — the hashed alternative is dominated by the sorted
  one (fuzzy-equal cost + pathkey superset; PG's `add_path` dominance
  does the same). But PG's `create_ordered_paths` iterates
  `input_rel->pathlist` with no minimum count (`planner.c:5337`) — a
  lone AggPath must still be offered. Either seam's fix flips Q8's top
  shape; (a) is the general one (also covers `*MergeJoin`,
  `*GatherMerge`, `*IncrementalSort` tops — the same `default: nil`
  blind spot the IS-arm `nonemptykeys=0` records point at).

`nonemptykeys=0` on `upper.ordered.candidates` is a *red herring* for
this query: those are the searched-root (join rel {0,1,2,3}) pathlist's
re-earned keys — join-path ordering claims below the aggregate, which
cannot satisfy the ORDER BY anyway. The missing claim is on the seed.

### 3. Cost inputs — not the layer-1 blocker

The bare-Aggregate offer, once generated, wins trivially (seed total
19524.47 vs the stacked Sort's 19524.61 — and the margin census prices
the PG-family shape −46.8%). Cost is only implicated one layer deeper
(below).

### 4. Election — one layer deeper: `{dd⋈ss} ⋈ {store, ca-view}`

rel `{0,1,2,3}`: goopg elected `join.hash total=19455.50`
(verdict=accepted — the plan's `Hash Join 3487.55..19455.50`); the
PG-chosen `join.nestloop total=19852.53` was generated and
`verdict=dominated`. PG elected the NL at 28502 in its own model.
So: NL exists but loses on goopg's inner-side pricing — cost-input or
stats layer, not generation, not tie-break. PG's inner is
`Materialize(NL(store Index Scan ⋈ ca-view-subquery))` (12×200 rows
rescanned per outer); goopg lacks `Materialize`, so the inner economics
are priced on a different shape.

### 5. Executor existence — `Materialize` is a real gap

`MISSING-NODE: PG-only kinds: Materialize` (parity diff). goopg has no
Materialize plan node (only `MaterializedCTEScan`, a different thing).
PG: `create_material_path` (`pathnode.c:1637`). Named capability for
the residue ledger if the NL layer lands without it.

## Children filed

- **M0144-0011a** — ordering-claim propagation at the ORDERED-step
  boundary: `inputNodePathkeys` ordered-emission cases (starting
  `*Aggregate`; survey MergeJoin/GatherMerge/IncrementalSort tops) +
  `electOrderedGrouping` lone-candidate gate review.
- **M0144-0011b** — join election under Q8's aggregate input: reprice/
  trace the NL inner (Materialize+Index-Scan rescan economics) at rel
  {0,1,2,3}; stats check 818 vs 812.
- **M0144-0011c** — `Materialize` node existence (path + executor), or
  measured residue.

Slice end state: open — Q8 not yet matching; campaign continues through
the children. Sibling election-class records (Q17/Q25/Q29, same family)
are the blast-radius check for 0011a.
