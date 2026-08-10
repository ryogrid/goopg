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
// The key is still one opaque blob (see `(item).marshal`), so its length is
// recovered as postingOffset - SizeOfIndexTupleData and postingOffset is NOT
// MAXALIGNed. Slice 3 fixes both together.

// BTPostingMaxTIDs is the largest TID count the offset-number field can hold
// (BT_OFFSET_MASK). Far above what a page can physically store — one 8 KiB
// page holds at most ~1350 six-byte TIDs — so it is a structural guard, not a
// policy limit.
const BTPostingMaxTIDs = BTOffsetMask

// marshalPosting serialises a posting-list item for `key` over `tids`.
// Callers hold to upstream's rule that a posting list has at least two TIDs;
// a shorter list is a caller bug and panics rather than silently writing a
// tuple whose alt-TID bits say "posting" but whose array holds one entry.
func marshalPosting(key []byte, tids []storage.ItemPointer) []byte {
	n := len(tids)
	postingOffset := SizeOfIndexTupleData + len(key)
	raw := make([]byte, postingOffset+n*6)
	binary.LittleEndian.PutUint16(raw[6:8], uint16(len(raw)))
	copy(raw[SizeOfIndexTupleData:], key)
	off := postingOffset
	for _, tid := range tids {
		PutPGItemPointer(raw[off:off+6], tid)
		off += 6
	}
	if err := BTreeTupleSetPosting(raw, uint16(n), postingOffset); err != nil {
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
func parsePostingRaw(raw []byte) (key []byte, tids []storage.ItemPointer, err error) {
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
	key = append([]byte(nil), raw[SizeOfIndexTupleData:postingOffset]...)
	return key, tids, nil
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
	if postingOffset < SizeOfIndexTupleData || postingOffset+n*6 != size {
		return 0, 0, fmt.Errorf("btree posting: size mismatch (size %d, postingOffset %d, n %d)",
			size, postingOffset, n)
	}
	return postingOffset, n, nil
}

// postingKeyOf extracts only the key bytes from a raw posting item
// without allocating a TID slice. Used for key comparisons.
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
func appendTIDToPosting(raw []byte, tid storage.ItemPointer) ([]byte, error) {
	key, tids, err := parsePostingRaw(raw)
	if err != nil {
		return nil, err
	}
	tids = append(tids, tid)
	if len(tids) > BTPostingMaxTIDs {
		return nil, fmt.Errorf("btree posting: %d TIDs exceeds BT_OFFSET_MASK %d", len(tids), BTPostingMaxTIDs)
	}
	return marshalPosting(key, tids), nil
}

// promoteSingleToPosting (M0055-0003) builds a 2-TID posting
// payload from an existing single-TID line pointer's `(key, ptr)`
// plus a new `tid`. Used by the steady-state insert path when a
// duplicate-key insert hits a non-posting line pointer.
func promoteSingleToPosting(existingKey []byte, existingPtr storage.ItemPointer, newTID storage.ItemPointer) []byte {
	return marshalPosting(existingKey, []storage.ItemPointer{existingPtr, newTID})
}
