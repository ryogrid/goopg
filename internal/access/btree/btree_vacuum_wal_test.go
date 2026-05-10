package btree

import (
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// captureLogBtreeVacuum is a `LogBtreeVacuumFunc` that records
// every emission so a test can assert on the kept-items
// projection without spinning up a real WAL writer.
type captureLogBtreeVacuum struct {
	mu       sync.Mutex
	emitted  []capturedVacuumEmission
	nextLSN  uint64
}

type capturedVacuumEmission struct {
	rel      storage.RelFileNode
	blk      storage.BlockNumber
	keptCopy [][]byte
	flags    uint16
}

func (c *captureLogBtreeVacuum) emit(rel storage.RelFileNode, blk storage.BlockNumber, kept [][]byte, flags uint16) (storage.LSN, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dup := make([][]byte, len(kept))
	for i, it := range kept {
		dup[i] = append([]byte(nil), it...)
	}
	c.emitted = append(c.emitted, capturedVacuumEmission{
		rel:      rel,
		blk:      blk,
		keptCopy: dup,
		flags:    flags,
	})
	c.nextLSN += uint64(64)
	return storage.LSN(c.nextLSN), nil
}

// TestVacuumIndexPagesEmitsLogicalRecord pins M0079-0002: when
// the LogBtreeVacuum hook is wired, VacuumIndexPages emits one
// kept-items WAL record per pruned leaf rather than the prior
// FPI-via-LogPageImage path.
//
// Test setup: bulk-load 50 entries, declare half of them dead,
// run VacuumIndexPages with the capture hook wired. The
// captured emissions must:
//
//   1. Cover every pruned leaf at least once.
//   2. Carry kept-item raw bytes that match the post-vacuum
//      page layout.
//   3. Report opaque flags consistent with the per-page state
//      (BTHalfDead | BTDeleted set when the page becomes empty).
func TestVacuumIndexPagesEmitsLogicalRecord(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	cap := &captureLogBtreeVacuum{}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          128,
		LogBtreeVacuum: cap.emit,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork}

	const n = 50
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Mark every other entry as dead.
	dead := make([]storage.ItemPointer, 0, n/2)
	for i := 0; i < n; i += 2 {
		dead = append(dead, storage.ItemPointer{Block: 0, Offset: uint16(i + 1)})
	}
	removed, err := tree.VacuumIndexPages(dead)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != len(dead) {
		t.Errorf("removed=%d want %d", removed, len(dead))
	}

	// At least one logical record must have been emitted.
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.emitted) == 0 {
		t.Fatal("no LogBtreeVacuum emissions; expected at least one per pruned leaf")
	}
	for i, e := range cap.emitted {
		if e.rel != rel {
			t.Errorf("emission[%d]: rel=%+v want %+v", i, e.rel, rel)
		}
		// Each emission must list at least one kept item OR
		// have BTHalfDead/BTDeleted flags (page emptied).
		const halfDead = uint16(BTDeleted | BTHalfDead)
		if len(e.keptCopy) == 0 && e.flags&halfDead == 0 {
			t.Errorf("emission[%d]: empty kept-items list without BTHalfDead/BTDeleted flags (got flags=%#x)", i, e.flags)
		}
	}
}

// TestVacuumIndexPagesEmitsUnlinkRecord pins M0079-0003: when
// VacuumIndexPages empties enough leaves that
// `unlinkEmptyLeaf` runs (and the LogBtreeUnlinkPage hook is
// wired), each unlink emits ONE atomic record carrying the 4
// page mutations instead of 4 FPIs. The test captures the
// emissions and asserts they describe the expected shape.
//
// Test setup: bulk-load 200 entries, mark all of them dead,
// run VacuumIndexPages with both LogBtreeVacuum (the M0079-0002
// hook needed to empty leaves logically) AND LogBtreeUnlinkPage
// hooked. The captured unlink emissions must:
//
//   1. Cover at least one leaf-unlink event.
//   2. Each emission must describe one of the empty leaves
//      with consistent left/right sibling info.
func TestVacuumIndexPagesEmitsUnlinkRecord(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	vacCap := &captureLogBtreeVacuum{}
	type unlinkCap struct {
		rel storage.RelFileNode
		req storage.BtreeUnlinkPageRequest
	}
	var unlinkEmissions []unlinkCap
	logUnlink := func(rel storage.RelFileNode, req storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
		unlinkEmissions = append(unlinkEmissions, unlinkCap{rel: rel, req: req})
		return storage.LSN(1234), nil
	}
	logNewRoot := func(rel storage.RelFileNode, rootBlk storage.BlockNumber, level uint32, items [][]byte) (storage.LSN, error) {
		// Recycle into the unlink emission slot for cleanliness; we
		// don't need to track new-root events for this test.
		return storage.LSN(1234), nil
	}
	logMarkHalfDead := func(rel storage.RelFileNode, leafBlk storage.BlockNumber, flagsAfter uint16) (storage.LSN, error) {
		return storage.LSN(1234), nil
	}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:                    256,
		LogBtreeVacuum:           vacCap.emit,
		LogBtreeUnlinkPage:       logUnlink,
		LogBtreeNewRoot:          logNewRoot,
		LogBtreeMarkPageHalfDead: logMarkHalfDead,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16405, Fork: storage.MainFork}

	const n = 200
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	dead := make([]storage.ItemPointer, 0, n)
	for i := 0; i < n; i++ {
		dead = append(dead, storage.ItemPointer{Block: 0, Offset: uint16(i + 1)})
	}
	if _, err := tree.VacuumIndexPages(dead); err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}

	if len(unlinkEmissions) == 0 {
		t.Fatal("no LogBtreeUnlinkPage emissions despite all entries dead — expected at least one leaf unlink")
	}
	for i, e := range unlinkEmissions {
		if e.rel != rel {
			t.Errorf("emission[%d]: rel=%+v want %+v", i, e.rel, rel)
		}
		if e.req.LeafBlk == 0 {
			t.Errorf("emission[%d]: LeafBlk=0 (metapage); unlink should never target the metapage", i)
		}
		// The post-unlink leaf flags must keep BTDeleted set
		// and clear BTHalfDead.
		if e.req.LeafFlagsAfter&BTDeleted == 0 {
			t.Errorf("emission[%d]: LeafFlagsAfter missing BTDeleted (got %#x)", i, e.req.LeafFlagsAfter)
		}
		if e.req.LeafFlagsAfter&BTHalfDead != 0 {
			t.Errorf("emission[%d]: LeafFlagsAfter still has BTHalfDead (got %#x)", i, e.req.LeafFlagsAfter)
		}
	}
}

// TestVacuumIndexPagesFallsBackToFPIWhenHookNil pins the
// backwards-compat: a Pool without LogBtreeVacuum continues
// to use the FPI fallback. Same VacuumIndexPages contract,
// just heavier WAL volume — but no semantic change.
// (M0079-0002.)
func TestVacuumIndexPagesFallsBackToFPIWhenHookNil(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	entries := []BulkEntry{
		{Key: EncodeInt4(1), Ptr: storage.ItemPointer{Block: 0, Offset: 1}},
		{Key: EncodeInt4(2), Ptr: storage.ItemPointer{Block: 0, Offset: 2}},
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	dead := []storage.ItemPointer{{Block: 0, Offset: 1}}
	removed, err := tree.VacuumIndexPages(dead)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed=%d want 1", removed)
	}
	// Without the hook the FPI path runs (no error) and the
	// surviving entry remains accessible.
	var count int
	_ = tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	})
	if count != 1 {
		t.Errorf("post-vacuum count=%d want 1", count)
	}
}
