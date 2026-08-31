# 0107-0007ah — WAL `tryAppend` RWMutex: parallel stripe writers from concurrent backends

**Status:** accepted  
**Date:** 2026-05-21  
**Milestone:** M0107-0007 (slice B call-site rewrite part 3 of N)

## Summary

Changes `state.appendMu` from `sync.Mutex` to `sync.RWMutex` and switches
`tryAppend`'s PG-compat path from `Lock()/Unlock()` to
`RLock()/RUnlock()`.  This is the main TPS-improvement step for slice B:
multiple backend goroutines that previously serialised on `appendMu.Lock()`
now proceed in parallel, each acquiring only their own per-stripe lock inside
`stripeWriterCore.AppendXLogPayload`.

## Problem

After call-site rewrite part 2 (`state.append` / `state.tryAppend` mount
`core.AppendXLogPayload`), the internal WAL append is stripe-safe, but
`tryAppend` still held a full `sync.Mutex` lock.  Under c=100 SU pgbench,
100 backend goroutines serialise on this one mutex — peak concurrency = 1.
The stripe infrastructure (8 `paddedMutex` stripes, `insertPosTracker`,
`walBuffer.writeReserved`) was designed for 8-way parallel writes, but
`appendMu.Lock()` in `tryAppend` nullified that by admitting only one backend
at a time.

## Solution: RWMutex with two-tier locking

```
RLock (multiple holders, concurrent):
    tryAppend PG-compat Path B
    — stripe locks inside core.AppendXLogPayload provide per-stripe serialisation
    — walBuf bytes written to disjoint LSN ranges by construction
    — walBuf.head/base stable (only mutated under Lock by advanceHead)

Lock (exclusive, waits for all RLock to release):
    appendPGCompat Path A  (direct-to-disk write + resetPosition)
    appendPGCompat Path B  (may call drainBufferBytes → advanceHead)
    appendRaw              (may drain + resetPosition)
    append non-pageHeaders (walBuf.append is not stripe-safe)
    DrainedLSN / readBufferedAt readers that observe drainedLSN
```

PG's analogy: `WALInsertLocks[i]` (per-stripe reader locks) vs. the
checkpoint/recovery code that takes all 8 locks exclusively before resetting
the WAL buffer.

## Changes

### `writer.go`

**`appendMu`** field: `sync.Mutex` → `sync.RWMutex`; comment rewritten
to document the two-tier access pattern.

**`tryAppend` PG-compat path**:
- `s.appendMu.Lock()` → `s.appendMu.RLock()`
- `defer s.appendMu.Unlock()` → `defer s.appendMu.RUnlock()`
- Drop `s.writePos = ...` update — Lock() paths derive it from
  `s.core.Load()` after all RLock holders finish.
- Drop `s.writeLSN = end` update — `flushUpTo` / `close()` now use
  `writeLSNMirror.Load()`.
- Drop `s.prevRecPtr = ...` update — the stripe tracker maintains `prev`
  internally; Path A reads it via `core.Load()`.
- `s.writeLSNMirror.Store(end)` → `storeMaxLSN(s.writeLSNMirror, end)`
  — CAS-max because multiple concurrent RLock holders may update
  `writeLSNMirror` simultaneously (plain Store would let a goroutine with
  a lower LSN overwrite a higher one finished first by a peer).

**`appendPGCompat` Path A** (Lock, exclusive):
- `writePos := s.writePos` → `trackerCurr, trackerPrev := s.core.Load();
  writePos := int64(trackerCurr)` — derives from the tracker so that
  concurrent RLock tryAppend goroutines that advanced the tracker before
  Lock() was acquired are not overwritten.
- `encodeRecordXLog(payload, s.prevRecPtr)` → `encodeRecordXLog(payload,
  trackerPrev)` — same rationale.
- `s.prevRecPtr = start - 1` removed — tracker's `resetPosition(end,
  start-1)` that already follows is the authoritative update.

**`appendPGCompat` Path B** (Lock, exclusive — drain capability retained):
- `s.prevRecPtr = ...` update removed (tracker is authoritative).
- Comment updated.

**`appendRaw`** (Lock, exclusive):
- Both Path A and Path B now derive `writePos` from `s.core.Load()` so
  concurrent tryAppend goroutines that advanced the tracker before Lock()
  was acquired are accounted for.
- Both paths call `s.core.resetPosition(end, trackerPrev)` at the end.
  Without this, the tracker's `curr` stayed at the pre-`appendRaw` position;
  the next stripe write from `tryAppend` would then reserve starting at that
  old position, overwriting the raw bytes.  `trackerPrev` is the pre-write
  tracker prev, so the xl_prev chain "skips" the raw bytes — same behaviour
  as before this change since `appendRaw` was never in the regular xl_prev
  chain.  This also fixes a pre-existing latent bug: `appendRaw` had never
  called `resetPosition`, making concurrent `Append` + `AppendRaw` unsafe
  even under the old Mutex (the first `Append` after `AppendRaw` would
  overwrite the raw stream).

**`flushUpTo`**: `if lsn > s.writeLSN` → reads
`max(s.writeLSN, s.writeLSNMirror.Load())` so tryAppend-written LSNs
are visible without acquiring appendMu.

**`close()`**: `s.writeLSN > 0; flushUpTo(s.writeLSN)` → same
`max(s.writeLSN, writeLSNMirror.Load())` pattern.

**New helper `storeMaxLSN(ptr *atomic.Uint64, val uint64)`**: CAS-max
loop used by all concurrent writeLSNMirror writers.

### Safety properties preserved

| Property | How preserved |
|---|---|
| walBuf.head / base consistency | Only mutated by `advanceHead` under Lock(); RLock() excludes Lock(), so no concurrent read/write on non-atomic head/base |
| WAL gap prevention | `walBuf.free()` check under RLock() is safe (head stable while RLock held); if buffer is overcommitted (extremely rare), `walBuffer.writeReserved` returns `errWALBufferReservedOutOfRange` propagated as an error, not silent corruption |
| Tracker position consistency | Lock() paths call `core.Load()` under exclusive access; `resetPosition` called before Lock() release |
| PG-compat byte stream | tryAppend calls the same `core.AppendXLogPayload` as before; bytes emitted are identical |
| xl_prev chain | `insertPosTracker.reserveAndPublish` (under `posMu`) maintains the chain atomically across stripes; tryAppend no longer stamps `s.prevRecPtr` directly |

## PG counterpart

`XLogInsertRecord` (`postgres/src/backend/access/transam/xlog.c`) acquires
`WALInsertLockAcquire(MyProcNumber % NUM_XLOGINSERT_LOCKS)` — a per-stripe
lightweight lock — and multiple backends proceed in parallel on different
stripes.  The global `appendMu.Lock()` behaviour was equivalent to PG
grabbing all 8 WAL insert locks exclusively, which only happens during
checkpoints and recovery reset.  The RWMutex change aligns goopg with PG's
normal multi-backend concurrency model for WAL insertion.

## Regression tests

Five tests in `internal/wal/tryappend_rwmutex_test.go`:

| Test | What it pins |
|---|---|
| `TestAppendMuIsRWMutex` | Compile-time proof: `state.appendMu` is `*sync.RWMutex` |
| `TestConcurrentTryAppendProceedsInParallel` | 8 goroutines append simultaneously; all records distinct and error-free; peak concurrency > 1 (fails to reach > 1 under old `sync.Mutex`) |
| `TestFlushUpToSeesLSNFromConcurrentTryAppend` | `FlushUpTo(endLSN)` succeeds for LSNs written by concurrent tryAppend goroutines — pins the `writeLSNMirror` fix in `flushUpTo` |
| `TestAppendRawResetsTrackerSoSubsequentAppendDoesNotOverwrite` | Post-`AppendRaw` Append starts at `endRaw`, not at the pre-raw position — pins the `resetPosition` fix in `appendRaw` |
| `TestTryAppendRLockDoesNotBlockSiblings` | Two goroutines holding RLock concurrently; goroutine B acquires in << 50 ms while A sleeps under RLock — proves multiple RLock holders coexist |

## Performance impact

- **Common case** (all backends on `tryAppend` Path B, walBuf large): 8+
  backends proceed in parallel; `appendMu` contention drops to near-zero.
- **Rare case** (Lock paths: drain, appendRaw, Path A): Lock() waits for
  in-flight RLock holders to drain; not on the hot path.
- **pgbench c=100 SU TPS ≥ 500 gate**: requires this loop plus the
  `appendMu`-from-drain decoupling (next loop) to fully realise the
  stripe parallelism.  This loop removes the dominant serialisation point.

## Out of scope (later loops)

- Making drain (`drainBufferBytes`) run concurrently with stripe writers
  (currently `appendPGCompat` Path B holds Lock(), blocking concurrent
  tryAppend goroutines during drain).
- Making `appendMu` a true non-blocking path for the group-commit fast
  path (`handleGroupFlush` → `flushUpTo`).
- `appendRaw` migration to `core.AppendXLogPayload` / `stripeAppend`
  (so it uses the tracker natively rather than needing `resetPosition`).
