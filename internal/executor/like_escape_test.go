package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestLikeEscapeRewrite pins likeEscapeRewrite's PG-faithful do_like_escape
// transform (PG oracle: postgres/src/backend/utils/adt/like_match.c:392-486).
// M0134-0070.
func TestLikeEscapeRewrite(t *testing.T) {
	cases := []struct {
		name     string
		pattern  string
		escape   string
		runeMode bool
		want     string
		wantErr  bool
	}{
		{"one-char-custom-escape", `h#%`, "#", true, `h\%`, false},
		{"escape-is-backslash-noop", `h\%`, `\`, true, `h\%`, false},
		{"empty-escape-doubles-backslash", `50\%`, "", true, `50\\%`, false},
		{"escape-equals-pattern-char", `m%a%%a`, "%", true, `m\a\%a`, false},
		{"multi-char-escape-errors", "h%", "##", true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := likeEscapeRewrite(c.pattern, c.escape, c.runeMode, 0)
			if c.wantErr {
				if err == nil {
					t.Fatalf("likeEscapeRewrite(%q, %q): expected error, got nil", c.pattern, c.escape)
				}
				ee, ok := err.(*ExecError)
				if !ok {
					t.Fatalf("err=%T, want *ExecError", err)
				}
				if ee.Code != "22025" {
					t.Errorf("Code=%q, want 22025", ee.Code)
				}
				if ee.Hint != "Escape string must be empty or one character." {
					t.Errorf("Hint=%q, want exact PG hint text", ee.Hint)
				}
				return
			}
			if err != nil {
				t.Fatalf("likeEscapeRewrite(%q, %q): unexpected error %v", c.pattern, c.escape, err)
			}
			if got != c.want {
				t.Errorf("likeEscapeRewrite(%q, %q) = %q, want %q", c.pattern, c.escape, got, c.want)
			}
		})
	}
}

// TestLikeEscapeRewriteBytea covers the byte-mode (bytea) path used by
// `'a_c'::bytea LIKE 'a$__'::bytea ESCAPE '$'::bytea` (strings.sql:475-476):
// bytea counts and iterates raw bytes, not runes.
func TestLikeEscapeRewriteBytea(t *testing.T) {
	got, err := likeEscapeRewrite("a$__", "$", false, 0)
	if err != nil {
		t.Fatalf("likeEscapeRewrite bytea: unexpected error %v", err)
	}
	want := `a\__`
	if got != want {
		t.Errorf("likeEscapeRewrite bytea = %q, want %q", got, want)
	}
	// Rewritten pattern matches 'a_c' as LIKE 'a\__' — '\_' is literal
	// underscore, second '_' is the one-char wildcard.
	if !matchSQLLike("a_c", got) {
		t.Errorf("matchSQLLike(a_c, %q) = false, want true", got)
	}
}

// TestEvalLikeEscapePatternEndToEnd exercises the optimizer.LikeEscapePattern
// node through evalExprSlot, mirroring how the planner wires a parsed
// `x LIKE pattern ESCAPE escape` into the BinaryOp's Right operand.
func TestEvalLikeEscapePatternEndToEnd(t *testing.T) {
	node := &optimizer.LikeEscapePattern{
		Pattern: &optimizer.StringConst{Value: "h#%"},
		Escape:  &optimizer.StringConst{Value: "#"},
	}
	got, err := evalExprSlot(node, nil, nil)
	if err != nil {
		t.Fatalf("evalExprSlot(LikeEscapePattern): unexpected error %v", err)
	}
	if got.Kind != KindString {
		t.Fatalf("Kind=%v, want KindString", got.Kind)
	}
	if got.StringValue() != `h\%` {
		t.Errorf("got=%q, want %q", got.StringValue(), `h\%`)
	}
	// Full LIKE evaluation: 'h%' LIKE 'h#%' ESCAPE '#' → true.
	left := NewStringDatum("h%")
	matched, err := evalBinary(parser.OpLike, left, got, 0, nil)
	if err != nil {
		t.Fatalf("evalBinary(OpLike): unexpected error %v", err)
	}
	if matched.Kind != KindBool || !matched.BoolValue() {
		t.Errorf("'h%%' LIKE 'h#%%' ESCAPE '#' = %#v, want true", matched)
	}
}

// TestEvalLikeEscapePatternMultiCharErrors pins the SQLSTATE 22025 error
// path end-to-end through evalExprSlot.
func TestEvalLikeEscapePatternMultiCharErrors(t *testing.T) {
	node := &optimizer.LikeEscapePattern{
		Pattern: &optimizer.StringConst{Value: "h%"},
		Escape:  &optimizer.StringConst{Value: "##"},
	}
	_, err := evalExprSlot(node, nil, nil)
	if err == nil {
		t.Fatal("expected 22025 error for multi-char escape, got nil")
	}
	if !strings.Contains(err.Error(), "22025") {
		t.Errorf("err=%v, want SQLSTATE 22025", err)
	}
}
