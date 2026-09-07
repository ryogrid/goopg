package storage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// E-19 slice S1 — Manager.ReadBlocks / relFile.readBlocks.
//
// The point of the slice is upstream's io_combine_limit: PG merges adjacent
// blocks into ONE vectored read (read_stream.c:474-477 → bufmgr.c:1777) and
// goopg had no vectored read at all, so an N-block run cost N syscalls. These
// tests pin the three things that make the combined read safe to substitute
// for N single-block reads: same bytes, same checksum verification, same
// latching.

func extendN(t *testing.T, mgr *Manager, rel RelFileNode, n int) [][]byte {
	t.Helper()
	pages := make([][]byte, n)
	for i := 0; i < n; i++ {
		p := makeDataPage(byte(i))
		if _, err := mgr.Extend(rel, p); err != nil {
			t.Fatalf("Extend %d: %v", i, err)
		}
		pages[i] = p
	}
	return pages
}

func blockBufs(n int) [][]byte {
	bufs := make([][]byte, n)
	for i := range bufs {
		bufs[i] = make([]byte, BlockSize)
	}
	return bufs
}

// TestReadBlocksMatchesReadBlock is the substitution proof: a combined read of
// a run must produce byte-for-byte what N single-block reads produce.
func TestReadBlocksMatchesReadBlock(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 700, Fork: MainFork}
	const n = 9
	extendN(t, mgr, rel, n)

	// Reference: one ReadBlock per block.
	want := blockBufs(n)
	for i := 0; i < n; i++ {
		if err := mgr.ReadBlock(rel, BlockNumber(i), want[i]); err != nil {
			t.Fatalf("ReadBlock %d: %v", i, err)
		}
	}

	got := blockBufs(n)
	full, err := mgr.ReadBlocks(rel, 0, got)
	if err != nil {
		t.Fatalf("ReadBlocks: %v", err)
	}
	if full != n {
		t.Fatalf("ReadBlocks filled %d blocks, want %d", full, n)
	}
	for i := 0; i < n; i++ {
		if string(got[i]) != string(want[i]) {
			t.Fatalf("block %d differs between ReadBlocks and ReadBlock", i)
		}
	}

	// A run starting mid-relation must be offset correctly, not just at 0 —
	// an off-by-one in the offset arithmetic would still pass the test above.
	mid := blockBufs(3)
	if full, err := mgr.ReadBlocks(rel, 4, mid); err != nil || full != 3 {
		t.Fatalf("ReadBlocks(first=4): full=%d err=%v", full, err)
	}
	for i := 0; i < 3; i++ {
		if string(mid[i]) != string(want[4+i]) {
			t.Fatalf("mid-run block %d is not relation block %d", i, 4+i)
		}
	}
}

// TestReadBlocksVerifiesChecksums is the correctness guard the design calls
// out: a vectored read that skipped verification would be a regression no
// values suite could catch, because the suites compare query results and a
// corrupt page that still parses produces wrong rows silently.
func TestReadBlocksVerifiesChecksums(t *testing.T) {
	dir := t.TempDir()
	rel := RelFileNode{DBOid: 1, RelOid: 701, Fork: MainFork}

	mgrCk := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	extendN(t, mgrCk, rel, 4)
	mgrCk.Close()

	// Corrupt block 2 through a checksum-less Manager (writes no checksum).
	mgrRaw := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: false})
	raw := make([]byte, BlockSize)
	if err := mgrRaw.ReadBlock(rel, 2, raw); err != nil {
		t.Fatalf("raw ReadBlock: %v", err)
	}
	raw[SizeOfPageHeaderData+7] ^= 0xFF
	if err := mgrRaw.WriteBlock(rel, 2, raw); err != nil {
		t.Fatalf("raw WriteBlock: %v", err)
	}
	mgrRaw.Close()

	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	// Single-block control: ReadBlock rejects it.
	var cerr *ChecksumError
	if err := mgr.ReadBlock(rel, 2, make([]byte, BlockSize)); !errors.As(err, &cerr) {
		t.Fatalf("control ReadBlock on a corrupt page: got %v, want *ChecksumError", err)
	}

	// The combined read must reject it too, and must not report the corrupt
	// block as successfully filled.
	full, err := mgr.ReadBlocks(rel, 0, blockBufs(4))
	if !errors.As(err, &cerr) {
		t.Fatalf("ReadBlocks over a corrupt page: got %v, want *ChecksumError", err)
	}
	if full > 2 {
		t.Errorf("ReadBlocks reported %d blocks filled past the corrupt block 2", full)
	}
}

// TestReadBlocksShortRunAtEOF pins the end-of-relation contract: whole blocks
// that landed are reported with a nil error, and a run that STARTS beyond the
// relation is ErrShortRead — matching ReadBlock for a single block.
func TestReadBlocksShortRunAtEOF(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 702, Fork: MainFork}
	extendN(t, mgr, rel, 3)

	full, err := mgr.ReadBlocks(rel, 1, blockBufs(5))
	if err != nil {
		t.Fatalf("ReadBlocks past EOF should not error: %v", err)
	}
	if full != 2 {
		t.Errorf("ReadBlocks from block 1 of a 3-block relation filled %d, want 2", full)
	}

	if _, err := mgr.ReadBlocks(rel, 9, blockBufs(2)); !errors.Is(err, ErrShortRead) {
		t.Errorf("ReadBlocks starting beyond the relation: got %v, want ErrShortRead", err)
	}
}

// TestReadBlocksRejectsBadArgs pins the narrow contract. A caller that passed a
// short buffer would otherwise get a silently misaligned iovec.
func TestReadBlocksRejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()
	rel := RelFileNode{DBOid: 1, RelOid: 703, Fork: MainFork}
	extendN(t, mgr, rel, 2)

	if n, err := mgr.ReadBlocks(rel, 0, nil); n != 0 || err != nil {
		t.Errorf("empty run: got (%d, %v), want (0, nil)", n, err)
	}
	bad := [][]byte{make([]byte, BlockSize), make([]byte, 100)}
	if _, err := mgr.ReadBlocks(rel, 0, bad); err == nil {
		t.Error("ReadBlocks accepted a non-BlockSize buffer")
	}
	if _, err := mgr.ReadBlocks(rel, 0, blockBufs(MaxIOCombineLimit+1)); err == nil {
		t.Error("ReadBlocks accepted a run longer than MaxIOCombineLimit")
	}
}

// TestReadBlocksLatchesEveryBlockInTheRun proves the run takes the per-block
// latch for EVERY block, not just the first. Without it a combined read could
// interleave with a concurrent writeBlock of a block inside its own range and
// land a torn mixture of pre- and post-write pages.
//
// The check is behavioural: hold block 2's latch, then assert a run covering
// blocks 0..3 cannot complete until it is released.
func TestReadBlocksLatchesEveryBlockInTheRun(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 704, Fork: MainFork}
	extendN(t, mgr, rel, 4)

	f, err := mgr.relFile(rel)
	if err != nil {
		t.Fatalf("relFile: %v", err)
	}

	release := f.lockBlock(2) // a concurrent single-block op owns block 2

	done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := mgr.ReadBlocks(rel, 0, blockBufs(4))
		done <- err
	}()

	select {
	case err := <-done:
		release()
		wg.Wait()
		t.Fatalf("ReadBlocks completed while block 2's latch was held (err=%v): "+
			"the run is not latching every block, so it can tear against a "+
			"concurrent single-block write", err)
	case <-time.After(250 * time.Millisecond):
		// Correct: still blocked.
	}

	release()
	if err := <-done; err != nil {
		t.Fatalf("ReadBlocks after latch release: %v", err)
	}
	wg.Wait()
}
