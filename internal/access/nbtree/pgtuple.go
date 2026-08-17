package nbtree

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4, doc docs/design/0130-0011-nbtree-pg-on-disk-format.md
// §"S11.4 — tuple shape".
//
// This file is the UPSTREAM index-tuple layout — `IndexTupleData` plus the
// nbtree alternative-TID conventions. Slice 1 landed it as an additive layer;
// slice 2 flipped the writers, so `(item).marshal` / `parseItem` /
// `marshalPosting` in btree.go and posting.go now speak exactly this shape and
// the goopg-private `itemPrefixSize` layout (keyLen | flat LE uint32 block |
// offset) is gone. What slice 3 still owes: the key is one opaque
// order-preserving blob rather than per-attribute binary datums, so
// FormPGIndexTuple/DeformPGIndexTuple below are not yet on the writer path,
// and the two MAXALIGNs that depend on a descriptor-derived key length
// (index_form_tuple's size rounding and the posting offset) are deferred.
//
// Why it is needed: after S11.2b/S11.3 a real PG 18.3 accepts a goopg index
// PAGE (`_bt_checkpage` passes), but the first thing it then does is
// `PageGetItem` + `index_getattr`, which interprets the item body as
//
//	IndexTupleData { ItemPointerData t_tid; unsigned short t_info; }
//	[ IndexAttributeBitMapData, iff t_info & INDEX_NULL_MASK ]
//	[ attribute values, starting at MAXALIGN(header [+ bitmap]) ]
//
// Before slice 2 goopg's body was none of that: its first two bytes were a key
// length, its pointer a flat LE uint32 block (upstream splits BlockIdData into
// (bi_hi, bi_lo) uint16 halves — see storage/block.h BlockIdSet), and its key
// bytes an order-preserving *encoding* (EncodeInt4 flips the sign bit) rather
// than the type's binary datum image. The header and the pointer are fixed;
// the key encoding is what slice 3 still has to convert.
//
// The byte layout is pinned by tests against the hand-rolled per-catalog
// encoders goopg already ships in internal/initdb/btree_index_bootstrap.go and
// internal/executor/sys_catalog_index_insert.go, which produce real
// index_form_tuple output for a handful of fixed shapes — a real PG 18.3 reads
// the bootstrap indexes they write, so they are an oracle.

// Sizes and masks from postgres/src/include/access/itup.h.
const (
	// SizeOfIndexTupleData is sizeof(IndexTupleData): ItemPointerData (6) +
	// unsigned short t_info (2). No trailing padding — the struct's strictest
	// member is a uint16.
	SizeOfIndexTupleData = 8

	// SizeOfItemPointerData is sizeof(ItemPointerData) (postgres/src/include/
	// storage/itemptr.h): BlockIdData (two uint16 halves) + OffsetNumber. It is
	// the size of t_tid, and also the size of the tiebreaker heap TID a pivot
	// tuple carries after its key attributes.
	SizeOfItemPointerData = 6

	// IndexMaxKeys is INDEX_MAX_KEYS (postgres/src/include/pg_config_manual.h).
	IndexMaxKeys = 32

	// SizeOfIndexAttributeBitMapData is sizeof(IndexAttributeBitMapData) =
	// (INDEX_MAX_KEYS + 8 - 1) / 8. Note the bitmap is a FIXED size that does
	// not vary with the index's column count: itup.h explains there is no room
	// in the header to record the attribute count, and MAXALIGN would eat the
	// savings anyway.
	SizeOfIndexAttributeBitMapData = (IndexMaxKeys + 8 - 1) / 8

	// t_info bit layout: high bit has-nulls, next has-varwidth, next
	// AM-defined, low 13 bits the tuple size.
	IndexSizeMask      = 0x1FFF
	IndexAMReservedBit = 0x2000
	IndexVarMask       = 0x4000
	IndexNullMask      = 0x8000

	// IndexAltTIDMask is nbtree's name for the AM-defined bit
	// (postgres/src/include/access/nbtree.h): when set, t_tid is NOT a heap
	// pointer but carries nbtree status bits — the tuple is a pivot tuple or a
	// posting-list tuple.
	IndexAltTIDMask = IndexAMReservedBit
)

// nbtree's t_tid offset-number overlay for alternative-TID tuples
// (postgres/src/include/access/nbtree.h).
const (
	BTOffsetMask       = 0x0FFF
	BTStatusOffsetMask = 0xF000

	// BTPivotHeapTIDAttr marks a pivot tuple that keeps a tiebreaker heap TID
	// as a trailing (untyped) attribute in its last sizeof(ItemPointerData)
	// bytes.
	BTPivotHeapTIDAttr = 0x1000
	// BTIsPosting marks a posting-list tuple (deduplication).
	BTIsPosting = 0x2000
)

// MaxAlign is upstream's MAXALIGN for a 64-bit build (MAXIMUM_ALIGNOF = 8),
// which is the only target goopg's on-disk format claims compatibility with.
func MaxAlign(n int) int { return (n + 7) &^ 7 }

// MaxAlignDown is upstream's MAXALIGN_DOWN.
func MaxAlignDown(n int) int { return n &^ 7 }

// BTMaxItemSize / BTMaxItemSizeNoHeapTid are nbtree.h's per-item ceilings:
// three items must fit on every page, so one item may occupy at most a third
// of the page's usable space, and _bt_truncate must be able to grow a leaf
// tuple by a tiebreaker heap TID (hence the extra MAXALIGN(sizeof(
// ItemPointerData)) subtraction in the first form).
//
// With BLCKSZ 8192 these evaluate to 2704 and 2712; TestBTMaxItemSizeMatchesUpstream
// re-derives them from the same terms so a future BLCKSZ change cannot silently
// drift.
const (
	btThirdPageBudget      = (storage.BlockSize - (storage.SizeOfPageHeaderData+3*4+7)&^7 - SizeOfBTPageOpaquePG) / 3
	BTMaxItemSizeNoHeapTid = btThirdPageBudget &^ 7
	BTMaxItemSize          = BTMaxItemSizeNoHeapTid - 8 // - MAXALIGN(sizeof(ItemPointerData))
)

// PGIndexAttr is the subset of Form_pg_attribute (really CompactAttribute)
// that the tuple codec consults. Keeping it a value type local to this package
// keeps the on-disk layer free of a catalog import — the btree package
// currently depends only on internal/storage, and S11.4 must not be the slice
// that introduces a cycle to the type layer.
type PGIndexAttr struct {
	// Len is attlen: > 0 fixed width, -1 varlena, -2 cstring.
	Len int16
	// ByVal is attbyval.
	ByVal bool
	// AlignBy is attalignby: 1, 2, 4 or 8 (the decoded form of attalign).
	AlignBy uint8
	// Storage is attstorage: 'p' plain, 'e' external, 'm' main, 'x' extended.
	Storage byte
}

// packable mirrors COMPACT_ATTR_IS_PACKABLE (attlen == -1 && attstorage !=
// TYPSTORAGE_PLAIN): only such attributes may be re-headered into the 1-byte
// short varlena form.
func (a PGIndexAttr) packable() bool { return a.Len == -1 && a.Storage != 'p' }

// PGAlignBy decodes an attalign char ('c'/'s'/'i'/'d') to its byte count.
func PGAlignBy(attalign byte) (uint8, error) {
	switch attalign {
	case 'c':
		return 1, nil
	case 's':
		return 2, nil
	case 'i':
		return 4, nil
	case 'd':
		return 8, nil
	default:
		return 0, fmt.Errorf("btree: invalid attalign value: %q", attalign)
	}
}

func alignTo(off int, by uint8) int {
	if by <= 1 {
		return off
	}
	n := int(by)
	return (off + n - 1) &^ (n - 1)
}

// ---------------------------------------------------------------------------
// varlena helpers
//
// A "value" handed to / returned by this codec is the datum's memory image as
// PG would see it through DatumGetPointer: for a by-value type, exactly attlen
// little-endian bytes; for a varlena, a complete varlena INCLUDING its header
// (1-byte short or 4-byte long form). The codec never invents or strips a
// header except for the documented long->short conversion that
// index_form_tuple itself performs.
// ---------------------------------------------------------------------------

const (
	varHdrSz       = 4
	varHdrSzShort  = 1
	varAttShortMax = 0x7F
)

// pgVarattIs1B is VARATT_IS_1B: the low bit of the first byte set means a
// 1-byte-header varlena (short, or the external/compressed TOAST pointer form
// when the second bit is set too).
func pgVarattIs1B(b []byte) bool { return len(b) > 0 && b[0]&0x01 == 0x01 }

// pgVarattIs1BE is VARATT_IS_1B_E — a TOAST pointer (0x01 header byte).
func pgVarattIs1BE(b []byte) bool { return len(b) > 0 && b[0] == 0x01 }

// pgVarSizeAny is VARSIZE_ANY: the total on-disk length of a varlena in any
// header form.
func pgVarSizeAny(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("btree: empty varlena datum")
	}
	if pgVarattIs1BE(b) {
		// External: 1 header byte + 1 tag byte + tag-specific payload; the
		// length is recorded in the second byte's tag via VARTAG_SIZE. goopg
		// never stores an external datum in an index (index_form_tuple
		// de-toasts first), so reject it rather than half-support it.
		return 0, fmt.Errorf("btree: external (TOAST-pointer) datum not storable in an index tuple")
	}
	if pgVarattIs1B(b) {
		return int(b[0] >> 1), nil // VARSIZE_SHORT
	}
	if len(b) < varHdrSz {
		return 0, fmt.Errorf("btree: truncated 4-byte varlena header (%d bytes)", len(b))
	}
	// VARSIZE: the length lives in bits 2..31 of the little-endian word.
	return int(binary.LittleEndian.Uint32(b[0:4]) >> 2), nil
}

// pgVarattIs4BU is VARATT_IS_4B_U — an uncompressed 4-byte-header varlena.
func pgVarattIs4BU(b []byte) bool { return len(b) >= 4 && b[0]&0x03 == 0x00 }

// pgCanMakeShort is VARATT_CAN_MAKE_SHORT.
func pgCanMakeShort(b []byte) bool {
	if !pgVarattIs4BU(b) {
		return false
	}
	sz, err := pgVarSizeAny(b)
	if err != nil {
		return false
	}
	return sz-varHdrSz+varHdrSzShort <= varAttShortMax
}

// ---------------------------------------------------------------------------
// header accessors
// ---------------------------------------------------------------------------

// PutPGItemPointer writes an ItemPointerData at dst[0:6].
//
// The block number is NOT a flat little-endian uint32: BlockIdData is two
// uint16 halves in struct order (bi_hi, bi_lo), so block 3 is {0,0,3,0} and
// not {3,0,0,0}. Getting this backwards makes PG read block 196608 — the trap
// already documented at internal/initdb/btree_index_bootstrap.go.
func PutPGItemPointer(dst []byte, tid storage.ItemPointer) {
	binary.LittleEndian.PutUint16(dst[0:2], uint16(uint32(tid.Block)>>16))
	binary.LittleEndian.PutUint16(dst[2:4], uint16(uint32(tid.Block)&0xFFFF))
	binary.LittleEndian.PutUint16(dst[4:6], tid.Offset)
}

// PGItemPointerAt reads the ItemPointerData at src[0:6].
func PGItemPointerAt(src []byte) storage.ItemPointer {
	hi := uint32(binary.LittleEndian.Uint16(src[0:2]))
	lo := uint32(binary.LittleEndian.Uint16(src[2:4]))
	return storage.ItemPointer{
		Block:  storage.BlockNumber(hi<<16 | lo),
		Offset: binary.LittleEndian.Uint16(src[4:6]),
	}
}

func pgTInfo(raw []byte) uint16 { return binary.LittleEndian.Uint16(raw[6:8]) }

// PGIndexTupleSize is IndexTupleSize(): the low 13 bits of t_info. This is the
// tuple's OWN idea of its length, which the caller should cross-check against
// the line pointer's length.
func PGIndexTupleSize(raw []byte) int { return int(pgTInfo(raw) & IndexSizeMask) }

// PGIndexTupleHasNulls is IndexTupleHasNulls().
func PGIndexTupleHasNulls(raw []byte) bool { return pgTInfo(raw)&IndexNullMask != 0 }

// PGIndexTupleHasVarwidths is IndexTupleHasVarwidths().
func PGIndexTupleHasVarwidths(raw []byte) bool { return pgTInfo(raw)&IndexVarMask != 0 }

// PGIndexTupleIsAltTID reports the nbtree INDEX_ALT_TID_MASK bit.
func PGIndexTupleIsAltTID(raw []byte) bool { return pgTInfo(raw)&IndexAltTIDMask != 0 }

// PGIndexTupleTID reads t_tid verbatim. For an alternative-TID tuple this is
// nbtree status data, not a heap pointer — check PGIndexTupleIsAltTID first.
func PGIndexTupleTID(raw []byte) storage.ItemPointer { return PGItemPointerAt(raw) }

// SetPGIndexTupleTID overwrites t_tid in place.
func SetPGIndexTupleTID(raw []byte, tid storage.ItemPointer) { PutPGItemPointer(raw, tid) }

// PGIndexInfoFindDataOffset is IndexInfoFindDataOffset(): where the attribute
// values begin, given t_info.
func PGIndexInfoFindDataOffset(tInfo uint16) int {
	if tInfo&IndexNullMask == 0 {
		return MaxAlign(SizeOfIndexTupleData)
	}
	return MaxAlign(SizeOfIndexTupleData + SizeOfIndexAttributeBitMapData)
}

// ---------------------------------------------------------------------------
// nbtree alternative-TID accessors (nbtree.h)
// ---------------------------------------------------------------------------

// BTreeTupleIsPivot is nbtree.h's BTreeTupleIsPivot(): ALT_TID set and the
// posting bit clear.
func BTreeTupleIsPivot(raw []byte) bool {
	if !PGIndexTupleIsAltTID(raw) {
		return false
	}
	return PGIndexTupleTID(raw).Offset&BTIsPosting == 0
}

// BTreeTupleIsPosting is nbtree.h's BTreeTupleIsPosting().
func BTreeTupleIsPosting(raw []byte) bool {
	if !PGIndexTupleIsAltTID(raw) {
		return false
	}
	return PGIndexTupleTID(raw).Offset&BTIsPosting != 0
}

// BTreeTupleSetNAtts stamps a pivot tuple's key-attribute count into t_tid's
// offset-number field and sets INDEX_ALT_TID_MASK, per nbtree.h. heapTID says
// the pivot keeps a trailing tiebreaker heap TID (the _bt_truncate special
// representation). The block half of t_tid is left alone — on an internal page
// it is the downlink, and on a leaf high key it is the "top parent" link.
func BTreeTupleSetNAtts(raw []byte, nkeyatts uint16, heapTID bool) error {
	if nkeyatts > IndexMaxKeys {
		return fmt.Errorf("btree: pivot natts %d exceeds INDEX_MAX_KEYS %d", nkeyatts, IndexMaxKeys)
	}
	if nkeyatts&BTStatusOffsetMask != 0 {
		return fmt.Errorf("btree: pivot natts %d collides with status bits", nkeyatts)
	}
	if heapTID && nkeyatts == 0 {
		return fmt.Errorf("btree: heap-TID pivot must have at least one key attribute")
	}
	off := nkeyatts
	if heapTID {
		off |= BTPivotHeapTIDAttr
	}
	// BT_IS_POSTING is deliberately left unset.
	binary.LittleEndian.PutUint16(raw[4:6], off)
	binary.LittleEndian.PutUint16(raw[6:8], pgTInfo(raw)|IndexAltTIDMask)
	return nil
}

// BTreeTupleGetNAtts is nbtree.h's BTreeTupleGetNAtts(): a pivot tuple carries
// its own key-attribute count; every other tuple implicitly has the index's
// full column count, which the caller supplies as indexNAtts.
func BTreeTupleGetNAtts(raw []byte, indexNAtts uint16) uint16 {
	if BTreeTupleIsPivot(raw) {
		return PGIndexTupleTID(raw).Offset & BTOffsetMask
	}
	return indexNAtts
}

// BTreeTupleGetDownLink / BTreeTupleSetDownLink are the internal-page child
// pointer, which upstream stores in t_tid's block half (nbtree.h).
func BTreeTupleGetDownLink(raw []byte) storage.BlockNumber {
	return PGIndexTupleTID(raw).Block
}

func BTreeTupleSetDownLink(raw []byte, child storage.BlockNumber) {
	binary.LittleEndian.PutUint16(raw[0:2], uint16(uint32(child)>>16))
	binary.LittleEndian.PutUint16(raw[2:4], uint16(uint32(child)&0xFFFF))
}

// BTreeTupleSetPosting is nbtree.h's BTreeTupleSetPosting(): it turns a plain
// index tuple into a posting-list tuple by setting INDEX_ALT_TID_MASK and
// overwriting t_tid with (postingOffset, nhtids|BT_IS_POSTING) — the byte
// offset of the trailing ItemPointerData array and its length.
//
// Upstream additionally asserts postingOffset == MAXALIGN(postingOffset).
// goopg cannot honour that yet: its key is still one opaque blob whose length
// is only recoverable as postingOffset - SizeOfIndexTupleData, so padding the
// offset would lose it. S11.4 slice 3 (per-attribute datums) makes the key
// length derivable from the index descriptor and restores the MAXALIGN — see
// the deferral ledger. The divergence is invisible to a non-assert PG build,
// which reaches the array by plain pointer arithmetic.
func BTreeTupleSetPosting(raw []byte, nhtids uint16, postingOffset int) error {
	if nhtids < 2 {
		return fmt.Errorf("btree: posting list needs at least 2 TIDs, got %d", nhtids)
	}
	if nhtids&BTStatusOffsetMask != 0 {
		return fmt.Errorf("btree: posting count %d exceeds BT_OFFSET_MASK %d", nhtids, BTOffsetMask)
	}
	if postingOffset < SizeOfIndexTupleData || postingOffset >= IndexSizeMask {
		return fmt.Errorf("btree: invalid posting offset %d", postingOffset)
	}
	binary.LittleEndian.PutUint16(raw[4:6], nhtids|BTIsPosting)
	BTreeTupleSetDownLink(raw, storage.BlockNumber(postingOffset))
	binary.LittleEndian.PutUint16(raw[6:8], pgTInfo(raw)|IndexAltTIDMask)
	return nil
}

// BTreeTupleGetNPosting is nbtree.h's BTreeTupleGetNPosting().
func BTreeTupleGetNPosting(raw []byte) uint16 {
	return PGIndexTupleTID(raw).Offset & BTOffsetMask
}

// BTreeTupleGetPostingOffset is nbtree.h's BTreeTupleGetPostingOffset(): the
// byte offset of the TID array, carried in t_tid's block half.
func BTreeTupleGetPostingOffset(raw []byte) int { return int(PGIndexTupleTID(raw).Block) }

// BTreeTupleGetHeapTID returns the tiebreaker heap TID of a pivot tuple that
// kept one, or ok=false when the pivot's TID was truncated away. For a plain
// (non-alternative-TID) tuple t_tid IS the heap TID.
func BTreeTupleGetHeapTID(raw []byte) (storage.ItemPointer, bool) {
	if BTreeTupleIsPivot(raw) {
		if PGIndexTupleTID(raw).Offset&BTPivotHeapTIDAttr == 0 {
			return storage.ItemPointer{}, false
		}
		size := PGIndexTupleSize(raw)
		if size < 6 || size > len(raw) {
			return storage.ItemPointer{}, false
		}
		return PGItemPointerAt(raw[size-6 : size]), true
	}
	if BTreeTupleIsPosting(raw) {
		// The first (lowest) heap TID of a posting list lives at the posting
		// offset, which t_tid's block half carries.
		off := int(PGIndexTupleTID(raw).Block)
		if off < 0 || off+6 > len(raw) {
			return storage.ItemPointer{}, false
		}
		return PGItemPointerAt(raw[off : off+6]), true
	}
	return PGIndexTupleTID(raw), true
}

// CheckPGBTItemSize is _bt_check_third_page's criterion
// (postgres/src/backend/access/nbtree/nbtutils.c): an item that exceeds the
// per-page third is rejected outright, because nbtree needs three items per
// page. needHeapTID selects the tighter bound that leaves room for a
// tiebreaker heap TID a later _bt_truncate may have to add.
func CheckPGBTItemSize(size int, needHeapTID bool) error {
	limit := BTMaxItemSizeNoHeapTid
	if needHeapTID {
		limit = BTMaxItemSize
	}
	if size > limit {
		return fmt.Errorf("btree: index row size %d exceeds btree version 4 maximum %d", size, limit)
	}
	return nil
}

// CheckPGBTThirdPage is `_bt_check_third_page` itself — the admission gate every
// nbtree writer runs before an item goes on a page (`_bt_findinsertloc` for the
// leaf insert path, `_bt_buildadd` for the bulk loader). It is the rule that
// replaced goopg's `MaxHighKeyLen` in M0130-S11.4 slice 3b-3d.
//
// The leaf/internal split is not cosmetic. A leaf tuple is charged the TIGHTER
// bound because `_bt_truncate` may have to append a tiebreaker heap TID to the
// separator derived from it (3b-3c), which makes the resulting PIVOT up to one
// MAXALIGN quantum larger than the leaf tuple it came from; the internal level
// is charged the looser bound precisely so it can accept that pivot. Upstream
// says the same by passing `needheaptidspace = isleaf` from the bulk loader and
// `heapkeyspace` (always true on v4) from the leaf insert path.
//
// The distinct internal-page message mirrors upstream's `elog(ERROR, "cannot
// insert oversized tuple ... on internal page")`: reaching it means an earlier
// leaf-level insertion that should have failed did not, i.e. a goopg bug rather
// than a user's over-wide index row.
func CheckPGBTThirdPage(leaf bool, size int) error {
	if err := CheckPGBTItemSize(size, leaf); err == nil {
		return nil
	}
	if !leaf {
		return fmt.Errorf("btree: cannot insert oversized tuple of size %d on an internal page", size)
	}
	return CheckPGBTItemSize(size, true)
}

// ---------------------------------------------------------------------------
// form / deform
// ---------------------------------------------------------------------------

// FormPGIndexTuple is index_form_tuple (postgres/src/backend/access/common/
// indextuple.c) restricted to what an index tuple may legally contain: values
// are already de-toasted, so the TOAST_INDEX_HACK branches (detoast_external_attr
// and the >TOAST_INDEX_TARGET in-line compression) are NOT performed here — see
// the deferral ledger row for M0130-S11.4.
//
// values[i] is the datum image (see the varlena note above) and is ignored when
// isnull[i]. tid becomes t_tid; callers building a pivot tuple overwrite it
// afterwards via BTreeTupleSetNAtts / BTreeTupleSetDownLink, exactly as
// upstream does.
func FormPGIndexTuple(attrs []PGIndexAttr, values [][]byte, isnull []bool, tid storage.ItemPointer) ([]byte, error) {
	if len(attrs) > IndexMaxKeys {
		return nil, fmt.Errorf("btree: number of index columns (%d) exceeds limit (%d)", len(attrs), IndexMaxKeys)
	}
	if len(values) != len(attrs) || len(isnull) != len(attrs) {
		return nil, fmt.Errorf("btree: FormPGIndexTuple arity mismatch: %d attrs, %d values, %d isnull",
			len(attrs), len(values), len(isnull))
	}

	hasNull := false
	for _, n := range isnull {
		if n {
			hasNull = true
			break
		}
	}

	infomask := uint16(0)
	if hasNull {
		infomask |= IndexNullMask
	}
	hoff := PGIndexInfoFindDataOffset(infomask)

	dataSize, err := pgComputeDataSize(attrs, values, isnull)
	if err != nil {
		return nil, err
	}
	size := MaxAlign(hoff + dataSize)
	if size&IndexSizeMask != size {
		return nil, fmt.Errorf("btree: index row requires %d bytes, maximum size is %d", size, IndexSizeMask)
	}

	out := make([]byte, size)
	PutPGItemPointer(out, tid)

	// heap_fill_tuple: the null bitmap (when present) sits immediately after
	// the header at offset sizeof(IndexTupleData), NOT at hoff — the MAXALIGN
	// padding between bitmap and data belongs to the data area.
	hasVarWidth, err := pgFillTuple(attrs, values, isnull, out, hoff, hasNull)
	if err != nil {
		return nil, err
	}
	if hasVarWidth {
		infomask |= IndexVarMask
	}
	binary.LittleEndian.PutUint16(out[6:8], infomask|uint16(size))
	return out, nil
}

// pgComputeDataSize is heap_compute_data_size restricted to non-expanded,
// non-external datums.
func pgComputeDataSize(attrs []PGIndexAttr, values [][]byte, isnull []bool) (int, error) {
	length := 0
	for i, att := range attrs {
		if isnull[i] {
			continue
		}
		val := values[i]
		switch {
		case att.packable() && pgCanMakeShort(val):
			// Anticipating the short-header conversion: no alignment at all,
			// and the length shrinks by the 3 bytes the header loses.
			sz, err := pgVarSizeAny(val)
			if err != nil {
				return 0, err
			}
			length += sz - varHdrSz + varHdrSzShort
		default:
			// att_datum_alignby: a 1-byte-header varlena is never aligned.
			if !(att.Len == -1 && pgVarattIs1B(val)) {
				length = alignTo(length, att.AlignBy)
			}
			add, err := pgAttLength(att, val)
			if err != nil {
				return 0, err
			}
			length += add
		}
	}
	return length, nil
}

// pgAttLength is att_addlength_datum.
func pgAttLength(att PGIndexAttr, val []byte) (int, error) {
	switch {
	case att.Len > 0:
		if len(val) < int(att.Len) {
			return 0, fmt.Errorf("btree: fixed-length datum of %d bytes supplied for attlen %d", len(val), att.Len)
		}
		return int(att.Len), nil
	case att.Len == -1:
		return pgVarSizeAny(val)
	case att.Len == -2:
		// cstring: strlen + 1.
		for i, b := range val {
			if b == 0 {
				return i + 1, nil
			}
		}
		return 0, fmt.Errorf("btree: cstring datum is not NUL-terminated")
	default:
		return 0, fmt.Errorf("btree: invalid attlen %d", att.Len)
	}
}

// pgFillTuple is heap_fill_tuple: it writes the null bitmap and the aligned
// attribute values into out, and reports whether any varying-width attribute
// was stored (upstream's HEAP_HASVARWIDTH, which index_form_tuple translates
// into INDEX_VAR_MASK).
func pgFillTuple(attrs []PGIndexAttr, values [][]byte, isnull []bool, out []byte, hoff int, hasNull bool) (bool, error) {
	// Bitmap bits run LSB-first within each byte, and a SET bit means the
	// value is PRESENT (fill_val ORs the mask in only on the non-null path).
	bitmapAt := SizeOfIndexTupleData
	off := hoff
	hasVarWidth := false

	for i, att := range attrs {
		if hasNull && !isnull[i] {
			out[bitmapAt+i/8] |= 1 << uint(i%8)
		}
		if isnull[i] {
			continue
		}
		val := values[i]
		switch {
		case att.ByVal:
			off = alignTo(off, att.AlignBy)
			if len(val) < int(att.Len) {
				return false, fmt.Errorf("btree: by-value datum of %d bytes supplied for attlen %d", len(val), att.Len)
			}
			copy(out[off:off+int(att.Len)], val[:att.Len])
			off += int(att.Len)
		case att.Len == -1:
			hasVarWidth = true
			switch {
			case pgVarattIs1BE(val):
				return false, fmt.Errorf("btree: external (TOAST-pointer) datum not storable in an index tuple")
			case pgVarattIs1B(val):
				// Already short: copied verbatim, no alignment.
				sz, err := pgVarSizeAny(val)
				if err != nil {
					return false, err
				}
				copy(out[off:off+sz], val[:sz])
				off += sz
			case att.packable() && pgCanMakeShort(val):
				// Re-header into the short form: no alignment, 1-byte header
				// holding (length << 1) | 1.
				sz, err := pgVarSizeAny(val)
				if err != nil {
					return false, err
				}
				short := sz - varHdrSz + varHdrSzShort
				out[off] = byte(short<<1) | 0x01
				copy(out[off+1:off+short], val[varHdrSz:sz])
				off += short
			default:
				off = alignTo(off, att.AlignBy)
				sz, err := pgVarSizeAny(val)
				if err != nil {
					return false, err
				}
				copy(out[off:off+sz], val[:sz])
				off += sz
			}
		case att.Len == -2:
			hasVarWidth = true
			sz, err := pgAttLength(att, val)
			if err != nil {
				return false, err
			}
			copy(out[off:off+sz], val[:sz])
			off += sz
		default:
			// Fixed-length pass-by-reference.
			off = alignTo(off, att.AlignBy)
			if len(val) < int(att.Len) {
				return false, fmt.Errorf("btree: datum of %d bytes supplied for attlen %d", len(val), att.Len)
			}
			copy(out[off:off+int(att.Len)], val[:att.Len])
			off += int(att.Len)
		}
		if off > len(out) {
			return false, fmt.Errorf("btree: index tuple fill overran its %d-byte allocation", len(out))
		}
	}
	return hasVarWidth, nil
}

// DeformPGIndexTuple is index_deform_tuple: it walks the attribute area and
// returns each datum's bytes (aliasing raw — callers that retain them must
// copy) together with the null flags.
//
// natts is how many attributes to extract. For a pivot tuple that is
// BTreeTupleGetNAtts, not the index's column count: a truncated pivot
// physically stores fewer attributes than the index has columns, and reading
// past them walks into the tiebreaker TID or the MAXALIGN pad.
func DeformPGIndexTuple(raw []byte, attrs []PGIndexAttr, natts int) ([][]byte, []bool, error) {
	if natts > len(attrs) {
		return nil, nil, fmt.Errorf("btree: natts %d exceeds %d described attributes", natts, len(attrs))
	}
	if len(raw) < SizeOfIndexTupleData {
		return nil, nil, fmt.Errorf("btree: index tuple too short (%d bytes)", len(raw))
	}
	tInfo := pgTInfo(raw)
	size := int(tInfo & IndexSizeMask)
	if size > len(raw) {
		return nil, nil, fmt.Errorf("btree: t_info size %d exceeds the %d supplied bytes", size, len(raw))
	}
	hasNulls := tInfo&IndexNullMask != 0
	off := PGIndexInfoFindDataOffset(tInfo)

	values := make([][]byte, natts)
	isnull := make([]bool, natts)
	for i := range natts {
		att := attrs[i]
		if hasNulls {
			// A CLEAR bit means null (fill_val sets the bit for present values).
			if raw[SizeOfIndexTupleData+i/8]&(1<<uint(i%8)) == 0 {
				isnull[i] = true
				continue
			}
		}
		if !(att.Len == -1 && off < size && pgVarattIs1B(raw[off:])) {
			off = alignTo(off, att.AlignBy)
		}
		if off >= size {
			return nil, nil, fmt.Errorf("btree: attribute %d starts at %d, past the %d-byte tuple", i, off, size)
		}
		n, err := pgAttLength(att, raw[off:size])
		if err != nil {
			return nil, nil, fmt.Errorf("btree: attribute %d: %w", i, err)
		}
		if off+n > size {
			return nil, nil, fmt.Errorf("btree: attribute %d of %d bytes overruns the %d-byte tuple", i, n, size)
		}
		values[i] = raw[off : off+n]
		off += n
	}
	return values, isnull, nil
}
