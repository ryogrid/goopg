package btree

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// Posting-list items in upstream's shape (M0130-S11.4 slice 2).
//
// A deduplicated nbtree tuple is an ordinary IndexTupleData whose t_tid does
// NOT address a heap row: INDEX_ALT_TID_MASK is set in t_info, the offset half
// of t_tid carries BT_IS_POSTING|nhtids and its block half carries the byte
// offset of a trailing ItemPointerData array
// (postgres/src/include/access/nbtree.h, `BTreeTupleSetPosting`). Layout:
//
//	[0:6]                  t_tid  = (postingOffset, nhtids|BT_IS_POSTING)
//	[6:8]                  t_info = INDEX_ALT_TID_MASK | total size
//	[8:postingOffset]      key bytes
//	[postingOffset:size]   nhtids × 6-byte ItemPointerData, ascending
//
// Before slice 2 this was a goopg-private layout discriminated by the high bit
// of a leading keyLen field. That bit now lands inside t_tid's bi_hi half, so
// the discriminator had to move with the tuple shape or a large enough block
// number would have made a plain leaf item read back as a posting list.
//
// Slice 3b-2c-ii-B2-c-vi makes the layout a property of the index's format,
// because the two formats disagree about what "the key" is:
//
//   - blob format (desc == nil): the key is an opaque payload that the header
//     is built AROUND, so it sits at [8:postingOffset] and postingOffset is
//     8+len(key), NOT MAXALIGNed (the alignment gap is a 3b-3 deferral);
//   - tuple format (desc != nil): the key IS a whole nbtree tuple, so it sits
//     at [0:len(key)] and postingOffset is MAXALIGN(len(key)) — exactly
//     upstream's `_bt_form_posting` (nbtdedup.c), which copies `keysize` bytes
//     of the base tuple and puts the TID array at MAXALIGN(keysize).
//
// The round trip is stable in both: parsing a posting yields the plain leaf
// tuple the posting stands for (its first TID stamped back into t_tid, the
// alt-TID bit cleared), which is precisely the `base` a re-marshal expects.

// BTPostingMaxTIDs is the largest TID count the offset-number field can hold
// (BT_OFFSET_MASK). Far above what a page can physically store — one 8 KiB
// page holds at most ~1350 six-byte TIDs — so it is a structural guard, not a
// policy limit.
const BTPostingMaxTIDs = BTOffsetMask

// postingOffsetFor is where the TID array starts for a posting item over
// `key` in this format — and, equivalently, how many bytes of key material the
// posting carries in front of it.
func (f indexFormat) postingOffsetFor(key []byte) int {
	if f.tupleKeys() {
		// _bt_form_posting: MAXALIGN(keysize), where the key IS the tuple.
		return MaxAlign(len(key))
	}
	return SizeOfIndexTupleData + len(key)
}

// postingSizeFor is `_bt_form_posting`'s `newsize` (nbtdedup.c): the whole
// on-page length of a posting item over `key` and `nhtids` TIDs.
//
// M0130-S11.4 slice 3b-3b. Upstream rounds the total —
// `newsize = MAXALIGN(keysize + nhtids * sizeof(ItemPointerData))`, with an
// `Assert(newsize == MAXALIGN(newsize))` behind it — because a six-byte TID
// array leaves the tuple unaligned even when the key material is not, and
// nbtree reasons about a posting's length with MAXALIGN throughout (the
// dedup pass's byte budget, `_bt_swap_posting`, `insertstate.itemsz`).
// goopg wrote the exact length, which is a divergence that only becomes
// REACHABLE with a real PG in the picture: after a failover a promoted PG
// writes MAXALIGNed postings into these indexes, and goopg has to read them
// back (see postingBounds, which used to reject the padding outright).
//
// Blob format keeps the exact length. Its posting offset is already unaligned
// by construction (`8 + len(key)` — a blob key has no tuple of its own to
// round), so rounding the total alone would buy no upstream property while
// rewriting the bytes of every blob index on disk.
func (f indexFormat) postingSizeFor(key []byte, nhtids int) int {
	size := f.postingOffsetFor(key) + nhtids*SizeOfItemPointerData
	if f.tupleKeys() {
		return MaxAlign(size)
	}
	return size
}

// marshalPosting serialises a posting-list item for `key` over `tids`.
// Callers hold to upstream's rule that a posting list has at least two TIDs;
// a shorter list is a caller bug and panics rather than silently writing a
// tuple whose alt-TID bits say "posting" but whose array holds one entry.
func (f indexFormat) marshalPosting(key []byte, tids []storage.ItemPointer) []byte {
	if f.tupleKeys() {
		return PGBTPostingRaw(key, tids)
	}
	n := len(tids)
	postingOffset := f.postingOffsetFor(key)
	raw := make([]byte, f.postingSizeFor(key, n))
	binary.LittleEndian.PutUint16(raw[6:8], uint16(len(raw)))
	copy(raw[SizeOfIndexTupleData:], key)
	off := postingOffset
	for _, tid := range tids {
		PutPGItemPointer(raw[off:off+SizeOfItemPointerData], tid)
		off += SizeOfItemPointerData
	}
	if err := BTreeTupleSetPosting(raw, uint16(n), postingOffset); err != nil {
		panic(err)
	}
	return raw
}

// PGBTPostingRaw is upstream's `_bt_form_posting` (nbtdedup.c) and the ONE
// encoder for a tuple-format posting-list item — the same role PGBTItemRaw
// plays for a plain item, exported for the same reason: out-of-package
// fixtures and validators must build postings through the engine's encoder
// rather than transcribe the alt-TID layout a second time.
//
// `base` is the plain leaf tuple the posting stands for; it is copied WHOLE
// (its header's null-bitmap / varwidth flag bits are what deforms it, so
// rebuilding a header around the key material would make the posting
// undecodable), and only the size half is restamped. The TID array starts at
// MAXALIGN(len(base)) and the total is MAXALIGNed, exactly as upstream's
// `keysize` / `newsize`.
func PGBTPostingRaw(base []byte, tids []storage.ItemPointer) []byte {
	postingOffset := MaxAlign(len(base))
	raw := make([]byte, MaxAlign(postingOffset+len(tids)*SizeOfItemPointerData))
	copy(raw, base)
	pgPutIndexTupleSize(raw, len(raw))
	off := postingOffset
	for _, tid := range tids {
		PutPGItemPointer(raw[off:off+SizeOfItemPointerData], tid)
		off += SizeOfItemPointerData
	}
	if err := BTreeTupleSetPosting(raw, uint16(len(tids)), postingOffset); err != nil {
		panic(err)
	}
	return raw
}

// isPostingRaw reports whether raw encodes a posting-list item.
func isPostingRaw(raw []byte) bool {
	if len(raw) < SizeOfIndexTupleData {
		return false
	}
	return BTreeTupleIsPosting(raw)
}

// parsePostingRaw decodes a posting-list item into its key and TID list.
// Returns an error when the bytes are structurally invalid.
//
// In the tuple format the returned key is the plain leaf TUPLE the posting
// stands for — the key material restamped to declare its own length, with the
// alt-TID bit cleared and t_tid set back to the first heap TID — not a slice of
// anonymous bytes. That is what every consumer of an `item.key` in this package
// expects (`indexFormat.compare` refuses a posting tuple outright), and it is
// the same `base` shape `marshalPosting` takes back, so the round trip closes.
func (f indexFormat) parsePostingRaw(raw []byte) (key []byte, tids []storage.ItemPointer, err error) {
	postingOffset, n, err := postingBounds(raw)
	if err != nil {
		return nil, nil, err
	}
	tids = make([]storage.ItemPointer, n)
	off := postingOffset
	for i := range tids {
		tids[i] = PGItemPointerAt(raw[off : off+6])
		off += 6
	}
	if !f.tupleKeys() {
		key = append([]byte(nil), raw[SizeOfIndexTupleData:postingOffset]...)
		return key, tids, nil
	}
	key = append([]byte(nil), raw[:postingOffset]...)
	// Undo BTreeTupleSetPosting: size back to the key material's own length
	// (padding included, as upstream's keysize is), alt-TID bit off, and the
	// first heap TID back in t_tid. A posting always has n >= 1 here —
	// postingBounds proved the array is that long.
	binary.LittleEndian.PutUint16(key[6:8], pgTInfo(key)&^IndexAltTIDMask)
	pgPutIndexTupleSize(key, postingOffset)
	PutPGItemPointer(key, tids[0])
	return key, tids, nil
}

// postingItems expands a posting-list item into one `item` per heap TID, the
// form every in-package page reader hands to insert/sort/compare code.
//
// It exists because the expansion is NOT "the same key repeated" once keys are
// tuples: an entry's key carries its own heap TID, so each expanded item needs
// its TID stamped into its own copy. Sharing one key slice across the run (what
// the blob format did, correctly, because a blob key has no TID in it) would
// give every expanded item the FIRST TID — an ordering that disagrees with
// `item.ptr` and with what a re-marshal writes back.
func (f indexFormat) postingItems(raw []byte) ([]item, error) {
	key, tids, err := f.parsePostingRaw(raw)
	if err != nil {
		return nil, err
	}
	out := make([]item, len(tids))
	for i, tid := range tids {
		k := key
		if f.tupleKeys() && i > 0 {
			k = append([]byte(nil), key...)
			PutPGItemPointer(k, tid)
		}
		out[i] = item{ptr: tid, key: k}
	}
	return out, nil
}

// postingBounds validates a posting tuple's header and returns its posting
// offset and TID count.
func postingBounds(raw []byte) (postingOffset, n int, err error) {
	if len(raw) < SizeOfIndexTupleData {
		return 0, 0, fmt.Errorf("btree posting: raw too short (%d bytes)", len(raw))
	}
	size := PGIndexTupleSize(raw)
	if size != len(raw) {
		return 0, 0, fmt.Errorf("btree posting: t_info size %d != raw length %d", size, len(raw))
	}
	postingOffset = BTreeTupleGetPostingOffset(raw)
	n = int(BTreeTupleGetNPosting(raw))
	// The TID array need not end at the tuple's declared size: since 3b-3b (and
	// in upstream `_bt_form_posting` always) the total is MAXALIGNed, so up to
	// seven bytes of padding may follow the last TID. The array's LOCATION and
	// LENGTH are both stated explicitly in t_tid, so the padding is inert — but
	// it must be tolerated rather than read as a count mismatch, or a posting
	// written by a promoted PG (or by goopg after this slice) fails to parse.
	body := postingOffset + n*SizeOfItemPointerData
	if postingOffset < SizeOfIndexTupleData || body > size || size-body >= 8 {
		return 0, 0, fmt.Errorf("btree posting: size mismatch (size %d, postingOffset %d, n %d)",
			size, postingOffset, n)
	}
	return postingOffset, n, nil
}

// postingKeyOf extracts only the key bytes from a BLOB-format posting item
// without allocating a TID slice.
//
// It is deliberately not an indexFormat method: a tuple-format key cannot be
// returned as a sub-slice of the posting at all (the header has to be
// restamped — see parsePostingRaw), so a "cheap aliasing key" is a
// blob-format-only affordance and naming the format keeps that explicit rather
// than implied. No production caller remains; the round-trip tests do.
func postingKeyOf(raw []byte) []byte {
	postingOffset, _, err := postingBounds(raw)
	if err != nil {
		return nil
	}
	return raw[SizeOfIndexTupleData:postingOffset]
}

// appendTIDToPosting (M0055-0003) returns a new posting payload
// with `tid` appended to an existing posting's TID list. Used by
// the steady-state insert path to grow a posting in place when an
// inserted key matches an existing posting's key, instead of
// allocating a fresh single-TID line pointer.
func (f indexFormat) appendTIDToPosting(raw []byte, tid storage.ItemPointer) ([]byte, error) {
	key, tids, err := f.parsePostingRaw(raw)
	if err != nil {
		return nil, err
	}
	tids = append(tids, tid)
	if len(tids) > BTPostingMaxTIDs {
		return nil, fmt.Errorf("btree posting: %d TIDs exceeds BT_OFFSET_MASK %d", len(tids), BTPostingMaxTIDs)
	}
	return f.marshalPosting(key, tids), nil
}

// SwapPosting is upstream's `_bt_swap_posting` (nbtdedup.c), the posting-list
// SPLIT: `newitem`'s heap TID belongs in the middle of the existing posting
// `oposting`, at array index `postingoff`. It returns the rewritten posting and
// the FINAL form of the new item.
//
// The trade is upstream's and is not obvious from the name — nothing grows:
//
//   - `nposting` keeps oposting's exact byte length and TID count. TIDs at
//     [postingoff, nhtids-1) shift one slot right, oposting's rightmost/max TID
//     falls off the end, and newitem's original TID fills the gap at postingoff.
//   - the evicted max TID becomes the new item's t_tid, so the item that is
//     actually inserted on the page is NOT the item the caller passed in.
//
// Keeping the length fixed is what lets the caller overwrite oposting in place
// (`memcpy(oposting, nposting, MAXALIGN(IndexTupleSize(nposting)))` upstream,
// PageReplaceItemRaw's in-place branch here) — the page only has to find room
// for the one new item.
//
// M0131-S21b part 2. goopg's own insert path never splits a posting list (it
// appends via appendTIDToPosting), so this exists for WAL redo of a real PG's
// XLOG_BTREE_INSERT_POST: redo must repeat the primary's split byte for byte,
// because the record carries `orignewitem`, not the final item.
//
// Both inputs are read-only; the returned slices are fresh.
func SwapPosting(newitem, oposting []byte, postingoff int) (nposting, finalNewitem []byte, err error) {
	postingOffset, nhtids, err := postingBounds(oposting)
	if err != nil {
		return nil, nil, fmt.Errorf("btree swap-posting: %w", err)
	}
	if !BTreeTupleIsPosting(oposting) {
		return nil, nil, fmt.Errorf("btree swap-posting: existing item is not a posting list")
	}
	// Upstream's own sanity check: postingoff came from _bt_binsrch_posting and
	// is 0 only when the leaf page holds a non-pivot tuple identical to newitem
	// (corruption). Redo must refuse rather than write a posting whose TIDs are
	// no longer ascending.
	if postingoff <= 0 || postingoff >= nhtids {
		return nil, nil, fmt.Errorf("btree swap-posting: posting list tuple with %d items cannot be split at offset %d", nhtids, postingoff)
	}
	if len(newitem) < SizeOfIndexTupleData {
		return nil, nil, fmt.Errorf("btree swap-posting: new item too short (%d bytes)", len(newitem))
	}
	if BTreeTupleIsPosting(newitem) || BTreeTupleIsPivot(newitem) {
		return nil, nil, fmt.Errorf("btree swap-posting: new item is a posting list or pivot tuple")
	}

	nposting = append([]byte(nil), oposting...)
	tidAt := func(i int) int { return postingOffset + i*SizeOfItemPointerData }
	// copy() is memmove semantics in Go, so the overlap is safe.
	copy(nposting[tidAt(postingoff+1):tidAt(nhtids)], nposting[tidAt(postingoff):tidAt(nhtids-1)])
	PutPGItemPointer(nposting[tidAt(postingoff):tidAt(postingoff+1)], PGIndexTupleTID(newitem))

	finalNewitem = append([]byte(nil), newitem...)
	PutPGItemPointer(finalNewitem, PGItemPointerAt(oposting[tidAt(nhtids-1):tidAt(nhtids)]))
	return nposting, finalNewitem, nil
}

// promoteSingleToPosting (M0055-0003) builds a 2-TID posting
// payload from an existing single-TID line pointer's `(key, ptr)`
// plus a new `tid`. Used by the steady-state insert path when a
// duplicate-key insert hits a non-posting line pointer.
func (f indexFormat) promoteSingleToPosting(existingKey []byte, existingPtr storage.ItemPointer, newTID storage.ItemPointer) []byte {
	return f.marshalPosting(existingKey, []storage.ItemPointer{existingPtr, newTID})
}
