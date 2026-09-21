# Upper-rel pathlists (M0145-0006)

Status: slice 1 (the order-delivering tops `inputNodePathkeys` swallowed)
landed; slices 2-4 ledgered below. Task: `.ralph/fix_plan.md` M0145-0006.
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

## Remaining slices (ledgered)

| slice | scope | blocker |
|---|---|---|
| 2 | the `*Join{Algo: JoinAlgoMerge}` top | Coordinate boundary. A merge join's result ordering is stated by its merge clauses, but the node's `HashKeys` pairs are expressed per side and the node republishes a JOINED layout (`in.publishedSchema(jt)`), so the claim needs a translation the walk deliberately does not do. The `*Path` twin already carries `p.Pathkeys` (validated ascending/nulls-last by `createMergeJoinPlan`), so the resume point is to stamp that validated claim onto the node at build time — a `searchedTree`-style carry — rather than re-deriving it. |
| 3 | `electOrderedGrouping`'s `node != agg.node` precondition | The elected node and the recorded grouping surface diverge whenever a stage wraps the aggregate (the `Project{Aggregate}` rename is the measured case). Needs the surface to name the rel rather than the node pointer. |
| 4 | `electOrderedDistinct`'s `cands<2` gate | With one `PathDistinct` candidate the wrapper has nothing to elect between; the real fix is upstream — `createDistinctPaths` must offer both the hashed and the sorted candidate — which is a grouping-paths slice, not an ordered-paths one. |

Each has a `.ralph/deferral_ledger.md` row with the same resume points.
