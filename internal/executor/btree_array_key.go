package executor

// btree_array_key.go — B-tree key encoding for ARRAY-typed key columns
// (M0119-0006, design docs/design/0119-0006-array-index-key-encoding.md).
//
// A goopg array column carries catalog.Type{Name:<ELEMENT type>, IsArray:true}
// (see codec_array.go), so every `isInt4Type(col.Type.Name)`-style predicate in
// encodeBTreeKeyForColumn answers for the ELEMENT and silently claims the array.
// An `int4[]` column therefore used to reach the scalar int4 arm with the array
// TEXT ("{1,2}") as its datum, which produced two distinct defects:
//
//   - CREATE INDEX over rows failed with a bogus `22P02 invalid input syntax
//     for type integer: "{1,2}"` (the unknown-literal coercion of the array
//     text into an int4), and
//   - CREATE INDEX on an EMPTY table succeeded, after which every INSERT wrote
//     NO index entry at all — maintainUniqueIndexesForInsert swallows key-encode
//     errors by design (operators_storage.go) — leaving a permanently empty
//     index that an index scan reads as "no rows" and a UNIQUE array index that
//     enforces nothing.
//
// The order this file reproduces is upstream's array_ops, i.e. `array_cmp`
// (src/backend/utils/adt/arrayfuncs.c): compare element by element under the
// element type's comparator; two NULLs are equal and NULL sorts AFTER
// not-NULL; if one array is a prefix of the other, the shorter one is smaller;
// remaining ties are broken by dimensionality, which goopg cannot reach (below).
//
// Encoding: each element contributes a 1-byte presence tag followed by the
// element's own scalar B-tree key bytes (nothing for a NULL element), and the
// whole array is closed by an end marker:
//
//	non-NULL element:  0x01 ++ encodeBTreeKeyForColumn(element)
//	NULL element:      0xFF
//	end of array:      0x00
//
// Why that is exactly array_cmp's order:
//
//   - Element-wise: all non-NULL tags are equal, so the comparison of two
//     arrays falls through to the first differing element's key bytes, which
//     are the element type's own order-preserving encoding (the same bytes the
//     scalar column path writes).
//   - NULL > not-NULL: 0xFF is above the non-NULL tag, and the tag is the first
//     byte of the element's segment.
//   - Shorter-is-smaller: where the longer array continues with an element tag
//     (0x01 or 0xFF) the shorter one has already emitted its end marker 0x00,
//     which is below both — so a prefix sorts first, which is what
//     `nitems1 < nitems2 ⇒ -1` says.
//
// Neither marker byte is optional. Without the per-element tag a NULL marker
// would be indistinguishable from an element whose encoding begins with the
// same byte (EncodeInt4(maxint32) is 0xFFFFFFFF). Without the end marker the
// array segment would not be self-delimiting, which breaks it as one column of
// a COMPOSITE key: `(a int4[], b int4)` with a='{1}', b=2 would encode to the
// same byte run as a='{1,2}' continuing into b, and the shorter array would
// then sort by b's leading byte instead of by array length. It also makes the
// empty array `{}` a one-byte key rather than a zero-byte one — a zero-length
// key is read as "no key" by encodeIndexKeyFromCols (nil slice), which silently
// dropped the row from the index entirely.
//
// Out of scope, declined explicitly rather than mis-encoded (deferral ledger
// 2026-08-10): multidimensional arrays and non-default lower bounds — goopg's
// array codec only ever writes ndim=1/lbound=1 (encodeArrayValuePG), so
// array_cmp's dimension tie-breaks are unreachable, and a multi-dim literal is
// rejected here instead of being flattened into a wrong key. Element types
// with no scalar key encoding (bool, enum, nested arrays) surface the scalar
// arm's own 0A000.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

const (
	arrayKeyEnd         byte = 0x00 // end of the array segment: below every element tag
	arrayKeyElemPresent byte = 0x01 // tag preceding a non-NULL element's key bytes
	arrayKeyElemNull    byte = 0xFF // a NULL element: sorts after every non-NULL (array_cmp)
)

// encodeArrayBTreeKey builds the stored/probe B-tree key for one array-typed
// key column. v is the array's runtime value, which is its canonical text form
// ("{1,2}") as a KindString datum — the representation codec_array.go's
// encodeArrayValuePG consumes on the heap side.
//
// col is the ARRAY column: col.Type.Name is the element type name, which is
// what the per-element scalar encoding is resolved against.
func encodeArrayBTreeKey(v Datum, col *catalog.Column, pos int) ([]byte, *ExecError) {
	if v.Kind != KindString {
		// A pre-built ArrayType blob (KindBytes, the catalog-seeder form) carries
		// no text to re-parse; nothing indexes those, and guessing would risk an
		// encoding that disagrees with the text path's.
		return nil, &ExecError{Code: "42804", Pos: pos,
			Message: fmt.Sprintf("column %q is not an array literal at runtime (kind %d)", col.Name, v.Kind)}
	}
	s := strings.TrimSpace(v.StringValue())
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed array literal: %q", v.StringValue())}
	}
	if strings.ContainsRune(s[1:len(s)-1], '{') {
		return nil, &ExecError{Code: "0A000", Pos: pos,
			Message: fmt.Sprintf("btree v0 cannot index multidimensional array column %q", col.Name)}
	}

	// The element column: same type name, minus the array-ness, so the scalar
	// dispatch in encodeBTreeKeyForColumn resolves the ELEMENT type. Reusing
	// that function (rather than a private element encoder) is what keeps an
	// array key byte-identical to the scalar keys of its elements — the
	// sibling-path rule that the float and enum expression-key slices were
	// written to enforce.
	elemCol := catalog.Column{Name: col.Name, Type: catalog.Type{Name: col.Type.Name}}

	out := make([]byte, 0, 8)
	for _, e := range parseTextArray(s) {
		// An unquoted NULL token is PG's array NULL (case-insensitive,
		// array_in). goopg's heap codec rejects NULL elements outright
		// (encodeArrayValuePG), so this arm is reachable only from a probe key
		// built out of a query literal; encoding it correctly is still required
		// so the probe lands where array_cmp says it belongs.
		if strings.EqualFold(strings.TrimSpace(e), "NULL") {
			out = append(out, arrayKeyElemNull)
			continue
		}
		eb, encErr := encodeBTreeKeyForColumn(NewStringDatum(e), &elemCol, pos)
		if encErr != nil {
			return nil, encErr
		}
		out = append(out, arrayKeyElemPresent)
		out = append(out, eb...)
	}
	return append(out, arrayKeyEnd), nil
}
