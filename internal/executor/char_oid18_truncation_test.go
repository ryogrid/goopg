package executor

import "testing"

// TestEvalCastCharTruncatesToFirstByte pins the M0122-0005 residual: PostgreSQL's
// charin() (postgres/src/backend/utils/adt/char.c) takes the first byte of any
// non-"\NNN"-escape input and silently discards the rest. Before this fix,
// evalCast's "char" branch only special-cased the octal-escape form and
// returned a plain multi-byte string unchanged.
func TestEvalCastCharTruncatesToFirstByte(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"xyz", "x"},
		{"x", "x"},
		{"", ""},              // zero-length input: charin returns ch[0] == '\0' → charout empty string
		{"\\101", "A"},        // octal escape form still takes precedence (unchanged behavior)
		{"hello world", "h"},
	}
	for _, c := range cases {
		got, err := evalCast(NewStringDatum(c.in), "char", 0, nil)
		if err != nil {
			t.Fatalf("evalCast(%q, \"char\") unexpected error: %v", c.in, err)
		}
		if got.Kind != KindString || got.StringValue() != c.want {
			t.Errorf("evalCast(%q, \"char\") = %q, want %q", c.in, got.StringValue(), c.want)
		}
	}
}

// TestCastExprCharTypmodDisambiguation exercises the full parse→plan→eval
// pipeline for M0122-0005's OID-18-vs-bpchar(1) disambiguation at the
// CastExpr evaluation call site: the quoted `"char"` identifier (Typmod==0)
// must truncate to its first byte (real PG's charin() semantics), while the
// bare `char`/CHARACTER keyword (grammar-synthesized Typmod==1, a distinct
// bpchar(1) type sharing the same TargetType=="char" string) is routed
// through the bpchar(n) typmod-truncation path instead (verified against
// real PG 18.3: `SELECT 'xyz'::char` → 'x', i.e. bpchar(1) truncation, not
// OID-18's charin() first-byte rule — the two happen to agree at length 1,
// but arrive via distinct code paths).
func TestCastExprCharTypmodDisambiguation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Quoted "char" (OID 18): truncates to the first byte.
	rows := runQuery(t, ctx, `SELECT 'xyz'::"char"`)
	if len(rows) != 1 || rows[0][0].StringValue() != "x" {
		t.Errorf(`SELECT 'xyz'::"char" = %v, want "x"`, rows)
	}

	// Bare char (bpchar with implicit length 1, Typmod==1 at the AST level):
	// truncated to 1 character via the bpchar(n) typmod path.
	rows = runQuery(t, ctx, `SELECT 'xyz'::char`)
	if len(rows) != 1 || rows[0][0].StringValue() != "x" {
		t.Errorf(`SELECT 'xyz'::char = %v, want "x" (bpchar(1) truncation)`, rows)
	}
}

// TestInlineCastVarcharBpcharTypmodTruncation pins the M0122-0005 follow-up:
// explicit `::varchar(n)`/`::bpchar(n)`/`::char(n)` casts must truncate an
// over-length value to n characters. Verified against real PG 18.3: this is
// silent truncation (no 22001 error), unlike assignment/INSERT coercion —
// e.g. `SELECT 'abcdef'::varchar(3)` returns 'abc' with no error.
//
// UPDATED 2026-09-22 (AI-20260922-004850-016): the short-value bpchar cases
// now expect PADDING. This comment used to say goopg "has no distinct padded
// representation for bpchar (matching the existing coerceTextLikeDatum
// storage-path convention), so padding stays deferred" — that convention was
// REPLACED by M0143-0007b slice 1, which made storage blank-pad, and the cast
// was then the only bpchar producer still emitting a trimmed image. The
// expectations below are not flipped to match new code; they are flipped to
// match PostgreSQL, measured on a live PG 18.3:
//
//	select octet_length(''::bpchar(3))   -> 3   (the cast pads)
//	select quote_literal(''::bpchar(3))  -> ''  (quote_literal takes text, so
//	                                             the implicit bpchar->text
//	                                             cast, rtrim1, strips it again)
//
// Both readings are of the SAME padded value; which one a caller sees depends
// on whether it consumes the bpchar directly or through the text cast. varchar
// is unaffected and its cases below are unchanged, which is the point of
// keeping them side by side here.
func TestInlineCastVarcharBpcharTypmodTruncation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		query string
		want  string
	}{
		{`SELECT 'abcdef'::varchar(3)`, "abc"},
		{`SELECT 'abcdef'::char(3)`, "abc"},      // truncated to n, then already n wide
		{`SELECT 'abcdef'::character(3)`, "abc"}, // same
		{`SELECT 'abc'::varchar(3)`, "abc"},      // exact fit: no truncation
		{`SELECT 'ab'::varchar(5)`, "ab"},        // varchar never pads
		{`SELECT ''::bpchar(3)`, "   "},          // bpchar pads: PG octet_length is 3
		{`SELECT 'ab'::char(4)`, "ab  "},         // short bpchar pads to n
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.query)
		if len(rows) != 1 || rows[0][0].StringValue() != c.want {
			t.Errorf("%s = %v, want %q", c.query, rows, c.want)
		}
	}
}
