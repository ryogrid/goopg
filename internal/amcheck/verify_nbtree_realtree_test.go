package amcheck_test

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/storage"
)

// The B-tree verification engine (verify_nbtree.go, heapallindexed*.go) was
// built and unit-tested entirely against HAND-CONSTRUCTED pages — makeItemsPage,
// makeLinkedPage, makeInternalPage, makeLeafPage and friends poke bytes directly
// because the only way to exhibit corruption is to write it by hand. That proves
// the *detection* paths fire, but it leaves the complementary risk uncovered: a
// hand-built "clean" page only models what the test author believed goopg writes.
// If the engine's decode assumptions (metapage magic/version, opaque level/flag
// layout, the inline (keyLen|block|offset|key) item format, posting-list framing,
// the high key in the opaque area, downlink child blocks, sibling links across a
// split) diverge from what the REAL btree.Create/Insert/split machinery actually
// lays on disk, the eventual verify_heapam()/bt_index_check() SQL surface would
// either report corruption on perfectly healthy user indexes, or stay silent on
// genuinely corrupt ones — the most expensive class of compatibility bug in this
// project (cf. the heap-side analogue verify_heapam_realpage_test.go, which
// surfaced a real VacuumHeapPageBySlots MAXALIGN bug exactly this way).
//
// These tests drive the REAL producer — they build live B-trees by inserting
// enough keys to force leaf splits, internal-page creation and a multi-level
// tree — then run every tier of the verification engine over the on-disk pages.
//
// They split into two outcomes by insertion order, which is itself a finding:
//
//   - Sorted (append-only) insertion only ever splits the RIGHTMOST page of a
//     level, which has no right sibling, so every tier — including the
//     cross-page sibling-link walk — must stay SILENT.
//
//   - Shuffled insertion forces splits in the MIDDLE of a level. goopg's split
//     (btree.go:1454-1466 / 1522) links the new right page's btpo_prev to the
//     left page and sets the left page's btpo_next to the new page, but it never
//     updates the OLD right sibling's btpo_prev to point at the new page. The
//     old right sibling's left-link is therefore left STALE after any non-rightmost
//     split. The sibling-link tier correctly flags this ("left link/right link
//     pair ... not in agreement"); every other tier (which does not depend on
//     btpo_prev) stays silent. This is a genuine goopg btree correctness gap —
//     btpo_prev is load-bearing (btree_vacuum.go reads op.Prev and WAL-logs
//     RightSibNewPrev to relink siblings during page deletion), so a stale
//     left-link can mislead page-deletion relinking and any backward navigation.
//     Tracked for fix as the split right-sibling prev-link maintenance task; when
//     that lands, TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree must flip
//     to a silence assertion (see its trailing comment).

// realPageSource returns a PageSource that reads the current (buffer-pool-visible,
// including not-yet-flushed dirty) bytes of each block of rel, copied out so the
// engine never aliases a live buffer.
func realPageSource(t *testing.T, pool *storage.Pool, rel storage.RelFileNode) amcheck.PageSource {
	t.Helper()
	return func(blk storage.BlockNumber) (storage.Page, error) {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, err
		}
		defer pool.Unpin(slot)
		src := slot.Page()
		cp := make(storage.Page, len(src))
		copy(cp, src)
		return cp, nil
	}
}

// buildRealTree creates a fresh B-tree, inserts keys[i] -> a distinct heap TID,
// and returns the manager, pool, rel and a cleanup. Distinct, monotonically
// derived TIDs let the heapallindexed round-trip distinguish entries.
func buildRealTree(t *testing.T, keys [][]byte) (*storage.Manager, *storage.Pool, storage.RelFileNode, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}
	bt, err := btree.Create(pool, rel)
	if err != nil {
		_ = pool.Close()
		_ = mgr.Close()
		t.Fatalf("btree.Create: %v", err)
	}
	for i, k := range keys {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
		if err := bt.Insert(k, ptr); err != nil {
			_ = pool.Close()
			_ = mgr.Close()
			t.Fatalf("Insert(#%d): %v", i, err)
		}
	}
	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	return mgr, pool, rel, cleanup
}

// leftmostByLevel descends root -> leftmost child at each level via the
// negative-infinity (slot-1) downlink, returning the leftmost block of every
// level top-down. It is the starting point the sibling-link walk needs per level.
func leftmostByLevel(t *testing.T, src amcheck.PageSource, root storage.BlockNumber) []storage.BlockNumber {
	t.Helper()
	var out []storage.BlockNumber
	for blk := root; ; {
		p, err := src(blk)
		if err != nil {
			t.Fatalf("descend read block %d: %v", blk, err)
		}
		out = append(out, blk)
		op := btree.ParseOpaque(p)
		if op.IsLeaf() {
			return out
		}
		dls, err := btree.PageDownlinks(p)
		if err != nil {
			t.Fatalf("PageDownlinks(blk %d): %v", blk, err)
		}
		if len(dls) == 0 {
			t.Fatalf("internal block %d has no downlinks", blk)
		}
		blk = dls[0].Child // leftmost (negative-infinity) downlink
	}
}

// assertNonSiblingTiersSilent runs every B-tree engine tier EXCEPT the cross-page
// sibling-link walk over a real on-disk index and asserts zero findings. None of
// these tiers reads btpo_prev, so they must be clean regardless of insertion
// order. It returns the per-level leftmost blocks so the caller can drive the
// sibling-link tier separately (silent for sorted trees, a detection for shuffled
// trees). wantLeaves pins the collected leaf-entry count (one per inserted key —
// proves the level walk reached them all, not merely that nothing was flagged).
func assertNonSiblingTiersSilent(t *testing.T, mgr *storage.Manager, pool *storage.Pool, rel storage.RelFileNode, wantLeaves int) []storage.BlockNumber {
	t.Helper()
	const name = "ix_real"
	const tbl = "tbl_real"
	src := realPageSource(t, pool, rel)

	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	if nblocks < 2 {
		t.Fatalf("expected a split tree (>=2 blocks incl. metapage), got %d", nblocks)
	}

	// Per-page tiers over every block, including the metapage (block 0).
	for blk := range nblocks {
		p, err := src(blk)
		if err != nil {
			t.Fatalf("read block %d: %v", blk, err)
		}
		if r := amcheck.VerifyBtreePage(p, blk, name); r != nil {
			t.Fatalf("VerifyBtreePage(blk %d) false positive: %+v", blk, r)
		}
		if r := amcheck.VerifyBtreeItemOrder(p, blk, name); r != nil {
			t.Fatalf("VerifyBtreeItemOrder(blk %d) false positive: %+v", blk, r)
		}
	}

	// Cross-level downlink tier on every internal (non-leaf, live) page.
	for blk := range nblocks {
		if blk == btree.MetaBlock {
			continue
		}
		p, err := src(blk)
		if err != nil {
			t.Fatalf("read block %d: %v", blk, err)
		}
		op := btree.ParseOpaque(p)
		if op.IsLeaf() || op.IsDeleted() {
			continue
		}
		if r := amcheck.VerifyBtreeParentDownlinks(src, blk, name); r != nil {
			t.Fatalf("VerifyBtreeParentDownlinks(internal blk %d) false positive: %+v", blk, r)
		}
	}

	// heapallindexed round-trip: the leaf entries collected by the engine's own
	// relation walk, fed back as both the index set and the heap set, must report
	// zero "lacks matching index tuple" findings — every entry fingerprints and
	// probes to the same value through fingerprintLeafEntry (sibling-path
	// invariant on real leaf bytes).
	leaves, err := amcheck.CollectBtreeLeafEntries(src)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(leaves) != wantLeaves {
		t.Fatalf("leaf walk collected %d entries, want %d (level walk missed entries)", len(leaves), wantLeaves)
	}
	const seed = 0x9e3779b97f4a7c15
	if r := amcheck.VerifyBtreeHeapAllIndexed(leaves, leaves, name, tbl, seed); r != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexed round-trip false positive: %+v", r)
	}
	if rr, err := amcheck.VerifyBtreeHeapAllIndexedRelation(src, leaves, name, tbl, seed); err != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation: %v", err)
	} else if rr != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation false positive: %+v", rr)
	}

	meta := func() btree.BTreeMeta {
		p, err := src(btree.MetaBlock)
		if err != nil {
			t.Fatalf("read metapage: %v", err)
		}
		return btree.ParseMeta(p)
	}()
	return leftmostByLevel(t, src, meta.Root)
}

func TestVerifyBtreeEngineSilentOnRealInt4Sequential(t *testing.T) {
	const n = 3000 // forces multiple leaf splits and at least one internal level
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt4(int32(i))
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		// Append-only inserts split only the rightmost page (no old right
		// sibling), so the sibling-link tier must be silent here too.
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on sorted tree: %+v", lm, r)
		}
	}
}

func TestVerifyBtreeEngineSilentOnRealVarchar(t *testing.T) {
	// Variable-length keys put a real, non-trivial separator in each page's
	// opaque high-key area — the path the fixed-width int cases barely exercise
	// (the high-key invariant tier reads BTPageOpaque.HighKey directly). Sorted
	// order keeps splits rightmost, so all tiers (incl. sibling links) stay silent.
	const n = 2500
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeVarchar(fmt.Appendf(nil, "user-%010d-row", i))
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on sorted varchar tree: %+v", lm, r)
		}
	}
}

func TestVerifyBtreeEngineSilentOnRealInt8(t *testing.T) {
	const n = 2500
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = btree.EncodeInt8(int64(i) * 1_000_003)
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)
	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on sorted int8 tree: %+v", lm, r)
		}
	}
}

// TestVerifyBtreeEngineSilentOnRealShuffledInt4 builds a B-tree with SHUFFLED
// inserts (forcing MIDDLE-of-level splits, not just rightmost ones) and asserts
// every tier — including the cross-page sibling-link walk — stays silent.
//
// This test was previously a DETECTION assertion: the real-producer harness
// uncovered that goopg's splitAndInsert never updated the OLD right sibling's
// btpo_prev on a non-rightmost split (it set the new right page's prev and the
// left page's next, but the page that used to be left's right sibling kept its
// btpo_prev pointing at the left block — a stale left-link on any non-append
// insert pattern). btpo_prev is load-bearing (btree_vacuum.go reads op.Prev and
// WAL-logs RightSibNewPrev to relink siblings on page deletion), so the stale
// link was a genuine on-disk correctness gap.
//
// M0110-0007 fixed the split path to relink the old right sibling's btpo_prev to
// the new right page, atomically with the split (third page in the
// BtreeSplit WAL record; mirrors PostgreSQL _bt_split). The shuffled tree is now
// internally consistent, so the sibling-link tier — which a healthy PG tree
// never trips — must stay silent here, exactly as it does for the sorted cases.
func TestVerifyBtreeEngineSilentOnRealShuffledInt4(t *testing.T) {
	const n = 3000
	rng := rand.New(rand.NewSource(20260614))
	perm := rng.Perm(n)
	keys := make([][]byte, n)
	for i, v := range perm {
		keys[i] = btree.EncodeInt4(int32(v))
	}
	mgr, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()
	src := realPageSource(t, pool, rel)

	for _, lm := range assertNonSiblingTiersSilent(t, mgr, pool, rel, n) {
		// Shuffled inserts split middle pages, so each split must relink the
		// old right sibling's btpo_prev — the sibling-link tier proves it did.
		if r := amcheck.VerifyBtreeLevelSiblingLinks(src, lm, "ix_real"); r != nil {
			t.Fatalf("VerifyBtreeLevelSiblingLinks(leftmost %d) false positive on "+
				"shuffled tree — a stale old-right-sibling prev-link survived a "+
				"middle-of-level split (M0110-0007 regression?): %+v", lm, r)
		}
	}
}

// buildRealTreeConcurrent creates a fresh B-tree and inserts n keys drawn from a
// SHARED, narrow key range across `writers` concurrent goroutines calling
// bt.Insert directly. Unlike TestMultiWriterStress_M0055_Phase_C
// (multi_writer_stress_test.go), whose writers use disjoint key ranges spread
// across the whole uint64 keyspace (wid<<32|i) and therefore almost never split
// the same internal page at the same time, every goroutine here contends on the
// SAME narrow region of the tree — the shape pgbench's uniformly-random-aid
// UPDATE workload produces on pgbench_accounts_pkey (concurrent non-HOT-update
// re-inserts colliding/adjacent keys, goopg has no HOT update). poolSlots is
// deliberately caller-controlled: a small pool forces heavy eviction churn,
// which is load-bearing for the repro this harness feeds (see
// TestVerifyBtreeEngineSilentOnRealConcurrentContended below) — a large pool
// (no eviction pressure) does not reproduce it.
func buildRealTreeConcurrent(t *testing.T, n, writers, poolSlots int, keyRange int32, seed int64) (*storage.Manager, *storage.Pool, storage.RelFileNode, int, []btree.LeafEntry, *btree.BTree, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: poolSlots})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// M-NIGHTLY investigation aid (AI-20260708-064334-001): catch a
	// "clean" eviction that's about to discard unflushed content
	// red-handed, with byte-level detail, instead of only detecting the
	// resulting loss after the fact via the leaf-entry diff below.
	pool.DebugValidateCleanEvictions = true
	pool.DebugTraceSlotEvents = true
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9200, Fork: storage.MainFork}
	bt, err := btree.Create(pool, rel)
	if err != nil {
		_ = pool.Close()
		_ = mgr.Close()
		t.Fatalf("btree.Create: %v", err)
	}
	// M-NIGHTLY investigation aid (AI-20260708-064334-001, 5th loop): record
	// every insertItemSorted call (block+slot a key actually landed at) so a
	// lost entry can be cross-referenced against its own write history, not
	// just its absence from the final leaf walk.
	bt.DebugTraceInserts = true
	// M-NIGHTLY investigation aid (AI-20260708-064334-001, 7th loop): the
	// 6th loop's RewriteLogEvent instrumentation proved the loss is NOT in
	// the split/dedup-rewrite path -- some missing entries are touched
	// only by plain fast-path insertItemSorted calls afterward. Verify
	// every fast-path call's pre/post pageItems() survivor set directly.
	bt.DebugVerifyFastPathInserts = true
	// M-NIGHTLY investigation aid (AI-20260708-064334-001, 8th loop): the
	// 7th loop isolated the loss window to an unpin/re-pin gap on the same
	// block -- the only remaining not-yet-instrumented mechanism in that
	// gap is a dirty-page flush (evictVictim's wasDirty branch ->
	// flushSlot). Capture the exact bytes each flush writes to disk so
	// they can be cross-referenced against the last known-good insert/
	// rewrite/fast-path snapshot for the same block.
	bt.DebugTraceFlushes = true
	pool.OnFlushSnapshot = bt.RecordFlushSnapshot
	// M-NIGHTLY investigation aid (AI-20260708-064334-001, 9th loop): the
	// 8th loop proved the dirty-flush WRITE side always faithfully writes
	// whatever bytes are in memory (18/18) -- the only remaining
	// uninstrumented step in the eviction cycle is the READ side. Capture
	// the exact bytes every disk reload serves so they can be
	// cross-referenced against the immediately preceding flush snapshot
	// for the same block.
	bt.DebugTraceReloads = true
	pool.OnBlockReload = bt.RecordReloadSnapshot

	var wg sync.WaitGroup
	var failed atomic.Bool
	var inserted atomic.Int64
	perWriter := n / writers
	// TEMPORARY M-NIGHTLY investigation aid (AI-20260708-064334-001):
	// record every successfully-inserted (key, TID) pair so the caller can
	// diff it against the final on-disk leaf walk and identify exactly
	// which entries were lost (not just how many).
	perWriterEntries := make([][]btree.LeafEntry, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed + int64(wid)))
			entries := make([]btree.LeafEntry, 0, perWriter)
			for i := 0; i < perWriter; i++ {
				k := btree.EncodeInt4(rng.Int31n(keyRange))
				ptr := storage.ItemPointer{Block: storage.BlockNumber(wid + 1), Offset: uint16(i%60000 + 1)}
				if err := bt.Insert(k, ptr); err != nil {
					t.Errorf("writer %d insert #%d: %v", wid, i, err)
					failed.Store(true)
					return
				}
				entries = append(entries, btree.LeafEntry{Key: k, TID: ptr})
				inserted.Add(1)
			}
			perWriterEntries[wid] = entries
		}(w)
	}
	wg.Wait()

	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	if failed.Load() {
		cleanup()
		t.FailNow()
	}
	var all []btree.LeafEntry
	for _, e := range perWriterEntries {
		all = append(all, e...)
	}
	return mgr, pool, rel, int(inserted.Load()), all, bt, cleanup
}

// TestVerifyBtreeEngineSilentOnRealConcurrentContended is a KNOWN-FAILING
// regression test for the M-NIGHTLY pgbench "empty internal page" abort
// (AI-20260708-064334-001) — skipped by default until the underlying bug is
// fixed; un-skip locally to confirm a candidate fix, then remove the t.Skip.
//
// Root cause, as narrowed by this loop's investigation: NOT a btree
// split/high-key logic bug (single-threaded and multi_writer_stress_test.go's
// disjoint-range concurrent stress both stay clean). Insert() itself never
// returns an error for a lost key. The loss is reproducible with a SMALL
// buffer pool (heavy eviction churn under contention) and DISAPPEARS with a
// large pool (Slots: 2048, no eviction needed for this working set) — same
// insert logic, same contention pattern, zero loss. That isolates the bug to
// the eviction/flush path in internal/storage/bufpool.go (claimVictim /
// evictVictim / pinLoad), not internal/access/btree. A prior bug in exactly
// this area (evictVictim's bufmap.Delete-before-flush race) was fixed in
// commit 510615b4 for M0056-followup-multiwriter-flake; this is very likely a
// remaining/sibling race in the same machinery, not yet identified at the
// line level — see .ralph/deferral_ledger.md 2026-07-08 row for the next-step
// pointer.
//
// Repro reliability at these parameters (Slots: 64, n=200000, writers=64,
// keyRange=50000): lost 6-42 real leaf entries in every local run so far
// (never 0) in well under a second — roughly 300x cheaper than the
// pgbench -s 50 -c 100 -j 20 -T 180 nightly repro.
//
// Update (later loop, same day): pool.DebugValidateCleanEvictions (see
// bufpool.go) proves the mechanism directly — every run logs one or more
// "BUG: clean-eviction content mismatch" errors, where a "clean"
// (non-dirty) eviction's in-memory page differs from its on-disk image
// by dozens to hundreds of bytes starting right after the page header
// (byte 12). This is DEFINITIVE: the dirty bit read false for a page
// that had unflushed writes, so evictVictim's fast path (skip the flush
// because "on-disk already matches") silently discarded them. Ruled out
// this loop: (1) ABA via the 15-bit slot generation counter wrapping —
// measured max ~2500 claims per slot in a run, far below the 32768-wrap
// threshold; (2) stale recycled-block reuse (pinNewOrRecycled) — dead
// code path here, this test never calls btree_vacuum's recycleBlock;
// (3) a plain data race — `go test -race` on this exact repro reports
// zero races while still reproducing the mismatch, so this is a pure
// synchronized-but-wrong-order protocol bug, not a torn/unsynchronized
// memory access; (4) every MarkDirty call site in btree.go was audited
// and correctly precedes Unpin; every dirty-bit-clearing site in
// bufpool.go was enumerated (flushBatch, InvalidateRel/InvalidateBlock,
// claimVictim's own claim) and none apply to this insert-only workload.
// Still open: the exact sequence that makes claimVictim observe
// dirty=false for a slot that MarkDirty set true. Next step: add a
// monotonic per-slot event log (timestamp + old/new state) at every
// MarkDirty and claimVictim call for one specific slot that triggers a
// mismatch, to catch the transition in the act.
//
// Update (4th loop, same day, AI-20260708-064334-001): built
// pool.DebugTraceSlotEvents (see bufpool.go) — a per-slot ring log of
// every MarkDirty/claimVictim/evictVictim/pinLoad-publish/PinNew-publish/
// releaseVictimSlot state transition, dumped automatically on a
// DebugValidateCleanEvictions mismatch (both slot-local and a cross-slot
// scan for the same tag, to check for impossible double-ownership).
// Result: **the "clean eviction discards a real write" mechanism is NOT
// the (sole) cause of the missing entries.** Captured one real mismatch
// in the act: a slot loaded a page fresh from disk via pinLoad (cache
// miss), was Unpinned with ZERO intervening MarkDirty call, then was
// re-claimed as a victim moments later — genuinely clean by every
// tracked state transition — yet DebugValidateCleanEviction's disk
// re-read still disagreed with what was just read into memory. The
// cross-slot scan found no other slot ever recorded touching the same
// tag, ruling out double-ownership via the bufmap (Insert's mutex
// correctly serializes tag ownership; audited bufmap.go's Insert/
// Delete/Lookup/compact, relFile's readBlock/writeBlock/extend (all
// share one per-relation r.mu, using ReadAt/WriteAt not Seek+Read so no
// torn-offset race), Manager.relFile's single-instance-per-rel cache,
// and arena.slot's non-overlapping three-index slicing — all clean).
// Decisive tiebreaker: 3 back-to-back runs recorded 1/0/0
// DebugValidateCleanEviction mismatches but 12/20/13 missing entries —
// the run with the MOST missing entries (20) had ZERO mismatches. Missing-
// entry count and mismatch-firing are uncorrelated, so clean-eviction
// mismatches are at most a minor/coincidental contributor, and the
// dominant loss mechanism has to be something that never touches the
// dirty-bit machinery at all — most likely a genuine lost btree.Insert
// (an insert that reaches insertItemSorted, as loop 2 already proved via
// the write-log hook, but whose effect never survives to the final leaf
// walk) via a structural race (concurrent split/redistribution corrupting
// or dropping an item) rather than an eviction/flush race. Next step:
// reuse loop 2's reverted insertItemSorted write-log hook, but this time
// keep it live end-to-end and cross-reference it against the final leaf
// walk to find the exact (page, slot-offset) a lost key was written to,
// then trace that page's slotEvents history (now available without
// building new instrumentation) to see what clobbered it — likely a
// split/redistribution logic bug in internal/access/btree, not
// internal/storage/bufpool.go. See .ralph/deferral_ledger.md's 4th
// 2026-07-08 row for the full writeup.
//
// Update (5th loop, same day, AI-20260708-064334-001): built
// BTree.DebugTraceInserts (btree.go) — an unbounded, global log of every
// insertItemSorted call (block, line-pointer index, key, TID), plus
// Pool.DumpEventsForTag (bufpool.go), the exported sibling of
// dumpCrossSlotEventsForTag for cross-package lookups. Result: **the
// dominant loss mechanism is now proven, not just hypothesized, to be
// inside the split/redistribution rewrite path.** For every missing entry
// in a fresh repro: (1) insertItemSorted WAS called exactly once for that
// exact (key, TID) — confirmed physically written (PageInsertItemRawAt
// panics on failure, and no panic occurred); (2) a global scan of the
// ENTIRE run's insert log for that TID found NO second occurrence anywhere,
// on ANY block — ruling out "the item was carried to a new block during a
// later split" (a split's left/right redistribution loop re-invokes
// insertItemSorted for every surviving item, which would have logged a
// second record); (3) the original block went on to receive hundreds more
// insertItemSorted calls (healthy ongoing activity, not abandoned/evicted);
// (4) reading the block's CURRENT on-disk bytes at test end confirms the
// exact TID is genuinely absent — sometimes with sibling entries sharing
// the identical duplicate key still present and intact. Since a plain
// single-item insertItemSorted call only ADDS a line pointer (shifts
// others, never deletes existing tuple bytes), the only mechanism able to
// make a previously, successfully-written entry disappear without a trace
// is `insertIntoBlock`'s split branch: `resetPageItems` wipes the entire
// line-pointer array, then `pageItems`+`appendSorted`+`dedupConsolidate`
// recompute the survivor set from scratch and reinsert it split across
// left/right. The bug is therefore localized to that exact sequence
// (btree.go's insertIntoBlock, ~line 1523-1574) — either `pageItems` fails
// to read back an item that's genuinely on the page at read time, or
// `dedupConsolidate`/`appendSorted` incorrectly drops a non-duplicate item
// during the rebuild. Both were re-read function-by-function this loop and
// no single-threaded logic flaw was spotted by inspection alone (dedup only
// drops EXACT (key,ptr) duplicates via a safe in-place filter; appendSorted
// is a straightforward sorted insert) — next step needs the SAME kind of
// direct instrumentation used here, but INSIDE insertIntoBlock's split
// branch itself: log `len(allItems)` immediately after `pageItems()` and
// again after `dedupConsolidate`, cross-referenced against
// `PageLinePointerCount` read just before the split starts, to catch
// whether the read-back undercounts or the dedup rebuild overcounts-then-
// drops. See .ralph/deferral_ledger.md's 5th 2026-07-08 row for the full
// writeup and exact resume point.
//
// Update (6th loop, same day, AI-20260708-064334-001): executed the 5th
// loop's exact next step — built BTree.RewriteLogEvent/traceRewrite
// (btree.go), a deep-copy snapshot of allItems taken right after
// pageItems()+appendSorted() and again right after dedupConsolidate(),
// for every insertIntoBlock rewrite (both the dedup-recovery no-split
// path and the real split path), sharing one monotonic Seq counter with
// insertLog so the two logs' events can be temporally ordered against
// each other. Wired a per-missing-entry diagnostic (below) that finds
// the first rewrite event on the lost entry's block at/after its insert
// and reports whether the (key,TID) was present in each snapshot.
// **This REFUTES the 5th loop's "split/redistribution" localization**:
// every rewrite event on an affected block already shows
// presentAfterPageItems=false — i.e., `pageItems()` correctly decodes
// every physical line pointer on the page (postPageItemsCount is always
// EXACTLY preLineCount+1, matching PageLinePointerCount with no
// discrepancy) but the physical page ITSELF already lacks the lost
// entry's line pointer before this rewrite ever runs. dedupConsolidate
// is therefore also cleared (nothing to drop that was never there). Some
// missing entries (e.g. writer 34 iters 2284/2601) show NO rewrite event
// at all ever touching their block after the insert — for those, only
// the plain single-item fast-path insertItemSorted calls (tryInsertNoSplit
// / insertIntoBlock's own no-split branch / tryInsertOnCachedRightmost)
// ever touched that page afterward, yet the entry still vanished. The
// loss mechanism is therefore in the FAST-PATH insert sequence itself
// (PageInsertItemRawAt's line-pointer-array bookkeeping under repeated
// same-page inserts), not the split/dedup rewrite. Also ran this exact
// repro under `-race` for the first time (all 20+ prior -race-clean runs
// used the DISJOINT-key TestMultiWriterStress_M0055_Phase_C repro, never
// this heavily-DUPLICATE-key one) and got exactly ONE report — inside
// `dumpCrossSlotEventsForTag`/`traceSlotEvent` (bufpool.go), the
// DebugTraceSlotEvents ring-buffer debug tool itself, whose doc comment
// already documents cross-slot torn reads as an accepted best-effort
// tradeoff; it does not touch or explain the missing-entry mechanism.
// No other race fired (GORACE=halt_on_error=0, full run to completion).
// This rules out a plain memory data race for the THIRD independent repro
// in this investigation thread — reinforcing loop 3's "pure
// protocol/ordering bug in properly-synchronized code, not a torn/
// unsynchronized access" conclusion. Next step: apply the SAME
// before/after pageItems()-snapshot technique built this loop, but to
// the fast-path single-item insertItemSorted call sites (tryInsertNoSplit,
// insertIntoBlock's no-split branch, tryInsertOnCachedRightmost) instead
// of the rewrite path — snapshot pageItems() immediately before and after
// each fast-path call for the specific blocks known (post-hoc) to have
// lost an entry, to find the exact consecutive pair of fast-path inserts
// where a previously-present (key,TID) silently disappears. See
// .ralph/deferral_ledger.md's 6th 2026-07-08 row for the full writeup.
//
// Update (7th loop, same day, AI-20260708-064334-001): executed the 6th
// loop's exact next step. Built BTree.DebugVerifyFastPathInserts /
// insertItemSortedVerified (btree.go) — wraps all 3 fast-path single-item
// insertItemSorted call sites (tryInsertNoSplit, insertIntoBlock's own
// no-split branch, tryInsertOnCachedRightmost) with a pageItems() snapshot
// immediately before and immediately after each call, and asserts every
// pre-existing (key,TID) pair the "before" snapshot held is still present
// in the "after" snapshot (a plain fast-path insert only ever ADDS a line
// pointer). **REFUTES this loop's own hypothesis too, with a clean
// negative result**: across a full 200000-insert/64-writer run that still
// lost 12 real entries, ZERO FastPathViolations were recorded — every
// fast-path call, at every site, preserved every pre-existing entry.
// Combined with the 6th loop's finding (pageItems() at rewrite time
// already lacks the lost entry, so dedupConsolidate/appendSorted are also
// clean), this pins the loss window down precisely: for one traced
// example (key=47087, TID={3,1508}, inserted at blk=16 lineIdx=6
// seq=216430), the LAST fast-path call on blk=16 to include this entry in
// its "post" snapshot, and the eventual split's own pageItems() read
// (which already lacked it), are separated by an UNPIN(blk=16) ...
// re-PIN(blk=16) gap — the entry vanished while nobody held a pin on the
// page, i.e. during (or across) a buffer-pool eviction. The existing
// DebugValidateCleanEvictions/DebugTraceSlotEvents instrumentation
// (loops 3-4) only checks the "clean" (skip-flush) eviction fast path and
// found no mismatch on blk=16 in this run (its one hit this run was on an
// unrelated block, 133) — the DIRTY flush-then-evict path
// (evictVictim's `wasDirty` branch → flushSlot, bufpool.go ~1123) has
// never been directly instrumented to compare "bytes about to be written
// to disk" against "the last known-good in-memory pageItems() snapshot"
// for the SAME block. Next step: instrument flushSlot (or evictVictim's
// dirty branch immediately around it) to snapshot pageItems() on the page
// being flushed right before the WriteAt call, keyed by block, and
// compare against the most recent fast-path/rewrite "post" snapshot
// recorded for that block (both logs already exist and share Seq
// ordering) — a mismatch there would directly catch a stale/torn flush.
// Also worth a quick audit pass: does claimVictim correctly refuse to
// select a slot with a nonzero pin count as a victim (a pinned page being
// evicted at all would itself be the bug, independent of flush
// correctness)? See .ralph/deferral_ledger.md's 7th 2026-07-08 row for
// the full writeup.
//
// Update (8th loop, same day, AI-20260708-064334-001): audited claimVictim
// first, per the 7th loop's suggestion — it correctly excludes any slot
// with a nonzero pin count (`if statePin(old) != 0 { continue }`,
// bufpool.go ~1079); not the bug. Then executed the 7th loop's main next
// step: built storage.Pool.OnFlushSnapshot (bufpool.go, called from
// flushSlot immediately before the WriteBlock call, zero cost when nil)
// plus BTree.RecordFlushSnapshot/DebugTraceFlushes/FlushSnapshotEvent
// (btree.go), which decodes pageItems() from the exact bytes a flush is
// about to write to disk and logs it sharing insertLog/rewriteLog's Seq
// counter. **This is the strongest, most reproducible signature found in
// this entire 8-loop investigation.** For all 18 missing entries in a
// fresh repro (previously only spot-checked 1-2 per run), the SAME exact
// pattern held with zero exceptions: the entry is present in the first
// recorded flush-snapshot of its block after its insert, and already
// ABSENT in the very next recorded flush-snapshot of that same block —
// often only tens to a few hundred Seq ticks later (e.g. key=48709
// TID={6,1512}: present at flush seq=254194 [332 items], gone at flush
// seq=254306 [333 items], 112 ticks later). Crucially, DebugVerifyFastPathInserts
// (7th loop) recorded ZERO violations for the entire run, which this
// finding now explains rather than contradicts: that check only compares
// a page's survivor set immediately before/after each individual
// insertItemSorted call, so if the entry is ALREADY missing by the time
// ANY fast-path call next touches the reloaded page, every such call
// correctly reports no violation (nothing it touched went missing on its
// watch). Combined with the already-clean rewrite-path (6th loop) and
// clean-eviction (4th loop) findings, this leaves exactly one
// uninstrumented mechanism able to make the entry vanish between "last
// good flush" and "next flush already missing it": the RELOAD side of an
// eviction cycle — Pool's cache-miss read path (pinLoad / Manager.ReadBlock)
// serving stale or wrong bytes for the block after it was correctly
// flushed. Also notable: the item count between the two flushes is NOT
// always a clean -1 (e.g. blk=192 above went 332->333, up by one, because
// other writers' inserts landed in the same window) — consistent with the
// loss happening once, silently, during a reload that other concurrent
// fast-path inserts then build on top of without ever detecting the gap.
// Next step: instrument the read/reload side directly — snapshot
// pageItems() on any block reload that follows a prior flush of the same
// block (Pool.pinLoad's cache-miss branch, or Manager.ReadBlock itself),
// and compare against the most recent FlushSnapshotEvent recorded for
// that exact block+Rel via the same Seq-ordered cross-reference already
// built here — a mismatch there would catch a stale/wrong read red-handed
// and finally close the loop this investigation opened at the 7th loop.
// See .ralph/deferral_ledger.md's 8th 2026-07-08 row for the full
// writeup.
//
// Update (9th loop, same day, AI-20260708-064334-001): executed the 8th
// loop's next step. Built storage.Pool.OnBlockReload (bufpool.go, called
// from pinLoad's cache-miss branch immediately after Manager.ReadBlock
// succeeds, before the slot is published for Pin) plus
// BTree.RecordReloadSnapshot/DebugTraceReloads/ReloadSnapshotEvent
// (btree.go), sharing the same Seq counter. While wiring the cross-
// reference, found and fixed a real bug in the 8th loop's OWN
// instrumentation first: OnFlushSnapshot fired BEFORE flushSlot's
// WriteBlock call (pre-write "intent" time) while the new OnBlockReload
// fires AFTER ReadBlock returns (post-read "completed" time) — comparing
// Seq across the two logs as-is would have been apples-to-oranges, since
// a reload's real disk read could complete before a flush's real disk
// write yet still log a HIGHER Seq purely from insertLogMu lock-
// acquisition jitter. Moved OnFlushSnapshot to fire after WriteBlock
// succeeds so both hooks consistently log "operation durably completed"
// time (bufpool.go's flushSlot). Re-ran the repro AFTER this fix and the
// signature not only survived, it strengthened: for every one of 14 lost
// entries in this run, the FIRST reload of the affected block recorded
// after the last flush that had the entry ALREADY lacks it (e.g. blk=142:
// flush seq=609678 itemCount=360 present=true; the very next reload,
// seq=609829, itemCount=359 present=false) — one item short of what was
// just durably written. Critically, this is not a one-off race that
// later reads recover from: EVERY subsequent reload of the same block for
// the rest of the run (3179 reload-events checked across all 14 missing
// entries, from Seq deltas of a few hundred up to well over 100000 ticks
// later) still lacks the entry, while the item count keeps climbing as
// other writers insert on top. A transient read/write race would show
// SOME later reloads recovering the entry once the real disk state
// settles; a permanent, unrecovering loss instead means either (a) the
// "good" flush's WriteBlock call did not durably land the entry despite
// its in-memory snapshot having it, or (b) a second, older/stale write to
// the same block landed on disk AFTER the good flush and clobbered it — a
// classic lost-update pattern. storage.Pool.slotEvent traces for one
// affected block (173) show three DIFFERENT slot indices (17, 25, 31)
// holding the same tag across a span of ~3000 Seq ticks — slot 17's dirty
// eviction (flush) immediately followed by slot 25's fresh pinLoad
// (reload) then an immediate CLEAN eviction (no insert landed on it
// while resident) then slot 31's own pinLoad (another reload) — i.e. the
// block is churning through eviction/reload cycles on a ~64-slot pool
// under 64-writer contention fast enough that back-to-back cycles for the
// SAME block, on DIFFERENT physical slots, are common. Manual code
// review of evictVictim's dirty branch (bufpool.go) and smgr.go's
// relFile.readBlock/writeBlock (both properly serialized per-relFile
// under r.mu, and evictVictim only calls bm.Delete(oldTag) strictly AFTER
// flushSlot's WriteBlock returns) did not surface an obvious lock gap for
// hypothesis (a); hypothesis (b) — a second, stale write clobbering the
// good one — remains unrefuted and is the most likely remaining
// mechanism, but requires either wall-clock (not Seq) instrumentation at
// the smgr.relFile.writeBlock/readBlock layer directly, or a raw
// independent re-read of the block right after the "good" flush (mirroring
// what debugValidateCleanEviction already does for the clean-eviction
// path) to catch a losing racer red-handed. Next step: instrument
// relFile.writeBlock and relFile.readBlock directly (smgr.go, both
// already under r.mu) with a monotonic op-sequence counter local to the
// relFile (not bt's insertLogMu-guarded Seq, to eliminate ANY
// cross-goroutine lock-ordering jitter) so a caller can determine, for a
// specific block, the exact TRUE order of every WriteAt/ReadAt syscall —
// then re-run this repro and check whether two writeBlock calls for the
// SAME block are ever interleaved such that an OLDER in-memory copy's
// write lands after the newer one's, which would confirm hypothesis (b)
// directly and point at whichever caller is flushing stale content (likely
// a second concurrent evictVictim/WriteDirtyPages/flushBatch caller
// holding an outdated slot). See .ralph/deferral_ledger.md's 9th
// 2026-07-08 row for the full writeup.
func TestVerifyBtreeEngineSilentOnRealConcurrentContended(t *testing.T) {
	t.Skip("known-failing: tracks M-NIGHTLY AI-20260708-064334-001 (btree lost-index-entry bug; 10th loop cross-checked the 9th loop's reload-snapshot smoking gun against an INDEPENDENT smgr/syscall-level content-hash IO tracer (GOOPG_IO_TRACE=1, internal/storage/io_trace.go) and REFUTES hypothesis (b) -- across 60863 real ReadAt/WriteAt-completion events spanning 38 blocks and 49 lost entries in one run, EVERY postRead exactly matched the immediately-preceding postWrite's byte-content hash for that same (relFile, block); zero stale/superseded reads at the true hardware-completion-order layer. Combined with the 8th loop's proof that flush-side WriteBlock always durably wrote whatever was in memory, the entire storage/smgr layer (bufpool eviction+reload+flush, and the OS/file read/write path) is now cleared -- the loss is a genuine IN-MEMORY content bug, not a storage-layer one. Reviewed bufpool.go's pinLoad/tryPinSlot/claimVictim tag-publish-before-load window on inspection (state-bit gating + pinMu serialization) and found no obvious gap. Next step: instrument at the contentMu-critical-section level directly (one layer below the flush/reload snapshots already built) to catch which specific content mutation clobbers the entry; see .ralph/deferral_ledger.md's 10th 2026-07-08 row for full detail); un-skip to confirm a fix")
	if testing.Short() {
		t.Skip("skipping concurrent real-tree amcheck stress in short mode")
	}
	const n = 200000
	const writers = 64
	const poolSlots = 64
	const keyRange = 50000
	mgr, pool, rel, got, expected, bt, cleanup := buildRealTreeConcurrent(t, n, writers, poolSlots, keyRange, 20260708)
	defer cleanup()
	hit, read, dirtied, written := pool.BufferCounters()
	t.Logf("buffer counters: hit=%d read=%d dirtied=%d written=%d dirtyVictimRate=%.4f",
		hit, read, dirtied, written, pool.DirtyVictimRate())
	if got != n {
		t.Fatalf("inserted %d, want %d", got, n)
	}

	// M-NIGHTLY investigation aid (AI-20260708-064334-001, 7th loop): any
	// fast-path insertItemSorted call whose pre/post pageItems() survivor
	// sets disagree is a direct, localized capture of the loss mechanism
	// -- dump every one unconditionally before the leaf-entry diff below.
	if violations := bt.FastPathViolations(); len(violations) > 0 {
		t.Logf("FAST-PATH VIOLATIONS: %d recorded", len(violations))
		for _, v := range violations {
			t.Logf("  violation seq=%d blk=%d site=%s inserted=%s preCount=%d postCount=%d",
				v.Seq, v.Block, v.Site, v.Inserted, v.PreCount, v.PostCount)
			for _, m := range v.Missing {
				t.Logf("    missing: %s", m)
			}
		}
	} else {
		t.Logf("FAST-PATH VIOLATIONS: none recorded (fast-path insertItemSorted calls all preserved pre-existing entries)")
	}

	// M-NIGHTLY investigation aid (AI-20260708-064334-001): diff the
	// actual on-disk leaf entries against every successfully-inserted
	// (key, TID) pair to identify exactly which entries were lost, and
	// whether the losses cluster by writer (TID.Block == wid+1) or by
	// iteration (TID.Offset == i+1) — a cluster would point at a specific
	// eviction/flush window rather than a uniformly-random race.
	src := realPageSource(t, pool, rel)
	actual, err := amcheck.CollectBtreeLeafEntries(src)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	present := make(map[storage.ItemPointer]bool, len(actual))
	for _, e := range actual {
		present[e.TID] = true
	}
	if len(actual) != n {
		for _, e := range expected {
			if !present[e.TID] {
				kv, _ := btree.DecodeInt4(e.Key)
				wid := int(e.TID.Block) - 1
				i := int(e.TID.Offset) - 1
				t.Logf("MISSING entry: key=%d TID=%v (writer=%d iter=%d of %d)",
					kv, e.TID, wid, i, n/writers)
				// M-NIGHTLY investigation aid (AI-20260708-064334-001, 5th
				// loop): was this entry ever actually written, and where?
				logRecs := bt.InsertLogRecordsForTID(e.TID)
				if len(logRecs) == 0 {
					t.Logf("  insertItemSorted NEVER called for this TID — insert path silently returned without writing it")
					continue
				}
				for _, r := range logRecs {
					t.Logf("  insert-log: %s", r)
				}
				// Did the target block get rewritten wholesale afterward (a
				// split/dedup-recovery redistribution burst), and did our
				// item's (key,TID) survive that rewrite as a fresh
				// insertItemSorted call? Look at every recorded write to the
				// same block from just after our insert onward (count only —
				// the individual records are too voluminous to log per-entry).
				last := logRecs[len(logRecs)-1]
				after := bt.InsertLogRecordsForBlockAfter(last.Block, last.Seq+1)
				rewritten := false
				for _, r := range after {
					if r.Ptr == e.TID {
						rewritten = true
					}
				}
				t.Logf("  %d later insertItemSorted call(s) touched blk=%d after seq=%d; our TID reappears in that log: %v",
					len(after), last.Block, last.Seq, rewritten)
				// M-NIGHTLY investigation aid (AI-20260708-064334-001, 6th
				// loop): pinpoint the exact insertIntoBlock rewrite event
				// (dedup-recovery or split) that should have carried this
				// entry forward, and check whether it was present at the
				// pageItems()+appendSorted() checkpoint but missing after
				// dedupConsolidate() (a dedup bug) versus already missing
				// at the pageItems() checkpoint itself (a page read/decode
				// bug -- pageItems undercounting a page that genuinely held
				// the entry). insertLog and rewriteLog share one monotonic
				// Seq counter, so Seq comparison across the two logs
				// reflects true temporal order.
				rewriteEvents := bt.RewriteLogRecordsForBlock(last.Block)
				foundRewrite := false
				for _, re := range rewriteEvents {
					if re.Seq < last.Seq {
						continue
					}
					foundRewrite = true
					inPageItems := btree.RewriteSnapshotHasTID(re.PostPageItems, e.TID)
					inDedup := btree.RewriteSnapshotHasTID(re.PostDedup, e.TID)
					t.Logf("  rewrite-event seq=%d phase=%s preLineCount=%d postPageItemsCount=%d postDedupCount=%d presentAfterPageItems=%v presentAfterDedup=%v",
						re.Seq, re.Phase, re.PreLineCount, len(re.PostPageItems), len(re.PostDedup), inPageItems, inDedup)
					if !inPageItems {
						t.Logf("  => LOCALIZED: pageItems() already lacked this (key,TID) at rewrite seq=%d -- bug is in the page read/decode, not dedupConsolidate", re.Seq)
					} else if !inDedup {
						t.Logf("  => LOCALIZED: pageItems() had this (key,TID) but dedupConsolidate() dropped it at rewrite seq=%d -- bug is in dedupConsolidate", re.Seq)
					}
				}
				if !foundRewrite {
					t.Logf("  no rewrite event recorded for blk=%d at/after seq=%d (block never underwent a split/dedup-recovery rewrite after this insert -- loss did NOT happen via insertIntoBlock's rewrite path)",
						last.Block, last.Seq)
				}
				// M-NIGHTLY investigation aid (AI-20260708-064334-001, 8th
				// loop): does any dirty-page flush of this block show the
				// entry already missing from the EXACT bytes written to
				// disk? A flush is the only thing that touches a page's
				// bytes while nobody holds a pin on it (the 7th loop's
				// isolation of the loss window to an unpin/re-pin gap on
				// the same block) -- if a flush's own pageItems() snapshot
				// already lacks the entry, the loss is upstream of
				// flushSlot/WriteBlock (evictVictim's dirty branch, or the
				// in-memory page was already corrupt before the flush
				// began), not the flush call itself.
				flushEvents := bt.FlushSnapshotRecordsForBlock(last.Block)
				if len(flushEvents) == 0 {
					t.Logf("  no flush-snapshot event recorded for blk=%d at/after seq=%d (block never flushed to disk while dirty during this run)",
						last.Block, last.Seq)
				} else {
					for _, fe := range flushEvents {
						if fe.Seq < last.Seq {
							continue
						}
						has := btree.RewriteSnapshotHasTID(fe.Items, e.TID)
						t.Logf("  flush-snapshot seq=%d blk=%d itemCount=%d presentAtFlush=%v",
							fe.Seq, fe.Block, len(fe.Items), has)
						if !has {
							t.Logf("  => LOCALIZED: dirty-page flush at seq=%d already lacked this (key,TID) in the exact bytes written to disk -- bug is upstream of flushSlot/WriteBlock, not the flush call itself", fe.Seq)
						}
					}
				}
				// M-NIGHTLY investigation aid (AI-20260708-064334-001, 9th
				// loop): the 8th loop proved the flush WRITE side always
				// faithfully wrote whatever was in memory. Now check the
				// READ side directly: find the last flush of this block
				// that still had the entry present (the "good" flush) and
				// every reload of this block recorded after it -- does
				// pageItems() on the reload already lack the entry? If so,
				// that IS the smoking gun (a stale/wrong disk read); if
				// every reload after the last good flush still HAS the
				// entry, the loss must be happening after the reload
				// (post-Pin, in-memory) instead, ruling the reload path
				// out entirely.
				{
					var lastGoodFlushSeq uint64
					haveLastGood := false
					for _, fe := range flushEvents {
						if btree.RewriteSnapshotHasTID(fe.Items, e.TID) {
							if !haveLastGood || fe.Seq > lastGoodFlushSeq {
								lastGoodFlushSeq = fe.Seq
								haveLastGood = true
							}
						}
					}
					reloadEvents := bt.ReloadSnapshotRecordsForBlock(last.Block)
					if len(reloadEvents) == 0 {
						t.Logf("  no reload-snapshot event recorded for blk=%d (block never went through a disk reload during this run)", last.Block)
					} else if !haveLastGood {
						t.Logf("  %d reload-snapshot event(s) recorded for blk=%d, but no flush ever had this entry present -- cannot anchor a last-known-good flush", len(reloadEvents), last.Block)
					} else {
						for _, re := range reloadEvents {
							if re.Seq < lastGoodFlushSeq {
								continue
							}
							has := btree.RewriteSnapshotHasTID(re.Items, e.TID)
							t.Logf("  reload-snapshot seq=%d blk=%d itemCount=%d presentAtReload=%v (last good flush seq=%d)",
								re.Seq, re.Block, len(re.Items), has, lastGoodFlushSeq)
							if !has {
								t.Logf("  => SMOKING GUN: disk reload at seq=%d already lacked this (key,TID) despite the last flush of this block (seq=%d) having it -- the reload served stale or wrong bytes", re.Seq, lastGoodFlushSeq)
							}
						}
					}
				}
				// M-NIGHTLY investigation aid (AI-20260708-064334-001, 10th
				// loop): the 9th loop's smoking-gun reload-snapshot check
				// operates at the btree layer (pageItems() decoded from
				// bt's own OnBlockReload/OnFlushSnapshot hooks, timestamped
				// by a shared insertLogMu Seq counter with residual
				// cross-goroutine jitter). Cross-check the SAME block at
				// the smgr/syscall layer instead, using the independent
				// content-hash IO tracer (internal/storage/io_trace.go,
				// GOOPG_IO_TRACE=1) whose events are timestamped by
				// time.Now() taken immediately after each real ReadAt
				// (readBlock) / WriteAt (writeBlock) call returns, under
				// the SAME relFile.mu that serializes the syscalls
				// themselves -- true completion order, zero lock-ordering
				// ambiguity. If a postRead's byte-content hash exactly
				// matches an EARLIER postWrite's hash rather than the most
				// recent one, that is direct syscall-level proof of a
				// stale/superseded read (confirms hypothesis (b): an older
				// write's bytes really did reach disk after -- or were
				// read in preference to -- the newer write's bytes). If a
				// postRead's hash matches NO prior postWrite at all, the
				// bytes on disk were never legitimately written through
				// this relFile, pointing at a filesystem/file-identity bug
				// instead of a buffer-pool write-write race.
				{
					tag := storage.BufferTag{Rel: rel, Block: last.Block}
					ioEvents := storage.IOTraceEventsForTag(tag)
					if len(ioEvents) == 0 {
						t.Logf("  no smgr-level IO-trace events for blk=%d (re-run with GOOPG_IO_TRACE=1 to enable this diagnostic)", last.Block)
					} else {
						var priorWrites []struct {
							Hash  uint64
							LPCnt int
						}
						for _, ev := range ioEvents {
							t.Logf("  io-trace: phase=%s blk=%d hash=%016x lpCnt=%d lpErr=%q t=%s",
								ev.Phase, tag.Block, ev.Hash, ev.LPCnt, ev.LPErr, ev.T.Format("15:04:05.000000000"))
							switch ev.Phase {
							case "postWrite":
								priorWrites = append(priorWrites, struct {
									Hash  uint64
									LPCnt int
								}{ev.Hash, ev.LPCnt})
							case "postRead":
								if len(priorWrites) == 0 {
									continue
								}
								latest := priorWrites[len(priorWrites)-1]
								if ev.Hash == latest.Hash {
									continue
								}
								matchedStale := -1
								for i := len(priorWrites) - 2; i >= 0; i-- {
									if priorWrites[i].Hash == ev.Hash {
										matchedStale = i
										break
									}
								}
								if matchedStale >= 0 {
									t.Logf("  => SMOKING GUN (syscall-level): postRead hash=%016x lpCnt=%d matches an EARLIER postWrite (%d writes back, lpCnt=%d), not the most recent postWrite (lpCnt=%d) -- a stale/superseded write's bytes were read back, confirming hypothesis (b) at the smgr layer",
										ev.Hash, ev.LPCnt, len(priorWrites)-1-matchedStale, priorWrites[matchedStale].LPCnt, latest.LPCnt)
								} else {
									t.Logf("  => ANOMALY (syscall-level): postRead hash=%016x lpCnt=%d matches NO prior postWrite to this tag (most recent postWrite hash=%016x lpCnt=%d) -- bytes on disk were never legitimately written through this relFile",
										ev.Hash, ev.LPCnt, latest.Hash, latest.LPCnt)
								}
							}
						}
					}
				}
				// M-NIGHTLY investigation aid (AI-20260708-064334-001, 7th
				// loop): does any recorded FastPathViolation directly name
				// this TID as a casualty?
				for _, v := range bt.FastPathViolations() {
					for _, m := range v.Missing {
						if m.Ptr == e.TID {
							t.Logf("  => LOCALIZED: fast-path violation seq=%d site=%s blk=%d (triggered by inserting %s) dropped this exact TID",
								v.Seq, v.Site, v.Block, v.Inserted)
						}
					}
				}
				// Ground truth: is the key/TID physically present on the
				// block's CURRENT on-disk bytes right now, regardless of what
				// the insert log says? Distinguishes "line-pointer array
				// entry lost" (key absent entirely) from "TID silently
				// changed" (key present, different TID) from "present but
				// CollectBtreeLeafEntries's sibling-chain walk never reached
				// this page" (key present here, entry also present in
				// `actual` under a still-correct TID — would mean the earlier
				// `present` map lookup itself is the bug, not the tree).
				if p, perr := src(last.Block); perr == nil {
					op := btree.ParseOpaque(p)
					entries, eerr := btree.PageLeafEntries(p)
					var sameKey []btree.LeafEntry
					if eerr == nil {
						for _, le := range entries {
							if len(le.Key) == len(e.Key) && string(le.Key) == string(e.Key) {
								sameKey = append(sameKey, le)
							}
						}
					}
					lineCount, _ := storage.PageLinePointerCount(p)
					t.Logf("  CURRENT blk=%d: isLeaf=%v deleted=%v lineCount=%d entriesErr=%v sameKeyEntries=%v",
						last.Block, op.IsLeaf(), op.IsDeleted(), lineCount, eerr, sameKey)
				} else {
					t.Logf("  CURRENT blk=%d: read failed: %v", last.Block, perr)
				}
				// Also dump the storage layer's per-slot event history for
				// every block this entry was ever written to (bounded ring,
				// may only show the tail of a long run).
				seenBlk := map[storage.BlockNumber]bool{}
				for _, r := range logRecs {
					if seenBlk[r.Block] {
						continue
					}
					seenBlk[r.Block] = true
					tag := storage.BufferTag{Rel: rel, Block: r.Block}
					for _, se := range pool.DumpEventsForTag(tag) {
						t.Logf("  storage-event(blk=%d): %s", r.Block, se)
					}
				}
			}
		}
	}

	assertNonSiblingTiersSilent(t, mgr, pool, rel, got)
}
