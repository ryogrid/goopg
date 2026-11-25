package postmaster

import "testing"

// TestExtractFirstSQLIdentQuoted pins the scanner's quoted-identifier rule
// for the compat role-DDL path: a doubled "" inside a quoted name is one
// literal quote, and the name ends at the first lone '"'.
func TestExtractFirstSQLIdentQuoted(t *testing.T) {
	cases := map[string]string{
		`"regress_""quoted" with login`: `regress_"quoted`,
		`"a b";`:                        `a b`,
		`""""`:                          `"`,
		`plain_name login`:              `plain_name`,
		`"unbalanced`:                   `unbalanced`,
	}
	for in, want := range cases {
		if got := extractFirstSQLIdent("", in); got != want {
			t.Errorf("extractFirstSQLIdent(%q) = %q, want %q", in, got, want)
		}
	}
}
