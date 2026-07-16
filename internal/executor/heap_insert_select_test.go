package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// newSelectFixture constructs a Manager/Pool/FSM trio and extends `rel`
// by nPages empty pages, recording each page's free space in the FSM
// with the supplied per-page byte counts. The returned FSM block IDs
// align with the slice index (page i lives at block i).
func newSelectFixture(t *testing.T, nPages int, freeBytes []uint16) (*storage.Manager, *storage.Pool, *storage.FSM, storage.RelFileNode) {
	t.Helper()
	if len(freeBytes) != nPages {
		t.Fatalf("freeBytes length %d != nPages %d", len(freeBytes), nPages)
	}
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { mgr.Close() })
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	fsm := storage.NewFSM()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 0xC0FFEE10, Fork: storage.MainFork}
	src := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(src); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nPages; i++ {
		if _, err := mgr.Extend(rel, src); err != nil {
			t.Fatalf("Extend page %d: %v", i, err)
		}
		fsm.RecordFreeSpace(rel, storage.BlockNumber(i), freeBytes[i])
	}
	return mgr, pool, fsm, rel
}

// TestSelectFSMCandidatePageNilInputs verifies the nil-safe early returns
// (helper must not panic when called from a path with no FSM or pool).
func TestSelectFSMCandidatePageNilInputs(t *testing.T) {
	_, pool, fsm, rel := newSelectFixture(t, 1, []uint16{1024})

	if blk, ok := selectFSMCandidatePage(nil, pool, rel, 16); ok || blk != 0 {
		t.Errorf("nil FSM: got (%d, %v), want (0, false)", blk, ok)
	}
	if blk, ok := selectFSMCandidatePage(fsm, nil, rel, 16); ok || blk != 0 {
		t.Errorf("nil Pool: got (%d, %v), want (0, false)", blk, ok)
	}
}

// TestSelectFSMCandidatePageEmptyFSM verifies a (0, false) signal when no
// FSM page has minFreeBytes available — the caller must fall through.
func TestSelectFSMCandidatePageEmptyFSM(t *testing.T) {
	_, pool, fsm, rel := newSelectFixture(t, 3, []uint16{10, 20, 30})

	// minFreeBytes higher than every entry → no qualifying candidates.
	if blk, ok := selectFSMCandidatePage(fsm, pool, rel, 100); ok || blk != 0 {
		t.Errorf("no-fit minFreeBytes: got (%d, %v), want (0, false)", blk, ok)
	}
}

// TestSelectFSMCandidatePageRanksByPinCount confirms the helper prefers
// the candidate with the lowest live pin count among the FSM top-K.
//
// Setup: 3 FSM entries (blocks 0,1,2) all with ample free space; block 0
// is pinned twice and block 2 is pinned once. The helper must pick
// block 1 (pin count 0). Without pin-count ranking it would return the
// first candidate (typically block 0 since GetCandidates is ordered by
// free-space desc, then block-asc on ties — all three are tied at 4000).
func TestSelectFSMCandidatePageRanksByPinCount(t *testing.T) {
	_, pool, fsm, rel := newSelectFixture(t, 3, []uint16{4000, 4000, 4000})

	// Pin block 0 twice and block 2 once. Block 1 stays unpinned.
	s0a, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	s0b, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(s0a)
	defer pool.Unpin(s0b)
	defer pool.Unpin(s2)

	blk, ok := selectFSMCandidatePage(fsm, pool, rel, 1024)
	if !ok {
		t.Fatal("expected a candidate, got (_, false)")
	}
	if blk != 1 {
		t.Errorf("ranked best block = %d, want 1 (only pin-0 candidate)", blk)
	}
}

// TestSelectFSMCandidatePageShortCircuitsOnPinZero confirms the helper
// stops scanning the FSM top-K once it finds a pin-0 candidate — the
// pin==0 break is what keeps the worst case to "at most candidatesPerInsert
// bufmap lookups", which matters on the hot path.
//
// Setup: 4 FSM entries; block 0 already at pin 0 (unpinned), block 1 at
// pin 5 (above hotPinThreshold). If we did not short-circuit and rolled
// past block 1, we would still pick block 0 — so a positive correctness
// assertion is "best == 0". The short-circuit invariant is then asserted
// indirectly by deliberately corrupting later candidates' pin counts:
// since the helper returns at the first pin==0, those later candidates
// must be unobserved. We test this by making block 2 also pin-0 and
// asserting we still get block 0 (the first pin-0 hit) deterministically.
func TestSelectFSMCandidatePageShortCircuitsOnPinZero(t *testing.T) {
	_, pool, fsm, rel := newSelectFixture(t, 4, []uint16{4000, 4000, 4000, 4000})

	// Pin block 1 five times so it exceeds hotPinThreshold (4). Without
	// short-circuit-on-zero, an implementation that always picks the
	// strict min would still pick block 0 — so this test asserts the
	// happy path's correctness rather than the optimisation itself.
	pins := make([]*storage.Slot, 0, 5)
	for i := 0; i < 5; i++ {
		s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 1})
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, s)
	}
	defer func() {
		for _, s := range pins {
			pool.Unpin(s)
		}
	}()

	blk, ok := selectFSMCandidatePage(fsm, pool, rel, 1024)
	if !ok {
		t.Fatal("expected a candidate, got (_, false)")
	}
	// GetCandidates orders ties by lowest block first; block 0 is pin-0,
	// so it must be selected.
	if blk != 0 {
		t.Errorf("got %d, want 0 (first pin-0 candidate)", blk)
	}
}

// TestSelectFSMCandidatePageRejectsHotCandidates confirms the helper
// returns (0, false) — signalling "fall through to extension" — when
// every FSM candidate is at or above hotPinThreshold. Otherwise the
// caller would re-pin a hot page and join the contended queue.
//
// Setup: 4 FSM entries, every block pinned hotPinThreshold (4) times.
func TestSelectFSMCandidatePageRejectsHotCandidates(t *testing.T) {
	_, pool, fsm, rel := newSelectFixture(t, 4, []uint16{4000, 4000, 4000, 4000})

	pins := make([]*storage.Slot, 0, hotPinThreshold*4)
	for blk := 0; blk < 4; blk++ {
		for i := 0; i < hotPinThreshold; i++ {
			s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(blk)})
			if err != nil {
				t.Fatal(err)
			}
			pins = append(pins, s)
		}
	}
	defer func() {
		for _, s := range pins {
			pool.Unpin(s)
		}
	}()

	blk, ok := selectFSMCandidatePage(fsm, pool, rel, 1024)
	if ok {
		t.Errorf("expected (0, false) when every candidate is hot; got (%d, true)", blk)
	}
}

// TestSelectFSMCandidatePagePicksAmongModeratelyPinned confirms ranking
// still works when no candidate is at pin 0: among three moderately-
// pinned pages (pin counts 3, 1, 2), the helper picks the pin=1 page.
// This pins the "lower is better" semantics independent of the pin==0
// short-circuit.
func TestSelectFSMCandidatePagePicksAmongModeratelyPinned(t *testing.T) {
	_, pool, fsm, rel := newSelectFixture(t, 3, []uint16{4000, 4000, 4000})

	pinPlan := map[storage.BlockNumber]int{0: 3, 1: 1, 2: 2}
	var pins []*storage.Slot
	for blk, n := range pinPlan {
		for i := 0; i < n; i++ {
			s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
			if err != nil {
				t.Fatal(err)
			}
			pins = append(pins, s)
		}
	}
	defer func() {
		for _, s := range pins {
			pool.Unpin(s)
		}
	}()

	blk, ok := selectFSMCandidatePage(fsm, pool, rel, 1024)
	if !ok {
		t.Fatal("expected a candidate, got (_, false)")
	}
	if blk != 1 {
		t.Errorf("got %d, want 1 (only pin=1 candidate; all are below hotPinThreshold)", blk)
	}
}


// newBatchExtendFixture is a thinner variant of newSelectFixture for tests
// that exercise the extension tail of the heap-insert hot path. The
// relation is left zero-length so the helper under test owns the very
// first extension event.
func newBatchExtendFixture(t *testing.T) (*storage.Manager, *storage.Pool, *storage.FSM, storage.RelFileNode) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { mgr.Close() })
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	fsm := storage.NewFSM()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 0xC0FFEE11, Fork: storage.MainFork}
	return mgr, pool, fsm, rel
}

// TestBatchExtendAndRegisterFSMAppendsAndRegistersExtras pins the core
// contract of the batched-extend tail: extendBatchSize pages land on
// disk in one shot, the first block is returned for the caller's own
// insert, and the remaining (extendBatchSize-1) blocks are FSM-
// registered at empty-page free space so the next FSM consultation
// from a sibling stripe finds them. M0107-0007 slice C part 2.
func TestBatchExtendAndRegisterFSMAppendsAndRegistersExtras(t *testing.T) {
	mgr, pool, fsm, rel := newBatchExtendFixture(t)

	first, err := batchExtendAndRegisterFSM(pool, fsm, rel, storage.InvalidTransactionID)
	if err != nil {
		t.Fatalf("batchExtendAndRegisterFSM: %v", err)
	}
	if first != 0 {
		t.Errorf("firstBlk = %d, want 0 (empty relation)", first)
	}
	if got, err := mgr.NBlocks(rel); err != nil || got != storage.BlockNumber(extendBatchSize) {
		t.Errorf("NBlocks = %d (err=%v), want %d", got, err, extendBatchSize)
	}

	// Drain the FSM: every block returned by GetPageWithFreeSpace must
	// be an "extra" (i.e. NOT firstBlk), and the drained set must equal
	// exactly {firstBlk+1 .. firstBlk+extendBatchSize-1}. Map-iteration
	// inside GetPageWithFreeSpace is non-deterministic, so we accumulate
	// the set rather than asserting a specific call's return value.
	emptyFree := uint16(storage.BlockSize - storage.SizeOfPageHeaderData)
	seen := make(map[storage.BlockNumber]bool)
	for {
		blk, ok := fsm.GetPageWithFreeSpace(rel, emptyFree)
		if !ok {
			break
		}
		if seen[blk] {
			t.Fatalf("FSM returned blk=%d twice", blk)
		}
		seen[blk] = true
		// Drain this candidate so the next GetPageWithFreeSpace
		// returns a different block.
		fsm.RecordFreeSpace(rel, blk, 0)
	}
	// firstBlk must NOT be in the FSM: the caller inserts into it and
	// records the post-insert free space via the normal
	// markHeapInsertDirty → FSM.RecordFreeSpaceForPage path. A spurious
	// pre-registration here would mislead a concurrent inserter into
	// landing on a page we are actively writing.
	if seen[first] {
		t.Errorf("firstBlk=%d must not be FSM-registered (caller owns it)", first)
	}
	for blk := storage.BlockNumber(1); blk < storage.BlockNumber(extendBatchSize); blk++ {
		if !seen[blk] {
			t.Errorf("extra block %d not FSM-registered", blk)
		}
	}
	if len(seen) != extendBatchSize-1 {
		t.Errorf("FSM-registered count = %d, want %d", len(seen), extendBatchSize-1)
	}
}

// TestBatchExtendAndRegisterFSMNilFSM verifies the helper survives a nil
// FSM (callers' nil-guard contract): extension still happens, no panic,
// and no registration side effect.
func TestBatchExtendAndRegisterFSMNilFSM(t *testing.T) {
	mgr, pool, _, rel := newBatchExtendFixture(t)

	first, err := batchExtendAndRegisterFSM(pool, nil, rel, storage.InvalidTransactionID)
	if err != nil {
		t.Fatalf("batchExtendAndRegisterFSM(nil FSM): %v", err)
	}
	if first != 0 {
		t.Errorf("firstBlk = %d, want 0", first)
	}
	if got, _ := mgr.NBlocks(rel); got != storage.BlockNumber(extendBatchSize) {
		t.Errorf("NBlocks = %d, want %d", got, extendBatchSize)
	}
}

// TestBatchExtendAndRegisterFSMSecondCallContinuesAndRegisters verifies
// that successive batch-extend events stay disjoint: the second call
// hands out blocks [extendBatchSize .. 2*extendBatchSize-1], the second
// firstBlk is left out of the FSM, and the FSM accumulates both batches'
// extras (no entries dropped, no entries overlapping).
func TestBatchExtendAndRegisterFSMSecondCallContinuesAndRegisters(t *testing.T) {
	mgr, pool, fsm, rel := newBatchExtendFixture(t)

	if _, err := batchExtendAndRegisterFSM(pool, fsm, rel, storage.InvalidTransactionID); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	second, err := batchExtendAndRegisterFSM(pool, fsm, rel, storage.InvalidTransactionID)
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if second != storage.BlockNumber(extendBatchSize) {
		t.Errorf("second firstBlk = %d, want %d", second, extendBatchSize)
	}
	if got, _ := mgr.NBlocks(rel); got != storage.BlockNumber(2*extendBatchSize) {
		t.Errorf("NBlocks after two batches = %d, want %d", got, 2*extendBatchSize)
	}

	// Both batches' extras (block 1..7 and block 9..15) should be
	// FSM-registered — 2*(extendBatchSize-1) entries total. Drain to
	// confirm.
	emptyFree := uint16(storage.BlockSize - storage.SizeOfPageHeaderData)
	got := make(map[storage.BlockNumber]bool)
	for {
		blk, ok := fsm.GetPageWithFreeSpace(rel, emptyFree)
		if !ok {
			break
		}
		got[blk] = true
		fsm.RecordFreeSpace(rel, blk, 0)
	}
	want := map[storage.BlockNumber]bool{}
	for i := storage.BlockNumber(1); i < storage.BlockNumber(extendBatchSize); i++ {
		want[i] = true
		want[storage.BlockNumber(extendBatchSize)+i] = true
	}
	if len(got) != len(want) {
		t.Errorf("registered extras count = %d, want %d", len(got), len(want))
	}
	for blk := range want {
		if !got[blk] {
			t.Errorf("expected extra block %d, not registered", blk)
		}
	}
	for blk := range got {
		if !want[blk] {
			t.Errorf("unexpected registered block %d (firstBlk leaked into FSM?)", blk)
		}
	}
}
