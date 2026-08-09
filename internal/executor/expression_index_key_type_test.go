package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// TestExprKeyDecodeTypeRoundTrip is the sibling-path pin between the two halves
// of the expression-key contract (Hard-won Rule #2): whatever
// encodeArbiterExprKey writes for a Datum, decodeIndexKeyColumn under the
// surrogate type exprKeyDecodeType names must consume EXACTLY those bytes.
//
// The width half is what makes this non-obvious: an int4-typed index expression
// is encoded through EncodeInt8 (8 bytes, kind dispatch) while an int4 *column*
// key is 4 bytes, and a date-typed expression is encoded as int64 micros while a
// date column key is int4 days. Decoding under the SQL type instead of the
// surrogate would consume the wrong width and silently desynchronize every
// later column of a composite key — the comparator would then report bogus
// "item order invariant violated" findings on a healthy index.
func TestExprKeyDecodeTypeRoundTrip(t *testing.T) {
	ts := time.Date(2024, 3, 5, 6, 7, 8, 0, time.UTC).Truncate(time.Microsecond)
	// keyExpr is the resolved key expression the encoder is handed. It only
	// matters for the type-directed float arm (exprKeyIsFloat); every other row
	// encodes off the Datum kind and passes nil, exactly as a caller with an
	// unresolvable expression would.
	floatKeyExpr := &planner.ColumnRef{Type: catalog.Type{Name: "float8"}}
	for _, tc := range []struct {
		name    string
		sqlType string
		v       Datum
		keyExpr planner.Expr
	}{
		{"int2", "int2", NewIntDatum(7), nil},
		{"int4", "int4", NewIntDatum(42), nil},
		{"integer alias", "integer", NewIntDatum(42), nil},
		{"int8", "int8", NewIntDatum(1 << 40), nil},
		{"bool", "bool", NewBoolDatum(true), nil},
		{"numeric", "numeric", Datum{Kind: KindNumeric, Int: 12345, Scale: 2}, nil},
		{"timestamp", "timestamp", NewTimeDatum(ts), nil},
		{"date", "date", NewTimeDatum(ts), nil},
		{"text", "text", NewStringDatum("abc"), nil},
		{"varchar", "varchar", NewStringDatum("abc"), nil},
		{"uuid", "uuid", NewStringDatum("00000000-0000-0000-0000-000000000001"), nil},
		{"bytea", "bytea", NewBytesDatum([]byte{0x00, 0x01, 0x7f}), nil},
		// The two kinds ONE float column produces (floatTextDatum): a plain
		// decimal re-parses as numeric, an exponent/Infinity/NaN text does not.
		// Both must land on the float8 surrogate's 8 bytes.
		{"float8 numeric-kind", "float8", Datum{Kind: KindNumeric, Int: 15, Scale: 1}, floatKeyExpr},
		{"float8 string-kind", "float8", NewStringDatum("1e+30"), floatKeyExpr},
		{"float4", "float4", NewStringDatum("Infinity"), floatKeyExpr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surrogate, _, ok := exprKeyDecodeType(catalog.Type{Name: tc.sqlType})
			if !ok {
				t.Fatalf("exprKeyDecodeType(%s) declined — the comparator would "+
					"fall back to whole-key byte order for this type", tc.sqlType)
			}
			enc := encodeArbiterExprKey(tc.v, tc.keyExpr, 0)
			if enc == nil {
				t.Fatalf("encodeArbiterExprKey returned nil for %s", tc.name)
			}
			// A trailing sentinel stands in for "another key column follows":
			// the decoder must not read into it, and must report exactly the
			// prefix length it consumed.
			withTail := append(append([]byte(nil), enc...), 0xAB, 0xCD)
			_, n, err := decodeIndexKeyColumn(withTail, catalog.Column{Type: surrogate})
			if err != nil {
				t.Fatalf("decodeIndexKeyColumn under surrogate %q: %v", surrogate.Name, err)
			}
			if n != len(enc) {
				t.Fatalf("surrogate %q consumed %d bytes, encoder wrote %d — a "+
					"composite key walk would desynchronize here",
					surrogate.Name, n, len(enc))
			}
		})
	}
}

// TestExprKeyDecodeTypeDeclinesUninvertible pins the deliberate declines: kinds
// whose key bytes cannot be turned back into a value keep the pre-M0119-0006
// behaviour (whole-key byte order) rather than decoding to something wrong.
// An enum's EncodeFloat8(sort order) key needs the enum catalog entry to invert;
// interval/point/composite have no expression-key encoding arm at all.
// (float4/float8 used to be listed here. They are now accepted — the encoder's
// type-directed arm gives them the float8 column encoding — so their absence
// from this list is the point, not an omission.)
func TestExprKeyDecodeTypeDeclinesUninvertible(t *testing.T) {
	for _, name := range []string{"mood", "interval", "point", ""} {
		if _, _, ok := exprKeyDecodeType(catalog.Type{Name: name}); ok {
			t.Errorf("exprKeyDecodeType(%q) accepted, but encodeArbiterExprKey "+
				"has no invertible encoding for it", name)
		}
	}
	if _, _, ok := exprKeyDecodeType(catalog.Type{Name: "int4", IsArray: true}); ok {
		t.Error("exprKeyDecodeType accepted int4[]: array keys have no encoding arm")
	}
}

// TestExprKeyDecodeTypeRoutineSafety pins which surrogates may be fed to a user
// opclass FUNCTION 1 routine. bool and bytea decode to a Datum of a different
// kind than a comparator declared over the SQL type expects (integer 0/1, and a
// string of raw bytes), so they must order by bytes instead of inventing a call.
func TestExprKeyDecodeTypeRoutineSafety(t *testing.T) {
	for _, tc := range []struct {
		sqlType string
		want    bool
	}{
		{"int4", true},
		{"numeric", true},
		{"timestamp", true},
		{"text", true},
		{"bool", false},
		{"bytea", false},
		// float8 decodes exactly as a float8 COLUMN key does ('g'-formatted
		// string), so a comparator declared over float8 sees nothing new.
		{"float8", true},
		{"float4", true},
	} {
		_, allow, ok := exprKeyDecodeType(catalog.Type{Name: tc.sqlType})
		if !ok {
			t.Fatalf("%s unexpectedly declined", tc.sqlType)
		}
		if allow != tc.want {
			t.Errorf("%s: allowRoutine = %v, want %v", tc.sqlType, allow, tc.want)
		}
	}
}
