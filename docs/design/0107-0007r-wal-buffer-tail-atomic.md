# 0107-0007r — Phase D4: `walBuffer.tail` upgraded to `atomic.Int64` (M0107-0007, slice B foundation 11)

Status: accepted
Milestone: M0107-0007 (Phase D4 — 8-stripe WAL insert striping + FSM page distribution)
Parent chapter: [[07-wal-fsm-insert]] §2
Related foundations: [[0107-0007h]], [[0107-0007i]], [[0107-0007j]], [[0107-0007k]], [[0107-0007l]], [[0107-0007m]], [[0107-0007n]], [[0107-0007o]], [[0107-0007p]], [[0107-0007q]]

## Problem

Foundation 10 ([[0107-0007q]]) landed `walBuffer.publishTail`, the
bytes-side publication primitive that lets a drain goroutine
monotonically advance `b.tail`. Its "Out of scope" note left the
field-level atomicity upgrade as a separate foundation:

> upgrading `b.tail` to `atomic.Int64` so a single drain goroutine's
> `publishTail` and stripe writers' tail readers can coexist without
> a data race (mechanical but ripples to 5 production sites + the
> existing test that pokes `b.tail` directly; lands in its own loop
> so this foundation's footprint stays minimal).

The slice B call-site rewrite (multi-loop scope) will move stripe
writers' byte writes (`writeReserved`) out of the `appendMu`
critical section. After that rewrite, stripe writers will not touch
`b.tail` themselves, but readers that consult `b.tail` —
`resident()` (called by drain and by stripe writers to compute
`free()` headroom), `readForDrain()` (called by drain), and
`readAt()` (called by walsender) — must observe `b.tail` race-free
against the drain goroutine's `publishTail` store. With `b.tail`
as a plain `int64`, that read-vs-store pair is a data race
under Go's memory model (the read is unsynchronised relative to
the store) and on 32-bit ARM the read can also tear.

This foundation upgrades the field to `atomic.Int64` so every
read uses `Load` and every write uses `Store`. The publication
discipline established by [[0107-0007q]] is unchanged.

## Change summary

- `walBuffer.tail` field: `int64 → atomic.Int64`.
- All five production reads/writes of `b.tail` rewritten:
  - `reset()`: `b.tail = startLSN` → `b.tail.Store(startLSN)`.
  - `resident()`: `b.tail - b.head` → `b.tail.Load() - b.head`.
  - `append()`: a single `Load` captures the start position,
    a final `Store(tail + n)` publishes the advance. Single-
    goroutine usage (the Path A `state.append` holds `appendMu`),
    so a plain non-CAS Load+Store is correct here.
  - `readAt()`: `pos >= b.tail` and `b.tail - pos` both consume
    a single `Load` snapshot stored in a local `tail` variable
    so the two reads are coherent.
  - `publishTail()`: `Load → if safeTail > cur → Store(safeTail)`,
    monotonic by construction.
- Test files updated: 11 reads via `.Load()`, 3 direct writes via
  `.Store(...)`.

## Why a single drain goroutine is sufficient

Under the slice B call-site rewrite:

- Stripe writers reserve LSNs via [[0107-0007p]]
  `insertPosTracker.reserveAndPublish` and land bytes via
  [[0107-0007l]] `walBuffer.writeReserved`. Neither operation
  touches `b.tail`.
- The drain goroutine consults [[0107-0007n]] `tailPublisher` for
  a monotonic safe-tail value and calls `publishTail(safeTail)`.

So `publishTail` is single-writer. The `atomic.Int64` upgrade
covers the writer-vs-reader race; CAS is unnecessary because there
is no writer-vs-writer race.

If a future caller deliberately drives `publishTail` from multiple
goroutines, the current Load+Store pattern can lose updates under
a race (two goroutines Load `X`, both decide to Store `Y > X`,
both Store completes — net effect identical, but if their
candidates differ the second Store could regress relative to the
first observable max). Documented at the call site so a future
caller can promote to a CAS loop if needed.

## What this does NOT change

- `b.head` and `b.base` stay plain `int64`. They are mutated only
  by `advanceHead` (drain goroutine) and read only by methods that
  run on the drain goroutine (`resident`, `readForDrain`,
  `readAt`'s `pos < b.head` guard). Reader/writer relationships
  among them remain single-goroutine.
- `walBuffer.append` keeps its single-goroutine semantics. It is
  still on the Path A code path that runs under `appendMu`; the
  Load+Store pair is correct because no other goroutine writes to
  `b.tail` while `append` holds the lock.
- The `publishTail` API surface is unchanged. Its monotonicity
  contract from [[0107-0007q]] is unchanged.

## Tests

- `TestWALBufferTailIsAtomicInt64` — compile-time pin via
  `*atomic.Int64 = &b.tail`. Anyone shrinking the field back to a
  plain `int64` trips a compile error. Pointer form sidesteps the
  `atomic.noCopy` lint that the value-assignment form would emit.
- `TestWALBufferPublishTailObservedByConcurrentReader` — 100 K
  iterations: a writer goroutine alternates `publishTail(i)`
  with `stored.Store(i)`; a reader goroutine in the main test
  Load's `b.tail` and asserts (a) the observed value never
  regresses across successive Loads (monotonic snapshot), and
  (b) the observed value never exceeds the writer's last Store
  by more than 1 (publishTail may have run for `i` but the
  subsequent `stored.Store(i)` hasn't propagated). Under -race
  any data race on a plain `int64` field would be flagged; the
  monotonicity check is defence in depth for non-race CI runs.
- Existing 11 tests in `wal_buffer_publish_tail_test.go` and 5 in
  `wal_buffer_write_reserved_test.go` continue to pass — they
  consume `b.tail` via `.Load()` (and the few direct writes use
  `.Store(...)`).

Verified: `go test -race -count=1 ./internal/wal/` PASS (3.18 s);
`go vet ./internal/wal/` clean.

## PG counterpart

PG stores `XLogCtl->LogwrtResult.Write` as a plain `XLogRecPtr`
(64-bit unsigned) but accesses it under `info_lck` (a spinlock)
or via `pg_atomic_read_u64` on platforms where that's available.
goopg's atomic.Int64 is the direct equivalent of the latter
path — both rely on the platform's 8-byte atomic load/store
being natively race-free.

## Lock-ordering tier (after foundation 11)

Unchanged from [[0107-0007q]]. This foundation only changes
how a field is read; the lock-ordering DAG is unaffected.

```
(stripe writer):
  appendLockSet.lockByProcNum
    → insertPosTracker.reserveAndPublish
    → walBuffer.writeReserved          (no b.tail touched)
    → MemRing.WriteReserved
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock

(drain goroutine, separately):
  safeTail := tailPublisher.publishUpTo(upperBound, insertionTracker)
  walBuffer.publishTail(safeTail)      ← b.tail.Store
  walBuffer.advanceHead(safeTail - prior)
  MemRing.PublishUpTo(safeTail)

(reader goroutines — walsender / drain pre-read):
  walBuffer.resident() / readAt() / readForDrain()  ← b.tail.Load
```

## Dead code until the call-site rewrite

Like the ten earlier slice B foundations, this foundation does not
change the production caller; `state.append` still holds `appendMu`
across `walBuf.append` and the Load+Store pair runs serialised.
The atomicity becomes load-bearing once the slice B call-site
rewrite splits `state.appendMu`'s four invariants and the drain
goroutine begins calling `publishTail` outside any global lock.

## Out of scope (later slice B foundations and call-site rewrite)

- Upgrading `b.head` and `b.base` to atomics. Stays single-
  goroutine in the planned model (drain owns both); promotion
  becomes necessary only if future work moves head/base mutation
  off the drain goroutine.
- Mounting `publishTail` on `Writer` and consuming it from the
  drain/flush goroutine (multi-loop because `state.append`
  currently advances `walBuf.tail` and `memRing.tail`
  synchronously inside `appendMu`; the rewrite splits the four
  invariants — writePos / walBuf / memRing / writeLSN — into
  per-stripe local state vs. shared state).
- Drain coordination with concurrent stripe writes
  (`drainBufferBytes` currently runs under `appendMu` — the
  rewrite must let drain run concurrently with stripe writes by
  consuming `tailPublisher.publishUpTo`'s return as drain ceiling
  for `walBuffer.publishTail` / `walBuffer.advanceHead` /
  `MemRing.PublishUpTo`).
- Deciding whether [[0107-0007h]] `lsnAllocator` becomes
  dead-code-removed once the call-site converges on
  `insertPosTracker` + `insertionTracker` + `tailPublisher` +
  `reserveAndPublish` + `publishTail` — `reserve` remains in the
  API as a callable primitive without a tracker.
