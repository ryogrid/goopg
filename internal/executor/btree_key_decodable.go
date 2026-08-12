package executor

// btree_key_decodable.go — "can this index key column be inverted?", asked
// BEFORE a scan reads a key rather than discovered while decoding one
// (M0119-0006, design docs/design/0119-0006-array-index-key-decodability.md).
//
// goopg's blob index key is one order-preserving byte run per key column, and a
// few of those encodings are deliberately NOT invertible: `interval`'s key is
// upstream's `interval_cmp_value` span, which has already thrown away the
// month/day/micros split, and an ARRAY key's element is only recoverable when
// goopg can spell that element exactly as the heap-side array decode spells it
// (arrayKeyElemRenderer). The index-only scan needs that answer up front,
// because its ALL_VISIBLE fast path answers the query FROM the key: a refusal
// discovered mid-decode is not a slower plan, it is `XX000` for the whole query.
//
// The interval predicate had that guard; arrays did not, and the miss was
// explicitly array-shaped — `indexKeyIsDecodable` tested
// `!col.Type.IsArray && isIntervalTypeName(...)`, i.e. it declined an `interval`
// column and PASSED an `interval[]` one, whose element key is the same
// non-invertible span. Confirmed at HEAD: with an index on an `interval[]`
// column, `SELECT i FROM av WHERE i = '{3 days}'` failed with
//
//	XX000: IOS decode: btree: interval key is the comparison span
//	       (interval_cmp_value) and cannot be decoded back to month/day/time
//
// — a plain SELECT over an indexed column, no corruption and no exotic plan
// needed. Declining sends the scan to the heap, which is what the interval
// scalar case has done since the 17th slice.
//
// This file is the single place that answers the question; it is kept honest
// against the decoders themselves by TestIndexKeyDecodableMatchesDecoder, which
// encodes a value of every indexable type (scalar and array), decodes it, and
// asserts the predicate agreed with what actually happened.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgarray"
)

// indexKeyColumnIsDecodable reports whether decodeBTreeKeyToDatum /
// decodeIndexKeyColumn can invert the blob key bytes this column contributes.
//
// It is a property of the TYPE, not of any particular key: the refusals below
// are structural (information the encoding never carried, or a rendering goopg
// cannot make agree with the heap), never data-dependent.
func indexKeyColumnIsDecodable(col catalog.Column) bool {
	name := col.Type.Name
	if col.Type.IsArray {
		// Type.Name is the ELEMENT type name for an array column, so this is
		// the recursion decodeArrayBTreeKey performs: an array key is
		// invertible exactly when its elements are, both as VALUES (the
		// element's own scalar key) and as TEXT (arrayKeyElemRenderer).
		if arrayKeyElemRenderer(name, pgarray.DefaultOutputStyle()) == nil {
			return false
		}
		return indexKeyColumnIsDecodable(catalog.Column{Name: col.Name, Type: catalog.Type{Name: name}})
	}
	// interval: refused by construction — see intervalKeyNotDecodable.
	return !isIntervalTypeName(name)
}

// indexKeyColumnRendersHeapText is the SECOND question an index-only scan has to
// ask, and until the 34th slice goopg asked only the first one. Decodability is
// about the VALUE: can the key bytes be inverted to a Datum at all (the amcheck
// comparator's question, and the one indexKeyColumnIsDecodable answers).
// Renderability is about the SPELLING: does that Datum print the way the same row
// prints when it is read from the HEAP. A scan that answers a query from the key
// substitutes one for the other, so it needs both — and they are not the same
// question for `numeric`.
//
// PG's numeric carries its DISPLAY SCALE as part of the value: `1.50` and `1.5`
// are the same number spelled two ways, and `numeric_out` prints the spelling
// that was stored. goopg's blob key deliberately throws that away —
// EncodeNumericKey strips trailing mantissa zeros so that numerically equal
// values encode to IDENTICAL bytes, which is what makes a UNIQUE index on
// numeric reject `1.00` after `1.0` (the probe: both encode to
// [02 80 00 00 00 '1' 00]). That is not a defect to be repaired in the encoder:
// byte-identical keys cannot also distinguish two spellings of one number, so
// carrying the display scale in the key and keeping unique-equality are mutually
// exclusive. The information is simply not in the key, and the heap is where it
// still lives.
//
// So the refusal belongs HERE, not in the decoder — moving it into the decoder
// (the containment the 2026-08-12 ledger row weighed) would take
// `bt_index_check` down with it on every numeric index, for a loss that buys
// nothing: a value-correct decode is all a COMPARATOR ever needed.
//
// Strictly narrower than indexKeyColumnIsDecodable by construction, so the two
// can never disagree about a type the decoder refuses outright; asserted by
// TestIndexKeyRenderableIsNarrowerThanDecodable.
func indexKeyColumnRendersHeapText(col catalog.Column) bool {
	if !indexKeyColumnIsDecodable(col) {
		return false
	}
	// Type.Name is the ELEMENT type name for an array column, so this one test
	// covers `numeric` and `numeric[]` alike — the element's key is the same
	// zero-stripped digit run, and array_out prints elements with numeric_out.
	return !isNumericType(col.Type.Name)
}
