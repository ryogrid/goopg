package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

// TestArraySetOps verifies the anyarray containment/overlap operators
// (@> <@ &&) used by predicate-gin (design 0118-0139). These route to
// set-membership semantics rather than the geometric box operators when both
// operands are array literals.
func TestArraySetOps(t *testing.T) {
	cases := []struct {
		op   parser.OpCode
		a, b string
		want bool
	}{
		// a @> b: a contains every element of b
		{parser.OpContains, "{1,2,3}", "{1}", true},
		{parser.OpContains, "{1,2,3}", "{1,3}", true},
		{parser.OpContains, "{1,2,3}", "{4}", false},
		{parser.OpContains, "{1,2,3}", "{}", true},
		{parser.OpContains, "{1}", "{1,2}", false},
		// a <@ b: every element of a is in b
		{parser.OpContainedBy, "{1}", "{1,2,3}", true},
		{parser.OpContainedBy, "{1,4}", "{1,2,3}", false},
		{parser.OpContainedBy, "{}", "{1,2,3}", true},
		// a && b: overlap
		{parser.OpOverlap, "{1,2}", "{2,3}", true},
		{parser.OpOverlap, "{1,2}", "{3,4}", false},
		{parser.OpOverlap, "{}", "{1}", false},
		// text elements
		{parser.OpContains, "{a,b,c}", "{b}", true},
		{parser.OpContains, `{a,"b c"}`, `{"b c"}`, true},
	}
	for _, c := range cases {
		if got := evalArraySetOp(c.op, c.a, c.b); got != c.want {
			t.Errorf("evalArraySetOp(%v, %q, %q) = %v, want %v", c.op, c.a, c.b, got, c.want)
		}
	}
}
