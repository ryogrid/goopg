package parser

import (
	"strings"
	"testing"

)

// TestTriggerCommentAlterFunction pins P5.6 — CREATE/DROP TRIGGER, COMMENT ON
// and ALTER FUNCTION/PROCEDURE/ROUTINE.
//
// Almost every word of a trigger definition is an ORDINARY IDENTIFIER in
// goopg's lexer (BEFORE, AFTER, INSTEAD, EACH, ROW, STATEMENT, REFERENCING are
// all acceptIdentKeyword calls), so the rules discriminate on their text. OLD
// and NEW are the exception and use their own terminals: written as ColIds the
// REFERENCING list became ambiguous with the EXECUTE that follows it — EXECUTE
// is unreserved too, so it could start another transition — and shift won,
// which would have eaten the EXECUTE.
//
// ALTER FUNCTION disagrees with CREATE FUNCTION in two places, and both are
// legacy behaviour rather than oversight: `EXTERNAL SECURITY ...` records
// NOTHING on an ALTER (the loop only knows the bare `security` word), and
// PARALLEL / COST / ROWS / SUPPORT / WINDOW have no AlterFunctionStmt field at
// all.
func TestTriggerCommentAlterFunction(t *testing.T) {
	for _, q := range []string{

		"CREATE TRIGGER tr BEFORE UPDATE ON t FOR EACH ROW EXECUTE PROCEDURE f()",
		"CREATE TRIGGER tr AFTER INSERT OR DELETE ON s.t FOR EACH STATEMENT EXECUTE FUNCTION f('a', 1)",
		"CREATE TRIGGER tr INSTEAD OF UPDATE ON v FOR EACH ROW EXECUTE FUNCTION f()",
		"CREATE TRIGGER tr BEFORE UPDATE OF a, b ON t EXECUTE FUNCTION f()",
		"CREATE TRIGGER tr AFTER TRUNCATE ON t FOR STATEMENT EXECUTE FUNCTION f()",
		"CREATE TRIGGER tr AFTER UPDATE ON t REFERENCING OLD TABLE AS o NEW TABLE n FOR EACH STATEMENT EXECUTE FUNCTION f()",
		"CREATE TRIGGER tr BEFORE UPDATE ON t FOR EACH ROW WHEN (OLD.a IS DISTINCT FROM NEW.a) EXECUTE FUNCTION f()",
		"CREATE CONSTRAINT TRIGGER tr AFTER INSERT ON t DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION f()",
		"CREATE CONSTRAINT TRIGGER tr AFTER INSERT ON t NOT DEFERRABLE FOR EACH ROW EXECUTE FUNCTION f()",
		"CREATE TRIGGER tr AFTER INSERT ON t EXECUTE FUNCTION f(007, 'x', ident)",
		"DROP TRIGGER tr ON t", "DROP TRIGGER IF EXISTS tr ON s.t CASCADE",
		"COMMENT ON TABLE t IS 'x'", "COMMENT ON TABLE t IS NULL",
		"COMMENT ON COLUMN t.c IS 'x'", "COMMENT ON COLUMN s.t.c IS 'x'",
		"COMMENT ON INDEX i IS 'x'", "COMMENT ON VIEW v IS 'x'",
		"COMMENT ON SEQUENCE s IS 'x'", "COMMENT ON TYPE ty IS 'x'",
		"COMMENT ON DOMAIN d IS 'x'", "COMMENT ON SCHEMA s IS 'x'",
		"COMMENT ON EXTENSION e IS 'x'",
		"COMMENT ON COLLATION c IS 'x'", "COMMENT ON STATISTICS st IS 'x'",
		"COMMENT ON MATERIALIZED VIEW mv IS 'x'", "COMMENT ON ACCESS METHOD am IS 'x'",
		"COMMENT ON FOREIGN TABLE ft IS 'x'", "COMMENT ON FOREIGN DATA WRAPPER fdw IS 'x'",
		"COMMENT ON SERVER sv IS 'x'",
		"COMMENT ON CONSTRAINT c ON t IS 'x'", "COMMENT ON CONSTRAINT c ON DOMAIN d IS 'x'",
		"COMMENT ON TRIGGER tr ON t IS 'x'", "COMMENT ON POLICY p ON t IS 'x'",
		"COMMENT ON RULE r ON t IS 'x'",
		"COMMENT ON FUNCTION f IS 'x'", "COMMENT ON FUNCTION f(int, text) IS 'x'",
		"COMMENT ON FUNCTION f() IS 'x'",
		"COMMENT ON CAST (int AS text) IS 'x'",
		"ALTER FUNCTION f(int) VOLATILE",
		"ALTER FUNCTION f IMMUTABLE STRICT LEAKPROOF SECURITY DEFINER",
		"ALTER FUNCTION f(int) RENAME TO g",
		"ALTER FUNCTION f(int) OWNER TO r", "ALTER FUNCTION f(int) OWNER TO CURRENT_USER",
		"ALTER FUNCTION f(int) SET SCHEMA s",
		"ALTER FUNCTION f(int) SET search_path TO a, b",
		"ALTER FUNCTION f(int) SET search_path FROM CURRENT",
		"ALTER FUNCTION f(int) RESET ALL", "ALTER FUNCTION f(int) RESET timezone",
		"ALTER PROCEDURE p(int) SECURITY INVOKER", "ALTER ROUTINE r(int) STABLE",
		"ALTER FUNCTION f(int) COST 5 ROWS 10 PARALLEL SAFE",
		"ALTER FUNCTION f(int) CALLED ON NULL INPUT",
		"ALTER FUNCTION f(int) RETURNS NULL ON NULL INPUT",
		"ALTER FUNCTION f(int) NOT LEAKPROOF",
		"ALTER FUNCTION f(int) EXTERNAL SECURITY DEFINER",
		} {
		assertParity(t, q)
	}
}

// TestCommentRoutingIsNarrow — COMMENT ON routes only for the object kinds
// parseCommentOnTail's switch covers. Every other kind falls through THERE to
// a bare CompatNoopStmt built by a skip-to-semicolon scan, which a grammar
// cannot reproduce without accepting arbitrary token soup, so those stay on
// legacy. DATABASE is the one that shows up in practice.
func TestCommentRoutingIsNarrow(t *testing.T) {
	routed := func(q string) bool {
		toks, err := Lex(q)
		if err != nil {
			t.Fatalf("lex %q: %v", q, err)
		}
		frags := SplitStatements(toks)
		return len(frags) == 1 && fragmentRouted(frags[0])
	}
	for _, q := range []string{
		"COMMENT ON DATABASE d IS 'x'",
		"COMMENT ON LARGE OBJECT 1 IS 'x'",
		"COMMENT ON ROLE r IS 'x'",
	} {
		if routed(q) {
			t.Errorf("%q routed, but the grammar does not cover its object kind", q)
		}
	}
	for _, q := range []string{
		"COMMENT ON TABLE t IS 'x'",
		"COMMENT ON MATERIALIZED VIEW mv IS 'x'",
	} {
		if !routed(q) {
			t.Errorf("%q not routed, but it is fully covered", q)
		}
	}
}

// TestDropFamily pins P5.7 — the rest of the DROP classes. Every one is a
// plain name-list form in legacy, not a skip-to-semicolon compat scan, so all
// of them get a real grammar.
//
// Two details are easy to get wrong and are pinned here: an operator NAME is
// not one token (the lexer splits `===` into three `=`, so the run is rejoined
// and `!====` must keep its original spelling rather than coming back as
// `<>===`), and DROP FOREIGN DATA WRAPPER is stored with a HYPHEN —
// "foreign-data wrapper" — unlike every other multi-word kind.
func TestDropFamily(t *testing.T) {
	for _, q := range []string{
		"DROP SEQUENCE s", "DROP SEQUENCE IF EXISTS a, b CASCADE",
		"DROP SCHEMA s", "DROP SCHEMA IF EXISTS a, b CASCADE", "DROP SCHEMA s RESTRICT",
		"DROP EXTENSION e", "DROP STATISTICS st", "DROP COLLATION c",
		"DROP SERVER sv", "DROP CONVERSION c", "DROP EVENT TRIGGER et",
		"DROP ACCESS METHOD am", "DROP FOREIGN TABLE ft",
		"DROP FOREIGN DATA WRAPPER fdw",
		"DROP TEXT SEARCH PARSER p", "DROP TEXT SEARCH DICTIONARY d",
		"DROP TEXT SEARCH TEMPLATE t", "DROP TEXT SEARCH CONFIGURATION c",
		"DROP AGGREGATE a(int)", "DROP AGGREGATE IF EXISTS a(int, text) CASCADE",
		"DROP AGGREGATE a(*)",
		"DROP OPERATOR === (bool, bool)", "DROP OPERATOR !==== (boolean, real)",
		"DROP OPERATOR #### (NONE, int4)", "DROP OPERATOR pg_catalog.+ (int, int)",
		"DROP OPERATOR CLASS oc USING btree", "DROP OPERATOR FAMILY opf USING hash",
		"DROP CAST (int AS text)", "DROP CAST IF EXISTS (text AS int) CASCADE",
		"DROP TRANSFORM FOR int LANGUAGE sql",
		"DROP RULE r ON t", "DROP RULE IF EXISTS r ON s.t CASCADE",
		"DROP POLICY p ON t", "DROP POLICY IF EXISTS p ON t CASCADE",
		"DROP PUBLICATION p", "DROP SUBSCRIPTION s", "DROP TABLESPACE ts",
	} {
		if !strings.HasPrefix(q, "DROP") {
			t.Fatalf("not a DROP case: %q", q)
		}
		assertParity(t, q)
	}
}
