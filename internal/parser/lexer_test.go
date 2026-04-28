package parser

import "testing"

// TestLexBasicTokens pins the canonical token shapes: keyword
// upgrade, integer literal, single-quoted string with embedded
// doubled-quote, parameter placeholder, and operator/symbol families.
func TestLexBasicTokens(t *testing.T) {
	toks, err := Lex(`SELECT 1 + 2 FROM t WHERE a <> 'it''s' AND b = $1;`)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(toks))
	for _, tk := range toks {
		got = append(got, tk.Value)
	}
	want := []string{"select", "1", "+", "2", "from", "t", "where", "a", "<>", "it's", "and", "b", "=", "1", ";", ""}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("tok[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// TestLexComments verifies that line and (nested) block comments are
// stripped without affecting downstream tokens.
func TestLexComments(t *testing.T) {
	toks, err := Lex("/* outer /* inner */ outer-still */ -- trailing\nSHOW all")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 3 || toks[0].Keyword != KwShow || toks[1].Keyword != KwAll || toks[2].Kind != TokenEOF {
		t.Fatalf("unexpected: %+v", toks)
	}
}

// TestLexQuotedIdent makes sure quoted identifiers preserve case and
// support "" doubling, distinct from unquoted (lower-cased).
func TestLexQuotedIdent(t *testing.T) {
	toks, err := Lex(`SELECT "MixedCase""quoted" FROM "T"`)
	if err != nil {
		t.Fatal(err)
	}
	if toks[1].Kind != TokenQuotedIdent || toks[1].Value != `MixedCase"quoted` {
		t.Fatalf("unexpected token: %+v", toks[1])
	}
	if toks[3].Kind != TokenQuotedIdent || toks[3].Value != "T" {
		t.Fatalf("unexpected token: %+v", toks[3])
	}
}

// TestLexUnterminated confirms structured errors at the right offset
// for the canonical "no closing quote" case.
func TestLexUnterminated(t *testing.T) {
	_, err := Lex(`'not closed`)
	if err == nil {
		t.Fatal("expected error")
	}
	le, ok := err.(*LexError)
	if !ok {
		t.Fatalf("err type=%T", err)
	}
	if le.Pos != 0 {
		t.Errorf("pos=%d want=0", le.Pos)
	}
}
