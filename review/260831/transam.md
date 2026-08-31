# Transam (clog / mvcc / snapshot / ssi / subxact) — Code Review 2026-08-31

Files:
- internal/access/transam/clog.go
- internal/access/transam/clog_bufferpool.go
- internal/access/transam/clog_statuscache.go
- internal/access/transam/combocid.go
- internal/access/transam/manager.go
- internal/access/transam/partition_detach_epoch.go
- internal/access/transam/predlock.go
- internal/access/transam/procarray.go
- internal/access/transam/snapshot.go
- internal/access/transam/ssi.go
- internal/access/transam/ssi_conflict.go
- internal/access/transam/ssi_precommit.go
- internal/access/transam/subxact.go
- internal/access/transam/subxact_slru.go
- internal/access/transam/subxact_visibility.go
- internal/access/transam/visibility.go
- internal/access/transam/xidgen.go
- internal/access/transam/multixact/multixact.go
- internal/access/transam/multixact/store.go
- internal/access/transam/control/control.go
- internal/access/transam/control/pgcontrol.go

Findings count: 14

---

### `manager.go:captureSnapshot` — Full copy of `abortedXIDs` on every snapshot capture
- **Issue**: `captureSnapshot` copies the entire `m.abortedXIDs` slice (allocation + memcpy, manager.go:1371-1374) into every snapshot. `SnapshotFor` calls `captureSnapshot` on *every* statement for READ COMMITTED, so this is O(len(abortedXIDs)) work + a heap allocation per statement. `abortedXIDs` only ever grows (append-only via `insertSortedXID`), so the cost increases over the manager's lifetime. RR/SSI transactions pay it again on every statement via `Snapshot.Clone` (see the snapshot.go finding).
- **Why**: The copy protects snapshots from `insertSortedXID`'s in-place right-shift (`s = append(s,0); copy(s[idx+1:], s[idx:])`, manager.go:1397-1399) which can write inside the existing backing array while a concurrent reader iterates it.
- **Suggestion**: Make `abortedXIDs` copy-on-write: `insertSortedXID` allocates a fresh slice whenever it inserts (never mutate an array a snapshot may reference), then `captureSnapshot` can alias the slice (or a monotonically-versioned snapshot) with no per-statement copy. As a cheaper partial fix, compact/evict entries below `OldestClogXid()`/`oldestXmin` periodically so the list doesn't grow unbounded and the per-statement copy stays small.
- **Severity**: medium

### `manager.go:SnapshotFor` — Deep-`Clone()` of the pinned snapshot on every RR/SSI statement
- **Issue**: For REPEATABLE READ / SERIALIZABLE, `SnapshotFor` returns `s.firstSnap.Clone()` (manager.go:475), which allocates + copies both the `InProgress` and `Aborted` arrays on *every* statement of the transaction. The pinned snapshot is immutable after first capture (never mutated), so the deep copy buys nothing.
- **Why**: `Clone`'s doc says it exists so callers "can hold it independently from manager internals"; but `firstSnap` is never written after being pinned, so a value copy (sharing the slice backing arrays) is safe.
- **Suggestion**: Return `*s.firstSnap` (a value copy sharing the immutable arrays) instead of `Clone()`. Keep `Clone` for the rare caller that genuinely needs to detach.
- **Severity**: medium

### `manager.go:WaitForXID / WaitForSlotsToCommit` — A goroutine + channel spawned per wait
- **Issue**: Each `WaitForXID` (manager.go:847-857) and `WaitForSlotsToCommit` (manager.go:1069-1081) call spawns a dedicated `go func()` with a `done` channel whose only job is to broadcast on `ctx.Done()` (and to close on the lock-timeout timer for the latter). Under contention (many upsert/row-lock waiters) this is a goroutine + channel pair per blocked wait.
- **Why**: The goroutine is only a wake-up bridge from a cancelled context / timer to the `sync.Cond`.
- **Suggestion**: Use `context.AfterFunc(ctx, func(){ m.commitCond.Broadcast() })` (Go 1.21+) and `time.AfterFunc` for the lock-timeout, dropping the hand-rolled goroutine, `done` channel and `defer close(done)` bookkeeping entirely.
- **Severity**: low

### `manager.go:xidActiveWithSubxact / xidInProgress` — Full proc-array scan (up to 1024 atomic loads) per check, repeated in wait loops
- **Issue**: `xidInProgress` (manager.go:1110-1117) linearly scans all `DefaultProcArraySize` (1024) slots per call, and `xidActiveWithSubxact` (manager.go:1143-1159) can invoke it twice plus `xidWaitersReleased` (RLock + map probe) plus `TopLevelXid`/`IsAborted`. `WaitForXID` re-runs `xidActiveWithSubxact` on every `commitCond` wake, so a wait that wakes N times costs N×O(1024) atomic loads.
- **Why**: No secondary index of "active XID → slot" is maintained; liveness is re-derived by brute force.
- **Suggestion**: Maintain a lock-free active-XID lookup (e.g. a small sharded map of xid → procNum, updated on AssignXID/finish) or short-circuit with a fast "no subxact" gate (nParents) before the second scan. At minimum, hoist the `xidWaitersReleased` probe so it isn't done twice per loop iteration.
- **Severity**: low

### `clog.go:DidCommit` — Allocates a `visited` map on every call, even when never needed
- **Issue**: `DidCommit` (clog.go:244-273) does `visited := make(map[storage.TransactionID]struct{})` at entry regardless of outcome. For the overwhelmingly common terminal cases (Committed / Aborted / Unknown resolved in one iteration) the map is never read but still allocates.
- **Why**: The map is only a cycle guard for corrupt self/cyclic `pg_subtrans` parent chains.
- **Suggestion**: Use a bounded-hop loop (e.g. 64 iterations, as `TopLevelXid` already does) and only fall back to an allocated visited set on excessive depth; or lazily allocate the map on first `SubCommitted` hop.
- **Severity**: low

### `clog.go:firstRetrainedSLRUXID` — Re-implements `parseSLRUSegName` inline
- **Issue**: `firstRetainedSLRUXID` (clog.go:392-408) hand-rolls the 4-hex-digit segment-name parser that `parseSLRUSegName` (clog.go:481-500) already provides, and `highestSLRUXID`/`truncateSLRUSegments` use their own variants too. Four copies of the same hex decoder.
- **Why**: Pure duplication; `parseSLRUSegName` was extracted specifically to be shared.
- **Suggestion**: Call `parseSLRUSegName` from `firstRetainedSLRUXID` (and `truncateSLRUSegments`) — one fewer copy, and any future parser fix applies everywhere.
- **Severity**: low

### `clog_bufferpool.go:evictVictimLocked` — O(nslots) full LRU scan per page fault
- **Issue**: Every cache miss that must evict runs a full linear scan of all resident slots (`evictVictimLocked`, clog_bufferpool.go:206-216). With `transaction_buffers` auto-tuned to hundreds/1024 of resident pages, each fault-in that evicts is O(resident).
- **Why**: Classic all-slots LRU; PG uses a clock-sweep hand for the same reason.
- **Suggestion**: A clock/CLOCK-pro hand (rotate a "next" index, scan only until a stale slot) bounds eviction cost well below O(nslots). Low priority since faults are rarer than hits, but it's a cheap, well-understood improvement.
- **Severity**: low

### `multixact/store.go:Members` — Allocates + copies the member slice on every call, and takes the global mutex
- **Issue**: `Members` (store.go:112-122) locks the store mutex and returns a fresh `make+copy` of the member slice each call. It is invoked from the visibility hot path (`TupleVisible` / `TupleVisibleSubxact`, visibility.go:141 / subxact_visibility.go:373) for every tuple whose xmax carries `HEAP_XMAX_IS_MULTI`, so multi-xmax scans pay a mutex + allocation + copy per tuple.
- **Why**: The copy documents immutability of the stored set.
- **Suggestion**: Return the stored slice directly (the store guarantees it is never mutated after insertion) and document the immutability contract, dropping the per-call allocation; or keep a per-multi cached decode. Consider `sync.RWMutex` or an `atomic.Pointer` snapshot of the map for read concurrency.
- **Severity**: medium

### `multixact/store.go:CreateFromMembers` — Sorts/copies before the dedup lookup
- **Issue**: `CreateFromMembers` calls `sortedMembers` (copy + `sort.Slice`, store.go:160/228-238) to build the `bySet` key *before* checking whether the set already exists. The common lock pattern (the same two transactions locking many tuples) hits the dedup path, yet still pays an allocation + sort each time.
- **Why**: The canonical key requires sorted members.
- **Suggestion**: On the already-sorted/membership fast path, probe `bySet` with an alternative unsorted identity (e.g. a composite hash) before paying the sort; or accept the caller's slice for sorting when it's disposable. Lower value while multixact is engine-only, but worth it before the write path goes live.
- **Severity**: low

### `ssi_conflict.go:coveringPredicateLockTags` — Heap slice allocation per conflict-in check
- **Issue**: `coveringPredicateLockTags` (ssi_conflict.go:775-793) allocates a 1-3 element `[]PredicateLockTag` on every `CheckForSerializableConflictIn` call (per SERIALIZABLE write).
- **Why**: Callers only iterate the result linearly.
- **Suggestion**: Return a fixed `[3]PredicateLockTag` + length, or iterate the three cases inline at the single call site, to avoid the per-write allocation.
- **Severity**: low

### `subxact_slru.go:SetParent / GetParent` — Open/stat/truncate/write/fsync (or open/read) per XID under a global mutex
- **Issue**: `SetParent` (subxact_slru.go:93-134) performs open + stat + truncate + write + fsync for every subxact registration, serialised by one mutex; `GetParent` (subxact_slru.go:141-173) does an open + read per XID. Each subxact write-path savepoint pays a synchronous fsync.
- **Why**: Mirrors clog's `mirrorToSLRUUnlocked` structure but PG batches pg_subtrans writes through an SLRU buffer and only flushes at checkpoint.
- **Suggestion**: Buffer parent-link writes in an in-memory page cache and flush lazily (checkpoint/eviction), like the CLOG pool; at minimum batch the fsync (one per segment per commit) instead of one per XID. `GetParent` is currently test-only, so it can stay until the G5 read path goes live.
- **Severity**: low

### `subxact_visibility.go:RestoreFromSLRU / Truncate` — `nParents.Store` inside the loop
- **Issue**: Both `RestoreFromSLRU` (subxact_visibility.go:121) and `Truncate` (subxact_visibility.go:151) call `m.nParents.Store(int64(len(m.parents)))` inside the per-entry loop, re-storing the same monotonically-growing value on every iteration — O(n) atomic stores where one suffices.
- **Why**: Only the final length matters for the atomic fast-path gate.
- **Suggestion**: Hoist a single `m.nParents.Store(...)` after each loop.
- **Severity**: low

### `subxact_visibility.go:isCurrentTxXID` — Multiple RLock acquisitions per tuple on the subxact hot path
- **Issue**: `isCurrentTxXID` (subxact_visibility.go:420-456) may take up to three separate `RLock`+`RUnlock` pairs (IsSubxact, TopLevelXid, IsAborted) and is called twice per tuple in `TupleVisibleSubxact` (for xmin and xmax). For OLTP workloads running inside savepoints this is repeated mutex traffic on the per-tuple path.
- **Why**: The `nParents` atomic fast path handles the common no-subxact case; the subxact-in-use case falls through to mutex-guarded map walks.
- **Suggestion**: Take a single `RLock` across the whole `isCurrentTxXID` resolution (callers hold one snapshot of the resolver state), or resolve top-level XID once per statement and reuse. Minor until savepoint-heavy OLTP is a benchmark target.
- **Severity**: low

### `clog_bufferpool.go:readPageFromDisk` — Manual zero-fill loop (minor)
- **Issue**: `readPageFromDisk` zeroes the page with `for i := range buf { buf[i] = 0 }` (clog_bufferpool.go:186-188).
- **Why**: `clear(buf)` (Go 1.21+) is the idiomatic, guaranteed-memclr form of exactly this.
- **Suggestion**: `clear(buf)`. Purely cosmetic; modern compilers already lower the loop to memclr.
- **Severity**: low

---

## Files with no significant findings

- **clog_statuscache.go** — already aggressively optimized (single-word atomic slot, terminal-only caching, power-of-two mask, bulk-invalidate).
- **combocid.go** — hash + array lookup, no redundancy.
- **partition_detach_epoch.go** — trivial atomic counter.
- **predlock.go** — cold SERIALIZABLE path; linear scans are bounded by per-xact lock counts; `largestRelationFootprintLocked` map build is acceptable for the coarsening event.
- **procarray.go** — fixed 64-byte slot layout, no waste.
- **visibility.go** — hot path is tight (hint-bit fast paths, early lock-only shortcut); no redundant work found.
- **xidgen.go** — lock-free, minimal.
- **ssi.go** — `purgeFinishedSerializableLocked` is O(active+finished) but only on Begin/Release of SERIALIZABLE txns (cold).
- **ssi_precommit.go** — cold pre-commit scan, correct and bounded.
- **subxact.go** — small stack ops; `All()` allocates per call but is used for lock transfer at release (rare).
- **multixact/multixact.go** — static tables + pure functions; fine.
- **control/control.go** — control-plane socket, negligible traffic.
- **control/pgcontrol.go** — read-modify-write at checkpoint frequency; the full 8 KB write + fsync is inherent; no redundancy.

## Files reviewed without issues

(empty — every file was reviewed; those above are noted as clean)
