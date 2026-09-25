# R21 — give the upper planner the join rel's paths, not a finished Node

*Round 21 of `../TODO.md`. Implements K24. Merges what were R20 and R21
(partial aggregation over the flip's Gather; the window's ordering
requirement), because R20 established they are one root cause.*

## 1. The seam, precisely

`planJoinlistSearch` (`relfromjoinlist.go:185`):

```go
func planJoinlistSearch(jl joinlist, prob *joinlistProblem) (Node, error)
```

It returns a **`Node`**. The `RelOptInfo` built inside
`makeRelFromJoinlist` — the object carrying `PartialPathlist`,
`Pathkeys`, `Pathlist` and `CheapestTotal` — is discarded when the
function returns. `tryPGShapedJoinSearch` (`joinsearchseam.go:216`)
returns `(Node, Expr, bool)` for the same reason, and every upper stage
downstream therefore works on finished nodes.

Two consumers need what was thrown away:

| consumer | needs | today's symptom |
|---|---|---|
| `addPartialAggSplitPath` (`partialaggupper.go:92`) | the join rel's `PartialPathlist` | refuses on `subtreeHasGather(child)`; under the flip goopg loses the `Partial`/`Finalize` split PG emits (K23) |
| `createWindowPaths` / `addGroupingPaths` | the join rel's `Pathkeys` | no path is ever credited for an ordering, so the sorted aggregate cannot win a contest PG's wins constantly (K12(B)) |

Both files say so themselves — `partialaggupper.go`'s header
(*"the rel carrying `PartialPathlist` dies inside `planJoinlistSearch`
before the aggregate stage runs"*) and `windowsetoppaths.go:19`
(*"above the search seam the inputs are finished Nodes with no
pathkeys"*).

## 2. Oracle

PG never has this problem because its upper stages consume
`RelOptInfo`s throughout:

- `create_partial_grouping_paths` (`planner.c:7351`) seeds
  `partially_grouped_rel` from **`input_rel->partial_pathlist`** —
  aggregating on the partial path, *below* any Gather;
- `gather_grouping_paths` (`planner.c:7704`) gathers the partially
  grouped rel and finalises above it;
- `create_grouping_paths` offers sorted aggregate paths carrying their
  pathkeys, and the upper planner selects the cheapest path *satisfying*
  a required ordering rather than merely the cheapest.

The ordering is what makes PG's shape possible: partial aggregate
first, Gather second. goopg's flip inverts it (Gather first, materialised
as a Node), which is why R20 found the guard cannot simply be relaxed —
relaxing it yields a double Gather, not PG's plan.

## 3. The change

**Slice 1 — carry the rel out.** `planJoinlistSearch` returns
`(Node, *RelOptInfo, error)`; `tryPGShapedJoinSearch` returns the rel
alongside its node. Nothing consumes it yet. Purely additive: every
existing caller ignores the new value, so no plan can move. This slice
is separately committable and its gate is "zero plan movement on both
corpora", which is a strong, cheap check.

**Slice 2 — partial aggregation from the partial path (K23).** Give
`addPartialAggSplitPath` the rel. When `PartialPathlist` is non-empty,
build PG's shape — partial aggregate over the partial path, Gather,
finalise — instead of refusing on `subtreeHasGather`. The
`subtreeHasGather` guard stays for the post-pass route, which still
needs it; the new arm simply never reaches it, because it works below
the Gather rather than above it.

**Slice 3 — pathkeys for the ordering contest (K12(B)).** Give the
grouping and window stages the rel's `Pathkeys` so a path delivering a
required ordering is credited for it, and a sorted aggregate can win
where PG's does.

Slices 2 and 3 are independent given slice 1, and must be measured
separately — bundling them would make an unexplained plan move
impossible to attribute, which is the mistake R10's design avoided by
leaving the post-pass running.

## 4. Gates

- Slice 1: **byte-identical plans on both corpora** (it adds a return
  value and no consumer). Anything else means the refactor moved
  something and must be found before proceeding.
- Slices 2 and 3, each: values all-zero both corpora; parity per-query
  against the live references; every moved plan adjudicated **against
  PG**, per R16/R17 — including running synthetic fixtures' SQL on
  `:65432` rather than reading a capture, and re-reading each report's
  headline against its own caveats before committing (K22).
- After slice 2: re-measure `aggregation-strategy` under the flip. The
  expected outcome is that the 10 → 14 move disappears, which is the
  test of whether K23's diagnosis was right.

## 5. Prediction

- Slice 1 moves nothing.
- Slice 2 restores `Partial`/`Finalize` under the flip and returns
  `aggregation-strategy` to ~10 on TPC-H, removing the last documented
  objection to landing the flip.
- Slice 3 raises TPC-DS `GroupAggregate` adoption toward PG's 100 from
  today's 13 — the single largest identified lever on that corpus.
- **Match counts:** I do not predict movement on TPC-H, whose remaining
  divergences are dominated by `join-order` (18 of 22 queries), which
  none of this touches. TPC-DS may gain its first matches; I would not
  bet on it.

## 6. Scope honesty

This is the largest item in the workstream and is not a one-round task.
Slice 1 is bounded and safe; slices 2 and 3 each change plan selection
on both corpora. The rounds should be taken separately, with the flip
landing only after slice 2 measures out.

## 7. Review record

Subagent delegation unavailable (`Task` not exposed; recorded since R0).
Self-review:

- **The seam was read at both ends** — `planJoinlistSearch`'s signature
  and `tryPGShapedJoinSearch`'s — rather than inferred from the two
  files' comments, because this workstream has falsified nine claims
  drawn from comments (K4, K22).
- **The guard is preserved, not deleted** (§3 slice 2). R20 established
  it is correct for the post-pass route; a design that removed it would
  trade K23 for a double-Gather.
- **Slices 2 and 3 are deliberately not bundled**, so an unexplained
  move stays attributable — the same discipline that let R19 attribute
  `aggregation-strategy` to a single producer.

---

## 8. Slice 2 plumbing map (added after slice 1 landed)

Slice 1 made the rel available at `tryPGShapedJoinSearch`. Slice 2's
first task is getting it to the consumer, and the route is longer than
the signature change suggests — recorded here so it is not rediscovered:

| hop | today | needs |
|---|---|---|
| `tryPGShapedJoinSearch` (`joinsearchseam.go:532`) | has the rel, discards it with `_` | return it |
| `tryJoinSearch` (`joinsearchseam.go:203`) | returns `(Node, Expr, bool)` | carry the rel |
| `planSelectWithSettings` (`planner.go:1529` and the two other call sites) | consumes the node | hold the rel for the upper stages |
| `createGroupingPaths` (`groupingpaths.go:46`) | `(u, aggNode, cat, ps, tupleFraction)` — **no rel** | take the rel |
| `addPartialAggSplitPath` (`partialaggupper.go:73`) | refuses on `subtreeHasGather(child)` | use `rel.PartialPathlist` to build below the Gather |

`createWindowPaths` (`windowsetoppaths.go:91`) needs the same rel for
slice 3, so the plumbing is shared and should be done once.

**Recommended split**: slice 2a is the plumbing alone, with slice 1's
gate (byte-identical plans on both corpora, since nothing consumes it);
slice 2b is the behaviour change. The same reasoning that justified
splitting slice 1 applies with more force here, because the route
crosses `planner.go`.


---

## 9. Slice 2b construction plan (added after the prerequisite landed)

The prerequisite (`REPORT-slice2b-prereq.md`) established by measurement
that `searchedRelOf(child)` returns a rel with a **non-empty**
`PartialPathlist` under the flip. The construction site and shape are
now known too, so slice 2b is fully specified:

**Site**: `addPartialAggSplitPath` (`partialaggupper.go`), a new arm
placed BEFORE the `subtreeHasGather` guard.

**What the existing code already does**, and which slice 2b mirrors: it
builds the Gather itself rather than finding one —

```go
nsGatherCost := gatherCost(cp, pseed.Cost, inputRows)
nsGather := &Path{Kind: PathGather, Rel: grouped, Rows: inputRows,
                  Cost: nsGatherCost, Children: []*Path{pseed}}
```

so the shape is `pseed` → partial agg → `nsGather` → finalise, with
`addPartialAggSplitArm` assembling the partial/final pair.

**The one difference**: today `pseed` is `newPrebuiltPath(partialRel,
child)` — a path over the WHOLE child, which under the flip already
contains a Gather, hence the refusal. Slice 2b instead seeds from
`searchedRelOf(child).PartialPathlist[0]` (PG takes
`cheapest_partial_path`, `planner.c:7452`), whose node is obtained via
`createPlanNode`. That path is BELOW the Gather by construction, so the
guard is never reached and no double-Gather can arise.

**Row counts** are the correctness-critical part and must not be
improvised: the existing arm already divides by
`getParallelDivisor(workers, ps.ParallelLeaderParticipation)` and sizes
partial groups through `estimateNumGroups` on the per-worker count
(upstream's `dNumPartialPartialGroups`, `planner.c:7452`). Seeding from
a partial path means `pseed.Rows` is ALREADY per-worker, so the divisor
must not be applied twice — this is the specific trap, and it produces
N+1-copy or short-count results rather than an error.

**Gates**: values all-zero on both corpora are load-bearing here, not a
formality — a mis-split parallel aggregate returns wrong rows, not an
error. Success test unchanged: `aggregation-strategy` 10 → 14 under the
flip disappears.
