# 08-01 — C5 §2: drain/fsync split (pipelined commit groups)

status: design · date: 2026-07-14 · base: `a640d2b0` · depends on:
wal-backend-flush bundle (landed) · gates: G-race, G-crash, G-standby,
G-waldump, G-perf → [README](README.md)

Promotes [`../../perf-optimize3/05-improvement-designs/05-c5-pipelined-commit-groups.md`](../../perf-optimize3/05-improvement-designs/05-c5-pipelined-commit-groups.md)
Idea item 2 to a full design. The sketch's decision gate is satisfied (see §1).

## 1. Problem and numbers

After C1+C2+C3 landed, the `-N` (write) block profile is dominated by a single
serialized wait — the WAL flush cycle — exactly as PostgreSQL's own is:

| run | commit wait share of `-N` block delay | sink |
|---|---:|---|
| 06 scale-100 (`postdash_6e3b7a37`) | **59.2 %** | `CommitTransaction` → `wal.FlushUpTo` → `walWriteLock.acquireOrWait` |
| 07 scale-500 (`scale500b_2159d329`) | **66.1 %** (3,072.9 s of 4,650.2 s) | same |

PostgreSQL in the same runs waits 86.8–87.5 % on `LWLock:WALWrite`
(06-02, 07-02) — so goopg already *blocks on the same thing PG blocks on*. The
residual gap is **commit-group amortization**: goopg's `END` statement is
3.26 ms at scale 100 (vs PG 2.83 ms) and 5.19 ms at scale 500 (vs PG 3.31 ms),
i.e. 27 % (06) rising to 52 % (07) of the per-transaction excess over PG
(06-02, 07-01). Converging `END` to PG's ceiling is worth ~1.47× → ~1.34× at
scale 100 and, at scale 500, ~6.6 k → ~8.8 k TPS on the averaged window
(07-02).

**Why a split helps.** Today the `writeMu` holder (`flushAsHolder`,
`writer.go:983`) performs the drain (`pwrite`) **and** the `fdatasync` under one
lock hold. While that holder is blocked in `fdatasync` (the ~1.5–3 ms device
floor), the next group's records are already appended to the ring but cannot
begin draining — the drain of group N+1 is serialized *behind the fsync of
group N*.

**This is a deliberate divergence beyond PostgreSQL, not PG parity.**
PostgreSQL holds `WALWriteLock` across *both* the `pg_pwrite` and the
`issue_xlog_fsync` inside a single `XLogWrite` call — one hold, one backend does
both, and no other backend enters `XLogWrite` during that fsync. goopg's current
structure (drain + fsync under one `writeMu` hold, fast-path appenders
continuing under `appendMu.RLock` — `writer.go:977`–979) is therefore *already*
structurally identical to PG. What PG overlaps is WAL **insertion** into
`wal_buffers` (the separate `WALInsertLock`s / `WaitXLogInsertionsToFinish`
frontier) with the write+fsync — not write-of-N+1 with fsync-of-N. The split
proposed here overlaps the *drain of group N+1 with the fsync of group N*, which
PG does **not** do; it is a goopg-specific optimization made cheap by Go's
goroutine parking, in the same spirit as the wal-backend-flush bundle's other
deliberate divergences (error-return instead of PANIC, `04` §4.7). It should be
justified on its own merits (below), not as catching up to PG.

## 2. Current-code map (verified at `a640d2b0`)

The one-hold structure, top to bottom:

- **`Writer.FlushUpTo(lsn)`** — `internal/wal/writer.go:912`. Fast exit
  `lsn <= flushedLSNAtomic` (durable already); else `flushUpToBackend`.
- **`Writer.flushUpToBackend(lsn)`** — `writer.go:942`. `frontier :=
  st.waitInsertionsToFinish(lsn)` **before** the lock (`writer.go` around :952);
  loop: recheck `flushedLSNAtomic` → `held, err := st.writeMu.acquireOrWait(w.done)`
  → on `held` call `flushAsHolder`, on `!held` recheck and `continue`.
- **`Writer.flushAsHolder(lsn, frontier)`** — `writer.go:983`. `defer
  st.writeMu.release()` (line 985), recheck under lock, optional `commit_delay`
  holder sleep, widen frontier via `publishedFrontier()`, then the **single**
  `err = st.xlogWrite(writeRqst{write: frontier, flush: frontier})` (writer.go
  :999). `writeMu` is held across the *entire* `xlogWrite`.
- **`state.xlogWrite(rq)`** — `writer.go:2075`. Two stages, both under the
  caller's `writeMu` hold:
  - **Stage 1 — drain** (`writer.go:2092`+): `drainBufferUpTo(rq.write)`
    (pwrite via `writeAt` → `f.WriteAt` / AIO), then publish
    `drainedLSNMirror.Store(s.drainedLSN)` (`writer.go:2102`).
  - **Stage 1/2 boundary**: `if rq.flush == 0 { return nil }` (`writer.go:2106`).
  - **Stage 2 — flush** (`writer.go:2105`+ comment): sorted dirty-segment
    `doSync` (`Fdatasync`), then `flushedLSN = rq.flush` and
    `flushedLSNMirror.Store(rq.flush)` (`writer.go:2145`) — durable-then-publish.
- **`walWriteLock`** — `internal/wal/wal_write_lock.go:24`: `mu` (the lock),
  `genMu` + `gen chan struct{}` (generation broadcast). `acquireOrWait` (:44)
  is the tri-state park; `release` (:71) unlocks then closes+swaps `gen` to wake
  parked losers. `lock()` (:67) is the plain blocking acquire used by
  `BackgroundWrite`.
- **`Writer.backgroundFlushLocked(frontier)`** — `writer.go:1050`: the walwriter
  ticker's plain-lock pre-writer; also holds `writeMu` across its `xlogWrite`.

LSN accounting (`writer.go:400`–431, mirrors at :276–297):
`writeLSN`/`writeLSNAtomic` ≥ `drainedLSN`/`drainedLSNAtomic` ≥
`flushedLSN`/`flushedLSNAtomic`. The three-tier invariant is stress-checked by
`TestDrainSafetyStress`.

## 3. PostgreSQL reference

- `src/backend/access/transam/xlog.c`
  - `XLogWrite(WriteRqst, flexible)` — the write/flush loop. Both the **write**
    (`pg_pwrite` of full pages) and the **sync** (`issue_xlog_fsync`, at segment
    boundaries and at the requested flush point) execute **inside** this call,
    **under the single `WALWriteLock` hold** — the same backend does both, and no
    other backend enters `XLogWrite` during that fsync.
  - `XLogFlush(record)` → `WaitXLogInsertionsToFinish(WriteRqstPtr)` **before**
    `LWLockAcquireOrWait(WALWriteLock)`; on acquisition, re-reads
    `LogwrtResult.Flush`, and only if still insufficient calls `XLogWrite`. This
    ordering is the part goopg already mirrors (`flushUpToBackend` +
    `waitInsertionsToFinish` + `publishedFrontier` before `acquireOrWait`).
  - `issue_xlog_fsync(fd, segno)` — the fsync primitive, called from within
    `XLogWrite`, not as a separate lock episode.
- `src/backend/storage/lmgr/lwlock.c` — `LWLockAcquireOrWait` (goopg's
  `acquireOrWait`), `LWLockWaitForVar` / `LWLockUpdateVar` (the
  published-frontier wait, goopg's `waitInsertionsToFinish` + `publishedFrontier`).

**What this doc does NOT mirror from PG.** PG's write-path concurrency comes
from separating WAL *insertion* (`WALInsertLock`s, drained via
`WaitXLogInsertionsToFinish`) from the write+fsync — it does **not** overlap the
write of group N+1 with the fsync of group N, because both are one `WALWriteLock`
hold. The split in §4 adds exactly that overlap, going *beyond* PG's model. The
`WaitXLogInsertionsToFinish`-before-the-lock discipline (which goopg keeps) is
the genuine parity; the drain/fsync overlap is a goopg-specific enhancement.

## 4. Target design

Split `flushAsHolder`/`xlogWrite` so the `writeMu` hold covers **only** the
drain, and the `fdatasync` runs outside `writeMu`, serialized instead by a
dedicated **flush baton** with an LSN-ordered completion frontier.

### 4.1 Shape

Introduce a second, lighter lock `syncMu` (a plain `sync.Mutex`) and a
`syncFrontierReq atomic.Uint64` (the highest LSN any backend wants durable).
The holder protocol becomes:

```
flushAsHolder(lsn, frontier):                       // holds writeMu on entry
    recheck flushedLSN; commit_delay sleep; widen frontier   // unchanged
    xlogWriteDrainOnly(writeRqst{write: frontier})  // Stage 1 only, publishes drainedLSNMirror
    release writeMu                                 // <-- released BEFORE the fsync
    syncFrontierReq.storeMax(frontier)              // CAS-max: "someone must fsync up to here"
    if syncMu.TryLock():                            // become the syncer for this baton
        for:
            target = syncFrontierReq.Load()
            if target <= flushedLSN: break
            doSyncUpTo(target)                       // fdatasync dirty segments <= target
            flushedLSN = target; flushedLSNMirror.Store(target)  // durable-then-publish
            wakeFlushWaiters()                       // close a generation so parked backends recheck
        syncMu.Unlock()
    else:
        // capture-before-park, mirroring walWriteLock (wal_write_lock.go:48-61):
        // read the sync generation channel under syncGenMu BEFORE the failed
        // TryLock's recheck, so a syncer that raises flushedLSN and closes the
        // generation between our storeMax and our park cannot be missed.
        ch = captureSyncGen()
        if flushedLSNAtomic >= lsn: return
        park on ch (or w.done → ErrClosed); on wake, recheck flushedLSNAtomic
```

The **capture-before-park discipline is load-bearing** (finding from review):
without capturing the sync generation *before* the post-`storeMax` durability
recheck, a syncer that advances `flushedLSN` and closes the generation in the
window between our `storeMax` and our park would leave us blocked forever. This
is the identical missed-wakeup argument `walWriteLock` already carries
(`wal_write_lock.go:48`–61 / `docs/design/wal-backend-flush/03` §3.1); the
sync-generation primitive must replicate it, and slice S2 builds it that way.

- **Drain and fsync are now distinct critical sections.** Backend B can acquire
  `writeMu`, drain group N+1's ring bytes, and publish `drainedLSNMirror` while
  backend A is still inside `doSyncUpTo` for group N under `syncMu`. That is the
  overlap this design adds — one that neither goopg nor PG has today (§3).
- **The syncer coalesces.** Whoever wins `syncMu` fsyncs the *aggregate*
  `syncFrontierReq`, not just its own LSN — the group-commit batching effect
  survives, now on the sync side instead of the drain side. The re-loop
  (`target = syncFrontierReq.Load()` after each fsync) absorbs requests that
  arrived during the previous fsync: one fsync, N durable commits, as before.
- **Waiters park on a sync generation**, mirroring `walWriteLock`'s
  `gen`-channel mechanism (a second `genMu`/`gen` pair scoped to `syncMu`), so a
  non-syncer backend blocks on durability without spinning.

### 4.2 Decision log

- **D1 — dedicated `syncMu` baton vs. reuse `writeMu` for the fsync.** Chosen:
  separate `syncMu`. Reusing `writeMu` for the fsync is exactly today's
  (PG-equivalent) structure; the whole point of this doc is to let draining
  proceed during a peer's fsync — which PG does not do and which requires the
  two to be different locks. `syncMu` is uncontended-fast (TryLock) and only one
  syncer runs at a time — same single-fdatasync-at-a-time guarantee as today.
- **D2 — the syncer is "whoever gets there first," not a dedicated goroutine.**
  Chosen: emergent syncer (the committing backend), matching the
  wal-backend-flush philosophy (no dedicated writer goroutine, `RESULTS.md`).
  A dedicated fsync goroutine would reintroduce a channel hand-off per commit —
  the exact latency cost slice 6 deleted. Rejected.
- **D3 — `syncFrontierReq` is CAS-max, not a queue.** Chosen: a single atomic
  max. An explicit LSN-ordered queue (the sketch's literal "completion queue")
  is unnecessary because fsync of segment ≤ target covers *all* smaller LSNs by
  construction — durability is a monotone frontier, not per-request. The
  "completion queue" the sketch imagined collapses to one atomic + one
  generation broadcast. (Recorded as the sketch's over-specification.)
- **D4 — publication order is preserved.** `drainedLSNMirror` is stored at the
  end of Stage 1 (inside `writeMu`); `flushedLSNMirror` only after `doSyncUpTo`
  returns (inside `syncMu`). The `writeLSN ≥ drained ≥ flushed` invariant holds
  because drain always precedes the sync that covers the same LSN.
- **D5 — `commit_delay` stays a holder-only sleep under `writeMu`** (unchanged),
  because widening the drain frontier is what batches; the sync side batches
  automatically via the re-loop.

## 5. Invariants and failure modes

- **I1 — durability frontier monotonic.** `flushedLSN` only ever advances (each
  syncer stores `max`), and is published only after the covering `fdatasync`
  returns. A late/duplicate syncer that observes `target <= flushedLSN` exits
  without action.
- **I2 — no drained-but-never-synced gap.** Every `writeMu` holder that advances
  `drainedLSN` also `storeMax`es `syncFrontierReq` to at least that LSN before
  releasing, and either becomes the syncer or a syncer is already running (it
  will re-read the raised `syncFrontierReq`). The walwriter's write-only ticks
  (`flush == 0`) also raise `syncFrontierReq` if they leave a segment partially
  synced — carried from `xlogWrite`'s finishing-segment rule
  (`docs/design/wal-backend-flush/03` §3.2).
- **I3 — WAL-before-data unchanged.** `FlushUpTo(lsn)` still returns only after
  `flushedLSNAtomic >= lsn`; the buffer-pool/clog/checkpoint barriers are
  untouched (they call `FlushUpTo`, which now parks on the sync generation
  instead of returning from `flushAsHolder`).
- **F1 — syncer dies mid-fsync (panic).** `syncMu` must be released via `defer`
  in a panic-safe scope, re-panicking after, exactly as `flushAsHolder` does for
  `writeMu` today (`04` §4.7 M4) — else a panic leaks `syncMu` and wedges every
  commit. The parked waiters wake on `w.done` (`ErrClosed`) at shutdown.
- **F2 — fsync I/O error.** The syncer records the sticky error epoch (as today)
  **before** waking waiters; parked backends observe it in their recheck and
  return the error without needing `syncMu`. Retry idempotency holds: `doSyncUpTo`
  never advances `flushedLSN` early, so a re-fsync of the same segments is safe.
- **F3 — the accounting-lag race (the 2159d329 finding).** A drain publishes
  `drainedLSNMirror` and raises `syncFrontierReq`, but a concurrent evicting
  backend's `FlushUpTo` may request an LSN transiently beyond the syncer's
  in-progress `flushedLSN`. This is the *same* `ErrLSNNotWritten`/
  `ErrWALAccountingLag` transient the eviction path already retries
  (`bufpool.go:813` `flushWALWithRetry`). Doc 01 must keep that retry valid:
  the waiter's park-and-recheck loop is itself the retry, so the sentinel should
  surface only to non-parking callers (verify the eviction path still routes
  through a retry, not a fatal error).

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | split `xlogWrite` | extract `xlogWriteDrainOnly` (Stage 1 + `drainedLSNMirror`) and `doSyncUpTo` (Stage 2) as separate methods; `xlogWrite` becomes `drain(); if flush>0 { sync() }` — **behavior-identical**, still one `writeMu` hold. Pure refactor. | G-race, G-unit |
| S2 | `syncMu` + `syncFrontierReq` + sync-generation | add the baton, the CAS-max frontier, and the park/broadcast primitives; not yet wired into the commit path. | G-race, G-unit |
| S3 | rewire `flushAsHolder` | release `writeMu` after `xlogWriteDrainOnly`; raise `syncFrontierReq`; TryLock `syncMu` → `doSyncUpTo` loop, else park. `backgroundFlushLocked` (walwriter) uses the same split. **This is the concurrency change.** | G-race (`TestDrainSafetyStress`), G-crash, G-standby, G-waldump |
| S4 | error/panic/shutdown | sticky-error epoch on the sync side, panic-safe `syncMu` scope, `w.done` wake for parked sync-waiters, `Close()` drains both batons in order. | G-crash, G-race |
| S5 | perf acceptance | re-measure; confirm `END` converges toward PG and the `acquireOrWait` block share drops; check group width did not collapse. | G-perf, G-tpch |

Rollback: each slice is a single revertable commit; S1/S2 are inert until S3.

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| `TestDrainSafetyStress` | `internal/wal/*_test.go` (drain-safety) | S3 (extend: add concurrent syncers) |
| `TestFlushUpToPreEnqueueFastExit` | `internal/wal/` | S3 (must still pass verbatim) |
| group-commit effect tests (`fsyncCount < concurrent committers`) | `internal/wal/group_commit_test.go` | S3 (assert overlap: drained LSN advances during an in-flight fsync) |
| `wal_write_lock` tests | `internal/wal/wal_write_lock_test.go` | S2 (sibling sync-generation tests) |
| crash/recovery | `internal/initdb/`, `TestKillKillRecovery` | S3, S4 |
| `pg_waldump` parity | `internal/wal/pg_waldump_compat_test.go` | S3 (on-disk format unchanged — a guard, not a change) |

## 8. Performance verification

`analysis/perf-optimize3/scripts/run_rw50.sh` (now `-M prepared`, doc 13) at
scale 100, `GOOPG_BLOCK_PROFILE_RATE=1`. Success criteria:

1. `-N` `END` per-statement latency drops toward PG's (target: within ~10 %).
2. The `walWriteLock.acquireOrWait` block-delay share falls (overlap is
   working: some of the wait moves to the shorter sync-generation park).
3. `drainedLSNAtomic` observably advances while a fsync is in flight (a new
   assertion in the group-commit test), which is the mechanism proof.
4. Group width (txns/fsync) does not regress vs the pre-C5 baseline.
5. `aux2_fsync_probe.sh` fdatasync count per 60 s unchanged-or-lower at equal
   TPS.

## 9. Open questions

- **O-C5-1** — Does the walwriter's write-only tick (`flush == 0`) ever need to
  *become* the syncer, or only raise `syncFrontierReq`? Under
  `wal_writer_flush_after` gating the walwriter sometimes must fsync; decide
  whether it competes for `syncMu` or always defers to a committing backend.
- **O-C5-2** — Interaction with SyncRep (`operators_tx.go` waits after
  `FlushUpTo`): confirm the standby-ack wait still composes correctly when the
  local durability now comes from a parked sync-generation wake rather than a
  direct `flushAsHolder` return.
- **O-C5-3** — Is a single `syncFrontierReq` atomic enough, or does a
  multi-segment fsync batch benefit from tracking per-segment sync debt to avoid
  re-fsyncing a segment a walwriter already synced? (Likely no — `dirty` map
  already dedupes — but verify under the write-only walwriter interleaving.)
