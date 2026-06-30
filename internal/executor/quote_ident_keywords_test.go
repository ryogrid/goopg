package executor

import "testing"

// TestPgQuoteIdentReservedKeywords pins pgQuoteIdent (the server-side
// quote_ident builtin, %I in format(), and pg_get_*def identifier rendering) to
// PostgreSQL's quote_identifier() in src/backend/utils/adt/ruleutils.c, which
// quotes every keyword whose category is NOT UNRESERVED_KEYWORD even when the
// identifier is otherwise a bare-safe all-lowercase word.
//
// Regression guard for the latent divergence recorded in DU-002 slice 379: a
// foreign-data-wrapper/user-mapping option (or role/column/table name) called
// "user" must round-trip through pg_dump as the quoted "user", not the bare
// keyword. Before the fix pgQuoteIdent had no reserved-word check despite its
// comment claiming one.
func TestPgQuoteIdentReservedKeywords(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Reserved / type-func-name / col-name keywords -> always quoted.
		{"user", `"user"`},                   // RESERVED_KEYWORD (the slice-379 trap)
		{"select", `"select"`},               // RESERVED_KEYWORD
		{"table", `"table"`},                 // RESERVED_KEYWORD
		{"where", `"where"`},                 // RESERVED_KEYWORD
		{"authorization", `"authorization"`}, // TYPE_FUNC_NAME_KEYWORD
		{"between", `"between"`},             // COL_NAME_KEYWORD
		{"values", `"values"`},               // COL_NAME_KEYWORD
		{"freeze", `"freeze"`},               // TYPE_FUNC_NAME_KEYWORD
		// UNRESERVED keywords -> left bare (need no quoting, matching PG).
		{"name", "name"},
		{"type", "type"},
		{"version", "version"},
		{"value", "value"},
		{"option", "option"},
		// Ordinary non-keyword identifiers -> bare.
		{"username", "username"},
		{"foo_bar", "foo_bar"},
		{"_x1", "_x1"},
		// Char-class / case rules unchanged by the keyword work.
		{"User", `"User"`}, // uppercase
		{"a b", `"a b"`},   // space
		{"1col", `"1col"`}, // leading digit
		{"", `""`},         // empty
		{`a"b`, `"a""b"`},  // embedded quote doubled
	}
	for _, c := range cases {
		if got := pgQuoteIdent(c.in); got != c.want {
			t.Errorf("pgQuoteIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestQuoteViewIdentReservedKeywords pins quoteViewIdent (pg_get_viewdef column
// alias rendering) to the same quote_identifier() reserved-word rule as
// pgQuoteIdent. Both now share internal/sqlkeywords, so a view alias named
// e.g. "select" must round-trip quoted, not bare (DU-002 slice 383: closes the
// sibling-quoter gap deferred in slice 382).
func TestQuoteViewIdentReservedKeywords(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"select", `"select"`}, // RESERVED_KEYWORD
		{"user", `"user"`},     // RESERVED_KEYWORD
		{"values", `"values"`}, // COL_NAME_KEYWORD
		{"name", "name"},       // UNRESERVED_KEYWORD -> bare
		{"col1", "col1"},       // ordinary identifier -> bare
		{"Col", `"Col"`},       // uppercase -> quoted (char-class)
	}
	for _, c := range cases {
		if got := quoteViewIdent(c.in); got != c.want {
			t.Errorf("quoteViewIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
