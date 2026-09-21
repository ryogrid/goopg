# Upper-rel pathlists (M0145-0006)

Status: slices 1 (the order-delivering tops `inputNodePathkeys` swallowed),
2 (the merge-join top) and 3-partial (the election sees through a rename)
4 (the DISTINCT election's candidate minimum) and 5 (the HAVING filter,
priced) landed. The task's own residual gates are closed; what remains is
ledgered below. Task: `.ralph/fix_plan.md` M0145-0006.
Parent: M0145-0005. Kind: impl.

## What the task is

PG elects ordering and grouping over CANDIDATE SETS: `create_grouping_paths`
and `create_ordered_paths` read `input_rel->pathlist`, price every member, and
`add_path` the winners into the upper rel
(`postgres/src/backend/optimizer/plan/planner.c:5308` for the ordered case).
goopg's upper rels exist (`upperrel.go`, `upperordered.go`,
`upperorderedgrouping.go`, `upperordereddistinct.go`) but several elections
still run as stage-builder producers: one node is built, and the lattice is
consulted only to reprice it. M0145-0006 closes that gap.

D4 left three residual gates behind, all of the same shape — a place where the
lattice has to DECLINE because the information it needs never reaches it:

1. `inputNodePathkeys`' `default: nil` top (`upperorderedinput.go`). The seam
   publishes a finished Node, so the ordering claim is derived by a walk; every
   node kind the walk does not know is a total loss of the claim.
2. `electOrderedGrouping`'s `gate-precondition` decline when
   `node != agg.node` (`upperorderedgrouping.go:219`) — the elected node is not
   the one the grouping surface recorded, so the function cannot prove which
   rel it is electing over.
3. `electOrderedDistinct`'s `cands<2` gate (`upperordereddistinct.go:140`) —
   with one candidate there is nothing to elect BETWEEN, so the wrapper
   declines rather than repricing a single path.

## Slice 1 — the order-delivering tops (landed)

`inputNodePathkeys` knew four node kinds: `*Sort`, `*Aggregate`, a searched
subtree root, and the two order-preserving wrappers `*Filter`/`*Limit`. Three
node kinds that DO deliver an ordering in PG fell into `default: nil`, so the
ORDERED step re-seeded with `keys=0` and `addOrderedPaths` could only take its
`create_sort_path` arm — a redundant Sort over an already-ordered input, which
is exactly the divergence class M0144-0011 measured for the `*Aggregate` arm.

| node | PG statement | what the arm claims |
|---|---|---|
| `*IncrementalSort` | `create_incremental_sort_path`, pathnode.c:3191 — `pathkeys` is the FULL list | `pathkeysForSortKeys(t.Keys)`; `PresortedCount` bounds the WORK, never the order emitted |
| `*GatherMerge` | `create_gather_merge_path`, pathnode.c:2128 — keeps its pathkeys where `create_gather_path` publishes nil | `pathkeysForSortKeys(t.Keys)`; `NewGatherMerge` publishes `child.Output()`, so the claim is already in the walk's coordinates |
| `*WindowAgg` | `create_windowagg_path`, pathnode.c:3740-3741 — "WindowAgg preserves the input sort order", `pathkeys = subpath->pathkeys` | descends to the CHILD's claim, and only when `Presorted` |

### The two rules the arms had to respect

- **Fail closed on the private sort.** goopg's `*WindowAgg` is not PG's: with
  `Presorted` false the executor sorts its own input by
  `PartitionBy ++ OrderBy` (the field note in `plan.go`), so the rows it emits
  carry THAT order and the child's claim is void. The node records no direction
  or NULL placement for that private sort, so the arm refuses instead of
  reconstructing one. `Presorted` true is the PG shape — the plan stacked the
  Sort — and there the child's claim is copied verbatim, as upstream does.
- **Narrow the coordinate space, never translate it.** The window functions are
  APPENDED to the child's schema (the builder in `planner.go` starts
  `outputSchema` as a copy of the input schema and appends one column per
  func), so the child's columns keep their positions and a positional claim
  from below already addresses the right column of the published output. The
  walk therefore carries a `limit` — how many LEADING columns of `out` the
  current space must agree with — and narrows it when it crosses a `*WindowAgg`,
  after re-checking `schemaCoordinatesAgree(out[:len(child)], child)` rather
  than trusting the builder. Narrowing claims LESS, never something else, which
  is the same soundness argument truncation already rests on (rule 1 in
  `upperorderedinput.go`). A node that re-assigns positions — the window column
  PREPENDED, say — fails that check and the walk returns nil.

This keeps the file's standing contract intact: a wrong ordering claim would
still need a node to lie about its own output schema.

### Pins

`upperorderedinput_m0145_test.go`: the full-ordering claim of an
`IncrementalSort` with `PresortedCount=1`; the `GatherMerge` claim next to its
`*Gather` twin, which must keep claiming nothing; the presorted `*WindowAgg`
crossing; the fail-closed self-sorting `*WindowAgg`; a `*WindowAgg` that
prepends rather than appends; and narrowing composed with the new arms
(a `GatherMerge` claim delivered THROUGH a window node, and a `*Gather` below
one still yielding nothing).

### Evidence

Retirement-shaped, not movement-shaped: the arms can only REMOVE a redundant
Sort, never change a row. TPC-DS SF0.25 sweep and the TPC-H acceptance arm are
the value evidence; the sweep's plan channel is where a Sort removal shows up.

Measured: SF0.25 `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`, plan
shapes `same=98 changed=1` — and the one changed shape is the witness. **Q51**
lost the redundant top Sort:

```
  Limit
-   ->  Sort  (Sort Key: item_sk, d_date)
-         ->  WindowAgg  (Filter: web_cumulative > store_cumulative)
+   ->  WindowAgg  (Filter: web_cumulative > store_cumulative)
          ->  Sort  (Sort Key: (CASE WHEN item_sk IS NOT NULL ...), (CASE WHEN d_date ...))
                ->  Merge Full Join
```

The claim that removed it is the child Sort's own key list, carried up through
the presorted `*WindowAgg` — the ORDER BY resolves to those same two `CASE`
expressions, so `pathkeysContainedIn` certifies the ordering is already
delivered. Cost fell `419.08..422.64` to `277.76..381.02`. This is a PARITY
move, not merely a saving: PG 18.3 on the same dataset (`:65438`, db
`tpcds025`) plans `Limit -> Subquery Scan -> WindowAgg -> Sort -> Merge Full
Join`, with no second Sort either.

## Slice 2 — the merge-join top (landed)

### What was actually missing

The ledgered plan was to stamp the winning path's `Pathkeys` onto the node at
build time. Recon showed that already happens for every merge join the
PG-shaped search produces: `createPlanAtSearchRootRange` calls
`stampSearchPathkeys`, which validates `p.Pathkeys` against the published root
schema and records them on the `searchedTree` embed — and `inputNodePathkeys`
reads that stamp BEFORE the type switch. `TestOrderedInputArmFiresEndToEndAndRemovesTheSort`
has been pinning exactly that since C-07.

What has no stamp is the LEGACY constructor's merge join. `chooseInnerJoinAlgo`
(joincost.go:33) elects `JoinAlgoMerge` on cost, and `chooseOuterFillJoinAlgo`
leaves RIGHT/FULL on merge by default, so every statement the seam declines on
— the census's 104 `leaf-count` fires are the bulk — builds its joins outside
the search and reached the walk's `default: nil`. That is the gap this slice
closes, with a node-side derivation rather than a carry.

### The four things that make a node-side claim sound

1. **The direction is fixed, not inferred.** goopg's merge comparator is
   ascending with NULL-keyed rows last, unconditionally
   (`mergeSortedSource.less`, `internal/executor/join_merge_stream.go:280`), and
   the operator sorts BOTH inputs itself. `createMergeJoinPlan` already refuses
   to build a node whose path claimed any other direction.
2. **The join-type rule is upstream's.** The arm routes through
   `buildJoinPathkeys` (PG `pathkeys.c:1295`) rather than restating it: FULL and
   RIGHT claim NIL, because their unmatched inner rows are injected wherever the
   merge reaches them instead of where the outer ordering would put them.
3. **The key list is the EXECUTOR's, not `HashKeys`.** `ExecMergeKeyPlan` drops
   a pair that is not `pairIsHashSafe` into the residual, and keeps only the
   lead pair for a NULL-aware join, so the sides are sorted on a possibly
   shorter tuple. Claiming `HashKeys` would assert an ordering on a column the
   sort never keyed. The claim additionally stops at the first non-merge-safe
   pair — including refusing outright when the LEAD key is unsafe, since
   `Keys[0]` is kept unconditionally and an exotic-typed lead key is ordered by
   `compareDatum` while the SQL `=` it stands for may disagree.
4. **Validate, never translate.** The keys are checked against the node's
   published schema with `validatedSearchPathkeys` — the same validator the
   search boundary uses — so a coordinate convention this function guessed wrong
   degrades to "less ordering claimed", never to a wrong claim.

A searched join with an EMPTY stamp returns nil rather than re-deriving: the
search was given the chance to claim and declined (nil `build_join_pathkeys`,
or a claim that failed validation against this very schema), and a weaker
mechanism must not overrule it.

### Pins

`upperorderedinput_m0145_test.go`: the legacy merge join's delivered ordering
(ascending / NULLs-last); hash and nested-loop refusals; FULL and RIGHT
refusals (the wrong-answer guard, node side); deference to an empty search
stamp; validator truncation, leading and trailing; and the unsafe-lead-key
refusal.

### Evidence

Plan-IDENTICAL on both corpora: TPC-DS SF0.25 `PASS=96 MISMATCH=0 ERROR=0`
with shapes `same=99 changed=0`, TPC-H acceptance arm 24/24 MATCH. The arm
therefore has NO corpus witness today — every merge join those 121 statements
reach the ORDERED seam with is one the search produced and stamped, so the
stamp answers first. The gap it closes is real (the legacy constructor elects
merge on cost, and the census records 104 seam declines that take that route)
but it is not exercised by a query in either benchmark, which is worth stating
plainly rather than implying a measurement that did not happen. The unit pins
are the guarantee here; the corpus is only the no-regression channel.

## Slice 3 (partial) — what the election's pointer gate actually costs

`electOrderedGrouping` declined `gate-precondition` whenever the ordered-seam
input was not the aggregate node itself. The ledgered plan was to make the
grouping surface name the REL instead of the node pointer. Measuring first
changed the shape of the answer.

### What the probe found

A trace probe (`dpTrace`, three statement shapes through `PlanWithSettings`)
measured which wrapper production actually puts between the aggregate and the
ORDER BY stage:

| statement | seam input | outcome |
|---|---|---|
| `select c as p, count(*) … group by … order by p` | IS `agg.node` (the rename Project is added ABOVE the ORDER BY stage) | already elected before this slice |
| `select p, c from (select … group by …) t order by p` | no grouping surface in this scope at all | correctly declined — nothing to elect |
| `… group by … HAVING … order by …` | `Filter{Aggregate}` | **still declines** |

So the identity-`Project` case this slice admits is sound but has NO witness in
the probed shapes, and the reachable decliner is the HAVING filter — which the
function's own doc comment named all along.

### What landed

The see-through admission plus the splice it requires. `identityProjectChainTo`
shares `projectIsPositionalIdentity` with `inputNodePathkeys`' walk, so the
election and the walk cannot disagree about which projections are transparent.
The splice is the part that needed pinning: the elected spec is copied back
onto `agg.node` in place, so the chain already carries it, but the winner was
BUILT over the bare aggregate — returning that node would drop the projection
and publish the aggregate's labels instead of the statement's. A sort winner is
therefore re-parented over the chain, and a bare-aggregate winner returns the
chain itself.

### Why the HAVING filter could not be admitted with it (closed by slice 5)

Admitting it needs the filter PRICED, not merely stepped over.
`addOrderedPaths` prices its `create_sort_path` arm from the input path's
`Rows`/`Cost`, which for a `PathAgg` candidate are the aggregate's — pre-HAVING.
The normal `createOrderedPaths` call this would replace prices from the
finished `Filter` node, i.e. post-HAVING rows. Admitting the filter without
modelling it would therefore swap an accurate cost for an optimistic one on
every `GROUP BY … HAVING … ORDER BY` statement, which is the opposite of what
the ORDERED rel exists for. PG has no such gap: its HAVING quals live ON the
`AggPath` (`create_agg_path`'s `qual` argument), so every pathlist entry
already carries post-HAVING rows. Ledgered with that as the resume point.

## Slice 4 — the DISTINCT election's candidate minimum (landed)

### The ledgered reading was wrong again, and the probe said so

`electOrderedDistinct` declined at `cands<2`, and the ledger read that as a
candidate-SUPPLY gap: "`createDistinctPaths` must offer both the hashed and the
sorted candidate". It already does — `addDistinctPaths` files the hashed
candidate and the unique-over-sorted one unconditionally. What removes the
second one is `add_path` DOMINANCE, downstream of supply. The gate was
therefore refusing the election on statements where one candidate simply lost,
which is the ordinary case rather than a corner one.

PG has no minimum at all: `create_ordered_paths` iterates the whole input
pathlist, `foreach(lc, input_rel->pathlist)`
(`postgres/src/backend/optimizer/plan/planner.c:5337`). This is the same
divergence M0144-0011a-2 removed from the grouping twin, and the two are
sibling paths — a gate one of them dropped must not survive in the other
(`pattern_sibling_paths_must_agree`).

### The witness

A `dpTrace` A/B on `select distinct c from t order by c` with
`enable_hashagg = off` (so only the unique-over-sorted candidate survives
dominance):

| gate | trace | plan top |
|---|---|---|
| `cands < 2` | `loop-decline reason=cands<…(1)` | `*Sort` over the DistinctOn |
| `cands < 1` | `elected shape=bare-*DistinctOn` | `*DistinctOn` |

The unique candidate's producer Sort orders every output column ascending, so
the ORDER BY key is a prefix of what it already delivers and the stacked Sort
was redundant. Both corpora stay plan-identical (`same=99 changed=0`,
acceptance 24/24) because neither benchmark runs a lone-candidate DISTINCT with
an ORDER BY — the witness is the probe, and it is a real statement shape, not a
constructed one.

## Slice 5 — the HAVING filter, priced (landed)

Slice 3 left the reachable case refused because admitting it without modelling
the qual would have replaced an accurate cost with an optimistic one. Slice 5
models it.

`seeThroughChainTo` (which replaces slice 3's `identityProjectChainTo`) now
also crosses `*Filter`s and RETURNS the quals it crossed. The filter itself is
ordering-transparent — it publishes its child's schema and removes rows without
reordering them, so a positional claim from below survives — but its predicate
is not free, and `applyHavingQualsToOffer` prices it onto every candidate as
PG's `cost_agg` does in its `if (quals)` arm:

```
total_cost   += qual_cost.startup + output_tuples * qual_cost.per_tuple;
output_tuples = clamp_row_est(output_tuples * clauselist_selectivity(...));
```

goopg's `qualEvalCost` supplies the first line and the per-clause
`clauseSelectivity` product the second. The ORDERED rel is now also sized from
`node` (post-qual) rather than `agg.node`; the two are the same object whenever
nothing wraps the aggregate, so the unwrapped path is unchanged.

### Measured

An A/B on `GROUP BY … HAVING … ORDER BY` through `PlanWithSettings` shows the
route change with no shape change on that statement: `gate-precondition`
decline before, `elected shape=Sort-over-Aggregate` after, same
`Project -> Sort -> Filter -> Aggregate` plan. That is the grouping twin's
M0144-0011a-2 result exactly — shape-inert, cost-different, because the Sort is
now priced by `cost_sort` over a properly qual-priced candidate instead of by
the fallback's `DeriveLegacyDisplayCost`.

On the corpus it is not inert. TPC-DS SF0.25 moves two shapes, both HAVING
statements, `PASS=96 MISMATCH=0`:

- **Q24** — the real move, and a PARITY move. Before: `Sort` over
  `HashAggregate` carrying `Filter: (sum(netpaid) > (InitPlan 1).col1)`. After:
  `GroupAggregate` with that same Filter over a `Sort` of its input, and NO top
  Sort — the sorted-aggregate candidate was elected because its emission order
  already delivers the ORDER BY. PG 18.3 on the same dataset plans
  `GroupAggregate … Filter: (sum(ssales.netpaid) > (InitPlan 2).col1)` with no
  top Sort either.
- **Q6** — cost-only (`35244.72..35244.74` to `35244.96..35244.98`): the
  repricing, no shape change.

TPC-H acceptance arm 24/24 MATCH, spotcheck Q12=2/Q13=33.

## Remaining slices (ledgered)

| 3 | `electOrderedGrouping`'s `node != agg.node` precondition | The elected node and the recorded grouping surface diverge whenever a stage wraps the aggregate (the `Project{Aggregate}` rename is the measured case). Needs the surface to name the rel rather than the node pointer. |

Each has a `.ralph/deferral_ledger.md` row with the same resume points.
