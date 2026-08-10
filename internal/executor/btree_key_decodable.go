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

import "github.com/goopg/goopg/internal/catalog"

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
		if arrayKeyElemRenderer(name) == nil {
			return false
		}
		return indexKeyColumnIsDecodable(catalog.Column{Name: col.Name, Type: catalog.Type{Name: name}})
	}
	// interval: refused by construction — see intervalKeyNotDecodable.
	return !isIntervalTypeName(name)
}
