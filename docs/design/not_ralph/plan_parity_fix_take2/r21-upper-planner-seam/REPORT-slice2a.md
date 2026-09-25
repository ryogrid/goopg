# R21 slice 2a — the rel reaches the upper stages, without touching planner.go

*Round 21, slice 2a. Landed 2026-09-08/09.*

## 1. Verdict

Landed; gate passes.

| gate | result |
|---|---|
| TPC-H plans vs slice 1 | **byte-identical** |
| TPC-DS plans vs slice 1 | **byte-identical** |
| optimizer + executor suites | green |

## 2. The plan changed — for the better

`DESIGN.md` §8 mapped slice 2a as threading the rel through five hops
including `planSelectWithSettings`, and warned the route crosses
`planner.go`. **It does not have to.** The codebase had already solved
this exact problem and said so:

> *"Carried on the tag rather than threaded through `planJoinlistSearch`'s
> return because the seam publishes a Node: this is the one field already
> attached to exactly the node kinds a search root can be, and the
> alternative — a second return value through every caller between
> `searchOneProblem` and `createOrderedPaths` — would touch fifteen
> signatures to carry a list that only one consumer reads."*
> — `searchedTree.searchPathkeys`, `searchedtree.go:103`

So `searchedTree` gains `searchRel *RelOptInfo`, stamped in
`stampSearchPathkeys` — the one place where the published root and the
path that produced it are both in scope, and which already had `p` in
hand. **Zero signatures changed outside `searchedtree.go` and
`createplanroot.go`.**

The stamp is deliberately *not* conditional on `len(p.Pathkeys) == 0`,
unlike the pathkeys beside it: a rel with no useful ordering still
carries the `PartialPathlist` that partial aggregation needs (K23).

## 3. What the reflective walk taught us

Adding the pointer broke `TestPlanQ85IsDeterministic`, and the breakage
was informative rather than incidental. `planFingerprint` walks plans
**by reflection on purpose**, so it can notice a reordering anywhere.
A `*RelOptInfo` is a gateway to the entire path graph, so the walk now
descended into every `Path` the search ever built — and panicked on
`reflect.Value.Interface` for values read out of unexported fields.

Two fixes, both matching precedent already in that file:

1. `*RelOptInfo` is a **leaf**, for the same stated reason
   `*catalog.Table` already is — *"shared … state, not plan structure,
   and recursing into it would walk the whole schema on every scan"*.
   It prints its presence, so the fingerprint still notices the rel
   appearing or vanishing, which is plan-relevant.
2. Guard `v.Interface()` with `CanInterface()`. `searchRel` is the first
   unexported **pointer** the walk reaches — `searchPathkeys` is a slice
   and never took that branch — so the latent panic had simply never
   been triggered.

This is worth recording as a property of the design rather than a test
fix: **anything that walks a plan generically now has a path into the
search graph**, and every such walker needs the leaf rule. Copying,
serialisation and any future deep-equal will hit it too.

## 4. Next

- **Slice 2b (K23)**: read `searchedRel().PartialPathlist` in
  `addPartialAggSplitPath` and build the partial aggregate below the
  Gather, as `create_partial_grouping_paths` does (`planner.c:7351`).
  Success test: `aggregation-strategy` 10 → 14 under the flip
  disappears.
- **Slice 3 (K12 B)**: the ordering contest, from the same rel.
