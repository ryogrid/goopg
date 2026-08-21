package similarto

import "testing"

// TestConvert pins Convert against PostgreSQL's similar_escape_internal
// output (PG oracle: postgres/src/test/regress/expected/strings.out:617-690;
// see internal/parser/similar_to_test.go for the full byte-for-byte
// EXPLAIN-shape pins driven through the parser's constant-fold path — these
// cases exercise the conversion function directly).
func TestConvert(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		escape  string
		want    string
	}{
		{"plain", "_bcd%", DefaultEscape, "^(?:.bcd.*)$"},
		{"custom-escape", "_bcd#%", "#", `^(?:.bcd\%)$`},
		{"no-escape", "_bcd\\%", "", `^(?:.bcd\\.*)$`},
		{"class-underscore", "_[_[:alpha:]_]_", DefaultEscape, "^(?:.[_[:alpha:]_].)$"},
		{"class-paren", "()[([:alnum:](]()", DefaultEscape, `^(?:(?:)[([:alnum:](](?:))$`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Convert(c.pattern, c.escape)
			if got != c.want {
				t.Errorf("Convert(%q, %q) = %q, want %q", c.pattern, c.escape, got, c.want)
			}
		})
	}
}

// TestValidateEscape pins the 22025 length check (PG oracle: regexp.c:797-806).
func TestValidateEscape(t *testing.T) {
	cases := []struct {
		escape  string
		wantErr bool
	}{
		{"", false},
		{"#", false},
		{"\\", false},
		{"##", true},
		{"abc", true},
	}
	for _, c := range cases {
		if err := ValidateEscape(c.escape); (err != nil) != c.wantErr {
			t.Errorf("ValidateEscape(%q) err=%v, wantErr=%v", c.escape, err, c.wantErr)
		}
	}
}
