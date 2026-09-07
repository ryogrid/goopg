package storage

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/port/runtimeshim"
)

// E-19 / EX5-05 slice S3 — StartRead / FinishRead, the installing prefetch.
//
// Design: docs/design/storage-prefetch-buffer/E19-INSTALLING-PREFETCH.md,
// slice order §5.9a. Read §2 before changing anything here: PG's
// PrefetchBuffer does NOT install into shared buffers either — the installing
// path is StartReadBuffers (bufmgr.c:1489) / WaitReadBuffers (:1632), whose
// buffers are PINNED BEFORE SUBMISSION (StartReadBuffersImpl's
// PinBufferForBlock loop, :1318-1325) and whose pins are held by the caller's
// ReadBuffersOperation until it hands them over (read_stream.c:983, "Pin
// transferred to caller"). That is the shape transcribed here, and the pin
// discipline is not a detail: it is what makes the mechanism safe.
//
// ------------------------------------------------------------------
// The lock discipline, which is the whole of slice S3
// ------------------------------------------------------------------
//
// The source review's finding 2 is a real deadlock, and it is a CYCLE with two
// edges:
//
//	(a) the issuer holds pinMu across a blocking Submit — methodWorker.Submit
//	    blocks when its queue is full, by design, and the queue is io_workers*4
//	    = 12 deep at default GUCs; and
//	(b) the completion work runs on the worker goroutine and wants pinMu (it
//	    must read slotWaiters[idx] under pinMu before clearing the IO bit).
//
// A window deeper than the queue, or two concurrent scans, closes the cycle.
// This file removes BOTH edges rather than one, because removing one leaves
// the next caller free to re-add it:
//
//	(a) StartRead releases pinMu BEFORE it submits. pinMu covers only the
//	    victim claim, the eviction and the bufmap publish — the same prologue
//	    pinLoad runs — and is dropped before PrefetchBlock is called.
//	(b) The AIO completion callback does no pool work AT ALL. It releases the
//	    relFile block latch and nothing else, exactly as PrefetchBlock already
//	    arranges. Publication — waking waiters, clearing the IO bit, marking
//	    the slot valid — happens in FinishRead, on the CONSUMER's goroutine,
//	    which is by construction not holding pinMu.
//
// So no goroutine ever holds pinMu across a submission, and no AIO worker
// goroutine ever asks for pinMu. The cycle has no edge left.
// TestStartReadDoesNotHoldPinMuAcrossSubmit builds the cycle explicitly (a
// capacity-bounded engine whose worker takes pinMu) and hangs on the naive
// implementation.
//
// ------------------------------------------------------------------
// What a concurrent Pin of an in-flight block does
// ------------------------------------------------------------------
//
// Nothing new, and that is the point. claimVictim sets slotIOBit and bumps the
// generation, so tryPinSlot rejects the slot (stateIO) and Pin falls into
// pinSlow, which finds the tag in the bufmap with the IO bit set and parks on
// slotSema[idx] (bufpool.go, pinSlow's stateIO branch). It waits on slot
// VALIDITY and never issues a second read of the block — obstacle 2 of the
// E-19 row, answered structurally. Manager.PrefetchBlock's lockBlock latch is
// released by the engine's OnComplete when the read really finishes, so the
// latch is NOT held across FinishRead and a synchronous reader of the same
// block is serialised by the pool, not by the smgr.
//
// ------------------------------------------------------------------
// Invalidation, and why S3 does not need a new mechanism for it
// ------------------------------------------------------------------
//
// §4.1a's unnumbered finding says InvalidateRel/InvalidateBlock bail on
// !stateValid, so an IO-in-progress slot is skipped and its bufmap entry
// survives a concurrent truncate. True — but taking the caller's pin AT
// StartRead time (which findings 3 and 4 force anyway) collapses this into an
// existing, documented class: both invalidators also skip any slot with
// statePin > 0, so an in-flight prefetch slot is skipped for exactly the same
// reason an ordinary pinned buffer is, and the standing contract that a pinner
// must not outlive a truncation of its own relation covers it unchanged. No
// new exposure is created, so no new mechanism is added.
// TestInvalidateTreatsInFlightSlotAsPinned pins that reasoning.

// ErrReadInFlight is returned by StartRead when another backend already has a
// read of the same block in flight. The caller must NOT wait for it under any
// lock of its own; the correct response is to skip the block in the look-ahead
// window and let the ordinary Pin path park on the slot semaphore when the
// consumer actually reaches it. Declining is what keeps the window's cost
// bounded: waiting here would serialise the window behind another backend.
var ErrReadInFlight = errors.New("storage: a read of this block is already in flight")

// ReadOp is one outstanding installing prefetch: a pool slot that is PINNED
// but NOT YET VALID, with a disk read in flight into the slot's own page.
//
// This is the deviation from §4.1's sketched `StartRead(tag) (*Slot, bool,
// error)` signature, and it is a deliberate move TOWARDS upstream rather than
// away from it: PG's equivalent state lives in a ReadBuffersOperation
// (bufmgr.h), not in the buffer. Keeping the handle and the identity here
// rather than on the Slot also means an in-flight read adds no field that a
// concurrent Pin could observe, and no field the race detector has to reason
// about — the op is touched only by the goroutine that created it.
//
// The caller MUST call Finish or Abort exactly once. Both are idempotent
// against a second call so that a drain loop can be unconditional.
type ReadOp struct {
	pool *Pool
	slot *Slot
	tag  BufferTag
	h    AIOHandle
	idx  int32
	gen  uint32

	// hit is set when StartRead found the block already resident and valid.
	// There is no I/O to wait for; Finish is a no-op that returns the slot
	// still pinned. This is the alreadyValid half of §4.1's signature.
	hit bool

	// settled guards against a double Finish/Abort, which would drop a pin
	// that is no longer ours and corrupt another backend's accounting.
	settled bool
}

// Slot returns the pinned slot. Its contents are only valid after Finish
// returns nil.
func (op *ReadOp) Slot() *Slot { return op.slot }

// Tag returns the block this op is reading.
func (op *ReadOp) Tag() BufferTag { return op.tag }

// AlreadyValid reports whether StartRead found the block resident, so no I/O
// was issued. The slot is pinned either way.
func (op *ReadOp) AlreadyValid() bool { return op.hit }

// StartRead begins loading tag into a pool slot and returns a ReadOp holding a
// PINNED slot whose contents are not yet valid. The caller MUST call Finish or
// Abort on it exactly once.
//
// Unlike the deleted Pool.Prefetch, the read lands in the SLOT'S OWN PAGE —
// which is the entire point of E-19 and the exact thing the deleted function
// failed to do (it allocated a fresh 8 KiB buffer, submitted a real read into
// it, and dropped the buffer on return, so the following Pin re-read the block
// anyway).
//
// Returns ErrReadInFlight when another backend is already reading the block;
// see that error's contract. Returns ErrNoBuffer when the clock sweep cannot
// find a victim, which a look-ahead window must treat as "stop growing", not
// as a failure — the window is discretionary, the consumer's own Pin is not.
func (p *Pool) StartRead(tag BufferTag) (*ReadOp, error) {
	// Fast path: already resident and valid. Pin it and report a hit; the
	// window still owns the pin, so the accounting is identical either way.
	if s, ok := p.TryPin(tag); ok {
		p.sharedHitCount.Add(1)
		return &ReadOp{pool: p, slot: s, tag: tag, idx: s.idx, hit: true}, nil
	}

	op, err := p.startReadPrologue(tag)
	if err != nil || op == nil || op.hit {
		return op, err
	}

	// SUBMISSION HAPPENS HERE, WITH pinMu RELEASED. See the file header: this
	// is edge (a) of the deadlock cycle, and moving this one call is what
	// removes it. Do not fold this back into the prologue.
	h, serr := p.mgr.PrefetchBlock(tag.Rel, tag.Block, op.slot.page)
	if serr != nil {
		p.abortRead(op)
		return nil, serr
	}
	op.h = h
	return op, nil
}

// startReadPrologue runs the pinMu-protected half of StartRead: resolve the
// tag, claim and evict a victim, publish the tag with the IO bit set, and take
// the caller's pin. It returns with pinMu RELEASED and nothing submitted.
//
// A note on cost that §4.1a finding 6 is right about and this code does not
// fix: evictVictim performs a SYNCHRONOUS writeback for a dirty victim (a WAL
// flush plus a pwrite, inline on this goroutine). A window of depth D can
// therefore front-load up to D synchronous writebacks before a single read is
// in flight. Fixing that needs a background writer that does not exist; the
// consequence is that the window depth knob defaults to 0 and any arm that
// enables it must report the eviction delta beside the timing.
func (p *Pool) startReadPrologue(tag BufferTag) (*ReadOp, error) {
	p.pinMu.Lock()

	for {
		slotIdx, gen := p.bm.Lookup(tag)
		if slotIdx < 0 {
			break // genuine miss: fall through to the claim below
		}
		s := &p.slots[slotIdx]
		st := s.state.Load()
		if stateIO(st) {
			// Another backend's read is in flight. Decline rather than wait:
			// waiting here would hold the window's goroutine behind somebody
			// else's I/O, and re-reading would be worse still.
			p.pinMu.Unlock()
			return nil, ErrReadInFlight
		}
		if stateValid(st) && stateGen(st) == gen {
			if s2 := p.tryPinSlot(slotIdx, gen); s2 != nil {
				p.pinMu.Unlock()
				p.sharedHitCount.Add(1)
				return &ReadOp{pool: p, slot: s2, tag: tag, idx: s2.idx, hit: true}, nil
			}
		}
		// Transient state (just evicted, or a lost CAS): re-resolve.
	}

	victimIdx, wasDirty, oldTag, err := p.claimVictim()
	if err != nil {
		p.pinMu.Unlock()
		return nil, err
	}
	s := &p.slots[victimIdx]

	if err := p.evictVictim(victimIdx, wasDirty, oldTag); err != nil {
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, fmt.Errorf("flush victim: %w", err)
	}

	// evictVictim drops and retakes pinMu for a dirty flush, so re-check.
	if existingIdx, existingGen := p.bm.Lookup(tag); existingIdx >= 0 {
		if existing := p.tryPinSlot(existingIdx, existingGen); existing != nil {
			p.releaseVictimSlot(victimIdx)
			p.pinMu.Unlock()
			p.sharedHitCount.Add(1)
			return &ReadOp{pool: p, slot: existing, tag: tag, idx: existing.idx, hit: true}, nil
		}
	}

	gen := stateGen(s.state.Load())
	p.stashEvictedImageLSN(s)
	s.tag = tag
	s.nativeImageLSN.Store(p.takeEvictedImageLSN(tag))
	if !p.bmInsert(tag, int32(victimIdx), gen) {
		p.traceSlotEvent(int32(victimIdx), evReleaseVictimSlot, tag, s.state.Load(), 0)
		s.tag = BufferTag{}
		p.releaseVictimSlot(victimIdx)
		p.pinMu.Unlock()
		return nil, nil // caller may retry; a window simply skips the block
	}

	// Take the caller's pin NOW, while the IO bit is still set. Findings 3 and
	// 4 both land here: pinLoad's publish is an absolute Store with pin
	// hard-coded to 1, which would clobber a pin taken later, and Unpin panics
	// on pin 0, so "FinishRead or Unpin exactly once" is unimplementable
	// unless the pin exists from the start. It also matches upstream, where
	// StartReadBuffersImpl pins the whole run BEFORE submitting.
	// The encoding permits pin>0 alongside slotIOBit and every reader handles
	// it; it is only the publish path that had to change (see publishValid).
	for {
		old := s.state.Load()
		if s.state.CompareAndSwap(old, (old&^slotPinMask)|(statePin(old)+1)) {
			break
		}
	}

	p.pinMu.Unlock()
	return &ReadOp{pool: p, slot: s, tag: tag, idx: int32(victimIdx), gen: gen}, nil
}

// Finish blocks until the op's read completes, verifies it, publishes the slot
// as valid and returns it still pinned. Idempotent: a second call is a no-op.
//
// On any failure the slot is torn down and the pin released, so a caller that
// ignores the error still leaks nothing — but the *Slot must not be used.
func (op *ReadOp) Finish() error {
	if op == nil || op.settled {
		return nil
	}
	if op.hit {
		op.settled = true
		return nil
	}
	p := op.pool
	s := op.slot

	n, err := op.h.Wait()
	if err == nil {
		err = p.readFault(op.tag)
	}
	if err == nil && n != BlockSize {
		err = fmt.Errorf("prefetch %v: short read %d of %d", op.tag, n, BlockSize)
	}
	if err != nil {
		p.abortRead(op)
		return err
	}

	// Checksum verification is NOT skipped here and must never be. It happens
	// inside the read itself, one layer down, which is what makes it
	// unbypassable: with an engine attached, runOp dispatches to
	// op.File.ReadAt and relFile.ReadAt verifies under r.mu; the io_uring
	// raw-fd path takes it from aio.ChecksumFile (which the adapter did not
	// implement until commit cf8333ee — a live bug on the WRITE path that this
	// slice's review found); and with no engine attached PrefetchBlock falls
	// back to readBlock, which verifies. TestStartReadVerifiesChecksums holds
	// that line: it corrupts a page on disk and asserts Finish FAILS. A
	// prefetch path that silently dropped verification would be a correctness
	// regression no values suite could ever catch.

	s.contentMu.Lock()
	recordIOTrace(op.tag, "postRead", s.page)
	PageIdentityObserve(op.tag, s.page, "postRead")
	if p.OnBlockReload != nil {
		p.OnBlockReload(op.tag, s.page)
	}
	s.contentMu.Unlock()

	p.pinMu.Lock()
	// Read the waiter count under pinMu BEFORE clearing the IO bit, so no new
	// waiter can arrive between the read and the wake — the same ordering
	// pinLoad relies on.
	waiters := p.slotWaiters[op.idx].Load()
	p.publishValid(s, op.gen, 0)
	for i := int32(0); i < waiters; i++ {
		runtimeshim.SemaRelease(&p.slotSema[op.idx])
	}
	p.sharedReadCount.Add(1)
	p.pinMu.Unlock()

	op.settled = true
	return nil
}

// Abort discards the op without waiting for its read to be useful: the slot is
// unmapped, waiters are woken (they re-resolve and miss, then load normally)
// and the caller's pin is released. Idempotent.
//
// Abort still WAITS for the in-flight read, and must: the AIO engine is
// writing into the slot's page, and returning the slot to the free list while
// a worker is still filling it would hand a live write target to whichever
// backend claims the slot next. Upstream's answer to the same problem is the
// IO subsystem's own second pin (buffer_stage_common, bufmgr.c:6852-6853,
// released in TerminateBufferIO); goopg's AIO layer has no such pin, so the
// wait is the equivalent and it is not optional.
func (op *ReadOp) Abort() {
	if op == nil || op.settled {
		return
	}
	if op.hit {
		op.pool.Unpin(op.slot)
		op.settled = true
		return
	}
	op.pool.abortRead(op)
}

func (p *Pool) abortRead(op *ReadOp) {
	if op.h != nil {
		_, _ = op.h.Wait() // see Abort's contract: the page is a live write target
	}
	s := op.slot

	p.pinMu.Lock()
	p.bmDelete(op.tag, op.idx)
	p.tombstones.Add(1)
	s.tag = BufferTag{}
	// Clear IO and valid but KEEP the pin count. This deliberately does NOT
	// call releaseVictimSlot: that does state.Store(0), wiping the generation
	// out from under a *Slot the caller is still holding, so the slot becomes
	// claimable for a different tag while the caller believes it owns it, and
	// the caller's later Unpin decrements somebody else's pin (§4.1a finding
	// 5 — and note a pin-BALANCE test passes while this is broken: the counts
	// balance, the ownership does not). Keeping the pin means claimVictim
	// cannot touch the slot until the caller lets go.
	for {
		old := s.state.Load()
		newSt := (uint64(op.gen) << slotGenShift) | statePin(old)
		if s.state.CompareAndSwap(old, newSt) {
			p.traceSlotEvent(op.idx, evReleaseVictimSlot, op.tag, old, newSt)
			break
		}
	}
	waiters := p.slotWaiters[op.idx].Load()
	for i := int32(0); i < waiters; i++ {
		runtimeshim.SemaRelease(&p.slotSema[op.idx])
	}
	p.pinMu.Unlock()

	// Drop our pin last, outside pinMu. The slot now has pin 0, valid clear
	// and usage 0, so the next clock sweep takes it as a free slot with an
	// empty oldTag — no eviction is counted for a page that never landed.
	p.Unpin(s)
	op.settled = true
}

// publishValid marks a slot valid for generation gen, clearing the IO and
// dirty bits and PRESERVING the current pin count plus extraPin.
//
// §4.1a finding 3: pinLoad's publish was an absolute Store with the pin
// hard-coded to 1
// (slotValidBit | 1 | (1<<slotUsageShift) | gen<<slotGenShift), which is
// correct only because nothing can hold a pin on a slot between claimVictim
// and publish under the synchronous path. StartRead breaks that premise by
// design — the caller's pin is taken at claim time — so the publish becomes a
// CAS that merges rather than a Store that overwrites. Both callers use this
// one function so the two paths cannot drift: pinLoad passes extraPin=1 (it
// publishes and pins in one step), Finish passes 0 (it already holds its pin).
func (p *Pool) publishValid(s *Slot, gen uint32, extraPin uint64) {
	for {
		old := s.state.Load()
		newSt := slotValidBit |
			(statePin(old) + extraPin) |
			(uint64(1) << slotUsageShift) |
			(uint64(gen) << slotGenShift)
		if s.state.CompareAndSwap(old, newSt) {
			p.traceSlotEvent(s.idx, evPinLoadPublish, s.tag, old, newSt)
			return
		}
	}
}
