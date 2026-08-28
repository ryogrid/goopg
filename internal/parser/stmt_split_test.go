package parser

import (
	"strings"
	"testing"
)

// TestStatementSplitRespectsNesting pins the three fragment-boundary rules that
// per-fragment routing made load-bearing. Before routing was per-fragment a
// batch containing ANY unrouted statement went to the legacy parser WHOLE, so a
// mis-placed boundary was invisible: legacy re-parsed the original source and
// got the right answer anyway. Now each fragment is parsed on its own, and a
// boundary in the wrong place is a syntax error the user sees.
//
// All three cases come from the regress corpus (copydml, create_function_sql).
func TestStatementSplitRespectsNesting(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		// gram.y RuleActionMulti: the ';' between the two DELETEs belongs to
		// the rule action, not to stmtmulti.
		{"rule action multi", "create rule r as on insert to t do instead (delete from t; delete from t);", 1},
		// gram.y routine_body_stmt_list: same for a SQL-standard function body.
		{"begin atomic", "CREATE FUNCTION f() RETURNS boolean BEGIN ATOMIC SELECT 1; SELECT false; END;", 1},
		// routine_body_stmt_or_empty allows empty statements inside the body.
		{"begin atomic empty stmts", "CREATE FUNCTION g() RETURNS boolean BEGIN ATOMIC ;;RETURN false;; END;", 1},
		// A CASE ... END inside the body must not be mistaken for the body's END.
		{"begin atomic case end", "CREATE FUNCTION h() RETURNS int BEGIN ATOMIC SELECT CASE WHEN true THEN 1 ELSE 2 END; END;", 1},
		// The ordinary case still splits.
		{"plain batch", "select 1; select 2;", 2},
		{"trailing semicolons", "select 1;;;", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.src)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tc.src, err)
			}
			if len(stmts) != tc.want {
				t.Errorf("Parse(%q) produced %d statements, want %d", tc.src, len(stmts), tc.want)
			}
		})
	}
}

// TestSyntaxErrorAnchorsAtSemicolon pins that a fragment KEEPS its terminating
// ';'. Dropping it made the yacc parser hit EOF on a truncated statement and
// report "syntax error at end of input" where PG's scanner hands ';' to the
// grammar and reports it by name — 12 such cases in the errors regress file.
func TestSyntaxErrorAnchorsAtSemicolon(t *testing.T) {
	for _, q := range []string{
		"select * from;",
		"drop operator;",
	} {
		_, err := Parse(q)
		if err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", q)
			continue
		}
		if got := err.Error(); !strings.Contains(got, `at or near ";"`) {
			t.Errorf("Parse(%q) = %q, want the error anchored at \";\"", q, got)
		}
	}
}

// TestSyntaxErrorEchoesSourceSpelling pins scanner_yyerror's contract: the "at
// or near" text is yytext — the SOURCE spelling — not the lexer's down-cased
// Value. Echoing Value reported `NOT NUL` as near "nul" where PG says "NUL",
// and needed a hand-maintained uppercase list to paper over the same bug for
// soft keywords such as OIDS.
func TestSyntaxErrorEchoesSourceSpelling(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"CREATE TABLE t (id INTEGER NOT NUL)", `"NUL"`},
		{"CREATE TABLE withoid() WITH OIDS", `"OIDS"`},
		{"create table t (id integer not nul)", `"nul"`},
	} {
		_, err := Parse(tc.src)
		if err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", tc.src)
			continue
		}
		if got := err.Error(); !strings.Contains(got, tc.want) {
			t.Errorf("Parse(%q) = %q, want it to name %s", tc.src, got, tc.want)
		}
	}
}

// TestDropOperatorMissingArgument pins gram.y's oper_argtypes (gram.y:9095):
// its ONE-argument alternative is an ereport, not a reduction. Sharing goopg's
// drop_arg_types with DROP AGGREGATE — for which a single type IS legal —
// swallowed the error and let the catalog report `operator "===" does not
// exist` instead.
func TestDropOperatorMissingArgument(t *testing.T) {
	for _, q := range []string{
		"drop operator === (int4)",
		"drop operator = (nonesuch)",
	} {
		_, err := Parse(q)
		if err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", q)
		}
		se, ok := err.(*SyntaxError)
		if !ok {
			t.Fatalf("Parse(%q) = %T, want *SyntaxError", q, err)
		}
		if se.Message != "missing argument" || !se.Raw {
			t.Errorf("Parse(%q) message = %q (raw=%v), want a raw \"missing argument\"", q, se.Message, se.Raw)
		}
		if !strings.HasPrefix(se.Hint, "Use NONE") {
			t.Errorf("Parse(%q) hint = %q, want PG's NONE hint", q, se.Hint)
		}
	}
	// Two argument types stay legal, and so does DROP AGGREGATE's single one.
	for _, q := range []string{
		"drop operator === (int4, int4)",
		"drop operator === (none, int4)",
		"drop aggregate a(int4)",
	} {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q) = %v, want success", q, err)
		}
	}
}
