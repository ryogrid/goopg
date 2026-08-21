package parser

import "testing"

// TestLexStringContinuation covers PostgreSQL's string-literal continuation
// lexing (scan.l lines 574-631, quotecontinue/quotecontinuefail macros at
// lines 224-239, M0134-0070): two or more single-quoted literals separated
// ONLY by whitespace/comments that include at least one newline concatenate
// into a single TokenStringLit. The example is the one used by
// postgres/src/test/regress/sql/strings.sql.
func TestLexStringContinuation(t *testing.T) {
	toks, err := Lex("SELECT 'first line'\n' - next line'\n\t' - third line'\n\tAS \"Three lines to one\";")
	if err != nil {
		t.Fatal(err)
	}
	// select, string, as, "Three lines to one", ;, EOF
	if len(toks) != 6 {
		t.Fatalf("got %d tokens, want 6: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit {
		t.Fatalf("tok[1] kind=%v want TokenStringLit: %+v", toks[1].Kind, toks[1])
	}
	want := "first line - next line - third line"
	if toks[1].Value != want {
		t.Errorf("value=%q want %q", toks[1].Value, want)
	}
}

// TestLexStringContinuationSameLineDoesNotMerge pins the negative case: two
// single-quoted literals on the SAME line (no newline in the gap) must NOT
// concatenate — matches PG's quotecontinuefail branch (yyless(0), token
// emitted as-is). `'a' 'b'` therefore stays two separate string tokens.
func TestLexStringContinuationSameLineDoesNotMerge(t *testing.T) {
	toks, err := Lex(`SELECT 'a' 'b';`)
	if err != nil {
		t.Fatal(err)
	}
	// select, 'a', 'b', ;, EOF
	if len(toks) != 5 {
		t.Fatalf("got %d tokens, want 5: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "a" {
		t.Fatalf("tok[1]=%+v want string 'a'", toks[1])
	}
	if toks[2].Kind != TokenStringLit || toks[2].Value != "b" {
		t.Fatalf("tok[2]=%+v want string 'b'", toks[2])
	}
}

// TestLexStringContinuationEscapeString verifies that a continuation
// fragment following an E'...' opener is STILL backslash-decoded per
// escape-string rules (PG resumes scanning in state_before_str_stop, scan.l
// lines 574-631) — not the plain `''`-doubling rule. The continuation
// fragment is a bare `'...'` (never re-prefixed with E).
func TestLexStringContinuationEscapeString(t *testing.T) {
	// NB: PG's whitespace_with_newline rule (scan.l) requires an actual
	// newline character in the SOURCE gap between fragments — not merely a
	// decoded \n inside the E'...' literal's own body. So the gap here uses
	// a real newline, per postgres/src/backend/parser/scan.l lines 224-228.
	toks, err := Lex("SELECT E'a\\n'\n'b\\tc';")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 4 {
		t.Fatalf("got %d tokens, want 4: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit {
		t.Fatalf("tok[1] kind=%v want TokenStringLit: %+v", toks[1].Kind, toks[1])
	}
	want := "a\nb\tc"
	if toks[1].Value != want {
		t.Errorf("value=%q want %q", toks[1].Value, want)
	}
}

// TestLexStringContinuationWithComments confirms line (--) and block (/* */)
// comments between fragments, with a newline present somewhere in the gap,
// still allow concatenation.
func TestLexStringContinuationWithComments(t *testing.T) {
	// Line comment case: newline is inherent to terminating a -- comment.
	toks, err := Lex("SELECT 'a' -- comment\n'b';")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 4 {
		t.Fatalf("got %d tokens, want 4: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "ab" {
		t.Fatalf("tok[1]=%+v want string 'ab'", toks[1])
	}

	// Block comment case containing a newline.
	toks, err = Lex("SELECT 'a' /* multi\nline */ 'b';")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 4 {
		t.Fatalf("got %d tokens, want 4: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "ab" {
		t.Fatalf("tok[1]=%+v want string 'ab'", toks[1])
	}

	// Block comment on ONE line (no newline anywhere in the gap) must NOT
	// concatenate.
	toks, err = Lex("SELECT 'a' /* comment */ 'b';")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 5 {
		t.Fatalf("got %d tokens, want 5: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "a" {
		t.Fatalf("tok[1]=%+v want string 'a'", toks[1])
	}
	if toks[2].Kind != TokenStringLit || toks[2].Value != "b" {
		t.Fatalf("tok[2]=%+v want string 'b'", toks[2])
	}
}

// TestLexStringContinuationThreeFragments pins a 3+ fragment chain
// (matches strings.sql's exact example).
func TestLexStringContinuationThreeFragments(t *testing.T) {
	toks, err := Lex("SELECT 'a'\n'b'\n'c';")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 4 {
		t.Fatalf("got %d tokens, want 4: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "abc" {
		t.Fatalf("tok[1]=%+v want string 'abc'", toks[1])
	}
}
