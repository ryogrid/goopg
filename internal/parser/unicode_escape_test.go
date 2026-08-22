package parser

import "testing"

// TestLexEscapeStringUnicodeErrors pins goopg's \u/\U Unicode-escape
// decoder inside E'...' literals (scanEscapeQuoteInto,
// internal/parser/lexer.go) to PostgreSQL 18.3's exact SQLSTATE,
// message, Hint-or-absence, and byte position for malformed escapes:
// short digit counts, mismatched/lone UTF-16 surrogates, and
// out-of-range codepoints. Case wording/positions were confirmed live
// against a throwaway PG 18.3 instance (initdb + pg_ctl, scratch port)
// per M0134-0070's brief — this is not derived solely from reading
// scan.l. See postgres/src/backend/parser/scan.l:266-267,642-705,
// 1221-1242,1378-1395 and postgres/src/include/mb/pg_wchar.h:534-556.
//
// Scope: E'...' escape-string literals only. U&'...'/U&"..."
// Unicode-escape STRING LITERAL syntax (the U& prefix + UESCAPE
// clause) is out of scope for this brief.
func TestLexEscapeStringUnicodeErrors(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		needle  string // substring whose start byte offset is the expected Pos
		code    string
		message string
		hint    string
	}{
		{
			name:    "u-short-digit-count",
			sql:     `SELECT E'wrong: \u061';`,
			needle:  `\u061`,
			code:    "22025",
			message: "invalid Unicode escape",
			hint:    "Unicode escapes must be \\uXXXX or \\UXXXXXXXX.",
		},
		{
			name:    "U-short-digit-count",
			sql:     `SELECT E'wrong: \U0061';`,
			needle:  `\U0061`,
			code:    "22025",
			message: "invalid Unicode escape",
			hint:    "Unicode escapes must be \\uXXXX or \\UXXXXXXXX.",
		},
		{
			name:    "surrogate-first-then-close-quote",
			sql:     `SELECT E'wrong: \udb99';`,
			needle:  `';`,
			code:    "42601",
			message: `invalid Unicode surrogate pair at or near "'"`,
		},
		{
			name:    "surrogate-first-then-ordinary-char",
			sql:     `SELECT E'wrong: \udb99xy';`,
			needle:  `xy';`,
			code:    "42601",
			message: `invalid Unicode surrogate pair at or near "x"`,
		},
		{
			name:    "surrogate-first-then-escaped-backslash",
			sql:     `SELECT E'wrong: \udb99\\';`,
			needle:  `\\';`,
			code:    "42601",
			message: `invalid Unicode surrogate pair at or near "\"`,
		},
		{
			name:    "surrogate-first-then-plain-a",
			sql:     `SELECT E'wrong: \udb99a';`,
			needle:  `a';`,
			code:    "42601",
			message: `invalid Unicode surrogate pair at or near "a"`,
		},
		{
			name:    "surrogate-first-then-well-formed-non-surrogate-U-escape",
			sql:     `SELECT E'wrong: \U0000db99\U00000061';`,
			needle:  `\U00000061`,
			code:    "42601",
			message: `invalid Unicode surrogate pair at or near "\U00000061"`,
		},
		{
			name:    "out-of-range-codepoint",
			sql:     `SELECT E'wrong: \U002FFFFF';`,
			needle:  `\U002FFFFF`,
			code:    "42601",
			message: `invalid Unicode escape value at or near "\U002FFFFF"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Lex(tc.sql)
			if err == nil {
				t.Fatalf("Lex(%q): want error, got none", tc.sql)
			}
			se, ok := err.(*SyntaxError)
			if !ok {
				t.Fatalf("Lex(%q): want *SyntaxError, got %T: %v", tc.sql, err, err)
			}
			if !se.Raw {
				t.Errorf("Raw = false, want true")
			}
			if se.Code != tc.code {
				t.Errorf("Code = %q, want %q", se.Code, tc.code)
			}
			if se.Message != tc.message {
				t.Errorf("Message = %q, want %q", se.Message, tc.message)
			}
			if se.Hint != tc.hint {
				t.Errorf("Hint = %q, want %q", se.Hint, tc.hint)
			}
			wantPos := indexOrFatal(t, tc.sql, tc.needle)
			if se.Pos != wantPos {
				t.Errorf("Pos = %d, want %d (needle %q)", se.Pos, wantPos, tc.needle)
			}
		})
	}
}

// TestLexEscapeStringUnicodeValidCase pins the still-valid \U escape
// case from the strings.sql diff (line 92-98 of the ported fixture) —
// no regression from the malformed-input fixes above.
func TestLexEscapeStringUnicodeValidCase(t *testing.T) {
	toks, err := Lex(`SELECT E'dat\U00000061' AS "data";`)
	if err != nil {
		t.Fatalf("Lex: unexpected error: %v", err)
	}
	var lit string
	for _, tk := range toks {
		if tk.Kind == TokenStringLit {
			lit = tk.Value
			break
		}
	}
	if lit != "data" {
		t.Fatalf("string literal = %q, want %q", lit, "data")
	}
}

func indexOrFatal(t *testing.T, s, needle string) int {
	t.Helper()
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return i
		}
	}
	t.Fatalf("needle %q not found in %q", needle, s)
	return -1
}
