package postmaster

import "testing"

// TestNormalizeCompatSQLPreservesQuotedIdentifierCase is the review/260831-2
// CP-2 guard. normalizeSQLPreservingLiterals lowercased everything outside
// SINGLE quotes, so a quoted identifier was folded too and
// `SELECT * FROM "Foo"` and `SELECT * FROM "foo"` produced the same
// planCacheKey. PG downcases only UNquoted identifiers (scan.l's {identifier}
// rule calls downcase_truncate_identifier; the <xd> delimited form is taken
// as-is), so those are two different tables — and on a live goopg server the
// collision was a wrong-results bug: with rows 111 in "Foo" and 222 in "foo",
// `select * from "foo"` returned 111 from the cached "Foo" plan.
func TestNormalizeCompatSQLPreservesQuotedIdentifierCase(t *testing.T) {
	upper := normalizeCompatSQL(`SELECT * FROM "Foo"`)
	lower := normalizeCompatSQL(`SELECT * FROM "foo"`)
	if upper == lower {
		t.Errorf("quoted identifiers folded together: both normalize to %q", upper)
	}
	if want := `select * from "Foo"`; upper != want {
		t.Errorf("normalizeCompatSQL(SELECT * FROM \"Foo\") = %q, want %q", upper, want)
	}
	// The keyword/unquoted-identifier folding, the whitespace collapse and the
	// single-quoted-literal preservation must all survive the change.
	if got, want := normalizeCompatSQL("SELECT   A\nFROM  T"), "select a from t"; got != want {
		t.Errorf("unquoted normalization = %q, want %q", got, want)
	}
	if got, want := normalizeCompatSQL(`INSERT INTO t VALUES ('A')`), `insert into t values ('A')`; got != want {
		t.Errorf("string literal normalization = %q, want %q", got, want)
	}
	// A doubled quote is an escaped quote inside the identifier, not its end,
	// so the following text must stay inside the preserved span.
	if got, want := normalizeCompatSQL(`SELECT * FROM "A""B" X`), `select * from "A""B" x`; got != want {
		t.Errorf("escaped-quote identifier = %q, want %q", got, want)
	}
	// Distinct keys are what the wrong-results bug actually hinged on.
	fp := sessionPlannerFingerprint(nil)
	if planCacheKey(`SELECT * FROM "Foo"`, 5, fp) == planCacheKey(`SELECT * FROM "foo"`, 5, fp) {
		t.Error(`planCacheKey collides for "Foo" and "foo"`)
	}
}
