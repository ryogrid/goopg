# 08 — Parallel Paths and the Parallelize Decision

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [02](02-pg-path-and-cost-oracle.md), [06](06-scan-and-join-path-costs.md), [07](07-cost-driven-join-order.md) |
| premise | this chapter delivers the milestone's "whether to parallelize and how many workers" |

## 0. Why this chapter exists

The milestone requires the planner to decide, per query, **whether to parallelize
and at what degree**, sensibly. Round 4 gave goopg the parallel *operators*
([parallel-query/](../parallel-query/README.md)); it did not give it a principled
*decision* — insertion is a structural push-down (`findPartialSubtree`,
`parallel.go:243`) gated by a block-size threshold, not a cost comparison against
the serial plan. This chapter supplies the decision, and it is the chapter the
Path model most earns: only because a **partial path** is a first-class object with
its own divisor-divided cost can the parallelize question be asked honestly.

## 1. The dishonest comparison this chapter avoids

The tempting shortcut — and the one an earlier design review caught — is: take the
one serial plan, splice a `Gather` on top, re-cost, and keep it if cheaper. **This
never turns parallelism on.** If the subtree under the Gather is the *unchanged*
serial subtree, its cost is the full serial cost, and the gathered total is
`full_serial + parallel_setup_cost(1000) + parallel_tuple_cost · rows` — strictly
greater than serial, for every query. A cost model built that way would refuse to
parallelize anything.

The error is that the subtree under a Gather is **not** the serial subtree. Each
worker processes ~1/d of the pages, so the partial subtree's scan, filter, and
join costs are **divided by the parallel divisor** ([02](02-pg-path-and-cost-oracle.md) §4.8).
The Gather's job is to divide the bulk of the plan's cost by `d` and add back a
flat setup and a per-tuple transfer. Whether that trade wins is a real question —
but only if the divided-cost partial path exists as an object to cost. That is why
this bundle chose the Path model ([README](README.md)).

## 2. Partial paths and generate_gather_paths

Every rel that can be produced in parallel carries a `PartialPathlist`
([03](03-path-substrate-and-plan-creation.md) §1) alongside its serial `Pathlist`:

- **Base rels** get a partial SeqScan path at scan time
  ([06](06-scan-and-join-path-costs.md) §1.1), costed with the page/tuple terms
  divided by the divisor for the ladder-chosen worker count.
- **Join rels** get partial paths by joining a partial outer with a full inner
  (the probe side is partitioned, the build side is shared) — exactly the shape
  [parallel-query/07](../parallel-query/07-parallel-hash-join.md) §3.1 and
  [parallel-query/12](../parallel-query/12-parallel-multi-way-hash-join.md) design
  for hash and multi-hash joins. The partial join path's cost divides the probe
  term by the divisor and leaves the shared build serial.

`generateGatherPaths(rel)` reproduces `generate_gather_paths`
(`postgres/src/backend/optimizer/path/allpaths.c:3099`): for the rel's cheapest
partial path, create a **Gather path** over it and `add_path` it into the rel's
*serial* `Pathlist`, where it competes on equal terms with the serial paths. When
the partial path carries useful pathkeys, also create a **Gather Merge path**
(`create_gather_merge_path`, `pathnode.c:2098`; `cost_gather_merge`,
`costsize.c:485`) that preserves the order, reproducing `generate_useful_gather_paths`
(`allpaths.c:3236`).

The Gather path's cost ([02](02-pg-path-and-cost-oracle.md) §4.8):

```
startup = partial_path.startup + parallel_setup_cost         (+1000)
total   = partial_path.total    + parallel_setup_cost
        + parallel_tuple_cost · (partial_path.rows · d)       (rows emerging)
```

## 3. The parallelize decision is just set_cheapest

Because the Gather path is `add_path`'d into the same pathlist as the serial paths,
**the parallelize decision requires no special logic**: `set_cheapest` picks the
Gather path when its total (divided partial cost + setup + transfer) beats the
serial cheapest, and picks serial otherwise. Parallelism turns on exactly when it
pays, in the same currency as every other choice. This is the whole benefit of the
Path model stated in one sentence.

**Where it runs.** Per [03](03-path-substrate-and-plan-creation.md) §4, the
parallel decision must run **post-cache, per-statement** so it sees fresh
statistics and the current `max_parallel_workers_per_gather`. The partial pathlist
for the cached plan's rels is reconstructed at that point — the serial join order
is fixed by the cache, but the partial paths over that fixed shape, and the Gather
decision over them, are re-derived each execution. This preserves `MaybeAddGather`'s
existing non-mutating, post-cache property (`parallel.go:13-28`) while replacing its
structural push-down with a cost comparison.

## 4. Worker degree stays the size ladder

The worker **count** is **not** cost-optimised — invariant #3 of the
[README](README.md). PG chooses it by size in `compute_parallel_worker`
(`allpaths.c:4274`): 1 worker, +1 per factor-of-three increase in heap pages, capped
at `max_parallel_workers_per_gather`. goopg already reproduces this exactly in
`computeParallelWorkers` (`internal/planner/parallel.go:459`, run on the live block
count `BlocksForTable`). The cost model **keeps it**: the partial path is generated
at the ladder-chosen degree, and cost decides only whether to Gather that path, not
how many workers to give it. Cost-optimising the count would diverge from the
oracle and discard a correct, already-implemented mechanism. This still fully
delivers the milestone's "how many workers": the count falls out of the size
ladder, as PG intends, and Gather-vs-serial falls out of `set_cheapest`.

For ordered results the Gather Merge degree follows the same ladder; the choice
between Gather and Gather Merge is `add_path`'s (Gather Merge costs more per tuple
but supplies pathkeys that save a downstream sort).

## 5. Generalising chapter 11's partial-aggregate split

[parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) built a
self-contained *ratio* comparison for whether to split an aggregate into
Partial/Finalize, precisely because there was no absolute cost model. With this
bundle there is one, and the split becomes a special case: two candidate paths over
the same aggregate rel — a **serial-aggregate-over-Gather** path and a
**Finalize-over-Gather-over-Partial** path — both `add_path`'d, `set_cheapest`
chosen. The ratio ρ = min(1, (ndistinct/R)·d) chapter 11 derived is what the two
paths' costs *evaluate to* once the shared subtree cancels; the absolute model
reproduces the same decision without the special-case gate.

**The one reconciliation that must be carried forward:** goopg's partial aggregate
state does **not** cross the Gather as tuples — it merges into a shared
`aggPartialAccum` under one mutex ([parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §2.1).
So `cost_gather` over a Partial aggregate does **not** charge `parallel_tuple_cost ·
(ndistinct · d)` for state transfer the way PG's does; the split's real cost is the
`Gw·d` mutex-serialised merges, chapter 11's `c_merge` term. The Path cost of the
Finalize-over-Partial path must use chapter 11's merge model, not a literal
`cost_gather` transfer term, for the aggregate case. This is the one place the
oracle's Gather formula is *replaced* rather than reproduced, and it is replaced
because goopg's execution genuinely differs — recorded so an implementer does not
"fix" it back to PG's formula.

The hard memory-ceiling refusal (chapter 11 §3.4 — no hash-agg spill, so
`Gw·entrySize > work_mem` refuses outright) stays a **refusal**, not a cost term:
it is applied before `add_path` even sees the split path, because an OOM is not a
slow query.

## 6. Divergence from PostgreSQL

- **Partial aggregate cost uses the mutex-merge model, not `cost_gather`'s tuple
  transfer** (§5) — goopg's partial state crosses a mutex, not the Gather. The one
  deliberate substitution of a goopg cost for a PG cost, justified by a real
  execution difference.
- **Worker count is the size ladder, not a cost search** (§4) — faithful to PG,
  stated because the milestone phrase "how many workers" invites the opposite
  reading.
- **The memory ceiling is a hard refusal outside the cost comparison** (§5),
  goopg-specific because goopg's hash aggregate cannot spill; PG can, and costs the
  spill instead of refusing.
- **The parallel decision runs post-cache** (§3), inheriting the ANALYZE-
  invalidation gap ([03](03-path-substrate-and-plan-creation.md) §4.1) — but the
  parallel decision is exactly the part that already re-reads fresh statistics, so
  it is the least affected.
