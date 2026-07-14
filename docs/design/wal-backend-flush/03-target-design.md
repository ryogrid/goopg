# 03 — Target design

status: draft · date: 2026-07-12

Mirrors PG's control flow (02) onto goopg's goroutine runtime. All new code
lives in `internal/wal/` plus small call-site changes in `internal/initdb/open.go`.

## 3.1 `walWriteLock` — the `LWLockAcquireOrWait` analog

New file `internal/wal/wal_write_lock.go`. Recommendation: **`sync.Mutex.TryLock`
+ a swap-on-release generation channel.** (Rejected: an LWLock clone over
`runtimeshim`'s semaphore linknames — fragile for no gain; `sync.Cond` — cannot
`select` against shutdown.)

```go
type walWriteLock struct {
    mu    sync.Mutex    // the WALWriteLock itself
    genMu sync.Mutex    // guards the generation swap
    gen   chan struct{} // closed once per release ("one flush completed")
}

// acquireOrWait: (true, nil)  = caller holds mu and must release().
//                (false, nil) = a release happened while we waited; caller
//                               holds NOTHING and must re-check shared state.
//                (false, err) = writer closed.
func (l *walWriteLock) acquireOrWait(closed <-chan struct{}) (bool, error) {
    if l.mu.TryLock() { return true, nil }
    l.genMu.Lock(); ch := l.gen; l.genMu.Unlock() // capture gen BEFORE retry
    if l.mu.TryLock() { return true, nil }
    select {
    case <-ch:     return false, nil
    case <-closed: return false, ErrClosed
    }
}

func (l *walWriteLock) release() {
    l.mu.Unlock()
    l.genMu.Lock()
    close(l.gen)
    l.gen = make(chan struct{})
    l.genMu.Unlock()
}
```

**Missed-wakeup argument** (adversarially reviewed; holds). Generations
advance only in `release()`. A waiter captures gen `N` under `genMu`, then
TryLocks. If TryLock fails, the mutex is locked *or an acquisition is pending*
(Go's starvation-mode handoff can fail TryLock on a technically-unlocked
mutex with queued acquirers) — either way a future `release()` runs, and it
closes whichever gen is current at release time; `N` stays current until some
release closes exactly `N`. So the first release after the capture closes `N`
and wakes the waiter. Capture and close/swap are both `genMu` critical
sections, so a waiter can never capture an already-swapped channel that will
never close. If the second TryLock succeeds, the waiter never touches `ch`.
A release between capture and the second TryLock only makes the select wake
immediately (benign). `flushedLSNAtomic` is stored before `release()`, and a
channel close is a happens-before edge, so the post-wake recheck always
observes the closing holder's flush. No lost wakeup; no permanent block.

**Fairness and coverage.** TryLock is barging, like PG's LWLock. Progress does
not depend on lock fairness, but the coverage claim needs its two real
qualifiers (m6):

- the guarantee comes from the **widen-inside-the-lock** step: a holder that
  acquired *before* the waiter published may flush a frontier below the
  waiter's LSN; it is the *next flush-requesting holder that acquired after
  the waiter's publish* whose widened frontier (`PublishUpTo` under the lock)
  must cover it — that widen is load-bearing, not an optimization;
- a **walwriter** hold can interleave: its frontier is computed before its
  plain `Lock()` and may be page-rounded with `flush=0`, so its release wakes
  waiters without covering them — a benign extra generation.

Wall-clock bound: at most the straddling hold + one full committer hold
(+ one interleaved walwriter hold). The generation *count* is finite but not a
small constant (delayed `close(gen)` after `Unlock` can bank cheap spurious
wakes); each spurious wake costs one recheck. A waiter exits via the recheck,
not via acquisition.

**Threads.** No `LockOSThread` anywhere. `fdatasync` blocks the holder's OS
thread; the Go runtime hands the P to another thread (~20 µs sysmon retake) and
the rest of the server keeps running — same as any blocking syscall goopg
already issues from backends.

## 3.2 `xlogWrite(writeRqst)` — the `XLogWrite` analog

Refactor today's `state.flushUpTo` into a write/flush-split routine.
**Precondition: caller holds `writeMu`** (the `walWriteLock`).

```go
type writeRqst struct{ write, flush uint64 } // flush == 0 → write-only

func (s *state) xlogWrite(rq writeRqst) error
```

- **Stage 1 — drain** `[drainedLSN, rq.write)` from the walBuf ring to segment
  files (`readForDrain` → `writeAt` → `openSegment`; `f.WriteAt` or the AIO
  seam). At most two contiguous chunks per call (ring wrap) — the analog of
  PG's npages batching.
  - **NEW: unconditional finishing-segment fsync.** Whenever the drain crosses
    a segment boundary, `doSync` that completed segment immediately and remove
    it from `dirty` — PG xlog.c:2464-2505. This is what makes the walwriter's
    write-only mode safe: a *completed* segment never accumulates unbounded
    unsynced bytes.
  - Publish `drainedLSNAtomic` (new mirror; = PG `logWriteResult`).
- **Stage 2 — flush** (only when `rq.flush > 0`): today's sorted
  dirty-segment `doSync` loop up to `segmentForLSN(rq.flush)`; publish
  `flushedLSNAtomic` after the last fsync returns. Publication order
  Write-then-Flush; Go's seq-cst atomics supply the barrier PG writes manually.
- `dirty` bookkeeping stays inside `writeAt`; `files` FD cache and `dirty` are
  now `writeMu`-owned state (04 §4.1).
- PG's close-FD-on-segment-switch is **not** adopted: goopg's `files` cache +
  `opRecycle`-driven retirement already bounds open FDs. Noted follow-up.

Callers and their request shapes:

| caller | writeRqst |
|---|---|
| committing backend (`FlushUpTo`) | `{write: frontier, flush: frontier}` |
| background walwriter (`BackgroundWrite`) | `{write: pageRoundDown(frontier), flush: 0 or frontier}` per §3.4 policy |
| ring-overflow drain (slow-path append / `tryAppend` fallback) | `{write: neededFrontier, flush: 0}` — the `AdvanceXLInsertBuffer` analog (02 §2.4) |

## 3.3 The new `FlushUpTo` — emergent group commit

`flushGroup`, `groupFlushReq`, `handleGroupFlush`, and the `fg.signal` channel
are **deleted**. Replacement (runs entirely on the calling backend goroutine):

```go
func (w *Writer) FlushUpTo(lsn uint64) error {
    if lsn == 0 { return nil }
    if lsn <= w.flushedLSNAtomic.Load() { return nil }   // fast exit (unchanged, fix-03)
    if w.OnWALSync != nil { w.OnWALSync() }              // wait event — now truly per-backend
    if w.OnWALSyncDone != nil { defer w.OnWALSyncDone() }
    w.flushWaiters.Add(1); defer w.flushWaiters.Add(-1)  // commit_siblings signal
    frontier := st.waitInsertionsToFinish(lsn)           // BEFORE the lock (04 §4.3)
    for {
        if lsn <= w.flushedLSNAtomic.Load() { return nil }   // someone covered us
        if err := w.stickyErr(); err != nil { return err }   // failing disk: exit without the lock (m5)
        held, err := st.writeMu.acquireOrWait(w.done)
        if err != nil { return err }
        if !held { continue }                            // woken w/o holding → recheck
        done, err := st.flushAsHolder(lsn, &frontier)    // panic-safe holder scope (M4)
        if done || err != nil { return err }
    }
}

// flushAsHolder runs the holder section with a DEFERRED release: goopg
// recovers backend panics per connection, so a bare unlock would leak
// writeMu forever on any panic in xlogWrite (permanently wedging every
// commit on a live server). Deferred release + re-panic preserves today's
// crash-then-recover semantics instead.
func (st *state) flushAsHolder(lsn uint64, frontier *uint64) (done bool, err error) {
    defer st.writeMu.release()
    if st.closed { return true, ErrClosed }              // Close-race recheck (04 §4.7)
    if lsn <= st.flushedLSN { return true, nil }         // recheck under the lock
    if d := commitDelay.Load(); d > 0 &&                 // GUC, DEFAULT 0 (PG-faithful)
        st.w.flushWaiters.Load() >= commitSiblings.Load() {
        time.Sleep(time.Duration(d) * time.Microsecond)  // holder-only sleep (02 §2.2)
    }
    if t := st.publishFrontier(); t > *frontier { *frontier = t } // widen, no waiting
    err = st.xlogWrite(writeRqst{write: *frontier, flush: *frontier})
    if err != nil { st.w.setStickyErr(err) }             // let losers exit lock-free (m5)
    return true, err
}
```

- **Batching is emergent**: committers arriving during a holder's fsync park
  on the generation channel; on release they observe
  `flushedLSNAtomic >= lsn` (their records were published before the holder
  widened its frontier) and return with zero I/O. One fdatasync, N commits —
  today's batching effect without the queue or the hand-off.
- **`commit_delay` semantics restored to PG's** (00 D2): a real GUC,
  **default 0 → the sleep never fires unless an operator opts in**; when set,
  only the holder sleeps, holding `writeMu`, gated on
  `flushWaiters >= commit_siblings` (the `MinimumActiveBackends` analog —
  counting in-flight flushers is cheaper and a better predictor than scanning
  the activity registry, and matches the spirit of PG's gate).
- **Error propagation**: the holder returns the error AND records it in a
  sticky error epoch; losers observe it in their recheck and return the error
  **without** winning the barging lock — guaranteeing termination and
  preventing a committer stampede from re-running drain+fsync against a
  failing disk. (PG PANICs on WAL I/O errors; goopg keeps its error-return
  convention — deliberate divergence, 04 §4.7.)
- **Legacy (non-pageHeaders) mode**: `waitInsertionsToFinish` must branch —
  legacy appends bypass the stripe core, so the frontier is simply the
  published write LSN with no waiting (04 §4.3), or the loop spins forever.
- The fix-03 fast exit and its test (`TestFlushUpToPreEnqueueFastExit`) survive
  verbatim; the group-commit tests are rewritten against observable effects
  (fsyncCount < concurrent committers) rather than queue mechanics.

## 3.4 Background walwriter — `Writer.BackgroundWrite()`

Replaces `FlushUpTo(WrittenLSN())` in the `initdb/open.go` ticker. Policy =
`XLogBackgroundFlush` (02 §2.6):

```
every wal_writer_delay (200 ms):
  frontier = publishFrontier()                       // PublishUpTo, no waiting
  target   = pageRoundDown(frontier)                 // 8 KiB pages (pageHeaders mode);
                                                     // raw frontier in legacy mode
  flush    = target if wal_writer_flush_after == 0
                    || bytesWrittenSinceLastFlush >= wal_writer_flush_after
                    || now - lastFlushTime >= wal_writer_delay
             else 0                                  // write-to-OS-cache only
  if target <= drainedLSNAtomic && flush == 0: skip  // idle round
  writeMu.mu.Lock()                                  // PLAIN acquire (PG parity)
  xlogWrite(writeRqst{write: target, flush: flush})
  writeMu.release()
```

- `wal_writer_flush_after` **already exists as a registered GUC**
  (`internal/config/defaults.go`, BootVal 1048576 = 1 MiB — PG's default —
  and already present in `postgresql.conf.sample`) but has **zero behavioral
  readers today**; this design wires it into the fsync-throttle above.
  `wal_writer_delay` (existing GUC, 200 ms) unchanged.
- The clog WAL-before-data hook (`clog.SetFlushWALHook`) **stays a synchronous
  `FlushUpTo`** — it is goopg's ordering barrier for async commits, not PG's
  fire-and-forget `XLogSetAsyncXactLSN`. Hibernation and an asyncXactLSN-style
  latch wake are noted follow-ups, not v1.
- The pg_stat_activity "walwriter" backend row (today registered by the
  state-loop's `OnLoopStart`) moves to this ticker goroutine — the honest
  PG-equivalent process.

## 3.5 Fate of the writer goroutine: deleted

End state (slice 6): `state.loop` and the ops channel are gone. PG has no
equivalent, and a permanently-retained "reduced loop" would keep `files`/`dirty`
dual-owner — the hazard this redesign exists to kill.

| today's op | end state |
|---|---|
| `opAppend` (slow/overflow) | caller context under `appendMu.Lock` (+ `writeMu` for its I/O portion) |
| `opAppendRaw` (walreceiver) | caller context, same locks (the lock order makes it safe regardless of concurrent flushers — a standby's restartpoint checkpointer and bufpool barriers do call `FlushUpTo`) |
| `opFlush` | already dead — deleted |
| `opRecycle` | direct method on the checkpointer's goroutine under `writeMu` (touches only `files`/`dirty`/dirents) |
| `opWALBufStat` | direct atomic reads |
| `opClose` | `Close()` takes `appendMu.Lock` + `writeMu`: final `xlogWrite`, `eagerWG.Wait()` (ordering unchanged), close FDs, set closed + `close(w.done)` (wakes gen-channel waiters with `ErrClosed`) |

Remaining background goroutines: the walwriter ticker (§3.4), eager-prealloc
workers (unchanged), checkpointer (unchanged), AIO engine internals (unchanged).
The loop's `LockOSThread` pin disappears with it.
