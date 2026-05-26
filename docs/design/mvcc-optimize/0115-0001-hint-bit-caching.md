# 0115-0001 — Hint Bit Caching in Heap Tuple Visibility

**Status:** draft
**Date:** 2026-05-26
**Milestone:** M0115
**Supersedes:** —

---

## 1. Problem Statement

Every call to `TupleVisible` in `internal/mvcc/visibility.go` invokes
`snap.SeesCommittedXID(h.Xmin)` unconditionally, even for tuples whose commit
status has already been determined in a previous scan pass.

`SeesCommittedXID` evaluates four conditions:

```go
func (s Snapshot) SeesCommittedXID(xid TransactionID) bool {
    if xid == InvalidTransactionID { return false }
    if s.HasAborted(xid)           { return false }  // O(|abortedXIDs|), binary search above threshold
    if xid < s.Xmin                { return true  }
    if xid >= s.Xmax               { return false }
    return !s.HasInProgress(xid)                     // O(|inProgress|), binary search above threshold
}
```

For a full-table sequential scan with N committed tuples, this runs N times
per query. `HasAborted` and `HasInProgress` each walk a list whose length is
proportional to the number of recently-aborted or currently-in-progress
transactions (binary search kicks in above `snapshotLinearScanThreshold`).
Under concurrent OLTP workloads the total cost approaches O(N × in-progress
list length) — significant for analytical queries.

PostgreSQL avoids this by caching the result in the tuple header's
`t_infomask` field after the first status determination:

| Infomask bit | Meaning |
|---|---|
| `HEAP_XMIN_COMMITTED` (0x0100) | xmin is a committed transaction |
| `HEAP_XMIN_INVALID`   (0x0200) | xmin is invalid / rolled-back |
| `HEAP_XMAX_COMMITTED` (0x0400) | xmax is a committed transaction |
| `HEAP_XMAX_INVALID`   (0x0800) | xmax is invalid (row is live) |

Once a bit is set, all future scans short-circuit with a single bit test.

goopg defines `HeapXmaxCommitted` and `HeapXmaxInvalid` in
`internal/storage/heap.go` (lines 64–65) but has no corresponding
`HeapXminCommitted` / `HeapXminInvalid` constants. Adding those constants
(M0115-0002) and wiring them into the visibility check are both part of this
milestone.

---

## 2. FrozenTransactionID Fast Path

`FrozenTransactionID = 2` is a special sentinel meaning "permanently
committed". It is used by VACUUM Freeze (`internal/storage/freeze.go`) to
rewrite old xmin values so they never need a status lookup.

Currently `TupleVisible` reaches `SeesCommittedXID(2)`, which returns true via
the `xid < s.Xmin` branch — so frozen-tuple visibility is already **correct**.
Adding an explicit early exit is a CPU micro-optimization: it skips all four
conditions in `SeesCommittedXID` and the subsequent xmax check setup cost:

```go
if h.Xmin == FrozenTransactionID {
    // Frozen tuples are universally visible — proceed directly to xmax check.
    // (No snapshot arithmetic needed; this is a performance shortcut only.)
}
```

This is M0115-0001.

---

## 3. Hint-Bit Read Path

After the frozen fast path, `TupleVisible` should check hint bits before
calling `SeesCommittedXID`:

```go
// --- xmin ---
xminCommitted := h.Infomask & HeapXminCommitted != 0
xminInvalid   := h.Infomask & HeapXminInvalid   != 0

if xminInvalid {
    return false // xmin is rolled back or crashed
}
if !xminCommitted {
    if !snap.SeesCommittedXID(h.Xmin) {
        return false
    }
    // hint-bit write deferred to §4 (requires Slot)
}

// xmin is committed — proceed to xmax check
```

Symmetrically for xmax:

```go
// --- xmax ---
if h.Xmax == InvalidTransactionID {
    return true // no deleter
}
// ... lock-only check ...
xmaxCommitted := h.Infomask & HeapXmaxCommitted != 0
xmaxInvalid   := h.Infomask & HeapXmaxInvalid   != 0

if xmaxInvalid {
    return true // xmax is invalid; row is live
}
if xmaxCommitted {
    return false // xmax committed; row is deleted
}
// fall through to SeesCommittedXID(h.Xmax)
```

This is M0115-0002.

---

## 4. Hint-Bit Write Path

### 4.1 WAL considerations

Hint bit changes are deliberately **not WAL-logged** in PostgreSQL. On
crash recovery, `pg_xact` (the commit log) is the authoritative source of
commit status. Hint bits are lazily re-set on the first scan after recovery.
This is the correct design and goopg will follow it.

Consequence: hint bit writes must not call `pool.MarkDirtyLogicalChange`
(which emits a WAL record) or `pool.MarkDirty` (which, on checkpoint, writes
the full page to WAL via FPI). Instead, a lighter "hint-bit-only dirty"
mechanism is needed.

### 4.2 `MarkDirtyHintBit`

Add a new method to `storage.Pool`:

```go
// MarkDirtyHintBit marks s dirty solely for hint-bit updates. Unlike
// MarkDirty, it does not schedule a WAL FPI: hint bits are re-derived
// from pg_xact on recovery.  The page will still be flushed to disk
// at checkpoint time, persisting the cached bits for future restarts
// (not required for correctness, but avoids repeated re-derivation).
func (p *Pool) MarkDirtyHintBit(s *Slot) {
    // Implementation: set a new hintOnly bit on the buffer frame rather than
    // the standard dirty bit, so the WAL writer skips FPI emission while the
    // checkpoint flusher still writes the page. Alternatively, reuse the
    // existing dirty bit and suppress WAL emission at the call site by not
    // calling MarkDirtyLogicalChange or MarkDirtyWithLSN.
}
```

The existing buffer-state model uses a single `slotDirtyBit`. Introducing
`MarkDirtyHintBit` requires either a second state bit (hint-only dirty) or a
convention that hint-bit writes call `MarkDirty` directly without a WAL record.
The implementation choice is deferred to M0115-0003.

### 4.3 Caller Signature Changes

`TupleVisible` is pure today (no side effects). Adding hint-bit writes
requires a writable page slot. Two options:

**Option A** — New function `TupleVisibleWithHintBits(h, snap, xid, slot)`.
The original `TupleVisible(h, snap, xid)` keeps its signature and is used
by code that doesn't hold a slot (e.g., tests, standby, utility paths).
The scan operators (`seqScanOp`, `indexScanOp`) call the new variant.

**Option B** — Make `TupleVisible` accept a `*Slot` that may be nil; nil
means "read-only, skip hint-bit write".

Option A is preferred: it keeps the public API backward-compatible and makes
the read-only guarantee explicit for callers that want it.

The signature change to `TupleVisible` is part of M0115-0004 (hint-bit write
path); the scan-operator call-site wiring is M0115-0005.

### 4.4 Apply the Bit

After determining xmin is committed:

```go
if slot != nil && !xminCommitted {
    h.Infomask |= HeapXminCommitted
    // Write h.Infomask back to the on-page tuple header at linePtr.
    // PageWriteHintBit is a new helper to be defined; it updates only
    // the t_infomask field in-page under the page's existing content lock.
    PageWriteHintBit(slot.Page(), linePtr, h.Infomask)
    pool.MarkDirtyHintBit(slot)
}
```

`PageWriteHintBit` is a new function (to be created in `internal/storage/`) that
writes only the `t_infomask` field back to the on-page tuple header at the given
line pointer. The caller must already hold the page's content lock, consistent
with existing heap mutation patterns (see `PageSetHeapTupleXmax` in `heap.go`).

---

## 5. Context Wiring (M0115-0005)

The seqScan and indexScan operators already hold the page slot while
iterating tuples. The relevant code is in `operators_storage.go` /
`operators_index.go`. Changes required:

1. Acquire a write-capable slot reference at the start of each tuple
   iteration.
2. Pass the slot to `TupleVisibleWithHintBits`.
3. Release after the hint-bit write (the existing unlock/unpin timing is
   unchanged; hint-bit write happens under the existing content lock).

The indexOnlyScan operator already skips heap fetches for VM-visible tuples;
it does not need hint bit support.

---

## 6. Recovery / Standby Behaviour

On crash recovery:
- The WAL replayer calls `TupleVisible` (or an equivalent) in some paths.
  These paths pass `nil` for the slot (no hint-bit writes during replay).
- After recovery, the first scan of each page re-derives and re-stamps hint
  bits lazily.

On a PG18 standby reading goopg-written heap pages:
- PG's `HeapTupleSatisfiesMVCC` reads the same infomask bits; if they are
  set (either from goopg's hint-bit write or from PG's own pass), it
  short-circuits identically.
- If they are not yet set, PG derives the bits from `pg_xact` and sets them
  itself — no compatibility concern.

---

## 7. Testing Plan (M0115-0006)

| Test | What it verifies |
|---|---|
| `TestFrozenXIDFastPath` | `TupleVisible` with `xmin=FrozenTransactionID` returns true without calling any snapshot method |
| `TestHintBitReadShortCircuit` | Pre-set `HEAP_XMIN_COMMITTED`; confirm `SeesCommittedXID` is not called |
| `TestHintBitWrite` | After a committed-tuple scan, the on-page infomask has `HEAP_XMIN_COMMITTED` set |
| `TestMarkDirtyHintBitNoWAL` | `MarkDirtyHintBit` does not append a record to the WAL stream |
| `TestHintBitRecovery` | After a simulated restart (page flushed without hint bits), re-scanning re-sets the bits |

---

## 8. Performance Expectations (M0115-0007)

The hint-bit path eliminates `HasAborted` and `HasInProgress` walks for all
tuples that have been scanned at least once. For workloads that re-read the
same committed rows repeatedly (TPC-H analytical scans, pgbench read-only):

- Expected improvement: 5–15% reduction in `TupleVisible` CPU time on a
  warm table where all tuples have already been scanned once.
- First-scan cost is unchanged (hint bits are not set until first scan).
- Write workloads (INSERT-heavy pgbench) see negligible change because newly
  inserted tuples have no hint bits and the visible-tuple fraction is small.

A benchmark regression gate (`pgbench -T 60 -c 10 -M simple -S`) is
required in the DoD to ensure no performance regression.

---

## 9. References

- `postgres/src/backend/access/heap/heapam_visibility.c` —
  `SetHintBits`, `HeapTupleSatisfiesMVCC`
- `postgres/src/include/access/htup_details.h` — `HEAP_XMIN_COMMITTED` etc.
- `internal/mvcc/visibility.go` — current `TupleVisible` implementation
- `internal/storage/heap.go:60–65` — infomask constants
- `practice/pg_mvcc_internals.md` §"Hint Bits"
