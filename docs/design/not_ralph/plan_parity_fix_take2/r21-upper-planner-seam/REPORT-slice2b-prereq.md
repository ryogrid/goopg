# R21 slice 2b prerequisite — the partial path IS reachable, measured

*Round 21, slice 2b groundwork. Landed 2026-09-09. The behaviour change
itself is not in this commit; its prerequisite is now a measurement
rather than an assumption.*

## 1. What landed

`searchedRelOf(n Node) *RelOptInfo` — the accessor the upper stages use
to reach the search's own rel from slice 2a's tag. Gated: TPC-H plans
byte-identical, suites green. No consumer yet.

## 2. The assumption that was wrong, caught by probing

The obvious implementation reads the tag off the node it is handed:

```go
s, ok := n.(searchRootNode)   // ← wrong
```

Probed under `GOOPG_GATHER_PATHS=all`, that returns **nil**:

```
R21PROBE child=*optimizer.Project rel=false partialPaths=-1 hasGather=true
```

The aggregate's child is a `*Project` **wrapping** the search root, not
the root itself. Had this been assumed rather than measured, slice 2b
would have been built on an accessor that always returns nil — and the
symptom would have been "partial aggregation still does not fire",
indistinguishable from a costing problem, sending the next round
hunting in `cost_agg`.

This is the discipline `planner_verify_both_candidates_generated`
prescribes and that this workstream has needed ten times: **confirm the
input arrives before building on it.**

## 3. The fix, and why it reuses an existing contract

`searchedRelOf` descends via `boundaryWalkChildren`, whose documented
contract is *"every kind that can sit between a statement's root and a
spliced searched subtree"* — which is precisely this question. It stops
at the first searched root, returns nil at a join/set-op/unknown kind,
and is depth-bounded.

Note this is the same function R11 had to teach about `Gather`, and the
same contract paid off twice.

## 4. The prerequisite, now measured

Re-probed after the fix, on TPC-H under the flip:

```
R21PROBE child=*optimizer.Project rel=true partialPaths=1 hasGather=true
```

Three facts, all now evidence rather than inference:

1. the search's rel **is** reachable from `addPartialAggSplitPath`;
2. its `PartialPathlist` is **non-empty** — the partial path PG's
   `create_partial_grouping_paths` seeds `partially_grouped_rel` from
   (`planner.c:7351`) exists;
3. `hasGather=true` — so the existing guard is exactly what stands
   between that partial path and PG's `Partial`/`Finalize` shape.

K23 is therefore not blocked on anything unknown. The refusal site now
carries this measurement in a comment, so slice 2b resumes from a fact.

## 5. Slice 2b, remaining

Add an arm **before** the guard: when `searchedRelOf(child)` yields a
rel with a non-empty `PartialPathlist`, build partial aggregate → Gather
→ finalise from that partial path, rather than refusing. The guard stays
for the post-pass route, which genuinely needs it (two Gathers = every
worker reads the whole relation, N+1 copies back).

Success test, unchanged: `aggregation-strategy` 10 → 14 under the flip
disappears.
