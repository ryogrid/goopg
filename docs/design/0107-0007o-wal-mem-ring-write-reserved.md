# 0107-0007o — `MemRing.WriteReserved` + `MemRing.PublishUpTo` (M0107-0007, slice B foundation 8)

Eighth slice B foundation for parent chapter
[[perf-optimize/07-wal-fsm-insert]] §2 (Phase D4 — 8-stripe WAL insert locks).
Foundations 1–7 — [[0107-0007h]] `lsnAllocator`, [[0107-0007i]]
`appendLockSet`, [[0107-0007j]] `buildSegmentPadRecord`,
[[0107-0007k]] `insertPosTracker`, [[0107-0007l]]
`walBuffer.writeReserved`, [[0107-0007m]] `insertionTracker`, and
[[0107-0007n]] `tailPublisher` — landed first per the foundation-first
pattern. The call-site rewrite that mounts these primitives on
`Writer` and drives them from the drain/flush path is multi-loop scope
and is NOT in this loop.

## Problem

`MemRing` ([[0010-0002-walsender-in-memory-wal-handoff]],
`internal/wal/mem_ring.go`) is the bounded in-memory mirror of recently-
written WAL bytes that lets walsender goroutines stream from RAM
instead of re-reading the segment files. Its `Append(pos, data)` API
has a sequential-writer contract:

```go
if pos != r.tail {
    // Reset to the new position; the existing residence is
    // no longer trustworthy.
    r.head = pos
    r.tail = pos
}
```

A non-`tail` `pos` *resets the whole ring*. Under the slice B 8-stripe
WAL insert model, peer stripes reserve and write LSN ranges
concurrently — calls into the ring's writer side will arrive
out-of-order (a stripe reserving `[100, 124)` may land its bytes before
a peer reserving `[124, 148)` even though both finish before a third
stripe at `[148, 172)`). Feeding such out-of-order writes through
`Append` would reset the ring on every other call, destroying the
walsender RAM cache. The slice B writer-side needs a primitive that
matches the [[0107-0007l]] `walBuffer.writeReserved` shape:
disjoint-LSN writes with no implicit tail advance, plus a separate
publication step driven by [[0107-0007n]] `tailPublisher`.

## Design

Two new methods on `MemRing`. No changes to existing `Append`,
`ReadAt`, `Range`, `BytesResident`, `Hits`, `Misses`, or `Cap` — those
all keep working for sequential callers (the current
`state.append` path stays on `Append` until the slice B call-site
rewrite switches it over).

### `WriteReserved`

```go
func (r *MemRing) WriteReserved(pos int64, data []byte) error
```

Writes `len(data)` bytes into the ring at LSN byte position `pos`
without advancing `head` or `tail`. Bytes become visible to `ReadAt`
only after a subsequent `PublishUpTo` advances `tail` past
`pos+len(data)`.

- **Window validation.** `pos < r.head` or `pos+len(data) > r.head+r.cap`
  returns `errMemRingReservedOutOfRange`. The valid window is
  `[head, head+cap)` — i.e., the ring's currently-allocated address
  range.
- **Empty data.** No-op returning `nil`. Matches `Append`'s `len==0`
  short-circuit and `walBuffer.writeReserved`'s contract. Runs before
  the range check so a zero-length "reservation" with an
  out-of-window `pos` is still benign.
- **Nil receiver.** No-op returning `nil`. Matches the `MemRing.Append`
  nil-safe convention (`NewMemRing(0) == nil`), so the slice B
  call-site rewrite needs no extra nil-guard at every write site under
  `wal_sender_memory_buffer == 0`.
- **Wrap.** Same wrap-aware copy as `Append`'s fast path; when
  `pos % cap + len(data) > cap`, the write splits across the ring
  boundary.

### `PublishUpTo`

```go
func (r *MemRing) PublishUpTo(safeTail int64)
```

Advances `r.tail` monotonically to `safeTail`, making bytes in
`[r.tail_old, safeTail)` — previously landed via `WriteReserved` —
visible to `ReadAt`. If the new tail would exceed capacity, `head` is
advanced so `tail-head ≤ cap`.

- **Monotonic.** `safeTail ≤ r.tail` short-circuits with no mutation.
  The caller is expected to derive `safeTail` from
  `tailPublisher.publishUpTo` (already monotonic), so a regressing
  `safeTail` typically reflects either a fresh ring (`r.tail == 0`) or
  a caller error.
- **Eviction.** When `safeTail - r.head > r.cap`, `head` advances to
  `safeTail - r.cap`. This mirrors `Append`'s eviction discipline.
- **Nil receiver.** No-op.

### Concurrency

| Operation | Lock | Concurrency vs others |
|---|---|---|
| `WriteReserved` | `mu.RLock` | many in parallel at disjoint LSN ranges; excludes `PublishUpTo` and `Append` |
| `PublishUpTo` | `mu.Lock` | excludes everything |
| `ReadAt` | `mu.RLock` (existing) | parallel with `WriteReserved`; excluded by `PublishUpTo` and `Append` |
| `Append` | `mu.Lock` (existing) | excludes everything |

Two `WriteReserved`s writing into disjoint LSN ranges write to
disjoint ring-slot ranges (since each single reservation is smaller
than ring capacity; the slice B insertion path bounds individual
records well below the 16 MiB default cap). Disjoint slot ranges plus
disjoint `copy` regions are race-free under Go's memory model. The
slice B call site guarantees disjoint LSN ranges by serialising
reservation allocation through [[0107-0007k]] `insertPosTracker`
(joint atomicity of `(curr, prev)` under `posMu`).

Overlapping LSN ranges from two `WriteReserved`s are a *contract
violation* and produce undefined ring contents. We do not detect this
(would need a per-byte ownership map); the slice B writer chain makes
it structurally impossible.

`PublishUpTo` taking the write lock against `WriteReserved`'s read
lock is required: head advance reclaims ring slots that an in-flight
`WriteReserved` at a low LSN might still be mid-memcpy on. The slice B
call site further constrains: `tailPublisher.publishUpTo` cannot
return a value above any stripe's active reservation LSN
(`min(upperBound, lowestActiveLSN)`), so a well-behaved drain never
asks `PublishUpTo` to advance past an active write. The lock here is
defence in depth for misbehaving callers and for the bootstrap window
where stripes have not yet published their first active LSN.

### Coexistence with `Append`

`Append` and `WriteReserved` + `PublishUpTo` describe two writer
modes. They can theoretically coexist on the same `MemRing` (both
take `mu` correctly), but in practice a given `Writer` will use one
or the other after the slice B call-site rewrite — `state.append`'s
current sequential `MemRing.Append` becomes
`MemRing.WriteReserved(pos, bytes)` per stripe, and the drain
goroutine calls `MemRing.PublishUpTo(safeTail)` after
`tailPublisher.publishUpTo`. The old `Append` API stays for any
non-stripe writer (e.g., bootstrap, recovery replay) that still
prefers sequential semantics.

## PG counterpart

PG does not have a separate `MemRing` — its equivalent role is taken
by the shared WAL buffer (`XLogCtl->pages`) that
`CopyXLogRecordToWAL` writes into under WAL insert locks. The PG
design parallels this: writes land at reserved offsets without a
global tail update; readers (walsender, redo) consult
`XLogCtl->LogwrtResult.Write` (the published watermark) to bound
their reads. The split is the same; goopg's `MemRing` plus
`walBuffer` exists for a different reason (M0010-0001's direct-IO
write path bypassing the OS page cache), but under stripe-concurrent
writers both rings need identical publication discipline.

## Lock-ordering tier

The new methods are leaves in the slice B lock-ordering DAG. The full
chain (after the call-site rewrite) becomes:

```
appendLockSet.lockByProcNum  (one of 8 stripes)
  → insertPosTracker.reserve  (briefly under posMu)
    → insertionTracker.setInsertingAt(stripe, start)
      → walBuffer.writeReserved
      → MemRing.WriteReserved          ← this foundation (writer)
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock

(separately, on the drain/flush goroutine, after the above:)
  tailPublisher.publishUpTo(upperBound, insertionTracker)
  walBuffer.advanceHead(published - prior)
  MemRing.PublishUpTo(published)        ← this foundation (publisher)
```

`MemRing.WriteReserved` runs in the writer's stripe-locked critical
section but holds only its own read lock — it never reaches back up
the chain. `MemRing.PublishUpTo` runs in the drain goroutine and
holds only its own write lock. Neither composes with `appendLockSet`,
`insertPosTracker`, `insertionTracker`, or `tailPublisher` directly;
the call site sequences them.

## Pre-reserve race carry-over

The same race documented in [[0107-0007m]] §"Pre-reserve race" and
carried over in [[0107-0007n]]'s contract applies here transitively:
between `insertPosTracker.reserve` returning and the matching
`insertionTracker.setInsertingAt(stripe, start)`, the observed
`lowestActiveLSN` (consumed by `tailPublisher.publishUpTo` and thence
fed to `MemRing.PublishUpTo`) can temporarily exceed the true minimum.
`MemRing.PublishUpTo` cannot close this race by itself; closing it is
the call-site rewrite's responsibility (option A: move
`setInsertingAt` under `posMu` so it is sequenced with the reserve;
option B: emit a pre-reserve marker). This foundation's contract is
"given an honest `(pos, data)` write and an honest `safeTail`
publish, deliver the bytes-write and publish atomically vs ring
readers."

## Test coverage

New file `internal/wal/mem_ring_concurrent_test.go`:

| Test | Pins |
|---|---|
| `TestMemRingWriteReservedAtHeadNoWrap` | basic write at `pos == head == 0`; head/tail untouched; readback after `PublishUpTo` |
| `TestMemRingWriteReservedAtNonZeroOffset` | LSN→ring-slot arithmetic at non-zero offset; neighbouring slots untouched |
| `TestMemRingWriteReservedWrapsAcrossRingBoundary` | wrap-aware copy split across the cap boundary |
| `TestMemRingWriteReservedRejectsBelowHead` | `pos < head` after a `PublishUpTo` advanced head returns `errMemRingReservedOutOfRange` |
| `TestMemRingWriteReservedRejectsPastWindow` | `pos+n > head+cap` rejected; exact boundary (`pos+n == head+cap`) accepted |
| `TestMemRingWriteReservedEmptyIsNoop` | nil and zero-length data short-circuit before range check; head/tail untouched |
| `TestMemRingWriteReservedNilReceiver` | nil receiver returns nil (no-op convention) |
| `TestMemRingWriteReservedDoesNotMutateHeadTail` | series of writes leaves head/tail exactly as before |
| `TestMemRingPublishUpToAdvancesTail` | tail tracks `safeTail`; head unchanged when below cap |
| `TestMemRingPublishUpToMonotonic` | regressing `safeTail` ≤ current `tail` is a no-op |
| `TestMemRingPublishUpToEvictsWhenOverCap` | `safeTail - head > cap` advances head to maintain residency invariant |
| `TestMemRingPublishUpToNilReceiver` | nil receiver no-op |
| `TestMemRingWriteReservedReadbackViaReadAt` | end-to-end: write at LSN X, publish past X+n, ReadAt(X) hits with the same bytes |
| `TestMemRingWriteReservedConcurrentDisjoint` | 8 goroutines × 50 records × 16 bytes in disjoint LSN ranges, race-clean under `-race`, every stripe's marker bytes land in the right slot after `PublishUpTo` |
| `TestMemRingPublishUpToAndWriteReservedSerialise` | publication during active writes: writers race, publisher periodically advances tail, final ReadAt of every written LSN range succeeds |

## Verification

```
go test -race -count=1 -run 'TestMemRing' ./internal/wal/
go test -race -count=1 ./internal/wal/
```

Both PASS.

## PG-compat

None. In-memory primitive; WAL record / file format / catalog / wire
all unchanged. Identical role to PG's `XLogCtl->pages` reserve +
publish split, executed at a different layer of the stack.

## Out of scope (later slice B foundations and call-site rewrite)

- Mounting `MemRing.WriteReserved` + `PublishUpTo` on `Writer` and
  rewriting `state.append`'s current `MemRing.Append` call (multi-
  loop work because `state.append` currently advances `walBuf.tail`
  and `memRing.tail` jointly inside `appendMu`; the rewrite splits
  the two and lets drain run concurrently).
- Closing the pre-reserve race ([[0107-0007m]] §"Pre-reserve race")
  — owned by the call-site rewrite.
- Drain coordination with concurrent stripe writes
  (`drainBufferBytes` currently runs under `appendMu` — the rewrite
  must let drain run concurrently with stripe writes by consuming
  `tailPublisher.publishUpTo`'s return as the drain ceiling for both
  `walBuffer.advanceHead` and `MemRing.PublishUpTo`).
- Deciding whether `lsnAllocator` ([[0107-0007h]]) becomes
  dead-code-removed once the call-site converges on
  `insertPosTracker` + `insertionTracker` + `tailPublisher`.
