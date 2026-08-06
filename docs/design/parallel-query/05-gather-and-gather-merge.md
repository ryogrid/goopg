# 05 — Gather and Gather Merge

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | [03](03-concurrency-substrate.md), [04](04-parallel-scan.md) |

`Gather` is the boundary between parallel and serial execution: below it, N
workers each run a copy of the partial plan; above it, ordinary serial
execution in the leader. `Gather Merge` is the same thing with output ordering
preserved. Both appear 11 times in the TPC-H reference set, so neither is
optional.

## 1. Plan nodes

```go
// Gather runs Child in nWorkers goroutines and interleaves their output in
// arbitrary order.
type Gather struct {
    pos           int
    Child         Node
    WorkersPlanned int
    SingleCopy    bool   // PG's single_copy: run Child in exactly one worker
    schema        Schema
}

// GatherMerge additionally requires each worker's output to be sorted by
// Keys, and merges the streams preserving that order.
type GatherMerge struct {
    pos           int
    Child         Node
    WorkersPlanned int
    Keys          []SortKey
    schema        Schema
}
```

Both output their child's schema unchanged. `SortKey` already exists
(`internal/planner/plan.go:1050`) and is reused verbatim.

Both nodes must be handled in **`planChildren`**
(`internal/executor/operators_explain.go:1221`) or they are invisible in
EXPLAIN, and in **both build paths** — `Build`
(`internal/executor/executor.go:22`) and the live `BuildFast`/`opNext`
(`:545`, `opnode.go:643`) — per [01](01-current-state-and-gap-analysis.md) B10.
The pragmatic first cut is to implement Gather as a legacy `Operator` and let
the slab builder reach it through the existing `OpAdapter` fallback
(`executor.go:563-569`), which keeps the slab path untouched; that is a
deliberate choice recorded in [10](10-roadmap.md), not an oversight.

**Slab ownership must be explicit either way.** Whatever builds a worker's
partial subtree, *each worker builds its own tree* — including its own
`opTreeSlab` if the slab path is used. N workers driving one shared slab would
race on the per-node `state` it carries (`opnode.go:236-243`).

## 2. The channel

### 2.1 Batching

A channel send per row would dominate the cost of a cheap scan. Workers
therefore accumulate a batch and send the batch:

```go
type rowBatch struct {
    rows []Row   // fully materialised — see 03 §3
    // worker index carried for stats attribution and determinism in merges
    worker int
}
```

Batch size is a tuning constant, not a semantic one. The starting value should
be chosen so a batch amortises the channel operation without adding meaningful
latency to `LIMIT`-terminated queries — on the order of a few hundred rows,
with the exact number settled by measurement in
[09](09-verification-and-measurement.md). A worker also flushes a partial batch
on EOF and on cancellation.

### 2.2 Backpressure

The channel is **buffered with a small capacity** (a few batches per worker).
This is the entire flow-control mechanism: when the leader is slower than the
workers, sends block, and workers stop producing. No credit scheme is needed.

PG needs one — `shm_mq` has a fixed 64 KiB per worker
(`execParallel.c:69`), and a worker that fills it must detach and wait — because
shared memory is a bounded pre-allocated resource. A Go channel of slices is
bounded by the same constant times the batch size, but the memory is ordinary
heap, so the size is a tuning choice rather than a fixed budget.

**Memory note:** N workers × channel capacity × batch size rows are in flight
at once. That is real memory not accounted for by `work_mem` — the same
accounting gap PG has, and called out in [09](09-verification-and-measurement.md)
as something to measure rather than assume.

### 2.3 What crosses it

Only fully materialised rows ([03](03-concurrency-substrate.md) §3). The
worker calls `Materialize()` — never `cloneRow` or `Slot.CopyTo` — at its
top-level output, immediately before appending to the batch.

## 3. Gather execution

```
Open:
   create child context (WithCancel of ctx.Ctx)
   pre-allocate N worker mctx children      // 03 §5: leader must do this
   build N operator trees from the same plan
   launch N-1 worker goroutines             // leader takes one share
   (each worker: defer recover(); defer close-and-report)

Next:
   if leader participation is on and the leader's own tree is not exhausted,
       pull from it
   otherwise receive the next batch from the channel
   drain the batch row by row
   on channel close and all workers joined: check first-error slot; EOF or error

Close:
   cancel the child context
   drain the channel until closed (so no worker blocks on send)
   JOIN every worker                        // 03 §6.1: mandatory, all paths
   merge per-worker notices, stats, instrumentation
   close the leader's own tree
```

Four details that are easy to get wrong:

- **`Close` must drain before joining.** A worker blocked on a channel send
  will never observe cancellation, so cancelling and then joining without
  draining deadlocks. This is the classic Go shutdown bug and it is worth an
  explicit test.
- **A drain needs a closer, so `Open` must start one on every exit path.**
  The drain above ends only when someone closes the channel, and that someone
  is a goroutine `Open` starts. Starting it at the *end* of `Open` — after the
  leader's own child is built and opened — means every error return in between
  hands `Close` a live channel with no closer, and the backend parks in the
  drain forever at 0 % CPU with the statement's real error never delivered.
  That was a live wedge for two loops
  ([leftdeep-joins/09](../leftdeep-joins/09-verification-and-acceptance.md)
  §5.20, TPC-H Q17). The invariant: **once the channel exists, a closer for it
  exists on every path out of `Open`** — started after the last worker launch
  (earlier and a worker could send on a closed channel, which panics a
  goroutine `serveConn`'s recover does not cover), and idempotent.
  `gatherMergeOp` gets this for free: each worker closes its own channel with a
  `defer`, so the closer cannot outlive-or-precede the goroutine that owns it.
- **`Close` must join on *every* path** — early `LIMIT`, error, cancellation.
  Worker lifetime is strictly nested inside the statement
  ([03](03-concurrency-substrate.md) §6.1) because the statement `mctx` is
  released by `defer stmtCtx.Release()` (`internal/server/dispatch.go:290-306`)
  and statement-end lock release runs on the statement backend ID.
- **Leader participation is not free.** While the leader is executing its own
  share, it is not draining the channel, so workers can block. PG has the same
  property. `parallel_leader_participation = off` exists precisely to turn this
  off, and goopg honours it.

### 3.1 Early termination

`LIMIT` above a Gather stops pulling. The Gather's `Close` cancels the child
context; workers observe it at their next throttled poll — per block boundary
in the scan (`operators_storage.go:1397-1401`) — flush nothing further, and
exit. The leader drains and joins.

Latency of shutdown is therefore bounded by one block's worth of work per
worker, which is the same bound serial execution already has.

## 4. Gather Merge

Identical machinery plus an ordering constraint: each worker's stream must
already be sorted by `Keys`, and the leader merges.

**The merge primitive already exists.** `sortHeap`
(`internal/executor/operators.go:953`) with `container/heap` methods at
`:958-962` is used today to merge N spill files in the external sort, and
`lessRows` (`:747`) is a standalone comparator over `[]SortKey`. Gather Merge
reuses both, with the sources being worker channels rather than spill readers.

Consequences worth stating:

- The leader must hold **one row per worker** at all times (the heap front), so
  it cannot simply drain batches; it interleaves.
- A worker whose stream is exhausted is removed from the heap, exactly as
  `nodeGatherMerge.c` removes readers.
- Determinism: for equal sort keys, output order between workers is *not*
  defined. Neither is PG's. Tests must sort or compare as multisets, which the
  existing TPC-H parity harness already does for unordered queries.

### 4.1 Why a sort below the Gather, not above

`Gather Merge` exists so the sort can be *partial*: each worker sorts its own
share (N smaller sorts, in parallel) and the leader merges. Sorting above a
plain `Gather` would serialise the whole sort in the leader, losing most of the
benefit. This is why PG has the node at all, and the same reasoning applies
unchanged here.

goopg's sort already spills to disk (`operators.go:640,792`), and each parallel
worker sorting its share means N concurrent spillers. Spill files are created
via `newSpillWriter(os.TempDir())` (`operators.go:797`); file naming must be
verified collision-free under concurrency —
[07](07-parallel-hash-join.md) §5 covers the same concern for the join's
bounded drain, and [09](09-verification-and-measurement.md) makes it a test.

## 5. Statistics and EXPLAIN

`Workers Planned:` is plan-time and belongs in `emitNodeDetailLines`
(`internal/executor/operators_explain.go:775`) so it renders in plain EXPLAIN,
matching PG.

`Workers Launched:` is execution-time and follows the **Memoize counter
pattern** exactly: a `map[*planner.Gather]*GatherStats` threaded as a new
parameter through `walkPlanAnalyze` / `walkPlanAnalyzeFiltered`
(`operators_explain.go:805,811`), emitted next to the Memoize block at
`:862-870`, with the stats struct registered on the executor context as
`MemoizeStats` is (`context.go:181`).

Launched may be fewer than planned when the cluster-wide worker budget is
exhausted — see [08](08-planner-integration.md) §4. Reporting the difference is
the point of having two numbers.

Per-worker instrumentation merges at join time
([03](03-concurrency-substrate.md) §6.4), which is what makes per-node `loops=`
and row counts meaningful under parallelism.

### 5.1 `loops=` semantics under workers

This must be specified explicitly or every parallel `EXPLAIN ANALYZE` row count
will be wrong by a factor of N, and plan-gate snapshots will bake the error in.

PG's convention: a node's reported `actual rows` is the **per-loop average**,
and each worker's execution of a node counts as a loop. So for a node below a
Gather run by L executors (leader + workers), `loops = L` and the true total is
`rows × loops`. A reader who sums the reported `rows` across workers
double-counts; a reader who takes it as the total under-counts by L.

goopg must reproduce that convention rather than reporting per-worker totals,
because EXPLAIN output in this project is compared against PG's.

## 6. Divergence from PostgreSQL

| PG | goopg | Cost |
| --- | --- | --- |
| One `shm_mq` per worker, fixed 64 KiB (`execParallel.c:69`), tuples serialised in and out | One buffered Go channel of `[]Row` batches | No encode/decode; but the ownership guarantee `shm_mq` gave for free must be produced deliberately ([03](03-concurrency-substrate.md) §3) |
| Round-robin over `TupleQueueReader`s, exhausted readers `memmove`d out (`nodeGather.c:332-349`) | Receive from one shared channel; workers simply stop sending | Simpler; loses per-worker fairness control, which nothing needs |
| Worker startup: fork + DSM attach + state restore | `go` statement | Startup cost falls from milliseconds to microseconds — the main reason PG's `parallel_setup_cost = 1000` does not describe goopg ([08](08-planner-integration.md) §3) |
| Gather Merge maintains a binary heap over readers (`nodeGatherMerge.c`) | Same algorithm, reusing the existing `sortHeap` (`operators.go:953`) | None — the primitive was already there for external sort |
| Worker crash surfaces as an error through the queue | Panic must be recovered per worker or the process dies ([03](03-concurrency-substrate.md) §4.3) | Requires explicit `recover()`; blast radius then matches PG |

The structural simplification is large: PG's Gather is substantially a
transport implementation, and goopg's is a fan-out with a shutdown discipline.
The risk correspondingly moves from "is the transport correct" to "is the
shutdown correct", which is why §3's three details and the leak test in
[09](09-verification-and-measurement.md) carry the weight here.
