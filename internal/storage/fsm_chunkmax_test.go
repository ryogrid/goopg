package storage

import (
	"math/rand"
	"reflect"
	"testing"
)

// refGetCandidates is the pre-chunking implementation of GetCandidates,
// verbatim in behaviour: a straight scan over every page keeping the n
// most-free, ties going to the lower block number.
func refGetCandidates(pages []uint16, minFreeBytes uint16, n int) []BlockNumber {
	type entry struct {
		free uint16
		blk  BlockNumber
	}
	kept := make([]entry, 0, n)
	for blk, free := range pages {
		if free < minFreeBytes {
			continue
		}
		e := entry{free: free, blk: BlockNumber(blk)}
		if len(kept) < n {
			kept = append(kept, e)
			for i := len(kept) - 1; i > 0 && kept[i].free > kept[i-1].free; i-- {
				kept[i], kept[i-1] = kept[i-1], kept[i]
			}
			continue
		}
		if e.free <= kept[n-1].free {
			continue
		}
		kept[n-1] = e
		for i := n - 1; i > 0 && kept[i].free > kept[i-1].free; i-- {
			kept[i], kept[i-1] = kept[i-1], kept[i]
		}
	}
	if len(kept) == 0 {
		return nil
	}
	out := make([]BlockNumber, len(kept))
	for i, e := range kept {
		out[i] = e.blk
	}
	return out
}

func refFirstWithFreeSpace(pages []uint16, minFreeBytes uint16) (BlockNumber, bool) {
	for blk, free := range pages {
		if free >= minFreeBytes {
			return BlockNumber(blk), true
		}
	}
	return 0, false
}

// TestFSMChunkMaxMatchesFullScan pins review/260831 ST-6: skipping chunks whose
// maximum cannot answer the query must return exactly what the old full scan
// returned, including the tie-breaking on block number. The relation spans
// several fsmChunkBlocks chunks and free space is rewritten repeatedly, which
// is what exercises the "the chunk maximum just went down" recompute in
// RecordFreeSpace.
func TestFSMChunkMaxMatchesFullScan(t *testing.T) {
	const npages = fsmChunkBlocks*3 + 137
	rng := rand.New(rand.NewSource(20260831))
	f := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 16384}
	want := make([]uint16, npages)

	set := func(blk int, free uint16) {
		want[blk] = free
		f.RecordFreeSpace(rel, BlockNumber(blk), free)
	}
	for blk := 0; blk < npages; blk++ {
		set(blk, uint16(rng.Intn(4)*1000)) // many zeroes: whole chunks get skipped
	}

	for round := 0; round < 2000; round++ {
		blk := rng.Intn(npages)
		set(blk, uint16(rng.Intn(8192)))

		minFree := uint16(rng.Intn(8192))
		n := 1 + rng.Intn(4)
		if got, wantC := f.GetCandidates(rel, minFree, n), refGetCandidates(want, minFree, n); !reflect.DeepEqual(got, wantC) {
			t.Fatalf("round %d: GetCandidates(min=%d, n=%d) = %v, full scan says %v",
				round, minFree, n, got, wantC)
		}
		gotBlk, gotOK := f.GetPageWithFreeSpace(rel, minFree)
		wantBlk, wantOK := refFirstWithFreeSpace(want, minFree)
		if gotOK != wantOK || (gotOK && gotBlk != wantBlk) {
			t.Fatalf("round %d: GetPageWithFreeSpace(min=%d) = (%d,%v), full scan says (%d,%v)",
				round, minFree, gotBlk, gotOK, wantBlk, wantOK)
		}
	}
}

// TestFSMRecordFreeSpaceGrowth pins review/260831 ST-5: recording a high block
// number grows the array in one step and leaves every skipped block at zero.
func TestFSMRecordFreeSpaceGrowth(t *testing.T) {
	f := NewFSM()
	rel := RelFileNode{DBOid: 1, RelOid: 16385}
	f.RecordFreeSpace(rel, 5000, 4096)
	if _, ok := f.GetPageWithFreeSpace(rel, 1); !ok {
		t.Fatal("block 5000 not visible after RecordFreeSpace")
	}
	blk, ok := f.GetPageWithFreeSpace(rel, 4096)
	if !ok || blk != 5000 {
		t.Fatalf("GetPageWithFreeSpace = (%d,%v), want (5000,true)", blk, ok)
	}
	if got := f.GetCandidates(rel, 1, 4); !reflect.DeepEqual(got, []BlockNumber{5000}) {
		t.Fatalf("GetCandidates = %v, want [5000] — the skipped blocks must read as full", got)
	}
}
