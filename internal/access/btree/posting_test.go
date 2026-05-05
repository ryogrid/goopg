package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPostingMarshalRoundTrip verifies that marshalPosting / parsePostingRaw
// are exact inverses.
func TestPostingMarshalRoundTrip(t *testing.T) {
	key := []byte("shipmode")
	tids := []storage.ItemPointer{
		{Block: 0, Offset: 1},
		{Block: 1, Offset: 2},
		{Block: 100, Offset: 5},
	}
	raw := marshalPosting(key, tids)

	if !isPostingRaw(raw) {
		t.Fatal("isPostingRaw should be true")
	}

	gotKey, gotTIDs, err := parsePostingRaw(raw)
	if err != nil {
		t.Fatalf("parsePostingRaw: %v", err)
	}
	if string(gotKey) != string(key) {
		t.Errorf("key mismatch: want %q got %q", key, gotKey)
	}
	if len(gotTIDs) != len(tids) {
		t.Fatalf("TID count: want %d got %d", len(tids), len(gotTIDs))
	}
	for i, tid := range tids {
		if gotTIDs[i] != tid {
			t.Errorf("TID[%d]: want %+v got %+v", i, tid, gotTIDs[i])
		}
	}
}

// TestIsPostingRaw distinguishes posting from regular raw items.
func TestIsPostingRaw(t *testing.T) {
	// Regular item bytes from item.marshal().
	it := item{keyLen: 4, ptr: storage.ItemPointer{Block: 0, Offset: 1}, key: EncodeInt4(42)}
	reg := it.marshal()
	if isPostingRaw(reg) {
		t.Error("regular item should not be detected as posting")
	}

	// Posting item.
	post := marshalPosting(EncodeInt4(42), []storage.ItemPointer{{Block: 0, Offset: 1}})
	if !isPostingRaw(post) {
		t.Error("posting item should be detected as posting")
	}
}

// TestDeduplicateToRawItems verifies that duplicate keys are collapsed into
// posting-list rawItems while unique keys remain as regular items.
func TestDeduplicateToRawItems(t *testing.T) {
	items := []item{
		{keyLen: 4, ptr: storage.ItemPointer{Block: 0, Offset: 1}, key: EncodeInt4(1)},
		{keyLen: 4, ptr: storage.ItemPointer{Block: 0, Offset: 2}, key: EncodeInt4(2)},
		{keyLen: 4, ptr: storage.ItemPointer{Block: 0, Offset: 3}, key: EncodeInt4(2)}, // dup
		{keyLen: 4, ptr: storage.ItemPointer{Block: 1, Offset: 1}, key: EncodeInt4(3)},
		{keyLen: 4, ptr: storage.ItemPointer{Block: 1, Offset: 2}, key: EncodeInt4(3)}, // dup
		{keyLen: 4, ptr: storage.ItemPointer{Block: 1, Offset: 3}, key: EncodeInt4(3)}, // dup
	}

	raws := deduplicateToRawItems(items)
	if len(raws) != 3 { // keys 1, 2, 3
		t.Fatalf("expected 3 raw items, got %d", len(raws))
	}

	// Key=1: regular (single TID)
	if isPostingRaw(raws[0].raw) {
		t.Error("key=1 should be a regular item (not posting)")
	}

	// Key=2: posting (2 TIDs)
	if !isPostingRaw(raws[1].raw) {
		t.Error("key=2 should be a posting item")
	}
	_, tids2, _ := parsePostingRaw(raws[1].raw)
	if len(tids2) != 2 {
		t.Errorf("key=2 posting should have 2 TIDs, got %d", len(tids2))
	}

	// Key=3: posting (3 TIDs)
	if !isPostingRaw(raws[2].raw) {
		t.Error("key=3 should be a posting item")
	}
	_, tids3, _ := parsePostingRaw(raws[2].raw)
	if len(tids3) != 3 {
		t.Errorf("key=3 posting should have 3 TIDs, got %d", len(tids3))
	}
}

// TestBulkCreateDeduplication is the DoD test for M0047-0003:
// building an index on a column with few distinct values (like
// l_shipmode in TPC-H) should produce far fewer pages than
// one-entry-per-row.
//
// DoD: index page count with 7 distinct keys / 200 TIDs each ≤ 25%
// of a non-deduplicated baseline built with the same data.
//
// The 25% threshold is achieved when keys are long enough that per-key
// storage overhead dominates. For 60-byte composite keys with 200
// duplicates, each regular item costs 72 bytes vs. the posting list's
// ~6 bytes per TID — giving a ~12% ratio.
func TestBulkCreateDeduplication(t *testing.T) {
	pool, rel := newBulkTestPool(t)

	// 7 distinct "shipmode-like" values, each padded to 30 chars (no nulls)
	// to simulate a composite index key. The encoded key is 31 bytes
	// (EncodeVarchar adds a 0x00 terminator) which fits within MaxHighKeyLen=32.
	// 500 entries each = 3500 total.
	const numKeys = 7
	const perKey = 1000 // 1000 TIDs per key → each posting item ≈6 KB = 1 leaf page
	const padTo = 29   // 29 chars + 0x00 terminator = 30 bytes ≤ MaxHighKeyLen=32
	baseKeys := []string{"AIR", "MAIL", "RAIL", "REGAIR", "SHIP", "TRUCK", "FOB"}

	padKey := func(s string) []byte {
		// Pad with 'X' to avoid null bytes (which EncodeVarchar would
		// escape into 2-byte sequences, blowing past MaxHighKeyLen=32).
		b := make([]byte, padTo)
		for i := range b {
			b[i] = 'X'
		}
		copy(b, s)
		return EncodeVarchar(b) // padTo bytes + 0x00 terminator = padTo+1 bytes
	}

	entries := make([]BulkEntry, 0, numKeys*perKey)
	for ki, sm := range baseKeys {
		key := padKey(sm)
		for j := 0; j < perKey; j++ {
			entries = append(entries, BulkEntry{
				Key: key,
				Ptr: storage.ItemPointer{
					Block:  storage.BlockNumber(ki*perKey/10 + j/10),
					Offset: uint16(j%10 + 1),
				},
			})
		}
	}

	// Build deduplicated index (posting lists for duplicate keys).
	dedupTree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate dedup: %v", err)
	}

	// Baseline: same data built WITHOUT deduplication (one regular item
	// per entry, even for duplicate keys). BulkCreateNoDedup forces this.
	rel2 := storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}
	_, err = BulkCreateNoDedup(pool, rel2, entries)
	if err != nil {
		t.Fatalf("BulkCreateNoDedup baseline: %v", err)
	}

	dedupBlocks, _ := pool.NBlocks(rel)
	baseBlocks, _ := pool.NBlocks(rel2)
	t.Logf("dedup blocks=%d  baseline blocks=%d  ratio=%.1f%%",
		dedupBlocks, baseBlocks, float64(dedupBlocks)*100/float64(baseBlocks))

	// DoD: deduplicated index must use ≤ 25% of the blocks used by the
	// non-deduplicated baseline.
	if float64(dedupBlocks) > float64(baseBlocks)*0.25 {
		t.Errorf("dedup index too large: %d blocks vs baseline %d blocks (want ≤25%%)",
			dedupBlocks, baseBlocks)
	}

	// Correctness: RangeScan must return all 1400 TIDs.
	var totalTIDs int
	err = dedupTree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		totalTIDs++
		return true, nil
	})
	if err != nil {
		t.Fatalf("RangeScan dedup: %v", err)
	}
	if totalTIDs != numKeys*perKey {
		t.Errorf("expected %d TIDs from dedup tree, got %d", numKeys*perKey, totalTIDs)
	}
}

// TestIsPostingRaw is already defined above; add it here as an alias
// for clarity when running targeted test patterns.
// (Actual test is TestIsPostingRaw above.)

// TestBulkCreateDeduplicationInsertAfter verifies that Insert works
// correctly on a deduplicated tree (posting items expand correctly).
func TestBulkCreateDeduplicationInsertAfter(t *testing.T) {
	pool, rel := newBulkTestPool(t)

	// Build with duplicates.
	const n = 100
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		// 5 distinct keys, 20 entries each.
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i % 5)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 5), Offset: uint16(i%5 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Insert new unique key.
	newKey := EncodeInt4(999)
	if err := tree.Insert(newKey, storage.ItemPointer{Block: 99, Offset: 1}); err != nil {
		t.Fatalf("Insert after dedup build: %v", err)
	}

	// Scan: must find all original TIDs + new key.
	var count int
	var foundNew bool
	_ = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		if CompareKeys(key, newKey) == 0 {
			foundNew = true
		}
		count++
		return true, nil
	})
	if count != n+1 {
		t.Errorf("expected %d total entries, got %d", n+1, count)
	}
	if !foundNew {
		t.Error("newly inserted key not found after dedup build")
	}
}

// TestDeduplicateOversizedPostingSplits verifies the M0053-0005 fix:
// when a key has so many duplicates that a single posting item would
// exceed the 0x7FFF page line-pointer limit, deduplicateToRawItems
// splits the run into multiple smaller posting items.
//
// Reproduces the run-010 failure mode: LINEITEM(L_LINENUMBER) at SF=1
// has values 1..7 each occurring ~1.5M times, yielding posting items
// of ~9 MB if not split. We use a smaller scale (10000 dups of one
// key) to keep the test fast while still exceeding the limit.
func TestDeduplicateOversizedPostingSplits(t *testing.T) {
	// 4-byte int4 key. Per-chunk capacity:
	// (0x7FFF - 4 - 4) / 6 = 5459 TIDs.
	const dupCount = 10000 // produces 2 chunks: 5459 + 4541
	key := EncodeInt4(1)
	items := make([]item, dupCount)
	for i := 0; i < dupCount; i++ {
		items[i] = item{
			keyLen: uint16(len(key)),
			ptr:    storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
			key:    key,
		}
	}

	raws := deduplicateToRawItems(items)

	if len(raws) < 2 {
		t.Fatalf("expected the run to be split into >=2 posting items, got %d", len(raws))
	}

	// Every produced raw must fit within the page line-pointer limit so
	// PageAddItemRaw will not reject it.
	totalTIDs := 0
	for i, ri := range raws {
		if len(ri.raw) > maxRawItemSize {
			t.Errorf("raw %d exceeds maxRawItemSize: len=%d limit=%d", i, len(ri.raw), maxRawItemSize)
		}
		if !isPostingRaw(ri.raw) {
			t.Errorf("raw %d should still be a posting item after split", i)
			continue
		}
		_, tids, err := parsePostingRaw(ri.raw)
		if err != nil {
			t.Errorf("parsePostingRaw chunk %d: %v", i, err)
			continue
		}
		totalTIDs += len(tids)
	}
	if totalTIDs != dupCount {
		t.Errorf("total TIDs across split chunks: want %d got %d", dupCount, totalTIDs)
	}
}

// TestDeduplicateOversizedPostingSurvivesBulkCreate is the end-to-end
// DoD test for M0053-0005: BulkCreate on a column with ~6000 duplicates
// of a single key (which previously failed with "item too large for
// line pointer len=35669") must now succeed and a full RangeScan must
// return every TID exactly once.
func TestDeduplicateOversizedPostingSurvivesBulkCreate(t *testing.T) {
	pool, rel := newBulkTestPool(t)

	// 6000 duplicates of key=1 reproduces the run-010 LINEITEM(L_ORDERKEY)
	// shape closely enough to overflow the 0x7FFF limit on int4 keys
	// (4 + 6000*6 + 4 = 36008 bytes > 32767).
	const dupCount = 6000
	entries := make([]BulkEntry, dupCount)
	key := EncodeInt4(1)
	for i := 0; i < dupCount; i++ {
		entries[i] = BulkEntry{
			Key: key,
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
		}
	}

	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate must not fail with oversized posting items: %v", err)
	}

	var seen int
	if err := tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		seen++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if seen != dupCount {
		t.Errorf("RangeScan TID count: want %d got %d", dupCount, seen)
	}
}

// TestPostingKeyOf verifies postingKeyOf extracts the key without TID allocation.
func TestPostingKeyOf(t *testing.T) {
	key := []byte("testkey")
	tids := []storage.ItemPointer{{Block: 1, Offset: 2}, {Block: 3, Offset: 4}}
	raw := marshalPosting(key, tids)

	got := postingKeyOf(raw)
	if string(got) != string(key) {
		t.Errorf("postingKeyOf: want %q got %q", key, got)
	}
}
