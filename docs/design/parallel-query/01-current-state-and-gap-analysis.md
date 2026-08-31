# 01 — Current State and Concurrency Hazard Inventory

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| surveyed at | `66c3c482` |

This chapter establishes what already works under concurrency, what does not,
and why the gap is narrower than it looks. All line references were verified at
the commit above; treat them as pointers, not as guarantees against drift.

## 1. The headline: the shared subsystems are already concurrent

goopg was built with one goroutine per connection, so every subsystem shared
*between backends* had to be made thread-safe years before anyone considered
intra-query parallelism. That work transfers wholesale. A set of worker
goroutines running inside one transaction touches almost exactly the same
shared state that two concurrent sessions already do.

| Subsystem | Verdict | Evidence |
| --- | --- | --- |
| Buffer pool | **SAFE** | `internal/storage/bufpool.go:105` documents "It is goroutine-safe". Lock-free pin/unpin via a packed 64-bit state word (`bufpool.go:65`) with generation checks defeating ABA; per-page content latch `contentMu sync.RWMutex` (`:81`) taken without any global lock; lock-free open-addressing buf-mapping table (`bufmap.go:53`, `Lookup` is atomic-loads-only per `bufmap.go:48`). Two goroutines pinning the same page is normal and yields the same `*Slot`. |
| B-tree read path | **SAFE** | `internal/access/btree/btree.go:609-625` states the contract: readers take no tree-wide lock, synchronising through per-page `Slot.RLock()` latches with Lehman-Yao right-link recovery. `rightmostLeafBlk` is `atomic.Uint64` (`:661`) precisely so concurrent readers cannot tear. Designed in [`0002-0002-btree-concurrency.md`](../0000-0049/0002-0002-btree-concurrency.md). |
| MVCC snapshot | **SAFE** | `mvcc.Snapshot` is a plain value struct (`internal/mvcc/snapshot.go:69-91`) whose XID arrays are documented immutable after capture (`:114-115`). Held **by value** on the executor context (`internal/executor/context.go:60`), so copying a context per worker copies the snapshot correctly and cheaply. |
| Visibility checking | **SAFE** | `mvcc.TupleVisible` (`internal/mvcc/visibility.go:25`) reads only the tuple header, the snapshot value and the multixact store. **No per-check memoization anywhere** — the classic parallel-query hazard is simply absent. |
| Catalog reads | **SAFE** | `sync.RWMutex`-guarded (`internal/catalog/catalog.go:2110`); `LookupTable` (`:11799`) holds `RLock` across the whole resolution. The mutation-on-read hazard was already designed out: the pure reader `ns()` (`:3511`) is separate from `getOrCreateNS` (`:3526`), whose contract comment demands the write lock. |
| CLOG / multixact | **SAFE but serialising** | CLOG LRU mutates on read (pin/evict) but under `mu sync.Mutex` (`internal/mvcc/clog_bufferpool.go:133,293`); multixact `Members()` locks and returns a defensive copy (`internal/multixact/store.go:112`). Correct — but one global mutex on a per-tuple path. Mitigated by the common case never reaching it (`snapshot.go:228-237`). |

Two subsystems are conditionally safe and need a policy, not an
implementation:

| Subsystem | Verdict | Evidence |
| --- | --- | --- |
| `lockmgr` | acquire **SAFE**, release **UNSAFE** | Holders are a *bitmask*, not a refcount (`internal/lockmgr/lockmgr.go:199`). Acquire is idempotent and self-conflict-free (`:419-430`), so N workers sharing a backend ID cannot double-grant. But `Release` clears the bit outright (`:557`) — **the first worker to release drops the lock for the entire transaction**. Policy: workers acquire (or, better, never need to); only the leader releases. |
| Transaction state | **SAFE to read** | `mvcc.Transaction` is a small value struct (`internal/mvcc/manager.go:42-56`); `Manager` is documented safe for concurrent use (`:58-60`). Workers need only `XID`, `Handle`, `Isolation` and the snapshot, all value-copyable. What they must never do is listed in §3. |

**A pleasant absence:** there is no command counter. No `Cmin`/`Cmax`/
`CommandId` exists in `internal/storage/heap.go` or `internal/mvcc/`. PG spends
real machinery on propagating and freezing the command counter across workers
(`GetCurrentCommandId`, and the rule that workers cannot advance it); that
entire concern is out of scope here by construction. This should be
re-confirmed before relying on it, since its absence is inferred from a search
rather than from a design statement.

## 2. What does not exist

Nothing about parallel execution exists today. Specifically:

- **No plan node**: no `Gather`, `GatherMerge`, `Append`, `MergeAppend`, or
  `Materialize` in `internal/planner/plan.go`. Grepping `parallel|Gather|Worker`
  across `internal/planner` returns only unrelated uses of the English words —
  the namespace is entirely free. Two comments (`unnest.go:667,768`) note that
  PG "also has index-driven and parallel semi joins; goopg does not yet."
- **No parallel execution machinery**: the only `go func` in
  `internal/executor` is test scaffolding (`spec_insert_registry.go:94`). There
  is no worker pool, no `errgroup`, no fan-out anywhere in
  `internal/storage`, `internal/vacuum`, `internal/access/btree`, or
  `internal/autovacuum` either. This is greenfield: every concurrency guarantee
  the engine has was built for *cross-session* safety, so there is no
  leader/worker lifecycle, error-propagation, or cancellation precedent to
  copy.
- **No partial/final aggregate split**: `planner.Aggregate` (`plan.go:885`) has
  no mode field, and built-in aggregates go transition → final directly.
- **No absolute node costs**: see §4.

## 3. The eleven blockers

These are the actual work items. None is in a shared subsystem; all are in
per-operator scratch state, per-session identity, or missing plumbing.

### B1 — `mctx` is single-owner by contract *(highest severity)*

`internal/mctx/mctx.go:61-63` states it outright: *"Each backend owns its own
context tree exclusively; concurrent access is a contract violation."* `Alloc`
(`:254`) is an unsynchronised bump pointer mutating `c.chunks`/`c.head`. The
package-level `ctxMu` (`:82`) guards a global registry, **not** per-context
allocation.

Worse, the reset point collides exactly with the parallel work unit:
`seqScanOp.sctx` is a per-page arena whose `Reset()` fires **at each block
boundary** (`internal/executor/operators_storage.go:794-798`, reset at
`:1727-1731`) — which is precisely where a parallel block cursor would hand a
worker its next unit of work. Its own doc says consumers retaining rows past
the boundary must call `slot.Materialize()`.

Consequence: arena-backed Datums (`KindString`/`KindBytes` with `ArenaID != 0`,
`internal/executor/datum.go:109`) that cross a channel are unsound unless
promoted. Owned by [03](03-concurrency-substrate.md).

Secondary hazard: `mctx.Acquire` appends to `parent.children` without
synchronisation (`mctx.go:180`), so **workers cannot allocate their own
contexts concurrently** — the leader must pre-allocate them.

Scalability note: `mctx.Lookup` (`:110-121`) takes the global `ctxMu` on every
arena-string dereference. Under N workers this is a process-wide serialisation
point on a hot path, and is worth a lock-free registry before parallelism buys
much.

### B2 — slot aliasing is pervasive

The governing contract (`internal/executor/slot.go:88-108`) is that a slot is
valid only until the next `Next()` unless materialised. Operators that return
slots aliasing reused internal buffers include `projectOp`
(`operators.go:360-370`, with an explicit audited-consumer list and design doc
`0092-0002-projectop-slot-aliasing.md`), `seqScanOp`
(`operators_storage.go:789,1693-1706`), `indexScanOp` (`operators_index.go:186-188`),
`indexOnlyScanOp`, `nestedLoopIndexJoinOp` (`operators_nljoin.go:27-32`), and
Phase C's reused `dst *Slot` (`opnode.go:642-643`). Pass-through operators
(`filterOp`, `limitOp`, `instrumentedOp`) inherit their child's aliasing.

Crossing a goroutine boundary therefore requires `Materialize()` /
`cloneRowOwned` (`datum.go:408-434`), which promotes arena payloads into freshly
allocated buffers. It must **not** use `cloneRow` (`datum.go:859-867`) or
`Slot.CopyTo` (`opnode.go:83-94`) — both are shallow and preserve `ArenaID`,
leaving the receiver pointing into memory the producer will recycle.

### B3 — `*executor.Context` mixes shared and per-statement state

`internal/executor/context.go:26` is a ~700-line struct passed to every `Open`.
Effectively immutable during execution and safe to share: `Pool`, `Catalog`,
`TxnMgr`, `Tx`, `Snap`, `MultiXact`, `WorkMem`, `FSM`/`VM`, `BackendID`,
`Params`, `Now`.

Mutable per-statement state that concurrent workers would corrupt includes
`OuterRows` (`:98`, a push/pop stack for correlated scopes), `ParamExec`/
`ParamSet`/`ParamDirty` (`:137-139`, grown lazily by `SetParamExec` at `:763`),
`subqCacheSafe`/`subqCacheScoped`/`subqCacheScope` (`:120-123`), `CorrSubqOps`
(`:147` — caches *pre-opened operators*, so sharing would also share cursor
state), `CorrSubqHashMaps` (`:155`), `SubPlanHandles` (`:163`), `SubPlanStats`
(`:176`, whose own comment says "Written on the single statement-executing
goroutine … no synchronisation"), `MemoizeStats` (`:181`), `CTERowCache`
(`:467`), `MaterializedCTEs` (`:461`), `WorkTableRows` (`:251`), and `Notices`
(`:337`, appended by `AddNotice` at `:828` with no lock).

Additionally ~40 callback fields (`GetSetting`, `SetSetting`, `CancelBackend`,
`QueueNotify`, …) close over per-connection state. In PG these are exactly the
things that make a function `parallel unsafe`; goopg needs the equivalent gate.

### B4 — instrumentation is a package global

`var instrumentScope *instrumenter` (`internal/executor/instrument.go:215`)
over `nodeStatsTable map[planner.Node]*nodeStats` (`:202`), set around a build
by `withInstrumentation` (`:225-228`). Under real worker goroutines this is a
guaranteed data race, and `race-gate` will say so. It is also the thing that
must be fixed *correctly* rather than merely locked, because per-worker
statistics that are merged at the Gather boundary are what make
`Workers Launched:` and per-worker row counts meaningful.

### B5 — `kvcache.Budget` has no mutex

`internal/executor/kvcache/kvcache.go:20-52`: `Reserve`/`Release` are plain
`b.used += n`. The package exposes `NewShared(b *Budget)` (`:78`) so several
caches can share one budget — a shape that becomes racy the moment two
goroutines use it.

### B6 — `ScanRing` has zero synchronisation

`internal/storage/scan_ring.go:23-37`: `bufs`, `bufHead`, `activeSlot`,
`activePage` are plain fields. Strictly per-scan private, so each worker needs
its own ring — which in turn makes the activation heuristic
(`nBlocks > pool.Capacity()/4`, `operators_storage.go:772`) wrong under N
workers, since N rings consume N times the private buffers.

### B7 — `Unpin` panics on underflow

`internal/storage/bufpool.go:1918-1930`. With a shared pin count and N workers, any
imbalance **crashes the process** rather than leaking a pin. Pin accounting is
therefore a correctness cliff and deserves a debug-build assertion early,
before the bugs get subtle.

### B8 — no per-session GUC path into the planner

`planner.Plan(stmt, cat)` (`internal/planner/planner.go:89`) takes no settings,
and `executor.Context` carries no GUC handle. The existing GUC→planner bridge
(`Registry.OnChange`, `internal/config/guc.go:423`, wired at
`cmd/goopg/main.go:397,403`) sets a **process-global** `atomic.Bool` — adequate
for a boolean kill switch, but wrong for `max_parallel_workers_per_gather`,
which is a per-session integer the planner must read. New plumbing is required;
[08](08-planner-integration.md) specifies it.

### B9 — no error-aggregation or panic-recovery primitive

The executor propagates errors purely by return value up the `Next()` chain.
`ExecError` (`internal/executor/expr.go:43-51`) is a plain value type with no
`Unwrap`, so "first error wins" must be made explicit. There is no `errgroup`
or error-channel pattern anywhere to copy. Worker panics need the same
`recover()` treatment `serveConn` has (`internal/server/server.go:779-792`);
without it one worker panic kills the process instead of the query.

### B10 — there are two build paths, and the live one is not `Build`

`Build` (`internal/executor/executor.go:22-330`) is the interface-dispatch
tree; `BuildFast` (`:545`) produces a slab of `OpNode` driven by
a concrete-dispatch switch (`opnode.go:643-687`). **The live server path is
`BuildFastIterator`** (`opnode.go:393`, called from
`internal/server/dispatch.go:2650,3502`). Only nine kinds are migrated to
concrete dispatch; everything else falls back through `OpAdapter`. A Gather
that lands only in `Build` would be dead code in production.

### B11 — SSI writes on the read path

Under SERIALIZABLE the scan takes SIREAD predicate locks — genuine writes —
via `ssiRecordInvisibleTupleRead` (`internal/executor/ssi.go:455`) and the
visible-tuple hook (`operators_storage.go:1541-1547`), funnelling through the
manager's `ssiMu` (`internal/mvcc/manager.go:136`). The gist/gin predicate-lock
paths (`operators_storage.go:726-753`) are in the same category. This is why
SERIALIZABLE is refused in v1.

## 4. There is no cost model to hang parallel costing on

EXPLAIN's cost column is a placeholder. Both emission sites hardcode it:
`internal/executor/operators_explain.go:378` and `:836` format
`(cost=0.00..0.00 rows=%d width=0)`. Only `rows=` is real (via
`planner.EstimateRows`); `cost=` and `width=` are fiction.

A real but *coarse and relative* cost model does exist underneath, and it is
worth understanding because the parallel gate should look like it, not like PG:

- Cardinality: `internal/planner/cardinality.go:38` `EstimateRows`, with
  selectivity constants at `:25-29`. Returns **0 meaning "no estimate"**, and
  callers are documented to skip cost decisions on 0 (`:32-34`).
- Join algorithm choice: `internal/planner/joincost.go:19` — explicitly
  unit-row, "no page / IO weighting in v0" (`:41`).
- Bushy join-order DP: `internal/planner/bushy.go:785` with integer weights at
  `:760-762`.
- SubPlan cost: `internal/planner/subplan_cost.go:29`, whose doc (`:5-28`) is
  the house philosophy — *"The ONLY job of this number is ordering safety …
  Precision beyond that is explicitly a non-goal."*

There is no `(startup_cost, total_cost)` pair on any node. Consequently PG's
parallel arithmetic — compare a partial path's `total_cost + parallel_setup_cost
+ rows * parallel_tuple_cost` against the serial path's `total_cost` — **cannot
be reproduced**: there is no `total_cost` to add to. [08](08-planner-integration.md)
specifies the stats-gated size rule used instead, which is faithful to how PG
actually chooses the worker *count*.

## 5. Parallel GUC audit — all registered, all inert, two wrong

Registered explicitly in `internal/config/defaults.go`:

| GUC | line | type / boot value |
| --- | --- | --- |
| `max_parallel_workers_per_gather` | `:602-608` | Int, `2` |
| `max_parallel_maintenance_workers` | `:609-614` | Int, `2` |
| `min_parallel_table_scan_size` | `:615-621` | Int, `UnitKB`, `8388608` |
| `min_parallel_index_scan_size` | `:622-628` | Int, `UnitKB`, `524288` |
| `parallel_setup_cost` | `:703-709` | Real, `1000` |
| `parallel_tuple_cost` | `:710-716` | Real, `0.1` |
| `debug_parallel_query` | `:723-729` | Enum `off/on/regress`, `off` |
| `parallel_leader_participation` | `:1008-1012` | Bool, `on` |
| `enable_parallel_hash`, `enable_parallel_append`, `enable_gathermerge` | `:909-910` | Bool `on`, inside the no-op loop |

Grepping every one of these outside `internal/config/` finds only a static
`pg_settings` row and comments. **Zero reads in `internal/planner` or
`internal/executor`.** `unimplemented_feat.json:2025-2043` records the same
conclusion independently.

### 5.1 Two fidelity bugs

`min_parallel_table_scan_size` is registered as `UnitKB` with boot value
`8388608`. PostgreSQL declares it `GUC_UNIT_BLOCKS` with default
`(8 * 1024 * 1024) / BLCKSZ` = 1024 blocks
(`postgres/src/backend/utils/misc/guc_tables.c:3727-3734`), which `SHOW`
renders as **`8MB`**. goopg's registration renders as **`8GB`** — 1024× too
large. `min_parallel_index_scan_size` has the identical error: `524288` KB =
512 MB where PG shows `512kB`.

Both boot values are correct as *byte* counts (8388608 B = 8 MB;
524288 B = 512 kB) and were evidently mislabelled as KB. goopg has no
`UnitBlocks` at all (`internal/config/guc.go:77`), which is the underlying
cause; PG's unit is blocks, so faithful rendering needs either a new unit or a
converted-to-KB boot value (`8192` and `512` respectively).

These are observable through `SHOW` today, independently of parallelism, so
they are worth fixing regardless of whether this bundle is implemented.

### 5.2 One GUC missing entirely

`max_parallel_workers` — PG's cluster-wide cap on concurrently active workers,
distinct from the per-Gather cap — is not registered at all. It matters more
here than in PG: goroutines are cheap enough that without a global cap, a
handful of concurrent sessions each planning 4 workers can oversubscribe the
machine in a way PG's process-slot accounting would have prevented.

Also note `parallel_workers` as a per-table reloption *is* parsed and stored
(`internal/executor/operators_ddl.go:2125-2145`) but never read — the comment
at `:2128` says so. PG consults it first in `compute_parallel_worker()`, so
honouring it is nearly free once the rule exists.

## 6. Assets worth reusing

The engine already contains several pieces built for, or trivially adaptable
to, this work:

- **`CombineFunc`** on `catalog.UserAggregate` (`internal/catalog/catalog.go:3129`)
  is commented literally "combine function name for **parallel agg**". It is
  parsed by `CREATE AGGREGATE … COMBINEFUNC` (`internal/parser/ddl.go:1388`)
  and already invoked (`internal/executor/operators_join_agg.go:2534-2543`) —
  in the degenerate single-partial case PG also exercises.
- **`avg` already accumulates as a (sum, count) pair** — `sum` and `avg` share
  one transition case and diverge only in `finishAgg`. The partial state PG has
  to synthesise exists here for free.
- **`sortHeap`** (`internal/executor/operators.go:953`) is exactly the
  order-preserving merge primitive Gather Merge needs, with `lessRows` (`:747`)
  as a standalone comparator.
- **The Memoize ANALYZE-counter pattern** (`operators_memoize.go:34` →
  `operators_explain.go:862-870`) is a complete worked example of threading a
  per-node stats map through the EXPLAIN walk — the template for
  `Workers Launched:`.
- **`debug_parallel_query`** is registered and enum-validated, with tests
  (`internal/config/debug_parallel_query_test.go`). It is PG's own lever for
  forcing parallel plans in testing, and [09](09-verification-and-measurement.md)
  builds the correctness gate on it.
- **Per-P sharded counters** — `internal/stats/counter.go` with
  `runtimeshim.PinP`, cache-line padded, designed in
  [`perf-optimize/08-runtime-internals.md`](../perf-optimize/08-runtime-internals.md)
  §4. Contention-free worker statistics without inventing anything.
- **External sort with N-way merge** (`operators.go:640,792,953`) already
  exists, so a parallel sort feeding Gather Merge is not starting from zero.

## 7. Divergence from PostgreSQL

This chapter is a survey, so its only divergence is one of *starting position*:
PG's equivalent groundwork is dominated by making state reachable across
process boundaries (dynamic shared memory, `shm_mq`, serialising the snapshot,
the GUC state, the libraries, the reindex state, …). Almost none of that
applies here. What goopg pays instead is the cost PG never has to pay: it must
prove the absence of data races in a shared address space, which is why
`race-gate` is promoted to an acceptance criterion in
[09](09-verification-and-measurement.md) rather than left as a periodic check.

The net is favourable. Of the eleven blockers above, seven (B2–B8, excluding
B6) are bounded, local changes to per-operator scratch; two (B9, B10) are
missing plumbing; B1 is the one that requires genuine design thought, and B11
is avoided by scope.
