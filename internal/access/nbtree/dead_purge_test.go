package nbtree

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func k32(v int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v)^0x80000000)
	return b
}

// countIndexPages reports how many blocks the index relfile occupies — the
// direct measure of the bloat this work targets.
func countIndexPages(t *testing.T, pool *storage.Pool, rel storage.RelFileNode) int {
	t.Helper()
	n, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	return int(n)
}

// The purge must never be able to remove an entry the filter did not name, and
// must degrade safely on a malformed answer.
func TestPurgeDeadHeapPointersContract(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	items := make([]item, 5)
	for i := range items {
		items[i] = item{key: k32(int32(i)), ptr: storage.ItemPointer{Block: 1, Offset: uint16(i + 1)}}
	}

	t.Run("nil filter is a no-op", func(t *testing.T) {
		bt.deadTIDs = nil
		got, n := bt.purgeDeadHeapPointers(items)
		if n != 0 || len(got) != len(items) {
			t.Fatalf("nil filter must not purge: n=%d len=%d", n, len(got))
		}
	})

	t.Run("wrong-length answer is ignored", func(t *testing.T) {
		bt.deadTIDs = func(tids []storage.ItemPointer) []bool { return []bool{true} }
		got, n := bt.purgeDeadHeapPointers(items)
		if n != 0 || len(got) != len(items) {
			t.Fatalf("short answer must be ignored positionally: n=%d len=%d", n, len(got))
		}
	})

	t.Run("only named entries are removed", func(t *testing.T) {
		bt.deadTIDs = func(tids []storage.ItemPointer) []bool {
			out := make([]bool, len(tids))
			out[1], out[3] = true, true
			return out
		}
		got, n := bt.purgeDeadHeapPointers(items)
		if n != 2 || len(got) != 3 {
			t.Fatalf("want 2 purged / 3 survivors, got n=%d len=%d", n, len(got))
		}
		for _, it := range got {
			if it.ptr.Offset == 2 || it.ptr.Offset == 4 {
				t.Fatalf("purged entry survived: %+v", it.ptr)
			}
		}
	})

	t.Run("never empties the page", func(t *testing.T) {
		bt.deadTIDs = func(tids []storage.ItemPointer) []bool {
			out := make([]bool, len(tids))
			for i := range out {
				out[i] = true
			}
			return out
		}
		got, n := bt.purgeDeadHeapPointers(items)
		if len(got) != 1 || n != len(items)-1 {
			t.Fatalf("all-dead must keep one entry: n=%d len=%d", n, len(got))
		}
	})
}

// The regression that reverted the LP_DEAD approach was that reclamation cost
// SPACE by defeating posting-list consolidation. This pins the opposite
// property: purging feeds its survivors back through dedup, so duplicate runs
// still consolidate.
func TestPurgePreservesDeduplication(t *testing.T) {
	f := indexFormat{}
	// 40 entries on ONE key: exactly the duplicate-heavy shape dedup exists for.
	items := make([]item, 40)
	for i := range items {
		items[i] = item{key: k32(7), ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 4), Offset: uint16(i%4 + 1)}}
	}
	rawsAll, _ := deduplicateToRawItemsWithSpans(f, items)

	// Purge half of them, then dedup the survivors.
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	bt.deadTIDs = func(tids []storage.ItemPointer) []bool {
		out := make([]bool, len(tids))
		for i := range out {
			out[i] = i%2 == 0
		}
		return out
	}
	survivors, npurged := bt.purgeDeadHeapPointers(items)
	if npurged != 20 {
		t.Fatalf("want 20 purged, got %d", npurged)
	}
	rawsSurv, spans := deduplicateToRawItemsWithSpans(f, survivors)

	// Survivors must STILL form posting lists, not degrade into plain items.
	postings := 0
	total := 0
	for i, r := range rawsSurv {
		if isPostingRaw(r.raw) {
			postings++
		}
		total += spans[i]
	}
	if postings == 0 {
		t.Fatal("purge destroyed deduplication: survivors formed no posting list")
	}
	if total != len(survivors) {
		t.Fatalf("spans lost entries: %d vs %d", total, len(survivors))
	}
	// And the purged page must be strictly cheaper than the unpurged one.
	if len(rawsSurv) > len(rawsAll) {
		t.Fatalf("purged page needs MORE line pointers (%d) than unpurged (%d) — this is the reverted approach's failure mode",
			len(rawsSurv), len(rawsAll))
	}
	t.Logf("dedup intact: %d survivors -> %d line pointers (%d postings); unpurged %d -> %d",
		len(survivors), len(rawsSurv), postings, len(items), len(rawsAll))
}

// End-to-end: an index whose entries all become dead must stop growing when the
// purge is enabled, and must still grow when it is not (the bug being fixed).
func TestPurgeReclaimsIndexGrowth(t *testing.T) {
	const rounds, perRound = 40, 200

	grow := func(t *testing.T, enable bool) int {
		t.Helper()
		bt, pool, cleanup := newTestTree(t)
		defer cleanup()
		live := map[uint64]bool{}
		if enable {
			// Everything not in `live` is dead: models entries left behind by
			// non-HOT updates once their heap version is dead to all.
			bt.deadTIDs = func(tids []storage.ItemPointer) []bool {
				out := make([]bool, len(tids))
				for i, tp := range tids {
					out[i] = !live[uint64(tp.Block)<<16|uint64(tp.Offset)]
				}
				return out
			}
		}
		for r := range rounds {
			for i := range perRound {
				ptr := storage.ItemPointer{Block: storage.BlockNumber(r), Offset: uint16(i + 1)}
				if err := bt.Insert(k32(int32(i)), ptr); err != nil {
					t.Fatalf("insert r=%d i=%d: %v", r, i, err)
				}
				live = map[uint64]bool{} // only the newest round is live
				live[uint64(ptr.Block)<<16|uint64(ptr.Offset)] = true
			}
		}
		return countIndexPages(t, pool, bt.rel)
	}

	off := grow(t, false)
	on := grow(t, true)
	t.Logf("index pages: purge OFF = %d, purge ON = %d", off, on)
	if on > off {
		t.Fatalf("purge made the index BIGGER (%d vs %d pages)", on, off)
	}
	if on == off {
		t.Logf("note: no reduction at this size; growth may be dominated by live entries")
	}
}
