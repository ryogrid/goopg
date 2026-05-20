# 0107-0007l — `walBuffer.writeReserved` (M0107-0007, slice B foundation 5)

Status: accepted (2026-05-21)
Parent: [[07-wal-fsm-insert]] §2 "8-stripe WAL insert locks"; M0107-0007 slice B.
Companions: [[0107-0007h]] (`lsnAllocator`), [[0107-0007i]] (`paddedMutex` /
`appendLockSet`), [[0107-0007j]] (`buildSegmentPadRecord`),
[[0107-0007k]] (`insertPosTracker`).
PG counterpart: `CopyXLogRecordToWAL` in
`postgres/src/backend/access/transam/xlog.c` (line drifts between
minor versions; the symbol is the anchor).

## Problem

Slice B replaces the single `state.appendMu sync.Mutex` (which
serialises every WAL record append) with an 8-stripe `appendLockSet`
(landed in [[0107-0007i]]) plus an atomic LSN reservation primitive
(landed in [[0107-0007k]] `insertPosTracker`). With LSNs reserved
under one mutex (`posMu`, briefly) and bytes written under the
stripe lock, two stripes can append concurrently into disjoint LSN
ranges. PG does the same: `WALInsertLocks[stripe]` serialises only
the bytes write into the stripe's reserved range, while
`ReserveXLogInsertLocation` advances `XLogCtl->Insert.CurrBytePos`
atomically.

The missing primitive on the goopg side is a way to write the
record's bytes *into the shared* `walBuffer` *at a pre-reserved LSN*
without touching `tail` (which would race with peer stripes' own
writes) and without holding a lock that excludes peers from writing
into their own reserved ranges.

## Design

Add a new method to the package-private `walBuffer`:

```go
func (b *walBuffer) writeReserved(lsn int64, record []byte) error
```

Semantics:

- Copy `record` into the ring at the offset corresponding to
  absolute byte LSN `lsn`.
- Do *not* mutate `head`, `tail`, or `base`. Publication of the
  written bytes (advancing `tail` so `readForDrain` / `readAt` can
  see them) is a separate primitive — to be added in a later slice
  B foundation alongside the call-site rewrite.
- Return `errWALBufferNil` if the receiver is nil (matches the
  existing `s.walBuf != nil` guard pattern in `state.append`, makes
  the 8-stripe call-site rewrite safe against
  `Config.WALBuffers == 0` deployments).
- Return `errWALBufferReservedOutOfRange` if
  `[lsn, lsn+len(record))` falls outside `[base, base+cap)`. The
  upstream `insertPosTracker.reserve` is supposed to keep
  reservations inside the window; the error catches a contract
  violation rather than silently corrupting the ring.
- Empty record is a no-op (matches `state.appendRaw`'s `len == 0`
  short-circuit). The short-circuit runs *before* the range check
  so a zero-length out-of-range reservation is still a no-op.

### Concurrent safety

Two writers writing into disjoint LSN ranges of the same buffer are
safe under Go's memory model — `copy` on disjoint byte slice regions
does not introduce a data race. The `insertPosTracker` guarantees
disjoint reservations (its `posMu` makes `curr` advance atomically
with `prev`), so the contract is enforceable end-to-end.

Two writers writing into *overlapping* ranges is a caller contract
violation and produces undefined byte contents in the overlap. The
range check does not detect this — it would require a per-byte
ownership map, which is overkill for a contract that the
`insertPosTracker` already enforces upstream.

### Why `head`/`tail`/`base` stay untouched

The publication step (advancing `tail` so resident bytes become
visible to drain / readers) is non-trivial under stripe-concurrent
writes: `tail` cannot advance past LSN X until *every* reservation
strictly below X has had its bytes written by its owning stripe.
That requires either:

1. A "publishedLSN" atomic that walkers advance only when they
   observe `prev` chains complete up to X, or
2. PG-style: `WaitXLogInsertionsToFinish(LSN)` walks all stripe
   locks waiting for the ones whose reserved ranges fall below X.

Either approach belongs in its own foundation — this primitive
deliberately stays narrow so the byte-write path can land and be
covered by stripe-style unit tests before publication coupling is
introduced.

### Wrap branch

The implementation mirrors `walBuffer.append`'s wrap-aware copy
(split into `buf[off..cap]` + `buf[0..rem]` when the LSN range
crosses the underlying ring boundary). Under the current contract
(`lsn + len ≤ base + cap`, with `cap == len(buf)`), the wrap branch
is structurally unreachable from valid callers — the LSN window
equals the ring capacity, so the underlying offset wraps at most
once inside the window, at the exact LSN where any further-extending
reservation would exceed `base+cap`. The wrap-copy code is retained
verbatim for symmetry with `append` and `readAt` and to remain
robust against future contract changes (e.g., a >cap publication
window during reorganisation).

## Lock-ordering tier

Future slice B call-site rewrite:

```
appendLockSet.lockByProcNum (one of 8 stripes)
  → insertPosTracker.reserve (briefly under posMu — joint (curr,prev) atomicity)
    → (rare, only on segment crossings) buildSegmentPadRecord
    → walBuffer.writeReserved (no lock — disjoint reservations from peers)
```

`writeReserved` sits at the leaf of this chain; it acquires no
locks and is the bytes-write counterpart to the LSN reserve.

## Rejected alternatives

1. **Make `writeReserved` advance `tail` to `lsn + len`** — would
   race with concurrent stripes whose reservations land *below*
   ours (their writes might not have happened yet). The reader
   would then see uninitialised bytes between the previous
   reservation's start and ours.

2. **Take a stripe lock inside `writeReserved`** — would require
   passing `procNum` (or the stripe index) through, defeating the
   point of pre-reserving the LSN under a coarser lock. The stripe
   lock is held by the caller for the duration of "reserve →
   write" anyway; `writeReserved` itself does not need it.

3. **Accept a `tail` parameter and CAS-advance** — couples the
   publication policy to this primitive; deferred to its own
   foundation so the policy (highest-published LSN tracking,
   `WaitXLogInsertionsToFinish` equivalent) can be designed in
   isolation.

## Verification

- `go test -race -count=1 -run 'TestWALBufferWriteReserved'
  ./internal/wal/` PASS (1.02 s).
- `go test -race -count=1 ./internal/wal/` PASS (3.12 s).

Nine regression tests in
`internal/wal/wal_buffer_write_reserved_test.go`:

- `TestWALBufferWriteReservedAtBaseNoWrap` — write at `lsn==base`,
  bytes at `buf[0..n]`, head/tail/base unchanged.
- `TestWALBufferWriteReservedAtNonZeroOffset` — LSN→ring-offset
  arithmetic at a non-zero offset; neighbouring bytes untouched.
- `TestWALBufferWriteReservedRejectsBelowBase` — `lsn < base` and
  `lsn = 0` (when `base > 0`) both rejected.
- `TestWALBufferWriteReservedRejectsPastEnd` — `lsn+n > base+cap`
  rejected, `lsn = base+cap` rejected (zero-length window above
  the right edge would still need at least one byte), `lsn+n =
  base+cap` exactly accepted.
- `TestWALBufferWriteReservedEmptyIsNoop` — `nil` and `[]byte{}`
  short-circuit before range check; head/tail/base unmodified.
- `TestWALBufferWriteReservedNilReceiver` — nil receiver returns
  `errWALBufferNil`.
- `TestWALBufferWriteReservedConcurrentDisjoint` — 8 goroutines × 50
  records × 16 bytes each in disjoint LSN ranges; race-clean under
  `-race`; after manually publishing tail, `readAt` confirms every
  stripe's marker bytes land in the right slot.
- `TestWALBufferWriteReservedReadbackViaReadAt` — bytes written at
  LSN X read back identically via `readAt(X)`.
- `TestWALBufferWriteReservedDoesNotMutateTailHeadBase` — a series
  of `writeReserved` calls leaves `base`, `head`, `tail` exactly
  as they were before.

## Out of scope (future slice B foundations)

- Tail publication primitive (advance `tail` only when reservations
  below the new tail have all been written by their owning stripes).
- Mounting `appendLockSet` + `insertPosTracker` + `writeReserved`
  on `Writer` and rewriting `state.append` / `state.appendRaw` to
  use them (the full call-site rewrite that splits
  `state.appendMu`'s four invariants — writePos / walBuf / memRing
  / writeLSN — into per-stripe local state vs. shared state).
- `prevRecPtr` chain integrity under per-stripe writers (currently
  consumed by `[[0107-0007k]]`'s `insertPosTracker.reserve` return
  value; the call-site rewrite stamps it).
- `memRing` mirror handling under stripe-concurrent writes (the
  current `memRing.Append(writePos, stream)` is sequential under
  `appendMu`).
- Drain coordination with concurrent stripe writes (`drainBufferBytes`
  currently runs under `appendMu`).
- Deciding whether `lsnAllocator` ([[0107-0007h]]) becomes
  dead-code-removed once the call-site converges on
  `insertPosTracker`.
