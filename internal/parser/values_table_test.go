package parser

import "testing"

func TestValuesTable(t *testing.T) {
	for _, q := range []string{
		"VALUES (1),(2),(3)",
		"VALUES (1,2) ORDER BY 1 LIMIT 1",
		"TABLE t",
		"SELECT * FROM (VALUES (1),(2)) AS t(x)",
	} {
		assertParity(t, q)
	}
	// Upstream-capable forms the LEGACY parser rejected. They must parse, and
	// their shape is pinned by the golden captured while both parsers existed.
	for _, q := range []string{
		"WITH w AS (SELECT 1 AS v) TABLE w",
		"VALUES (1),(2) UNION ALL VALUES (3)",
	} {
		assertParity(t, q)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (func() bool { for i := 0; i+len(sub) <= len(s); i++ { if s[i:i+len(sub)] == sub { return true } }; return false })() }
