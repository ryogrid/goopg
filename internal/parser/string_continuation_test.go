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

// TestLexStringContinuationWithComments confirms that a `--` line comment
// between fragments (with a newline present somewhere in the gap) allows
// concatenation, but a `/* */` block comment in the gap does NOT — PG's
// scan.l quotecontinue macro (lines ~215-225) only admits `--` comments;
// `/* */` is a separate <xc> start-condition entirely absent from the gap
// grammar, so any `/*` there must fail the continuation attempt (M0134-0070
// regression fix: goopg previously treated a block comment in the gap as
// skippable "gap" content and wrongly continued the literal).
func TestLexStringContinuationWithComments(t *testing.T) {
	// Line comment case: newline is inherent to terminating a -- comment.
	// This must still SUCCEED (regression guard — do not change this part).
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

	// Block comment case containing a newline: must NOT concatenate — the
	// `/*` immediately fails the quotecontinue lookahead, so 'a' ends as its
	// own token, the block comment is skipped as ordinary whitespace, and
	// 'b' starts a new, separate string literal token.
	toks, err = Lex("SELECT 'a' /* multi\nline */ 'b';")
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

	// Block comment on ONE line (no newline anywhere in the gap) must NOT
	// concatenate either (same failure mode, doubly so since there's no
	// newline at all).
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

// TestLexStringContinuationBlockCommentBetweenFragmentsIsSyntaxError pins
// the exact strings.sql regression case (M0134-0070): three fragments where
// a block comment sits between the second and third. The first two
// fragments (separated only by a newline) still concatenate; the block
// comment then breaks the chain, so the third fragment starts a brand-new,
// unrelated string literal token — which the SQL grammar rejects as a
// syntax error (two adjacent string literals with no operator between them
// in expression position).
func TestLexStringContinuationBlockCommentBetweenFragmentsIsSyntaxError(t *testing.T) {
	sql := "SELECT 'first line' ' - next line' /* comment */ ' - third line';"
	toks, err := Lex(sql)
	if err != nil {
		t.Fatal(err)
	}
	// select, 'first line', ' - next line', ' - third line', ;, EOF — no
	// continuation anywhere here since the ' - next line' fragment is on the
	// SAME line as 'first line' (no newline in that gap either), and the
	// block comment gap before ' - third line' fails outright.
	if len(toks) != 6 {
		t.Fatalf("got %d tokens, want 6: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "first line" {
		t.Fatalf("tok[1]=%+v want string 'first line'", toks[1])
	}
	if toks[2].Kind != TokenStringLit || toks[2].Value != " - next line" {
		t.Fatalf("tok[2]=%+v want string ' - next line'", toks[2])
	}
	if toks[3].Kind != TokenStringLit || toks[3].Value != " - third line" {
		t.Fatalf("tok[3]=%+v want string ' - third line'", toks[3])
	}

	if _, err := Parse(sql); err == nil {
		t.Fatalf("expected a syntax error parsing two adjacent bare string literals, got none")
	}
}

// TestBlockCommentAsOrdinaryWhitespaceUnaffected confirms a block comment
// used as plain whitespace OUTSIDE a quote-continuation gap (i.e. before a
// string literal token, not between two closing/opening quotes) still
// parses fine — this path goes through skipWhitespaceAndComments, not
// tryQuoteContinuation, and must be untouched by this fix.
func TestBlockCommentAsOrdinaryWhitespaceUnaffected(t *testing.T) {
	toks, err := Lex("SELECT /* c */ 'a';")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 4 {
		t.Fatalf("got %d tokens, want 4: %+v", len(toks), toks)
	}
	if toks[1].Kind != TokenStringLit || toks[1].Value != "a" {
		t.Fatalf("tok[1]=%+v want string 'a'", toks[1])
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
