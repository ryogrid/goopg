# R45 — goopg cannot combine a split aggregate with an ordered gather (K96)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: **DESIGN REJECTED on review (rev 2).** The diagnosis in §2 is
correct and worth keeping; the FIX in §3 is architecturally impossible in
goopg and would have shipped a label lie plus a wrong-answer hazard. §8
records the refutation and what the real item is.*

## 1. The measurement

| node | goopg TPC-H | goopg TPC-DS | PG TPC-H | PG TPC-DS |
|---|---|---|---|---|
| `Finalize GroupAggregate` | **0** | **0** | 5 | 18 |
| `Gather Merge` | **0** | 3 | 9 | **85** |

goopg emits `Finalize GroupAggregate` **nowhere in either corpus**, and
`Gather Merge` 3 times against PG's 94. That is a systematic gap of the
same order as the `Parallel Hash` one (K79: 157 build nodes), and it feeds
the three largest TPC-DS categories after join-order — `parallelism` 88,
`sort-strategy` 83, `aggregation-strategy` 82.

## 2. Root cause — structural, not a cost verdict

K94 recorded Q1's gap as "a costing divergence, because
`GOOPG_PARTIAL_SORT_PATHS=on` leaves the plan byte-identical". That
observation was right; the conclusion was incomplete. **The shape PG picks
is not reachable at all**, so no cost setting could select it.

`rebuildWithGather` (`parallel.go`) decides with a switch whose arms are
**mutually exclusive**:

```go
switch {
case tgt.mergeKeys != nil:   // → NewGatherMerge(...)   (target is a Sort)
case tgt.splitAgg:           // → splitAggregate(...)   (target is an Aggregate)
}
```

and `splitAggregate` (`parallel.go:1059`) **hardcodes a plain Gather**:

```go
gather := NewGather(a.Pos(), &partial, workers)   // :1067 — never NewGatherMerge
final := *a                                        // copies the ORIGINAL strategy
final.Mode = AggModeFinal
```

Two consequences follow directly:

1. A split aggregate always gets an **unordered** `Gather`. `Gather Merge`
   can therefore only arise from the *other* arm, where the partial target
   is itself a `Sort` — which is why goopg has exactly 3, all on TPC-DS.
2. The Finalize inherits the original node's strategy, so a HashAggregate
   input yields a Finalize **HashAggregate**. `Finalize GroupAggregate` is
   unreachable **by construction**, which the 0/0 measurement confirms.

PG's Q1 shape needs both arms at once:

```
PG                                    goopg
Finalize GroupAggregate               Sort
 -> Gather Merge                       -> Finalize HashAggregate
      -> Sort                              -> Gather
           -> Partial HashAggregate             -> Partial HashAggregate
```

PG sorts **inside the workers**, merges order-preservingly, and finalises
with an ordered `GroupAggregate`. goopg gathers unordered, finalises with a
hash, and sorts at the very top.

## 3. Why this is the PG-faithful fix, not plan-forcing

This is upstream's ordinary `create_ordered_paths` behaviour over partial
grouping paths: a partial aggregate's output can be sorted per worker, and
`gather_merge` then preserves that order so the finalize step can be a
`GroupAggregate` rather than a hash plus a top-level sort. goopg already
has every ingredient — `NewGatherMerge`, `AggModePartial`/`AggModeFinal`,
a `Sort` node, and a priced tournament in `partialsortpaths.go`. What is
missing is the **combination**, because the switch treats the two as
alternatives.

So the change is to let the two compose and then let cost choose — not to
prefer PG's shape. If goopg's cost model still prefers the unordered arm
after the shape is reachable, that is a separate (and now *legitimately*
costing) question, and K94's framing becomes true rather than premature.

## 4. Scope and risk

Touching `rebuildWithGather`/`splitAggregate` moves **every parallel
aggregate in both corpora** — the widest surface of any round this session.
`splitAggregate`'s own comments record that this pass runs on plans "the
process-wide cache may be handing to other sessions right now", so the
non-mutating shallow-copy discipline is load-bearing and must be preserved
in any new arm.

The `Gather Merge` executor exists (`operators_gather_merge.go`) and is
exercised by the 3 TPC-DS occurrences, so this is not a new executor
feature — unlike K92's Parallel Hash, which is.

## 5. Prediction, recorded before implementing (METHODOLOGY §2)

- `Finalize GroupAggregate` should become non-zero and `Gather Merge`
  should rise materially on TPC-DS.
- **Q1 should lose `sort-strategy`**, leaving `[parallelism]` — the same
  position Q14 is in. Whether that becomes a MATCH depends on the
  `parallelism` category, which for Q1 is the `Gather` vs `Gather Merge`
  node itself, so it may resolve together.
- Expect movement in `sort-strategy` (83) and `aggregation-strategy` (82)
  on TPC-DS; `join-order` (95) is not expected to move, as it has not for
  any round this session.
- **Match count may finally move.** If it does not, the report must say so
  as plainly as every prior round has.

## 6. Gates

Standard bar, with the sweep binding and held uncommitted until clean:
suites + `RALPH_PRECOMMIT_SCOPE=units`; TPC-H values **A/B against a
purpose-built pre-change binary, with the serving binary verified by inode**
(K91 — this is not optional boilerplate, it is the check whose absence
produced a false result this session); TPC-H plan structure diffed and every
change explained; TPC-DS SF0.5 sweep `PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0` with all 99 verdicts and row counts identical; parity
re-measured on both corpora, reporting `Finalize GroupAggregate` and
`Gather Merge` counts alongside the category table.

## 7. Open questions for review

1. Is `tgt.splitAgg` vs `tgt.mergeKeys` genuinely exclusive by
   construction, or does `findPartialSubtree` already have a notion of "an
   aggregate whose partial output is worth sorting"? If the latter, the
   change may be smaller than §2 implies.
2. Does `AggModeFinal` support a `GroupAggregate` strategy at all
   (ordered input, no hash table), or is the Finalize path hash-only in the
   executor as well as the planner? If the executor cannot run an ordered
   finalize, this round needs an executor slice and its size changes.
3. `splitAggregate` sets `InputTarget/InputTargetKnown = nil,false`
   deliberately. Does inserting a Sort between the Partial and the Gather
   invalidate anything else that reads positions into the partial's output
   schema?


## 8. REJECTED on review — the fix is impossible as designed (K97)

The adversarial review **verified every claim in §1 and §2** — the switch
really is mutually exclusive, `splitAggregate` really does hardcode
`NewGather` at `parallel.go:1067`, and the 0/0 measurement is real. Then it
refuted §3 at the root.

### Why PG's shape cannot be built here

**goopg's Partial aggregate emits ZERO rows.** It publishes transition
states through a side channel, not through the plan tree.
`parallel_agg_split.go` says so in terms: *"They do not travel through
Gather at all."* `AggModePartial` merges its groups into `aggPartialAccum`
and sets `o.rows = nil`; `AggModeFinal` rebuilds from `accum.order` —
**worker-arrival order**, not sorted order.

So the shape §3 proposed would be:

- a `Sort` between Partial and Gather sorting an **empty** stream;
- a `Gather Merge` above it merging **empty** streams;
- a plan that *renders* like PG's while executing exactly today's
  semantics.

That is precisely the "arbitrary plan-forcing" the goal forbids — and it is
worse than cosmetic, because the only *point* of the shape is to let the
top-level `Sort` be dropped, which requires claiming pathkeys on the
Finalize. Its output order comes from `accum.order`, so **Q1 would return
unordered rows**.

**A trap the review flagged for whoever tries this next:** `aggregateOp`
already sorts its output by group-key columns for determinism, including on
the Finalize path, so Q1 might *appear* correct after dropping the Sort.
That incidental sort uses collation 0, fixed ASC, fixed NULL ordering — it
cannot serve `DESC`, `NULLS FIRST/LAST`, non-default collations, or
ordering by aggregate outputs. **Do not mistake it for a pathkey.**

### `Finalize GroupAggregate` is unreachable for a SECOND, independent reason

§4's claim that this "is not a new executor feature" is **false**. Sorted
aggregation is gated to `Mode == AggModeSimple`
(`operators_join_agg.go:2222`), so a Finalize marked `AggStrategySorted`
falls through to the **hash** path. Marking it would print
`Finalize GroupAggregate` over a hash table — a label lie that
`operators_explain.go`'s own comment was written to prevent.

Also corrected: §2 said the Finalize is hashed because it *copies* the
original's strategy. More precisely, **the split arm never offers
`AggStrategySorted` at all** (`partialaggupper.go`).

### Prior art this round did not find

`partialaggupper.go:352-359` already reasons that presortedness cannot
survive goopg's Gather, and `parallel_agg_split.go` records the rejected
alternative (a pointer-bearing Datum kind, or a side channel threaded
through `rowBatch`/`TupleSlot`). This ground was surveyed before; R45
rediscovered the symptom without finding the note.

### What the real item is

PG's `AGGSPLIT_INITIAL_SERIAL` / `FINAL_DESERIAL` — **row-borne partial
aggregate states**, with per-aggregate serialize/deserialize, plus
`openSorted` extended to `AggModeFinal`. That is a multi-round executor
programme (state (de)serialisation per aggregate kind, `rowBatch`/
`TupleSlot` plumbing, an ordered-combine path, and Gather/Gather Merge
pass-through) — comparable to or larger than K92's Parallel Hash.

Only after it does §2's planner combination become meaningful, and only
then can Q1's shape be **priced** rather than merely rendered.

### Honest status of the diagnosis

§1's measurement and §2's structural cause stand and are worth keeping: the
combination is unreachable, which is why K94's "costing divergence" framing
was premature. But the reachability blocker is **two layers deeper** than
the switch — it is the partial-aggregate transport, and no planner change
can reach past it.
