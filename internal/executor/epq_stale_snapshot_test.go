package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// pinBlockZero pins block 0 of rel, extending the relation first when it does
// not exist yet (Pin alone reports "short read at block" on an empty fork).
func pinBlockZero(t *testing.T, ctx *Context, rel storage.RelFileNode) (*storage.Slot, error) {
	t.Helper()
	if n, err := ctx.Pool.NBlocks(rel); err != nil || n == 0 {
		if _, err := ctx.Pool.ExtendRelationBatch(rel, 1); err != nil {
			return nil, err
		}
	}
	return ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
}

// TestEPQChainTailLiveButUnseen is the M0131-S32.3 regression guard.
//
// The defect: the EvalPlanQual chain-follow helpers decide which version of a
// row is "the latest" by SNAPSHOT visibility (epqFollowHOT passes ctx.Snap to
// followHOTChain; epqFollowChainFull calls mvcc.TupleVisible at the chain tail).
// Under contention the winning writer routinely commits in the window BETWEEN
// the EPQ loop's snapshot refresh and its chain walk, so its version is live on
// the page but outside our snapshot. Chain-follow then reports not-found,
// epqChainPendingWriter correctly reports nobody in flight, and the caller took
// the silent skip — dropping this statement's write while still reporting
// `UPDATE 1`. Measured as 6 lost increments in 42487 pgbench transactions over 5
// hot rows, matching the epq_chain_notfound probe count exactly; it was the last
// residue of the M0131-S32 series (docs/design/0131-0026 §8).
//
// epqChainTailLiveButUnseen is what tells the caller to refresh and re-lap
// instead. This test pins BOTH halves of its contract, because the false
// direction is what keeps the retry from livelocking:
//
//	live tail our snapshot cannot see yet  -> true   (retry)
//	same tail once the snapshot sees it    -> false  (the next lap terminates)
//	tail deleted by a committed xmax       -> false  (skipping is correct)
//	tail written by an in-flight xmin      -> false  (epqChainPendingWriter's case)
func TestEPQChainTailLiveButUnseen(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	rel := storage.RelFileNode{TblOid: 1663, DBOid: 5, RelOid: 90001}

	// Build a two-version chain on block 0: slot 1 (created by xid 100, updated
	// away by xid 200) -> slot 2 (created by xid 200, self-pointing tail).
	// Neither xid is known to the transaction manager, so epqXmaxSettled
	// classifies both as committed — the state the defect needs.
	const (
		creatorXID = storage.TransactionID(100)
		updaterXID = storage.TransactionID(200)
		deleterXID = storage.TransactionID(400)
	)
	buildChain := func(tailXmax storage.TransactionID) {
		s, err := pinBlockZero(t, ctx, rel)
		if err != nil {
			t.Fatalf("Pin: %v", err)
		}
		s.Lock()
		if err := storage.InitPage(s.Page()); err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			t.Fatalf("InitPage: %v", err)
		}
		old := storage.NewHeapTuple(creatorXID, updaterXID, []byte("old"))
		old.Header.SetHotUpdated()
		old.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
		if slot, err := storage.PageAddHeapTuple(s.Page(), old); err != nil || slot != 1 {
			s.Unlock()
			ctx.Pool.Unpin(s)
			t.Fatalf("add old: slot=%d err=%v", slot, err)
		}
		tail := storage.NewHeapTuple(updaterXID, tailXmax, []byte("new"))
		tail.Header.SetHeapOnly()
		tail.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2} // self-pointing tail
		if slot, err := storage.PageAddHeapTuple(s.Page(), tail); err != nil || slot != 2 {
			s.Unlock()
			ctx.Pool.Unpin(s)
			t.Fatalf("add tail: slot=%d err=%v", slot, err)
		}
		s.Unlock()
		ctx.Pool.Unpin(s)
	}

	// --- the defect's exact state: tail committed after our snapshot ---------
	buildChain(storage.InvalidTransactionID)
	ctx.Snap = mvcc.Snapshot{Xmin: 150, Xmax: 150}
	if !epqChainTailLiveButUnseen(ctx, rel, 0, 1) {
		t.Error("live tail outside the snapshot: got false, want true (the EPQ lap must retry, not drop the write)")
	}

	// --- anti-livelock: once the snapshot sees it, stop retrying -------------
	ctx.Snap = mvcc.Snapshot{Xmin: 300, Xmax: 300}
	if epqChainTailLiveButUnseen(ctx, rel, 0, 1) {
		t.Error("visible tail: got true, want false — retrying here would livelock instead of skipping a failed WHERE")
	}

	// --- a genuinely deleted row must still be skipped -----------------------
	buildChain(deleterXID)
	ctx.Snap = mvcc.Snapshot{Xmin: 150, Xmax: 150}
	if epqChainTailLiveButUnseen(ctx, rel, 0, 1) {
		t.Error("tail deleted by a committed xmax: got true, want false")
	}

	// --- an in-flight writer belongs to epqChainPendingWriter, not here ------
	inflight, err := ctx.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = ctx.TxnMgr.Rollback(inflight) }()
	// XIDs are assigned lazily at first write; without this the tuples below
	// would carry InvalidTransactionID and both classifiers would bail early
	// for the wrong reason, making the assertions pass vacuously.
	inflightXID, err := ctx.TxnMgr.AssignXID(inflight)
	if err != nil {
		t.Fatalf("AssignXID: %v", err)
	}
	if inflightXID == storage.InvalidTransactionID {
		t.Fatal("AssignXID returned InvalidTransactionID")
	}
	s, err := pinBlockZero(t, ctx, rel)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	s.Lock()
	if err := storage.InitPage(s.Page()); err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		t.Fatalf("InitPage: %v", err)
	}
	old := storage.NewHeapTuple(creatorXID, inflightXID, []byte("old"))
	old.Header.SetHotUpdated()
	old.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
	if _, err := storage.PageAddHeapTuple(s.Page(), old); err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		t.Fatalf("add old: %v", err)
	}
	tail := storage.NewHeapTuple(inflightXID, storage.InvalidTransactionID, []byte("new"))
	tail.Header.SetHeapOnly()
	tail.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
	if _, err := storage.PageAddHeapTuple(s.Page(), tail); err != nil {
		s.Unlock()
		ctx.Pool.Unpin(s)
		t.Fatalf("add tail: %v", err)
	}
	s.Unlock()
	ctx.Pool.Unpin(s)
	if epqChainTailLiveButUnseen(ctx, rel, 0, 1) {
		t.Error("tail written by an in-flight xmin: got true, want false (epqChainPendingWriter must own that case, so the caller WAITS)")
	}
	if _, pending := epqChainPendingWriter(ctx, rel, 0, 1); !pending {
		t.Error("epqChainPendingWriter did not claim the in-flight tail — the two classifiers must partition the not-found cases between them")
	}
}
