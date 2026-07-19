# 0107-0012 — WAL drain-safety: LSN-0 idle sentinel + Path A appendMu discipline

Status: accepted
Milestone: M-NIGHTLY (AI-20260717-010601-001, race/internal/wal REOPEN)
Related: [[0107-0011]] (the earlier, test-only drain artifact fix), [[0107-0007ai]]
(head/base atomic async drain), [[0107-0007n]] tailPublisher,
[[0107-0007m]] insertionTracker, [[0107-0007aa]] reserveEmittedAndPublish,
docs/design/wal-backend-flush/ (slice 7 drain-safety certification).

## Symptom

`go test -race ./internal/wal/` failed intermittently (~1 in 8–12 runs) in
`TestDrainSafetyStress` with a genuine **data race** on the WAL ring backing
array (`walBuffer.buf`):

- **Read** by the drain goroutine: `writeAt` → `os.File.Pwrite` reading the
  `first`/`second` slices returned by `drainBufferBytes` → `readForDrain`.
- **Write** by a fast-path stripe writer: `walBuffer.writeReserved`
  (`stripeAppendBuiltEmitted` → `tryAppend`).

This is distinct from the [[0107-0011]] artifact (a false LSN-invariant read
ordering); this is a real concurrent read/write of the same bytes: the drain
persists a ring region to disk while a stripe writer is still writing into it.

The [[0107-0011]] fix was test-only and did not touch this race.

## Diagnosis method

A temporary assertion in `writeReserved` — `panic if lsn < b.tail.Load()` —
turned the timing-dependent race into a deterministic invariant check. A
stripe writer must never land bytes **below** the published drain watermark
(`tail`), because `[head, tail)` is exactly the region the drain reads. The
assertion fired reliably, and its captured `(lsn, tail, head, base, n)` tuples
revealed **two independent root causes**.

## Root cause 1 — `lsnIdle == 0` aliases a legitimate LSN-0 reservation

`insertionTracker` records, per stripe, the start LSN of the record it is
currently writing; the tail publisher caps `safeTail` at
`min(upperBound, lowestActiveLSN())` so the drain never advances past a
still-in-flight write. The idle sentinel was `int64(0)`, chosen so the
`atomic.Int64` zero value meant "idle" and the constructor needed no init loop.
The comment asserted "call sites that legitimately reserve LSN 0 do not exist in
production paths (LSN 0 is the invalid sentinel)".

That assumption is **false**. The `walBuffer` is byte-addressed and a fresh
cluster / freshly `reset` buffer starts its LSN space at 0, so the very first
WAL record reserves `[0, total)` and calls `setInsertingAt(stripe, 0)`. With
`lsnIdle == 0`, `lowestActiveLSN()`'s `v != lsnIdle` skips that stripe — it
looks idle — so the publisher advances `tail` to the record's end while the
stripe writer is still writing at LSN 0. The drain then races those bytes.

Captured tuple: `lsn=0 tail=3144 head=0 n=3144` — `tail` published to the
record's end before its bytes were written.

**Fix:** make `lsnIdle = -1` (a value no byte-LSN can take) so byte-LSN 0 is a
distinguishable *active* value. `newInsertionTracker` now explicitly stores
`lsnIdle` into every slot (the zero value no longer means idle). The
"lsnIdle == 0 is load-bearing" pinning test was inverted to pin `-1` and the
rationale rewritten.

## Root cause 2 — Path A released `appendMu` across its direct `writeAt`

`appendPGCompat` **Path A** (walBuf disabled / record too large / overflow
drain) writes its record **straight to the segment file**, bypassing the stripe
ring, and does **not** reserve its `[writePos, end)` LSN range in the
`insertPosTracker`. Its own comment claimed "appendMu.Lock() is held across
drain + write", but the code **released** `appendMu` before the `writeAt` and
re-acquired it afterwards for the trailing `walBuf.reset(end)` +
`resetPosition(end)`.

During that release window, concurrent fast-path stripe writers (holding
`appendMu.RLock`) reserve from the not-yet-advanced `curr` — which still equals
Path A's `writePos` — land bytes in the ring, and publish a `tail` covering
them. Path A then re-acquires the lock and `reset`s the ring / rewinds `curr`
to `end`, **rewinding `tail` backwards** over the stripe writers' just-published
region. A subsequent (or in-flight) `writeReserved` then lands below the
rewound `tail`, which the drain is concurrently reading.

Captured tuple signature: `head == base == lsn` with `tail == lsn + 40` (or
`+80`) — the exact fingerprint of a `reset(end)` rewind followed by a
reservation at the rewound `curr` while `tail` sat above it.

**Fix:** hold `appendMu.Lock()` across the **entire** Path A critical section
(drain → encode → `writeAt` → `reset` → `resetPosition`) via `defer`, honoring
the invariant the comment always documented. This freezes RLock stripe writers
for the duration of this uncommon direct write, so no reservation can collide
with Path A's file write or observe a mid-reset ring. Lock ordering is
unchanged (`writeMu → appendMu`); Path A already holds `writeMu`, so no other
slow path or flush holder runs concurrently, and the only added cost is briefly
blocking fast-path appenders during a rare overflow/oversized write. `writePos`
is now assigned only after a successful `writeAt` (no separate rollback branch
needed).

`appendRaw`'s Path A has the same release-before-`writeAt` shape but is the
single-threaded physical-walreceiver path (no concurrent fast-path appenders by
contract), so it is not implicated by this race and is left unchanged; if a
mixed-caller scenario ever arises it should receive the identical treatment.

## Verification

- Deterministic assertion (`writeReserved` panic on `lsn < tail`): **0/60**
  below-tail writes after both fixes (was 25/25 before fix 1, ~55/60 after fix 1
  alone — proving fix 2 was necessary).
- `go test -race -run '^TestDrainSafetyStress$' ./internal/wal/`: **40/40** PASS
  (was ~1-in-10 failing).
- `go test -race ./internal/wal/` (whole package, the nightly repro): 3/3 PASS.
- `go test ./internal/wal/` + `go vet ./internal/wal/`: clean.
- pre-commit pgbench smoke: PASS (WAL append is on the write hot path; Path A
  holding `appendMu` slightly longer is a rare-path change, no TPS regression).

## Files

- `internal/wal/insertion_tracker.go` — `lsnIdle = -1`; constructor initialises
  all slots; doc comments updated.
- `internal/wal/insertion_tracker_test.go` — inverted the sentinel pinning test.
- `internal/wal/writer.go` — `appendPGCompat` Path A holds `appendMu.Lock()`
  across the whole section.
