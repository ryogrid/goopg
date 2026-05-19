# 08 — Recommendations

Ranked optimisation backlog. Each item lists: estimated TPS lift (band), implementation cost (S / M / L), workloads helped, prerequisites, and citations into the practice doc (`practice/go_rdbms_performance_techniques.md`) and the upstream PG source.

The ordering is by expected lift × inverse cost. Implementing items #1, #2, #4 in that order is expected to recover most of the gap on `simple` mode; items #3 and #5 unlock the c=100 write workloads.

## §8.1 The ranked list

### #1 — Per-statement / per-transaction arena (`palloc`-style memory context)

| | |
|---|---|
| **Targets** | §02 (50–77 % GC CPU), §03 (~ 5 GB/120 s alloc rate) |
| **Lift est.** | **3–5× TPS** on c=10 SO and SU; 1.5–2× on c=50/c=100 |
| **Cost** | **L** — touches parser, planner, executor, server.dispatch (many files) |
| **Prereqs** | None — `internal/executor/arena.go` exists as foundation (M0098-0007a) |
| **Practice doc** | §1 (Memory Allocation & GC Pressure), §6 (Data Layout) |
| **PG counterpart** | `postgres/src/backend/utils/mmgr/aset.c` (`AllocSetContextCreate`, `palloc`, `MemoryContextReset`) |

**What to do:** Plumb an `*Arena` (or `MemoryContext`-style interface) through `parser.Parse`, `planner.Plan`, `executor.Open/Next/Close`, and `dispatchSimpleQueryViaExecutor`. Allocate AST nodes, Plan nodes, intermediate `Row`/`TupleSlot`/`Datum` slices, and protocol scratch buffers from the per-statement arena. Reset (single pointer assignment) at statement boundary.

The largest single win is the planner: 26 % of c=100 SO allocs come from `planner.Plan` (`internal/planner/planner.go:32`). A planner that emits `Plan` nodes into an arena and never lets them escape to the GC heap removes that 26 % directly.

**Verification:** Re-run §02 with the arena engaged; expect `gcBgMarkWorker cum%` to drop from 63 % to < 20 % at c=10 SO.

---

### #2 — `mvcc.Manager` mutex decomposition

| | |
|---|---|
| **Targets** | §04 §4.2 (92 % of write-side mutex delay; 63 % of write-side block delay; the c=10→c=50 SU regression) |
| **Lift est.** | **3–6× TPS** on c=50 SU/standard; unblocks c=100 writes (combined with #4) |
| **Cost** | **M** — touches `internal/mvcc/manager.go` (single file but central) |
| **Prereqs** | None |
| **Practice doc** | §16 (Concurrency), specifically sharded mutexes and lock-free freelists |
| **PG counterpart** | `postgres/src/backend/storage/ipc/procarray.c:2175` (`GetSnapshotData`); `postgres/src/backend/access/transam/varsup.c:77` (`GetNewTransactionId`); `postgres/src/backend/access/transam/clog.c:304` (CLOG bank locks) |

**What to do:** Split `Manager.mu` into:

- A `xidGen` atomic for `nextXID` (replace map with `[N]atomic` shards).
- A `procArray`-equivalent: per-backend `txState` slot allocated once at backend start; CAS to register/deregister; readers (snapshot, OldestXmin) walk slots lock-free.
- A small `commitMu` (sharded) for the commit-status write to the abortedXIDs / clog. Keep this critical section tiny.

The goal: `SnapshotFor` should be O(active_backends) read-only walk with no mutex (or only an `RLock` on a partitioned shard). Begin should be an atomic xid increment + slot register. Commit should be an atomic slot deregister + a small CLOG write.

**Verification:** Re-run §04; expect `Manager.mu` to disappear from mutex top-20 on c=50 SU. c=50 SU TPS should rise from 347 to > 2 000.

---

### #3 — Lock-free `activity.Registry` (per-backend `wait_event_info`)

| | |
|---|---|
| **Targets** | §04 §4.3 (95 % of c=100 SO mutex delay) |
| **Lift est.** | **1.5–2× TPS** on c=100 SO and SU |
| **Cost** | **S–M** — single file `internal/activity/activity.go` |
| **Prereqs** | None |
| **Practice doc** | §16 (avoid central registries; use per-goroutine state); §6 (per-CPU sharding) |
| **PG counterpart** | `postgres/src/include/storage/proc.h:104` (`PGPROC->wait_event_info`); `postgres/src/backend/utils/activity/wait_event.c` |

**What to do:** Replace the central `Registry` mutex + `backends map[string]*BackendEntry` with:

- One `BackendEntry` per backend, owned by the backend goroutine.
- `WaitEventStart` / `WaitEventEnd` become `atomic.Uint32.Store` on the backend's own entry.
- Stats consumers (`pg_stat_activity` queries) iterate over the backend slice without a lock; staleness is acceptable.
- Registration / deregistration of backends takes a slow-path mutex but only on connect / disconnect.

**Verification:** mutex profile c=100 SO should no longer show `WaitEventStart/End` in top-10. TPS should rise from 6 400 to ~ 10 000+.

---

### #4 — Distribute `pgbench_history`-style append inserts via FSM

| | |
|---|---|
| **Targets** | §04 §4.4 (c=100 write livelock; 19 goroutines stalled for 23 min on one partition) |
| **Lift est.** | Unblocks c=100 SU and c=100 standard (currently SKIPPED); enables measurement |
| **Cost** | **M–L** — `internal/storage`, `internal/access/heap` insert path; FSM may already exist in skeleton |
| **Prereqs** | None (independent of #1–#3 but combine for max benefit) |
| **Practice doc** | §16 (avoid hot serialisation points), §6 (false-sharing avoidance via distribution) |
| **PG counterpart** | `postgres/src/backend/access/heap/hio.c` (`RelationGetBufferForTuple`); `postgres/src/backend/storage/freespace/freespace.c` (FSM) |

**What to do:** When the insert path picks a target page for a new tuple:

1. Consult the relation's FSM (free space map) for a page with at least `tuple_size` free bytes.
2. If FSM returns a page that some other backend is currently writing to (heavy pin count), pick a different page from the FSM's top-N candidates.
3. Only as a last resort, extend the relation by one page and target the new last page.

Today's path appears to deterministically target the tail page, which is fine at low concurrency but causes the c=100 deadlock.

**Open item:** confirm by reading `internal/storage/heap/insert.go` (or equivalent) — `04-contention.md` §4.4 flagged this as "requires confirmation".

**Verification:** rerun c=100 SU; expect TPS > 500 (vs current SKIPPED).

---

### #5 — WAL insert lock striping (`NUM_XLOGINSERT_LOCKS=8`)

| | |
|---|---|
| **Targets** | §02 (write-side `syscall.Syscall6` for fdatasync amortised, but WAL append serialises behind `appendMu`) |
| **Lift est.** | 1.5–2× TPS on c=50/c=100 write workloads (after #2 unbottlenecks the snapshot path) |
| **Cost** | **M** — `internal/wal/writer.go:355` (split `appendMu` → array) |
| **Prereqs** | #2 (otherwise mvcc.Manager bottleneck masks the gain) |
| **Practice doc** | §16 (sharded mutexes) |
| **PG counterpart** | `postgres/src/backend/access/transam/xlog.c:151,570` (`WALInsertLocks[NUM_XLOGINSERT_LOCKS=8]`); `xlog.c:1392,1399` (`MyProcNumber % NUM_XLOGINSERT_LOCKS` striping) |

**What to do:** Replace `appendMu sync.Mutex` with `appendLocks [8]sync.Mutex`. Each backend stripes into one based on a hash of its goroutine ID (or backend index). The WAL writer's `FlushUpTo` already merges all stripes; only the append path needs sharding.

**Verification:** mutex profile under c=50 SU should show the 8 stripes balanced; TPS should improve (combined with #2) to > 3 000.

---

### #6 — Pointer-free buffer pool inner structures

| | |
|---|---|
| **Targets** | §03 §3.4 (GC scans `partitions[128].byTag map` every cycle even though it doesn't change) |
| **Lift est.** | 1.2–1.5× TPS across all workloads (CPU savings on GC scan) |
| **Cost** | **M** — `internal/storage/bufpool.go` data-layout refactor |
| **Prereqs** | None |
| **Practice doc** | §6 (Data Layout & CPU Cache; pointer-free dense structures), §8 (custom open-addressing maps) |
| **PG counterpart** | `postgres/src/backend/storage/buffer/buf_table.c` — `BufTableLookup` over a shared hash table; entries are `BufferLookupEnt` POD structs |

**What to do:** Replace `bufferPartition.byTag map[BufferTag]int` with a hand-rolled open-addressing table:

- `keys []BufferTag` (fixed-width, no pointers — `BufferTag` is already POD).
- `vals []int32` (slot indices into `Pool.slots`).
- Tombstone marker for deletions.
- `runtime/internal/atomic` for lock-free `Load` on read fast path; mutex only on insert/delete.

GC scanner sees only `[]BufferTag` and `[]int32` — both pointer-free, scanned in O(slab_count) not O(entries).

**Verification:** heap profile and GC time after the change. Expect 10–15 % GC-CPU reduction.

---

### #7 — Avoid `Row` materialisation copy at operator boundaries

| | |
|---|---|
| **Targets** | §03 §3.5 (105 KB per UPDATE; many of these are `Materialize().Row()` copies) |
| **Lift est.** | 1.3–1.5× TPS on writes; modest on reads |
| **Cost** | **L** — invasive change to operator API |
| **Prereqs** | #1 (so resulting alloc savings actually translate to GC savings) |
| **Practice doc** | §1 (eliminate heap escapes), §5 (zero-copy unsafe), §6 (avoid pointer chasing) |
| **PG counterpart** | `postgres/src/backend/executor/execTuples.c` (`TupleTableSlot` reuse) |

**What to do:** Operators pass `TupleSlot` (a *view* over an underlying pinned buffer or arena) rather than `Row` (a copy). Introduce a `Materialize()` only at the public Run / wire-encode boundary. The pattern matches PG's `TupleTableSlot` discipline.

**Verification:** allocs profile c=10 SU should show `updateOp.Next` drop from 35 % to < 10 %.

---

### #8 — `sync.Pool` for protocol message buffers and operator state

| | |
|---|---|
| **Targets** | §03 protocol encode allocations; per-statement operator-state allocations |
| **Lift est.** | 1.1–1.3× TPS (additive to #1, not redundant) |
| **Cost** | **S** — small per-package additions |
| **Prereqs** | None |
| **Practice doc** | §1 (`sync.Pool` for transient objects) |
| **PG counterpart** | Per-backend scratchpads (`palloc` from `MessageContext`) |

**What to do:** Add `sync.Pool` for:

- `internal/protocol.FrameWriter` output buffers.
- Per-operator state structs (`seqScanOp`, `indexScanOp`, `updateOp`) — reset on `Open`, return on `Close`.
- WAL record body buffers (`internal/wal/writer.go` Append scratch).

**Verification:** allocs profile should show net reduction; operator-state alloc lines should disappear from top-10.

---

### #9 — Per-buffer content-lock CAS (lock-free pin path)

| | |
|---|---|
| **Targets** | §04 §4.4 (bufpool partition contention beyond the c=100 livelock) |
| **Lift est.** | 1.2× TPS at c=50/c=100 (assuming #4 already unblocks c=100 writes) |
| **Cost** | **M** — `internal/storage/bufpool.go` slot state machine refactor |
| **Prereqs** | #4 |
| **Practice doc** | §16 (lock-free freelist; latch-coupling) |
| **PG counterpart** | `postgres/src/backend/storage/buffer/bufmgr.c:3058,3112,3313` (atomic state CAS) |

**What to do:** Slot pin/unpin uses a single atomic CAS on a packed `state uint64` (containing `pinCount`, `usageCount`, `valid`, `dirty`). No mutex needed for the fast path. Eviction takes a per-slot spinlock only on slow path.

**Verification:** Pool.Pin should disappear from mutex profile entirely.

---

### #10 — Plan cache for prepared statements

| | |
|---|---|
| **Targets** | §02 (parser + planner allocations) — only helps when applications use `-M prepared` |
| **Lift est.** | 1.5–2× TPS for prepared-mode pgbench / real applications |
| **Cost** | **M–L** — `internal/parser`, `internal/planner`, `internal/server/extended.go` |
| **Prereqs** | None |
| **Practice doc** | §1 (reuse), §12 (compile to closures at plan time) |
| **PG counterpart** | `postgres/src/backend/utils/cache/plancache.c:94,667` (`RevalidateCachedQuery`, `BuildCachedPlan`) |

**What to do:** When a backend re-executes a prepared statement with the same name, reuse the cached `Plan` tree. Invalidate on DDL / stats refresh. M0098-0005 is the design doc; status unverified.

**Verification:** under `pgbench -M prepared`, parser+planner cum% should drop to < 1 %.

## §8.2 What *not* to do (in scope for this exercise)

- **Lockmgr partitioning**: was hypothesised to be top-3 but doesn't appear in any profile. Defer; the `internal/lockmgr` single-mutex is not on pgbench's critical path because pgbench acquires only 2–3 relation locks per transaction and those are short.
- **`huge_pages` / `MADV_HUGEPAGE`**: marginal until #1 reduces allocation churn. With 5 GB/120s of alloc, huge pages save TLB misses but the cost is still in the scan.
- **PGO recompilation of `bin/goopg`**: already in place per M0098-0007; gains of 2–10 % are dwarfed by #1's 3–5× potential. Worth keeping but not a recommendation.
- **`GOGC=off`** during measurements: increases TPS at the cost of heap growth — useful for benchmarking ceilings but not for production. Mention only.

## §8.3 Order of attack

If implementing in priority order:

1. **#3 (activity.Registry)** — quickest win (small file change), unblocks c=100 SO scaling.
2. **#1 (per-statement arena)** — biggest single lift; expensive but foundational. Do after #3 because the activity Registry fix changes the contention picture and lets you measure GC impact more cleanly.
3. **#2 (mvcc.Manager decomposition)** — biggest write-side win.
4. **#4 (FSM-distributed inserts)** — unblocks c=100 write measurement.
5. **#5 (WAL insert striping)** — additive after #2.
6. **#7 / #8 / #9** — incremental wins, reinforcing #1 / #4.
7. **#10 (plan cache)** — only when application workloads warrant; pgbench `simple` mode doesn't.

Each item is independently measurable against the §01 baseline; re-running the suite (`bash analysis/perf-optimize/scripts/run_perf_suite.sh`) takes ~ 60 min and produces directly comparable numbers.

## §8.4 What this analysis did *not* establish

- **Tail latency under burst load**: 180 s steady state is not the same as bursty traffic. The `runtime/trace` data hints at multi-ms GC pause windows that could be problematic for SLO-bound workloads.
- **Recovery / replication performance**: out of scope.
- **NUMA / NIC affinity**: 16 logical cores on a single socket; no NUMA effects expected on this host.
- **Larger-than-buffer-pool workloads**: scale 100 (~ 1.5 GB) fits in 2.5 GB shared_buffers; the I/O path is not on the critical path here.
- **`-M extended`**: only `simple` mode tested.

The recommendations above are sized for the measured `simple`-mode pgbench workload; gains on prepared / extended modes will be *additionally* higher (because plan-cache reuse compounds with arena reuse).
