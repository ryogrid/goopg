# 0021-0005 — Tuple-Level Pessimistic Locking: Storage Primitives + MVCC Hook

**Status:** accepted (step 1 — storage primitives + MVCC visibility
hook only; executor wiring deferred)
**Milestone:** [0021 — Pessimistic Row Locking](../milestones/0021-pessimistic-lock-select-for-update.md)
(deferred follow-up: tuple-level locking on top of M0012).
**Spans seam:** Heap tuple infomask flag bits, page-level lock
stamper, MVCC visibility helper.
**Cross-links:**
[0021-0003](0021-0003-wait-policy-nowait-skip-locked.md) (Stage A
relation-level executor),
[0021-0004](0021-0004-deadlock-observability-and-test-matrix.md)
(NOWAIT runtime; SKIP LOCKED gate gated on this slice's
follow-ups).

## Context

M0021-0001 → M0021-0004 delivered SELECT FOR UPDATE / FOR SHARE
end-to-end at the **relation-coarse** locking layer: `lockRowsOp`
acquires `RowShareLock` on the target relation, plus NOWAIT runtime
support. Per-row blocking — preventing concurrent UPDATEs to a row
a SELECT FOR UPDATE just observed, and unlocking SKIP LOCKED's
"silently drop contended rows" semantics — needs the upstream
mechanism: tuple-level `xmax` stamping with the
`HEAP_XMAX_LOCK_ONLY` infomask bit, paired with MVCC visibility
that recognises a lock-only xmax doesn't make the tuple invisible.

This slice is the **foundation** — additive only. No callers
yet; the executor doesn't stamp lock-only xmax on rows. Future
loops wire this through:

1. lockRowsOp: stamp lock-only xmax on each yielded row.
2. Insert/Update/Delete operators: detect a row's lock-only xmax
   from another live xact and block (or fail-NOWAIT / skip-LOCKED).
3. Row-lock WAL records (`xl_heap_lock`) for crash recovery.
4. MultiXact-aware multi-holder support for FOR SHARE.

## Filename note

Reserved as `0021-0005-tuple-level-locking-storage-and-mvcc.md`
because the original M0021 milestone numbers (0001-0004) are
already used. The deferred fix_plan task "Tuple-level
pessimistic locking on top of M0012 lock manager" gets a fresh
sub-numbering.

## Storage primitives

### Infomask flag bits

Mirror upstream's `postgres/src/include/access/htup_details.h`
constants byte-for-byte so future on-disk format / pg_waldump
compat work doesn't have to translate:

```go
const (
    HeapXmaxInvalid    uint16 = 0x0800
    HeapXmaxCommitted  uint16 = 0x0400
    HeapXmaxLockOnly   uint16 = 0x0080
    HeapXmaxKeyShrLock uint16 = 0x0010
    HeapXmaxExclLock   uint16 = 0x0040
    HeapXmaxShrLock           = HeapXmaxKeyShrLock | HeapXmaxExclLock
    HeapXmaxLockMask          = HeapXmaxKeyShrLock | HeapXmaxExclLock
)

func IsHeapTupleLockOnly(infomask uint16) bool {
    return infomask&HeapXmaxLockOnly != 0
}
```

### PageSetHeapTupleLockOnly

Companion to the existing `PageSetHeapTupleXmax` (DELETE /
UPDATE-old-image path). Stamps xmax + sets the
`HEAP_XMAX_LOCK_ONLY` bit + sets the chosen lock-strength bit;
clears stale lock-strength bits and `HEAP_XMAX_INVALID` so the
xmax is honoured as a real (lock-only) holder.

```go
func PageSetHeapTupleLockOnly(p Page, slot uint16, xmax TransactionID, lockStrength uint16) error {
    // ... validate slot, item flags ...
    binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(xmax))
    infomask := binary.LittleEndian.Uint16(p[off+20 : off+22])
    infomask &^= HeapXmaxLockMask
    infomask &^= HeapXmaxInvalid
    infomask |= HeapXmaxLockOnly
    infomask |= lockStrength & HeapXmaxLockMask
    binary.LittleEndian.PutUint16(p[off+20:off+22], infomask)
    return nil
}
```

`lockStrength` parameter is one of:

- `HeapXmaxExclLock` for FOR UPDATE.
- `HeapXmaxShrLock` for FOR SHARE.
- `HeapXmaxKeyShrLock` for FOR KEY SHARE (out of v0 scope but
  accepted by the encoding layer).

Zero `lockStrength` is rejected — the resulting infomask would
have `HEAP_XMAX_LOCK_ONLY` set but no strength, which is the
"lock-only with unknown mode" corruption upstream's cleanup
paths can't interpret.

Re-stamping with a different strength clears prior strength bits
(KeyShr → Excl, Excl → Shr) instead of OR-ing them. Without this
the infomask could end up with both ExclLock and KeyShrLock bits
set and a future predicate `(infomask & HeapXmaxLockMask) ==
HeapXmaxExclLock` would false-negative.

The byte-offset layout is (Infomask2, Infomask, Hoff) at
positions 18, 20, 22 — note the swap relative to the
HeapTupleHeader struct field order. PageSetHeapTupleLockOnly
writes to offsets 20..21 to match `MarshalBinary` /
`ParseHeapTuple`'s on-disk encoding.

## MVCC visibility hook

`mvcc.TupleVisible` learns the lock-only branch:

```go
xmaxIsLockOnly := h.Xmax != storage.InvalidTransactionID && storage.IsHeapTupleLockOnly(h.Infomask)

// Self-locked tuple (Xmin == Xmax == currentXID + LOCK_ONLY) →
// still visible to ourselves. The pre-LOCK_ONLY rule treated
// this as "deleted by current xact"; we now short-circuit.
if h.Xmin == currentXID {
    if xmaxIsLockOnly { return true }
    return h.Xmax != currentXID
}
// ...
// Lock-only xmax — visible regardless of holder progress
// (committed / aborted / in-progress).
if xmaxIsLockOnly { return true }
```

Mirrors upstream's `HeapTupleSatisfiesMVCC` handling of the
LOCK_ONLY bit. The crucial property: a SELECT FOR UPDATE that
stamps a row's xmax must NOT make the row invisible to
concurrent readers (or to itself on a re-scan within the same
statement).

## Tests

`internal/storage/heap_test.go`:

- `TestPageSetHeapTupleLockOnly` — stamps a lock-only xmax,
  verifies xmax + LockOnly bit + ExclLock bit + cleared
  Invalid bit, and `IsHeapTupleLockOnly` predicate fires.
- `TestPageSetHeapTupleLockOnlyClearsStaleStrength` — re-stamp
  with KeyShrLock after ExclLock; ExclLock bit cleared.
- `TestPageSetHeapTupleLockOnlyRejectsZeroStrength` — API
  misuse guard.
- `TestPageSetHeapTupleLockOnlyInvalidSlot` — fall-through
  parity with `PageSetHeapTupleXmax`.

`internal/mvcc/visibility_test.go`:

- `TestTupleVisibleLockOnlyXmax` — 4 scenarios pinning the
  lock-only short-circuit: committed-deleter xmax with
  LOCK_ONLY → visible; in-progress xmax → visible; future
  xmax → visible; self-locked tuple (Xmin = Xmax = current +
  LOCK_ONLY) → visible.
- `TestTupleVisibleNonLockXmaxRegression` — sanity guard:
  plain committed delete (no LOCK_ONLY infomask) is still
  invisible, the new branch didn't accidentally let real
  deletes through.

Full `go test ./...` green.

## Out of scope

- Executor wiring: lockRowsOp stamping per-row lock-only xmax
  before yielding. (Next slice of this follow-up.)
- INSERT / UPDATE / DELETE path: detect lock-only xmax from a
  live other xact and block / fail-NOWAIT / skip-LOCKED.
- WAL records: `xl_heap_lock` for crash-recovery replay.
- MultiXact infrastructure: multiple FOR SHARE holders per row.
- pg_locks-style introspection of tuple-level lock holders.
- Promotion of relation-level RowShareLock to a finer mode in
  the lockmgr (relation-level lock stays unchanged — the
  tuple-level layer is orthogonal, attaches via xmax stamping).
