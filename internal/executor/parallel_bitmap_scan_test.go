package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestParallelBitmapStateEmpty verifies that an uninitialised state returns
// no pages.
func TestParallelBitmapStateEmpty(t *testing.T) {
	pbm := newParallelBitmapState()
	_, _, ok := pbm.nextPage()
	if ok {
		t.Error("uninitialised state should return no pages")
	}
	if n := pbm.claimed(); n != 0 {
		t.Errorf("uninitialised state claimed %d, want 0", n)
	}
}

// TestParallelBitmapStateSinglePage verifies that a state with one exact page
// hands it out exactly once.
func TestParallelBitmapStateSinglePage(t *testing.T) {
	tbm := &TIDBitmap{}
	tbmAddTuples(tbm, []storage.ItemPointer{
		{Block: 42, Offset: 1},
		{Block: 42, Offset: 3},
	}, false)

	pbm := newParallelBitmapState()
	pbm.init(tbm)

	// First call should return the only page.
	block, entry, ok := pbm.nextPage()
	if !ok {
		t.Fatal("first call should return a page")
	}
	if block != 42 {
		t.Errorf("block = %d, want 42", block)
	}
	if entry == nil {
		t.Fatal("entry should not be nil")
	}
	if entry.isLossy {
		t.Error("entry should be exact, not lossy")
	}

	// Second call should be exhausted.
	_, _, ok = pbm.nextPage()
	if ok {
		t.Error("second call should be exhausted")
	}
	// claimed should be 2 (one success + one past-end).
	if n := pbm.claimed(); n != 2 {
		t.Errorf("claimed = %d, want 2", n)
	}
}

// TestParallelBitmapStateMultiplePages verifies that multiple pages are
// handed out in sorted order.
func TestParallelBitmapStateMultiplePages(t *testing.T) {
	tbm := &TIDBitmap{}
	tbmAddTuples(tbm, []storage.ItemPointer{
		{Block: 100, Offset: 1},
		{Block: 50, Offset: 1},
		{Block: 75, Offset: 1},
	}, false)

	pbm := newParallelBitmapState()
	pbm.init(tbm)

	seen := make(map[storage.BlockNumber]bool)
	for i := 0; i < 3; i++ {
		block, _, ok := pbm.nextPage()
		if !ok {
			t.Fatalf("call %d should return a page", i)
		}
		if seen[block] {
			t.Errorf("block %d returned twice", block)
		}
		seen[block] = true
	}

	// Should be exhausted.
	_, _, ok := pbm.nextPage()
	if ok {
		t.Error("fourth call should be exhausted")
	}

	// Verify all three blocks were seen (regardless of order within sorted).
	if len(seen) != 3 {
		t.Errorf("saw %d unique blocks, want 3", len(seen))
	}
}

// TestParallelBitmapStateLossyPage verifies that lossy pages are returned
// correctly.
func TestParallelBitmapStateLossyPage(t *testing.T) {
	tbm := &TIDBitmap{maxEntries: 1} // force lossification with low ceiling
	tbmAddTuples(tbm, []storage.ItemPointer{
		{Block: 1, Offset: 1},
		{Block: 1, Offset: 2},
	}, false)
	// Force the page to lossy: 1 exact page (5 effective entries) > 1 max.
	tbmLossify(tbm)

	// Re-verify after lossification.
	pbm := newParallelBitmapState()
	pbm.init(tbm)

	block, entry, ok := pbm.nextPage()
	if !ok {
		t.Fatal("should return the lossy page")
	}
	if block != 1 {
		t.Errorf("block = %d, want 1", block)
	}
	if !entry.isLossy {
		t.Error("entry should be lossy")
	}
}

// TestParallelBitmapStateNilSafety verifies that methods on a nil state are
// safe.
func TestParallelBitmapStateNilSafety(t *testing.T) {
	var pbm *parallelBitmapState
	// init on nil is a no-op.
	pbm.init(nil)

	// nextPage on nil returns false.
	_, _, ok := pbm.nextPage()
	if ok {
		t.Error("nil state nextPage should return false")
	}

	// claimed on nil returns 0.
	if n := pbm.claimed(); n != 0 {
		t.Errorf("nil state claimed = %d, want 0", n)
	}
}

// TestParallelBitmapStateInitIdempotent verifies that init is idempotent.
func TestParallelBitmapStateInitIdempotent(t *testing.T) {
	tbm1 := &TIDBitmap{}
	tbmAddTuples(tbm1, []storage.ItemPointer{{Block: 1, Offset: 1}}, false)

	tbm2 := &TIDBitmap{}
	tbmAddTuples(tbm2, []storage.ItemPointer{{Block: 2, Offset: 1}}, false)

	pbm := newParallelBitmapState()
	pbm.init(tbm1)
	// Second init should be a no-op.
	pbm.init(tbm2)

	// Should only see pages from tbm1.
	block, _, ok := pbm.nextPage()
	if !ok {
		t.Fatal("should return page from first init")
	}
	if block != 1 {
		t.Errorf("block = %d, want 1 (should be from first init)", block)
	}

	// Exhausted — no page from tbm2.
	_, _, ok = pbm.nextPage()
	if ok {
		t.Error("should be exhausted, init was idempotent")
	}
}
