# M0146-0049 — a parameterised inner path through a non-scan node

Status: held (S4 escalation 2026-10-03, owner decision needed) — slices (a) recon, (b)+(c) (`f661e6933`), and (d) — d1 (`d2fdb535a`), d2 (`cd1dbec34`), d3 (`6667e4a72`) — landed 2026-10-03; (e) and (f) open but unselectable. Parent: M0146
(banner item 3: owner-named next in the structural line, the blocker of
M0146-0005dp/dq and M0145-0008ac/0008y).
Background: `docs/design/0100-0149/m0146-0005dt-parameterised-append-recon.md`.

## PG mechanism (recap)

- `add_paths_to_append_rel` builds an Append for every child
  parameterisation (`./postgres/src/backend/optimizer/path/allpaths.c:1321`,
  `get_cheapest_parameterized_child_path` `:2048`).
- `create_nestloop_plan` binds `NestLoopParam`s into ANY inner subtree
  (`./postgres/src/backend/optimizer/plan/createplan.c:4341`,
  `replace_nestloop_params` `:5036`).
- `ExecReScanAppend` (`./postgres/src/backend/executor/nodeAppend.c:421`)
  propagates the changed params to each child.

## Slice (a) — recon: the executor substrate (2026-10-03)

The probe used `li(id pk, cat)`, `cs1(item, ord) pk` (100k rows) and
`ws1(item, ord) pk` (50k rows), with `li.cat = 3` keeping 40 rows.

| query | PG 18.3 | goopg |
|---|---|---|
| `li, LATERAL (SELECT amt FROM cs1 WHERE item = li.id UNION ALL SELECT amt FROM ws1 WHERE item = li.id) x` | Nested Loop → Append(Bitmap Heap cs1, Bitmap Heap ws1), `Index Cond: (item = li.id)` | the SAME shape, values identical (3000 rows, same sum) |
| `li, (SELECT item, amt FROM cs1 UNION ALL …) x WHERE x.item = li.id` | the same parameterised Append (Q54 in miniature) | Hash Join over a Parallel Append of seq scans |

So the executor already runs PG's parameterised-Append shape. goopg's
explicit LATERAL plans each member as a one-relation scope whose
correlated restriction becomes an index probe (M0146-0015a). The lateral
join stream then re-opens the right subtree per outer row with the outer
tuple bound.

The lowering contract also exists. Since R25 the NLI arm lowers to
`Join{Algo: NestedLoop, Lateral: true, Right: IndexScan}` with keys as
level-1 `OuterColumnRef`s (createplannl.go). A parameterised Append is the
same contract with an `Append` of such probes on the right.

What is missing is entirely planner-side:

- the appendrel leaf (M0145-0004's partial-path hoist) has no
  parameterised paths;
- there is no Append path kind;
- NL path generation never pairs an outer rel with a parameterised
  non-index inner.

The LATERAL probe also showed an estimate gap: goopg's NL over the
LATERAL Append estimates 1 row where PG estimates 3000.

## Slices (b)+(c) — landed together (2026-10-03)

(b) alone would have filed paths nothing consumes, so the two landed as
one change.

**Producer.** `addParameterizedAppendPaths` (paramappend.go) runs after
the base-rel parameterised producers (pathindexordered.go).

- It applies to a level-1 rel marked `appendrel` whose baseLeaf is a
  plain serial UNION ALL chain (`unionAllMembers`, orderedappend.go's
  predicate).
- Every member must be `[Project →] [Filter →] scan`. Each gets a
  synthetic member rel whose baseLeaf is that scan.
- The leaf's `indexableJoinClausesFor` candidates are re-read onto the
  member column at the same output position. The `restrictInfo` is kept,
  so `probeEnforcedClauses` (now intersecting over an Append's children)
  still recognises the enforced clause and the join drops it.
- `addOneParameterizedIndexPath` and `buildOneParameterizedBitmapPath`
  price the member's probes: index and bitmap, as `create_index_paths`
  builds both.
- The cheapest member path per parameterisation is kept. If any member
  has none, that parameterisation is abandoned, as
  `get_cheapest_parameterized_child_path` returning NULL does.
- The Append is costed as `cost_append`'s unordered arm
  (`./postgres/src/backend/optimizer/path/costsize.c:2255`): the first
  child's startup, the summed totals and rows, plus
  `cpu_tuple_cost × 0.5 × rows`.
- Member probes are never index-only: the statement-wide needed set
  cannot attribute a member's columns.

**Lowering.** `PathParamAppend` gets its own `createPlanNode` arm, which
rebuilds the leaf's UNION ALL around the member probes, each under the
member's own projection. `createNestLoopPlan` dispatches it to
`createNestLoopParamAppendPlan`:

- a `Join{Algo: NestedLoop, Lateral: true}`, R25's NLI contract;
- each probe's keys re-rooted as level-1 `OuterColumnRef`s by
  `outerParamKey`;
- for a bitmap member, the recheck equality is kept on the heap scan's
  `Cond`, because `BitmapQual`'s merged outer++inner coordinates do not
  exist under a lateral join.

**Effect.**
- TPC-DS Q54's `my_customers` subtree becomes Nested Loop(item,
  Append(Bitmap Heap catalog_sales, Bitmap Heap web_sales)): cost 19832 →
  8561, PG 6537.
- `TestParameterisedAppendOverUnionAll` pins the shape and PG's values
  for an inner join, a LEFT JOIN, a member with no index (declines) and a
  three-member chain.

**Gates:** units, spotcheck, SF0.25 sweep 96/96, fire set (only Q54 fires,
at both scales, no timeouts), TPC-H arm, ea-ratchet 10/10. Regress union,
join, subselect, inherit, partition_join, partition_prune, select,
equivclass and with are byte-identical.

**Movement: none.** Q54's categories are unchanged:
- PG drives the probe from a Parallel Seq Scan under a Gather, and goopg's
  param Append is serial-only (0049e);
- PG probes `web_sales` by Index Scan, where goopg's bitmap is cheaper by
  its own cost.

## Slice (d) — through a join: the producer, and two executor prerequisites (2026-10-03)

**The producer is PG's.** `hash_inner_and_outer`
(`./postgres/src/backend/optimizer/path/joinpath.c`) pairs the two rels'
`cheapest_parameterized_paths`. `try_hashjoin_path` admits a result whose
`calc_non_nestloop_required_outer` overlaps the joinrel's
`param_source_rels`, and the path is sized by
`get_parameterized_joinrel_size` (costsize.c) over
`get_joinrel_parampathinfo`'s clause list. A lateral re-plan of the semi
body was the alternative; it would be a second planner route for one
shape.

A reproducer without Q95's CTE:

```
pjo(id, k) 20 rows; pjbig(ord, x) 400k rows, no index;
pjret(ord, item) 30k rows, primary key (ord, item)
SELECT count(*) FROM pjo WHERE pjo.id = 3
  AND pjo.k IN (SELECT pjret.ord FROM pjret JOIN pjbig ON pjbig.ord = pjret.ord)
```

| | plan |
|---|---|
| PG 18.3 | Nested Loop Semi Join(pjo, Hash Join(Seq Scan pjbig, Hash(Index Only Scan pjret_pkey, `Index Cond: (ord = pjo.k)`)), `Join Filter: (pjo.k = pjbig.ord)`) |
| goopg | Hash Right Semi Join over the full pjbig ⋈ pjret hash join |

The `Join Filter` is the EC clause `get_joinrel_parampathinfo` re-derives
for an EC whose join clause was dropped as movable into the inner input.

**Executor prerequisites.** Before d1, two executor behaviours would have
made that plan time out, as M0145-0008ac's Q95 did at SF1:

1. goopg's hash join always read its whole probe side, even over an empty
   hash table. A parameterised build usually finds nothing, and Q95's
   probe side is a 1.7M-row CTE scan.
2. `lateralJoinStream` starts each outer row with an empty CTE cache
   (`m.innerCTE = nil`), so a statement-level CTE scanned under the
   parameterised inner is re-computed per outer row. That is slice d2.

### Slice (d1) — ExecHashJoin's empty-inner exit and outer prefetch (landed)

`openLazyHashJoin` (operators_join_agg.go) now follows
`nodeHashjoin.c`'s HJ_BUILD_HASHTABLE state:

- **Outer prefetch** (`wantOuterPrefetch`). It never runs for a join that
  fills the build side. It always runs for one that fills the probe side
  (HJ_FILL_OUTER). Otherwise it runs when the probe child's startup cost
  is below the build child's total (the Hash node's cost is its child's)
  and the previous scan did not find the outer non-empty. The cost answer
  is cached per operator.
  - The first probe tuple is fetched before the build. An empty outer ends
    the join without building.
  - `outerNotEmpty` is `hj_OuterNotEmpty`. It survives a re-Open, is
    cleared after the build, and is set by `pullProbe` on every probe
    tuple, the prefetched one included.
- **Empty-inner exit** (`emptyBuildEndsJoin`). If the build loops drained
  no row and the join does not emit unmatched probe rows
  (`probeFillsUnmatched`), the probe is never opened. Next finds no
  stream and returns EOF.
  - `buildRows` is counted by the shared build loops, so the cooperative
    parallel build counts too.
  - The CTID-preserving build and the shared and Parallel Hash builds
    leave the count incomplete and keep their old order, as PG's parallel
    arm skips the prefetch.

`TestHashJoinEmptyBuildSkipsProbe` pins the open counts and rows for
inner, semi, right, left and anti joins, over empty builds and empty
outers.

**Effect.**
- EXPLAIN ANALYZE matches PG on the probe side: `Seq Scan … (actual
  rows=1 loops=1)` where goopg read the whole outer.
- Values and plans are unchanged: SF0.25 sweep 96/96 with all 99 plan
  shapes identical, TPC-H arm 24/24, and regress join, join_hash,
  subselect, select, union, with, partition_join, select_parallel and
  aggregates are byte-identical.
- ea-ratchet passes, reporting three findings fixed (Q23 ×2, Q92). This
  is an instrument artefact: those subtrees sit under hash joins whose
  outer is now found empty, so they no longer execute and are not
  scored. The baseline was not re-pinned.

**Ledgered.**
- goopg prints a never-run node as `actual rows=0.00 loops=0`; PG prints
  `(never executed)` (explain.c).
- The cooperative build does not record its build scan's instrumentation
  (`Hash … loops=0`).
- PG re-uses a single-batch hash table on a rescan with no changed inner
  parameter (`ExecReScanHashJoin`). goopg rebuilds on every re-Open.

### Slice (d2) — an uncorrelated CTE under a LATERAL is materialised once (landed)

`lateralJoinStream` swaps `ctx.CTERowCache` for every outer tuple
(`bindOuter` / `unbindOuter`), so that a CTE body reading the outer row is
re-materialised. It applied that swap to every CTE, so a statement-level
CTE scanned on the lateral's right side was recomputed once per outer row.

**The PG rule is correlation, not declaration site.** `ExecReScanCteScan`
(`./postgres/src/backend/executor/nodeCtescan.c`) clears the shared
tuplestore only when the CTE plan has changed parameters, and otherwise
rewinds it. Two consequences follow:

- an uncorrelated CTE declared inside a LATERAL subquery also runs once;
- a CTE body may reference an enclosing query level (PG accepts
  `EXISTS (WITH c AS (SELECT s.x) …)`), so "declared above the lateral"
  is not the same as "uncorrelated".

**Mechanism.**
- `cteScanOp` classifies its body once, when the operator is built:
  `optimizer.PlanHasOuterRef` (the binder-aware `planHasOuterRef`, so a
  probe key bound by a lateral join inside the body does not count).
- An uncorrelated body is cached in the new `ctx.CTEStableCache`, which
  no lateral swap touches. It is reset with `CTERowCache` at statement
  start, and parallel workers start it empty.
- A correlated body keeps the swapped per-outer-row `CTERowCache`.

**Effect.**
- A wrong result is fixed: `WITH c AS MATERIALIZED (SELECT random() r)
  … LATERAL (SELECT r FROM c …)` gave 5 distinct values over 5 outer rows,
  where PG gives 1.
- Q95's parameterised inner (d3) reads its 1.7M-row CTE once.
- `TestCTEUnderLateralMaterialisesOnce`.

**Side finding (S2, filed M0146-0050).** A correlated CTE inside a
correlated scalar subplan replays its first execution: goopg answers
`2,2,2` where PG answers `2,4,6`. It predates d2 (a HEAD build gives the
same answer). Subplan re-execution never resets `CTERowCache`.

### Slice (d3) — the parameterised hash join and its nested loop (landed, `6667e4a72`)

**Producer** (`addParameterizedHashJoinPaths`, paramjoin.go). This is
`hash_inner_and_outer`'s parameterised pairing. It runs after the serial
hash arm and handles INNER joins with no unique-ified side.

- It pairs `[CheapestTotal] ++ CheapestParameterized` of the two rels,
  skipping the all-unparameterised pair and any input parameterised by
  the other side.
- Every parameterised input must be a plain index probe or a bitmap heap
  scan over one bitmap index scan, with equality clauses.
- A pair is admitted when `calc_non_nestloop_required_outer` overlaps
  the joinrel's `param_source_rels` (`try_hashjoin_path`).
- Cost is `hashJoinCost` over the input paths. Rows come from
  `parameterizedJoinrelSize`: `calcJoinrelSize` over the paths' rows, plus
  the clauses joining a required-outer rel to the joinrel that no probe
  applies, capped by the joinrel's rows
  (`get_parameterized_joinrel_size`).
- `probeEnforcedClauses` reports a parameterised hash join as enforcing
  what its probes enforce, so the nested loop drops those clauses from its
  residual.

**Lowering** (`createNestLoopParamJoinPlan`). It emits R25's
`Join{Algo: NestedLoop, Lateral: true}` over the hash join. The probes
sit below the hash join, and the outer's layout is only final after
`joinInputsFor` narrows it. So the loop hangs a sink on every
parameterised path of the inner (`Path.paramSink`, PG's create_plan-time
`curOuterRels`). `createPlanNode`'s index and bitmap arms record the probe
nodes there, and `createHashJoinPlan` accepts a parameterised path only
under such a sink. After the build, `bindParamProbe` rebinds each probe's
keys as level-1 `OuterColumnRef`s. For a bitmap probe the recheck goes on
the heap scan's `Cond`. `bindParamProbe` is 0049c's per-member binding,
extracted and shared.

**Effect.** The reproducer plans PG's shape and cost (7279.58 against
PG's 7279.63), and EXPLAIN ANALYZE over 7 outer rows matches PG node for
node. The probe side is read 5 times, because d1's empty-inner exit skips
the other two. `TestParameterisedHashJoinInner` covers the semi, anti and
inner forms. No TPC-DS plan moves by default, and the fire set saw no
changed query at either scale. Q95's semi RHS reaches the search only
through M0145-0008ac's CTE-leaf pull-up, which this unblocks.

**Not reproduced (ledgered).**
- PG's EC-regenerated `Join Filter: (pjo.k = pjbig.ord)`
  (`get_joinrel_parampathinfo`'s dropped-EC arm). goopg's hash join
  estimates rows=4 where PG estimates 1.
- Memoize over a join inner.
- Parameterised merge and nested-loop join results.
- Non-INNER parameterised joins.
- A parameterised index-only probe costs about twice PG's (16.27 against
  8.30), so goopg picks a bitmap probe where PG keeps the index-only scan.
  This is not filed as a task: M0146-0049's S4 lineage budget is spent, so
  it is named in the task's escalation block and ledgered.

## Remaining slices

- **(b)** Per-member parameterised index paths for a flattened UNION ALL
  leaf whose members are single-table scans. The join clause
  `leafcol = outer` is translated through the member's output column to
  `membercol = outer`. Built with `addOneParameterizedIndexPath`'s pricing
  (pathparamindex.go), as `get_cheapest_parameterized_child_path`.
- **(c)** A parameterised Append path for the leaf: cost and rows are
  the sum of its children (`create_append_path` with `required_outer`).
  NL path generation also needs to accept it as an inner, and it lowers
  to `Join{Lateral}` over `Append{param probes}`.
- **(d)** The through-a-join case: Q95's parameterised Hash Join on the
  semi inner (M0146-0049d). d1, d2 and d3 have all landed (above). Q95
  moves with M0145-0008ac's re-applied pull-up.
- **(e)** Parallel: a partial outer driving the parameterised Append
  (PG's Q54 Gather). `PathParamAppend` is not parallel-safe yet
  (M0146-0049e).
- **(f)** The IN/semi form: `li.id IN (SELECT item FROM cs1 UNION ALL …)`
  probes in PG, not in goopg (M0146-0049f).
