package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestGeneratedTablesSane pins the init-time joins between keywords_gen.go,
// the generated grammar tables, and the adapter: every keyword terminal and
// every adapter-referenced base terminal must resolve, and the generated
// header layout must match goyacc's documented shape ($end/error/$unk).
func TestGeneratedTablesSane(t *testing.T) {
	if len(yyToknames) < 3 || yyToknames[0] != "$end" || yyToknames[2] != "$unk" {
		t.Fatalf("yyToknames head = %v", yyToknames[:min(3, len(yyToknames))])
	}
	if n := len(unresolved); n > 0 {
		t.Fatalf("%d unresolved terminals (keywords_gen/grammar skew): %v", n, unresolved)
	}
	if got := len(keywordTokenNum); got != 494 {
		t.Fatalf("keywordTokenNum = %d entries, want 494", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestParseOneEmptyInput accepts the skeleton's only production: nothing.
func TestParseOneEmptyInput(t *testing.T) {
	stmts, err := ParseOne(nil, 0)
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if stmts != nil {
		t.Fatalf("empty input produced %v", stmts)
	}
}

// TestParseOneGarbageErrors pins the error contract on a statement the
// skeleton does not support yet: *parser.SyntaxError, PG-style wording, and
// absolute byte position preserved.
//
// Known divergence (ledgered for the P2 lexer-conformance fixtures): the
// legacy lexer lowercases keyword text in Token.Value, so the echoed token
// reads "select" where scan.c would echo the raw source spelling "SELECT".
// The adapter will recover raw source slices when statements carry them.
func TestParseOneGarbageErrors(t *testing.T) {
	toks, err := parser.Lex("SELECT")
	if err != nil {
		t.Fatal(err)
	}
	_, perr := ParseOne(toks, 0)
	if perr == nil {
		t.Fatal("SELECT: want syntax error from skeleton grammar, got none")
	}
	se, ok := perr.(*parser.SyntaxError)
	if !ok {
		t.Fatalf("error type = %T, want *parser.SyntaxError", perr)
	}
	if se.Pos != 0 {
		t.Errorf("Pos = %d, want 0", se.Pos)
	}
	if want := `syntax error at or near "select"`; se.Error() != want {
		t.Errorf("message = %q, want %q", se.Error(), want)
	}
}

// lexAll drives lexerState.Lex over a token slice and returns the terminal
// sequence — the direct probe for the base_yylex substitution port.
//
// EOF contract: Lex returns <= 0 at end of input (yylex1 maps that to $end).
// Looping until == 0 vs == yyEofCode(1) has bitten once already: goyacc's
// EOF sentinel is 1 but yylex1 accepts anything <= 0; only <= 0 is safe for
// direct Lex callers like this helper.
func lexAll(t *testing.T, sql string) []int {
	t.Helper()
	toks, err := parser.Lex(sql)
	if err != nil {
		t.Fatal(err)
	}
	l := &lexerState{toks: toks}
	l.lastPos = eofPos(toks)
	out := make([]int, 0, len(toks)+1)
	for i := 0; i < len(toks)+8; i++ {
		var lv yySymType
		n := l.Lex(&lv)
		if n <= 0 {
			return out
		}
		out = append(out, n)
	}
	t.Fatalf("Lex never reached EOF for %q", sql)
	return nil
}

func seq(names ...string) []int {
	nums := make([]int, len(names))
	for i, n := range names {
		nums[i] = resolve(n)
	}
	return nums
}

// TestBaseYLexSubstitutions ports parser.c's substitution table tests: each
// cur/follower pair must surface as the _LA variant, and the same keyword
// WITHOUT its follower must pass through untouched.
func TestBaseYLexSubstitutions(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{"NOT BETWEEN", []string{"NOT_LA", "BETWEEN"}},
		{"NOT IN", []string{"NOT_LA", "IN_P"}},
		{"NOT LIKE", []string{"NOT_LA", "LIKE"}},
		{"NOT ILIKE", []string{"NOT_LA", "ILIKE"}},
		{"NOT SIMILAR", []string{"NOT_LA", "SIMILAR"}},
		{"NULLS FIRST", []string{"NULLS_LA", "FIRST_P"}},
		{"NULLS LAST", []string{"NULLS_LA", "LAST_P"}},
		{"WITH TIME", []string{"WITH_LA", "TIME"}},
		{"WITH ORDINALITY", []string{"WITH_LA", "ORDINALITY"}},
		{"WITHOUT TIME", []string{"WITHOUT_LA", "TIME"}},
		// Negative cases: same keywords without followers stay un-substituted.
		{"NOT A", []string{"NOT", "IDENT"}},
		{"WITH X", []string{"WITH", "IDENT"}},
		{"NULLS X", []string{"NULLS_P", "IDENT"}},
		{"WITHOUT X", []string{"WITHOUT", "IDENT"}},
	}
	for _, c := range cases {
		got := lexAll(t, c.sql)
		want := seq(c.want...)
		eq := len(got) == len(want)
		if eq {
			for i := range got {
				if got[i] != want[i] {
					eq = false
					break
				}
			}
		}
		if !eq {
			t.Errorf("%q: terminals = %v, want %v", c.sql, got, want)
		}
	}
}

// TestSymbolTerminalsAreAscii pins the goyacc char-literal contract: Lex
// returns the ASCII CODE for single-char symbols (yylex1 translates via
// yyTok1[ascii]) — NOT the sequential yyToknames number.
func TestSymbolTerminalsAreAscii(t *testing.T) {
	got := lexAll(t, ";")
	if len(got) != 1 || got[0] != ';' {
		t.Errorf("terminals for %q = %v, want [%d]", ";", got, ';')
	}
}
