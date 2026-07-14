# 00 — Overview

status: draft · date: 2026-07-12 · supersedes: (on implementation) 0098-0002,
0099-0002, parts of wal_fsync_flow_primary.md; see README.

## Problem

goopg's WAL durability path is architecturally different from PostgreSQL's, and
the difference is now the dominant cost of update-heavy short transactions:

- **goopg today:** every commit's `FlushUpTo` appends a `groupFlushReq` to a
  queue and parks the backend on a channel; a single dedicated writer goroutine
  (`wal.state.loop`, `LockOSThread`-pinned) dequeues, optionally sleeps a
  hardcoded `commit_delay` of 1000 µs **on that shared goroutine** (stalling
  every queued waiter and all subsequent WAL ops), drains the WAL ring to
  segment files, `fdatasync`s, and wakes all waiters. The background walwriter
  tick (200 ms) routes a *full group-commit flush* through the same single
  goroutine.
- **PostgreSQL:** the committing backend runs `XLogFlush` itself: a lock-free
  fast exit, then `LWLockAcquireOrWait(WALWriteLock)`; exactly one backend
  becomes the flusher and performs `pg_pwrite` + `issue_xlog_fsync` for the
  *aggregate* frontier; the losers are woken on release, re-check the flushed
  LSN, and usually return with **zero I/O and zero hand-off**. The walwriter
  process only pre-*writes* completed pages, fsyncing on
  `wal_writer_flush_after`/delay thresholds or segment completion. `commit_delay`
  (default **0**) sleeps only in the would-be flusher, while holding the lock.

## Measured evidence (analysis/perf-optimize2)

| observation | value |
|---|---|
| c=50 `-N` sync-on TPS after the CPU fixes | 1,145 (flat vs 1,121 before — flush-bound) |
| same, `synchronous_commit=off` | 9,820 (3.9× — CPU headroom exists) |
| PostgreSQL 18.3, same conditions | 15,556 TPS, with **77.8 %** of backend samples in `LWLock:WALWrite` |
| goopg block profile | 72.9 % of block time under `CommitTransaction`: `selectgo` 40 % + `chanrecv` 27 % — backends parked on the flush-done channel |
| group batching today | ≈8.9 txns/fdatasync (the *batching* works; the *hand-off* and serialization around it are the cost) |

PG at 15.5 k TPS is *also* WAL-write serialized — but its per-commit cost around
the serialization is a lock wait with an often-zero-I/O exit, not a queue
mutex + signal channel + park/unpark + a foreign goroutine's scheduling latency
+ a shared 1 ms sleep.

## Goals

1. **PG-parity control flow:** committing backends perform WAL write+fsync
   themselves under a `WALWriteLock` analog with `LWLockAcquireOrWait`
   semantics; group commit is emergent (aggregate frontier + loser recheck),
   with the explicit queue deleted.
2. **PG-parity walwriter:** background goroutine pre-writes completed pages;
   fsync gated by `wal_writer_flush_after` / delay-elapsed / segment
   completion; never a forced full group-commit flush.
3. **PG-parity latency semantics:** no per-commit channel hand-off; the
   `commit_delay` sleep exists only in the lock holder and **defaults to 0**.
4. **Any backend can write WAL** (PG's `AdvanceXLInsertBuffer` property): the
   ring-overflow drain becomes a write-only (`flush=0`) call in the
   overflowing backend's context.

## Non-goals / invariants preserved

- **On-disk WAL format unchanged** — nothing in this design touches record or
  page layout (PG-compat invariants `0107-0001`). `pg_waldump` and PG-standby
  attach behavior must be byte-identical before/after.
- **walsender `memRing` stays fed-on-append** (0010-0002) — unaffected by who
  flushes.
- **WAL-before-data** (bufpool flush barriers, clog hook) — same `FlushUpTo`
  contract: returns only after the LSN is durable.
- **Crash safety** — `fdatasync` barrier semantics unchanged (0007-0002).
- Not a general fsync-latency fix: raw device/WSL2 `fdatasync` cost is
  untouched; c=1 remains ~one-fsync-per-commit.

## Decision log

| # | decision | rationale |
|---|---|---|
| D1 | `walWriteLock` = `sync.Mutex.TryLock` + swap-on-release generation channel (03 §1) | stdlib-only, composes with shutdown via `select` on `w.done`; an LWLock clone over `runtimeshim` semaphores adds linkname fragility for no benefit; `sync.Cond` cannot select against shutdown |
| D2 | **`commit_delay`/`commit_siblings` become real GUCs with PG defaults: `commit_delay=0` (no delay), `commit_siblings=5`.** The hardcoded 1000 µs/5 (M0099-0003) is dropped | goopg exists for vanilla-PG compatibility: a GUC whose default differs from PG silently changes behavior for every operator who didn't set it. The 1000 µs constant was a stopgap for the weaker queue mechanism; under the emergent model, arrivals during a holder's fsync are batched into the next holder's frontier anyway. Operators who want PG's throughput-for-latency trade set `commit_delay>0` exactly as on PG |
| D3 | `state.loop` is **deleted** at the end state (slice 6) | PG has no equivalent; keeping a reduced loop forces the FD-cache/dirty maps to stay dual-owner forever — the exact unsynchronized-state hazard this redesign kills |
| D4 | The flusher and the walwriter take **`writeMu` only** — never `appendMu` — from slice 3 onward. This *matches today's behavior*: at HEAD the loop's flush drain already runs **without** `appendMu`, concurrent with RLock stripe appenders (safety = the atomic ring, 0107-0007ai). The slow paths (Path-A append, appendRaw), which mutate drain-coupled state (`walBuf.reset`, `resetPosition`), nest `writeMu` *inside* their existing `appendMu.Lock` section, covering the entire drain-coupled tail. Slice 7 is re-scoped to the reset-site audit + heavy race-stress gate | inserters are never blocked by a flush (PG parity, and today's parity); a drain-under-`appendMu.Lock` variant was rejected in adversarial review — one reading serializes all commits on `appendMu` and kills emergent group commit, the other is an ABBA deadlock with the slow paths |
| D5 | No runtime feature flag; slice-by-slice replacement, old path behind a compile-time const until slice 6 | a runtime toggle keeps two concurrency protocols alive against the same `files`/`dirty`/ring state — strictly more dangerous than either protocol |
| D6 | the **existing but currently-inert** `wal_writer_flush_after` GUC (already registered, BootVal 1 MiB, already in postgresql.conf.sample — zero behavioral readers today) is wired into the walwriter's fsync throttle; `wal_writer_delay` stays 200 ms | PG-default-faithful (guc_tables.c); the fsync throttle is what makes pre-write cheap |

## Chapter map

01 = what exists (verified at HEAD, commit `fedb0eec` era). 02 = what PG does
(verified citations). 03 = what we build. 04 = why it is safe. 05 = how we get
there without a big-bang. 06 = what to expect and how to observe it.
