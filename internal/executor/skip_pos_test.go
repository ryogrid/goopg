package executor

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// TestSkipPosMatchesBoundCalls pins M0122-0012 slice 57: walkLeaf reads its
// three per-entry answers off one comparison with the group's probe
// (skipPos), and each must be exactly the bound call it replaced —
// pastGroup = CompareHighBound(entry, pivot) > 0, beforeProbe =
// CompareLowBound(entry, probe) < 0, pastProbe = CompareHighBound(entry,
// probe) > 0. A (DESC int4, int4 NULLS FIRST, int4) index over random entries
// (NULLs and duplicates included) is checked against groups of one and two
// prefix attributes, with probes that name fewer than every key attribute
// and probes that name all three (the full-arity case only the heap-TID
// tiebreak orders), and the pivot being the probe's prefix as groupBounds
// forms it.
func TestSkipPosMatchesBoundCalls(t *testing.T) {
	int4 := func() nbtree.PGKeyAttr {
		return nbtree.PGKeyAttr{PGIndexAttr: nbtree.PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}, Compare: nbtree.PGCompareInt4}
	}
	a, b, c := int4(), int4(), int4()
	a.Desc, b.NullsFirst = true, true
	desc := &nbtree.PGIndexKeyDesc{Attrs: []nbtree.PGKeyAttr{a, b, c}}
	phys := desc.Physical()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()
	tree, err := nbtree.CreateWithOptions(pool, storage.RelFileNode{DBOid: 1, RelOid: 9157, Fork: storage.MainFork}, nbtree.Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	rng := rand.New(rand.NewSource(57))
	val := func(nulls bool) []byte {
		if nulls && rng.Intn(7) == 0 {
			return nil
		}
		v := make([]byte, 4)
		binary.LittleEndian.PutUint32(v, uint32(int32(rng.Intn(4)-1)))
		return v
	}
	form := func(vals [][]byte, natts int, tid storage.ItemPointer) []byte {
		isnull := make([]bool, len(vals))
		for i, v := range vals {
			isnull[i] = v == nil
		}
		raw, err := nbtree.FormPGIndexTuple(phys[:len(vals)], vals, isnull, tid)
		if err != nil {
			t.Fatalf("FormPGIndexTuple: %v", err)
		}
		if natts < len(desc.Attrs) {
			if err := nbtree.BTreeTupleSetNAtts(raw, uint16(natts), false); err != nil {
				t.Fatalf("BTreeTupleSetNAtts: %v", err)
			}
		}
		return raw
	}
	var entries [][]byte
	for range 80 {
		tid := storage.ItemPointer{Block: storage.BlockNumber(rng.Intn(3)), Offset: uint16(1 + rng.Intn(3))}
		entries = append(entries, form([][]byte{val(true), val(true), val(true)}, 3, tid))
	}
	checks, fused := 0, 0
	for _, s := range []int{1, 2} {
		for _, n := range []int{s, s + 1} {
			for range 30 {
				vals := [][]byte{val(true), val(true), val(false)}[:n]
				probe := form(vals, n, storage.ItemPointer{})
				pivot := form(vals[:s], s, storage.ItemPointer{})
				for _, e := range entries {
					p := probePos(tree, e, probe)
					if p.ok {
						fused++
					}
					if got, want := p.pastGroup(tree, e, pivot, s), tree.CompareHighBound(e, pivot) > 0; got != want {
						t.Fatalf("s=%d n=%d entry %x probe %x: pastGroup %v, CompareHighBound(pivot) %v", s, n, e, probe, got, want)
					}
					if got, want := p.beforeProbe(tree, e, probe), tree.CompareLowBound(e, probe) < 0; got != want {
						t.Fatalf("s=%d n=%d entry %x probe %x: beforeProbe %v, CompareLowBound %v", s, n, e, probe, got, want)
					}
					if got, want := p.pastProbe(tree, e, probe), tree.CompareHighBound(e, probe) > 0; got != want {
						t.Fatalf("s=%d n=%d entry %x probe %x: pastProbe %v, CompareHighBound(probe) %v", s, n, e, probe, got, want)
					}
					checks++
				}
			}
		}
	}
	if fused < checks*9/10 {
		t.Fatalf("only %d of %d checks used the fused comparison — the fixture never reaches it", fused, checks)
	}
}
