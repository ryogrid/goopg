# 03 — How PostgreSQL 18.3 handles each confirmed bottleneck

Source: the read-only oracle tree at `./postgres/` (18.3). Each section maps a
goopg bottleneck from `02-bottleneck-analysis.md` to the upstream mechanism,
with file/function citations. Sections 7–10 cover the bulk-load (pgbench -i)
gap. See also the pre-existing references `docs/reference/ref-015-wal.md` and
`ref-016-wal-buffer.md`, which this document extends with commit-path and
COPY specifics.

| goopg bottleneck (02) | PG mechanism | § |
|---|---|---|
| #1 runtime.Stack per WAL append | `MyProcNumber` + static `lockToTry` | 1 |
| #3 two commit records | single `XLOG_XACT_COMMIT` (`RecordTransactionCommit`) | 2 |
| #3 every committer enqueues for flush | `XLogFlush` fast exit + `LWLockAcquireOrWait` group flush | 2 |
| #3 no pre-enqueue already-flushed fast exit | walwriter `XLogBackgroundFlush` + `XLogFlush` fast exit | 3 |
| #4/#7 snapshot per RC statement | dense xid array + `xactCompletionCount` reuse | 4 |
| commit slot-clear wakeups | `ProcArrayGroupClearXid` leader batching | 5 |
| activity registry RWMutex+map | `pgstat_report_wait_start` single uint32 store | 6 |
| COPY 56× | `CopyMultiInsertInfo` → `heap_multi_insert`, BulkInsertState, FREEZE | 7 |
| PK build 2.6× | sort-based `_bt_leafbuild` + bulk write | 8 |
| startup 12+× WAL scans | single-pass rmgr redo | 9 |
| post-load vacuum 4.5× | visibility-map skipping | 10 |

## 1. Per-backend identity for WAL insert-lock selection

Files: `postgres/src/backend/access/transam/xlog.c` (`WALInsertLockAcquire`),
`postgres/src/backend/utils/init/globals.c` (`MyProcNumber`),
`postgres/src/backend/storage/lmgr/proc.c` (`InitProcess`),
`postgres/src/include/storage/proc.h` (`GetNumberFromPGProc`).

A PostgreSQL backend is a process, so its identity is a global variable set
once, not something re-derived per operation. At startup `InitProcess()`
claims a `PGPROC` slot and computes `MyProcNumber = GetNumberFromPGProc(MyProc)`
— pure pointer arithmetic (`(proc) - &ProcGlobal->allProcs[0]`, the slot
index). `WALInsertLockAcquire()` does not even consult it on the hot path: it
keeps a `static int lockToTry`, seeded once as
`MyProcNumber % NUM_XLOGINSERT_LOCKS` (8 locks) and reused on every call;
ownership rides the file-scope `MyLockNo` across the insertion. Stripe
selection is O(1), zero lookups, and the per-backend affinity keeps each
lock's cache line from bouncing. goopg's equivalent decision point
(`internal/wal.(*state).stripeNum`) pays a `runtime.Stack` call because
goroutines lack a cheap stable identity — see
`04-improvement-designs/fix-01-wal-stripe-backend-id.md` for the goroutine-
local alternatives.

## 2. Commit record and flush path

Files: `postgres/src/backend/access/transam/xact.c`
(`RecordTransactionCommit`), `postgres/src/backend/access/transam/xlog.c`
(`XLogFlush`, `WaitXLogInsertionsToFinish`, `XLogSetAsyncXactLSN`).

`RecordTransactionCommit()` emits **exactly one** commit record via a single
`XactLogCommitRecord(...)` call (xact.c:1442); the record carries commit
timestamp, subxacts, dropped relations, and invalidation messages inline, and
clog updates are deliberately not separately WAL-logged ("the commit record
written above already contains the data").

The flush is a group-commit protocol with a lock-free fast exit:
`XLogFlush(record)` returns immediately when
`record <= LogwrtResult.Flush` — a backend whose LSN someone else already
flushed performs **zero I/O**. Otherwise it calls
`WaitXLogInsertionsToFinish()` (waits only on concurrent inserters'
`insertingAt` markers, not the device) and then
`LWLockAcquireOrWait(WALWriteLock, LW_EXCLUSIVE)`: if the lock is busy, the
backend sleeps until *release*, loops, and usually finds its LSN flushed "for
free" by the previous holder — N concurrent committers cost ~1 fsync.
`commit_delay`/`commit_siblings` (xlog.c:2881) optionally `pg_usleep` before
flushing, gated by `MinimumActiveBackends(CommitSiblings)`. With
`synchronous_commit=off`, the backend merely advances a shared
`asyncXactLSN` via `XLogSetAsyncXactLSN` (xact.c:1523) and does no flush.

goopg's group commit (writer-goroutine queue + commit_delay, M0098/M0099)
reproduces the batching (measured ≈8.9 txns/flush), but lacks the
zero-I/O-fast-exit *and* pays two records + channel handoffs per commit —
see fix-02 and fix-03.

## 3. Background walwriter

Files: `postgres/src/backend/postmaster/walwriter.c` (`WalWriterMain`),
`postgres/src/backend/access/transam/xlog.c` (`XLogBackgroundFlush`).

`WalWriterMain()` loops: `XLogBackgroundFlush()` then
`WaitLatch(..., WalWriterDelay /* 200 ms default */)`. It advertises itself
(`ProcGlobal->walwriterProc = MyProcNumber`) so async committers can wake it,
and hibernates (sleep × `HIBERNATE_FACTOR`) after `LOOPS_UNTIL_HIBERNATE`
unproductive cycles. `XLogBackgroundFlush()` normally writes only *completed*
WAL pages (`WriteRqst.Write -= WriteRqst.Write % XLOG_BLCKSZ`); when those
are already flushed it pushes the newest async-commit LSN instead, bounded by
`wal_writer_flush_after`. Net effect: foreground `XLogFlush` calls frequently
hit the `record <= LogwrtResult.Flush` fast path and skip I/O entirely.
goopg's background walwriter loop is active (`FlushUpTo(WrittenLSN())` every
200 ms, `internal/initdb/open.go:2126`) but goopg lacks PG's *pre-enqueue*
fast exit — an already-flushed committer still round-trips the flush queue
(fix-03). (An older sentinel bug described in
`docs/design/wal_fsync_flow_primary.md` is fixed in code; that doc is stale.)

## 4. Snapshot acquisition

Files: `postgres/src/backend/storage/ipc/procarray.c` (`GetSnapshotData`,
`GetSnapshotDataReuse`).

Since PG14, XIDs live in a *dense* shared array `ProcGlobal->xids[]` indexed
by `pgxactoff` (parallel `subxidStates[]`, `statusFlags[]`), so
`GetSnapshotData()` scans a contiguous cache-friendly array
(`UINT32_ACCESS_ONCE(other_xids[pgxactoff])`, immediate `continue` on the
common invalid-XID case). The decisive optimization is reuse: every snapshot
stores `snapXactCompletionCount`, a copy of the global monotone
`TransamVariables->xactCompletionCount` bumped whenever an XID-bearing
transaction ends. `GetSnapshotData()` takes `ProcArrayLock` *shared*
(procarray.c:2231) and first calls `GetSnapshotDataReuse()`, which compares
counters (asserting the lock is held) and, if nothing committed in between,
returns the previous snapshot **without scanning at all** — for a Read-Committed backend taking a
snapshot per statement, cost collapses to a shared-lock + counter compare.
goopg's `captureSnapshot` (O(slots) scan + slice alloc + sort per RC
statement) is currently only 1.5 % of CPU, so this is a P3 item (fix-07).

## 5. Commit-time proc-slot clearing

Files: `postgres/src/backend/storage/ipc/procarray.c`
(`ProcArrayEndTransaction`, `ProcArrayEndTransactionInternal`,
`ProcArrayGroupClearXid`).

`ProcArrayEndTransaction()` tries `LWLockConditionalAcquire(ProcArrayLock,
LW_EXCLUSIVE)`; uncontended commits clear their own slot cheaply. Under
contention, `ProcArrayGroupClearXid()` CAS-pushes the backend onto a lock-free
Treiber stack (`procArrayGroupFirst`); the first pusher becomes leader,
acquires `ProcArrayLock` once, detaches the whole list atomically, clears
every queued member's slot, releases, then wakes followers via semaphores —
one exclusive acquisition per burst, wakeups outside the lock. goopg's
ProcArray-equivalent is already atomic per-slot (no global lock), but its
per-commit `commitCond.Broadcast()` under `waitMu`
(`internal/mvcc/manager.go:757`) is a per-commit global wakeup with a similar
batching opportunity (folded into fix-03's re-profile step).

## 6. Wait-event reporting cost

Files: `postgres/src/include/utils/wait_event.h`
(`pgstat_report_wait_start`/`end`),
`postgres/src/backend/utils/activity/wait_event.c` (`my_wait_event_info`).

Reporting a wait is one unsynchronized 4-byte store:
`*(volatile uint32 *) my_wait_event_info = wait_event_info;` (end stores 0).
The pointer targets `MyProc->wait_event_info` in shared memory; there are no
locks, maps, or allocation — even a `pgstat_track_activities` branch was
removed because "the check … seems to add more cost than it saves". Readers
(`pg_stat_activity`) poll the word and tolerate races. goopg's
`activity.Registry` (map + RWMutex per event, May report's #3) should
converge on a per-session atomic word; more urgently, the same registry's
goroutine-ID lookup is the engine of bottleneck #1 (fix-01 removes the
registry from the WAL hot path entirely).

## 7. COPY FROM bulk-insert path (the 56× load gap)

Files: `postgres/src/bin/pgbench/pgbench.c` (`initPopulateTable`,
`initGenerateDataClientSide`), `postgres/src/backend/commands/copyfrom.c`
(`CopyMultiInsertInfo*`), `postgres/src/backend/access/heap/heapam.c`
(`heap_multi_insert`, `GetBulkInsertState`),
`postgres/src/backend/storage/buffer/freelist.c` (`BAS_BULKWRITE`).

- **pgbench uses COPY FREEZE.** `initPopulateTable()` streams all rows over
  one `copy %s from stdin with (freeze on)` (pgbench.c:5040) — gated on
  server ≥ v14 and the target being an ordinary table
  (`RELKIND_RELATION`; `--partitions` disables FREEZE). The default
  `pgbench -i` used in this study hits the FREEZE path, so
  `pgbench_accounts` arrives as a single frozen COPY stream.
- **Batching.** copyfrom.c buffers up to `MAX_BUFFERED_TUPLES = 1000` /
  `MAX_BUFFERED_BYTES = 65535` per flush; `table_multi_insert` →
  `heap_multi_insert()` packs as many tuples as fit onto each page in an
  inner loop and emits **one `XLOG_HEAP2_MULTI_INSERT` record per page
  batch** (heapam.c:2521-2629) — one `XLogBeginInsert`/`XLogInsert` for
  hundreds of rows; freshly initialized pages use
  `XLOG_HEAP_INIT_PAGE`/`REGBUF_WILL_INIT` so per-tuple offsets are not even
  logged.
- **BulkInsertState.** `GetBulkInsertState()` uses a `BAS_BULKWRITE` 16 MB
  ring strategy and keeps the current target buffer pinned
  (`RelationGetBufferForTuple(..., bistate, ...)`), with bulk relation
  extension — no per-tuple buffer-mapping lookups or pin/unpin churn, and no
  shared-buffer-pool pollution.
- **FREEZE.** With `HEAP_INSERT_FROZEN`, pages are born
  all-visible+all-frozen and `visibilitymap_set(... ALL_VISIBLE|ALL_FROZEN)`
  is done during the load (heapam.c:2640-2653).
- **WAL volume.** Fixed WAL framing cost is divided by the page-batch size
  (~hundreds), versus one full record per row — the bulk of the 56× gap.

goopg ingests COPY row-at-a-time through the insert operator (one heap
insert + one WAL record + one stripe lookup per row). Fix design: fix-04.

## 8. Btree index build (PK phase 2.6×)

Files: `postgres/src/backend/access/nbtree/nbtsort.c` (`_bt_leafbuild`,
`_bt_load`, `_bt_blwritepage`), `postgres/src/backend/storage/smgr/bulk_write.c`
(`smgr_bulk_start_rel`, `smgr_bulk_flush`, `smgr_bulk_finish`).

PG builds btrees by *sorting*: index tuples are spooled into a `tuplesort`
(optionally with parallel workers), then `_bt_load()` streams the sorted
tuples densely into pre-linked leaf pages — no descents, no splits. Pages go
through the bulk-write path; `smgr_bulk_flush()` WAL-logs accumulated pages
wholesale via `log_newpages()` (one record per batch), and
`smgr_bulk_finish()` registers the fork for sync at the next checkpoint
instead of per-page fsync (immediate sync only if a checkpoint raced the
build). Under `wal_level = minimal` per-page WAL is skipped entirely.

## 9. Startup redo: single pass

Files: `postgres/src/backend/access/transam/xlog.c` (`StartupXLOG`),
`postgres/src/backend/access/transam/xlogrecovery.c` (`PerformWalRecovery`,
`ApplyWalRecord`), `postgres/src/backend/access/transam/rmgr.c`
(`RmgrTable[]` from `postgres/src/include/access/rmgrlist.h`).

PG replays WAL **once**, beginning at the checkpoint's REDO pointer, in a
single `do { ApplyWalRecord(); } while (record)` loop; each record is
dispatched by resource-manager id through a static jump table
(`GetRmgr(record->xl_rmid).rm_redo(xlogreader)`). Every rmgr — heap, btree,
xact, smgr — sees each record exactly once, in LSN order. goopg's dozen-plus
`internal/initdb/*_ddl_recovery.go` modules each call `wal.ReadAll(walDir, 0)`
— re-reading the entire WAL per module (200 GB of startup allocation on a
1.7 GB WAL, ~28 s). Fix design: fix-05 (rmgr-style single-pass dispatch).

## 10. VACUUM after bulk load (4.5×)

Files: `postgres/src/backend/access/heap/vacuumlazy.c`
(`heap_vac_scan_next_block`, `find_next_unskippable_block`),
`postgres/src/backend/access/heap/visibilitymap.c`.

Vacuum consults the visibility map before reading heap blocks:
`find_next_unskippable_block()` skips all-frozen pages outright (and
all-visible pages for non-aggressive vacuums), jumping over runs ≥
`SKIP_PAGES_THRESHOLD` (32) pages without reading them. After a COPY FREEZE
load the whole table is already all-visible+all-frozen in the VM (§7), so the
post-load vacuum reads the VM plus a page or two — goopg re-scans the heap.
