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

// TestConvertSubstring pins ConvertSubstring's escape-double-quote
// part-separator handling (PG oracle: regexp.c:920-953, :1033-1063) against
// the SUBSTRING(... SIMILAR ... ESCAPE ...) cases in
// postgres/src/test/regress/expected/strings.out ("T581 regular expression
// substring"). Cases cover zero, one, two, and (error) three separators.
func TestConvertSubstring(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		escape  string
		want    string
		wantErr error
	}{
		{"zero-separators", `a%g`, "#", `^(?:a.*g)$`, nil},
		{"one-separator", `a#"%g`, "#", `^(?:a){1,1}?(.*g)$`, nil},
		{"two-separators", `a#"(b_d)#"%`, "#", `^(?:a){1,1}?((?:b.d)){1,1}(?:.*)$`, nil},
		{"two-separators-leading-quote", `#"(b_d)#"%`, "#", `^(?:){1,1}?((?:b.d)){1,1}(?:.*)$`, nil},
		{"two-separators-alt-in-part1", `a|b#"%#"g`, "#", `^(?:a|b){1,1}?(.*){1,1}(?:g)$`, nil},
		{"two-separators-alt-in-part3", `a#"%#"x|g`, "#", `^(?:a){1,1}?(.*){1,1}(?:x|g)$`, nil},
		{"two-separators-alt-in-part2", `a#"%|ab#"g`, "#", `^(?:a){1,1}?(.*|ab){1,1}(?:g)$`, nil},
		{"two-separators-star-parts", `a*#"%#"g*`, "#", `^(?:a*){1,1}?(.*){1,1}(?:g*)$`, nil},
		{"three-separators-error", `a*#"%#"g*#"x`, "#", "", ErrTooManyQuoteSeparators},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ConvertSubstring(c.pattern, c.escape)
			if err != c.wantErr {
				t.Fatalf("ConvertSubstring(%q, %q) err = %v, want %v", c.pattern, c.escape, err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Errorf("ConvertSubstring(%q, %q) = %q, want %q", c.pattern, c.escape, got, c.want)
			}
		})
	}
}

// TestConvertUnaffectedBySubstringMode verifies Convert's byte-for-byte
// output is unchanged by the convert() refactor: existing SIMILAR TO callers
// never see the escape-double-quote part-separator rewrite, even for
// patterns containing an escaped '"'.
func TestConvertUnaffectedBySubstringMode(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		escape  string
	}{
		{"escaped-quote", `a#"b`, "#"},
		{"plain", "_bcd%", DefaultEscape},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Convert(c.pattern, c.escape)
			want, err := convert(c.pattern, c.escape, false)
			if err != nil {
				t.Fatalf("convert(substringMode=false) returned error: %v", err)
			}
			if got != want {
				t.Errorf("Convert(%q, %q) = %q, want %q", c.pattern, c.escape, got, want)
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
