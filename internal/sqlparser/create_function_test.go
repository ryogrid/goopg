package sqlparser

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateFunctionFamily pins CREATE FUNCTION / PROCEDURE, DROP FUNCTION /
// PROCEDURE / ROUTINE and CALL — 981 CREATE FUNCTION-or-PROCEDURE fragments in
// the regress corpus (plpgsql 497, sql 396, internal 34, c 29).
//
// The attribute list is a fold of closures over one *fnAttrs carrier rather
// than a mid-rule action per attribute, which would place an empty nonterminal
// right after each keyword and decide a token early. Three shapes needed the
// legacy AST checked rather than guessed:
//
//   - RETURNS TABLE (cols) is recorded as `SETOF record` plus the columns
//     appended as trailing OUT arguments, not as a distinct return type.
//   - CREATE PROCEDURE keeps only LANGUAGE / body / WINDOW / STRICT; every
//     other attribute is consumed and DISCARDED (consumeFunctionAttribute), so
//     SECURITY DEFINER and the volatility words leave no trace on a procedure.
//   - `SET x FROM CURRENT` and `SET x TO DEFAULT` record no config op at all.
func TestCreateFunctionFamily(t *testing.T) {
	for _, q := range []string{
		"CREATE FUNCTION f(int) RETURNS int LANGUAGE sql AS 'SELECT $1 + 1'",
		"CREATE OR REPLACE FUNCTION f(a int, b text DEFAULT 'x') RETURNS SETOF record LANGUAGE plpgsql AS $$ BEGIN RETURN; END $$",
		"CREATE FUNCTION f() RETURNS TABLE (a int, b text) LANGUAGE sql AS $$ SELECT 1, 'x' $$",
		"CREATE FUNCTION f(OUT a int, INOUT b text, VARIADIC c int[]) RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql",
		"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql IMMUTABLE STRICT LEAKPROOF SECURITY DEFINER PARALLEL SAFE COST 5 ROWS 10 WINDOW",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql STABLE CALLED ON NULL INPUT SECURITY INVOKER PARALLEL RESTRICTED AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql VOLATILE RETURNS NULL ON NULL INPUT AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql NOT LEAKPROOF EXTERNAL SECURITY DEFINER AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SET search_path TO public RESET timezone AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SET search_path = a, b AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SET search_path FROM CURRENT AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SET search_path TO DEFAULT AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql RESET ALL AS 'x'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE 'sql' AS 'x'",
		"CREATE FUNCTION f(int) RETURNS int LANGUAGE c AS 'obj', 'link'",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql RETURN 1 + 1",
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql SUPPORT g AS 'x'",
		"CREATE FUNCTION f() RETURNS SETOF int LANGUAGE sql COST 0.5 ROWS 200 AS 'x'",
		"CREATE FUNCTION f(x IN int, y OUT text) RETURNS int LANGUAGE sql AS 'x'",
		"CREATE FUNCTION f(a interval year to month, b \"char\") RETURNS void LANGUAGE sql AS 'x'",
		"CREATE FUNCTION s.f() RETURNS numeric(5,2) LANGUAGE sql AS 'x'",
		"CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$",
		"CREATE PROCEDURE p(a int) LANGUAGE plpgsql AS $$ BEGIN END $$",
		"CREATE OR REPLACE PROCEDURE p() LANGUAGE sql SECURITY DEFINER AS 'x'",
		"CREATE PROCEDURE p(INOUT a int) LANGUAGE sql AS 'x'",
		"DROP FUNCTION f(int)",
		"DROP FUNCTION IF EXISTS f(int, text) CASCADE",
		"DROP FUNCTION f",
		"DROP FUNCTION f()",
		"DROP FUNCTION f(int), g(text)",
		"DROP FUNCTION f(a int, OUT b text) RESTRICT",
		"DROP PROCEDURE p(int)",
		"DROP PROCEDURE IF EXISTS p, q CASCADE",
		"DROP ROUTINE r(int)",
		"CALL p()",
		"CALL p(1, 2)",
		"CALL p(a => 1)",
		"CALL p(1, b => 2)",
		"CALL s.p()",
		"CALL p",
	} {
		assertParity(t, q)
	}
	// gram.y allows `= expr` as an argument default and makes RETURNS optional
	// when every argument is OUT; legacy accepts neither, so this grammar must
	// reject them too rather than silently widening the accepted language.
	assertBothReject(t, "CREATE FUNCTION f(a int = 3) RETURNS int LANGUAGE sql AS 'x'")
	assertBothReject(t, "CREATE FUNCTION f(OUT a int, OUT b int) LANGUAGE sql AS 'x'")
	// Four errors legacy raises AFTER a successful parse, all of which the
	// create_function_sql regress case checks byte-for-byte. The grammar has no
	// way to fail mid-attribute-list (the reduce has already happened), so the
	// attribute carrier records the first one and the statement rule raises it.
	// A body is MANDATORY, and `AS 'a','b'` is LANGUAGE C only.
	assertBothReject(t, "CREATE FUNCTION f(x int) RETURNS int LANGUAGE SQL AS $$ SELECT x * 2 $$ RETURN x * 3")
	assertBothReject(t, "CREATE FUNCTION test1 (int) RETURNS int LANGUAGE SQL AS 'a', 'b'")
	assertBothReject(t, "CREATE FUNCTION f() RETURNS int LANGUAGE sql")
	assertBothReject(t, "CREATE PROCEDURE p() LANGUAGE sql")
	assertBothReject(t, "CREATE FUNCTION f() RETURNS int LANGUAGE sql AS 'x' AS 'y'")
	assertBothReject(t, "CREATE FUNCTION f() RETURNS int LANGUAGE sql LANGUAGE c AS 'x'")
	// ... but the C two-item form itself stays legal.
	assertParity(t, "CREATE FUNCTION test1 (int) RETURNS int LANGUAGE C AS 'a', 'b'")
}

// TestCreateRoutineRoutingVetoes — the two sub-forms the grammar deliberately
// does not cover must stay on the legacy path. routeBatch never falls back
// once a fragment is routed, so an un-vetoed one would be a hard 42601.
//
// BEGIN ATOMIC has no grammar to mirror at all: legacy scans raw tokens to the
// matching END. TRANSFORM FOR TYPE is legacy-REJECTED, and staying unrouted
// keeps legacy's own error message.
func TestCreateRoutineRoutingVetoes(t *testing.T) {
	routed := func(q string) bool {
		toks, err := parser.Lex(q)
		if err != nil {
			t.Fatalf("lex %q: %v", q, err)
		}
		frags := SplitStatements(toks)
		return len(frags) == 1 && fragmentRouted(frags[0])
	}
	for _, q := range []string{
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END",
		"CREATE PROCEDURE p() LANGUAGE sql BEGIN ATOMIC SELECT 1; END",
		"CREATE FUNCTION f(int) RETURNS int LANGUAGE sql TRANSFORM FOR TYPE hstore AS 'x'",
	} {
		if routed(q) {
			t.Errorf("%q routed, but the grammar does not cover it", q)
		}
	}
	// The veto must not be so broad that it swallows ordinary routines: both
	// words are matched at paren depth 0 only.
	for _, q := range []string{
		"CREATE FUNCTION f(transform int) RETURNS int LANGUAGE sql AS 'x'",
		"CREATE FUNCTION f(a int DEFAULT length('begin atomic')) RETURNS int LANGUAGE sql AS 'x'",
	} {
		if !routed(q) {
			t.Errorf("%q not routed, but it is fully covered", q)
		}
	}
}

// TestCreateFunctionRoutingCoversCorpus — every CREATE FUNCTION / PROCEDURE in
// the repository's own SQL literals either routes and matches legacy, or is
// vetoed. A silent third outcome (routed and diverging) is what this catches.
func TestCreateFunctionRoutingCoversCorpus(t *testing.T) {
	routedN := 0
	for _, q := range harvestSQLLiterals(t) {
		u := strings.ToUpper(strings.TrimSpace(q))
		if !strings.HasPrefix(u, "CREATE FUNCTION") && !strings.HasPrefix(u, "CREATE PROCEDURE") &&
			!strings.HasPrefix(u, "CREATE OR REPLACE FUNCTION") && !strings.HasPrefix(u, "CREATE OR REPLACE PROCEDURE") {
			continue
		}
		toks, err := parser.Lex(q)
		if err != nil {
			continue
		}
		frags := SplitStatements(toks)
		if len(frags) != 1 || !fragmentRouted(frags[0]) {
			continue
		}
		routedN++
		l, n, derr := diffParse(q)
		switch {
		case derr != nil && strings.HasPrefix(derr.Error(), "legacy:"):
			// Neither parser accepts it; not this test's concern.
		case derr != nil:
			t.Errorf("routed but yacc rejects: %q: %v", q, derr)
		case l != n:
			t.Errorf("routed but diverges: %q\n  legacy=%s\n  yacc  =%s", q, l, n)
		}
	}
	if routedN == 0 {
		t.Skip("no CREATE FUNCTION literals harvested")
	}
	t.Logf("checked %d routed CREATE FUNCTION/PROCEDURE literals", routedN)
}
