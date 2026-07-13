package mvcc

import (
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

	err := c.applyGroupBatchLocked()

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

// applyGroupBatchLocked performs the group's durable write. Every batch
// member's lane was already written by setStatus before the group formed, so
// this needs no per-member data — it just flushes the pool's dirty pages. The
// caller holds flushMu. M0117-0005; M0117-0006 Part C removed the legacy
// flat-file + bank→SLRU mirror alternative once the buffer pool became the
// only store.
func (c *CLog) applyGroupBatchLocked() error {
	// C2-S3 (the cut): the eager durable write-back is gone — PG performs
	// ZERO SLRU I/O at commit (clog.c TransactionIdSetPageStatus sets bits
	// in the shared buffer under the bank lock only). Each batch member's
	// 2-bit lane was already written to the resident page by setStatus
	// before the group formed; durability now rides exclusively on
	//   (a) the checkpointer's FlushCLOGFn (CLog.FlushAll, error fails the
	//       checkpoint — pg_xact on disk covers everything before redo),
	//   (b) pool eviction's writePageToDisk (barrier-protected: the page's
	//       group LSNs — armed for sync commits since C2-S2 — force a WAL
	//       FlushUpTo before the bytes reach disk), and
	//   (c) startup replayCLogFromWAL re-stamping from WAL commit records
	//       (pinned by TestReplayCLogFromWAL_RecoversUnflushedSyncCommit).
	// The now-vestigial group machinery itself is removed in C2-S4.
	return nil
}
