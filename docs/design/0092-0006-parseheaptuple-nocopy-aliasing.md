# Design 0092-0006 — ParseHeapTuple aliasing for the hot read path

**Status:** authoritative for M0092-0006 implementation.
**Milestone:** [M0092](../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Problem

`internal/storage/heap.go::ParseHeapTuple` reads a tuple from
a page slot and returns a `HeapTuple` whose `Data []byte`
field is a FRESH copy of the page bytes:

```go
return HeapTuple{
    Header: ...,
    Data: append([]byte(nil), payload...),  // ← copy
}, nil
```

`indexScanOp.Next` (M0092-0001) calls `PageGetHeapTuple` →
`ParseHeapTuple`, then unpins, then calls `DecodeRowInto`
which reads `tuple.Data`. The copy is necessary because the
decode happens AFTER Unpin (the alias would be invalid).

This per-Next allocation is ~tuple-size bytes (e.g., ~100 B
for pgbench_accounts). At 437 TPS that's ~44 KB/s.

## Approach

Mirror M0091-0002's `PageGetItemRawNoCopy` pattern: add a
no-copy variant that returns a HeapTuple whose Data field
aliases the page bytes. Caller must hold the page pin AND
the RLock for the lifetime of the returned tuple.

```go
// PageGetHeapTupleNoCopy returns the tuple at the 1-based
// slot whose Data field ALIASES the page bytes (no copy).
// Caller MUST hold the page pin and a content RLock for the
// lifetime of the returned tuple. Used by the hot read
// path (M0092-0006); other callers should keep using
// PageGetHeapTuple.
func PageGetHeapTupleNoCopy(p Page, slot uint16) (HeapTuple, error)
```

Internally, refactor ParseHeapTuple to support both modes
via a private helper, or duplicate the parse logic.

### Re-order indexScanOp.Next

The current M0092-0001 Next() releases the pin BEFORE
DecodeRowInto. To use NoCopy, we keep the pin + RLock held
across decode:

```go
// before:
slot, err := Pool.Pin(...)
slot.RLock()
tuple, actualSlot, found := followHOTChain(...)
slot.RUnlock()
Pool.Unpin(slot)
DecodeRowInto(scanRow, cols, tuple.Data)  // tuple.Data is COPY

// after (M0092-0006):
slot, err := Pool.Pin(...)
slot.RLock()
tuple, actualSlot, found := followHOTChain(...)  // tuple.Data ALIASES page
DecodeRowInto(scanRow, cols, tuple.Data)  // safe — RLock held
slot.RUnlock()
Pool.Unpin(slot)
```

**`followHOTChain` itself currently uses `PageGetHeapTuple`
(copy).** Update to use `PageGetHeapTupleNoCopy` since the
RLock is held throughout HOT-chain traversal.

After this change, Next() does zero allocation for the tuple
bytes. DecodeRowInto still allocates for variable-length
columns (per-column `make([]byte)`), but that's separate
(M0093 candidate).

## Safety

RLock-held-across-decode means:
- Heap writers on this page (concurrent UPDATE / INSERT)
  must wait for our RLock to release.
- Decode time for an int row is ~hundreds of ns; for a wide
  row with strings ~µs.
- Bounded write-starvation. For SELECT-only workloads
  (pgbench -S) there are no writers; for mixed workloads
  the cost is small.

This is the same trade-off M0091-0002 made for btree
RangeScan and audited as acceptable.

## Test coverage

- Existing executor / storage / btree tests continue to
  pass.
- The existing `BenchmarkIndexScanPointLookup` (or
  equivalent) should show alloc reduction.
- New test: under concurrent UPDATE on the same row,
  verify the SELECT path doesn't deadlock and that data
  consistency holds (the M0090-0002 concurrent-xmax
  detection still applies; the UPDATE's xmax check happens
  AFTER our RLock releases).

## Expected impact

- ~100 B / query allocation eliminated for pgbench.
- Combined with M0092-0007 (SlotFromRow pool) and
  M0092-0004 (protocol layer), the per-query allocation
  rate drops materially.
- Should reduce GC CPU share noticeably (currently 80 %).
