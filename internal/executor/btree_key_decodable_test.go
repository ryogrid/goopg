package executor

// Gates for btree_key_decodable.go (M0119-0006, design
// docs/design/0119-0006-array-index-key-decodability.md).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// indexKeyTypeCases lists one indexable value per type goopg can write a blob
// B-tree key for. Both gates below run every case twice — as the scalar column
// and as the ARRAY column of the same element type — because the array key is
// built by recursing into the scalar encoder, so the two must stay one story.
func indexKeyTypeCases() []struct{ typ, lit string } {
	return []struct{ typ, lit string }{
		{"int2", "3"}, {"int4", "3"}, {"int8", "3"}, {"oid", "3"},
		// reg* family: default oid_ops, so a numeric OID literal routes through
		// the reg* arm (isRegType) and the nil-ctx numeric passthrough; a NAME
		// literal is exercised by the VM-backed IOS tests instead, which can
		// resolve it. M0119-0006-0006.
		{"regproc", "3"}, {"regprocedure", "3"}, {"regclass", "3"},
		{"regtype", "3"}, {"regrole", "3"}, {"regcollation", "3"},
		{"bool", "true"}, {"float4", "1.5"}, {"float8", "1.5"},
		{"numeric", "1.50"}, {"text", "ab"}, {"varchar", "ab"},
		{"bpchar", "ab"}, {"name", "ab"},
		{"uuid", "00000000-0000-0000-0000-000000000001"},
		{"bytea", `\x0102`}, {"date", "2020-01-02"}, {"time", "01:02:03"},
		{"timetz", "01:02:03+01"}, {"timestamp", "2020-01-02 03:04:05"},
		{"timestamptz", "2020-01-02 03:04:05+00"}, {"interval", "3 days"},
	}
}

// TestIndexKeyDecodableMatchesDecoder pins the contract the index-only scan now
// relies on: indexKeyColumnIsDecodable answers, ahead of any scan, exactly what
// the decoders do when handed a real key. A predicate that says "decodable" for
// a key the decoder refuses is an XX000 for the whole query (the interval[]
// defect this slice fixed); one that says "not decodable" for a key that decodes
// fine only costs heap fetches, but silently — so both directions are asserted.
func TestIndexKeyDecodableMatchesDecoder(t *testing.T) {
	refusedScalars, refusedArrays := 0, 0
	for _, c := range indexKeyTypeCases() {
		for _, isArray := range []bool{false, true} {
			col := catalog.Column{Name: "c", Type: catalog.Type{Name: c.typ, IsArray: isArray}}
			lit := c.lit
			if isArray {
				lit = "{" + c.lit + "}"
			}
			key, encErr := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), &col, 0)
			if encErr != nil {
				t.Fatalf("%s(array=%v): encode %q: %s", c.typ, isArray, lit, encErr.Message)
			}
			want := indexKeyColumnIsDecodable(col)
			if !want {
				if isArray {
					refusedArrays++
				} else {
					refusedScalars++
				}
			}
			// Sibling 1: the single-column index-only scan lane.
			if _, err := decodeBTreeKeyToDatum(key, col); (err == nil) != want {
				t.Errorf("%s(array=%v): decodeBTreeKeyToDatum err=%v, predicate says decodable=%v",
					c.typ, isArray, err, want)
			}
			// Sibling 2: the composite-walk lane (also the amcheck comparator's).
			if _, _, err := decodeIndexKeyColumn(key, col); (err == nil) != want {
				t.Errorf("%s(array=%v): decodeIndexKeyColumn err=%v, predicate says decodable=%v",
					c.typ, isArray, err, want)
			}
		}
	}
	// Non-vacuity: the table must exercise both answers, and specifically must
	// contain array types that are refused while their scalar is not (the whole
	// point of the array recursion).
	if refusedScalars == 0 || refusedArrays <= refusedScalars {
		t.Fatalf("degenerate case table: refused scalars=%d arrays=%d", refusedScalars, refusedArrays)
	}
}

// TestIndexKeyDecodeSiblingsAgree is Hard-won Rule #2 on the two blob decoders:
// wherever the key is decodable at all, the single-column lane and the composite
// lane must produce the SAME Datum. They drifted for `uuid` — decodeIndexKeyColumn
// listed it with the text-likes while decodeBTreeKeyToDatum let it fall to the
// `default:` arm, which reads any 8 leading bytes as an enum sort order and never
// errors, so the single-column lane answered an empty enum Datum for a real uuid.
func TestIndexKeyDecodeSiblingsAgree(t *testing.T) {
	for _, c := range indexKeyTypeCases() {
		for _, isArray := range []bool{false, true} {
			col := catalog.Column{Name: "c", Type: catalog.Type{Name: c.typ, IsArray: isArray}}
			if !indexKeyColumnIsDecodable(col) {
				continue
			}
			lit := c.lit
			if isArray {
				lit = "{" + c.lit + "}"
			}
			key, encErr := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), &col, 0)
			if encErr != nil {
				t.Fatalf("%s(array=%v): encode %q: %s", c.typ, isArray, lit, encErr.Message)
			}
			one, err := decodeBTreeKeyToDatum(key, col)
			if err != nil {
				t.Fatalf("%s(array=%v): decodeBTreeKeyToDatum: %v", c.typ, isArray, err)
			}
			many, n, err := decodeIndexKeyColumn(key, col)
			if err != nil {
				t.Fatalf("%s(array=%v): decodeIndexKeyColumn: %v", c.typ, isArray, err)
			}
			if n != len(key) {
				t.Errorf("%s(array=%v): composite walk consumed %d of %d bytes", c.typ, isArray, n, len(key))
			}
			if one.Kind != many.Kind || one.Format() != many.Format() {
				t.Errorf("%s(array=%v): siblings disagree: single-column %v/%q, composite %v/%q",
					c.typ, isArray, one.Kind, one.Format(), many.Kind, many.Format())
			}
		}
	}
}
