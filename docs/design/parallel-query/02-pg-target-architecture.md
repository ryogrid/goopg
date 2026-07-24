# 02 — PostgreSQL's Parallel Query Architecture (Oracle Reference)

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| oracle | PostgreSQL 18.3, `postgres/` submodule (read-only) |

This chapter records what PostgreSQL does, so the rest of the bundle can say
"as PG does" or "unlike PG" precisely. It closes with the fidelity matrix that
governs every later decision: which of PG's behaviours goopg must reproduce,
and which are artefacts of process isolation that goopg should not.

Primary sources: `postgres/src/backend/access/transam/README.parallel`,
`access/transam/parallel.c`, `executor/execParallel.c`, `executor/nodeGather.c`,
`executor/nodeGatherMerge.c`, `optimizer/path/allpaths.c`,
`optimizer/plan/planner.c`.

## 1. The shape of a PG parallel query

A parallel plan is an ordinary serial plan with a **`Gather`** (or
**`Gather Merge`**) node inserted at a chosen point. Everything *below* that
node is a *partial path*: a plan that, when run by several workers
simultaneously, collectively produces the full result exactly once. Everything
*above* it is ordinary serial execution in the leader.

The partial-ness is a property of the scan at the bottom. A `Parallel Seq Scan`
does not scan the whole relation in each worker — workers draw block ranges
from one shared cursor, so their union is the relation. Nodes above it inherit
partial-ness: a `Partial Aggregate` over a partial scan produces per-worker
partial aggregate states, which `Finalize Aggregate` above the Gather combines.

The canonical TPC-H shapes, from the reference capture:

```
Finalize GroupAggregate
  -> Gather Merge
       Workers Planned: 2
       -> Partial GroupAggregate
            -> Sort
                 -> Parallel Seq Scan on lineitem
```

## 2. Worker lifecycle and state sharing

`README.parallel` "Overview": the initiating backend (the *leader*) creates a
**dynamic shared memory segment** and a `ParallelContext`, then launches
workers which attach to that segment.

Because workers are separate *processes*, none of the leader's memory is
visible to them. PG's response is explicitly pragmatic
(`README.parallel` "State Sharing"):

> There's no general mechanism for ensuring that every global variable in the
> worker will have the same value that it does in the initiating backend […]
> Instead, we take a more pragmatic approach. First, we try to make as many of
> the operations that are safe outside of parallel mode work correctly in
> parallel mode as well. Second, we try to prohibit common unsafe operations
> via suitable error checks.

So PG *copies* the important state into the DSM segment for workers to restore:
the transaction snapshot and active snapshot, the transaction state, combo CIDs,
GUC values, the current user/role and security context, the libraries loaded,
`pg_class` relmapper state, and the reindex state. This is a large, ongoing
maintenance surface, and every new piece of backend-global state is a candidate
to be added to it.

**The single most consequential restriction** (`README.parallel`, same section):

> The most significant restriction imposed by parallel mode is that all
> operations must be strictly read-only; we allow no writes to the database and
> no DDL.

It is enforced by `EnterParallelMode()` / `ExitParallelMode()`, which arm and
disarm the error checks. PG is explicit that these checks catch "100% of unsafe
things that a user might do from the SQL interface" but cannot catch unsafe C
code.

Transaction integration (`README.parallel` "Transaction Integration"): workers
are given a transaction state that makes `TransactionIdIsCurrentTransactionId`
and friends answer as they would in the leader, with the block state set to
`TBLOCK_PARALLEL_INPROGRESS` so it is not mistaken for an ordinary transaction.
No meaningful change to transaction state may be made while in parallel mode.

## 3. Tuple transport

Workers return tuples to the leader through **`shm_mq`** — a shared-memory
ring queue, one per worker, each `PARALLEL_TUPLE_QUEUE_SIZE = 65536` bytes
(`executor/execParallel.c:69`, allocated at `:568-580`). Tuples are serialised
into the queue by the worker and deserialised by the leader's
`TupleQueueReader`.

`nodeGather.c` reads round-robin across readers (`nextreader`, `:209,332-333`),
removing exhausted readers from the array by `memmove` (`:349`). The leader
also participates in execution itself unless `parallel_leader_participation`
is off or the plan is `single_copy` (`nodeGather.c:72,214`).

`Gather Merge` (`nodeGatherMerge.c`) additionally maintains ordering: each
worker produces a sorted stream and the leader merges them with a binary heap,
which is why the node exists separately rather than Gather sorting afterwards.

## 4. Choosing the worker count

This is the part goopg *can* reproduce faithfully, because it is a rule over
relation size rather than a cost comparison.

`compute_parallel_worker()` (`optimizer/path/allpaths.c:4274`):

1. If the table's `parallel_workers` reloption is set, use it verbatim.
2. Otherwise, if the relation is a base relation and
   `heap_pages < min_parallel_table_scan_size` (or the index equivalent),
   **return 0** — no parallel path.
3. Otherwise start at 1 worker and add one worker every time the page count
   passes another factor of **three**:

   ```c
   heap_parallel_threshold = Max(min_parallel_table_scan_size, 1);
   while (heap_pages >= (BlockNumber) (heap_parallel_threshold * 3))
   {
       heap_parallel_workers++;
       heap_parallel_threshold *= 3;
       ...
   }
   ```

   The comment is candid: *"This probably needs to be a good deal more
   sophisticated, but we need something here for now."*
4. The result is then clamped by `max_parallel_workers_per_gather`, and at
   execution time by the cluster-wide `max_parallel_workers` pool.

With the default `min_parallel_table_scan_size` of 1024 blocks (8 MB), the
ladder is: ≥8 MB → 1 worker, ≥24 MB → 2, ≥72 MB → 3, ≥216 MB → 4, and so on.

## 5. Costing the choice

Separately from the worker count, PG decides *whether* to use the partial path
by comparing costs. A Gather's cost is the partial path's total cost plus
`parallel_setup_cost` (default 1000, a deliberately large constant reflecting
process fork and DSM setup) plus `rows * parallel_tuple_cost` (default 0.1, the
per-tuple transport charge). The partial path's own cost is divided among the
workers.

Both constants encode *process* overheads: forking a backend and pushing tuples
through shared memory. Neither corresponds to anything goopg pays — see §7.

## 6. Parallel safety

Every function carries `proparallel` in `pg_proc`: `'s'` safe, `'r'`
restricted, `'u'` unsafe (default). Safe functions may run in a worker;
restricted functions may run in the leader but not a worker; unsafe functions
prevent the plan from being parallel at all. Aggregates additionally need
`aggcombinefn` to be usable in a partial/final split, and `aggserialfn` /
`aggdeserialfn` when the transition state is `internal`.

## 7. Fidelity matrix

The line goopg draws between "reproduce" and "diverge".

### 7.1 Reproduce — observable behaviour

| PG behaviour | goopg obligation |
| --- | --- |
| Result sets identical to the serial plan | Absolute. Enforced by the identity gate in [09](09-verification-and-measurement.md). |
| `Gather`, `Gather Merge`, `Parallel Seq Scan`, `Partial`/`Finalize` aggregate node labels | Match exactly; EXPLAIN is compared against PG in this project. |
| `Workers Planned:` / `Workers Launched:` detail lines | Match, including that Planned is plan-time and Launched is ANALYZE-time. |
| GUC names, contexts, units and defaults | Match. Two are currently wrong — see [01](01-current-state-and-gap-analysis.md) §5.1. |
| Worker-count rule (`compute_parallel_worker`) | Reproduce exactly, including the `parallel_workers` reloption precedence and the ×3 ladder. |
| `parallel_leader_participation` semantics | Reproduce. |
| `debug_parallel_query` forcing behaviour | Reproduce; it is the test lever. |
| Read-only restriction; no DDL in parallel mode | Reproduce as policy, even though goopg *could* technically permit more. |
| `proparallel` gating | Reproduce; the marker is already stored. |
| Aggregates need a combine function to split | Reproduce via the existing `COMBINEFUNC`. |

### 7.2 Diverge — mechanism, not meaning

| PG mechanism | Why it exists | goopg instead |
| --- | --- | --- |
| Dynamic shared memory segment | Workers are separate processes with disjoint address spaces | Nothing. Workers are goroutines; the plan, catalog, buffer pool and snapshot are simply reachable. |
| Copying snapshot / GUC / transaction state into DSM | Same | Nothing. `mvcc.Snapshot` is an immutable value struct already held by value on the executor context. |
| `shm_mq` tuple queues with serialise/deserialise | Same | A Go channel of already-materialised row batches. No encoding step exists. |
| `aggserialfn` / `aggdeserialfn` for `internal` transition states | Transition state must survive a process boundary | **Not needed at all.** `aggRuntime` values are handed across a channel directly. |
| `parallel_setup_cost = 1000` | fork + DSM setup is genuinely expensive | Retained as a GUC for compatibility, but a goroutine costs ~µs to start. The profitable-parallelism threshold is far lower; [08](08-planner-integration.md) discusses whether to honour the constant or the reality. |
| Shared hash table needs DSA + `Parallel Hash` machinery | Building a hash table in shared memory across processes is hard | A `map[string][]Row` published after a barrier is safe for concurrent reads. See [07](07-parallel-hash-join.md). |
| Per-worker instrumentation shipped back through DSM | Same | Per-worker stats structs merged at the Gather boundary in memory. |

### 7.3 The cost of diverging

PG's process model makes data races *structurally impossible* between
backends; goopg's does not. Everything in §7.2 that says "simply reachable" is
also "simply corruptible". That is why [03](03-concurrency-substrate.md)
defines an explicit ownership and lifetime contract rather than relying on the
address space being shared, and why `race-gate` is an acceptance criterion
rather than a periodic check.

There is also a subtler cost. PG's serialisation boundary is an accidental
correctness *guarantee*: a tuple that survives `shm_mq` is by construction a
self-contained copy. goopg has no such boundary, so the equivalent guarantee
must be produced deliberately — every row crossing a channel must be
materialised, and nothing enforces that but review and tests. This is the
single most likely source of a subtle bug in the whole design, and
[03](03-concurrency-substrate.md) §3 treats it as such.

## 8. What PG does that this bundle explicitly does not attempt

- **Parallel Index Scan / Parallel Bitmap Heap Scan.** PG has both; the TPC-H
  reference set uses neither (0 occurrences). Out of scope.
- **Parallel Append.** Requires partitioning work goopg does not have.
- **Parallel maintenance** (`CREATE INDEX`, `VACUUM`). Out of scope by user
  decision; `max_parallel_maintenance_workers` stays inert.
- **Parallel DML** (PG's limited parallel-safe insert paths). Out of scope; the
  read-only restriction is absolute in v1.
- **Parallel query under SERIALIZABLE.** PG gained this in v12 with real
  machinery; goopg refuses it in v1 for the reasons in
  [01](01-current-state-and-gap-analysis.md) §3 B11.
