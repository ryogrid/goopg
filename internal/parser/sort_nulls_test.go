package parser

import "testing"

// TestSortNullsOrderParity — `ORDER BY x NULLS FIRST|LAST` without an explicit
// ASC/DESC. gram.y's sortby is `a_expr opt_asc_desc opt_nulls_order`, so the
// two parts are optional independently; this grammar only had the combinations
// that spell out ASC or DESC, so `ORDER BY 1 NULLS LAST` was a syntax error on
// the routed SELECT path.
func TestSortNullsOrderParity(t *testing.T) {
	for _, q := range []string{
		"SELECT a FROM t ORDER BY 1 NULLS LAST",
		"SELECT a FROM t ORDER BY 1 NULLS FIRST",
		"SELECT a FROM t ORDER BY a NULLS LAST",
		"SELECT a FROM t ORDER BY a, b NULLS FIRST",
		// the explicit forms must stay identical
		"SELECT a FROM t ORDER BY a DESC NULLS LAST",
		"SELECT a FROM t ORDER BY a ASC NULLS FIRST",
		"SELECT a FROM t ORDER BY a DESC",
		"SELECT a FROM t ORDER BY a",
	} {
		assertParity(t, q)
	}
}
