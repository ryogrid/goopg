# M0134-0001 P7 — an order-sensitive aggregate refuses its SPLIT, not the whole plan

status: accepted
milestone: M0134-0001 (`aggregates.sql` regress digestion), slice **S16**
date: 2026-08-17

## Summary

goopg refused parallelism for an **entire statement** whenever an undecorated
order-sensitive aggregate (`array_agg`, `string_agg`, `json_agg`, `jsonb_agg`,
`xmlagg`, `json_object_agg`, `jsonb_object_agg`) appeared anywhere in the plan.
PostgreSQL refuses no such thing: it declines only to **split** such an
aggregate into Partial/Finalize, and is perfectly happy to plant a scan-level
`Gather` underneath the serial `Aggregate`. The fix deletes the
whole-statement veto; the split refusal — which is the property that actually
protects correctness — was already implemented independently and is untouched.

Target hunk, `aggregates.sql` (`explain (costs off) select
array_dims(array_agg(s)) from (select * from pagg_test) s;`):

```
PG 18.3                              goopg (before)
  Aggregate                            Aggregate
  ->  Gather                           ->  Seq Scan on pagg_test
        Workers Planned: 2
        ->  Parallel Seq Scan on pagg_test
```

Note PG's node label is a plain `Aggregate`, **not** `Finalize Aggregate` —
that is the whole point. PG did not split the `array_agg`; it parallelised only
the scan below it.

## Measured outcome — the veto was the blocker, but not the last one

`aggregates` went **1001 → 999 lines, 29 → 29 hunks** (sentinels byte-identical:
`functional_deps` 56, `groupingsets` 2373). The predicted hunk did **not**
close, and the reason is worth more than the two lines:

```
 explain (costs off)
 select array_dims(array_agg(s)) from (select * from pagg_test) s;
   Aggregate            <- context: goopg now matches
    ->  Gather          <- context: goopg now matches
    Workers Planned: 2  <- context: goopg now matches
-  ->  Parallel Seq Scan on pagg_test     (PG 18.3)
+  ->  Seq Scan on pagg_test              (goopg)
```

S16 did exactly what it claimed at the **plan** level: the `Gather` and its
`Workers Planned: 2` moved from `+`/`-` divergence into unchanged context, which
is the whole structural half of the hunk. What survives is one line, and it is
not a planner gap at all — it is a **rendering** gap. PG prefixes a node's label
with `"Parallel "` whenever `plan->parallel_aware` is set
(`postgres/src/backend/commands/explain.c:1630-1631`, immediately before the
`pname` append and the sibling `"Async "` prefix); goopg's EXPLAIN walker has no
such prefix, so a scan below a `Gather` prints undecorated. The same single-line
residue appears at the `v_pagg_test` hunk.

This surface could not have been observed before this slice: with the veto in
place no `Gather` was ever planted, so goopg had no scan-below-a-Gather to
mislabel. **Narrowing the veto is what exposed it** — the divergence is not a
regression S16 introduced but a pre-existing renderer omission S16 made
reachable. Ledgered as the follow-up slice; it is a formatter change in the P2
family, not a parallel-planning one, and carries that family's much smaller risk
shape.

The honest summary of this slice is therefore: the bucket label was right, the
fix was right, and the predicted line-count win was wrong because a second,
independent gap sat behind the first. Two lines of the diff moved; the
structural claim is verified by the hunk's own context lines rather than by the
line count.

## Why the old veto was both redundant and too strong

`MaybeAddGather` (`internal/optimizer/parallel.go:115`) calls
`statementIsParallelSafe(root)` **first**. A `false` there returns the plan
untouched and no `Gather` is placed **anywhere** — not below the aggregate, not
below anything. `statementIsParallelSafe` delegates the walk to
`subtreeHasUnsafeNode`, whose `*Aggregate` arm set `unsafe = true` as soon as
`AggregateIsOrderSensitive` matched any call in the node.

That arm defended two claims, and neither survives:

1. *"It is not decomposable either, so parallelising below it buys only the
   scan."* — Buying the scan is exactly what PG does here, and the split is
   already refused independently: `aggregateSplitIsSafe`
   (`internal/optimizer/parallel_agg.go:104-129`) consults
   `AggregateIsDecomposable` (`:28-65`), a whitelist where `array_agg` and
   `string_agg` fall through to `default: return false`. The veto could not
   prevent a split that the whitelist was not going to permit in the first
   place. Note that `aggregateSplitIsSafe` calls `AggregateIsDecomposable`,
   **not** `AggregateIsOrderSensitive` — the two predicates were never wired
   together, which is why removing one leaves the other's guarantee intact.
2. *"An order-sensitive aggregate above a Gather returns its elements in
   worker-arrival order — a different order on every run … costs determinism."*
   — True, and PG accepts it. A `Gather` is documented to destroy input
   ordering (`postgres/doc/src/sgml/parallel.sgml:101-104`), and an
   `array_agg` with no `ORDER BY` has no defined element order to begin with.
   PG's parallel-safety walk, `max_parallel_hazard_walker`
   (`postgres/src/backend/optimizer/util/clauses.c:827-970`), has **no**
   `Aggref` order-sensitivity case at all — parallel safety is decided solely
   by the function's `proparallel` catalog marking. The only aggregate-specific
   restriction is on the split, in `create_partial_grouping_paths`
   (`postgres/src/backend/optimizer/plan/planner.c`), consistent with the
   split-only wording at `parallel.sgml:380-391`.

Preserving a determinism guarantee PostgreSQL does not offer is not
compatibility — it is a divergence with a comment attached.

## The change

Delete the `case *Aggregate:` arm from `subtreeHasUnsafeNode`
(`internal/optimizer/parallel.go`, ~lines 184-194). Everything else in the
refusal set stays: the whole-plan `*Insert`/`*Update`/`*Delete`/`*Merge`/
`*CTEDMLPrefix`/`*DDL`/`*Transaction` cases in `statementIsParallelSafe`, the
`*LockRows` arm (PG likewise disables parallelism for plans carrying row
marks), and the four `tableIsUnsafeForParallel` scan arms.

With the veto gone, the fallback shape that `findPartialSubtree` /
`terminatesPartial` (`parallel.go:265`, `:322-335`) already implement and
document — a `Gather` below a non-split `Aggregate` — becomes reachable for the
first time. No new plan-construction code was needed; the machinery existed and
was unreachable behind the veto.

`AggregateIsOrderSensitive` (`parallel_agg.go:87`) loses its only caller and is
**deliberately retained** as documented zero-caller code, with a comment
recording why. It encodes a real PG distinction that the ledgered S10b slice
needs: once combine-function support lands in
`internal/executor/parallel_agg_combine.go` and `array_agg`/`string_agg` become
decomposable, order-sensitivity is exactly what must gate splitting an
`ORDER BY`-decorated call. Deleting it would only force S10b to re-derive it.
(Go does not report unused *exported* functions, so neither `go build` nor
`go vet` flags this — it is a judgement call, recorded here so it is not
mistaken for an oversight.)

## An existing test encoded the old veto's guarantee

`TestPartialAggregateRefusals` (`internal/executor/parallel_agg_split_test.go`)
went red on this diff, and the failure was correct behaviour reported as a
regression. The test asserts, for `string_agg`/`array_agg`/`count(DISTINCT v)`,
both that the SPLIT is refused **and** that the parallel-planned output equals
serial output positionally — element order inside the array/string included.
That second assertion held only as a side effect of the veto: `isSplit=false`
used to imply "no `Gather` anywhere", so these aggregates always consumed rows
in single-threaded scan order. With a `Gather` now legal below the unsplit
`Aggregate`, element order varies run to run (observed: two different orderings
from consecutive runs of the same binary).

The assertion was relaxed to sorted-multiset equality **for those two subtests
only**. `count(DISTINCT v)` keeps exact comparison — a scalar count did not
become nondeterministic. The split-refusal assertion (`isSplit` false) and the
exact row-count check are untouched; they are the guarantees S16 genuinely
preserves. A multiset comparison still catches a dropped or duplicated row,
which is the actual parallel-execution failure mode this test guards.

The alternative — shrinking the fixture below the size gate to keep the subtests
serial — was rejected: it would have kept the old assertion meaningful only by
no longer exercising the Gather-below-Aggregate path this slice exists to make
reachable. Pinning positional order would assert a property PostgreSQL does not
offer (`postgres/doc/src/sgml/parallel.sgml:101-104`), which is the same
category of error as the veto itself.

## Risk shape

This change can only ever **add** parallelism to a plan, which makes its blast
radius the set of statements whose plans newly acquire a `Gather` — a different
risk class from the milestone's other slices, and the reason it was ledgered
separately from S15 rather than folded into it. The gate set is therefore the
full regress sweep of the touched file plus its sentinels, plus the TPC-H
row-count spot-check, not just the optimizer package tests.

## What this does NOT close

The `"Parallel "` label prefix (see "Measured outcome" above) — the single
surviving line of the target hunk. `explain.c:1630-1631` gates it on
`plan->parallel_aware`, so the goopg fix needs the equivalent per-node flag to
be readable at render time, not just the `Gather` to be present.

`v_pagg_test` in `aggregates.sql` closes only **partially**. Its PG shape needs
the inner aggregate genuinely split (Partial/Finalize), which requires
combine-function support that `combineAggRuntime`
(`internal/executor/parallel_agg_combine.go`) does not have for
`array_agg`/`string_agg` — the already-ledgered **S10b** work. Whoever takes
S10b must move the matched pair together: `AggregateIsDecomposable` (planner
whitelist) and `combineAggRuntime` (executor dispatch). S10a's `balk` bug is
what happens when only one of them moves.

## Process note carried forward

`ci/logs/<run>/testport/regress-diffs/` snapshots go stale fast in this
milestone — the nightly batch lags same-day commits, and the
`ci/logs/20260817-011734` capture does not contain this hunk at all because it
predates S15. Regenerate with `scripts/pg-regress-runner.sh <file>` before
starting any slice; never bucket against a stale capture.

This is also the milestone's first bucket label that survived verification.
S11 ("underline width") and S15 ("parallel planner") were both plan-shape
misattributions — bucketing a regress diff by the shape of the divergent output
over-attributes to whichever subsystem *prints* it. The discipline that caught
those (delegate a root-cause research pass before believing a bucket) is what
confirmed this one; the answer being "yes, the bucket was right" is the value
the check produces, not a sign the check was unnecessary.
