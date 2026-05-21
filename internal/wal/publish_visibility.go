package wal

// publishVisibility is the drain-side publication composer for
// M0107-0007 slice B (Phase D4 — 8-stripe WAL insert locks per
// `docs/design/perf-optimize/07-wal-fsm-insert.md` §2). It computes the
// safe-tail watermark from [[0107-0007n]] `tailPublisher` and advances
// both the bytes-side ring ([[0107-0007q]] `walBuffer.publishTail`) and
// the walsender mirror ([[0107-0007o]] `MemRing.PublishUpTo`) to that
// watermark in a single call.
//
// Foundation 13 of slice B. The writer-side composer [[0107-0007s]]
// `emitSegmentPad` is its symmetric counterpart on the stripe-writer
// chain: emitSegmentPad lands cross-segment pad bytes into both rings
// without advancing visibility; publishVisibility advances visibility
// for both rings without writing bytes. Composing the publication chain
// into one function lets the planned slice B call-site rewrite install
// it as a one-liner inside the drain goroutine, exactly mirroring how
// emitSegmentPad will install as a one-liner inside
// `insertPosTracker.onCrossSegment`.
//
// Drain-chain placement. Per [[0107-0007q]]'s lock-ordering tier, the
// drain goroutine (running concurrently with stripe writers in the
// planned model) executes:
//
//	(drain goroutine, after stripe writers land bytes via writeReserved)
//	  safeTail := tailPublisher.publishUpTo(upperBound, insertionTracker)
//	  walBuffer.publishTail(safeTail)         ┐
//	  MemRing.PublishUpTo(safeTail)           ┘ ← composed into one call here
//	  // … drain ceiling consumed by walBuffer.readForDrain + writeAt;
//	  // walBuffer.advanceHead runs after disk write confirms persistence.
//
// publishVisibility composes the three inner calls (publishUpTo,
// publishTail, PublishUpTo). The post-write `advanceHead` and the
// disk-side `readForDrain → writeAt` stay outside the composer because
// they belong to the drain loop's IO scheduling, not the publication
// step.
//
// Why compose these three and not include advanceHead? `publishTail` /
// `PublishUpTo` mark bytes drainable (visible to readers); `advanceHead`
// reclaims ring slots after they have been durably flushed to disk.
// The two phases must not be coupled — a drain that publishes
// visibility before flushing disk is what consumers want (concurrent
// readers can read fully-written bytes without waiting for fsync), but
// advancing head before flush would let stripe writers overwrite bytes
// that have not yet hit the WAL files.
//
// upperBound contract. The caller passes the highest LSN that has
// been reserved (typically `insertPosTracker.load()`'s curr). The
// internal `tailPublisher.publishUpTo` then bounds the safe tail at
// the lowest of (upperBound, every active stripe's start LSN);
// publishVisibility relays that watermark to both rings unchanged.
// The composer does not impose any additional bound — if the caller
// passes a stale or speculative upperBound, the publisher's monotonic
// CAS still prevents regression.
//
// Returns the safe-tail value published to both rings (i.e., the value
// `tailPublisher.publishUpTo` returned, capped at the caller's
// upperBound by that primitive). Callers consume the return as the
// new drain ceiling for the subsequent `readForDrain` / `writeAt`
// pair.
//
// Nil-safety. Each ring is independently nil-safe so the composer
// works under `Config.WALBuffers == 0` (no walBuf) and/or
// `wal_sender_memory_buffer == 0` (no memRing). Both
// `walBuffer.publishTail` and `MemRing.PublishUpTo` short-circuit on
// nil receivers, so the composer needs no per-ring guard. `publisher`
// is also nil-safe — `tailPublisher.publishUpTo` returns 0 for a nil
// receiver — but a nil publisher leaves the rings un-advanced (return
// value 0); the composer is intended for a fully-constructed drain
// pipeline, and a nil publisher in production would be a wiring bug.
// `tracker` may be nil; `publishUpTo` treats nil as "all stripes
// idle" and the safe tail collapses to `upperBound`.
//
// Why no error return. The three composed primitives are infallible
// (`publishUpTo` is a pure CAS loop with no failure mode;
// `publishTail` is a monotonic store; `PublishUpTo` is a monotonic
// store under a write lock that internally clamps head when residency
// would exceed cap). The composer is therefore infallible too — the
// drain goroutine can call it on every tick without error-handling
// scaffolding.
//
// Why does this primitive exist if it is only three lines? Three
// reasons, matching the rationale that made [[0107-0007s]]
// `emitSegmentPad` worth lifting out:
//
//  1. Lock-ordering documentation. The doc comment pins the exact
//     drain-side composition the call-site rewrite must perform; any
//     future drift (e.g., publishing MemRing before walBuffer) would
//     fail the foundation's contract-anchored tests rather than slip
//     through a buried call site.
//  2. Single nil-handling site. The three primitives' nil-safe
//     contracts compose cleanly here; without the composer, every
//     drain call would need to re-derive the no-op-on-nil rules.
//  3. Foundation-first pattern. The slice B call-site rewrite already
//     consumes twelve foundations. Lifting the drain composition into
//     a thirteenth lets the rewrite stay a structural mechanical
//     change (move-the-call, not invent-the-call).
//
// Lock-ordering tier (when invoked from the planned call-site rewrite,
// in the drain goroutine — separately from the stripe writer chain):
//
//	(drain goroutine, after publication-watermark inputs are captured:)
//	  publishVisibility(publisher, walBuf, memRing, tracker, upperBound)
//	  walBuffer.readForDrain(safeTail - head, dst)
//	  writeAt(...)                              ← disk I/O
//	  walBuffer.advanceHead(safeTail - head)    ← reclaim ring slots
//
// publishVisibility takes no locks of its own; the composed primitives
// take only what they need (tailPublisher is lock-free, walBuffer's
// tail is `atomic.Int64` per [[0107-0007r]], MemRing.PublishUpTo holds
// its own write lock for the head-clamp window).
//
// Dead code until the slice B call-site rewrite mounts the publisher
// + the rings on `Writer` and runs the drain on a dedicated goroutine;
// foundation-first pattern matches slice C ([[0107-0007b]] /
// [[0107-0007c]] / [[0107-0007d]] before [[0107-0007e]] /
// [[0107-0007f]] / [[0107-0007g]]) and the eleven earlier slice B
// foundations ([[0107-0007i]] / [[0107-0007j]] /
// [[0107-0007k]] / [[0107-0007l]] / [[0107-0007m]] / [[0107-0007n]] /
// [[0107-0007o]] / [[0107-0007p]] / [[0107-0007q]] / [[0107-0007r]] /
// [[0107-0007s]]).
func publishVisibility(
	publisher *tailPublisher,
	walBuf *walBuffer,
	memRing *MemRing,
	tracker *insertionTracker,
	upperBound int64,
) int64 {
	safeTail := publisher.publishUpTo(upperBound, tracker)
	walBuf.publishTail(safeTail)
	memRing.PublishUpTo(safeTail)
	return safeTail
}
