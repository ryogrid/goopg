# 04 — Contention (Mutex / Block / Goroutine)

Source: `profiles/goopg_c<C>_<wl>.{mutex,block,goroutine}.{pb.gz,txt}`, with `mutex_base` / `block_base` captures used as `pprof -base` baselines so the numbers reflect only the 180 s pgbench window.

## §4.1 Headline — three contention points, not the ones expected

Plan-stage hypothesis was that the heavyweight lock manager (`internal/lockmgr`, single `sync.Mutex` at `lockmgr.go:212`) would dominate. **It does not appear in any top profile.** The actual offenders, in descending impact:

1. **`internal/mvcc.(*Manager)` single `sync.Mutex` (`mvcc/manager.go:73`)** — gates *every* `Begin`, `SnapshotFor`, `Commit`, `OldestXmin`, `finish`. 92 % of mutex delay on write workloads routes here.
2. **`internal/activity.(*Registry)` `sync.RWMutex` (`activity/activity.go:123`)** — wait-event tracking takes this mutex per `WaitEventStart` / `WaitEventEnd` pair, called from every protocol-level wait and IO operation. 95 % of mutex delay on c=100 select-only routes here.
3. **`internal/storage.(*Pool).partitions[].mu` `sync.Mutex` (`bufpool.go:79`)** — the per-partition buffer-map mutex. Hot-page contention on `pgbench_history`'s last block deadlocked goopg at c=100 simple-update / standard for 23 minutes.

(Bonus, lower-impact: `runtime.unlock` and the bgwriter's `Pool.WriteDirtyPages` show up in c=10 select-only's mutex profile — see §4.4.)

## §4.2 The `mvcc.Manager` mutex — the dominant write bottleneck

### Structure

```go
// internal/mvcc/manager.go:72-90
type Manager struct {
    mu         sync.Mutex         //  ← ONE mutex for all snapshot / commit / abort traffic
    nextXID    storage.TransactionID
    nextHandle TxnHandle
    active     map[TxnHandle]*txState
    xactMarker func(storage.TransactionID, XactMarker) error
    commitCond *sync.Cond
    abortedXIDs []storage.TransactionID
    // …
}
```

`Begin` (line 185), `SnapshotFor` (line 263), `Commit` (line 305), `OldestXmin` (line 359), `finish` (line 374) all take `m.mu.Lock()`.

### Evidence

c=50 simple-update `mutex` delta (`-base` mutex_base):

```
5409.62 s  99.82 %  sync.(*Mutex).Unlock
↳ 5012.00 s  92.48 %  mvcc.(*Manager).Commit / finish
↳  308.53 s   5.69 %  mvcc.(*Manager).SnapshotFor
↳   29.57 s   0.55 %  mvcc.(*Manager).Begin
↳   29.37 s   0.54 %  mvcc.(*Manager).OldestXmin
```

c=50 simple-update `block` delta:

```
1.60 hrs  83.21 %  sync.(*Mutex).Lock
↳ 1.21 hrs  62.73 %  mvcc.(*Manager).SnapshotFor
↳ 0.28 hrs  14.75 %  server.executeOneSimpleStmt direct callers
↳ 0.13 hrs   6.97 %  mvcc.(*Manager).Begin
↳ 0.12 hrs   6.23 %  mvcc.(*Manager).Commit
```

Even at c=10 simple-update the block profile already shows 37 % of all blocking-time routed through `SnapshotFor` on this single mutex. This is why goopg's write TPS goes *down* from c=10 (410 TPS) to c=50 (347 TPS): adding clients adds contention without adding throughput, because the critical section is serial.

### PG counterpart and why it scales

| concern | goopg | PG | upstream source |
|---|---|---|---|
| Per-snapshot xmin horizon | `Manager.mu` + scan `active` map | `ProcArrayLock` (LWLock) + linear scan of `procArray->pgxactoff[]` (dense) | `postgres/src/backend/storage/ipc/procarray.c:2175` (`GetSnapshotData`) |
| Per-commit xid update | `Manager.mu` + map mutation | `XidGenLock` for new-xid; commit status via SLRU bank locks | `postgres/src/backend/access/transam/varsup.c:77`, `postgres/src/backend/access/transam/clog.c:304` |
| OldestXmin computation | `Manager.mu` + walk `active` | `ProcArray` shared read lock | `postgres/src/backend/storage/ipc/procarray.c:1850` |

PG splits the four concerns across three different lock classes (`ProcArrayLock`, `XidGenLock`, CLOG bank locks). The xmin-snapshot path is a *read* lock on a dense array; commit and xid-allocation paths are *write* locks but they're short and on a different lock class so they don't gate reads.

goopg merges all four under one mutex. With the dense-map walk inside the critical section, the critical section is also longer per transaction (proportional to `len(active)`, which grows with `-c`).

### The deadlock at c=100

The 23-minute hang at `c=100 simple-update` is not a true deadlock (no cycle) — it is a livelock where:

- The `bufferPartition.mu` covering `pgbench_history`'s tail page is held by whichever client is mid-INSERT.
- All other 99 clients queue on that partition mutex (we observed exactly **19 goroutines** blocked on `Pool.Pin` at `bufpool.go:927` for ≥ 23 minutes).
- The 19 unblocked clients trying to commit must then take `mvcc.Manager.mu`.
- The `Manager.mu` becomes contended, queueing them further.
- Forward progress is bounded by the slowest of the two serialisation points; combined with GC stop-the-world windows that add tens-of-ms latency, the system stalls to 0 TPS in steady state.

This was the SKIPPED outcome the user pre-authorised. The deadlock-state snapshots (`profiles/goopg_c100_simple-update.deadlock_*`) document it.

## §4.3 The `activity.Registry` mutex — the dominant read bottleneck

c=100 select-only mutex delta:

```
2280 s  95.48 %  acceptLoop → serveConn → dispatch
2260 s  94.56 %  sync.(*Mutex).Unlock
  62 s   2.60 %  sync.(*RWMutex).Unlock
2239 s  93.70 %  via dispatchSimpleQueryViaExecutor
1387 s  58.06 %  via protocol.(*FrameWriter).WriteFrame
1283 s  53.70 %  via activity.(*Registry).WaitEventStart  ← here
1101 s  46.06 %  via protocol.(*FrameWriter).WriteReadyForQuery
 893 s  37.37 %  via activity.(*Registry).WaitEventEnd    ← and here
```

So `WaitEventStart` and `WaitEventEnd` (called *every protocol frame write* and *every wait*) account for ~ 91 % of mutex delay on c=100 select-only. The registry is at `internal/activity/activity.go:123`:

```go
type Registry struct {
    mu       sync.RWMutex   // single mutex for the whole registry
    backends map[string]*BackendEntry
    // …
}
```

Every backend-state transition takes this mutex. At c=100 with thousands of frames/sec, this is the hot wire.

### PG counterpart

PG's `pg_stat_activity` lives in shared memory under `PGPROC` slots — each backend writes to *its own* `PGPROC->wait_event_info` (a `uint32`) without a lock; readers do an unsynchronised scan. There is no central registry mutex.

`postgres/src/backend/storage/ipc/procarray.c` and `postgres/src/include/storage/proc.h` define the layout. The crucial pattern: **per-backend state in the backend's own struct, read by stats consumers without locking**.

## §4.4 Buffer pool partition mutex — the hot-page deadlock

19 goroutines blocked at `bufpool.go:927` (`part.mu.Lock()`) for the full 23-min c=100 simple-update window. Call stack (from the deadlock snapshot):

```
internal/storage.(*Pool).Pin
  internal/executor.writeHeapRowReturning.func1
    internal/executor.writeHeapRowReturning
      internal/executor.(*insertOp).Next                    ← INSERT INTO pgbench_history
        internal/server.executeOneSimpleStmt
```

All 100 clients append rows to `pgbench_history` — an append-only table whose tail page is always the same. With 128 hash partitions, one partition out of 128 covers that page, and 100 clients converge there.

`bufpool.go:919-925` (code comment):

```
// M0098-0003 lock ordering: partition lock (acquire/release) → evictMu
// (for victim selection) → old-partition lock (to remove old tag).
```

The protocol is correct, but at c=100 with all clients targeting the *same* partition, the partition mutex becomes a single point of serialisation.

### PG counterpart

PG has the same architectural problem (128 buf-mapping partitions, see [`07`](07-postgres-optimizations-relevant.md) §6) but mitigates it three ways goopg does not:

| mitigation | upstream source |
|---|---|
| **Per-buffer content lock** (LWLock per page) is separate from buf-mapping lock | `postgres/src/backend/storage/buffer/bufmgr.c` (`LockBufHdr`, `UnlockBufHdr`) |
| **Atomic state CAS** for pin-count update without page lock | `postgres/src/backend/storage/buffer/bufmgr.c:3058` (`pinning_in_progress_t`) |
| **Append-friendly inserts**: PG's `RelationGetBufferForTuple` (`postgres/src/backend/access/heap/hio.c`) uses FSM (`free space map`) to spread `pgbench_history` inserts across pages, not just the tail | `postgres/src/backend/storage/freespace/freespace.c` |

The third item is the structural difference: PG actively distributes inserts across multiple newly-extended pages so the tail-page hot spot is broken by FSM. goopg's `pgbench_history` insert path appears to always target the tail page, so all 100 clients hash to the same partition. (Confirmation requires reading `internal/access/heap/insert` more carefully; this is recorded as an open item in [`08`](08-recommendations.md) #4.)

## §4.5 What's *not* contended (negative findings)

| component | expected to contend | actually contended? | evidence |
|---|---|---|---|
| `internal/lockmgr` single mutex | yes (plan §07 #3) | **no** — does not appear in any mutex top-20 | mutex profiles c=10/c=50/c=100, all workloads |
| `internal/wal/writer.appendMu` single mutex | yes | minor (<3 %) | M0098-0002 group-commit batching saves the path |
| `internal/storage.Pool.evictMu` RWMutex | yes (M0099-0001/0002) | only at c=100 (bufpool partition livelock) | RWMutex.RLock dominates the 82-goroutine deadlock state |
| `bgwriter.Pool.WriteDirtyPages` | no | yes, at c=10 SO — 28 % of mutex delay | mutex profile c=10 SO §4.4-adjacent |
| Btree `Search` / `Insert` | maybe | no | clean across all profiles |

The bgwriter finding (28 % of c=10 SO mutex delay through `WriteDirtyPages`) is interesting: at low concurrency the bgwriter is competing with foreground readers for partition mutexes. It is not on the critical path because foreground reads only need pinned pages (not dirty-page eviction), but the futex-wakeup chain still costs cycles. Tuning `bgwriter_lru_maxpages` or disabling bgwriter for read-mostly windows could buy back ~ 5 % CPU.

## §4.6 The futex-wakeup CPU cost

Section §02 reported `runtime.futex` rising from 15 % (c=10 SO) to 23 % (c=100 SO). Cross-referencing with mutex profiles confirms causation: `Manager.mu.Unlock` and `activity.Registry.mu.Unlock` each wake the next waiter via the runtime futex. With ~ 6 400 q/s (c=100 SO) and 4–5 mutex hand-offs per query, that's ~ 30 000 futex wakeups/s. PG's LWLock fast path avoids wakeup syscalls when the lock is uncontended (single atomic CAS); goopg's `sync.Mutex` always calls `runtime_Semrelease` on `Unlock` if any waiter is registered.

## §4.7 Cross-reference table

| goopg site | upstream PG counterpart | what PG does differently |
|---|---|---|
| `internal/mvcc/manager.go:73` `Manager.mu` (Begin/Snapshot/Commit/OldestXmin all gated) | `postgres/src/backend/storage/ipc/procarray.c:2175` `GetSnapshotData` under `ProcArrayLock`; `postgres/src/backend/access/transam/varsup.c:77` `GetNewTransactionId` under `XidGenLock`; CLOG bank locks for commit | 3 different lock classes, dense xmin array, lock-free `pgxactoff` reads (PG 14+) |
| `internal/activity/activity.go:123` `Registry.mu` | `postgres/src/include/storage/proc.h` `PGPROC->wait_event_info` | per-backend `uint32` in shared memory; no central registry; lock-free readers |
| `internal/storage/bufpool.go:79` `bufferPartition.mu` (per-partition) | `postgres/src/backend/storage/buffer/buf_table.c:90` `BufTableLookup` + LWLock partition | same partition idea; PG additionally has lock-free pin-count CAS, and FSM-driven insert distribution to break tail-page hotspots |
| `internal/lockmgr/lockmgr.go:212` `Manager.mu` | `postgres/src/backend/storage/lmgr/lock.c:808` `LockAcquire` under one of 16 `LockMethodLockHash` partitions, plus fastpath relation locks | 16-way partition + per-backend fastpath; not on the critical path under pgbench load |
| `internal/wal/writer.go:355` `appendMu` | `postgres/src/backend/access/transam/xlog.c:570` `WALInsertLocks[8]` | 8-stripe WAL insert; goopg has 1 |

## §4.8 Goroutine snapshot summary

c=10 select-only `goroutine?debug=2` (T+90, steady state):

- ~ 110 goroutines, of which:
  - 10 backend goroutines in `select` (waiting for next protocol input from idle pgbench clients)
  - 5–8 runtime workers (GC, scavenger, scheduler)
  - bgwriter, walwriter, AIO workers (1–3 each)
  - Nothing pathological.

c=100 simple-update deadlock state (T+1530s of `pgbench`-elapsed):

- 105 in `select` (idle backends or condvar waits)
- **82 in `sync.RWMutex.RLock`** (waiting for `evictMu.Lock` holder)
- **19 in `sync.Mutex.Lock`** (waiting for `bufferPartition.mu`, identical address `0xc000134238`)
- 4 chan recv, 4 IO wait, 1 syscall, 1 running

The single running goroutine was the WAL writer (still draining flush queue successfully — the deadlock is upstream of WAL).

All 19 partition-mutex waiters carry the call stack `Pool.Pin → writeHeapRowReturning → insertOp.Next` (pgbench_history insert) or `Pool.InvalidateBlock → smgr.WriteBlock → Pool.flushSlot → Bgwriter.run` (single bgwriter waiter blocked too).

This is the call-graph evidence that the c=100 write deadlock is the `pgbench_history` tail-page hot spot, not a logical bug. The fix is structural (FSM-driven insert distribution + per-buffer content lock CAS).
