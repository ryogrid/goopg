package xlog

import "fmt"

// stripeWriterCore is the slice B packaging struct that bundles the
// seven writer-side primitives needed to perform a stripe-locked WAL
// append plus the matching drain-side visibility publication. It is
// the planned mount-point on `Writer` for slice B's 8-stripe insert
// path (`docs/design/perf-optimize/07-wal-fsm-insert.md` §2) — the
// call-site rewrite that consumes this core (lifting
// `state.appendMu`'s four invariants into per-stripe local state plus
// shared `stripeWriterCore` state) is multi-loop scope and is NOT in
// this loop.
//
// Foundation 15 for M0107-0007 slice B. Foundations 1–14 landed the
// primitives in isolation; this foundation packages them with a
// single constructor that wires the cross-segment hook so the
// call-site rewrite can mount one field on `Writer` (the
// `*stripeWriterCore`) instead of seven, and call one method per
// site:
//
//   - `core.Append(procNum, record)` at the per-record write entry
//     point (replaces today's `state.append` body modulo the
//     LSN/page-header/pre-encoding pre-amble);
//   - `core.PublishUpTo(upperBound)` inside the drain goroutine's
//     per-tick loop (advances both ring tails to a single safe
//     watermark before disk I/O).
//
// Owned vs borrowed primitives. The core owns the four "new"
// primitives that do not pre-exist anywhere else on the Writer:
// `appendLockSet`, `insertPosTracker`, `insertionTracker`,
// `tailPublisher`. The two ring buffers (`walBuffer`, `MemRing`)
// are borrowed from the Writer — the Writer is the lifetime owner
// and the legacy `state.append` path also references the same
// rings, so the call-site rewrite does NOT duplicate them under the
// core. Borrowed pointers are stored as-is so a `nil` ring in
// Writer (under `Config.WALBuffers == 0` or
// `wal_sender_memory_buffer == 0`) propagates verbatim to every
// foundation that consults it; each foundation is independently
// nil-safe.
//
// Cross-segment hook. The constructor installs `emitSegmentPad`
// ([[0107-0007s]]) as `insertPosTracker.onCrossSegment` so a
// reservation that straddles a segment boundary automatically
// gets a well-formed XLOG_NOOP pad record dropped into the gap
// with the xl_prev chain preserved. The hook fires synchronously
// under `posMu` inside `insertPosTracker.reserveLocked`, so the
// pad bytes land in both rings before the triggering reservation
// is published to the active-stripe slot — the publication
// walker can never see a hole.
//
// emitSegmentPad returns an error (malformed padLen, range
// violation). The `onCrossSegment` signature has no error return,
// so the hook callback panics on any error. Per
// [[0107-0007s]]'s design: "callers under posMu should treat any
// error as fatal — the hook fires exactly once per crossing and a
// failed pad write breaks the xl_prev chain". A panic is the
// correct fatal escalation — silently dropping the pad would
// corrupt the xl_prev chain across the boundary, and returning
// the error up the stripeAppend stack would require restructuring
// reserveLocked's contract (the reservation has already been
// granted; rolling it back would race peer stripes that have
// observed the new curr). Sub-24-byte gaps and the 25-byte
// gap-anomaly are real corner cases that the call-site rewrite
// addresses via record-size alignment (the eventual `Append`
// pre-amble will round to 8 B so gaps are always multiples of 8);
// this packaging foundation does not invent that alignment
// policy.
//
// Recovery resume. `startCurr` / `startPrev` parameters let
// recovery position the tracker at the last-known reserved-LSN
// pair from the on-disk segment scan; the tracker advances
// forward from there. A fresh cluster passes `(0, 0)`.
//
// Concurrency. The core's methods inherit their composed
// primitives' concurrency properties:
//
//   - `Append` is per-stripe-serialised via `appendLockSet`; two
//     appends on different stripes proceed in parallel.
//   - `PublishUpTo` takes no locks of its own; the composed
//     primitives take only what they each need (publisher is
//     lock-free, walBuffer.publishTail is an atomic store,
//     MemRing.PublishUpTo holds its own write lock for the
//     head-clamp window).
//   - `Load` reads (curr, prev) under `posMu` (the joint
//     atomicity of (curr, prev) is preserved by the tracker's
//     existing contract).
//
// Lock-ordering tier — combines the writer-chain from
// [[0107-0007u]] `stripeAppend` and the drain-chain from
// [[0107-0007t]] `publishVisibility`:
//
//	(stripe writer, per-record):
//	  core.Append(procNum, record)
//	    → appendLockSet.lockByProcNum               (one of 8 stripes)
//	      → insertPosTracker.reserveAndPublish      (posMu held)
//	          → (rare) onCrossSegment(start, boundary, gapPrev)
//	              → emitSegmentPad → buildSegmentPadRecord +
//	                walBuffer.writeReserved + MemRing.WriteReserved
//	          → insertionTracker.setInsertingAt(stripe, start)
//	        (posMu released)
//	      → walBuffer.writeReserved
//	      → MemRing.WriteReserved
//	      → insertionTracker.setInsertingAt(stripe, lsnIdle)
//	    → drop stripe lock
//
//	(drain goroutine, per-tick):
//	  core.PublishUpTo(upperBound)
//	    → tailPublisher.publishUpTo(upperBound, insertionTracker)
//	    → walBuffer.publishTail(safeTail)
//	    → MemRing.PublishUpTo(safeTail)
//
// Dead code until the slice B call-site rewrite mounts
// `*stripeWriterCore` on `Writer` and switches `state.append` to
// call `core.Append`; foundation-first pattern matches slice C
// ([[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]] before
// [[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]]) and the
// thirteen earlier slice B foundations ([[0107-0007i]] /
// [[0107-0007j]] / [[0107-0007k]] /
// [[0107-0007l]] / [[0107-0007m]] / [[0107-0007n]] /
// [[0107-0007o]] / [[0107-0007p]] / [[0107-0007q]] /
// [[0107-0007r]] / [[0107-0007s]] / [[0107-0007t]] /
// [[0107-0007u]]).
type stripeWriterCore struct {
	locks      *appendLockSet
	posTracker *insertPosTracker
	inserting  *insertionTracker
	publisher  *tailPublisher
	walBuf     *walBuffer
	memRing    *MemRing
}

// newStripeWriterCore constructs a stripeWriterCore wired with the
// cross-segment hook so cross-boundary reservations automatically
// emit XLOG_NOOP pad records into the gap.
//
// segSize MUST be > 0 (forwarded to newInsertPosTracker which
// panics otherwise; matches PG's `wal_segment_size` invariant).
// startCurr / startPrev set the initial (curr, prev) — typically
// (0, 0) for a fresh cluster, or the recovery-resume pair from a
// segment-scan after restart. walBuf and memRing may each be nil
// independently; per-foundation nil-safety propagates.
//
// onCrossSegment closure. The constructor pins `walBuf` / `memRing`
// into the closure so the call-site rewrite cannot inadvertently
// rewire them without rebuilding the core. emitSegmentPad errors
// panic per the "fatal pad failure" contract documented above.
// `lay` (M0131-S30.1b) carries the page-header layout the pad must be
// emitted with — the pad occupies stream bytes, and in page-header mode
// those bytes include any page header the gap spans. Its zero value keeps
// the legacy raw-bytes shape for header-less fixtures.
func newStripeWriterCore(segSize, startCurr, startPrev uint64, walBuf *walBuffer, memRing *MemRing, lay padLayout) *stripeWriterCore {
	onCross := func(start, boundary, gapPrev uint64) (padded bool) {
		_, padded, err := emitSegmentPad(walBuf, memRing, start, boundary, gapPrev, lay)
		if err != nil {
			panic(fmt.Sprintf("wal: stripeWriterCore: cross-segment pad emit failed: %v", err))
		}
		return padded
	}
	return &stripeWriterCore{
		locks:      &appendLockSet{},
		posTracker: newInsertPosTracker(startCurr, startPrev, segSize, onCross),
		inserting:  newInsertionTracker(),
		publisher:  newTailPublisher(),
		walBuf:     walBuf,
		memRing:    memRing,
	}
}

// Append performs one stripe-locked WAL append by delegating to
// [[0107-0007u]] stripeAppend with the core's bundled primitives.
// Returns the reservation's start LSN, the prev LSN to stamp into
// the next record's xl_prev field, and any error from the byte
// writes. Per stripeAppend's contract, the END marker fires before
// unlock regardless of byte-write outcome so the drain's
// publication watermark is never frozen by a failed append.
//
// nil receiver returns an explicit error so the call-site rewrite
// can detect a mis-wired Writer (core unset before append path
// reached) without a nil-pointer panic.
func (c *stripeWriterCore) Append(procNum int32, record []byte) (start, prev uint64, err error) {
	if c == nil {
		return 0, 0, errStripeWriterCoreNil
	}
	return stripeAppend(c.locks, c.posTracker, c.inserting, c.walBuf, c.memRing, procNum, record)
}


// AppendBuilt performs one stripe-locked WAL append where the record's
// bytes can only be materialised AFTER the LSN reservation grants a
// stable `prev` LSN. Delegates to [[0107-0007y]] stripeAppendBuild with
// the core's bundled primitives. Returns the reservation's start LSN,
// the prev LSN that was passed into `build`, and any error from build
// or the byte writes. Per stripeAppendBuild's contract, the END marker
// fires before unlock regardless of outcome.
//
// Use AppendBuilt for PG-compat records whose xl_prev field
// (`XLogRecord.Prev`) must reflect the joint-atomic prev returned by
// the reservation; use Append when the caller has already encoded the
// bytes without prev linkage (e.g., legacy goopg-internal records).
//
// nil receiver returns errStripeWriterCoreNil so the call-site rewrite
// can detect a mis-wired Writer (core unset before append path
// reached) without a nil-pointer panic — same contract as Append.
func (c *stripeWriterCore) AppendBuilt(procNum int32, size int, build func(prev uint64) ([]byte, error)) (start, prev uint64, err error) {
	if c == nil {
		return 0, 0, errStripeWriterCoreNil
	}
	return stripeAppendBuild(c.locks, c.posTracker, c.inserting, c.walBuf, c.memRing, procNum, size, build)
}

// AppendBuiltEmitted performs one stripe-locked WAL append where both the
// record's byte length (after page-header insertion) and the prev LSN
// must be resolved by the reservation itself. Delegates to foundation 19
// ([[0107-0007ab]]) stripeAppendBuiltEmitted with the core's bundled
// primitives. Returns the reservation's start LSN, the prev LSN that was
// passed into `build`, the total emitted byte count (page headers +
// record body), the leading page-header byte count (0 if start is not
// page-aligned), and any error from reservation, build, or the byte
// writes.
//
// Use AppendBuiltEmitted for PG-compat records whose on-the-wire size
// includes page headers stamped by emitWithPageHeaders — the build
// closure receives the post-reservation (start, prev, total, leading)
// tuple pre-computed under posMu — start is needed by emitWithPageHeaders
// to stamp page headers at the correct LSN — and must return exactly
// `total` bytes of page-headered output. Use [[0107-0007y]] AppendBuilt
// for records whose page-header schedule is the caller's responsibility
// (currently no such site exists in goopg — kept as the size-explicit
// primitive for tests and future flexibility). Use Append for records
// that are already fully encoded without prev linkage.
//
// nil receiver returns errStripeWriterCoreNil so the call-site rewrite
// can detect a mis-wired Writer without a nil-pointer panic — same
// contract as Append and AppendBuilt.
func (c *stripeWriterCore) AppendBuiltEmitted(procNum int32, recordLen int, build func(start, prev uint64, total, leading int) ([]byte, error)) (start, prev uint64, total, leading int, err error) {
	if c == nil {
		return 0, 0, 0, 0, errStripeWriterCoreNil
	}
	return stripeAppendBuiltEmitted(c.locks, c.posTracker, c.inserting, c.walBuf, c.memRing, procNum, recordLen, build)
}


// AppendXLogPayload is the top-level PG-compat WAL append composer.
// Closes the foundation chain by delegating to foundation 22
// ([[0107-0007ae]]) appendXLogPayload, which wraps predictXLogRecordLen
// + AppendBuiltEmitted + encodeRecordXLog + emitWithPageHeaders into
// a single call. Returns the reservation's start LSN, the prev LSN
// stamped into xl_prev, the total emitted byte count (page headers
// + record body), the leading page-header byte count (0 if start is
// not page-aligned), and any error from reservation, encode, or the
// byte writes.
//
// This is the mount-point the slice B call-site rewrite will install
// at `state.append`'s PG-compat write path; the byte stream produced
// is identical to today's `state.append` PG-compat path (encodeRecordXLog
// + emitWithPageHeaders under appendMu), only under per-stripe locking.
//
// nil receiver returns errStripeWriterCoreNil so the call-site rewrite
// can detect a mis-wired Writer without a nil-pointer panic — same
// contract as Append / AppendBuilt / AppendBuiltEmitted.
func (c *stripeWriterCore) AppendXLogPayload(procNum int32, payload []byte, segSize int64, sysID uint64, tli uint32) (start, prev uint64, total, leading int, err error) {
	return appendXLogPayload(c, procNum, payload, segSize, sysID, tli)
}

// PublishUpTo advances both ring tails to the safe-tail watermark
// derived from upperBound and the core's insertion tracker, by
// delegating to [[0107-0007t]] publishVisibility. Returns the
// safe-tail value published to both rings (capped at upperBound)
// for callers to use as the drain ceiling on the subsequent
// `readForDrain` / `writeAt` / `advanceHead` chain.
//
// nil receiver returns 0 (no watermark advanced) so a transitional
// call-site state with the core unset is benign on the drain
// goroutine.
func (c *stripeWriterCore) PublishUpTo(upperBound int64) int64 {
	if c == nil {
		return 0
	}
	return publishVisibility(c.publisher, c.walBuf, c.memRing, c.inserting, upperBound)
}

// Load returns the current (curr, prev) reservation snapshot under
// posMu. Callers feed `curr` as the upperBound to PublishUpTo (it
// is the highest LSN that has been reserved); the prev field is
// exposed for diagnostics and recovery scenarios.
//
// nil receiver returns (0, 0).
func (c *stripeWriterCore) Load() (curr, prev uint64) {
	if c == nil {
		return 0, 0
	}
	return c.posTracker.load()
}

// resetPosition force-advances posTracker to (curr, prev). Used by
// state.append's Path A (direct disk write) to resync the core after a
// write that bypasses stripe B. Must not be called while any stripe
// reservation is in flight (no concurrent AppendXLogPayload callers).
// nil receiver is a no-op.
func (c *stripeWriterCore) resetPosition(curr, prev uint64) {
	if c == nil {
		return
	}
	c.posTracker.resetPosition(curr, prev)
}

// PublishedTail returns the publisher's current watermark without
// re-computing it. Useful for diagnostics and assertions
// (`PublishUpTo`'s return value is the published-then-capped
// watermark; this method exposes the raw published value).
//
// nil receiver returns 0.
func (c *stripeWriterCore) PublishedTail() int64 {
	if c == nil {
		return 0
	}
	return c.publisher.load()
}

// errStripeWriterCoreNil is returned when methods are invoked on a
// nil *stripeWriterCore. The call-site rewrite will mount the core
// non-conditionally on Writer; this guard exists so transitional
// states (e.g., tests that pass a nil core into a function expecting
// one) surface a structured error instead of a nil-pointer panic.
var errStripeWriterCoreNil = fmt.Errorf("wal: stripeWriterCore: nil receiver")
