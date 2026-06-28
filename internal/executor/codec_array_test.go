package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestArrayCodecRoundTrip verifies that a user array column value (the text
// "{...}" produced by array_construct) survives the heap encode → decode cycle
// byte-for-byte, for each supported element type. This is the storage half of
// the predicate-gin enabler (M0118-0002, design 0118-0138): without it an
// int4[] column collapsed to a scalar int4 and raised 22P02 on INSERT.
func TestArrayCodecRoundTrip(t *testing.T) {
	cases := []struct {
		elem string
		in   string
		want string // canonical PG text the decoder should reproduce
	}{
		{"int4", "{1}", "{1}"},
		{"int4", "{1,2,3}", "{1,2,3}"},
		{"int4", "{-5,2147483647,-2147483648}", "{-5,2147483647,-2147483648}"},
		{"int2", "{1,2,30000}", "{1,2,30000}"},
		{"int8", "{1,9223372036854775807}", "{1,9223372036854775807}"},
		{"oid", "{0,16384}", "{0,16384}"},
		{"float8", "{1,2.5,1000000}", "{1,2.5,1000000}"},
		{"bool", "{t,f,t}", "{t,f,t}"},
		{"text", "{a,bb,ccc}", "{a,bb,ccc}"},
		{"int4", "{}", "{}"},
	}
	for _, tc := range cases {
		typ := catalog.Type{Name: tc.elem, IsArray: true}
		blob, err := encodeValuePG(typ, NewStringDatum(tc.in))
		if err != nil {
			t.Fatalf("%s encode %q: %v", tc.elem, tc.in, err)
		}
		got, n, err := decodePhysicalPGValueMctx(typ, blob, nil)
		if err != nil {
			t.Fatalf("%s decode %q: %v", tc.elem, tc.in, err)
		}
		if n != len(blob) {
			t.Errorf("%s %q: decoded %d bytes, blob is %d", tc.elem, tc.in, n, len(blob))
		}
		if got.Kind != KindString || got.StringValue() != tc.want {
			t.Errorf("%s %q: round-trip = %q, want %q", tc.elem, tc.in, got.StringValue(), tc.want)
		}
	}
}

// TestArrayCodecTextElementQuoting checks that text elements needing quotes
// (whitespace, delimiters, the literal NULL) are emitted in PG's quoted form.
func TestArrayCodecTextElementQuoting(t *testing.T) {
	typ := catalog.Type{Name: "text", IsArray: true}
	in := `{a,"b c","d,e"}`
	blob, err := encodeValuePG(typ, NewStringDatum(in))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, _, err := decodePhysicalPGValueMctx(typ, blob, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := `{a,"b c","d,e"}`
	if got.StringValue() != want {
		t.Errorf("round-trip = %q, want %q", got.StringValue(), want)
	}
}
