package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDecodePhysicalPGInt8RoundTrip pins the M0111-0004 fix: int8/bigint
// values must decode from the PG-native physical tuple format. Before the
// fix, decodePhysicalPGValueMctx had no int8 case, so every int8 value
// (count(*)/sum() results, plain bigint columns) fell through to the
// "unsupported PostgreSQL physical type" default branch. The seqscan then
// silently dropped the un-decodable row, making bigint columns appear empty.
func TestDecodePhysicalPGInt8RoundTrip(t *testing.T) {
	for _, name := range []string{"int8", "bigint", "bigserial"} {
		typ := catalog.Type{Name: name}
		for _, v := range []int64{0, 5, -1, 9223372036854775807, -9223372036854775808} {
			enc, err := encodeValuePG(typ, NewIntDatum(v))
			if err != nil {
				t.Fatalf("encodeValuePG(%s, %d): %v", name, v, err)
			}
			got, n, err := decodePhysicalPGValueMctx(typ, enc, nil)
			if err != nil {
				t.Fatalf("decodePhysicalPGValueMctx(%s, %d): %v", name, v, err)
			}
			if n != 8 {
				t.Fatalf("%s decode consumed %d bytes, want 8", name, n)
			}
			if got.Kind != KindInt || got.Int != v {
				t.Fatalf("%s round-trip got kind=%v int=%d, want KindInt %d", name, got.Kind, got.Int, v)
			}
		}
	}
}

// TestDecodePhysicalPGNameRoundTrip pins that the fixed-64-byte PG "name"
// type round-trips. Same M0111-0004 silent-row-drop class as int8.
func TestDecodePhysicalPGNameRoundTrip(t *testing.T) {
	typ := catalog.Type{Name: "name"}
	for _, s := range []string{"", "abc", "hello_world", "pg_catalog"} {
		enc, err := encodeValuePG(typ, NewStringDatum(s))
		if err != nil {
			t.Fatalf("encodeValuePG(name, %q): %v", s, err)
		}
		got, n, err := decodePhysicalPGValueMctx(typ, enc, nil)
		if err != nil {
			t.Fatalf("decodePhysicalPGValueMctx(name, %q): %v", s, err)
		}
		if n != 64 {
			t.Fatalf("name decode consumed %d bytes, want 64", n)
		}
		if got.Kind != KindString || got.StringValue() != s {
			t.Fatalf("name round-trip got kind=%v str=%q, want %q", got.Kind, got.StringValue(), s)
		}
	}
}

// TestDecodePhysicalPGRowInt8DoesNotDropRow exercises the exact heap read
// path that regressed: a full row containing a single int8 column must
// decode without error. Before the fix decodePhysicalPGRowIntoMctx returned
// an error here, which the seqscan swallowed as "no visible row".
func TestDecodePhysicalPGRowInt8DoesNotDropRow(t *testing.T) {
	cols := []catalog.Column{{Name: "n", Type: catalog.Type{Name: "bigint"}}}
	body, err := EncodeRowPG(cols, Row{NewIntDatum(152)})
	if err != nil {
		t.Fatalf("EncodeRowPG(bigint): %v", err)
	}
	got := make(Row, 1)
	if err := decodePhysicalPGRowIntoMctx(got, cols, body, nil); err != nil {
		t.Fatalf("decodePhysicalPGRowIntoMctx(bigint): %v", err)
	}
	if got[0].Kind != KindInt || got[0].Int != 152 {
		t.Fatalf("row decode got kind=%v int=%d, want KindInt 152", got[0].Kind, got[0].Int)
	}
}

// TestEncodeValuePGInt2OutOfRangeMessage pins the M0111-0005 fix: storing an
// out-of-range *string literal* into an int2/smallint column (the int2in
// equivalent, e.g. INSERT INTO t(c) VALUES ('100000')) must report the
// input-function wording `value "N" is out of range for type smallint`, not
// the bare `smallint out of range` which PostgreSQL reserves for int2pl /
// int2mul arithmetic overflow. The int4 sibling arm already did this; int2
// drifted and surfaced as the residual 4-line diff in the int2 regress case.
func TestEncodeValuePGInt2OutOfRangeMessage(t *testing.T) {
	for _, name := range []string{"int2", "smallint"} {
		typ := catalog.Type{Name: name}
		_, err := encodeValuePG(typ, NewStringDatum("100000"))
		if err == nil {
			t.Fatalf("encodeValuePG(%s, '100000'): expected out-of-range error", name)
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Fatalf("encodeValuePG(%s, '100000'): error type %T, want *ExecError", name, err)
		}
		if ee.Code != "22003" {
			t.Fatalf("encodeValuePG(%s, '100000'): code %q, want 22003", name, ee.Code)
		}
		const want = `value "100000" is out of range for type smallint`
		if ee.Message != want {
			t.Fatalf("encodeValuePG(%s, '100000'): message %q, want %q", name, ee.Message, want)
		}
	}
	// In-range string literals still encode cleanly.
	if _, err := encodeValuePG(catalog.Type{Name: "int2"}, NewStringDatum("32767")); err != nil {
		t.Fatalf("encodeValuePG(int2, '32767'): unexpected error %v", err)
	}
}

// TestDecodePhysicalPGFloatKind pins the M0111-0006 fix: float4/float8 values
// stored as varlena text must decode back to KindNumeric (not KindString) so
// that ORDER BY / comparison are numeric. Before the fix the PG-physical
// decoder lumped floats in with the text case and returned KindString, so a
// float8 column sorted lexicographically (e.g. -1234 ahead of -2147483647) —
// the residual failure in the numerology regress case. The legacy decoders
// already returned KindNumeric; this restores parity in the PG-native path
// that became the primary read path in M0111-0001.
func TestDecodePhysicalPGFloatKind(t *testing.T) {
	for _, name := range []string{"float4", "real", "float8", "double precision", "double"} {
		typ := catalog.Type{Name: name}
		for _, v := range []int64{0, 1234, -2147483647, 123456, -32767} {
			enc, err := encodeValuePG(typ, NewIntDatum(v))
			if err != nil {
				t.Fatalf("encodeValuePG(%s, %d): %v", name, v, err)
			}
			got, _, err := decodePhysicalPGValueMctx(typ, enc, nil)
			if err != nil {
				t.Fatalf("decodePhysicalPGValueMctx(%s, %d): %v", name, v, err)
			}
			if got.Kind != KindNumeric {
				t.Fatalf("%s decode kind=%v, want KindNumeric (string sort regression)", name, got.Kind)
			}
		}
	}

	// Comparison proof: -1234 vs -123456 sort the wrong way under string
	// comparison ("-1234" < "-123456") but correctly under numeric
	// comparison (-1234 > -123456). Decode both as float8 and assert the
	// numeric ordering holds.
	typ := catalog.Type{Name: "float8"}
	decode := func(v int64) Datum {
		enc, err := encodeValuePG(typ, NewIntDatum(v))
		if err != nil {
			t.Fatalf("encodeValuePG(float8, %d): %v", v, err)
		}
		d, _, err := decodePhysicalPGValueMctx(typ, enc, nil)
		if err != nil {
			t.Fatalf("decodePhysicalPGValueMctx(float8, %d): %v", v, err)
		}
		return d
	}
	cmp, err := compareDatum(decode(-1234), decode(-123456), 0)
	if err != nil {
		t.Fatalf("compareDatum(-1234, -123456): %v", err)
	}
	if cmp <= 0 {
		t.Fatalf("compareDatum(-1234, -123456) = %d, want > 0 (lexicographic regression)", cmp)
	}
}
