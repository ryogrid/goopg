# 04 — WAL persistence

The user question this chapter answers is "how efficiently can goopg persist WAL
for simple-update?" The measured answer is: **as efficiently as PostgreSQL
18.3.** This chapter documents why, so that the conclusion is auditable and so
that no future loop reopens a closed problem.

## 1. The evidence

| measure | goopg | PG 18.3 | ratio |
|---|---:|---:|---:|
| `END` statement latency (`pgbench -r`) | 3.215 ms | 3.190 ms | **1.008×** |
| share of transaction latency | 69.4 % | 76.5 % | — |
| backend-samples waiting to flush | 60.1 % `LWLock:WALWriteLock` | 63.5 % `LWLock:WALWrite` | — |
| backend-samples inside `fdatasync` | 2.0 % `IO:WALSync` | 1.9 % `IO:WalSync` | — |
| WAL bytes / txn | ~1,157 | 905 | 1.28× |
| write syscalls / txn | 6.24 | — | — |

Three independent instruments — client-side statement latency, server-side wait
sampling, and WAL byte volume — all say the same thing. For reference, the same
row in `analysis/perf-optimize3` (2026-07-13) read 21.20 ms vs 2.619 ms (8.1×)
with 33.0 KB of WAL per transaction (18×).

## 2. Why: goopg implements PostgreSQL's group commit faithfully

PostgreSQL's group commit is **emergent**, not scheduled. `XLogFlush`
(`postgres/src/backend/access/transam/xlog.c:2780`) has three relevant moves:

1. **Quick exit if already flushed** — `if (record <= LogwrtResult.Flush)`.
2. **Re-check after acquiring the lock** (`xlog.c:2864-2870`):
   ```c
   /* Got the lock; recheck whether request is satisfied */
   RefreshXLogWriteResult(LogwrtResult);
   if (record <= LogwrtResult.Flush) { LWLockRelease(WALWriteLock); break; }
   ```
   Backends that queued behind the holder find their LSN already durable and do
   **zero I/O**. This is the group commit.
3. **Optional `CommitDelay`** (`xlog.c:2881-2884`), gated on `enableFsync` and
   `MinimumActiveBackends(CommitSiblings)`, to widen the batch — default 0.

goopg's `Writer.FlushUpTo` (`internal/access/transam/xlog/writer.go:957`)
mirrors all three:

1. **Quick exit** — `if lsn <= w.flushedLSNAtomic.Load() { return nil }`
   (`writer.go:970`).
2. **Tri-state acquire with loser re-check** — `flushUpToBackend`
   (`writer.go:986`) waits for in-flight stripe inserts *outside* the write lock
   (`waitInsertionsToFinish`, `writer.go:1005`), then:
   ```go
   held, err := st.writeMu.acquireOrWait(w.done)   // writer.go:1019
   ...
   if !held { if lsn <= w.flushedLSNAtomic.Load() { return nil } }
   ```
   `walWriteLock` (`xlog/wal_write_lock.go:24-77`) is a `sync.Mutex` plus a
   generation channel closed on release, so losers wake, observe the holder's
   aggregate frontier, and return without I/O — the same emergent grouping.
3. **`commit_delay` / `commit_siblings` are real GUCs** at PG defaults (0 / 5),
   `internal/utils/misc/defaults.go:613,619`, consumed in `flushAsHolder`
   (`writer.go:1054-1057`) with the same `flushWaiters >= commitSiblings`
   guard PG uses.

`flushAsHolder` additionally widens the batch to `st.publishedFrontier()` before
writing, and holds only `writeMu` — never `appendMu` — so appends continue
during the `fdatasync`.

The CPU profile confirms the path is cheap: `Writer.Append` is 1.20 % of `-N`
CPU and `CommitTransaction` 1.37 %. The mutex profile puts `FlushUpTo` at 2.88 %
of mutex delay. The cost of committing is *waiting for the disk*, shared across
a batch — which is the correct and irreducible answer for
`synchronous_commit=on`.

## 3. What closed the gap

For the record, since three design bundles targeted this:

- **The canonical `0xFE` WAL record family was deleted** (2026-07-15). It
  embedded a full 8 KB page image in *every* record, which is where 33 KB/txn
  came from. FPI is now gated once-per-page-per-checkpoint by `Pool.needsImage`
  (`internal/storage/bufpool.go:1096`).
- **The commit-path CLOG fsync was removed** (C2). `setStatusWithLSN`
  (`internal/access/transam/clog.go:491`) now performs **no I/O** on commit;
  write-back rides LRU eviction and the checkpointer.
- **Backend-driven flush** (`docs/design/wal-backend-flush/`, slices 1–7)
  removed the dedicated writer goroutine and produced the emergent group commit
  described in §2.

`analysis/perf-optimize3/05-improvement-designs/01-c1-incremental-canonical-heap-wal.md`
(C1) is **obsolete** — the record family it was going to make incremental no
longer exists.

## 4. The residual 28 % of WAL volume

goopg writes ~1,157 B/txn against PG's 905. The most likely mechanism is
visible in `Pool.MarkDirtyLogicalChange` (`internal/storage/bufpool.go:2398`):

```go
lsn, err := emitter()                       // the logical change record
MustHeader(s.page).SetLSN(lsn)
if needFPI && p.logFPI != nil && p.fullPageWrites.Load() {
	pageCopy := make(Page, BlockSize)       // bufpool.go:2417 — 8 KB copy
	copy(pageCopy, s.page)
	fpiLSN, fpiErr := p.logFPI(tag.Rel, tag.Block, pageCopy)   // SECOND record
```

goopg emits the change record **plus a separate `XLOG_FPI` record**, and copies
the whole 8 KB page to the heap to do it. PostgreSQL attaches the block image
*into* the same record in `XLogRecordAssemble`
(`postgres/src/backend/access/transam/xloginsert.c`), paying one record header
instead of two and no intermediate copy.

At 1.28× on a path that is already at latency parity, this is **not worth
chasing for throughput**. It is worth noting for two other reasons: the 8 KB
`make(Page, ...)` per FPI is pure allocator pressure on the write path, and the
two-record shape is a divergence from the PG stream format that matters to the
`pg_waldump`/standby parity work tracked elsewhere.

## 5. What NOT to do here

- **Do not implement C5 "pipelined commit groups" for parity reasons.** The
  design (`analysis/perf-optimize3/05-improvement-designs/05-c5-pipelined-commit-groups.md`,
  promoted to perf-optimize3-dash doc 01) splits the drain and the `fsync` so
  they are not both under `writeMu`. **PostgreSQL holds `WALWriteLock` across
  both the `pwrite` and the `fsync` too** — so this would be an enhancement
  *beyond* PG, not a parity fix. With `END` already at 1.008× it has no
  measurable headroom on this workload, and it carries a WAL-before-data
  ordering risk. Deprioritised, not refuted: it may matter at higher client
  counts or on slower storage.
- **Do not tune `commit_delay`.** It is wired and defaults to PG's 0. Raising it
  trades latency for batch width; at 1.008× parity there is nothing to buy.
- **Do not re-target the CLOG commit fsync or the canonical-record FPI.** Both
  are gone; `analysis/perf-optimize3/03-code-attribution.md` M1 and M2 are stale.
