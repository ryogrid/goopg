package nbtree

import (
	"bytes"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-1 — the key-comparison layer, additive.
//
// Slices 1..3a made goopg's on-page tuples byte-identical to upstream's
// IndexTupleData, but the KEY inside them is still one opaque
// order-preserving blob compared with `CompareKeys` (bytes.Compare). Every
// remaining S11.4 deferral traces back to that single fact: with an opaque
// blob the key's length is recoverable only as `size - SizeOfIndexTupleData`,
// so nothing INSIDE the tuple may be padded (index_form_tuple's size rounding
// and the posting offset — 3b-3a restored the third MAXALIGN, the PAGE
// PLACEMENT one, precisely because it pads OUTSIDE the tuple and leaves lp_len
// exact), and nothing may be truncated attribute-wise (`_bt_keep_natts`, hence
// pivot natts stuck at 1).
//
// The bridge out is upstream's own: nbtree never knows a key's type. It
// compares through an opclass support function (BTORDER_PROC) that the
// *caller* — really the catalog — installs into a BTScanInsert's ScanKeys
// (`_bt_mkscankey`). This file is goopg's transcription of that seam:
//
//   - PGKeyAttr / PGIndexKeyDesc are the descriptor: per key attribute, the
//     physical layout the codec needs (PGIndexAttr) plus the ordering
//     properties `_bt_compare` consults — the comparator itself, SK_BT_DESC
//     and SK_BT_NULLS_FIRST.
//   - ComparePGIndexTuples is `_bt_compare`'s body for the tuple-vs-tuple
//     case: attribute-wise comparison with upstream's NULL rules, upstream's
//     minus-infinity treatment of truncated attributes, and the heap-TID
//     tiebreak that makes the ordering total in heapkeyspace.
//
// It stays additive: no writer, descent path or split builds a descriptor
// yet, so no on-disk format changes here. Threading a descriptor from the
// catalog into BTree.Options and retiring `CompareKeys` is 3b-2; the
// remaining MAXALIGNs and real suffix truncation are 3b-3. See
// docs/design/0130-0011-nbtree-pg-on-disk-format.md.
//
// Deliberately NOT modelled (goopg has no user of them at this layer):
// collations (sk_collation — goopg's collation handling lives above the AM),
// cross-type comparisons (the scankey's sk_argument may be another type;
// tuple-vs-tuple comparison is always same-type), and INCLUDE columns
// (nkeyatts < natts — the descriptor covers key attributes only, which is
// exactly what `_bt_compare` iterates).
// ---------------------------------------------------------------------------

// PGAttrComparator compares two datums of ONE index attribute, each in the
// binary image DeformPGIndexTuple hands back (attlen bytes for a by-value
// type, a complete varlena INCLUDING its header otherwise). It returns the
// usual <0 / 0 / >0.
//
// This is upstream's BTORDER_PROC support function, with one convention
// difference worth stating because it is the classic sign bug: upstream calls
// `sk_func(index_datum, sk_argument)` and then INVERTS the result for an ASC
// column, because a ScanKey is conceptually "the scankey compared to the
// item" while the function was handed the item first. There is no scankey
// here — both sides are index datums — so a PGAttrComparator is a plain
// left-vs-right comparator with no inversion, and DESC is applied once, by
// ComparePGIndexTuples.
type PGAttrComparator func(a, b []byte) int

// PGKeyAttr describes one key attribute of an index: how the tuple codec
// lays it out, and how the tree orders it.
type PGKeyAttr struct {
	// PGIndexAttr is the physical layout FormPGIndexTuple /
	// DeformPGIndexTuple consult.
	PGIndexAttr
	// Compare is the attribute's opclass comparator. A nil Compare means
	// "bytewise", i.e. CompareKeys — the honest description of goopg's
	// current order-preserving key encodings (EncodeInt4, EncodeNumericKey,
	// EncodeVarchar, …), all of which are built so that lexicographic byte
	// order IS the type's order. Keeping nil meaningful is what lets 3b-2
	// migrate one type at a time instead of in a flag day.
	Compare PGAttrComparator
	// Desc is SK_BT_DESC: the column is indexed in descending order.
	Desc bool
	// NullsFirst is SK_BT_NULLS_FIRST. PG's default for an ASC column is
	// NULLS LAST (this field false); for DESC the catalog records NULLS
	// FIRST, so a caller building a descriptor from pg_index.indoption must
	// carry both bits across rather than deriving one from the other.
	NullsFirst bool
}

// PGIndexKeyDesc is the per-index key descriptor: the first nkeyatts
// attributes, in index order. It is the goopg analogue of the (TupleDesc,
// BTScanInsert.scankeys) pair, minus everything that belongs to a scan
// rather than to the index.
type PGIndexKeyDesc struct {
	// Attrs are the KEY attributes only. An index with INCLUDE columns
	// describes just its key prefix here, matching
	// IndexRelationGetNumberOfKeyAttributes.
	Attrs []PGKeyAttr
}

// NKeyAtts is IndexRelationGetNumberOfKeyAttributes.
func (d *PGIndexKeyDesc) NKeyAtts() int {
	if d == nil {
		return 0
	}
	return len(d.Attrs)
}

// Physical projects the descriptor down to the layout-only view the tuple
// codec takes, so callers never hand-maintain two parallel slices.
func (d *PGIndexKeyDesc) Physical() []PGIndexAttr {
	if d == nil {
		return nil
	}
	out := make([]PGIndexAttr, len(d.Attrs))
	for i, a := range d.Attrs {
		out[i] = a.PGIndexAttr
	}
	return out
}

// compareAttr applies one attribute's ordering rules to a pair of (datum,
// isnull) — upstream `_bt_compare`'s loop body
// (postgres/src/backend/access/nbtree/nbtsearch.c:737).
func (a PGKeyAttr) compareAttr(av []byte, anull bool, bv []byte, bnull bool) int {
	// NULL ordering first: a NULL never reaches the opclass comparator.
	// Upstream writes these four cases against the scankey's SK_ISNULL; for
	// two index datums they collapse to "a NULL sorts to the end, or to the
	// front under NULLS FIRST".
	if anull || bnull {
		switch {
		case anull && bnull:
			return 0 // NULL "=" NULL
		case a.NullsFirst == anull:
			// Either a is the NULL and NULLs come first, or b is the NULL
			// and NULLs come last: a sorts before b.
			return -1
		default:
			return 1
		}
	}
	var cmp PGAttrComparator = CompareKeys
	if a.Compare != nil {
		cmp = a.Compare
	}
	res := cmp(av, bv)
	if a.Desc {
		// SK_BT_DESC. Upstream expresses this as "don't invert" against its
		// flipped-argument convention; with a plain comparator it is a
		// straight negation. Negating rather than swapping the arguments
		// keeps a non-antisymmetric comparator from changing the answer
		// depending on which slice happened to be on the left.
		res = -res
	}
	return res
}

// ComparePGIndexTuples is `_bt_compare` reduced to its tuple-vs-tuple form:
// it orders two on-page nbtree tuples of the SAME index under desc.
//
// It reproduces three upstream rules that a naive attribute loop misses:
//
//   - Truncated attributes are MINUS INFINITY, not "absent". A pivot that
//     kept k of nkeyatts attributes compares equal on those k and then sorts
//     BEFORE any tuple that has more (upstream returns 1 for
//     `key->keysz > ntupatts`, i.e. the truncated side is the smaller one).
//   - The heap TID is the last key attribute (heapkeyspace), so two tuples
//     equal on every key attribute still have a deterministic order. A pivot
//     whose tiebreaker TID was truncated away is minus infinity there too.
//   - NULL ordering is per-attribute (NULLS FIRST/LAST), never global.
//
// It deliberately does NOT reproduce `_bt_compare`'s first line — the "force
// > for the first data item on an internal page" minus-infinity rule. That
// one is a property of a PAGE POSITION (P_FIRSTDATAKEY), not of a tuple, so
// it belongs to the descent code that will call this in 3b-2; here the
// minus-infinity downlink is simply a zero-attribute pivot, which the
// truncation rule already sorts first.
func ComparePGIndexTuples(desc *PGIndexKeyDesc, a, b []byte) (int, error) {
	return comparePGIndexTuples(desc, a, b, true)
}

// ComparePGIndexTupleKeyAttrs is ComparePGIndexTuples WITHOUT the heap-TID
// tiebreak: it returns 0 for two tuples that agree on every key attribute, even
// though they name different heap rows and are therefore distinct entries of a
// heapkeyspace tree.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-v. This is the question a UNIQUE index build
// asks, and it is a different question from ordering. Upstream splits them
// inside one comparator: `comparetup_index_btree`
// (postgres/src/backend/utils/sort/tuplesortvariants.c:1668, PG 18.3) walks the
// key attributes, raises 23505 "could not create unique index" the moment they
// all compare equal, and only THEN falls through to the ItemPointer tiebreak
// "required for btree indexes, since heap TID is treated as an implicit last
// key attribute". If the duplicate test used the full ordering, no two entries
// of a heapkeyspace index would ever compare equal — every duplicate key would
// be admitted, because the TIDs differ by construction.
//
// Under the blob format the distinction cannot arise (a blob key carries no
// TID, so `CompareKeys` already answers both questions); this exists for the
// tuple format, where the two answers diverge for exactly the rows a unique
// build must reject.
func ComparePGIndexTupleKeyAttrs(desc *PGIndexKeyDesc, a, b []byte) (int, error) {
	return comparePGIndexTuples(desc, a, b, false)
}

func comparePGIndexTuples(desc *PGIndexKeyDesc, a, b []byte, withHeapTID bool) (int, error) {
	nkey := desc.NKeyAtts()
	if nkey == 0 {
		return 0, fmt.Errorf("btree: ComparePGIndexTuples needs a key descriptor with at least one attribute")
	}
	if len(a) < SizeOfIndexTupleData || len(b) < SizeOfIndexTupleData {
		// An operand too short to hold an IndexTupleData header is not a tuple
		// at all. The case that actually occurs is a MINUS-INFINITY search key:
		// `rangeScanPos(nil, …)` and the leftmost descent pass an empty key,
		// which upstream expresses as a BTScanInsert with keysz = 0 rather than
		// as a tuple. Returning an error (rather than reading past the slice,
		// which panicked before M0130-S11.4 slice 3b-2c-ii-B1) lets
		// indexFormat.compare fall back to the bytewise order, where an empty
		// key sorts before every tuple — which IS minus infinity, so the
		// descent lands on the leftmost leaf exactly as intended.
		return 0, fmt.Errorf("btree: ComparePGIndexTuples operand of %d/%d bytes is shorter than an index tuple header", len(a), len(b))
	}
	if BTreeTupleIsPosting(a) || BTreeTupleIsPosting(b) {
		// A posting list's key is a single value shared by many heap TIDs;
		// comparing one is meaningful, but the heap-TID tiebreak below would
		// have to pick which of its TIDs to use, and every caller that has a
		// posting tuple in hand knows which. Refuse rather than guess.
		return 0, fmt.Errorf("btree: ComparePGIndexTuples does not accept posting-list tuples")
	}

	nattsA := int(BTreeTupleGetNAtts(a, uint16(nkey)))
	nattsB := int(BTreeTupleGetNAtts(b, uint16(nkey)))
	if nattsA > nkey || nattsB > nkey {
		return 0, fmt.Errorf("btree: tuple has %d/%d attributes, more than the descriptor's %d", nattsA, nattsB, nkey)
	}
	ncmp := min(nattsA, nattsB)

	phys := desc.Physical()
	if ncmp > 0 {
		av, anull, err := DeformPGIndexTuple(a, phys, ncmp)
		if err != nil {
			return 0, fmt.Errorf("btree: left tuple: %w", err)
		}
		bv, bnull, err := DeformPGIndexTuple(b, phys, ncmp)
		if err != nil {
			return 0, fmt.Errorf("btree: right tuple: %w", err)
		}
		for i := range ncmp {
			if res := desc.Attrs[i].compareAttr(av[i], anull[i], bv[i], bnull[i]); res != 0 {
				return res, nil
			}
		}
	}

	// Equal on every attribute both tuples physically store: the one with
	// fewer attributes has minus infinity where the other has a value.
	if nattsA != nattsB {
		if nattsA < nattsB {
			return -1, nil
		}
		return 1, nil
	}

	if !withHeapTID {
		// Equal on every key attribute. The caller asked the uniqueness
		// question, not the ordering one — see ComparePGIndexTupleKeyAttrs.
		return 0, nil
	}

	// Heap TID, the implicit final key attribute. Absent (truncated) is
	// minus infinity, matching upstream's scantid == NULL branch.
	tidA, okA := BTreeTupleGetHeapTID(a)
	tidB, okB := BTreeTupleGetHeapTID(b)
	switch {
	case !okA && !okB:
		return 0, nil
	case !okA:
		return -1, nil
	case !okB:
		return 1, nil
	}
	return compareItemPointers(tidA, tidB), nil
}

// compareItemPointers is ItemPointerCompare
// (postgres/src/backend/storage/page/itemptr.c): block number first, then
// offset, both unsigned.
func compareItemPointers(a, b storage.ItemPointer) int {
	switch {
	case a.Block < b.Block:
		return -1
	case a.Block > b.Block:
		return 1
	case a.Offset < b.Offset:
		return -1
	case a.Offset > b.Offset:
		return 1
	}
	return 0
}

// PGCompareBytewise is the explicit spelling of a nil PGKeyAttr.Compare: the
// bytewise comparator goopg's order-preserving key encodings are built for.
// Exported so a descriptor can say "bytewise on purpose" instead of relying
// on a zero value being read correctly.
func PGCompareBytewise(a, b []byte) int { return bytes.Compare(a, b) }
