package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// E-19 slice S4 — the window's own accounting.
//
// §4.2 hazard 1 is "a prefetch that pins and never unpins", and the design
// names three arms: a scan run to EOF, a scan closed mid-way, and a scan that
// hits an injected read error. All three are assertions about the pin SUM, and
// none of them is visible to any values suite: a leaked pin is not a wrong
// answer, it is a buffer that can never be evicted again.

func windowTestPool(t *testing.T, slots, blocks int) (*storage.Manager, *storage.Pool, storage.RelFileNode) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	t.Cleanup(func() { mgr.Close() })
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4242, Fork: storage.MainFork}
	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < blocks; i++ {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatalf("Extend %d: %v", i, err)
		}
	}
	pool.InvalidateRel(rel) // start cold, as a real scan would
	return mgr, pool, rel
}

func blocksUpTo(n int) []storage.BlockNumber {
	b := make([]storage.BlockNumber, n)
	for i := range b {
		b[i] = storage.BlockNumber(i)
	}
	return b
}

// TestPrefetchWindowIsInertAtDepthZero is the default-off gate. §4.3: the knob
// defaults to 0 and the mechanism must then be indistinguishable from its
// absence — not "cheap", absent. A nil window is what every call site sees.
func TestPrefetchWindowIsInertAtDepthZero(t *testing.T) {
	_, pool, rel := windowTestPool(t, 32, 8)
	if w := newHeapPrefetchWindow(pool, rel, 0); w != nil {
		t.Fatalf("depth 0 built a window (%+v); it must be nil", w)
	}
	if w := newHeapPrefetchWindow(pool, rel, -1); w != nil {
		t.Fatal("a negative depth built a window")
	}
	// Every method must be safe on the nil window, because the operator calls
	// them unconditionally rather than guarding four call sites.
	var nilw *heapPrefetchWindow
	nilw.refill(blocksUpTo(4))
	if s := nilw.take(0); s != nil {
		t.Fatal("nil window returned a slot")
	}
	nilw.drain()
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("inert window took %d pins", total)
	}
}

// TestPrefetchWindowConsumedToEOFIsBalanced is arm 1: a scan that walks every
// block and hands every slot back.
func TestPrefetchWindowConsumedToEOFIsBalanced(t *testing.T) {
	_, pool, rel := windowTestPool(t, 64, 12)
	w := newHeapPrefetchWindow(pool, rel, 4)
	if w == nil {
		t.Fatal("depth 4 on a 64-slot pool produced no window")
	}
	all := blocksUpTo(12)

	for i, blk := range all {
		w.refill(all[i:])
		slot := w.take(blk)
		if slot == nil {
			t.Fatalf("block %d: window did not have the block it had just queued", blk)
		}
		if len(w.ops) > w.depth {
			t.Fatalf("window grew to %d, past its depth %d", len(w.ops), w.depth)
		}
		pool.Unpin(slot)
	}
	w.drain()
	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("EOF arm leaked %d pins over %d slots", total, pinned)
	}
	if w.consumed != 12 {
		t.Fatalf("consumed %d of 12 blocks from the window", w.consumed)
	}
}

// TestPrefetchWindowDrainedMidScanIsBalanced is arm 2: the mid-scan Close /
// Rescan case, which is the one that matters most in practice because a bitmap
// heap scan under a nested loop is Rescanned once per outer row.
func TestPrefetchWindowDrainedMidScanIsBalanced(t *testing.T) {
	_, pool, rel := windowTestPool(t, 64, 20)
	w := newHeapPrefetchWindow(pool, rel, 6)
	all := blocksUpTo(20)

	for i := 0; i < 5; i++ {
		w.refill(all[i:])
		if slot := w.take(all[i]); slot != nil {
			pool.Unpin(slot)
		}
	}
	if len(w.ops) == 0 {
		t.Fatal("nothing outstanding mid-scan; the arm would prove nothing")
	}
	outstanding := len(w.ops)
	w.drain()
	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("mid-scan drain of %d outstanding ops leaked %d pins over %d slots",
			outstanding, total, pinned)
	}
	// Draining twice must be safe: Close runs after Rescan may already have.
	w.drain()
}

// TestPrefetchWindowSurvivesAnInjectedReadError is arm 3, and the reason S2
// added the fault seam: an I/O error inside a look-ahead must not leak the pin
// that the look-ahead took, and must not turn the scan's error into a
// different one.
func TestPrefetchWindowSurvivesAnInjectedReadError(t *testing.T) {
	_, pool, rel := windowTestPool(t, 64, 10)
	boom := errors.New("injected")
	pool.DebugReadFault = func(tag storage.BufferTag) error {
		if tag.Block == 3 {
			return boom
		}
		return nil
	}
	w := newHeapPrefetchWindow(pool, rel, 5)
	all := blocksUpTo(10)

	w.refill(all)
	// Blocks 0..2 come out of the window; block 3's read fails, and take must
	// report "not in the window" so the caller's own Pin reproduces the error.
	for _, blk := range all[:3] {
		s := w.take(blk)
		if s == nil {
			t.Fatalf("block %d missing from a freshly refilled window", blk)
		}
		pool.Unpin(s)
	}
	if s := w.take(3); s != nil {
		pool.Unpin(s)
		t.Fatal("take returned a slot for a block whose read failed")
	}
	if _, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 3}); !errors.Is(err, boom) {
		t.Fatalf("the caller's own Pin reported %v, not the underlying error", err)
	}
	w.drain()
	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("injected-error arm leaked %d pins over %d slots", total, pinned)
	}
}

// TestPrefetchWindowDropsBlocksTheScanSkipped covers the stale-head case: a
// bitmap scan can skip a queued page (an all-dead page, a rescan boundary), and
// the window must drop it rather than hand it to the wrong block or hold its
// pin for the rest of the scan.
func TestPrefetchWindowDropsBlocksTheScanSkipped(t *testing.T) {
	_, pool, rel := windowTestPool(t, 64, 10)
	w := newHeapPrefetchWindow(pool, rel, 6)
	w.refill(blocksUpTo(10))
	queued := len(w.ops)
	if queued < 4 {
		t.Fatalf("only %d ops queued; the test needs a fuller window", queued)
	}

	// Jump straight to the last queued block: everything before it is stale.
	target := w.ops[queued-1].Tag().Block
	slot := w.take(target)
	if slot == nil {
		t.Fatalf("window did not yield block %d", target)
	}
	pool.Unpin(slot)
	if w.dropped != int64(queued-1) {
		t.Fatalf("dropped %d stale ops, want %d", w.dropped, queued-1)
	}
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("stale ops kept %d pins", total)
	}
	w.drain()
}

// TestPrefetchWindowDepthIsCappedByThePool is §4.2 hazard 2's structural half:
// StartRead claims victims, so an unbounded window IS eviction pressure. The
// window is capped at a fraction of the pool and floored at one, transcribing
// read_stream.c's shrink-and-guarantee-progress shape rather than declining.
func TestPrefetchWindowDepthIsCappedByThePool(t *testing.T) {
	_, pool, rel := windowTestPool(t, 16, 40)
	w := newHeapPrefetchWindow(pool, rel, storage.MaxIOCombineLimit)
	if w == nil {
		t.Fatal("a large depth on a small pool produced no window; it must shrink, not decline")
	}
	if w.depth > pool.Capacity()/8 || w.depth < 1 {
		t.Fatalf("depth %d is not within [1, capacity/8=%d]", w.depth, pool.Capacity()/8)
	}

	// And the eviction delta is bounded by the depth: walking the whole
	// relation with a window must not evict more than the same walk without
	// one, plus the window's own depth.
	base := pool.EvictionCount()
	all := blocksUpTo(40)
	for i, blk := range all {
		w.refill(all[i:])
		if s := w.take(blk); s != nil {
			pool.Unpin(s)
		} else {
			s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
			if err != nil {
				t.Fatal(err)
			}
			pool.Unpin(s)
		}
	}
	w.drain()
	withWindow := pool.EvictionCount() - base

	// Control: the same walk, no window, on a fresh pool of the same size.
	_, pool2, rel2 := windowTestPool(t, 16, 40)
	base2 := pool2.EvictionCount()
	for _, blk := range all {
		s, err := pool2.Pin(storage.BufferTag{Rel: rel2, Block: blk})
		if err != nil {
			t.Fatal(err)
		}
		pool2.Unpin(s)
	}
	noWindow := pool2.EvictionCount() - base2

	if withWindow > noWindow+int64(w.depth) {
		t.Fatalf("window of depth %d evicted %d against the control's %d — "+
			"more than depth extra (E-19 §4.2 hazard 2)", w.depth, withWindow, noWindow)
	}
	t.Logf("E-19 S4: evictions with a depth-%d window = %d, control = %d", w.depth, withWindow, noWindow)
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("eviction arm leaked %d pins", total)
	}
}

// TestHeapPrefetchDepthKnobFailsClosed: every failure mode of this feature is
// a buffer-pool accounting bug rather than a wrong answer, so a knob nobody
// can parse must mean OFF.
func TestHeapPrefetchDepthKnobFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want int
	}{
		{"", 0}, {"0", 0}, {"-3", 0}, {"banana", 0}, {"8", 8},
		{"100000", maxPrefetchDepth},
	} {
		t.Setenv(prefetchDepthEnv, tc.val)
		if got := heapPrefetchDepth(); got != tc.want {
			t.Errorf("%s=%q -> depth %d, want %d", prefetchDepthEnv, tc.val, got, tc.want)
		}
	}
}
