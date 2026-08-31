# 0021-0006 — Tuple-Level Locking: xl_heap_lock WAL Record + Replay

**Status:** accepted (step 3 — WAL record types and replay; pure
additive, no producer yet)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
**Spans seam:** WAL record-kind catalog, encode/decode, replay
dispatch.
**Cross-links:**
[0021-0005](0021-0005-tuple-level-locking-storage-and-mvcc.md)
(storage primitives + MVCC hook — step 1),
[0002-0003](0002-0003-redo-records.md) (existing redo-record
catalog).

## Context

M0021-0005 (tuple-level locking step 1) added the storage
primitives — infomask flag bits + `PageSetHeapTupleLockOnly` +
`mvcc.TupleVisible` recognition of lock-only xmax. This slice adds
the **WAL record** that crash recovery uses to re-apply row-lock
stamps after a restart.

The slice is pure additive: defines `RecordKindHeapLock` +
`EncodeHeapLock` / `DecodeHeapLock` + `replayHeapLock` and wires
the case into `ApplyRecord`. No producer emits HeapLock records
yet; the executor wiring (lockRowsOp stamping per-row +
`ctx.Pool.LogHeapLock` plumbing through the pool's
change-record hook) is a separate slice. Mirrors the
M0014-0001 / M0014-0002 / M0017-0001 step-1 pure-additive
pattern.

## Filename note

The reservation `0021-0006-tuple-locking-heap-lock-wal.md`
follows on from 0021-0005 (storage primitives). Subsequent
slices in this follow-up cluster keep the 0021-00NN
numbering even though they're past the original M0021 milestone
0001-0004 doc-set — the numbering is for documentation
sequencing, not for Ralph's run-tracking.

## Record format

Mirrors upstream's `xl_heap_lock` at the level of detail goopg's
replay path needs. v0 format (22 bytes, fixed):

```
offset  field          width
0       kind = 10      uint8       (RecordKindHeapLock)
1       DBOid          uint32      (rel)
5       RelOid         uint32
9       Fork           uint8
10      Block          uint32
14      LineSlot       uint16
16      Xmax           uint32      (locking xact's xid)
20      LockStrength   uint16      (HeapXmax* mode bits)
```

`LockStrength` is one of `HeapXmaxExclLock` (FOR UPDATE),
`HeapXmaxShrLock` (FOR SHARE), or `HeapXmaxKeyShrLock` (FOR KEY
SHARE — accepted by the encoder; gated at the executor / planner
layer).

XID-tracking (the "xact-was-running" set the recovery loop reads
from xact-commit/abort markers) and MultiXact handling stay
deferred — Stage A locking is single-holder per row.

## Helpers

```go
func EncodeHeapLock(rel storage.RelFileNode, blk storage.BlockNumber,
    lineSlot uint16, xmax storage.TransactionID, lockStrength uint16) []byte

func DecodeHeapLock(payload []byte) (rel storage.RelFileNode,
    blk storage.BlockNumber, lineSlot uint16,
    xmax storage.TransactionID, lockStrength uint16, err error)
```

The decoder validates `payload[0] == RecordKindHeapLock` and
length == `heapLockSize` so a stray payload from the wrong
record kind surfaces as an error rather than silently
constructing nonsense ItemPointer / xmax values.

## Replay

`replayHeapLock(mgr, r)` mirrors `replayHeapDelete`:

1. Decode the record.
2. Validate the target block exists (locks against non-existent
   tuples are always a producer bug — fail loudly).
3. Read the page; reject if the page is uninitialised.
4. **Idempotency via pd_lsn**: if `MustHeader(page).LSN() >=
   r.EndLSN`, the lock has already been applied on a prior
   recovery pass — silently no-op.
5. Call `PageSetHeapTupleLockOnly(page, lineSlot, xmax,
   lockStrength)` — the storage primitive landed in step 1.
6. Stamp `pd_lsn = r.EndLSN`.
7. Write the page back.

`ApplyRecord`'s switch grew a `case RecordKindHeapLock` arm
routing to `replayHeapLock`.

## Logical decoder

`Classify` (the M0008 logical-decoder dispatcher) intentionally
**skips** `RecordKindHeapLock` — locking a row doesn't change
replicated data, so apply workers shouldn't see a lock event.
The classifier's trailing comment ("Other kinds ... aren't
user-data transactional events — skip silently") covers the new
kind by default; no classifier change is needed.

## Tests

`internal/wal/recovery_test.go`:

- `TestEncodeDecodeHeapLockRoundTrip` — pins the on-the-wire
  shape (kind byte + length + per-field round-trip).
- `TestDecodeHeapLockRejectsWrongKind` — feeding a HeapDelete
  payload to DecodeHeapLock errors instead of silently
  succeeding (defensive guard mirroring the other heap-record
  decoders).
- `TestReplayHeapLockIdempotent` — seed a tuple, replay one
  HeapLock record, verify (a) xmax stamped, (b)
  `HeapXmaxLockOnly` + `HeapXmaxExclLock` bits set in infomask,
  (c) second replay is a no-op via pd_lsn.
- `TestReplayHeapLockMissingBlock` — locking a non-existent
  block surfaces an error.
- `TestApplyRecordRoutesHeapLock` — the per-record kernel
  dispatches `RecordKindHeapLock` to `replayHeapLock` (missing
  the case would silently drop the lock at recovery time).

Full `go test ./...` green.

## Out of scope

- Producer wiring: lockRowsOp emits HeapLock through
  `ctx.Pool.LogHeapLock` (parallel to LogHeapInsert /
  LogHeapDelete) — next slice of this follow-up.
- MultiXact-aware multi-holder support for FOR SHARE.
- pg_waldump compat / upstream xl_heap_lock byte layout — gated
  on the M0014-0002 record-frame switchover.
- Visibility / tuple lookup integration during apply (the
  visibility hook from step 1 is what readers consult; the
  replay path here just stamps the bytes).
