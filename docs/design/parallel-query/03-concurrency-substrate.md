# 03 — Execution Substrate: Ownership, Lifetime, Failure

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | [01](01-current-state-and-gap-analysis.md) hazard inventory |
| consumed by | [04](04-parallel-scan.md), [05](05-gather-and-gather-merge.md), [06](06-parallel-aggregation.md), [07](07-parallel-hash-join.md) |

This is the load-bearing chapter. Chapters 04–07 describe *what* runs in
parallel; this one defines the rules that make any of it sound. Reviewing those
chapters without this one will produce designs that look correct and are not.

Four contracts are specified here, in decreasing order of how easy they are to
get wrong:

1. **Tuple ownership** — what a worker may hand to the leader (§3).
2. **Context partitioning** — what state is shared and what is per-worker (§2).
3. **Failure and cancellation** — how one worker's error stops the rest (§4).
4. **Lifecycle and resource discipline** — arenas, pins, locks (§5, §6).

## 1. The execution model

```
                 leader goroutine
                 ┌──────────────────────────┐
                 │  … serial plan above …   │
                 │        Gather            │
                 └────────────┬─────────────┘
                              │  chan []Row (materialised batches)
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
   worker 0              worker 1              leader-as-worker
   own opTree            own opTree            own opTree
   own workerCtx         own workerCtx         own workerCtx
   own mctx child        own mctx child        own mctx child
        └─────────────────────┼─────────────────────┘
                              │
                    shared, read-only:
              plan tree · catalog · buffer pool
              snapshot · transaction identity
              (and, for hash join, the built table)
```

Each worker runs **its own operator tree**, built independently from the same
plan. This is already sound: `Build` (`internal/executor/executor.go:21`) and
`BuildFast` (`:545`) are pure functions of the plan node — they construct fresh
operator instances and never mutate the plan. N calls give N independent trees
sharing read-only plan nodes.

The leader participates as a worker by default (PG's
`parallel_leader_participation`), which is why it appears above as a peer
rather than as a pure consumer.

### Divergence from PostgreSQL

PG must serialise the plan into DSM and have each worker *reconstruct* an
executor tree from it. goopg's workers share the plan tree by pointer. The
requirement this creates — and it is a real one — is that **plan nodes must be
immutable during execution**. That is true today, but not enforced. Two known
passes mutate plan nodes at *build* time (`tryBuildNLI`'s tentative
`Filter`-inner unwrap, and Memoize attaching to `NestedLoopIndexJoin.Inner`),
both during planning, before any worker exists. The design rule is: **no
operator may write through its `*planner.Node` pointer after `Open`**. This
should be spot-checked during implementation rather than assumed.

## 2. Context partitioning

`*executor.Context` (`internal/executor/context.go:26`) is passed to every
`Open` and mixes three kinds of field. The split is by *field*, not by
introducing a new type hierarchy — a full refactor of a 700-line struct used
everywhere is a much larger change than this feature needs.

### 2.1 Shared, read-only — one instance, referenced by all workers

`Pool`, `Catalog`, `PlanCatalog`, `TxnMgr`, `Tx`, `Snap`, `MultiXact`,
`LockMgr`, `BackendID`, `TxnLockBackendID`, `ProcNum`, `WorkMem`, `FSM`, `VM`,
`Params`, `Now`, `MaxRows`, `StatsTarget`, `DataDir`, `CurrentDatabase`,
`CurrentDatabaseOid`, `IsStandby`.

All are either immutable for the statement's duration or guarded internally.

**The snapshot needs a more careful argument than "it is a value struct".**
`mvcc.Snapshot` (`internal/mvcc/snapshot.go:69-91`) holds two slices
(`InProgress`, `Aborted`) and a `*CLog`, so a copy is shallow and aliases the
manager's arrays — which is precisely why `Clone()` exists at `:94`. Sharing is
nonetheless sound because **the backing arrays are never mutated after
capture** (`snapshot.go:114-115` states this, and no append or index-write to
them exists outside tests). That is the invariant to preserve: if any future
change mutates a captured snapshot in place, sharing it across workers becomes
unsound and `Clone()` becomes mandatory at fan-out.

The `*CLog` reference is a different matter and is **not** covered by that
invariant — see §6.5.

### 2.2 Per-worker, COPIED BY VALUE at fan-out

These carry live values the worker's subtree will *read*. Cloning them empty
breaks correctness.

`OuterRows`, `ParamExec` / `ParamSet` / `ParamDirty`.

`ExecParamRef` evaluation raises `XX000 "SubPlan parameter $N read before
assignment"` when the slot is unset (`internal/executor/expr.go:390-392`), and
the slot is written by the *enclosing* sublink's eval site before the inner
plan runs (`bindSubPlanParams`, `expr.go:6716-6729`). A Gather placed anywhere
below a param-consuming node with an empty `ParamExec` therefore fails at that
read. `OuterRows` has the identical problem for LATERAL and correlated
references.

Copying by value at fan-out is the rule: each worker gets a snapshot of the
slots as they stood when the Gather opened. That is sound because the values
are bound *above* the Gather and are constant for the duration of the partial
subtree's execution.

This is also why [08](08-planner-integration.md) §1's partial-terminating list
must include **the inner side of any correlated SubPlan or LATERAL join** — a
Gather there would need slots rebound per outer row, which no snapshot can
provide.

### 2.3 Per-worker — created empty in each worker's context

`subqBudget`, `subqCacheSafe`, `subqCacheScoped`, `subqCacheScope`,
`CorrSubqOps`, `CorrSubqHashMaps`, `SubPlanHandles`, `SubPlanStats`,
`MemoizeStats`, `MultiAssignSubqCache`, `Mctx`, `Ctx`, `Notices`,
`NoticesWithDetail`, `Warnings`, `MergeAction` / `MergeOldRow` / `MergeNewRow`,
`CTERowCache`, `MaterializedCTEs`, `WorkTableRows`, `TempTableShadows`,
`CurrSeqVals` / `LastSeqVal` / `LastSeqValSet` / `LastSeqValName`,
`DeadlockVictim`, `AnalyzeRandSeed`.

Rationale for the non-obvious entries:

- **`Ctx`** is the one field that provably *must* differ per worker: it is the
  per-worker child context from §4.1.
- **`CorrSubqOps` / `SubPlanHandles`** cache *pre-opened operators*. Sharing
  them would share cursor state, not just memory — two workers would advance
  the same subplan.
- **`CTERowCache` / `MaterializedCTEs` / `WorkTableRows`** are written *during*
  execution (`internal/executor/operators_cte_dml.go:52-53,99,254-257`;
  `operators_recursive_cte.go:118`). These deserve special emphasis: a
  concurrent write to a shared Go map is a **fatal runtime throw**, not a
  race-detector report — `race-gate` will not catch it, and the process dies.
  Note [08](08-planner-integration.md) §1 already terminates partial-ness at
  `RecursiveUnion` / `WorkTableScan`, and CTE scans below a Gather would each
  re-fill their own cache, which is correct but wasteful; a CTE-bearing subtree
  is a candidate for refusal rather than duplication.
- **`ParamExec` grown lazily** by `SetParamExec` (`context.go:763`) means even
  the copied-by-value slots (§2.2) must be *copied*, not aliased — a shared
  backing array would race on append.
- **`SubPlanStats`** already documents itself as single-goroutine
  (`context.go:174-175`).
- **`subqBudget`** is a `kvcache.Budget` with **no mutex**
  (`internal/executor/kvcache/kvcache.go:20-52`). Per-worker budgets of
  `WorkMem/4` each mean N workers may use N× the memory a serial plan would;
  this is the same behaviour PG has (work_mem is per-node per-worker) and is
  accepted, but it must be a conscious choice and is called out in
  [09](09-verification-and-measurement.md) as something to measure. The
  alternative — making `Budget` atomic and sharing it — is *not* chosen,
  because contention on a single counter in the cache hot path would cost more
  than the memory saves.

This list is derived from `internal/executor/context.go` and should be
re-derived against that file at implementation time; the struct is large and
actively edited, and a field added between now and then will default to being
*shared*, which is the dangerous direction.

### 2.4 Merged back at the Gather boundary

`Notices`, `NoticesWithDetail`, `Warnings`, `SubPlanStats`, `MemoizeStats`, and
the new per-worker Gather stats.

Notices need care: `AddNotice` (`context.go:828`) has no lock, and
`NoticeFlush` (`context.go:350`) writes to the wire. A worker must never call
`NoticeFlush`; it appends to its own slice, and the leader flushes after
merging, in worker order for determinism.

### 2.5 Forbidden to workers

The ~40 callback fields (`GetSetting`, `SetSetting`, `AllSettings`,
`CancelBackend`, `TerminateBackend`, `QueueNotify`, `Promote`,
`OnRoleDropped`, …) close over per-connection state and must be nil in a
worker context, so that a violation is a nil-pointer panic at the call site
rather than a silent cross-goroutine mutation. This is the mechanical
counterpart to the `proparallel` gate in [08](08-planner-integration.md): the
planner refuses plans that would need them, and the nil-ing makes a planner
miss loud instead of subtle.

## 3. The tuple ownership contract

**Rule: every row a worker sends to the leader must be fully owned by that
row.** Concretely, a worker must call `Materialize()` (equivalently
`cloneRowOwned`, `internal/executor/datum.go:408-434`) before the send.

Two independent reasons, either sufficient:

1. **Slot aliasing.** Scans, `projectOp`, NLI and Phase C's `dst *Slot` all
   return slots aliasing a buffer overwritten on the next `Next()`
   ([01](01-current-state-and-gap-analysis.md) B2). Sending such a slot races
   the producer's own next iteration.
2. **Arena lifetime.** A `KindString`/`KindBytes` Datum with `ArenaID != 0`
   (`datum.go:109`) is an `(offset, length)` into an `mctx` arena. In
   `seqScanOp` that arena is reset **at every block boundary**
   (`operators_storage.go:794-798`, reset at `:1727-1731`) — the exact cadence
   at which a parallel worker takes new work. A row that crossed the channel
   without promotion would be read by the leader after the producer recycled
   the bytes underneath it.

**What must NOT be used:** `cloneRow` (`datum.go:859-867`) and `Slot.CopyTo`
(`opnode.go:83-94`). Both are shallow copies that preserve `ArenaID`. They are
correct within one goroutine and silently wrong across one. This is the single
easiest mistake to make in the whole design, because both are the obvious-looking
helper and both pass any single-threaded test.

### 3.1 `cloneRowOwned` does not promote every arena-backed kind

`cloneRowOwned` (`datum.go:408-432`) promotes **only** `KindString` and
`KindBytes`:

```go
if d.ArenaID != 0 && (d.Kind == KindString || d.Kind == KindBytes) { … promote … }
else { dst[i] = d }        // copied verbatim, ArenaID preserved
```

But `KindString`/`KindBytes` are not the only arena-backed kinds. A
**big-mantissa `KindNumeric`** also carries `ArenaID` with
`Int = (offset<<32 | length)` into an arena (`datum.go:70,101`,
`newBigNumericInCtx` at `datum.go:504-520`) — and it falls into the `else`
branch, copied with its `ArenaID` intact.

**This is nonetheless safe today, for a reason that lives elsewhere and must be
preserved.** Every call site of `newBigNumericInCtx` allocates from
`mctx.Perm()` — the process-global permanent context (`datum.go:499`,
`numeric.go:194`, `expr.go:1190`) — which is never `Reset()` and never
`Release()`d. A big numeric's payload therefore outlives any worker.

So the contract in §3 holds, but it holds **conditionally**, and the condition
is invisible at the point of use. Two consequences for implementation:

1. **Document the invariant at `cloneRowOwned`**: it is complete only while all
   arena-backed numerics live in `Perm()`. If any future change allocates a big
   numeric from a statement or per-page context, `cloneRowOwned` silently stops
   being sufficient and rows will carry dangling offsets across the Gather.
2. The debug assertion specified in [09](09-verification-and-measurement.md) §3
   must therefore check `ArenaID == 0` **or `ArenaID == PermContextID`** on
   every datum leaving a worker — not merely on strings and bytes. Checking
   only strings would let exactly this case through.

### 3.2 A pre-existing hazard this design must not inherit

The same investigation surfaces something broader than the tuple contract.
`mctx.Perm()` is a process-global `*Context` (`mctx.go:85-90`) whose allocator
is an **unsynchronised bump pointer**: `allocBytes` (`mctx.go:308-320`) appends
to `c.chunks`, mutates `c.head`, and re-slices `cur.buf` with no lock anywhere
in the type.

Any expression evaluation that produces a numeric too large for an int64
mantissa allocates from it — for example numeric arithmetic or negation
(`expr.go:1190`). Under this design, **N workers evaluating such expressions
concurrently race on the process-global arena.**

Note the scope carefully: because `Perm()` is process-global rather than
per-backend, this is already reachable by two *concurrent sessions* doing
big-numeric arithmetic, independently of parallel query. That makes it a
pre-existing defect rather than one this bundle introduces — but parallel query
turns it from rare (two sessions colliding on the same instruction window) into
routine (N workers in one query).

**Requirement:** `Perm()` must be made safe for concurrent allocation before
P4 of [10](10-roadmap.md) lands. The cheapest correct fix is a mutex on the
permanent context alone — it is off the per-row hot path (only big numerics
touch it), so contention is not a concern. A lock-free bump allocator is
available if measurement ever disagrees.

This should also be reported as a standalone bug independently of whether this
bundle is implemented.

### 3.3 Where the materialisation happens

At the **worker's top-level output**, immediately before the channel send —
not inside individual operators. Pushing it lower would materialise rows that
later get filtered away; pushing it higher is impossible, because "higher" is
the leader.

Batching (see [05](05-gather-and-gather-merge.md)) makes this natural: the
worker fills a `[]Row` of materialised rows and sends the batch.

### Divergence from PostgreSQL

PG gets this guarantee for free: a tuple that survives `shm_mq` serialisation
is by construction a self-contained copy in the receiver's memory. goopg must
produce the same guarantee deliberately. The compensating advantage is real —
no encode/decode step, no fixed-size queue to size, no `MinimalTuple`
round-trip — but it is bought with a discipline that only review and tests
enforce.

Mitigations specified in [09](09-verification-and-measurement.md): a debug-build
assertion that every datum leaving a worker has `ArenaID == 0` **or
`ArenaID == PermContextID`** (§3.1), and race-gate coverage.

## 4. Failure, cancellation, and panics

None of this machinery exists today ([01](01-current-state-and-gap-analysis.md)
B9), so it is specified from scratch.

### 4.1 Cancellation

The existing model already fits: one `context.Context` per query
(`ctx.Ctx`), polled by every drain loop at throttled intervals — every 4096
rows in the join/agg paths (`operators_join_agg.go:511-517`), every 4096 in
`sortOp.Open` (`operators.go:658-664`), per block boundary in `seqScanOp`
(`operators_storage.go:1397-1401`) — and converted to SQLSTATE `57014`.

Design: the Gather derives a **child context** from `ctx.Ctx` via
`context.WithCancel` and gives it to every worker. Workers keep their existing
polling, unchanged. Cancelling the child stops all workers; cancelling the
parent (statement timeout, client EOF via `internal/server/eof_watch.go`, user
cancel) propagates automatically.

The client-EOF watcher matters here. Its header comment
(`eof_watch.go:12-20`) records the incident that motivated it: orphaned work
spinning at over 100 % CPU and 11.7 GB RSS after the client died. N workers
multiply that failure mode, so worker shutdown must be verified, not assumed —
[09](09-verification-and-measurement.md) requires a goroutine-leak test.

### 4.2 Error propagation

`ExecError` (`internal/executor/expr.go:43-51`) is a plain value with no
`Unwrap`, so "first error wins" must be explicit rather than emergent.

Design: a buffered error channel of capacity N (so no worker blocks on send
during shutdown) plus a `sync.Once`-guarded "first error" slot. On any worker
error: record it if first, cancel the child context, return. The leader, on
observing worker completion, checks the slot and returns that error.

**Determinism caveat, stated rather than hidden:** which error surfaces when
two workers fail simultaneously is genuinely non-deterministic. PG has the same
property. Tests must not assert a specific error when provoking multi-worker
failure; they assert *an* error of the right class.

### 4.3 Panics

A panic in a worker goroutine kills the process — there is no ambient
`recover()` on a goroutine the server did not start. `serveConn` has one
(`internal/server/server.go:779-792`) but it only protects the connection
goroutine.

Design: every worker goroutine begins with a `defer recover()` that converts
the panic into an `ExecError` (SQLSTATE `XX000`), routes it through the same
first-error path as §4.2, and cancels siblings. This converts a
process-wide crash into a failed query, which is the same blast radius PG has
(a crashed worker fails the query, not the cluster).

## 5. Arena lifetime

Rules, in order of importance:

1. **Each worker gets its own `mctx` child**, created by the **leader** before
   fan-out. Workers must not call `mctx.Acquire` concurrently: it appends to
   `parent.children` without synchronisation (`mctx/mctx.go:180`), so
   concurrent acquisition is itself a slice-append race.
2. **A worker's arena is reset only by that worker.** `Reset()` cascades to
   children (`mctx.go:186-188`), so the leader must not reset the statement
   context while workers are live — another reason worker lifetime is strictly
   nested inside the statement (§6).
3. **Nothing arena-backed crosses the channel** (§3).
4. Release happens leader-side after all workers have joined.

### 5.1 A scalability note that is not a correctness issue

`mctx.Lookup` (`mctx.go:110-121`) takes the global `ctxMu` on every arena
dereference. With N workers each resolving arena strings on the scan hot path,
this becomes a process-wide serialisation point that could plausibly eat the
parallel gain outright.

This is **not** a blocker for correctness and is deliberately *not* fixed in
this bundle's scope. It is flagged as the most likely reason an early
measurement disappoints, with the remedy (a lock-free registry: an atomic
pointer array indexed by `ContextID`) recorded in [10](10-roadmap.md) as a
prerequisite to expect, not a surprise to discover.

## 6. Lifecycle, locks, and pins

### 6.1 Worker lifetime is strictly nested inside the statement

Non-negotiable, for two independent reasons:

- **Locks.** `ReleaseTableLocks` / `ReleaseTupleLocks` (`context.go:1037,1054`)
  run `ReleaseAll` on the *statement* backend ID at statement end. A worker
  outliving that would have its locks vanish underneath it.
- **Arenas.** The statement `mctx` is released by
  `defer stmtCtx.Release()` in the dispatcher (`internal/server/dispatch.go:290-306`),
  which cascades to worker children.

Therefore the Gather's `Close()` must **join every worker** before returning,
including on the error and early-`Limit` paths. "Fire and forget" shutdown is
forbidden.

### 6.2 Workers never release locks

`lockmgr` holders are a bitmask, not a refcount
(`internal/lockmgr/lockmgr.go:199`), so `Release` clears the bit outright
(`:557`) — the first releaser drops the lock for the whole transaction
([01](01-current-state-and-gap-analysis.md) §1). Acquisition is idempotent and
self-conflict-free (`:419-430`), so a worker acquiring a lock the transaction
already holds is harmless.

Rule: **workers may acquire, only the leader releases.** In practice v1
workers should not need to acquire at all — the leader's scan setup has already
taken the relation lock, and `Context.LockMgr` is nil by design in the
production server (`context.go:960-961`).

### 6.3 Pin accounting is a crash cliff

`Unpin` **panics** on underflow (`internal/storage/bufpool.go:1918-1930`). With a
shared pin count across N workers, an imbalance takes down the process rather
than leaking a buffer.

Rules: each worker pins and unpins its own pages; no worker unpins a page it
did not pin; the Gather's join (§6.1) happens before any statement-level
cleanup that might unpin. A debug-build per-worker pin counter, asserted zero
at worker exit, is specified in [09](09-verification-and-measurement.md) — the
cost of finding this class of bug late is disproportionate.

### 6.4 CLog / SLRU under N concurrent readers — must be verified

Visibility checking falls through to the snapshot's `*CLog`
(`internal/mvcc/snapshot.go:88`) → `CLog.GetStatus`
(`internal/mvcc/clog.go:160-168`) → the buffer pool's `getStatus`, which takes
`mu sync.Mutex` and performs LRU pin/evict
(`internal/mvcc/clog_bufferpool.go:133,293`).

This is the hottest shared *mutable* structure a parallel scan touches, and it
is reached only for XIDs the in-memory snapshot arrays cannot classify
(`snapshot.go:228-237`) — so the common path avoids it, but the uncommon path
funnels N workers through one mutex with page replacement underneath.

The locking looks correct, and the `atomic.Pointer` on the pool
(`clog.go:73`) is encouraging, but **"looks correct" is not the standard here**:
SLRU page replacement under concurrent readers must be verified explicitly
before P4 of [10](10-roadmap.md), not inferred from the presence of a mutex.

### 6.5 Instrumentation

`instrumentScope` is a package-level global over an unsynchronised map
(`internal/executor/instrument.go:215,202`). Under workers it is a race, and
`race-gate` will fail.

Design: each worker builds with **its own instrumenter**; the Gather merges
per-node stats into the leader's table at join time. This is the same shape PG
uses (worker instrumentation is aggregated into the leader) and it is what
makes per-worker row counts and `Workers Launched:` meaningful. Locking the
global instead would be simpler and would produce numbers that are correct but
useless — no per-worker attribution.

## 7. What forbids writes, mechanically

The read-only restriction ([README](README.md)) is enforced at three layers,
deliberately redundant:

1. **Planner** — refuses to place a Gather under any DML statement node, under
   SERIALIZABLE, or over a plan containing parallel-unsafe functions
   ([08](08-planner-integration.md)).
2. **Worker context** — connection callbacks are nil (§2.5), so a slipped-through
   write attempt panics at the call site instead of corrupting session state.
3. **Review-time invariants** — workers must not call `Manager.AssignXID`
   (`internal/mvcc/manager.go:335`), mutate the subxact stack, call
   `SetRelcacheInvalPending` (`:1309`), touch `getOrCreateNS`
   (`internal/catalog/catalog.go:3526`), or record SSI conflicts
   (`internal/executor/ssi.go:455`).

### 7.1 One permitted write: hint bits

Sequential scans set `HeapXminCommitted` hint bits on pages they read
(`internal/executor/operators_storage.go:1683-1691`), under the page's content
latch with `Pool.MarkDirtyHintBit`. This *is* a write, it *is* on the read
path, and it is **permitted** — it is correctly latched, and PG allows parallel
workers to set hint bits for the same reason. Called out explicitly so it is
not mistaken for a violation of §7 during review. The guard at `:1526-1529`
correctly excludes self-XID tuples.

## 8. Divergence from PostgreSQL — summary

| Concern | PG | goopg | Cost of the difference |
| --- | --- | --- | --- |
| Plan availability in workers | Serialised into DSM, reconstructed | Shared by pointer | Requires plan immutability during execution (§1) |
| Snapshot / txn state | Copied into DSM, restored | Shared value struct | None — it is already immutable |
| Tuple transport | `shm_mq`, 64 KiB/worker, serialise + deserialise | Channel of materialised `[]Row` batches | Loses PG's accidental ownership guarantee; §3 must supply it deliberately |
| Worker startup | fork + DSM attach; `parallel_setup_cost = 1000` | goroutine, ~µs | Profitable-parallelism threshold is far lower; [08](08-planner-integration.md) decides whether to honour PG's constant |
| Error transport | Error propagated through the message queue | Channel + `sync.Once` first-error | Non-deterministic which error wins under simultaneous failure (§4.2) |
| Worker crash | Process dies; query fails | Panic would kill the process | Requires explicit `recover()` per worker (§4.3) |
| Instrumentation | Shipped back through DSM | Per-worker structs merged in memory | None |
| Data races | Structurally impossible | Possible | `race-gate` becomes an acceptance criterion |

The pattern is consistent: goopg removes an entire transport layer and pays for
it with explicit discipline in exactly two places — tuple ownership (§3) and
worker lifecycle (§6). Everything else is strictly simpler than PG.
