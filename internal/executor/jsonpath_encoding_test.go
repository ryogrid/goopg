package executor

import (
	"strings"
	"testing"
)

// TestJSONPathTypedLiteralEscapes exercises the ::jsonpath escape-lexing
// cases jsonpath_encoding.sql checks against PG 18.3
// (postgres/src/test/regress/expected/jsonpath_encoding.out). M0134-0134.
//
// Every `\uXXXX` input is assembled via string concatenation ("\\" + "u" +
// hex), never written as a literal backslash-u escape in this file's source
// text: some tooling in this environment silently decodes a literal
// backslash-u sequence into the actual Unicode codepoint before the file
// reaches disk, which both defeats the point of testing the RAW escape
// spelling and, for lone surrogates like D83D, produces invalid UTF-8 that
// corrupts the file (bit us once already writing jsonpath_encoding.go — see
// its jsonpathNulEscapeError comment).
func TestJSONPathTypedLiteralEscapes(t *testing.T) {
	bs := "\\"
	u := func(hex string) string { return bs + "u" + hex }

	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string // substring of the expected error message; "" means no error
	}{
		{"incomplete-escape-bare", `"` + u("") + `"`, "", "invalid Unicode escape sequence"},
		{"incomplete-escape-two-digits", `"` + u("00") + `"`, "", "invalid Unicode escape sequence"},
		{"non-hex-digit", `"` + u("000") + "g" + `"`, "", "invalid Unicode escape sequence"},
		{"legal-null-escape", `"` + u("0000") + `"`, "", "unsupported Unicode escape sequence"},
		{"mixed-case-hex", `"` + u("aBcD") + `"`, "", ""}, // want overwritten below
		{"surrogate-pair-emoji", `"` + u("d83d") + u("de04") + u("d83d") + u("dc36") + `"`, "\"\U0001F604\U0001F436\"", ""},
		{"double-high-surrogate", `"` + u("d83d") + u("d83d") + `"`, "", "high surrogate must not follow a high surrogate"},
		{"surrogates-wrong-order", `"` + u("de04") + u("d83d") + `"`, "", "low surrogate must follow a high surrogate"},
		{"orphan-high-surrogate", `"` + u("d83d") + `X"`, "", "low surrogate must follow a high surrogate"},
		{"orphan-low-surrogate", `"` + u("de04") + `X"`, "", "low surrogate must follow a high surrogate"},
		{"simple-escape-copyright", `"the Copyright ` + u("00a9") + ` sign"`, "\"the Copyright © sign\"", ""},
		{"dollar-escape", `"dollar ` + u("0024") + ` character"`, `"dollar $ character"`, ""},
		{"double-backslash-not-an-escape", `"dollar ` + bs + bs + `u0024 character"`, `"dollar ` + bs + bs + `u0024 character"`, ""},
		{"null-in-text", `"null ` + u("0000") + ` escape"`, "", "unsupported Unicode escape sequence"},
		{"quoted-key-mixed-case", `$."` + u("aBcD") + `"`, "", ""}, // want overwritten below
	}
	// U+ABCD is the Meetei Mayek letter case-tested by jsonpath_encoding.sql
	// ("ꯍ" mixed-case hex digits) — PG's own expected file spells it
	// literally as the character, which this Go source can hold safely as a
	// rune literal since 0xABCD is a normal codepoint, not a surrogate.
	cases[4].want = `"` + string(rune(0xABCD)) + `"`
	cases[14].want = `$."` + string(rune(0xABCD)) + `"`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rewriteJSONPathText(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("rewriteJSONPathText(%q) = %q, want error containing %q", tc.in, got, tc.wantErr)
				}
				ee, ok := err.(*ExecError)
				if !ok {
					t.Fatalf("rewriteJSONPathText(%q) error type = %T, want *ExecError", tc.in, err)
				}
				if !strings.Contains(ee.Message, tc.wantErr) && !strings.Contains(ee.Detail, tc.wantErr) {
					t.Fatalf("rewriteJSONPathText(%q) error = %q / detail %q, want containing %q", tc.in, ee.Message, ee.Detail, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("rewriteJSONPathText(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("rewriteJSONPathText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
