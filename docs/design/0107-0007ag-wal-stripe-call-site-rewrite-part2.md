# 0107-0007ag — WAL stripe B call-site rewrite (part 2 of N)

**Milestone**: M0107-0007 Phase D4 — WAL insert striping + FSM page distribution  
**Status**: Landed 2026-05-21

---

## Summary

Replaces the inline `encodeRecordXLog + emitWithPageHeaders + walBuf.append +
memRing.Append` sequence in `state.append` (Path B) and `state.tryAppend` (Path
B) with a single call to `stripeWriterCore.AppendXLogPayload` ([[0107-0007ae]])
followed by a synchronous `PublishUpTo` that advances `walBuf.tail` and
`memRing.tail` under `appendMu`.

This is the first call-site rewrite loop for slice B.  It wires all twenty-two
earlier slice B foundations ([[0107-0007i]] through [[0107-0007af]]) into the
production WAL write path while keeping `appendMu` serialisation for the
transitional state.  Concurrency improvements — removing `appendMu` from
`tryAppend` and making drain asynchronous — are deferred to subsequent loops.

---

## Changes

### `internal/wal/insert_pos.go`

Added `(*insertPosTracker).resetPosition(curr, prev uint64)`.  Called by Path A
of `state.appendPGCompat` after a direct-to-disk write bypasses stripe B, to
resync the core's `(curr, prev)` state so subsequent Path B appends chain
correctly.

### `internal/wal/stripe_writer_core.go`

Added `(*stripeWriterCore).resetPosition(curr, prev uint64)`.  Thin wrapper
around `posTracker.resetPosition`; nil-safe.

### `internal/wal/writer.go`

1. **Import**: added `"github.com/goopg/goopg/internal/activity"` for
   `LookupCurrentGoroutine`.

2. **`state` struct**: added `core *stripeWriterCore` field.  Set to `w.core`
   (the same pointer) in `NewWriter` before launching `state.loop`, so both the
   caller-goroutine fast path (`tryAppend`) and the state-loop slow path
   (`appendPGCompat`) share one core.

3. **`state.stripeNum()`** (new helper): calls `activity.LookupCurrentGoroutine()`
   and returns the caller's `procNum`, falling back to 0 for non-registered
   goroutines (initdb, checkpointer, walreceiver, tests) — they all land on
   stripe 0, which is always valid.

4. **`state.append`**: split into non-pageHeaders (unchanged legacy
   `encodeRecord` path) and a delegation to `state.appendPGCompat` for the
   PG-compat path.

5. **`state.appendPGCompat`** (new):

   *Path A* (walBuf nil or record too large for ring): keeps the old
   `encodeRecordXLog + emitWithPageHeaders + writeAt` sequence for correctness,
   then calls `s.core.resetPosition(end, start-1)` so the posTracker reflects
   the new write position before any subsequent Path B call.

   *Path B* (buffered):
   ```
   pre-drain if conservative size (paddedLen + 64 B) doesn't fit
     ↳ core.PublishUpTo(curr)   — make pending bytes drainable
     ↳ drainBufferBytes(need)   — write to disk, advance walBuf.head
   core.AppendXLogPayload(procNum, payload, segSize, sysID, tli)
     ↳ stripe lock → reserveEmittedAndPublish → encodeRecordXLog
       → emitWithPageHeaders → walBuf.writeReserved → MemRing.WriteReserved
       → setInsertingAt(idle) → unlock
   core.PublishUpTo(start0 + total)   — advance walBuf.tail + memRing.tail
   update: writePos, writeLSN, writeLSNMirror, prevRecPtr
   ```

6. **`state.tryAppend`**: same split.  PG-compat path does a conservative size
   check (`paddedLen + 64`) before acquiring `appendMu`; returns `false` (slow
   path) if walBuf is nil, too small, or would overflow — identical fall-through
   semantics as before.

---

## Why a synchronous `PublishUpTo` (transitional state)

Under the planned fully-concurrent slice B model, `walBuf.tail` is advanced by
the drain goroutine via `tailPublisher.publishUpTo` independently of the stripe
writers.  In the transitional state (stripe writes still serialised by
`appendMu`), the drain goroutine does not yet exist.  Calling
`core.PublishUpTo(start0 + total)` immediately after `AppendXLogPayload`
advances `walBuf.tail` synchronously so that `walBuf.resident()` / `free()` /
`readForDrain` remain correct for the existing overflow-drain and flush paths.

The cost is one extra `tailPublisher.publishUpTo` CAS + one `walBuffer.publishTail`
atomic store per append on the hot path — negligible next to the existing
`appendMu.Lock/Unlock` pair.

---

## Parity gate confirmation

All four `TestAppendXLogPayloadParity*` tests in
`internal/wal/append_xlog_payload_parity_test.go` pass without `t.Skip`:

| Test | Before this loop | After |
|------|-----------------|-------|
| `TestAppendXLogPayloadParityFirstRecordAlwaysAgrees` | PASS | PASS |
| `TestAppendXLogPayloadParityWithLegacyEncodeEmit` | SKIP | PASS |
| `TestAppendXLogPayloadParityShortRecordsSingleStripe` | SKIP | PASS |
| `TestAppendXLogPayloadParityEmptyBodyRecords` | SKIP | PASS |

The multi-record chain tests (`WithLegacyEncodeEmit`, `ShortRecordsSingleStripe`,
`EmptyBodyRecords`) were previously deferred because the pre-fix `reserveEmittedAndPublish`
stored `t.prev = start` (reservation start) rather than `t.prev = start +
uint64(leading)` (record-content start).  The prev-RecPtr fix from [[0107-0007af]]
makes them pass; this loop proves the fix is wired into the production path.

---

## Lock ordering

```
appendMu                              ← unchanged (serialises writer goroutines)
  → stripe lock (appendLockSet[i])    ← new, inside AppendXLogPayload
    → insertPosTracker.posMu          ← new, inside reserveEmittedAndPublish
      → (rare) emitSegmentPad         ← cross-segment pad write
    → walBuffer.writeReserved         ← leaf, no lock
    → MemRing.mu (read-lock)          ← WriteReserved
  → walBuffer.publishTail             ← PublishUpTo, no lock (atomic store)
  → MemRing.mu (write-lock)           ← MemRing.PublishUpTo
```

The `appendMu → stripe lock → posMu` chain is new.  There is no reverse path
(stripe lock → appendMu is never taken), so no deadlock is introduced.

---

## Out of scope (deferred to later loops)

- Removing `appendMu` from `tryAppend` so concurrent callers use different
  stripes truly in parallel (the main TPS-improvement step).
- Making drain asynchronous (`drainBufferBytes` currently runs under `appendMu`;
  the rewrite lets drain run concurrently by consuming `PublishUpTo`'s return
  as the drain ceiling without holding `appendMu`).
- `appendRaw` (walreceiver replay path): receives pre-encoded bytes from the
  primary; continues to use the size-explicit `appendMu` path unchanged.
- Removing `lsnAllocator` dead code (already done in [[0107-0007x]]).
- pgbench c=100 SU TPS ≥ 500 gate: runs after `appendMu` is removed from
  `tryAppend` (a subsequent loop).
