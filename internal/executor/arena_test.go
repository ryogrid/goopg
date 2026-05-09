package executor

import (
	"bytes"
	"testing"
)

// TestM0072ArenaAllocateRead covers the basic happy path:
// allocate small slices, write into them, read back unchanged.
func TestM0072ArenaAllocateRead(t *testing.T) {
	a := NewArena(0)

	s1 := a.Allocate(8)
	copy(s1, []byte("abcdefgh"))
	s2 := a.Allocate(4)
	copy(s2, []byte("WXYZ"))
	s3 := a.Allocate(16)
	copy(s3, []byte("0123456789ABCDEF"))

	if !bytes.Equal(s1, []byte("abcdefgh")) {
		t.Errorf("s1 readback mismatch: %q", s1)
	}
	if !bytes.Equal(s2, []byte("WXYZ")) {
		t.Errorf("s2 readback mismatch: %q", s2)
	}
	if !bytes.Equal(s3, []byte("0123456789ABCDEF")) {
		t.Errorf("s3 readback mismatch: %q", s3)
	}
	if got := a.TotalAllocated(); got != 28 {
		t.Errorf("TotalAllocated = %d, want 28", got)
	}
}

// TestM0072ArenaResetReuses pins that Reset rewinds the cursor
// and reuses the same page memory rather than reallocating —
// the per-batch hot-loop scenario.
func TestM0072ArenaResetReuses(t *testing.T) {
	a := NewArena(0)

	for i := 0; i < 10; i++ {
		s := a.Allocate(32)
		copy(s, bytes.Repeat([]byte{byte('A' + i)}, 32))
	}
	pagesBefore := a.PageCount()
	if pagesBefore == 0 {
		t.Fatalf("expected at least 1 page, got 0")
	}

	a.Reset()
	if got := a.TotalAllocated(); got != 0 {
		t.Errorf("after Reset, TotalAllocated = %d, want 0", got)
	}

	// New allocations should reuse the existing page memory.
	s := a.Allocate(16)
	copy(s, []byte("after-reset-payload"[:16]))
	if got := a.PageCount(); got != pagesBefore {
		t.Errorf("PageCount after Reset+Allocate: got %d, want unchanged %d",
			got, pagesBefore)
	}
	if !bytes.Equal(s, []byte("after-reset-payl")) {
		t.Errorf("post-reset readback mismatch: %q", s)
	}
}

// TestM0072ArenaPageGrowth pins that exceeding a single page's
// capacity grows to a second page transparently and the
// previously-returned slice is unaffected.
func TestM0072ArenaPageGrowth(t *testing.T) {
	a := NewArena(64) // tiny pages to force growth

	s1 := a.Allocate(48)
	copy(s1, bytes.Repeat([]byte{'X'}, 48))

	// 48 + 32 = 80 > 64 → should land in page 2 and leave s1
	// intact in page 1.
	s2 := a.Allocate(32)
	copy(s2, bytes.Repeat([]byte{'Y'}, 32))

	if !bytes.Equal(s1, bytes.Repeat([]byte{'X'}, 48)) {
		t.Errorf("s1 corrupted after page-2 allocation: %q", s1)
	}
	if !bytes.Equal(s2, bytes.Repeat([]byte{'Y'}, 32)) {
		t.Errorf("s2 readback mismatch: %q", s2)
	}
	if a.PageCount() < 2 {
		t.Errorf("expected ≥ 2 pages after growth, got %d", a.PageCount())
	}
}

// TestM0072ArenaOversizedAllocation pins the dedicated-page
// fallback for payloads larger than the configured pageSize.
func TestM0072ArenaOversizedAllocation(t *testing.T) {
	a := NewArena(64)

	// Small allocation establishes the active small page.
	s1 := a.Allocate(16)
	copy(s1, []byte("0123456789ABCDEF"))

	// Oversized payload (256 > 64) gets its own dedicated page.
	big := a.Allocate(256)
	copy(big, bytes.Repeat([]byte{'B'}, 256))

	// Subsequent small allocation should still land in the
	// original active page (s2 fits next to s1).
	s2 := a.Allocate(16)
	copy(s2, bytes.Repeat([]byte{'S'}, 16))

	if !bytes.Equal(s1, []byte("0123456789ABCDEF")) {
		t.Errorf("s1 corrupted after oversized + small alloc: %q", s1)
	}
	if !bytes.Equal(big, bytes.Repeat([]byte{'B'}, 256)) {
		t.Errorf("big payload corrupted: len=%d", len(big))
	}
	if !bytes.Equal(s2, bytes.Repeat([]byte{'S'}, 16)) {
		t.Errorf("s2 readback mismatch: %q", s2)
	}
}

// TestM0072ArenaZeroLengthAllocation pins the n=0 contract:
// returns nil so callers don't get a stale page reference.
func TestM0072ArenaZeroLengthAllocation(t *testing.T) {
	a := NewArena(0)
	if got := a.Allocate(0); got != nil {
		t.Errorf("Allocate(0) = %v, want nil", got)
	}
}

// TestM0072ArenaDropResets pins that Drop releases page memory;
// subsequent Allocate calls re-grow from scratch.
func TestM0072ArenaDropResets(t *testing.T) {
	a := NewArena(0)
	a.Allocate(100)
	if a.PageCount() == 0 {
		t.Fatalf("setup: expected ≥ 1 page after Allocate")
	}

	a.Drop()
	if got := a.PageCount(); got != 0 {
		t.Errorf("after Drop, PageCount = %d, want 0", got)
	}

	// Reusable after Drop.
	s := a.Allocate(8)
	if len(s) != 8 {
		t.Errorf("post-Drop Allocate(8) returned len %d", len(s))
	}
	if a.PageCount() == 0 {
		t.Errorf("post-Drop Allocate did not allocate a fresh page")
	}
}
