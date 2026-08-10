package btree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-vi guards.
//
// Two claims, and neither is visible to a row-count gate — a build whose
// posting lists all collapse to length 1 returns exactly the same rows in
// exactly the same order as one that deduplicates properly, it is just several
// times larger. So both are asserted STRUCTURALLY, on the encoded items:
//
//  1. a posting run is closed by the KEY ATTRIBUTES, not by the tree's
//     ordering (which, in the tuple format, breaks ties on the heap TID and
//     therefore never reports equality);
//  2. the posting tuple's LAYOUT follows the format — key-at-8 for a blob
//     payload, MAXALIGNed key-at-0 for a tuple — and the parse gives back the
//     plain leaf tuple the posting stands for.

// tupleEntries builds n items sharing one int4 key value but naming distinct
// heap rows: exactly the input a duplicate-heavy build sees.
func tupleEntries(t *testing.T, f indexFormat, val int32, n int) []item {
	t.Helper()
	out := make([]item, n)
	for i := range out {
		tid := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: uint16(i + 1)}
		out[i] = item{ptr: tid, key: tup(t, f.desc.Attrs, [][]byte{int4Val(val)}, tid)}
	}
	return out
}

// TestDedupGroupsByKeyAttrsNotOrdering is the slice's reason to exist: under
// the tuple format the run must close on the key attributes. Grouping by
// `compare` (the tree's ordering) is asserted to be the broken alternative, so
// the test fails if the fix is reverted rather than merely passing by luck.
func TestDedupGroupsByKeyAttrsNotOrdering(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	items := tupleEntries(t, f, 42, 5)

	raws := deduplicateToRawItems(f, items)
	if len(raws) != 1 {
		t.Fatalf("deduplicateToRawItems produced %d items for 5 entries of one key, want 1 posting", len(raws))
	}
	if !isPostingRaw(raws[0].raw) {
		t.Fatalf("the single item is not a posting tuple: %x", raws[0].raw)
	}

	// The ordering says all five are distinct — that is correct FOR ORDERING
	// (a heapkeyspace tree is totally ordered by key then heap TID) and is
	// precisely why it cannot close a posting run.
	for i := 1; i < len(items); i++ {
		if f.compare(items[i].key, items[0].key) == 0 {
			t.Fatalf("entry %d compares EQUAL under the ordering; the TID tiebreak is missing "+
				"and this test can no longer distinguish the two groupings", i)
		}
		if f.compareKeyAttrs(items[i].key, items[0].key) != 0 {
			t.Fatalf("entry %d compares unequal on key attributes, want equal", i)
		}
	}
}

// TestDedupBlobFormatUnchanged pins the no-op half: a descriptor-less index
// must group and encode exactly as it did before the seam, because that is
// what is on disk today.
func TestDedupBlobFormatUnchanged(t *testing.T) {
	key := EncodeInt4(7)
	items := make([]item, 4)
	for i := range items {
		items[i] = item{ptr: storage.ItemPointer{Block: storage.BlockNumber(i), Offset: 1}, key: key}
	}
	raws := deduplicateToRawItems(blobFormat, items)
	if len(raws) != 1 {
		t.Fatalf("blob dedup produced %d items, want 1", len(raws))
	}
	tids := make([]storage.ItemPointer, len(items))
	for i, it := range items {
		tids[i] = it.ptr
	}
	if want := blobFormat.marshalPosting(key, tids); !bytes.Equal(raws[0].raw, want) {
		t.Fatalf("blob posting bytes = %x, want %x", raws[0].raw, want)
	}
	// The pre-seam layout, spelled out: header, then the key, then the TIDs,
	// with the posting offset NOT MAXALIGNed (a 3b-3 deferral).
	if got := blobFormat.postingOffsetFor(key); got != SizeOfIndexTupleData+len(key) {
		t.Fatalf("blob postingOffsetFor = %d, want %d", got, SizeOfIndexTupleData+len(key))
	}
	if !bytes.Equal(postingKeyOf(raws[0].raw), key) {
		t.Fatalf("blob posting key at [8:offset] = %x, want %x", postingKeyOf(raws[0].raw), key)
	}
}

// TestPostingTupleFormatLayoutAndRoundTrip pins the tuple-format layout
// against upstream's `_bt_form_posting` and closes the round trip: what comes
// back out of a posting is the plain leaf tuple it stands for, which is the
// only thing the rest of the package can compare or re-marshal.
func TestPostingTupleFormatLayoutAndRoundTrip(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	items := tupleEntries(t, f, 42, 3)
	base := items[0].key
	tids := []storage.ItemPointer{items[0].ptr, items[1].ptr, items[2].ptr}

	raw := f.marshalPosting(base, tids)

	// _bt_form_posting: the TID array starts at MAXALIGN(keysize), and the key
	// material is the base tuple itself at offset 0 — NOT after a second
	// header, which is what the blob layout would have produced.
	// A single int4 key is a 16-byte tuple (8-byte header, a 4-byte datum,
	// MAXALIGNed by FormPGIndexTuple exactly as index_form_tuple does), so the
	// TID array starts at 16 — the tuple itself, with NO second header in
	// front of it. The blob layout would have put it at 8+16 = 24, so this
	// constant is what separates the two layouts. (MAXALIGN is consequently a
	// no-op for every key a formed tuple can produce; it is in
	// postingOffsetFor because _bt_form_posting states the rule, not because a
	// caller here can violate it.)
	const wantOffset = 16
	if len(base) != wantOffset {
		t.Fatalf("int4 key tuple is %d bytes, not the %d this test's constants assume", len(base), wantOffset)
	}
	if got := BTreeTupleGetPostingOffset(raw); got != wantOffset {
		t.Fatalf("posting offset = %d, want the key tuple's own length %d (blob layout would say %d)",
			got, wantOffset, SizeOfIndexTupleData+len(base))
	}
	// _bt_form_posting's newsize: MAXALIGN(keysize + nhtids * 6), which for three
	// TIDs after a 16-byte key is MAXALIGN(34) = 40 — six bytes of inert tail
	// padding. The array's location and length are both stated in t_tid, so the
	// padding is unreadable by construction; asserting the ROUNDED total (rather
	// than the exact one goopg wrote before M0130-S11.4 slice 3b-3b) is what
	// keeps goopg's postings byte-identical to a promoted PG's.
	if len(raw) != MaxAlign(wantOffset+3*6) {
		t.Fatalf("posting size = %d, want MAXALIGN(%d) = %d", len(raw), wantOffset+3*6, MaxAlign(wantOffset+3*6))
	}
	if !BTreeTupleIsPosting(raw) || BTreeTupleGetNPosting(raw) != 3 {
		t.Fatalf("posting header wrong: isPosting=%v n=%d", BTreeTupleIsPosting(raw), BTreeTupleGetNPosting(raw))
	}

	gotKey, gotTIDs, err := f.parsePostingRaw(raw)
	if err != nil {
		t.Fatalf("parsePostingRaw: %v", err)
	}
	for i := range tids {
		if gotTIDs[i] != tids[i] {
			t.Fatalf("TID %d = %v, want %v", i, gotTIDs[i], tids[i])
		}
	}
	// The recovered key is a PLAIN leaf tuple naming the first TID: the
	// alt-TID bit is off (so BTreeTupleGetNAtts reads it as a full-width
	// entry) and the ordering accepts it, which `ComparePGIndexTuples`
	// explicitly refuses to do for a posting tuple.
	if BTreeTupleIsPosting(gotKey) || PGIndexTupleIsAltTID(gotKey) {
		t.Fatalf("recovered key still carries the posting/alt-TID bits: %x", gotKey)
	}
	if PGIndexTupleTID(gotKey) != tids[0] {
		t.Fatalf("recovered key names %v, want the first TID %v", PGIndexTupleTID(gotKey), tids[0])
	}
	if _, err := ComparePGIndexTuples(f.desc, gotKey, base); err != nil {
		t.Fatalf("recovered key is not comparable: %v", err)
	}
	if f.compare(gotKey, base) != 0 {
		t.Fatalf("recovered key does not equal the base tuple under the ordering")
	}
	// Re-marshalling it reproduces the posting byte for byte — the round trip
	// is what appendTIDToPosting and the page rewrites rely on.
	if again := f.marshalPosting(gotKey, gotTIDs); !bytes.Equal(again, raw) {
		t.Fatalf("re-marshal = %x, want %x", again, raw)
	}
}

// TestPostingItemsStampsPerTIDKeys guards the expansion every page reader
// performs. A tuple carries its own heap TID, so handing the same key to every
// expanded item would make `item.key` disagree with `item.ptr` — and
// `indexFormat.marshal` writes the tuple back from the key, so the
// disagreement would reach the page.
func TestPostingItemsStampsPerTIDKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		f     indexFormat
		key   []byte
		tuple bool
	}{
		{name: "blob", f: blobFormat, key: EncodeInt4(7)},
		{name: "tuple", f: indexFormat{desc: int4Desc()}, tuple: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			key := tc.key
			tids := []storage.ItemPointer{{Block: 1, Offset: 1}, {Block: 2, Offset: 9}, {Block: 3, Offset: 4}}
			if tc.tuple {
				key = tup(t, f.desc.Attrs, [][]byte{int4Val(42)}, tids[0])
			}
			its, err := f.postingItems(f.marshalPosting(key, tids))
			if err != nil {
				t.Fatalf("postingItems: %v", err)
			}
			if len(its) != len(tids) {
				t.Fatalf("expanded to %d items, want %d", len(its), len(tids))
			}
			for i, it := range its {
				if it.ptr != tids[i] {
					t.Fatalf("item %d ptr = %v, want %v", i, it.ptr, tids[i])
				}
				if !tc.tuple {
					if !bytes.Equal(it.key, key) {
						t.Fatalf("blob item %d key = %x, want the shared payload %x", i, it.key, key)
					}
					continue
				}
				if PGIndexTupleTID(it.key) != tids[i] {
					t.Fatalf("tuple item %d key names %v, want its own TID %v", i, PGIndexTupleTID(it.key), tids[i])
				}
				// Marshalling it back must be a fixpoint: key and ptr agree.
				if !bytes.Equal(f.marshal(it), it.key) {
					t.Fatalf("tuple item %d does not re-marshal to its own key", i)
				}
				if f.compareKeyAttrs(it.key, its[0].key) != 0 {
					t.Fatalf("tuple item %d lost its key value during expansion", i)
				}
			}
		})
	}
}

// TestDedupChunkingUsesFormatOffset guards the split of an oversized run: the
// per-chunk TID budget is computed from the format's posting offset, so a
// tuple-format key (whose header is INSIDE the key) is not charged for a
// second one. Getting this wrong overflows a page rather than returning an
// error, which is why it is asserted on the encoded sizes.
func TestDedupChunkingUsesFormatOffset(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	items := tupleEntries(t, f, 42, 4000) // 4000 × 6 B of TIDs > maxRawItemSize
	raws := deduplicateToRawItems(f, items)
	if len(raws) < 2 {
		t.Fatalf("4000 duplicates produced %d items, want a split run", len(raws))
	}
	total := 0
	for i, r := range raws {
		if len(r.raw) > maxRawItemSize {
			t.Fatalf("chunk %d is %d bytes, over the %d limit", i, len(r.raw), maxRawItemSize)
		}
		if isPostingRaw(r.raw) {
			_, tids, err := f.parsePostingRaw(r.raw)
			if err != nil {
				t.Fatalf("chunk %d: %v", i, err)
			}
			total += len(tids)
			continue
		}
		total++
	}
	if total != len(items) {
		t.Fatalf("chunks hold %d TIDs, want all %d", total, len(items))
	}
	// And it is still a real saving: one item per TID would be 4000 items.
	if len(raws) > 10 {
		t.Fatalf("4000 duplicates needed %d items; deduplication is not doing its job", len(raws))
	}
}

// TestPostingBoundsToleratesAlignmentPaddingOnly is the read side of
// M0130-S11.4 slice 3b-3b. A posting tuple's TID array is located and counted
// by t_tid alone, so upstream's MAXALIGNed total leaves up to seven bytes of
// tail padding that no reader looks at. goopg's bounds check used to demand the
// array end exactly at the declared size, which rejected every posting a
// promoted PG writes (`_bt_form_posting` has always rounded). It must now
// tolerate the padding — and ONLY the padding: a tail of eight bytes or more is
// not alignment, it is a TID the count does not admit to, and staying strict
// there is what keeps the check a corruption detector.
func TestPostingBoundsToleratesAlignmentPaddingOnly(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	items := tupleEntries(t, f, 77, 3)
	base := items[0].key
	tids := []storage.ItemPointer{items[0].ptr, items[1].ptr, items[2].ptr}
	raw := f.marshalPosting(base, tids)

	off, n, err := postingBounds(raw)
	if err != nil {
		t.Fatalf("postingBounds on a MAXALIGNed posting: %v", err)
	}
	if n != len(tids) {
		t.Fatalf("n = %d, want %d", n, len(tids))
	}
	if pad := len(raw) - (off + n*SizeOfItemPointerData); pad == 0 {
		t.Fatalf("this fixture is meant to carry tail padding; got none (size %d, off %d, n %d)",
			len(raw), off, n)
	}

	// One padding byte too many: the same header over a tuple grown by a whole
	// MAXALIGN unit is a size the alignment rule cannot produce.
	overpadded := append(append([]byte(nil), raw...), make([]byte, 8)...)
	pgPutIndexTupleSize(overpadded, len(overpadded))
	if _, _, err := postingBounds(overpadded); err == nil {
		t.Fatal("postingBounds accepted a posting with a full MAXALIGN unit of unexplained tail")
	}

	// And the array must still fit: a count that runs past the declared size is
	// corruption in the other direction.
	truncated := append([]byte(nil), raw[:off+SizeOfItemPointerData]...)
	pgPutIndexTupleSize(truncated, len(truncated))
	if _, _, err := postingBounds(truncated); err == nil {
		t.Fatal("postingBounds accepted a posting whose TID array runs past its size")
	}
}

// TestPostingBlobFormatSizeStaysExact pins the deliberate asymmetry of 3b-3b:
// the blob format does NOT round. Its posting offset is `8 + len(key)`, already
// unaligned by construction because a blob key has no tuple of its own, so
// rounding the total would rewrite every blob index's bytes on disk without
// buying any upstream invariant.
func TestPostingBlobFormatSizeStaysExact(t *testing.T) {
	key := []byte("abcde")
	tids := []storage.ItemPointer{{Block: 1, Offset: 1}, {Block: 1, Offset: 2}}
	raw := blobFormat.marshalPosting(key, tids)
	want := SizeOfIndexTupleData + len(key) + len(tids)*SizeOfItemPointerData
	if len(raw) != want {
		t.Fatalf("blob posting size = %d, want the exact %d", len(raw), want)
	}
	gotKey, gotTIDs, err := blobFormat.parsePostingRaw(raw)
	if err != nil {
		t.Fatalf("parsePostingRaw: %v", err)
	}
	if !bytes.Equal(gotKey, key) || len(gotTIDs) != len(tids) {
		t.Fatalf("blob round trip: key %x tids %v", gotKey, gotTIDs)
	}
}
