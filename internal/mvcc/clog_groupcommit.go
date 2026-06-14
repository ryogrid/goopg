package mvcc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/goopg/goopg/internal/storage"
)

// Group commit for the commit log (M0117-0005, gap G7).
//
// A faithful Go port of PostgreSQL's group XID status update optimisation
// (postgres/src/backend/access/transam/clog.c:TransactionGroupUpdateXidStatus,
// lines 441-653). Concurrent committers do not each acquire the durable-write
// lock and issue their own fsync; instead they enqueue onto a lock-free Treiber
// stack (the head pointer ≙ ProcGlobal->clogGroupFirst). The first arrival is
// the leader: it takes the durable-write lock once, atomically swaps the whole
// stack out (ABA-free, ≙ pg_atomic_exchange), applies every queued update in a
// single batched flush (one incremental flat-page pass + one fsync per touched
// SLRU segment), drops the lock, then wakes the followers.
//
// Unlike PG — which restricts a group to a single SLRU page so the leader holds
// exactly one bank lock — goopg serialises ALL durable writes on a single
// flushMu, so the leader can batch updates spanning any pages/segments. That is
// strictly more batching than PG's same-page group, never less.

// clogGroupNode is one queued status update on the group-commit stack. It is
// the goopg analogue of the clogGroupMember* fields PG stores in PGPROC.
type clogGroupNode struct {
	xid    storage.TransactionID
	status TxnStatus
	// next links to the node pushed before this one (≙ PGPROC.clogGroupNext).
	next *clogGroupNode
	// done delivers the batch's flush result to a follower; the leader sends on
	// every follower's done after releasing flushMu (≙ waking the proc
	// semaphore once clogGroupMember clears). Buffered so the leader never
	// blocks on a follower that has not yet reached its receive.
	done chan error
}

// groupUpdate performs the durable part of a status change (incremental flat
// page + SLRU fsync) through the group-commit layer. The caller has already set
// the in-memory bank byte and recorded the dirty flat page. Mirrors
// TransactionGroupUpdateXidStatus.
func (c *CLog) groupUpdate(xid storage.TransactionID, status TxnStatus) error {
	node := &clogGroupNode{xid: xid, status: status, done: make(chan error, 1)}

	// Push onto the Treiber stack (≙ the CAS loop writing MyProcNumber into
	// ProcGlobal->clogGroupFirst). We never bail out to a "normal path" the way
	// PG does on a page mismatch — flushMu lets the leader batch across pages.
	for {
		old := c.groupHead.Load()
		node.next = old
		if c.groupHead.CompareAndSwap(old, node) {
			if old == nil {
				// The stack was empty ⇒ we are the leader.
				return c.runLeader(node)
			}
			// A leader is already forming/processing a group ⇒ we are a
			// follower; wait for it to flush our update (≙ sleeping on the proc
			// semaphore until clogGroupMember clears).
			return <-node.done
		}
		// Lost the CAS race; reload and retry. If the leader Swap'd the stack to
		// nil between our Load and CAS, the next iteration observes nil and we
		// become a leader ourselves.
	}
}

// runLeader is executed by the node that found the stack empty. It drains the
// whole group and applies it under flushMu, then wakes the followers. Mirrors
// the leader half of TransactionGroupUpdateXidStatus (lines 554-652).
func (c *CLog) runLeader(self *clogGroupNode) error {
	c.flushMu.Lock()

	// Close the group out by swapping the head to nil in one atomic op (≙
	// pg_atomic_exchange_u32(&clogGroupFirst, INVALID) — popping one at a time
	// would expose an ABA problem). Any committer arriving after this forms a
	// new group.
	head := c.groupHead.Swap(nil)

	// Walk the list into a slice (self is always the tail — its push saw an
	// empty stack — so the traversal includes it).
	var batch []*clogGroupNode
	for n := head; n != nil; n = n.next {
		batch = append(batch, n)
	}

	err := c.applyGroupBatchLocked(batch)

	c.flushMu.Unlock()

	// Wake followers AFTER dropping the lock (≙ PG signalling the semaphores
	// outside the bank lock, lines 627-650), keeping lock hold time minimal.
	for _, n := range batch {
		if n != self {
			n.done <- err
		}
	}
	return err
}

// applyGroupBatchLocked performs the two batched durable writes for a group.
// The caller holds flushMu. M0117-0005.
func (c *CLog) applyGroupBatchLocked(batch []*clogGroupNode) error {
	// 1. Flat file: replay only the dirty pages (the batch's setStatus calls
	//    already recorded them via markFlatDirty).
	if err := c.flushDirtyPagesLocked(); err != nil {
		return err
	}
	// 2. SLRU mirror: one fsync per touched segment instead of one per XID.
	return c.mirrorGroupToSLRULocked(batch)
}

// mirrorGroupToSLRULocked writes the 2-bit SLRU lanes for every XID in the
// batch, grouping by pg_xact/ segment so each segment file is opened, OR-updated
// and fsynced exactly once (vs the per-XID fsync of mirrorToSLRUUnlocked). OR
// lane semantics mirror PG's TransactionIdSetStatusBit (a lane only ever
// advances toward a terminal state). No-op when the SLRU mirror is disabled.
// The caller holds flushMu.
func (c *CLog) mirrorGroupToSLRULocked(batch []*clogGroupNode) error {
	c.banksMu.RLock()
	dir := c.slruDir
	c.banksMu.RUnlock()
	if dir == "" {
		return nil
	}

	// Bucket the batch's lane bits by segment. PG's TransactionLogFetch
	// short-circuits XIDs below FirstNormalTransactionID as committed without
	// touching the SLRU; we mirror that so basebackup byte-equality holds.
	type segUpdate struct {
		segNo uint64
		bits  map[int64]byte // byteOffset within segment → OR-mask
	}
	segs := make(map[uint64]*segUpdate)
	for _, n := range batch {
		if n.xid < FirstNormalTransactionID {
			continue
		}
		var bits byte
		switch n.status {
		case TxnStatusCommitted:
			bits = pgClogStatusCommitted
		case TxnStatusAborted:
			bits = pgClogStatusAborted
		case TxnStatusSubCommitted:
			bits = pgClogStatusSubCommitted
		default:
			continue
		}
		segNo := uint64(n.xid) / clogXactsPerSegment
		xidInSeg := uint64(n.xid) % clogXactsPerSegment
		pageInSeg := xidInSeg / clogXactsPerPage
		xidInPage := xidInSeg % clogXactsPerPage
		byteOffset := int64(pageInSeg)*int64(storage.BlockSize) + int64(xidInPage/clogXactsPerByte)
		bShift := uint((xidInPage % clogXactsPerByte) * clogBitsPerXact)

		su := segs[segNo]
		if su == nil {
			su = &segUpdate{segNo: segNo, bits: make(map[int64]byte)}
			segs[segNo] = su
		}
		su.bits[byteOffset] |= bits << bShift
	}
	if len(segs) == 0 {
		return nil
	}

	// Apply segments in ascending order for deterministic I/O.
	order := make([]uint64, 0, len(segs))
	for s := range segs {
		order = append(order, s)
	}
	slices.Sort(order)

	for _, segNo := range order {
		su := segs[segNo]
		if err := c.applySegmentLanesLocked(dir, su.segNo, su.bits); err != nil {
			return err
		}
	}
	return nil
}

// applySegmentLanesLocked opens one pg_xact/ segment file, ORs the given
// byteOffset→mask updates into it (extending in BLCKSZ-page units so a reader
// always sees complete pages), and fsyncs once. Caller holds flushMu.
func (c *CLog) applySegmentLanesLocked(dir string, segNo uint64, masks map[int64]byte) error {
	if len(masks) == 0 {
		return nil
	}
	// Highest byte offset we touch determines the minimum page-aligned size.
	var maxOff int64 = -1
	for off := range masks {
		if off > maxOff {
			maxOff = off
		}
	}
	maxPage := maxOff / int64(storage.BlockSize)
	minSize := (maxPage + 1) * int64(storage.BlockSize)

	segPath := filepath.Join(dir, fmt.Sprintf("%04X", segNo))
	f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("clog slru: open %q: %w", segPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("clog slru: stat %q: %w", segPath, err)
	}
	bufSize := minSize
	if fi.Size() > bufSize {
		bufSize = fi.Size()
	}
	buf := make([]byte, bufSize)
	if fi.Size() > 0 {
		if _, err := f.ReadAt(buf[:fi.Size()], 0); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("clog slru: read %q: %w", segPath, err)
		}
	}
	for off, mask := range masks {
		buf[off] |= mask
	}
	if _, err := f.WriteAt(buf, 0); err != nil {
		return fmt.Errorf("clog slru: write %q: %w", segPath, err)
	}
	return f.Sync()
}
