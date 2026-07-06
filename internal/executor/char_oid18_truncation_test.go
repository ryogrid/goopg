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
		got, err := evalCast(NewStringDatum(c.in), "char", 0)
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
// bpchar(1) type sharing the same TargetType=="char" string) must NOT be
// routed through that OID-18-specific truncation — it falls through
// unchanged, same as before this fix (bpchar's own typmod truncation/padding
// is a separate, broader gap, out of scope here).
func TestCastExprCharTypmodDisambiguation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Quoted "char" (OID 18): truncates to the first byte.
	rows := runQuery(t, ctx, `SELECT 'xyz'::"char"`)
	if len(rows) != 1 || rows[0][0].StringValue() != "x" {
		t.Errorf(`SELECT 'xyz'::"char" = %v, want "x"`, rows)
	}

	// Bare char (bpchar with implicit length 1, Typmod==1 at the AST level):
	// must not gain the OID-18 truncation behavior from this fix — the
	// generic cast path still passes the string through unchanged (a
	// pre-existing, separately-scoped bpchar-typmod gap).
	rows = runQuery(t, ctx, `SELECT 'xyz'::char`)
	if len(rows) != 1 || rows[0][0].StringValue() != "xyz" {
		t.Errorf(`SELECT 'xyz'::char = %v, want "xyz" (unchanged pass-through)`, rows)
	}
}
