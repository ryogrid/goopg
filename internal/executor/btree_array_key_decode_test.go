package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// arrayKeyDecodeCases pins the canonical text an array key decodes back to, per
// element type. Round-tripping is the property that matters: the decode's only
// consumers (a single-column index-only scan and the composite key walk of
// btIndexOpClassComparator) both need the value the encoder was handed.
//
// The two entries that are deliberately NOT byte-identical to their input are
// value-preserving, exactly as the scalar decodes are: EncodeNumericKey strips
// trailing mantissa zeros (1.50 → 1.5), and array_out re-quotes only the
// elements that need it (an element with a space comes back quoted).
var arrayKeyDecodeCases = []struct {
	elemType string
	lit      string
	want     string
}{
	{"int4", "{}", "{}"},
	{"int4", "{1}", "{1}"},
	{"int4", "{1,2}", "{1,2}"},
	{"int4", "{-2147483648,2147483647}", "{-2147483648,2147483647}"},
	{"int4", "{1,NULL}", "{1,NULL}"},
	{"int4", "{NULL,NULL}", "{NULL,NULL}"},
	{"int2", "{-32768,0,32767}", "{-32768,0,32767}"},
	{"int8", "{-9223372036854775808,9223372036854775807}", "{-9223372036854775808,9223372036854775807}"},
	// oid keys widen to the int8 key form (oidcmp is unsigned) — the decode has
	// to invert that width, not int4's.
	{"oid", "{0,4294967295}", "{0,4294967295}"},
	{"bool", "{t,f}", "{t,f}"},
	{"float8", "{1.5,2,-0.25}", "{1.5,2,-0.25}"},
	{"numeric", "{1.50,-3}", "{1.5,-3}"},
	{"text", "{a,ab,b}", "{a,ab,b}"},
	{"text", `{a,"b c"}`, `{a,"b c"}`},
	{"text", "{}", "{}"},
}

// TestDecodeArrayBTreeKeyRoundTrip is the primary gate for the decode arm the
// array ENCODE slice left open. Before it, every case here decoded under the
// ELEMENT type (an array column carries catalog.Type{Name:<element>,
// IsArray:true}), so an int4[] key was read as a single int4 out of the first
// four bytes of the segment — the 0x01 presence tag plus three bytes of the
// element key.
func TestDecodeArrayBTreeKeyRoundTrip(t *testing.T) {
	for _, tc := range arrayKeyDecodeCases {
		col := catalog.Column{Name: "a", Type: catalog.Type{Name: tc.elemType, IsArray: true}}
		key, encErr := encodeBTreeKeyForColumn(NewStringDatum(tc.lit), &col, 0)
		if encErr != nil {
			t.Fatalf("%s[] encode %s: %v", tc.elemType, tc.lit, encErr)
		}
		d, n, err := decodeIndexKeyColumn(key, col)
		if err != nil {
			t.Errorf("%s[] decode %s: %v", tc.elemType, tc.lit, err)
			continue
		}
		if n != len(key) {
			t.Errorf("%s[] decode %s consumed %d of %d bytes", tc.elemType, tc.lit, n, len(key))
		}
		if d.Kind != KindString {
			t.Errorf("%s[] decode %s: kind %d, want KindString (arrays are carried as their text form)",
				tc.elemType, tc.lit, d.Kind)
			continue
		}
		if got := d.StringValue(); got != tc.want {
			t.Errorf("%s[] decode %s = %s, want %s", tc.elemType, tc.lit, got, tc.want)
		}
	}
}

// TestArrayBTreeKeyDecodeSiblingParity holds the two decode siblings together
// (Hard-won Rule #2): decodeBTreeKeyToDatum serves a single-column index-only
// scan and decodeIndexKeyColumn the composite/amcheck walk, and an array key
// column that decoded one way in one and another way in the other would make an
// index-only scan disagree with the same index's verification.
func TestArrayBTreeKeyDecodeSiblingParity(t *testing.T) {
	for _, tc := range arrayKeyDecodeCases {
		col := catalog.Column{Name: "a", Type: catalog.Type{Name: tc.elemType, IsArray: true}}
		key, encErr := encodeBTreeKeyForColumn(NewStringDatum(tc.lit), &col, 0)
		if encErr != nil {
			t.Fatalf("%s[] encode %s: %v", tc.elemType, tc.lit, encErr)
		}
		single, err := decodeBTreeKeyToDatum(key, col)
		if err != nil {
			t.Errorf("%s[] single-column decode %s: %v", tc.elemType, tc.lit, err)
			continue
		}
		composite, _, err := decodeIndexKeyColumn(key, col)
		if err != nil {
			t.Errorf("%s[] composite decode %s: %v", tc.elemType, tc.lit, err)
			continue
		}
		if single.Kind != composite.Kind || single.StringValue() != composite.StringValue() {
			t.Errorf("%s[] %s: single-column decode %v/%q disagrees with composite decode %v/%q",
				tc.elemType, tc.lit, single.Kind, single.StringValue(),
				composite.Kind, composite.StringValue())
		}
	}
}

// TestDecodeArrayBTreeKeyCompositeWalk is the defect this slice exists for: the
// array segment is variable width, so a decode that consumes the wrong number of
// bytes does not merely return a wrong array — it desynchronizes the offset for
// every LATER key column. That is the walk btIndexOpClassComparator runs, which
// would then compare unrelated bytes and could report a corrupt index clean.
func TestDecodeArrayBTreeKeyCompositeWalk(t *testing.T) {
	acol := catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	bcol := catalog.Column{Name: "b", Type: catalog.Type{Name: "int4"}}
	for _, arr := range []string{"{}", "{1}", "{1,2}", "{1,NULL}", "{10,20,30}"} {
		ak, encErr := encodeBTreeKeyForColumn(NewStringDatum(arr), &acol, 0)
		if encErr != nil {
			t.Fatalf("encode %s: %v", arr, encErr)
		}
		bk, encErr := encodeBTreeKeyForColumn(NewIntDatum(7), &bcol, 0)
		if encErr != nil {
			t.Fatalf("encode b: %v", encErr)
		}
		key := append(append([]byte{}, ak...), bk...)

		ad, an, err := decodeIndexKeyColumn(key, acol)
		if err != nil {
			t.Errorf("composite (%s,7): array column: %v", arr, err)
			continue
		}
		if an != len(ak) {
			t.Errorf("composite (%s,7): array column consumed %d bytes, want %d — the walk is desynchronized",
				arr, an, len(ak))
			continue
		}
		if got := ad.StringValue(); got != arr {
			t.Errorf("composite (%s,7): array column decoded %s", arr, got)
		}
		bd, bn, err := decodeIndexKeyColumn(key[an:], bcol)
		if err != nil {
			t.Errorf("composite (%s,7): int4 column: %v", arr, err)
			continue
		}
		if bd.Int != 7 || bn != len(bk) {
			t.Errorf("composite (%s,7): int4 column decoded %d (%d bytes), want 7 (%d bytes)",
				arr, bd.Int, bn, len(bk))
		}
	}
}

// TestDecodeArrayBTreeKeyRejectsMalformed pins that a byte run which is not this
// encoding is refused rather than silently mis-read. The refusal matters because
// the comparator's decode-failure fallback (whole-key byte order) is only
// reached when the decode actually reports an error — the M0119-0006 NUMERIC
// slice found the shared `default:` arm never erroring, which disabled it.
func TestDecodeArrayBTreeKeyRejectsMalformed(t *testing.T) {
	col := catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	cases := []struct {
		name string
		key  []byte
	}{
		{"empty", nil},
		{"unterminated element run", []byte{arrayKeyElemPresent, 0x80, 0x00, 0x00, 0x01}},
		{"missing end marker", []byte{arrayKeyElemNull}},
		{"invalid element tag", []byte{0x7f, 0x00}},
		{"truncated element", []byte{arrayKeyElemPresent, 0x80, arrayKeyEnd}},
	}
	for _, tc := range cases {
		if d, n, err := decodeIndexKeyColumn(tc.key, col); err == nil {
			t.Errorf("%s: decoded %q (%d bytes) instead of erroring", tc.name, d.StringValue(), n)
		}
	}
	// A well-formed array followed by trailing bytes is fine for the composite
	// walk (that is the next column) but not for a single-column key, where the
	// key IS the one column.
	key, encErr := encodeBTreeKeyForColumn(NewStringDatum("{1}"), &col, 0)
	if encErr != nil {
		t.Fatalf("encode {1}: %v", encErr)
	}
	if _, err := decodeBTreeKeyToDatum(append(append([]byte{}, key...), 0x01), col); err == nil {
		t.Error("single-column decode accepted a key with trailing bytes")
	}
}

// TestBtIndexCheck_OpClassDamageDetectedAfterArrayColumn is the end-to-end half
// of this slice: the amcheck comparator walk (btIndexOpClassComparator) reading a
// composite key whose LEADING column is an array.
//
// The array value is the same on every row, so the array column is always equal
// and the damaged second-column comparator alone decides the order — which is
// only reachable if the array column consumed exactly its own segment. With the
// element-type misdecode the walk resumed 4 bytes into a 7-byte array segment,
// handed the user FUNCTION 1 routine the tail of the array's own bytes instead
// of the int4 column, and reported this index clean with a comparator that sorts
// backwards.
func TestBtIndexCheck_OpClassDamageDetectedAfterArrayColumn(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION arr_asc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN 1 ELSE -1 END; $$`,
		`CREATE FUNCTION arr_desc_cmp (a int4, b int4) RETURNS int LANGUAGE sql AS $$
			SELECT CASE WHEN $1 = $2 THEN 0 WHEN $1 > $2 THEN -1 ELSE 1 END; $$`,
		`CREATE OPERATOR CLASS int4_arrtail_ops FOR TYPE int4 USING btree AS
			OPERATOR 1 < (int4, int4), OPERATOR 2 <= (int4, int4),
			OPERATOR 3 = (int4, int4), OPERATOR 4 >= (int4, int4),
			OPERATOR 5 > (int4, int4), FUNCTION 1 arr_asc_cmp(int4, int4)`,
		"CREATE TABLE arrcomp (a int4[], i int4)",
		"INSERT INTO arrcomp VALUES ('{1,2}',1), ('{1,2}',2), ('{1,2}',3), ('{1,2}',4)," +
			" ('{1,2}',5), ('{1,2}',6), ('{1,2}',7), ('{1,2}',8)",
		"CREATE INDEX arrficklec ON arrcomp USING btree (a, i int4_arrtail_ops)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('arrficklec')"); err != nil {
		t.Fatalf("healthy array-leading index under a user operator class raised: %v", err)
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc
		SET amproc = 'arr_desc_cmp'::regproc
		WHERE amproc = 'arr_asc_cmp'::regproc`); err != nil {
		t.Fatalf("amproc damage UPDATE: %v", err)
	}

	_, err := runQueryWithErr(ctx, "SELECT bt_index_check('arrficklec')")
	if err == nil {
		t.Fatal("broken operator class after an array key column not detected: bt_index_check reported clean")
	}
	const want = `item order invariant violated for index "arrficklec"`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want substring %q", err.Error(), want)
	}
}
