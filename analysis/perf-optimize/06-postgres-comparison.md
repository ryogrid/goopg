# 06 — PostgreSQL Comparison

For each bottleneck identified in chapters 02–05, this chapter cites the PostgreSQL counterpart and explains why PG does not suffer the same cost. All upstream paths are under `./postgres/src/` (read-only oracle); navigated with `global -x` and `mcp__any-script__pg_search_symbols`.

## §6.1 Bottleneck-to-PG cross-reference table

| # | goopg bottleneck | source | PG counterpart | upstream source | why PG is faster |
|---|---|---|---|---|---|
| 1 | **GC mark + scan cost** (54–77 % CPU) | runtime, scanning `bufpool.Pool.partitions[128]` + churning planner / executor allocations | No GC: per-statement `MemoryContext` (palloc), reset in bulk at statement end | `postgres/src/backend/utils/mmgr/aset.c`, `mcxt.c` | PG has *zero* GC scanning cost. Per-statement memory is bump-allocated and freed in O(1) at end of statement. |
| 2 | **`mvcc.Manager.mu`** — Begin / SnapshotFor / Commit / OldestXmin all gated | `internal/mvcc/manager.go:73` | 3 different lock classes: `ProcArrayLock` (snapshot), `XidGenLock` (xid alloc), CLOG bank locks (commit status) | `postgres/src/backend/storage/ipc/procarray.c:2175` (`GetSnapshotData`); `postgres/src/backend/access/transam/varsup.c:77` (`GetNewTransactionId`); `postgres/src/backend/access/transam/clog.c:304` | Reads (`GetSnapshotData`) take shared ProcArrayLock over a dense `pgxactoff[]` array (PG 14+); writes (xid alloc, commit) take different locks; the four operations never contend with each other. |
| 3 | **`activity.Registry.mu`** — wait-event tracking per protocol frame | `internal/activity/activity.go:123` | Per-backend `PGPROC->wait_event_info` (uint32) in shared memory; readers do lock-free scan | `postgres/src/include/storage/proc.h:104` (`PGPROC` struct); `postgres/src/backend/utils/activity/wait_event.c` | No central registry: each backend writes to its own `uint32`. Stats consumers read without locking. |
| 4 | **`bufferPartition.mu`** — hot-page contention on `pgbench_history` tail (c=100 livelock) | `internal/storage/bufpool.go:79` | Same 128-partition bufmapping LWLock pattern, plus per-buffer content lock + atomic pin-count CAS + FSM-distributed inserts | `postgres/src/backend/storage/buffer/buf_table.c:90`; `postgres/src/backend/storage/buffer/bufmgr.c:3058` (pin-count CAS); `postgres/src/backend/storage/freespace/freespace.c` (FSM) | PG's insert path consults FSM and spreads new tuples across multiple newly-extended pages, breaking the tail-page hot spot. |
| 5 | **WAL insert serialisation** (single `appendMu`) | `internal/wal/writer.go:355` | 8-way stripe array of `WALInsertLocks[]` | `postgres/src/backend/access/transam/xlog.c:151,570` | 8 inserters can run in parallel; `MyProcNumber % 8` distributes them. |
| 6 | **`runtime.futex` cost on every mutex hand-off** (15–23 % CPU) | Go runtime `sync.Mutex` | LWLock fast path: single atomic CAS on lock state; SysV semaphore only on slow path | `postgres/src/backend/storage/lmgr/lwlock.c:1180`; `postgres/src/backend/storage/lmgr/lwlock.c:867` (`LWLockWaitListLock` — separate spinlock for the wait list) | Go's `sync.Mutex.Unlock` always wakes a waiter via `runtime_Semrelease` (futex syscall). PG's LWLock has no syscall on uncontended Unlock. |
| 7 | **Per-statement re-parse and re-plan** | `internal/parser/parser.go:82`, `internal/planner/planner.go:32`; allocates ~ 6 KB / SELECT | Plan cache for prepared statements (`RevalidateCachedQuery` / `BuildCachedPlan`) | `postgres/src/backend/utils/cache/plancache.c:94,667` | pgbench's `simple` mode also re-parses on PG, but PG's parse + plan is ~5× cheaper per call because it goes into a freed-in-bulk `MessageContext` and re-uses syscache entries; `simple` mode is rarely a real-app workload anyway. |
| 8 | **Per-statement `Row` materialisation copies** | `internal/executor/executor.go:282` (`Materialize().Row()`) | `TupleTableSlot` passes pointers into the pinned buffer; no copy until necessary | `postgres/src/backend/executor/execTuples.c` | PG never copies a tuple unless it crosses a `MaterializeReceiver` boundary or needs to be detoasted. goopg copies at every operator boundary. |
| 9 | **`updateOp.Next` allocates ~ 105 KB per UPDATE** (HOT version + datum slices) | `internal/executor/operators_storage.go:985` | `heap_update` builds the new tuple into a palloc'd region; HOT path threads on-page; no `Datum` slice rebuild | `postgres/src/backend/access/heap/heapam.c:3242` | PG's HOT path does not re-form a tuple from `Datum`s; it modifies the on-page bytes directly via `heap_inplace_update` / `heap_update`. |
| 10 | **Bgwriter contends with reader pins at c=10** | `internal/storage/bgwriter.go:73` → `Pool.WriteDirtyPages` → `Pool.flushSlot` (partition mu) | Bgwriter writes via `BufferSync` using the same buf-mapping lock but with O(1) per-buffer atomic state; clean buffers are not re-locked | `postgres/src/backend/storage/buffer/bufmgr.c:2729` (`BgBufferSync`) | PG's bgwriter skips clean buffers without taking the per-buffer lock (atomic dirty-flag check). goopg's bgwriter takes the partition mu to check. |

## §6.2 Architectural gap summary

There are two **architectural** differences (not parameter-tuneable) that account for the majority of the goopg/PG gap:

### Gap 1 — Memory management

PG's `MemoryContext` is a hierarchical bump allocator with O(1) bulk free at context reset. Every statement runs in a `MessageContext` reset on statement end; every transaction runs in `TopTransactionContext` reset on COMMIT/ABORT. The cost of *all* per-statement allocations is amortised into one pointer reset.

Go's GC is whole-process. Even with `sync.Pool`, arena, and `GOGC=200` mitigations, the GC scanner walks the live object graph on every cycle — and `internal/storage.bufpool.Pool.partitions` is a 128-element array of structs each containing a `map[BufferTag]int`. That map is GC-scanned every cycle even if it does not change.

**Implication**: goopg cannot match PG's memory cost without either (a) an arena-style allocator wired into the entire executor/planner/parser path (matching the practice doc's §1), or (b) a manual lifetime model using `unsafe.Pointer` + manual deallocation (sacrificing safety for raw throughput). M0098-0007a (arena_registry.go) is the foundation but is not yet engaged on the OLTP path.

### Gap 2 — Process model vs goroutine model

PG forks one process per backend. Each backend has:
- Its own address space → no GC sweep across backends.
- Its own statement-scope memory context → freed independently.
- Its own `PGPROC` slot in shared memory → no central registry needed.
- Lock-free reads of `wait_event_info`, `xid`, `xmin` from peers (shared memory, atomic reads).

goopg has one process with N goroutines. Per-backend state lives in central data structures (`mvcc.Manager.active map`, `activity.Registry.backends map`) which need locks. PG's per-backend isolation is *trivially* parallel; goopg's shared-state model requires careful locking to match.

**Implication**: To match PG's scaling, goopg needs to push per-backend state out of the central manager into per-backend (per-goroutine) structures. The `mvcc.Manager` is the prime candidate: replace `Manager.mu` + `active map[TxnHandle]*txState` with per-backend `txState` registered in shared memory by atomic CAS, and a `GetSnapshotData`-equivalent that walks the per-backend slots without taking a write lock.

## §6.3 What PG does that goopg cannot easily copy

A handful of PG techniques rely on the process model and would require non-trivial Go-side replication:

| technique | upstream | Go barrier |
|---|---|---|
| Per-backend `palloc` context isolation | `postgres/src/backend/utils/mmgr/aset.c` | No process boundary; would have to be a per-goroutine `*Arena` with discipline |
| Per-`PGPROC` lock-free `wait_event_info` | `postgres/src/backend/utils/activity/wait_event.c` | No shared-memory address; could be per-backend struct + `atomic.Uint32` — feasible |
| Fork-cost amortisation (process startup once, backend persists) | `BackendStartup` | Go has zero-cost goroutines — already wins this trade-off |
| `unsafe`-style page typed access | All of PG | Possible in Go (practice doc §5) but not yet applied uniformly |

## §6.4 Where goopg already matches PG

A handful of recent milestones (M0098-0003 buffer pool 128-partition, M0098-0002 WAL group commit, M0098-0007 GC tuning, M0099-0001/0002 buffer pin fast path) have *closed* the parts of the gap they targeted:

| optimisation | goopg | PG | status |
|---|---|---|---|
| 128-way buf-mapping partitioning | ✅ M0098-0003 | ✅ NUM_BUFFER_PARTITIONS=128 | parity |
| WAL group commit (waiter chain) | ✅ M0098-0002 | ✅ XLogFlush | parity |
| Pin fast path (atomic.Int32 pinCount) | ✅ M0099-0002 | ✅ atomic state CAS | near parity (RWMutex.RLock vs lock-free) |
| Persistent connections (no fork per query) | ✅ (goroutine) | ✅ (process reused) | parity / Go wins |
| WAL `commit_delay` / `commit_siblings` | ✅ M0098-0002 | ✅ | parity |

The remaining gap is concentrated in (1) memory allocation and (2) per-backend isolation, both architectural.

## §6.5 Where the workload structure matters

pgbench's `simple` query mode re-parses every statement, so the planner/executor allocation cost is **paid every transaction** — which inflates the gap on goopg's GC-heavy path. Real applications using prepared statements (`-M prepared`) would amortise the parse + plan cost, narrowing the c=10 SO gap from 16× to perhaps 4–6×. This is *not* a reason to dismiss the result — it just means the parse/plan portion of §02–§03 lifts proportionally more on `simple` mode than on prepared.

## §6.6 PG configuration that we did *not* test

PG's `huge_pages = on` and `shared_buffers >= 8 GB` are typical OLTP settings that would push PG's TPS even higher on this benchmark. We held PG at the same 2.5 GB shared_buffers for parity. goopg has no equivalent of `huge_pages` today (would require `unix.Mmap` with `MADV_HUGEPAGE`).
