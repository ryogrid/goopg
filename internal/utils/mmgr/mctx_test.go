package mmgr

import (
	"testing"
	"unsafe"
)

func TestContextSizeof(t *testing.T) {
	// Context must be ≤ 96 B per design doc §10.
	got := unsafe.Sizeof(Context{})
	if got > 96 {
		t.Fatalf("Context size %d > 96 B", got)
	}
}

func TestAcquireRelease(t *testing.T) {
	sess := Acquire(nil, KindSession)
	if sess.id == InvalidContextID {
		t.Fatal("expected valid ID")
	}
	stmt := Acquire(sess, KindStmt)
	if stmt.id == InvalidContextID {
		t.Fatal("expected valid stmt ID")
	}
	if len(sess.children) != 1 {
		t.Fatalf("sess.children=%d, want 1", len(sess.children))
	}
	stmt.Release()
	// After release, sess.children should shrink.
	if len(sess.children) != 0 {
		t.Fatalf("sess.children=%d after stmt release, want 0", len(sess.children))
	}
	sess.Release()
}

func TestAllocAndBytes(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()

	payload := []byte("hello, mctx")
	off, length := c.AllocBytes(payload)
	got := c.Bytes(off, length)
	if string(got) != "hello, mctx" {
		t.Fatalf("Bytes returned %q, want %q", got, "hello, mctx")
	}
}

func TestAllocString(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()

	off, length := c.AllocString("world")
	got := c.Bytes(off, length)
	if string(got) != "world" {
		t.Fatalf("got %q, want %q", got, "world")
	}
}

func TestReset(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()

	off, length := c.AllocBytes([]byte("before-reset"))
	c.Reset()
	// After reset, new allocation reuses same offset.
	off2, length2 := c.AllocBytes([]byte("after-reset"))
	if off != off2 {
		t.Fatalf("offset after reset: got %d, want %d (reuse)", off2, off)
	}
	got := c.Bytes(off2, length2)
	if string(got) != "after-reset" {
		t.Fatalf("got %q after reset", got)
	}
	_ = length // suppress unused
}

func TestResetCascadesToChild(t *testing.T) {
	parent := Acquire(nil, KindStmt)
	defer parent.Release()
	child := Acquire(parent, KindExpr)
	off, length := child.AllocBytes([]byte("child-data"))
	parentGen := parent.gen
	childGen := child.gen
	parent.Reset()
	if parent.gen != parentGen+1 {
		t.Fatalf("parent gen not bumped")
	}
	if child.gen != childGen+1 {
		t.Fatalf("child gen not bumped on parent Reset")
	}
	// Re-allocate after reset.
	off2, length2 := child.AllocBytes([]byte("new-child"))
	got := child.Bytes(off2, length2)
	if string(got) != "new-child" {
		t.Fatalf("got %q, want new-child", got)
	}
	_ = off
	_ = length
}

func TestOversizedAlloc(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()

	// Allocate something smaller first to partially fill chunk 0.
	small := []byte("small")
	_, _ = c.AllocBytes(small)

	// Allocate something larger than defaultChunkSize.
	big := make([]byte, defaultChunkSize+100)
	for i := range big {
		big[i] = byte(i & 0xFF)
	}
	off, length := c.AllocBytes(big)
	got := c.Bytes(off, length)
	if len(got) != len(big) {
		t.Fatalf("got len %d, want %d", len(got), len(big))
	}
	for i, b := range got {
		if b != byte(i&0xFF) {
			t.Fatalf("byte %d: got %d, want %d", i, b, byte(i&0xFF))
		}
	}
}

// TestOversizedChunkNeverEntersSizePool guards the invariant the (offset,
// length) encoding rests on: every chunk in a size pool has cap exactly ==
// that pool's size class. offset is chunkIdx*c.cs + offsetWithinChunk, so a
// recycled chunk with cap > c.cs lets an allocation land at an in-chunk
// offset >= c.cs, which aliases to chunkIdx+1 and makes Bytes() resolve into
// a different (often nonexistent) chunk. growChunk allocates such oversized
// chunks by design (make([]byte, 0, n) for n > cs) and Release hands every
// chunk to putChunk keyed by c.cs, so the leak needs an explicit guard.
// Regression gate for the load-sensitive internal/mctx race-gate failure
// (AI-20260810-011258, `TestMultipleChunks: second chunk: got ""`).
func TestOversizedChunkNeverEntersSizePool(t *testing.T) {
	putChunk(defaultChunkSize, make([]byte, 0, defaultChunkSize+100))
	putChunk(smallChunkSize, make([]byte, 0, smallChunkSize+100))
	for _, cs := range []uint32{defaultChunkSize, smallChunkSize} {
		for i := 0; i < 64; i++ {
			if got := cap(getChunk(cs)); got != int(cs) {
				t.Fatalf("getChunk(%d) returned cap %d: an oversized chunk leaked into the size pool", cs, got)
			}
		}
	}
}

// TestAllocBytesRoundTripAcrossChunks poisons the size pool the way a released
// context holding an oversized chunk used to, then checks every allocation
// still resolves to its own bytes across several chunk boundaries. Before the
// putChunk cap guard this failed with Bytes() returning nil for the first
// allocation whose in-chunk offset reached c.cs.
func TestAllocBytesRoundTripAcrossChunks(t *testing.T) {
	putChunk(defaultChunkSize, make([]byte, 0, defaultChunkSize+4096))

	c := Acquire(nil, KindStmt)
	defer c.Release()

	const blk = 1024
	nBlocks := 4 * defaultChunkSize / blk
	offs := make([]uint32, nBlocks)
	lens := make([]uint32, nBlocks)
	for i := 0; i < nBlocks; i++ {
		payload := make([]byte, blk)
		for j := range payload {
			payload[j] = byte(i + j)
		}
		offs[i], lens[i] = c.AllocBytes(payload)
		// Read back immediately: a stale read here is an aliasing bug, not a
		// lifetime bug.
		got := c.Bytes(offs[i], lens[i])
		if len(got) != blk {
			t.Fatalf("block %d: Bytes returned %d bytes, want %d (offset %d aliased outside its chunk)", i, len(got), blk, offs[i])
		}
		for j := range got {
			if got[j] != byte(i+j) {
				t.Fatalf("block %d byte %d: got %d, want %d", i, j, got[j], byte(i+j))
			}
		}
	}
	// And again after all allocation, so a late growChunk that memmoves the
	// chunk tail cannot invalidate an earlier offset.
	for i := 0; i < nBlocks; i++ {
		got := c.Bytes(offs[i], lens[i])
		if len(got) != blk || got[0] != byte(i) {
			t.Fatalf("block %d re-read: got %d bytes, first byte %v", i, len(got), got)
		}
	}
}

func TestMultipleChunks(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()

	// Force multiple chunks by filling beyond defaultChunkSize.
	chunkFull := make([]byte, defaultChunkSize)
	off1, len1 := c.AllocBytes(chunkFull)
	off2, len2 := c.AllocBytes([]byte("second-chunk"))
	if off1/c.cs == off2/c.cs {
		t.Fatal("expected different chunks")
	}
	got2 := c.Bytes(off2, len2)
	if string(got2) != "second-chunk" {
		t.Fatalf("second chunk: got %q", got2)
	}
	_ = len1
}

func TestLookup(t *testing.T) {
	c := Acquire(nil, KindStmt)
	id := c.ID()
	got := Lookup(id)
	if got != c {
		t.Fatal("Lookup returned wrong context")
	}
	c.Release()
	if Lookup(id) != nil {
		t.Fatal("Lookup after Release should return nil")
	}
}

func TestPerm(t *testing.T) {
	p := Perm()
	if p == nil {
		t.Fatal("Perm() returned nil")
	}
	if p.id != PermContextID {
		t.Fatalf("Perm id=%d, want %d", p.id, PermContextID)
	}
	off, length := p.AllocString("literal")
	got := p.Bytes(off, length)
	if string(got) != "literal" {
		t.Fatalf("got %q", got)
	}
}

func TestAllocFor(t *testing.T) {
	type pair struct {
		A, B int64
	}
	c := Acquire(nil, KindStmt)
	defer c.Release()
	p := AllocFor[pair](c)
	p.A = 42
	p.B = 99
	if p.A != 42 || p.B != 99 {
		t.Fatalf("AllocFor: got (%d,%d)", p.A, p.B)
	}
}

func TestAllocSlice(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()
	s := AllocSlice[int32](c, 10)
	if len(s) != 10 {
		t.Fatalf("AllocSlice len=%d, want 10", len(s))
	}
	s[9] = 77
	if s[9] != 77 {
		t.Fatal("AllocSlice: write not retained")
	}
}

func TestZeroLengthAlloc(t *testing.T) {
	c := Acquire(nil, KindStmt)
	defer c.Release()
	got := c.Alloc(0)
	if got != nil {
		t.Fatal("Alloc(0) should return nil")
	}
	off, length := c.AllocBytes(nil)
	if off != 0 || length != 0 {
		t.Fatalf("AllocBytes(nil): (%d,%d)", off, length)
	}
	if c.Bytes(0, 0) != nil {
		t.Fatal("Bytes(0,0) should return nil")
	}
}
