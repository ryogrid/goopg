# fix-03 — Commit-pipeline streamlining (P1, re-profile after fix-01)

> **Status: PARTIALLY IMPLEMENTED** (commit `8f30f11d`, 2026-07-12). Landed
> items (a) per-flush log demotion, (b) pre-enqueue already-flushed fast exit
> (`flushedLSNAtomic`), and (d) stale-sentinel doc hygiene. Item (c) commit-
> wakeup batching remains DEFERRED ("measure first" — no waiter count exists;
> `commitCond` is broadcast from ~8 sites) — see `.ralph/deferral_ledger.md`.

## Problem (evidence)

goopg's group commit batches correctly (143 fdatasync/s at 1,269 TPS,
≈8.9 txns/flush; block profile: 72.9 % of block time is commit-wait, the
same *shape* as PG's 77.7 % `LWLock:WALWrite`). What differs is fixed
per-flush and per-commit overhead around the batching:

1. **Per-flush structured log.** `flushUpTo` logs
   `l.Info("walwriter flush", lsn, segments_fsynced)` on every barrier
   (`internal/wal/writer.go:1962`) — 25,784 lines in the 180 s headline run,
   formatted while committers wait on the done channel.
2. **No pre-enqueue flushed-LSN fast exit.** The background walwriter *is*
   active — `internal/initdb/open.go:2126` calls `FlushUpTo(WrittenLSN())`
   on a 200 ms ticker — but `Writer.FlushUpTo` (`writer.go:859`) enqueues a
   `groupFlushReq` + signal-channel poke + `<-done` block even when the LSN
   is already durable; the existing fast exit (`writer.go:1919`) runs
   *post-dequeue* in the writer goroutine. PG's `XLogFlush` checks
   `record <= LogwrtResult.Flush` before any coordination and returns with
   zero I/O and zero handoff (03 §2–3). (Cleanup item: the stale
   `^uint64(0)`-sentinel description in
   `docs/design/wal_fsync_flow_primary.md` and the comment at
   `open.go:188` should be corrected — the code no longer does that.)
3. **Per-commit global wakeup.** `Manager.finish` broadcasts
   `commitCond` under `waitMu` (`internal/mvcc/manager.go:758`) on every
   commit to wake potential `WaitForXID` waiters — usually none in pgbench.

## Design

(a) **Flush log demotion** — one-line change: `l.Info` → `l.Debug`, plus a
sampled Info summary (every N seconds: flush count, mean batch size, max
latency) so operators keep the signal. No format change to existing tooling
that greps `walwriter flush` (update `run_su50.sh`'s counter to the summary
line or a counter endpoint).

(b) **Pre-enqueue fast exit on already-flushed LSN**: maintain `flushedLSN`
as an `atomic.Uint64` mirror (writer goroutine stores it after each fsync
barrier); `Writer.FlushUpTo` returns immediately when
`lsn <= flushedLSN.Load()` **before** touching the queue or channels. Safe:
the mirror is monotone and published after the fdatasync returns, so a
reader that passes the check has a durable LSN; a stale read only costs the
old (queue) path. Because the background walwriter already flushes
`WrittenLSN()` every 200 ms, this fast path will genuinely hit under bursty
or lulled load — reproducing PG's `record <= LogwrtResult.Flush` zero-
coordination exit on goopg's writer-goroutine model without restructuring
the group-commit queue. Optionally align the pre-flush target to completed
WAL pages (`XLogBackgroundFlush`'s
`WriteRqst.Write -= WriteRqst.Write % XLOG_BLCKSZ` rule) to avoid flushing
a partial trailing page twice.

(c) **Wakeup batching (measure first)**: move `commitCond.Broadcast` behind
a check that waiters exist (`waitMu`-guarded waiter count or atomic), turning
the common no-waiter commit into zero lock traffic. This mirrors
`ProcArrayGroupClearXid`'s "wakeups outside the lock, only when needed"
(03 §5).

(d) **Doc/comment hygiene**: correct the stale `^uint64(0)` sentinel
description in `docs/design/wal_fsync_flow_primary.md` and the comment at
`internal/initdb/open.go:188` (this analysis initially propagated that
stale claim; the reviewer caught it against the code).

## Expected lift

Individually small (log demotion ~2–5 %; the pre-enqueue fast exit mainly
cuts p99 latency and commit-queue occupancy; wakeup batching removes a
global mutex touch per commit). Combined estimate ×1.1–1.2 at c=50 *after* fix-01
(before fix-01 the CPU storm masks these). This fix is deliberately staged
after a fix-01 re-profile so each item is confirmed against a clean profile.

## Risks

- The atomic `flushedLSN` mirror must be stored **after** fdatasync returns
  (happens-before via the writer goroutine's program order + atomic store);
  an early store would let a commit return before durability.
- Page-aligning the background pre-flush target changes its fsync cadence
  slightly — verify barrier counts don't inflate (both pre-flush and commit
  flushes serialize in the writer goroutine).

## Verification plan

1. Unit: pre-enqueue fast-exit test (flush X, then FlushUpTo(X) must not
   enqueue); waiter-count wakeup test.
2. `make race-gate`; units + pgbench smoke.
3. Crash recovery e2e unchanged (kill-9 during load).
4. Perf acceptance: `run_su50.sh` — expect: server.log flush lines → ~1/5 s
   summary; barrier count similar or lower; p99 latency down; TPS +10–20 %
   over post-fix-01 baseline; c=1 aux TPS unchanged (still 1 flush/txn,
   correct).
