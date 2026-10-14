package storage

import (
	"sync"
	"testing"
)

// TestBTreeStructuralLockStripes pins the pool's structural B-tree lock
// table: the same relation always gets the same mutex — before and after
// its buffers are invalidated (a DROP), so a reused relfilenode cannot hand a
// new handle a different mutex while an old handle holds one — and
// consecutive relation OIDs spread over the stripes instead of piling onto a
// few. The table is a fixed array, so opening many relations allocates
// nothing.
func TestBTreeStructuralLockStripes(t *testing.T) {
	mgr := NewManager(ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	p, err := NewPool(mgr, PoolConfig{Slots: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	rel := RelFileNode{DBOid: 5, RelOid: 16400}
	mu := p.BTreeStructuralLock(rel)
	p.InvalidateRel(rel)
	if got := p.BTreeStructuralLock(rel); got != mu {
		t.Fatal("the same relation got a different structural mutex after InvalidateRel")
	}
	used := map[*sync.Mutex]bool{}
	for oid := uint32(16384); oid < 16384+2000; oid++ {
		used[p.BTreeStructuralLock(RelFileNode{DBOid: 5, RelOid: oid})] = true
	}
	if len(used) < btreeStructureStripes*9/10 {
		t.Errorf("2000 consecutive OIDs used %d of %d stripes", len(used), btreeStructureStripes)
	}
	if n := testing.AllocsPerRun(100, func() { p.BTreeStructuralLock(RelFileNode{DBOid: 5, RelOid: 99999}) }); n != 0 {
		t.Errorf("BTreeStructuralLock allocates %v times per call", n)
	}
}
