package parser

import "testing"

// TestLexUnicodeEscapeQuote pins goopg's U&'...'/U&"..." Unicode-escape
// string/identifier literal lexing (lexUnicodeEscapeQuote,
// decodeUnicodeEscapes, internal/parser/lexer.go) to PostgreSQL 18.3's
// exact decode/error behavior, including the UESCAPE clause. See
// postgres/src/backend/parser/scan.l:301-304,574,636-639,794-798;
// postgres/src/backend/parser/parser.c:253-320 (base_yylex UIDENT/USCONST),
// :352-362 (check_uescapechar), :371-527 (str_udeescape);
// postgres/src/include/mb/pg_wchar.h:535-556. M0134-0070 Round B.
func TestLexUnicodeEscapeQuote(t *testing.T) {
	t.Run("default-escape-mixed-4-and-6-hex", func(t *testing.T) {
		toks, err := Lex(`SELECT U&'d\0061t\+000061';`)
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireStringLit(t, toks, "data")
	})

	t.Run("identifier-form-default-escape", func(t *testing.T) {
		toks, err := Lex(`SELECT 1 AS U&"d\0061t\+000061";`)
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireQuotedIdent(t, toks, "data")
	})

	t.Run("custom-escape-char", func(t *testing.T) {
		toks, err := Lex(`SELECT U&'d!0061t!+000061' UESCAPE '!';`)
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireStringLit(t, toks, "data")
	})

	t.Run("identifier-form-uescape-clause", func(t *testing.T) {
		toks, err := Lex(`SELECT 1 AS U&"d!0061t!+000061" UESCAPE '!';`)
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireQuotedIdent(t, toks, "data")
	})

	t.Run("plain-no-uescape-clause-still-dispatches", func(t *testing.T) {
		// No escapes present at all: sanity-check the U& dispatch fires
		// (not misparsed as ident `u` + operator `&` + plain string).
		toks, err := Lex(`SELECT u&'abc';`)
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireStringLit(t, toks, "abc")
	})

	t.Run("continuation-concatenates", func(t *testing.T) {
		toks, err := Lex("SELECT U&'a'\n'b';")
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireStringLit(t, toks, "ab")
	})

	t.Run("valid-surrogate-pair", func(t *testing.T) {
		toks, err := Lex(`SELECT U&'\D800\DC00';`)
		if err != nil {
			t.Fatalf("Lex: unexpected error: %v", err)
		}
		requireStringLit(t, toks, "\U00010000")
	})

	badEscapeChars := []string{"+", "a", " ", "'"}
	for _, ec := range badEscapeChars {
		ec := ec
		t.Run("bad-uescape-char/"+ec, func(t *testing.T) {
			sql := `SELECT U&'\0061' UESCAPE '` + ec + `';`
			if ec == "'" {
				sql = `SELECT U&'\0061' UESCAPE '''';`
			}
			_, err := Lex(sql)
			requireSyntaxError(t, sql, err, "42601", "invalid Unicode escape character", "")
		})
	}

	t.Run("out-of-range-codepoint", func(t *testing.T) {
		sql := `SELECT U&'\+11FFFF';`
		_, err := Lex(sql)
		requireSyntaxError(t, sql, err, "42601", `invalid Unicode escape value at or near "\+11FFFF"`, "")
	})

	t.Run("unpaired-surrogate-first-at-eof", func(t *testing.T) {
		sql := `SELECT U&'\D800';`
		_, err := Lex(sql)
		requireSyntaxError(t, sql, err, "42601", `invalid Unicode surrogate pair at or near ""`, "")
	})

	t.Run("malformed-escape", func(t *testing.T) {
		sql := `SELECT U&'\xyz1';`
		_, err := Lex(sql)
		requireSyntaxError(t, sql, err, "22025", "invalid Unicode escape",
			"Unicode escapes must be \\XXXX or \\+XXXXXX.")
		// Distinct from Round A's E-string hint text.
		se := err.(*SyntaxError)
		if se.Hint == "Unicode escapes must be \\uXXXX or \\UXXXXXXXX." {
			t.Fatalf("Hint matches Round A's E-string hint verbatim, want distinct U& wording")
		}
	})
}

func requireStringLit(t *testing.T, toks []Token, want string) {
	t.Helper()
	for _, tk := range toks {
		if tk.Kind == TokenStringLit {
			if tk.Value != want {
				t.Fatalf("string literal = %q, want %q", tk.Value, want)
			}
			return
		}
	}
	t.Fatalf("no TokenStringLit found in %+v", toks)
}

func requireQuotedIdent(t *testing.T, toks []Token, want string) {
	t.Helper()
	for _, tk := range toks {
		if tk.Kind == TokenQuotedIdent {
			if tk.Value != want {
				t.Fatalf("quoted identifier = %q, want %q", tk.Value, want)
			}
			return
		}
	}
	t.Fatalf("no TokenQuotedIdent found in %+v", toks)
}

func requireSyntaxError(t *testing.T, sql string, err error, wantCode, wantMessage, wantHint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Lex(%q): want error, got none", sql)
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("Lex(%q): want *SyntaxError, got %T: %v", sql, err, err)
	}
	if !se.Raw {
		t.Errorf("Raw = false, want true")
	}
	if se.Code != wantCode {
		t.Errorf("Code = %q, want %q", se.Code, wantCode)
	}
	if se.Message != wantMessage {
		t.Errorf("Message = %q, want %q", se.Message, wantMessage)
	}
	if se.Hint != wantHint {
		t.Errorf("Hint = %q, want %q", se.Hint, wantHint)
	}
}
