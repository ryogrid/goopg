# R21 slice 2b — the right design, one assertion short

*Round 21, slice 2b. 2026-09-09. The unwrap is written and NOT enabled;
the reason is a specific, named assertion and the fix is identified.*

## 1. The design changed, and for the better

`DESIGN.md` §9 planned to seed the partial aggregate from
`PartialPathlist[0]` via `createPlanNode`. **That was the wrong design**,
and this file said so all along (blocker 1):

> *"goopg does not need a distinct partial PLAN. `gatherOp.runWorker`
> builds each worker's own copy of the Gather's child subtree … so the
> partial plan IS the serial subtree."*

If the partial plan is the Gather's child subtree, then the partial
input is **a node that already exists** — no `createPlanNode` on a Path,
no rebuild, no coordinate translation, and no boundary-map hole. So
slice 2b is an **unwrap**, not a construction:

```
child contains a search-placed Gather  →  child = thatGather.Child
```

and the existing arm then builds `partial agg → Gather → finalise` over
it unchanged, which is PG's shape. The `subtreeHasGather` guard is then
satisfied **honestly** rather than bypassed: after the unwrap there is
genuinely no Gather left, so the two-Gather hazard cannot arise.

`gatherToUnwrapForPartialAgg` is deliberately narrow: only a Gather
reached through the boundary chain's single-child wrappers (one under a
join is another rel's parallelism), and **never a `*GatherMerge`**,
which carries an ordering its consumer may depend on — dropping it would
lose that ordering silently.

## 2. It works, and it crashes

Enabled and measured under the flip, TPC-H:

| | flip, before 2b | flip, with 2b |
|---|---|---|
| `aggregation-strategy` | 14 | **10** |

**The K23 regression disappears** — the success test written down in
R19, met exactly.

And two queries (Q9, Q13) crash:

```
createPlan: Aggregate input target [] drops group-input column "l_year"
of a 26-column input row
```

So `aggregation-strategy=10` is partly real and partly an artefact of
two plans not existing. Both facts are reported; neither is netted
against the other.

## 3. Why it crashes, and why the assertion is right

The aggregate carries a **B-01c input target** — a keep-list of the
child columns it needs — derived from the child it was handed, i.e. the
**Gather**. Swapping in the Gather's child changes what "the input row"
is, and the stamped target no longer describes it, so the totality
assertion fires.

A Gather is schema-preserving, so the target is not wrong in *content*;
it is stale in *provenance*. The assertion is load-bearing and must not
be weakened: dropping a group-input column silently changes `GROUP BY`
semantics — wrong rows, not an error.

The fix is to re-derive the aggregate's input target against the
unwrapped child after the swap. That is a known, named path
(`stampAggInputTarget`'s), not new machinery.

**Verified the crash is mine, not the flip's**: R19 captured all 22
TPC-H plans under `GOOPG_GATHER_PATHS=all` with no failure.

## 4. What is committed

The unwrap helper, fully written and documented, with its call site
**commented out** and the exact failure recorded above it. Enabling
slice 2b is one line plus the input-target re-derivation.

Gate for the committed (disabled) state: TPC-H plans byte-identical,
suites green.

Landing it disabled rather than reverting it keeps the design, the
narrowness argument, and the measured result together with the one
obstacle — instead of leaving the next round to re-derive all four.

## 5. Second attempt, also measured, also wrong

§3 concluded the fix was to clear the stale stamp. **It was not**, and
the correction matters more than the guess:

Clearing the stamp on a copy of the aggregate spec produced **the same
panic, unchanged**. The target is not carried on the spec the producer
passes down — it is applied **post-hoc to the emitted node** by the
caller, the same ordering `createWindowPlan`'s comment describes for
windows (*"buildWindowStage stamps it on the emitted node after the
producer returns"*). Clearing a spec the assertion never reads changes
nothing.

So the live question is narrower and different from both my guesses:
**why does `deriveAggregateInputKeep` return an EMPTY keep marked
KNOWN for the unwrapped shape?** The panic reports `[]` with
`InputTargetKnown = true`, and `plan.go` is explicit that an empty list
"is NOT the same as unknown". Either the derivation should return
`ok = false` here — unknown, the safe direction — or it is failing to
enumerate group inputs through the new child, and that is the bug.

**The next attempt starts in `group_input_target.go`, not in the
producer.** Both of my producer-side fixes were wrong, and each was
found wrong by running it rather than by reasoning about it.

## 6. Next

1. Diagnose `deriveAggregateInputKeep`'s empty-but-known result for an
   unwrapped-Gather child.
2. Uncomment the call. Expect `aggregation-strategy` 14 → 10 with **22
   plans**, not 20.
3. Values gates on both corpora — load-bearing here, since a mis-split
   parallel aggregate returns wrong rows rather than an error.
4. Then slice 3, then the flip.
