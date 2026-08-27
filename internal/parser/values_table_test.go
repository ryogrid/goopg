package parser

import "testing"

func TestValuesTable(t *testing.T) {
	for _, q := range []string{
		"VALUES (1),(2),(3)",
		"VALUES (1,2) ORDER BY 1 LIMIT 1",
		"TABLE t",
		"SELECT * FROM (VALUES (1),(2)) AS t(x)",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
		} else if l != n {
			t.Errorf("DIFF %q\n L=%s\n N=%s", q, l, n)
		}
	}
	// upstream-capable forms legacy rejects: must at least PARSE
	for _, q := range []string{
		"WITH w AS (SELECT 1 AS v) TABLE w",
		"VALUES (1),(2) UNION ALL VALUES (3)",
	} {
		if _, _, err := diffParse(q); err != nil {
			if !contains(err.Error(), "legacy:") {
				t.Errorf("NEW parser fails %q: %v", q, err)
			}
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (func() bool { for i := 0; i+len(sub) <= len(s); i++ { if s[i:i+len(sub)] == sub { return true } }; return false })() }
