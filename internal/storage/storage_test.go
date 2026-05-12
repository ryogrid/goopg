package storage

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type recordingWAL struct {
	calls []uint64
	err   error
}

func (w *recordingWAL) FlushUpTo(lsn uint64) error {
	w.calls = append(w.calls, lsn)
	if w.err != nil {
		return w.err
	}
	return nil
}

// TestInitPagePinsHeaderLayout verifies a freshly-initialised page has
// the byte-for-byte layout from docs/design/0006-storage-format.md.
func TestInitPagePinsHeaderLayout(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)
	if h.Lower() != SizeOfPageHeaderData {
		t.Errorf("Lower = %d, want %d", h.Lower(), SizeOfPageHeaderData)
	}
	if h.Upper() != BlockSize {
		t.Errorf("Upper = %d, want %d", h.Upper(), BlockSize)
	}
	if h.Special() != BlockSize {
		t.Errorf("Special = %d, want %d", h.Special(), BlockSize)
	}
	if h.PagesizeVersion() != pdPagesizeVersion {
		t.Errorf("PagesizeVersion = %#x, want %#x",
			h.PagesizeVersion(), pdPagesizeVersion)
	}
	if h.LSN() != 0 || h.Checksum() != 0 || h.Flags() != 0 || h.PruneXID() != 0 {
		t.Errorf("expected zero LSN/checksum/flags/pruneXID on fresh page; got %+v", h)
	}
	if h.FreeSpace() != BlockSize-SizeOfPageHeaderData {
		t.Errorf("FreeSpace = %d, want %d",
			h.FreeSpace(), BlockSize-SizeOfPageHeaderData)
	}
	// IsNew should be false for an InitPage'd page (Upper != 0).
	if IsNew(p) {
		t.Errorf("IsNew should be false after InitPage")
	}
	// IsNew should be true for an all-zero page.
	zero := make(Page, BlockSize)
	if !IsNew(zero) {
		t.Errorf("IsNew should be true for an all-zero page")
	}
}

// TestSMGRReadWriteRoundTrip pins the smgr file lifecycle: Extend grows
// the relation, ReadBlock returns what WriteBlock wrote, Sync doesn't
// error.
func TestSMGRReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 16385, Fork: MainFork}

	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	for i := SizeOfPageHeaderData; i < BlockSize; i++ {
		page[i] = byte(i % 251)
	}

	blk, err := mgr.Extend(rel, page)
	if err != nil {
		t.Fatal(err)
	}
	if blk != 0 {
		t.Errorf("first Extend returned blk=%d, want 0", blk)
	}
	if n, err := mgr.NBlocks(rel); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Errorf("NBlocks=%d, want 1", n)
	}

	got := make(Page, BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, page) {
		t.Errorf("ReadBlock returned different bytes than WriteBlock wrote")
	}

	// Modify page and overwrite; verify it round-trips.
	page[100] = 0xAB
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[100] != 0xAB {
		t.Errorf("post-write byte = %#x, want 0xAB", got[100])
	}

	if err := mgr.Sync(rel); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Out-of-range read returns ErrShortRead.
	if err := mgr.ReadBlock(rel, 5, got); !errors.Is(err, ErrShortRead) {
		t.Errorf("read past EOF = %v, want ErrShortRead", err)
	}

	// Confirm the file lives where we expect.
	want := filepath.Join(dir, "base", "1", "16385")
	if _, err := mgr.relFile(rel); err != nil {
		t.Fatal(err)
	}
	if mgr.files[rel].path != want {
		t.Errorf("relPath = %q, want %q", mgr.files[rel].path, want)
	}
}

// TestPoolPinReadsThroughSMGR verifies a pool Pin loads a page from
// smgr, and a second Pin on the same tag doesn't re-read.
func TestPoolPinReadsThroughSMGR(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 100, Fork: MainFork}

	// Stage: extend a block via smgr directly so the file exists.
	src := make(Page, BlockSize)
	_ = InitPage(src)
	src[100] = 0x42
	if _, err := mgr.Extend(rel, src); err != nil {
		t.Fatal(err)
	}

	tag := BufferTag{Rel: rel, Block: 0}
	s1, err := pool.Pin(tag)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Page()[100] != 0x42 {
		t.Errorf("page[100] = %#x, want 0x42", s1.Page()[100])
	}
	if !s1.valid {
		t.Errorf("slot not marked valid after read")
	}

	// Second Pin reuses the slot; verify pin count grew.
	s2, err := pool.Pin(tag)
	if err != nil {
		t.Fatal(err)
	}
	if s1 != s2 {
		t.Errorf("second Pin returned different slot")
	}
	if got := s2.pinCount.Load(); got != 2 {
		t.Errorf("pinCount = %d, want 2", got)
	}
	pool.Unpin(s1)
	pool.Unpin(s2)
}

// TestPoolDirtyEvictionWritesBack verifies that evicting a dirty slot
// flushes the page to smgr before reusing the slot.
func TestPoolDirtyEvictionWritesBack(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 200, Fork: MainFork}

	// Extend three blocks via PinNew so they exist on disk.
	for i := 0; i < 3; i++ {
		s, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatal(err)
		}
		// Mutate the page contents distinctly per block.
		s.Page()[100] = byte(0x10 + i)
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	// At this point the pool has 2 slots holding blocks 1 and 2; block 0
	// has already been evicted (and flushed) when block 2 was loaded.
	// Re-pin block 0 — it must still have the byte we wrote because
	// the flush wrote it back.
	s, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	if s.Page()[100] != 0x10 {
		t.Errorf("block 0 byte[100] = %#x, want 0x10 (dirty page must have been flushed before eviction)",
			s.Page()[100])
	}
	pool.Unpin(s)
}

// TestPoolPinAllExhaustsBuffers exercises the ErrNoBuffer path.
func TestPoolPinAllExhaustsBuffers(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 300, Fork: MainFork}

	// Extend 3 blocks.
	for i := 0; i < 3; i++ {
		s, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatal(err)
		}
		pool.Unpin(s)
	}

	// Pin two distinct blocks; pool is now full.
	a, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	b, err := pool.Pin(BufferTag{Rel: rel, Block: 1})
	if err != nil {
		t.Fatal(err)
	}
	// A third distinct pin must report ErrNoBuffer.
	if _, err := pool.Pin(BufferTag{Rel: rel, Block: 2}); !errors.Is(err, ErrNoBuffer) {
		t.Errorf("third Pin = %v, want ErrNoBuffer", err)
	}
	pool.Unpin(a)
	pool.Unpin(b)
	// After unpinning one, the third pin should succeed.
	if _, err := pool.Pin(BufferTag{Rel: rel, Block: 2}); err != nil {
		t.Errorf("third Pin after Unpin: %v", err)
	}
}

// TestPoolEvictHighUsageDoesNotSpuriouslyExhaust verifies that the
// clock sweep keeps searching long enough to find a victim when every
// unpinned slot has usageCount=maxUsageCount.
func TestPoolEvictHighUsageDoesNotSpuriouslyExhaust(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 301, Fork: MainFork}

	// Create 4 blocks directly in smgr so pinning block 3 requires
	// evicting one of the cached blocks 0..2.
	for i := 0; i < 4; i++ {
		pg := make(Page, BlockSize)
		if err := InitPage(pg); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.Extend(rel, pg); err != nil {
			t.Fatal(err)
		}
	}

	// Fill the pool with blocks 0..2.
	for blk := 0; blk < 3; blk++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(blk)})
		if err != nil {
			t.Fatal(err)
		}
		pool.Unpin(s)
	}

	// Re-pin enough times to saturate usageCount to maxUsageCount.
	for i := 0; i < int(maxUsageCount); i++ {
		for blk := 0; blk < 3; blk++ {
			s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(blk)})
			if err != nil {
				t.Fatal(err)
			}
			pool.Unpin(s)
		}
	}

	// The next distinct pin should evict after enough decrements,
	// not fail with ErrNoBuffer.
	s, err := pool.Pin(BufferTag{Rel: rel, Block: 3})
	if err != nil {
		t.Fatalf("Pin(block=3) = %v, want success", err)
	}
	pool.Unpin(s)
}

// TestPoolFlushAllClearsDirty verifies FlushAll writes dirty pages and
// clears their dirty bits.
func TestPoolFlushAllClearsDirty(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 400, Fork: MainFork}

	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	s.Page()[200] = 0x99
	pool.MarkDirty(s)
	pool.Unpin(s)

	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}
	for i, slot := range pool.slots {
		if slot.dirty {
			t.Errorf("slot %d still dirty after FlushAll", i)
		}
	}

	// Direct smgr read: the byte must be there.
	got := make(Page, BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[200] != 0x99 {
		t.Errorf("smgr read byte[200] = %#x, want 0x99", got[200])
	}
}

func TestPoolEvictionFlushesWALBeforeData(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	wal := &recordingWAL{}

	pool, err := NewPool(mgr, PoolConfig{Slots: 1, WAL: wal})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 500, Fork: MainFork}

	s0, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	s0.Page()[300] = 0x7A
	pool.MarkDirtyWithLSN(s0, LSN(42))
	pool.Unpin(s0)

	// A second PinNew with a one-slot pool forces eviction+flush.
	s1, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s1)

	if len(wal.calls) == 0 {
		t.Fatalf("expected at least one WAL flush request")
	}
	if wal.calls[0] != 42 {
		t.Fatalf("first WAL flush LSN = %d, want 42", wal.calls[0])
	}

	got := make(Page, BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[300] != 0x7A {
		t.Fatalf("persisted byte = %#x, want 0x7A", got[300])
	}
}

// TestPoolFPIEmittedOncePerEpoch pins the restored FPI contract:
// MarkDirty emits at most one full-page-image per slot per
// checkpoint epoch. ResetCheckpointEpoch re-arms emission for the
// next mutation.
func TestPoolFPIEmittedOncePerEpoch(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	var emitted int
	logFPI := func(_ RelFileNode, _ BlockNumber, _ Page) (LSN, error) {
		emitted++
		return LSN(100 + emitted), nil
	}
	pool, err := NewPool(mgr, PoolConfig{
		Slots:          2,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 600, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range []byte{1, 2, 3} {
		s.Lock()
		s.Page()[200+i] = b
		pool.MarkDirty(s)
		s.Unlock()
	}
	pool.Unpin(s)

	if emitted != 1 {
		t.Fatalf("emitted=%d FPIs across 3 mutations in one epoch, want 1", emitted)
	}

	// ResetCheckpointEpoch re-arms the first-dirty emission.
	pool.ResetCheckpointEpoch()
	s, err = pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	s.Lock()
	s.Page()[210] = 9
	pool.MarkDirty(s)
	s.Unlock()
	pool.Unpin(s)
	if emitted != 2 {
		t.Errorf("emitted=%d after epoch reset + one mutation, want 2", emitted)
	}
}

// TestPoolFPISkippedWhenDisabled verifies that flipping
// full_page_writes off via SetFullPageWrites suppresses FPI
// emission even though the callback is wired.
func TestPoolFPISkippedWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	calls := 0
	pool, err := NewPool(mgr, PoolConfig{
		Slots: 1,
		LogPageImage: func(_ RelFileNode, _ BlockNumber, _ Page) (LSN, error) {
			calls++
			return 1, nil
		},
		FullPageWrites: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 601, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	s.Lock()
	s.Page()[300] = 9
	pool.MarkDirty(s)
	s.Unlock()
	pool.Unpin(s)

	if calls != 0 {
		t.Fatalf("FPI callback called %d times when full_page_writes=off", calls)
	}
}

func TestPoolEvictionReturnsWALFlushError(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	wal := &recordingWAL{err: errors.New("simulated wal failure")}

	pool, err := NewPool(mgr, PoolConfig{Slots: 1, WAL: wal})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 501, Fork: MainFork}

	s0, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkDirtyWithLSN(s0, LSN(77))
	pool.Unpin(s0)

	_, _, err = pool.PinNew(rel)
	if err == nil {
		t.Fatalf("expected flush-victim error")
	}
	if !strings.Contains(err.Error(), "flush wal") {
		t.Fatalf("error = %v, want WAL flush context", err)
	}
}

// recordingAIOEngine is a fake AIOEngine that captures every
// Submit so a test can assert the storage Manager actually
// flowed reads through the engine when one is attached.
type recordingAIOEngine struct {
	submits []AIOSubmitOp
}

func (r *recordingAIOEngine) Submit(op AIOSubmitOp) AIOHandle {
	r.submits = append(r.submits, op)
	var n int
	var err error
	switch op.Direction {
	case AIODirWrite:
		n, err = op.File.WriteAt(op.Buffer, op.Offset)
	default:
		n, err = op.File.ReadAt(op.Buffer, op.Offset)
	}
	return recordingAIOHandle{n: n, err: err}
}

type recordingAIOHandle struct {
	n   int
	err error
}

func (h recordingAIOHandle) Wait() (int, error) { return h.n, h.err }

// TestPrefetchBlockSyncFallback pins the no-engine path: with
// no AIO engine attached, PrefetchBlock returns a pre-completed
// handle whose Wait yields the just-read bytes synchronously.
// This is the substrate's "preserve existing behaviour" guard.
func TestPrefetchBlockSyncFallback(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17000, Fork: MainFork}

	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	page[64] = 0xAA
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	got := make(Page, BlockSize)
	h, err := mgr.PrefetchBlock(rel, 0, got)
	if err != nil {
		t.Fatal(err)
	}
	n, werr := h.Wait()
	if werr != nil {
		t.Fatalf("Wait err=%v", werr)
	}
	if n != BlockSize {
		t.Errorf("Wait n=%d want %d", n, BlockSize)
	}
	if got[64] != 0xAA {
		t.Errorf("byte 64 = %#x want 0xAA", got[64])
	}
}

// TestPrefetchBlockUsesAttachedEngine pins the engine path:
// once SetAIO is called, every PrefetchBlock submits through
// the engine. Asserts the engine sees the right offset and
// the read still round-trips bytes.
func TestPrefetchBlockUsesAttachedEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17001, Fork: MainFork}

	// Two pages so PrefetchBlock(blk=1) lands at offset BlockSize.
	page := make(Page, BlockSize)
	_ = InitPage(page)
	page[100] = 0xCD
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}
	page[100] = 0xEF
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)

	got := make(Page, BlockSize)
	h, err := mgr.PrefetchBlock(rel, 1, got)
	if err != nil {
		t.Fatal(err)
	}
	n, werr := h.Wait()
	if werr != nil || n != BlockSize {
		t.Fatalf("Wait n=%d err=%v", n, werr)
	}
	if got[100] != 0xEF {
		t.Errorf("byte 100 = %#x want 0xEF", got[100])
	}
	if len(eng.submits) != 1 {
		t.Fatalf("engine saw %d submits, want 1", len(eng.submits))
	}
	if off := eng.submits[0].Offset; off != BlockSize {
		t.Errorf("submit offset=%d want %d", off, BlockSize)
	}
}

// TestPrefetchBlockOutOfRange: a prefetch past EOF returns an
// already-complete handle whose Wait surfaces ErrShortRead —
// matches ReadBlock's behaviour.
func TestPrefetchBlockOutOfRange(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17002, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	h, err := mgr.PrefetchBlock(rel, 5, make(Page, BlockSize))
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := h.Wait(); !errors.Is(werr, ErrShortRead) {
		t.Errorf("Wait err=%v want ErrShortRead", werr)
	}
}

// TestPoolPrefetchDisabledIsNoOp pins the default-off behaviour:
// SetPrefetchEnabled is false at construction so seqScan-style
// callers that fire Prefetch don't trigger any I/O on
// AIO-engine-less deployments.
func TestPoolPrefetchDisabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 17100, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	pool.Prefetch(BufferTag{Rel: rel, Block: 0})
	if got := len(eng.submits); got != 0 {
		t.Errorf("disabled Prefetch fired %d submits, want 0", got)
	}
}

// TestPoolPrefetchEnabledFiresThroughEngine pins the enabled
// path: with prefetching on and an engine attached, every
// non-cached Prefetch call submits one read through the engine.
// A subsequent Prefetch for an already-cached tag is a no-op.
func TestPoolPrefetchEnabledFiresThroughEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetPrefetchEnabled(true)

	rel := RelFileNode{DBOid: 1, RelOid: 17101, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	pool.Prefetch(BufferTag{Rel: rel, Block: 0})
	pool.Prefetch(BufferTag{Rel: rel, Block: 1})
	if got := len(eng.submits); got != 2 {
		t.Errorf("Prefetch fired %d submits, want 2", got)
	}

	// Pin block 0 so it lands in the pool, then Prefetch it
	// again — the cached-tag check should suppress the submit.
	s, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)
	pool.Prefetch(BufferTag{Rel: rel, Block: 0})
	if got := len(eng.submits); got != 2 {
		t.Errorf("Prefetch on cached tag fired extra submit; submits=%d want 2", got)
	}
}

// TestWriteBlockAIOSyncFallback: with no engine attached the
// WriteBlockAIO path runs the write inline and returns a
// pre-completed handle whose Wait yields BlockSize / nil. The
// bytes round-trip through ReadBlock — confirming the
// fallback path is identical to WriteBlock semantically.
func TestWriteBlockAIOSyncFallback(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17200, Fork: MainFork}

	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	page[256] = 0x42
	h, err := mgr.WriteBlockAIO(rel, 0, page)
	if err != nil {
		t.Fatal(err)
	}
	n, werr := h.Wait()
	if werr != nil || n != BlockSize {
		t.Fatalf("Wait n=%d err=%v want %d/nil", n, werr, BlockSize)
	}

	got := make(Page, BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[256] != 0x42 {
		t.Errorf("byte 256 = %#x want 0x42", got[256])
	}
}

// TestWriteBlockAIOUsesAttachedEngine: with an engine attached,
// WriteBlockAIO submits through it with Direction=Write so the
// engine sees the right shape. The recording engine echoes the
// op as a sync write; bytes round-trip through ReadBlock.
func TestWriteBlockAIOUsesAttachedEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17201, Fork: MainFork}

	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)

	page[300] = 0xAB
	h, err := mgr.WriteBlockAIO(rel, 0, page)
	if err != nil {
		t.Fatal(err)
	}
	n, werr := h.Wait()
	if werr != nil || n != BlockSize {
		t.Fatalf("Wait n=%d err=%v", n, werr)
	}
	if len(eng.submits) != 1 {
		t.Fatalf("engine saw %d submits, want 1", len(eng.submits))
	}
	if eng.submits[0].Direction != AIODirWrite {
		t.Errorf("Direction=%v want AIODirWrite", eng.submits[0].Direction)
	}

	got := make(Page, BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[300] != 0xAB {
		t.Errorf("byte 300 = %#x want 0xAB", got[300])
	}
}

// TestWriteBlockAIORejectsOutOfRange: writing past nblocks
// returns a pre-completed handle whose Wait surfaces a
// descriptive error. Mirrors WriteBlock's behaviour (Extend is
// the only path that grows a relation; AIO doesn't extend).
func TestWriteBlockAIORejectsOutOfRange(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17202, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	h, err := mgr.WriteBlockAIO(rel, 5, page)
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := h.Wait(); werr == nil {
		t.Errorf("Wait err=nil want out-of-range failure")
	}
}

// TestFlushAllPacedBatchedSubmitsThroughEngine pins the new
// hot-path wiring: with an AIO engine attached and async-flush
// enabled, FlushAllPaced batches dirty slots and submits writes
// through Manager.WriteBlockAIO with Direction=AIODirWrite. The
// recording engine sees one submit per dirty slot, all with
// DirWrite. Bytes round-trip through ReadBlock confirming
// WAL-before-data wasn't broken (writes still landed correctly).
func TestFlushAllPacedBatchedSubmitsThroughEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)
	pool, err := NewPool(mgr, PoolConfig{Slots: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetAsyncFlushBatchSize(4)

	rel := RelFileNode{DBOid: 1, RelOid: 17300, Fork: MainFork}
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	// Extend three blocks and dirty them via Pin + MarkDirty.
	for i := 0; i < 3; i++ {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatal(err)
		}
		s.page[200+i] = byte(0xA0 + i)
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	// Reset the engine counter so the flush path is what we
	// observe, not the eviction-path Pins above (none here, but
	// future test additions might).
	eng.submits = nil
	if err := pool.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if got := len(eng.submits); got != 3 {
		t.Errorf("engine saw %d submits, want 3", got)
	}
	for i, op := range eng.submits {
		if op.Direction != AIODirWrite {
			t.Errorf("submit[%d] direction=%v want AIODirWrite", i, op.Direction)
		}
	}

	// Round-trip via fresh Pins (forced by InvalidateRel).
	pool.InvalidateRel(rel)
	for i := 0; i < 3; i++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatal(err)
		}
		want := byte(0xA0 + i)
		if got := s.page[200+i]; got != want {
			t.Errorf("block %d byte = %#x want %#x", i, got, want)
		}
		pool.Unpin(s)
	}
}

// TestFlushAllPacedBatchSizeOneEquivalentToLegacy: with the
// async-flush batch size left at 0 (the default) FlushAllPaced
// runs the per-slot path, equivalent to the pre-AIO behaviour
// — every dirty slot still flushes correctly even with no
// engine attached.
func TestFlushAllPacedBatchSizeOneEquivalentToLegacy(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 17301, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}
	s, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	s.page[100] = 0xCD
	pool.MarkDirty(s)
	pool.Unpin(s)

	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}
	pool.InvalidateRel(rel)
	s, _ = pool.Pin(BufferTag{Rel: rel, Block: 0})
	if got := s.page[100]; got != 0xCD {
		t.Errorf("byte 100 = %#x want 0xCD", got)
	}
	pool.Unpin(s)
}

// TestSetAsyncFlushBatchSizeClamps pins the input validation:
// negative values clamp to 0, values above MaxFlushBatchSize
// clamp to the cap.
func TestSetAsyncFlushBatchSizeClamps(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	pool.SetAsyncFlushBatchSize(-1)
	if got := pool.flushBatchSize(); got != 1 {
		t.Errorf("batch=-1 → flushBatchSize=%d want 1 (legacy serial)", got)
	}
	pool.SetAsyncFlushBatchSize(MaxFlushBatchSize * 10)
	if got := pool.flushBatchSize(); got != MaxFlushBatchSize {
		t.Errorf("batch=10×Max → flushBatchSize=%d want %d", got, MaxFlushBatchSize)
	}
}

// TestPrefetchBlockPopulatesTarget pins that PrefetchBlock
// stamps the AIOSubmitOp.Target field with the relfile's
// path so pg_aios.target_desc can render it.
func TestPrefetchBlockPopulatesTarget(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17400, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)
	got := make(Page, BlockSize)
	h, err := mgr.PrefetchBlock(rel, 0, got)
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := h.Wait(); werr != nil {
		t.Fatal(werr)
	}
	if len(eng.submits) != 1 {
		t.Fatalf("submits=%d want 1", len(eng.submits))
	}
	want := filepath.Join(dir, "base", "1", "17400")
	if got := eng.submits[0].Target; got != want {
		t.Errorf("Target=%q want %q", got, want)
	}
}

// TestWriteBlockAIOPopulatesTarget: the write path also stamps
// Target with the relfile path.
func TestWriteBlockAIOPopulatesTarget(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 17401, Fork: MainFork}
	page := make(Page, BlockSize)
	_ = InitPage(page)
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	eng := &recordingAIOEngine{}
	mgr.SetAIO(eng)
	h, err := mgr.WriteBlockAIO(rel, 0, page)
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := h.Wait(); werr != nil {
		t.Fatal(werr)
	}
	want := filepath.Join(dir, "base", "1", "17401")
	if got := eng.submits[0].Target; got != want {
		t.Errorf("Target=%q want %q", got, want)
	}
}

// TestPinNewEmitsSmgrCreateOnFirstBlock verifies that Pool.PinNew calls the
// LogSmgrCreate hook with the correct RelFileNode when it creates block 0 of
// a new relation, and does NOT call it for subsequent blocks.
func TestPinNewEmitsSmgrCreateOnFirstBlock(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 88888, Fork: MainFork}

	var emitted []RelFileNode
	pool, err := NewPool(mgr, PoolConfig{
		Slots: 8,
		LogSmgrCreate: func(r RelFileNode) error {
			emitted = append(emitted, r)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// First PinNew: creates block 0 → should emit SmgrCreate.
	s0, blk0, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	if blk0 != 0 {
		t.Errorf("first PinNew: blk=%d want 0", blk0)
	}
	pool.MarkDirty(s0)
	pool.Unpin(s0)

	if len(emitted) != 1 {
		t.Fatalf("SmgrCreate calls after block 0 = %d, want 1", len(emitted))
	}
	if emitted[0] != rel {
		t.Errorf("emitted rel = %+v, want %+v", emitted[0], rel)
	}

	// Second PinNew: creates block 1 → must NOT emit SmgrCreate.
	s1, blk1, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	if blk1 != 1 {
		t.Errorf("second PinNew: blk=%d want 1", blk1)
	}
	pool.MarkDirty(s1)
	pool.Unpin(s1)

	if len(emitted) != 1 {
		t.Errorf("SmgrCreate calls after block 1 = %d, want still 1", len(emitted))
	}
}
