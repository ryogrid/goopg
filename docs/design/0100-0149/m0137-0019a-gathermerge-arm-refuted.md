# M0137-0019a — the GatherMerge arm is not mispriced; goopg cannot build PG's shape

Status: REFUTED BY PROBE 2026-09-20 — no repricing is possible or correct;
the task is blocked on an executor capability and marked `[!]`
Kind: recon (filed as `impl`; the probe that had to run first refuted its
premise before any code was written)
Parent: M0137-0019
Milestone: M0137 (plan-parity harness)
Predecessor: `docs/design/0100-0149/m0137-0019-parallel-divergence-triage.md`

## 1. What this task was filed to do, and why that is wrong

M0137-0019's triage found the `upper.groupagg.gathermerge` candidate
**generated and ACCEPTED 9 times out of 9** in a `DP_TRACE=1` probe, yet losing
the election by a wide margin on TPC-H Q1:

```
upper.groupagg.split       total=    67840.37  accepted   <- wins
upper.groupagg.gathermerge total=  1510695.91  accepted
```

From that it concluded "one mispriced arm — M0140 costing territory", and filed
this task to reprice it against `cost_gather_merge`.

**That conclusion was wrong, and this loop's probe says why: the candidate
goopg generates is not the shape PG picks.** No repricing can make it PG's
plan, because it is a different plan.

## 2. The two shapes are not the same shape

PG's Q1 (and Q5, Q12, Q15a-VIEWBODY):

```
Finalize GroupAggregate
  -> Gather Merge
       -> Sort                      <- sorts the PARTIAL GROUP-STATES (6 per worker)
            -> Partial HashAggregate
                 -> Parallel Seq Scan on lineitem
```

goopg's `upper.groupagg.gathermerge` arm (`partialaggupper.go:418-476`) is
labelled, in its own comment, the **no-split** arm:

```
GroupAggregate
  -> Gather Merge
       -> Sort                      <- sorts the RAW INPUT (5.9 M / d rows per worker)
            -> partial scan seed
```

It sorts the scan's rows, not an aggregate's output. For Q1 that is ~1.48 M
rows per worker against PG's 6 group-states. **1 510 695.91 is therefore not a
mispricing — it is the correct price of a genuinely expensive plan**, and it is
the reason the arm loses. The `split` arm that wins (67 840.37) is the right
call on the candidates goopg actually has.

The candidate PG picks — worker-side sort of the PARTIAL AGGREGATE's output,
then a merge — has **no producer in goopg at all**. Upstream builds it in
`gather_grouping_paths`
(`postgres/src/backend/optimizer/plan/planner.c:7704-7724`), which walks the
partially-grouped rel's `partial_pathlist`, stacks `create_sort_path` on the
group-by pathkeys and wraps the result in `create_gather_merge_path`.

## 3. Why that producer cannot simply be added

The missing arm looked like a contained change: mirror `addPartialAggSplitArm`
with a `Sort → GatherMerge` boundary instead of a plain `Gather`, reusing
`sortPathForBounded`, `gatherMergeCost` and `costAgg`'s sorted arm — no new
constant. Before writing it, one fact had to be checked: what crosses goopg's
partial-aggregate boundary?

Nothing does.

```go
// internal/executor/operators_join_agg.go:2351-2356
case optimizer.AggModePartial:
        // Publish this worker's groups and emit NOTHING. The Finalize node
        // supplies every output row from the accumulator, so a Partial node
        // returning zero rows is by construction, not a failure …
        pub := lookupAggPartialAccum(ctx, o.plan)
```

goopg's Partial Aggregate **emits zero rows**. It merges each group into a
shared, mutex-guarded accumulator, and the Finalize node reads that accumulator
rather than a tuple stream. The `Gather` in goopg's split arm is a
control-flow device, not a row conduit — the split arm's own comment says so
when it explains why it charges anything at all at the boundary:

> goopg's Partial node emits no rows at all and merges each group into a
> mutex-guarded accumulator instead (operators_join_agg.go:2351-2372); charging
> a group-state at `parallel_tuple_cost` is C-19g's one deliberate adaptation
> and its whole economic argument.
> — `partialaggupper.go:553-556`

A `Sort` over a node that emits no rows sorts nothing, and a `Gather Merge`
over it merges nothing. **PG's shape is not expressible in goopg's execution
model**, so filing the producer would file an unbuildable path.

## 4. The corrected verdict, and what it means for the corpus

M0137-0019 §3.2 classified family B (8 queries, plus the Q3/Q18 mirrors) as a
**costing gap in M0140's territory**. That is hereby corrected: family B is a
**designed executor-model divergence**, the same class as family A.

- **Family A** (7 queries) — goopg builds one shared hash table in the leader;
  PG's `Parallel Hash` builds a partial inner cooperatively in DSM.
  `parallel_hash = true` is a stated refusal
  (`internal/optimizer/joinpathsparallel.go:59-61`).
- **Family B** (8-10 queries) — goopg's partial aggregate publishes into a
  shared accumulator and emits no rows; PG's emits partial group-states as a
  tuple stream that a worker-side `Sort` can order and a `Gather Merge` can
  merge.

Both are coherent choices for an engine whose "workers" are goroutines sharing
one address space, and both are written down in the code that makes them. But
together they mean:

**`parallelism=16/22` on the canonical TPC-H parity corpus is essentially
floored by two executor design decisions. No planner or costing task can move
it.** The only remaining planner-side member is Q4 (M0137-0019b, no partial
path beneath a `Nested Loop Semi Join`), which is one query.

That is an owner-level fact about the corpus the owner made canonical on
2026-09-20, which is why it is reported here rather than worked around.

## 5. Disposition

- This task is marked `[!]` — blocked on an executor capability
  (row-emitting partial aggregation), not on a decision the loop may take. The
  repricing it was filed to do is not correct work: the arm is priced right.
- A ledger row records the executor-model divergence with its resume point.
- **M0137-0019b** (Q4's semi-join partial path) is unaffected and remains the
  one selectable planner-side member of this family.

## 6. Method note

The M0137-0019 triage stopped at "generated and accepted, loses on cost",
which is true and was measured. What it did not do was check whether the
generated candidate is PG's candidate. The cheap check that would have caught
it — read the producer's own comment, which says "no-split" in the first line —
cost about two minutes this loop. Recorded because this is the third premise in
this session that survived until someone compared the goopg shape to the PG
shape node by node.
