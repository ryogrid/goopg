# R20 — the wiring gap is one guard, and it unifies with K12

*Round 20 of `../TODO.md`. Findings round. Locates K23's cause exactly,
and finds it is the same root cause as K12's slice (B).*

## 1. The exact line

`addPartialAggSplitPath` (`partialaggupper.go:92`):

```go
// Gather (C-19d's coexistence rule, here as a generation refusal rather
// than a post-pass stand-down), and must have a driving scan — without one
// every worker reads the whole relation and the Gather returns N+1 copies
// of every row.
if subtreeHasUnsafeNode(child) || subtreeHasGather(child) || drivingScan(child) == nil {
    return nil
}
```

**`subtreeHasGather(child)` → refuse.** Under the flip,
`generateUsefulGatherPaths` puts a Gather at the join level, so the
aggregate's child subtree contains one, and the partial-aggregation
producer declines outright. That is the whole of K23 and of
`aggregation-strategy` 10 → 14.

It is a **deliberate coexistence guard**, not an oversight: two Gathers
in one subtree would have every worker read the whole relation and
return N+1 copies of every row. Under the post-pass mechanism the guard
is exactly right.

## 2. Why the guard cannot simply be relaxed

PG does not face this choice. It builds `partially_grouped_rel` from
`input_rel->partial_pathlist` (`planner.c:7351`), i.e. it aggregates
**on the partial path, below the Gather**, then gathers, then finalises
(`gather_grouping_paths`, `planner.c:7704`). There is never a Gather
below the partial aggregate, because the partial aggregate is built
*before* anything is gathered.

goopg's flip inverts that order: the Gather is already materialised as a
finished `Node` by the time the grouping stage runs, so the only place
left for an aggregate is above it — where a *partial* aggregate would be
meaningless. Deleting the guard would not produce PG's plan; it would
produce a double-Gather.

## 3. The unification: K23 and K12 are the same root cause

`partialaggupper.go`'s own header names blocker 1 as:

> *"No input rel — upstream seeds `partially_grouped_rel` from
> `input_rel->partial_pathlist`, and the rel carrying `PartialPathlist`
> **dies inside `planJoinlistSearch` before the aggregate stage runs**."*

That is the identical seam K12's slice (B) is blocked on
(`windowsetoppaths.go:19`: *"above the search seam the inputs are
finished Nodes with no pathkeys"*).

**One root cause, two symptoms:**

| symptom | needs from the join rel |
|---|---|
| K23 — no partial aggregation over the flip's Gather | its `PartialPathlist` |
| K12(B) — sorted aggregate never wins under a WindowAgg | its `Pathkeys` |

Both fail because the upper planner receives a **finished `Node`**
instead of the join rel's paths. Whatever is done for one is most of
what is needed for the other, and they should not be scheduled as
independent rounds — which is how the TODO had them (R20 and R21).

## 4. Consequence for the flip

R19 recommended fixing the wiring before landing the flip, so it would
be strictly a move toward PG. That recommendation now has a price
attached: the fix is **the upper-planner seam**, not a local change.

So the honest options are narrower than R19 implied:

1. **Land the flip with the regression named** (R10 `DESIGN.md` §7
   permits it now that it is explained): four queries lose the
   `Partial`/`Finalize` split while gaining PG's parallel-join
   mechanism, until the seam work lands.
2. **Do the seam work first** — the largest single item in this
   workstream, unblocking K12(B) and K23 together, after which the flip
   is strictly an improvement.

Option 2 is right on the merits and is a substantial piece of work.
Option 1 is defensible only because it is reversible and the loss is
documented; it is not recommended, because knowingly shipping a
four-query parity loss to land a mechanism change inverts this goal's
priority.

## 5. Filed — TODO restructured

- **R21 (was two rounds) — the upper-planner seam**: give the grouping
  and window stages the join rel's paths (`PartialPathlist`,
  `Pathkeys`) instead of a finished `Node`. Unblocks K23 and K12(B)
  together. Oracle: `create_partial_grouping_paths` +
  `gather_grouping_paths` (`planner.c:7351`, `:7704`) and
  `create_grouping_paths`' pathkey handling.
- The flip stays reverted until then, on the reasoning in §4.
