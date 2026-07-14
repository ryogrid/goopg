# 04 — Concurrency model and invariants

status: draft · date: 2026-07-12

## 4.1 State ownership migration

`writeMu` (the `walWriteLock`) becomes the owner of the write/flush machinery —
everything that today is implicitly "loop-goroutine-local":

| field (today) | new owner | notes |
|---|---|---|
| `s.files map[uint64]*os.File` | `writeMu` | `openSegment`, `writeAt`, `removeOldSegments`, `close` all run under `writeMu`. Concurrent `pwrite`/`fdatasync` on a shared `*os.File` is OS-safe (positioned I/O), but the **map mutations are not** — this is the hazard that forces the ownership move. |
| `s.dirty map[uint64]bool` | `writeMu` | set in `writeAt`, cleared by Stage-2 sync / finishing-segment fsync / recycle |
| `s.drainedLSN` | `writeMu` authoritative + **new `drainedLSNAtomic` mirror** | this *is* PG's `logWriteResult` ("written to OS cache"); mirror published after Stage 1 |
| `s.flushedLSN` | `writeMu` authoritative; `flushedLSNAtomic` mirror unchanged (published after Stage 2) | |
| walBuf `head`/`base` advance (drain side) | `writeMu` | the ring's atomics (0107-0007ai) already permit drain concurrent with RLock appenders; `writeMu` guarantees at most one drainer — §4.4 |
| `s.writePos`, `s.prevRecPtr`, `walBuf` reset (slow paths) | stay under `appendMu.Lock`; **the entire drain-coupled tail** — `drainBufferBytes`, `writeAt`, `walBuf.reset`, the `drainedLSN` update, `core.resetPosition` — additionally runs under `writeMu` nested inside that `appendMu.Lock` section (not just vaguely "the I/O portions": a `writeMu`-only walwriter drain must never race a Path-A reset) | |

Stay lock-free atomics: `writeLSNAtomic` (insert-publish frontier, CAS-max by
backends — unchanged), `flushedLSNAtomic`, new `drainedLSNAtomic`.
`DrainedLSN()`/`readBufferedAt` observers switch to the atomic mirror instead
of taking `appendMu.Lock`.

## 4.2 Lock ordering

Single global order — acquire left-to-right, never right-to-left:

```
appendMu (RLock or Lock)  →  stripe locks (under RLock only)  →  writeMu
```

- Fast-path appenders: `appendMu.RLock` → stripe lock. Never touch `writeMu`.
- Slow-path append / appendRaw: `appendMu.Lock` → `writeMu` (nested, covering
  the whole drain-coupled tail — §4.1).
- **Committing flusher and walwriter: `writeMu` only, never `appendMu`** —
  from slice 3 onward. (Adversarial review rejected a drain-under-
  `appendMu.Lock` variant: taking `appendMu` *before* `writeMu` serializes all
  committers on `appendMu` across the whole fsync and kills emergent group
  commit; taking it *after* is an ABBA deadlock against the slow paths.)
- checkpointer recycle / Close: `writeMu` (Close additionally takes
  `appendMu.Lock` first, honoring the order).
- **Nothing acquires `appendMu` while holding `writeMu`** — enforced by
  convention + a debug assertion in `-race` builds (implementation task).

Extends the `0107-0007ah` lock table with the `writeMu` tier (supersession
note there, implementation slice 3).

## 4.3 `waitInsertionsToFinish` — the `WaitXLogInsertionsToFinish` analog

Built on the existing primitives (`insertionTracker.lowestActiveLSN`,
`stripeWriterCore.PublishUpTo`):

```go
// Run BEFORE acquiring writeMu. Returns a published frontier >= lsn.
func (s *state) waitInsertionsToFinish(lsn uint64) uint64 {
    for i := 0; ; i++ {
        curr, _ := s.core.Load()                 // highest reserved LSN
        tail := s.core.PublishUpTo(int64(curr))  // safe contiguous frontier
        if uint64(tail) >= lsn { return uint64(tail) }
        if i < 64 { runtime.Gosched() } else { time.Sleep(10 * time.Microsecond) }
    }
}
```

The in-flight window it waits out is a bounded memcpy under a stripe lock —
microseconds — so spin-yield suffices; a per-stripe `sync.Cond` (PG's
`LWLockWaitForVar`) is a follow-up, not v1.

**Prerequisite — `publishTail` must become CAS-max (adversarial-review M2).**
Today `walBuffer.publishTail` is Load-then-Store and documented as
single-drain-goroutine-only; this design makes `PublishUpTo` a hot
multi-caller path (every waiter's spin iteration, the holder's widen, the
walwriter's pre-lock frontier — concurrent with the existing fast-path RLock
publishers). Two racing publishers can regress the tail
(A computes 100, B computes-and-stores 105, A stores 100), leaving bytes
counted in neither `reservedBytes` nor `resident()` — `tryReserve` can then
grant ring space overlapping undrained WAL bytes: **corruption**. Fix: make
`publishTail` a CAS-max loop (mirror `storeMaxLSN`); land it in slice 1 as a
hard precondition of slice 3. (The race is latent at HEAD between two RLock
`tryAppend` peers; this design merely makes it hot.) `resetPosition`/
`walBuf.reset` are `writeMu`-covered and forward-only (§4.1), so a stale
CAS-max can never regress below a reset point. `MemRing.PublishUpTo` is
already mutex-guarded and monotonic — unaffected.

**Legacy (non-pageHeaders) mode branch (adversarial-review M3).** Legacy
appends bypass the stripe core entirely (`core.Load()` stays frozen), so the
loop above would spin forever. In legacy mode there is no in-flight-insertion
window at all — legacy appends complete atomically under `appendMu` — so
`waitInsertionsToFinish` degenerates to "return the current published write
frontier (`writeLSNAtomic`)" with no waiting. The walwriter's legacy handling
in 03 §3.4 already says the same; this makes it explicit for the commit path.

**Spin-cost note (m11):** `core.Load()` takes the global reservation mutex;
the spin loop must not hammer it per iteration from every waiting committer —
read the published tail (atomic) for the recheck and call
`Load()`/`PublishUpTo` only every few iterations.

**Discipline carried from PG verbatim (02 §2.5):**

1. **Wait before `writeMu`, never while holding it.** Today no fast-path
   inserter can block on `writeMu` (`tryReserve` fails cleanly *before* any LSN
   reservation, so an in-flight reservation never waits on a drain), which
   makes waiting inside currently deadlock-free — but slow-path appenders *do*
   take `writeMu`, and keeping PG's ordering makes the invariant future-proof
   and shortens hold time. Inside the lock the holder calls `PublishUpTo` once
   more to widen the frontier — publish-only, no waiting.
2. **The flush target is the published frontier, not the caller's LSN** — the
   over-flush *is* the group-commit batching.

This replaces the implicit safety of the single drainer ("tail is safe because
only the loop drains") with an explicit published frontier.

## 4.4 Drain vs. concurrent appenders

**Drain-without-`appendMu` is today's behavior, not an innovation of this
design.** At HEAD, the loop's flush drain (`flushUpTo` → `drainBufferUpTo` →
`drainBufferBytes`) runs on the writer goroutine **without** `appendMu`, fully
concurrent with RLock stripe appenders; that safety is already load-bearing via
0107-0007ai: walBuf `head`/`base` are atomics; `readForDrain` reads only
`[head, tail)`; stripe writers under `appendMu.RLock` write only into reserved
space at/above `tail`; `advanceHead` only grows the free space that
`tryReserve`'s CAS observes. The flusher (a committing backend under `writeMu`)
simply inherits this — same interleavings, different executor.

**What changes is the single-drainer guarantee's source.**
**0107-0007ai's single-drainer assumption is superseded, not violated**: it
becomes "the single drainer is whoever holds `writeMu`" — dynamic identity,
still exactly one at a time. Two corollaries:

1. Every remaining `writeAt`/drain/reset site — including the slow paths'
   `walBuf.reset`/`resetPosition` — must hold `writeMu` (nested inside their
   `appendMu.Lock`, §4.1/§4.2) from slice 3 onward, or `files`/`dirty`/the
   ring head race with a backend holder.
2. **Slice 7 is re-scoped**: with the flusher `writeMu`-only from day one,
   "drop `appendMu` from the flusher" is vacuous. Slice 7 becomes the
   exhaustive reset-site audit + the heavy race-stress gate (concurrent
   append+flush+recycle loops) that certifies the corollary-1 coverage.

## 4.5 LSN invariant chain and publication order

Invariant (PG `Insert >= Write >= Flush`):

```
PublishedTail  >=  drainedLSNAtomic  >=  flushedLSNAtomic
  (published)        (OS cache)            (durable)
```

**The Insert frontier is the published tail, not `writeLSNAtomic`** (m8): at
HEAD, `tryAppend` publishes the tail *before* its CAS-max store to
`writeLSNAtomic`, so `writeLSNAtomic >= drainedLSNAtomic` is transiently false
— a holder can drain to the tail before the mirror store lands. Consequently
`xlogWrite` must validate its target against the published tail (not
`writeLSNAtomic`, as today's `ErrLSNNotWritten` guard does — a widened
frontier could spuriously trip it).

Publication order in the holder: store `drainedLSNAtomic` after the pwrites,
*then* fsync, *then* store `flushedLSNAtomic`. Go's sequentially-consistent
atomics provide the Write→Flush store barrier PG inserts manually
(xlog.c:2573-2581); a reader needing both reads flushed first, then drained
(the reverse order, matching `RefreshXLogWriteResult`). `flushedLSNAtomic` is
published **only after `doSync` returns** — the same durable-then-publish rule
fix-03 established; any earlier store would let the fast exit return before
durability.

## 4.6 Crash safety (WAL-before-data) — unchanged

The `FlushUpTo(lsn)` contract is preserved bit-for-bit: *returns only after
every byte ≤ lsn is fdatasync-durable*. All WAL-before-data barriers
(bufpool page writeback, clog page writeback, checkpoint marker) call the same
function and keep the same guarantee; only the goroutine executing the fsync
changes. Recovery (`ReplayFromDir`, stream replayer) is read-side and
untouched. Kill-9 crash-recovery tests are a per-slice gate (05).

## 4.7 Error, panic, and shutdown semantics

- **I/O errors**: the holder returns the error to its caller. To guarantee
  termination for the losers (m5): the holder also records the failure in a
  **sticky error epoch** (`atomic` lastErr/epoch); waiters check it in their
  recheck and return the error **without** needing to win the barging lock —
  otherwise "every caller eventually becomes holder" is only probabilistic
  under a continuous committer stream, and every winner would re-run a full
  drain+fsync against a failing disk with no backoff. Retry idempotency holds
  regardless: `drainBufferBytes` advances `head`/`drainedLSN` only after both
  chunk writes succeed, so a retry re-reads identical ring bytes to identical
  offsets, and Stage 2 never advances `flushedLSN` early. *Deliberate
  divergence from PG*, which PANICs on WAL write/fsync failure; goopg's
  error-return convention is kept. Revisit if fsyncgate-style semantics ever
  matter.
- **Holder panics (adversarial-review M4 — mandatory):** goopg recovers
  backend-goroutine panics per connection (`server.serveConn`), so a panic
  between acquiring `writeMu` and `release()` would be swallowed and leak the
  lock **forever** — every subsequent commit, clog barrier, and bufpool
  eviction hangs on a live server (today the same panic kills the unrecovered
  loop goroutine → process crash → clean WAL recovery — strictly better).
  The holder section MUST run in a panic-safe scope: `defer release()` in a
  holder-scoped function, re-panic after releasing (letting the existing
  crash/recovery semantics apply), never a bare unlock at the end.
- **Shutdown**: `Close()` sets the closed flag and `close(w.done)` — waiters
  parked in `acquireOrWait` wake via the `closed` arm with `ErrClosed`.
  Close itself takes `appendMu.Lock` then `writeMu` (honoring the order),
  runs the final `xlogWrite{write:flush:end}`, waits `eagerWG` (preallocation
  workers — ordering constraint unchanged from today's `s.close()`), then
  closes FDs. Two sharp edges (m9):
  - a late `FlushUpTo`'s *first* `TryLock` runs before any closed check, so
    either **Close never releases `writeMu`** or every holder re-checks
    `closed` under the lock and returns `ErrClosed` before touching
    `files`/`dirty` — pick one and enforce it (this design picks the
    holder-side recheck; it also covers the walwriter);
  - `BackgroundWrite`'s plain `mu.Lock()` has no `done` escape, so the
    Runtime-level ordering (stop the walwriter ticker **before** WAL close,
    01 §1.6) is load-bearing, not just tidy — document it as a hard
    invariant. (The `Append` fast path likewise has no closed check today;
    pre-existing, same runtime-ordering reliance.)

## 4.8 Integration seams

| seam | effect |
|---|---|
| clog flush hook | same `FlushUpTo` value; barrier now executes on the clog-writing goroutine — WAL-before-data preserved |
| checkpointer | marker flush + `RemoveOldSegments*` become direct calls; recycle runs under `writeMu`; the checkpointer never holds `writeMu` across a `FlushUpTo` → no self-deadlock |
| bufpool WAL-before-data | unchanged call sites; an evicting backend doing its own WAL fsync *is* PG behavior |
| AIO seam | `writeAt`'s `aio.Submit(...).Wait()` already runs from many goroutines via bufpool; under `writeMu` there is at most one WAL submitter at a time |
| memRing / walsender | fed-on-append (0010-0002), untouched; `notifyAppend` subscribers untouched |
| SyncRep | unchanged: waits after `FlushUpTo` returns (`operators_tx.go`) |
| wait events | `OnWALWrite`/`OnWALSync` now fire on the goroutine actually doing the I/O — pg_stat_activity attribution becomes accurate without plumbing changes |
