# 02 — PostgreSQL 18.3 reference (the oracle)

status: draft · date: 2026-07-12

All citations are repository-relative into the read-only oracle tree
(`postgres/src/...`), verified against this checkout (18.3). Line numbers drift
across minor releases; symbols are the anchors.

The single fact to internalize: **PostgreSQL has no dedicated WAL-I/O process
and no commit queue.** Any backend can become the process that writes and
fsyncs WAL — committers via `XLogFlush`, buffer-starved inserters via
`AdvanceXLInsertBuffer`, and the walwriter via `XLogBackgroundFlush` — all
funneling into one function, `XLogWrite`, serialized by `WALWriteLock`. Group
commit is emergent.

## 2.1 Shared state: requests, results, and their protection

`postgres/src/backend/access/transam/xlog.c`:

- `XLogwrtRqst { Write, Flush }` (xlog.c:320-324) — the *demand* side, protected
  by the `info_lck` **spinlock** (xlog.c:299-303).
- Results are **atomics, not spinlocked**, in PG18 (xlog.c:471-474):
  `logInsertResult`, `logWriteResult`, `logFlushResult` (`pg_atomic_uint64`).
- Each backend keeps a private stale copy `LogwrtResult` (xlog.c:613),
  refreshed by `RefreshXLogWriteResult` (xlog.c:621-626) with a **load-bearing
  read order** — Flush first, read barrier, then Write:

  ```c
  _target.Flush = pg_atomic_read_u64(&XLogCtl->logFlushResult);
  pg_read_barrier();
  _target.Write = pg_atomic_read_u64(&XLogCtl->logWriteResult);
  ```

  The writer side mirrors it: Write stored first, write barrier, then Flush
  (xlog.c:2573-2581). Invariant asserted: `Insert >= Write >= Flush`
  (xlog.c:2582-2600). `logInsertResult` advances via
  `pg_atomic_monotonic_advance_u64` (atomics.h:584-607 — CAS loop, full
  barrier).
- Lock roles (xlog.c:304-315): `info_lck` = short spinlock for requests;
  `WALBufMappingLock` = replace a page in the WAL buffer ring;
  **`WALWriteLock` = "must be held to write WAL buffers to disk (XLogWrite or
  XLogFlush)"**.
- Per-process segment FD cache (xlog.c:636-638): `static int openLogFile`,
  `openLogSegNo`, `openLogTLI` — **each backend caches its own FD**; there is
  no shared handle to contend on.

## 2.2 `XLogFlush` — the committing backend's algorithm (xlog.c:2780-2941)

1. **Fast exit** (xlog.c:2799-2801): `if (record <= LogwrtResult.Flush) return;`
   — against the backend's *local* copy; no lock, no atomic in the common
   already-durable case.
2. Loop (`for (;;)`, xlog.c:2828):
   - Refresh atomics; recheck; `break` if someone flushed past `record`
     (xlog.c:2833-2835).
   - **Aggregate**: under `info_lck`, raise `WriteRqstPtr` to
     `XLogCtl->LogwrtRqst.Write` (xlog.c:2841-2844) — piggyback everything
     others requested.
   - `insertpos = WaitXLogInsertionsToFinish(WriteRqstPtr)` (xlog.c:2845) —
     **called BEFORE acquiring `WALWriteLock`** (deadlock discipline, §2.5).
   - **`LWLockAcquireOrWait(WALWriteLock, LW_EXCLUSIVE)`** (xlog.c:2854). If it
     returns false — the lock was busy; we slept until *release* but hold
     nothing — `continue` (xlog.c:2856-2862): refresh, and usually
     `record <= Flush` now holds → exit with **zero I/O**. The in-code comment
     (xlog.c:2847-2853) names the purpose: "helps to maintain a good rate of
     group committing."
   - Holder: recheck under the lock (xlog.c:2864-2870); then the
     **commit_delay gate** (xlog.c:2881-2897):
     `if (CommitDelay > 0 && enableFsync && MinimumActiveBackends(CommitSiblings)) pg_usleep(CommitDelay);`
     — **only the would-be flusher sleeps, and it sleeps while HOLDING
     `WALWriteLock`**, deliberately widening the batch. (Default
     `commit_delay = 0` — no sleep; `commit_siblings = 5`;
     `postgres/src/backend/utils/misc/guc_tables.c`.) After the sleep,
     `WaitXLogInsertionsToFinish(insertpos)` is called again only to *advance*
     the position — the comment (xlog.c:2886-2895) is emphatic that actually
     waiting here, holding the lock, would deadlock.
   - Become the flusher: `WriteRqst.Write = WriteRqst.Flush = insertpos;
     XLogWrite(WriteRqst, tli, false); LWLockRelease; break;`
     (xlog.c:2899-2905).
3. After the loop: `WalSndWakeupProcessRequests(...)` (xlog.c:2913) — wake
   walsenders *after* releasing the contended lock. Post-condition check
   errors if `Flush < record` (xlog.c:2936-2940).

The flusher flushes the **aggregate frontier** (`insertpos` keeps climbing as
inserters finish), not just its own record. That *is* group commit — no queue,
no leader election.

## 2.3 `XLogWrite` — what the flusher does under the lock (xlog.c:2304-2601)

Preconditions: holds `WALWriteLock`; in a critical section;
`WaitXLogInsertionsToFinish(WriteRqst.Write)` already called (xlog.c:2299).

- **Batched contiguous-page writes**: walk the ring from
  `LogwrtResult.Write`, page end LSNs from the atomic `xlblocks[]`
  (xlog.c:2349); accumulate `npages` and issue **one
  `pg_pwrite(openLogFile, from, npages*XLOG_BLCKSZ, startoffset)`** per
  contiguous run (xlog.c:2420-2460), wrapped in the `IO:WALWrite` wait event;
  flush the run at ring-memory end, request end, or `finishing_seg`.
- **Segment lifecycle**: crossing into a new segment closes the old FD
  (`XLogFileClose`) and opens/creates the next (`XLogFileInit`/`XLogFileOpen`,
  xlog.c:2360-2387).
- **Unconditional fsync at segment completion** (xlog.c:2464-2505): when the
  write finishes a segment, `issue_xlog_fsync` runs **immediately, regardless
  of `WriteRqst.Flush`** — "one and only one backend will perform this fsync"
  — then `LogwrtResult.Flush = LogwrtResult.Write` for that boundary, walsender
  wakeup request, archive notify, possibly `RequestCheckpoint(CAUSE_XLOG)`.
- **Final partial flush** (xlog.c:2523-2557): if `Flush < WriteRqst.Flush`,
  `issue_xlog_fsync` on the (possibly reopened) current segment — skipped
  entirely for `wal_sync_method = open_datasync/open_sync` because the write
  itself was O_DSYNC/O_SYNC (`get_sync_bit`, xlog.c:8654-8696).
- **Publish**: floor `LogwrtRqst` under `info_lck` (results never trail
  requests), then store atomics Write → write barrier → Flush
  (xlog.c:2559-2581).

`issue_xlog_fsync` (xlog.c:8744-8811): no-op if `!enableFsync` or an
open_sync method; else `pg_fdatasync`/`pg_fsync...` under the `IO:WALSync`
wait event; PANIC on failure.

## 2.4 Any backend can write WAL: the buffer-eviction path

`GetXLogBuffer` → `AdvanceXLInsertBuffer` (xlog.c:1988-…): to reuse a ring
slot whose page is dirty and unwritten (`LogwrtResult.Write < OldPageRqstPtr`
— "WAL buffers full"), the **inserting backend itself**:

1. bumps `LogwrtRqst.Write` under `info_lck`;
2. **releases `WALBufMappingLock` first** (deadlock avoidance, xlog.c:2043),
   calls `WaitXLogInsertionsToFinish(OldPageRqstPtr)`, then
   `LWLockAcquire(WALWriteLock)` (plain acquire);
3. if still unwritten: `WriteRqst.Write = OldPageRqstPtr;
   WriteRqst.Flush = 0; XLogWrite(...)` (xlog.c:2055-2071) — **write-only, no
   fsync**, `pgWalUsage.wal_buffers_full++`.

goopg's analog is the walBuf-overflow drain. The redesign gives it the same
shape: write-only `xlogWrite` in the overflowing backend's context (03 §4).

## 2.5 `WaitXLogInsertionsToFinish` and the deadlock discipline (xlog.c:1506-1616)

Fast path: return if `upto <= logInsertResult` (membarrier read). Else read the
reserved frontier under `insertpos_lck`, then `LWLockWaitForVar` on each of the
`NUM_XLOGINSERT_LOCKS` insertion locks' `insertingAt`, computing the oldest
still-in-progress position; finally `pg_atomic_monotonic_advance_u64(logInsertResult, finishedUpto)`.

**Discipline** (xlog.c:1500-1504): call it *before* taking `WALWriteLock`,
never to wait while holding it — an in-flight inserter may need `WALWriteLock`
(via §2.4) to make progress; waiting for that inserter while holding the lock
is a deadlock. The commit_delay recheck (§2.2) is the sole, carefully-argued
exception (it never actually waits there).

## 2.6 The walwriter — pre-write, throttled fsync (walwriter.c, xlog.c:2967-3102)

`WalWriterMain` (walwriter.c:88): loop of `XLogBackgroundFlush()` +
`WaitLatch(WalWriterDelay /* default 200 ms */)`; advertises
`ProcGlobal->walwriterProc` for wakeups; hibernates ×`HIBERNATE_FACTOR`(25)
after `LOOPS_UNTIL_HIBERNATE`(50) idle rounds.

`XLogBackgroundFlush` policy:

- `WriteRqst = LogwrtRqst` (under `info_lck`); **round the write target DOWN to
  a page boundary** — `WriteRqst.Write -= WriteRqst.Write % XLOG_BLCKSZ`
  (xlog.c:2993): the walwriter normally writes only *complete* pages. The one
  partial-page case is pushing `asyncXactLSN` (async commits) when everything
  else is already flushed (xlog.c:2997-3003).
- **Flush decision** (xlog.c:3031-3061): fsync this round only if
  `wal_writer_flush_after == 0`, or `wal_writer_delay` elapsed since the last
  flush, or `flushblocks >= wal_writer_flush_after` (default **1 MB**,
  guc_tables.c). Otherwise `WriteRqst.Flush = 0` — **write to the OS cache
  only**. (Even then, a segment completed inside `XLogWrite` is fsynced
  unconditionally, §2.3.)
- Uses **plain `LWLockAcquire`**, not AcquireOrWait — the walwriter is not a
  group-commit participant.
- `XLogSetAsyncXactLSN` (xlog.c:2609-2659): async committers record their LSN
  under `info_lck` and wake the walwriter's latch per the same flush-threshold
  policy.

So: *"the background writer mostly only pwrites to the OS cache; it fsyncs only
per `wal_writer_flush_after`/delay thresholds — and segment completion always
fsyncs that segment, whoever is writing."*

## 2.7 `LWLockAcquireOrWait` — the tri-state primitive (lwlock.c:1408-1523)

Try to acquire; if busy, queue in `LW_WAIT_UNTIL_FREE` mode and retry; if still
busy, **sleep until the lock is released — and wake WITHOUT holding it**.
Return value: true = you hold the lock; false = "the lock became free and you
were woken, but you hold nothing" — the caller must re-check shared state
(`LogwrtResult.Flush`) and typically discovers its work was done for it.
Fairness is barging (like all LWLock fast paths); the group-commit progress
argument doesn't depend on lock fairness, only on frontier aggregation +
recheck.

## 2.8 Commit-path integration (xact.c:1315-1572)

`RecordTransactionCommit`: emit the (single) commit record →
sync path: `XLogFlush(XactLastRecEnd)` **then** `TransactionIdCommitTree`
(CLOG) (xact.c:1498-1532); async path: `XLogSetAsyncXactLSN` +
`TransactionIdAsyncCommitTree`. After CLOG:
`SyncRepWaitForLSN(XactLastRecEnd)` (xact.c:1544-1557) — **local flush precedes
the remote-durability wait**, the same ordering goopg already has.

Wait events: `IO:WALWrite` around `pg_pwrite` (xlog.c:2433), `IO:WALSync` in
`issue_xlog_fsync` (xlog.c:8765), `LWLock:WALWrite` from the lock waits — the
77.8 % share measured in `analysis/perf-optimize2/01-results.md` §4.
