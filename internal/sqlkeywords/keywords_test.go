package sqlkeywords

import "testing"

// TestIsReservedForQuoting pins the shared keyword set to PostgreSQL's
// quote_identifier() rule in src/backend/utils/adt/ruleutils.c: a keyword is
// "reserved for quoting" iff its kwlist.h category is NOT UNRESERVED_KEYWORD.
// This set is consulted by every goopg identifier quoter (pgQuoteIdent,
// quoteViewIdent, quoteCollationIdent) so the divergence stays fixed across all
// of them.
func TestIsReservedForQuoting(t *testing.T) {
	reserved := []string{
		"user",          // RESERVED_KEYWORD
		"select",        // RESERVED_KEYWORD
		"table",         // RESERVED_KEYWORD
		"where",         // RESERVED_KEYWORD
		"authorization", // TYPE_FUNC_NAME_KEYWORD
		"between",       // COL_NAME_KEYWORD
		"values",        // COL_NAME_KEYWORD
		"freeze",        // TYPE_FUNC_NAME_KEYWORD
		"collation",     // TYPE_FUNC_NAME_KEYWORD
	}
	for _, kw := range reserved {
		if !IsReservedForQuoting(kw) {
			t.Errorf("IsReservedForQuoting(%q) = false, want true", kw)
		}
	}
	// UNRESERVED keywords and ordinary identifiers need no quoting.
	notReserved := []string{
		"name", "type", "version", "value", "option", // UNRESERVED_KEYWORD
		"username", "foo_bar", "_x1", // ordinary identifiers
	}
	for _, id := range notReserved {
		if IsReservedForQuoting(id) {
			t.Errorf("IsReservedForQuoting(%q) = true, want false", id)
		}
	}
}
