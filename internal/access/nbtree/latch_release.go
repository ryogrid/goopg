package nbtree

import "github.com/goopg/goopg/internal/storage"

// Panic-safe page-latch release — goopg's narrow equivalent of PostgreSQL's
// LWLockReleaseAll().
//
// WHY THIS EXISTS (M-NIGHTLY regress/suite-wedge root cause, found 2026-08-06;
// design doc docs/design/root-0040-btree-stranded-latch-release.md).
//
// A mutation site takes a page's exclusive content latch through pinW and
// releases it with an explicit unpinW call — not a `defer`, because the split
// path hands latches between frames. That is fine while control leaves through
// a `return`. It is NOT fine when the stack unwinds through a PANIC:
// internal/server's per-connection handler recovers every backend panic
// (server.go:~799) so one bad statement cannot kill the postmaster, which means
// the goroutine holding the latch simply disappears and sync.RWMutex has no
// owner left to release it.
//
// The observed failure: insertItemSorted panicked ("storage: not enough free
// space in page") inside insertIntoBlock's leaf-write window, one frame below
// its pinW — regress suite, 2026-08-06 22:14:14. The panic is in that server's
// log and its checkpointer goroutine was still blocked on that very slot's
// RLock ten minutes later. The consequences are exactly the wedge signature:
//
//  1. every later statement touching that page blocks on contentMu FOREVER —
//     a mutex wait observes no statement_timeout, which is why a regress case
//     sailed past its own 5 s deadline and died on the harness's 120 s one;
//  2. the checkpointer's FlushAll wants the same latch shared, so the shutdown
//     checkpoint never completes and the server ignores SIGTERM — that is the
//     orphaned-test-server crawl, observed on this workstation the same night;
//  3. the wedge MOVES between runs, because which case wedges depends only on
//     who next touches the poisoned page.
//
// PostgreSQL never has this problem: elog(ERROR) longjmps to the abort path and
// AbortTransaction() calls LWLockReleaseAll()
// (postgres/src/backend/storage/lmgr/lwlock.c,
// postgres/src/backend/access/transam/xact.c), dropping every content lock the
// backend held.
//
// WHY A HOLDER AND NOT A REGISTRY: an earlier cut of this fix kept the held
// latches on the *BTree and swept them at each public entry point. That is
// unsound — a *BTree instance is NOT goroutine-private (TestConcurrentSearch-
// AfterInserts runs concurrent Searches through one instance), so one
// goroutine's sweep releases another's latches. wlatch is a plain local
// variable instead: private to the frame that owns the latch, with no
// cross-goroutine state at all.
//
// The panic is deliberately NOT recovered: it keeps propagating to the
// connection handler with its original stack, so a genuine bug stays loud. This
// only stops one statement's bug from poisoning the whole cluster.
//
// SCOPE: this covers insertIntoBlock, the path the wedge was observed on. The
// remaining unprotected windows — the descent's shared latches, the split
// path's rightSlot/sibSlot, and the non-btree Slot.Lock() sites in
// internal/executor (sys_catalog_*, toast, sequences) — are recorded in
// .ralph/deferral_ledger.md; a general per-backend release-all needs latch
// ownership plumbed through Context, which is a milestone of its own.

// wlatch owns one exclusive page latch for the duration of a frame. release()
// is idempotent, so a function can call it on its normal exit paths AND defer
// it for the panic path without double-releasing.
type wlatch struct {
	bt *BTree
	s  *storage.Slot
}

// hold takes ownership of a freshly pinned+latched slot (from pinW).
func (w *wlatch) hold(s *storage.Slot) { w.s = s }

// release drops the latch if this holder still owns it. Safe to call any
// number of times, including on a slot that was never held.
func (w *wlatch) release() {
	if w.s == nil {
		return
	}
	s := w.s
	w.s = nil
	w.bt.unpinW(s)
}
