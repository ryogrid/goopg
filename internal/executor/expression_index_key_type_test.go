package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
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
	for _, tc := range []struct {
		name    string
		sqlType string
		v       Datum
	}{
		{"int2", "int2", NewIntDatum(7)},
		{"int4", "int4", NewIntDatum(42)},
		{"integer alias", "integer", NewIntDatum(42)},
		{"int8", "int8", NewIntDatum(1 << 40)},
		{"bool", "bool", NewBoolDatum(true)},
		{"numeric", "numeric", Datum{Kind: KindNumeric, Int: 12345, Scale: 2}},
		{"timestamp", "timestamp", NewTimeDatum(ts)},
		{"date", "date", NewTimeDatum(ts)},
		{"text", "text", NewStringDatum("abc")},
		{"varchar", "varchar", NewStringDatum("abc")},
		{"uuid", "uuid", NewStringDatum("00000000-0000-0000-0000-000000000001")},
		{"bytea", "bytea", NewBytesDatum([]byte{0x00, 0x01, 0x7f})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surrogate, _, ok := exprKeyDecodeType(catalog.Type{Name: tc.sqlType})
			if !ok {
				t.Fatalf("exprKeyDecodeType(%s) declined — the comparator would "+
					"fall back to whole-key byte order for this type", tc.sqlType)
			}
			enc := encodeArbiterExprKey(tc.v, 0)
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
// float8 has no encodeArbiterExprKey arm at all (such rows are not indexed);
// an enum's EncodeFloat8(sort order) key needs the enum catalog entry to invert.
func TestExprKeyDecodeTypeDeclinesUninvertible(t *testing.T) {
	for _, name := range []string{"float4", "float8", "mood", "interval", "point", ""} {
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
