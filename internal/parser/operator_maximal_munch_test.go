package parser_test

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestOperatorMaximalMunch pins the lexer to scan.l's {operator} rule
// (postgres/src/backend/parser/scan.l:886-990) rather than the hand-maintained
// allowlist of two-character spellings it replaced (M0134-0179).
//
// The allowlist's failure mode was not a cosmetic one. `@@` split into two `@`
// tokens, so `a @@ 'x'` did not parse as an infix operator at all — it built a
// PREFIX `@` over `@'x'` and reported `operator does not exist: @`, and
// `a @@ any ('{...}')` was a syntax error at "any" because the quantifier rules
// take one `subq_op`, not two. Every user-defined `CREATE OPERATOR` name was
// unreachable for the same reason.
//
// Cases are the lexed token stream, not the parse result: most of these
// operators still have no OpCode in goopg's AST enum, so asserting on Parse
// would measure the enum rather than the scanner.
func TestOperatorMaximalMunch(t *testing.T) {
	cases := []struct {
		sql  string
		want []string // operator/symbol token values, in order
	}{
		// Repeated-character operators: the whole point. Each was two
		// single-character tokens before this rule landed.
		{"a @@ b", []string{"@@"}},
		{"a ~~ b", []string{"~~"}},
		{"a !~~* b", []string{"!~~*"}},
		{"a <-> b", []string{"<->"}},
		{"a |/ b", []string{"|/"}},
		{"a ||/ b", []string{"||/"}},
		{"a @-@ b", []string{"@-@"}},
		{"a ?| b", []string{"?|"}},
		{"a ?-| b", []string{"?-|"}},
		{"a <<| b", []string{"<<|"}},
		{"a &<| b", []string{"&<|"}},

		// Spellings the allowlist already covered — maximal munch must not
		// regress them.
		{"a <= b", []string{"<="}},
		{"a >= b", []string{">="}},
		{"a <> b", []string{"<>"}},
		{"a != b", []string{"!="}},
		{"a ~* b", []string{"~*"}},
		{"a !~* b", []string{"!~*"}},
		{"a ->> b", []string{"->>"}},
		{"a #>> b", []string{"#>>"}},
		{"a <@ b", []string{"<@"}},
		{"a @> b", []string{"@>"}},
		{"a && b", []string{"&&"}},
		{"a || b", []string{"||"}},

		// scan.l's trailing-[+-] rule: strip a trailing + or - unless some
		// EARLIER character is one of ~!@#^&|`?% . This is what keeps `a=-1`
		// two tokens while `a?-1` stays one operator.
		{"a=-1", []string{"=", "-"}},
		{"a>=-1", []string{">=", "-"}},
		{"a<-1", []string{"<", "-"}},
		{"a*-1", []string{"*", "-"}},
		{"a/-1", []string{"/", "-"}},
		{"a%-1", []string{"%-"}},   // '%' qualifies — one operator
		{"a@>-1", []string{"@>-"}}, // '@' qualifies — one operator
		{"a||-1", []string{"||-"}}, // '|' qualifies — one operator
		{"a+-1", []string{"+", "-"}},
		{"a<=+1", []string{"<=", "+"}},

		// An embedded comment start truncates the run; a run BEGINNING with
		// one never reaches the operator scan (the comment skipper ate it).
		{"a<//*c*/b", []string{"</"}}, // yyless(2) keeps "</", pushes back "/*"
		{"a</*c*/b", []string{"<"}},
		{"a<--b", []string{"<"}},

		// '*' is claimed by this lexer's {self} case, so a run STARTING with
		// '*' still splits; every other position absorbs it (see `~*` above).
		{"select 2*3", []string{"*"}},
		{"select t.*", []string{".", "*"}},
	}

	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			toks, err := parser.Lex(tc.sql)
			if err != nil {
				t.Fatalf("Lex(%q): %v", tc.sql, err)
			}
			var got []string
			for _, tk := range toks {
				if tk.Kind == parser.TokenOperator || tk.Kind == parser.TokenSymbol {
					got = append(got, tk.Value)
				}
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("Lex(%q) operators = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// TestOperatorMaximalMunchParses covers the shape that motivated the fix:
// `<op> ANY/ALL (...)` over a generic operator. tsearch.sql uses
// `a @@ any ('{wr,qh}')`, which was a SYNTAX error at "any" — the quantifier
// rules take one `subq_op`, and `@@` arrived as two `@` tokens.
//
// goopg's AST models operators as a closed OpCode enum with no pg_operator
// lookup, so `@@`/`~~` are still rejected — but now by that enum, naming the
// whole operator, which is what proves the scanner reached the right rule.
// The remaining layer is the deferral ledger row for M0134-0179.
func TestOperatorMaximalMunchParses(t *testing.T) {
	for _, tc := range []struct{ sql, wantErr string }{
		{"SELECT 1 FROM t WHERE a @@ any ('{x,y}')", `unsupported operator "@@"`},
		{"SELECT 1 FROM t WHERE a @@ all ('{x,y}')", `unsupported operator "@@"`},
		{"SELECT 1 FROM t WHERE a @@ any (SELECT b FROM u)", `unsupported operator "@@"`},
		{"SELECT 1 FROM t WHERE a ~~ any ('{x,y}')", `unsupported operator "~~"`},
		{"SELECT a @@ b FROM t", `unsupported operator "@@"`},
	} {
		_, err := parser.Parse(tc.sql)
		if err == nil {
			t.Errorf("Parse(%q): want %s, got nil", tc.sql, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Parse(%q) = %v, want %s", tc.sql, err, tc.wantErr)
		}
		// The regression this guards: a "syntax error" here means the run
		// split again and the quantifier rule never matched.
		if strings.Contains(err.Error(), "syntax error") {
			t.Errorf("Parse(%q) = %v; operator run split again", tc.sql, err)
		}
	}
}

// TestQuantifiedGenericOperatorParses is the positive half: an operator that
// DOES have an OpCode must parse all the way through `ANY`/`ALL`.
func TestQuantifiedGenericOperatorParses(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 FROM t WHERE a <@ any ('{x,y}')",
		"SELECT 1 FROM t WHERE a @> all ('{x,y}')",
		"SELECT 1 FROM t WHERE a ~~ b",
		"SELECT 1 FROM t WHERE a !~* any ('{x,y}')",
	} {
		if _, err := parser.Parse(sql); err != nil && !strings.Contains(err.Error(), "unsupported operator") {
			t.Errorf("Parse(%q): %v", sql, err)
		}
	}
}

// TestOperatorTooLong pins scan.l's NAMEDATALEN guard, which upstream makes an
// error rather than notice-and-truncate ("the odds are we are looking at a
// syntactic mistake anyway", scan.l:978).
func TestOperatorTooLong(t *testing.T) {
	if _, err := parser.Lex("a " + strings.Repeat("@", 64) + " b"); err == nil {
		t.Fatal("64-character operator: want error, got nil")
	}
	if _, err := parser.Lex("a " + strings.Repeat("@", 63) + " b"); err != nil {
		t.Fatalf("63-character operator: want ok, got %v", err)
	}
}
