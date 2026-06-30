package catalog

import "testing"

// TestQuoteCollationIdentReservedKeywords pins quoteCollationIdent (the COLLATE
// identifier rendering in pg_get_indexdef) to PostgreSQL's quote_identifier()
// reserved-word rule via the shared internal/sqlkeywords set. A collation named
// e.g. "select" must render quoted, not bare (DU-002 slice 383: closes the
// sibling-quoter gap deferred in slice 382, whose comment previously conceded
// "reserved-word quoting is not reproduced").
func TestQuoteCollationIdentReservedKeywords(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"select", `"select"`},   // RESERVED_KEYWORD
		{"user", `"user"`},       // RESERVED_KEYWORD
		{"between", `"between"`}, // COL_NAME_KEYWORD
		{"name", "name"},         // UNRESERVED_KEYWORD -> bare
		{"my_coll", "my_coll"},   // ordinary identifier -> bare
		{"C", `"C"`},             // uppercase (the "C" locale) -> quoted
		{"", `""`},               // empty -> quoted
	}
	for _, c := range cases {
		if got := quoteCollationIdent(c.in); got != c.want {
			t.Errorf("quoteCollationIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
