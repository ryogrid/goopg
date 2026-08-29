# 03 — Contention

Block and mutex profiles from `R3-goopg-N-contention` and
`R4-goopg-S-contention`, captured with `GOOPG_BLOCK_PROFILE_RATE=1
GOOPG_MUTEX_PROFILE_RATE=1`. Measured profiling cost: −1.9 % TPS on `-N`,
−6.2 % on `-S`; the TPS from these runs is disclosed as perturbed and is not
used anywhere as a headline.

Read these as *rankings*, not absolute times: Go's mutex profile accumulates
delay across all 50 client goroutines, so totals exceed wall-clock.

## 1. `-S` select-only — one surface dominates

Mutex profile, total delay 1,715.19 s:

| frame | cum | share | |
|---|---:|---:|---|
| `executor.(*Context).acquireRelLockMaybeTransient` | 1,116.96 s | **65.12 %** | acquire path |
| ` └ executor.(*Context).acquireScanIndexReadLocksTxn` | 450.77 s | 26.28 % | *(subset of the row above)* |
| `executor.ReleaseTupleLocks` → `lmgr.(*LockManager).ReleaseAll` | 441.00 s | **25.71 %** | release path |
| `catalog.(*InMemory).RelationBlocks` | 40.71 s | 2.37 % | |
| `executor.(*OpIterator).Next` | 24.88 s | 1.45 % | |

**The acquire path is 65.12 % of read-path mutex delay and the release path a
further 25.71 % — together 90.8 %, all through one mutex.** Note the nesting:
`acquireScanIndexReadLocksTxn`'s 450.77 s flows 100 % into
`acquireRelLockMaybeTransient`, so it is a component of the 65.12 %, not an
addition to it. Block profile agrees on the ordering: `opOpen` →
`indexScanOp.openPrep` → `acquireScanReadLockTxn` →
`acquireRelLockMaybeTransient` is 7.29 % of block delay.

The wait-event sampling agrees **quantitatively**, not merely in direction: the
mutex profile's 1,715.19 s of delay over 50 goroutines × 180 s of backend wall
time is **19.06 %**, against sampling's **19.9 %** of backend samples in
`Lock:relation` ([01 §3](01-results.md)) — two unrelated instruments landing
within a point of each other.

### Root cause

`internal/storage/lmgr/lockmgr.go:310-314`:

```go
type LockManager struct {
	mu              sync.Mutex
	states          map[LockTag]*lockState
	deadlockTimeout time.Duration
}
```

**One process-global mutex and one map for every lock in the system.** There is
no analogue of PostgreSQL's `NUM_LOCK_PARTITIONS = 16`
(`postgres/src/include/storage/lwlock.h:96-97`), let alone its per-backend fast-path
array.

The production instance is `var tableLockMgr = lmgr.New()`
(`internal/executor/context.go:1357`). Every statement reaches it through
`Context.acquireRelLockMaybeTransient` (`context.go:1603-1631`), which does
`AcquireWithTimeout` and then **immediately releases** — two global-mutex
acquisitions plus a map insert and delete, per relation *and per index*, per
statement. `acquireScanIndexReadLocksTxn` (`context.go:1495`) repeats it for
each index.

### What PostgreSQL does

PG never touches a shared hash table for this case. `LockAcquireExtended`
(`postgres/src/backend/storage/lmgr/lock.c`) routes weak relation locks into the
backend's own fast-path array via `FastPathGrantRelationLock`
(`lock.c:2750`), which is backend-private memory guarded only by that backend's
own `MyProc->fpInfoLock`. Eligibility is the macro at `lock.c:267-272`:

```c
#define EligibleForRelationFastPath(locktag, mode) \
	((locktag)->locktag_lockmethodid == DEFAULT_LOCKMETHOD && \
	(locktag)->locktag_type == LOCKTAG_RELATION && \
	(locktag)->locktag_field1 == MyDatabaseId && \
	MyDatabaseId != InvalidOid && \
	(mode) < ShareUpdateExclusiveLock)
```

i.e. every mode below `ShareUpdateExclusiveLock` — which covers
`AccessShareLock`, `RowShareLock` and `RowExclusiveLock`, the three modes a
pgbench transaction ever needs.

Three details an implementer must not get wrong. The fast-path array is **not
backend-private memory**: it lives in shared memory referenced from PGPROC
(`postgres/src/include/storage/proc.h:82-84`, `:308-310`) and is read and stolen
by *other* backends via `FastPathTransferRelationLocks` /
`FastPathGetRelationLockEntry`. Even the fast path reads the shared
`FastPathStrongRelationLocks->count[]` (`lock.c:999`). And the shared tables are
still used for shared catalogs, for backends not bound to a database, and when
the relation's **16-slot fast-path group is full** (`FP_LOCK_SLOTS_PER_GROUP`,
`lock.c:987`; 64 slots per backend at the default `max_locks_per_transaction`).
What the fast path avoids is the shared **hash table**, not shared memory. The shared `LOCK`/`PROCLOCK`
hash tables are consulted only when a strong lock is present on the relation
(tracked by `FastPathStrongRelationLocks`). For a pgbench `SELECT`, PG's
relation-lock cost is a handful of local array writes and zero shared
contention — which is precisely why `P2-pg-S` records **zero** `Lock:relation`
samples.

## 2. `-N` simple-update — the lock manager again, then CLOG

Mutex profile, total delay 1,052.88 s:

| frame | cum | share |
|---|---:|---:|
| `lmgr.(*LockManager).ReleaseAll` | 388.53 s | **36.90 %** |
| `executor.(*Context).acquireRelLockMaybeTransient` → `lmgr.acquire` | 181.62 s | **17.25 %** |
| `transam.(*CLog).GetStatus` → `clogBufferPool.getStatus` | 146.00 s | **13.87 %** |
| ` └ transam.Snapshot.SeesCommittedXID` / `clogSaysNotAborted` | 126.70 s | 12.03 % *(subset of the row above)* |
| `xlog.(*Writer).FlushUpTo` → `flushUpToBackend` | 30.37 s | 2.88 % |
| `transam.(*CLog).setStatusWithLSN` | 22.16 s | 2.10 % |

(The `ReleaseAll` and `acquire` rows are the same mutex; `ReleaseTupleLocks`
20.38 % and `ReleaseTableLocks` 16.53 % are the callers that funnel into
`ReleaseAll`.)

**The lock manager is ~54 % of write-path mutex delay too** — and `ReleaseAll`
(`lockmgr.go:569`) is the worse half, because it **iterates the entire `states`
map under `mu`**, and it fires roughly **six times per `-N` transaction**:
`ReleaseTupleLocks` (`internal/executor/context.go:1381`) runs per Query message
(five for `-N`, `dispatch.go:336`) and `ReleaseTableLocks` (`context.go:1364`) at
transaction end (`conn_tx.go:340`, `:558`).

### Second surface: the CLOG global mutex on every visibility check

`internal/access/transam/clog_bufferpool.go:133-139` guards the whole CLOG
buffer pool with one `sync.Mutex`, and `getStatus` (`clog_bufferpool.go:302`)
takes it for a page lookup on **every tuple visibility test**.

The reach is structural. `internal/access/transam/visibility.go:98-106` calls
`snap.SeesCommittedXID(h.Xmin)` on **both** branches of the `HeapXminCommitted`
hint-bit test — the hint bit does not short-circuit the consult — and
`Snapshot.SeesCommittedXID` (`snapshot.go:199`) places the CLOG lookup **before**
the `xid < s.Xmin` fast exit (`snapshot.go:222`):

```go
if !s.clogSaysNotAborted(xid) { return false }
if xid < s.Xmin { return true }
```

That ordering is deliberate — it fixed a torn-pgbench-transaction visibility bug
(M0131-S30.7) — so **this is load-bearing correctness, not an oversight.** Any
fix must preserve the invariant rather than reorder the check.

**PostgreSQL does not consult CLOG at all on this path.** When the
`HEAP_XMIN_COMMITTED` hint bit is set, `HeapTupleSatisfiesMVCC` takes this
branch (`postgres/src/backend/access/heap/heapam_visibility.c:1076-1082`):

```c
else
{
	/* xmin is committed, but maybe not according to our snapshot */
	if (!HeapTupleHeaderXminFrozen(tuple) &&
		XidInMVCCSnapshot(HeapTupleHeaderGetRawXmin(tuple), snapshot))
		return false;		/* treat as still in progress */
}
```

`XidInMVCCSnapshot` is a lock-free scan of the snapshot's own in-memory `xip`
array — **no shared state, no lock, no SLRU**. `TransactionIdDidCommit` is
reached only on the *not-yet-hinted* branch (`heapam_visibility.c:1065`), and
even there it is fronted by a single-entry cache (`cachedFetchXid`,
`postgres/src/backend/access/transam/transam.c:33-62`) and by CLOG SLRU **bank
locks** (`SimpleLruGetBankLock`,
`postgres/src/backend/access/transam/clog.c:305`), never one global lock.

So the divergence is larger than "goopg's CLOG lock is unpartitioned": on a
steady-state pgbench scan, where hint bits are set, **PostgreSQL performs an
in-memory array check and goopg takes a process-global mutex.** The source
comment at `snapshot.go:218-221` claims this ordering mirrors upstream; that is
true of the unhinted branch only.

### Third: WAL flush — small, and expected

`FlushUpTo` is only **2.88 %** of mutex delay and 19.38 % of *block* delay. That
is the group-commit wait, and it is the correct place for a
commit-flush-bound workload to spend time; PostgreSQL's own profile is 63.5 %
`LWLock:WALWrite` for the same reason. See
[04-wal-persistence.md](04-wal-persistence.md).

## 3. Block profile shape

Both workloads are dominated by `runtime.selectgo` (82.8 % of `-S` block delay,
93.2 % of `-N`). This is **not** a finding: it is idle goroutines parked in
`select` — the per-connection context watcher (`internal/postmaster/server.go:926-936`)
and the per-statement EOF watcher (`internal/postmaster/eof_watch.go:65`), plus
background tickers. It should be filtered out, not optimised, when reading these
profiles. The meaningful block-profile rows are the `sync.Mutex.Lock` entries
(10.1 % `-S`, 3.9 % `-N`) and the named call chains above.

## 4. Surfaces checked and found clean

Worth recording so future studies do not re-investigate them:

- **`activity.Registry`** — the per-backend slot rewrite holds. Atomic-only hot
  path (`internal/utils/activity/registry.go:278-311`); it was ~91 % of mutex
  delay at c=100 before the fix and does not appear in either profile now.
- **`internal/access/transam` ProcArray** — no global lock; `captureSnapshot` shows as CPU
  (2.97 % on `-N`, mostly the 1024-slot scan and its `sort.Slice`) rather than
  contention.
- **Buffer pool** — `bufmap` lookup is lock-free and `Pool.Pin` is a single CAS;
  `pinSlow`/`pinMu` does not surface at scale 100 (it was the top item at scale
  500 in `analysis/perf-optimize3-dash/07-scale500-analysis`, a different regime).
- **`runtime.Stack`** — the 57 %-of-CPU regression from `analysis/perf-optimize2`
  stays fixed; `gls.BackendID()` (`internal/port/gls/gls_linkname.go:87`) is
  allocation-free and `runtime.Stack` appears nowhere. Note the documented
  silent-fallback cliff: the linkname path is build-tagged
  `go1.24 && !go1.27 && !noLinkname`, and this host runs go1.26.3 — **one Go minor release from
  falling back** to the `runtime.Stack` path.
- **WAL insert striping** — `LWLock:WALInsert` is 0.3 % of `-N` samples. The
  8-stripe design is doing its job; the `insertPosTracker.posMu` serialization
  point (`internal/access/transam/xlog/insert_pos.go:60`) is not currently a
  measurable bottleneck at 50 clients.
