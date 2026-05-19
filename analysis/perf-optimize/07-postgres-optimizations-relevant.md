# 07 — PostgreSQL Optimizations Relevant to pgbench

This chapter enumerates upstream PostgreSQL optimizations that materially move the needle on the three pgbench workloads (TPC-B-like, simple-update, select-only) and notes goopg's current implementation status against each. Each entry cites the canonical upstream source so a reviewer can verify the technique directly.

Status legend:

- **✅ implemented** — equivalent technique present in goopg.
- **🟡 partial** — partial or rudimentary implementation exists; the upstream optimization is not yet applied in full.
- **❌ absent** — no analogue in goopg today.

## 1. WAL insert lock striping (`NUM_XLOGINSERT_LOCKS = 8`)

- **Upstream**: `postgres/src/backend/access/transam/xlog.c:151` (`NUM_XLOGINSERT_LOCKS = 8`), `xlog.c:570` (`WALInsertLocks[]`), `xlog.c:1392,1399` (`MyProcNumber % NUM_XLOGINSERT_LOCKS` striping).
- **What it does**: rather than one global WAL-insert lock, eight partitions distribute concurrent inserters across stripes; each insert reserves space, writes its record, and releases the partition without serialising against unrelated inserters.
- **Workloads helped**: `standard` and `simple-update` (every write transaction enters WAL insert at least once).
- **goopg status**: 🟡 partial. `internal/wal/writer.go` has `appendMu sync.Mutex` at line 355 — a single lock for the entire WAL append path. Group commit (M0098-0002) batches *flushes* but inserts still serialise.

## 2. WAL group commit (waiter chain in `XLogFlush`)

- **Upstream**: `postgres/src/backend/access/transam/xlog.c:2780` (`XLogFlush`), `xlog.c:2851` (waiter-chain piggybacking).
- **What it does**: a backend asking to flush WAL up to LSN X discovers a leader is already flushing further; it links onto the waiter chain and gets woken when the leader's fsync completes. One fdatasync drains N waiters.
- **Workloads helped**: `standard`, `simple-update` (every COMMIT issues `XLogFlush`).
- **goopg status**: ✅ implemented (M0098-0002). `internal/wal/writer.go:611-1113` — `groupFlushReq` queue drained by writer goroutine, with optional `commit_delay`-style sleep at line 1048 to grow batches. Mirrors PG semantics.

## 3. Heavyweight lock manager partitioning (`NUM_LOCK_PARTITIONS = 16`)

- **Upstream**: `postgres/src/include/storage/lwlock.h:96` (`NUM_LOCK_PARTITIONS = 1 << 4`), `postgres/src/backend/storage/lmgr/lock.c:808` (`LockAcquire`), `lock.c:321` (`LockMethodLockHash` table).
- **What it does**: the global lock table is hashed into 16 partitions; each `LockAcquire` only takes the partition LWLock for its tag.
- **Workloads helped**: all three (every statement acquires a relation lock).
- **goopg status**: ❌ absent. `internal/lockmgr/lockmgr.go:212` exposes a single `sync.Mutex` for the entire lock manager. Under `-c 100` this is expected to be a top contention point.

## 4. Fastpath relation locks

- **Upstream**: `postgres/src/backend/storage/lmgr/lock.c:176` (`FastPathLocalUseCounts`), `lock.c:2829` (`FastPathTransferRelationLocks`). Conceptual overview in `postgres/src/backend/storage/lmgr/README:312-322`.
- **What it does**: weak relation locks (`AccessShareLock`, `RowExclusiveLock`) held by a single backend never touch the shared lock table — they live in 16 per-backend slots. The shared table is only consulted when an exclusive locker arrives, at which point fastpath holders are *transferred*.
- **Workloads helped**: `select-only` (only `AccessShareLock` on pgbench_accounts) and `simple-update` (RowExclusiveLock on accounts + history; never conflicts).
- **goopg status**: ❌ absent. Every lock acquisition hits the same `sync.Mutex`. Fixing #3 (partitioning) is a prerequisite to a credible fastpath implementation.

## 5. LWLock with wait-list spinlock + per-partition arrays

- **Upstream**: `postgres/src/backend/storage/lmgr/lwlock.c:867` (`LWLockWaitListLock`), `lwlock.c:1180` (`LWLockAcquire`), `postgres/src/include/storage/lwlocklist.h:40-57` (BufMapping/ProcArray/WALInsert per-partition arrays).
- **What it does**: LWLock waiters enqueue under a tiny spinlock distinct from the main lock; the main lock fast path is a single atomic CAS. Per-purpose lock arrays mean BufMapping(128) vs ProcArray(1) vs WALInsert(8) live in independent cache lines.
- **Workloads helped**: all three (LWLock is the substrate beneath buffer mapping, ProcArray, WAL).
- **goopg status**: 🟡 partial. `internal/storage/bufpool.go` already has 128 buffer partitions (M0098-0003, line 180) — each with a `sync.Mutex` and per-partition `sync.Cond`; that's the buffer-mapping analogue. `bufferPartition.mu` plus `contentMu sync.RWMutex` per slot mirror the PG dual-lock pattern. ProcArray-equivalent (`internal/mvcc/manager.go`) and WAL-insert arrays are not partitioned.

## 6. Buffer manager: 128 buf-mapping partitions + clocksweep + freelist

- **Upstream**: `postgres/src/backend/storage/buffer/buf_table.c:90` (`BufTableLookup`), `bufmgr.c:2000,2345` (`BufferAlloc`, `GetVictimBuffer` clocksweep), `freelist.c:196` (`StrategyGetBuffer`); `NUM_BUFFER_PARTITIONS = 128` in `lwlock.h:93`.
- **What it does**: hash-based partitioning of the buf-mapping table reduces probe-and-pin contention; the freelist provides O(1) victims when the working set churns; clocksweep amortises eviction.
- **Workloads helped**: all three — `select-only` reads accounts pages repeatedly (mostly cache-hit), the write workloads also churn `pgbench_history`.
- **goopg status**: ✅ implemented (M0098-0003, M0099-0002). `internal/storage/bufpool.go:180` declares `partitions [128]bufferPartition`; `Pool.Pin`/`TryPin` (line 923) hits the per-partition lock first; `evictLocked` (line 1541) performs eviction. The fast-path uses `RWMutex` + `atomic.Int32` for pin/usage counters (M0099-0002, lines 32–40, 195).

## 7. Lock-free `MarkBufferDirtyHint`

- **Upstream**: `postgres/src/backend/storage/buffer/bufmgr.c:5430,3058,3112,3313`. CAS loop on `bufHdr->state` avoids spinlock when setting `BM_DIRTY` for hint-bit updates.
- **What it does**: visibility hint bits (e.g. `HEAP_XMIN_COMMITTED`) get persisted lazily without taking the buffer content lock, eliminating a hot path bottleneck on first-after-commit reads.
- **Workloads helped**: `select-only` (hint-bit propagation after recent commits), `standard` (after history inserts commit, subsequent scans flip hints).
- **goopg status**: 🟡 partial. `bufpool.go:1156,1174` (`MarkDirty`, `MarkDirtyWithLSN`) update under the partition lock; the slot's pin/usage counters are atomic (M0099-0002) but the dirty-state transition is not lock-free.

## 8. ProcArray + lock-free `GetSnapshotData`

- **Upstream**: `postgres/src/backend/storage/ipc/procarray.c:2175` (`GetSnapshotData`); `ProcArrayLock` declared in `lwlocklist.h:37`.
- **What it does**: every transaction calls `GetSnapshotData` to compute its visibility horizon. Modern PG keeps the active xid array dense and walks it under a shared LWLock; with hot-standby simplifications (PG 14+) the cost is sub-microsecond on typical hardware.
- **Workloads helped**: all three (snapshot acquired per query / per transaction).
- **goopg status**: 🟡 partial. `internal/mvcc/manager.go` and `internal/mvcc/snapshot.go` provide snapshots, but the implementation has not been profile-tuned for the GetSnapshotData hot path; expect this to show up in `04-contention.md`.

## 9. Transaction ID assignment + CLOG SLRU bank locks

- **Upstream**: `postgres/src/backend/access/transam/varsup.c:77` (`GetNewTransactionId`), `xact.c:1315` (`RecordTransactionCommit`), `clog.c:304,308` (CLOG bank-lock grouping).
- **What it does**: xid allocation under `XidGenLock`; commit status written to CLOG under bank locks (each CLOG page is one bank — bank-lock granularity is page-level, not slot-level).
- **Workloads helped**: `standard`, `simple-update` (every txn allocates xid + writes commit status).
- **goopg status**: 🟡 partial. `internal/mvcc/clog.go` exists; bank-lock granularity not yet validated against the upstream layout. Profile this run to confirm.

## 10. HOT (Heap-Only Tuple) updates

- **Upstream**: `postgres/src/backend/access/heap/heapam.c:3242` (`heap_update`), `hio.c` (`HeapTupleSetHotUpdated`).
- **What it does**: when an UPDATE does not touch any indexed column and the new tuple version fits on the same page, PG threads the new version to the old via the `t_ctid` pointer chain and skips secondary index maintenance entirely.
- **Workloads helped**: `simple-update` and `standard` — pgbench UPDATE statements modify `abalance` (non-key) and `filler` columns; index on `aid` is untouched. HOT eliminates the btree insert per UPDATE.
- **goopg status**: ✅ implemented. `internal/storage/bufpool.go:132` references `logHeapHotUpdate` as an "atomic HOT-update WAL record"; the storage layer threads new tuples on-page. Profile data will reveal whether HOT engages at the same rate as PG.

## 11. B-tree lock coupling and rightmost-leaf optimization

- **Upstream**: `postgres/src/backend/access/nbtree/nbtsearch.c:107,887,1597` (`_bt_search`, `_bt_first`, `_bt_next`). Descent uses pin + content RLock per level; rightmost-only optimisations cut work for append-heavy patterns.
- **What it does**: latch-coupling (`crab`) walks the tree without holding a single global lock; modern PG further reduces traversal for append-mostly insertions.
- **Workloads helped**: `select-only` (index probe on `pgbench_accounts(aid)`), `standard` and `simple-update` (UPDATE looks up `aid`, INSERT appends to history index).
- **goopg status**: 🟡 partial. `internal/access/btree/btree.go` (`Search` line 1906, `Insert` line 1065) — latch-coupling not explicitly documented in this layer; descent is read-locked per level. Append-tail optimisation status unknown.

## 12. Plan caching (`RevalidateCachedQuery` / `BuildCachedPlan`)

- **Upstream**: `postgres/src/backend/utils/cache/plancache.c:94,97,667,1019`.
- **What it does**: when a backend reuses a prepared statement, the cached plan is checked for invalidation (relcache version, statistics) and reused if still valid; saves the parser + planner round-trip.
- **Workloads helped**: pgbench in `simple` query mode re-parses every statement, so this only helps if `-M prepared` or `-M extended` is used. With `simple` mode, every transaction pays for parse + plan.
- **goopg status**: 🟡 partial / not exercised. `internal/parser/parser.go` uses `tokenSlicePool` and `parserPool` (line 13, 22) but no plan cache. Plan-cache work would be additive; in this exercise pgbench's `simple` mode means it does not show up.

## 13. Per-CPU statistics counters

- **Upstream**: `pgstat_*` family (`postgres/src/backend/utils/activity/`); per-backend local counters flushed periodically.
- **What it does**: shared statistics (rows scanned, buffers hit) are accumulated per backend without taking a shared lock; the stats collector aggregates them on a slower cadence.
- **Workloads helped**: all (every statement nudges per-relation counters).
- **goopg status**: 🟡 partial. `internal/activity/` exists; verify it's not a single global counter.

## 14. Backend startup amortisation (persistent connections)

- **Upstream**: `postgres/src/backend/postmaster/postmaster.c:3536` (`BackendStartup`). pgbench by default holds connections open for the run.
- **What it does**: a forked PG backend is expensive (~10 ms) but pgbench creates 100 backends *once*, then reuses them for 180 s.
- **Workloads helped**: all three (mitigates per-run startup cost in the `initial connection time` line).
- **goopg status**: ✅ analogue — goopg's goroutine-per-connection model has zero fork cost. The trade-off is per-connection memory and GC scanning overhead, which surfaces in heap profiles.

## 15. WAL `commit_delay` / `commit_siblings`

- **Upstream**: `postgres/src/backend/access/transam/xlog.c` (commit-delay GUC).
- **What it does**: when many backends commit concurrently, intentionally delaying fdatasync by up to `commit_delay` µs grows the group-commit batch.
- **Workloads helped**: `standard`, `simple-update` under high `-c`.
- **goopg status**: ✅ implemented (M0098-0002). `internal/wal/writer.go:1048` references "PostgreSQL's commit_delay / commit_siblings semantics" in the group-commit drain loop.

## Summary scoreboard

| # | Optimization | goopg status | Expected impact on goopg if implemented |
|---|---|---|---|
| 1  | WAL insert lock striping (8 stripes) | 🟡 | Lifts `standard`/`simple-update` at high c |
| 2  | WAL group commit (waiter chain) | ✅ | Already realized |
| 3  | Lock manager partitioning (16) | ❌ | Likely highest single lift across all workloads |
| 4  | Fastpath relation locks | ❌ | Big lift on `select-only`; requires #3 first |
| 5  | LWLock + per-purpose arrays | 🟡 | Buffer side done; ProcArray/WAL not |
| 6  | Buffer manager partitioning (128) | ✅ | Already realized |
| 7  | Lock-free `MarkBufferDirtyHint` | 🟡 | Lifts `select-only` (hint-bit propagation) |
| 8  | ProcArray lock-free `GetSnapshotData` | 🟡 | Lifts all three |
| 9  | CLOG SLRU bank locks | 🟡 | Lifts write workloads |
| 10 | HOT updates | ✅ | Already realized; validate engagement |
| 11 | Btree latch coupling + rightmost | 🟡 | Lifts `select-only` index path |
| 12 | Plan cache | 🟡 | Not exercised by pgbench `simple` mode |
| 13 | Per-CPU statistics counters | 🟡 | Hidden cost; verify |
| 14 | Backend startup amortisation | ✅ (Go analogue) | Already realized |
| 15 | WAL commit_delay | ✅ | Already realized |

`08-recommendations.md` ranks these by expected lift / implementation cost using empirical profile data from `02-cpu-bottlenecks.md` and `04-contention.md`.
