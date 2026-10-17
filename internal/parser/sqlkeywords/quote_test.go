package sqlkeywords

import "testing"

// TestQuoteIdentifier pins quote_identifier (ruleutils.c): bare only for
// lowercase identifier characters that do not form a non-UNRESERVED keyword.
func TestQuoteIdentifier(t *testing.T) {
	for in, want := range map[string]string{
		"public": "public", "_a1": "_a1", "Foo": `"Foo"`, "1a": `"1a"`, "": `""`,
		"$user": `"$user"`, "a b": `"a b"`, `x"y`: `"x""y"`, "select": `"select"`,
		"user": `"user"`, "true": `"true"`, "abort": "abort", "é": `"é"`,
	} {
		if got := QuoteIdentifier(in); got != want {
			t.Errorf("QuoteIdentifier(%q) = %s, want %s", in, got, want)
		}
	}
}
